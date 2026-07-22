// Package middleware provides HTTP middleware for the calendar service that
// is not authentication-related — e.g. request body size limits.
package middleware

import "net/http"

// BodyLimitMiddleware returns an HTTP middleware that wraps every inbound
// request body with http.MaxBytesReader, capping the number of bytes a client
// is allowed to send. This prevents OOM-style DoS from oversized request
// bodies (e.g. a malicious client streaming a multi-gigabyte PUT to /dav/).
//
// When the limit is exceeded, the underlying http.MaxBytesReader causes the
// next Read on r.Body to return an error; the net/http machinery then aborts
// the request and writes a 413 Request Entity Too Large response to the
// client. Callers do not need to handle the error explicitly — the middleware
// only needs to install the limited reader before handing control to next.
//
// A maxBytes of 0 disables the limit (no wrapping is applied). Negative
// values are treated the same as 0 by the caller; this function does not
// validate them — validation is the config layer's responsibility.
func BodyLimitMiddleware(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if maxBytes > 0 && r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}
