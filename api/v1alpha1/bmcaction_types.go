package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PowerAction is an out-of-band power operation. Values follow the Redfish ResetType names.
// +kubebuilder:validation:Enum=On;GracefulShutdown;GracefulRestart;ForceRestart;PowerCycle;ForceOff
type PowerAction string

const (
	ActionOn               PowerAction = "On"
	ActionGracefulShutdown PowerAction = "GracefulShutdown"
	ActionGracefulRestart  PowerAction = "GracefulRestart"
	ActionForceRestart     PowerAction = "ForceRestart"
	ActionPowerCycle       PowerAction = "PowerCycle"
	ActionForceOff         PowerAction = "ForceOff"
)

// PowerActions lists all supported actions.
var PowerActions = []PowerAction{
	ActionOn, ActionGracefulShutdown, ActionGracefulRestart, ActionForceRestart, ActionPowerCycle, ActionForceOff,
}

// Valid reports whether a is a supported action.
func (a PowerAction) Valid() bool {
	for _, x := range PowerActions {
		if a == x {
			return true
		}
	}
	return false
}

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

// BMCActionSpec describes a requested power operation.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type BMCActionSpec struct {
	// BMCName is the name of the target BMC object, which equals the node name.
	// +kubebuilder:validation:MinLength=1
	BMCName string `json:"bmcName"`

	Action PowerAction `json:"action"`

	// RequestedBy is the identity of the requester. An admission policy shipped with
	// kube-bmc requires it to match the authenticated Kubernetes user.
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
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
}

// BMCAction requests a power operation on a BMC. Actions are executed once by the
// kube-bmc server and kept as an audit record until their TTL expires.
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
