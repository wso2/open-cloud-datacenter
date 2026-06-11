import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useApi } from './useApi';

/**
 * Project activity feed — GET /v1/tenants/{tenant_id}/projects/{project_id}/activity.
 *
 * The endpoint ships in dc-api/openapi.yaml on this same branch, so the call
 * uses the generated path + typed query params directly (`pnpm gen:api` runs
 * on predev/prebuild). The interfaces below mirror the spec's ActivityEvent /
 * ActivityPage schemas for consumers that want a stable local name.
 */

export interface ActivityEvent {
  id: string;
  /** Absent once the resource has been deleted — only deep-link when present. */
  resource_id?: string;
  resource_name: string;
  resource_type: string;
  action: string;
  actor_id: string;
  from_status?: string;
  to_status?: string;
  message?: string;
  created_at: string;
}

export interface ActivityList {
  items: ActivityEvent[];
  total: number;
}

/**
 * Maps an ActivityEvent.resource_type to the project-scoped route segment
 * for deep-linking (`../<route>/<resource_id>`). Types not listed here
 * (or no longer routable) render as plain text.
 *
 * Keys are the spec's ActivityEvent.resource_type enum — the stored
 * resources.type values (VIRTUAL_MACHINE, CLUSTER, VOLUME, BASTION), NOT
 * lowercase short names. VOLUME has no detail page yet (M2 storage is in
 * flight), so it deliberately renders as plain text.
 */
export const ACTIVITY_RESOURCE_ROUTES: Record<string, string> = {
  VIRTUAL_MACHINE: 'vms',
  CLUSTER: 'clusters',
  BASTION: 'bastions',
};

export function useActivityQuery(
  tenantId: string | undefined,
  projectId: string | undefined,
  limit: number,
  offset: number,
) {
  const api = useApi();
  return useQuery({
    queryKey: ['activity', tenantId, projectId, limit, offset],
    enabled: Boolean(tenantId) && Boolean(projectId),
    refetchInterval: 30_000,
    // Keep the previous page rendered while the next page loads so
    // pagination doesn't flash the loading state.
    placeholderData: keepPreviousData,
    queryFn: async (): Promise<ActivityList> => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/activity',
        {
          params: {
            path: { tenant_id: tenantId!, project_id: projectId! },
            query: { limit, offset },
          },
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data ?? { items: [], total: 0 };
    },
  });
}
