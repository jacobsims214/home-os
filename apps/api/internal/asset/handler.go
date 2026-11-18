package asset

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"home-os/api/internal/auth"
	"home-os/api/internal/config"
	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
	"home-os/api/internal/searchqueue"
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
	// Delegates to the one document builder (#5314). searchqueue.Document
	// reads the row and produces the same document the index queue's drain
	// writes, so a write made here and the same write made through an MCP tool
	// cannot index differently — and there is no second copy of the document
	// rules to drift. A failure is only logged: the trigger already queued the
	// row, so the drain retries it.
	if h.search == nil {
		return
	}
	if err := searchqueue.IndexNow(ctx, h.repo.pool, h.search, "asset", a.ID.String()); err != nil {
		slog.Warn("asset: indexed later by the queue", "id", a.ID, "error", err)
	}
}

func (h *Handler) deleteAssetIndex(ctx context.Context, id string) {
	if h.search == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("asset: panic during search deletion", "panic", r)
		}
	}()
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

// assetMoney is every money field of an asset response, in one struct, so that
// hiding it from a role is one decision (assetToResponse) rather than a check
// per field or per handler: the #4806 pattern, applied to the asset half of
// the money policy (#4912). It is embedded in assetResponse, so the fields
// still serialize at the top level next to name and category and the JSON
// shape stays the shape the UI already types.
//
// ResAssets read is rolesEveryone, so a housesitter and a vendor may list the
// family's things. Until #4912 the same response also told them what each one
// cost and is worth, which is money they cannot open the bills or loans for: a
// side door around the money policy. A field added to this struct is hidden
// from those roles by default, which is why the money lives here and nowhere
// else in the response.
//
// The JSON tags keep this endpoint's existing null-vs-absent contract exactly
// as it was (decisions/2026-11-16-money-redaction.md: "the JSON shape is
// unchanged"): purchase_date and purchase_price are omitempty and were already
// left out when null, current_value has no omitempty and was already present as
// null. A role without money therefore sees the same shape any role sees for an
// asset with no such value, and no existing client changes.
//
// This is deliberately not the MCP shape. There (#4808) hidden money is absent
// rather than null, because a model reads that JSON and "purchase_price": null
// is a value it can quote; here the API keeps null for the UI's string | null.
// The policy is the same one (auth.CanSeeMoney); only the serialization
// convention differs.
type assetMoney struct {
	PurchaseDate  *string `json:"purchase_date,omitempty"`
	PurchasePrice *string `json:"purchase_price,omitempty"`
	CurrentValue  *string `json:"current_value"`
}

// assetResponse is an asset as a caller may see it: the record itself, plus the
// embedded money fields assetToResponse fills only for a role with money read.
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
	WarrantyExpiry *string `json:"warranty_expiry,omitempty"`
	Notes          *string `json:"notes,omitempty"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`

	assetMoney
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

	role := middleware.Role(r)
	resp := make([]assetResponse, 0, len(assets))
	for _, a := range assets {
		resp = append(resp, assetToResponse(a, role))
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
		HouseholdID:    householdID,
		Name:           req.Name,
		PropertyID:     parseUUIDPtr(req.PropertyID),
		RoomID:         parseUUIDPtr(req.RoomID),
		Category:       req.Category,
		Manufacturer:   req.Manufacturer,
		Model:          req.Model,
		SerialNumber:   req.SerialNumber,
		PurchaseDate:   parseDatePtr(req.PurchaseDate),
		PurchasePrice:  parseFloatPtr(req.PurchasePrice),
		CurrentValue:   parseFloatPtr(req.CurrentValue),
		WarrantyExpiry: parseDatePtr(req.WarrantyExpiry),
		Notes:          req.Notes,
	}

	// Property is optional (a family-level asset has neither property nor
	// room). With a room, the database derives property_id from it; another
	// family's room or property is a foreign_key_violation → 404.
	created, err := h.repo.Create(r.Context(), asset)
	if err != nil {
		apierr.DB(w, err)
		return
	}

	h.indexAsset(r.Context(), created)

	apierr.JSON(w, http.StatusCreated, map[string]any{"data": assetToResponse(created, middleware.Role(r))})
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

	apierr.JSON(w, http.StatusOK, map[string]any{"data": assetToResponse(asset, middleware.Role(r))})
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
	// An empty property_id or room_id clears it (uuid.Nil marks the clear).
	updates := &Asset{
		PropertyID:     parseUUIDOrClear(req.PropertyID),
		RoomID:         parseUUIDOrClear(req.RoomID),
		Category:       req.Category,
		Manufacturer:   req.Manufacturer,
		Model:          req.Model,
		SerialNumber:   req.SerialNumber,
		PurchaseDate:   parseDatePtr(req.PurchaseDate),
		PurchasePrice:  parseFloatPtr(req.PurchasePrice),
		CurrentValue:   parseFloatPtr(req.CurrentValue),
		WarrantyExpiry: parseDatePtr(req.WarrantyExpiry),
		Notes:          req.Notes,
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
		if errors.Is(err, pgx.ErrNoRows) {
			apierr.NotFound(w, "asset not found")
			return
		}
		apierr.DB(w, err)
		return
	}

	h.indexAsset(r.Context(), updated)

	apierr.JSON(w, http.StatusOK, map[string]any{"data": assetToResponse(updated, middleware.Role(r))})
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

// assetToResponse is the one place an asset becomes JSON, and therefore the one
// place the caller's role decides which fields it carries. Money is filled only
// when auth.CanSeeMoney(role); otherwise assetMoney stays zero and the amount
// fields are null or absent. Every handler that returns an asset goes through
// here, so a new endpoint cannot forget, and a money field added to assetMoney
// is hidden by default (#4806, #4912).
//
// The repo still selects purchase_date, purchase_price and current_value for
// every caller. Unlike a property's mortgage (#4806) these are columns of the
// asset row itself, not values derived from loans or bills, so there is no join
// to skip and nothing to gain from a second SQL form: one decision, here, is
// what keeps a new caller from leaking.
func assetToResponse(a *Asset, role string) assetResponse {
	resp := assetResponse{
		ID:             a.ID.String(),
		HouseholdID:    a.HouseholdID.String(),
		PropertyID:     uuidPtrToString(a.PropertyID),
		RoomID:         uuidPtrToString(a.RoomID),
		Name:           a.Name,
		Category:       a.Category,
		Manufacturer:   a.Manufacturer,
		Model:          a.Model,
		SerialNumber:   a.SerialNumber,
		WarrantyExpiry: datePtrToString(a.WarrantyExpiry),
		Notes:          a.Notes,
		CreatedAt:      a.CreatedAt.Format(time.RFC3339),
		UpdatedAt:      a.UpdatedAt.Format(time.RFC3339),
	}
	if auth.CanSeeMoney(role) {
		resp.assetMoney = assetMoney{
			PurchaseDate:  datePtrToString(a.PurchaseDate),
			PurchasePrice: floatPtrToString(a.PurchasePrice),
			CurrentValue:  floatPtrToString(a.CurrentValue),
		}
	}
	return resp
}

// parseUUIDOrClear is parseUUIDPtr for partial updates: an empty string
// returns a pointer to uuid.Nil, which the repo treats as "set to NULL".
func parseUUIDOrClear(s *string) *uuid.UUID {
	if s != nil && *s == "" {
		return &uuid.Nil
	}
	return parseUUIDPtr(s)
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
