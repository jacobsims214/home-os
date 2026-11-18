package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/config"
	"home-os/api/internal/household"
	"home-os/api/internal/invite"
	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
)

// newProtectedFixture is cmd/api/main.go's protected group end to end: the
// real RequireAuth (Postgres member resolver), RequirePolicy and every
// protected route, on TEST_DATABASE_URL. Search points at nothing (indexing
// is best effort and only logs).
func newProtectedFixture(t *testing.T) *stage2Fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{key.Public()}}
	v := auth.NewVerifierWithKeySet(stage2TestIssuer, ks, []string{"home-os-ui"}).
		WithResolver(auth.NewMemberResolver(pool))

	cfg := &config.Config{TypesenseHost: "127.0.0.1", TypesensePort: "1"}
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		r.Use(middleware.RequirePolicy)
		registerProtectedRoutes(r, protectedDeps{
			pool:             pool,
			cfg:              cfg,
			search:           search.NewClient(cfg),
			householdHandler: household.NewHandler(household.NewRepo(pool)),
			inviteHandler:    invite.NewHandler(invite.NewRepo(pool), cfg, nil),
		})
	})
	return &stage2Fixture{pool: pool, h: r, key: key}
}

func dataOf(t *testing.T, body []byte) json.RawMessage {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &env), string(body))
	return env.Data
}

