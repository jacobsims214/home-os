package vendors

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
)

func TestNormalizeTrade(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plumber", "plumber"},
		{"  Plumbing ", "plumber"},
		{"Pest Control", "pest_control"},
		{"pest-control", "pest_control"},
		{"pest__control", "pest_control"},
		{"Veterinarian", "vet"},
		{"HVAC", "hvac"},
		{"heating and cooling", "hvac"},
		{"Piano Tuner", "piano_tuner"},
		{"   ", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeTrade(tt.in))
		})
	}
}

func TestNormalizeTrades(t *testing.T) {
	long := string(bytes.Repeat([]byte("a"), maxTradeLen+1))
	many := make([]string, maxTrades+1)
	for i := range many {
		many[i] = "trade" + string(rune('a'+i))
	}
	tests := []struct {
		name    string
		in      []string
		want    []string
		wantErr bool
	}{
		{"nil gives empty", nil, []string{}, false},
		{"normalizes and dedupes in order", []string{"Plumbing", "vet", "plumber", "Veterinarian"}, []string{"plumber", "vet"}, false},
		{"blank rejected", []string{"plumber", " "}, nil, true},
		{"too long rejected", []string{long}, nil, true},
		{"too many rejected", many, nil, true},
		{"duplicates do not count toward the limit", append(append([]string{}, many[:maxTrades]...), many[0]), many[:maxTrades], false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, msg := normalizeTrades(tt.in)
			if tt.wantErr {
				assert.NotEmpty(t, msg)
				return
			}
			assert.Empty(t, msg)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- integration (TEST_DATABASE_URL, all migrations applied) ---

type vendorFixture struct {
	pool *pgxpool.Pool
	h    http.Handler
	hid  string
}

// setup builds the vendor routes with household claims injected directly
// (role policy is covered by cmd/api/routes_test.go).
func setup(t *testing.T) *vendorFixture {
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
		`INSERT INTO households (name) VALUES ('Vendor trades HH') RETURNING id::text`).Scan(&hid))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hid) })

	h := NewHandler(NewRepo(pool))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := &auth.Claims{HouseholdID: hid, Role: auth.RoleOwner}
			next.ServeHTTP(w, req.WithContext(middleware.WithClaims(req.Context(), c)))
		})
	})
	r.Get("/api/v1/vendors", h.List)
	r.Post("/api/v1/vendors", h.Create)
	r.Get("/api/v1/vendors/{id}", h.Get)
	r.Put("/api/v1/vendors/{id}", h.Update)
	return &vendorFixture{pool: pool, h: r, hid: hid}
}

func (f *vendorFixture) do(t *testing.T, method, path string, body any) (int, json.RawMessage) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec.Code, env.Data
}

type vendorJSON struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Trades []string `json:"trades"`
	Phone  *string  `json:"phone"`
}

func (f *vendorFixture) create(t *testing.T, body map[string]any) vendorJSON {
	t.Helper()
	code, data := f.do(t, http.MethodPost, "/api/v1/vendors", body)
	require.Equal(t, http.StatusCreated, code)
	var v vendorJSON
	require.NoError(t, json.Unmarshal(data, &v))
	return v
}

func (f *vendorFixture) list(t *testing.T, query string) []string {
	t.Helper()
	code, data := f.do(t, http.MethodGet, "/api/v1/vendors"+query, nil)
	require.Equal(t, http.StatusOK, code)
	var vs []vendorJSON
	require.NoError(t, json.Unmarshal(data, &vs))
	names := make([]string, 0, len(vs))
	for _, v := range vs {
		names = append(names, v.Name)
	}
	return names
}

