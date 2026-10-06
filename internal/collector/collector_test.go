package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aireet/kube-bmc/hardware"
	"github.com/aireet/kube-bmc/ipmi"
	"github.com/aireet/kube-bmc/ipmi/ipmitest"
)

func TestDescribeFillsEmptySELEvents(t *testing.T) {
	r := ipmitest.Recorded()
	c := New("n", ipmi.New(r), Options{SELEntries: 100}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	if gets := slices.DeleteFunc(r.Calls(), func(c string) bool { return !strings.HasPrefix(c, "sel get") }); len(gets) != 1 ||
		again[0].Event != "Invalid username or password" {
		t.Fatalf("cached record fetched again (%v): %+v", gets, again)
	}
}

// A failed phase must not overwrite data read earlier; the failure shows in Errors only.
func TestFailedPhasesKeepLastData(t *testing.T) {
	r := ipmitest.Recorded()
	hw := hardware.Inventory{CPU: hardware.CPU{Model: "Xeon", Sockets: 2}, GPUs: []hardware.PCIDevice{{Address: "0000:01:00.0"}}}
	hwErr := error(nil)
	c := New("n", ipmi.New(r), Options{
		Hardware:       func(context.Context) (hardware.Inventory, error) { return hw, hwErr },
		CommandTimeout: time.Second, SELTimeout: time.Second, SELEntries: 10, SELMinInterval: time.Hour,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.inventory(t.Context())
	c.readings(t.Context())
	before := c.Snapshot()
	if before.MC.FirmwareVersion == "" || before.GUID == "" || before.FRU == (ipmi.FRU{}) || before.PowerWatts <= 0 ||
		len(before.Sensors) == 0 || len(before.Errors) != 0 {
		t.Fatalf("first round incomplete: %+v", before)
	}

	for _, cmd := range []string{"mc", "lan", "fru", "chassis", "dcmi", "sdr", "sensor", "sel"} {
		r.Fail(cmd, errors.New("ipmitool: Unable to send command: Device or resource busy"))
	}
	hw, hwErr = hardware.Inventory{GPUs: hw.GPUs}, errors.New("dmidecode: exit status 1")
	c.inventory(t.Context())
	c.readings(t.Context())
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
