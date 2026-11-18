package asset

// #4912: an asset is readable by every role (ResAssets read = rolesEveryone),
// and its response carried purchase_price, current_value and purchase_date to
// whoever asked. That is money the family's bills and loans describe and a
// housesitter or a vendor cannot open, so the record route was a side door
// around the money policy (KB decisions/2026-11-15-role-policy.md). #4808 had
// already hidden the same fields in the MCP's asset_find, so until this test's
// fix the API and the MCP gave a housesitter different answers for one asset.
//
// These tests run against a real Postgres (TEST_DATABASE_URL, every migration
// applied) and ask for the same asset as every role, once through the list and
// once through the single get. The per-role table is written out here, field by
// field, on purpose: deriving it from auth.CanSeeMoney would make the test pass
// for every role if the helper or the handler were broken the same way.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/config"
	"home-os/api/internal/middleware"
)

// assetMoneyFields is every money field of an asset response. It is spelled out
// here, independently of the Go structs, so a money field added later fails
// these tests until it is covered on purpose.
var assetMoneyFields = []string{"purchase_date", "purchase_price", "current_value"}

// assetIdentityFields is what an asset response carries whatever the role.
var assetIdentityFields = []string{
	"id", "household_id", "property_id", "name", "category", "manufacturer",
	"model", "serial_number", "warranty_expiry", "notes", "created_at", "updated_at",
}

// assetMoneyValues is what the family sees, field by field. The numbers are in
// the amounts and in the raw-body check below, so a leak cannot hide behind a
// decoded shape the test did not expect.
var assetMoneyValues = map[string]string{
	"purchase_date":  "2023-04-09",
	"purchase_price": "1299.50",
	"current_value":  "875.00",
}

// assetHiddenShape pins how a field with no visible value serializes, which is
// this endpoint's existing UI contract and must not move with the role: an
// omitted field and a null field both mean "no value you may see".
var assetHiddenShape = map[string]string{
	"purchase_date":  "absent",
	"purchase_price": "absent",
	"current_value":  "null",
}

type assetFixture struct {
	pool      *pgxpool.Pool
	router    http.Handler
	household string
	asset     string
	property  string
}

// setupAsset seeds one household with one property and one asset carrying every
// money field, so "hidden" cannot be confused with "never set".
func setupAsset(t *testing.T) *assetFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var hid string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Asset money roles') RETURNING id::text`).Scan(&hid))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hid)
	})

	var pid string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO properties (household_id, name, address) VALUES ($1, 'Main Residence', '12 Elm Street')
		 RETURNING id::text`, hid).Scan(&pid))

	var aid string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO assets
		(household_id, property_id, name, category, manufacturer, model, serial_number,
		 purchase_date, purchase_price, current_value, warranty_expiry, notes)
		VALUES ($1, $2, 'Kitchen fridge', 'Appliance', 'Bosch', 'B36CD50SNS', 'SN-99112233',
		        '2023-04-09', 1299.50, 875.00, '2028-04-09', 'Water line on the left')
		RETURNING id::text`, hid, pid).Scan(&aid))

	f := &assetFixture{pool: pool, household: hid, asset: aid, property: pid}
	f.router = assetRouter(pool)
	return f
}

// assetRouter mounts the asset reads with claims taken from the X-Test-*
// headers, the way RequireAuth would have put them in context. The route policy
// itself is covered by cmd/api/routes_test.go; these tests are about what the
// response carries for a role.
func assetRouter(pool *pgxpool.Pool) http.Handler {
	h := NewHandler(NewRepo(pool), &config.Config{})
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := &auth.Claims{
				HouseholdID: req.Header.Get("X-Test-Household"),
				Role:        req.Header.Get("X-Test-Role"),
			}
			next.ServeHTTP(w, req.WithContext(middleware.WithClaims(req.Context(), c)))
		})
	})
	r.Get("/api/v1/assets", h.List)
	r.Get("/api/v1/assets/{id}", h.Get)
	r.Post("/api/v1/assets", h.Create)
	return r
}

// get asks for path as role and returns the status, the decoded body and the
// raw body (the raw bytes are what a leak would show up in).
func (f *assetFixture) get(t *testing.T, role, path string) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, &bytes.Buffer{})
	req.Header.Set("X-Test-Household", f.household)
	req.Header.Set("X-Test-Role", role)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	raw := rec.Body.String()
	var body map[string]any
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), raw)
	}
	return rec.Code, body, raw
}

