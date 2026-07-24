package property

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

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

// Handler holds the dependencies needed by property HTTP handlers.
type Handler struct {
	repo   *Repo
	search searchIndexer
}

// NewHandler creates a new property handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

func (h *Handler) WithSearchClient(sc *search.Client) *Handler {
	h.search = sc
	return h
}

func (h *Handler) indexProperty(ctx context.Context, p *Property) {
	if h.search == nil { return }
	defer func() { if r := recover(); r != nil { slog.Warn("property: panic during search indexing", "panic", r) } }()
	body := ""
	if p.Address != nil { body = *p.Address }
	if p.PropertyType != nil { body += " " + *p.PropertyType }
	if p.Notes != nil { body += " " + *p.Notes }
	doc := search.SearchDocument{
		ID: "property-" + p.ID.String(), HouseholdID: p.HouseholdID.String(),
		EntityType: "property", EntityID: p.ID.String(),
		Title: p.Name, Body: body,
		CreatedAt: p.CreatedAt.Unix(), UpdatedAt: p.UpdatedAt.Unix(),
	}
	if err := h.search.IndexDocument(context.Background(), doc); err != nil {
		slog.Warn("property: failed to index", "id", p.ID, "error", err)
	}
}

func (h *Handler) deletePropertyIndex(ctx context.Context, id string) {
	if h.search == nil { return }
	defer func() { if r := recover(); r != nil { slog.Warn("property: panic during search deletion", "panic", r) } }()
	if err := h.search.DeleteDocument(context.Background(), "property-"+id); err != nil {
		slog.Warn("property: failed to delete from search", "id", id, "error", err)
	}
}

// --- request / response types ---

// propertyRequest is the JSON body for create and update property requests.
// All fields are optional except name. Nullable numeric/date fields use *string
// to match the Property model (numeric values serialize as strings to avoid
// float precision loss; null in JSON = NULL in DB).
type propertyRequest struct {
	Name                  *string `json:"name"`
	Address               *string `json:"address"`
	PropertyType          *string `json:"property_type"`
	Notes                 *string `json:"notes"`
	PurchasePrice         *string `json:"purchase_price"`
	PurchaseDate          *string `json:"purchase_date"`
	CurrentValue          *string `json:"current_value"`
	DownPayment           *string `json:"down_payment"`
	MortgageAmount        *string `json:"mortgage_amount"`
	MortgageRate          *string `json:"mortgage_rate"`
	MortgageTermMonths    *string `json:"mortgage_term_months"`
	MortgageStartDate     *string `json:"mortgage_start_date"`
	MortgageLender        *string `json:"mortgage_lender"`
	MortgageAccountNumber *string `json:"mortgage_account_number"`
	PropertyTaxAnnual     *string `json:"property_tax_annual"`
	PropertyTaxDueMonths  *string `json:"property_tax_due_months"`
	InsuranceAnnual       *string `json:"insurance_annual"`
	InsuranceProvider     *string `json:"insurance_provider"`
	HoaFeeMonthly         *string `json:"hoa_fee_monthly"`
}

// toProperty builds a *Property from a propertyRequest, copying every field.
// Name is optional on update, so a nil req.Name yields "" which the repo's
// UpdateProperty treats as "leave unchanged" via COALESCE(NULLIF(...), name).
// CreateProperty handlers validate name is non-empty before calling this.
func (req *propertyRequest) toProperty() *Property {
	name := ""
	if req.Name != nil {
		name = *req.Name
	}
	return &Property{
		Name:                  name,
		Address:               req.Address,
		PropertyType:          req.PropertyType,
		Notes:                 req.Notes,
		PurchasePrice:         req.PurchasePrice,
		PurchaseDate:          req.PurchaseDate,
		CurrentValue:          req.CurrentValue,
		DownPayment:           req.DownPayment,
		MortgageAmount:        req.MortgageAmount,
		MortgageRate:          req.MortgageRate,
		MortgageTermMonths:    req.MortgageTermMonths,
		MortgageStartDate:     req.MortgageStartDate,
		MortgageLender:        req.MortgageLender,
		MortgageAccountNumber: req.MortgageAccountNumber,
		PropertyTaxAnnual:     req.PropertyTaxAnnual,
		PropertyTaxDueMonths:  req.PropertyTaxDueMonths,
		InsuranceAnnual:       req.InsuranceAnnual,
		InsuranceProvider:     req.InsuranceProvider,
		HOAFeeMonthly:         req.HoaFeeMonthly,
	}
}

type propertyResponse struct {
	ID           string  `json:"id"`
	HouseholdID  string  `json:"household_id"`
	Name         string  `json:"name"`
	Address      *string `json:"address,omitempty"`
	PropertyType *string `json:"property_type,omitempty"`
	Notes        *string `json:"notes,omitempty"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`

	// Financial fields. Carried as *string (no omitempty) so NULL in the DB
	// serializes as JSON null, matching the TS `string | null` type exactly.
	PurchasePrice         *string `json:"purchase_price"`
	PurchaseDate          *string `json:"purchase_date"`
	CurrentValue          *string `json:"current_value"`
	DownPayment           *string `json:"down_payment"`
	MortgageAmount        *string `json:"mortgage_amount"`
	MortgageRate          *string `json:"mortgage_rate"`
	MortgageTermMonths    *string `json:"mortgage_term_months"`
	MortgageStartDate     *string `json:"mortgage_start_date"`
	MortgageLender        *string `json:"mortgage_lender"`
	MortgageAccountNumber *string `json:"mortgage_account_number"`
	PropertyTaxAnnual     *string `json:"property_tax_annual"`
	PropertyTaxDueMonths  *string `json:"property_tax_due_months"`
	InsuranceAnnual       *string `json:"insurance_annual"`
	InsuranceProvider     *string `json:"insurance_provider"`
	HOAFeeMonthly         *string `json:"hoa_fee_monthly"`
}

