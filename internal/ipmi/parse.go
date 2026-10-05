package ipmi

import (
	"bufio"
	"bytes"
	"regexp"
	"strconv"
	"strings"
)

// parseKV reads ipmitool's "Key : Value" blocks. Continuation lines (empty key) are skipped.
func parseKV(out []byte) map[string]string {
	kv := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		if _, dup := kv[k]; !dup {
			kv[k] = strings.TrimSpace(v)
		}
	}
	return kv
}

// rows splits ipmitool's pipe-separated tables into trimmed fields.
func rows(out []byte) [][]string {
	var res [][]string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, "|") {
			continue
		}
		f := strings.Split(line, "|")
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		res = append(res, f)
	}
	return res
}

func first(kv map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := kv[k]; v != "" {
			return v
		}
	}
	return ""
}

func ParseMCInfo(out []byte) MCInfo {
	kv := parseKV(out)
	return MCInfo{
		FirmwareVersion:  kv["Firmware Revision"],
		IPMIVersion:      kv["IPMI Version"],
		ManufacturerID:   kv["Manufacturer ID"],
		ManufacturerName: kv["Manufacturer Name"],
		ProductID:        kv["Product ID"],
	}
}

func ParseGUID(out []byte) string {
	return first(parseKV(out), "System GUID", "GUID")
}

func ParseLAN(out []byte) LAN {
	kv := parseKV(out)
	return LAN{
		IPAddress:  kv["IP Address"],
		Netmask:    kv["Subnet Mask"],
		Gateway:    kv["Default Gateway IP"],
		MACAddress: kv["MAC Address"],
		Source:     kv["IP Address Source"],
		VLAN:       kv["802.1q VLAN ID"],
	}
}

func ParseFRU(out []byte) FRU {
	kv := parseKV(out)
	return FRU{
		Manufacturer:  first(kv, "Product Manufacturer", "Board Mfg"),
		Product:       first(kv, "Product Name", "Board Product"),
		Version:       kv["Product Version"],
		SerialNumber:  first(kv, "Product Serial", "Chassis Serial", "Board Serial"),
		PartNumber:    first(kv, "Product Part Number", "Board Part Number"),
		BoardProduct:  kv["Board Product"],
		BoardSerial:   kv["Board Serial"],
		ChassisType:   kv["Chassis Type"],
		ChassisSerial: kv["Chassis Serial"],
	}
}

var chassisFaults = []string{"Power Overload", "Main Power Fault", "Power Control Fault", "Drive Fault", "Cooling/Fan Fault"}

func ParseChassis(out []byte) Chassis {
	kv := parseKV(out)
	c := Chassis{
		PowerOn:            kv["System Power"] == "on",
		PowerRestorePolicy: kv["Power Restore Policy"],
		LastPowerEvent:     kv["Last Power Event"],
		IntrusionActive:    kv["Chassis Intrusion"] == "active",
	}
	for _, f := range chassisFaults {
		if kv[f] == "true" {
			c.Faults = append(c.Faults, f)
		}
	}
	return c
}

func ParseSELInfo(out []byte) SELInfo {
	kv := parseKV(out)
	n, _ := strconv.Atoi(kv["Entries"])
	pct, _ := strconv.Atoi(strings.TrimSuffix(kv["Percent Used"], "%"))
	return SELInfo{Entries: n, UsedPercent: pct, LastAddTime: kv["Last Add Time"]}
}

var dcmiWatts = regexp.MustCompile(`Instantaneous power reading:\s*(\d+)\s*Watts`)

// ParseDCMIPower returns the instantaneous power draw in watts, or -1 when unavailable.
func ParseDCMIPower(out []byte) int {
	m := dcmiWatts.FindSubmatch(out)
	if m == nil {
		return -1
	}
	w, _ := strconv.Atoi(string(m[1]))
	return w
}

// sensorType maps an ipmitool unit to a coarse sensor type.
func sensorType(unit string) string {
	switch strings.ToLower(unit) {
	case "degrees c", "degrees f":
		return "temperature"
	case "volts":
		return "voltage"
	case "rpm":
		return "fan"
	case "watts":
		return "power"
	case "amps":
		return "current"
	case "percent":
		return "utilization"
	}
	return "discrete"
}

