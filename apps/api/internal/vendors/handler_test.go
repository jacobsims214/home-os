package vendors

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
	Kind   string   `json:"kind"`
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

// TestVendorAddress pins #4766: address is its own column (065). POST and
// GET round-trip it on one line; PUT without address keeps it (the UI
// predates the column), null or "" clears it; notes stay separate.
func TestVendorAddress(t *testing.T) {
	f := setup(t)
	type addrJSON struct {
		ID      string  `json:"id"`
		Address *string `json:"address"`
		Notes   *string `json:"notes"`
	}
	decode := func(t *testing.T, data json.RawMessage) addrJSON {
		t.Helper()
		var v addrJSON
		require.NoError(t, json.Unmarshal(data, &v))
		return v
	}
	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}

	code, data := f.do(t, http.MethodPost, "/api/v1/vendors",
		map[string]any{"name": "Joe", "address": "  12 Main St\n Springfield ", "notes": "Ask for Joe"})
	require.Equal(t, http.StatusCreated, code)
	joe := decode(t, data)
	assert.Equal(t, "12 Main St Springfield", str(joe.Address), "one line, trimmed")
	assert.Equal(t, "Ask for Joe", str(joe.Notes), "notes carry no Address: line")

	var stored *string
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT address FROM vendors WHERE id = $1`, joe.ID).Scan(&stored))
	assert.Equal(t, "12 Main St Springfield", str(stored))

	code, data = f.do(t, http.MethodGet, "/api/v1/vendors/"+joe.ID, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "12 Main St Springfield", str(decode(t, data).Address))

	code, data = f.do(t, http.MethodPut, "/api/v1/vendors/"+joe.ID, map[string]any{"name": "Joe", "notes": "New notes"})
	require.Equal(t, http.StatusOK, code)
	v := decode(t, data)
	assert.Equal(t, "12 Main St Springfield", str(v.Address), "omitted address is kept")
	assert.Equal(t, "New notes", str(v.Notes))

	code, data = f.do(t, http.MethodPut, "/api/v1/vendors/"+joe.ID, map[string]any{"name": "Joe", "address": "9 Elm St"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "9 Elm St", str(decode(t, data).Address))

	code, data = f.do(t, http.MethodPut, "/api/v1/vendors/"+joe.ID, map[string]any{"name": "Joe", "address": nil})
	require.Equal(t, http.StatusOK, code)
	assert.Nil(t, decode(t, data).Address, "null clears")

	code, data = f.do(t, http.MethodPut, "/api/v1/vendors/"+joe.ID, map[string]any{"name": "Joe", "address": "1 A St"})
	require.Equal(t, http.StatusOK, code)
	code, data = f.do(t, http.MethodPut, "/api/v1/vendors/"+joe.ID, map[string]any{"name": "Joe", "address": "   "})
	require.Equal(t, http.StatusOK, code)
	assert.Nil(t, decode(t, data).Address, "blank clears")

	long := string(bytes.Repeat([]byte("x"), 501))
	code, _ = f.do(t, http.MethodPost, "/api/v1/vendors", map[string]any{"name": "Long", "address": long})
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = f.do(t, http.MethodPut, "/api/v1/vendors/"+joe.ID, map[string]any{"name": "Joe", "address": long})
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = f.do(t, http.MethodPut, "/api/v1/vendors/"+joe.ID, map[string]any{"name": "Joe", "address": 12})
	assert.Equal(t, http.StatusBadRequest, code, "non-string address")
}

func TestNormalizeAddress(t *testing.T) {
	s := func(v string) *string { return &v }
	for _, tc := range []struct {
		in   *string
		want string
		bad  bool
	}{
		{nil, "<nil>", false},
		{s(""), "<nil>", false},
		{s(" \n\t "), "<nil>", false},
		{s(" 12  Main St\r\nApt 2 "), "12 Main St Apt 2", false},
		{s(strings.Repeat("é", 500)), strings.Repeat("é", 500), false},
		{s(strings.Repeat("é", 501)), "", true},
	} {
		got, msg := normalizeAddress(tc.in)
		if tc.bad {
			assert.NotEmpty(t, msg)
			continue
		}
		assert.Empty(t, msg)
		g := "<nil>"
		if got != nil {
			g = *got
		}
		assert.Equal(t, tc.want, g)
	}
}

// --- kind: the two lists stay separate (#5529, migration 080) ---

// TestNormalizeKind covers the write path's validation: blank means "not
// given" (create -> the database default, update -> keep the stored kind),
// and every enum value passes through lower-cased.
func TestNormalizeKind(t *testing.T) {
	tests := []struct {
		in, want, wantErr string
	}{
		{"", "", ""},
		{"  ", "", ""},
		{"service_provider", "service_provider", ""},
		{"PAYEE", "payee", ""},
		{" Both ", "both", ""},
		{"utility", "", "kind must be service_provider, payee or both"},
		{"both ", "both", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, msg := normalizeKind(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantErr, msg)
		})
	}
}

// TestNormalizeKindFilter covers ?kind=: blank is the contacts list (the
// pre-080 behaviour), and an unknown value is an error rather than a silent
// fallback, so a mistyped client never gets contacts while asking for payees.
func TestNormalizeKindFilter(t *testing.T) {
	tests := []struct{ in, want, wantErr string }{
		{"", "", ""},
		{"service_provider", "service_provider", ""},
		{"payee", "payee", ""},
		{"Payee", "payee", ""},
		{"both", "both", ""},
		{"payees", "", "kind must be service_provider, payee or both"},
		{"service provider", "", "kind must be service_provider, payee or both"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, msg := normalizeKindFilter(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantErr, msg)
		})
	}
}

// TestVendorKindKeepsListsSeparate is the point of 080: a payee-only row
// (the electric utility) never shows up in the contacts list, a
// service-provider-only row (the gardener) never shows up in the payee
// picker, and a row that is both shows up in each.
func TestVendorKindKeepsListsSeparate(t *testing.T) {
	f := setup(t)

	gardener := f.create(t, map[string]any{"name": "Green Thumb Gardening", "trades": []string{"Gardening"}})
	assert.Equal(t, "service_provider", gardener.Kind, "an omitted kind is a contact")

	utility := f.create(t, map[string]any{"name": "Metro Electric", "kind": "payee"})
	assert.Equal(t, "payee", utility.Kind)

	both := f.create(t, map[string]any{"name": "Comfort HVAC", "kind": "both", "trades": []string{"hvac"}})
	assert.Equal(t, "both", both.Kind)

	t.Run("the contacts list excludes payee-only rows", func(t *testing.T) {
		got := f.list(t, "")
		assert.ElementsMatch(t, []string{"Green Thumb Gardening", "Comfort HVAC"}, got,
			"the plumber list must not fill with utilities")
		assert.NotContains(t, got, "Metro Electric")
		assert.ElementsMatch(t, []string{"Green Thumb Gardening", "Comfort HVAC"}, f.list(t, "?kind=service_provider"),
			"naming the kind explicitly gives the same set")
	})

	t.Run("the payee picker excludes service-provider-only rows", func(t *testing.T) {
		got := f.list(t, "?kind=payee")
		assert.ElementsMatch(t, []string{"Metro Electric", "Comfort HVAC"}, got,
			"the payee picker must not offer the gardener who is not also paid")
		assert.NotContains(t, got, "Green Thumb Gardening")
	})

	t.Run("kind=both is exactly the rows that are both", func(t *testing.T) {
		assert.Equal(t, []string{"Comfort HVAC"}, f.list(t, "?kind=both"))
	})

	t.Run("trade and kind compose", func(t *testing.T) {
		assert.Equal(t, []string{"Comfort HVAC"}, f.list(t, "?trade=hvac&kind=payee"))
		assert.Equal(t, []string{"Comfort HVAC"}, f.list(t, "?trade=hvac"))
		assert.Empty(t, f.list(t, "?trade=electrician&kind=payee"),
			"a trade filter does not resurrect a row the kind excludes")
	})

	t.Run("an unknown kind is a 400", func(t *testing.T) {
		code, _ := f.do(t, http.MethodGet, "/api/v1/vendors?kind=payees", nil)
		assert.Equal(t, http.StatusBadRequest, code)
	})

	t.Run("an unknown kind on a write is a 400", func(t *testing.T) {
		code, _ := f.do(t, http.MethodPost, "/api/v1/vendors", map[string]any{"name": "X", "kind": "utility"})
		assert.Equal(t, http.StatusBadRequest, code)
	})

	t.Run("update omitting kind keeps the stored kind", func(t *testing.T) {
		code, data := f.do(t, http.MethodPut, "/api/v1/vendors/"+utility.ID, map[string]any{"name": "Metro Electric Co"})
		require.Equal(t, http.StatusOK, code, string(data))
		var v vendorJSON
		require.NoError(t, json.Unmarshal(data, &v))
		assert.Equal(t, "payee", v.Kind, "a client that predates 080 must not reset a payee to service_provider")
	})

	t.Run("update can reclassify a row", func(t *testing.T) {
		code, data := f.do(t, http.MethodPut, "/api/v1/vendors/"+gardener.ID,
			map[string]any{"name": "Green Thumb Gardening", "kind": "both"})
		require.Equal(t, http.StatusOK, code, string(data))
		var v vendorJSON
		require.NoError(t, json.Unmarshal(data, &v))
		assert.Equal(t, "both", v.Kind)
		assert.Contains(t, f.list(t, "?kind=payee"), "Green Thumb Gardening",
			"once it is also paid, the payee picker offers it")
		assert.Contains(t, f.list(t, ""), "Green Thumb Gardening",
			"and it stays in the contacts list")
	})
}
