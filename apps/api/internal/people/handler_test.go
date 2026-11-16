package people_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/api/internal/people"
)

const testIssuer = "https://dex.test/dex"

type fixture struct {
	pool *pgxpool.Pool
	h    http.Handler
	key  *rsa.PrivateKey
}

// setup connects to TEST_DATABASE_URL (all migrations applied; skips when
// unset) and builds a router like cmd/api/main.go: the people routes behind
// the real RequireAuth middleware with the Postgres member resolver, so
// X-Household-ID is resolved exactly as in production.
func setup(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{key.Public()}}
	v := auth.NewVerifierWithKeySet(testIssuer, ks, []string{"home-os-ui"}).
		WithResolver(auth.NewMemberResolver(pool))

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		people.NewHandler(people.NewRepo(pool)).RegisterRoutes(r)
	})
	return &fixture{pool: pool, h: r, key: key}
}

type member struct {
	token, userID, householdID, personID string
}

// household creates an empty household, removed at cleanup (cascading to
// its people and preferences).
func (f *fixture) household(t *testing.T) string {
	t.Helper()
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`INSERT INTO households (name) VALUES ('People HH') RETURNING id::text`).Scan(&id))
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, id) })
	return id
}

// member creates a user with role in hhID and returns a Dex-style token. The
// memberships trigger (041) creates their person record.
func (f *fixture) member(t *testing.T, hhID, role string) member {
	t.Helper()
	ctx := context.Background()
	email := "people-" + uuid.NewString()[:8] + "@example.com"
	sub := "CiQ" + uuid.NewString() + "EgVsb2NhbA"
	var userID string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, $2, 'x', $3) RETURNING id::text`,
		email, "Member "+role, sub).Scan(&userID))
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	f.join(t, hhID, userID, role)

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": testIssuer, "aud": "home-os-ui", "sub": sub, "email": email,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	token, err := tok.SignedString(f.key)
	require.NoError(t, err)
	return member{token: token, userID: userID, householdID: hhID, personID: f.personOf(t, hhID, userID)}
}

func (f *fixture) join(t *testing.T, hhID, userID, role string) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hhID, userID, role)
	require.NoError(t, err)
}

func (f *fixture) personOf(t *testing.T, hhID, userID string) string {
	t.Helper()
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT id::text FROM people WHERE household_id = $1 AND user_id = $2`, hhID, userID).Scan(&id))
	return id
}

type req struct {
	method, path string
	as           member
	body         any
	household    string // X-Household-ID, optional
}

