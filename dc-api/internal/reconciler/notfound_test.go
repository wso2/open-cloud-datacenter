package reconciler

import (
	"errors"
	"fmt"
	"testing"

	"github.com/wso2/dc-api/internal/providers"
)

// TestIsNotFound covers both detection paths deletion-handling depends on:
// the typed providers.NotFoundError sentinel (preferred, survives wrapping)
// and the legacy substring fallback for providers that don't return it yet.
func TestIsNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"typed sentinel", &providers.NotFoundError{Kind: "VM", Name: "vm-1"}, true},
		{"typed sentinel wrapped", fmt.Errorf("reconcile: %w",
			&providers.NotFoundError{Kind: "cluster", Name: "c-1", Msg: `cluster "c-1" not found in Rancher`}), true},
		{"substring fallback", errors.New("VM vm-1 not found in Harvester"), true},
		// The fallback is deliberately case-sensitive (drivers emit lowercase
		// "not found"); mixed-case texts rely on the typed sentinel instead.
		{"substring fallback is case-sensitive", errors.New("404 Not Found"), false},
		{"unrelated error", errors.New("connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNotFound(tc.err); got != tc.want {
				t.Fatalf("isNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
