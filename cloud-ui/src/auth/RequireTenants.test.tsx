import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { FluentProvider } from '@fluentui/react-components';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import RequireTenants from './RequireTenants';
import { AuthContext, type AuthContextValue, type AuthUser } from './context';
import { ApiContext } from '../api/context';
import type { ApiClient } from '../api/client';
import { wso2LightTheme } from '../theme/themes';

const ADMIN: AuthUser = {
  sub: 'admin-sub',
  expiresAt: '2099-01-01T00:00:00Z',
  isAdmin: true,
  tenants: [],
};

function authValue(user: AuthUser | null): AuthContextValue {
  return { user, loading: false, login: () => {}, logout: async () => {} };
}

/**
 * Fake API client whose GET('/v1/tenants') returns a fresh snapshot of
 * whatever `tenants` holds at call time. Growing the backing array and then
 * invalidating the query reproduces "a tenant was just registered" without
 * touching the real BFF. The snapshot (slice) matters: a real API hands back
 * a newly deserialized array each request, so react-query sees changed data
 * and re-renders — returning the same mutated reference would defeat its
 * structural sharing and mask the very transition under test.
 */
function makeFakeApi(tenants: unknown[]): ApiClient {
  return {
    GET: async (path: string) => {
      if (path === '/v1/tenants') {
        return { data: tenants.slice(), error: undefined, response: new Response() };
      }
      return { data: undefined, error: undefined, response: new Response() };
    },
  } as unknown as ApiClient;
}

function renderGate(client: ApiClient, queryClient: QueryClient) {
  return render(
    <FluentProvider theme={wso2LightTheme}>
      <QueryClientProvider client={queryClient}>
        <AuthContext.Provider value={authValue(ADMIN)}>
          <ApiContext.Provider value={client}>
            <MemoryRouter initialEntries={['/tenants/acme']}>
              <Routes>
                <Route element={<RequireTenants />}>
                  <Route path="/tenants/:tenantId" element={<div>TENANT DASHBOARD</div>} />
                </Route>
              </Routes>
            </MemoryRouter>
          </ApiContext.Provider>
        </AuthContext.Provider>
      </QueryClientProvider>
    </FluentProvider>
  );
}

describe('RequireTenants gate', () => {
  it('flips from the no-tenants screen to the Outlet when the tenants cache refreshes', async () => {
    // Shared mutable backing store: starts empty (admin, zero tenants).
    const tenants: unknown[] = [];
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

    renderGate(makeFakeApi(tenants), queryClient);

    // Empty → admin variant of NoTenantsPage, NOT the protected Outlet.
    expect(await screen.findByText('No tenants registered yet')).toBeInTheDocument();
    expect(screen.queryByText('TENANT DASHBOARD')).not.toBeInTheDocument();

    // Simulate a successful registration: a tenant now exists and the dialog
    // invalidates ['tenants'] — the same key this gate reads. No remount.
    tenants.push({ id: 'acme', name: 'Acme', roles: ['owner'] });
    await queryClient.invalidateQueries({ queryKey: ['tenants'] });

    // Gate must flip empty → ready in place and resolve its Outlet, so the
    // freshly registered tenant lands the user on the dashboard without a reload.
    await waitFor(() => {
      expect(screen.getByText('TENANT DASHBOARD')).toBeInTheDocument();
    });
    expect(screen.queryByText('No tenants registered yet')).not.toBeInTheDocument();
  });
});
