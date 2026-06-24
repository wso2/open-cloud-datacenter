// Package placement is the single declarative source of truth for how each
// resource family acquires its (region, zone) pair — the "regional" property
// made declarative, the same way the capability registry made
// "agent-manageable" declarative.
//
// ── THE CONTRACT FOR NEW REGIONAL RESOURCE FAMILIES ──────────────────────────
//
// Before this package, the fact that a resource carries a region and a zone was
// hand-wired across ~11 files: the db layer stamped region/zone inline in every
// Create* method, the handlers copied region/zone from a parent VNet by hand,
// and the cross-region/cross-zone mismatch guard was open-coded in peering.go.
//
// Now a family's regional shape is ONE row in the var block below, consumed by
// shared code:
//
//   - db.Repository.Stamp(f, inRegion, inZone) resolves the (region, zone) to
//     write for any family, driven entirely by the Family's flags.
//   - placement.SamePlacement(...) reproduces the cross-region (400) /
//     cross-zone (422) guard.
//
// To make a new resource family regional: append one Family var here (and, if a
// child, name its ParentKind). No per-handler or per-db hand-wiring.
//
// This package is a LEAF — it imports nothing from the project (only std-lib
// behaviour via fmt) so both db and handlers can import it without an import
// cycle (db must never import handlers).
package placement

import "fmt"

// PlacementMode is how a family acquires its (region, zone).
type PlacementMode int

const (
	// Root families choose their own region/zone — they are default-stamped
	// from the control plane's local region/zone.
	Root PlacementMode = iota
	// Child families inherit region/zone from a named parent family.
	Child
)

// Family declares the regional shape of one resource family. The Kind matches
// the kind string used by the audit framework (auditedTable.kind).
type Family struct {
	// Kind is the family identifier, e.g. "VM", "VNET", "KEYVAULT".
	Kind string
	// Regional is whether this family carries/stamps region+zone at all.
	// Non-regional families have no region/zone columns and are never stamped.
	Regional bool
	// Mode is Root or Child. Ignored when !Regional.
	Mode PlacementMode
	// ParentKind, for Child families, is the Family.Kind it inherits from.
	ParentKind string
	// HasModelField is true when region/zone live on the Go model and round-trip;
	// false for write-only DB columns (KeyVault/Database/PrivateEndpoint have no
	// Region/Zone struct field).
	HasModelField bool
	// RegionFromModel is true ONLY for VNET: its region is taken from the model
	// (the validated request region), never from the local region stamp.
	RegionFromModel bool
}

// The per-family declarations. Adding a regional resource is appending one row.
//
// NOTE on Stamp keying: the shared Stamp helper keys on the flags below
// (RegionFromModel, Mode/inputs), NOT on Kind. VM/Cluster/Bastion share a single
// db Create, so any of those Family values is interchangeable there precisely
// because all three are Child/VNET/HasModelField/!RegionFromModel.
var (
	// ── Roots (choose their own placement) ───────────────────────────────────
	VNet     = Family{Kind: "VNET", Regional: true, Mode: Root, HasModelField: true, RegionFromModel: true}
	KeyVault = Family{Kind: "KEYVAULT", Regional: true, Mode: Root, HasModelField: false}
	// Database and PrivateEndpoint are conceptually VPC children, but TODAY they
	// unconditionally default-stamp region/zone and do NOT inherit from a parent
	// VNet. They are declared Root to preserve that exact behaviour. The
	// aspirational §13.1 parent-zone inheritance is deferred to step 3b; flipping
	// these to Child would silently change stamping from zoneStamp() to the
	// parent VNet's zone — do not do that here.
	Database        = Family{Kind: "DATABASE", Regional: true, Mode: Root, HasModelField: false}
	PrivateEndpoint = Family{Kind: "PRIVATE_ENDPOINT", Regional: true, Mode: Root, HasModelField: false}

	// ── Children (inherit region/zone from VNET) — resources-table families ──
	VM      = Family{Kind: "VM", Regional: true, Mode: Child, ParentKind: "VNET", HasModelField: true}
	Cluster = Family{Kind: "CLUSTER", Regional: true, Mode: Child, ParentKind: "VNET", HasModelField: true}
	Bastion = Family{Kind: "BASTION", Regional: true, Mode: Child, ParentKind: "VNET", HasModelField: true}

	// ── Non-regional families (no region/zone column) ───────────────────────
	// Declared for completeness/clarity. They inherit by FK containment and are
	// never stamped — their Create methods do not call Stamp.
	Subnet          = Family{Kind: "SUBNET", Regional: false}
	RouteTable      = Family{Kind: "ROUTE_TABLE", Regional: false}
	NSG             = Family{Kind: "NSG", Regional: false}
	NSGAttachment   = Family{Kind: "NSG_ATTACHMENT", Regional: false}
	Peering         = Family{Kind: "PEERING", Regional: false}
	PrivateDnsZone  = Family{Kind: "PRIVATE_DNS_ZONE", Regional: false}
	DnsRecord       = Family{Kind: "DNS_RECORD", Regional: false}
	RouteTableAssoc = Family{Kind: "ROUTE_TABLE_ASSOC", Regional: false}
)

// PlacementError carries the HTTP status and message for a placement mismatch,
// so callers (handlers) write the exact status/message the open-coded guard did.
type PlacementError struct {
	Status int
	Msg    string
}

func (e *PlacementError) Error() string { return e.Msg }

// SamePlacement reproduces the peering handler's two consecutive guards as one
// shared check, byte-identical in status and message:
//
//   - regions differ          → 400 "cross-region peering is not supported in M2 (...)"
//   - regions equal, zones differ → 422 "cross-zone peering is not supported (...)"
//   - otherwise               → nil
//
// Operand order matters: the messages list (a, b) — keep (vnet, peerVNet) at the
// call site or the rendered text flips.
func SamePlacement(aRegion, aZone, bRegion, bZone string) *PlacementError {
	if aRegion != bRegion {
		return &PlacementError{
			Status: 400,
			Msg: fmt.Sprintf("cross-region peering is not supported in M2 (VNet region: %s, peer VNet region: %s)",
				aRegion, bRegion),
		}
	}
	if aZone != bZone {
		return &PlacementError{
			Status: 422,
			Msg: fmt.Sprintf("cross-zone peering is not supported (VNet zone: %s, peer VNet zone: %s)",
				aZone, bZone),
		}
	}
	return nil
}
