package vendors

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

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
	if h.search == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("vendor: panic during search indexing", "panic", r)
		}
	}()
	parts := make([]string, 0, len(v.Trades)+1)
	if v.Specialty != nil && *v.Specialty != "" {
		parts = append(parts, *v.Specialty)
	}
	for _, t := range v.Trades {
		parts = append(parts, strings.ReplaceAll(t, "_", " "))
	}
	body := strings.Join(parts, " ")
	doc := search.SearchDocument{
		ID: "vendor-" + v.ID.String(), HouseholdID: v.HouseholdID.String(),
		EntityType: "vendor", EntityID: v.ID.String(),
		Title: v.Name, Body: body,
		CreatedAt: v.CreatedAt.Unix(), UpdatedAt: v.UpdatedAt.Unix(),
	}
	if err := h.search.IndexDocument(context.Background(), doc); err != nil {
		slog.Warn("vendor: failed to index", "id", v.ID, "error", err)
	}
}

func (h *Handler) deleteVendorIndex(ctx context.Context, id string) {
	if h.search == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("vendor: panic during search deletion", "panic", r)
		}
	}()
	if err := h.search.DeleteDocument(context.Background(), "vendor-"+id); err != nil {
		slog.Warn("vendor: failed to delete from search", "id", id, "error", err)
	}
}

// --- request / response types ---

type createVendorRequest struct {
	Name      string   `json:"name"`
	Trades    []string `json:"trades"`
	Specialty *string  `json:"specialty"`
	Phone     *string  `json:"phone"`
	Email     *string  `json:"email"`
	Website   *string  `json:"website"`
	Address   *string  `json:"address"`
	Notes     *string  `json:"notes"`
}

// updateVendorRequest is a full replace, except trades and address: omitted
// (or null) trades keep the stored trades, [] clears them; an omitted
// address keeps the stored one, null or "" clears it.
type updateVendorRequest struct {
	Name      string    `json:"name"`
	Trades    *[]string `json:"trades"`
	Specialty *string   `json:"specialty"`
	Phone     *string   `json:"phone"`
	Email     *string   `json:"email"`
	Website   *string   `json:"website"`
	Address   optString `json:"address"`
	Notes     *string   `json:"notes"`
}

