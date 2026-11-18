package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/config"
)

// meBody is GET /api/v1/auth/me's response: the user object bare, with no
// {"data": ...} envelope (unlike the collection routes).
type meBody struct {
	ID              string  `json:"id"`
	Email           string  `json:"email"`
	Name            string  `json:"name"`
	AvatarURL       *string `json:"avatar_url"`
	IsInstanceAdmin bool    `json:"is_instance_admin"`
}

// meUser inserts a user with an explicit is_instance_admin, a Dex sub and a
// membership with role, and returns the sub and email to sign a token for.
func meUser(t *testing.T, pool *pgxpool.Pool, admin bool, role string) (sub, email, userID string) {
	t.Helper()
	ctx := context.Background()
	sub, email = uniq("CiQme"), uniq("me")+"@example.com"
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub, is_instance_admin)
		 VALUES ($1, 'Me', 'x', $2, $3) RETURNING id::text`, email, sub, admin).Scan(&userID))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	insertMembership(t, pool, userID, role, time.Now())
	return sub, email, userID
}

// TestMe_ReportsIsInstanceAdmin: /auth/me answers with
// users.is_instance_admin, read from the caller's own users row on every
// request, for an admin and a non-admin alike.
//
// The flag is the instance-wide admin axis (the /admin routes), not the
// household role: an owner is not an admin, an admin on the housesitter role
// still is, and flipping the column changes the answer on the next request
// with the same token.
func TestMe_ReportsIsInstanceAdmin(t *testing.T) {
	pool := resolverTestDB(t)
	signer := newDexTokenSigner(t)
	v := signer.verifier().WithResolver(NewMemberResolver(pool))
	h := NewHandler(NewRepo(pool), &config.Config{}, nil, nil, v)
	ctx := context.Background()

	me := func(t *testing.T, token string) meBody {
		t.Helper()
		rec := getMe(h, token)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got meBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), rec.Body.String())
		return got
	}

	t.Run("instance admin", func(t *testing.T) {
		sub, email, _ := meUser(t, pool, true, RoleOwner)
		got := me(t, signer.token(t, sub, email))
		assert.True(t, got.IsInstanceAdmin)
		assert.Equal(t, email, got.Email)
	})

	t.Run("ordinary owner is not an instance admin", func(t *testing.T) {
		sub, email, _ := meUser(t, pool, false, RoleOwner)
		assert.False(t, me(t, signer.token(t, sub, email)).IsInstanceAdmin,
			"the household role grants no instance admin")
	})

	t.Run("instance admin on a non-managing household role", func(t *testing.T) {
		// The two axes are independent in both directions.
		sub, email, _ := meUser(t, pool, true, RoleHousesitter)
		assert.True(t, me(t, signer.token(t, sub, email)).IsInstanceAdmin)
	})

	t.Run("the field is always present, never omitted", func(t *testing.T) {
		sub, email, _ := meUser(t, pool, false, RoleFamilyMember)
		rec := getMe(h, signer.token(t, sub, email))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw), rec.Body.String())
		v, ok := raw["is_instance_admin"]
		require.True(t, ok, "is_instance_admin must be present even when false: %s", rec.Body.String())
		assert.JSONEq(t, "false", string(v))
	})

	t.Run("flag changes take effect on the next request", func(t *testing.T) {
		sub, email, userID := meUser(t, pool, false, RoleOwner)
		tok := signer.token(t, sub, email)
		require.False(t, me(t, tok).IsInstanceAdmin)

		_, err := pool.Exec(ctx, `UPDATE users SET is_instance_admin = true WHERE id = $1`, userID)
		require.NoError(t, err)
		assert.True(t, me(t, tok).IsInstanceAdmin,
			"the flag is read per request, not carried in the token")
	})

	t.Run("the payload describes the caller only", func(t *testing.T) {
		// /auth/me carries no household id, name, timezone or role, so it
		// cannot disclose anything about a family — the caller's or anyone
		// else's.
		sub, email, _ := meUser(t, pool, true, RoleOwner)
		rec := getMe(h, signer.token(t, sub, email))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw), rec.Body.String())
		for _, key := range []string{
			"household", "households", "household_id", "household_name",
			"timezone", "role", "memberships",
		} {
			assert.NotContains(t, raw, key, "/auth/me must not carry %q", key)
		}
		assert.NotContains(t, rec.Body.String(), "password", "no secrets in the profile")
	})
}
