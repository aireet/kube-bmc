package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/oob"
)

const ns = "kube-bmc-system"

type powerCall struct {
	target oob.Target
	action bmcv1.PowerAction
}

type fixture struct {
	t     *testing.T
	c     client.Client
	r     *ActionReconciler
	calls []powerCall
	err   error
	now   time.Time
}

func newFixture(t *testing.T, enabled bool, objs ...client.Object) *fixture {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = bmcv1.AddToScheme(scheme)
	f := &fixture{t: t, now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	f.c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&bmcv1.BMCAction{}, &bmcv1.BMC{}).Build()
	f.r = &ActionReconciler{
		Client:      f.c,
		Credentials: Credentials{Reader: f.c, Namespace: ns, Default: "bmc-credentials"},
		Power: func(_ context.Context, tg oob.Target, a bmcv1.PowerAction) error {
			f.calls = append(f.calls, powerCall{tg, a})
			return f.err
		},
		Enabled: enabled,
		Timeout: time.Minute,
		TTL:     24 * time.Hour,
		Now:     func() time.Time { return f.now },
	}
	return f
}

func (f *fixture) reconcile(name string) ctrl.Result {
	f.t.Helper()
	res, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func (f *fixture) action(name string) *bmcv1.BMCAction {
	f.t.Helper()
	a := &bmcv1.BMCAction{}
	if err := f.c.Get(context.Background(), client.ObjectKey{Name: name}, a); err != nil {
		f.t.Fatal(err)
	}
	return a
}

func bmc(name string) *bmcv1.BMC {
	b := &bmcv1.BMC{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: bmcv1.BMCSpec{NodeName: name}}
	b.Status.Network.IPAddress = "10.0.0.7"
	b.Status.PowerState = bmcv1.PowerOn
	return b
}

func action(name, target string, a bmcv1.PowerAction) *bmcv1.BMCAction {
	return &bmcv1.BMCAction{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       bmcv1.BMCActionSpec{BMCName: target, Action: a, RequestedBy: "alice@example.com", Reason: "maintenance"},
	}
}

var secret = &corev1.Secret{
	ObjectMeta: metav1.ObjectMeta{Name: "bmc-credentials", Namespace: ns},
	Data:       map[string][]byte{"username": []byte("admin"), "password": []byte("secret")},
}

func TestActionSucceeds(t *testing.T) {
	f := newFixture(t, true, bmc("gpu-01"), secret, action("a1", "gpu-01", bmcv1.ActionForceRestart))
	f.reconcile("a1")

	a := f.action("a1")
	if a.Status.Phase != bmcv1.PhaseSucceeded || a.Status.PowerStateBefore != bmcv1.PowerOn || a.Status.CompletionTime == nil {
		t.Fatalf("status = %+v", a.Status)
	}
	if len(f.calls) != 1 || f.calls[0].action != bmcv1.ActionForceRestart || f.calls[0].target.Address != "10.0.0.7" ||
		f.calls[0].target.Creds.Username != "admin" {
		t.Fatalf("calls = %+v", f.calls)
	}

	f.reconcile("a1")
	if len(f.calls) != 1 {
		t.Fatal("a finished action was executed again")
	}

	var events corev1.EventList
	_ = f.c.List(context.Background(), &events)
	if len(events.Items) != 1 || events.Items[0].Reason != "BMCPowerAction" || events.Items[0].InvolvedObject.Name != "gpu-01" {
		t.Fatalf("events = %+v", events.Items)
	}
}

func TestActionFails(t *testing.T) {
	f := newFixture(t, true, bmc("gpu-01"), secret, action("a1", "gpu-01", bmcv1.ActionOn))
	f.err = errors.New("connection refused")
	f.reconcile("a1")
	if a := f.action("a1"); a.Status.Phase != bmcv1.PhaseFailed || a.Status.Message != "connection refused" {
		t.Fatalf("status = %+v", a.Status)
	}
}

func TestActionRejected(t *testing.T) {
	cases := map[string]struct {
		enabled bool
		objs    []client.Object
		want    string
	}{
		"disabled":       {false, []client.Object{bmc("gpu-01"), secret}, "power actions are disabled on this kube-bmc server"},
		"unknown bmc":    {true, []client.Object{secret}, "BMC gpu-01 not found"},
		"no credentials": {true, []client.Object{bmc("gpu-01")}, `the node agent on gpu-01 did not execute the action within 0s and out-of-band access is not configured`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, tc.enabled, append(tc.objs, action("a1", "gpu-01", bmcv1.ActionForceOff))...)
			f.reconcile("a1")
			a := f.action("a1")
			if a.Status.Phase != bmcv1.PhaseRejected || a.Status.Message != tc.want {
				t.Fatalf("status = %+v", a.Status)
			}
			if len(f.calls) != 0 {
				t.Fatal("rejected action reached the BMC")
			}
		})
	}
}

