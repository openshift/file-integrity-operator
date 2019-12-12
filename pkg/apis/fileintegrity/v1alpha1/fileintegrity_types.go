package v1alpha1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type FileIntegrityStatusPhase string

const (
	PhaseInitializing FileIntegrityStatusPhase = "Initializing"
	PhaseActive       FileIntegrityStatusPhase = "Active"
	PhasePending      FileIntegrityStatusPhase = "Pending"
)

type FileIntegrityNodeCondition string

const (
	NodeConditionSucceeded FileIntegrityNodeCondition = "Succeeded"
	NodeConditionFailed    FileIntegrityNodeCondition = "Failed"
)

// FileIntegritySpec defines the desired state of FileIntegrity
// +k8s:openapi-gen=true
type FileIntegritySpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "operator-sdk generate k8s" to regenerate code after modifying this file
	// Add custom validation using kubebuilder tags: https://book-v1.book.kubebuilder.io/beyond_basics/generating_crd.html
	Config FileIntegrityConfig `json:"config"`
}

// FileIntegrityConfig defines the name, namespace, and data key for an AIDE config to use for integrity checking.
// +k8s:openapi-gen=true
type FileIntegrityConfig struct {
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Key       string `json:"key,omitempty"`
}

// FileIntegrityStatus defines the observed state of FileIntegrity
// +k8s:openapi-gen=true
type FileIntegrityStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "operator-sdk generate k8s" to regenerate code after modifying this file
	// Add custom validation using kubebuilder tags: https://book-v1.book.kubebuilder.io/beyond_basics/generating_crd.html
	Phase      FileIntegrityStatusPhase      `json:"phase,omitempty"`
	Conditions FileIntegrityStatusConditions `json:"conditions,omitempty"`
}

type FileIntegrityStatusConditions struct {
	Nodes []NodeStatus `json:"nodes"`
}

type NodeStatus struct {
	NodeName                 string                     `json:"nodeName"`
	LastProbeTime            time.Time                  `json:"lastProbeTime"`
	Condition                FileIntegrityNodeCondition `json:"condition"`
	ResultConfigMapName      string                     `json:"resultConfigMapName"`
	ResultConfigMapNamespace string                     `json:"resultConfigMapNamespace"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// FileIntegrity is the Schema for the fileintegrities API
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=fileintegrities,scope=Namespaced
type FileIntegrity struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FileIntegritySpec   `json:"spec,omitempty"`
	Status FileIntegrityStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// FileIntegrityList contains a list of FileIntegrity
type FileIntegrityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FileIntegrity `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FileIntegrity{}, &FileIntegrityList{})
}
