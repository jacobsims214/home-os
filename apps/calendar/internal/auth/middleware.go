// Package auth provides Basic Authentication middleware for the CalDAV service.
// Clients authenticate using their email and a dedicated CalDAV app password
// (not their main account password). The app password is generated from the
// Home OS settings UI and stored as a bcrypt hash in the users table.
package auth

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"

	"home-os/calendar/internal/db"
	"home-os/calendar/internal/logging"
	"home-os/calendar/internal/metrics"

	"golang.org/x/crypto/bcrypt"
)

// caldavRepo is the subset of *db.Repo methods used by AuthMiddleware. Declaring
// it as an interface lets unit tests substitute a fake repo instead of needing
// a live PostgreSQL connection. *db.Repo satisfies this interface implicitly,
// so the production call site in main.go is unchanged.
type caldavRepo interface {
	GetUserByEmailForCalDAV(ctx context.Context, email string) (*db.CalDAVUser, error)
	GetHouseholdIDForUser(ctx context.Context, userID string) (string, error)
}

// contextKey is used for context value keys to avoid collisions.
type contextKey string

const (
	// HouseholdIDKey is the context key for the household_id injected by AuthMiddleware.
	HouseholdIDKey contextKey = "household_id"
	// UserIDKey is the context key for the user_id injected by AuthMiddleware.
	UserIDKey contextKey = "user_id"
	// EmailKey is the context key for the user's email injected by AuthMiddleware.
	EmailKey contextKey = "email"
)

