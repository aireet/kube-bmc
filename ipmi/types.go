package ipmi

// Controller describes the BMC itself, as reported by `ipmitool mc info`.
type Controller struct {
	FirmwareVersion  string `json:"firmwareVersion"`
	IPMIVersion      string `json:"ipmiVersion"`
	ManufacturerID   string `json:"manufacturerID"`
	ManufacturerName string `json:"manufacturerName"`
	ProductID        string `json:"productID"`
}

// LAN is the network configuration of the BMC's management port.
type LAN struct {
	Channel    int    `json:"channel"`
	IPAddress  string `json:"ipAddress"`
	Netmask    string `json:"netmask"`
	Gateway    string `json:"gateway"`
	MACAddress string `json:"macAddress"`
	Source     string `json:"source"`
	VLAN       string `json:"vlan"`
}

// FRU is the field-replaceable unit inventory of the system board and chassis.
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

// Chassis is the chassis power and fault state.
type Chassis struct {
	PowerOn            bool     `json:"powerOn"`
	PowerRestorePolicy string   `json:"powerRestorePolicy"`
	LastPowerEvent     string   `json:"lastPowerEvent"`
	IntrusionActive    bool     `json:"intrusionActive"`
	Faults             []string `json:"faults"`
}

// SELInfo describes the System Event Log.
type SELInfo struct {
	Entries     int `json:"entries"`
	UsedPercent int `json:"usedPercent"`
	// LastAddTime changes whenever an entry is added, so it detects new events cheaply.
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

// SensorType is the kind of quantity a sensor measures, derived from its unit.
type SensorType string

const (
	Temperature SensorType = "temperature"
	Voltage     SensorType = "voltage"
	Fan         SensorType = "fan"
	Power       SensorType = "power"
	Current     SensorType = "current"
	Utilization SensorType = "utilization"
	// Discrete sensors report a state, such as "Presence detected", instead of a value.
	Discrete SensorType = "discrete"
)

// Thresholds of an analog sensor. A nil field is not set on the BMC.
type Thresholds struct {
	LowerNonRecoverable *float64 `json:"lnr,omitempty"`
	LowerCritical       *float64 `json:"lcr,omitempty"`
	LowerNonCritical    *float64 `json:"lnc,omitempty"`
	UpperNonCritical    *float64 `json:"unc,omitempty"`
	UpperCritical       *float64 `json:"ucr,omitempty"`
	UpperNonRecoverable *float64 `json:"unr,omitempty"`
}

// Sensor is one sensor of the Sensor Data Repository (SDR).
type Sensor struct {
	Name  string     `json:"name"`
	Type  SensorType `json:"type"`
	Value *float64   `json:"value,omitempty"`
	Unit  string     `json:"unit,omitempty"`
	// Reading is the reading as text, e.g. "46 degrees C" or "Presence detected".
	Reading string `json:"reading"`
	// Status is the ipmitool status code, e.g. "ok", "ucr" or "ns".
	Status   string   `json:"status"`
	Severity Severity `json:"severity"`
	Entity   string   `json:"entity,omitempty"`
	// Thresholds is not set by Client.Sensors; see Client.Thresholds.
	Thresholds *Thresholds `json:"thresholds,omitempty"`
}

// Event is one System Event Log entry.
type Event struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Sensor    string `json:"sensor"`
	// Event describes what happened. ipmitool leaves it empty for some sensor-specific
	// events; Client.EventRecord and EventRecord.Describe recover the description.
	Event    string `json:"event"`
	Asserted bool   `json:"asserted"`
	Detail   string `json:"detail,omitempty"`
}
