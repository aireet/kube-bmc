package collector

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aireet/kube-bmc/internal/ipmi"
)

// selRunner serves SEL records whose description ipmitool leaves empty.
type selRunner struct{ gets int }

func (r *selRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	if args[0] == "-S" {
		args = args[2:]
	}
	if strings.Join(args[:2], " ") == "sel get" {
		r.gets++
		return []byte(" Sensor Type : Session Audit\n Event Type : Sensor-specific Discrete\n Event Data : f2ffff\n"), nil
	}
	return nil, nil
}

func TestDescribeFillsEmptySELEvents(t *testing.T) {
	r := &selRunner{}
	c := New("n", ipmi.NewClient(r, ""), Options{SELEntries: 100}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	events := []ipmi.Event{
		{ID: "237c", Timestamp: "t2", Sensor: "Session Audit #0xff"},
		{ID: "237b", Timestamp: "t1", Sensor: "Temperature Inlet_Temp", Event: "Upper Non-critical going high"},
	}
	c.describe(context.Background(), events)
	if events[0].Event != "Invalid username or password" || events[1].Event != "Upper Non-critical going high" {
		t.Fatalf("events = %+v", events)
	}

	again := []ipmi.Event{{ID: "237c", Timestamp: "t2", Sensor: "Session Audit #0xff"}}
	c.describe(context.Background(), again)
	if r.gets != 1 || again[0].Event != "Invalid username or password" {
		t.Fatalf("cached record fetched again (%d gets): %+v", r.gets, again)
	}
}
