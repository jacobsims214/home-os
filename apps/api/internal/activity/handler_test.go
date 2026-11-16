package activity_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/activity"
	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
)

const testIssuer = "https://dex.test/dex"

type fixture struct {
	pool *pgxpool.Pool
	key  *rsa.PrivateKey
	v    *auth.Verifier
}

// setup connects to TEST_DATABASE_URL (all migrations applied; skips when
// unset). Routers are built like cmd/api/main.go: the activity routes behind
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
	return &fixture{pool: pool, key: key, v: v}
}

func (f *fixture) router(policy activity.ReadPolicy) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(f.v))
		activity.NewHandler(activity.NewRepo(f.pool)).WithReadPolicy(policy).RegisterRoutes(r)
	})
	return r
}

type member struct {
	token, userID, personID, householdID string
}

func (f *fixture) household(t *testing.T, name string) string {
	t.Helper()
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`INSERT INTO households (name) VALUES ($1) RETURNING id::text`, name).Scan(&id))
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, id) })
	return id
}

// member creates a user named name with role in hhID; the memberships
// trigger (041) creates their person record, whose display_name is the name.
func (f *fixture) member(t *testing.T, hhID, name, role string) member {
	t.Helper()
	ctx := context.Background()
	email := "activity-" + uuid.NewString()[:8] + "@example.com"
	sub := "CiQ" + uuid.NewString() + "EgVsb2NhbA"
	var userID string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, $2, 'x', $3) RETURNING id::text`,
		email, name, sub).Scan(&userID))
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	_, err := f.pool.Exec(ctx, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hhID, userID, role)
	require.NoError(t, err)
	var personID string
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT id::text FROM people WHERE household_id = $1 AND user_id = $2`, hhID, userID).Scan(&personID))

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": testIssuer, "aud": "home-os-ui", "sub": sub, "email": email,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	token, err := tok.SignedString(f.key)
	require.NoError(t, err)
	return member{token: token, userID: userID, personID: personID, householdID: hhID}
}

// agentToken creates an agent token for m (the row only; the hash is random).
func (f *fixture) agentToken(t *testing.T, m member, name string) string {
	t.Helper()
	hash := make([]byte, 32)
	_, _ = rand.Read(hash)
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`INSERT INTO agent_tokens (user_id, household_id, name, token_prefix, token_hash)
		 VALUES ($1, $2, $3, 'hos_test', $4) RETURNING id::text`,
		m.userID, m.householdID, name, hash).Scan(&id))
	return id
}

// write runs sql in a transaction with app.user_id (and app.agent_token_id
// when agentID is set) so the 050 triggers record the actor.
func (f *fixture) write(t *testing.T, userID, agentID, sql string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SELECT set_config('app.user_id', $1, true), set_config('app.agent_token_id', $2, true)`, userID, agentID)
	require.NoError(t, err)
	var id string
	if strings.Contains(sql, "RETURNING") {
		require.NoError(t, tx.QueryRow(ctx, sql, args...).Scan(&id))
	} else {
		_, err = tx.Exec(ctx, sql, args...)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit(ctx))
	return id
}

type entry struct {
	ID            int64    `json:"id"`
	Action        string   `json:"action"`
	EntityType    string   `json:"entity_type"`
	EntityID      string   `json:"entity_id"`
	ParentType    *string  `json:"parent_type"`
	ParentID      *string  `json:"parent_id"`
	ChangedFields []string `json:"changed_fields"`
	Summary       string   `json:"summary"`
	CreatedAt     string   `json:"created_at"`
	Actor         *struct {
		UserID   string  `json:"user_id"`
		PersonID *string `json:"person_id"`
		Name     *string `json:"name"`
	} `json:"actor"`
	ViaAgent bool `json:"via_agent"`
	Agent    *struct {
		TokenID string  `json:"token_id"`
		Name    *string `json:"name"`
	} `json:"agent"`
	Description string `json:"description"`
}

type page struct {
	Data       []entry `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

func get(t *testing.T, h http.Handler, as member, query url.Values, household string) (*httptest.ResponseRecorder, page) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/activity?"+query.Encode(), nil)
	r.Header.Set("Authorization", "Bearer "+as.token)
	if household != "" {
		r.Header.Set("X-Household-ID", household)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var p page
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	}
	return rec, p
}

