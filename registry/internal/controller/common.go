// Package controller holds the two reconcilers for the registry operator:
// Registry (a project inside a tenant's Harbor, and the only resource tenants
// create) and RegistryBackend (one Harbor deployment per tenant, provisioned by
// the operator when a tenant's first Registry appears). The Custom Resource is
// the single source of truth, and all work happens inside the reconcile loop,
// with slow waits handled via RequeueAfter.
package controller

import (
	"crypto/rand"
	"encoding/base64"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/harbor"
)

const (
	phaseProvisioning = "Provisioning"
	phaseReady        = "Ready"
	phaseFailed       = "Failed"
	phaseTerminating  = "Terminating"

	conditionReady = "Ready"

	reclaimDelete = "Delete"
	reclaimRetain = "Retain"

	// tenantLabel records which tenant a RegistryBackend serves, so backends can
	// be found by tenant without parsing their name.
	tenantLabel = "registry.opencloud.wso2.com/tenant-id"

	// defaultCommittedThresholdPercent is the share of provisioned registry
	// storage that may be committed to registry quotas before the Harbor
	// deployment is grown.
	defaultCommittedThresholdPercent = 80

	// eventReasonReady etc. are CamelCase to satisfy the condition-reason
	// and Event-reason conventions.
	reasonReady        = "Ready"
	reasonProvisioning = "Provisioning"
	reasonTransient    = "Transient"
	reasonError        = "Error"
	reasonBlocked      = "Blocked"
	reasonResized      = "Resized"
)

// newHarborClient returns a Harbor client honouring the configured TLS mode.
func newHarborClient(cfg config.HelmConfig, url, adminPass string) *harbor.Client {
	if cfg.InsecureHarborTLS {
		return harbor.NewInsecureClient(url, adminPass)
	}
	return harbor.NewClient(url, adminPass)
}

// backendReadinessChanged limits backend-driven wake-ups to the transitions a
// Registry cares about: whether Harbor became usable, and where it lives.
func backendReadinessChanged() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return true },
		DeleteFunc: func(event.DeleteEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldB, ok1 := e.ObjectOld.(*registryv1alpha1.RegistryBackend)
			newB, ok2 := e.ObjectNew.(*registryv1alpha1.RegistryBackend)
			if !ok1 || !ok2 {
				return true
			}
			return oldB.Status.Phase != newB.Status.Phase ||
				oldB.Status.RegistryURL != newB.Status.RegistryURL ||
				oldB.Status.AdminSecretName != newB.Status.AdminSecretName ||
				oldB.Status.HarborNamespace != newB.Status.HarborNamespace
		},
	}
}

// setReady sets/updates the Ready condition. It delegates to
// meta.SetStatusCondition, which only bumps LastTransitionTime when the status
// actually changes (the correct K8s semantics) and records observedGeneration.
func setReady(conds *[]metav1.Condition, gen int64, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:               conditionReady,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: gen,
	})
}

// genPassword returns a random password that satisfies Harbor's complexity
// policy (at least one upper, lower, and digit). The "Aa1" prefix guarantees
// the character classes; the rest is 18 bytes of URL-safe random.
func genPassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "Aa1" + base64.RawURLEncoding.EncodeToString(b), nil
}

// genAlphaNum returns an n-character alphanumeric secret. The Harbor chart
// requires exact lengths for its internal keys (secretKey/core.secret 16,
// xsrfKey 32), so unlike genPassword the length is caller-controlled.
func genAlphaNum(n int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}
