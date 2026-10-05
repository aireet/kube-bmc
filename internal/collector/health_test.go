package collector

import (
	"os"
	"testing"
	"time"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/ipmi"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../ipmi/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// realSnapshot is built from the anonymized output of a real 8-GPU server.
func realSnapshot(t *testing.T) *Snapshot {
	return &Snapshot{
		MC:         ipmi.ParseMCInfo(read(t, "mc_info.txt")),
		LAN:        ipmi.ParseLAN(read(t, "lan_print.txt")),
		FRU:        ipmi.ParseFRU(read(t, "fru_print.txt")),
		Chassis:    ipmi.ParseChassis(read(t, "chassis_status.txt")),
		SELInfo:    ipmi.ParseSELInfo(read(t, "sel_info.txt")),
		PowerWatts: ipmi.ParseDCMIPower(read(t, "dcmi_power_reading.txt")),
		Sensors:    ipmi.ParseSDR(read(t, "sdr_elist.txt")),
		Errors:     map[string]string{},
	}
}

func TestEvaluateRealServer(t *testing.T) {
	health, sum, problems := Evaluate(realSnapshot(t))
	if health != bmcv1.HealthCritical {
		t.Fatalf("health = %s", health)
	}
	if sum.Total != 219 || sum.Critical != 5 || sum.Warning != 2 || sum.NoReading != 39 || sum.OK != 173 {
		t.Fatalf("summary = %+v", sum)
	}
	// Five fans below lnr and the chassis fan fault, then inlet temperature, SEL_Status and SEL usage.
	if len(problems) != 9 {
		t.Fatalf("got %d problems: %+v", len(problems), problems)
	}
	for i, p := range problems {
		if i < 6 && p.Severity != bmcv1.HealthCritical || i >= 6 && p.Severity != bmcv1.HealthWarning {
			t.Fatalf("problems not sorted worst-first: %+v", problems)
		}
	}
	if problems[0].Source != "FAN11" || problems[0].Message != "0 RPM below lower non-recoverable" {
		t.Fatalf("first problem = %+v", problems[0])
	}
}

func TestEvaluateHealthy(t *testing.T) {
	v := 25.0
	s := &Snapshot{MC: ipmi.MCInfo{FirmwareVersion: "1"}, Sensors: []ipmi.Sensor{{Name: "Inlet", Type: "temperature", Value: &v, Severity: ipmi.SeverityOK}}}
	if h, _, p := Evaluate(s); h != bmcv1.HealthOK || len(p) != 0 {
		t.Fatalf("health = %s, problems = %v", h, p)
	}
	if h, _, _ := Evaluate(&Snapshot{}); h != bmcv1.HealthUnknown {
		t.Fatalf("empty snapshot health = %s", h)
	}
}

func TestStatus(t *testing.T) {
	s := realSnapshot(t)
	s.CollectedAt = time.Now()
	st := Status(s)
	if st.PowerState != bmcv1.PowerOn || *st.PowerWatts != 1215 || *st.InletTemperature != 44 {
		t.Fatalf("status = %+v", st)
	}
	if st.Device.Manufacturer != "Gooxi" || st.Network.IPAddress != "10.20.0.31" || st.Controller.FirmwareVersion != "2.18" {
		t.Fatalf("inventory = %+v %+v %+v", st.Device, st.Network, st.Controller)
	}
	s.Errors["chassis"] = "timeout"
	if st := Status(s); st.PowerState != bmcv1.PowerUnknown {
		t.Fatalf("power state with chassis error = %s", st.PowerState)
	}
}

func TestInletTemperature(t *testing.T) {
	a, b, c := 60.0, 44.0, 30.0
	got, ok := InletTemperature([]ipmi.Sensor{
		{Name: "CPU0_Inlet_Temp", Type: "temperature", Value: &a},
		{Name: "Inlet_Temp", Type: "temperature", Value: &b},
		{Name: "Ambient", Type: "temperature", Value: &c},
	})
	if !ok || got != 44 {
		t.Fatalf("got %v %v", got, ok)
	}
}
