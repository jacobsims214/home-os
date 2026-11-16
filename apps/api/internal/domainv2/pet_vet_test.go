package domainv2_test

// #4700: a pet's vet is a same-family vendor (pets.vet_vendor_id, composite
// FK from 043). The API never writes pets.vet_name / vet_phone; the response
// reads them from the vendor, and legacy vet_name / vet_phone in a request are
// mapped onto a family vendor.

import (
	"context"
	"net/http"
	"testing"

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

	// The pets table's legacy columns are never written.
	var legacy int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM pets WHERE household_id = $1 AND (vet_name IS NOT NULL OR vet_phone IS NOT NULL)`, a).Scan(&legacy))
	assert.Zero(t, legacy)
}
