package middleware

import (
	"context"
	"net/http"

	"home-os/api/internal/auth"
	"home-os/api/internal/db"
	"home-os/api/pkg/apierr"
)

// claimsCtxKey is an unexported type used for context keys to prevent
// collisions with keys from other packages.
type claimsCtxKey string

const claimsKey claimsCtxKey = "claims"

// RequireAuth returns a chi-compatible middleware that validates Dex-issued
// OIDC Bearer tokens on protected routes. It extracts the Authorization header,
// parses the bearer token, verifies it via the auth.Verifier, and injects the
// resulting *auth.Claims into the request context.
//
// When the verifier has a MemberResolver attached, the claims carry the Home OS
// users.id, household_id and role (see auth.PGMemberResolver for the lookup
// order and how users with several households are resolved).
//
// Household selection: a caller with several memberships picks one per
// request with the X-Household-ID header (a household UUID). It must be one of
// the caller's own memberships; instance admins get no implicit access. With
// no header the oldest membership is used. Agent tokens (hos_...) are not
// accepted by this API at all (401), so they cannot use the header to leave
// their bound household.
//
// Responses:
//   - 400 if X-Household-ID is present but not a UUID.
//   - 401 if the Authorization header is missing or malformed, or the token is
//     expired, tampered, or otherwise invalid.
//   - 403 FORBIDDEN if the token is valid but the identity is not a member of
//     any household, is not a member of the household named by X-Household-ID,
//     or the user is deactivated (users.active = false).
//   - 500 if the membership lookup itself fails.
func RequireAuth(verifier *auth.Verifier) func(http.Handler) http.Handler {
	return requireToken(verifier)
}

// RequireOIDC is an alias of RequireAuth, kept for existing callers.
func RequireOIDC(verifier *auth.Verifier) func(http.Handler) http.Handler {
	return requireToken(verifier)
}

func requireToken(verifier *auth.Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !verifier.Enabled() {
				// Local dev mode — inject anonymous identity.
				ctx := context.WithValue(r.Context(), claimsKey, &auth.Claims{
					UserID: "local-dev",
					Email:  "local-dev@homeos.local",
					Name:   "Local Dev",
				})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			tokenStr, errMsg := auth.BearerToken(r)
			if errMsg != "" {
				auth.WriteUnauthorized(w, errMsg)
				return
			}

			ctx, errMsg := auth.ContextWithHouseholdHeader(r)
			if errMsg != "" {
				apierr.BadRequest(w, errMsg)
				return
			}

			claims, err := verifier.VerifyToken(ctx, tokenStr)
			if err != nil {
				// One shared mapping: 401 bad token, 403 non-member (of any
				// or of the requested household) or deactivated, 500
				// membership lookup failure.
				auth.WriteVerifyError(w, err)
				return
			}

			ctx = WithClaims(r.Context(), claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClaimsFromContext extracts the *auth.Claims that were injected by the
// RequireAuth/RequireOIDC middleware. Returns nil if the middleware was not
// used on the request or if the claims were not found in the context.
func ClaimsFromContext(ctx context.Context) *auth.Claims {
	if claims, ok := ctx.Value(claimsKey).(*auth.Claims); ok {
		return claims
	}
	return nil
}

// WithClaims returns a copy of ctx carrying claims, as RequireAuth does for
// every authenticated request. It also makes claims.UserID the database actor
// (db.WithActorIDs) for claims.HouseholdID, so every write the request makes
// through a db.NewPool pool is attributed to that member by the 050
// authorship triggers (created_by/updated_by, activity_log.actor_user_id)
// and can raise 054 creator notifications. A UserID that is not a UUID (the
// auth-disabled "local-dev" identity) leaves the write anonymous.
//
// Tests that call handlers without a token verifier use it directly.
func WithClaims(ctx context.Context, claims *auth.Claims) context.Context {
	ctx = context.WithValue(ctx, claimsKey, claims)
	if claims != nil {
		ctx = db.WithActorIDs(ctx, claims.UserID, claims.HouseholdID)
	}
	return ctx
}
