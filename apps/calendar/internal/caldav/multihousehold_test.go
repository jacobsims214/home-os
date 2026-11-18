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

// seedTwoFamilyUser creates a user who belongs to two households: "older"
// (membership created first) and "newer". Each household has one calendar.
// The newer household is inserted first so its row (and UUID order) can't
// accidentally make the test pass — only the membership timestamps decide.
func seedTwoFamilyUser(t *testing.T, pool *pgxpool.Pool, email, password string) (olderHH, olderCal, newerHH, newerCal string) {
	t.Helper()
	ctx := context.Background()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, caldav_password_hash) VALUES ($1, 'Two Families', 'x', $2) RETURNING id::text`,
		email, string(hash)).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	newerHH, newerCal, _ = seedCalendar(t, pool)
	olderHH, olderCal, _ = seedCalendar(t, pool)
	for _, m := range []struct{ hh, ts string }{
		{newerHH, "2026-06-01T00:00:00Z"},
		{olderHH, "2025-01-01T00:00:00Z"},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO memberships (household_id, user_id, created_at) VALUES ($1, $2, $3)`,
			m.hh, userID, m.ts); err != nil {
			t.Fatalf("insert membership: %v", err)
		}
	}
	return olderHH, olderCal, newerHH, newerCal
}

// TestMultiHouseholdDiscoveryIntegration runs real requests through the auth
// middleware and the CalDAV handler for a user in two families.
func TestMultiHouseholdDiscoveryIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	ctx := context.Background()
	const email, password = "two-families@example.com", "app-pw-1"
	olderHH, olderCal, newerHH, newerCal := seedTwoFamilyUser(t, pool, email, password)

	// Deterministic default: the oldest membership, every time.
	for i := 0; i < 3; i++ {
		ids, err := repo.ListHouseholdIDsForUser(ctx, mustUserID(t, pool, email))
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 2 || ids[0] != olderHH || ids[1] != newerHH {
			t.Fatalf("ListHouseholdIDsForUser = %v, want [%s %s]", ids, olderHH, newerHH)
		}
		def, err := repo.GetHouseholdIDForUser(ctx, mustUserID(t, pool, email))
		if err != nil || def != olderHH {
			t.Fatalf("GetHouseholdIDForUser = %q, %v; want %q", def, err, olderHH)
		}
	}

	srv := auth.AuthMiddleware(repo)(NewHandler(repo))
	authz := "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+password))
	do := func(method, path, depth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(""))
		r.RemoteAddr = "198.51.100.20:5555"
		r.Header.Set("Authorization", authz)
		if depth != "" {
			r.Header.Set("Depth", depth)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	hasCal := func(body, uid string) bool { return strings.Contains(body, "<D:href>/dav/"+uid+"/</D:href>") }

	// Unscoped principal and home set list both households' calendars.
	for _, p := range []string{"/dav/", "/dav/calendars/"} {
		w := do("PROPFIND", p, "1")
		if w.Code != http.StatusMultiStatus {
			t.Fatalf("PROPFIND %s = %d: %s", p, w.Code, w.Body)
		}
		if !hasCal(w.Body.String(), olderCal) || !hasCal(w.Body.String(), newerCal) {
			t.Errorf("PROPFIND %s does not list both households' calendars:\n%s", p, w.Body)
		}
	}

	// A household-scoped principal lists only that household and points at
	// its own home set.
	scoped := "/dav/households/" + newerHH + "/"
	w := do("PROPFIND", scoped, "1")
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND %s = %d: %s", scoped, w.Code, w.Body)
	}
	body := w.Body.String()
	if !hasCal(body, newerCal) || hasCal(body, olderCal) {
		t.Errorf("scoped PROPFIND should list only %s:\n%s", newerCal, body)
	}
	if !strings.Contains(body, "<D:href>"+scoped+"</D:href>") ||
		!strings.Contains(body, "<C:calendar-home-set><D:href>"+scoped+"calendars/</D:href>") {
		t.Errorf("scoped PROPFIND hrefs not scoped:\n%s", body)
	}
	w = do("PROPFIND", scoped+"calendars/", "1")
	if !hasCal(w.Body.String(), newerCal) || hasCal(w.Body.String(), olderCal) {
		t.Errorf("scoped home set should list only %s:\n%s", newerCal, w.Body)
	}

	// The non-default household's calendar works through the unscoped href
	// discovery returned, and through the scoped one.
	for _, p := range []string{"/dav/" + newerCal + "/", scoped + newerCal + "/"} {
		if w := do("PROPFIND", p, "0"); w.Code != http.StatusMultiStatus {
			t.Errorf("PROPFIND %s = %d: %s", p, w.Code, w.Body)
		}
	}
	evPath := "/dav/" + newerCal + "/evt-second-family.ics"
	r := httptest.NewRequest(http.MethodPut, evPath, strings.NewReader(rawFixture))
	r.RemoteAddr = "198.51.100.20:5555"
	r.Header.Set("Authorization", authz)
	pw := httptest.NewRecorder()
	srv.ServeHTTP(pw, r)
	if pw.Code != http.StatusCreated {
		t.Fatalf("PUT into second household = %d: %s", pw.Code, pw.Body)
	}
	if w := do(http.MethodGet, evPath, ""); w.Code != http.StatusOK {
		t.Errorf("GET from second household = %d", w.Code)
	}

	// A household the user doesn't belong to is 404 when scoped, and its
	// calendar can't be reached unscoped either.
	strangerHH, strangerCal, _ := seedCalendar(t, pool)
	if w := do("PROPFIND", "/dav/households/"+strangerHH+"/", "1"); w.Code != http.StatusNotFound {
		t.Errorf("scoped PROPFIND on foreign household = %d, want 404", w.Code)
	}
	if w := do("PROPFIND", "/dav/"+strangerCal+"/", "0"); w.Code != http.StatusNotFound {
		t.Errorf("PROPFIND on foreign calendar = %d, want 404", w.Code)
	}
	if w := do("PROPFIND", scoped+olderCal+"/", "0"); w.Code != http.StatusNotFound {
		t.Errorf("calendar scoped to the wrong household = %d, want 404", w.Code)
	}
}

// TestDefaultHouseholdTieBreakIntegration: memberships created at the same
// instant still give one stable default (lowest household_id).
func TestDefaultHouseholdTieBreakIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	ctx := context.Background()
	var userID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('tie@example.com', 'Tie', 'x') RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	a, _, _ := seedCalendar(t, pool)
	b, _, _ := seedCalendar(t, pool)
	for _, hh := range []string{a, b} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO memberships (household_id, user_id, created_at) VALUES ($1, $2, '2026-01-01T00:00:00Z')`, hh, userID); err != nil {
			t.Fatal(err)
		}
	}
	want := a
	if b < a {
		want = b
	}
	for i := 0; i < 3; i++ {
		got, err := repo.GetHouseholdIDForUser(ctx, userID)
		if err != nil || got != want {
			t.Fatalf("GetHouseholdIDForUser = %q, %v; want %q", got, err, want)
		}
	}
}

func mustUserID(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT id::text FROM users WHERE email = $1`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
