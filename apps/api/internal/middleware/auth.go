package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"home-os/api/internal/auth"
	"home-os/api/internal/config"
	"home-os/api/pkg/apierr"
)

// claimsCtxKey is an unexported type used for context keys to prevent
// collisions with keys from other packages.
type claimsCtxKey string

const claimsKey claimsCtxKey = "claims"

// RequireAuth returns a chi-compatible middleware that validates Bearer tokens
// on protected routes. It first tries Dex OIDC (RS256) validation via
// auth.Verifier, and on failure falls back to the legacy HS256 validation.
//
// This dual-validation mode exists only during the OIDC migration transition.
// Once all clients have migrated to Dex-issued tokens (see Story 110 cleanup),
// the fallback path and this comment block should be removed.
func RequireAuth(verifier *auth.Verifier, cfg *config.Config) func(http.Handler) http.Handler {
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
				apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
					Error: apierr.ErrorDetail{
						Code:    "UNAUTHORIZED",
						Message: "missing authorization header",
					},
				})
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
					Error: apierr.ErrorDetail{
						Code:    "UNAUTHORIZED",
						Message: "invalid authorization header format",
					},
				})
				return
			}

			tokenStr := parts[1]

			// First try Dex OIDC (RS256) validation.
			claims, err := verifier.VerifyToken(r.Context(), tokenStr)
			if err == nil {
				ctx := context.WithValue(r.Context(), claimsKey, claims)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Fallback: try legacy HS256 validation (for transition).
			// TODO: Remove this fallback once all clients use Dex-issued tokens.
			oldClaims, oldErr := auth.OldVerifyToken(cfg, tokenStr)
			if oldErr == nil {
				slog.Debug("auth middleware: token validated via legacy HS256 fallback",
					"user_id", oldClaims.UserID)
				ctx := context.WithValue(r.Context(), claimsKey, oldClaims)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			slog.Warn("auth middleware: token verification failed (both methods)",
				"oidc_error", err, "legacy_error", oldErr)
			apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
				Error: apierr.ErrorDetail{
					Code:    "UNAUTHORIZED",
					Message: "invalid or expired token",
				},
			})
		})
	}
}

// RequireOIDC returns a chi-compatible middleware that validates Dex-issued
// OIDC Bearer tokens on protected routes. It extracts the Authorization header,
// parses the bearer token, verifies it via the auth.Verifier, and injects the
// resulting *auth.Claims into the request context.
//
// Returns a 401 JSON error response if the Authorization header is missing,
// does not use Bearer scheme, or the token is expired, tampered, or otherwise
// invalid.
func RequireOIDC(verifier *auth.Verifier) func(http.Handler) http.Handler {
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
				apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
					Error: apierr.ErrorDetail{
						Code:    "UNAUTHORIZED",
						Message: "missing authorization header",
					},
				})
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
					Error: apierr.ErrorDetail{
						Code:    "UNAUTHORIZED",
						Message: "invalid authorization header format",
					},
				})
				return
			}

			tokenStr := parts[1]
			claims, err := verifier.VerifyToken(r.Context(), tokenStr)
			if err != nil {
				slog.Warn("auth middleware: token verification failed", "error", err)
				apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
					Error: apierr.ErrorDetail{
						Code:    "UNAUTHORIZED",
						Message: "invalid or expired token",
					},
				})
				return
			}

			ctx := context.WithValue(r.Context(), claimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClaimsFromContext extracts the *auth.Claims that were injected by the
// RequireOIDC middleware. Returns nil if RequireOIDC was not used on the
// request or if the claims were not found in the context.
func ClaimsFromContext(ctx context.Context) *auth.Claims {
	if claims, ok := ctx.Value(claimsKey).(*auth.Claims); ok {
		return claims
	}
	return nil
}