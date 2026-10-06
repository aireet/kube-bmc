package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aireet/kube-bmc/internal/hostinv"
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

// fixtureRunner serves the real ipmitool fixtures, or fails every command while down is set.
type fixtureRunner struct {
	t    *testing.T
	down atomic.Bool
}

func (r *fixtureRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	if r.down.Load() {
		return nil, errors.New("ipmitool: Unable to send command: Device or resource busy")
	}
	if args[0] == "-S" {
		args = args[2:]
	}
	files := map[string]string{
		"mc info": "mc_info.txt", "mc guid": "mc_guid.txt", "fru print": "fru_print.txt",
		"dcmi power": "dcmi_power_reading.txt", "lan print": "lan_print.txt", "chassis status": "chassis_status.txt",
		"sdr elist": "sdr_elist.txt", "sel info": "sel_info.txt",
	}
	if f, ok := files[strings.Join(args[:min(2, len(args))], " ")]; ok {
		return read(r.t, f), nil
	}
	return nil, nil
}

// A failed phase must not overwrite data read earlier; the failure shows in Errors only.
func TestFailedPhasesKeepLastData(t *testing.T) {
	r := &fixtureRunner{t: t}
	hw := hostinv.Inventory{CPU: hostinv.CPU{Model: "Xeon", Sockets: 2}, GPUs: []hostinv.PCIDevice{{Address: "0000:01:00.0"}}}
	hwErr := error(nil)
	c := New("n", ipmi.NewClient(r, ""), Options{
		Hardware:       func(context.Context) (hostinv.Inventory, error) { return hw, hwErr },
		CommandTimeout: time.Second, SELTimeout: time.Second, SELEntries: 10, SELMinInterval: time.Hour,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.inventory(t.Context())
	c.fast(t.Context())
	before := c.Snapshot()
	if before.MC.FirmwareVersion == "" || before.GUID == "" || before.FRU == (ipmi.FRU{}) || before.PowerWatts <= 0 ||
		len(before.Sensors) == 0 || len(before.Errors) != 0 {
		t.Fatalf("first round incomplete: %+v", before)
	}

	r.down.Store(true)
	hw, hwErr = hostinv.Inventory{GPUs: hw.GPUs}, errors.New("dmidecode: exit status 1")
	c.inventory(t.Context())
	c.fast(t.Context())
	after := c.Snapshot()
	if after.MC != before.MC || after.GUID != before.GUID || after.FRU != before.FRU || after.LAN != before.LAN ||
		after.PowerWatts != before.PowerWatts || after.Chassis.PowerOn != before.Chassis.PowerOn ||
		len(after.Sensors) != len(before.Sensors) || after.SELInfo != before.SELInfo {
		t.Fatalf("data lost after failures:\nbefore %+v\nafter  %+v", before, after)
	}
	if after.Hardware == nil || after.Hardware.CPU.Model != "Xeon" || len(after.Hardware.GPUs) != 1 {
		t.Fatalf("hardware = %+v", after.Hardware)
	}
	for _, phase := range []string{"mc", "fru", "lan", "power", "chassis", "sensors", "sel-info", "hardware"} {
		if after.Errors[phase] == "" {
			t.Errorf("no error recorded for %s", phase)
		}
	}
}
