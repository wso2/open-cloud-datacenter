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
  const fetchMock = vi.fn(
    async () =>
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

describe('notifySessionExpiredOn401 (raw-fetch callers)', () => {
  afterEach(() => {
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });

  it('fires once for repeated 401s on session-scoped paths', async () => {
    // MembersPage's scopedJson bypasses the typed client, so it calls this
    // helper directly with the relative request path.
    const { notifySessionExpiredOn401, registerSessionExpiredHandler } = await freshClientModule();
    const onExpired = vi.fn();
    registerSessionExpiredHandler(onExpired);

    notifySessionExpiredOn401('/v1/tenants/t/projects/p/virtual-machines/vm/role-assignments', 401);
    notifySessionExpiredOn401('/v1/tenants/t/projects/p/virtual-machines/vm/role-assignments', 401);

    expect(onExpired).toHaveBeenCalledTimes(1);
  });

  it('ignores non-401 statuses, /v1/auth/* and non-API paths', async () => {
    const { notifySessionExpiredOn401, registerSessionExpiredHandler } = await freshClientModule();
    const onExpired = vi.fn();
    registerSessionExpiredHandler(onExpired);

    notifySessionExpiredOn401('/v1/tenants', 403);
    notifySessionExpiredOn401('/v1/auth/me', 401);
    notifySessionExpiredOn401('/healthz', 401);

    expect(onExpired).not.toHaveBeenCalled();
  });

  it('shares the once-latch with the typed-client middleware', async () => {
    // A 401 seen by the middleware and one seen by a raw fetch are the same
    // session expiry — the auth layer must still hear about it exactly once.
    const { makeApiClient, notifySessionExpiredOn401, registerSessionExpiredHandler } =
      await freshClientModule();
    const onExpired = vi.fn();
    registerSessionExpiredHandler(onExpired);
    stub401Fetch();

    await makeApiClient().GET('/v1/tenants');
    notifySessionExpiredOn401('/v1/tenants/t/role-assignments', 401);

    expect(onExpired).toHaveBeenCalledTimes(1);
  });

  it('re-arms the once-latch on re-registration', async () => {
    // AuthProvider re-registers on remount; the latch must reset so the
    // next session expiry notifies the fresh handler.
    const { notifySessionExpiredOn401, registerSessionExpiredHandler } = await freshClientModule();
    const first = vi.fn();
    registerSessionExpiredHandler(first);
    notifySessionExpiredOn401('/v1/tenants', 401);

    const second = vi.fn();
    registerSessionExpiredHandler(second);
    notifySessionExpiredOn401('/v1/tenants', 401);

    expect(first).toHaveBeenCalledTimes(1);
    expect(second).toHaveBeenCalledTimes(1);
  });

  it('is a no-op after the handler is detached with null', async () => {
    // AuthProvider's unmount cleanup passes null — a late 401 must neither
    // throw nor reach the stale handler.
    const { notifySessionExpiredOn401, registerSessionExpiredHandler } = await freshClientModule();
    const onExpired = vi.fn();
    registerSessionExpiredHandler(onExpired);
    registerSessionExpiredHandler(null);

    expect(() => notifySessionExpiredOn401('/v1/tenants', 401)).not.toThrow();
    expect(onExpired).not.toHaveBeenCalled();
  });
});
