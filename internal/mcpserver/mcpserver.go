// Package mcpserver exposes kube-bmc to AI agents over the Model Context Protocol.
//
// Power actions are created as BMCAction objects with the caller's identity as requester
// and executed by the controller, exactly like requests from the dashboard.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/auth"
	"github.com/aireet/kube-bmc/internal/ipmi"
	"github.com/aireet/kube-bmc/internal/server"
)

// Options configures the MCP server.
type Options struct {
	Backend server.Backend
	// Actions is true when BMCActions are accepted.
	Actions bool
	Version string
}

type tools struct{ Options }

// New returns an MCP server with the kube-bmc tools and prompts registered.
func New(o Options) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "kube-bmc", Title: "kube-bmc", Version: o.Version}, &mcp.ServerOptions{
		Instructions: "kube-bmc manages the baseboard management controllers (BMCs) of the servers in a Kubernetes cluster. " +
			"Each BMC is named after its Kubernetes node. Start with fleet_summary or list_servers, then use get_server, " +
			"get_sensors and get_events to investigate a server. power_action changes the power state of a physical " +
			"server and interrupts every workload on it; only call it when the user explicitly asks for it.",
	})
	t := &tools{o}
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "fleet_summary",
		Description: "Summarize hardware health and power across all servers, including every server that reports problems.",
		Annotations: readOnly,
	}, t.fleetSummary)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_servers",
		Description: "List servers with health, power state, power draw, inlet temperature, model and BMC address. Filter by health or a search string.",
		Annotations: readOnly,
	}, t.listServers)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_server",
		Description: "Get the full BMC record of one server: inventory, firmware, management network, chassis state, problems and Kubernetes node details.",
		Annotations: readOnly,
		// The schema cannot be inferred from the Kubernetes API types (metav1.Time).
		OutputSchema: &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"server": {Type: "object"}}},
	}, t.getServer)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_sensors",
		Description: "Read live sensor values (temperatures, fans, voltages, power supplies) with thresholds from the server's node agent.",
		Annotations: readOnly,
	}, t.getSensors)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_events",
		Description: "Read the newest entries of the server's System Event Log (SEL), newest first.",
		Annotations: readOnly,
	}, t.getEvents)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_actions",
		Description: "List recent power actions (BMCAction objects), newest first, optionally for one server.",
		Annotations: readOnly,
	}, t.listActions)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_action",
		Description: "Get the status of a power action by name. Poll this after power_action until the phase is Succeeded, Failed or Rejected.",
		Annotations: readOnly,
	}, t.getAction)
	mcp.AddTool(s, &mcp.Tool{
		Name: "power_action",
		Description: "Request a power operation on a physical server through its BMC. This interrupts every workload on the node. " +
			"Only use it when the user explicitly asks. The request is recorded with the caller's identity and reason. " +
			"GracefulShutdown, ForceOff, ForceRestart and PowerCycle are executed by the node agent; On and GracefulRestart need out-of-band access.",
		Annotations: &mcp.ToolAnnotations{Title: "Power action", DestructiveHint: ptr(true), IdempotentHint: false, OpenWorldHint: ptr(true)},
	}, t.powerAction)

	mcp.AddTool(s, &mcp.Tool{
		Name: "locate_server",
		Description: "Turn the chassis identify light of a server on or off so that data-center staff can find it. " +
			"The light stays on until it is turned off. Executed by the node agent.",
		Annotations: &mcp.ToolAnnotations{Title: "Locate server", DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(true)},
	}, t.locateServer)
	mcp.AddTool(s, &mcp.Tool{
		Name: "clear_sel",
		Description: "Clear the System Event Log of a server after saving it to a ConfigMap. Use it when the log is full and new " +
			"hardware events are being dropped, and only when the user asks. Executed by the node agent.",
		Annotations: &mcp.ToolAnnotations{Title: "Clear the System Event Log", DestructiveHint: ptr(true), IdempotentHint: false, OpenWorldHint: ptr(true)},
	}, t.clearSEL)

	s.AddPrompt(&mcp.Prompt{
		Name:        "diagnose_server",
		Description: "Investigate the hardware state of one server and recommend next steps.",
		Arguments:   []*mcp.PromptArgument{{Name: "name", Description: "Server (node) name", Required: true}},
	}, diagnosePrompt)
	return s
}

func ptr[T any](v T) *T { return &v }

