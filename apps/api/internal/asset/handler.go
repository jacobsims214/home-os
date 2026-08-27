package asset

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/config"
	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
	"home-os/api/pkg/apierr"
)

type searchIndexer interface {
	IndexDocument(ctx context.Context, doc search.SearchDocument) error
	DeleteDocument(ctx context.Context, id string) error
}

// Handler holds the dependencies needed by asset HTTP handlers.
type Handler struct {
	repo   *Repo
	cfg    *config.Config
	search searchIndexer
}

// NewHandler creates a new asset handler.
func NewHandler(repo *Repo, cfg *config.Config) *Handler {
	return &Handler{repo: repo, cfg: cfg}
}

func (h *Handler) WithSearchClient(sc *search.Client) *Handler {
	h.search = sc
	return h
}

func (h *Handler) indexAsset(ctx context.Context, a *Asset) {
	if h.search == nil { return }
	defer func() { if r := recover(); r != nil { slog.Warn("asset: panic during search indexing", "panic", r) } }()
	body := ""
	if a.Category != nil { body = *a.Category }
	if a.Manufacturer != nil { body += " " + *a.Manufacturer }
	if a.Model != nil { body += " " + *a.Model }
	if a.SerialNumber != nil { body += " " + *a.SerialNumber }
	doc := search.SearchDocument{
		ID: "asset-" + a.ID.String(), HouseholdID: a.HouseholdID.String(),
		EntityType: "asset", EntityID: a.ID.String(),
		Title: a.Name, Body: body,
		CreatedAt: a.CreatedAt.Unix(), UpdatedAt: a.UpdatedAt.Unix(),
	}
	if a.PropertyID != nil { pid := a.PropertyID.String(); doc.PropertyID = &pid }
	if err := h.search.IndexDocument(context.Background(), doc); err != nil {
		slog.Warn("asset: failed to index", "id", a.ID, "error", err)
	}
}

func (h *Handler) deleteAssetIndex(ctx context.Context, id string) {
	if h.search == nil { return }
	defer func() { if r := recover(); r != nil { slog.Warn("asset: panic during search deletion", "panic", r) } }()
	if err := h.search.DeleteDocument(context.Background(), "asset-"+id); err != nil {
		slog.Warn("asset: failed to delete from search", "id", id, "error", err)
	}
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
	CurrentValue   *string `json:"current_value"`
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
	CurrentValue   *string `json:"current_value"`
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
	CurrentValue   *string `json:"current_value"`
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
		CurrentValue:  parseFloatPtr(req.CurrentValue),
		WarrantyExpiry: parseDatePtr(req.WarrantyExpiry),
		Notes:         req.Notes,
	}

	created, err := h.repo.Create(r.Context(), asset)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	h.indexAsset(r.Context(), created)

	apierr.JSON(w, http.StatusCreated, map[string]any{"data": assetToResponse(created)})
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

	apierr.JSON(w, http.StatusOK, map[string]any{"data": assetToResponse(asset)})
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
		CurrentValue:  parseFloatPtr(req.CurrentValue),
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

	h.indexAsset(r.Context(), updated)

	apierr.JSON(w, http.StatusOK, map[string]any{"data": assetToResponse(updated)})
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

	h.deleteAssetIndex(r.Context(), assetID.String())

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
		CurrentValue:   floatPtrToString(a.CurrentValue),
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
