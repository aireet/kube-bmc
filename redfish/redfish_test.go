package redfish_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aireet/kube-bmc/redfish"
)

// redfishServer implements the subset of the Redfish API the client uses. It accepts
// admin/secret and records reset requests.
func redfishServer(t *testing.T) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var resets []string
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	authorized := func(r *http.Request) bool {
		u, p, ok := r.BasicAuth()
		return ok && u == "admin" && p == "secret"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/redfish/v1/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"@odata.id": "/redfish/v1/", "Systems": map[string]string{"@odata.id": "/redfish/v1/Systems"}})
	})
	mux.HandleFunc("/redfish/v1/Systems", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"Members": []map[string]string{{"@odata.id": "/redfish/v1/Systems/1"}}})
	})
	mux.HandleFunc("/redfish/v1/Systems/1", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"@odata.id": "/redfish/v1/Systems/1", "Id": "1", "PowerState": "On",
			"Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{
				"target": "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset",
			}}})
	})
	mux.HandleFunc("POST /redfish/v1/Systems/1/Actions/ComputerSystem.Reset", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ ResetType string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		resets = append(resets, body.ResetType)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), resets...)
	}
}

func TestReset(t *testing.T) {
	srv, resets := redfishServer(t)
	cfg := redfish.Config{Endpoint: srv.URL, Username: "admin", Password: "secret", Insecure: true}
	c, err := redfish.Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Reset(t.Context(), redfish.ForceRestart); err != nil {
		t.Fatal(err)
	}
	if got := resets(); len(got) != 1 || got[0] != "ForceRestart" {
		t.Fatalf("resets = %v", got)
	}
	if state, err := c.PowerState(t.Context()); err != nil || state != "On" {
		t.Fatalf("power state %q, %v", state, err)
	}
}

func TestDialFailures(t *testing.T) {
	srv, _ := redfishServer(t)
	for name, cfg := range map[string]redfish.Config{
		"wrong password":     {Endpoint: srv.URL, Username: "admin", Password: "wrong", Insecure: true},
		"untrusted TLS cert": {Endpoint: srv.URL, Username: "admin", Password: "secret"},
	} {
		c, err := redfish.Dial(context.Background(), cfg)
		if err == nil {
			err = c.Reset(t.Context(), redfish.On)
			_ = c.Close()
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
