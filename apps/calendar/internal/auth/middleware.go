// Package auth provides Basic Authentication middleware for the CalDAV service.
// Clients authenticate using their email and a dedicated CalDAV app password
// (not their main account password). The app password is generated from the
// Home OS settings UI and stored as a bcrypt hash in the users table.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

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
	ListHouseholdIDsForUser(ctx context.Context, userID string) ([]string, error)
	GetCalendarHouseholdID(ctx context.Context, caldavUID string) (string, error)
}

// appPasswordCost is the bcrypt cost of stored CalDAV app password hashes
// (apps/api/internal/auth.DefaultCost). The dummy hash uses the same cost so a
// rejected unknown/deactivated/no-password login takes as long as a wrong
// password for a real account.
const appPasswordCost = 12

// compareHash is bcrypt.CompareHashAndPassword, held in a variable so tests can
// count calls and see which hash each rejection compared against. Production
// code never reassigns it.
var compareHash = bcrypt.CompareHashAndPassword

// dummyHash is a bcrypt hash of a random secret nobody knows, generated once
// per process at appPasswordCost. Logins that have no stored hash to check
// (unknown email, deactivated account, no app password) compare against it
// so every credential rejection costs one bcrypt of the same cost.
//
// It is a variable so the package's tests can swap in a cheap hash; they
// check newDummyHash's cost separately.
var dummyHash = sync.OnceValue(newDummyHash)

// newDummyHash returns a bcrypt hash, at appPasswordCost, of 32 random bytes
// that are then discarded.
func newDummyHash() []byte {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic("caldav auth: generate dummy hash secret: " + err.Error())
	}
	h, err := bcrypt.GenerateFromPassword([]byte(base64.RawStdEncoding.EncodeToString(secret)), appPasswordCost)
	if err != nil {
		panic("caldav auth: generate dummy hash: " + err.Error())
	}
	return h
}

// invalidCredentials writes the one response every credential rejection after
// the user lookup gets (unknown email, deactivated account, no app password,
// wrong password): same status, headers and body, so the response can't tell
// a caller which case it hit.
func invalidCredentials(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Home OS CalDAV"`)
	http.Error(w, "Invalid credentials", http.StatusUnauthorized)
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
	// HouseholdIDsKey is the context key for every household the user belongs
	// to ([]string, default household first).
	HouseholdIDsKey contextKey = "household_ids"
	// HouseholdScopeKey is the context key for the household named in a
	// /dav/households/<id>/ request path. Empty for unscoped requests.
	HouseholdScopeKey contextKey = "household_scope"
)

// householdsPrefix is the path prefix for household-scoped CalDAV URLs:
// /dav/households/<household_id>/... is served exactly like /dav/... but
// against that one household.
const householdsPrefix = "/dav/households/"

// AuthMiddleware returns an HTTP middleware that validates CalDAV Basic Auth
// credentials against the users table's caldav_password_hash column.
//
// The username is the user's email address. The password is a CalDAV app
// password generated from the Home OS settings page (not the main account
// password). On success, the middleware injects household_id and user_id
// into the request context.
//
// Deactivated users (users.active = false) get 401 on every authenticated
// path, including /dav/households/<id>/..., even with a previously valid app
// password.
//
// Account enumeration: once the header parses, an unknown email, a
// deactivated account, an account with no app password and a wrong password
// all get the identical 401 (same status, WWW-Authenticate header and
// "Invalid credentials" body), and all four run exactly one bcrypt compare at
// the stored hashes' cost: the first three against dummyHash, the last
// against the user's hash. Neither the body nor the response time tells a
// caller whether the email is an account or has an app password. The metrics
// reason labels and log lines still distinguish the cases; they are
// server-side only.
//
// Households: a user's default household is their oldest membership (see
// db.ListHouseholdIDsForUser), so the same user always lands in the same
// household. Users in several families can address any of them with
// /dav/households/<id>/...; the prefix is stripped before the CalDAV handler
// runs, and the full membership list is available via HouseholdIDsFromContext
// so discovery can list every household's calendars.
//
// Rate limiting (golang.org/x/time/rate, see ratelimit.go) runs before any
// bcrypt work. With DefaultRateLimitConfig: each client IP gets 120 requests
// per minute (burst 60) through the credential check, 10 failed password
// checks per minute (burst 10), and each account email 30 failed password
// checks per hour (burst 30) across all IPs. An exhausted bucket gets 429
// with Retry-After, without a user lookup or bcrypt compare. Only failures
// that ran bcrypt (unknown email, deactivated, no app password, wrong
// password) spend failure tokens; a missing or malformed header does not. A
// successful login refills that account's failure bucket and resets the IP's
// consecutive-failure count (used for log severity), but not the IP's
// failure bucket. Bypass paths (health, .well-known, OPTIONS) skip both auth
// and the limiter. The client IP is the TCP peer unless the peer is in
// RateLimitConfig.TrustedProxies; then it is the rightmost untrusted
// X-Forwarded-For hop (see clientIP), never the client-supplied leftmost
// entry.
//
// Every rejected credential is also recorded via metrics.RecordAuthFailure with
// a stable snake_case reason string (e.g. "missing_header", "bad_password").
// Rate-limit (429) and DB-error (500) paths do NOT record an auth failure —
// those are not credential rejections. Reason strings become Prometheus label
// series, so new reasons must be added deliberately to keep cardinality bounded.
func AuthMiddleware(repo caldavRepo) func(http.Handler) http.Handler {
	return AuthMiddlewareWithConfig(repo, DefaultRateLimitConfig())
}

