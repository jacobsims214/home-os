package property

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/auth"
	"home-os/api/internal/legacyfinance"
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
	if h.search == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("property: panic during search indexing", "panic", r)
		}
	}()
	body := ""
	if p.Address != nil {
		body = *p.Address
	}
	if p.PropertyType != nil {
		body += " " + *p.PropertyType
	}
	if p.Notes != nil {
		body += " " + *p.Notes
	}
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
	if h.search == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("property: panic during search deletion", "panic", r)
		}
	}()
	if err := h.search.DeleteDocument(context.Background(), "property-"+id); err != nil {
		slog.Warn("property: failed to delete from search", "id", id, "error", err)
	}
}

// --- request / response types ---

// propertyRequest is the JSON body for create and update property requests.
// The mortgage_*, property_tax_*, insurance_* and hoa_fee_monthly fields are
// legacy (#4704): they are applied to the property's loan and bills, never
// stored on the property row.
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

// toProperty builds a *Property from a propertyRequest with the columns of
// the properties row. The legacy finance fields are not properties columns;
// finance() maps them onto the property's loan and bills.
// Name is optional on update, so a nil req.Name yields "" which the repo's
// UpdateProperty treats as "leave unchanged" via COALESCE(NULLIF(...), name).
// CreateProperty handlers validate name is non-empty before calling this.
func (req *propertyRequest) toProperty() *Property {
	name := ""
	if req.Name != nil {
		name = *req.Name
	}
	return &Property{
		Name:         name,
		Address:      req.Address,
		PropertyType: req.PropertyType,
		Notes:        req.Notes,
		// The property edit page sends "" for a blank field; a NUMERIC/DATE
		// column can't take "", so it means "not sent" (COALESCE keeps the
		// value on update), the same as for the legacy fields.
		PurchasePrice: legacyfinance.Text(req.PurchasePrice),
		PurchaseDate:  legacyfinance.Text(req.PurchaseDate),
		CurrentValue:  legacyfinance.Text(req.CurrentValue),
		DownPayment:   legacyfinance.Text(req.DownPayment),
	}
}

// finance parses the legacy finance fields (#4704). Each maps onto the
// property's loan or bill (KB apis/property-vehicle-finance.md); absent,
// null or "" leaves the stored value alone. A malformed value is an
// *legacyfinance.InvalidError (400).
func (req *propertyRequest) finance() (Finance, error) {
	var f Finance
	var err error
	lf := &f.Mortgage
	if lf.OriginalAmount, err = legacyfinance.Amount("mortgage_amount", req.MortgageAmount); err != nil {
		return f, err
	}
	if lf.InterestRate, err = legacyfinance.Rate("mortgage_rate", req.MortgageRate); err != nil {
		return f, err
	}
	if lf.TermMonths, err = legacyfinance.Months("mortgage_term_months", req.MortgageTermMonths); err != nil {
		return f, err
	}
	if lf.StartDate, err = legacyfinance.Date("mortgage_start_date", req.MortgageStartDate); err != nil {
		return f, err
	}
	lf.Lender = legacyfinance.Text(req.MortgageLender)
	lf.AccountNumber = legacyfinance.Text(req.MortgageAccountNumber)

	if f.Tax.Amount, err = legacyfinance.Amount("property_tax_annual", req.PropertyTaxAnnual); err != nil {
		return f, err
	}
	f.Tax.DueMonths = legacyfinance.Text(req.PropertyTaxDueMonths)
	if f.Insurance.Amount, err = legacyfinance.Amount("insurance_annual", req.InsuranceAnnual); err != nil {
		return f, err
	}
	f.Insurance.Provider = legacyfinance.Text(req.InsuranceProvider)
	if f.HOA.Amount, err = legacyfinance.Amount("hoa_fee_monthly", req.HoaFeeMonthly); err != nil {
		return f, err
	}
	return f, nil
}

// writeError reports a create/update error: a bad legacy field is 400,
// database constraint errors go through apierr.DB.
func writeError(w http.ResponseWriter, err error) {
	if ie, ok := legacyfinance.IsInvalid(err); ok {
		apierr.BadRequest(w, ie.Error())
		return
	}
	apierr.DB(w, err)
}

