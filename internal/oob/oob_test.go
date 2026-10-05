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

// fakeRedfish is the smallest Redfish service gofish can talk to: one system with a Reset action.
func fakeRedfish(t *testing.T) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var resets []string
	power := "On"
	mux := http.NewServeMux()
	j := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/redfish/v1/", func(w http.ResponseWriter, r *http.Request) {
		j(w, map[string]any{"@odata.id": "/redfish/v1/", "Systems": map[string]string{"@odata.id": "/redfish/v1/Systems"}})
	})
	mux.HandleFunc("/redfish/v1/Systems", func(w http.ResponseWriter, r *http.Request) {
		j(w, map[string]any{"Members": []map[string]string{{"@odata.id": "/redfish/v1/Systems/1"}}})
	})
	mux.HandleFunc("/redfish/v1/Systems/1", func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "admin" || p != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		j(w, map[string]any{"@odata.id": "/redfish/v1/Systems/1", "Id": "1", "PowerState": power,
			"Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{
				"target":                            "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset",
				"ResetType@Redfish.AllowableValues": []string{"On", "ForceOff", "GracefulShutdown", "GracefulRestart", "ForceRestart", "PowerCycle"},
			}}})
	})
	mux.HandleFunc("POST /redfish/v1/Systems/1/Actions/ComputerSystem.Reset", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ ResetType string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		resets = append(resets, body.ResetType)
		if body.ResetType == "ForceOff" {
			power = "Off"
		}
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv, &resets
}

func TestRedfishPower(t *testing.T) {
	srv, resets := fakeRedfish(t)
	tgt := Target{Address: srv.URL, Protocol: bmcv1.ProtocolRedfish, Insecure: true, Creds: Credentials{"admin", "secret"}}
	ctx := context.Background()

	if st, err := PowerState(ctx, tgt); err != nil || st != bmcv1.PowerOn {
		t.Fatalf("state: %v %v", st, err)
	}
	if err := Power(ctx, tgt, ForceOff); err != nil {
		t.Fatal(err)
	}
	if st, _ := PowerState(ctx, tgt); st != bmcv1.PowerOff {
		t.Fatalf("state after ForceOff: %v", st)
	}
	if len(*resets) != 1 || (*resets)[0] != "ForceOff" {
		t.Fatalf("resets = %v", *resets)
	}
	if err := Power(ctx, tgt, "SelfDestruct"); err == nil {
		t.Fatal("invalid action accepted")
	}
	bad := tgt
	bad.Creds.Password = "wrong"
	if _, err := PowerState(ctx, bad); err == nil {
		t.Fatal("bad credentials accepted")
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
		t.Fatalf("override = %+v", tg)
	}
}

func TestIPMIHasNoGracefulRestart(t *testing.T) {
	err := Power(context.Background(), Target{Address: "127.0.0.1", Protocol: bmcv1.ProtocolIPMI}, GracefulRestart)
	if err == nil || !strings.Contains(err.Error(), "not supported over IPMI") {
		t.Fatalf("err = %v", err)
	}
}
