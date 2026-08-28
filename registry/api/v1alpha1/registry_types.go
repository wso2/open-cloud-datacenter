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
// The Harbor serving a Registry is the one in its own namespace, derived from
// metadata.namespace and never declared in the spec — so a Registry cannot
// reference another namespace's Harbor, because there is no field to point
// elsewhere. The namespace's first Registry causes a Harbor deployment to be
// provisioned; later ones reuse it and only add a Harbor project.
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

	// HarborProject is the project created in Harbor for this Registry, and the
	// record that one exists: the finalizer reads it to decide whether there is
	// anything in Harbor to clean up.
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
