package collector

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/ipmi"
)

var statusText = map[string]string{
	"lnc": "below lower non-critical", "lcr": "below lower critical", "lnr": "below lower non-recoverable",
	"unc": "above upper non-critical", "ucr": "above upper critical", "unr": "above upper non-recoverable",
	"nc": "non-critical", "cr": "critical", "nr": "non-recoverable",
}

// Evaluate derives health, a sensor summary and a worst-first problem list from a snapshot.
func Evaluate(s *Snapshot) (bmcv1.Health, bmcv1.SensorSummary, []bmcv1.Problem) {
	var sum bmcv1.SensorSummary
	var problems []bmcv1.Problem
	add := func(sev bmcv1.Health, src, msg string) {
		problems = append(problems, bmcv1.Problem{Severity: sev, Source: src, Message: msg})
	}

	for _, sn := range s.Sensors {
		sum.Total++
		switch sn.Severity {
		case ipmi.SeverityOK:
			sum.OK++
		case ipmi.SeverityNoReading:
			sum.NoReading++
		case ipmi.SeverityWarning:
			sum.Warning++
			add(bmcv1.HealthWarning, sn.Name, describe(sn))
		case ipmi.SeverityCritical:
			sum.Critical++
			add(bmcv1.HealthCritical, sn.Name, describe(sn))
		}
	}

	for _, f := range s.Chassis.Faults {
		add(bmcv1.HealthCritical, "chassis", f+" reported by the BMC")
	}
	if s.Chassis.IntrusionActive {
		add(bmcv1.HealthWarning, "chassis", "Chassis intrusion detected")
	}
	if s.SELInfo.UsedPercent >= 90 {
		add(bmcv1.HealthWarning, "sel", fmt.Sprintf("System Event Log is %d%% full; new hardware events may be dropped", s.SELInfo.UsedPercent))
	}

	slices.SortStableFunc(problems, func(a, b bmcv1.Problem) int { return b.Severity.Rank() - a.Severity.Rank() })

	health := bmcv1.HealthOK
	switch {
	case len(s.Sensors) == 0 && s.MC.FirmwareVersion == "":
		health = bmcv1.HealthUnknown
	case len(problems) > 0:
		health = problems[0].Severity
	}
	return health, sum, problems
}

func describe(s ipmi.Sensor) string {
	if t := statusText[s.Status]; t != "" {
		return s.Reading + " " + t
	}
	return s.Reading
}

var inletName = regexp.MustCompile(`(?i)inlet|ambient|intake|front.?panel`)

// InletTemperature picks the most plausible air-inlet temperature sensor.
func InletTemperature(sensors []ipmi.Sensor) (float64, bool) {
	for _, s := range sensors {
		if s.Type == "temperature" && s.Value != nil && inletName.MatchString(s.Name) &&
			!strings.Contains(strings.ToLower(s.Name), "cpu") {
			return *s.Value, true
		}
	}
	return 0, false
}
