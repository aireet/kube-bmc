package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/collector"
	"github.com/aireet/kube-bmc/internal/ipmi"
)

// fixtureRunner answers ipmitool commands with recorded output from a real server.
type fixtureRunner struct{}

var fixtures = map[string]string{
	"mc info": "mc_info.txt", "mc guid": "mc_guid.txt", "lan print 1": "lan_print.txt", "fru print 0": "fru_print.txt",
	"chassis status": "chassis_status.txt", "sel info": "sel_info.txt", "dcmi power reading": "dcmi_power_reading.txt",
	"sdr elist": "sdr_elist.txt", "sensor": "sensor.txt", "sel elist last 100": "sel_elist.txt",
}

func (fixtureRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	if len(args) > 1 && args[0] == "-S" {
		args = args[2:]
	}
	if args[0] == "sdr" && args[1] == "dump" {
		return nil, os.WriteFile(args[2], []byte("sdr"), 0o600)
	}
	return os.ReadFile("../ipmi/testdata/" + fixtures[strings.Join(args, " ")])
}

func TestAgentPublishesStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = bmcv1.AddToScheme(scheme)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu-01", UID: "node-uid"}}
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).WithStatusSubresource(&bmcv1.BMC{}).Build()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	col := collector.New("gpu-01", ipmi.NewClient(fixtureRunner{}, t.TempDir()), collector.Options{
		Interval: time.Hour, InventoryInterval: time.Hour, SELMinInterval: 0, SELEntries: 100,
		CommandTimeout: 5 * time.Second, SELTimeout: 5 * time.Second,
	}, log)
	a := New(k8s, col, Options{NodeName: "gpu-01", StatusInterval: time.Hour, Version: "test"}, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go col.Run(ctx)
	<-col.Updates()

	if err := a.sync(ctx); err != nil {
		t.Fatal(err)
	}
	var bmc bmcv1.BMC
	if err := k8s.Get(ctx, client.ObjectKey{Name: "gpu-01"}, &bmc); err != nil {
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

	// An unchanged status within the interval must not be written again.
	first := a.lastWrite
	if err := a.sync(ctx); err != nil || a.lastWrite != first {
		t.Fatalf("unexpected rewrite (err=%v)", err)
	}

	rec := httptest.NewRecorder()
	a.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	for _, want := range []string{`kube_bmc_health{node="gpu-01"} 3`, `kube_bmc_power_watts{node="gpu-01"} 1215`,
		`kube_bmc_chassis_fault{fault="Cooling/Fan Fault",node="gpu-01"} 1`, `kube_bmc_sel_used_ratio{node="gpu-01"} 1`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("metrics missing %s", want)
		}
	}

	// The snapshot API exposes thresholds merged into sensors, and SEL events newest first.
	deadline := time.Now().Add(5 * time.Second)
	for len(col.Snapshot().Events) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	rec = httptest.NewRecorder()
	a.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/snapshot", nil))
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

func TestVolatileFree(t *testing.T) {
	w1, w2 := int32(100), int32(200)
	a := bmcv1.BMCStatus{PowerWatts: &w1, Problems: []bmcv1.Problem{{Source: "Inlet", Message: "41 C"}}}
	b := bmcv1.BMCStatus{PowerWatts: &w2, Problems: []bmcv1.Problem{{Source: "Inlet", Message: "42 C"}}}
	if x, y := volatileFree(a), volatileFree(b); x.Problems[0] != y.Problems[0] || x.PowerWatts != nil {
		t.Fatal("readings should be ignored")
	}
	if a.Problems[0].Message != "41 C" {
		t.Fatal("volatileFree must not mutate its input")
	}
}
