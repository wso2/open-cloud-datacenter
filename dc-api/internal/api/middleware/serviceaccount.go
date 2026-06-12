// Package middleware — serviceaccount.go
//
// ServiceAccountAuth is the second auth path introduced in M1.5 Chunk 5.
// It runs BEFORE the OIDC middleware in the CompositeAuth chain. Requests
// carrying a token that starts with "dcapi_sa_" are handled here exclusively;
// all other requests pass through untouched to the OIDC validator.
//
// ── Token format ──────────────────────────────────────────────────────────────
//
//	dcapi_sa_<lookup_id>_<secret>
//	           ^^^^^^^^^^^ ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
//	           12 chars     32 chars (random)
//	           stored plain (token_lookup_id column, indexed UNIQUE)
//	                        bcrypt-hashed (token_hash column)
//
// The lookup_id lets the middleware find the candidate row with a single
// indexed SELECT before running the expensive bcrypt comparison.  Without this
// two-part split the middleware would have to bcrypt-compare against every SA
// in the database (O(N) cost, ~100 ms per SA at DefaultCost=10).
//
// ── CompositeAuth ─────────────────────────────────────────────────────────────
//
// CompositeAuth chains multiple AuthValidator implementations. The first one
// that "claims" the request (sets ContextKeyPrincipalID in context) wins; the
// remaining validators are skipped. Claiming is detected by checking whether
// ContextKeyPrincipalID was set by the time the inner handler returns.
//
// Wiring in main.go:
//
//	saAuth  := middleware.NewServiceAccountAuth(repo, logger)
//	oidcAuth, _ := middleware.NewAuth(ctx, issuer, audience, cfg, repo)
//	composite := middleware.NewCompositeAuth(saAuth, oidcAuth)
//	// pass composite to NewRouter as AuthMiddleware
package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/api/respond"
	"github.com/wso2/dc-api/internal/audit"
	"github.com/wso2/dc-api/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// ── Constants ─────────────────────────────────────────────────────────────────

const (
	// ServiceAccountTokenPrefix is the mandatory prefix on all DC-API SA tokens.
	// Its presence is the signal that ServiceAccountAuth should handle the request.
	ServiceAccountTokenPrefix = "dcapi_sa_"

	// serviceAccountBcryptCost is the bcrypt work factor used when verifying
	// the token secret. Must match the cost used at token creation (Chunk 7).
	serviceAccountBcryptCost = bcrypt.DefaultCost // 10

	// saLookupIDLen is the number of plaintext characters that form the lookup_id.
	saLookupIDLen = 12
)

// ── ServiceAccountAuth ────────────────────────────────────────────────────────

// ServiceAccountAuth validates requests that carry a dcapi_sa_* bearer token.
// Implements AuthValidator — satisfies the same interface as *Auth and *TestModeAuth.
type ServiceAccountAuth struct {
	repo AuthRepo
	log  zerolog.Logger
}

// NewServiceAccountAuth constructs a ServiceAccountAuth.
// repo must implement GetServiceAccountByTokenLookupID and UpdateServiceAccountLastUsed.
func NewServiceAccountAuth(repo AuthRepo, log zerolog.Logger) *ServiceAccountAuth {
	return &ServiceAccountAuth{repo: repo, log: log}
}

