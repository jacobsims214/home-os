// Package domainv2_test checks the API against the domain model v2 ownership
// rules (KB decisions/domain-model-v2.md, migrations 038/039): family-owned
// pets and vehicles, typed bill/loan/maintenance subjects, family-level
// notes/files/secrets, 404 on cross-family references, and the property
// delete confirmation. It needs TEST_DATABASE_URL with every migration
// applied and skips without it.
package domainv2_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/asset"
	"home-os/api/internal/auth"
	"home-os/api/internal/bill"
	"home-os/api/internal/config"
	"home-os/api/internal/link"
	"home-os/api/internal/loan"
	"home-os/api/internal/maintenance"
	"home-os/api/internal/middleware"
	"home-os/api/internal/note"
	"home-os/api/internal/pet"
	"home-os/api/internal/property"
	"home-os/api/internal/secret"
	"home-os/api/internal/vehicle"
	"home-os/api/internal/vendors"
)

func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// family creates a household; deleting it on cleanup cascades to its rows.
func family(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO households (name) VALUES ('Domain v2 test') RETURNING id::text`).Scan(&id))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, id) })
	return id
}

func router(pool *pgxpool.Pool) http.Handler {
	r := chi.NewRouter()
	ph := property.NewHandler(property.NewRepo(pool))
	r.Post("/properties", ph.CreateProperty)
	r.Delete("/properties/{id}", ph.DeleteProperty)
	r.Post("/properties/{id}/rooms", ph.CreateRoom)
	ah := asset.NewHandler(asset.NewRepo(pool), &config.Config{})
	r.Post("/assets", ah.Create)
	r.Put("/assets/{id}", ah.Update)
	r.Post("/pets", pet.NewHandler(pet.NewRepo(pool)).Create)
	r.Post("/vehicles", vehicle.NewHandler(vehicle.NewRepo(pool)).Create)
	vh := vendors.NewHandler(vendors.NewRepo(pool))
	r.Post("/vendors", vh.Create)
	bh := bill.NewHandler(bill.NewRepo(pool))
	r.Post("/bills", bh.Create)
	r.Put("/bills/{id}", bh.Update)
	lh := loan.NewHandler(loan.NewRepo(pool))
	r.Post("/loans", lh.Create)
	r.Get("/loans", lh.List)
	mh := maintenance.NewHandler(maintenance.NewRepo(pool))
	r.Post("/maintenance/tasks", mh.CreateTask)
	r.Patch("/maintenance/tasks/{id}", mh.UpdateTask)
	r.Get("/maintenance/tasks", mh.ListTasks)
	r.Post("/notes", note.NewHandler(note.NewRepo(pool)).Create)
	r.Post("/secrets", secret.NewHandler(secret.NewRepo(pool)).CreateSecret)
	r.Post("/links", link.NewHandler(link.NewRepo(pool)).Create)
	return r
}

type result struct {
	Code int
	Body map[string]any
}

func (r result) data() map[string]any {
	d, _ := r.Body["data"].(map[string]any)
	return d
}

func (r result) id(t *testing.T) string {
	t.Helper()
	id, _ := r.data()["id"].(string)
	require.NotEmpty(t, id, "response has no data.id: %v", r.Body)
	return id
}

func call(t *testing.T, h http.Handler, hh, method, path string, body any) result {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req = req.WithContext(middleware.WithClaims(req.Context(), &auth.Claims{HouseholdID: hh, Role: "owner"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := result{Code: rec.Code}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out.Body)
	}
	return out
}

func TestFamilyOwnedAssetsNeedNoProperty(t *testing.T) {
	pool := testDB(t)
	h := router(pool)
	hh := family(t, pool)

	assert.Equal(t, http.StatusCreated, call(t, h, hh, "POST", "/pets", map[string]any{"name": "Rex"}).Code)
	assert.Equal(t, http.StatusCreated, call(t, h, hh, "POST", "/vehicles", map[string]any{"make": "Ford"}).Code)
	res := call(t, h, hh, "POST", "/assets", map[string]any{"name": "Laptop"})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	assert.Nil(t, res.data()["property_id"], "family-level asset has no property")

	// Vendors no longer carry a property; an old client sending one is ignored.
	res = call(t, h, hh, "POST", "/vendors", map[string]any{"name": "Plumber", "property_id": uuid.NewString()})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	assert.NotContains(t, res.data(), "property_id")
}

func TestPropertyScopedAssetsAndRooms(t *testing.T) {
	pool := testDB(t)
	h := router(pool)
	hh := family(t, pool)

	propID := call(t, h, hh, "POST", "/properties", map[string]any{"name": "Lake house"}).id(t)
	room := call(t, h, hh, "POST", "/properties/"+propID+"/rooms", map[string]any{"name": "Kitchen"})
	require.Equal(t, http.StatusCreated, room.Code, room.Body)
	roomID := room.id(t)
	var roomHH string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT household_id::text FROM rooms WHERE id = $1`, roomID).Scan(&roomHH))
	assert.Equal(t, hh, roomHH)

	// An asset in a room gets its property from the room.
	res := call(t, h, hh, "POST", "/assets", map[string]any{"name": "Fridge", "room_id": roomID})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	assetID := res.id(t)
	assert.Equal(t, propID, res.data()["property_id"])

	// Clearing the property makes it family-level and takes it out of the room.
	res = call(t, h, hh, "PUT", "/assets/"+assetID, map[string]any{"property_id": ""})
	require.Equal(t, http.StatusOK, res.Code, res.Body)
	assert.Nil(t, res.data()["property_id"])
	assert.Nil(t, res.data()["room_id"])

	// Another family's room is a 404, not a 500.
	other := family(t, pool)
	res = call(t, h, other, "POST", "/assets", map[string]any{"name": "Stolen", "room_id": roomID})
	assert.Equal(t, http.StatusNotFound, res.Code, res.Body)
}

