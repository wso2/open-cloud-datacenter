package controller

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetReady_SetsFieldsCorrectly(t *testing.T) {
	var conds []metav1.Condition
	setReady(&conds, 3, metav1.ConditionTrue, reasonReady, "Harbor is running")

	if len(conds) != 1 {
		t.Fatalf("len(conds) = %d, want 1", len(conds))
	}
	c := conds[0]
	if c.Type != conditionReady {
		t.Errorf("Type = %q, want %q", c.Type, conditionReady)
	}
	if c.Status != metav1.ConditionTrue {
		t.Errorf("Status = %q, want True", c.Status)
	}
	if c.Reason != reasonReady {
		t.Errorf("Reason = %q, want %q", c.Reason, reasonReady)
	}
	if c.Message != "Harbor is running" {
		t.Errorf("Message = %q, want %q", c.Message, "Harbor is running")
	}
	if c.ObservedGeneration != 3 {
		t.Errorf("ObservedGeneration = %d, want 3", c.ObservedGeneration)
	}
}

func TestSetReady_OnlyBumpsTransitionTimeOnActualStatusChange(t *testing.T) {
	var conds []metav1.Condition
	setReady(&conds, 1, metav1.ConditionFalse, reasonProvisioning, "waiting for Harbor")
	firstTransition := conds[0].LastTransitionTime

	// Re-running with the SAME status but a different message/generation (the
	// steady-state case: the reconciler patches status every loop) must not
	// look like a fresh transition.
	time.Sleep(2 * time.Millisecond)
	setReady(&conds, 2, metav1.ConditionFalse, reasonProvisioning, "still waiting for Harbor")
	if !conds[0].LastTransitionTime.Equal(&firstTransition) {
		t.Errorf("LastTransitionTime changed on a same-status update: got %v, want unchanged %v",
			conds[0].LastTransitionTime, firstTransition)
	}
	if conds[0].ObservedGeneration != 2 {
		t.Errorf("ObservedGeneration did not update on same-status call: got %d, want 2", conds[0].ObservedGeneration)
	}

	// A genuine status flip (False -> True) must bump the transition time.
	time.Sleep(2 * time.Millisecond)
	setReady(&conds, 3, metav1.ConditionTrue, reasonReady, "Harbor is running")
	if conds[0].LastTransitionTime.Equal(&firstTransition) {
		t.Error("LastTransitionTime did not change on a real status transition (False -> True)")
	}
}

func TestGenPassword(t *testing.T) {
	pass, err := genPassword()
	if err != nil {
		t.Fatalf("genPassword() error = %v", err)
	}
	if !strings.HasPrefix(pass, "Aa1") {
		t.Errorf("genPassword() = %q, want prefix %q (guarantees upper/lower/digit for Harbor's complexity policy)", pass, "Aa1")
	}
	if len(pass) <= len("Aa1") {
		t.Errorf("genPassword() = %q, too short — random suffix missing", pass)
	}
}

func TestGenPassword_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		p, err := genPassword()
		if err != nil {
			t.Fatalf("genPassword() error = %v", err)
		}
		if seen[p] {
			t.Fatalf("genPassword() produced a duplicate on iteration %d: %q", i, p)
		}
		seen[p] = true
	}
}

func TestGenAlphaNum_ExactLength(t *testing.T) {
	for _, n := range []int{16, 32, 1, 0} {
		s, err := genAlphaNum(n)
		if err != nil {
			t.Fatalf("genAlphaNum(%d) error = %v", n, err)
		}
		if len(s) != n {
			t.Errorf("genAlphaNum(%d) has length %d, want %d", n, len(s), n)
		}
	}
}

func TestGenAlphaNum_OnlyUsesAllowedCharacters(t *testing.T) {
	const allowed = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	s, err := genAlphaNum(64)
	if err != nil {
		t.Fatalf("genAlphaNum(64) error = %v", err)
	}
	for _, r := range s {
		if !strings.ContainsRune(allowed, r) {
			t.Fatalf("genAlphaNum(64) produced disallowed character %q in %q", r, s)
		}
	}
}
