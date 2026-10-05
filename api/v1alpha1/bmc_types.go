package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Protocol is the out-of-band management protocol used to reach a BMC over the network.
// +kubebuilder:validation:Enum=Redfish;IPMI
type Protocol string

const (
	ProtocolRedfish Protocol = "Redfish"
	ProtocolIPMI    Protocol = "IPMI"
)

// Health is the aggregated hardware health reported by a BMC.
// +kubebuilder:validation:Enum=OK;Warning;Critical;Unknown
type Health string

const (
	HealthOK       Health = "OK"
	HealthWarning  Health = "Warning"
	HealthCritical Health = "Critical"
	HealthUnknown  Health = "Unknown"
)

// Rank orders health values so the worst one can be picked.
func (h Health) Rank() int {
	switch h {
	case HealthOK:
		return 1
	case HealthWarning:
		return 2
	case HealthCritical:
		return 3
	}
	return 0
}

// PowerState is the chassis power state.
// +kubebuilder:validation:Enum=On;Off;Unknown
type PowerState string

const (
	PowerOn      PowerState = "On"
	PowerOff     PowerState = "Off"
	PowerUnknown PowerState = "Unknown"
)

// SecretReference points to a Secret holding `username` and `password` keys.
type SecretReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// BMCSpec is the desired, user-owned configuration of a BMC.
// The node agent creates the object with only nodeName set; everything else is optional.
type BMCSpec struct {
	// NodeName is the Kubernetes node this BMC belongs to.
	NodeName string `json:"nodeName"`

	// Address overrides the BMC address discovered in-band (IP or hostname, optional :port).
	// +optional
	Address string `json:"address,omitempty"`

	// Protocol used for out-of-band operations. Defaults to Redfish.
	// +kubebuilder:default=Redfish
	// +optional
	Protocol Protocol `json:"protocol,omitempty"`

	// CredentialsRef selects the Secret used for out-of-band access.
	// When empty, the server's default credentials Secret is used.
	// +optional
	CredentialsRef *SecretReference `json:"credentialsRef,omitempty"`

	// InsecureSkipVerify disables TLS certificate verification for Redfish.
	// +kubebuilder:default=true
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// Device is the FRU inventory of the server.
type Device struct {
	Manufacturer  string `json:"manufacturer,omitempty"`
	Product       string `json:"product,omitempty"`
	Version       string `json:"version,omitempty"`
	SerialNumber  string `json:"serialNumber,omitempty"`
	PartNumber    string `json:"partNumber,omitempty"`
	BoardProduct  string `json:"boardProduct,omitempty"`
	BoardSerial   string `json:"boardSerial,omitempty"`
	ChassisType   string `json:"chassisType,omitempty"`
	ChassisSerial string `json:"chassisSerial,omitempty"`
}

// Controller describes the BMC itself.
type Controller struct {
	FirmwareVersion string `json:"firmwareVersion,omitempty"`
	IPMIVersion     string `json:"ipmiVersion,omitempty"`
	ManufacturerID  string `json:"manufacturerID,omitempty"`
	ProductID       string `json:"productID,omitempty"`
	GUID            string `json:"guid,omitempty"`
}

// Network is the BMC management network configuration.
type Network struct {
	Channel    int    `json:"channel,omitempty"`
	IPAddress  string `json:"ipAddress,omitempty"`
	Netmask    string `json:"netmask,omitempty"`
	Gateway    string `json:"gateway,omitempty"`
	MACAddress string `json:"macAddress,omitempty"`
	Source     string `json:"source,omitempty"`
	VLAN       string `json:"vlan,omitempty"`
}

// Chassis is the chassis power and fault state.
type Chassis struct {
	PowerRestorePolicy string   `json:"powerRestorePolicy,omitempty"`
	LastPowerEvent     string   `json:"lastPowerEvent,omitempty"`
	IntrusionActive    bool     `json:"intrusionActive,omitempty"`
	Faults             []string `json:"faults,omitempty"`
}

// SEL summarizes the System Event Log.
type SEL struct {
	Entries     int    `json:"entries,omitempty"`
	UsedPercent int    `json:"usedPercent,omitempty"`
	LastAddTime string `json:"lastAddTime,omitempty"`
}

// SensorSummary counts sensors by state.
// Counts are optional so status merge patches, which omit unchanged zeros, stay valid.
type SensorSummary struct {
	Total     int `json:"total,omitempty"`
	OK        int `json:"ok,omitempty"`
	Warning   int `json:"warning,omitempty"`
	Critical  int `json:"critical,omitempty"`
	NoReading int `json:"noReading,omitempty"`
}

// Problem is a human-readable reason that degrades health.
type Problem struct {
	Severity Health `json:"severity"`
	// Source is what raised the problem: a sensor name, "chassis" or "sel".
	Source  string `json:"source"`
	Message string `json:"message"`
}

// BMCStatus is observed state, written by the node agent.
type BMCStatus struct {
	// +optional
	Health Health `json:"health,omitempty"`
	// +optional
	PowerState PowerState `json:"powerState,omitempty"`
	// PowerWatts is the instantaneous system power draw (DCMI).
	// +optional
	PowerWatts *int32 `json:"powerWatts,omitempty"`
	// InletTemperature in degrees Celsius.
	// +optional
	InletTemperature *int32 `json:"inletTemperature,omitempty"`

	// +optional
	Device Device `json:"device,omitempty"`
	// +optional
	Controller Controller `json:"controller,omitempty"`
	// +optional
	Network Network `json:"network,omitempty"`
	// +optional
	Chassis Chassis `json:"chassis,omitempty"`
	// +optional
	SEL SEL `json:"sel,omitempty"`
	// +optional
	Sensors SensorSummary `json:"sensors,omitempty"`
	// Problems lists everything that is not healthy, worst first.
	// +optional
	// +listType=atomic
	Problems []Problem `json:"problems,omitempty"`

	// AgentVersion of the node agent that last reported.
	// +optional
	AgentVersion string `json:"agentVersion,omitempty"`
	// LastUpdated is when the agent last reported, used to detect stale agents.
	// +optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// BMC represents the baseboard management controller of one Kubernetes node.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=bmcs
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.nodeName`
// +kubebuilder:printcolumn:name="BMC-IP",type=string,JSONPath=`.status.network.ipAddress`
// +kubebuilder:printcolumn:name="Vendor",type=string,JSONPath=`.status.device.manufacturer`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.status.device.product`
// +kubebuilder:printcolumn:name="Power",type=string,JSONPath=`.status.powerState`
// +kubebuilder:printcolumn:name="Watts",type=integer,JSONPath=`.status.powerWatts`
// +kubebuilder:printcolumn:name="Inlet",type=integer,JSONPath=`.status.inletTemperature`
// +kubebuilder:printcolumn:name="Health",type=string,JSONPath=`.status.health`
// +kubebuilder:printcolumn:name="Firmware",type=string,JSONPath=`.status.controller.firmwareVersion`,priority=1
// +kubebuilder:printcolumn:name="Serial",type=string,JSONPath=`.status.device.serialNumber`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type BMC struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BMCSpec   `json:"spec,omitempty"`
	Status BMCStatus `json:"status,omitempty"`
}

// BMCList contains a list of BMC.
// +kubebuilder:object:root=true
type BMCList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BMC `json:"items"`
}
