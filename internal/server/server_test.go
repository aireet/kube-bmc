package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/agent"
	"github.com/aireet/kube-bmc/internal/auth"
	"github.com/aireet/kube-bmc/internal/collector"
	"github.com/aireet/kube-bmc/internal/controller"
	"github.com/aireet/kube-bmc/ipmi"
)

const ns = "kube-bmc-system"

// newTestBackend returns a Cluster backend over a fake client holding one node, its BMC
// and its agent pod, whose snapshot API is served by an httptest server.
func newTestBackend(t *testing.T, extra ...client.Object) (*Cluster, client.Client) {
	t.Helper()
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		v := 55.0
		_ = json.NewEncoder(w).Encode(collector.Snapshot{
			Node: "gpu-01", CollectedAt: time.Now(),
			Sensors: []ipmi.Sensor{{Name: "Inlet_Temp", Type: "temperature", Value: &v, Unit: "degrees C", Severity: ipmi.SeverityWarning}},
			Events:  []ipmi.Event{{ID: "1", Sensor: "Fan FAN3", Event: "Lower Critical going low", Asserted: true}},
		})
	}))
	t.Cleanup(agent.Close)
	host, port, _ := net.SplitHostPort(agent.Listener.Addr().String())
	agentPort, _ := strconv.Atoi(port)

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = bmcv1.AddToScheme(scheme)
	now := metav1.Now()
	b := &bmcv1.BMC{ObjectMeta: metav1.ObjectMeta{Name: "gpu-01"}, Spec: bmcv1.BMCSpec{NodeName: "gpu-01"}}
	b.Status.Health, b.Status.LastUpdated = bmcv1.HealthWarning, &now
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu-01"}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-x", Namespace: ns, Labels: map[string]string{"app": "agent"}},
		Spec:       corev1.PodSpec{NodeName: "gpu-01"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: host},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append([]client.Object{b, node, pod}, extra...)...).WithStatusSubresource(&bmcv1.BMC{}).Build()
	return NewCluster(c, ClusterOptions{
		Namespace: ns, AgentSelector: labels.SelectorFromSet(labels.Set{"app": "agent"}), AgentPort: agentPort,
		Endpoints: controller.Endpoints{Reader: c, Namespace: ns}, StaleAfter: time.Hour,
	}), c
}

// headerAuthn identifies callers by the X-Test-User header.
type headerAuthn struct{}

func (headerAuthn) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get("X-Test-User")
		if user == "" {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Username: user})))
	})
}

