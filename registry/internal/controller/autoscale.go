package controller

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/harbor"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/helm"
)

// planOrder lists the plans from smallest to largest. Sizing only ever moves
// forward through this list.
var planOrder = []string{"starter", "professional", "enterprise"}

// planRank returns a plan's position in planOrder, or -1 if unknown.
func planRank(plan string) int {
	for i, p := range planOrder {
		if p == plan {
			return i
		}
	}
	return -1
}

// largerPlan returns whichever of two plans is bigger, preferring a over b when
// either is unrecognised.
func largerPlan(a, b string) string {
	if planRank(b) > planRank(a) {
		return b
	}
	return a
}

// computeEffectivePlan returns the plan the Harbor deployment should run at.
//
// A deployment grows when the storage its registries have been promised
// approaches what is provisioned for them: quotas are commitments Harbor will
// honour, so capacity has to exist before those registries fill up rather than
// after. Usage is recorded for visibility but does not drive growth, since a
// registry that is merely full does not need a larger deployment — its own
// quota already bounds it.
//
// The result never falls below spec.plan, and never below the plan already
// deployed, because expanding a PersistentVolumeClaim cannot be undone.
func computeEffectivePlan(cr *registryv1alpha1.RegistryBackend, totals harbor.StorageTotals) (string, error) {
	floor := cr.Spec.Plan
	if floor == "" {
		floor = planOrder[0]
	}
	current := largerPlan(floor, cr.Status.EffectivePlan)

	if !cr.Spec.Autoscale.Enabled {
		return current, nil
	}

	threshold := int64(cr.Spec.Autoscale.CommittedThresholdPercent)
	if threshold <= 0 {
		threshold = 80
	}

	// An unlimited quota commits an unbounded amount, so there is nothing to
	// compare against. Hold the current size and let an administrator decide.
	if totals.Unlimited > 0 {
		return current, nil
	}

	for {
		capacityBytes, err := planRegistryStorageBytes(current)
		if err != nil {
			return "", err
		}
		if totals.Committed*100 <= capacityBytes*threshold {
			return current, nil
		}
		next := nextPlan(current)
		if next == "" {
			// Already at the largest plan; report the pressure rather than
			// silently accepting it.
			return current, nil
		}
		current = next
	}
}

// nextPlan returns the plan one step larger, or "" at the largest.
func nextPlan(plan string) string {
	i := planRank(plan)
	if i < 0 || i+1 >= len(planOrder) {
		return ""
	}
	return planOrder[i+1]
}

// planRegistryStorageBytes returns the registry volume size a plan provisions.
func planRegistryStorageBytes(plan string) (int64, error) {
	p, err := helm.PlanFor(plan)
	if err != nil {
		return 0, err
	}
	q, err := resource.ParseQuantity(p.RegistryStorage)
	if err != nil {
		return 0, fmt.Errorf("parse plan %q registry storage %q: %w", plan, p.RegistryStorage, err)
	}
	return q.Value(), nil
}
