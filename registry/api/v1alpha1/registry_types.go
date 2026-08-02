package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=reg
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=`.status.harborProject`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.registryURL`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Registry requests a container registry for the namespace it is created in.
//
// The tenant is derived from the namespace's Harvester project, never declared
// in the spec — so a Registry cannot reference another tenant's Harbor. The
// first Registry in a tenant causes a Harbor deployment to be provisioned;
// later ones reuse it and only add a Harbor project.
type Registry struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RegistrySpec   `json:"spec,omitempty"`
	Status RegistryStatus `json:"status,omitempty"`
}

// RegistrySpec is the desired state of one registry.
type RegistrySpec struct {
	// Plan sets this registry's storage quota inside Harbor
	// (starter=5Gi, professional=20Gi, enterprise=100Gi). It can be raised or
	// lowered; Harbor rejects a decrease below current usage until images are
	// removed. It does not size the underlying Harbor deployment.
	// +kubebuilder:validation:Enum=starter;professional;enterprise
	// +kubebuilder:default=starter
	Plan string `json:"plan,omitempty"`

	// ReclaimPolicy controls what happens to the Harbor project and its images
	// when this Registry is deleted. Retain keeps them; Delete removes them.
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	ReclaimPolicy string `json:"reclaimPolicy,omitempty"`
}

// RegistryStatus is the observed state of one registry.
type RegistryStatus struct {
	// Phase is the lifecycle state. Empty until the first reconcile.
	// +kubebuilder:validation:Enum=Provisioning;Ready;Failed;Terminating
	Phase string `json:"phase,omitempty"`

	// ObservedGeneration is the .metadata.generation last reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions holds standard Kubernetes status conditions.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// TenantID is the Harvester project this registry resolved to.
	TenantID string `json:"tenantID,omitempty"`

	// BackendName records which RegistryBackend serves this registry, set once
	// the binding succeeds. The backend uses it to refuse deletion while
	// registries still depend on it.
	BackendName string `json:"backendName,omitempty"`

	// HarborProject is the project created in Harbor, derived as
	// <namespace>-<name> so registries can never collide.
	HarborProject string `json:"harborProject,omitempty"`

	// RegistryURL is the Harbor URL to log in and push to.
	RegistryURL string `json:"registryURL,omitempty"`

	// CredentialsSecretName is the Secret in this namespace holding the robot
	// username and token for this registry.
	CredentialsSecretName string `json:"credentialsSecretName,omitempty"`

	// Message describes the current phase, including why it is not Ready.
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true

// RegistryList is a list of Registry objects.
type RegistryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Registry `json:"items"`
}

// init registers Registry and its list type with the scheme.
func init() {
	SchemeBuilder.Register(&Registry{}, &RegistryList{})
}
