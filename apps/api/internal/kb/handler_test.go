package kb

// The REST equivalents the UI uses (#5502): the same four operations the MCP
// tools offer, over /api/v1/kb. These tests cover the HTTP contract — status
// codes, the path in the URL, the "an update REPLACES the doc" note — and the
// role scoping the ticket asks for: every member reads, only the family roles
// write. The end-to-end "A writes, B finds it by meaning" proof lives in
// kb_integration_test.go (it needs Typesense).

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
	"home-os/api/internal/middleware"
)

// testPool returns the database to test against: TEST_DATABASE_URL when set
// (CI's must-PASS step, and a machine without Docker), else a fresh container.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if dsn := testEnv("TEST_DATABASE_URL"); dsn != "" {
		pool, err := pgxpool.New(context.Background(), dsn)
		require.NoError(t, err)
		t.Cleanup(pool.Close)
		return pool
	}
	t.Skip("no TEST_DATABASE_URL and no container helper in this package; run with TEST_DATABASE_URL")
	return nil
}

// household seeds a household with one member per role and returns the ids.
type household struct {
	id      uuid.UUID
	owner   uuid.UUID
	sitter  uuid.UUID
	viewer  uuid.UUID
	vendor  uuid.UUID
	family  uuid.UUID
	members map[string]uuid.UUID
}

