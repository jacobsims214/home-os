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

// #4688: removing a member, or demoting them below an invitation's role,
// deletes the pending invitations they created in that household (in the
// same transaction), so their leaked links stop working.

// invite stores an invitation in hhID created by invitedBy and returns its id.
func (f *mfix) invite(t *testing.T, hhID, invitedBy, role string, accepted bool) string {
	t.Helper()
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`INSERT INTO invitations (household_id, email, token, role, invited_by, expires_at, accepted_at)
		 VALUES ($1, $2, $3, $4, $5, NOW() + interval '7 days', CASE WHEN $6 THEN NOW() END)
		 RETURNING id::text`,
		hhID, "i-"+uuid.NewString()[:8]+"@example.com", uuid.NewString(), role, invitedBy, accepted).Scan(&id))
	return id
}

func (f *mfix) inviteExists(t *testing.T, id string) bool {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM invitations WHERE id = $1`, id).Scan(&n))
	return n == 1
}

func TestMembers_RemovalDropsPendingInvites(t *testing.T) {
	f := newMFix(t)
	ownerTok, ownerID := f.member(t, f.hhID, "owner")
	_, mgrID := f.member(t, f.hhID, "family_manager")

	pending := f.invite(t, f.hhID, mgrID, "family_member", false)
	pendingViewer := f.invite(t, f.hhID, mgrID, "viewer", false)
	accepted := f.invite(t, f.hhID, mgrID, "viewer", true)
	othersInvite := f.invite(t, f.hhID, ownerID, "viewer", false)
	// The manager's invite into another household they also manage is not
	// touched by removal from this one.
	hhB := addHousehold(t, f.pool, "B", mgrID, "family_manager", 0)
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hhB) })
	inB := f.invite(t, hhB, mgrID, "viewer", false)

	rec := f.do(http.MethodDelete, mgrID, ownerTok, f.hhID, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.False(t, f.inviteExists(t, pending))
	assert.False(t, f.inviteExists(t, pendingViewer))
	assert.True(t, f.inviteExists(t, accepted), "accepted invitations are history")
	assert.True(t, f.inviteExists(t, othersInvite), "other inviters' invitations stay")
	assert.True(t, f.inviteExists(t, inB), "other households are untouched")

	t.Run("a refused removal deletes nothing", func(t *testing.T) {
		mtok, _ := f.member(t, f.hhID, "family_manager")
		inv := f.invite(t, f.hhID, ownerID, "owner", false)
		rec := f.do(http.MethodDelete, ownerID, mtok, f.hhID, nil)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.True(t, f.inviteExists(t, inv))
	})
}

func TestMembers_DemotionDropsInvitesTheyCouldNoLongerCreate(t *testing.T) {
	cases := []struct {
		from, to string
		kept     []string // invitation roles that survive; the rest are deleted
	}{
		{"owner", "family_manager", []string{"family_manager", "family_member", "viewer", "housesitter", "vendor"}},
		{"owner", "family_member", nil},
		{"family_manager", "viewer", nil},
		{"family_manager", "owner", []string{"owner", "family_manager", "family_member", "viewer", "housesitter", "vendor"}},
	}
	for _, tc := range cases {
		t.Run(tc.from+" to "+tc.to, func(t *testing.T) {
			f := newMFix(t)
			ownerTok, _ := f.member(t, f.hhID, "owner")
			_, target := f.member(t, f.hhID, tc.from)
			ids := map[string]string{}
			for _, r := range []string{"owner", "family_manager", "family_member", "viewer", "housesitter", "vendor"} {
				ids[r] = f.invite(t, f.hhID, target, r, false)
			}
			acceptedOwner := f.invite(t, f.hhID, target, "owner", true)

			rec := f.do(http.MethodPatch, target, ownerTok, f.hhID, map[string]string{"role": tc.to})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			kept := map[string]bool{}
			for _, r := range tc.kept {
				kept[r] = true
			}
			for r, id := range ids {
				assert.Equal(t, kept[r], f.inviteExists(t, id), "%s invitation", r)
			}
			assert.True(t, f.inviteExists(t, acceptedOwner), "accepted invitations are history")
		})
	}
}
