package v1alpha1

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ActionType is an operation on a BMC. Power actions follow the Redfish ResetType names.
// +kubebuilder:validation:Enum=On;GracefulShutdown;GracefulRestart;ForceRestart;PowerCycle;ForceOff;IdentifyOn;IdentifyOff;ClearSEL
type ActionType string

const (
	ActionOn               ActionType = "On"
	ActionGracefulShutdown ActionType = "GracefulShutdown"
	ActionGracefulRestart  ActionType = "GracefulRestart"
	ActionForceRestart     ActionType = "ForceRestart"
	ActionPowerCycle       ActionType = "PowerCycle"
	ActionForceOff         ActionType = "ForceOff"
	// ActionIdentifyOn turns the chassis identify light on until ActionIdentifyOff.
	ActionIdentifyOn  ActionType = "IdentifyOn"
	ActionIdentifyOff ActionType = "IdentifyOff"
	// ActionClearSEL archives the System Event Log to a ConfigMap and then clears it.
	ActionClearSEL ActionType = "ClearSEL"
)

// PowerActions lists the actions that change the power state.
var PowerActions = []ActionType{
	ActionOn, ActionGracefulShutdown, ActionGracefulRestart, ActionForceRestart, ActionPowerCycle, ActionForceOff,
}

// Actions lists all supported actions.
var Actions = append(slices.Clone(PowerActions), ActionIdentifyOn, ActionIdentifyOff, ActionClearSEL)

// Valid reports whether a is a supported action.
func (a ActionType) Valid() bool { return slices.Contains(Actions, a) }

// IsPower reports whether a changes the power state.
func (a ActionType) IsPower() bool { return slices.Contains(PowerActions, a) }

// ActionPhase is the lifecycle phase of a BMCAction.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Rejected
type ActionPhase string

const (
	PhasePending   ActionPhase = "Pending"
	PhaseRunning   ActionPhase = "Running"
	PhaseSucceeded ActionPhase = "Succeeded"
	PhaseFailed    ActionPhase = "Failed"
	PhaseRejected  ActionPhase = "Rejected"
)

// Done reports whether the phase is terminal.
func (p ActionPhase) Done() bool {
	return p == PhaseSucceeded || p == PhaseFailed || p == PhaseRejected
}

// BMCActionSpec describes a requested operation.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type BMCActionSpec struct {
	// BMCName is the name of the target BMC object, which equals the node name.
	// +kubebuilder:validation:MinLength=1
	BMCName string `json:"bmcName"`

	Action ActionType `json:"action"`

	// RequestedBy is the identity of the requester. The kube-bmc server and kubectl-bmc
	// set it to the authenticated user.
	// +kubebuilder:validation:MinLength=1
	RequestedBy string `json:"requestedBy"`

	// Reason is a free-form justification recorded for auditing.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	Reason string `json:"reason,omitempty"`
}

// BMCActionStatus is the observed state of a BMCAction.
type BMCActionStatus struct {
	// +optional
	Phase ActionPhase `json:"phase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// PowerStateBefore is the power state reported by the node agent when execution started.
	// +optional
	PowerStateBefore PowerState `json:"powerStateBefore,omitempty"`
	// SELArchive is the ConfigMap, as namespace/name, that holds the System Event Log
	// saved by ClearSEL before the log was cleared.
	// +optional
	SELArchive string `json:"selArchive,omitempty"`
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// Deadline is set by the executor when it starts the action: the time by which it
	// records the outcome. An action still Running after its deadline was interrupted,
	// for example because the executing process stopped, and is marked Failed.
	// +optional
	Deadline *metav1.Time `json:"deadline,omitempty"`
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
}

// BMCAction requests an operation on a BMC: a power action, the identify light, or
// clearing the System Event Log. Each action is executed once, by the node agent in-band
// or by the kube-bmc server out-of-band, and kept as an audit record until its TTL expires.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=bmca
// +kubebuilder:printcolumn:name="BMC",type=string,JSONPath=`.spec.bmcName`
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.spec.action`
// +kubebuilder:printcolumn:name="Requested-By",type=string,JSONPath=`.spec.requestedBy`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type BMCAction struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BMCActionSpec   `json:"spec"`
	Status BMCActionStatus `json:"status,omitempty"`
}

// BMCActionList contains a list of BMCAction.
// +kubebuilder:object:root=true
type BMCActionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BMCAction `json:"items"`
}
