// Package middleware — tenant_context.go
//
// TenantContext is the per-request middleware mounted on every
// /v1/tenants/{tenant_id}/... route. It reads tenant_id from the URL,
// resolves it to the immutable tenant_uuid, validates the caller has access,
// and injects both ContextKeyTenantID and ContextKeyTenantUUID into the
// request context so downstream handlers can read the active tenant exactly
// as they did before this refactor.
//
// Why this is its own middleware (not inlined into Auth):
//   - Auth runs once per request before any router branching, so it has no
//     access to chi URL params; the tenant_id is only known after the URL
//     dispatcher routes the request.
//   - Separating "who you are" (Auth) from "which tenant are you acting on"
//     (TenantContext) is the model the M5 scope hierarchy wants — adding
//     subscription/resource_group context later is purely additive.
package middleware

import (
	"context"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/wso2/dc-api/internal/api/respond"
	"github.com/wso2/dc-api/internal/models"
)

// tenantIDPattern matches the canonical tenant slug — mirrors the
// `^[a-z][a-z0-9-]{0,30}[a-z0-9]$` pattern declared in openapi.yaml on the
// `tenant_id` path parameter. Validated here so a non-compliant client (or
// a malformed URL) doesn't get a 500 from the slug → tenant_uuid DB lookup
// (Postgres rejects null bytes and other non-text characters in TEXT
// columns; the slug input would surface as "Internal Server Error").
var tenantIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)

// TenantContext validates the URL tenant_id against the authenticated
// principal's permissions and injects both the slug and the canonical
// tenant_uuid into the request context.
//
// Validation rules:
//   - Tenant must exist in the `tenants` registry — applies uniformly to
//     admins, users, and service accounts. Returning 404 (rather than 403
//     or silently allowing) means a deleted-then-recycled slug can never
//     inherit orphan rows from the old tenant: those rows still carry the
//     old UUID and become invisible.
//   - Platform admins (is_admin=true): allowed on any registered tenant.
//   - Service accounts: allowed only on their bound tenant. The
//     ServiceAccountAuth middleware pre-sets ContextKeyTenantID from
//     sa.TenantID; if it doesn't match the URL, deny.
//   - Users: must have a role_assignments row (any role) for the URL
//     tenant_id.
//
// HTTP status codes:
//   - 400 — tenant_id missing/empty in URL (router misconfiguration)
//   - 401 — no principal in context (Auth didn't run or didn't set it)
//   - 403 — caller is in the IdP tenant group but has no role row yet
//     (autoprovision disabled, awaiting owner invite)
//   - 404 — tenant slug not registered, OR caller has no IdP affinity and
//     no role row for the tenant. Returning 404 here avoids leaking
//     tenant existence across tenant boundaries.
//   - 500 — DB lookup failed
type TenantContext struct {
	repo AuthRepo
}

// NewTenantContext constructs a TenantContext middleware.
// repo is used to resolve slug→uuid and look up role_assignments.
func NewTenantContext(repo AuthRepo) *TenantContext {
	return &TenantContext{repo: repo}
}

// Validate is the Chi middleware function.
// Usage: r.Route("/v1/tenants/{tenant_id}", func(r chi.Router) { r.Use(tc.Validate); ... })
func (t *TenantContext) Validate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urlTenant := chi.URLParam(r, "tenant_id")
		if urlTenant == "" {
			respond.Error(w, http.StatusBadRequest, "Bad Request: tenant_id required in URL")
			return
		}
		if !tenantIDPattern.MatchString(urlTenant) {
			respond.Error(w, http.StatusBadRequest, "Bad Request: tenant_id must match ^[a-z][a-z0-9-]{0,30}[a-z0-9]$")
			return
		}

		if t.repo == nil {
			log.Error().Msg("tenant_context: repo is nil — refusing tenant-scoped request")
			respond.Error(w, http.StatusInternalServerError, "Internal Server Error")
			return
		}

		// Resolve slug → tenant_uuid up front. A missing row means the slug
		// is not a registered tenant: 404 for everyone (admins included),
		// so a re-registered slug must go through POST /v1/admin/tenants
		// before any tenant-scoped request will succeed.
		tenantUUID, err := t.repo.GetTenantUUIDBySlug(r.Context(), urlTenant)
		if err != nil {
			log.Error().Err(err).Str("tenant", urlTenant).
				Msg("tenant_context: tenant uuid lookup failed")
			respond.Error(w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		if tenantUUID == uuid.Nil {
			respond.Error(w, http.StatusNotFound, "tenant not found")
			return
		}

		// Helper to inject both keys and dispatch.
		dispatch := func(ctx context.Context) {
			ctx = context.WithValue(ctx, ContextKeyTenantID, urlTenant)
			ctx = context.WithValue(ctx, ContextKeyTenantUUID, tenantUUID)
			next.ServeHTTP(w, r.WithContext(ctx))
		}

		// Platform admin bypass — admins act across all registered tenants.
		if IsAdminFromContext(r.Context()) {
			dispatch(r.Context())
			return
		}

		pType, pID, ok := PrincipalFromContext(r.Context())
		if !ok {
			respond.Error(w, http.StatusUnauthorized, "Unauthorized: no principal in context")
			return
		}

		// Service accounts are bound to a single tenant at creation time.
		// The SA auth middleware already injected ContextKeyTenantID; if the
		// URL points elsewhere we return 404 (not 403) so we don't leak
		// the existence of other tenants to a confused or malicious caller.
		if pType == models.PrincipalTypeServiceAccount {
			saTenant, _ := TenantFromContext(r.Context())
			if saTenant != urlTenant {
				respond.Error(w, http.StatusNotFound, "tenant not found")
				return
			}
			dispatch(r.Context())
			return
		}

		// Users: require a role_assignments row for this tenant.
		assignments, err := t.repo.ListRoleAssignmentsForPrincipal(
			r.Context(), pType, pID,
		)
		if err != nil {
			log.Error().Err(err).Str("principal", pID).Str("tenant", urlTenant).
				Msg("tenant_context: list role assignments failed")
			respond.Error(w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		for _, a := range assignments {
			if a.ScopeType == models.ScopeTypeTenant && a.ScopeID == urlTenant {
				dispatch(r.Context())
				return
			}
		}

		// Distinguish "in IdP group but no role row" (actionable 403 — owner
		// must invite) from "no IdP affinity to this tenant" (404 — don't
		// leak whether the tenant exists).
		for _, t := range IdPTenantsFromContext(r.Context()) {
			if t == urlTenant {
				respond.Error(w, http.StatusForbidden,
					"no membership in tenant "+urlTenant+"; ask an owner to invite you")
				return
			}
		}
		respond.Error(w, http.StatusNotFound, "tenant not found")
	})
}
