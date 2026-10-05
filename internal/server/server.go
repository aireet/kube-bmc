// Package server serves the kube-bmc dashboard and its JSON API.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/collector"
	"github.com/aireet/kube-bmc/internal/oob"
)

// NodeInfo is the Kubernetes side of a server.
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

// AgentInfo is the node agent pod for a server.
type AgentInfo struct {
	Pod   string `json:"pod"`
	IP    string `json:"ip"`
	Ready bool   `json:"ready"`
}

// View joins a BMC with its Node and agent, which is what the dashboard needs per server.
type View struct {
	Name    string          `json:"name"`
	Created metav1.Time     `json:"created"`
	Spec    bmcv1.BMCSpec   `json:"spec"`
	Status  bmcv1.BMCStatus `json:"status"`
	Node    *NodeInfo       `json:"node,omitempty"`
	Agent   *AgentInfo      `json:"agent,omitempty"`
	// Stale is true when the agent has not reported for a while; the status may be outdated.
	Stale bool `json:"stale"`
	// OOBConfigured is true when credentials for out-of-band access are available.
	OOBConfigured bool `json:"oobConfigured"`
}

var ErrNotFound = errors.New("not found")

// Backend is where views come from: a live cluster or the built-in demo fleet.
type Backend interface {
	List(ctx context.Context) ([]View, error)
	Get(ctx context.Context, name string) (View, error)
	Live(ctx context.Context, name string) (*collector.Snapshot, error)
	PowerState(ctx context.Context, name string) (bmcv1.PowerState, error)
	Power(ctx context.Context, name string, a oob.Action, actor string) error
}

type Config struct {
	Version      string `json:"version"`
	PowerActions bool   `json:"powerActions"`
	Demo         bool   `json:"demo"`
	ClusterName  string `json:"clusterName,omitempty"`
}

type Server struct {
	backend Backend
	cfg     Config
	ui      fs.FS
	log     *slog.Logger
}

func New(b Backend, cfg Config, ui fs.FS, log *slog.Logger) *Server {
	return &Server{backend: b, cfg: cfg, ui: ui, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /api/v1/config", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, s.cfg) })
	mux.HandleFunc("GET /api/v1/bmcs", s.list)
	mux.HandleFunc("GET /api/v1/bmcs/{name}", s.get)
	mux.HandleFunc("GET /api/v1/bmcs/{name}/live", s.live)
	mux.HandleFunc("GET /api/v1/bmcs/{name}/power", s.powerState)
	mux.HandleFunc("POST /api/v1/bmcs/{name}/power", s.power)
	mux.Handle("/", spa(s.ui))
	return logRequests(s.log, mux)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	views, err := s.backend.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, views)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	v, err := s.backend.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, v)
}

func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	snap, err := s.backend.Live(r.Context(), r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, snap)
}

func (s *Server) powerState(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	st, err := s.backend.PowerState(ctx, r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"powerState": st})
}

type powerRequest struct {
	Action oob.Action `json:"action"`
	// Confirm must repeat the BMC name, so a stray click or a replayed request can't power off the wrong host.
	Confirm string `json:"confirm"`
}

func (s *Server) power(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.cfg.PowerActions {
		httpError(w, http.StatusForbidden, "power actions are disabled; start the server with --enable-power-actions")
		return
	}
	var req powerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !req.Action.Valid() {
		httpError(w, http.StatusBadRequest, "unknown action "+string(req.Action))
		return
	}
	if req.Confirm != name {
		httpError(w, http.StatusBadRequest, "confirm must equal the BMC name")
		return
	}
	actor := actorOf(r)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.backend.Power(ctx, name, req.Action, actor); err != nil {
		s.log.Error("power action failed", "bmc", name, "action", req.Action, "actor", actor, "err", err)
		writeErr(w, err)
		return
	}
	s.log.Info("power action executed", "bmc", name, "action", req.Action, "actor", actor)
	writeJSON(w, map[string]string{"result": "accepted"})
}

// actorOf identifies who triggered an action, trusting headers set by an auth proxy such as oauth2-proxy.
func actorOf(r *http.Request) string {
	for _, h := range []string{"X-Auth-Request-Email", "X-Auth-Request-User", "X-Forwarded-Email", "X-Forwarded-User", "X-Remote-User"} {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return "anonymous@" + host
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	switch {
	case errors.Is(err, ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, context.DeadlineExceeded):
		code = http.StatusGatewayTimeout
	}
	httpError(w, code, err.Error())
}

// spa serves the embedded UI and falls back to index.html for client-side routes.
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

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Debug("request", "method", r.Method, "path", r.URL.Path, "took", time.Since(start))
		}
	})
}
