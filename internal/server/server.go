// Package server serves the kube-bmc dashboard, its JSON API and the MCP endpoint.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/auth"
	"github.com/aireet/kube-bmc/internal/collector"
)

// NodeInfo is the Kubernetes view of a server.
type NodeInfo struct {
	Ready          bool     `json:"ready"`
	Unschedulable  bool     `json:"unschedulable"`
	Roles          []string `json:"roles"`
	InternalIP     string   `json:"internalIP"`
	KubeletVersion string   `json:"kubeletVersion"`
	OSImage        string   `json:"osImage"`
	KernelVersion  string   `json:"kernelVersion"`
	CPU            string   `json:"cpu"`
	Memory         string   `json:"memory"`
	GPUs           int64    `json:"gpus"`
	GPUModel       string   `json:"gpuModel,omitempty"`
}

// AgentInfo describes the node agent pod of a server.
type AgentInfo struct {
	Pod   string `json:"pod"`
	IP    string `json:"ip"`
	Ready bool   `json:"ready"`
}

// View joins a BMC with its Node and agent.
type View struct {
	Name    string          `json:"name"`
	Created metav1.Time     `json:"created"`
	Spec    bmcv1.BMCSpec   `json:"spec"`
	Status  bmcv1.BMCStatus `json:"status"`
	Node    *NodeInfo       `json:"node,omitempty"`
	Agent   *AgentInfo      `json:"agent,omitempty"`
	// LastSeen is the last heartbeat of the node agent.
	LastSeen *metav1.Time `json:"lastSeen,omitempty"`
	// Stale is set when the agent heartbeat has expired.
	Stale bool `json:"stale"`
	// InBandPower is set when the node agent is running and can execute shutdown, restart
	// and power cycle through the local BMC interface.
	InBandPower bool `json:"inBandPower"`
	// OOBConfigured is set when out-of-band credentials are available for this BMC, which
	// power on requires.
	OOBConfigured bool `json:"oobConfigured"`
}

// ErrNotFound is returned by a Backend for unknown objects.
var ErrNotFound = errors.New("not found")

// Backend provides BMC data and accepts power action requests.
type Backend interface {
	List(ctx context.Context) ([]View, error)
	Get(ctx context.Context, name string) (View, error)
	Live(ctx context.Context, name string) (*collector.Snapshot, error)
	CreateAction(ctx context.Context, req ActionRequest) (*bmcv1.BMCAction, error)
	// ListActions returns actions newest first, optionally filtered by BMC name.
	ListActions(ctx context.Context, bmc string) ([]bmcv1.BMCAction, error)
	GetAction(ctx context.Context, name string) (*bmcv1.BMCAction, error)
}

// ActionRequest is a validated request to create a BMCAction.
type ActionRequest struct {
	BMC         string
	Action      bmcv1.ActionType
	RequestedBy string
	Reason      string
}

// Config is exposed to the dashboard.
type Config struct {
	Version string `json:"version"`
	// Actions is true when BMCActions (power, identify light, clearing the SEL) are accepted.
	Actions     bool   `json:"actions"`
	ClusterName string `json:"clusterName,omitempty"`
	// Auth is the browser sign-in mode: "none", "password" or "oidc".
	Auth string `json:"auth"`
}

// Authenticator establishes request identities and implements browser sign-in.
type Authenticator interface {
	// Middleware stores the caller identity in the request context or rejects the request.
	Middleware(http.Handler) http.Handler
	Login(http.ResponseWriter, *http.Request)
	Callback(http.ResponseWriter, *http.Request)
	Logout(http.ResponseWriter, *http.Request)
}

// Options configures a Server.
type Options struct {
	Backend Backend
	Config  Config
	UI      fs.FS
	Authn   Authenticator
	// MCP is mounted at /mcp when set.
	MCP http.Handler
	// ProtectedResourceMetadata is served at /.well-known/oauth-protected-resource when set.
	ProtectedResourceMetadata http.Handler
	Log                       *slog.Logger
}

type Server struct{ Options }