func TestVendorTradesCreateUpdateList(t *testing.T) {
	f := setup(t)

	joe := f.create(t, map[string]any{"name": "Joe's Plumbing", "trades": []string{"Plumbing", "handyman", "plumber"}, "phone": "555-0100"})
	assert.Equal(t, []string{"plumber", "handyman"}, joe.Trades)

	vet := f.create(t, map[string]any{"name": "Oak Vet", "trades": []string{"Veterinarian"}})
	assert.Equal(t, []string{"vet"}, vet.Trades)

	none := f.create(t, map[string]any{"name": "No Trades Co"})
	assert.Equal(t, []string{}, none.Trades, "trades is always an array")

	t.Run("filter by trade", func(t *testing.T) {
		assert.Equal(t, []string{"Joe's Plumbing"}, f.list(t, "?trade=plumber"))
		assert.Equal(t, []string{"Joe's Plumbing"}, f.list(t, "?trade=Plumbing"), "synonym normalized")
		assert.Equal(t, []string{"Oak Vet"}, f.list(t, "?trade=vet"))
		assert.Empty(t, f.list(t, "?trade=roofer"))
		assert.Len(t, f.list(t, ""), 3)
		assert.Len(t, f.list(t, "?trade=+"), 3, "blank trade is no filter")
	})

	t.Run("filter is household scoped", func(t *testing.T) {
		var other string
		require.NoError(t, f.pool.QueryRow(context.Background(),
			`INSERT INTO households (name) VALUES ('Other HH') RETURNING id::text`).Scan(&other))
		t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, other) })
		_, err := f.pool.Exec(context.Background(),
			`INSERT INTO vendors (household_id, name, trades) VALUES ($1, 'Other Plumber', ARRAY['plumber'])`, other)
		require.NoError(t, err)
		assert.Equal(t, []string{"Joe's Plumbing"}, f.list(t, "?trade=plumber"))
	})

	t.Run("update without trades keeps them", func(t *testing.T) {
		code, data := f.do(t, http.MethodPut, "/api/v1/vendors/"+joe.ID, map[string]any{"name": "Joe's Plumbing LLC"})
		require.Equal(t, http.StatusOK, code)
		var v vendorJSON
		require.NoError(t, json.Unmarshal(data, &v))
		assert.Equal(t, []string{"plumber", "handyman"}, v.Trades)
	})

	t.Run("update replaces trades", func(t *testing.T) {
		code, data := f.do(t, http.MethodPut, "/api/v1/vendors/"+joe.ID,
			map[string]any{"name": "Joe's Plumbing LLC", "trades": []string{"Electrical", "plumber"}})
		require.Equal(t, http.StatusOK, code)
		var v vendorJSON
		require.NoError(t, json.Unmarshal(data, &v))
		assert.Equal(t, []string{"electrician", "plumber"}, v.Trades)
		assert.Equal(t, []string{"Joe's Plumbing LLC"}, f.list(t, "?trade=electrician"))
	})

	t.Run("update with empty trades clears them", func(t *testing.T) {
		code, data := f.do(t, http.MethodPut, "/api/v1/vendors/"+vet.ID,
			map[string]any{"name": "Oak Vet", "trades": []string{}})
		require.Equal(t, http.StatusOK, code)
		var v vendorJSON
		require.NoError(t, json.Unmarshal(data, &v))
		assert.Equal(t, []string{}, v.Trades)
		assert.Empty(t, f.list(t, "?trade=vet"))
	})

	t.Run("get returns trades", func(t *testing.T) {
		code, data := f.do(t, http.MethodGet, "/api/v1/vendors/"+joe.ID, nil)
		require.Equal(t, http.StatusOK, code)
		var v vendorJSON
		require.NoError(t, json.Unmarshal(data, &v))
		assert.Equal(t, []string{"electrician", "plumber"}, v.Trades)
	})

	t.Run("invalid trades are 400", func(t *testing.T) {
		code, _ := f.do(t, http.MethodPost, "/api/v1/vendors", map[string]any{"name": "Bad", "trades": []string{" "}})
		assert.Equal(t, http.StatusBadRequest, code)
		code, _ = f.do(t, http.MethodPut, "/api/v1/vendors/"+joe.ID, map[string]any{"name": "Bad", "trades": []string{""}})
		assert.Equal(t, http.StatusBadRequest, code)
		code, _ = f.do(t, http.MethodGet, "/api/v1/vendors?trade="+string(bytes.Repeat([]byte("x"), 65)), nil)
		assert.Equal(t, http.StatusBadRequest, code)
	})
}
