package vendors

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
	"home-os/api/pkg/apierr"
)

type searchIndexer interface {
	IndexDocument(ctx context.Context, doc search.SearchDocument) error
	DeleteDocument(ctx context.Context, id string) error
}

// Handler holds dependencies for vendor HTTP handlers.
type Handler struct {
	repo   *Repo
	search searchIndexer
}

func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

func (h *Handler) WithSearchClient(sc *search.Client) *Handler {
	h.search = sc
	return h
}

func (h *Handler) indexVendor(ctx context.Context, v *Vendor) {
	if h.search == nil { return }
	defer func() { if r := recover(); r != nil { slog.Warn("vendor: panic during search indexing", "panic", r) } }()
	body := ""
	if v.Specialty != nil { body = *v.Specialty }
	doc := search.SearchDocument{
		ID: "vendor-" + v.ID.String(), HouseholdID: v.HouseholdID.String(),
		EntityType: "vendor", EntityID: v.ID.String(),
		Title: v.Name, Body: body,
		CreatedAt: v.CreatedAt.Unix(), UpdatedAt: v.UpdatedAt.Unix(),
	}
	if v.PropertyID != nil { pid := v.PropertyID.String(); doc.PropertyID = &pid }
	if err := h.search.IndexDocument(context.Background(), doc); err != nil {
		slog.Warn("vendor: failed to index", "id", v.ID, "error", err)
	}
}

func (h *Handler) deleteVendorIndex(ctx context.Context, id string) {
	if h.search == nil { return }
	defer func() { if r := recover(); r != nil { slog.Warn("vendor: panic during search deletion", "panic", r) } }()
	if err := h.search.DeleteDocument(context.Background(), "vendor-"+id); err != nil {
		slog.Warn("vendor: failed to delete from search", "id", id, "error", err)
	}
}

// --- request / response types ---

type createVendorRequest struct {
	PropertyID *string `json:"property_id"`
	Name       string  `json:"name"`
	Specialty  *string `json:"specialty"`
	Phone      *string `json:"phone"`
	Email      *string `json:"email"`
	Website    *string `json:"website"`
	Notes      *string `json:"notes"`
}

type updateVendorRequest struct {
	PropertyID *string `json:"property_id"`
	Name       string  `json:"name"`
	Specialty  *string `json:"specialty"`
	Phone      *string `json:"phone"`
	Email      *string `json:"email"`
	Website    *string `json:"website"`
	Notes      *string `json:"notes"`
}

type vendorResponse struct {
	ID          string  `json:"id"`
	HouseholdID string  `json:"household_id"`
	PropertyID  *string `json:"property_id,omitempty"`
	Name        string  `json:"name"`
	Specialty   *string `json:"specialty,omitempty"`
	Phone       *string `json:"phone,omitempty"`
	Email       *string `json:"email,omitempty"`
	Website     *string `json:"website,omitempty"`
	Notes       *string `json:"notes,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func toVendorResponse(v *Vendor) vendorResponse {
	var propertyID *string
	if v.PropertyID != nil {
		s := v.PropertyID.String()
		propertyID = &s
	}

	return vendorResponse{
		ID:          v.ID.String(),
		HouseholdID: v.HouseholdID.String(),
		PropertyID:  propertyID,
		Name:        v.Name,
		Specialty:   v.Specialty,
		Phone:       v.Phone,
		Email:       v.Email,
		Website:     v.Website,
		Notes:       v.Notes,
		CreatedAt:   v.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   v.UpdatedAt.Format(time.RFC3339),
	}
}

func toVendorResponses(vendors []*Vendor) []vendorResponse {
	out := make([]vendorResponse, 0, len(vendors))
	for _, v := range vendors {
		out = append(out, toVendorResponse(v))
	}
	return out
}

// householdID extracts the household UUID from the JWT claims in the request context.
func householdID(r *http.Request) (uuid.UUID, error) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		return uuid.Nil, apierr.ErrForbidden
	}
	return uuid.Parse(claims.HouseholdID)
}

// List returns all vendors for the authenticated household.
// GET /api/v1/vendors
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	vendors, err := h.repo.List(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toVendorResponses(vendors),
	})
}

// Get returns a single vendor by ID.
// GET /api/v1/vendors/:id
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid vendor id")
		return
	}

	vendor, err := h.repo.Get(r.Context(), hid, id)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if vendor == nil {
		apierr.NotFound(w, "vendor not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toVendorResponse(vendor),
	})
}

// Create inserts a new vendor for the authenticated household.
// POST /api/v1/vendors
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	var req createVendorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}

	var propertyID *uuid.UUID
	if req.PropertyID != nil && *req.PropertyID != "" {
		pid, err := uuid.Parse(*req.PropertyID)
		if err != nil {
			apierr.BadRequest(w, "invalid property_id")
			return
		}
		propertyID = &pid
	}

	v := &Vendor{
		PropertyID: propertyID,
		Name:       req.Name,
		Specialty:  req.Specialty,
		Phone:      req.Phone,
		Email:      req.Email,
		Website:    req.Website,
		Notes:      req.Notes,
	}

	created, err := h.repo.Create(r.Context(), hid, v)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	h.indexVendor(r.Context(), created)

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": toVendorResponse(created),
	})
}

// Update modifies an existing vendor.
// PUT /api/v1/vendors/:id
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid vendor id")
		return
	}

	var req updateVendorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}

	var propertyID *uuid.UUID
	if req.PropertyID != nil && *req.PropertyID != "" {
		pid, err := uuid.Parse(*req.PropertyID)
		if err != nil {
			apierr.BadRequest(w, "invalid property_id")
			return
		}
		propertyID = &pid
	}

	v := &Vendor{
		PropertyID: propertyID,
		Name:       req.Name,
		Specialty:  req.Specialty,
		Phone:      req.Phone,
		Email:      req.Email,
		Website:    req.Website,
		Notes:      req.Notes,
	}

	updated, err := h.repo.Update(r.Context(), hid, id, v)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if updated == nil {
		apierr.NotFound(w, "vendor not found")
		return
	}

	h.indexVendor(r.Context(), updated)

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toVendorResponse(updated),
	})
}

// Delete removes a vendor.
// DELETE /api/v1/vendors/:id
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid vendor id")
		return
	}

	if err := h.repo.Delete(r.Context(), hid, id); err != nil {
		apierr.NotFound(w, "vendor not found")
		return
	}

	h.deleteVendorIndex(r.Context(), id.String())

	w.WriteHeader(http.StatusNoContent)
}
