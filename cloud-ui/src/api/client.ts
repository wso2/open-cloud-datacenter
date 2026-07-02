import createClient, { type Middleware } from 'openapi-fetch';
import type { paths } from './generated/types';

// Empty default → relative paths → caught by the Vite dev proxy in
// vite.config.ts (which forwards /v1/* to the deployed dc-api).
// In production, set VITE_API_BASE to the full dc-api host
// (e.g. https://dcapi.lk.internal.wso2.com) so calls go cross-origin.
const DEFAULT_BASE_URL = '';

const baseUrl =
  (import.meta.env.VITE_API_BASE as string | undefined)?.replace(/\/$/, '') ?? DEFAULT_BASE_URL;

/**
 * Session-expiry notification between the API client and the auth layer.
 *
 * client.ts must stay React-free, so instead of touching AuthContext the
 * auth provider registers a callback here on mount. When any /v1/* call
 * (except /v1/auth/*) comes back 401 mid-session, the handler fires and
 * AuthProvider flips to signed-out — RequireAuth then redirects to /login.
 *
 * The latch is module-level (shared by every client instance) and fires
 * once: when a session expires, every in-flight query 401s at the same
 * time and the handler must not run N times. It never re-arms itself —
 * re-login is a full-page navigation through /v1/auth/login, which resets
 * module state anyway. Registering a handler resets the latch (tests, or
 * a remounted AuthProvider).
 */
type SessionExpiredHandler = () => void;

let sessionExpiredHandler: SessionExpiredHandler | null = null;
let sessionExpiryNotified = false;

export function registerSessionExpiredHandler(handler: SessionExpiredHandler | null) {
  sessionExpiredHandler = handler;
  sessionExpiryNotified = false;
}

/**
 * Shared 401 check for anything that talks to the API outside the typed
 * client (e.g. the dynamic resource-scope calls in MembersPage). Same
 * rules as the middleware: session-scoped /v1/* paths only, /v1/auth/*
 * excluded, fires the registered handler once.
 */
export function notifySessionExpiredOn401(pathname: string, status: number) {
  if (status !== 401) return;
  // Only session-scoped API calls count. /v1/auth/* is excluded: the
  // /v1/auth/me probe legitimately 401s for signed-out visitors, and
  // login/logout/callback must never re-enter the handler (loop risk).
  if (!pathname.startsWith('/v1/') || pathname.startsWith('/v1/auth/')) return;

  if (sessionExpiryNotified) return;
  sessionExpiryNotified = true;
  sessionExpiredHandler?.();
}

const sessionExpiryMiddleware: Middleware = {
  async onResponse({ request, response }) {
    notifySessionExpiredOn401(new URL(request.url).pathname, response.status);
  },
};

/**
 * Typed DC-API client.
 *
 * Authentication uses the dc-api BFF cookie flow: every request carries
 * `credentials: 'include'` so the HttpOnly `dcapi_session` cookie
 * travels to dc-api, where the auth middleware extracts and validates
 * the sealed access token.
 */
export function makeApiClient() {
  const client = createClient<paths>({ baseUrl });

  const credentialsMiddleware: Middleware = {
    async onRequest({ request }) {
      // Cookie-based auth: the browser sends the dcapi_session cookie
      // automatically when credentials:include is set on the fetch call.
      // openapi-fetch doesn't expose a direct `credentials` option on the
      // client constructor, so we clone the request with the flag set.
      // The clone is required because Request objects are immutable.
      return new Request(request, { credentials: 'include' });
    },
  };
  client.use(credentialsMiddleware);
  client.use(sessionExpiryMiddleware);

  return client;
}

export type ApiClient = ReturnType<typeof makeApiClient>;
