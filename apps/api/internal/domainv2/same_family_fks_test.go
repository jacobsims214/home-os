package domainv2_test

// Migration 058 (#4697): vendor_id, schedule_id and calendars.property_id are
// same-family FKs, and the handlers answer a cross-family reference with 404
// (apierr.DB maps 23503) instead of writing it or failing with 500.

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/bill"
	"home-os/api/internal/calendar"
	"home-os/api/internal/maintenance"
	"home-os/api/internal/property"
	"home-os/api/internal/vendors"
)

func TestCrossFamilyVendorScheduleAndCalendarPropertyAre404(t *testing.T) {
	pool := testDB(t)
	r := chi.NewRouter()
	r.Post("/properties", property.NewHandler(property.NewRepo(pool)).CreateProperty)
	r.Post("/vendors", vendors.NewHandler(vendors.NewRepo(pool)).Create)
	bh := bill.NewHandler(bill.NewRepo(pool))
	r.Post("/bills", bh.Create)
	r.Put("/bills/{id}", bh.Update)
	mh := maintenance.NewHandler(maintenance.NewRepo(pool))
	r.Post("/maintenance/tasks", mh.CreateTask)
	r.Patch("/maintenance/tasks/{id}", mh.UpdateTask)
	r.Post("/maintenance/schedules", mh.CreateSchedule)
	r.Post("/calendars", calendar.NewHandler(calendar.NewRepo(pool)).CreateCalendar)

	a, b := family(t, pool), family(t, pool)
	vendorB := call(t, r, b, "POST", "/vendors", map[string]any{"name": "B plumber"}).id(t)
	propB := call(t, r, b, "POST", "/properties", map[string]any{"name": "B house"}).id(t)
	vendorA := call(t, r, a, "POST", "/vendors", map[string]any{"name": "A plumber"}).id(t)
	propA := call(t, r, a, "POST", "/properties", map[string]any{"name": "A house"}).id(t)

	billA := call(t, r, a, "POST", "/bills", map[string]any{"name": "Water"}).id(t)
	taskA := call(t, r, a, "POST", "/maintenance/tasks", map[string]any{"name": "Sink"}).id(t)

	for _, tc := range []struct {
		name, method, path string
		body               map[string]any
	}{
		{"create bill", "POST", "/bills", map[string]any{"name": "x", "vendor_id": vendorB}},
		{"update bill", "PUT", "/bills/" + billA, map[string]any{"vendor_id": vendorB}},
		{"create task", "POST", "/maintenance/tasks", map[string]any{"name": "x", "vendor_id": vendorB}},
		{"update task", "PATCH", "/maintenance/tasks/" + taskA, map[string]any{"vendor_id": vendorB}},
		{"create schedule", "POST", "/maintenance/schedules", map[string]any{"name": "x", "rrule": "FREQ=YEARLY", "vendor_id": vendorB}},
		{"create calendar", "POST", "/calendars", map[string]any{"name": "x", "type": "properties", "property_id": propB}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := call(t, r, a, tc.method, tc.path, tc.body)
			assert.Equal(t, http.StatusNotFound, res.Code, res.Body)
		})
	}

	// Same-family references still work.
	res := call(t, r, a, "PUT", "/bills/"+billA, map[string]any{"vendor_id": vendorA})
	assert.Equal(t, http.StatusOK, res.Code, res.Body)
	res = call(t, r, a, "POST", "/calendars", map[string]any{"name": "House", "type": "properties", "property_id": propA})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
}
