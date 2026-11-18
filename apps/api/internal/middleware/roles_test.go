package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
)

// policyRouter mounts a few real routes behind a fake-claims middleware and
// RequirePolicy. role "" means no claims at all.
func policyRouter(role string) http.Handler {
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if role != "" {
					c := &auth.Claims{UserID: uuid.NewString(), HouseholdID: uuid.NewString(), Role: role}
					req = req.WithContext(middleware.WithClaims(req.Context(), c))
				}
				next.ServeHTTP(w, req)
			})
		})
		r.Use(middleware.RequirePolicy)
		r.Get("/api/v1/bills", ok)
		r.Head("/api/v1/bills", ok)
		r.Post("/api/v1/bills", ok)
		r.Options("/api/v1/bills", ok)
		r.Get("/api/v1/secrets", ok)
		r.Patch("/api/v1/maintenance/tasks/{id}", ok)
	})
	return r
}

func TestRequirePolicy(t *testing.T) {
	tests := []struct {
		name, role, method, path string
		want                     int
	}{
		{"viewer reads bills", auth.RoleViewer, http.MethodGet, "/api/v1/bills", 200},
		{"viewer HEAD is a read", auth.RoleViewer, http.MethodHead, "/api/v1/bills", 200},
		{"viewer cannot write bills", auth.RoleViewer, http.MethodPost, "/api/v1/bills", 403},
		{"OPTIONS passes (CORS preflight)", auth.RoleVendor, http.MethodOptions, "/api/v1/bills", 200},
		{"member writes bills", auth.RoleFamilyMember, http.MethodPost, "/api/v1/bills", 200},
		{"housesitter cannot read bills", auth.RoleHousesitter, http.MethodGet, "/api/v1/bills", 403},
		{"viewer cannot read secrets", auth.RoleViewer, http.MethodGet, "/api/v1/secrets", 403},
		{"vendor reaches task status update", auth.RoleVendor, http.MethodPatch, "/api/v1/maintenance/tasks/" + uuid.NewString(), 200},
		{"housesitter cannot update tasks", auth.RoleHousesitter, http.MethodPatch, "/api/v1/maintenance/tasks/" + uuid.NewString(), 403},
		{"no claims", "", http.MethodGet, "/api/v1/bills", 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			policyRouter(tc.role).ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}

func TestDenyMessage(t *testing.T) {
	assert.Equal(t, "the vendor role cannot write bills", middleware.DenyMessage(auth.RoleVendor, auth.ResBills, auth.ActWrite))
	assert.Equal(t, "the viewer role cannot update status maintenance",
		middleware.DenyMessage(auth.RoleViewer, auth.ResMaintenance, auth.ActUpdateStatus))
	assert.Equal(t, "the unknown role cannot read bills", middleware.DenyMessage("", auth.ResBills, auth.ActRead))
}