// tooManyAttempts writes the 429 for a rate-limited request.
func tooManyAttempts(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(wait)))
	http.Error(w, "Too many auth attempts", http.StatusTooManyRequests)
}

// AuthMiddlewareWithConfig is AuthMiddleware with explicit rate limits and
// trusted proxies.
func AuthMiddlewareWithConfig(repo caldavRepo, cfg RateLimitConfig) func(http.Handler) http.Handler {
	// The limiter is built once per server lifetime (the middleware is built
	// once in main.go), so buckets persist across requests. Its cleanup
	// goroutine evicts idle, refilled entries — see ratelimit.go.
	limiter := startAuthLimiter(cfg)
	// Build the dummy hash now (~250 ms once per process) so the first
	// rejected login doesn't pay for it and stand out.
	dummyHash()

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

			// Rate limits, checked before any credential work. Bypass
			// paths above (health, well-known, OPTIONS) never get here.
			now := time.Now()
			ip := clientIP(r, limiter.trusted)
			if ok, wait := limiter.requests.take(ip, now); !ok {
				logging.Logger.Warn("caldav auth: request rate limit exceeded",
					slog.String("ip", ip),
					slog.String("path", r.URL.Path))
				tooManyAttempts(w, wait)
				return
			}
			if blocked, wait := limiter.ipFailures.blocked(ip, now); blocked {
				logging.Logger.Warn("caldav auth: failed-login rate limit exceeded for ip",
					slog.String("ip", ip),
					slog.String("path", r.URL.Path))
				tooManyAttempts(w, wait)
				return
			}

			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				count := limiter.ipFailures.countFailure(ip, now)
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
				count := limiter.ipFailures.countFailure(ip, now)
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
				count := limiter.ipFailures.countFailure(ip, now)
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
				count := limiter.ipFailures.countFailure(ip, now)
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
				count := limiter.ipFailures.countFailure(ip, now)
				metrics.RecordAuthFailure("empty_credentials")
				logging.Logger.Warn("caldav auth: empty email or password",
					slog.String("ip", ip),
					slog.Int("failures", count))
				w.Header().Set("WWW-Authenticate", `Basic realm="Home OS CalDAV"`)
				http.Error(w, "Email and password required", http.StatusUnauthorized)
				return
			}

			// Per-account failed-login limit, before the lookup and bcrypt.
			acct := accountKey(email)
			if blocked, wait := limiter.accountFailures.blocked(acct, now); blocked {
				logging.Logger.Warn("caldav auth: failed-login rate limit exceeded for account",
					slog.String("ip", ip),
					slog.String("email", email))
				tooManyAttempts(w, wait)
				return
			}
			// passwordFailure records a failed password check (one that ran
			// bcrypt) against both the IP and the account.
			passwordFailure := func() int {
				limiter.accountFailures.fail(acct, now)
				return limiter.ipFailures.fail(ip, now)
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
				_ = compareHash(dummyHash(), []byte(password))
				count := passwordFailure()
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
				invalidCredentials(w)
				return
			}
			if !user.Active {
				// Deactivated accounts are rejected before the password
				// check, so previously issued app passwords stop working
				// immediately. The dummy compare and shared response make
				// this indistinguishable from an unknown email or a wrong
				// password.
				_ = compareHash(dummyHash(), []byte(password))
				count := passwordFailure()
				metrics.RecordAuthFailure("inactive_user")
				logging.Logger.Warn("caldav auth: deactivated user",
					slog.String("ip", ip),
					slog.String("email", email),
					slog.Int("failures", count))
				invalidCredentials(w)
				return
			}
			if user.CalDAVPasswordHash == nil || *user.CalDAVPasswordHash == "" {
				// Same cost and response as a wrong password: a distinct
				// message here would confirm the email is an active account.
				_ = compareHash(dummyHash(), []byte(password))
				count := passwordFailure()
				metrics.RecordAuthFailure("no_caldav_password")
				logging.Logger.Warn("caldav auth: no caldav password set",
					slog.String("ip", ip),
					slog.String("email", email),
					slog.Int("failures", count))
				invalidCredentials(w)
				return
			}

			// Compare password against stored caldav_password_hash.
			if err := compareHash([]byte(*user.CalDAVPasswordHash), []byte(password)); err != nil {
				count := passwordFailure()
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
				invalidCredentials(w)
				return
			}

			// Successful authentication: refill the account's failure
			// bucket and reset the IP's consecutive-failure count, so a
			// user who fat-fingered their password is back to a clean
			// slate. The IP's failure bucket is left alone: a valid login
			// must not buy more guesses at other accounts.
			limiter.accountFailures.forget(acct)
			limiter.ipFailures.resetCount(ip)

			// Load every household the user belongs to. The list is ordered
			// oldest membership first, so its first element is a stable
			// default household for unscoped requests.
			householdIDs, err := repo.ListHouseholdIDsForUser(r.Context(), user.ID)
			if err != nil {
				logging.Logger.Error("caldav auth: household lookup error",
					slog.String("user_id", user.ID),
					slog.String("email", email),
					slog.String("ip", ip),
					slog.String("error", err.Error()))
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}

			householdID, scope, path, ok, err := resolveHousehold(r.Context(), repo, r.URL.Path, householdIDs)
			if err != nil {
				logging.Logger.Error("caldav auth: calendar household lookup error",
					slog.String("user_id", user.ID),
					slog.String("path", r.URL.Path),
					slog.String("error", err.Error()))
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			if !ok {
				// Scoped to a household the user is not a member of. 404
				// rather than 403 so household IDs can't be probed.
				http.Error(w, "Not Found", http.StatusNotFound)
				return
			}

			// Inject household context, user_id, and email.
			ctx := context.WithValue(r.Context(), HouseholdIDKey, householdID)
			ctx = context.WithValue(ctx, HouseholdIDsKey, householdIDs)
			ctx = context.WithValue(ctx, HouseholdScopeKey, scope)
			ctx = context.WithValue(ctx, UserIDKey, user.ID)
			ctx = context.WithValue(ctx, EmailKey, email)
			r2 := r.WithContext(ctx)
			if path != r.URL.Path {
				u := *r.URL
				u.Path = path
				u.RawPath = ""
				r2.URL = &u
			}
			next.ServeHTTP(w, r2)
		})
	}
}

