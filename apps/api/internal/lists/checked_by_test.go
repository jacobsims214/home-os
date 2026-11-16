package lists_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/db"
	"home-os/api/internal/lists"
	"home-os/api/internal/middleware"
)

// actorPool is testDB through db.NewPool, so the request's user reaches
// Postgres as app.user_id the way it does in production.
func actorPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := db.NewPool(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// member inserts a user named name with a membership of role in hid.
func member(t *testing.T, pool *pgxpool.Pool, hid, name, role string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id::text`,
		uuid.NewString()+"@test.homeos.local", name).Scan(&id))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	if role != "" {
		_, err := pool.Exec(ctx, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hid, id, role)
		require.NoError(t, err)
	}
	return id
}

// as makes requests as user (claims and database actor, as RequireAuth does).
type as struct {
	t               *testing.T
	h               http.Handler
	hid, user, role string
}

func (c as) do(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(c.t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	claims := &auth.Claims{UserID: c.user, HouseholdID: c.hid, Role: c.role}
	req = req.WithContext(middleware.WithClaims(req.Context(), claims))
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

type checkedJSON struct {
	ID            string  `json:"id"`
	Text          string  `json:"text"`
	Checked       bool    `json:"checked"`
	CheckedAt     *string `json:"checked_at"`
	CheckedBy     *string `json:"checked_by"`
	CheckedByName *string `json:"checked_by_name"`
}

func TestItems_CheckedBy(t *testing.T) {
	pool := actorPool(t)
	r := chi.NewRouter()
	lists.NewHandler(lists.NewRepo(pool)).RegisterRoutes(r)

	hid := household(t, pool)
	amber := member(t, pool, hid, "Amber", auth.RoleFamilyMember)
	bob := member(t, pool, hid, "Bob", auth.RoleOwner)
	sitter := member(t, pool, hid, "Sam", auth.RoleHousesitter)
	outsider := member(t, pool, household(t, pool), "Olga", auth.RoleOwner) // a user of another family

	a := as{t: t, h: r, hid: hid, user: amber, role: auth.RoleFamilyMember}
	b := as{t: t, h: r, hid: hid, user: bob, role: auth.RoleOwner}
	s := as{t: t, h: r, hid: hid, user: sitter, role: auth.RoleHousesitter}
	o := as{t: t, h: r, hid: hid, user: outsider, role: auth.RoleOwner}

	rec := b.do("POST", "/api/v1/lists", map[string]any{"name": "Groceries", "kind": "grocery",
		"items": items("milk", "eggs", "bread", "jam")})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		Data struct {
			ID    string        `json:"id"`
			Items []checkedJSON `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	lid := created.Data.ID
	milk, eggs, bread, jam := created.Data.Items[0].ID, created.Data.Items[1].ID, created.Data.Items[2].ID, created.Data.Items[3].ID
	for _, it := range created.Data.Items {
		assert.Nil(t, it.CheckedBy, "new items are open")
	}

	byID := func() map[string]checkedJSON {
		t.Helper()
		rec := b.do("GET", "/api/v1/lists/"+lid, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out struct {
			Data struct {
				Items []checkedJSON `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		m := map[string]checkedJSON{}
		for _, it := range out.Data.Items {
			m[it.ID] = it
		}
		return m
	}
	assertBy := func(it checkedJSON, user, name string) {
		t.Helper()
		if user == "" {
			assert.Nil(t, it.CheckedBy, it.Text)
			assert.Nil(t, it.CheckedByName, it.Text)
			return
		}
		require.NotNil(t, it.CheckedBy, it.Text)
		require.NotNil(t, it.CheckedByName, it.Text)
		assert.Equal(t, user, *it.CheckedBy, it.Text)
		assert.Equal(t, name, *it.CheckedByName, it.Text)
	}

	// Batch check: the response and GET carry the caller.
	rec = a.do("POST", "/api/v1/lists/"+lid+"/items/check", map[string]any{"item_ids": []string{milk, eggs}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var batch struct {
		Data []checkedJSON `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &batch))
	require.Len(t, batch.Data, 2)
	for _, it := range batch.Data {
		assertBy(it, amber, "Amber")
	}
	got := byID()
	assertBy(got[milk], amber, "Amber")
	assertBy(got[eggs], amber, "Amber")
	assertBy(got[bread], "", "")

	// Checking an item that is already checked keeps the first checker.
	require.Equal(t, http.StatusOK, b.do("POST", "/api/v1/lists/"+lid+"/items/check",
		map[string]any{"item_ids": []string{milk}}).Code)
	require.Equal(t, http.StatusOK, b.do("PATCH", "/api/v1/lists/"+lid+"/items/"+milk,
		map[string]any{"checked": true}).Code)
	assertBy(byID()[milk], amber, "Amber")

	// A housesitter's check-off (PATCH with only checked) is recorded too.
	rec = s.do("PATCH", "/api/v1/lists/"+lid+"/items/"+bread, map[string]any{"checked": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assertBy(byID()[bread], sitter, "Sam")

	// Editing a checked item's text keeps who checked it.
	require.Equal(t, http.StatusOK, b.do("PATCH", "/api/v1/lists/"+lid+"/items/"+bread,
		map[string]any{"text": "Sourdough"}).Code)
	assertBy(byID()[bread], sitter, "Sam")

	// Uncheck clears it; checking again records the new caller.
	require.Equal(t, http.StatusOK, b.do("POST", "/api/v1/lists/"+lid+"/items/uncheck",
		map[string]any{"item_ids": []string{milk}}).Code)
	got = byID()
	assertBy(got[milk], "", "")
	assert.Nil(t, got[milk].CheckedAt)
	require.Equal(t, http.StatusOK, b.do("POST", "/api/v1/lists/"+lid+"/items/check",
		map[string]any{"item_ids": []string{milk}}).Code)
	assertBy(byID()[milk], bob, "Bob")
	require.Equal(t, http.StatusOK, b.do("PATCH", "/api/v1/lists/"+lid+"/items/"+bread,
		map[string]any{"checked": false}).Code)
	assertBy(byID()[bread], "", "")

	// A user who is not a member of this family is never recorded: the
	// check-off still works, anonymously (same-family FK, no 500).
	require.Equal(t, http.StatusOK, o.do("POST", "/api/v1/lists/"+lid+"/items/check",
		map[string]any{"item_ids": []string{jam}}).Code)
	got = byID()
	assert.True(t, got[jam].Checked)
	assertBy(got[jam], "", "")

	// The change feed carries it as well.
	rec = b.do("GET", "/api/v1/lists/"+lid+"/items", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"checked_by_name":"Amber"`)

	// The same-family FK refuses another family's user, and removing a
	// membership clears only checked_by.
	_, err := pool.Exec(context.Background(), `UPDATE list_items SET checked_by = $1 WHERE id = $2`, outsider, jam)
	require.Error(t, err)
	_, err = pool.Exec(context.Background(), `DELETE FROM memberships WHERE household_id = $1 AND user_id = $2`, hid, amber)
	require.NoError(t, err)
	got = byID()
	assert.True(t, got[eggs].Checked)
	assert.NotNil(t, got[eggs].CheckedAt)
	assertBy(got[eggs], "", "")
}
