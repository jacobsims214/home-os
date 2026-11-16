package household_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/household"
	"home-os/api/internal/middleware"
)

// #4820: PATCH /api/v1/households/{id} renames a family and/or changes its
// time zone, owner and family_manager only.

// updateRouter mirrors cmd/api/main.go for this route: inside RequireAuth
// (real Postgres member resolver, so the household role comes from
// memberships) and RequirePolicy, so the route rule in middleware/roles.go is
// exercised too, not just the handler's own record-level check.
func updateRouter(pool *pgxpool.Pool, v *auth.Verifier) http.Handler {
	h := household.NewHandler(household.NewRepo(pool))
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		r.Use(middleware.RequirePolicy)
		r.Patch("/api/v1/households/{id}", h.Update)
	})
	return r
}

type ufix struct {
	pool *pgxpool.Pool
	s    *signer
	h    http.Handler
	hhID string
	name string
	tz   string
}

func newUFix(t *testing.T) *ufix {
	t.Helper()
	pool := testDB(t)
	s := newSigner(t)
	f := &ufix{
		pool: pool, s: s, h: updateRouter(pool, s.verifier(pool)),
		name: "Update HH", tz: "America/New_York",
	}
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO households (name, timezone) VALUES ($1, $2) RETURNING id::text`, f.name, f.tz,
	).Scan(&f.hhID))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, f.hhID) })
	return f
}

// member adds a user with role in hh and returns their token and users.id.
func (f *ufix) member(t *testing.T, hh, role string) (token, userID string) {
	t.Helper()
	sub, email := uniq(role)
	ctx := context.Background()
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, 'U', 'x', $2) RETURNING id::text`,
		email, sub).Scan(&userID))
	_, err := f.pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hh, userID, role)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	return f.s.token(t, sub, email), userID
}

// patch sends PATCH /api/v1/households/{id} with an optional X-Household-ID.
func (f *ufix) patch(id, token, hhHeader string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/households/"+id, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if hhHeader != "" {
		req.Header.Set(auth.HouseholdHeader, hhHeader)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

// stored reads name and timezone straight from Postgres, so the assertions do
// not depend on the response shape.
func (f *ufix) stored(t *testing.T, id string) (name, tz string) {
	t.Helper()
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT name, timezone FROM households WHERE id = $1`, id).Scan(&name, &tz))
	return name, tz
}

type updated struct {
	Data struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Timezone  string `json:"timezone"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	} `json:"data"`
}

// TestHouseholdUpdate_NameAndTimezone: an owner or family_manager can change
// both fields, one at a time or together, and what comes back is what was
// stored.
func TestHouseholdUpdate_NameAndTimezone(t *testing.T) {
	f := newUFix(t)
	ownerTok, _ := f.member(t, f.hhID, auth.RoleOwner)

	t.Run("both fields", func(t *testing.T) {
		rec := f.patch(f.hhID, ownerTok, "", map[string]string{
			"name": "  The Sims Family  ", "timezone": "America/Chicago"})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var got updated
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, f.hhID, got.Data.ID)
		assert.Equal(t, "The Sims Family", got.Data.Name, "the name is trimmed")
		assert.Equal(t, "America/Chicago", got.Data.Timezone)
		assert.NotEmpty(t, got.Data.CreatedAt)
		assert.NotEmpty(t, got.Data.UpdatedAt)

		name, tz := f.stored(t, f.hhID)
		assert.Equal(t, "The Sims Family", name)
		assert.Equal(t, "America/Chicago", tz)
	})

	t.Run("name only leaves the timezone alone", func(t *testing.T) {
		rec := f.patch(f.hhID, ownerTok, "", map[string]string{"name": "Renamed"})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		name, tz := f.stored(t, f.hhID)
		assert.Equal(t, "Renamed", name)
		assert.Equal(t, "America/Chicago", tz, "an unsent field is unchanged")
	})

	t.Run("timezone only leaves the name alone", func(t *testing.T) {
		rec := f.patch(f.hhID, ownerTok, "", map[string]string{"timezone": "Europe/London"})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		name, tz := f.stored(t, f.hhID)
		assert.Equal(t, "Renamed", name)
		assert.Equal(t, "Europe/London", tz)
	})

	t.Run("a family_manager may too", func(t *testing.T) {
		fmTok, _ := f.member(t, f.hhID, auth.RoleFamilyManager)
		rec := f.patch(f.hhID, fmTok, "", map[string]string{"timezone": "America/Denver"})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		_, tz := f.stored(t, f.hhID)
		assert.Equal(t, "America/Denver", tz)
	})
}

