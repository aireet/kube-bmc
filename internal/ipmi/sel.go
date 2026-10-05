package ipmi

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// SELRecord is the raw content of one SEL entry as reported by `ipmitool sel get`.
type SELRecord struct {
	SensorType string
	EventType  string
	EventData  []byte
}

// ParseSELRecord parses `ipmitool sel get <id>`.
func ParseSELRecord(out []byte) (SELRecord, error) {
	kv := parseKV(out)
	data, err := hex.DecodeString(kv["Event Data"])
	if err != nil || len(data) == 0 {
		return SELRecord{}, fmt.Errorf("sel record has no event data")
	}
	return SELRecord{SensorType: kv["Sensor Type"], EventType: kv["Event Type"], EventData: data}, nil
}

// sensorSpecificOffsets lists event offsets of sensor-specific discrete sensors (IPMI 2.0,
// Table 42-3) that ipmitool does not always describe.
var sensorSpecificOffsets = map[string][]string{
	"Session Audit": {
		"Session activated",
		"Session deactivated",
		"Invalid username or password",
		"Invalid password disable",
	},
	"Event Logging Disabled": {
		"Correctable memory error logging disabled",
		"Event type logging disabled",
		"Log area reset/cleared",
		"All event logging disabled",
		"SEL full",
		"SEL almost full",
		"Correctable machine check error logging disabled",
	},
	"Watchdog 2": {
		"Timer expired",
		"Hard reset",
		"Power down",
		"Power cycle",
		"", "", "", "",
		"Timer interrupt",
	},
}

// Describe returns a description of a sensor-specific event, or "" if it is unknown.
// The event offset is the low nibble of the first event data byte.
func (r SELRecord) Describe() string {
	if !strings.Contains(r.EventType, "Sensor-specific") {
		return ""
	}
	offsets := sensorSpecificOffsets[r.SensorType]
	offset := int(r.EventData[0] & 0x0f)
	if offset >= len(offsets) {
		return ""
	}
	return offsets[offset]
}

// parseRawIPv4 parses the response of `ipmitool raw 0x0c 0x02 <ch> <param> 0 0` for an
// IPv4 parameter: a parameter revision byte followed by four address bytes.
func parseRawIPv4(out []byte) (string, error) {
	fields := strings.Fields(string(out))
	if len(fields) < 5 {
		return "", fmt.Errorf("unexpected raw response %q", strings.TrimSpace(string(out)))
	}
	ip := make([]string, 4)
	for i, f := range fields[1:5] {
		b, err := hex.DecodeString(f)
		if err != nil || len(b) != 1 {
			return "", fmt.Errorf("unexpected raw response %q", strings.TrimSpace(string(out)))
		}
		ip[i] = fmt.Sprint(b[0])
	}
	return strings.Join(ip, "."), nil
}
