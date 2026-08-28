package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=rb
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Plan",type=string,JSONPath=`.status.effectivePlan`
// +kubebuilder:printcolumn:name="Registries",type=integer,JSONPath=`.status.registryCount`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.registryURL`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RegistryBackend is one namespace's Harbor deployment, shared by every
// Registry in that namespace.
//
// It is a singleton per namespace: always the same name, in the namespace it
// serves, with Harbor's pods, volumes, and credentials deployed there too. The
// namespace is the whole identity, so there is no field naming what this
// backend is for. The operator creates it when the namespace's first Registry
// appears; users never author one. A fixed name makes "one Harbor per
// namespace" enforceable without locking — concurrent first Registries attempt
// the identical object and the API server settles it — the same way Kubernetes
// provisions its own per-namespace singletons (the default ServiceAccount, the
// kube-root-ca.crt ConfigMap).
type RegistryBackend struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RegistryBackendSpec   `json:"spec,omitempty"`
	Status RegistryBackendStatus `json:"status,omitempty"`
}

// RegistryBackendSpec is the desired state of a namespace's Harbor deployment.
type RegistryBackendSpec struct {
	// Plan is the floor for deployment sizing. The operator may size above it
	// when Autoscale is enabled, never below. Upgrade-only, because sizing
	// expands PersistentVolumeClaims and Kubernetes cannot shrink them.
	// +kubebuilder:validation:Enum=starter;professional;enterprise
	// +kubebuilder:default=starter
	// +kubebuilder:validation:XValidation:rule="self == oldSelf || (oldSelf == 'starter' && self != 'starter') || (oldSelf == 'professional' && self == 'enterprise')",message="plan can only be upgraded (starter -> professional -> enterprise), never downgraded"
	Plan string `json:"plan,omitempty"`

	// Autoscale raises the deployment size when the storage committed to the
	// namespace's registries approaches what is provisioned. Growth is permanent,
	// since PersistentVolumeClaims cannot shrink.
	// +kubebuilder:default={enabled:true}
	Autoscale AutoscaleSpec `json:"autoscale,omitempty"`
}

// AutoscaleSpec controls automatic growth of the Harbor deployment.
type AutoscaleSpec struct {
	// Enabled turns automatic growth on.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// CommittedThresholdPercent is the share of provisioned registry storage
	// that may be committed to registry quotas before the plan is raised.
	// +kubebuilder:validation:Minimum=50
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=80
	CommittedThresholdPercent int32 `json:"committedThresholdPercent,omitempty"`
}

// RegistryBackendStatus is the observed state of a namespace's Harbor deployment.
type RegistryBackendStatus struct {
	// Phase is the lifecycle state. Empty until the first reconcile.
	// +kubebuilder:validation:Enum=Provisioning;Ready;Failed;Terminating
	Phase string `json:"phase,omitempty"`

	// ObservedGeneration is the .metadata.generation last reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions holds standard Kubernetes status conditions.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// EffectivePlan is the size actually deployed: the greater of spec.plan and
	// what autoscaling has determined is needed. Sizing reads this, not
	// spec.plan, so growth never rewrites the spec the admin authored.
	// +kubebuilder:validation:Enum=starter;professional;enterprise
	EffectivePlan string `json:"effectivePlan,omitempty"`

	// CommittedStorageBytes is the total storage committed to this namespace's
	// registry quotas, which is what autoscaling compares against provisioned
	// capacity.
	CommittedStorageBytes int64 `json:"committedStorageBytes,omitempty"`

	// UsedStorageBytes is the storage this namespace's registries actually
	// consume, as reported by Harbor. Normally recorded for visibility only;
	// it becomes the growth trigger only when it exceeds
	// CommittedStorageBytes, which can only happen when
	// UnlimitedProjectCount is greater than zero (see computeEffectivePlan).
	UsedStorageBytes int64 `json:"usedStorageBytes,omitempty"`

	// UnlimitedProjectCount is how many of this namespace's Harbor projects
	// currently have no storage quota ceiling (set directly through Harbor,
	// never through a Registry). Nonzero means sizing for this Harbor is
	// currently driven by UsedStorageBytes rather than CommittedStorageBytes.
	UnlimitedProjectCount int32 `json:"unlimitedProjectCount,omitempty"`

	// RegistryCount is how many Registry objects this backend serves. Zero
	// means it is idle and safe for an administrator to remove.
	RegistryCount int32 `json:"registryCount,omitempty"`

	// RegistryURL is the Harbor URL for this namespace.
	RegistryURL string `json:"registryURL,omitempty"`

	// AdminSecretName is the Secret in this namespace holding Harbor's pinned
	// credentials.
	AdminSecretName string `json:"adminSecretName,omitempty"`

	// Message describes the current phase, including why it is not Ready.
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true

// RegistryBackendList is a list of RegistryBackend objects.
type RegistryBackendList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RegistryBackend `json:"items"`
}

// init registers RegistryBackend and its list type with the scheme.
func init() {
	SchemeBuilder.Register(&RegistryBackend{}, &RegistryBackendList{})
}
