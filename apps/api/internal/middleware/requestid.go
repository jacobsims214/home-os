package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// contextKey is an unexported type used for context keys to avoid collisions.
type contextKey string

const requestIDKey contextKey = "request_id"

// RequestID is chi-compatible middleware that ensures every request has an
// X-Request-ID header. If the header is missing, a new UUIDv4 is generated.
// The request ID is also stored in the request context for downstream use.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-ID")
		if rid == "" {
			rid = uuid.New().String()
		}

		w.Header().Set("X-Request-ID", rid)
		ctx := context.WithValue(r.Context(), requestIDKey, rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetRequestID extracts the request ID from the context. Returns an empty
// string if RequestID middleware was not used.
func GetRequestID(ctx context.Context) string {
	if rid, ok := ctx.Value(requestIDKey).(string); ok {
		return rid
	}
	return ""
}