func (f *fixture) do(t *testing.T, rq req) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if rq.body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(rq.body))
	}
	r := httptest.NewRequest(rq.method, rq.path, &buf)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+rq.as.token)
	if rq.household != "" {
		r.Header.Set("X-Household-ID", rq.household)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out struct {
		Data T `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out.Data
}

type personJSON struct {
	ID           string     `json:"id"`
	HouseholdID  string     `json:"household_id"`
	UserID       *string    `json:"user_id"`
	HasLogin     bool       `json:"has_login"`
	DisplayName  string     `json:"display_name"`
	Relationship *string    `json:"relationship"`
	Birthdate    *string    `json:"birthdate"`
	School       *string    `json:"school"`
	Grade        *string    `json:"grade"`
	Allergies    []string   `json:"allergies"`
	Notes        *string    `json:"notes"`
	Preferences  []prefJSON `json:"preferences"`
}

type prefJSON struct {
	ID       string  `json:"id"`
	PersonID string  `json:"person_id"`
	Kind     string  `json:"kind"`
	Category string  `json:"category"`
	Value    string  `json:"value"`
	Notes    *string `json:"notes"`
}

func kidBody(name string) map[string]any {
	return map[string]any{
		"display_name": " " + name + " ", "relationship": "child", "birthdate": "2016-04-09",
		"school": "Maple Elementary", "grade": "4th",
		"allergies": []string{" peanuts ", "", "Peanuts", "penicillin"}, "notes": "  ",
	}
}

// createKid adds a person without a login as the given manager.
func (f *fixture) createKid(t *testing.T, as member, name string) personJSON {
	t.Helper()
	rec := f.do(t, req{method: http.MethodPost, path: "/api/v1/people", as: as, body: kidBody(name)})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	return decode[personJSON](t, rec)
}

func TestPeople_CRUDWithPreferences(t *testing.T) {
	f := setup(t)
	hh := f.household(t)
	owner := f.member(t, hh, "owner")

	kid := f.createKid(t, owner, "Ada")
	t.Run("create trims and normalizes", func(t *testing.T) {
		assert.Equal(t, "Ada", kid.DisplayName)
		assert.Equal(t, hh, kid.HouseholdID)
		assert.Nil(t, kid.UserID)
		assert.False(t, kid.HasLogin)
		require.NotNil(t, kid.Birthdate)
		assert.Equal(t, "2016-04-09", *kid.Birthdate)
		assert.Equal(t, []string{"peanuts", "penicillin"}, kid.Allergies, "trimmed, blanks and case-duplicates dropped")
		assert.Nil(t, kid.Notes, "blank notes become null")
	})

	t.Run("list includes members and the kid; no-store", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/people", as: owner})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		list := decode[[]personJSON](t, rec)
		ids := map[string]personJSON{}
		for _, p := range list {
			ids[p.ID] = p
		}
		require.Len(t, ids, 2)
		require.Contains(t, ids, kid.ID)
		require.Contains(t, ids, owner.personID)
		assert.True(t, ids[owner.personID].HasLogin)
		assert.Nil(t, ids[kid.ID].Preferences, "list does not embed preferences")
	})

	var pref prefJSON
	t.Run("preferences CRUD", func(t *testing.T) {
		base := "/api/v1/people/" + kid.ID + "/preferences"
		rec := f.do(t, req{method: http.MethodPost, path: base, as: owner,
			body: map[string]any{"kind": "Dietary", "category": " food ", "value": " no peanuts ", "notes": "EpiPen in backpack"}})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		pref = decode[prefJSON](t, rec)
		assert.Equal(t, "dietary", pref.Kind)
		assert.Equal(t, "food", pref.Category)
		assert.Equal(t, "no peanuts", pref.Value)
		assert.Equal(t, kid.ID, pref.PersonID)

		rec = f.do(t, req{method: http.MethodPost, path: base, as: owner,
			body: map[string]any{"kind": "like", "value": "dinosaurs"}})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		like := decode[prefJSON](t, rec)
		assert.Equal(t, "general", like.Category, "category defaults to general")

		rec = f.do(t, req{method: http.MethodPost, path: base, as: owner,
			body: map[string]any{"kind": "like", "category": "GENERAL", "value": "Dinosaurs"}})
		assert.Equal(t, http.StatusConflict, rec.Code, "same preference ignoring case: %s", rec.Body.String())

		for name, body := range map[string]map[string]any{
			"bad kind":      {"kind": "love", "value": "x"},
			"blank value":   {"kind": "like", "value": "  "},
			"long category": {"kind": "like", "value": "x", "category": string(bytes.Repeat([]byte("c"), 51))},
		} {
			rec := f.do(t, req{method: http.MethodPost, path: base, as: owner, body: body})
			assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", name, rec.Body.String())
		}

		rec = f.do(t, req{method: http.MethodPut, path: base + "/" + like.ID, as: owner,
			body: map[string]any{"kind": "dislike", "category": "food", "value": "broccoli"}})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		updated := decode[prefJSON](t, rec)
		assert.Equal(t, "dislike", updated.Kind)
		assert.Equal(t, "broccoli", updated.Value)

		rec = f.do(t, req{method: http.MethodGet, path: base, as: owner})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Len(t, decode[[]prefJSON](t, rec), 2)

		rec = f.do(t, req{method: http.MethodDelete, path: base + "/" + like.ID, as: owner})
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		rec = f.do(t, req{method: http.MethodDelete, path: base + "/" + like.ID, as: owner})
		assert.Equal(t, http.StatusNotFound, rec.Code, "already deleted")
		rec = f.do(t, req{method: http.MethodPut, path: base + "/" + like.ID, as: owner,
			body: map[string]any{"kind": "like", "value": "x"}})
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("get embeds preferences", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/people/" + kid.ID, as: owner})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		got := decode[personJSON](t, rec)
		require.Len(t, got.Preferences, 1)
		assert.Equal(t, pref.ID, got.Preferences[0].ID)
		assert.Equal(t, "Maple Elementary", *got.School)

		rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/people/" + owner.personID, as: owner})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), `"preferences":[]`, "empty list, not omitted")
	})

	t.Run("update replaces fields", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodPut, path: "/api/v1/people/" + kid.ID, as: owner,
			body: map[string]any{"display_name": "Ada L.", "grade": "5th", "allergies": []string{"shellfish"}}})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		got := decode[personJSON](t, rec)
		assert.Equal(t, "Ada L.", got.DisplayName)
		assert.Equal(t, "5th", *got.Grade)
		assert.Nil(t, got.School, "omitted optional fields are cleared (PUT)")
		assert.Nil(t, got.Birthdate)
		assert.Equal(t, []string{"shellfish"}, got.Allergies)
	})

	t.Run("validation", func(t *testing.T) {
		for name, body := range map[string]any{
			"blank name":       map[string]any{"display_name": "  "},
			"bad birthdate":    map[string]any{"display_name": "X", "birthdate": "04/09/2016"},
			"future birthdate": map[string]any{"display_name": "X", "birthdate": time.Now().AddDate(1, 0, 0).Format("2006-01-02")},
			"long grade":       map[string]any{"display_name": "X", "grade": string(bytes.Repeat([]byte("g"), 51))},
			"not json":         "nope",
		} {
			rec := f.do(t, req{method: http.MethodPost, path: "/api/v1/people", as: owner, body: body})
			assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", name, rec.Body.String())
		}
		rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/people/not-a-uuid", as: owner})
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/people/" + uuid.NewString(), as: owner})
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("delete removes the person and their preferences", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodDelete, path: "/api/v1/people/" + kid.ID, as: owner})
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/people/" + kid.ID, as: owner})
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var n int
		require.NoError(t, f.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM person_preferences WHERE person_id = $1`, kid.ID).Scan(&n))
		assert.Zero(t, n)
		rec = f.do(t, req{method: http.MethodDelete, path: "/api/v1/people/" + kid.ID, as: owner})
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestPeople_LinkedMemberCantBeDeleted(t *testing.T) {
	f := setup(t)
	hh := f.household(t)
	owner := f.member(t, hh, "owner")
	spouse := f.member(t, hh, "family_member")

	for _, target := range []member{spouse, owner} {
		rec := f.do(t, req{method: http.MethodDelete, path: "/api/v1/people/" + target.personID, as: owner})
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "LINKED_MEMBER", decodeConflict(t, rec).Error.Code)
	}

	// Removing the member unlinks the person (041 trigger); now it can go.
	_, err := f.pool.Exec(context.Background(),
		`DELETE FROM memberships WHERE household_id = $1 AND user_id = $2`, hh, spouse.userID)
	require.NoError(t, err)
	rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/people/" + spouse.personID, as: owner})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, decode[personJSON](t, rec).HasLogin)
	rec = f.do(t, req{method: http.MethodDelete, path: "/api/v1/people/" + spouse.personID, as: owner})
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

