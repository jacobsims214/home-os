package auth

import "context"

// claimsCtxKey is used for context keys to prevent collisions.
type claimsCtxKey string

// ClaimsContextKey is the context key used to store *Claims.
const ClaimsContextKey claimsCtxKey = "claims"

// ClaimsFromContext extracts the *Claims that were injected by the
// RequireAuth middleware. Returns nil if the claims were not found.
func ClaimsFromContext(ctx context.Context) *Claims {
	if claims, ok := ctx.Value(ClaimsContextKey).(*Claims); ok {
		return claims
	}
	return nil
}
