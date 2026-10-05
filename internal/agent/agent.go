// Package agent runs on every node (DaemonSet). It owns the status of its node's BMC object
// and serves the full, live snapshot (all sensors, SEL events) to the server.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/collector"
)

type Options struct {
	NodeName string
	Listen   string
	// StatusInterval caps how often fast-moving readings (watts, temperature) are written to the API server.
	// Health, power state and inventory changes are written immediately.
	StatusInterval time.Duration
	Version        string
}

type Agent struct {
	k8s  client.Client
	col  *collector.Collector
	opts Options
	log  *slog.Logger

	lastWrite  time.Time
	lastStatus bmcv1.BMCStatus
}

func New(k8s client.Client, col *collector.Collector, opts Options, log *slog.Logger) *Agent {
	return &Agent{k8s: k8s, col: col, opts: opts, log: log}
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

	heartbeat := time.NewTicker(a.opts.StatusInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.col.Updates():
		case <-heartbeat.C:
		}
		if err := a.sync(ctx); err != nil {
			a.log.Error("status sync failed", "err", err)
		}
	}
}

func (a *Agent) handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(collector.NewRegistry(a.col), promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !a.col.Ready() {
			http.Error(w, "first collection round not finished", http.StatusServiceUnavailable)
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

// sync creates the BMC object if needed and writes the status when it changed meaningfully.
func (a *Agent) sync(ctx context.Context) error {
	if !a.col.Ready() {
		return nil
	}
	snap := a.col.Snapshot()
	status := collector.Status(&snap)

	significant := !equality.Semantic.DeepEqual(volatileFree(status), volatileFree(a.lastStatus))
	if !significant && time.Since(a.lastWrite) < a.opts.StatusInterval {
		return nil
	}

	bmc, err := a.ensure(ctx)
	if err != nil {
		return err
	}
	orig := bmc.DeepCopy()
	conditions := bmc.Status.Conditions
	bmc.Status = status
	bmc.Status.Conditions = conditions
	bmc.Status.AgentVersion = a.opts.Version
	bmc.Status.LastUpdated = ptr.To(metav1.Now())
	meta.SetStatusCondition(&bmc.Status.Conditions, readyCondition(snap.Errors, bmc.Generation))

	if err := a.k8s.Status().Patch(ctx, bmc, client.MergeFrom(orig)); err != nil {
		return fmt.Errorf("patch status: %w", err)
	}
	a.lastWrite, a.lastStatus = time.Now(), status
	return nil
}

// volatileFree drops readings that change every round so they don't force a write on their own.
func volatileFree(s bmcv1.BMCStatus) bmcv1.BMCStatus {
	s.PowerWatts, s.InletTemperature = nil, nil
	s.Problems = slices.Clone(s.Problems)
	for i := range s.Problems {
		s.Problems[i].Message = "" // messages embed live readings, e.g. "41 degrees C above …"
	}
	return s
}

func readyCondition(errs map[string]string, gen int64) metav1.Condition {
	c := metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Collecting",
		Message: "In-band IPMI collection is working", ObservedGeneration: gen}
	if len(errs) == 0 {
		return c
	}
	phases := make([]string, 0, len(errs))
	for p, e := range errs {
		phases = append(phases, p+": "+e)
	}
	slices.Sort(phases)
	c.Message = strings.Join(phases, "; ")
	c.Reason = "PartialCollection"
	if _, ok := errs["sensors"]; ok {
		c.Status, c.Reason = metav1.ConditionFalse, "CollectionFailed"
	}
	return c
}

func (a *Agent) ensure(ctx context.Context) (*bmcv1.BMC, error) {
	bmc := &bmcv1.BMC{}
	err := a.k8s.Get(ctx, client.ObjectKey{Name: a.opts.NodeName}, bmc)
	if err == nil || !apierrors.IsNotFound(err) {
		return bmc, err
	}

	node := &corev1.Node{}
	if err := a.k8s.Get(ctx, client.ObjectKey{Name: a.opts.NodeName}, node); err != nil {
		return nil, fmt.Errorf("get node: %w", err)
	}
	bmc = &bmcv1.BMC{
		ObjectMeta: metav1.ObjectMeta{
			Name:   a.opts.NodeName,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "kube-bmc"},
			// The BMC object is garbage-collected together with its Node.
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID,
			}},
		},
		Spec: bmcv1.BMCSpec{NodeName: a.opts.NodeName, Protocol: bmcv1.ProtocolRedfish, InsecureSkipVerify: true},
	}
	if err := a.k8s.Create(ctx, bmc); err != nil {
		return nil, fmt.Errorf("create bmc: %w", err)
	}
	a.log.Info("registered BMC")
	return bmc, nil
}
