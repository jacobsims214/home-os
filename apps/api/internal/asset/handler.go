package asset

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/config"
	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// Handler holds the dependencies needed by asset HTTP handlers.
type Handler struct {
	repo *Repo
	cfg  *config.Config
}

// NewHandler creates a new asset handler.
func NewHandler(repo *Repo, cfg *config.Config) *Handler {
	return &Handler{repo: repo, cfg: cfg}
}

// --- request / response types ---

type createAssetRequest struct {
	Name           string  `json:"name"`
	PropertyID     *string `json:"property_id"`
	RoomID         *string `json:"room_id"`
	Category       *string `json:"category"`
	Manufacturer   *string `json:"manufacturer"`
	Model          *string `json:"model"`
	SerialNumber   *string `json:"serial_number"`
	PurchaseDate   *string `json:"purchase_date"`
	PurchasePrice  *string `json:"purchase_price"`
	WarrantyExpiry *string `json:"warranty_expiry"`
	Notes          *string `json:"notes"`
}

type updateAssetRequest struct {
	Name           *string `json:"name"`
	PropertyID     *string `json:"property_id"`
	RoomID         *string `json:"room_id"`
	Category       *string `json:"category"`
	Manufacturer   *string `json:"manufacturer"`
	Model          *string `json:"model"`
	SerialNumber   *string `json:"serial_number"`
	PurchaseDate   *string `json:"purchase_date"`
	PurchasePrice  *string `json:"purchase_price"`
	WarrantyExpiry *string `json:"warranty_expiry"`
	Notes          *string `json:"notes"`
}

type assetResponse struct {
	ID             string  `json:"id"`
	HouseholdID    string  `json:"household_id"`
	PropertyID     *string `json:"property_id,omitempty"`
	RoomID         *string `json:"room_id,omitempty"`
	Name           string  `json:"name"`
	Category       *string `json:"category,omitempty"`
	Manufacturer   *string `json:"manufacturer,omitempty"`
	Model          *string `json:"model,omitempty"`
	SerialNumber   *string `json:"serial_number,omitempty"`
	PurchaseDate   *string `json:"purchase_date,omitempty"`
	PurchasePrice  *string `json:"purchase_price,omitempty"`
	WarrantyExpiry *string `json:"warranty_expiry,omitempty"`
	Notes          *string `json:"notes,omitempty"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

// List returns all assets for the authenticated household.
// GET /api/v1/assets
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "no claims in context")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	var propertyID *uuid.UUID
	if pidStr := r.URL.Query().Get("property_id"); pidStr != "" {
		pid, err := uuid.Parse(pidStr)
		if err != nil {
			apierr.BadRequest(w, "invalid property_id")
			return
		}
		propertyID = &pid
	}

	assets, err := h.repo.List(r.Context(), householdID, propertyID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	resp := make([]assetResponse, 0, len(assets))
	for _, a := range assets {
		resp = append(resp, assetToResponse(a))
	}

	apierr.JSON(w, http.StatusOK, map[string]any{"data": resp})
}

// Create inserts a new asset and returns it with a 201 status.
// POST /api/v1/assets
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "no claims in context")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	var req createAssetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}

	asset := &Asset{
		HouseholdID:   householdID,
		Name:          req.Name,
		PropertyID:    parseUUIDPtr(req.PropertyID),
		RoomID:        parseUUIDPtr(req.RoomID),
		Category:      req.Category,
		Manufacturer:  req.Manufacturer,
		Model:         req.Model,
		SerialNumber:  req.SerialNumber,
		PurchaseDate:  parseDatePtr(req.PurchaseDate),
		PurchasePrice: parseFloatPtr(req.PurchasePrice),
		WarrantyExpiry: parseDatePtr(req.WarrantyExpiry),
		Notes:         req.Notes,
	}

	created, err := h.repo.Create(r.Context(), asset)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusCreated, assetToResponse(created))
}

// Get returns a single asset by ID, scoped to the authenticated household.
// GET /api/v1/assets/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "no claims in context")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	assetID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid asset id")
		return
	}

	asset, err := h.repo.Get(r.Context(), assetID, householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if asset == nil {
		apierr.NotFound(w, "asset not found")
		return
	}

	apierr.JSON(w, http.StatusOK, assetToResponse(asset))
}

// Update modifies an existing asset, scoped to the authenticated household.
// PUT /api/v1/assets/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "no claims in context")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	assetID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid asset id")
		return
	}

	var req updateAssetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	// Build partial update — only fields that are present in the request.
	updates := &Asset{
		PropertyID:    parseUUIDPtr(req.PropertyID),
		RoomID:        parseUUIDPtr(req.RoomID),
		Category:      req.Category,
		Manufacturer:  req.Manufacturer,
		Model:         req.Model,
		SerialNumber:  req.SerialNumber,
		PurchaseDate:  parseDatePtr(req.PurchaseDate),
		PurchasePrice: parseFloatPtr(req.PurchasePrice),
		WarrantyExpiry: parseDatePtr(req.WarrantyExpiry),
		Notes:         req.Notes,
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			apierr.BadRequest(w, "name cannot be empty")
			return
		}
		updates.Name = trimmed
	}

	updated, err := h.repo.Update(r.Context(), assetID, householdID, updates)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusOK, assetToResponse(updated))
}

// Delete removes an asset by ID, scoped to the authenticated household.
// DELETE /api/v1/assets/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "no claims in context")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	assetID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid asset id")
		return
	}

	if err := h.repo.Delete(r.Context(), assetID, householdID); err != nil {
		apierr.InternalError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

func assetToResponse(a *Asset) assetResponse {
	return assetResponse{
		ID:             a.ID.String(),
		HouseholdID:    a.HouseholdID.String(),
		PropertyID:     uuidPtrToString(a.PropertyID),
		RoomID:         uuidPtrToString(a.RoomID),
		Name:           a.Name,
		Category:       a.Category,
		Manufacturer:   a.Manufacturer,
		Model:          a.Model,
		SerialNumber:   a.SerialNumber,
		PurchaseDate:   datePtrToString(a.PurchaseDate),
		PurchasePrice:  floatPtrToString(a.PurchasePrice),
		WarrantyExpiry: datePtrToString(a.WarrantyExpiry),
		Notes:          a.Notes,
		CreatedAt:      a.CreatedAt.Format(time.RFC3339),
		UpdatedAt:      a.UpdatedAt.Format(time.RFC3339),
	}
}

func parseUUIDPtr(s *string) *uuid.UUID {
	if s == nil || *s == "" {
		return nil
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil
	}
	return &id
}

func parseDatePtr(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", *s)
	if err != nil {
		return nil
	}
	return &t
}

func uuidPtrToString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func datePtrToString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format("2006-01-02")
	return &s
}

func parseFloatPtr(s *string) *float64 {
	if s == nil || *s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(*s), 64)
	if err != nil {
		return nil
	}
	return &f
}

func floatPtrToString(f *float64) *string {
	if f == nil {
		return nil
	}
	s := strconv.FormatFloat(*f, 'f', 2, 64)
	return &s
}
