/*
Copyright © 2019 - 2022 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type FileIntegrityStatusPhase string

const (
	PhaseInitializing FileIntegrityStatusPhase = "Initializing"
	PhaseActive       FileIntegrityStatusPhase = "Active"
	PhasePending      FileIntegrityStatusPhase = "Pending"
	PhaseError        FileIntegrityStatusPhase = "Error"
)

type FileIntegrityNodeCondition string

const (
	NodeConditionSucceeded FileIntegrityNodeCondition = "Succeeded"
	NodeConditionFailed    FileIntegrityNodeCondition = "Failed"
	NodeConditionErrored   FileIntegrityNodeCondition = "Errored"
)

// FileIntegritySpec defines the desired state of FileIntegrity
type FileIntegritySpec struct {
	NodeSelector map[string]string   `json:"nodeSelector,omitempty"`
	Config       FileIntegrityConfig `json:"config"`
	Debug        bool                `json:"debug,omitempty"`
	// Specifies tolerations for custom taints. Defaults to allowing scheduling on master and infra nodes.
	// +kubebuilder:default={{key: "node-role.kubernetes.io/master", operator: "Exists", effect: "NoSchedule"},{key: "node-role.kubernetes.io/infra", operator: "Exists", effect: "NoSchedule"}}
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// Specifies the PriorityClass to use for the pods created by the operator.
	// This is an optional field. If PriorityClass is invalid or not found,
	// it will be ignored and cleared from the spec.
	PriorityClassName string `json:"priorityClassName,omitempty"`
	// Labels to add to the AIDE daemon pods. Keys must not use the reserved key "app"
	// or the reserved "file-integrity.openshift.io/" prefix, which are managed by the
	// operator. Format validation: admission catches common errors (first character, size limits)
	// as a best-effort fast-fail; the DaemonSet API is authoritative and performs full validation,
	// with failures surfaced as Warning Events. Re-init pods are excluded. The entire map replaces
	// (does not merge with) the pod template labels, and changing labels triggers a rolling restart
	// of the AIDE daemon pods.
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:validation:XValidation:rule="self.all(k, k != 'app' && !k.startsWith('file-integrity.openshift.io/'))",message="labels must not use the reserved key 'app' or the reserved prefix 'file-integrity.openshift.io/'"
	// +kubebuilder:validation:XValidation:rule="self.all(k, size(k) >= 1 && size(k) <= 253 && size(self[k]) <= 63)",message="label keys must be 1-253 characters and values must be at most 63 characters"
	// +kubebuilder:validation:XValidation:rule="self.all(k, !k.startsWith(' ') && !k.startsWith('-') && !self[k].startsWith(' ') && !self[k].startsWith('-') && (self[k] == '' || !self[k].startsWith('!')))",message="label keys and values must start with an alphanumeric character, not space, '-', or '!'"
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations to add to the AIDE daemon pods. Keys must not use the reserved
	// "file-integrity.openshift.io/" prefix, which is managed by the operator, and must
	// not use runtime-affecting prefixes (k8s.v1.cni.cncf.io/, io.kubernetes.cri-o.,
	// container.apparmor.security.beta.kubernetes.io/, seccomp.security.alpha.kubernetes.io/)
	// which can break the privileged DaemonSet. Format validation: admission catches common errors
	// (first character, size limits) as a best-effort fast-fail; the DaemonSet API is authoritative
	// and performs full validation, with failures surfaced as Warning Events. Re-init pods are
	// excluded. This field is for observability metadata. The entire map replaces (does not merge
	// with) the pod template annotations, and changing annotations triggers a rolling restart of
	// the AIDE daemon pods.
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:validation:XValidation:rule="self.all(k, !k.startsWith('file-integrity.openshift.io/'))",message="annotations must not use the reserved prefix 'file-integrity.openshift.io/'"
	// +kubebuilder:validation:XValidation:rule="self.all(k, !k.startsWith('k8s.v1.cni.cncf.io/') && !k.startsWith('io.kubernetes.cri-o.') && !k.startsWith('container.apparmor.security.beta.kubernetes.io/') && !k.startsWith('seccomp.security.alpha.kubernetes.io/'))",message="annotations must not use runtime-affecting prefixes (k8s.v1.cni.cncf.io/, io.kubernetes.cri-o., container.apparmor.security.beta.kubernetes.io/, seccomp.security.alpha.kubernetes.io/)"
	// +kubebuilder:validation:XValidation:rule="self.all(k, size(k) >= 1 && size(k) <= 253)",message="annotation keys must be 1-253 characters"
	// +kubebuilder:validation:XValidation:rule="self.all(k, !k.startsWith(' ') && !k.startsWith('-'))",message="annotation keys must start with an alphanumeric character, not space or '-'"
	Annotations map[string]string `json:"annotations,omitempty"`
}

// FileIntegrityConfig defines the name, namespace, and data key for an AIDE config to use for integrity checking.
type FileIntegrityConfig struct {
	// Name of a configMap that contains custom AIDE configuration. A default configuration would be created if omitted.
	Name string `json:"name,omitempty"`
	// Namespace of a configMap that contains custom AIDE configuration. A default configuration would be created if omitted.
	Namespace string `json:"namespace,omitempty"`
	// The key that contains the actual AIDE configuration in a configmap specified by Name and Namespace. Defaults to aide.conf
	Key string `json:"key,omitempty"`
	// Time between individual aide scans
	// +kubebuilder:default=900
	GracePeriod int `json:"gracePeriod,omitempty"`
	// The maximum number of AIDE database and log backups (leftover from the re-init process) to keep on a node.
	// Older backups beyond this number are automatically pruned by the daemon.
	// +kubebuilder:default=5
	MaxBackups int `json:"maxBackups,omitempty"`
	// InitialDelaySeconds is the number of seconds to wait before the first scan.
	// It is an optional field, and if not specified, the operator will default to 0
	InitialDelay int `json:"initialDelay,omitempty"`
}

// FileIntegrityStatus defines the observed state of FileIntegrity
type FileIntegrityStatus struct {
	Phase FileIntegrityStatusPhase `json:"phase,omitempty"`
}

// +kubebuilder:object:root=true

// FileIntegrityNodeStatus defines the status of a specific node
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=`.nodeName`
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=`.lastResult.condition`
type FileIntegrityNodeStatus struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	NodeName          string                    `json:"nodeName"`
	Results           []FileIntegrityScanResult `json:"results"`
	LastResult        FileIntegrityScanResult   `json:"lastResult"`
}

// FileIntegrityScanResult defines the one-time result of a scan.
type FileIntegrityScanResult struct {
	LastProbeTime            metav1.Time                `json:"lastProbeTime"`
	Condition                FileIntegrityNodeCondition `json:"condition"`
	ResultConfigMapName      string                     `json:"resultConfigMapName,omitempty"`
	ResultConfigMapNamespace string                     `json:"resultConfigMapNamespace,omitempty"`
	ErrorMsg                 string                     `json:"errorMessage,omitempty"`
	FilesAdded               int                        `json:"filesAdded,omitempty"`
	FilesChanged             int                        `json:"filesChanged,omitempty"`
	FilesRemoved             int                        `json:"filesRemoved,omitempty"`
}

// +kubebuilder:object:root=true

// FileIntegrity is the Schema for the fileintegrities API
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=fileintegrities,scope=Namespaced
type FileIntegrity struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FileIntegritySpec   `json:"spec,omitempty"`
	Status FileIntegrityStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FileIntegrityList contains a list of FileIntegrity
type FileIntegrityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FileIntegrity `json:"items"`
}

// +kubebuilder:object:root=true

// FileIntegrityNodeStatusList contains a list of FileIntegrityNodeStatus
type FileIntegrityNodeStatusList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FileIntegrityNodeStatus `json:"items"`
}

func init() {
	SchemeBuilder.Register(
		&FileIntegrity{},
		&FileIntegrityList{},
		&FileIntegrityNodeStatus{},
		&FileIntegrityNodeStatusList{},
	)
}