// TestAssetMoneyIsHiddenFromRolesWithoutMoney is the #4912 test: owner,
// family_manager, family_member and viewer see the amounts on both the list and
// the single get; housesitter, vendor and an unknown role get 200 with no
// amount in the response and the record itself intact.
//
// The roles are listed one by one with their expectation written out, so this
// fails if the handler stops asking who is calling — and it is not re-derived
// from auth.CanSeeMoney, which would let a single wrong policy edit hide.
func TestAssetMoneyIsHiddenFromRolesWithoutMoney(t *testing.T) {
	f := setupAsset(t)

	// Written out, not computed: the answer for each role.
	roleTable := []struct {
		role      string
		wantMoney bool
	}{
		{auth.RoleOwner, true},
		{auth.RoleFamilyManager, true},
		{auth.RoleFamilyMember, true},
		{auth.RoleViewer, true},
		{auth.RoleHousesitter, false},
		{auth.RoleVendor, false},
		{"something_else", false},
		{"", false},
	}

	// Both ways in: the list and the single get.
	paths := map[string]func(t *testing.T, role string) (map[string]any, string){
		"GET /assets": func(t *testing.T, role string) (map[string]any, string) {
			code, body, raw := f.get(t, role, "/api/v1/assets")
			require.Equal(t, http.StatusOK, code, "%s must still read the asset list", role)
			rows, ok := body["data"].([]any)
			require.True(t, ok, "list response has no data array: %s", raw)
			require.Len(t, rows, 1)
			row, ok := rows[0].(map[string]any)
			require.True(t, ok)
			return row, raw
		},
		"GET /assets/{id}": func(t *testing.T, role string) (map[string]any, string) {
			code, body, raw := f.get(t, role, "/api/v1/assets/"+f.asset)
			require.Equal(t, http.StatusOK, code, "%s must still read one asset", role)
			row, ok := body["data"].(map[string]any)
			require.True(t, ok, "get response has no data object: %s", raw)
			return row, raw
		},
	}

	for pathName, fetch := range paths {
		for _, tc := range roleTable {
			role := tc.role
			name := pathName + "/" + role
			if role == "" {
				name = pathName + "/(no role)"
			}
			t.Run(name, func(t *testing.T) {
				row, raw := fetch(t, role)

				// The record itself is never hidden: an outsider may still see
				// the family's fridge, its room and its warranty.
				for _, field := range assetIdentityFields {
					assert.Contains(t, row, field, "%s must carry %s", pathName, field)
				}
				assert.Equal(t, f.asset, row["id"])
				assert.Equal(t, "Kitchen fridge", row["name"])
				assert.Equal(t, "Appliance", row["category"])
				assert.Equal(t, "Bosch", row["manufacturer"])
				assert.Equal(t, "B36CD50SNS", row["model"])
				assert.Equal(t, "SN-99112233", row["serial_number"])
				assert.Equal(t, "2028-04-09", row["warranty_expiry"])
				assert.Equal(t, "Water line on the left", row["notes"])
				assert.Equal(t, f.property, row["property_id"])

				if tc.wantMoney {
					for _, field := range assetMoneyFields {
						assert.Equal(t, assetMoneyValues[field], row[field],
							"%s must be visible to %s", field, role)
					}
					// The whole point of the fixture values.
					assert.Contains(t, raw, "1299.50", "%s must carry the price", role)
					assert.Contains(t, raw, "875.00", "%s must carry the value", role)
					return
				}

				// No money: each field is either null or absent, exactly as this
				// endpoint already serializes a value that is not there.
				for _, field := range assetMoneyFields {
					value, present := row[field]
					assert.False(t, present && value != nil,
						"%s must carry no %s for %s: %v", pathName, field, role, value)
					switch assetHiddenShape[field] {
					case "null":
						assert.True(t, present, "%s must stay in the shape as null for %s", field, role)
						assert.Nil(t, value, "%s must be null for %s", field, role)
					case "absent":
						assert.False(t, present, "%s is omitted when it has no value for %s", field, role)
					}
				}
				// Belt and braces: the amounts appear nowhere in the bytes,
				// whatever key a future encoding puts them under.
				for _, amount := range []string{"1299.50", "875.00", "1299.5", "2023-04-09"} {
					assert.NotContains(t, raw, amount, "%s must not carry %s for %s", pathName, amount, role)
				}
			})
		}
	}
}

// TestAssetCreateStillReturnsMoneyToTheFamily guards the other direction: the
// write routes are rolesFamily only, so the family must keep seeing what it
// typed. It also catches a fix that hides money from everyone, which the
// matrix above alone would not (a handler that always hides passes it).
func TestAssetCreateStillReturnsMoneyToTheFamily(t *testing.T) {
	f := setupAsset(t)

	body := `{"name":"Dryer","purchase_date":"2024-01-02","purchase_price":"899.99","current_value":"700.00"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assets", strings.NewReader(body))
	req.Header.Set("X-Test-Household", f.household)
	req.Header.Set("X-Test-Role", auth.RoleOwner)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var resp struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "899.99", resp.Data["purchase_price"])
	assert.Equal(t, "700.00", resp.Data["current_value"])
	assert.Equal(t, "2024-01-02", resp.Data["purchase_date"])
}
