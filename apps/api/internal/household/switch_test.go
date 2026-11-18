package household_test

import (
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

// switchRouter mirrors cmd/api/main.go for the routes this test drives.
func switchRouter(pool *pgxpool.Pool, v *auth.Verifier) http.Handler {
	h := household.NewHandler(household.NewRepo(pool)).WithVerifier(v)
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		r.Get("/api/v1/me/households", h.ListMine)
		r.Get("/api/v1/households/me", h.GetMe)
	})
	return r
}

func doWithHousehold(h http.Handler, path, token, householdID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if householdID != "" {
		req.Header.Set(auth.HouseholdHeader, householdID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// addHousehold creates a household and, when userID is set, a membership for
// it whose created_at is ageDays in the past (so the oldest is deterministic).
func addHousehold(t *testing.T, pool *pgxpool.Pool, name, userID, role string, ageDays int) string {
	t.Helper()
	ctx := context.Background()
	var hid string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ($1) RETURNING id::text`, name).Scan(&hid))
	if userID != "" {
		_, err := pool.Exec(ctx,
			`INSERT INTO memberships (household_id, user_id, role, created_at)
			 VALUES ($1, $2, $3, NOW() - make_interval(days => $4))`, hid, userID, role, ageDays)
		require.NoError(t, err)
	}
	return hid
}

type listed struct {
	Data []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Role      string `json:"role"`
		IsDefault bool   `json:"is_default"`
		Current   bool   `json:"current"`
	} `json:"data"`
}

func currentHousehold(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data struct {
			ID string `json:"ID"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body.Data.ID
}

func TestHouseholdSwitch_TwoFamilyUser(t *testing.T) {
	pool := testDB(t)
	s := newSigner(t)
	h := switchRouter(pool, s.verifier(pool))

	sub, email := uniq("switch")
	uid := insertUser(t, pool, email, false, true)
	older := addHousehold(t, pool, "Older Family", uid, "owner", 10)
	newer := addHousehold(t, pool, "Newer Family", uid, "family_member", 1)
	stranger := addHousehold(t, pool, "Someone Else", "", "", 0)
	tok := s.token(t, sub, email)

	t.Run("no header defaults to the oldest membership", func(t *testing.T) {
		assert.Equal(t, older, currentHousehold(t, doWithHousehold(h, "/api/v1/households/me", tok, "")))
	})
	t.Run("header switches to the other family", func(t *testing.T) {
		assert.Equal(t, newer, currentHousehold(t, doWithHousehold(h, "/api/v1/households/me", tok, newer)))
		assert.Equal(t, older, currentHousehold(t, doWithHousehold(h, "/api/v1/households/me", tok, older)))
	})
	t.Run("non-member household is 403", func(t *testing.T) {
		rec := doWithHousehold(h, "/api/v1/households/me", tok, stranger)
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "requested household")
	})
	t.Run("unknown household is 403", func(t *testing.T) {
		rec := doWithHousehold(h, "/api/v1/households/me", tok, uuid.NewString())
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
	t.Run("malformed header is 400", func(t *testing.T) {
		rec := doWithHousehold(h, "/api/v1/households/me", tok, "family-2")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("me/households lists both with roles, default and current", func(t *testing.T) {
		for _, tc := range []struct{ header, wantCurrent string }{
			{"", older},
			{newer, newer},
		} {
			rec := doWithHousehold(h, "/api/v1/me/households", tok, tc.header)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var got listed
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			require.Len(t, got.Data, 2)
			assert.Equal(t, older, got.Data[0].ID)
			assert.Equal(t, "Older Family", got.Data[0].Name)
			assert.Equal(t, "owner", got.Data[0].Role)
			assert.True(t, got.Data[0].IsDefault)
			assert.Equal(t, newer, got.Data[1].ID)
			assert.Equal(t, "family_member", got.Data[1].Role)
			assert.False(t, got.Data[1].IsDefault)
			for _, d := range got.Data {
				assert.Equal(t, d.ID == tc.wantCurrent, d.Current, d.Name)
			}
		}
	})
}

// Instance admins get no implicit access to families they are not members of.
func TestHouseholdSwitch_InstanceAdminNeedsMembership(t *testing.T) {
	pool := testDB(t)
	s := newSigner(t)
	h := switchRouter(pool, s.verifier(pool))

	sub, email := uniq("switch-admin")
	uid := insertUser(t, pool, email, true, true)
	own := addHousehold(t, pool, "Admin Family", uid, "owner", 1)
	other := addHousehold(t, pool, "Not Theirs", "", "", 0)
	tok := s.token(t, sub, email)

	assert.Equal(t, own, currentHousehold(t, doWithHousehold(h, "/api/v1/households/me", tok, own)))
	rec := doWithHousehold(h, "/api/v1/households/me", tok, other)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	rec = doWithHousehold(h, "/api/v1/me/households", tok, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var got listed
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Data, 1)
	assert.Equal(t, own, got.Data[0].ID)
}
