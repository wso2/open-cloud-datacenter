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