type roomRequest struct {
	Name  *string `json:"name"`
	Floor *int    `json:"floor"`
	Notes *string `json:"notes"`
}

type roomResponse struct {
	ID         string  `json:"id"`
	PropertyID string  `json:"property_id"`
	Name       string  `json:"name"`
	Floor      *int    `json:"floor,omitempty"`
	Notes      *string `json:"notes,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

// --- helpers ---

// householdID extracts the household UUID from JWT claims injected by the
// RequireAuth middleware. Writes a 401 and returns uuid.Nil if not found.
func householdID(w http.ResponseWriter, r *http.Request) uuid.UUID {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{
				Code:    "UNAUTHORIZED",
				Message: "missing claims",
			},
		})
		return uuid.Nil
	}

	id, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{
				Code:    "UNAUTHORIZED",
				Message: "invalid household_id in token",
			},
		})
		return uuid.Nil
	}
	return id
}

func toPropertyResponse(p *Property) propertyResponse {
	return propertyResponse{
		ID:           p.ID.String(),
		HouseholdID:  p.HouseholdID.String(),
		Name:         p.Name,
		Address:      p.Address,
		PropertyType: p.PropertyType,
		Notes:        p.Notes,
		CreatedAt:    p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:    p.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),

		PurchasePrice:         p.PurchasePrice,
		PurchaseDate:          p.PurchaseDate,
		CurrentValue:          p.CurrentValue,
		DownPayment:           p.DownPayment,
		MortgageAmount:        p.MortgageAmount,
		MortgageRate:          p.MortgageRate,
		MortgageTermMonths:    p.MortgageTermMonths,
		MortgageStartDate:     p.MortgageStartDate,
		MortgageLender:        p.MortgageLender,
		MortgageAccountNumber: p.MortgageAccountNumber,
		PropertyTaxAnnual:     p.PropertyTaxAnnual,
		PropertyTaxDueMonths:  p.PropertyTaxDueMonths,
		InsuranceAnnual:       p.InsuranceAnnual,
		InsuranceProvider:     p.InsuranceProvider,
		HOAFeeMonthly:         p.HOAFeeMonthly,
	}
}

func toRoomResponse(r *Room) roomResponse {
	return roomResponse{
		ID:         r.ID.String(),
		PropertyID: r.PropertyID.String(),
		Name:       r.Name,
		Floor:      r.Floor,
		Notes:      r.Notes,
		CreatedAt:  r.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// --- property handlers ---

// ListProperties handles GET /api/v1/properties.
func (h *Handler) ListProperties(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}

	properties, err := h.repo.ListProperties(r.Context(), hhID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	resp := make([]propertyResponse, len(properties))
	for i, p := range properties {
		resp[i] = toPropertyResponse(p)
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": resp,
	})
}

// CreateProperty handles POST /api/v1/properties.
func (h *Handler) CreateProperty(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}

	var req propertyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Name == nil || *req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}

	property, err := h.repo.CreateProperty(r.Context(), hhID, req.toProperty())
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	h.indexProperty(r.Context(), property)

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": toPropertyResponse(property),
	})
}

// GetProperty handles GET /api/v1/properties/{id}.
func (h *Handler) GetProperty(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}

	propertyID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid property id")
		return
	}

	property, err := h.repo.GetProperty(r.Context(), propertyID, hhID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if property == nil {
		apierr.NotFound(w, "property not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toPropertyResponse(property),
	})
}

// UpdateProperty handles PUT /api/v1/properties/{id}.
func (h *Handler) UpdateProperty(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}

	propertyID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid property id")
		return
	}

	var req propertyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	property, err := h.repo.UpdateProperty(r.Context(), propertyID, hhID, req.toProperty())
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if property == nil {
		apierr.NotFound(w, "property not found")
		return
	}

	h.indexProperty(r.Context(), property)

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toPropertyResponse(property),
	})
}

// DeleteProperty handles DELETE /api/v1/properties/{id}.
func (h *Handler) DeleteProperty(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}

	propertyID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid property id")
		return
	}

	deleted, err := h.repo.DeleteProperty(r.Context(), propertyID, hhID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if !deleted {
		apierr.NotFound(w, "property not found")
		return
	}

	h.deletePropertyIndex(r.Context(), propertyID.String())

	w.WriteHeader(http.StatusNoContent)
}

// --- room handlers ---

// ListRooms handles GET /api/v1/properties/{id}/rooms.
func (h *Handler) ListRooms(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}

	propertyID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid property id")
		return
	}

	// Verify the property exists and belongs to this household.
	property, err := h.repo.GetProperty(r.Context(), propertyID, hhID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if property == nil {
		apierr.NotFound(w, "property not found")
		return
	}

	rooms, err := h.repo.ListRooms(r.Context(), propertyID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	resp := make([]roomResponse, len(rooms))
	for i, rm := range rooms {
		resp[i] = toRoomResponse(rm)
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": resp,
	})
}

// CreateRoom handles POST /api/v1/properties/{id}/rooms.
func (h *Handler) CreateRoom(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}

	propertyID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid property id")
		return
	}

	// Verify the property exists and belongs to this household.
	property, err := h.repo.GetProperty(r.Context(), propertyID, hhID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if property == nil {
		apierr.NotFound(w, "property not found")
		return
	}

	var req roomRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Name == nil || *req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}

	room, err := h.repo.CreateRoom(r.Context(), propertyID, *req.Name, req.Floor, req.Notes)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": toRoomResponse(room),
	})
}