func TestPropertyDeleteNeedsConfirmAndReportsAssets(t *testing.T) {
	pool := testDB(t)
	h := router(pool)
	hh := family(t, pool)

	propID := call(t, h, hh, "POST", "/properties", map[string]any{"name": "Cabin"}).id(t)
	for _, n := range []string{"Boat", "Grill"} {
		require.Equal(t, http.StatusCreated, call(t, h, hh, "POST", "/assets", map[string]any{"name": n, "property_id": propID}).Code)
	}

	res := call(t, h, hh, "DELETE", "/properties/"+propID, nil)
	require.Equal(t, http.StatusConflict, res.Code, res.Body)
	assert.EqualValues(t, 2, res.Body["asset_count"])

	assert.Equal(t, http.StatusNotFound, call(t, h, family(t, pool), "DELETE", "/properties/"+propID+"?confirm=true", nil).Code)

	res = call(t, h, hh, "DELETE", "/properties/"+propID+"?confirm=true", nil)
	require.Equal(t, http.StatusOK, res.Code, res.Body)
	assert.EqualValues(t, 2, res.data()["asset_count"])
	var left int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM assets WHERE household_id = $1`, hh).Scan(&left))
	assert.Zero(t, left, "assets go with their property")
}

func TestBillAndLoanTypedSubjects(t *testing.T) {
	pool := testDB(t)
	h := router(pool)
	hh := family(t, pool)

	propID := call(t, h, hh, "POST", "/properties", map[string]any{"name": "Home"}).id(t)
	vehID := call(t, h, hh, "POST", "/vehicles", map[string]any{"make": "Subaru"}).id(t)
	assetID := call(t, h, hh, "POST", "/assets", map[string]any{"name": "HVAC", "property_id": propID}).id(t)

	// An asset subject derives the property.
	res := call(t, h, hh, "POST", "/bills", map[string]any{"name": "HVAC plan", "asset_id": assetID})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	billID := res.id(t)
	assert.Equal(t, propID, res.data()["property_id"])
	assert.Equal(t, "asset", res.data()["entity_type"], "legacy field derived for old clients")

	// Switching to a vehicle clears property and asset.
	res = call(t, h, hh, "PUT", "/bills/"+billID, map[string]any{"vehicle_id": vehID})
	require.Equal(t, http.StatusOK, res.Code, res.Body)
	assert.Equal(t, vehID, res.data()["vehicle_id"])
	assert.Nil(t, res.data()["property_id"])
	assert.Nil(t, res.data()["asset_id"])

	// Naming both is a 400.
	res = call(t, h, hh, "POST", "/bills", map[string]any{"name": "Bad", "vehicle_id": vehID, "property_id": propID})
	assert.Equal(t, http.StatusBadRequest, res.Code, res.Body)

	// Family-level bill.
	res = call(t, h, hh, "POST", "/bills", map[string]any{"name": "Netflix"})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	assert.Nil(t, res.data()["entity_type"])

	// Legacy entity_type/entity_id still works on loans and lands in the typed column.
	res = call(t, h, hh, "POST", "/loans", map[string]any{"name": "Car loan", "original_amount": "100", "remaining_balance": "50", "entity_type": "vehicle", "entity_id": vehID})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	assert.Equal(t, vehID, res.data()["vehicle_id"])
	res = call(t, h, hh, "POST", "/loans", map[string]any{"name": "Mortgage", "original_amount": "100", "remaining_balance": "50", "property_id": propID})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	list := call(t, h, hh, "GET", "/loans?entity_type=vehicle", nil)
	require.Equal(t, http.StatusOK, list.Code)
	rows, _ := list.Body["data"].([]any)
	assert.Len(t, rows, 1)

	// Another family's property is a 404.
	other := family(t, pool)
	res = call(t, h, other, "POST", "/bills", map[string]any{"name": "Sneaky", "property_id": propID})
	assert.Equal(t, http.StatusNotFound, res.Code, res.Body)
	res = call(t, h, other, "POST", "/loans", map[string]any{"name": "Sneaky", "original_amount": "1", "remaining_balance": "1", "vehicle_id": vehID})
	assert.Equal(t, http.StatusNotFound, res.Code, res.Body)
}

func TestMaintenanceNeverWritesVehicleAndProperty(t *testing.T) {
	pool := testDB(t)
	h := router(pool)
	hh := family(t, pool)

	propID := call(t, h, hh, "POST", "/properties", map[string]any{"name": "Home"}).id(t)
	vehID := call(t, h, hh, "POST", "/vehicles", map[string]any{"make": "Honda"}).id(t)
	assetID := call(t, h, hh, "POST", "/assets", map[string]any{"name": "Water heater", "property_id": propID}).id(t)

	res := call(t, h, hh, "POST", "/maintenance/tasks", map[string]any{"name": "Both", "vehicle_id": vehID, "property_id": propID})
	assert.Equal(t, http.StatusBadRequest, res.Code, res.Body)

	res = call(t, h, hh, "POST", "/maintenance/tasks", map[string]any{"name": "Flush", "asset_id": assetID})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	taskID := res.id(t)

	// The property filter includes tasks about the property's assets.
	list := call(t, h, hh, "GET", "/maintenance/tasks?property_id="+propID, nil)
	rows, _ := list.Body["data"].([]any)
	assert.Len(t, rows, 1)

	// Moving the task to the vehicle clears property and asset in the same write.
	res = call(t, h, hh, "PATCH", "/maintenance/tasks/"+taskID, map[string]any{"vehicle_id": vehID})
	require.Equal(t, http.StatusOK, res.Code, res.Body)
	var gotProp, gotAsset *string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT property_id::text, asset_id::text FROM maintenance_tasks WHERE id = $1`, taskID).Scan(&gotProp, &gotAsset))
	assert.Nil(t, gotProp)
	assert.Nil(t, gotAsset)

	res = call(t, h, family(t, pool), "POST", "/maintenance/tasks", map[string]any{"name": "Sneaky", "vehicle_id": vehID})
	assert.Equal(t, http.StatusNotFound, res.Code, res.Body)
}

