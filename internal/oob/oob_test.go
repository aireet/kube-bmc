package oob

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
)

// redfishServer implements the subset of the Redfish API used by Power.
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

func TestRedfishPower(t *testing.T) {
	srv, resets := redfishServer(t)
	target := Target{Address: srv.URL, Protocol: bmcv1.ProtocolRedfish, Insecure: true, Creds: Credentials{"admin", "secret"}}

	if err := Power(context.Background(), target, bmcv1.ActionForceRestart); err != nil {
		t.Fatal(err)
	}
	if got := resets(); len(got) != 1 || got[0] != "ForceRestart" {
		t.Fatalf("resets = %v", got)
	}
	if err := Power(context.Background(), target, "Explode"); err == nil {
		t.Fatal("unsupported action accepted")
	}

	wrong := target
	wrong.Creds.Password = "wrong"
	if err := Power(context.Background(), wrong, bmcv1.ActionOn); err == nil {
		t.Fatal("wrong credentials accepted")
	}

	strict := target
	strict.Insecure = false
	if err := Power(context.Background(), strict, bmcv1.ActionOn); err == nil {
		t.Fatal("self-signed certificate accepted without Insecure")
	}
}

func TestTargetFor(t *testing.T) {
	b := &bmcv1.BMC{}
	b.Name = "n1"
	if _, err := TargetFor(b, Credentials{}); err == nil || !strings.Contains(err.Error(), "no address") {
		t.Fatalf("err = %v", err)
	}
	b.Status.Network.IPAddress = "10.0.0.5"
	if tg, _ := TargetFor(b, Credentials{}); tg.Address != "10.0.0.5" || tg.Protocol != bmcv1.ProtocolRedfish {
		t.Fatalf("target = %+v", tg)
	}
	b.Spec.Address, b.Spec.Protocol = "bmc-n1.example.com", bmcv1.ProtocolIPMI
	if tg, _ := TargetFor(b, Credentials{}); tg.Address != "bmc-n1.example.com" || tg.Protocol != bmcv1.ProtocolIPMI {
		t.Fatalf("target = %+v", tg)
	}
}

func TestIPMIRejectsGracefulRestart(t *testing.T) {
	err := Power(context.Background(), Target{Address: "127.0.0.1", Protocol: bmcv1.ProtocolIPMI}, bmcv1.ActionGracefulRestart)
	if err == nil || !strings.Contains(err.Error(), "not supported over IPMI") {
		t.Fatalf("err = %v", err)
	}
}