// seeded is a household with two members, an agent and a known history.
type seeded struct {
	hh, other              string
	dana, sam, outsider    member
	agentID                string
	listID, itemID, secret string
}

func seed(t *testing.T, f *fixture) seeded {
	t.Helper()
	s := seeded{hh: f.household(t, "Activity HH"), other: f.household(t, "Other HH")}
	s.dana = f.member(t, s.hh, "Dana", "owner")
	s.sam = f.member(t, s.hh, "Sam", "family_member")
	s.outsider = f.member(t, s.other, "Olive", "owner")
	s.agentID = f.agentToken(t, s.dana, "Claude")
	// Joining creates each member's person record (041), logged with no
	// actor; start the history clean so the assertions name every row.
	_, err := f.pool.Exec(context.Background(),
		`DELETE FROM activity_log WHERE household_id IN ($1, $2)`, s.hh, s.other)
	require.NoError(t, err)

	s.listID = f.write(t, s.dana.userID, "",
		`INSERT INTO lists (household_id, name) VALUES ($1, 'Groceries') RETURNING id::text`, s.hh)
	s.itemID = f.write(t, s.sam.userID, "",
		`INSERT INTO list_items (list_id, household_id, text, position) VALUES ($1, $2, 'Milk', 1) RETURNING id::text`,
		s.listID, s.hh)
	s.secret = f.write(t, s.dana.userID, s.agentID,
		`INSERT INTO secrets (household_id, name, secret_type, encrypted_data, iv)
		 VALUES ($1, 'Wifi', 'password', 'SUPER-SECRET-CIPHERTEXT'::bytea, 'iv-bytes'::bytea) RETURNING id::text`, s.hh)
	f.write(t, s.sam.userID, "",
		`UPDATE secrets SET encrypted_data = 'ANOTHER-SECRET-VALUE'::bytea WHERE id = $1`, s.secret)
	// A system write (no actor) and another family's activity.
	f.write(t, "", "", `UPDATE lists SET name = 'Weekly groceries' WHERE id = $1`, s.listID)
	f.write(t, s.outsider.userID, "",
		`INSERT INTO lists (household_id, name) VALUES ($1, 'Not yours') RETURNING id::text`, s.other)
	return s
}

func TestList_NewestFirstWithActors(t *testing.T) {
	f := setup(t)
	s := seed(t, f)
	h := f.router(nil)

	rec, p := get(t, h, s.sam, nil, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Len(t, p.Data, 5, "only this household's five changes")
	assert.Nil(t, p.NextCursor)
	for i := 1; i < len(p.Data); i++ {
		assert.Greater(t, p.Data[i-1].ID, p.Data[i].ID, "newest first")
	}

	sys, upd, ins, item, list := p.Data[0], p.Data[1], p.Data[2], p.Data[3], p.Data[4]

	assert.Equal(t, "update", sys.Action)
	assert.Nil(t, sys.Actor)
	assert.False(t, sys.ViaAgent)
	assert.True(t, strings.HasPrefix(sys.Description, "System updated list"), sys.Description)

	assert.Equal(t, "secret", upd.EntityType)
	assert.Equal(t, []string{"encrypted_data"}, upd.ChangedFields)
	require.NotNil(t, upd.Actor)
	assert.Equal(t, "Sam", *upd.Actor.Name)

	assert.Equal(t, "create", ins.Action)
	require.NotNil(t, ins.Actor)
	assert.Equal(t, s.dana.userID, ins.Actor.UserID)
	assert.Equal(t, s.dana.personID, *ins.Actor.PersonID)
	assert.Equal(t, "Dana", *ins.Actor.Name)
	assert.True(t, ins.ViaAgent)
	require.NotNil(t, ins.Agent)
	assert.Equal(t, s.agentID, ins.Agent.TokenID)
	assert.Equal(t, "Claude", *ins.Agent.Name)
	assert.Equal(t, `Dana created secret "Wifi" via agent "Claude"`, ins.Description)

	assert.Equal(t, "list_item", item.EntityType)
	assert.Equal(t, s.itemID, item.EntityID)
	require.NotNil(t, item.ParentID)
	assert.Equal(t, "list", *item.ParentType)
	assert.Equal(t, s.listID, *item.ParentID)
	assert.Equal(t, "Sam", *item.Actor.Name)

	assert.Equal(t, "list", list.EntityType)
	assert.Equal(t, []string{}, list.ChangedFields)
	assert.Equal(t, `Dana created list "Groceries"`, list.Description)

	// Secret material never reaches the response, nor agent token details.
	body := rec.Body.String()
	for _, leak := range []string{"SUPER-SECRET", "ANOTHER-SECRET", "iv-bytes", "hos_test", "Not yours", "@example.com"} {
		assert.NotContains(t, body, leak)
	}
}

func TestList_Filters(t *testing.T) {
	f := setup(t)
	s := seed(t, f)
	h := f.router(nil)

	ids := func(p page) (out []string) {
		for _, e := range p.Data {
			out = append(out, e.Action+" "+e.EntityType)
		}
		return out
	}
	cases := []struct {
		name  string
		query url.Values
		want  []string
	}{
		{"member by user id", url.Values{"member": {s.sam.userID}}, []string{"update secret", "create list_item"}},
		{"member by person id", url.Values{"member": {s.sam.personID}}, []string{"update secret", "create list_item"}},
		{"member from another household", url.Values{"member": {s.outsider.userID}}, nil},
		{"entity type", url.Values{"entity_type": {"list"}}, []string{"update list", "create list"}},
		{"entity id", url.Values{"entity_id": {s.secret}}, []string{"update secret", "create secret"}},
		// The list itself is family-level (no parent); its items sit in it.
		{"parent", url.Values{"parent_type": {"list"}, "parent_id": {s.listID}}, []string{"create list_item"}},
		{"type and member", url.Values{"entity_type": {"secret"}, "member": {s.dana.userID}}, []string{"create secret"}},
		{"since future", url.Values{"since": {time.Now().Add(time.Hour).Format(time.RFC3339)}}, nil},
		{"since past", url.Values{"since": {time.Now().Add(-time.Hour).Format(time.RFC3339)}, "entity_type": {"list_item"}}, []string{"create list_item"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, p := get(t, h, s.dana, tc.query, "")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, tc.want, ids(p))
		})
	}
}