// identity returns the caller of a tool. Without authentication there is no token
// and the call is anonymous.
func identity(req *mcp.CallToolRequest) auth.Identity {
	if req != nil && req.Extra != nil && req.Extra.TokenInfo != nil {
		if id, ok := req.Extra.TokenInfo.Extra["identity"].(auth.Identity); ok {
			return id
		}
	}
	return auth.Anonymous
}

// ServerSummary is a compact view of one server.
type ServerSummary struct {
	Name             string           `json:"name"`
	Health           bmcv1.Health     `json:"health"`
	PowerState       bmcv1.PowerState `json:"powerState"`
	PowerWatts       *int32           `json:"powerWatts,omitempty"`
	InletTemperature *int32           `json:"inletTemperatureC,omitempty"`
	Manufacturer     string           `json:"manufacturer,omitempty"`
	Model            string           `json:"model,omitempty"`
	Serial           string           `json:"serial,omitempty"`
	BMCAddress       string           `json:"bmcAddress,omitempty"`
	NodeReady        *bool            `json:"nodeReady,omitempty"`
	Stale            bool             `json:"stale"`
	Problems         []bmcv1.Problem  `json:"problems,omitempty"`
}

func summarize(v server.View) ServerSummary {
	s := ServerSummary{
		Name: v.Name, Health: v.Status.Health, PowerState: v.Status.PowerState, PowerWatts: v.Status.PowerWatts,
		InletTemperature: v.Status.InletTemperature, Manufacturer: v.Status.Device.Manufacturer, Model: v.Status.Device.Product,
		Serial: v.Status.Device.SerialNumber, BMCAddress: v.Status.Network.IPAddress, Stale: v.Stale, Problems: v.Status.Problems,
	}
	if v.Node != nil {
		s.NodeReady = &v.Node.Ready
	}
	return s
}

type fleetIn struct{}

type FleetSummary struct {
	Servers        int             `json:"servers"`
	ByHealth       map[string]int  `json:"byHealth"`
	PoweredOn      int             `json:"poweredOn"`
	TotalWatts     int64           `json:"totalWatts"`
	StaleAgents    []string        `json:"staleAgents,omitempty"`
	NeedsAttention []ServerSummary `json:"needsAttention"`
}

func (t *tools) fleetSummary(ctx context.Context, req *mcp.CallToolRequest, _ fleetIn) (*mcp.CallToolResult, FleetSummary, error) {
	views, err := t.Backend.List(ctx)
	if err != nil {
		return nil, FleetSummary{}, err
	}
	out := FleetSummary{Servers: len(views), ByHealth: map[string]int{}, NeedsAttention: []ServerSummary{}}
	for _, v := range views {
		h := v.Status.Health
		if h == "" {
			h = bmcv1.HealthUnknown
		}
		out.ByHealth[string(h)]++
		if v.Status.PowerState == bmcv1.PowerOn {
			out.PoweredOn++
			if v.Status.PowerWatts != nil {
				out.TotalWatts += int64(*v.Status.PowerWatts)
			}
		}
		if v.Stale {
			out.StaleAgents = append(out.StaleAgents, v.Name)
		}
		if h == bmcv1.HealthWarning || h == bmcv1.HealthCritical {
			out.NeedsAttention = append(out.NeedsAttention, summarize(v))
		}
	}
	slices.SortStableFunc(out.NeedsAttention, func(a, b ServerSummary) int { return b.Health.Rank() - a.Health.Rank() })
	return nil, out, nil
}

type listIn struct {
	Health string `json:"health,omitempty" jsonschema:"only servers with this health: OK, Warning, Critical or Unknown"`
	Query  string `json:"query,omitempty" jsonschema:"case-insensitive match on name, model, manufacturer, serial or BMC address"`
}

type ServerList struct {
	Servers []ServerSummary `json:"servers"`
}

func (t *tools) listServers(ctx context.Context, req *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, ServerList, error) {
	views, err := t.Backend.List(ctx)
	if err != nil {
		return nil, ServerList{}, err
	}
	q := strings.ToLower(in.Query)
	out := ServerList{Servers: []ServerSummary{}}
	for _, v := range views {
		s := summarize(v)
		if in.Health != "" && !strings.EqualFold(string(s.Health), in.Health) {
			continue
		}
		if q != "" && !slices.ContainsFunc([]string{s.Name, s.Model, s.Manufacturer, s.Serial, s.BMCAddress},
			func(f string) bool { return strings.Contains(strings.ToLower(f), q) }) {
			continue
		}
		out.Servers = append(out.Servers, s)
	}
	return nil, out, nil
}