// severityOf classifies an ipmitool status code (sdr: ok/ns/lnc/ucr/…, sensor: ok/na/nc/cr/nr).
func severityOf(status string) Severity {
	switch status {
	case "ok":
		return SeverityOK
	case "ns", "na", "":
		return SeverityNoReading
	case "nc", "lnc", "unc":
		return SeverityWarning
	case "cr", "lcr", "ucr", "nr", "lnr", "unr":
		return SeverityCritical
	}
	// Discrete sensors report hex state bits (e.g. 0x0080) in `ipmitool sensor`; they carry no verdict.
	return SeverityOK
}

// discreteRules classify discrete sensors, which report status "ok" even while asserting a
// failure state. The first matching rule applies.
var discreteRules = []struct {
	words []string
	sev   Severity
}{
	{[]string{"failure detected", "ac lost", "fault", "uncorrectable", "thermal trip", "ierr", "non-recoverable", "machine check", "critical interrupt", "bus fatal"}, SeverityCritical},
	{[]string{"predictive failure", "redundancy lost", "redundancy degraded", "log full", "correctable ecc", "degraded", "almost full"}, SeverityWarning},
}

// DiscreteSeverity classifies the state text of a discrete sensor.
func DiscreteSeverity(reading string) Severity {
	r := strings.ToLower(reading)
	// "Predictive failure" would otherwise match the critical rule for "failure".
	if strings.Contains(r, "predictive failure") {
		return SeverityWarning
	}
	for _, rule := range discreteRules {
		for _, w := range rule.words {
			if strings.Contains(r, w) {
				return rule.sev
			}
		}
	}
	return SeverityOK
}

// splitReading turns "46 degrees C" into (46, "degrees C"). Non-numeric readings return ok=false.
func splitReading(s string) (float64, string, bool) {
	num, unit, _ := strings.Cut(s, " ")
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, "", false
	}
	return v, unit, true
}

// ParseSDR parses `ipmitool sdr elist`: name | id | status | entity | reading.
func ParseSDR(out []byte) []Sensor {
	var res []Sensor
	for _, f := range rows(out) {
		if len(f) < 5 {
			continue
		}
		s := Sensor{Name: f[0], Status: f[2], Entity: f[3], Reading: f[4], Severity: severityOf(f[2])}
		if v, unit, ok := splitReading(f[4]); ok {
			s.Value, s.Unit = &v, unit
		}
		s.Type = sensorType(s.Unit)
		if s.Type == "discrete" && s.Severity == SeverityOK {
			s.Severity = DiscreteSeverity(s.Reading)
		}
		res = append(res, s)
	}
	return res
}

func optFloat(s string) *float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

// ParseThresholds parses `ipmitool sensor`: name | value | unit | status | lnr | lcr | lnc | unc | ucr | unr.
func ParseThresholds(out []byte) map[string]Thresholds {
	res := map[string]Thresholds{}
	for _, f := range rows(out) {
		if len(f) < 10 || sensorType(f[2]) == "discrete" {
			continue
		}
		t := Thresholds{
			LowerNonRecoverable: optFloat(f[4]),
			LowerCritical:       optFloat(f[5]),
			LowerNonCritical:    optFloat(f[6]),
			UpperNonCritical:    optFloat(f[7]),
			UpperCritical:       optFloat(f[8]),
			UpperNonRecoverable: optFloat(f[9]),
		}
		if t != (Thresholds{}) {
			res[f[0]] = t
		}
	}
	return res
}

// ParseSEL parses `ipmitool sel elist`: id | date | time | sensor | event | direction [| detail].
func ParseSEL(out []byte) []Event {
	var res []Event
	for _, f := range rows(out) {
		if len(f) < 5 {
			continue
		}
		e := Event{ID: f[0], Timestamp: strings.TrimSpace(f[1] + " " + f[2]), Sensor: f[3], Event: f[4], Asserted: true}
		if len(f) > 5 {
			e.Asserted = f[5] != "Deasserted"
		}
		if len(f) > 6 {
			e.Detail = strings.Join(f[6:], " | ")
		}
		res = append(res, e)
	}
	return res
}
