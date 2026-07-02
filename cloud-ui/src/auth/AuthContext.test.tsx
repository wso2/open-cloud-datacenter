import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import { AuthProvider } from './AuthContext';
import { useAuth } from './useAuth';

/**
 * AuthProvider owns the user-facing half of session-expiry handling: it
 * registers a handler with the API client that flips `user` to null
 * (sending the router through RequireAuth to /login) and detaches it on
 * unmount. client.test.ts proves the client-side notification mechanism;
 * these tests prove the hop that makes it visible to users. The client
 * module is mocked so the registered handler can be captured and driven
 * directly.
 */
const { registrations } = vi.hoisted(() => ({
  registrations: [] as Array<(() => void) | null>,
}));

vi.mock('../api/client', () => ({
  registerSessionExpiredHandler: (handler: (() => void) | null) => {
    registrations.push(handler);
  },
}));

function WhoAmI() {
  const { user, loading } = useAuth();
  if (loading) return <div>loading</div>;
  return <div>{user ? user.sub : 'signed-out'}</div>;
}

function stubSignedInMeFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({ sub: 'user-1', expires_at: '2027-01-01T00:00:00Z', is_admin: false }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        )
    )
  );
}

describe('AuthProvider session-expiry wiring', () => {
  beforeEach(() => {
    registrations.length = 0;
    stubSignedInMeFetch();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('flips the app to signed-out when the registered handler fires', async () => {
    render(
      <AuthProvider>
        <WhoAmI />
      </AuthProvider>
    );
    expect(await screen.findByText('user-1')).toBeInTheDocument();

    // The mid-session 401 path: the client detects expiry and invokes the
    // handler AuthProvider registered on mount.
    const handler = registrations.at(-1);
    expect(handler).toBeTypeOf('function');
    act(() => handler!());

    expect(screen.getByText('signed-out')).toBeInTheDocument();
  });

  it('detaches the handler on unmount', async () => {
    const { unmount } = render(
      <AuthProvider>
        <WhoAmI />
      </AuthProvider>
    );
    await screen.findByText('user-1');

    unmount();

    expect(registrations.at(-1)).toBeNull();
  });
});