// propertyMoney is every money field of a property response, in one struct
// so that hiding money from a role is a single decision (see
// toPropertyResponse) rather than a check per field. The fields are carried
// as *string with no omitempty, so NULL in the DB serializes as JSON null,
// matching the TS `string | null` type exactly; mortgage_* through
// hoa_fee_monthly are derived from the property's loan and bills (#4704).
//
// A field added here is invisible to housesitters and vendors automatically:
// redaction zeroes the whole struct, and it is never filled for a role that
// may not see money (#4806).
type propertyMoney struct {
	PurchasePrice *string `json:"purchase_price"`
	// PurchaseDate is not an amount, but it belongs to the same financial
	// record (when the family bought the place) and an outsider has no use
	// for it.
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

// propertyResponse is a property as a caller may see it. The money fields
// are embedded, so they serialize at the top level next to id and name.
type propertyResponse struct {
	ID           string  `json:"id"`
	HouseholdID  string  `json:"household_id"`
	Name         string  `json:"name"`
	Address      *string `json:"address,omitempty"`
	PropertyType *string `json:"property_type,omitempty"`
	Notes        *string `json:"notes,omitempty"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`

	propertyMoney
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

// toPropertyResponse is the one place a property becomes JSON, and therefore
// the one place the money policy is applied to it (#4806). Every handler that
// returns a property goes through it, so a new endpoint cannot forget.
//
// A role without money read (housesitter, vendor, unknown) gets the identity
// fields and every money field as null. Housesitters and vendors may read
// properties (ResProperties read = rolesEveryone), but money is theirs to
// read only through ResBills/ResLoans, which they do not have; since #4704
// the property's money comes from the family's loans and bills, so without
// this the property route was a side door around the money policy.
func toPropertyResponse(p *Property, role string) propertyResponse {
	resp := propertyResponse{
		ID:           p.ID.String(),
		HouseholdID:  p.HouseholdID.String(),
		Name:         p.Name,
		Address:      p.Address,
		PropertyType: p.PropertyType,
		Notes:        p.Notes,
		CreatedAt:    p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:    p.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if auth.CanSeeMoney(role) {
		resp.propertyMoney = propertyMoney{
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
	return resp
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
//
// Every role may list properties (ResProperties read = rolesEveryone), but
// only roles with money read get the finance fields: housesitters and
// vendors get nulls, and the query skips the family's loans and bills
// entirely (#4806).
func (h *Handler) ListProperties(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}
	role := middleware.Role(r)

	properties, err := h.repo.ListProperties(r.Context(), hhID, auth.CanSeeMoney(role))
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	resp := make([]propertyResponse, len(properties))
	for i, p := range properties {
		resp[i] = toPropertyResponse(p, role)
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

	fin, err := req.finance()
	if err != nil {
		writeError(w, err)
		return
	}

	role := middleware.Role(r)
	property, err := h.repo.CreateProperty(r.Context(), hhID, req.toProperty(), fin, auth.CanSeeMoney(role))
	if err != nil {
		writeError(w, err)
		return
	}

	h.indexProperty(r.Context(), property)

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": toPropertyResponse(property, role),
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

	role := middleware.Role(r)
	property, err := h.repo.GetProperty(r.Context(), propertyID, hhID, auth.CanSeeMoney(role))
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if property == nil {
		apierr.NotFound(w, "property not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toPropertyResponse(property, role),
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

	fin, err := req.finance()
	if err != nil {
		writeError(w, err)
		return
	}

	role := middleware.Role(r)
	property, err := h.repo.UpdateProperty(r.Context(), propertyID, hhID, req.toProperty(), fin, auth.CanSeeMoney(role))
	if err != nil {
		writeError(w, err)
		return
	}
	if property == nil {
		apierr.NotFound(w, "property not found")
		return
	}

	h.indexProperty(r.Context(), property)

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toPropertyResponse(property, role),
	})
}

// DeleteProperty handles DELETE /api/v1/properties/{id}?confirm=true.
//
// A property carries its rooms and assets with it (domain model v2), so the
// delete must be confirmed. Without confirm=true nothing is deleted and the
// response is 409 CONFIRM_REQUIRED with asset_count, the number of assets
// that would go with it, for the confirm prompt. With confirm=true the
// property is deleted and the response is 200 with the asset_count deleted.
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

	if r.URL.Query().Get("confirm") != "true" {
		count, found, err := h.repo.CountAssets(r.Context(), propertyID, hhID)
		if err != nil {
			apierr.InternalError(w, err)
			return
		}
		if !found {
			apierr.NotFound(w, "property not found")
			return
		}
		apierr.JSON(w, http.StatusConflict, map[string]any{
			"error": apierr.ErrorDetail{
				Code:    "CONFIRM_REQUIRED",
				Message: fmt.Sprintf("deleting this property also deletes its rooms and %d asset(s); repeat with ?confirm=true", count),
			},
			"asset_count": count,
		})
		return
	}

	count, found, err := h.repo.DeleteProperty(r.Context(), propertyID, hhID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if !found {
		apierr.NotFound(w, "property not found")
		return
	}

	h.deletePropertyIndex(r.Context(), propertyID.String())

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{"deleted": true, "asset_count": count},
	})
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

	// Verify the property exists and belongs to this household. Only the
	// existence matters here, so the money fields are not read: a room list
	// never puts a loan or a bill in front of the caller (#4806).
	property, err := h.repo.GetProperty(r.Context(), propertyID, hhID, false)
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

	// Verify the property exists and belongs to this household. Only the
	// existence matters here (see ListRooms).
	property, err := h.repo.GetProperty(r.Context(), propertyID, hhID, false)
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

	room, err := h.repo.CreateRoom(r.Context(), hhID, propertyID, *req.Name, req.Floor, req.Notes)
	if err != nil {
		apierr.DB(w, err)
		return
	}

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": toRoomResponse(room),
	})
}
