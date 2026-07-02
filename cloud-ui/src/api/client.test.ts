import { afterEach, describe, expect, it, vi } from 'vitest';

/**
 * client.ts computes baseUrl at module scope from import.meta.env and keeps
 * the session-expiry latch as module state, so each test stubs the env and
 * dynamic-imports a fresh copy. An absolute VITE_API_BASE is required here:
 * in the browser a relative /v1 path resolves against the page origin, but
 * Node's Request constructor rejects relative URLs.
 */
async function freshClientModule() {
  vi.resetModules();
  vi.stubEnv('VITE_API_BASE', 'https://dcapi.test');
  return import('./client');
}

function stub401Fetch() {
  const fetchMock = vi.fn(async () =>
    new Response(JSON.stringify({ error: 'unauthenticated' }), {
      status: 401,
      headers: { 'Content-Type': 'application/json' },
    })
  );
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

describe('session-expiry middleware', () => {
  afterEach(() => {
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });

  it('notifies the auth layer exactly once for parallel 401s', async () => {
    const { makeApiClient, registerSessionExpiredHandler } = await freshClientModule();
    const onExpired = vi.fn();
    registerSessionExpiredHandler(onExpired);
    const fetchMock = stub401Fetch();

    const client = makeApiClient();
    await Promise.all([client.GET('/v1/tenants'), client.GET('/v1/tenants')]);

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(onExpired).toHaveBeenCalledTimes(1);
  });

  it('debounces across separately constructed clients', async () => {
    // RequireProject builds its own ad-hoc client next to the shared
    // ApiProvider one — a session expiry seen by both must still notify once.
    const { makeApiClient, registerSessionExpiredHandler } = await freshClientModule();
    const onExpired = vi.fn();
    registerSessionExpiredHandler(onExpired);
    stub401Fetch();

    const a = makeApiClient();
    const b = makeApiClient();
    await Promise.all([a.GET('/v1/tenants'), b.GET('/v1/tenants')]);

    expect(onExpired).toHaveBeenCalledTimes(1);
  });

  it('ignores 401 from the /v1/auth/* endpoints', async () => {
    // The /v1/auth/me probe legitimately 401s for signed-out visitors —
    // firing the handler there would loop the login flow.
    const { makeApiClient, registerSessionExpiredHandler } = await freshClientModule();
    const onExpired = vi.fn();
    registerSessionExpiredHandler(onExpired);
    stub401Fetch();

    const client = makeApiClient();
    await client.GET('/v1/auth/me');

    expect(onExpired).not.toHaveBeenCalled();
  });
});
