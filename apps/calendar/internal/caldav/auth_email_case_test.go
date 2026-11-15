package caldav

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"home-os/calendar/internal/auth"
)

// seedCaseUser inserts a user with an app password and one household
// membership, using seedCalendar for the household.
func seedCaseUser(t *testing.T, pool *pgxpool.Pool, email, password string) {
	t.Helper()
	ctx := context.Background()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, caldav_password_hash) VALUES ($1, 'Case Test', 'x', $2) RETURNING id::text`,
		email, string(hash)).Scan(&userID); err != nil {
		t.Fatalf("insert user %s: %v", email, err)
	}
	hh, _, _ := seedCalendar(t, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (household_id, user_id) VALUES ($1, $2)`, hh, userID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
}

// TestCalDAVAuthEmailCaseIntegration runs Basic auth through the real
// middleware against Postgres: the email matches in any capitalisation (as
// the API's member resolver does), a wrong password or a deactivated account
// still gets the unknown-user 401, and two accounts that differ only by case
// are never confused.
func TestCalDAVAuthEmailCaseIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	ctx := context.Background()
	handler := NewHandler(repo)
	// A new middleware (and per-IP limiter) per request keeps the rate limit
	// from turning expected 401s into 429s.
	do := func(user, pw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PROPFIND", "/dav/", strings.NewReader(""))
		r.RemoteAddr = "198.51.100.40:5555"
		r.Header.Set("Depth", "0")
		r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pw)))
		w := httptest.NewRecorder()
		auth.AuthMiddleware(repo)(handler).ServeHTTP(w, r)
		return w
	}

	unknown := do("nobody-case@example.com", "whatever")
	if unknown.Code != http.StatusUnauthorized {
		t.Fatalf("unknown user = %d, want 401", unknown.Code)
	}
	want401 := func(name string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusUnauthorized || w.Body.String() != unknown.Body.String() ||
			w.Header().Get("WWW-Authenticate") != unknown.Header().Get("WWW-Authenticate") {
			t.Errorf("%s = %d %q, want the unknown user's 401 %q", name, w.Code, w.Body.String(), unknown.Body.String())
		}
	}

	const stored, password = "Kid@Example.com", "app-pw-case"
	seedCaseUser(t, pool, stored, password)

	for _, typed := range []string{stored, "kid@example.com", "KID@EXAMPLE.COM", "kId@eXample.CoM"} {
		if w := do(typed, password); w.Code != http.StatusMultiStatus {
			t.Errorf("login as %q = %d, want 207: %s", typed, w.Code, w.Body)
		}
	}
	want401("lowercase email + wrong password", do("kid@example.com", "wrong"))
	want401("uppercase email + wrong password", do("KID@EXAMPLE.COM", "wrong"))

	// Deactivation still applies whatever case is typed.
	if _, err := pool.Exec(ctx, `UPDATE users SET active = false WHERE email = $1`, stored); err != nil {
		t.Fatal(err)
	}
	want401("deactivated, lowercase email", do("kid@example.com", password))
	if _, err := pool.Exec(ctx, `UPDATE users SET active = true WHERE email = $1`, stored); err != nil {
		t.Fatal(err)
	}

	// Two accounts that differ only by case (users.email UNIQUE is
	// case-sensitive). An exact match picks its own account and only its own
	// password works; a case-insensitive-only match is ambiguous and refused.
	const dupA, pwA = "dup@example.com", "app-pw-dup-a"
	const dupB, pwB = "Dup@Example.com", "app-pw-dup-b"
	seedCaseUser(t, pool, dupA, pwA)
	seedCaseUser(t, pool, dupB, pwB)
	if w := do(dupA, pwA); w.Code != http.StatusMultiStatus {
		t.Errorf("exact %q with its password = %d, want 207", dupA, w.Code)
	}
	if w := do(dupB, pwB); w.Code != http.StatusMultiStatus {
		t.Errorf("exact %q with its password = %d, want 207", dupB, w.Code)
	}
	want401("exact dupA with dupB's password", do(dupA, pwB))
	want401("exact dupB with dupA's password", do(dupB, pwA))
	want401("ambiguous DUP@EXAMPLE.COM with dupA's password", do("DUP@EXAMPLE.COM", pwA))
	want401("ambiguous DUP@EXAMPLE.COM with dupB's password", do("DUP@EXAMPLE.COM", pwB))

	// Repo level: the same rules, without the middleware.
	for _, tc := range []struct {
		typed, wantEmail string
	}{
		{"KID@example.com", stored},
		{dupA, dupA},
		{dupB, dupB},
		{"DUP@EXAMPLE.COM", ""},
		{"absent@example.com", ""},
	} {
		u, err := repo.GetUserByEmailForCalDAV(ctx, tc.typed)
		if err != nil {
			t.Fatalf("GetUserByEmailForCalDAV(%q): %v", tc.typed, err)
		}
		got := ""
		if u != nil {
			got = u.Email
		}
		if got != tc.wantEmail {
			t.Errorf("GetUserByEmailForCalDAV(%q) = %q, want %q", tc.typed, got, tc.wantEmail)
		}
	}
}