// resolveHousehold picks the household a CalDAV request runs against.
//
//   - /dav/households/<id>/rest: the scoped household, if the user is a
//     member (ok=false otherwise). The returned path is /dav/rest, so the
//     CalDAV handlers serve it unchanged.
//   - /dav/<calendar-uid>/...: calendar UIDs are globally unique, so for a
//     user in several households the calendar's own household is used when
//     it is one of theirs. This lets the unscoped calendar hrefs listed by
//     discovery work for every household.
//   - anything else: the default household (householdIDs[0]).
//
// A user with no memberships gets an empty household, as before.
func resolveHousehold(ctx context.Context, repo caldavRepo, path string, householdIDs []string) (householdID, scope, newPath string, ok bool, err error) {
	if len(householdIDs) > 0 {
		householdID = householdIDs[0]
	}

	if strings.HasPrefix(path, householdsPrefix) {
		rest := strings.TrimPrefix(path, householdsPrefix)
		id, tail, _ := strings.Cut(rest, "/")
		if id == "" {
			// /dav/households/ itself: nothing is served there.
			return householdID, "", path, true, nil
		}
		if !containsString(householdIDs, id) {
			return "", "", path, false, nil
		}
		return id, id, "/dav/" + tail, true, nil
	}

	if len(householdIDs) < 2 {
		return householdID, "", path, true, nil
	}
	seg, _, _ := strings.Cut(strings.TrimPrefix(path, "/dav/"), "/")
	if seg == "" || seg == "calendars" || seg == "households" || !strings.HasPrefix(path, "/dav/") {
		return householdID, "", path, true, nil
	}
	owner, err := repo.GetCalendarHouseholdID(ctx, seg)
	if err != nil {
		return "", "", path, false, err
	}
	if owner != "" && containsString(householdIDs, owner) {
		householdID = owner
	}
	return householdID, "", path, true, nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// HouseholdIDsFromContext returns every household the authenticated user
// belongs to, default household first. Returns nil if not found.
func HouseholdIDsFromContext(ctx context.Context) []string {
	v, _ := ctx.Value(HouseholdIDsKey).([]string)
	return v
}

// HouseholdScopeFromContext returns the household ID from a
// /dav/households/<id>/ request path, or "" for unscoped requests.
func HouseholdScopeFromContext(ctx context.Context) string {
	v, _ := ctx.Value(HouseholdScopeKey).(string)
	return v
}

// DAVBase returns the URL base that hrefs for the current request should use:
// "/dav/households/<id>/" for household-scoped requests, "/dav/" otherwise.
func DAVBase(ctx context.Context) string {
	if scope := HouseholdScopeFromContext(ctx); scope != "" {
		return householdsPrefix + scope + "/"
	}
	return "/dav/"
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
