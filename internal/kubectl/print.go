package kubectl

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"k8s.io/apimachinery/pkg/util/duration"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/collector"
	"github.com/aireet/kube-bmc/internal/ipmi"
)

const (
	red    = "\x1b[31m"
	yellow = "\x1b[33m"
	green  = "\x1b[32m"
	dim    = "\x1b[2m"
	reset  = "\x1b[0m"
)

func paint(on bool, color, s string) string {
	if !on || s == "" {
		return s
	}
	return color + s + reset
}

func healthText(h bmcv1.Health, color bool) string {
	switch h {
	case bmcv1.HealthOK:
		return paint(color, green, "OK")
	case bmcv1.HealthWarning:
		return paint(color, yellow, "Warning")
	case bmcv1.HealthCritical:
		return paint(color, red, "Critical")
	}
	return "Unknown"
}

func newTable(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 4, 3, ' ', 0) }

func int32Text(v *int32, unit string) string {
	if v == nil {
		return "-"
	}
	return strconv.Itoa(int(*v)) + unit
}

func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return duration.HumanDuration(time.Since(t))
}

func printBMCs(w io.Writer, items []bmcv1.BMC, wide, color bool) {
	if len(items) == 0 {
		fmt.Fprintln(w, "No BMCs found.")
		return
	}
	t := newTable(w)
	header := "NAME\tHEALTH\tPOWER\tWATTS\tINLET\tVENDOR\tMODEL\tBMC-IP\tPROBLEMS"
	if wide {
		header += "\tFIRMWARE\tSERIAL\tLAST-REPORT"
	}
	fmt.Fprintln(t, header)
	for _, b := range items {
		s := b.Status
		problems := "-"
		if n := len(s.Problems); n > 0 {
			problems = s.Problems[0].Source + ": " + s.Problems[0].Message
			if n > 1 {
				problems += fmt.Sprintf(" (+%d)", n-1)
			}
		}
		row := []string{b.Name, healthText(s.Health, color), orDash(string(s.PowerState)), int32Text(s.PowerWatts, ""),
			int32Text(s.InletTemperature, "°C"), orDash(s.Device.Manufacturer), orDash(s.Device.Product), orDash(s.Network.IPAddress), problems}
		if wide {
			last := "-"
			if s.LastUpdated != nil {
				last = age(s.LastUpdated.Time)
			}
			row = append(row, orDash(s.Controller.FirmwareVersion), orDash(s.Device.SerialNumber), last)
		}
		fmt.Fprintln(t, strings.Join(row, "\t"))
	}
	_ = t.Flush()
}

func describe(w io.Writer, b *bmcv1.BMC, actions []bmcv1.BMCAction, color bool) {
	s := b.Status
	t := newTable(w)
	line := func(k, v string) { fmt.Fprintf(t, "%s:\t%s\n", k, v) }
	line("Name", b.Name)
	line("Node", b.Spec.NodeName)
	line("Health", healthText(s.Health, color))
	line("Power", fmt.Sprintf("%s, %s W, inlet %s", orDash(string(s.PowerState)), int32Text(s.PowerWatts, ""), int32Text(s.InletTemperature, "°C")))
	line("System", strings.TrimSpace(fmt.Sprintf("%s %s %s", s.Device.Manufacturer, s.Device.Product, s.Device.Version)))
	line("Serial", fmt.Sprintf("%s (part %s)", orDash(s.Device.SerialNumber), orDash(s.Device.PartNumber)))
	line("Board", fmt.Sprintf("%s (serial %s)", orDash(s.Device.BoardProduct), orDash(s.Device.BoardSerial)))
	line("BMC firmware", fmt.Sprintf("%s (IPMI %s, IANA %s)", orDash(s.Controller.FirmwareVersion), orDash(s.Controller.IPMIVersion), orDash(s.Controller.ManufacturerID)))
	line("BMC network", fmt.Sprintf("%s/%s via %s, MAC %s, %s, channel %d", orDash(s.Network.IPAddress), orDash(s.Network.Netmask),
		orDash(s.Network.Gateway), orDash(s.Network.MACAddress), orDash(s.Network.Source), s.Network.Channel))
	if b.Spec.Address != "" || b.Spec.CredentialsRef != nil {
		ref := "-"
		if b.Spec.CredentialsRef != nil {
			ref = b.Spec.CredentialsRef.Name
		}
		line("Out-of-band", fmt.Sprintf("%s %s, credentials %s", orDash(string(b.Spec.Protocol)), orDash(b.Spec.Address), ref))
	}
	faults := "none"
	if len(s.Chassis.Faults) > 0 {
		faults = paint(color, red, strings.Join(s.Chassis.Faults, ", "))
	}
	line("Chassis", fmt.Sprintf("restore policy %s, last power event %s, faults: %s", orDash(s.Chassis.PowerRestorePolicy), orDash(s.Chassis.LastPowerEvent), faults))
	line("System event log", fmt.Sprintf("%d entries, %d%% used, last event %s", s.SEL.Entries, s.SEL.UsedPercent, orDash(s.SEL.LastAddTime)))
	line("Sensors", fmt.Sprintf("%d total, %d ok, %d warning, %d critical, %d without reading",
		s.Sensors.Total, s.Sensors.OK, s.Sensors.Warning, s.Sensors.Critical, s.Sensors.NoReading))
	last := "never"
	if s.LastUpdated != nil {
		last = age(s.LastUpdated.Time) + " ago"
	}
	line("Agent", fmt.Sprintf("%s, last report %s", orDash(s.AgentVersion), last))
	for _, c := range s.Conditions {
		line("Condition "+c.Type, fmt.Sprintf("%s (%s) %s", c.Status, c.Reason, c.Message))
	}
	_ = t.Flush()

	fmt.Fprintln(w, "\nProblems:")
	if len(s.Problems) == 0 {
		fmt.Fprintln(w, "  none")
	} else {
		t = newTable(w)
		for _, p := range s.Problems {
			fmt.Fprintf(t, "  %s\t%s\t%s\n", healthText(p.Severity, color), p.Source, p.Message)
		}
		_ = t.Flush()
	}

	fmt.Fprintln(w, "\nRecent actions:")
	if len(actions) == 0 {
		fmt.Fprintln(w, "  none")
		return
	}
	if len(actions) > 5 {
		actions = actions[:5]
	}
	t = newTable(w)
	for _, a := range actions {
		fmt.Fprintf(t, "  %s\t%s\t%s\t%s\t%s ago\n", a.Name, a.Spec.Action, a.Spec.RequestedBy, phaseText(a.Status.Phase, color), age(a.CreationTimestamp.Time))
	}
	_ = t.Flush()
}