// TestHouseholdUpdate_OnlyManagers: every other household role is refused at
// the route (403 naming the role and the policy action) and nothing is
// written.
func TestHouseholdUpdate_OnlyManagers(t *testing.T) {
	f := newUFix(t)
	body := map[string]string{"name": "Hijacked", "timezone": "Pacific/Kiritimati"}

	for _, role := range []string{
		auth.RoleFamilyMember, auth.RoleViewer, auth.RoleHousesitter, auth.RoleVendor,
	} {
		t.Run(role, func(t *testing.T) {
			tok, _ := f.member(t, f.hhID, role)
			rec := f.patch(f.hhID, tok, "", body)
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "the "+role+" role cannot write household")

			name, tz := f.stored(t, f.hhID)
			assert.Equal(t, f.name, name, "a refused PATCH must not write")
			assert.Equal(t, f.tz, tz)
		})
	}

	t.Run("an instance admin member with no managing role is still refused", func(t *testing.T) {
		// is_instance_admin is a different axis: it opens the /admin routes,
		// not a household's settings.
		sub, email := uniq("instanceadmin")
		ctx := context.Background()
		var uid string
		require.NoError(t, f.pool.QueryRow(ctx,
			`INSERT INTO users (email, name, password_hash, dex_sub, is_instance_admin)
			 VALUES ($1, 'IA', 'x', $2, true) RETURNING id::text`, email, sub).Scan(&uid))
		t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, uid) })
		_, err := f.pool.Exec(ctx,
			`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'family_member')`, f.hhID, uid)
		require.NoError(t, err)

		rec := f.patch(f.hhID, f.s.token(t, sub, email), "", body)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		name, _ := f.stored(t, f.hhID)
		assert.Equal(t, f.name, name)
	})

	t.Run("a non-member cannot tell the family exists", func(t *testing.T) {
		// Another family's household id answers 404, exactly like an unknown
		// id, so ids cannot be probed. The route policy passes first (the
		// caller is an owner), then the repo refuses from memberships.
		otherHH := addHousehold(t, f.pool, "Somebody Else", "", "", 0)
		ownerTok, _ := f.member(t, f.hhID, auth.RoleOwner)

		rec := f.patch(otherHH, ownerTok, "", body)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

		rec = f.patch(uuid.NewString(), ownerTok, "", body)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

		name, _ := f.stored(t, otherHH)
		assert.Equal(t, "Somebody Else", name)
	})

	t.Run("an X-Household-ID for another family does not widen the call", func(t *testing.T) {
		// The {id} in the path is what is edited; the header only picks which
		// membership RequireAuth resolves. Naming family A in the path and
		// family B in the header must not let B's manager edit A.
		hhB := addHousehold(t, f.pool, "Family B", "", "", 0)
		tokB, _ := f.member(t, hhB, auth.RoleOwner)

		rec := f.patch(f.hhID, tokB, hhB, body)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		name, _ := f.stored(t, f.hhID)
		assert.Equal(t, f.name, name)
	})
}

// TestHouseholdUpdate_InvalidTimezone: a timezone that is not a real IANA zone
// name is a 400 and writes nothing. Every consumer loads the stored name and
// silently falls back to UTC when it fails, so a bad value would corrupt every
// date the family sees without any error.
func TestHouseholdUpdate_InvalidTimezone(t *testing.T) {
	f := newUFix(t)
	ownerTok, _ := f.member(t, f.hhID, auth.RoleOwner)

	for _, tz := range []string{
		"Mars/Olympus_Mons",      // not a zone
		"not a zone",             // junk
		"Local",                  // the SERVER's zone, not a place
		"Factory",                // tzdata's fixed-UTC test placeholder
		"america/chicago",        // right zone, wrong case: fails on a Linux container
		"posixrules",             // a host zoneinfo file, not a zone name
		"../etc/passwd",          // path traversal
		"/etc/passwd",            // absolute path
		"America",                // a directory, not a zone
		"America/",               // empty segment
		"America//Chicago",       // empty segment
		strings.Repeat("a", 101), // over households.timezone VARCHAR(100)
	} {
		t.Run(fmt.Sprintf("%q", tz), func(t *testing.T) {
			rec := f.patch(f.hhID, ownerTok, "", map[string]string{"timezone": tz})
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "IANA time zone name")

			_, stored := f.stored(t, f.hhID)
			assert.Equal(t, f.tz, stored, "a refused timezone must not be stored")
		})
	}

	for _, tz := range []string{"", "   "} {
		t.Run(fmt.Sprintf("blank %q", tz), func(t *testing.T) {
			rec := f.patch(f.hhID, ownerTok, "", map[string]string{"timezone": tz})
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "timezone is required")
			_, stored := f.stored(t, f.hhID)
			assert.Equal(t, f.tz, stored)
		})
	}

	t.Run("a name sent with a bad timezone is not written either", func(t *testing.T) {
		rec := f.patch(f.hhID, ownerTok, "", map[string]string{"name": "Half Applied", "timezone": "Mars/Base"})
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		name, tz := f.stored(t, f.hhID)
		assert.Equal(t, f.name, name, "validation happens before the write")
		assert.Equal(t, f.tz, tz)
	})

	t.Run("a real zone with a numeric name is accepted", func(t *testing.T) {
		rec := f.patch(f.hhID, ownerTok, "", map[string]string{"timezone": "Etc/GMT+5"})
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		_, tz := f.stored(t, f.hhID)
		assert.Equal(t, "Etc/GMT+5", tz)
	})
}

