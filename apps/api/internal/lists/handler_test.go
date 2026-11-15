package lists_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/lists"
	"home-os/api/internal/middleware"
)

// testDB connects to TEST_DATABASE_URL (all migrations applied). Skips when unset.
func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// router mounts the lists routes behind a stand-in for RequireAuth that
// takes the household and role from test headers.
func router(h *lists.Handler) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				claims := &auth.Claims{
					UserID:      uuid.NewString(),
					HouseholdID: req.Header.Get("X-Test-Household"),
					Role:        req.Header.Get("X-Test-Role"),
				}
				next.ServeHTTP(w, req.WithContext(middleware.WithClaims(req.Context(), claims)))
			})
		})
		h.RegisterRoutes(r)
	})
	return r
}

type client struct {
	t    *testing.T
	h    http.Handler
	hid  string
	role string
}

func (c client) do(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(c.t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Household", c.hid)
	role := c.role
	if role == "" {
		role = auth.RoleFamilyMember
	}
	req.Header.Set("X-Test-Role", role)
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
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

type itemJSON struct {
	ID               string   `json:"id"`
	ListID           string   `json:"list_id"`
	Text             string   `json:"text"`
	Quantity         *float64 `json:"quantity"`
	Unit             *string  `json:"unit"`
	Checked          bool     `json:"checked"`
	CheckedAt        *string  `json:"checked_at"`
	AssigneePersonID *string  `json:"assignee_person_id"`
	DueDate          *string  `json:"due_date"`
	Position         float64  `json:"position"`
	UpdatedAt        string   `json:"updated_at"`
}

type listJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	EntityType string     `json:"entity_type"`
	EntityID   string     `json:"entity_id"`
	IsTemplate bool       `json:"is_template"`
	Archived   bool       `json:"archived"`
	ItemCount  int        `json:"item_count"`
	OpenCount  int        `json:"open_count"`
	Items      []itemJSON `json:"items"`
}

// household inserts a household and returns its id.
func household(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO households (name) VALUES ('Lists HH') RETURNING id::text`).Scan(&id))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, id) })
	return id
}

func person(t *testing.T, pool *pgxpool.Pool, hid string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO people (household_id, display_name) VALUES ($1, 'Kid') RETURNING id::text`, hid).Scan(&id))
	return id
}

