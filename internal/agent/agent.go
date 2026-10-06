// Package agent implements the node agent. It maintains the BMC object of its node, renews
// a heartbeat Lease and serves the full snapshot, including all sensors and SEL entries,
// over HTTP.
//
// To keep the load on the API server and etcd low, liveness is reported through a small
// Lease, as kubelet does for nodes, and the BMC status is written only when it changes
// meaningfully (see writeReason).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aireet/kube-bmc/internal/collector"
)

type Options struct {
	NodeName string
	// Namespace holds the heartbeat Lease.
	Namespace string
	Listen    string
	// LeaseDuration is the validity of the heartbeat Lease; it is renewed every third of it.
	LeaseDuration time.Duration
	// StatusRefresh is the maximum age of the readings (power, inlet temperature, SEL
	// counters) in the BMC status when they change only within their deadband.
	StatusRefresh time.Duration
	// MaxCollectionAge is how old the last collection round may be before /readyz fails.
	MaxCollectionAge time.Duration
	Version          string
}

type Agent struct {
	k8s    client.Client
	col    *collector.Collector
	opts   Options
	log    *slog.Logger
	status *statusWriter
	lease  *heartbeat
	// selCleared is signalled after a ClearSEL action, see SELCleared.
	selCleared chan struct{}
}

var statusWrites = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "kube_bmc_status_writes_total",
	Help: "BMC status writes to the API server by reason: initial, change, readings or refresh.",
}, []string{"reason"})

func New(k8s client.Client, col *collector.Collector, opts Options, log *slog.Logger) *Agent {
	return &Agent{
		k8s: k8s, col: col, opts: opts, log: log,
		status:     &statusWriter{k8s: k8s, node: opts.NodeName, version: opts.Version, refresh: opts.StatusRefresh, log: log},
		lease:      &heartbeat{k8s: k8s, namespace: opts.Namespace, node: opts.NodeName, duration: opts.LeaseDuration},
		selCleared: make(chan struct{}, 1),
	}
}

// SELCleared tells the agent that the System Event Log was cleared on purpose. Problems
// derived from the log are dropped at once instead of after the usual clear delay, and
// the log is read again immediately. It is safe to call from any goroutine.
func (a *Agent) SELCleared() {
	select {
	case a.selCleared <- struct{}{}:
	default:
	}
}

func (a *Agent) Run(ctx context.Context) error {
	srv := &http.Server{Addr: a.opts.Listen, Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.log.Error("http server failed", "err", err)
		}
	}()
	go a.col.Run(ctx)

	renew := time.NewTicker(a.opts.LeaseDuration / 3)
	defer renew.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.col.Updates():
			if err := a.sync(ctx); err != nil {
				a.log.Error("status sync failed", "err", err)
			}
		case <-a.selCleared:
			a.status.forget(selProblemSources...)
			a.col.Refresh()
		case <-renew.C:
			if !a.col.Fresh(a.opts.MaxCollectionAge) {
				continue // a stalled collector must not look alive
			}
			if err := a.lease.renew(ctx, a.node); err != nil {
				a.log.Error("lease renewal failed", "err", err)
			}
		}
	}
}

// sync writes the status and, on the first round, the heartbeat.
func (a *Agent) sync(ctx context.Context) error {
	if !a.col.Ready() {
		return nil
	}
	snap := a.col.Snapshot()
	if err := a.status.write(ctx, &snap, a.node); err != nil {
		return err
	}
	if a.lease.lease == nil {
		return a.lease.renew(ctx, a.node)
	}
	return nil
}

// node returns the Node object, which owns the BMC object and the Lease.
func (a *Agent) node(ctx context.Context) (*corev1.Node, error) {
	n := &corev1.Node{}
	if err := a.k8s.Get(ctx, client.ObjectKey{Name: a.opts.NodeName}, n); err != nil {
		return nil, fmt.Errorf("get node: %w", err)
	}
	return n, nil
}

func (a *Agent) handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(collector.NewRegistry(a.col, statusWrites), promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !a.col.Fresh(a.opts.MaxCollectionAge) {
			http.Error(w, "no collection round completed recently", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /api/v1/snapshot", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a.col.Snapshot())
	})
	return mux
}
