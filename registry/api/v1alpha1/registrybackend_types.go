package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Tenant",type=string,JSONPath=`.spec.tenantID`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.registryURL`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
//
// RegistryBackend is the per-tenant shared Harbor deployment. One Backend per tenant;
// all of that tenant's RegistryInstances (datacenter projects) share it.
// dc-api creates this CR lazily on the first registry create for a tenant.
type RegistryBackend struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RegistryBackendSpec   `json:"spec,omitempty"`
	Status RegistryBackendStatus `json:"status,omitempty"`
}

type RegistryBackendSpec struct {
	// TenantID is the unique identifier for the tenant.
	// The Harbor Helm release and namespace are derived from this.
	TenantID string `json:"tenantID"`

	// Plan selects the Harbor resource profile.
	// +kubebuilder:validation:Enum=starter;professional;enterprise
	// +kubebuilder:default=starter
	Plan string `json:"plan,omitempty"`
}

type RegistryBackendStatus struct {
	// Phase is the lifecycle state of the Harbor deployment.
	// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Failed;Terminating
	Phase string `json:"phase,omitempty"`

	// Conditions holds standard Kubernetes status conditions.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// RegistryURL is the Harbor portal URL, populated when Phase is Ready.
	RegistryURL string `json:"registryURL,omitempty"`

	// Progress tracks sub-step status during Helm deployment.
	Progress map[string]string `json:"progress,omitempty"`

	// Message contains error details when Phase is Failed.
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
type RegistryBackendList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RegistryBackend `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RegistryBackend{}, &RegistryBackendList{})
}
