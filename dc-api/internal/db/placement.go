// Package db — placement.go
//
// The shared region/zone stamping primitive. Before this file every Create*
// method open-coded its own region/zone binding (regionStamp()/zoneStamp(), or
// the v.Zone-default-then-bind for VNet, or the res.Zone-or-default for VPC
// children). Those five shapes are now ALL produced by one helper, Stamp,
// driven by a placement.Family declaration — so a new regional resource needs
// no per-Create hand-wiring, only a Family row.
//
// Stamp returns VALUES rather than mutating a model, deliberately: field-less
// families (KeyVault/Database/PrivateEndpoint have no Region/Zone struct field)
// pass the returned values straight to their INSERT and write nothing back to
// the struct, while field-bearing families assign them onto the model. One
// mechanism, both heterogeneous classes, no invented fields.
package db

import "github.com/wso2/dc-api/internal/placement"

// Stamp returns the (region, zone) to write for a row of family f.
//
// inRegion/inZone are caller-supplied hints:
//   - inZone: a child's inherited zone (the parent VNet's zone), or "" when the
//     caller has none — Stamp then falls back to the local zone (zoneStamp).
//   - inRegion: used ONLY by VNet (RegionFromModel) where region comes from the
//     validated model value; every other family always stamps the local region.
//
// This single function reproduces all five legacy stamp shapes:
//   - VPC child (VM/Cluster/Bastion): Stamp(VM, "", res.Zone)
//     → (regionStamp(), res.Zone-or-zoneStamp()).
//   - VNet root:                       Stamp(VNet, v.Region, v.Zone)
//     → (v.Region, v.Zone-or-zoneStamp()).
//   - field-less root (KV/DB/PE):      Stamp(KeyVault, "", "")
//     → (regionStamp(), zoneStamp()).
//
// For a non-regional family it returns ("", ""); such families never bind these
// columns and must not call Stamp.
func (r *Repository) Stamp(f placement.Family, inRegion, inZone string) (region, zone string) {
	if !f.Regional {
		return "", ""
	}
	// Region: VNet trusts the model value (RegionFromModel); everyone else is
	// always-local — identical to every non-VNet region binding today.
	if f.RegionFromModel {
		region = inRegion
	} else {
		region = r.regionStamp()
	}
	// Zone: an inherited value wins, else the local-zone default — identical to
	// the two legacy patterns (child res.Zone-or-default, root empty-or-default).
	zone = inZone
	if zone == "" {
		zone = r.zoneStamp()
	}
	return region, zone
}