// AuthMiddleware returns an HTTP middleware that validates CalDAV Basic Auth
// credentials against the users table's caldav_password_hash column.
//
// The username is the user's email address. The password is a CalDAV app
// password generated from the Home OS settings page (not the main account
// password). On success, the middleware injects household_id and user_id
// into the request context.
//
// Per-IP rate limiting (10 attempts per minute, golang.org/x/time/rate) is
// applied to every request that reaches the credential check. Bypass paths
// (health, .well-known, OPTIONS) skip both auth and the rate limiter.
// Consecutive failures are logged with the source IP and supplied email; a
// successful authentication resets the failure counter for that IP. See
// ratelimit.go for the limiter implementation.
//
// Every rejected credential is also recorded via metrics.RecordAuthFailure with
// a stable snake_case reason string (e.g. "missing_header", "bad_password").
// Rate-limit (429) and DB-error (500) paths do NOT record an auth failure —
// those are not credential rejections. Reason strings become Prometheus label
// series, so new reasons must be added deliberately to keep cardinality bounded.
func AuthMiddleware(repo caldavRepo) func(http.Handler) http.Handler {
	// The rate limiter is constructed once per server lifetime (AuthMiddleware
	// is called exactly once in main.go). Each request that reaches the auth
	// check consults the same ipRateLimiter instance, so per-IP token buckets
	// persist across requests. A background goroutine inside the limiter
	// evicts idle entries — see ratelimit.go.
	limiter := newIPRateLimiter()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Health endpoint and well-known redirect don't need auth.
			if r.URL.Path == "/health" || r.URL.Path == "/.well-known/caldav" || r.URL.Path == "/.well-known/carddav" {
				next.ServeHTTP(w, r)
				return
			}

			// OPTIONS requests never need auth — Apple Calendar uses them
			// for capability discovery before sending credentials.
			if r.Method == "OPTIONS" {
				next.ServeHTTP(w, r)
				return
			}

			// Per-IP rate limit: 10 auth attempts per minute (burst 10).
			// Bypass paths above (health, well-known, OPTIONS) do not
			// consume tokens — they don't reach here. This prevents a
			// flood of bad credentials from running bcrypt on every
			// request.
			ip := clientIP(r)
			if !limiter.allow(ip) {
				logging.Logger.Warn("caldav auth: rate limit exceeded",
					slog.String("ip", ip),
					slog.String("path", r.URL.Path))
				w.Header().Set("Retry-After", "6")
				http.Error(w, "Too many auth attempts", http.StatusTooManyRequests)
				return
			}

			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				count := limiter.recordFailure(ip)
				metrics.RecordAuthFailure("missing_header")
				logging.Logger.Warn("caldav auth: missing authorization header",
					slog.String("ip", ip),
					slog.Int("failures", count))
				w.Header().Set("WWW-Authenticate", `Basic realm="Home OS CalDAV"`)
				http.Error(w, "Authorization required", http.StatusUnauthorized)
				return
			}

			// Parse Basic Auth header.
			if !strings.HasPrefix(authHeader, "Basic ") {
				count := limiter.recordFailure(ip)
				metrics.RecordAuthFailure("non_basic")
				logging.Logger.Warn("caldav auth: non-basic authorization header",
					slog.String("ip", ip),
					slog.Int("failures", count))
				w.Header().Set("WWW-Authenticate", `Basic realm="Home OS CalDAV"`)
				http.Error(w, "Invalid authorization header", http.StatusUnauthorized)
				return
			}

			encoded := strings.TrimPrefix(authHeader, "Basic ")
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				count := limiter.recordFailure(ip)
				metrics.RecordAuthFailure("bad_base64")
				logging.Logger.Warn("caldav auth: invalid base64 in authorization header",
					slog.String("ip", ip),
					slog.Int("failures", count))
				w.Header().Set("WWW-Authenticate", `Basic realm="Home OS CalDAV"`)
				http.Error(w, "Invalid base64 encoding", http.StatusUnauthorized)
				return
			}

			credentials := strings.SplitN(string(decoded), ":", 2)
			if len(credentials) != 2 {
				count := limiter.recordFailure(ip)
				metrics.RecordAuthFailure("malformed_credentials")
				logging.Logger.Warn("caldav auth: malformed credentials",
					slog.String("ip", ip),
					slog.Int("failures", count))
				w.Header().Set("WWW-Authenticate", `Basic realm="Home OS CalDAV"`)
				http.Error(w, "Invalid credentials format", http.StatusUnauthorized)
				return
			}

			email := credentials[0]
			password := credentials[1]

			if email == "" || password == "" {
				count := limiter.recordFailure(ip)
				metrics.RecordAuthFailure("empty_credentials")
				logging.Logger.Warn("caldav auth: empty email or password",
					slog.String("ip", ip),
					slog.Int("failures", count))
				w.Header().Set("WWW-Authenticate", `Basic realm="Home OS CalDAV"`)
				http.Error(w, "Email and password required", http.StatusUnauthorized)
				return
			}

			// Look up user by email.
			user, err := repo.GetUserByEmailForCalDAV(r.Context(), email)
			if err != nil {
				logging.Logger.Error("caldav auth: db error",
					slog.String("email", email),
					slog.String("ip", ip),
					slog.String("error", err.Error()))
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			if user == nil {
				count := limiter.recordFailure(ip)
				metrics.RecordAuthFailure("unknown_user")
				if count >= failureLogThreshold {
					logging.Logger.Warn("caldav auth: repeated failures — unknown email",
						slog.String("ip", ip),
						slog.String("email", email),
						slog.Int("failures", count))
				} else {
					logging.Logger.Info("caldav auth: unknown email",
						slog.String("ip", ip),
						slog.String("email", email),
						slog.Int("failures", count))
				}
				w.Header().Set("WWW-Authenticate", `Basic realm="Home OS CalDAV"`)
				http.Error(w, "Invalid credentials", http.StatusUnauthorized)
				return
			}
			if user.CalDAVPasswordHash == nil || *user.CalDAVPasswordHash == "" {
				count := limiter.recordFailure(ip)
				metrics.RecordAuthFailure("no_caldav_password")
				logging.Logger.Warn("caldav auth: no caldav password set",
					slog.String("ip", ip),
					slog.String("email", email),
					slog.Int("failures", count))
				w.Header().Set("WWW-Authenticate", `Basic realm="Home OS CalDAV"`)
				http.Error(w, "No CalDAV password set. Generate one in Settings.", http.StatusUnauthorized)
				return
			}

			// Compare password against stored caldav_password_hash.
			if err := bcrypt.CompareHashAndPassword([]byte(*user.CalDAVPasswordHash), []byte(password)); err != nil {
				count := limiter.recordFailure(ip)
				metrics.RecordAuthFailure("bad_password")
				if count >= failureLogThreshold {
					logging.Logger.Warn("caldav auth: repeated password failures",
						slog.String("ip", ip),
						slog.String("email", email),
						slog.Int("failures", count))
				} else {
					logging.Logger.Info("caldav auth: invalid password",
						slog.String("ip", ip),
						slog.String("email", email),
						slog.Int("failures", count))
				}
				w.Header().Set("WWW-Authenticate", `Basic realm="Home OS CalDAV"`)
				http.Error(w, "Invalid credentials", http.StatusUnauthorized)
				return
			}

			// Successful authentication: reset the consecutive-failure
			// counter for this IP so a user who fat-fingered their
			// password a few times is back to a clean slate.
			limiter.recordSuccess(ip)

			// Get the user's household from their first membership.
			householdID, err := repo.GetHouseholdIDForUser(r.Context(), user.ID)
			if err != nil {
				logging.Logger.Error("caldav auth: household lookup error",
					slog.String("user_id", user.ID),
					slog.String("email", email),
					slog.String("ip", ip),
					slog.String("error", err.Error()))
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}

			// Inject household_id, user_id, and email into context.
			ctx := context.WithValue(r.Context(), HouseholdIDKey, householdID)
			ctx = context.WithValue(ctx, UserIDKey, user.ID)
			ctx = context.WithValue(ctx, EmailKey, email)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// HouseholdIDFromContext extracts the household_id injected by AuthMiddleware.
// Returns empty string if not found.
func HouseholdIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(HouseholdIDKey).(string); ok {
		return v
	}
	return ""
}

// UserIDFromContext extracts the user_id injected by AuthMiddleware.
// Returns empty string if not found.
func UserIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(UserIDKey).(string); ok {
		return v
	}
	return ""
}

// EmailFromContext extracts the email injected by AuthMiddleware.
// Returns empty string if not found.
func EmailFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(EmailKey).(string); ok {
		return v
	}
	return ""
}