func TestList_PagingAndPolling(t *testing.T) {
	f := setup(t)
	s := seed(t, f)
	h := f.router(nil)

	_, all := get(t, h, s.dana, nil, "")
	require.Len(t, all.Data, 5)

	var seen []int64
	q := url.Values{"limit": {"2"}}
	for pages := 0; pages < 5; pages++ {
		rec, p := get(t, h, s.dana, q, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		for _, e := range p.Data {
			seen = append(seen, e.ID)
		}
		if p.NextCursor == nil {
			break
		}
		q.Set("cursor", *p.NextCursor)
	}
	var want []int64
	for _, e := range all.Data {
		want = append(want, e.ID)
	}
	assert.Equal(t, want, seen, "pages cover the feed once, in order")

	// Poll: nothing newer than the newest, then one new change.
	newest := all.Data[0].ID
	after := url.Values{"after": {jsonInt(newest)}}
	_, p := get(t, h, s.dana, after, "")
	assert.Empty(t, p.Data)
	f.write(t, s.sam.userID, "", `UPDATE list_items SET text = 'Oat milk' WHERE id = $1`, s.itemID)
	_, p = get(t, h, s.dana, after, "")
	require.Len(t, p.Data, 1)
	assert.Equal(t, []string{"text"}, p.Data[0].ChangedFields)
	assert.Equal(t, `Sam updated list item "Oat milk" (text)`, p.Data[0].Description)
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestList_HouseholdScoping(t *testing.T) {
	f := setup(t)
	s := seed(t, f)
	h := f.router(nil)

	// The outsider sees only their own family's entry.
	rec, p := get(t, h, s.outsider, nil, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, p.Data, 1)
	assert.Contains(t, p.Data[0].Summary, "Not yours")

	// X-Household-ID naming a family they don't belong to is refused.
	rec, _ = get(t, h, s.outsider, nil, s.hh)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// A member of both families picks one with X-Household-ID.
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'family_member')`, s.other, s.dana.userID)
	require.NoError(t, err)
	rec, p = get(t, h, s.dana, nil, s.other)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, p.Data, 2, "joining logged Dana's new person record there")
	assert.Equal(t, `created person "Dana"`, p.Data[0].Summary)
	assert.Contains(t, p.Data[1].Summary, "Not yours")
	rec, p = get(t, h, s.dana, nil, s.hh)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, p.Data, 5)
}

func TestList_BadParams(t *testing.T) {
	f := setup(t)
	s := seed(t, f)
	h := f.router(nil)
	for _, q := range []url.Values{
		{"limit": {"0"}}, {"limit": {"201"}}, {"limit": {"x"}},
		{"cursor": {"abc"}}, {"cursor": {"-1"}}, {"after": {"0"}},
		{"since": {"yesterday"}}, {"member": {"nope"}},
		{"entity_id": {"1"}}, {"parent_id": {"x"}},
		{"entity_type": {"Bill; DROP"}}, {"parent_type": {"1list"}},
	} {
		rec, _ := get(t, h, s.dana, q, "")
		assert.Equal(t, http.StatusBadRequest, rec.Code, q.Encode())
	}
}

func TestList_ReadPolicyHook(t *testing.T) {
	f := setup(t)
	s := seed(t, f)

	// A policy that hides secrets from plain members and refuses no one.
	hideSecrets := f.router(func(role string) (bool, []string) {
		if role == "family_member" {
			return true, []string{"secret"}
		}
		return true, nil
	})
	_, p := get(t, hideSecrets, s.sam, nil, "")
	require.Len(t, p.Data, 3)
	for _, e := range p.Data {
		assert.NotEqual(t, "secret", e.EntityType)
	}
	_, p = get(t, hideSecrets, s.dana, nil, "")
	assert.Len(t, p.Data, 5)

	// A policy that refuses a role answers 403.
	deny := f.router(func(role string) (bool, []string) { return role != "family_member", nil })
	rec, _ := get(t, deny, s.sam, nil, "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestList_NamesCalendarEvents: migration 055 names events from ical_data's
// JSON title (050 only read an iCal SUMMARY line), so every row, a deleted
// event's included, carries the name it had when written. The feed still
// fills the name from a live event for rows logged before 055 (see repo.go).
func TestList_NamesCalendarEvents(t *testing.T) {
	f := setup(t)
	s := seed(t, f)
	h := f.router(nil)

	cal := f.write(t, s.dana.userID, "",
		`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', $2) RETURNING id::text`,
		s.hh, uuid.NewString())
	jsonEvent := f.write(t, s.sam.userID, "",
		`INSERT INTO calendar_objects (calendar_id, uid, etag, ical_data)
		 VALUES ($1, $2, 'e1', '{"title":"Date  night","start":"2026-11-28T19:00:00Z","description":"PRIVATE-DETAILS"}')
		 RETURNING id::text`, cal, uuid.NewString())
	f.write(t, s.dana.userID, "",
		`UPDATE calendar_objects SET ical_data = '{"title":"Date night","start":"2026-11-29T19:00:00Z","description":"PRIVATE-DETAILS"}' WHERE id = $1`,
		jsonEvent)
	icsEvent := f.write(t, s.sam.userID, "",
		`INSERT INTO calendar_objects (calendar_id, uid, etag, ical_data)
		 VALUES ($1, $2, 'e2', E'BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nSUMMARY;LANGUAGE=en:Dentist\r\nEND:VEVENT\r\nEND:VCALENDAR')
		 RETURNING id::text`, cal, uuid.NewString())
	gone := f.write(t, s.sam.userID, "",
		`INSERT INTO calendar_objects (calendar_id, uid, etag, ical_data)
		 VALUES ($1, $2, 'e3', '{"title":"Cancelled"}') RETURNING id::text`, cal, uuid.NewString())
	f.write(t, s.dana.userID, "", `DELETE FROM calendar_objects WHERE id = $1`, gone)

	rec, p := get(t, h, s.dana, url.Values{"entity_type": {"calendar_event"}}, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := map[string][]string{}
	for _, e := range p.Data {
		got[e.EntityID] = append(got[e.EntityID], e.Description)
	}
	assert.Equal(t, []string{
		`Dana updated calendar event "Date night" (ical_data, starts_at)`,
		`Sam created calendar event "Date night"`,
	}, got[jsonEvent])
	assert.Equal(t, []string{`Sam created calendar event "Dentist"`}, got[icsEvent])
	assert.Equal(t, []string{`Dana deleted calendar event "Cancelled"`, `Sam created calendar event "Cancelled"`}, got[gone],
		"055 names a JSON event in the log itself, so it stays named after delete")
	assert.NotContains(t, rec.Body.String(), "PRIVATE-DETAILS")
}
