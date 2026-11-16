package invite_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4799: an owner or family_manager inviting someone with role vendor may
// link the invitation to a vendors row of the same family; accepting copies
// the link to memberships.vendor_id.

func (f *fixture) vendor(t *testing.T, hhID, name string) string {
	t.Helper()
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`INSERT INTO vendors (household_id, name) VALUES ($1, $2) RETURNING id::text`, hhID, name).Scan(&id))
	return id
}

func (f *fixture) otherHousehold(t *testing.T) string {
	t.Helper()
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`INSERT INTO households (name) VALUES ('Other HH') RETURNING id::text`).Scan(&id))
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, id) })
	return id
}

// acceptAs accepts token as a brand-new Dex identity for email (bearer mode)
// and returns the new member's memberships.vendor_id.
func (f *fixture) acceptAs(t *testing.T, token, email string) *string {
	t.Helper()
	f.cleanupUser(t, email)
	rec := do(f.h, http.MethodPost, "/api/v1/invites/accept", f.s.token(t, "CiQ"+uuid.NewString(), email), map[string]string{"token": token})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var acc struct {
		Data struct {
			UserID string `json:"user_id"`
			Role   string `json:"role"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &acc))
	var v *string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT vendor_id::text FROM memberships WHERE household_id = $1 AND user_id = $2`, f.hhID, acc.Data.UserID).Scan(&v))
	return v
}

func TestInvites_VendorLink(t *testing.T) {
	f := setup(t)
	plumber := f.vendor(t, f.hhID, "Plumber")
	foreign := f.vendor(t, f.otherHousehold(t), "Other family plumber")

	create := func(token string, body map[string]any) (int, map[string]any) {
		t.Helper()
		rec := do(f.h, http.MethodPost, "/api/v1/invites", token, body)
		var out struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.Data
	}
	tokenOf := func(id any) string {
		t.Helper()
		var tok string
		require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT token FROM invitations WHERE id = $1`, id).Scan(&tok))
		return tok
	}

	t.Run("owner links a vendor invite and accepting copies the link", func(t *testing.T) {
		email := "plumber-" + uuid.NewString()[:8] + "@example.com"
		code, data := create(f.owner, map[string]any{"email": email, "role": "vendor", "vendor_id": plumber})
		require.Equal(t, http.StatusCreated, code, data)
		assert.Equal(t, plumber, data["vendor_id"])
		v := f.acceptAs(t, tokenOf(data["id"]), email)
		require.NotNil(t, v)
		assert.Equal(t, plumber, *v)
	})

	t.Run("family_manager may link too", func(t *testing.T) {
		mgr, _ := f.addMember(t, f.hhID, "family_manager")
		email := "plumber2-" + uuid.NewString()[:8] + "@example.com"
		code, data := create(mgr, map[string]any{"email": email, "role": "vendor", "vendor_id": plumber})
		require.Equal(t, http.StatusCreated, code, data)
		assert.Equal(t, plumber, data["vendor_id"])
	})

	t.Run("unlinked vendor invite makes an unlinked vendor", func(t *testing.T) {
		email := "any-vendor-" + uuid.NewString()[:8] + "@example.com"
		code, data := create(f.owner, map[string]any{"email": email, "role": "vendor"})
		require.Equal(t, http.StatusCreated, code, data)
		assert.Nil(t, data["vendor_id"])
		assert.Nil(t, f.acceptAs(t, tokenOf(data["id"]), email))
	})

	t.Run("rejections store nothing", func(t *testing.T) {
		member, _ := f.addMember(t, f.hhID, "family_member")
		for name, c := range map[string]struct {
			token string
			body  map[string]any
			want  int
		}{
			"other family's vendor":      {f.owner, map[string]any{"role": "vendor", "vendor_id": foreign}, http.StatusNotFound},
			"unknown vendor":             {f.owner, map[string]any{"role": "vendor", "vendor_id": uuid.NewString()}, http.StatusNotFound},
			"malformed vendor":           {f.owner, map[string]any{"role": "vendor", "vendor_id": "nope"}, http.StatusBadRequest},
			"link on a non-vendor":       {f.owner, map[string]any{"role": "family_member", "vendor_id": plumber}, http.StatusBadRequest},
			"link on default role":       {f.owner, map[string]any{"vendor_id": plumber}, http.StatusBadRequest},
			"family_member can't invite": {member, map[string]any{"role": "vendor", "vendor_id": plumber}, http.StatusForbidden},
		} {
			email := "rej-" + uuid.NewString()[:8] + "@example.com"
			c.body["email"] = email
			code, data := create(c.token, c.body)
			assert.Equal(t, c.want, code, "%s: %v", name, data)
			assert.Zero(t, f.invitationCount(t, email), name)
		}
	})

	t.Run("a non-vendor invitation never links even if the column is set", func(t *testing.T) {
		// Defence in depth: joinHousehold copies vendor_id only for role vendor.
		email := "viewer-" + uuid.NewString()[:8] + "@example.com"
		code, data := create(f.owner, map[string]any{"email": email, "role": "viewer"})
		require.Equal(t, http.StatusCreated, code, data)
		_, err := f.pool.Exec(context.Background(), `UPDATE invitations SET vendor_id = $1 WHERE id = $2`, plumber, data["id"])
		require.NoError(t, err)
		assert.Nil(t, f.acceptAs(t, tokenOf(data["id"]), email))
	})

	t.Run("deleting the vendor keeps the invite, unlinked", func(t *testing.T) {
		gone := f.vendor(t, f.hhID, "Short-lived")
		email := "gone-" + uuid.NewString()[:8] + "@example.com"
		code, data := create(f.owner, map[string]any{"email": email, "role": "vendor", "vendor_id": gone})
		require.Equal(t, http.StatusCreated, code, data)
		_, err := f.pool.Exec(context.Background(), `DELETE FROM vendors WHERE id = $1`, gone)
		require.NoError(t, err)
		assert.Nil(t, f.acceptAs(t, tokenOf(data["id"]), email))
	})
}
