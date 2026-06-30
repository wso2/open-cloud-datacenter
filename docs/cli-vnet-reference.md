# dcctl VNet Reference

## Overview

VNets are the top-level networking boundary for tenant workloads. A VNet contains subnets, route tables, peerings, and security groups. VNets are regional and zonal — resources created within a VNet inherit the VNet's zone.

**Key constraints:**
- VNet names are unique within a tenant.
- Each VNet requires one or more RFC1918 address space CIDR blocks (max 5 in M2).
- Child resources (subnets, VMs, clusters) inherit the VNet's zone.
- VNet peering requires both VNets to be in the same zone (cross-zone peering not supported).

---

## dcctl vnet create

Create a new VNet.

**Usage:**
```bash
dcctl vnet create <name> --cidr <cidr> [--zone <zone>] [--description <text>]
```

**Required flags:**
- `<name>` — VNet name (1–63 characters; lowercase alphanumeric + hyphens; must start with a letter).
- `--cidr <cidr>` — Primary address space CIDR block (RFC1918; e.g. `10.0.0.0/16`). May be specified multiple times for up to 5 CIDRs. Each CIDR must not overlap reserved infrastructure ranges for the region.

**Optional flags:**
- `--zone <zone>` — Availability zone (e.g. `zone-1`, `zone-2`). Defaults to the control plane's local zone. Immutable after create. Child resources inherit this zone.
- `--description <text>` — Human-readable description (max 256 characters).

**Example:**
```bash
# Create a VNet in the default zone
dcctl vnet create prod-net --cidr 10.10.0.0/16

# Create a VNet in a specific zone with multiple address spaces
dcctl vnet create shared-net \
  --cidr 10.20.0.0/16 \
  --cidr 10.21.0.0/16 \
  --zone zone-2 \
  --description "Shared infrastructure VNet"
```

**Response:**
Returns `202 Accepted` with the VNet UUID and status `PENDING`. Poll the status with `dcctl vnet get <id>` until it reaches `ACTIVE` or `FAILED`.

```json
{
  "id": "a1b2c3d4-0000-0000-0000-000000000001",
  "name": "prod-net",
  "address_space": ["10.10.0.0/16"],
  "region": "lk",
  "zone": "zone-1",
  "status": "PENDING",
  "created_at": "2026-06-30T10:00:00Z"
}
```

---

## dcctl vnet list

List all VNets in the authenticated tenant.

**Usage:**
```bash
dcctl vnet list
```

**Output:**
Flat list of VNet objects with name, CIDR blocks, region, zone, status, and timestamps.

**Example:**
```bash
dcctl vnet list

# Output (formatted):
ID                                   NAME        CIDR              REGION  ZONE  STATUS
a1b2c3d4-0000-0000-0000-000000000001 prod-net    10.10.0.0/16      lk  zone-1  ACTIVE
b2c3d4e5-0000-0000-0000-000000000002 shared-net  10.20.0.0/16,...  lk  zone-2  ACTIVE
```

---

## dcctl vnet get

Retrieve details for a specific VNet.

**Usage:**
```bash
dcctl vnet get <vnet-id>
```

**Arguments:**
- `<vnet-id>` — UUID or name of the VNet.

**Output:**
Full VNet object including all address spaces, zone, status, and timestamps.

**Example:**
```bash
dcctl vnet get prod-net
dcctl vnet get a1b2c3d4-0000-0000-0000-000000000001
```

---

## dcctl vnet delete

Delete a VNet asynchronously.

**Usage:**
```bash
dcctl vnet delete <vnet-id>
```

**Arguments:**
- `<vnet-id>` — UUID or name of the VNet.

**Constraints:**
- VNet must have no active subnets, route tables, peerings, or NAT gateways.
- Returns `409 Conflict` if dependents exist. Delete them first.

**Response:**
Returns `202 Accepted`. Poll with `dcctl vnet get <id>` until the VNet is removed.

**Example:**
```bash
dcctl vnet delete prod-net

# Wait for deletion:
dcctl vnet get prod-net
# → 404 when complete
```

---

## Zone Selection and Inheritance

When you create a VNet with `--zone zone-1`, all child resources (subnets, VMs, clusters) created within that VNet inherit the zone. You cannot override the zone at the child resource level — the VNet's zone is authoritative.

**Example:**
```bash
# Create VNet in zone zone-1
dcctl vnet create zone-a-net --cidr 10.30.0.0/16 --zone zone-1

# Create subnet — inherits zone zone-1
dcctl subnet create zone-a-sub --vnet zone-a-net --cidr 10.30.0.0/24

# Create VM — inherits zone zone-1
dcctl vm create --name vm-01 --vnet zone-a-net --subnet zone-a-sub ...

# Verify all in same zone:
dcctl vnet get zone-a-net      # zone: zone-1
dcctl subnet get zone-a-sub    # zone: zone-1
dcctl vm get vm-01             # zone: zone-1
```

---

## Cross-Zone Peering Constraint

VNet peering is supported only between VNets in the **same zone**. Peering requests between VNets in different zones are rejected with:

```
Error: Both VNets must be in the same zone (cross-zone peering not supported)
```

To peer VNets across zones, create them in the same zone or wait for multi-zone peering support in a future release.

**Example:**
```bash
# Both VNets in zone-1 — peering works
dcctl vnet create net-a --cidr 10.30.0.0/16 --zone zone-1
dcctl vnet create net-b --cidr 10.31.0.0/16 --zone zone-1
dcctl vnet-peering create net-a --peer net-b  # ✓ OK

# VNets in different zones — peering fails
dcctl vnet create net-c --cidr 10.32.0.0/16 --zone zone-1
dcctl vnet create net-d --cidr 10.33.0.0/16 --zone zone-2
dcctl vnet-peering create net-c --peer net-d  # ✗ Error: different zones
```

---

## See Also

- `dcctl subnet --help` — subnet operations
- `dcctl vnet-peering --help` — VNet peering
- `dcctl security-group --help` — network security groups
- `dcctl route-table --help` — route table management