type sensorFilter struct {
	Type     string
	Problems bool
	All      bool
}

var severityRank = map[ipmi.Severity]int{ipmi.SeverityCritical: 0, ipmi.SeverityWarning: 1, ipmi.SeverityOK: 2, ipmi.SeverityNoReading: 3}

func printSensors(w io.Writer, sensors []ipmi.Sensor, f sensorFilter, color bool) {
	list := slices.DeleteFunc(slices.Clone(sensors), func(s ipmi.Sensor) bool {
		switch {
		case f.Type != "" && s.Type != f.Type:
			return true
		case f.Problems:
			return s.Severity != ipmi.SeverityWarning && s.Severity != ipmi.SeverityCritical
		case !f.All:
			return s.Severity == ipmi.SeverityNoReading
		}
		return false
	})
	slices.SortStableFunc(list, func(a, b ipmi.Sensor) int {
		if d := severityRank[a.Severity] - severityRank[b.Severity]; d != 0 {
			return d
		}
		if a.Type != b.Type {
			return strings.Compare(a.Type, b.Type)
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(list) == 0 {
		fmt.Fprintln(w, "No matching sensors.")
		return
	}
	t := newTable(w)
	fmt.Fprintln(t, "SENSOR\tTYPE\tREADING\tSTATE\tTHRESHOLDS")
	for _, s := range list {
		state := string(s.Severity)
		switch s.Severity {
		case ipmi.SeverityCritical:
			state = paint(color, red, state)
		case ipmi.SeverityWarning:
			state = paint(color, yellow, state)
		case ipmi.SeverityNoReading:
			state = paint(color, dim, "no reading")
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Type, orDash(s.Reading), state, thresholds(s.Thresholds))
	}
	_ = t.Flush()
}

func thresholds(t *ipmi.Thresholds) string {
	if t == nil {
		return "-"
	}
	var parts []string
	for _, x := range []struct {
		name string
		v    *float64
	}{{"lnr", t.LowerNonRecoverable}, {"lcr", t.LowerCritical}, {"lnc", t.LowerNonCritical},
		{"unc", t.UpperNonCritical}, {"ucr", t.UpperCritical}, {"unr", t.UpperNonRecoverable}} {
		if x.v != nil {
			parts = append(parts, x.name+" "+strconv.FormatFloat(*x.v, 'f', -1, 64))
		}
	}
	return orDash(strings.Join(parts, ", "))
}

func printEvents(w io.Writer, snap *collector.Snapshot, limit int, grep string) {
	fmt.Fprintf(w, "System event log: %d entries, %d%% used\n\n", snap.SELInfo.Entries, snap.SELInfo.UsedPercent)
	q := strings.ToLower(grep)
	t := newTable(w)
	fmt.Fprintln(t, "ID\tTIME\tSTATE\tSENSOR\tEVENT\tDETAIL")
	n := 0
	for _, e := range snap.Events {
		if q != "" && !strings.Contains(strings.ToLower(e.Sensor+" "+e.Event+" "+e.Detail), q) {
			continue
		}
		if n == limit {
			break
		}
		n++
		state := "asserted"
		if !e.Asserted {
			state = "deasserted"
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\n", e.ID, e.Timestamp, state, e.Sensor, e.Event, orDash(e.Detail))
	}
	_ = t.Flush()
	if n == 0 {
		fmt.Fprintln(w, "No matching events.")
	}
}

func phaseText(p bmcv1.ActionPhase, color bool) string {
	switch p {
	case bmcv1.PhaseSucceeded:
		return paint(color, green, string(p))
	case bmcv1.PhaseFailed, bmcv1.PhaseRejected:
		return paint(color, red, string(p))
	case "":
		return string(bmcv1.PhasePending)
	}
	return string(p)
}

func printActions(w io.Writer, actions []bmcv1.BMCAction, color bool) {
	if len(actions) == 0 {
		fmt.Fprintln(w, "No actions found.")
		return
	}
	t := newTable(w)
	fmt.Fprintln(t, "NAME\tBMC\tACTION\tREQUESTED-BY\tPHASE\tAGE\tMESSAGE")
	for _, a := range actions {
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", a.Name, a.Spec.BMCName, a.Spec.Action, a.Spec.RequestedBy,
			phaseText(a.Status.Phase, color), age(a.CreationTimestamp.Time), orDash(a.Status.Message))
	}
	_ = t.Flush()
}