type nameIn struct {
	Name string `json:"name" jsonschema:"server (Kubernetes node) name"`
}

type ServerDetail struct {
	Server server.View `json:"server"`
}

func (t *tools) getServer(ctx context.Context, req *mcp.CallToolRequest, in nameIn) (*mcp.CallToolResult, ServerDetail, error) {
	v, err := t.Backend.Get(ctx, in.Name)
	return nil, ServerDetail{Server: v}, err
}

type sensorsIn struct {
	Name         string `json:"name" jsonschema:"server (Kubernetes node) name"`
	Type         string `json:"type,omitempty" jsonschema:"only sensors of this type: temperature, fan, voltage, power, current, utilization or discrete"`
	ProblemsOnly bool   `json:"problemsOnly,omitempty" jsonschema:"only sensors in warning or critical state"`
}

type SensorList struct {
	CollectedAt string        `json:"collectedAt"`
	Sensors     []ipmi.Sensor `json:"sensors"`
}

func (t *tools) getSensors(ctx context.Context, req *mcp.CallToolRequest, in sensorsIn) (*mcp.CallToolResult, SensorList, error) {
	snap, err := t.Backend.Live(ctx, in.Name)
	if err != nil {
		return nil, SensorList{}, err
	}
	out := SensorList{CollectedAt: snap.CollectedAt.UTC().Format("2006-01-02T15:04:05Z"), Sensors: []ipmi.Sensor{}}
	for _, s := range snap.Sensors {
		if in.Type != "" && s.Type != in.Type {
			continue
		}
		if in.ProblemsOnly && s.Severity != ipmi.SeverityWarning && s.Severity != ipmi.SeverityCritical {
			continue
		}
		if s.Severity == ipmi.SeverityNoReading && !in.ProblemsOnly && in.Type == "" {
			continue
		}
		out.Sensors = append(out.Sensors, s)
	}
	return nil, out, nil
}

type eventsIn struct {
	Name  string `json:"name" jsonschema:"server (Kubernetes node) name"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of events to return (default 50)"`
	Query string `json:"query,omitempty" jsonschema:"case-insensitive match on sensor, event or detail"`
}

type EventList struct {
	SELUsedPercent int          `json:"selUsedPercent"`
	SELEntries     int          `json:"selEntries"`
	Events         []ipmi.Event `json:"events"`
}