func New(o Options) *Server { return &Server{o} }

func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/me", s.me)
	api.HandleFunc("GET /api/v1/bmcs", s.list)
	api.HandleFunc("GET /api/v1/bmcs/{name}", s.get)
	api.HandleFunc("GET /api/v1/bmcs/{name}/live", s.live)
	api.HandleFunc("POST /api/v1/bmcs/{name}/actions", s.action)
	api.HandleFunc("POST /api/v1/bmcs/{name}/power", s.action) // deprecated path
	api.HandleFunc("GET /api/v1/actions", s.listActions)
	api.HandleFunc("GET /api/v1/actions/{name}", s.getAction)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /api/v1/config", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.Config) })
	mux.Handle("/api/", s.Authn.Middleware(api))
	mux.HandleFunc("GET /auth/login", s.Authn.Login)
	mux.HandleFunc("POST /auth/login", s.Authn.Login)
	mux.HandleFunc("GET /auth/callback", s.Authn.Callback)
	mux.HandleFunc("GET /auth/logout", s.Authn.Logout)
	if s.MCP != nil {
		mux.Handle("/mcp", s.MCP)
	}
	if s.ProtectedResourceMetadata != nil {
		mux.Handle("GET /.well-known/oauth-protected-resource", s.ProtectedResourceMetadata)
	}
	mux.Handle("/", spa(s.UI))
	return mux
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	writeJSON(w, http.StatusOK, id)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	views, err := s.Backend.List(r.Context())
	respond(w, views, err)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	v, err := s.Backend.Get(r.Context(), r.PathValue("name"))
	respond(w, v, err)
}

func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	snap, err := s.Backend.Live(r.Context(), r.PathValue("name"))
	respond(w, snap, err)
}

func (s *Server) listActions(w http.ResponseWriter, r *http.Request) {
	actions, err := s.Backend.ListActions(r.Context(), r.URL.Query().Get("bmc"))
	respond(w, actions, err)
}

func (s *Server) getAction(w http.ResponseWriter, r *http.Request) {
	a, err := s.Backend.GetAction(r.Context(), r.PathValue("name"))
	respond(w, a, err)
}

type actionRequest struct {
	Action bmcv1.ActionType `json:"action"`
	// Confirm must equal the BMC name for actions other than the identify light.
	Confirm string `json:"confirm"`
	Reason  string `json:"reason"`
}

// needsConfirmation reports whether an action interrupts the server or deletes data.
func needsConfirmation(a bmcv1.ActionType) bool {
	return a != bmcv1.ActionIdentifyOn && a != bmcv1.ActionIdentifyOff
}

func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.Config.Actions {
		writeError(w, http.StatusForbidden, "actions are disabled on this kube-bmc server")
		return
	}
	// Requiring a JSON content type prevents cross-site form submissions from using
	// the session cookie, because browsers preflight such requests.
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var req actionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !req.Action.Valid() {
		writeError(w, http.StatusBadRequest, "unsupported action "+string(req.Action))
		return
	}
	if needsConfirmation(req.Action) && req.Confirm != name {
		writeError(w, http.StatusBadRequest, "confirm must equal the BMC name")
		return
	}
	id, _ := auth.FromContext(r.Context())
	a, err := s.Backend.CreateAction(r.Context(), ActionRequest{BMC: name, Action: req.Action, RequestedBy: id.Username, Reason: req.Reason})
	if err != nil {
		writeBackendError(w, err)
		return
	}
	s.Log.Info("action requested", "bmc", name, "action", req.Action, "user", id.Username, "bmcaction", a.Name)
	writeJSON(w, http.StatusAccepted, a)
}

func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeBackendError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeBackendError(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	switch {
	case errors.Is(err, ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, context.DeadlineExceeded):
		code = http.StatusGatewayTimeout
	}
	writeError(w, code, err.Error())
}

// spa serves the embedded dashboard and falls back to index.html for client-side routes.
func spa(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if _, err := fs.Stat(ui, path); err == nil {
				if strings.HasPrefix(path, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, ui, "index.html")
	})
}
