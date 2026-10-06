// Command kube-bmc runs one of the two kube-bmc components:
//
//	kube-bmc agent   node agent (DaemonSet): reads the local BMC in-band and publishes a BMC object
//	kube-bmc server  dashboard, API, MCP endpoint and BMCAction controller (Deployment)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/agent"
	"github.com/aireet/kube-bmc/internal/auth"
	"github.com/aireet/kube-bmc/internal/collector"
	"github.com/aireet/kube-bmc/internal/controller"
	"github.com/aireet/kube-bmc/internal/ipmi"
	"github.com/aireet/kube-bmc/internal/mcpserver"
	"github.com/aireet/kube-bmc/internal/oob"
	"github.com/aireet/kube-bmc/internal/server"
	"github.com/aireet/kube-bmc/web"
)

// version is set at build time with -ldflags "-X main.version=...".
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
	case "hash-password":
		return hashPassword(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version)
	default:
		usage()
	}
	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: kube-bmc <agent|server|hash-password|version> [flags]\n\nRun 'kube-bmc <command> -h' for flags.\n")
	os.Exit(2)
}

// hashPassword prints an htpasswd line for --auth=password. The password is read from
// standard input so that it does not appear in the shell history or process list.
func hashPassword(args []string) error {
	fs := flag.NewFlagSet("hash-password", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: echo -n PASSWORD | kube-bmc hash-password USERNAME\n")
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	pw, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
	if err != nil {
		return err
	}
	line, err := auth.HashPassword(fs.Arg(0), strings.TrimRight(string(pw), "\r\n"))
	if err != nil {
		return err
	}
	fmt.Println(line)
	return nil
}

func logger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
	ctrl.SetLogger(logr.FromSlogHandler(log.Handler()))
	return log
}

func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = bmcv1.AddToScheme(s)
	return s
}

// discoverOIDC retries provider discovery with backoff for up to two minutes, so that a
// provider that starts at the same time does not put the server into CrashLoopBackOff.
func discoverOIDC(ctx context.Context, log *slog.Logger, cfg auth.OIDCConfig) (*auth.OIDC, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	delay := time.Second
	for {
		o, err := auth.NewOIDC(ctx, cfg)
		if err == nil {
			return o, nil
		}
		log.Warn("OIDC discovery failed; retrying", "issuer", cfg.IssuerURL, "in", delay, "err", err)
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(delay):
		}
		delay = min(2*delay, 15*time.Second)
	}
}

// envOr returns the environment variable key, or def when it is unset.
func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func runAgent(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	node := fs.String("node-name", os.Getenv("NODE_NAME"), "Kubernetes node this agent runs on (defaults to $NODE_NAME)")
	listen := fs.String("listen", ":9580", "address for /metrics, /healthz, /readyz and the snapshot API")
	ipmitool := fs.String("ipmitool", "ipmitool", "path to the ipmitool binary")
	cacheDir := fs.String("cache-dir", os.TempDir(), "directory for the SDR cache file")
	interval := fs.Duration("interval", 30*time.Second, "sensor and chassis polling interval")
	invInterval := fs.Duration("inventory-interval", 10*time.Minute, "FRU, LAN, firmware and threshold polling interval")
	selInterval := fs.Duration("sel-min-interval", 5*time.Minute, "minimum time between SEL reads; the SEL is only read after it changed")
	selEntries := fs.Int("sel-entries", 100, "number of most recent SEL entries to keep")
	statusRefresh := fs.Duration("status-refresh", 10*time.Minute, "maximum age of readings in the BMC status when they change only within their deadband")
	leaseDuration := fs.Duration("lease-duration", 3*time.Minute, "validity of the heartbeat Lease; renewed every third of it")
	namespace := fs.String("namespace", os.Getenv("POD_NAMESPACE"), "namespace of the heartbeat Lease (defaults to $POD_NAMESPACE)")
	actions := fs.Bool("enable-actions", false, "execute BMCActions for this node through the local BMC interface")
	selArchives := fs.Int("sel-archives", 3, "number of SEL archives kept for this node; older ones are deleted")
	fs.BoolVar(actions, "enable-power-actions", false, "deprecated: use --enable-actions")
	logLevel := fs.String("log-level", "info", "debug, info, warn or error")
	_ = fs.Parse(args)

	log := logger(*logLevel).With("node", *node)
	if *node == "" {
		return errors.New("--node-name or $NODE_NAME is required")
	}
	if *namespace == "" {
		return errors.New("--namespace or $POD_NAMESPACE is required")
	}
	if _, err := os.Stat("/dev/ipmi0"); err != nil {
		log.Warn("/dev/ipmi0 not found; load the ipmi_si and ipmi_devintf kernel modules on the host")
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	k8s, err := client.New(cfg, client.Options{Scheme: scheme()})
	if err != nil {
		return err
	}
	ipmiClient := ipmi.NewClient(ipmi.Exec{Path: *ipmitool}, *cacheDir)
	col := collector.New(*node, ipmiClient, collector.Options{
		Interval: *interval, InventoryInterval: *invInterval, SELMinInterval: *selInterval, SELEntries: *selEntries,
		CommandTimeout: 30 * time.Second, SELTimeout: 3 * time.Minute,
	}, log)

	log.Info("starting agent", "version", version, "actions", *actions)
	ag := agent.New(k8s, col, agent.Options{
		NodeName: *node, Namespace: *namespace, Listen: *listen, LeaseDuration: *leaseDuration,
		StatusRefresh: *statusRefresh, MaxCollectionAge: 3**interval + 30*time.Second, Version: version,
	}, log)
	if *actions {
		// The manager watches BMCActions only; everything else is read directly.
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 scheme(),
			Metrics:                metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress: "0",
			Client:                 client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&bmcv1.BMC{}, &corev1.Event{}}}},
		})
		if err != nil {
			return err
		}
		if err := (&controller.InBandReconciler{
			Client: mgr.GetClient(), Node: *node, IPMI: ipmiClient, Namespace: *namespace, SELArchives: *selArchives, OnSELCleared: ag.SELCleared,
			Enabled: true, Timeout: 5 * time.Minute,
		}).SetupWithManager(mgr); err != nil {
			return err
		}
		go func() {
			if err := mgr.Start(ctx); err != nil {
				log.Error("action controller stopped", "err", err)
			}
		}()
	}
	return ag.Run(ctx)
}

