package household_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// #4683: changing roles and removing members is limited to owners and family
// managers, judged from memberships in the request's household.

func membersRouter(pool *pgxpool.Pool, v *auth.Verifier) http.Handler {
	h := household.NewHandler(household.NewRepo(pool))
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		r.Patch("/api/v1/households/me/members/{userId}", h.UpdateMemberRole)
		r.Delete("/api/v1/households/me/members/{userId}", h.RemoveMember)
	})
	return r
}

type mfix struct {
	pool *pgxpool.Pool
	s    *signer
	h    http.Handler
	hhID string
}

func newMFix(t *testing.T) *mfix {
	t.Helper()
	pool := testDB(t)
	s := newSigner(t)
	f := &mfix{pool: pool, s: s, h: membersRouter(pool, s.verifier(pool))}
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO households (name) VALUES ('Members HH') RETURNING id::text`).Scan(&f.hhID))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, f.hhID) })
	return f
}

// member adds a user with role in hhID; it returns the token and users.id.
func (f *mfix) member(t *testing.T, hhID, role string) (string, string) {
	t.Helper()
	sub, email := uniq(role)
	ctx := context.Background()
	var id string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, 'M', 'x', $2) RETURNING id::text`,
		email, sub).Scan(&id))
	_, err := f.pool.Exec(ctx, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hhID, id, role)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	return f.s.token(t, sub, email), id
}

func (f *mfix) role(t *testing.T, userID string) string {
	t.Helper()
	var r string
	err := f.pool.QueryRow(context.Background(),
		`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`, f.hhID, userID).Scan(&r)
	if err != nil {
		return ""
	}
	return r
}

func (f *mfix) do(method, userID, token, hh string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, "/api/v1/households/me/members/"+userID, &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	if hh != "" {
		req.Header.Set(auth.HouseholdHeader, hh)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func TestMembers_ChangeRoleAuthorization(t *testing.T) {
	cases := []struct {
		actor, target, newRole string
		want                   int
	}{
		// The wave-check class: low-trust members cannot touch roles at all.
		{"family_member", "family_member", "owner", http.StatusForbidden},
		{"viewer", "family_member", "viewer", http.StatusForbidden},
		{"housesitter", "viewer", "family_member", http.StatusForbidden},
		{"vendor", "viewer", "owner", http.StatusForbidden},
		// family_manager: any role except owner, never on an owner.
		{"family_manager", "family_member", "family_manager", http.StatusOK},
		{"family_manager", "viewer", "family_member", http.StatusOK},
		{"family_manager", "family_member", "owner", http.StatusForbidden},
		{"family_manager", "owner", "viewer", http.StatusForbidden},
		// owner: anything, including another owner.
		{"owner", "family_member", "owner", http.StatusOK},
		{"owner", "owner", "family_member", http.StatusOK},
		{"owner", "family_member", "superuser", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.actor+" sets "+tc.target+" to "+tc.newRole, func(t *testing.T) {
			f := newMFix(t)
			f.member(t, f.hhID, "owner") // keeps an owner besides the cases' own
			actorTok, _ := f.member(t, f.hhID, tc.actor)
			_, targetID := f.member(t, f.hhID, tc.target)
			rec := f.do(http.MethodPatch, targetID, actorTok, "", map[string]string{"role": tc.newRole})
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
			if tc.want == http.StatusOK {
				assert.Equal(t, tc.newRole, f.role(t, targetID))
			} else {
				assert.Equal(t, tc.target, f.role(t, targetID), "role unchanged")
			}
		})
	}

	t.Run("a member cannot promote themselves", func(t *testing.T) {
		f := newMFix(t)
		f.member(t, f.hhID, "owner")
		tok, id := f.member(t, f.hhID, "family_member")
		rec := f.do(http.MethodPatch, id, tok, "", map[string]string{"role": "owner"})
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.Equal(t, "family_member", f.role(t, id))
	})

	t.Run("the last owner cannot be demoted", func(t *testing.T) {
		f := newMFix(t)
		tok, id := f.member(t, f.hhID, "owner")
		rec := f.do(http.MethodPatch, id, tok, "", map[string]string{"role": "family_member"})
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "owner", f.role(t, id))
	})

	t.Run("target outside the household is 404", func(t *testing.T) {
		f := newMFix(t)
		tok, _ := f.member(t, f.hhID, "owner")
		rec := f.do(http.MethodPatch, uuid.NewString(), tok, "", map[string]string{"role": "viewer"})
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	})
}

func TestMembers_RemoveAuthorization(t *testing.T) {
	cases := []struct {
		actor, target string
		want          int
	}{
		{"family_member", "viewer", http.StatusForbidden},
		{"viewer", "family_member", http.StatusForbidden},
		{"housesitter", "vendor", http.StatusForbidden},
		{"vendor", "owner", http.StatusForbidden},
		{"family_manager", "owner", http.StatusForbidden},
		{"family_manager", "family_member", http.StatusOK},
		{"owner", "owner", http.StatusOK},
		{"owner", "vendor", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.actor+" removes "+tc.target, func(t *testing.T) {
			f := newMFix(t)
			f.member(t, f.hhID, "owner")
			actorTok, _ := f.member(t, f.hhID, tc.actor)
			_, targetID := f.member(t, f.hhID, tc.target)
			rec := f.do(http.MethodDelete, targetID, actorTok, "", nil)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
			if tc.want == http.StatusOK {
				assert.Empty(t, f.role(t, targetID))
			} else {
				assert.Equal(t, tc.target, f.role(t, targetID), "still a member")
			}
		})
	}

	t.Run("the last owner cannot be removed", func(t *testing.T) {
		f := newMFix(t)
		tok, id := f.member(t, f.hhID, "owner")
		rec := f.do(http.MethodDelete, id, tok, "", nil)
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "owner", f.role(t, id))
	})
}

// The actor's role is the one in the X-Household-ID household: owner of A
// but family_member of B cannot change roles in B.
func TestMembers_RoleIsPerHousehold(t *testing.T) {
	f := newMFix(t) // B
	f.member(t, f.hhID, "owner")
	tok, uid := f.member(t, f.hhID, "family_member")
	_, targetID := f.member(t, f.hhID, "viewer")
	hhA := addHousehold(t, f.pool, "A", uid, "owner", 30)
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hhA) })

	rec := f.do(http.MethodPatch, targetID, tok, f.hhID, map[string]string{"role": "owner"})
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = f.do(http.MethodDelete, targetID, tok, f.hhID, nil)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	// With no header the caller resolves to A, where the B member is not found.
	rec = f.do(http.MethodPatch, targetID, tok, "", map[string]string{"role": "family_member"})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, "viewer", f.role(t, targetID))
}