func (t *tools) getEvents(ctx context.Context, req *mcp.CallToolRequest, in eventsIn) (*mcp.CallToolResult, EventList, error) {
	snap, err := t.Backend.Live(ctx, in.Name)
	if err != nil {
		return nil, EventList{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	q := strings.ToLower(in.Query)
	out := EventList{SELUsedPercent: snap.SELInfo.UsedPercent, SELEntries: snap.SELInfo.Entries, Events: []ipmi.Event{}}
	for _, e := range snap.Events {
		if q != "" && !strings.Contains(strings.ToLower(e.Sensor+" "+e.Event+" "+e.Detail), q) {
			continue
		}
		if len(out.Events) == limit {
			break
		}
		out.Events = append(out.Events, e)
	}
	return nil, out, nil
}

// ActionSummary is a compact view of a BMCAction.
type ActionSummary struct {
	Name        string            `json:"name"`
	BMC         string            `json:"bmc"`
	Action      bmcv1.ActionType  `json:"action"`
	RequestedBy string            `json:"requestedBy"`
	Reason      string            `json:"reason,omitempty"`
	Phase       bmcv1.ActionPhase `json:"phase"`
	Message     string            `json:"message,omitempty"`
	Created     string            `json:"created"`
}

func summarizeAction(a *bmcv1.BMCAction) ActionSummary {
	phase := a.Status.Phase
	if phase == "" {
		phase = bmcv1.PhasePending
	}
	return ActionSummary{
		Name: a.Name, BMC: a.Spec.BMCName, Action: a.Spec.Action, RequestedBy: a.Spec.RequestedBy, Reason: a.Spec.Reason,
		Phase: phase, Message: a.Status.Message, Created: a.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

type listActionsIn struct {
	BMC string `json:"bmc,omitempty" jsonschema:"only actions for this server"`
}

type ActionList struct {
	Actions []ActionSummary `json:"actions"`
}

func (t *tools) listActions(ctx context.Context, req *mcp.CallToolRequest, in listActionsIn) (*mcp.CallToolResult, ActionList, error) {
	actions, err := t.Backend.ListActions(ctx, in.BMC)
	if err != nil {
		return nil, ActionList{}, err
	}
	out := ActionList{Actions: make([]ActionSummary, 0, len(actions))}
	for i := range actions {
		out.Actions = append(out.Actions, summarizeAction(&actions[i]))
	}
	return nil, out, nil
}

type getActionIn struct {
	Name string `json:"name" jsonschema:"BMCAction name returned by power_action"`
}

func (t *tools) getAction(ctx context.Context, req *mcp.CallToolRequest, in getActionIn) (*mcp.CallToolResult, ActionSummary, error) {
	a, err := t.Backend.GetAction(ctx, in.Name)
	if err != nil {
		return nil, ActionSummary{}, err
	}
	return nil, summarizeAction(a), nil
}

type powerIn struct {
	Name   string           `json:"name" jsonschema:"server (Kubernetes node) name"`
	Action bmcv1.ActionType `json:"action" jsonschema:"one of On, GracefulShutdown, GracefulRestart, ForceRestart, PowerCycle, ForceOff"`
	Reason string           `json:"reason" jsonschema:"why the action is needed; recorded for auditing"`
}

func (t *tools) powerAction(ctx context.Context, req *mcp.CallToolRequest, in powerIn) (*mcp.CallToolResult, ActionSummary, error) {
	id := identity(req)
	if !in.Action.IsPower() {
		return nil, ActionSummary{}, fmt.Errorf("unsupported power action %q", in.Action)
	}
	return t.request(ctx, id, in.Name, in.Action, in.Reason, true)
}

func (t *tools) request(ctx context.Context, id auth.Identity, name string, action bmcv1.ActionType, reason string, reasonRequired bool) (*mcp.CallToolResult, ActionSummary, error) {
	if !t.Actions {
		return nil, ActionSummary{}, errors.New("actions are disabled on this kube-bmc server")
	}
	if reasonRequired && strings.TrimSpace(reason) == "" {
		return nil, ActionSummary{}, errors.New("reason is required")
	}
	a, err := t.Backend.CreateAction(ctx, server.ActionRequest{
		BMC: name, Action: action, RequestedBy: id.Username, Reason: strings.TrimSpace("[mcp] " + reason),
	})
	if err != nil {
		return nil, ActionSummary{}, err
	}
	return nil, summarizeAction(a), nil
}

type locateIn struct {
	Name string `json:"name" jsonschema:"server (Kubernetes node) name"`
	On   bool   `json:"on" jsonschema:"true turns the identify light on, false turns it off"`
}

func (t *tools) locateServer(ctx context.Context, req *mcp.CallToolRequest, in locateIn) (*mcp.CallToolResult, ActionSummary, error) {
	action := bmcv1.ActionIdentifyOff
	if in.On {
		action = bmcv1.ActionIdentifyOn
	}
	return t.request(ctx, identity(req), in.Name, action, "", false)
}

type clearSELIn struct {
	Name   string `json:"name" jsonschema:"server (Kubernetes node) name"`
	Reason string `json:"reason" jsonschema:"why the log is cleared; recorded for auditing"`
}

func (t *tools) clearSEL(ctx context.Context, req *mcp.CallToolRequest, in clearSELIn) (*mcp.CallToolResult, ActionSummary, error) {
	return t.request(ctx, identity(req), in.Name, bmcv1.ActionClearSEL, in.Reason, true)
}

func diagnosePrompt(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	name := req.Params.Arguments["name"]
	if name == "" {
		return nil, errors.New("argument name is required")
	}
	text := fmt.Sprintf(`Diagnose the hardware of server %q with the kube-bmc tools.

1. Call get_server and summarize health, power state and every problem.
2. Call get_sensors with problemsOnly=true, then get_sensors for the types involved, and compare readings with their thresholds.
3. Call get_events and identify events that explain the problems, including when they started and whether they are still asserted.
4. If the System Event Log is nearly full, point out that new hardware events may be lost.
5. Conclude with the most likely cause and concrete next steps for the data-center team.

Do not call power_action.`, name)
	return &mcp.GetPromptResult{
		Description: "Hardware diagnosis for " + name,
		Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
	}, nil
}
