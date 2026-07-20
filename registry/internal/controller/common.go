// Package controller holds the two Pattern-A reconcilers for the registry
// operator: RegistryBackend (one Harbor per tenant) and RegistryInstance (one
// Harbor project + robot per registry). The Custom Resource is the single
// source of truth — there is no database, worker, or HTTP gateway. All work
// happens in the reconcile loop, and slow waits are handled with RequeueAfter.
package controller

import (
	"crypto/rand"
	"encoding/base64"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	phaseProvisioning = "Provisioning"
	phaseReady        = "Ready"
	phaseFailed       = "Failed"
	phaseTerminating  = "Terminating"

	conditionReady = "Ready"

	reclaimDelete = "Delete"

	// eventReasonReady etc. are CamelCase to satisfy the condition-reason
	// and Event-reason conventions.
	reasonReady        = "Ready"
	reasonProvisioning = "Provisioning"
	reasonTransient    = "Transient"
	reasonError        = "Error"
	reasonBlocked      = "Blocked"
)

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
