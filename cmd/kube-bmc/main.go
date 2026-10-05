// Command kube-bmc is a single binary with two roles:
//
//	kube-bmc agent   – DaemonSet; reads the local BMC in-band and publishes it as a BMC object
//	kube-bmc server  – Deployment; serves the dashboard/API and performs out-of-band power actions
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/agent"
	"github.com/aireet/kube-bmc/internal/collector"
	"github.com/aireet/kube-bmc/internal/ipmi"
	"github.com/aireet/kube-bmc/internal/server"
	"github.com/aireet/kube-bmc/web"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "agent":
		return runAgent(ctx, os.Args[2:])
	case "server":
		return runServer(ctx, os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version)
	default:
		usage()
	}
	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: kube-bmc <agent|server|version> [flags]\n\nRun 'kube-bmc <command> -h' for flags.\n")
	os.Exit(2)
}

func logger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = bmcv1.AddToScheme(s)
	return s
}

func runAgent(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	node := fs.String("node-name", os.Getenv("NODE_NAME"), "Kubernetes node this agent runs on (defaults to $NODE_NAME)")
	listen := fs.String("listen", ":9580", "address for /metrics, /healthz and the snapshot API")
	ipmitool := fs.String("ipmitool", "ipmitool", "path to the ipmitool binary")
	cacheDir := fs.String("cache-dir", os.TempDir(), "directory for the SDR cache file")
	interval := fs.Duration("interval", 30*time.Second, "sensor/chassis polling interval")
	invInterval := fs.Duration("inventory-interval", 10*time.Minute, "FRU/LAN/firmware/threshold polling interval")
	selInterval := fs.Duration("sel-min-interval", 5*time.Minute, "minimum time between SEL reads (only read when the log changed)")
	selEntries := fs.Int("sel-entries", 100, "number of newest SEL entries to keep")
	statusInterval := fs.Duration("status-interval", 2*time.Minute, "max time between status writes when only readings changed")
	logLevel := fs.String("log-level", "info", "debug, info, warn or error")
	_ = fs.Parse(args)

	log := logger(*logLevel).With("node", *node)
	if *node == "" {
		return errors.New("--node-name or $NODE_NAME is required")
	}
	if _, err := os.Stat("/dev/ipmi0"); err != nil {
		log.Warn("/dev/ipmi0 not found; load the ipmi_si and ipmi_devintf kernel modules on the host")
	}

	cfg, err := ctrlconfig.GetConfig()
	if err != nil {
		return err
	}
	k8s, err := client.New(cfg, client.Options{Scheme: scheme()})
	if err != nil {
		return err
	}
	col := collector.New(*node, ipmi.NewClient(ipmi.Exec{Path: *ipmitool}, *cacheDir), collector.Options{
		Interval: *interval, InventoryInterval: *invInterval, SELMinInterval: *selInterval, SELEntries: *selEntries,
		CommandTimeout: 30 * time.Second, SELTimeout: 3 * time.Minute,
	}, log)

	log.Info("starting agent", "version", version)
	return agent.New(k8s, col, agent.Options{NodeName: *node, Listen: *listen, StatusInterval: *statusInterval, Version: version}, log).Run(ctx)
}

func runServer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	listen := fs.String("listen", ":8080", "dashboard and API address")
	namespace := fs.String("namespace", os.Getenv("POD_NAMESPACE"), "namespace kube-bmc runs in (defaults to $POD_NAMESPACE)")
	agentSelector := fs.String("agent-selector", "app.kubernetes.io/name=kube-bmc,app.kubernetes.io/component=agent", "label selector of agent pods")
	agentPort := fs.Int("agent-port", 9580, "agent HTTP port")
	creds := fs.String("default-credentials", "", "Secret (username/password keys) used for out-of-band access when a BMC has no credentialsRef")
	powerActions := fs.Bool("enable-power-actions", false, "allow power on/off/reset from the dashboard and API")
	staleAfter := fs.Duration("stale-after", 10*time.Minute, "mark a BMC stale when its agent has not reported for this long")
	clusterName := fs.String("cluster-name", "", "display name of this cluster in the dashboard")
	demo := fs.Bool("demo", false, "serve a synthetic fleet instead of connecting to Kubernetes")
	logLevel := fs.String("log-level", "info", "debug, info, warn or error")
	_ = fs.Parse(args)
	log := logger(*logLevel)

	var backend server.Backend
	if *demo {
		backend = server.NewDemo()
		*powerActions = true
	} else {
		if *namespace == "" {
			return errors.New("--namespace or $POD_NAMESPACE is required")
		}
		sel, err := labels.Parse(*agentSelector)
		if err != nil {
			return fmt.Errorf("--agent-selector: %w", err)
		}
		cfg, err := ctrlconfig.GetConfig()
		if err != nil {
			return err
		}
		cl, err := cluster.New(cfg, func(o *cluster.Options) {
			o.Scheme = scheme()
			o.Cache.ByObject = map[client.Object]cache.ByObject{
				&corev1.Pod{}:    {Namespaces: map[string]cache.Config{*namespace: {}}, Label: sel},
				&corev1.Secret{}: {Namespaces: map[string]cache.Config{*namespace: {}}},
			}
		})
		if err != nil {
			return err
		}
		go func() {
			if err := cl.Start(ctx); err != nil {
				log.Error("cache stopped", "err", err)
			}
		}()
		if !cl.GetCache().WaitForCacheSync(ctx) {
			return errors.New("cache did not sync")
		}
		backend = server.NewKube(cl.GetClient(), cl.GetClient(), server.KubeOptions{
			Namespace: *namespace, AgentSelector: sel, AgentPort: *agentPort,
			DefaultCredentials: *creds, StaleAfter: *staleAfter,
		})
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           server.New(backend, server.Config{Version: version, PowerActions: *powerActions, Demo: *demo, ClusterName: *clusterName}, web.FS(), log).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("serving dashboard", "addr", *listen, "version", version, "demo", *demo, "powerActions", *powerActions)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
