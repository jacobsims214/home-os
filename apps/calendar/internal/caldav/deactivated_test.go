package caldav

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"home-os/calendar/internal/auth"
)

// TestDeactivatedUserIntegration runs real requests through the auth
// middleware and CalDAV handler against Postgres: an app password that works
// stops working the moment users.active is set to false, on every CalDAV path
// (unscoped, household-scoped, calendar and event URLs), with the same
// response an unknown email gets. Reactivating restores access.
func TestDeactivatedUserIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	ctx := context.Background()
	const email, password = "deactivated@example.com", "app-pw-deact"
	olderHH, olderCal, newerHH, newerCal := seedTwoFamilyUser(t, pool, email, password)

	handler := NewHandler(repo)
	// A new middleware (and so a new per-IP limiter) per request keeps the
	// 10/min rate limit from turning expected 401s into 429s.
	do := func(method, path, user, pw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(""))
		r.RemoteAddr = "198.51.100.30:5555"
		r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pw)))
		if method == "PROPFIND" {
			r.Header.Set("Depth", "0")
		}
		w := httptest.NewRecorder()
		auth.AuthMiddleware(repo)(handler).ServeHTTP(w, r)
		return w
	}

	paths := []string{
		"/dav/",
		"/dav/calendars/",
		"/dav/" + olderCal + "/",
		"/dav/" + newerCal + "/",
		"/dav/households/" + olderHH + "/",
		"/dav/households/" + newerHH + "/calendars/",
		"/dav/households/" + newerHH + "/" + newerCal + "/",
	}

	// Baseline: the app password works everywhere.
	for _, p := range paths {
		if w := do("PROPFIND", p, email, password); w.Code != http.StatusMultiStatus {
			t.Fatalf("active user PROPFIND %s = %d: %s", p, w.Code, w.Body)
		}
	}

	setActive := func(active bool) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE users SET active = $1 WHERE email = $2`, active, email); err != nil {
			t.Fatalf("set active=%v: %v", active, err)
		}
	}
	setActive(false)

	unknown := do("PROPFIND", "/dav/", "nobody-here@example.com", password)
	if unknown.Code != http.StatusUnauthorized {
		t.Fatalf("unknown user = %d, want 401", unknown.Code)
	}
	evPath := "/dav/" + newerCal + "/evt-after-deactivation.ics"
	for _, method := range []string{"PROPFIND", http.MethodGet, http.MethodPut, http.MethodDelete, "REPORT"} {
		for _, p := range append(paths, evPath) {
			w := do(method, p, email, password)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("deactivated %s %s = %d, want 401: %s", method, p, w.Code, w.Body)
			}
			if w.Body.String() != unknown.Body.String() ||
				w.Header().Get("WWW-Authenticate") != unknown.Header().Get("WWW-Authenticate") {
				t.Fatalf("deactivated %s %s response %q differs from unknown user's %q",
					method, p, w.Body.String(), unknown.Body.String())
			}
		}
	}
	// A wrong password for a deactivated account looks the same too.
	if w := do("PROPFIND", "/dav/", email, "wrong"); w.Code != http.StatusUnauthorized || w.Body.String() != unknown.Body.String() {
		t.Fatalf("deactivated + wrong password = %d %q, want 401 %q", w.Code, w.Body.String(), unknown.Body.String())
	}

	setActive(true)
	if w := do("PROPFIND", "/dav/", email, password); w.Code != http.StatusMultiStatus {
		t.Fatalf("reactivated user PROPFIND /dav/ = %d: %s", w.Code, w.Body)
	}
}