type vendorResponse struct {
	ID          string   `json:"id"`
	HouseholdID string   `json:"household_id"`
	Name        string   `json:"name"`
	Trades      []string `json:"trades"`
	Specialty   *string  `json:"specialty,omitempty"`
	Phone       *string  `json:"phone,omitempty"`
	Email       *string  `json:"email,omitempty"`
	Website     *string  `json:"website,omitempty"`
	Address     *string  `json:"address,omitempty"`
	Notes       *string  `json:"notes,omitempty"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

func toVendorResponse(v *Vendor) vendorResponse {
	return vendorResponse{
		ID:          v.ID.String(),
		HouseholdID: v.HouseholdID.String(),
		Name:        v.Name,
		Trades:      nonNil(v.Trades),
		Specialty:   v.Specialty,
		Phone:       v.Phone,
		Email:       v.Email,
		Website:     v.Website,
		Address:     v.Address,
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

// List returns the vendors for the authenticated household.
// GET /api/v1/vendors[?trade=plumber]
// trade is normalized like stored trades ("Plumbing" finds plumber) and
// matched exactly; blank means no filter.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	trade := NormalizeTrade(r.URL.Query().Get("trade"))
	if len(trade) > maxTradeLen {
		apierr.BadRequest(w, "trade is too long")
		return
	}

	vendors, err := h.repo.List(r.Context(), hid, trade)
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

	trades, msg := normalizeTrades(req.Trades)
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	address, msg := normalizeAddress(req.Address)
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}

	v := &Vendor{
		Name:      req.Name,
		Trades:    trades,
		Specialty: req.Specialty,
		Phone:     req.Phone,
		Email:     req.Email,
		Website:   req.Website,
		Address:   address,
		Notes:     req.Notes,
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

	address, msg := normalizeAddress(req.Address.Val)
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}

	v := &Vendor{
		Name:      req.Name,
		Specialty: req.Specialty,
		Phone:     req.Phone,
		Email:     req.Email,
		Website:   req.Website,
		Address:   address,
		Notes:     req.Notes,
	}
	if req.Trades != nil {
		trades, msg := normalizeTrades(*req.Trades)
		if msg != "" {
			apierr.BadRequest(w, msg)
			return
		}
		v.Trades = trades // non-nil: replaces the stored trades
	}

	updated, err := h.repo.Update(r.Context(), hid, id, v, !req.Address.Set)
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

// --- address ---

// maxAddressLen matches the MCP contact tools' limit.
const maxAddressLen = 500

// optString is a JSON string that remembers whether it was sent at all, so
// PUT can tell an omitted field (keep) from null (clear).
type optString struct {
	Set bool
	Val *string
}

// UnmarshalJSON runs for any value present in the body, null included.
func (o *optString) UnmarshalJSON(b []byte) error {
	o.Set = true
	o.Val = nil
	if string(b) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	o.Val = &s
	return nil
}

// normalizeAddress keeps an address on one line: whitespace runs (newlines
// included) become one space and the ends are trimmed. Blank means no
// address (nil). It returns a client error message when it is too long.
func normalizeAddress(in *string) (*string, string) {
	if in == nil {
		return nil, ""
	}
	a := strings.Join(strings.Fields(*in), " ")
	if a == "" {
		return nil, ""
	}
	if utf8.RuneCountInString(a) > maxAddressLen {
		return nil, "address must be at most 500 characters"
	}
	return &a, ""
}

// --- trades ---

const (
	maxTrades   = 20
	maxTradeLen = 64
)

// tradeSynonyms maps common words for a trade to the stored trade: the
// vocabulary 043 backfilled from specialty. Keep in step with
// apps/mcp/internal/vendors/contact.go so the API and MCP agree on what
// "plumbing" or "veterinarian" means.
var tradeSynonyms = map[string]string{
	"heating": "hvac", "heating_&_cooling": "hvac", "heating_and_cooling": "hvac", "ac": "hvac",
	"plumbing":   "plumber",
	"electrical": "electrician", "electric": "electrician",
	"landscaping": "landscaper", "lawn_care": "landscaper", "lawn": "landscaper", "gardener": "landscaper",
	"roofing":      "roofer",
	"exterminator": "pest_control", "pest": "pest_control",
	"cleaning": "cleaner", "house_cleaning": "cleaner", "housekeeper": "cleaner",
	"pool_service": "pool",
	"auto_repair":  "mechanic",
	"veterinarian": "vet", "veterinary": "vet", "animal_hospital": "vet",
	"pediatrics": "pediatrician",
	"physician":  "doctor",
	"dental":     "dentist",
}

// NormalizeTrade turns user text into the stored trade form: lower case,
// runs of spaces, hyphens and underscores as one underscore, synonyms mapped
// ("Pest Control" -> pest_control, "Plumbing" -> plumber). Blank stays blank.
func NormalizeTrade(s string) string {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.Join(strings.FieldsFunc(t, func(r rune) bool { return r == ' ' || r == '-' || r == '_' }), "_")
	if mapped, ok := tradeSynonyms[t]; ok {
		return mapped
	}
	return t
}

// normalizeTrades normalizes and de-duplicates trades, keeping first-seen
// order. It returns a non-nil slice, or a client error message for a blank
// or over-long trade or too many trades.
func normalizeTrades(in []string) ([]string, string) {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		t := NormalizeTrade(raw)
		switch {
		case t == "":
			return nil, "trades must not contain blank values"
		case len(t) > maxTradeLen:
			return nil, "each trade must be at most 64 characters"
		case seen[t]:
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > maxTrades {
		return nil, "at most 20 trades per vendor"
	}
	return out, ""
}