// Validate is the Chi middleware entrypoint for SA auth.
//
// Flow:
//  1. Extract the bearer token. If missing or malformed → pass through (not our token).
//  2. If the token does NOT start with ServiceAccountTokenPrefix → pass through.
//  3. Parse lookup_id and secret from the token.
//  4. Look up the SA row by lookup_id (indexed).
//  5. bcrypt.CompareHashAndPassword(storedHash, secret).
//  6. On success: inject context keys and fire-and-forget last_used update.
//  7. On any failure specific to a dcapi_sa_ token: 401 immediately.
func (a *ServiceAccountAuth) Validate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenStr, err := bearerToken(r)
		if err != nil {
			// No bearer token at all — not our problem. Pass through to OIDC.
			next.ServeHTTP(w, r)
			return
		}

		if !strings.HasPrefix(tokenStr, ServiceAccountTokenPrefix) {
			// Not an SA token. Let the next validator (OIDC) handle it.
			next.ServeHTTP(w, r)
			return
		}

		// ── Parse the two-part SA token ──────────────────────────────────────
		// Expected format after stripping the prefix:
		//   <12-char lookup_id>_<32-char secret>
		body := strings.TrimPrefix(tokenStr, ServiceAccountTokenPrefix)
		// Split on the first underscore that separates lookup_id from secret.
		// The lookup_id itself may not contain underscores (it's hex/alphanumeric).
		sepIdx := strings.Index(body, "_")
		if sepIdx != saLookupIDLen {
			// Malformed token — wrong lookup_id length or missing separator.
			a.log.Warn().Str("token_prefix", ServiceAccountTokenPrefix).
				Msg("sa auth: malformed token (bad lookup_id length or missing separator)")
			respond.Error(w, http.StatusUnauthorized, "Unauthorized: invalid service account token format")
			return
		}
		lookupID := body[:sepIdx]
		secret := body[sepIdx+1:]
		if secret == "" {
			respond.Error(w, http.StatusUnauthorized, "Unauthorized: invalid service account token format")
			return
		}

		// ── DB lookup by lookup_id (O(1) indexed) ───────────────────────────
		sa, err := a.repo.GetServiceAccountByTokenLookupID(r.Context(), lookupID)
		if err != nil {
			a.log.Error().Err(err).Str("lookup_id", lookupID).
				Msg("sa auth: db lookup failed")
			respond.Error(w, http.StatusUnauthorized, "Unauthorized: internal error")
			return
		}
		if sa == nil {
			// No SA row with this lookup_id — invalid token.
			respond.Error(w, http.StatusUnauthorized, "Unauthorized: invalid service account token")
			return
		}

		// ── bcrypt verification ──────────────────────────────────────────────
		if err := bcrypt.CompareHashAndPassword([]byte(sa.TokenHash), []byte(secret)); err != nil {
			// bcrypt mismatch — wrong secret, possible token forgery.
			a.log.Warn().Str("sa_id", sa.ID.String()).Str("tenant_id", sa.TenantID).
				Msg("sa auth: bcrypt mismatch — invalid secret")
			respond.Error(w, http.StatusUnauthorized, "Unauthorized: invalid service account token")
			return
		}

		// ── Inject context keys ──────────────────────────────────────────────
		ctx := r.Context()
		ctx = context.WithValue(ctx, ContextKeyPrincipalType, models.PrincipalTypeServiceAccount)
		ctx = context.WithValue(ctx, ContextKeyPrincipalID, sa.ID.String())
		ctx = context.WithValue(ctx, ContextKeyTenantID, sa.TenantID)
		ctx = context.WithValue(ctx, ContextKeyIsAdmin, false) // SAs cannot be platform-admin in M1.5
		// ContextKeyUserID: set to sa.ID.String() for legacy compat — audit_events.actor_id expects a string here.
		ctx = context.WithValue(ctx, ContextKeyUserID, sa.ID.String())
		// Stamp the actor for the repository layer's automatic audit recording.
		ctx = audit.WithActor(ctx, sa.ID.String())

		// ── Fire-and-forget last_used update ────────────────────────────────
		// We do not block the request on this write. A failed update is only
		// an operational concern (stale last_used), not a security issue.
		// Capture sa.ID by value to avoid the goroutine holding a reference
		// to the sa pointer after this handler returns.
		saIDCopy := sa.ID
		go func() {
			updateCtx := context.Background()
			if err := a.repo.UpdateServiceAccountLastUsed(updateCtx, saIDCopy); err != nil {
				a.log.Warn().Err(err).Str("sa_id", saIDCopy.String()).
					Msg("sa auth: failed to update last_used (non-fatal)")
			}
		}()

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ── CompositeAuth ─────────────────────────────────────────────────────────────

// CompositeAuth chains multiple AuthValidator implementations. Each validator
// is given a chance to handle the request in order. A validator "claims" the
// request when it sets ContextKeyPrincipalID in the context; subsequent
// validators are then skipped.
//
// If no validator claims the request, the last validator's rejection path
// (typically an OIDC 401) is what the client receives.
//
// Usage:
//
//	composite := middleware.NewCompositeAuth(saAuth, oidcAuth)
//	r.Use(composite.Validate)
type CompositeAuth struct {
	validators []AuthValidator
}

// NewCompositeAuth returns a CompositeAuth that tries each validator in order.
// Panics if validators is empty (misconfiguration).
func NewCompositeAuth(validators ...AuthValidator) *CompositeAuth {
	if len(validators) == 0 {
		panic("middleware.NewCompositeAuth: at least one validator is required")
	}
	return &CompositeAuth{validators: validators}
}

// Validate implements AuthValidator. It tries each validator in sequence.
//
// Semantics: the first validator that "claims" the request (by setting
// ContextKeyPrincipalID in the context it forwards) wins — subsequent
// validators are skipped. A validator that doesn't claim (e.g. ServiceAccountAuth
// passing through a non-dcapi_sa_ token) calls next.ServeHTTP with the original
// context, and the next validator in the chain gets its turn.
//
// Implementation: each validator's `next` is a sentinel that, on call, checks
// whether ContextKeyPrincipalID is set. If yes, the chain short-circuits to the
// real handler. If no, the next validator runs.
func (c *CompositeAuth) Validate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.runChain(w, r, 0, next)
	})
}

// runChain calls validator[idx] with a sentinel that either short-circuits to
// `final` (when the validator claimed the request) or recurses into the next
// validator (when it passed through without claiming).
func (c *CompositeAuth) runChain(w http.ResponseWriter, r *http.Request, idx int, final http.Handler) {
	if idx >= len(c.validators) {
		// All validators passed without claiming — request is unauthenticated.
		// Should not happen in practice because the last validator (OIDC) always
		// either claims or rejects with 401. Defensive 401 here just in case.
		respond.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	sentinel := http.HandlerFunc(func(w2 http.ResponseWriter, r2 *http.Request) {
		// If the validator claimed the request (set the principal), forward to
		// the real handler. Otherwise try the next validator with the ORIGINAL
		// request (so a pass-through doesn't pollute the context for OIDC).
		if _, ok := r2.Context().Value(ContextKeyPrincipalID).(string); ok {
			final.ServeHTTP(w2, r2)
			return
		}
		c.runChain(w2, r, idx+1, final)
	})
	c.validators[idx].Validate(sentinel).ServeHTTP(w, r)
}