func (headerAuthn) Login(w http.ResponseWriter, r *http.Request)    { http.NotFound(w, r) }
func (headerAuthn) Callback(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
func (headerAuthn) Logout(w http.ResponseWriter, r *http.Request)   { http.NotFound(w, r) }

func testHandler(t *testing.T, powerActions bool) (http.Handler, client.Client) {
	t.Helper()
	backend, c := newTestBackend(t)
	return New(Options{
		Backend: backend,
		Config:  Config{Version: "test", Actions: powerActions},
		UI:      fstest.MapFS{"index.html": {Data: []byte("<html>kube-bmc</html>")}, "assets/app.js": {Data: []byte("1")}},
		Authn:   headerAuthn{},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler(), c
}

func do(t *testing.T, h http.Handler, user, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestReadEndpoints(t *testing.T) {
	h, _ := testHandler(t, false)
	rec := do(t, h, "alice", "GET", "/api/v1/bmcs", "")
	var views []View
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil || len(views) != 1 || views[0].Agent == nil || views[0].Stale ||
		views[0].InBandPower {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "alice", "GET", "/api/v1/bmcs/gpu-01/live", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Inlet_Temp") {
		t.Fatalf("live: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "alice", "GET", "/api/v1/bmcs/missing", ""); rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
	if rec := do(t, h, "", "GET", "/api/v1/bmcs", ""); rec.Code != 401 {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
}

func TestStaleness(t *testing.T) {
	lease := func(renewed time.Duration) *coordinationv1.Lease {
		return &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: "gpu-01", Namespace: ns, Labels: map[string]string{agent.LeaseLabel: "true"}},
			Spec: coordinationv1.LeaseSpec{
				RenewTime:            &metav1.MicroTime{Time: time.Now().Add(-renewed)},
				LeaseDurationSeconds: ptr.To(int32(120)),
			},
		}
	}
	for name, tc := range map[string]struct {
		lease *coordinationv1.Lease
		stale bool
	}{
		"valid lease":   {lease(30 * time.Second), false},
		"expired lease": {lease(5 * time.Minute), true},
	} {
		backend, _ := newTestBackend(t, tc.lease)
		v, err := backend.Get(context.Background(), "gpu-01")
		if err != nil || v.Stale != tc.stale || v.LastSeen == nil {
			t.Errorf("%s: stale = %v, lastSeen = %v, err = %v", name, v.Stale, v.LastSeen, err)
		}
	}
}

func TestMe(t *testing.T) {
	h, _ := testHandler(t, true)
	rec := do(t, h, "alice", "GET", "/api/v1/me", "")
	var me auth.Identity
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if me.Username != "alice" {
		t.Fatalf("me = %+v", me)
	}
}

func TestPowerCreatesAction(t *testing.T) {
	h, c := testHandler(t, true)
	body := `{"action":"ForceRestart","confirm":"gpu-01","reason":"kernel hang"}`

	rec := do(t, h, "alice", "POST", "/api/v1/bmcs/gpu-01/power", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("operator: %d %s", rec.Code, rec.Body)
	}
	var list bmcv1.BMCActionList
	_ = c.List(context.Background(), &list)
	if len(list.Items) != 1 {
		t.Fatalf("%d actions created", len(list.Items))
	}
	a := list.Items[0].Spec
	if a.BMCName != "gpu-01" || a.Action != bmcv1.ActionForceRestart || a.RequestedBy != "alice" || a.Reason != "kernel hang" {
		t.Fatalf("spec = %+v", a)
	}
	if rec := do(t, h, "alice", "GET", "/api/v1/actions?bmc=gpu-01", ""); !strings.Contains(rec.Body.String(), "ForceRestart") {
		t.Fatalf("actions: %s", rec.Body)
	}
}

func TestPowerValidation(t *testing.T) {
	h, _ := testHandler(t, true)
	for body, want := range map[string]int{
		`{"action":"ForceOff","confirm":"gpu-02"}`: http.StatusBadRequest,
		`{"action":"Explode","confirm":"gpu-01"}`:  http.StatusBadRequest,
		`not json`: http.StatusBadRequest,
		`{"action":"ForceOff","confirm":"missing"}`: http.StatusBadRequest,
	} {
		if rec := do(t, h, "alice", "POST", "/api/v1/bmcs/gpu-01/power", body); rec.Code != want {
			t.Errorf("%s: got %d, want %d", body, rec.Code, want)
		}
	}
	if rec := do(t, h, "alice", "POST", "/api/v1/bmcs/missing/power", `{"action":"On","confirm":"missing"}`); rec.Code != 404 {
		t.Errorf("unknown BMC: %d", rec.Code)
	}

	req := httptest.NewRequest("POST", "/api/v1/bmcs/gpu-01/power", strings.NewReader(`{"action":"On","confirm":"gpu-01"}`))
	req.Header.Set("X-Test-User", "alice")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post: %d", rec.Code)
	}

	disabled, _ := testHandler(t, false)
	if rec := do(t, disabled, "alice", "POST", "/api/v1/bmcs/gpu-01/power", `{"action":"On","confirm":"gpu-01"}`); rec.Code != 403 {
		t.Errorf("disabled: %d", rec.Code)
	}
}

func TestIdentifyNeedsNoConfirmation(t *testing.T) {
	h, c := testHandler(t, true)
	if rec := do(t, h, "alice", "POST", "/api/v1/bmcs/gpu-01/actions", `{"action":"IdentifyOn"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("identify: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "alice", "POST", "/api/v1/bmcs/gpu-01/actions", `{"action":"ClearSEL","reason":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("ClearSEL without confirmation: %d", rec.Code)
	}
	var list bmcv1.BMCActionList
	_ = c.List(context.Background(), &list)
	if len(list.Items) != 1 || list.Items[0].Spec.Action != bmcv1.ActionIdentifyOn {
		t.Fatalf("actions = %+v", list.Items)
	}
}

func TestSPA(t *testing.T) {
	h, _ := testHandler(t, false)
	if rec := do(t, h, "", "GET", "/servers/gpu-01", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "kube-bmc") {
		t.Fatalf("client route: %d", rec.Code)
	}
	if rec := do(t, h, "", "GET", "/assets/app.js", ""); !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset headers: %v", rec.Header())
	}
	if rec := do(t, h, "", "GET", "/api/v1/config", ""); rec.Code != 200 {
		t.Fatalf("config must not require authentication: %d", rec.Code)
	}
}
