import { useQuery } from '@tanstack/react-query';
import { useApi } from './useApi';

/**
 * Region health — GET /v1/regions.
 *
 * Any authenticated member can call the endpoint — it is not tenant- or
 * project-scoped. The interfaces below mirror the spec's Region / Zone /
 * AgentInfo schemas (and are kept as the component's source of truth even
 * though the path is now in the generated types).
 */

export type RegionStatus = 'up' | 'degraded' | 'down' | 'unknown';

export interface AgentInfo {
  version: string;
  /** RFC3339 timestamp of the agent's last heartbeat. */
  last_seen: string;
}

export interface Zone {
  name: string;
  status: RegionStatus;
  /** Null when no agent has ever registered for the zone. */
  agent: AgentInfo | null;
}

export interface Region {
  name: string;
  display_name: string | null;
  description: string | null;
  status: RegionStatus;
  zones: Zone[];
}

export interface RegionList {
  items: Region[];
}

/**
 * Maps a region/zone status to the dashboard's dot-colour bucket:
 * green (ok) for up, orange (warn) for degraded, red (bad) for down,
 * grey (unknown) for anything else.
 */
export function regionDotVariant(status: string): 'ok' | 'warn' | 'bad' | 'unknown' {
  switch (status) {
    case 'up':
      return 'ok';
    case 'degraded':
      return 'warn';
    case 'down':
      return 'bad';
    default:
      return 'unknown';
  }
}

/** Compact zone-health summary for a region row, e.g. "1/1 zones up". */
export function zoneSummary(zones: Zone[]): string {
  if (zones.length === 0) return 'no zones';
  const up = zones.filter((z) => z.status === 'up').length;
  return `${up}/${zones.length} zone${zones.length === 1 ? '' : 's'} up`;
}

export function useRegionsQuery() {
  const api = useApi();
  return useQuery({
    queryKey: ['regions'],
    staleTime: 30_000,
    refetchInterval: 60_000,
    queryFn: async (): Promise<RegionList> => {
      const { data, error } = await api.GET('/v1/regions');
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? { items: [] }) as RegionList;
    },
  });
}

// ── Zone inventory (admin-only) ──────────────────────────────────────────────
//
// Node counts and capacity are infrastructure-internal, so the API gates
// GET /v1/admin/regions/{region}/zones/{zone}/inventory to platform admins and
// the dashboard only renders it for them. The data is fetched live from the
// zone's dc-agent, so it is only meaningful when the zone is `up`.

/** One node's readiness and capacity. CPU is milli-cores, memory MiB. */
export interface InventoryNode {
  name: string;
  ready: boolean;
  cpu_allocatable_m: number;
  cpu_used_m: number;
  mem_allocatable_mb: number;
  mem_used_mb: number;
}

export interface ZoneInventory {
  nodes: InventoryNode[];
  vm_count: number;
}

/**
 * Live inventory for a zone. `enabled` should be (isAdmin && zone is up): a
 * down/unknown zone has no connected agent, so the call would 503.
 */
export function useZoneInventoryQuery(region: string, zone: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: ['zone-inventory', region, zone],
    enabled,
    staleTime: 30_000,
    refetchInterval: enabled ? 60_000 : false,
    queryFn: async (): Promise<ZoneInventory> => {
      const { data, error } = await api.GET('/v1/admin/regions/{region}/zones/{zone}/inventory', {
        params: { path: { region, zone } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? { nodes: [], vm_count: 0 }) as ZoneInventory;
    },
  });
}

/** Compact admin summary, e.g. "3 nodes · 12/48 vCPU · 18 VMs" (used/allocatable). */
export function inventorySummary(inv: ZoneInventory): string {
  const allocM = inv.nodes.reduce((s, n) => s + n.cpu_allocatable_m, 0);
  const usedM = inv.nodes.reduce((s, n) => s + n.cpu_used_m, 0);
  const vcpuAlloc = Math.round(allocM / 1000);
  const vcpuUsed = Math.round(usedM / 1000);
  const nodeWord = inv.nodes.length === 1 ? 'node' : 'nodes';
  const vmWord = inv.vm_count === 1 ? 'VM' : 'VMs';
  return `${inv.nodes.length} ${nodeWord} · ${vcpuUsed}/${vcpuAlloc} vCPU · ${inv.vm_count} ${vmWord}`;
}
