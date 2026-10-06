package collector

import (
	"testing"
	"time"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/ipmi"
	"github.com/aireet/kube-bmc/ipmi/ipmitest"
)

// realSnapshot is read from the recorded output of a real 8-GPU server with five failed
// fans.
func realSnapshot(t *testing.T) *Snapshot {
	t.Helper()
	c, ctx := ipmi.New(ipmitest.Recorded()), t.Context()
	s := &Snapshot{Errors: map[string]string{}}
	var err error
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	s.MC, err = c.Controller(ctx)
	must(err)
	s.LAN, err = c.LAN(ctx)
	must(err)
	s.FRU, err = c.FRU(ctx)
	must(err)
	s.Chassis, err = c.Chassis(ctx)
	must(err)
	s.SELInfo, err = c.SELInfo(ctx)
	must(err)
	s.PowerWatts, err = c.PowerReading(ctx)
	must(err)
	s.Sensors, err = c.Sensors(ctx)
	must(err)
	return s
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

func TestEvaluateOneProblemPerSource(t *testing.T) {
	lnr := func(name string) ipmi.Sensor {
		return ipmi.Sensor{Name: name, Type: "fan", Reading: "0 RPM", Status: "lnr", Severity: ipmi.SeverityCritical}
	}
	unc := ipmi.Sensor{Name: "FAN1", Type: "fan", Reading: "900 RPM", Status: "lnc", Severity: ipmi.SeverityWarning}
	s := &Snapshot{
		MC:      ipmi.Controller{FirmwareVersion: "1"},
		Sensors: []ipmi.Sensor{unc, lnr("FAN1"), lnr("FAN2")},
		Chassis: ipmi.Chassis{Faults: []string{"Cooling/Fan Fault", "Power Overload"}, IntrusionActive: true},
	}
	_, _, problems := Evaluate(s)
	bySource := map[string]bmcv1.Problem{}
	for _, p := range problems {
		if _, dup := bySource[p.Source]; dup {
			t.Fatalf("source %s raised several problems: %+v", p.Source, problems)
		}
		bySource[p.Source] = p
	}
	if bySource["FAN1"].Severity != bmcv1.HealthCritical {
		t.Errorf("FAN1 = %+v; want the most severe reading", bySource["FAN1"])
	}
	if got := bySource["chassis"].Message; got != "Cooling/Fan Fault, Power Overload reported by the BMC" {
		t.Errorf("chassis = %q", got)
	}
	if bySource["intrusion"].Severity != bmcv1.HealthWarning {
		t.Errorf("intrusion = %+v", bySource["intrusion"])
	}
}

func TestEvaluateHealthy(t *testing.T) {
	v := 25.0
	s := &Snapshot{MC: ipmi.Controller{FirmwareVersion: "1"}, Sensors: []ipmi.Sensor{{Name: "Inlet", Type: "temperature", Value: &v, Severity: ipmi.SeverityOK}}}
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

func TestLoginFailures(t *testing.T) {
	s := &Snapshot{MC: ipmi.Controller{FirmwareVersion: "1"}}
	for range 12 {
		s.Events = append(s.Events, ipmi.Event{Sensor: "Session Audit #0xff", Event: "Invalid username or password"})
	}
	h, _, problems := Evaluate(s)
	if h != bmcv1.HealthWarning || len(problems) != 1 || problems[0].Source != "security" ||
		problems[0].Message != "12 failed BMC logins among the newest 12 SEL entries" {
		t.Fatalf("health = %s, problems = %+v", h, problems)
	}
	s.Events = s.Events[:3]
	if h, _, _ := Evaluate(s); h != bmcv1.HealthOK {
		t.Fatalf("three failures reported as %s", h)
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