func TestInterruptedActionFails(t *testing.T) {
	a := action("a1", "gpu-01", bmcv1.ActionOn)
	started := metav1.NewTime(time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC))
	a.Status = bmcv1.BMCActionStatus{Phase: bmcv1.PhaseRunning, StartTime: &started}
	f := newFixture(t, true, bmc("gpu-01"), secret, a)
	f.reconcile("a1")
	if got := f.action("a1"); got.Status.Phase != bmcv1.PhaseFailed || len(f.calls) != 0 {
		t.Fatalf("status = %+v, calls = %d", got.Status, len(f.calls))
	}
}

func TestFinishedActionExpires(t *testing.T) {
	a := action("a1", "gpu-01", bmcv1.ActionOn)
	done := metav1.NewTime(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC))
	a.Status = bmcv1.BMCActionStatus{Phase: bmcv1.PhaseSucceeded, CompletionTime: &done}
	f := newFixture(t, true, a)

	if res := f.reconcile("a1"); res.RequeueAfter != 12*time.Hour {
		t.Fatalf("requeue after %v", res.RequeueAfter)
	}
	f.now = f.now.Add(13 * time.Hour)
	f.reconcile("a1")
	err := f.c.Get(context.Background(), client.ObjectKey{Name: "a1"}, &bmcv1.BMCAction{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expired action still exists: %v", err)
	}
}

func TestInBandActionIsLeftToTheAgent(t *testing.T) {
	a := action("a1", "gpu-01", bmcv1.ActionForceRestart)
	f := newFixture(t, true, bmc("gpu-01"), secret, a)
	f.r.ClaimTimeout = 30 * time.Second
	created := f.action("a1").CreationTimestamp.Time
	f.now = created.Add(10 * time.Second)

	if res := f.reconcile("a1"); res.RequeueAfter != 20*time.Second || len(f.calls) != 0 {
		t.Fatalf("requeue = %v, calls = %d", res.RequeueAfter, len(f.calls))
	}
	if p := f.action("a1").Status.Phase; p != "" {
		t.Fatalf("phase = %s during the claim window", p)
	}
	// Unclaimed after the window: the node is down, so the server uses out-of-band access.
	f.now = created.Add(31 * time.Second)
	f.reconcile("a1")
	if got := f.action("a1"); got.Status.Phase != bmcv1.PhaseSucceeded || len(f.calls) != 1 {
		t.Fatalf("status = %+v, calls = %d", got.Status, len(f.calls))
	}
}

func TestUnclaimedInBandActionWithoutCredentials(t *testing.T) {
	f := newFixture(t, true, bmc("gpu-01"), action("a1", "gpu-01", bmcv1.ActionPowerCycle))
	f.r.ClaimTimeout = 30 * time.Second
	f.now = f.action("a1").CreationTimestamp.Add(time.Minute)
	f.reconcile("a1")
	a := f.action("a1")
	want := "the node agent on gpu-01 did not execute the action within 30s and out-of-band access is not configured"
	if a.Status.Phase != bmcv1.PhaseRejected || a.Status.Message != want {
		t.Fatalf("status = %+v", a.Status)
	}
}

func TestPowerOnNeedsOutOfBand(t *testing.T) {
	f := newFixture(t, true, bmc("gpu-01"), action("a1", "gpu-01", bmcv1.ActionOn))
	f.r.ClaimTimeout = time.Hour // On is never left to the agent
	f.reconcile("a1")
	a := f.action("a1")
	if a.Status.Phase != bmcv1.PhaseRejected || !strings.HasPrefix(a.Status.Message, "On requires out-of-band access, which is not configured") {
		t.Fatalf("status = %+v", a.Status)
	}
}