func property(t *testing.T, pool *pgxpool.Pool, hid string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO properties (household_id, name) VALUES ($1, 'Cabin') RETURNING id::text`, hid).Scan(&id))
	return id
}

func setup(t *testing.T) (*pgxpool.Pool, http.Handler) {
	t.Helper()
	pool := testDB(t)
	return pool, router(lists.NewHandler(lists.NewRepo(pool)))
}

func newList(t *testing.T, c client, body map[string]any) listJSON {
	t.Helper()
	rec := c.do("POST", "/api/v1/lists", body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	return decode[listJSON](t, rec)
}

func items(texts ...string) []map[string]any {
	out := make([]map[string]any, 0, len(texts))
	for _, s := range texts {
		out = append(out, map[string]any{"text": s})
	}
	return out
}

func texts(its []itemJSON) []string {
	out := make([]string, 0, len(its))
	for _, it := range its {
		out = append(out, it.Text)
	}
	return out
}

func getList(t *testing.T, c client, id string) listJSON {
	t.Helper()
	rec := c.do("GET", "/api/v1/lists/"+id, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decode[listJSON](t, rec)
}

// --- no database ---

func TestPolicy_Hook(t *testing.T) {
	var seen []lists.Action
	policy := func(role string, a lists.Action) bool {
		seen = append(seen, a)
		return role == auth.RoleHousesitter && a == lists.ActionCheckItems
	}
	// The policy runs before the repo is touched, so no database is needed.
	h := router(lists.NewHandler(nil).WithPolicy(policy))
	c := client{t: t, h: h, hid: uuid.NewString(), role: auth.RoleViewer}
	id, item := uuid.NewString(), uuid.NewString()

	cases := []struct {
		method, path string
		body         any
		want         lists.Action
	}{
		{"GET", "/api/v1/lists", nil, lists.ActionRead},
		{"POST", "/api/v1/lists", map[string]any{"name": "x"}, lists.ActionManage},
		{"POST", "/api/v1/lists/" + id + "/archive", nil, lists.ActionManage},
		{"POST", "/api/v1/lists/" + id + "/items", map[string]any{"items": items("a")}, lists.ActionEditItems},
		{"POST", "/api/v1/lists/" + id + "/items/check", map[string]any{"item_ids": []string{item}}, lists.ActionCheckItems},
		{"PATCH", "/api/v1/lists/" + id + "/items/" + item, map[string]any{"checked": true}, lists.ActionCheckItems},
		{"PATCH", "/api/v1/lists/" + id + "/items/" + item, map[string]any{"checked": true, "text": "y"}, lists.ActionEditItems},
		{"POST", "/api/v1/lists/" + id + "/items/" + item + "/move", map[string]any{}, lists.ActionEditItems},
	}
	for _, tc := range cases {
		seen = nil
		rec := c.do(tc.method, tc.path, tc.body)
		assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s", tc.method, tc.path)
		assert.Equal(t, []lists.Action{tc.want}, seen, "%s %s", tc.method, tc.path)
	}
}

// --- database ---

func TestLists_CRUDAndArchive(t *testing.T) {
	pool, h := setup(t)
	hid := household(t, pool)
	c := client{t: t, h: h, hid: hid}

	l := newList(t, c, map[string]any{"name": " Groceries ", "kind": "grocery", "items": items("milk", "eggs")})
	assert.Equal(t, "Groceries", l.Name)
	assert.Equal(t, "household", l.EntityType)
	assert.Equal(t, hid, l.EntityID)
	assert.Equal(t, []string{"milk", "eggs"}, texts(l.Items))
	assert.Equal(t, 2, l.ItemCount)

	// Validation.
	assert.Equal(t, http.StatusBadRequest, c.do("POST", "/api/v1/lists", map[string]any{"name": ""}).Code)
	assert.Equal(t, http.StatusBadRequest, c.do("POST", "/api/v1/lists", map[string]any{"name": "x", "kind": "nope"}).Code)
	assert.Equal(t, http.StatusBadRequest, c.do("POST", "/api/v1/lists",
		map[string]any{"name": "x", "entity_type": "spaceship", "entity_id": uuid.NewString()}).Code)

	rec := c.do("PATCH", "/api/v1/lists/"+l.ID, map[string]any{"name": "Weekly shop", "kind": "shopping"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "Weekly shop", decode[listJSON](t, rec).Name)

	// Archive hides it from the default listing and freezes items.
	require.Equal(t, http.StatusOK, c.do("POST", "/api/v1/lists/"+l.ID+"/archive", nil).Code)
	assert.Empty(t, decode[[]listJSON](t, c.do("GET", "/api/v1/lists", nil)))
	assert.Len(t, decode[[]listJSON](t, c.do("GET", "/api/v1/lists?archived=true", nil)), 1)
	assert.Equal(t, http.StatusConflict,
		c.do("POST", "/api/v1/lists/"+l.ID+"/items", map[string]any{"items": items("bread")}).Code)
	assert.Equal(t, http.StatusConflict, c.do("POST", "/api/v1/lists/"+l.ID+"/items/check",
		map[string]any{"item_ids": []string{l.Items[0].ID}}).Code)
	require.Equal(t, http.StatusOK, c.do("POST", "/api/v1/lists/"+l.ID+"/unarchive", nil).Code)
	assert.Len(t, decode[[]listJSON](t, c.do("GET", "/api/v1/lists?kind=shopping", nil)), 1)

	require.Equal(t, http.StatusNoContent, c.do("DELETE", "/api/v1/lists/"+l.ID, nil).Code)
	assert.Equal(t, http.StatusNotFound, c.do("GET", "/api/v1/lists/"+l.ID, nil).Code)
}

func TestLists_SubjectAndCrossFamily(t *testing.T) {
	pool, h := setup(t)
	a := client{t: t, h: h, hid: household(t, pool)}
	b := client{t: t, h: h, hid: household(t, pool)}
	propA, propB := property(t, pool, a.hid), property(t, pool, b.hid)
	kidB := person(t, pool, b.hid)

	l := newList(t, a, map[string]any{"name": "Cabin packing", "kind": "packing",
		"entity_type": "property", "entity_id": propA, "items": items("towels")})
	assert.Equal(t, "property", l.EntityType)
	assert.Equal(t, propA, l.EntityID)
	assert.Len(t, decode[[]listJSON](t, a.do("GET", "/api/v1/lists?entity_type=property&entity_id="+propA, nil)), 1)

	// Another family's subject or assignee is a 404.
	assert.Equal(t, http.StatusNotFound, a.do("POST", "/api/v1/lists",
		map[string]any{"name": "x", "entity_type": "property", "entity_id": propB}).Code)
	assert.Equal(t, http.StatusNotFound, a.do("PATCH", "/api/v1/lists/"+l.ID,
		map[string]any{"entity_type": "property", "entity_id": propB}).Code)
	assert.Equal(t, http.StatusNotFound, a.do("POST", "/api/v1/lists/"+l.ID+"/items",
		map[string]any{"items": []map[string]any{{"text": "x", "assignee_person_id": kidB}}}).Code)

	// Back to the household.
	rec := a.do("PATCH", "/api/v1/lists/"+l.ID, map[string]any{"entity_type": "household"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, a.hid, decode[listJSON](t, rec).EntityID)

	// Family B cannot see or touch family A's list or items.
	item := l.Items[0].ID
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/lists/" + l.ID, nil},
		{"GET", "/api/v1/lists/" + l.ID + "/items", nil},
		{"PATCH", "/api/v1/lists/" + l.ID, map[string]any{"name": "pwned"}},
		{"DELETE", "/api/v1/lists/" + l.ID, nil},
		{"POST", "/api/v1/lists/" + l.ID + "/archive", nil},
		{"POST", "/api/v1/lists/" + l.ID + "/items", map[string]any{"items": items("x")}},
		{"POST", "/api/v1/lists/" + l.ID + "/items/check", map[string]any{"item_ids": []string{item}}},
		{"POST", "/api/v1/lists/" + l.ID + "/items/remove", map[string]any{"item_ids": []string{item}}},
		{"PATCH", "/api/v1/lists/" + l.ID + "/items/" + item, map[string]any{"checked": true}},
		{"DELETE", "/api/v1/lists/" + l.ID + "/items/" + item, nil},
		{"POST", "/api/v1/lists/" + l.ID + "/items/" + item + "/move", map[string]any{}},
		{"POST", "/api/v1/lists", map[string]any{"template_id": l.ID}},
	} {
		rec := b.do(tc.method, tc.path, tc.body)
		assert.Equal(t, http.StatusNotFound, rec.Code, "%s %s: %s", tc.method, tc.path, rec.Body.String())
	}
	assert.Empty(t, decode[[]listJSON](t, b.do("GET", "/api/v1/lists", nil)))
	got := getList(t, a, l.ID)
	assert.Equal(t, "Cabin packing", got.Name)
	assert.False(t, got.Items[0].Checked)
}

func TestItems_BatchOpsAreAtomic(t *testing.T) {
	pool, h := setup(t)
	c := client{t: t, h: h, hid: household(t, pool)}
	other := client{t: t, h: h, hid: household(t, pool)}
	l := newList(t, c, map[string]any{"name": "Todo", "kind": "todo", "items": items("a", "b", "c")})
	ol := newList(t, other, map[string]any{"name": "Theirs", "items": items("z")})
	ids := []string{l.Items[0].ID, l.Items[1].ID}
	foreign := ol.Items[0].ID

	// Check with one id from another family: nothing changes.
	rec := c.do("POST", "/api/v1/lists/"+l.ID+"/items/check", map[string]any{"item_ids": append(ids, foreign)})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	for _, it := range getList(t, c, l.ID).Items {
		assert.False(t, it.Checked, it.Text)
	}
	assert.False(t, getList(t, other, ol.ID).Items[0].Checked)

	// Check with a missing id: nothing changes.
	assert.Equal(t, http.StatusNotFound, c.do("POST", "/api/v1/lists/"+l.ID+"/items/check",
		map[string]any{"item_ids": append(ids, uuid.NewString())}).Code)

	// Check two (duplicates are fine): both checked with checked_at.
	rec = c.do("POST", "/api/v1/lists/"+l.ID+"/items/check", map[string]any{"item_ids": append(ids, ids[0])})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	checked := decode[[]itemJSON](t, rec)
	require.Len(t, checked, 2)
	for _, it := range checked {
		assert.True(t, it.Checked)
		require.NotNil(t, it.CheckedAt)
	}
	firstAt := *checked[0].CheckedAt

	// Checking again is a no-op that keeps checked_at.
	rec = c.do("POST", "/api/v1/lists/"+l.ID+"/items/check", map[string]any{"item_ids": ids[:1]})
	assert.Equal(t, firstAt, *decode[[]itemJSON](t, rec)[0].CheckedAt)
	got := getList(t, c, l.ID)
	assert.Equal(t, 1, got.OpenCount)

	rec = c.do("POST", "/api/v1/lists/"+l.ID+"/items/uncheck", map[string]any{"item_ids": ids[:1]})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, decode[[]itemJSON](t, rec)[0].CheckedAt)

	// Add where the second item is invalid at the DB (foreign assignee):
	// neither is added.
	kidOther := person(t, pool, other.hid)
	rec = c.do("POST", "/api/v1/lists/"+l.ID+"/items", map[string]any{"items": []map[string]any{
		{"text": "ok"}, {"text": "bad", "assignee_person_id": kidOther}}})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Len(t, getList(t, c, l.ID).Items, 3)
	assert.Equal(t, http.StatusBadRequest, c.do("POST", "/api/v1/lists/"+l.ID+"/items",
		map[string]any{"items": []map[string]any{{"text": "ok"}, {"text": "  "}}}).Code)

	// Remove with a bogus id: nothing removed. Then remove two.
	assert.Equal(t, http.StatusNotFound, c.do("POST", "/api/v1/lists/"+l.ID+"/items/remove",
		map[string]any{"item_ids": append(ids, foreign)}).Code)
	assert.Len(t, getList(t, c, l.ID).Items, 3)
	assert.Len(t, getList(t, other, ol.ID).Items, 1)
	require.Equal(t, http.StatusNoContent, c.do("POST", "/api/v1/lists/"+l.ID+"/items/remove",
		map[string]any{"item_ids": ids}).Code)
	assert.Equal(t, []string{"c"}, texts(getList(t, c, l.ID).Items))
}

func TestItems_PatchAndFeed(t *testing.T) {
	pool, h := setup(t)
	c := client{t: t, h: h, hid: household(t, pool)}
	kid := person(t, pool, c.hid)
	l := newList(t, c, map[string]any{"name": "Chores", "kind": "chores", "items": []map[string]any{
		{"text": "flour", "quantity": 2.5, "unit": "kg"}, {"text": "dishes"}}})
	flour, dishes := l.Items[0], l.Items[1]
	require.NotNil(t, flour.Quantity)
	assert.InDelta(t, 2.5, *flour.Quantity, 1e-9)

	path := "/api/v1/lists/" + l.ID + "/items/" + dishes.ID
	rec := c.do("PATCH", path, map[string]any{"assignee_person_id": kid, "due_date": "2026-12-01"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	it := decode[itemJSON](t, rec)
	assert.Equal(t, kid, *it.AssigneePersonID)
	assert.Equal(t, "2026-12-01", *it.DueDate)

	assert.Equal(t, http.StatusBadRequest, c.do("PATCH", path, map[string]any{}).Code)
	assert.Equal(t, http.StatusBadRequest, c.do("PATCH", path, map[string]any{"due_date": "tomorrow"}).Code)
	assert.Equal(t, http.StatusNotFound, c.do("PATCH", "/api/v1/lists/"+l.ID+"/items/"+uuid.NewString(),
		map[string]any{"checked": true}).Code)

	// The feed: items changed after `since`.
	since := time.Now().UTC()
	time.Sleep(10 * time.Millisecond)
	rec = c.do("PATCH", "/api/v1/lists/"+l.ID+"/items/"+flour.ID,
		map[string]any{"quantity": nil, "unit": nil, "checked": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	it = decode[itemJSON](t, rec)
	assert.Nil(t, it.Quantity)
	assert.Nil(t, it.Unit)
	assert.True(t, it.Checked)

	rec = c.do("GET", "/api/v1/lists/"+l.ID+"/items?since="+since.Format(time.RFC3339Nano), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	feed := decode[[]itemJSON](t, rec)
	assert.Equal(t, []string{"flour"}, texts(feed))
	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assert.NotEmpty(t, raw["as_of"])
	assert.Equal(t, http.StatusBadRequest, c.do("GET", "/api/v1/lists/"+l.ID+"/items?since=yesterday", nil).Code)

	// Unassigning by null; deleting the person also clears it (046 FK).
	rec = c.do("PATCH", path, map[string]any{"assignee_person_id": nil})
	assert.Nil(t, decode[itemJSON](t, rec).AssigneePersonID)
}

func TestItems_Move(t *testing.T) {
	pool, h := setup(t)
	c := client{t: t, h: h, hid: household(t, pool)}
	l := newList(t, c, map[string]any{"name": "Order", "items": items("a", "b", "c", "d")})
	id := map[string]string{}
	for _, it := range l.Items {
		id[it.Text] = it.ID
	}
	move := func(item string, after *string) {
		t.Helper()
		body := map[string]any{}
		if after != nil {
			body["after_id"] = id[*after]
		}
		rec := c.do("POST", "/api/v1/lists/"+l.ID+"/items/"+id[item]+"/move", body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	s := func(v string) *string { return &v }
	updatedAt := func() map[string]string {
		out := map[string]string{}
		for _, it := range getList(t, c, l.ID).Items {
			out[it.Text] = it.UpdatedAt
		}
		return out
	}

	before := updatedAt()
	move("d", s("a"))
	assert.Equal(t, []string{"a", "d", "b", "c"}, texts(getList(t, c, l.ID).Items))
	after := updatedAt()
	for _, k := range []string{"a", "b", "c"} {
		assert.Equal(t, before[k], after[k], "only the moved row is written (%s)", k)
	}
	move("c", nil)
	assert.Equal(t, []string{"c", "a", "d", "b"}, texts(getList(t, c, l.ID).Items))
	move("c", s("b"))
	assert.Equal(t, []string{"a", "d", "b", "c"}, texts(getList(t, c, l.ID).Items))

	// Repeatedly splitting the same gap eventually collapses it; the list is
	// renumbered and the order is still right.
	for i := 0; i < 80; i++ {
		if i%2 == 0 {
			move("b", s("a"))
		} else {
			move("d", s("a"))
		}
	}
	assert.Equal(t, []string{"a", "d", "b", "c"}, texts(getList(t, c, l.ID).Items))

	// Ties in position (e.g. from equal positions) are renumbered too.
	_, err := pool.Exec(context.Background(), `UPDATE list_items SET position = 7 WHERE list_id = $1`, l.ID)
	require.NoError(t, err)
	order := texts(getList(t, c, l.ID).Items)
	move(order[3], s(order[0]))
	got := texts(getList(t, c, l.ID).Items)
	assert.Equal(t, []string{order[0], order[3], order[1], order[2]}, got)

	assert.Equal(t, http.StatusBadRequest, c.do("POST", "/api/v1/lists/"+l.ID+"/items/"+id["a"]+"/move",
		map[string]any{"after_id": id["a"]}).Code)
	assert.Equal(t, http.StatusNotFound, c.do("POST", "/api/v1/lists/"+l.ID+"/items/"+id["a"]+"/move",
		map[string]any{"after_id": uuid.NewString()}).Code)
}

func TestLists_FromTemplate(t *testing.T) {
	pool, h := setup(t)
	c := client{t: t, h: h, hid: household(t, pool)}
	tmpl := newList(t, c, map[string]any{"name": "Beach trip", "kind": "packing", "is_template": true,
		"items": []map[string]any{{"text": "sunscreen", "checked": true}, {"text": "towels", "quantity": 4}, {"text": "hats"}}})
	assert.True(t, tmpl.IsTemplate)
	assert.Len(t, decode[[]listJSON](t, c.do("GET", "/api/v1/lists?is_template=true", nil)), 1)

	rec := c.do("POST", "/api/v1/lists", map[string]any{"template_id": tmpl.ID, "items": items("passport")})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	l := decode[listJSON](t, rec)
	assert.Equal(t, "Beach trip", l.Name)
	assert.Equal(t, "packing", l.Kind)
	assert.False(t, l.IsTemplate)
	assert.Equal(t, []string{"passport", "sunscreen", "towels", "hats"}, texts(l.Items))
	for _, it := range l.Items {
		assert.False(t, it.Checked, it.Text)
	}
	require.NotNil(t, l.Items[2].Quantity)
	assert.InDelta(t, 4, *l.Items[2].Quantity, 1e-9)
	// The template is unchanged.
	assert.Len(t, getList(t, c, tmpl.ID).Items, 3)
	assert.Equal(t, http.StatusNotFound, c.do("POST", "/api/v1/lists",
		map[string]any{"template_id": uuid.NewString()}).Code)
}

func TestItems_ConcurrentAddsAllPersist(t *testing.T) {
	pool, h := setup(t)
	c := client{t: t, h: h, hid: household(t, pool)}
	l := newList(t, c, map[string]any{"name": "Party", "kind": "shopping"})

	const n = 25
	var wg sync.WaitGroup
	codes := make([]int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			body := map[string]any{"items": items(fmt.Sprintf("item-%02d-a", i), fmt.Sprintf("item-%02d-b", i))}
			var buf bytes.Buffer
			_ = json.NewEncoder(&buf).Encode(body)
			req := httptest.NewRequest("POST", "/api/v1/lists/"+l.ID+"/items", &buf)
			req.Header.Set("X-Test-Household", c.hid)
			req.Header.Set("X-Test-Role", auth.RoleFamilyMember)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	close(start)
	wg.Wait()
	for i, code := range codes {
		assert.Equal(t, http.StatusCreated, code, "request %d", i)
	}

	got := getList(t, c, l.ID).Items
	require.Len(t, got, 2*n)
	positions := map[float64]bool{}
	byText := map[string]float64{}
	for _, it := range got {
		positions[it.Position] = true
		byText[it.Text] = it.Position
	}
	assert.Len(t, positions, 2*n, "adders are serialised on the list row, so positions are distinct")
	for i := 0; i < n; i++ {
		assert.Less(t, byText[fmt.Sprintf("item-%02d-a", i)], byText[fmt.Sprintf("item-%02d-b", i)],
			"a batch keeps its order")
	}
}

func TestItems_ConcurrentCheckOffsDoNotOverwrite(t *testing.T) {
	pool, h := setup(t)
	c := client{t: t, h: h, hid: household(t, pool)}
	const n = 30
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("thing-%02d", i)
	}
	l := newList(t, c, map[string]any{"name": "Big shop", "kind": "grocery", "items": items(names...)})
	require.Len(t, l.Items, n)

	// Each even item is checked off by its own request (half via the batch
	// endpoint, half via PATCH) while each odd item's text is edited and
	// items are added: no request may undo another.
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan string, 3*n)
	send := func(method, path string, body any, want int) {
		defer wg.Done()
		<-start
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(body)
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("X-Test-Household", c.hid)
		req.Header.Set("X-Test-Role", auth.RoleHousesitter)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			errs <- fmt.Sprintf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
	}
	for i, it := range l.Items {
		wg.Add(1)
		switch {
		case i%4 == 0:
			go send("POST", "/api/v1/lists/"+l.ID+"/items/check", map[string]any{"item_ids": []string{it.ID}}, http.StatusOK)
		case i%2 == 0:
			go send("PATCH", "/api/v1/lists/"+l.ID+"/items/"+it.ID, map[string]any{"checked": true}, http.StatusOK)
		default:
			go send("PATCH", "/api/v1/lists/"+l.ID+"/items/"+it.ID, map[string]any{"text": it.Text + "!"}, http.StatusOK)
		}
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go send("POST", "/api/v1/lists/"+l.ID+"/items", map[string]any{"items": items(fmt.Sprintf("extra-%d", i))}, http.StatusCreated)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}

	got := getList(t, c, l.ID)
	require.Len(t, got.Items, n+5)
	byID := map[string]itemJSON{}
	for _, it := range got.Items {
		byID[it.ID] = it
	}
	for i, orig := range l.Items {
		it := byID[orig.ID]
		if i%2 == 0 {
			assert.True(t, it.Checked, "%s checked", orig.Text)
			assert.Equal(t, orig.Text, it.Text)
		} else {
			assert.False(t, it.Checked, "%s unchecked", orig.Text)
			assert.Equal(t, orig.Text+"!", it.Text)
		}
	}
	assert.Equal(t, n/2+5, got.OpenCount)
}