// TestHouseholdUpdate_Validation: the rest of the body validation.
func TestHouseholdUpdate_Validation(t *testing.T) {
	f := newUFix(t)
	ownerTok, _ := f.member(t, f.hhID, auth.RoleOwner)

	tests := []struct {
		name  string
		body  any
		want  int
		match string
	}{
		{"empty object", map[string]any{}, http.StatusBadRequest, "name or timezone is required"},
		{"null name", map[string]any{"name": nil}, http.StatusBadRequest, "name must be a string"},
		{"blank name", map[string]string{"name": "   "}, http.StatusBadRequest, "name is required"},
		{"null timezone", map[string]any{"timezone": nil}, http.StatusBadRequest, "IANA time zone name"},
		{"numeric timezone", map[string]any{"timezone": 5}, http.StatusBadRequest, "IANA time zone name"},
		{"only an unknown key", map[string]string{"tz": "UTC"}, http.StatusBadRequest, "name or timezone is required"},
		// A client may PATCH back the whole object it read: extra keys are
		// ignored, and settings are not writable through this route.
		{"extra keys are ignored", map[string]any{
			"id": "ignored", "name": "Full Object", "timezone": "UTC",
			"settings": map[string]any{"x": 1}, "created_at": "2020-01-01T00:00:00Z",
		}, http.StatusOK, ""},
		{"name too long", map[string]string{"name": strings.Repeat("a", 256)}, http.StatusBadRequest, "at most 255"},
		{"not json", "{", http.StatusBadRequest, "invalid body"},
		{"name at the limit", map[string]string{"name": strings.Repeat("a", 255)}, http.StatusOK, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.patch(f.hhID, ownerTok, "", tc.body)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
			if tc.match != "" {
				assert.Contains(t, rec.Body.String(), tc.match)
			}
		})
	}

	t.Run("a malformed {id} is a 400 naming the list endpoint", func(t *testing.T) {
		// "me" is a GET-only shorthand; a PATCH must name a household id.
		for _, id := range []string{"not-a-uuid", "me", "%20"} {
			rec := f.patch(id, ownerTok, "", map[string]string{"timezone": "UTC"})
			assert.Equal(t, http.StatusBadRequest, rec.Code, "%q: %s", id, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "GET /api/v1/me/households")
		}
	})

	t.Run("no token is 401", func(t *testing.T) {
		rec := f.patch(f.hhID, "", "", map[string]string{"timezone": "UTC"})
		assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	})
}

// TestHouseholdUpdate_DemotionAppliesAtOnce: the role is read from
// memberships per request, so a demoted manager loses the route immediately
// with a still-valid token.
func TestHouseholdUpdate_DemotionAppliesAtOnce(t *testing.T) {
	f := newUFix(t)
	tok, uid := f.member(t, f.hhID, auth.RoleFamilyManager)
	ctx := context.Background()

	rec := f.patch(f.hhID, tok, "", map[string]string{"name": "Before Demotion"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	_, err := f.pool.Exec(ctx,
		`UPDATE memberships SET role = 'family_member' WHERE household_id = $1 AND user_id = $2`, f.hhID, uid)
	require.NoError(t, err)

	rec = f.patch(f.hhID, tok, "", map[string]string{"name": "After Demotion"})
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	name, _ := f.stored(t, f.hhID)
	assert.Equal(t, "Before Demotion", name)
}
