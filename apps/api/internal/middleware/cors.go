package middleware

import (
	"net/http"
	"strings"
)

// CORS returns a chi-compatible middleware that sets CORS headers.
//
// When credentials: 'include' is used by the browser, the server MUST respond
// with the exact request Origin (not '*') and include Access-Control-Allow-Credentials: true.
// This middleware always echoes the request Origin back when one is present.
//
// In dev mode (allowAll=true) any origin is accepted.
// In production (allowAll=false) only the configured allowedOrigin is accepted.
func CORS(allowAll bool, allowedOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			allowed := false
			if origin != "" {
				if allowAll {
					allowed = true
				} else if allowedOrigin != "" && matchOrigin(origin, allowedOrigin) {
					allowed = true
				}
			}

			if allowed {
				// Echo the exact origin back — required when credentials: 'include' is used.
				// Browsers block '*' when credentials mode is 'include'.
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
				w.Header().Set("Access-Control-Max-Age", "86400")
				// Vary header tells caches the response differs by Origin
				w.Header().Add("Vary", "Origin")
			}

			// Handle preflight — return 204 immediately, no body
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// matchOrigin checks if the request origin matches the allowed origin pattern.
// Supports exact match or subdomain wildcard (e.g. "*.example.com").
func matchOrigin(origin, allowed string) bool {
	if origin == allowed {
		return true
	}
	if strings.HasPrefix(allowed, "*.") {
		suffix := allowed[1:] // ".example.com"
		return strings.HasSuffix(origin, suffix)
	}
	return false
}
