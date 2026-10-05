package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testServer(powerActions bool) http.Handler {
	ui := fstest.MapFS{
		"index.html":      {Data: []byte("<html>kube-bmc</html>")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(NewDemo(), Config{Version: "test", PowerActions: powerActions}, ui, log).Handler()
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestListAndGet(t *testing.T) {
	h := testServer(false)
	rec := do(t, h, "GET", "/api/v1/bmcs", "")
	var views []View
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil || len(views) != len(demoFleet) {
		t.Fatalf("list: %d %v (%d views)", rec.Code, err, len(views))
	}
	if rec := do(t, h, "GET", "/api/v1/bmcs/gpu-h200-02", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"health":"Critical"`) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", "/api/v1/bmcs/nope", ""); rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/api/v1/bmcs/cpu-02/live", ""); rec.Code != 502 {
		t.Fatalf("live without agent: %d", rec.Code)
	}
}

func TestPowerIsGuarded(t *testing.T) {
	if rec := do(t, testServer(false), "POST", "/api/v1/bmcs/cp-01/power", `{"action":"ForceOff","confirm":"cp-01"}`); rec.Code != 403 {
		t.Fatalf("disabled: %d", rec.Code)
	}
	h := testServer(true)
	for body, want := range map[string]int{
		`{"action":"ForceOff","confirm":"cp-02"}`: 400, // wrong confirmation
		`{"action":"Explode","confirm":"cp-01"}`:  400,
		`not json`:                                400,
		`{"action":"ForceOff","confirm":"cp-01"}`: 200,
	} {
		if rec := do(t, h, "POST", "/api/v1/bmcs/cp-01/power", body); rec.Code != want {
			t.Errorf("%s: got %d, want %d (%s)", body, rec.Code, want, rec.Body)
		}
	}
	if rec := do(t, h, "GET", "/api/v1/bmcs/cp-01/power", ""); !strings.Contains(rec.Body.String(), `"Off"`) {
		t.Fatalf("power state after ForceOff: %s", rec.Body)
	}
}

func TestSPA(t *testing.T) {
	h := testServer(false)
	if rec := do(t, h, "GET", "/nodes/gpu-01", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "kube-bmc") {
		t.Fatalf("client route: %d", rec.Code)
	}
	rec := do(t, h, "GET", "/assets/app-1.js", "")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %v", rec.Code, rec.Header())
	}
}

func TestActorFromAuthProxy(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("X-Auth-Request-Email", "ops@example.com")
	if got := actorOf(r); got != "ops@example.com" {
		t.Fatal(got)
	}
	if got := actorOf(httptest.NewRequest("POST", "/", nil)); got != "anonymous@192.0.2.1" {
		t.Fatal(got)
	}
}
