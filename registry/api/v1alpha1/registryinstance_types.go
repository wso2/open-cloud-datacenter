package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Tenant",type=string,JSONPath=`.spec.tenantID`
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=`.spec.projectID`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.registryURL`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type RegistryInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RegistryInstanceSpec   `json:"spec,omitempty"`
	Status RegistryInstanceStatus `json:"status,omitempty"`
}

// BackendRef points at the RegistryBackend that hosts this project's Harbor instance.
// Both Name and Namespace are required — the backend lives in a different namespace
// (dc-tenant-<tenantID>) from the instance (dc-<tenantID>-<projectID>).
type BackendRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type RegistryInstanceSpec struct {
	// TenantID is the unique identifier for the tenant (e.g. "acme").
	TenantID string `json:"tenantID"`

	// ProjectID is the datacenter project requesting a Harbor project inside the shared Harbor.
	ProjectID string `json:"projectID"`

	// RegistryName is the user-provided registry name. Used as the Harbor project name
	// and to derive the credentials Secret name (registry-credentials-<cr-name>).
	// Unique within the (tenantID, projectID) pair.
	RegistryName string `json:"registryName,omitempty"`

	// Plan selects the Harbor resource profile.
	// +kubebuilder:validation:Enum=starter;professional;enterprise
	// +kubebuilder:default=starter
	Plan string `json:"plan,omitempty"`

	// BackendRef references the per-tenant RegistryBackend CR (name: rb-<tenantID>,
	// namespace: dc-tenant-<tenantID>). dc-api sets this; the controller reads it
	// to locate the running Harbor instance across namespaces.
	BackendRef BackendRef `json:"backendRef"`

	// ReclaimPolicy controls what happens to the Harbor project (and its
	// images) when this instance is deleted. Retain (default) leaves the
	// project intact; Delete removes it upstream.
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	ReclaimPolicy string `json:"reclaimPolicy,omitempty"`
}

type RegistryInstanceStatus struct {
	// Phase is the lifecycle state of this registry instance.
	// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Failed;Terminating
	Phase string `json:"phase,omitempty"`

	// ObservedGeneration is the .metadata.generation the controller last
	// reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions holds standard Kubernetes status conditions.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// RegistryURL is the Harbor portal URL, populated when Phase is Ready.
	RegistryURL string `json:"registryURL,omitempty"`

	// CredentialsSecretName is the name (in this namespace) of the Secret
	// holding the robot username + token for this registry. Owned by this CR.
	CredentialsSecretName string `json:"credentialsSecretName,omitempty"`

	// Progress is retained for API compatibility; no longer populated.
	Progress map[string]string `json:"progress,omitempty"`

	// Message contains error details when Phase is Failed.
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
type RegistryInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RegistryInstance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RegistryInstance{}, &RegistryInstanceList{})
}