// TestRolePolicy_Records drives representative routes for every role through
// the real stack, including the record-level rules handlers apply from the
// same policy (#4689).
func TestRolePolicy_Records(t *testing.T) {
	f := newProtectedFixture(t)
	ctx := context.Background()
	hh := f.household(t)
	owner := f.member(t, hh, auth.RoleOwner)
	as := map[string]stage2Member{auth.RoleOwner: owner}
	for _, role := range auth.AllRoles[1:] {
		mem := f.member(t, hh, role)
		as[role] = mem
	}

	create := func(path string, body any) string {
		t.Helper()
		rec := f.do(t, http.MethodPost, path, body, &owner)
		require.Equal(t, http.StatusCreated, rec.Code, "%s: %s", path, rec.Body.String())
		return idOf(t, dataOf(t, rec.Body.Bytes()))
	}
	petID := create("/api/v1/pets", map[string]any{"name": "Max", "species": "dog"})
	billID := create("/api/v1/bills", map[string]any{"name": "Electric", "amount": "120.00", "frequency": "monthly"})
	taskID := create("/api/v1/maintenance/tasks", map[string]any{"name": "Fix sink"})
	petNote := create("/api/v1/notes", map[string]any{"entity_type": "pet", "entity_id": petID, "body": "feeds twice a day"})
	billNote := create("/api/v1/notes", map[string]any{"entity_type": "bill", "entity_id": billID, "body": "autopay"})

	var secretID string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type)
		 VALUES ($1, '\x00', '\x00', 'Wi-Fi password', 'login') RETURNING id::text`, hh).Scan(&secretID))
	secretNote := create("/api/v1/notes", map[string]any{"entity_type": "secret", "entity_id": secretID, "body": "router label"})

	family := map[string]bool{auth.RoleOwner: true, auth.RoleFamilyManager: true, auth.RoleFamilyMember: true}
	// familyThatManages is the narrower set: roles that may change the
	// household record itself (name, timezone), which is a management act.
	familyThatManages := map[string]bool{auth.RoleOwner: true, auth.RoleFamilyManager: true}

	t.Run("bills and secrets", func(t *testing.T) {
		for _, role := range auth.AllRoles {
			m := as[role]
			rec := f.do(t, http.MethodPost, "/api/v1/bills", map[string]any{"name": "Water " + role, "amount": "10.00", "frequency": "monthly"}, &m)
			if family[role] {
				assert.Equal(t, http.StatusCreated, rec.Code, "%s create bill: %s", role, rec.Body.String())
			} else {
				assert.Equal(t, http.StatusForbidden, rec.Code, "%s create bill: %s", role, rec.Body.String())
				assert.Contains(t, rec.Body.String(), "the "+role+" role cannot write bills")
			}

			rec = f.do(t, http.MethodGet, "/api/v1/bills", nil, &m)
			if family[role] || role == auth.RoleViewer {
				assert.Equal(t, http.StatusOK, rec.Code, "%s list bills", role)
			} else {
				assert.Equal(t, http.StatusForbidden, rec.Code, "%s list bills", role)
			}

			rec = f.do(t, http.MethodGet, "/api/v1/secrets", nil, &m)
			if family[role] {
				assert.Equal(t, http.StatusOK, rec.Code, "%s list secrets", role)
			} else {
				assert.Equal(t, http.StatusForbidden, rec.Code, "%s list secrets", role)
				assert.NotContains(t, rec.Body.String(), "Wi-Fi")
			}
		}
	})

	t.Run("housesitter notes", func(t *testing.T) {
		hs := as[auth.RoleHousesitter]
		rec := f.do(t, http.MethodGet, "/api/v1/notes", nil, &hs)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body := rec.Body.String()
		assert.Contains(t, body, petNote)
		assert.NotContains(t, body, billNote)
		assert.NotContains(t, body, secretNote)

		rec = f.do(t, http.MethodGet, "/api/v1/notes/"+billNote, nil, &hs)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

		rec = f.do(t, http.MethodPost, "/api/v1/notes", map[string]any{"entity_type": "pet", "entity_id": petID, "body": "walked at 6"}, &hs)
		assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		rec = f.do(t, http.MethodPost, "/api/v1/notes", map[string]any{"entity_type": "bill", "entity_id": billID, "body": "x"}, &hs)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		rec = f.do(t, http.MethodPut, "/api/v1/notes/"+petNote, map[string]any{"body": "changed"}, &hs)
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	})

	t.Run("viewer never sees credentials", func(t *testing.T) {
		vw := as[auth.RoleViewer]
		rec := f.do(t, http.MethodGet, "/api/v1/notes", nil, &vw)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), billNote)
		assert.NotContains(t, rec.Body.String(), secretNote)
		rec = f.do(t, http.MethodGet, "/api/v1/notes/"+secretNote, nil, &vw)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		rec = f.do(t, http.MethodPost, "/api/v1/notes", map[string]any{"entity_type": "pet", "entity_id": petID, "body": "x"}, &vw)
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

		rec = f.do(t, http.MethodGet, "/api/v1/activity?limit=200", nil, &vw)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var feed []struct {
			EntityType string  `json:"entity_type"`
			ParentType *string `json:"parent_type"`
			Summary    string  `json:"summary"`
		}
		require.NoError(t, json.Unmarshal(dataOf(t, rec.Body.Bytes()), &feed))
		require.NotEmpty(t, feed)
		for _, e := range feed {
			assert.NotEqual(t, "secret", e.EntityType, e.Summary)
			if e.ParentType != nil {
				assert.NotEqual(t, "secret", *e.ParentType, e.Summary)
			}
			assert.NotContains(t, e.Summary, "Wi-Fi")
		}
		// The owner does see the secret line.
		rec = f.do(t, http.MethodGet, "/api/v1/activity?limit=200", nil, &owner)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "Wi-Fi")

		for _, role := range []string{auth.RoleHousesitter, auth.RoleVendor} {
			m := as[role]
			rec = f.do(t, http.MethodGet, "/api/v1/activity", nil, &m)
			assert.Equal(t, http.StatusForbidden, rec.Code, role)
		}
	})

	t.Run("vendor updates maintenance status only", func(t *testing.T) {
		vd := as[auth.RoleVendor]
		rec := f.do(t, http.MethodGet, "/api/v1/maintenance/tasks", nil, &vd)
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rec = f.do(t, http.MethodPatch, "/api/v1/maintenance/tasks/"+taskID,
			map[string]any{"status": "done", "notes": "replaced the trap"}, &vd)
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rec = f.do(t, http.MethodPatch, "/api/v1/maintenance/tasks/"+taskID, map[string]any{"name": "renamed"}, &vd)
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		rec = f.do(t, http.MethodPost, "/api/v1/maintenance/tasks", map[string]any{"name": "new"}, &vd)
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		for _, path := range []string{"/api/v1/pets", "/api/v1/people", "/api/v1/calendars", "/api/v1/notes", "/api/v1/lists"} {
			rec = f.do(t, http.MethodGet, path, nil, &vd)
			assert.Equal(t, http.StatusForbidden, rec.Code, path)
		}
		rec = f.do(t, http.MethodGet, "/api/v1/properties", nil, &vd)
		assert.Equal(t, http.StatusOK, rec.Code)

		// A viewer cannot even change status.
		vw := as[auth.RoleViewer]
		rec = f.do(t, http.MethodPatch, "/api/v1/maintenance/tasks/"+taskID, map[string]any{"status": "pending"}, &vw)
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	})

	t.Run("housesitter lists: add and check, nothing else", func(t *testing.T) {
		rec := f.do(t, http.MethodPost, "/api/v1/lists", map[string]any{"name": "Chores", "kind": "todo", "items": []map[string]any{{"text": "water plants"}}}, &owner)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var l struct {
			ID    string `json:"id"`
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		require.NoError(t, json.Unmarshal(dataOf(t, rec.Body.Bytes()), &l))
		require.Len(t, l.Items, 1)
		item := l.Items[0].ID

		hs := as[auth.RoleHousesitter]
		rec = f.do(t, http.MethodPost, "/api/v1/lists/"+l.ID+"/items", map[string]any{"items": []map[string]any{{"text": "feed fish"}}}, &hs)
		assert.Less(t, rec.Code, 300, rec.Body.String())
		rec = f.do(t, http.MethodPatch, "/api/v1/lists/"+l.ID+"/items/"+item, map[string]any{"checked": true}, &hs)
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rec = f.do(t, http.MethodPatch, "/api/v1/lists/"+l.ID+"/items/"+item, map[string]any{"text": "renamed"}, &hs)
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		rec = f.do(t, http.MethodDelete, "/api/v1/lists/"+l.ID, nil, &hs)
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

		vw := as[auth.RoleViewer]
		rec = f.do(t, http.MethodPatch, "/api/v1/lists/"+l.ID+"/items/"+item, map[string]any{"checked": false}, &vw)
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	})

	t.Run("household name and timezone: managers only", func(t *testing.T) {
		// #4820: the whole route, through RequireAuth + RequirePolicy, for
		// every role. The body is valid, so a refusal can only be the role.
		body := map[string]any{"name": "Role Check", "timezone": "America/Chicago"}
		for _, role := range auth.AllRoles {
			m := as[role]
			rec := f.do(t, http.MethodPatch, "/api/v1/households/"+hh, body, &m)
			if familyThatManages[role] {
				assert.Equal(t, http.StatusOK, rec.Code, "%s: %s", role, rec.Body.String())
			} else {
				assert.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", role, rec.Body.String())
				assert.Contains(t, rec.Body.String(), "the "+role+" role cannot write household", role)
			}
		}

		// A bad timezone is a 400 for an owner, and the stored zone is kept.
		rec := f.do(t, http.MethodPatch, "/api/v1/households/"+hh,
			map[string]any{"timezone": "Mars/Olympus_Mons"}, &owner)
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		var tz string
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT timezone FROM households WHERE id = $1`, hh).Scan(&tz))
		assert.Equal(t, "America/Chicago", tz, "the refused zone was never written")
	})

	t.Run("agent tokens", func(t *testing.T) {
		for _, role := range auth.AllRoles {
			m := as[role]
			rec := f.do(t, http.MethodPost, "/api/v1/agent-tokens", map[string]any{"name": "rw", "scopes": []string{"read", "write"}}, &m)
			if family[role] {
				assert.Equal(t, http.StatusCreated, rec.Code, role)
			} else {
				assert.Equal(t, http.StatusForbidden, rec.Code, role)
				assert.True(t, strings.Contains(rec.Body.String(), role), rec.Body.String())
			}
		}
	})
}
