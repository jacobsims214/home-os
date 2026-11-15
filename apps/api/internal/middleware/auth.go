package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"home-os/api/internal/auth"
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
// Responses:
//   - 401 if the Authorization header is missing or malformed, or the token is
//     expired, tampered, or otherwise invalid.
//   - 403 FORBIDDEN if the token is valid but the identity is not a member of
//     any household.
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

			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				unauthorized(w, "missing authorization header")
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				unauthorized(w, "invalid authorization header format")
				return
			}

			claims, err := verifier.VerifyToken(r.Context(), parts[1])
			switch {
			case err == nil:
			case errors.Is(err, auth.ErrNoMembership):
				slog.Warn("auth middleware: authenticated identity has no household membership", "error", err)
				apierr.JSON(w, http.StatusForbidden, apierr.ErrorResponse{
					Error: apierr.ErrorDetail{
						Code:    "FORBIDDEN",
						Message: "authenticated, but this account is not a member of any household",
					},
				})
				return
			case errors.Is(err, auth.ErrMemberLookup):
				slog.Error("auth middleware: membership lookup failed", "error", err)
				apierr.JSON(w, http.StatusInternalServerError, apierr.ErrorResponse{
					Error: apierr.ErrorDetail{
						Code:    "INTERNAL_ERROR",
						Message: "could not resolve household membership",
					},
				})
				return
			default:
				slog.Warn("auth middleware: token verification failed", "error", err)
				unauthorized(w, "invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), claimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func unauthorized(w http.ResponseWriter, msg string) {
	apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
		Error: apierr.ErrorDetail{Code: "UNAUTHORIZED", Message: msg},
	})
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
