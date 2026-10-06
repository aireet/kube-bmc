package ipmi

import "testing"

func TestFRUFallsBackToBoard(t *testing.T) {
	got := parseFRU([]byte(" Board Mfg             : ACME\n Board Product         : X1\n Board Serial          : B1\n"))
	if got.Manufacturer != "ACME" || got.Product != "X1" || got.SerialNumber != "B1" {
		t.Fatalf("unexpected FRU: %+v", got)
	}
}

func TestPowerReadingUnavailable(t *testing.T) {
	if _, ok := parsePowerReading([]byte("DCMI request failed")); ok {
		t.Fatal("reading parsed from an error message")
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
		if got := discreteSeverity(reading); got != want {
			t.Errorf("%q: got %s, want %s", reading, got, want)
		}
	}
}

func TestDescribeOnlySensorSpecificEvents(t *testing.T) {
	if got := (EventRecord{SensorType: "Session Audit", EventType: "Threshold", EventData: []byte{2}}).Describe(); got != "" {
		t.Fatalf("threshold event described as %q", got)
	}
	if got := (EventRecord{SensorType: "Unknown", EventType: "Sensor-specific Discrete", EventData: []byte{2}}).Describe(); got != "" {
		t.Fatalf("unknown sensor type described as %q", got)
	}
	if _, err := parseEventRecord([]byte("SEL Record ID : 1\n")); err == nil {
		t.Fatal("record without event data accepted")
	}
}

func TestParseRawIPv4(t *testing.T) {
	if ip, err := parseRawIPv4([]byte(" 11 0a d4 b3 fe\n")); err != nil || ip != "10.212.179.254" {
		t.Fatalf("ip = %q, err = %v", ip, err)
	}
	for _, bad := range []string{"", " 11 0a", " 11 zz 00 00 00"} {
		if _, err := parseRawIPv4([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
