package controller

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/ipmi"
)

// ipmiRecorder records ipmitool calls, the action phase stored at the time of each call
// and the SEL archives that existed at that time.
type ipmiRecorder struct {
	c        client.Client
	calls    []string
	phases   []bmcv1.ActionPhase
	archives []int
	sel      string
	fail     map[string]error
}

func (r *ipmiRecorder) Run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := strings.Join(args, " ")
	r.calls = append(r.calls, cmd)
	a := &bmcv1.BMCAction{}
	_ = r.c.Get(ctx, client.ObjectKey{Name: "a1"}, a)
	r.phases = append(r.phases, a.Status.Phase)
	var cms corev1.ConfigMapList
	_ = r.c.List(ctx, &cms)
	r.archives = append(r.archives, len(cms.Items))
	if err := r.fail[cmd]; err != nil {
		return nil, err
	}
	if cmd == "sel elist" {
		return []byte(r.sel), nil
	}
	return nil, nil
}

func inBand(t *testing.T, enabled bool, objs ...client.Object) (*InBandReconciler, *ipmiRecorder, *fixture) {
	t.Helper()
	f := newFixture(t, true, objs...)
	rec := &ipmiRecorder{c: f.c, fail: map[string]error{}}
	return &InBandReconciler{Client: f.c, Node: "gpu-01", IPMI: ipmi.New(rec), Namespace: ns, SELArchives: 3,
		Enabled: enabled, Timeout: time.Minute, Now: func() time.Time { return f.now }}, rec, f
}

func reconcileInBand(t *testing.T, r *InBandReconciler) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "a1"}}); err != nil {
		t.Fatal(err)
	}
}

