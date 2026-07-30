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
	// TenantID is the unique identifier for the tenant, deriving the Harbor
	// Helm release, namespace, and URL. Immutable — changing it would
	// repoint the Backend at a new Harbor target without reclaiming the old one.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="tenantID is immutable"
	TenantID string `json:"tenantID"`

	// Plan selects the Harbor resource profile. Upgrades only
	// (starter -> professional -> enterprise): a downgrade would imply
	// shrinking PVCs, which Kubernetes cannot do. Enforced at admission by the
	// CEL transition rule below (evaluated on UPDATE only).
	// +kubebuilder:validation:Enum=starter;professional;enterprise
	// +kubebuilder:default=starter
	// +kubebuilder:validation:XValidation:rule="self == oldSelf || (oldSelf == 'starter' && self != 'starter') || (oldSelf == 'professional' && self == 'enterprise')",message="plan can only be upgraded (starter -> professional -> enterprise), never downgraded"
	Plan string `json:"plan,omitempty"`

	// ReclaimPolicy controls what happens to Harbor's data (PVCs) when this
	// Backend is deleted. Retain (default) leaves the PVCs on disk; Delete
	// removes them. Retain is the safe default for a registry.
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	ReclaimPolicy string `json:"reclaimPolicy,omitempty"`
}

type RegistryBackendStatus struct {
	// Phase is the lifecycle state of the Harbor deployment. Empty until the
	// first reconcile.
	// +kubebuilder:validation:Enum=Provisioning;Ready;Failed;Terminating
	Phase string `json:"phase,omitempty"`

	// ObservedGeneration is the .metadata.generation the controller last
	// reconciled. When it equals .metadata.generation the latest spec has
	// been processed.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions holds standard Kubernetes status conditions.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// RegistryURL is the Harbor portal URL, populated when Phase is Ready.
	RegistryURL string `json:"registryURL,omitempty"`

	// AdminSecretName is the name (in this namespace) of the Secret holding
	// Harbor's admin + database passwords. Owned by this CR.
	AdminSecretName string `json:"adminSecretName,omitempty"`

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
