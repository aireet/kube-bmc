package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/auth"
	"github.com/aireet/kube-bmc/internal/collector"
	"github.com/aireet/kube-bmc/internal/ipmi"
	"github.com/aireet/kube-bmc/internal/server"
)

// backend is an in-memory server.Backend with two servers.
type backend struct {
	actions []bmcv1.BMCAction
}

func (b *backend) List(context.Context) ([]server.View, error) {
	w := int32(4100)
	ok := server.View{Name: "gpu-01", Status: bmcv1.BMCStatus{Health: bmcv1.HealthOK, PowerState: bmcv1.PowerOn, PowerWatts: &w}}
	bad := server.View{Name: "gpu-02", Status: bmcv1.BMCStatus{Health: bmcv1.HealthCritical, PowerState: bmcv1.PowerOn,
		Problems: []bmcv1.Problem{{Severity: bmcv1.HealthCritical, Source: "FAN3", Message: "0 RPM below lower non-recoverable"}}}}
	bad.Status.Device.Product = "SY8108G-G4"
	return []server.View{ok, bad}, nil
}

func (b *backend) Get(ctx context.Context, name string) (server.View, error) {
	views, _ := b.List(ctx)
	for _, v := range views {
		if v.Name == name {
			return v, nil
		}
	}
	return server.View{}, fmt.Errorf("bmc %s: %w", name, server.ErrNotFound)
}

func (b *backend) Live(_ context.Context, name string) (*collector.Snapshot, error) {
	hot, cold := 47.0, 22.0
	return &collector.Snapshot{Node: name, CollectedAt: time.Now(),
		SELInfo: ipmi.SELInfo{Entries: 512, UsedPercent: 100},
		Sensors: []ipmi.Sensor{
			{Name: "Inlet_Temp", Type: "temperature", Value: &hot, Severity: ipmi.SeverityWarning},
			{Name: "Outlet_Temp", Type: "temperature", Value: &cold, Severity: ipmi.SeverityOK},
			{Name: "FAN3", Type: "fan", Severity: ipmi.SeverityCritical},
		},
		Events: []ipmi.Event{{ID: "2", Sensor: "Fan FAN3"}, {ID: "1", Sensor: "Temperature Inlet_Temp"}},
	}, nil
}

func (b *backend) CreateAction(_ context.Context, req server.ActionRequest) (*bmcv1.BMCAction, error) {
	a := bmcv1.BMCAction{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d", req.BMC, len(b.actions)), CreationTimestamp: metav1.Now()},
		Spec:       bmcv1.BMCActionSpec{BMCName: req.BMC, Action: req.Action, RequestedBy: req.RequestedBy, Reason: req.Reason},
	}
	b.actions = append(b.actions, a)
	return &a, nil
}

func (b *backend) ListActions(context.Context, string) ([]bmcv1.BMCAction, error) {
	return b.actions, nil
}

func (b *backend) GetAction(_ context.Context, name string) (*bmcv1.BMCAction, error) {
	for i := range b.actions {
		if b.actions[i].Name == name {
			return &b.actions[i], nil
		}
	}
	return nil, server.ErrNotFound
}