func runServer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	listen := fs.String("listen", ":8080", "address of the dashboard, API and MCP endpoint")
	namespace := fs.String("namespace", os.Getenv("POD_NAMESPACE"), "namespace kube-bmc runs in (defaults to $POD_NAMESPACE)")
	agentSelector := fs.String("agent-selector", "app.kubernetes.io/name=kube-bmc,app.kubernetes.io/component=agent", "label selector of agent pods")
	agentPort := fs.Int("agent-port", 9580, "agent HTTP port")
	creds := fs.String("default-credentials", "", "Secret with username and password keys used for BMCs without spec.credentialsRef")
	actions := fs.Bool("enable-actions", false, "accept BMCActions (power, identify light, clearing the SEL); when false they are rejected. Agents need the same flag")
	fs.BoolVar(actions, "enable-power-actions", false, "deprecated: use --enable-actions")
	actionTimeout := fs.Duration("action-timeout", 2*time.Minute, "timeout of a single power action")
	actionTTL := fs.Duration("action-ttl", 7*24*time.Hour, "how long finished BMCActions are kept; 0 keeps them forever")
	claimTimeout := fs.Duration("in-band-claim-timeout", 30*time.Second, "how long in-band actions are left to the node agent before out-of-band access is used")
	staleAfter := fs.Duration("stale-after", 15*time.Minute, "mark a BMC stale when its agent has neither a heartbeat Lease nor reported for this long")
	clusterName := fs.String("cluster-name", "", "cluster name shown in the dashboard")
	externalURL := fs.String("external-url", "", "public base URL of the dashboard, e.g. https://kube-bmc.example.com")
	authMode := fs.String("auth", "none", "authentication mode: none, password or oidc")
	kubeTokens := fs.Bool("kubernetes-tokens", true, "with --auth=password or oidc, also accept tokens of ServiceAccounts in --namespace")
	usersSecret := fs.String("users-secret", "kube-bmc-users", "with --auth=password, Secret in --namespace whose htpasswd key lists the users")
	issuer := fs.String("oidc-issuer-url", "", "OIDC issuer URL")
	clientID := fs.String("oidc-client-id", "", "OIDC client ID")
	clientSecret := fs.String("oidc-client-secret", "", "OIDC client secret (defaults to $OIDC_CLIENT_SECRET)")
	scopes := fs.String("oidc-scopes", "openid,profile,email,groups", "comma-separated scopes requested at sign-in")
	usernameClaim := fs.String("oidc-username-claim", "email", "ID token claim used as the username")
	groupsClaim := fs.String("oidc-groups-claim", "groups", "ID token claim used as the groups")
	usernamePrefix := fs.String("oidc-username-prefix", "oidc:", "prefix added to OIDC usernames for RBAC checks")
	groupsPrefix := fs.String("oidc-groups-prefix", "oidc:", "prefix added to OIDC groups for RBAC checks")
	audiences := fs.String("oidc-extra-audiences", "", "comma-separated token audiences accepted in addition to the client ID")
	sessionSecret := fs.String("session-secret", "", "secret of at least 32 bytes that encrypts session cookies (defaults to $SESSION_SECRET)")
	sessionTTL := fs.Duration("session-ttl", 12*time.Hour, "browser session lifetime")
	logLevel := fs.String("log-level", "info", "debug, info, warn or error")
	_ = fs.Parse(args)
	log := logger(*logLevel)

	if *namespace == "" {
		return errors.New("--namespace or $POD_NAMESPACE is required")
	}
	sel, err := labels.Parse(*agentSelector)
	if err != nil {
		return fmt.Errorf("--agent-selector: %w", err)
	}
	base := strings.TrimSuffix(*externalURL, "/")

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
			&corev1.Pod{}:           {Namespaces: map[string]cache.Config{*namespace: {}}, Label: sel},
			&corev1.Secret{}:        {Namespaces: map[string]cache.Config{*namespace: {}}},
			&coordinationv1.Lease{}: {Namespaces: map[string]cache.Config{*namespace: {}}},
		}},
	})
	if err != nil {
		return err
	}
	credentials := controller.Credentials{Reader: mgr.GetClient(), Namespace: *namespace, Default: *creds}
	if err := (&controller.ActionReconciler{
		Client: mgr.GetClient(), Credentials: credentials, Power: oob.Power,
		Enabled: *actions, ClaimTimeout: *claimTimeout, Timeout: *actionTimeout, TTL: *actionTTL,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	authOpts := auth.Options{SessionTTL: *sessionTTL, SecureCookies: strings.HasPrefix(base, "https://"), Log: log}
	var prm http.Handler
	switch *authMode {
	case "none":
		log.Warn("authentication is disabled; anyone who can reach the server has full access")
	case "password":
		secret := []byte(envOr("SESSION_SECRET", *sessionSecret))
		if len(secret) < 32 {
			return errors.New("--session-secret or $SESSION_SECRET of at least 32 bytes is required with --auth=password")
		}
		authOpts.SessionSecret = secret
		authOpts.Passwords = &auth.Passwords{Reader: mgr.GetClient(), Namespace: *namespace, Secret: *usersSecret}
		if *kubeTokens {
			authOpts.Tokens = &auth.TokenReviewer{Client: mgr.GetClient(), Namespace: *namespace, TTL: time.Minute}
		}
	case "oidc":
		if base == "" {
			return errors.New("--external-url is required with --auth=oidc")
		}
		secret := []byte(envOr("SESSION_SECRET", *sessionSecret))
		if len(secret) < 32 {
			return errors.New("--session-secret or $SESSION_SECRET of at least 32 bytes is required with --auth=oidc")
		}
		o, err := discoverOIDC(ctx, log, auth.OIDCConfig{
			IssuerURL: *issuer, ClientID: *clientID, ClientSecret: envOr("OIDC_CLIENT_SECRET", *clientSecret),
			RedirectURL: base + "/auth/callback", Scopes: splitList(*scopes),
			UsernameClaim: *usernameClaim, GroupsClaim: *groupsClaim,
			UsernamePrefix: *usernamePrefix, GroupsPrefix: *groupsPrefix, ExtraAudiences: splitList(*audiences),
		})
		if err != nil {
			return err
		}
		authOpts.OIDC, authOpts.SessionSecret = o, secret
		if *kubeTokens {
			authOpts.Tokens = &auth.TokenReviewer{Client: mgr.GetClient(), Namespace: *namespace, TTL: time.Minute}
		}
		prm = mcpauth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
			Resource:               base + "/mcp",
			AuthorizationServers:   []string{o.Issuer()},
			BearerMethodsSupported: []string{"header"},
			ScopesSupported:        splitList(*scopes),
		})
	default:
		return fmt.Errorf("--auth: unknown mode %q", *authMode)
	}
	authn, err := auth.New(authOpts)
	if err != nil {
		return err
	}

	backend := server.NewKube(mgr.GetClient(), server.KubeOptions{
		Namespace: *namespace, AgentSelector: sel, AgentPort: *agentPort, Credentials: credentials, StaleAfter: *staleAfter,
	})
	mcpSrv := mcpserver.New(mcpserver.Options{Backend: backend, Actions: *actions, Version: version})
	var mcpHandler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpSrv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	if authn.Enabled() {
		mcpHandler = mcpauth.RequireBearerToken(authn.MCPVerifier(), &mcpauth.RequireBearerTokenOptions{
			ResourceMetadataURL: base + "/.well-known/oauth-protected-resource",
		})(mcpHandler)
	}

	srv := &http.Server{
		Addr: *listen,
		Handler: server.New(server.Options{
			Backend: backend,
			Config:  server.Config{Version: version, Actions: *actions, ClusterName: *clusterName, Auth: authn.Mode()},
			UI:      web.FS(), Authn: authn, MCP: mcpHandler, ProtectedResourceMetadata: prm, Log: log,
		}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errc := make(chan error, 2)
	go func() { errc <- mgr.Start(ctx) }()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		return errors.New("informer cache did not sync")
	}
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	log.Info("serving", "addr", *listen, "version", version, "auth", *authMode, "powerActions", *actions)

	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
