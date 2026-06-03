import { useQuery } from '@tanstack/react-query';
import { useApi } from './useApi';

/**
 * useCan asks dc-api whether the caller may perform the given actions at the
 * tenant scope, and returns a `can(action)` predicate plus a loading flag.
 *
 * The server runs the authorization engine (POST /v1/tenants/{tid}/permissions:check)
 * and returns booleans; the UI only reads them, so it never re-implements the
 * matcher. One batched, cached request per (tenant, action-set) — pass every
 * action a screen gates on in a single call.
 *
 *   const { can } = useCan(tenantId, [ACTION_VNET_WRITE]);
 *   <Button disabled={!can(ACTION_VNET_WRITE)} ... />
 */
export function useCan(tenantId: string | undefined, actions: string[], projectId?: string) {
  const api = useApi();
  const query = useQuery({
    queryKey: ['permissions-check', tenantId, projectId ?? 'tenant', [...actions].sort().join(',')],
    enabled: Boolean(tenantId) && actions.length > 0,
    queryFn: async () => {
      // When a projectId is given, evaluate at PROJECT scope — the engine answers
      // "may I do X in THIS project", honouring both project-scope grants and
      // inherited tenant grants. Without it, tenant scope. This is why a project
      // Owner sees the full project even with only a narrow tenant role.
      if (projectId) {
        const { data, error } = await api.POST(
          '/v1/tenants/{tenant_id}/projects/{project_id}/permissions:check',
          { params: { path: { tenant_id: tenantId!, project_id: projectId } }, body: { actions } },
        );
        if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
        return data?.results ?? [];
      }
      const { data, error } = await api.POST('/v1/tenants/{tenant_id}/permissions:check', {
        params: { path: { tenant_id: tenantId! } },
        body: { actions },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data?.results ?? [];
    },
  });
  const results = query.data ?? [];
  return {
    can: (action: string) => results.some((r) => r.action === action && r.allowed),
    isLoading: query.isLoading,
  };
}