func TestInBandExecutes(t *testing.T) {
	for act, verb := range map[bmcv1.ActionType]string{
		bmcv1.ActionGracefulShutdown: "soft", bmcv1.ActionForceOff: "off",
		bmcv1.ActionForceRestart: "reset", bmcv1.ActionPowerCycle: "cycle",
	} {
		r, rec, f := inBand(t, true, newBMC("gpu-01"), action("a1", "gpu-01", act))
		reconcileInBand(t, r)
		if len(rec.calls) != 1 || rec.calls[0] != "chassis power "+verb {
			t.Fatalf("%s: calls = %v", act, rec.calls)
		}
		// The outcome is stored before the host goes down.
		if rec.phases[0] != bmcv1.PhaseSucceeded {
			t.Fatalf("%s: phase at execution = %s", act, rec.phases[0])
		}
		a := f.action("a1")
		if a.Status.Phase != bmcv1.PhaseSucceeded || a.Status.PowerStateBefore != bmcv1.PowerOn || a.Status.CompletionTime == nil ||
			a.Status.Deadline == nil || a.Status.Deadline.Sub(a.Status.StartTime.Time) != r.Timeout {
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
	r, rec, f := inBand(t, true, newBMC("gpu-01"), action("a1", "gpu-01", bmcv1.ActionForceRestart))
	rec.fail["chassis power reset"] = errors.New("ipmitool chassis power reset: Could not open device at /dev/ipmi0")
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
		r, rec, f := inBand(t, tc.enabled, newBMC("gpu-01"), tc.obj)
		reconcileInBand(t, r)
		if len(rec.calls) != 0 {
			t.Errorf("%s: executed %v", name, rec.calls)
		}
		if got := f.action("a1").Status.Phase; got != tc.obj.Status.Phase {
			t.Errorf("%s: phase changed to %s", name, got)
		}
	}
}

func TestIdentify(t *testing.T) {
	r, rec, f := inBand(t, true, newBMC("gpu-01"), action("a1", "gpu-01", bmcv1.ActionIdentifyOn))
	reconcileInBand(t, r)
	if strings.Join(rec.calls, ",") != "chassis identify force" {
		t.Fatalf("calls = %v", rec.calls)
	}
	if a := f.action("a1"); a.Status.Phase != bmcv1.PhaseSucceeded || a.Status.Message != "identify light turned on until IdentifyOff" {
		t.Fatalf("status = %+v", a.Status)
	}

	// BMCs without indefinite identify fall back to the maximum interval.
	r, rec, f = inBand(t, true, newBMC("gpu-01"), action("a1", "gpu-01", bmcv1.ActionIdentifyOn))
	rec.fail["chassis identify force"] = errors.New("Invalid data field in request")
	reconcileInBand(t, r)
	if strings.Join(rec.calls, ",") != "chassis identify force,chassis identify 255" {
		t.Fatalf("calls = %v", rec.calls)
	}
	if a := f.action("a1"); a.Status.Phase != bmcv1.PhaseSucceeded || !strings.Contains(a.Status.Message, "255 seconds") {
		t.Fatalf("status = %+v", a.Status)
	}

	r, rec, f = inBand(t, true, newBMC("gpu-01"), action("a1", "gpu-01", bmcv1.ActionIdentifyOff))
	reconcileInBand(t, r)
	if strings.Join(rec.calls, ",") != "chassis identify 0" || f.action("a1").Status.Phase != bmcv1.PhaseSucceeded {
		t.Fatalf("calls = %v, status = %+v", rec.calls, f.action("a1").Status)
	}
}

func TestClearSELArchivesFirst(t *testing.T) {
	a := action("a1", "gpu-01", bmcv1.ActionClearSEL)
	a.UID = "action-uid"
	r, rec, f := inBand(t, true, newBMC("gpu-01"), a)
	rec.sel = "237b | 10/04/26 | 17:01:03 UTC | Session Audit #0xff |  | Asserted\n237c | 10/04/26 | 17:01:03 UTC | Session Audit #0xff |  | Asserted\n"
	cleared := 0
	r.OnSELCleared = func() { cleared++ }
	reconcileInBand(t, r)
	if cleared != 1 {
		t.Fatalf("OnSELCleared called %d times", cleared)
	}

	if strings.Join(rec.calls, ",") != "sel elist,sel clear" {
		t.Fatalf("calls = %v", rec.calls)
	}
	if rec.archives[1] != 1 {
		t.Fatal("the log was cleared before the archive was stored")
	}
	var cms corev1.ConfigMapList
	_ = f.c.List(context.Background(), &cms)
	cm := cms.Items[0]
	if cm.Namespace != ns || cm.Labels[SELArchiveLabel] != "true" || cm.Labels[BMCLabel] != "gpu-01" ||
		cm.Annotations["bmc.kube-bmc.io/entries"] != "2" || cm.Annotations["bmc.kube-bmc.io/reason"] != "maintenance" {
		t.Fatalf("archive = %+v", cm)
	}
	zr, err := gzip.NewReader(bytes.NewReader(cm.BinaryData[SELArchiveKey]))
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := io.ReadAll(zr); string(text) != rec.sel {
		t.Fatalf("archived log = %q", text)
	}
	got := f.action("a1")
	if got.Status.Phase != bmcv1.PhaseSucceeded || got.Status.SELArchive != ns+"/"+cm.Name ||
		got.Status.Message != "System Event Log cleared; 2 entries saved to ConfigMap "+ns+"/"+cm.Name {
		t.Fatalf("status = %+v", got.Status)
	}
}

func TestClearSELKeepsLogWhenReadFails(t *testing.T) {
	r, rec, f := inBand(t, true, newBMC("gpu-01"), action("a1", "gpu-01", bmcv1.ActionClearSEL))
	rec.fail["sel elist"] = errors.New("Could not open device at /dev/ipmi0")
	r.OnSELCleared = func() { t.Fatal("OnSELCleared called although the log was not cleared") }
	reconcileInBand(t, r)
	if strings.Join(rec.calls, ",") != "sel elist" {
		t.Fatalf("calls = %v", rec.calls)
	}
	if a := f.action("a1"); a.Status.Phase != bmcv1.PhaseFailed || !strings.Contains(a.Status.Message, "it was not cleared") {
		t.Fatalf("status = %+v", a.Status)
	}
}

func TestClearSELKeepsNewestArchives(t *testing.T) {
	archive := func(node, ts string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name: "sel-" + node + "-" + ts, Namespace: ns, Labels: map[string]string{SELArchiveLabel: "true", BMCLabel: node},
		}}
	}
	r, _, f := inBand(t, true, newBMC("gpu-01"), action("a1", "gpu-01", bmcv1.ActionClearSEL),
		archive("gpu-01", "20261001-080000"), archive("gpu-01", "20261002-080000"), archive("gpu-01", "20261003-080000"),
		archive("gpu-02", "20260901-080000"))
	reconcileInBand(t, r)

	var cms corev1.ConfigMapList
	_ = f.c.List(context.Background(), &cms)
	var names []string
	for _, cm := range cms.Items {
		names = append(names, cm.Name)
	}
	slices.Sort(names)
	want := "sel-gpu-01-20261002-080000,sel-gpu-01-20261003-080000,sel-gpu-01-20261005-120000,sel-gpu-02-20260901-080000"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("archives = %s", got)
	}
}
