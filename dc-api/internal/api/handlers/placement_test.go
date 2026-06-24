package handlers

import (
	"testing"

	"github.com/wso2/dc-api/internal/placement"
)

// TestInheritPlacement_ChildPresent pins that a Child family with a present
// parent inherits the parent's zone (region returned but unused by callers).
func TestInheritPlacement_ChildPresent(t *testing.T) {
	for _, f := range []placement.Family{placement.VM, placement.Cluster, placement.Bastion} {
		region, zone := inheritPlacement(f, "lk", "zone-3", true)
		if zone != "zone-3" {
			t.Errorf("inheritPlacement(%s) zone = %q, want inherited zone-3", f.Kind, zone)
		}
		if region != "lk" {
			t.Errorf("inheritPlacement(%s) region = %q, want lk", f.Kind, region)
		}
	}
}

// TestInheritPlacement_ChildAbsent pins the legacy non-VPC path: no parent →
// ("", "") so repo.Create falls back to the local default.
func TestInheritPlacement_ChildAbsent(t *testing.T) {
	region, zone := inheritPlacement(placement.VM, "lk", "zone-3", false)
	if region != "" || zone != "" {
		t.Errorf("inheritPlacement(absent parent) = (%q,%q), want empty", region, zone)
	}
}

// TestInheritPlacement_NonChild pins that Root / non-regional families never
// inherit, even with a present parent.
func TestInheritPlacement_NonChild(t *testing.T) {
	for _, f := range []placement.Family{placement.VNet, placement.KeyVault, placement.Subnet} {
		region, zone := inheritPlacement(f, "lk", "zone-3", true)
		if region != "" || zone != "" {
			t.Errorf("inheritPlacement(%s) = (%q,%q), want empty (not a Child)", f.Kind, region, zone)
		}
	}
}
