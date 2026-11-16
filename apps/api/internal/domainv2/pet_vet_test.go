package domainv2_test

// #4700: a pet's vet is a same-family vendor (pets.vet_vendor_id, composite
// FK from 043). The API never writes pets.vet_name / vet_phone; the response
// reads them from the vendor, and legacy vet_name / vet_phone in a request are
// mapped onto a family vendor.

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/pet"
	"home-os/api/internal/vendors"
)

func petRouter(pool *pgxpool.Pool) http.Handler {
	r := chi.NewRouter()
	ph := pet.NewHandler(pet.NewRepo(pool))
	r.Get("/pets", ph.List)
	r.Post("/pets", ph.Create)
	r.Get("/pets/{id}", ph.Get)
	r.Put("/pets/{id}", ph.Update)
	r.Post("/vendors", vendors.NewHandler(vendors.NewRepo(pool)).Create)
	return r
}

func vendorCount(t *testing.T, pool *pgxpool.Pool, hh string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM vendors WHERE household_id = $1`, hh).Scan(&n))
	return n
}

func TestPetVetVendorIDSameFamily(t *testing.T) {
	pool := testDB(t)
	r := petRouter(pool)
	a, b := family(t, pool), family(t, pool)

	vetA := call(t, r, a, "POST", "/vendors", map[string]any{"name": "A Vet", "phone": "555-0100", "trades": []string{"vet"}}).id(t)
	vetB := call(t, r, b, "POST", "/vendors", map[string]any{"name": "B Vet", "trades": []string{"vet"}}).id(t)

	// Same-family vendor: stored, and name/phone come from the vendor.
	res := call(t, r, a, "POST", "/pets", map[string]any{"name": "Rex", "vet_vendor_id": vetA})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	petA := res.id(t)
	assert.Equal(t, vetA, res.data()["vet_vendor_id"])
	assert.Equal(t, "A Vet", res.data()["vet_name"])
	assert.Equal(t, "555-0100", res.data()["vet_phone"])

	got := call(t, r, a, "GET", "/pets/"+petA, nil)
	require.Equal(t, http.StatusOK, got.Code)
	assert.Equal(t, "A Vet", got.data()["vet_name"])

	list := call(t, r, a, "GET", "/pets", nil)
	require.Equal(t, http.StatusOK, list.Code)
	items, _ := list.Body["data"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, "555-0100", items[0].(map[string]any)["vet_phone"])

	// Another family's vendor: 404 on create and update, nothing written.
	res = call(t, r, a, "POST", "/pets", map[string]any{"name": "Spy", "vet_vendor_id": vetB})
	assert.Equal(t, http.StatusNotFound, res.Code, res.Body)
	res = call(t, r, a, "PUT", "/pets/"+petA, map[string]any{"name": "Rex", "vet_vendor_id": vetB})
	assert.Equal(t, http.StatusNotFound, res.Code, res.Body)
	got = call(t, r, a, "GET", "/pets/"+petA, nil)
	assert.Equal(t, vetA, got.data()["vet_vendor_id"])

	// PUT is a full replace: no vet clears it.
	res = call(t, r, a, "PUT", "/pets/"+petA, map[string]any{"name": "Rex"})
	require.Equal(t, http.StatusOK, res.Code, res.Body)
	assert.NotContains(t, res.data(), "vet_vendor_id")
	assert.NotContains(t, res.data(), "vet_name")

	// Updating a missing pet with a legacy vet creates no vendor.
	before := vendorCount(t, pool, a)
	res = call(t, r, a, "PUT", "/pets/00000000-0000-0000-0000-000000000001", map[string]any{"name": "x", "vet_name": "Nobody"})
	assert.Equal(t, http.StatusNotFound, res.Code)
	assert.Equal(t, before, vendorCount(t, pool, a))
}

func TestPetLegacyVetFieldsMapToFamilyVendor(t *testing.T) {
	pool := testDB(t)
	r := petRouter(pool)
	a, b := family(t, pool), family(t, pool)

	// B has a vendor with the same name; A must never be linked to it.
	call(t, r, b, "POST", "/vendors", map[string]any{"name": "Springfield Animal Hospital"})

	// No match in A: a new vendor with trade vet is created.
	res := call(t, r, a, "POST", "/pets", map[string]any{"name": "Max", "vet_name": "Springfield Animal Hospital", "vet_phone": "555-0100"})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	vet, _ := res.data()["vet_vendor_id"].(string)
	require.NotEmpty(t, vet)
	assert.Equal(t, "Springfield Animal Hospital", res.data()["vet_name"])
	assert.Equal(t, 1, vendorCount(t, pool, a))

	var hh string
	var trades []string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT household_id::text, trades FROM vendors WHERE id = $1`, vet).Scan(&hh, &trades))
	assert.Equal(t, a, hh)
	assert.Equal(t, []string{"vet"}, trades)

	// Same name (different case) with a compatible phone: reused.
	res = call(t, r, a, "POST", "/pets", map[string]any{"name": "Luna", "vet_name": " springfield animal hospital ", "vet_phone": "(555) 0100"})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	assert.Equal(t, vet, res.data()["vet_vendor_id"])

	// Phone only: matched by digits.
	res = call(t, r, a, "POST", "/pets", map[string]any{"name": "Bo", "vet_phone": "555.0100"})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	assert.Equal(t, vet, res.data()["vet_vendor_id"])
	assert.Equal(t, 1, vendorCount(t, pool, a))

	// Round trip the UI does: PUT back the name/phone it was shown.
	bo := res.id(t)
	res = call(t, r, a, "PUT", "/pets/"+bo, map[string]any{"name": "Bo", "vet_name": "Springfield Animal Hospital", "vet_phone": "555-0100"})
	require.Equal(t, http.StatusOK, res.Code, res.Body)
	assert.Equal(t, vet, res.data()["vet_vendor_id"])
	assert.Equal(t, 1, vendorCount(t, pool, a))

	// vet_vendor_id wins over legacy fields.
	other := call(t, r, a, "POST", "/vendors", map[string]any{"name": "Other Vet"}).id(t)
	res = call(t, r, a, "PUT", "/pets/"+bo, map[string]any{"name": "Bo", "vet_vendor_id": other, "vet_name": "Springfield Animal Hospital"})
	require.Equal(t, http.StatusOK, res.Code, res.Body)
	assert.Equal(t, other, res.data()["vet_vendor_id"])

	// The pets table has no legacy vet columns any more (migration 060).
	var legacy int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'pets' AND column_name IN ('vet_name', 'vet_phone')`).Scan(&legacy))
	assert.Zero(t, legacy)
}

// #4768: a vendor matched by legacy vet_name / vet_phone gets trade vet and a
// blank phone filled, as migrations 043/060 do, so it shows in
// GET /vendors?trade=vet. An explicit vet_vendor_id is never changed.
func TestPetLegacyVetMatchTagsVendor(t *testing.T) {
	pool := testDB(t)
	r := petRouter(pool)
	a := family(t, pool)
	bg := context.Background()
	old := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

	vendor := func(name string, trades []string, phone *string) string {
		var id string
		require.NoError(t, pool.QueryRow(bg,
			`INSERT INTO vendors (household_id, name, trades, phone, updated_at) VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
			a, name, trades, phone, old).Scan(&id))
		return id
	}
	type row struct {
		trades  []string
		phone   *string
		updated time.Time
	}
	read := func(id string) row {
		var v row
		require.NoError(t, pool.QueryRow(bg, `SELECT trades, phone, updated_at FROM vendors WHERE id = $1`, id).Scan(&v.trades, &v.phone, &v.updated))
		return v
	}
	p := func(s string) *string { return &s }

	// No trades, no phone: tagged vet, phone filled.
	smith := vendor("Dr. Smith", []string{}, nil)
	res := call(t, r, a, "POST", "/pets", map[string]any{"name": "Rex", "vet_name": "dr. smith", "vet_phone": " 555-0111 "})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	assert.Equal(t, smith, res.data()["vet_vendor_id"])
	assert.Equal(t, "555-0111", res.data()["vet_phone"])
	v := read(smith)
	assert.Equal(t, []string{"vet"}, v.trades)
	require.NotNil(t, v.phone)
	assert.Equal(t, "555-0111", *v.phone)
	assert.True(t, v.updated.After(old))

	// It is listed by GET /vendors?trade=vet.
	vr := chi.NewRouter()
	vr.Get("/vendors", vendors.NewHandler(vendors.NewRepo(pool)).List)
	list := call(t, vr, a, "GET", "/vendors?trade=vet", nil)
	require.Equal(t, http.StatusOK, list.Code, list.Body)
	items, _ := list.Body["data"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, smith, items[0].(map[string]any)["id"])

	// Other trades, no phone given: vet appended; a blank phone becomes NULL
	// (COALESCE(NULLIF(btrim(phone), ''), NULL), as in 060).
	jones := vendor("Dr. Jones", []string{"groomer"}, p(" "))
	res = call(t, r, a, "POST", "/pets", map[string]any{"name": "Max", "vet_name": "Dr. Jones"})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	v = read(jones)
	assert.Equal(t, []string{"groomer", "vet"}, v.trades)
	assert.Nil(t, v.phone)

	// Already a vet with a phone: not written at all.
	ready := vendor("Dr. Ready", []string{"vet"}, p("555-0133"))
	res = call(t, r, a, "POST", "/pets", map[string]any{"name": "Luna", "vet_name": "Dr. Ready", "vet_phone": "5550133"})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	assert.Equal(t, ready, res.data()["vet_vendor_id"])
	v = read(ready)
	assert.Equal(t, []string{"vet"}, v.trades)
	assert.Equal(t, "555-0133", *v.phone)
	assert.True(t, v.updated.Equal(old), "updated_at changed: %v", v.updated)

	// Phone only (PUT): matched by digits, tagged, its phone kept.
	clinic := vendor("Corner Clinic", []string{}, p("(555) 0144"))
	luna := res.id(t)
	res = call(t, r, a, "PUT", "/pets/"+luna, map[string]any{"name": "Luna", "vet_phone": "555-0144"})
	require.Equal(t, http.StatusOK, res.Code, res.Body)
	assert.Equal(t, clinic, res.data()["vet_vendor_id"])
	v = read(clinic)
	assert.Equal(t, []string{"vet"}, v.trades)
	assert.Equal(t, "(555) 0144", *v.phone)

	// Explicit vet_vendor_id (even with legacy fields): untouched.
	plain := vendor("Plain Contact", []string{}, nil)
	res = call(t, r, a, "PUT", "/pets/"+luna, map[string]any{"name": "Luna", "vet_vendor_id": plain, "vet_name": "Plain Contact", "vet_phone": "555-0166"})
	require.Equal(t, http.StatusOK, res.Code, res.Body)
	assert.Equal(t, plain, res.data()["vet_vendor_id"])
	res = call(t, r, a, "POST", "/pets", map[string]any{"name": "Bo", "vet_vendor_id": plain})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	v = read(plain)
	assert.Empty(t, v.trades)
	assert.Nil(t, v.phone)
	assert.True(t, v.updated.Equal(old), "updated_at changed: %v", v.updated)
}

// #4768: parallel legacy creates naming the same new vet make one vendor
// (resolveVet takes the pet_vet advisory lock, like the MCP pet tools).
func TestPetLegacyVetParallelOneVendor(t *testing.T) {
	pool := testDB(t)
	r := petRouter(pool)
	a := family(t, pool)
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = call(t, r, a, "POST", "/pets", map[string]any{"name": fmt.Sprintf("Pet %d", i), "vet_name": "Brand New Vet"}).Code
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		assert.Equal(t, http.StatusCreated, c, "create %d", i)
	}
	assert.Equal(t, 1, vendorCount(t, pool, a))
}
