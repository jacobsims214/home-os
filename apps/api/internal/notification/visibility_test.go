package notification

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
)

// TestRoleSeesMatchesMCP keeps the role condition identical to the MCP's
// notification tools, so the API and MCP list and count the same notices.
func TestRoleSeesMatchesMCP(t *testing.T) {
	fn := regexp.MustCompile(`(?s)\nfunc roleSeesSQL\(role string\) string \{\n.*?\n\}\n`)
	mine, err := os.ReadFile("visibility.go")
	require.NoError(t, err)
	mcp, err := os.ReadFile(filepath.Join("..", "..", "..", "mcp", "internal", "notification", "visibility.go"))
	require.NoError(t, err)
	a, b := fn.Find(mine), fn.Find(mcp)
	require.NotNil(t, a, "roleSeesSQL not found in visibility.go")
	require.NotNil(t, b, "roleSeesSQL not found in the MCP's visibility.go")
	require.Equal(t, string(b), string(a))
}

// TestListAndMarkReadFollowCurrentRole (#4794) runs the handlers on
// TEST_DATABASE_URL: notices written while a member was an owner disappear
// from the list, and can't be marked read, once they are a viewer (the role
// RequireAuth re-reads from memberships arrives as claims.Role). Another
// member's personal notices are never listed.
func TestListAndMarkReadFollowCurrentRole(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	id := func(sql string, args ...any) string {
		t.Helper()
		var s string
		require.NoError(t, pool.QueryRow(ctx, sql, args...).Scan(&s), sql)
		return s
	}
	hh := id(`INSERT INTO households (name) VALUES ('Notice HH') RETURNING id::text`)
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM households WHERE id = $1`, hh) })
	user := func(name string) string {
		uid := id(`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id::text`,
			name+"-"+uuid.NewString()[:8]+"@example.com", name)
		_, err := pool.Exec(ctx, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'owner')`, hh, uid)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, uid) })
		return uid
	}
	amber, dana := user("Amber"), user("Dana")

	secret := id(`INSERT INTO secrets (household_id, entity_type, entity_id, encrypted_data, iv, name, secret_type)
		VALUES ($1, 'household', $1, '\x00', '\x00', 'Bank login', 'login') RETURNING id::text`, hh)
	secretNote := id(`INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'secret', $2, 'pin') RETURNING id::text`, hh, secret)
	blob := id(`INSERT INTO file_blobs (data, size) VALUES ('\x00', 1) RETURNING id::text`)
	linkedFile := id(`INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id)
		VALUES ($1, $2, 'scan.pdf', 1, 'household', $1) RETURNING id::text`, hh, blob)
	_, err = pool.Exec(ctx, `INSERT INTO document_links (household_id, entity_type, entity_id, link_type, link_id)
		VALUES ($1, 'secret', $2, 'file', $3)`, hh, secret, linkedFile)
	require.NoError(t, err)
	bill := id(`INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Electric', 10, 20) RETURNING id::text`, hh)

	notify := func(userID any, title string, kind, entity any) string {
		return id(`INSERT INTO notifications (household_id, user_id, type, title, entity_type, entity_id)
			VALUES ($1, $2::uuid, 'item_changed', $3, $4::varchar, $5::uuid) RETURNING id::text`, hh, userID, title, kind, entity)
	}
	n := map[string]string{
		"secret":     notify(amber, "Dana updated secret", "secret", secret),
		"secretNote": notify(amber, "Dana updated note", "note", secretNote),
		"linkedFile": notify(amber, "Dana updated file", "file", linkedFile),
		"deleted":    notify(amber, "Dana deleted secret", nil, nil),
		"bill":       notify(nil, "Electric due soon", "bill", bill),
		"dana":       notify(dana, "Amber updated bill", "bill", bill),
	}

	h := NewHandler(NewRepo(pool), nil)
	role := "owner"
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := &auth.Claims{UserID: amber, HouseholdID: hh, Role: role}
			next.ServeHTTP(w, req.WithContext(middleware.WithClaims(req.Context(), c)))
		})
	})
	r.Get("/api/v1/notifications", h.List)
	r.Patch("/api/v1/notifications/{id}/read", h.MarkRead)

	list := func() []string {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body struct {
			Data []notificationResponse `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		var got []string
		for _, x := range body.Data {
			for k, v := range n {
				if v == x.ID {
					got = append(got, k)
				}
			}
		}
		slices.Sort(got)
		return got
	}
	markRead := func(k string) int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/v1/notifications/"+n[k]+"/read", strings.NewReader("")))
		return w.Code
	}

	// Owner: everything of theirs plus the family-wide bill; never Dana's.
	require.Equal(t, []string{"bill", "deleted", "linkedFile", "secret", "secretNote"}, list())
	require.Equal(t, http.StatusNotFound, markRead("dana"))

	// Demoted to viewer: the credential notices go.
	role = "viewer"
	require.Equal(t, []string{"bill"}, list())
	for _, k := range []string{"secret", "secretNote", "linkedFile", "deleted"} {
		require.Equal(t, http.StatusNotFound, markRead(k), k)
	}
	var unread int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE household_id = $1 AND read_at IS NULL`, hh).Scan(&unread))
	require.Equal(t, 6, unread, "a hidden notice was marked read")
	require.Equal(t, http.StatusNoContent, markRead("bill"))
	require.Empty(t, list())

	// An unknown or empty role sees nothing (fail closed).
	role = "astronaut"
	require.NoError(t, func() error {
		_, err := pool.Exec(ctx, `UPDATE notifications SET read_at = NULL WHERE id = $1`, n["bill"])
		return err
	}())
	require.Empty(t, list())

	// Promoted back: they reappear.
	role = "owner"
	require.Equal(t, []string{"bill", "deleted", "linkedFile", "secret", "secretNote"}, list())
}
