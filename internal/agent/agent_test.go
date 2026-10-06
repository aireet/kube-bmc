package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/collector"
	"github.com/aireet/kube-bmc/ipmi"
	"github.com/aireet/kube-bmc/ipmi/ipmitest"
)

type env struct {
	k8s     client.Client
	agent   *Agent
	col     *collector.Collector
	patches atomic.Int32
	gets    atomic.Int32
}

func newEnv(t *testing.T) *env {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = bmcv1.AddToScheme(scheme)
	e := &env{}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu-01", UID: "node-uid"}}
	e.k8s = fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).WithStatusSubresource(&bmcv1.BMC{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*bmcv1.BMC); ok {
					e.gets.Add(1)
				}
				return c.Get(ctx, key, obj, opts...)
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
				e.patches.Add(1)
				return c.SubResource(sub).Patch(ctx, obj, p, opts...)
			},
		}).Build()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.col = collector.New("gpu-01", ipmi.New(ipmitest.Recorded()), collector.Options{
		CacheDir: t.TempDir(), Interval: time.Hour, InventoryInterval: time.Hour, SELMinInterval: 0, SELEntries: 100,
		CommandTimeout: 5 * time.Second, SELTimeout: 5 * time.Second,
	}, log)
	e.agent = New(e.k8s, e.col, Options{
		NodeName: "gpu-01", Namespace: "kube-bmc-system", LeaseDuration: 2 * time.Minute,
		StatusRefresh: time.Hour, MaxCollectionAge: time.Hour, Version: "test",
	}, log)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.col.Run(ctx)
	<-e.col.Updates()
	return e
}

func TestAgentPublishesStatusAndLease(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.agent.sync(ctx); err != nil {
		t.Fatal(err)
	}

	var bmc bmcv1.BMC
	if err := e.k8s.Get(ctx, client.ObjectKey{Name: "gpu-01"}, &bmc); err != nil {
		t.Fatal(err)
	}
	if len(bmc.OwnerReferences) != 1 || bmc.OwnerReferences[0].UID != "node-uid" {
		t.Fatalf("owner = %+v", bmc.OwnerReferences)
	}
	st := bmc.Status
	if st.Health != bmcv1.HealthCritical || st.PowerState != bmcv1.PowerOn || st.Device.Product != "SY8108G-G4" ||
		st.Network.Channel != 1 || st.AgentVersion != "test" || st.LastUpdated == nil {
		t.Fatalf("status = %+v", st)
	}
	if len(st.Conditions) != 1 || st.Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("conditions = %+v", st.Conditions)
	}

	var lease coordinationv1.Lease
	if err := e.k8s.Get(ctx, client.ObjectKey{Namespace: "kube-bmc-system", Name: "gpu-01"}, &lease); err != nil {
		t.Fatal(err)
	}
	if lease.Labels[LeaseLabel] != "true" || lease.OwnerReferences[0].UID != "node-uid" || LeaseExpired(&lease, time.Now()) {
		t.Fatalf("lease = %+v", lease)
	}
	renewed := lease.Spec.RenewTime.Time
	time.Sleep(10 * time.Millisecond)
	if err := e.agent.lease.renew(ctx, e.agent.node); err != nil {
		t.Fatal(err)
	}
	_ = e.k8s.Get(ctx, client.ObjectKey{Namespace: "kube-bmc-system", Name: "gpu-01"}, &lease)
	if !lease.Spec.RenewTime.After(renewed) {
		t.Fatal("lease was not renewed")
	}

	// Unchanged state is neither read nor written again.
	gets, patches := e.gets.Load(), e.patches.Load()
	for range 3 {
		if err := e.agent.sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if e.gets.Load() != gets || e.patches.Load() != patches {
		t.Fatalf("idle syncs caused %d GETs and %d PATCHes", e.gets.Load()-gets, e.patches.Load()-patches)
	}
}

func TestAgentHTTP(t *testing.T) {
	e := newEnv(t)
	h := e.agent.handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	for _, want := range []string{`kube_bmc_health{node="gpu-01"} 3`, `kube_bmc_power_watts{node="gpu-01"} 1215`,
		`kube_bmc_chassis_fault{fault="Cooling/Fan Fault",node="gpu-01"} 1`, `kube_bmc_sel_used_ratio{node="gpu-01"} 1`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("metrics missing %s", want)
		}
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 200 {
		t.Fatalf("readyz = %d", rec.Code)
	}

	deadline := time.Now().Add(5 * time.Second)
	for len(e.col.Snapshot().Events) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/snapshot", nil))
	var snap collector.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Events) != 30 || snap.Events[0].ID != "827a" {
		t.Fatalf("events: %d, first %+v", len(snap.Events), snap.Events[0])
	}
	for _, s := range snap.Sensors {
		if s.Name == "Inlet_Temp" && (s.Thresholds == nil || *s.Thresholds.UpperCritical != 50) {
			t.Fatalf("inlet thresholds = %+v", s.Thresholds)
		}
	}
}