type deleteConflictJSON struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	References *people.References `json:"references"`
}

func decodeConflict(t *testing.T, rec *httptest.ResponseRecorder) deleteConflictJSON {
	t.Helper()
	var out deleteConflictJSON
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

// TestPeople_DeleteNeedsConfirmWhenReferenced: a person that notes, list
// items or events point at is deleted only with ?confirm=true (MCP
// person_delete parity, #4743). Without it the API answers 409
// CONFIRM_REQUIRED with the counts and deletes nothing.
func TestPeople_DeleteNeedsConfirmWhenReferenced(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	hh := f.household(t)
	owner := f.member(t, hh, "owner")
	kid := f.createKid(t, owner, "Nora")

	for _, title := range []string{"Shoe size", "Pediatrician"} {
		_, err := f.pool.Exec(ctx,
			`INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'person', $2, $3, 'x')`,
			hh, kid.ID, title)
		require.NoError(t, err)
	}
	var listID string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO lists (household_id, name, kind, entity_type, entity_id)
		 VALUES ($1, 'Chores', 'chores', 'household', $1) RETURNING id::text`, hh).Scan(&listID))
	_, err := f.pool.Exec(ctx,
		`INSERT INTO list_items (list_id, household_id, text, assignee_person_id, position)
		 VALUES ($1, $2, 'Feed the cat', $3, 1)`, listID, hh, kid.ID)
	require.NoError(t, err)
	var calID string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO calendars (household_id, name, type, caldav_uid)
		 VALUES ($1, 'Family', 'family', $2) RETURNING id::text`, hh, "cal-"+uuid.NewString()).Scan(&calID))
	_, err = f.pool.Exec(ctx,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, ical_raw)
		 VALUES ($1, $2, '{}', 'e1', $3)`, calID, "ev-"+uuid.NewString(),
		"BEGIN:VEVENT\r\nSUMMARY:Soccer\r\nX-HOMEOS-PERSON-ID="+kid.ID+"\r\nEND:VEVENT")
	require.NoError(t, err)

	path := "/api/v1/people/" + kid.ID
	want := people.References{Notes: 2, ListItems: 1, Events: 1}

	got, err := people.NewRepo(f.pool).References(ctx, uuid.MustParse(hh), uuid.MustParse(kid.ID))
	require.NoError(t, err)
	assert.Equal(t, want, got, "repo References")

	for _, q := range []string{"", "?confirm=false", "?confirm=1"} {
		rec := f.do(t, req{method: http.MethodDelete, path: path + q, as: owner})
		require.Equal(t, http.StatusConflict, rec.Code, "%q: %s", q, rec.Body.String())
		body := decodeConflict(t, rec)
		assert.Equal(t, "CONFIRM_REQUIRED", body.Error.Code)
		assert.Equal(t, "Nora has 2 notes about them (deleted with them), 1 list item, 1 event; repeat with ?confirm=true to delete them",
			body.Error.Message)
		require.NotNil(t, body.References, rec.Body.String())
		assert.Equal(t, want, *body.References)
		assert.Contains(t, rec.Body.String(), `"meal_ratings":0`, "every count is present, zeros too")
	}
	var notes int
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT count(*) FROM notes WHERE entity_type = 'person' AND entity_id = $1`, kid.ID).Scan(&notes))
	assert.Equal(t, 2, notes, "nothing deleted without confirm")
	rec := f.do(t, req{method: http.MethodGet, path: path, as: owner})
	require.Equal(t, http.StatusOK, rec.Code, "person still there: %s", rec.Body.String())

	rec = f.do(t, req{method: http.MethodDelete, path: path + "?confirm=true", as: owner})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	rec = f.do(t, req{method: http.MethodGet, path: path, as: owner})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT count(*) FROM notes WHERE entity_type = 'person' AND entity_id = $1`, kid.ID).Scan(&notes))
	assert.Zero(t, notes, "notes about the person are deleted with them")
	var text string
	var assignee *string
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT text, assignee_person_id::text FROM list_items WHERE list_id = $1`, listID).Scan(&text, &assignee))
	assert.Equal(t, "Feed the cat", text, "list item stays")
	assert.Nil(t, assignee, "list item is unassigned")

	t.Run("nothing attached deletes without confirm", func(t *testing.T) {
		plain := f.createKid(t, owner, "Leo")
		rec := f.do(t, req{method: http.MethodDelete, path: "/api/v1/people/" + plain.ID, as: owner})
		assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	})

	t.Run("a member is refused even with confirm", func(t *testing.T) {
		spouse := f.member(t, hh, "family_member")
		_, err := f.pool.Exec(ctx,
			`INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'person', $2, 'Gift ideas', 'x')`,
			hh, spouse.personID)
		require.NoError(t, err)
		for _, q := range []string{"", "?confirm=true"} {
			rec := f.do(t, req{method: http.MethodDelete, path: "/api/v1/people/" + spouse.personID + q, as: owner})
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			body := decodeConflict(t, rec)
			assert.Equal(t, "LINKED_MEMBER", body.Error.Code, q)
			assert.Nil(t, body.References, "no counts for a member: %s", rec.Body.String())
		}
		rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/people/" + spouse.personID, as: owner})
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestPeople_CrossHouseholdIsNotFound(t *testing.T) {
	f := setup(t)
	hhA, hhB := f.household(t), f.household(t)
	ownerA := f.member(t, hhA, "owner")
	ownerB := f.member(t, hhB, "owner")
	kidA := f.createKid(t, ownerA, "Kid A")
	kidB := f.createKid(t, ownerB, "Kid B")

	rec := f.do(t, req{method: http.MethodPost, path: "/api/v1/people/" + kidA.ID + "/preferences", as: ownerA,
		body: map[string]any{"kind": "like", "value": "lego"}})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	prefA := decode[prefJSON](t, rec)

	pA := "/api/v1/people/" + kidA.ID
	prefsB := "/api/v1/people/" + kidB.ID + "/preferences"
	pref := map[string]any{"kind": "like", "value": "x"}
	cases := []struct {
		name, method, path string
		body               any
	}{
		{"get person", http.MethodGet, pA, nil},
		{"update person", http.MethodPut, pA, map[string]any{"display_name": "Pwned"}},
		{"delete person", http.MethodDelete, pA, nil},
		{"get owner A's person", http.MethodGet, "/api/v1/people/" + ownerA.personID, nil},
		{"list prefs", http.MethodGet, pA + "/preferences", nil},
		{"add pref", http.MethodPost, pA + "/preferences", pref},
		{"update pref", http.MethodPut, pA + "/preferences/" + prefA.ID, pref},
		{"delete pref", http.MethodDelete, pA + "/preferences/" + prefA.ID, nil},
		// Own person in the path, other family's preference id.
		{"update pref via own person", http.MethodPut, prefsB + "/" + prefA.ID, pref},
		{"delete pref via own person", http.MethodDelete, prefsB + "/" + prefA.ID, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(t, req{method: tc.method, path: tc.path, as: ownerB, body: tc.body})
			assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "Kid A")
		})
	}

	t.Run("list shows only the caller's family", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/people", as: ownerB})
		require.Equal(t, http.StatusOK, rec.Code)
		for _, p := range decode[[]personJSON](t, rec) {
			assert.Equal(t, hhB, p.HouseholdID)
		}
	})

	t.Run("X-Household-ID picks the family", func(t *testing.T) {
		// ownerB also joins A as a viewer: with the header set to B, A's ids
		// are still 404; with it set to A they are readable but read-only.
		f.join(t, hhA, ownerB.userID, "viewer")
		rec := f.do(t, req{method: http.MethodGet, path: pA, as: ownerB, household: hhB})
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		rec = f.do(t, req{method: http.MethodGet, path: pA, as: ownerB, household: hhA})
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rec = f.do(t, req{method: http.MethodPut, path: pA, as: ownerB, household: hhA, body: map[string]any{"display_name": "X"}})
		assert.Equal(t, http.StatusForbidden, rec.Code, "owner of B is only a viewer in A: %s", rec.Body.String())
		rec = f.do(t, req{method: http.MethodPost, path: "/api/v1/people", as: ownerB, household: hhA, body: kidBody("X")})
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	})

	var name string
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT display_name FROM people WHERE id = $1`, kidA.ID).Scan(&name))
	assert.Equal(t, "Kid A", name, "nothing in A was changed")
}

func TestPeople_RolePermissions(t *testing.T) {
	f := setup(t)
	hh := f.household(t)
	owner := f.member(t, hh, "owner")
	kid := f.createKid(t, owner, "Kid")
	readKid := f.createKid(t, owner, "Read Kid") // never edited, for the read checks
	other := f.member(t, hh, "family_member")

	// Household role policy (#4689): managers change anyone; a family member
	// changes only their own record; viewers read everything read-only; a
	// housesitter reads names, relationship and allergies only (no
	// birthdates, schools, notes or preferences); a vendor sees no people.
	tests := []struct {
		role      string
		manager   bool
		editsSelf bool
		read      int // GET people / person
		readFull  bool
		readPrefs int // GET preferences
	}{
		{"owner", true, true, http.StatusOK, true, http.StatusOK},
		{"family_manager", true, true, http.StatusOK, true, http.StatusOK},
		{"family_member", false, true, http.StatusOK, true, http.StatusOK},
		{"viewer", false, false, http.StatusOK, true, http.StatusOK},
		{"housesitter", false, false, http.StatusOK, false, http.StatusForbidden},
		{"vendor", false, false, http.StatusForbidden, false, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			m := f.member(t, hh, tt.role)
			writeCode := http.StatusForbidden
			if tt.manager {
				writeCode = http.StatusOK
			}

			rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/people", as: m})
			assert.Equal(t, tt.read, rec.Code, rec.Body.String())
			rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/people/" + readKid.ID, as: m})
			assert.Equal(t, tt.read, rec.Code, rec.Body.String())
			if tt.read == http.StatusOK {
				got := decode[personJSON](t, rec)
				assert.Equal(t, "Read Kid", got.DisplayName)
				assert.Equal(t, []string{"peanuts", "penicillin"}, got.Allergies, "allergies are visible to every reader")
				if tt.readFull {
					assert.NotNil(t, got.Birthdate)
					assert.NotNil(t, got.School)
					assert.NotNil(t, got.Preferences)
				} else {
					assert.Nil(t, got.Birthdate, "birthdate hidden")
					assert.Nil(t, got.School, "school hidden")
					assert.Nil(t, got.Grade, "grade hidden")
					assert.Nil(t, got.Preferences, "preferences hidden")
					assert.NotContains(t, rec.Body.String(), "Maple Elementary")
					assert.NotContains(t, rec.Body.String(), "2016-04-09")
				}
			}
			rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/people/" + readKid.ID + "/preferences", as: m})
			assert.Equal(t, tt.readPrefs, rec.Code, rec.Body.String())

			// Create: managers only.
			rec = f.do(t, req{method: http.MethodPost, path: "/api/v1/people", as: m, body: kidBody("New " + tt.role)})
			if tt.manager {
				assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
			} else {
				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			}

			// A kid's record and another member's record: managers only.
			for _, id := range []string{kid.ID, other.personID} {
				rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/people/" + id, as: m,
					body: map[string]any{"display_name": "Renamed by " + tt.role}})
				assert.Equal(t, writeCode, rec.Code, rec.Body.String())
			}
			rec = f.do(t, req{method: http.MethodPost, path: "/api/v1/people/" + kid.ID + "/preferences", as: m,
				body: map[string]any{"kind": "like", "value": "set by " + tt.role}})
			if tt.manager {
				assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
			} else {
				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			}

			// Family roles edit their own record and preferences; read-only
			// roles cannot, not even their own.
			rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/people/" + m.personID, as: m,
				body: map[string]any{"display_name": "Me (" + tt.role + ")", "allergies": []string{"cats"}}})
			if !tt.editsSelf {
				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
				rec = f.do(t, req{method: http.MethodPost, path: "/api/v1/people/" + m.personID + "/preferences", as: m,
					body: map[string]any{"kind": "dislike", "category": "food", "value": "olives"}})
				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			} else {
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				rec = f.do(t, req{method: http.MethodPost, path: "/api/v1/people/" + m.personID + "/preferences", as: m,
					body: map[string]any{"kind": "dislike", "category": "food", "value": "olives"}})
				require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
				own := decode[prefJSON](t, rec)
				rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/people/" + m.personID + "/preferences/" + own.ID, as: m,
					body: map[string]any{"kind": "like", "category": "food", "value": "olives"}})
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				rec = f.do(t, req{method: http.MethodDelete, path: "/api/v1/people/" + m.personID + "/preferences/" + own.ID, as: m})
				assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			}

			// Delete a kid: managers only (non-managers get 403 before anything).
			victim := f.createKid(t, owner, "Delete me "+tt.role)
			rec = f.do(t, req{method: http.MethodDelete, path: "/api/v1/people/" + victim.ID, as: m})
			if tt.manager {
				assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			} else {
				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			}
			// Nobody deletes their own linked record.
			rec = f.do(t, req{method: http.MethodDelete, path: "/api/v1/people/" + m.personID, as: m})
			if tt.manager {
				assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			} else {
				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			}
		})
	}

	t.Run("a non-manager can't touch someone else's preferences", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodPost, path: "/api/v1/people/" + other.personID + "/preferences", as: other,
			body: map[string]any{"kind": "like", "value": "tea"}})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		pref := decode[prefJSON](t, rec)
		viewer := f.member(t, hh, "viewer")
		path := "/api/v1/people/" + other.personID + "/preferences/" + pref.ID
		rec = f.do(t, req{method: http.MethodPut, path: path, as: viewer, body: map[string]any{"kind": "dislike", "value": "tea"}})
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		rec = f.do(t, req{method: http.MethodDelete, path: path, as: viewer})
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	})
}
