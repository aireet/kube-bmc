package kubectl

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/collector"
	"github.com/aireet/kube-bmc/internal/ipmi"
)

func fixtureBMC(name string, h bmcv1.Health) *bmcv1.BMC {
	w, temp := int32(1240), int32(43)
	now := metav1.Now()
	b := &bmcv1.BMC{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: bmcv1.BMCSpec{NodeName: name}}
	b.Status = bmcv1.BMCStatus{Health: h, PowerState: bmcv1.PowerOn, PowerWatts: &w, InletTemperature: &temp, LastUpdated: &now}
	b.Status.Device.Manufacturer, b.Status.Device.Product = "Gooxi", "SY8108G-G4"
	b.Status.Network.IPAddress = "10.20.0.31"
	if h == bmcv1.HealthCritical {
		b.Status.Problems = []bmcv1.Problem{
			{Severity: bmcv1.HealthCritical, Source: "FAN7", Message: "0 RPM below lower non-recoverable"},
			{Severity: bmcv1.HealthWarning, Source: "sel", Message: "System Event Log is 100% full"},
		}
	}
	return b
}

type harness struct {
	out     *bytes.Buffer
	in      *bytes.Buffer
	client  client.Client
	clients *Clients
}

func newHarness(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) *harness {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = bmcv1.AddToScheme(scheme)
	b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&bmcv1.BMCAction{})
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	h := &harness{out: &bytes.Buffer{}, in: &bytes.Buffer{}, client: b.Build()}
	hot, ok := 47.0, 22.0
	h.clients = &Clients{
		Client: h.client,
		Snapshot: func(context.Context, string) (*collector.Snapshot, error) {
			return &collector.Snapshot{
				SELInfo: ipmi.SELInfo{Entries: 3639, UsedPercent: 100},
				Sensors: []ipmi.Sensor{
					{Name: "Outlet_Temp", Type: "temperature", Value: &ok, Reading: "22 degrees C", Severity: ipmi.SeverityOK},
					{Name: "Inlet_Temp", Type: "temperature", Value: &hot, Reading: "47 degrees C", Severity: ipmi.SeverityWarning,
						Thresholds: &ipmi.Thresholds{UpperNonCritical: &hot}},
					{Name: "OCP1_Temp", Type: "temperature", Reading: "No Reading", Severity: ipmi.SeverityNoReading},
				},
				Events: []ipmi.Event{
					{ID: "827a", Timestamp: "10/05/2026 18:05:51", Sensor: "Temperature Inlet_Temp", Event: "Upper Non-critical going high", Asserted: true},
					{ID: "8279", Timestamp: "10/05/2026 18:05:40", Sensor: "Fan FAN7", Event: "Lower Non-recoverable going low", Asserted: true},
				},
			}, nil
		},
		Whoami: func(context.Context) (string, error) { return "oidc:alice@example.com", nil },
	}
	return h
}

func (h *harness) run(t *testing.T, args ...string) error {
	t.Helper()
	h.out.Reset()
	cmd := NewCommand(genericiooptions.IOStreams{In: h.in, Out: h.out, ErrOut: h.out}, "test", h.clients)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	return cmd.Execute()
}

func TestList(t *testing.T) {
	h := newHarness(t, nil, fixtureBMC("server131", bmcv1.HealthCritical), fixtureBMC("host002", bmcv1.HealthOK))
	if err := h.run(t, "list"); err != nil {
		t.Fatal(err)
	}
	out := h.out.String()
	for _, want := range []string{"NAME", "host002", "server131", "Critical", "1240", "43°C", "FAN7: 0 RPM below lower non-recoverable (+1)"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "host002") > strings.Index(out, "server131") {
		t.Error("list is not sorted by name")
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("colors written to a non-terminal")
	}

	_ = h.run(t, "list", "--health", "critical", "-o", "name")
	if strings.TrimSpace(h.out.String()) != "bmc/server131" {
		t.Errorf("filtered names = %q", h.out.String())
	}
}

func TestDescribe(t *testing.T) {
	h := newHarness(t, nil, fixtureBMC("server131", bmcv1.HealthCritical))
	if err := h.run(t, "describe", "server131"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Health:", "Critical", "Gooxi SY8108G-G4", "Problems:", "FAN7", "Recent actions:"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("describe output missing %q:\n%s", want, h.out)
		}
	}
}

func TestSensorsAndEvents(t *testing.T) {
	h := newHarness(t, nil, fixtureBMC("server131", bmcv1.HealthCritical))
	_ = h.run(t, "sensors", "server131")
	out := h.out.String()
	if !strings.Contains(out, "unc 47") || strings.Contains(out, "OCP1_Temp") || strings.Index(out, "Inlet_Temp") > strings.Index(out, "Outlet_Temp") {
		t.Errorf("sensors output:\n%s", out)
	}
	_ = h.run(t, "sensors", "server131", "--problems")
	if strings.Contains(h.out.String(), "Outlet_Temp") {
		t.Errorf("--problems shows healthy sensors:\n%s", h.out)
	}
	_ = h.run(t, "events", "server131", "--grep", "fan")
	if out := h.out.String(); !strings.Contains(out, "100% used") || !strings.Contains(out, "8279") || strings.Contains(out, "827a") {
		t.Errorf("events output:\n%s", out)
	}
}

func TestPowerRequiresConfirmation(t *testing.T) {
	h := newHarness(t, nil, fixtureBMC("server131", bmcv1.HealthOK))
	h.in.WriteString("wrong-name\n")
	if err := h.run(t, "power", "server131", "ForceRestart", "--reason", "test"); err == nil {
		t.Fatal("mismatched confirmation accepted")
	}
	if err := h.run(t, "power", "server131", "ForceRestart"); err == nil || !strings.Contains(err.Error(), "--reason") {
		t.Fatalf("missing reason: %v", err)
	}
	if err := h.run(t, "power", "server131", "Explode", "--reason", "x", "--yes"); err == nil {
		t.Fatal("invalid action accepted")
	}
	var list bmcv1.BMCActionList
	_ = h.client.List(context.Background(), &list)
	if len(list.Items) != 0 {
		t.Fatalf("%d actions created without confirmation", len(list.Items))
	}

	h.in.WriteString("server131\n")
	if err := h.run(t, "power", "server131", "ForceRestart", "--reason", "kernel hang"); err != nil {
		t.Fatal(err)
	}
	_ = h.client.List(context.Background(), &list)
	if len(list.Items) != 1 {
		t.Fatalf("%d actions", len(list.Items))
	}
	spec := list.Items[0].Spec
	if spec.RequestedBy != "oidc:alice@example.com" || spec.Reason != "[kubectl] kernel hang" || spec.Action != bmcv1.ActionForceRestart {
		t.Fatalf("spec = %+v", spec)
	}
}

func TestPowerWait(t *testing.T) {
	funcs := &interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		if a, ok := obj.(*bmcv1.BMCAction); ok {
			a.Status.Phase, a.Status.Message = bmcv1.PhaseRejected, "power actions are disabled on this kube-bmc server"
		}
		return nil
	}}
	h := newHarness(t, funcs, fixtureBMC("server131", bmcv1.HealthOK))
	err := h.run(t, "power", "server131", "On", "--reason", "test", "--yes", "--wait", "--timeout", time.Minute.String())
	if err == nil || !strings.Contains(h.out.String(), "Rejected: power actions are disabled") {
		t.Fatalf("err = %v, output:\n%s", err, h.out)
	}
}
