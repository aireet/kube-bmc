// Package v1alpha1 contains the bmc.kube-bmc.io/v1alpha1 API.
// +kubebuilder:object:generate=true
// +groupName=bmc.kube-bmc.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "bmc.kube-bmc.io", Version: "v1alpha1"}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &BMC{}, &BMCList{}, &BMCAction{}, &BMCActionList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