// connect starts the MCP endpoint behind bearer authentication, where the token is the
// username, and returns a session authenticated as user.
func connect(t *testing.T, b *backend, powerActions bool, user string) *mcp.ClientSession {
	t.Helper()
	srv := New(Options{Backend: b, Actions: powerActions, Version: "test"})
	verifier := func(_ context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		if token == "" {
			return nil, mcpauth.ErrInvalidToken
		}
		return &mcpauth.TokenInfo{UserID: token, Expiration: time.Now().Add(time.Hour),
			Extra: map[string]any{"identity": auth.Identity{Username: token}}}, nil
	}
	h := mcpauth.RequireBearerToken(verifier, nil)(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	httpClient := &http.Client{Transport: bearer{token: user}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: ts.URL, HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, out any) error {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		return errors.New(res.Content[0].(*mcp.TextContent).Text)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	return json.Unmarshal(raw, out)
}

func TestTools(t *testing.T) {
	cs := connect(t, &backend{}, true, "viewer")

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.Name == "power_action" && (tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint) {
			t.Error("power_action must be annotated as destructive")
		}
	}
	slices.Sort(names)
	want := "clear_sel,fleet_summary,get_action,get_events,get_sensors,get_server,list_actions,list_servers,locate_server,power_action"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("tools = %s", got)
	}

	var fleet FleetSummary
	if err := call(t, cs, "fleet_summary", nil, &fleet); err != nil {
		t.Fatal(err)
	}
	if fleet.Servers != 2 || fleet.ByHealth["Critical"] != 1 || fleet.TotalWatts != 4100 || len(fleet.NeedsAttention) != 1 ||
		fleet.NeedsAttention[0].Name != "gpu-02" {
		t.Fatalf("fleet = %+v", fleet)
	}

	var list ServerList
	if err := call(t, cs, "list_servers", map[string]any{"query": "sy8108"}, &list); err != nil || len(list.Servers) != 1 {
		t.Fatalf("list = %+v, err = %v", list, err)
	}

	var sensors SensorList
	if err := call(t, cs, "get_sensors", map[string]any{"name": "gpu-02", "problemsOnly": true}, &sensors); err != nil {
		t.Fatal(err)
	}
	if len(sensors.Sensors) != 2 {
		t.Fatalf("problem sensors = %+v", sensors.Sensors)
	}

	var events EventList
	if err := call(t, cs, "get_events", map[string]any{"name": "gpu-02", "limit": 1}, &events); err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 1 || events.Events[0].ID != "2" || events.SELUsedPercent != 100 {
		t.Fatalf("events = %+v", events)
	}

	var detail ServerDetail
	if err := call(t, cs, "get_server", map[string]any{"name": "nope"}, &detail); err == nil {
		t.Fatal("unknown server returned no error")
	}
}

func TestPowerAction(t *testing.T) {
	b := &backend{}
	args := map[string]any{"name": "gpu-02", "action": "ForceRestart", "reason": "fan replaced"}
	var a ActionSummary

	if err := call(t, connect(t, b, false, "operator"), "power_action", args, &a); err == nil ||
		!strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled: %v", err)
	}

	cs := connect(t, b, true, "operator")
	if err := call(t, cs, "power_action", map[string]any{"name": "gpu-02", "action": "ForceRestart", "reason": " "}, &a); err == nil {
		t.Fatal("empty reason accepted")
	}
	if err := call(t, cs, "power_action", args, &a); err != nil {
		t.Fatal(err)
	}
	if a.BMC != "gpu-02" || a.RequestedBy != "operator" || a.Reason != "[mcp] fan replaced" || a.Phase != bmcv1.PhasePending {
		t.Fatalf("action = %+v", a)
	}
	var got ActionSummary
	if err := call(t, cs, "get_action", map[string]any{"name": a.Name}, &got); err != nil || got.Name != a.Name {
		t.Fatalf("get_action = %+v, %v", got, err)
	}
	if len(b.actions) != 1 {
		t.Fatalf("%d actions recorded", len(b.actions))
	}
}

func TestLocateAndClearSEL(t *testing.T) {
	b := &backend{}
	cs := connect(t, b, true, "operator")
	var a ActionSummary
	if err := call(t, cs, "locate_server", map[string]any{"name": "gpu-02", "on": true}, &a); err != nil || a.Action != bmcv1.ActionIdentifyOn {
		t.Fatalf("locate: %+v %v", a, err)
	}
	if err := call(t, cs, "clear_sel", map[string]any{"name": "gpu-02", "reason": ""}, &a); err == nil {
		t.Fatal("clear_sel without reason accepted")
	}
	if err := call(t, cs, "clear_sel", map[string]any{"name": "gpu-02", "reason": "log full"}, &a); err != nil || a.Action != bmcv1.ActionClearSEL {
		t.Fatalf("clear_sel: %+v %v", a, err)
	}
	if err := call(t, cs, "power_action", map[string]any{"name": "gpu-02", "action": "ClearSEL", "reason": "x"}, &a); err == nil {
		t.Fatal("power_action accepted a non-power action")
	}
}

func TestDiagnosePrompt(t *testing.T) {
	cs := connect(t, &backend{}, false, "viewer")
	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "diagnose_server", Arguments: map[string]string{"name": "gpu-02"}})
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Messages[0].Content.(*mcp.TextContent).Text; !strings.Contains(text, `"gpu-02"`) || !strings.Contains(text, "Do not call power_action") {
		t.Fatalf("prompt = %s", text)
	}
}
