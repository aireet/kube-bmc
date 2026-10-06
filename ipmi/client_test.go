package ipmi_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aireet/kube-bmc/ipmi"
	"github.com/aireet/kube-bmc/ipmi/ipmitest"
)

// The recorded server is a Gooxi SY8108G-G4 (AMI MegaRAC) with five failed fans.
func recorded() (*ipmi.Client, *ipmitest.Runner) {
	r := ipmitest.Recorded()
	return ipmi.New(r), r
}

func TestController(t *testing.T) {
	c, _ := recorded()
	got, err := c.Controller(t.Context())
	want := ipmi.Controller{FirmwareVersion: "2.18", IPMIVersion: "2.0", ManufacturerID: "0", ManufacturerName: "Unknown", ProductID: "514 (0x0202)"}
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
	if guid, err := c.GUID(t.Context()); err != nil || guid != "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0" {
		t.Fatalf("guid %q, %v", guid, err)
	}
}

func TestLAN(t *testing.T) {
	c, _ := recorded()
	got, err := c.LAN(t.Context())
	want := ipmi.LAN{Channel: 1, IPAddress: "10.20.0.31", Netmask: "255.255.255.0", Gateway: "10.20.0.1", MACAddress: "aa:bb:cc:00:11:22", Source: "Static Address", VLAN: "Disabled"}
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// partialRunner behaves like a Supermicro BMC: `lan print` prints the configuration
// without the gateway and exits 1; the gateway is available as a raw parameter.
type partialRunner struct{}

func (partialRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	if args[0] == "raw" {
		return []byte(" 11 0a 1e 07 fe\n"), nil
	}
	if args[2] != "1" {
		return nil, errors.New("Invalid channel: " + args[2])
	}
	b, _ := os.ReadFile("testdata/lan_print_partial.txt")
	return b, errors.New("exit status 1")
}

func TestLANUsesPartialOutput(t *testing.T) {
	lan, err := ipmi.New(partialRunner{}).LAN(t.Context())
	if err != nil || lan.IPAddress != "10.30.4.27" || lan.Channel != 1 || lan.MACAddress != "aa:bb:cc:00:22:33" ||
		lan.Gateway != "10.30.7.254" {
		t.Fatalf("lan = %+v, err = %v", lan, err)
	}
}

func TestFRU(t *testing.T) {
	c, _ := recorded()
	got, err := c.FRU(t.Context())
	if err != nil || got.Manufacturer != "Gooxi" || got.Product != "SY8108G-G4" || got.SerialNumber != "SRV0000000000000000001" ||
		got.BoardProduct != "G4DCT_PCBA" || got.ChassisType != "Rack Mount Chassis" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestChassis(t *testing.T) {
	c, _ := recorded()
	got, err := c.Chassis(t.Context())
	if err != nil || !got.PowerOn || got.PowerRestorePolicy != "always-on" || got.IntrusionActive ||
		!slices.Equal(got.Faults, []string{"Cooling/Fan Fault"}) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestPowerReading(t *testing.T) {
	c, r := recorded()
	if w, err := c.PowerReading(t.Context()); err != nil || w != 1215 {
		t.Fatalf("got %d, %v", w, err)
	}
	r.Set("dcmi power reading", []byte("DCMI request failed because: Invalid command (c1)\n"))
	if _, err := c.PowerReading(t.Context()); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestSensors(t *testing.T) {
	c, _ := recorded()
	sensors, err := c.Sensors(t.Context())
	if err != nil || len(sensors) != 219 {
		t.Fatalf("got %d sensors, %v", len(sensors), err)
	}
	count := map[ipmi.Severity]int{}
	byName := map[string]ipmi.Sensor{}
	for _, s := range sensors {
		count[s.Severity]++
		byName[s.Name] = s
	}
	// Five fans below lnr; warnings are Inlet_Temp (unc) and SEL_Status "Log full" (discrete).
	if count[ipmi.SeverityCritical] != 5 || count[ipmi.SeverityWarning] != 2 || count[ipmi.SeverityNoReading] != 39 {
		t.Fatalf("severity counts = %v", count)
	}
	if s := byName["Inlet_Temp"]; s.Type != ipmi.Temperature || *s.Value != 44 || s.Unit != "degrees C" || s.Severity != ipmi.SeverityWarning {
		t.Fatalf("inlet = %+v", s)
	}
	if s := byName["FAN7"]; s.Type != ipmi.Fan || s.Status != "lnr" || *s.Value != 0 {
		t.Fatalf("fan = %+v", s)
	}
	if s := byName["PSU1_Status"]; s.Type != ipmi.Discrete || s.Value != nil || s.Reading != "Presence detected" {
		t.Fatalf("psu = %+v", s)
	}
	if s := byName["OCP1_Temp"]; s.Value != nil || s.Severity != ipmi.SeverityNoReading {
		t.Fatalf("ocp = %+v", s)
	}
}

func TestThresholds(t *testing.T) {
	c, _ := recorded()
	th, err := c.Thresholds(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	inlet, ok := th["Inlet_Temp"]
	if !ok || *inlet.UpperNonCritical != 40 || *inlet.UpperCritical != 50 || *inlet.UpperNonRecoverable != 65 || inlet.LowerNonRecoverable == nil {
		t.Fatalf("inlet = %+v", inlet)
	}
	if _, ok := th["R_Outlet_Temp"]; ok {
		t.Fatal("a sensor without thresholds is listed")
	}
	if _, ok := th["PSU1_Status"]; ok {
		t.Fatal("a discrete sensor is listed")
	}
}

func TestEvents(t *testing.T) {
	c, r := recorded()
	info, err := c.SELInfo(t.Context())
	if err != nil || info.Entries != 3639 || info.UsedPercent != 100 || info.LastAddTime != "10/05/2026 18:05:51" {
		t.Fatalf("info = %+v, %v", info, err)
	}
	events, err := c.Events(t.Context(), 30)
	if err != nil || len(events) != 30 {
		t.Fatalf("got %d events, %v", len(events), err)
	}
	e := events[0]
	if e.ID != "825d" || e.Timestamp != "10/04/2026 12:28:07" || e.Sensor != "Temperature Inlet_Temp" ||
		e.Event != "Upper Non-critical going high" || e.Asserted || e.Detail != "Reading 39 > Threshold 40 degrees C" {
		t.Fatalf("event = %+v", e)
	}
	if !events[1].Asserted {
		t.Fatal("second event should be asserted")
	}
	rec, err := c.EventRecord(t.Context(), "237c")
	if err != nil || rec.Describe() != "Invalid username or password" {
		t.Fatalf("record %+v, %v", rec, err)
	}
	if calls := r.Calls(); !slices.Contains(calls, "sel elist last 30") || !slices.Contains(calls, "sel get 0x237c") {
		t.Fatalf("calls = %v", calls)
	}
}

func TestStateChangingCommands(t *testing.T) {
	c, r := recorded()
	ctx := t.Context()
	for _, err := range []error{
		c.SetPower(ctx, ipmi.PowerCycle),
		c.SetPower(ctx, ipmi.PowerSoftOff),
		c.Identify(ctx, ipmi.IdentifyIndefinitely),
		c.Identify(ctx, 10*time.Minute),
		c.Identify(ctx, 0),
		c.ClearSEL(ctx),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"chassis power cycle", "chassis power soft", "chassis identify force", "chassis identify 255", "chassis identify 0", "sel clear"}
	if got := r.Calls(); !slices.Equal(got, want) {
		t.Fatalf("calls = %v", got)
	}
	if err := c.SetPower(ctx, "halt"); err == nil {
		t.Fatal("unknown power action accepted")
	}
}

func TestCacheSDR(t *testing.T) {
	c, r := recorded()
	if err := c.CacheSDR(t.Context(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sensors(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.Fail("sdr dump", errors.New("sdr dump failed"))
	if err := c.CacheSDR(t.Context(), t.TempDir()); err == nil {
		t.Fatal("expected the failed dump to be reported")
	}
}

// The agent's collector refreshes the cache while its action controller reads the SEL
// through the same Client. Run with -race.
func TestConcurrentUse(t *testing.T) {
	c, _ := recorded()
	dir := t.TempDir()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				_ = c.CacheSDR(t.Context(), dir)
			}
		})
		wg.Go(func() {
			for range 50 {
				_, _ = c.ExportSEL(t.Context())
				_, _ = c.Sensors(t.Context())
			}
		})
	}
	wg.Wait()
}
