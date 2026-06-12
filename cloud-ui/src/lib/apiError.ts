/**
 * listErrorMessage turns a react-query error from a list fetch into a clean,
 * human-readable line for an ErrorState.
 *
 * The RBAC gate returns 403 with body {"error":"insufficient permissions for
 * this action"}. Without this, list pages render that raw JSON — e.g.
 * `Failed to load service accounts: {"error":"insufficient permissions…"}`.
 * Here a permission denial becomes a plain sentence, and every other error is
 * unwrapped from the {"error":"…"} envelope so it reads cleanly too.
 *
 *   <ErrorState message={listErrorMessage(query.error, 'service accounts')} />
 *
 * Note: with the SideNav gated on read access, a user normally never reaches a
 * list they can't read. This is the backstop for a direct URL or a stale link.
 */
export function listErrorMessage(error: unknown, resource: string): string {
  const raw = error instanceof Error ? error.message : String(error);
  const detail = unwrapApiError(raw);
  if (/insufficient permissions/i.test(detail)) {
    return `You don't have permission to view ${resource}.`;
  }
  return `Failed to load ${resource}: ${detail}`;
}

/** Pull the message out of a {"error":"…"} JSON envelope; pass through otherwise. */
function unwrapApiError(raw: string): string {
  try {
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed.error === 'string') return parsed.error;
  } catch {
    /* not JSON — use the raw string */
  }
  return raw;
}

/**
 * detailErrorMessage is listErrorMessage's sibling for detail pages: unwrap
 * the {"error":"…"} envelope and classify the two common cases (missing row,
 * permission denial) into plain sentences. Detail pages hit "not found" far
 * more than lists do — stale deep links, deleted resources, activity-feed
 * history — so the raw envelope must never reach the screen.
 */
export function detailErrorMessage(error: unknown, resource: string): string {
  const raw = error instanceof Error ? error.message : String(error);
  const detail = unwrapApiError(raw);
  if (/not found|404/i.test(detail)) {
    return `This ${resource} no longer exists — it may have been deleted.`;
  }
  if (/insufficient permissions/i.test(detail)) {
    return `You don't have permission to view this ${resource}.`;
  }
  return detail;
}

