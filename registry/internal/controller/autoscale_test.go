package controller

import (
	"testing"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/harbor"
)

const gib = int64(1024 * 1024 * 1024)

// backendFor builds a backend at a given floor and already-deployed size.
func backendFor(floor, effective string, enabled bool) *registryv1alpha1.RegistryBackend {
	return &registryv1alpha1.RegistryBackend{
		Spec: registryv1alpha1.RegistryBackendSpec{
			Plan: floor,
			Autoscale: registryv1alpha1.AutoscaleSpec{
				Enabled:                   enabled,
				CommittedThresholdPercent: defaultCommittedThresholdPercent,
			},
		},
		Status: registryv1alpha1.RegistryBackendStatus{EffectivePlan: effective},
	}
}

// starter provisions 20Gi of registry storage, professional 50Gi, enterprise
// 200Gi, so the thresholds below are expressed against those.
func TestComputeEffectivePlan(t *testing.T) {
	tests := []struct {
		name      string
		floor     string
		effective string
		enabled   bool
		totals    harbor.StorageTotals
		want      string
	}{
		{
			name:    "well under the threshold stays put",
			floor:   "starter",
			enabled: true,
			totals:  harbor.StorageTotals{Committed: 5 * gib}, // 25% of 20Gi
			want:    "starter",
		},
		{
			name:    "just under 80% of starter stays put",
			floor:   "starter",
			enabled: true,
			totals:  harbor.StorageTotals{Committed: 15 * gib}, // 75% of 20Gi
			want:    "starter",
		},
		{
			name:    "crossing 80% of starter grows one step",
			floor:   "starter",
			enabled: true,
			totals:  harbor.StorageTotals{Committed: 17 * gib}, // 85% of 20Gi
			want:    "professional",
		},
		{
			name:    "heavy commitment skips straight past professional",
			floor:   "starter",
			enabled: true,
			totals:  harbor.StorageTotals{Committed: 100 * gib}, // over 80% of 50Gi too
			want:    "enterprise",
		},
		{
			name:    "already at the largest plan cannot grow further",
			floor:   "starter",
			enabled: true,
			totals:  harbor.StorageTotals{Committed: 10_000 * gib},
			want:    "enterprise",
		},
		{
			name:    "autoscale off holds the floor despite pressure",
			floor:   "starter",
			enabled: false,
			totals:  harbor.StorageTotals{Committed: 100 * gib},
			want:    "starter",
		},
		{
			name:      "never shrinks below what is already deployed",
			floor:     "starter",
			effective: "professional",
			enabled:   true,
			totals:    harbor.StorageTotals{Committed: 1 * gib},
			want:      "professional",
		},
		{
			name:    "never sizes below the administrator's floor",
			floor:   "enterprise",
			enabled: true,
			totals:  harbor.StorageTotals{Committed: 1 * gib},
			want:    "enterprise",
		},
		{
			name:    "an unlimited quota cannot be summed, so size holds",
			floor:   "starter",
			enabled: true,
			totals:  harbor.StorageTotals{Committed: 1 * gib, Unlimited: 1},
			want:    "starter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := computeEffectivePlan(backendFor(tt.floor, tt.effective, tt.enabled), tt.totals)
			if err != nil {
				t.Fatalf("computeEffectivePlan() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("computeEffectivePlan() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Growth must be monotonic: expanding a PersistentVolumeClaim cannot be undone,
// so a drop in commitment must never produce a smaller plan.
func TestComputeEffectivePlan_GrowthIsOneWay(t *testing.T) {
	cr := backendFor("starter", "", true)

	grown, err := computeEffectivePlan(cr, harbor.StorageTotals{Committed: 100 * gib})
	if err != nil {
		t.Fatalf("computeEffectivePlan() error = %v", err)
	}
	if grown != "enterprise" {
		t.Fatalf("expected growth to enterprise, got %q", grown)
	}

	// Registries are deleted and commitment collapses.
	cr.Status.EffectivePlan = grown
	after, err := computeEffectivePlan(cr, harbor.StorageTotals{Committed: 1 * gib})
	if err != nil {
		t.Fatalf("computeEffectivePlan() error = %v", err)
	}
	if after != "enterprise" {
		t.Errorf("plan fell back to %q after commitment dropped; PVCs cannot shrink so it must stay at enterprise", after)
	}
}

func TestPlanOrderHelpers(t *testing.T) {
	if planRank("starter") >= planRank("professional") || planRank("professional") >= planRank("enterprise") {
		t.Error("planOrder is not ascending")
	}
	if planRank("nonsense") != -1 {
		t.Error("planRank() should report -1 for an unknown plan")
	}
	if got := largerPlan("starter", "enterprise"); got != "enterprise" {
		t.Errorf("largerPlan() = %q, want enterprise", got)
	}
	if got := largerPlan("enterprise", "starter"); got != "enterprise" {
		t.Errorf("largerPlan() = %q, want enterprise", got)
	}
	if got := nextPlan("enterprise"); got != "" {
		t.Errorf("nextPlan(enterprise) = %q, want empty at the largest plan", got)
	}
}
