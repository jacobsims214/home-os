package middleware_test

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
	"home-os/api/internal/bill"
	"home-os/api/internal/db"
	"home-os/api/internal/lists"
	"home-os/api/internal/middleware"
)

// actorPool is the pool main.go builds: db.NewPool, so request writes carry
// the member resolved by RequireAuth.
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

type member struct{ userID, token string }

// TestRequireAuth_AttributesWrites drives real writes through RequireAuth
// with a real token verifier and member resolver, and checks what the 050
// authorship triggers and 054 creator notifications recorded.
func TestRequireAuth_AttributesWrites(t *testing.T) {
	pool := actorPool(t)
	ctx := context.Background()
	s := newSigner(t)
	v := s.verifier().WithResolver(auth.NewMemberResolver(pool))

	var hhID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Actor HH') RETURNING id::text`).Scan(&hhID))
	newMember := func(name string) member {
		email := name + "-" + uuid.NewString()[:8] + "@example.com"
		var uid string
		require.NoError(t, pool.QueryRow(ctx,
			`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id::text`,
			email, name).Scan(&uid))
		_, err := pool.Exec(ctx,
			`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'owner')`, hhID, uid)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, uid) })
		return member{userID: uid, token: s.token(t, "sub-"+uuid.NewString(), email, "home-os-ui")}
	}
	// Registered after the users' cleanups so it runs first (LIFO).
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM households WHERE id = $1`, hhID) })
	alice, bob := newMember("Alice"), newMember("Bob")

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		bh := bill.NewHandler(bill.NewRepo(pool))
		r.Post("/api/v1/bills", bh.Create)
		r.Put("/api/v1/bills/{id}", bh.Update)
		r.Delete("/api/v1/bills/{id}", bh.Delete)
		lists.NewHandler(lists.NewRepo(pool)).RegisterRoutes(r)
	})
	send := func(m member, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			require.NoError(t, json.NewEncoder(&buf).Encode(body))
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+m.token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	idOf := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		var out struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
		require.NotEmpty(t, out.Data.ID, rec.Body.String())
		return out.Data.ID
	}
	authorship := func(table, id string) (createdBy, updatedBy *string) {
		t.Helper()
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT created_by::text, updated_by::text FROM `+table+` WHERE id = $1`, id).
			Scan(&createdBy, &updatedBy))
		return
	}
	actors := func(entityType, id string) map[string]string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT action, coalesce(actor_user_id::text, '')
			FROM activity_log WHERE entity_type = $1 AND entity_id = $2 ORDER BY id`, entityType, id)
		require.NoError(t, err)
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var action, actor string
			require.NoError(t, rows.Scan(&action, &actor))
			got[action] = actor
		}
		require.NoError(t, rows.Err())
		return got
	}
	itemChanged := func(userID, entityType, id string) []string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT title FROM notifications
			WHERE user_id = $1 AND type = 'item_changed' AND entity_type = $2 AND entity_id = $3`,
			userID, entityType, id)
		require.NoError(t, err)
		defer rows.Close()
		var titles []string
		for rows.Next() {
			var title string
			require.NoError(t, rows.Scan(&title))
			titles = append(titles, title)
		}
		return titles
	}

	t.Run("bill create and other-member edit are attributed and notify the creator", func(t *testing.T) {
		rec := send(alice, http.MethodPost, "/api/v1/bills", map[string]any{"name": "Netflix", "amount": "15.49"})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		id := idOf(rec)

		cb, ub := authorship("bills", id)
		require.NotNil(t, cb)
		require.NotNil(t, ub)
		assert.Equal(t, alice.userID, *cb)
		assert.Equal(t, alice.userID, *ub)

		rec = send(bob, http.MethodPut, "/api/v1/bills/"+id, map[string]any{"amount": "17.99"})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		cb, ub = authorship("bills", id)
		assert.Equal(t, alice.userID, *cb, "creator never changes")
		require.NotNil(t, ub)
		assert.Equal(t, bob.userID, *ub)

		assert.Equal(t, map[string]string{"create": alice.userID, "update": bob.userID}, actors("bill", id))
		titles := itemChanged(alice.userID, "bill", id)
		require.Len(t, titles, 1, "alice is told bob changed her bill")
		assert.Contains(t, titles[0], "Bob")
		assert.Empty(t, itemChanged(bob.userID, "bill", id), "the actor is not notified")

		// Alice editing her own bill notifies nobody.
		rec = send(alice, http.MethodPut, "/api/v1/bills/"+id, map[string]any{"notes": "family plan"})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		_, ub = authorship("bills", id)
		assert.Equal(t, alice.userID, *ub)
		assert.Len(t, itemChanged(alice.userID, "bill", id), 1)
	})

	t.Run("transactional list writes are attributed", func(t *testing.T) {
		rec := send(alice, http.MethodPost, "/api/v1/lists", map[string]any{"name": "Groceries"})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		id := idOf(rec)
		cb, _ := authorship("lists", id)
		require.NotNil(t, cb)
		assert.Equal(t, alice.userID, *cb)

		rec = send(bob, http.MethodPatch, "/api/v1/lists/"+id, map[string]any{"name": "Weekly groceries"})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		_, ub := authorship("lists", id)
		require.NotNil(t, ub)
		assert.Equal(t, bob.userID, *ub)
		assert.Equal(t, map[string]string{"create": alice.userID, "update": bob.userID}, actors("list", id))
		assert.Len(t, itemChanged(alice.userID, "list", id), 1)
	})

	t.Run("background write on the same pool records NULL", func(t *testing.T) {
		// Same pool the requests used: their actor must not leak.
		var id string
		require.NoError(t, pool.QueryRow(ctx,
			`INSERT INTO bills (household_id, name) VALUES ($1, 'Worker bill') RETURNING id::text`, hhID).Scan(&id))
		cb, ub := authorship("bills", id)
		assert.Nil(t, cb)
		assert.Nil(t, ub)
		_, err := pool.Exec(ctx, `UPDATE bills SET notes = 'by worker' WHERE id = $1`, id)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"create": "", "update": ""}, actors("bill", id))
	})
}