func TestWriteReason(t *testing.T) {
	base := bmcv1.BMCStatus{
		Health: bmcv1.HealthWarning, PowerState: bmcv1.PowerOn, PowerWatts: ptr.To(int32(1000)), InletTemperature: ptr.To(int32(25)),
		SEL:      bmcv1.SEL{Entries: 100, UsedPercent: 41, LastAddTime: "t1"},
		Problems: []bmcv1.Problem{{Severity: bmcv1.HealthWarning, Source: "Inlet_Temp", Message: "41 degrees C above upper non-critical"}},
		Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Collecting", Message: "ok",
			LastTransitionTime: metav1.Now()}},
	}
	now := time.Now()
	recent := now.Add(-time.Minute)
	mutate := func(f func(s *bmcv1.BMCStatus)) bmcv1.BMCStatus {
		s := *base.DeepCopy()
		f(&s)
		return s
	}
	cases := map[string]struct {
		next      bmcv1.BMCStatus
		lastWrite time.Time
		want      string
	}{
		"first write":            {base, time.Time{}, "initial"},
		"unchanged":              {base, recent, ""},
		"watts within deadband":  {mutate(func(s *bmcv1.BMCStatus) { s.PowerWatts = ptr.To(int32(1190)) }), recent, ""},
		"watts beyond deadband":  {mutate(func(s *bmcv1.BMCStatus) { s.PowerWatts = ptr.To(int32(1200)) }), recent, "readings"},
		"inlet within deadband":  {mutate(func(s *bmcv1.BMCStatus) { s.InletTemperature = ptr.To(int32(27)) }), recent, ""},
		"inlet beyond deadband":  {mutate(func(s *bmcv1.BMCStatus) { s.InletTemperature = ptr.To(int32(28)) }), recent, "readings"},
		"new SEL entry":          {mutate(func(s *bmcv1.BMCStatus) { s.SEL.Entries, s.SEL.LastAddTime = 101, "t2" }), recent, ""},
		"SEL usage step":         {mutate(func(s *bmcv1.BMCStatus) { s.SEL.UsedPercent = 46 }), recent, "change"},
		"problem message only":   {mutate(func(s *bmcv1.BMCStatus) { s.Problems[0].Message = "42 degrees C above" }), recent, ""},
		"new problem":            {mutate(func(s *bmcv1.BMCStatus) { s.Problems = append(s.Problems, bmcv1.Problem{Source: "FAN3"}) }), recent, "change"},
		"health":                 {mutate(func(s *bmcv1.BMCStatus) { s.Health = bmcv1.HealthCritical }), recent, "change"},
		"power state":            {mutate(func(s *bmcv1.BMCStatus) { s.PowerState = bmcv1.PowerOff }), recent, "change"},
		"condition message only": {mutate(func(s *bmcv1.BMCStatus) { s.Conditions[0].Message = "other" }), recent, ""},
		"condition status":       {mutate(func(s *bmcv1.BMCStatus) { s.Conditions[0].Status = metav1.ConditionFalse }), recent, "change"},
		"sensor counts only":     {mutate(func(s *bmcv1.BMCStatus) { s.Sensors.NoReading++ }), recent, ""},
		"refresh due":            {mutate(func(s *bmcv1.BMCStatus) { s.PowerWatts = ptr.To(int32(1010)) }), now.Add(-11 * time.Minute), "refresh"},
	}
	for name, tc := range cases {
		if got := writeReason(base, tc.next, tc.lastWrite, now, 10*time.Minute); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
	if base.Problems[0].Message == "" {
		t.Fatal("writeReason mutated its input")
	}
}

