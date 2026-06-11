import { useEffect, useState, type ReactNode } from 'react';
import { AuthContext, type AuthContextValue, type AuthUser } from './context';

/**
 * BFF-aware auth provider for cloud-ui.
 *
 * The browser never holds an access token. dc-api owns the Asgardeo
 * token exchange and seals the session in an HttpOnly `dcapi_session`
 * cookie. Every fetch to dc-api must carry `credentials: 'include'`
 * so the cookie travels with the request — see src/api/client.ts.
 *
 * Sign-in / sign-out flow:
 *   login()  → window.location.assign('/v1/auth/login')
 *              → Asgardeo authorize → /v1/auth/callback
 *              → dc-api sets cookie → 302 back to cloud-ui origin
 *   logout() → POST /v1/auth/logout (cookie cleared, 302 to Asgardeo
 *              end-session, then back to cloud-ui /login)
 */

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;

    fetch('/v1/auth/me', { credentials: 'include' })
      .then((res) => {
        if (cancelled) return;
        if (res.ok) {
          return res.json().then((raw: {
            sub: string;
            email?: string;
            expires_at: string;
            is_admin: boolean;
          }) => {
            if (!cancelled) {
              setUser({
                sub: raw.sub,
                email: raw.email,
                expiresAt: raw.expires_at,
                isAdmin: raw.is_admin ?? false,
              });
            }
          });
        }
        // 401 = no valid session; treat as signed out.
        if (!cancelled) setUser(null);
      })
      .catch(() => {
        // Network failure during mount — treat as signed out.
        if (!cancelled) setUser(null);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, []); // intentionally empty — runs once on mount only

  const login = (returnTo?: string) => {
    const base = '/v1/auth/login';
    const url =
      returnTo ? `${base}?return_to=${encodeURIComponent(returnTo)}` : base;
    window.location.assign(url);
  };

  const logout = async () => {
    // Optimistically clear local user so the UI reflects signed-out
    // state immediately, even before the redirect completes.
    setUser(null);

    // POST triggers the HttpOnly cookie clear + Asgardeo end-session 302.
    // fetch follows the 302 automatically — the browser lands on Asgardeo
    // end-session which then redirects back to cloud-ui /login.
    await fetch('/v1/auth/logout', { method: 'POST', credentials: 'include' }).catch(() => {
      window.location.assign('/login');
    });
  };

  const value: AuthContextValue = { user, loading, login, logout };

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