func TestFamilyLevelNotesSecretsAndLinks(t *testing.T) {
	pool := testDB(t)
	h := router(pool)
	hh := family(t, pool)
	other := family(t, pool)

	res := call(t, h, hh, "POST", "/notes", map[string]any{"entity_type": "household", "body": "Wi-Fi is on the fridge"})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	assert.Equal(t, "household", res.data()["entity_type"])
	assert.Equal(t, hh, res.data()["entity_id"])

	propID := call(t, h, hh, "POST", "/properties", map[string]any{"name": "Home"}).id(t)
	res = call(t, h, other, "POST", "/notes", map[string]any{"entity_type": "property", "entity_id": propID, "body": "x"})
	assert.Equal(t, http.StatusNotFound, res.Code, res.Body)
	res = call(t, h, hh, "POST", "/notes", map[string]any{"entity_type": "spaceship", "entity_id": propID, "body": "x"})
	assert.Equal(t, http.StatusBadRequest, res.Code, res.Body)

	secretBody := map[string]any{"encrypted_data": []byte("c"), "iv": []byte("i"), "key_version": 1, "name": "Wi-Fi", "secret_type": "password"}
	res = call(t, h, hh, "POST", "/secrets", secretBody)
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	assert.Equal(t, "household", res.data()["entity_type"], "a secret with no entity is family-level")
	secretBody["entity_type"], secretBody["entity_id"] = "property", propID
	res = call(t, h, other, "POST", "/secrets", secretBody)
	assert.Equal(t, http.StatusNotFound, res.Code, res.Body)

	res = call(t, h, hh, "POST", "/links", map[string]any{"entity_type": "household", "entity_id": hh, "link_type": "url", "link_id": "x", "title": "Bank", "url": "https://bank.example"})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	res = call(t, h, hh, "POST", "/links", map[string]any{"entity_type": "property", "entity_id": propID, "link_type": "paperless", "link_id": "x", "title": "Old"})
	assert.Equal(t, http.StatusBadRequest, res.Code, res.Body)
}