func TestProblemHysteresis(t *testing.T) {
	w := &statusWriter{}
	t0 := time.Now()
	fan := bmcv1.Problem{Severity: bmcv1.HealthCritical, Source: "FAN3", Message: "0 RPM"}
	inlet := bmcv1.Problem{Severity: bmcv1.HealthWarning, Source: "Inlet_Temp", Message: "41 C"}

	got, h := w.debounce([]bmcv1.Problem{inlet, fan}, bmcv1.HealthCritical, t0)
	if len(got) != 2 || got[0].Source != "FAN3" || h != bmcv1.HealthCritical {
		t.Fatalf("initial: %v %s", got, h)
	}
	// Inlet_Temp drops below its threshold: it is kept for problemClearDelay.
	got, _ = w.debounce([]bmcv1.Problem{fan}, bmcv1.HealthCritical, t0.Add(time.Minute))
	if len(got) != 2 {
		t.Fatalf("problem removed before the clear delay: %v", got)
	}
	// All problems clear; health stays at the remembered severity until the delay passes.
	got, h = w.debounce(nil, bmcv1.HealthOK, t0.Add(3*time.Minute))
	if len(got) != 2 || h != bmcv1.HealthCritical {
		t.Fatalf("within delay: %v %s", got, h)
	}
	got, h = w.debounce(nil, bmcv1.HealthOK, t0.Add(9*time.Minute))
	if got != nil || h != bmcv1.HealthOK {
		t.Fatalf("after delay: %v %s", got, h)
	}
	// A new problem is reported immediately.
	if got, _ = w.debounce([]bmcv1.Problem{inlet}, bmcv1.HealthWarning, t0.Add(10*time.Minute)); len(got) != 1 {
		t.Fatalf("new problem: %v", got)
	}
}

// A source is reported once, at the highest severity observed within the clear delay.
func TestProblemEscalation(t *testing.T) {
	w := &statusWriter{}
	t0 := time.Now()
	warn := bmcv1.Problem{Severity: bmcv1.HealthWarning, Source: "Inlet_Temp", Message: "38 C above upper non-critical"}
	crit := bmcv1.Problem{Severity: bmcv1.HealthCritical, Source: "Inlet_Temp", Message: "51 C above upper critical"}

	w.debounce([]bmcv1.Problem{warn}, bmcv1.HealthWarning, t0)
	got, h := w.debounce([]bmcv1.Problem{crit}, bmcv1.HealthCritical, t0.Add(time.Minute))
	if len(got) != 1 || got[0] != crit || h != bmcv1.HealthCritical {
		t.Fatalf("escalated: %v %s", got, h)
	}
	// Back to warning: the critical reading is held for the clear delay, then lowered.
	got, h = w.debounce([]bmcv1.Problem{warn}, bmcv1.HealthWarning, t0.Add(2*time.Minute))
	if len(got) != 1 || got[0] != crit || h != bmcv1.HealthCritical {
		t.Fatalf("within delay: %v %s", got, h)
	}
	got, h = w.debounce([]bmcv1.Problem{warn}, bmcv1.HealthWarning, t0.Add(7*time.Minute))
	if len(got) != 1 || got[0] != warn || h != bmcv1.HealthWarning {
		t.Fatalf("after delay: %v %s", got, h)
	}
}

func TestLeaseExpired(t *testing.T) {
	now := time.Now()
	l := &coordinationv1.Lease{Spec: coordinationv1.LeaseSpec{
		RenewTime: &metav1.MicroTime{Time: now.Add(-time.Minute)}, LeaseDurationSeconds: ptr.To(int32(120)),
	}}
	if LeaseExpired(l, now) {
		t.Fatal("valid lease reported expired")
	}
	if !LeaseExpired(l, now.Add(2*time.Minute)) || !LeaseExpired(nil, now) {
		t.Fatal("expired lease reported valid")
	}
}

func TestForgetSELProblems(t *testing.T) {
	w := &statusWriter{}
	now := time.Now()
	problems := []bmcv1.Problem{
		{Severity: bmcv1.HealthCritical, Source: "FAN3"},
		{Severity: bmcv1.HealthWarning, Source: "security"},
		{Severity: bmcv1.HealthWarning, Source: "sel"},
		{Severity: bmcv1.HealthWarning, Source: "SEL_Status"},
	}
	w.debounce(problems, bmcv1.HealthCritical, now)

	// The log was cleared: SEL problems disappear at once, others keep their clear delay.
	w.forget(selProblemSources...)
	got, _ := w.debounce(nil, bmcv1.HealthOK, now.Add(time.Minute))
	if len(got) != 1 || got[0].Source != "FAN3" {
		t.Fatalf("problems = %+v", got)
	}
}