func seedHousehold(t *testing.T, pool *pgxpool.Pool) household {
	t.Helper()
	ctx := context.Background()
	h := household{members: map[string]uuid.UUID{}}
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name, timezone) VALUES ('KB REST test', 'America/New_York') RETURNING id`).Scan(&h.id))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, h.id) })

	role := func(name, r string) uuid.UUID {
		var id uuid.UUID
		require.NoError(t, pool.QueryRow(ctx,
			`INSERT INTO users (name, email, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
			name, name+"-"+uuid.NewString()+"@example.test").Scan(&id))
		_, err := pool.Exec(ctx,
			`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, h.id, id, r)
		require.NoError(t, err)
		h.members[r] = id
		return id
	}
	h.owner = role("Owner", "owner")
	h.family = role("Family", "family_member")
	h.sitter = role("Sitter", "housesitter")
	h.viewer = role("Viewer", "viewer")
	h.vendor = role("Vendor", "vendor")
	return h
}

// router mounts the KB handler behind the policy middleware, as the real API
// does, with the caller's claims injected by a test middleware.
func router(pool *pgxpool.Pool, h household, role string) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := middleware.WithClaims(req.Context(), &auth.Claims{
				UserID:      h.members[role].String(),
				HouseholdID: h.id.String(),
				Role:        role,
			})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	// RequirePolicy is what refuses a role the route policy denies — the same
	// middleware the real API mounts. It runs in a group (as main.go does), so
	// chi has already matched the route and RoutePattern is set when it reads
	// the policy table.
	kb := NewHandler(NewRepo(pool), nil)
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequirePolicy)
		r.Get("/api/v1/kb", kb.ListOrSearch)
		r.Get("/api/v1/kb/*", kb.Get)
		r.Put("/api/v1/kb/*", kb.Upsert)
		r.Delete("/api/v1/kb/*", kb.Delete)
	})
	return r
}

func do(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRESTUpsertGetListDelete(t *testing.T) {
	pool := testPool(t)
	h := seedHousehold(t, pool)
	r := router(pool, h, "family_member")

	// PUT creates.
	rec := do(t, r, http.MethodPut, "/api/v1/kb/how-to/networking/router-reset", map[string]any{
		"title": "Router reset",
		"body":  "Unplug it for 30 seconds, then plug it back in.",
		"tags":  []string{"wifi", "networking"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		Data struct {
			Created bool `json:"created"`
			Note    string
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.True(t, created.Data.Created)
	assert.Contains(t, created.Data.Note, "Doc created", "a create says so")

	// GET reads it back by path, slashes and all.
	rec = do(t, r, http.MethodGet, "/api/v1/kb/how-to/networking/router-reset", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Data struct {
			Path  string `json:"path"`
			Title string `json:"title"`
			Body  string `json:"body"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "how-to/networking/router-reset", got.Data.Path)
	assert.Equal(t, "Router reset", got.Data.Title)

	// GET ?prefix= lists paths and titles.
	rec = do(t, r, http.MethodGet, "/api/v1/kb?prefix=how-to", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var listed struct {
		Data []struct {
			Path string `json:"path"`
		} `json:"data"`
		Total int `json:"total"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	require.Equal(t, 1, listed.Total)
	assert.Equal(t, "how-to/networking/router-reset", listed.Data[0].Path)

	// PUT again REPLACES rather than forking.
	rec = do(t, r, http.MethodPut, "/api/v1/kb/how-to/networking/router-reset", map[string]any{
		"title": "Router reset (updated)",
		"body":  "Hold the reset button for 10 seconds instead.",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var replaced struct {
		Data struct {
			Created bool   `json:"created"`
			Note    string `json:"note"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &replaced))
	assert.False(t, replaced.Data.Created, "the same path updates, it does not create a second doc")
	assert.Contains(t, replaced.Data.Note, "REPLACED", "an update must say it REPLACES the doc")

	// An unknown path is 404, not an empty doc.
	rec = do(t, r, http.MethodGet, "/api/v1/kb/how-to/nope", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	// DELETE soft-deletes; the path stops resolving.
	rec = do(t, r, http.MethodDelete, "/api/v1/kb/how-to/networking/router-reset", nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	rec = do(t, r, http.MethodGet, "/api/v1/kb/how-to/networking/router-reset", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	// The row is still there (soft delete), so a later write at the same path
	// restores it rather than colliding with the UNIQUE constraint.
	var deleted bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT deleted_at IS NOT NULL FROM kb_docs WHERE household_id = $1 AND path = $2`,
		h.id, "how-to/networking/router-reset").Scan(&deleted))
	assert.True(t, deleted, "the delete must be soft")
	rec = do(t, r, http.MethodPut, "/api/v1/kb/how-to/networking/router-reset", map[string]any{
		"title": "Router reset", "body": "back again",
	})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestRESTRoleScoping(t *testing.T) {
	pool := testPool(t)
	h := seedHousehold(t, pool)

	// The owner writes a doc for the sitter to read.
	owner := router(pool, h, "owner")
	rec := do(t, owner, http.MethodPut, "/api/v1/kb/sitters/what-the-babysitter-needs-to-know", map[string]any{
		"title": "What the babysitter needs to know",
		"body":  "Wifi is on the fridge. The dog is friendly. Call us before 9pm.",
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	// Every role READS — the sitter and the vendor included. That is the
	// point of the KB: the babysitter needs the router doc.
	for _, role := range []string{"owner", "family_member", "housesitter", "viewer", "vendor"} {
		t.Run("read/"+role, func(t *testing.T) {
			r := router(pool, h, role)
			rec := do(t, r, http.MethodGet, "/api/v1/kb/sitters/what-the-babysitter-needs-to-know", nil)
			assert.Equal(t, http.StatusOK, rec.Code, "%s must read the KB: %s", role, rec.Body.String())
			rec = do(t, r, http.MethodGet, "/api/v1/kb", nil)
			assert.Equal(t, http.StatusOK, rec.Code, "%s must list the KB", role)
		})
	}

	// Only the family roles WRITE. A housesitter reads and cannot write.
	for _, role := range []string{"owner", "family_manager", "family_member"} {
		if role == "family_manager" {
			continue // not seeded; the owner and family_member cover rolesFamily
		}
		t.Run("write/"+role, func(t *testing.T) {
			r := router(pool, h, role)
			rec := do(t, r, http.MethodPut, "/api/v1/kb/how-to/"+role+"/x", map[string]any{
				"title": "x", "body": "y",
			})
			assert.NotEqual(t, http.StatusForbidden, rec.Code, "%s may write", role)
		})
	}
	for _, role := range []string{"housesitter", "viewer", "vendor"} {
		t.Run("write-refused/"+role, func(t *testing.T) {
			r := router(pool, h, role)
			rec := do(t, r, http.MethodPut, "/api/v1/kb/how-to/refused/x", map[string]any{
				"title": "x", "body": "y",
			})
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s must not write the KB", role)
			rec = do(t, r, http.MethodDelete, "/api/v1/kb/sitters/what-the-babysitter-needs-to-know", nil)
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s must not delete from the KB", role)
		})
	}

	// The policy table itself agrees (auth.ResKB), so the routes and the
	// handler's own check cannot drift apart.
	assert.True(t, auth.Can("housesitter", auth.ResKB, auth.ActRead))
	assert.False(t, auth.Can("housesitter", auth.ResKB, auth.ActWrite))
	assert.True(t, auth.Can("vendor", auth.ResKB, auth.ActRead))
	assert.False(t, auth.Can("vendor", auth.ResKB, auth.ActWrite))
	assert.True(t, auth.Can("family_member", auth.ResKB, auth.ActWrite))
}

func TestRESTValidation(t *testing.T) {
	pool := testPool(t)
	h := seedHousehold(t, pool)
	r := router(pool, h, "owner")

	cases := []struct {
		name string
		path string
		body map[string]any
		want int
	}{
		{"empty title", "/api/v1/kb/how-to/x", map[string]any{"title": "  ", "body": "b"}, http.StatusBadRequest},
		{"empty body is allowed", "/api/v1/kb/how-to/empty-body", map[string]any{"title": "t", "body": ""}, http.StatusCreated},
		{"upper-case path is normalized", "/api/v1/kb/How-To/X", map[string]any{"title": "t", "body": "b"}, http.StatusCreated},
		{"dotdot", "/api/v1/kb/how-to/../x", map[string]any{"title": "t", "body": "b"}, http.StatusBadRequest},
		{"leading dot", "/api/v1/kb/.hidden", map[string]any{"title": "t", "body": "b"}, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, r, http.MethodPut, c.path, c.body)
			assert.Equal(t, c.want, rec.Code, rec.Body.String())
		})
	}
}

// testEnv reads an env var.
func testEnv(key string) string { return os.Getenv(key) }
