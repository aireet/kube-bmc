package ipmi

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
)

// Fixtures are real ipmitool 1.8.18 output from a Gooxi SY8108G-G4 (AMI MegaRAC, AST2600),
// with serial numbers and addresses anonymized.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseMCInfo(t *testing.T) {
	got := ParseMCInfo(fixture(t, "mc_info.txt"))
	want := MCInfo{FirmwareVersion: "2.18", IPMIVersion: "2.0", ManufacturerID: "0", ManufacturerName: "Unknown", ProductID: "514 (0x0202)"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseGUID(t *testing.T) {
	if got := ParseGUID(fixture(t, "mc_guid.txt")); got != "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0" {
		t.Fatalf("got %q", got)
	}
}

func TestParseLAN(t *testing.T) {
	got := ParseLAN(fixture(t, "lan_print.txt"))
	want := LAN{IPAddress: "10.20.0.31", Netmask: "255.255.255.0", Gateway: "10.20.0.1", MACAddress: "aa:bb:cc:00:11:22", Source: "Static Address", VLAN: "Disabled"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Supermicro prints the LAN configuration and then fails on an unsupported parameter.
func TestLANUsesPartialOutput(t *testing.T) {
	c := NewClient(partialRunner{}, "")
	lan, err := c.LAN(t.Context())
	if err != nil || lan.IPAddress != "10.30.4.27" || lan.Channel != 1 || lan.MACAddress != "aa:bb:cc:00:22:33" {
		t.Fatalf("lan = %+v, err = %v", lan, err)
	}
}

type partialRunner struct{}

func (partialRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	if args[2] != "1" {
		return nil, errors.New("Invalid channel: " + args[2])
	}
	b, _ := os.ReadFile("testdata/lan_print_partial.txt")
	return b, errors.New("exit status 1")
}

func TestParseFRU(t *testing.T) {
	got := ParseFRU(fixture(t, "fru_print.txt"))
	if got.Manufacturer != "Gooxi" || got.Product != "SY8108G-G4" || got.SerialNumber != "SRV0000000000000000001" ||
		got.BoardProduct != "G4DCT_PCBA" || got.ChassisType != "Rack Mount Chassis" {
		t.Fatalf("unexpected FRU: %+v", got)
	}
}

func TestParseFRUFallsBackToBoard(t *testing.T) {
	got := ParseFRU([]byte(" Board Mfg             : ACME\n Board Product         : X1\n Board Serial          : B1\n"))
	if got.Manufacturer != "ACME" || got.Product != "X1" || got.SerialNumber != "B1" {
		t.Fatalf("unexpected FRU: %+v", got)
	}
}

func TestParseChassis(t *testing.T) {
	got := ParseChassis(fixture(t, "chassis_status.txt"))
	if !got.PowerOn || got.PowerRestorePolicy != "always-on" || got.IntrusionActive {
		t.Fatalf("unexpected chassis: %+v", got)
	}
	if !slices.Equal(got.Faults, []string{"Cooling/Fan Fault"}) {
		t.Fatalf("faults = %v", got.Faults)
	}
}

func TestParseSELInfo(t *testing.T) {
	got := ParseSELInfo(fixture(t, "sel_info.txt"))
	if got.Entries != 3639 || got.UsedPercent != 100 || got.LastAddTime != "10/05/2026 18:05:51" {
		t.Fatalf("unexpected sel info: %+v", got)
	}
}

func TestParseDCMIPower(t *testing.T) {
	if got := ParseDCMIPower(fixture(t, "dcmi_power_reading.txt")); got != 1215 {
		t.Fatalf("got %d", got)
	}
	if got := ParseDCMIPower([]byte("DCMI request failed")); got != -1 {
		t.Fatalf("got %d", got)
	}
}

func TestParseSDR(t *testing.T) {
	sensors := ParseSDR(fixture(t, "sdr_elist.txt"))
	if len(sensors) != 219 {
		t.Fatalf("got %d sensors", len(sensors))
	}
	count := map[Severity]int{}
	byName := map[string]Sensor{}
	for _, s := range sensors {
		count[s.Severity]++
		byName[s.Name] = s
	}
	// 5 fans at lnr; warnings: Inlet_Temp (unc) and SEL_Status "Log full" (discrete).
	if count[SeverityCritical] != 5 || count[SeverityWarning] != 2 || count[SeverityNoReading] != 39 {
		t.Fatalf("severity counts = %v", count)
	}

	inlet := byName["Inlet_Temp"]
	if inlet.Type != "temperature" || *inlet.Value != 44 || inlet.Unit != "degrees C" || inlet.Severity != SeverityWarning {
		t.Fatalf("inlet = %+v", inlet)
	}
	if fan := byName["FAN7"]; fan.Type != "fan" || fan.Status != "lnr" || *fan.Value != 0 {
		t.Fatalf("fan = %+v", fan)
	}
	if psu := byName["PSU1_Status"]; psu.Type != "discrete" || psu.Value != nil || psu.Reading != "Presence detected" {
		t.Fatalf("psu = %+v", psu)
	}
	if ocp := byName["OCP1_Temp"]; ocp.Value != nil || ocp.Severity != SeverityNoReading {
		t.Fatalf("ocp = %+v", ocp)
	}
}

func TestParseThresholds(t *testing.T) {
	th := ParseThresholds(fixture(t, "sensor.txt"))
	inlet, ok := th["Inlet_Temp"]
	if !ok || *inlet.UpperNonCritical != 40 || *inlet.UpperCritical != 50 || *inlet.UpperNonRecoverable != 65 || inlet.LowerNonRecoverable == nil {
		t.Fatalf("inlet = %+v", inlet)
	}
	if _, ok := th["R_Outlet_Temp"]; ok {
		t.Fatal("sensor without thresholds should be omitted")
	}
	if _, ok := th["PSU1_Status"]; ok {
		t.Fatal("discrete sensors have no thresholds")
	}
}

func TestParseSEL(t *testing.T) {
	events := ParseSEL(fixture(t, "sel_elist.txt"))
	if len(events) != 30 {
		t.Fatalf("got %d events", len(events))
	}
	e := events[0]
	if e.ID != "825d" || e.Timestamp != "10/04/2026 12:28:07" || e.Sensor != "Temperature Inlet_Temp" ||
		e.Event != "Upper Non-critical going high" || e.Asserted || e.Detail != "Reading 39 > Threshold 40 degrees C" {
		t.Fatalf("event = %+v", e)
	}
	if !events[1].Asserted {
		t.Fatal("second event should be asserted")
	}
}

func TestDiscreteSeverity(t *testing.T) {
	for reading, want := range map[string]Severity{
		"Presence detected":          SeverityOK,
		"Fully Redundant":            SeverityOK,
		"":                           SeverityOK,
		"Log full":                   SeverityWarning,
		"Redundancy Lost":            SeverityWarning,
		"Predictive failure":         SeverityWarning,
		"Power Supply AC lost":       SeverityCritical,
		"Failure detected":           SeverityCritical,
		"Uncorrectable ECC":          SeverityCritical,
		"Presence detected, AC lost": SeverityCritical,
	} {
		if got := DiscreteSeverity(reading); got != want {
			t.Errorf("%q: got %s, want %s", reading, got, want)
		}
	}
}
