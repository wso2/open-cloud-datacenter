package db

import (
	"testing"

	"github.com/wso2/dc-api/internal/placement"
)

// TestStamp_RootFieldlessDefaults pins that a field-less Root family (KeyVault/
// Database/PrivateEndpoint) is stamped with the local region+zone defaults from
// empty inputs — byte-identical to the prior `r.regionStamp(), r.zoneStamp()`
// unconditional binds. No DB needed: Stamp only reads localRegion/localZone.
func TestStamp_RootFieldlessDefaults(t *testing.T) {
	// (a) unset locals → seeded fallbacks "lk"/"zone-1".
	r := &Repository{}
	for _, f := range []placement.Family{placement.KeyVault, placement.Database, placement.PrivateEndpoint} {
		region, zone := r.Stamp(f, "", "")
		if region != "lk" || zone != "zone-1" {
			t.Errorf("Stamp(%s) defaults = (%q,%q), want (lk,zone-1)", f.Kind, region, zone)
		}
	}

	// (b) configured locals → those values.
	r2 := &Repository{localRegion: "us", localZone: "zone-7"}
	region, zone := r2.Stamp(placement.KeyVault, "", "")
	if region != "us" || zone != "zone-7" {
		t.Errorf("Stamp(KeyVault) with locals = (%q,%q), want (us,zone-7)", region, zone)
	}
}

// TestStamp_ChildInheritsZoneRegionLocal pins the VPC-child behaviour shared by
// VM/Cluster/Bastion through resources.Create: region is ALWAYS the local stamp
// (never the parent's), zone is the inherited value when present, else the local
// default. Mirrors the legacy `r.regionStamp()` + `zone := res.Zone; if ""...`.
func TestStamp_ChildInheritsZoneRegionLocal(t *testing.T) {
	r := &Repository{localRegion: "lk", localZone: "zone-1"}

	// Inherited (VPC path): zone comes from the parent, region stays local.
	for _, f := range []placement.Family{placement.VM, placement.Cluster, placement.Bastion} {
		region, zone := r.Stamp(f, "", "zone-3")
		if region != "lk" {
			t.Errorf("Stamp(%s) region = %q, want lk (always-local)", f.Kind, region)
		}
		if zone != "zone-3" {
			t.Errorf("Stamp(%s) zone = %q, want inherited zone-3", f.Kind, zone)
		}
	}

	// Legacy non-VPC path: no inherited zone → falls back to the local default.
	region, zone := r.Stamp(placement.VM, "", "")
	if region != "lk" || zone != "zone-1" {
		t.Errorf("Stamp(VM) legacy path = (%q,%q), want (lk,zone-1)", region, zone)
	}
}

// TestStamp_VNetRegionFromModel pins VNet's lone exception: region passes through
// from the model unchanged (RegionFromModel), never the local stamp; zone defaults
// only when empty. Mirrors the legacy raw `v.Region` bind + `if v.Zone==""...`.
func TestStamp_VNetRegionFromModel(t *testing.T) {
	r := &Repository{localRegion: "lk", localZone: "zone-1"}

	// Model region passes through (not "lk"); model zone passes through.
	region, zone := r.Stamp(placement.VNet, "eu", "zone-5")
	if region != "eu" {
		t.Errorf("Stamp(VNet) region = %q, want model value eu (RegionFromModel)", region)
	}
	if zone != "zone-5" {
		t.Errorf("Stamp(VNet) zone = %q, want model value zone-5", zone)
	}

	// Empty model zone → local default; region still from model.
	region, zone = r.Stamp(placement.VNet, "eu", "")
	if region != "eu" || zone != "zone-1" {
		t.Errorf("Stamp(VNet) empty-zone = (%q,%q), want (eu,zone-1)", region, zone)
	}
}

// TestStamp_NonRegionalReturnsEmpty pins that non-regional families never get a
// region/zone (their INSERTs have no such columns).
func TestStamp_NonRegionalReturnsEmpty(t *testing.T) {
	r := &Repository{localRegion: "lk", localZone: "zone-1"}
	region, zone := r.Stamp(placement.Subnet, "", "")
	if region != "" || zone != "" {
		t.Errorf("Stamp(Subnet) = (%q,%q), want empty (non-regional)", region, zone)
	}
}
