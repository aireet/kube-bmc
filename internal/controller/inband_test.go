package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
)

// ipmiRecorder records ipmitool calls and the action phase stored at the time of each call.
type ipmiRecorder struct {
	c      client.Client
	calls  []string
	phases []bmcv1.ActionPhase
	err    error
}

func (r *ipmiRecorder) Run(ctx context.Context, args ...string) ([]byte, error) {
	r.calls = append(r.calls, strings.Join(args, " "))
	a := &bmcv1.BMCAction{}
	_ = r.c.Get(ctx, client.ObjectKey{Name: "a1"}, a)
	r.phases = append(r.phases, a.Status.Phase)
	return nil, r.err
}

func inBand(t *testing.T, enabled bool, objs ...client.Object) (*InBandReconciler, *ipmiRecorder, *fixture) {
	t.Helper()
	f := newFixture(t, true, objs...)
	rec := &ipmiRecorder{c: f.c}
	return &InBandReconciler{Client: f.c, Node: "gpu-01", Runner: rec, Enabled: enabled, Timeout: time.Minute}, rec, f
}

func reconcileInBand(t *testing.T, r *InBandReconciler) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "a1"}}); err != nil {
		t.Fatal(err)
	}
}

func TestInBandExecutes(t *testing.T) {
	for act, verb := range map[bmcv1.PowerAction]string{
		bmcv1.ActionGracefulShutdown: "soft", bmcv1.ActionForceOff: "off",
		bmcv1.ActionForceRestart: "reset", bmcv1.ActionPowerCycle: "cycle",
	} {
		r, rec, f := inBand(t, true, bmc("gpu-01"), action("a1", "gpu-01", act))
		reconcileInBand(t, r)
		if len(rec.calls) != 1 || rec.calls[0] != "chassis power "+verb {
			t.Fatalf("%s: calls = %v", act, rec.calls)
		}
		// The outcome is stored before the host goes down.
		if rec.phases[0] != bmcv1.PhaseSucceeded {
			t.Fatalf("%s: phase at execution = %s", act, rec.phases[0])
		}
		a := f.action("a1")
		if a.Status.Phase != bmcv1.PhaseSucceeded || a.Status.PowerStateBefore != bmcv1.PowerOn || a.Status.CompletionTime == nil {
			t.Fatalf("%s: status = %+v", act, a.Status)
		}
		var events corev1.EventList
		_ = f.c.List(context.Background(), &events)
		if len(events.Items) != 1 || events.Items[0].Reason != "BMCPowerAction" {
			t.Fatalf("%s: events = %+v", act, events.Items)
		}
	}
}

func TestInBandReportsFailure(t *testing.T) {
	r, rec, f := inBand(t, true, bmc("gpu-01"), action("a1", "gpu-01", bmcv1.ActionForceRestart))
	rec.err = errors.New("ipmitool chassis power reset: Could not open device at /dev/ipmi0")
	reconcileInBand(t, r)
	if a := f.action("a1"); a.Status.Phase != bmcv1.PhaseFailed || !strings.Contains(a.Status.Message, "/dev/ipmi0") {
		t.Fatalf("status = %+v", a.Status)
	}
}

func TestInBandIgnores(t *testing.T) {
	claimed := action("a1", "gpu-01", bmcv1.ActionForceOff)
	claimed.Status.Phase = bmcv1.PhaseRunning
	cases := map[string]struct {
		enabled bool
		obj     *bmcv1.BMCAction
	}{
		"disabled":        {false, action("a1", "gpu-01", bmcv1.ActionForceOff)},
		"other node":      {true, action("a1", "gpu-02", bmcv1.ActionForceOff)},
		"power on":        {true, action("a1", "gpu-01", bmcv1.ActionOn)},
		"graceful reset":  {true, action("a1", "gpu-01", bmcv1.ActionGracefulRestart)},
		"already claimed": {true, claimed},
	}
	for name, tc := range cases {
		r, rec, f := inBand(t, tc.enabled, bmc("gpu-01"), tc.obj)
		reconcileInBand(t, r)
		if len(rec.calls) != 0 {
			t.Errorf("%s: executed %v", name, rec.calls)
		}
		if got := f.action("a1").Status.Phase; got != tc.obj.Status.Phase {
			t.Errorf("%s: phase changed to %s", name, got)
		}
	}
}
