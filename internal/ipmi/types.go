// Package ipmi talks to the local BMC through ipmitool over the in-band (KCS/SSIF) interface
// and turns its text output into typed values.
package ipmi

// MCInfo is the output of `ipmitool mc info`.
type MCInfo struct {
	FirmwareVersion  string `json:"firmwareVersion"`
	IPMIVersion      string `json:"ipmiVersion"`
	ManufacturerID   string `json:"manufacturerID"`
	ManufacturerName string `json:"manufacturerName"`
	ProductID        string `json:"productID"`
}

// LAN is the output of `ipmitool lan print`.
type LAN struct {
	Channel    int    `json:"channel"`
	IPAddress  string `json:"ipAddress"`
	Netmask    string `json:"netmask"`
	Gateway    string `json:"gateway"`
	MACAddress string `json:"macAddress"`
	Source     string `json:"source"`
	VLAN       string `json:"vlan"`
}

// FRU is the output of `ipmitool fru print 0`.
type FRU struct {
	Manufacturer  string `json:"manufacturer"`
	Product       string `json:"product"`
	Version       string `json:"version"`
	SerialNumber  string `json:"serialNumber"`
	PartNumber    string `json:"partNumber"`
	BoardProduct  string `json:"boardProduct"`
	BoardSerial   string `json:"boardSerial"`
	ChassisType   string `json:"chassisType"`
	ChassisSerial string `json:"chassisSerial"`
}

// Chassis is the output of `ipmitool chassis status`.
type Chassis struct {
	PowerOn            bool     `json:"powerOn"`
	PowerRestorePolicy string   `json:"powerRestorePolicy"`
	LastPowerEvent     string   `json:"lastPowerEvent"`
	IntrusionActive    bool     `json:"intrusionActive"`
	Faults             []string `json:"faults"`
}

// SELInfo is the output of `ipmitool sel info`.
type SELInfo struct {
	Entries     int    `json:"entries"`
	UsedPercent int    `json:"usedPercent"`
	LastAddTime string `json:"lastAddTime"`
}

// Severity classifies a sensor reading.
type Severity string

const (
	SeverityOK        Severity = "ok"
	SeverityWarning   Severity = "warning"
	SeverityCritical  Severity = "critical"
	SeverityNoReading Severity = "noreading"
)

// Thresholds of an analog sensor; nil means "not set".
type Thresholds struct {
	LowerNonRecoverable *float64 `json:"lnr,omitempty"`
	LowerCritical       *float64 `json:"lcr,omitempty"`
	LowerNonCritical    *float64 `json:"lnc,omitempty"`
	UpperNonCritical    *float64 `json:"unc,omitempty"`
	UpperCritical       *float64 `json:"ucr,omitempty"`
	UpperNonRecoverable *float64 `json:"unr,omitempty"`
}

// Sensor is one SDR entry.
type Sensor struct {
	Name string `json:"name"`
	// Type is derived from the unit: temperature, voltage, fan, power, current, utilization or discrete.
	Type     string   `json:"type"`
	Value    *float64 `json:"value,omitempty"`
	Unit     string   `json:"unit,omitempty"`
	Reading  string   `json:"reading"`
	Status   string   `json:"status"`
	Severity Severity `json:"severity"`
	Entity   string   `json:"entity,omitempty"`
	// Thresholds are filled from `ipmitool sensor`, which is slow, so they refresh less often.
	Thresholds *Thresholds `json:"thresholds,omitempty"`
}

// Event is one System Event Log record.
type Event struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Sensor    string `json:"sensor"`
	Event     string `json:"event"`
	Asserted  bool   `json:"asserted"`
	Detail    string `json:"detail,omitempty"`
}
