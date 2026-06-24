// Package handlers — placement.go
//
// Shared, table-driven region/zone inheritance for handlers. Before this file
// each of vm.go / cluster.go / bastion.go open-coded `childZone = vnet.Zone` on
// the VPC path; the rule (a Child family inherits its parent's region/zone) is
// now read from the placement registry instead of repeated per handler.
//
// The helper takes the ALREADY-FETCHED parent region/zone — the handlers fetch
// the parent VNet anyway for the ACTIVE / subnet-belongs-to checks, so there is
// exactly one GetVNet per request, identical to before.
package handlers

import "github.com/wso2/dc-api/internal/placement"

// inheritPlacement returns the (region, zone) a Child family seeds onto its row
// before repo.Create, given its already-resolved parent's region/zone.
//
//   - present=false (the legacy non-VPC VM/Cluster path, where there is no
//     parent) → returns ("", "") so repo.Create falls back to the local
//     default, identical to the old empty `vmZone`.
//   - a Root or non-regional family → ("", "") (never inherits).
//   - a Child family with a present parent → the parent's (region, zone).
//
// Region is returned for completeness but the resources table never writes
// res.Region today (Create always stamps the local region), so VM/Cluster/
// Bastion callers use only the zone — preserving exact current behaviour. The
// region return is reserved for a future child family whose Create binds an
// inherited region.
func inheritPlacement(f placement.Family, parentRegion, parentZone string, present bool) (region, zone string) {
	if !f.Regional || f.Mode != placement.Child || !present {
		return "", ""
	}
	return parentRegion, parentZone
}
