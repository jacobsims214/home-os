package vehicle

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

// Handler holds dependencies for vehicle HTTP handlers.
type Handler struct {
	repo   *Repo
	search searchIndexer
}

// NewHandler creates a new vehicle handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

func (h *Handler) WithSearchClient(sc *search.Client) *Handler {
	h.search = sc
	return h
}

func (h *Handler) indexVehicle(ctx context.Context, v *Vehicle) {
	if h.search == nil { return }
	defer func() { if r := recover(); r != nil { slog.Warn("vehicle: panic during search indexing", "panic", r) } }()
	body := ""
	if v.Make != nil { body = *v.Make }
	if v.Model != nil { body += " " + *v.Model }
	if v.VIN != nil { body += " " + *v.VIN }
	if v.LicensePlate != nil { body += " " + *v.LicensePlate }
	if v.Color != nil { body += " " + *v.Color }
	doc := search.SearchDocument{
		ID: "vehicle-" + v.ID.String(), HouseholdID: v.HouseholdID.String(),
		EntityType: "vehicle", EntityID: v.ID.String(),
		Title: v.MakeString(), Body: body,
		CreatedAt: v.CreatedAt.Unix(), UpdatedAt: v.UpdatedAt.Unix(),
	}
	if err := h.search.IndexDocument(context.Background(), doc); err != nil {
		slog.Warn("vehicle: failed to index", "id", v.ID, "error", err)
	}
}

func (h *Handler) deleteVehicleIndex(ctx context.Context, id string) {
	if h.search == nil { return }
	defer func() { if r := recover(); r != nil { slog.Warn("vehicle: panic during search deletion", "panic", r) } }()
	if err := h.search.DeleteDocument(context.Background(), "vehicle-"+id); err != nil {
		slog.Warn("vehicle: failed to delete from search", "id", id, "error", err)
	}
}

// --- request / response types ---

type createVehicleRequest struct {
	Year                   *int    `json:"year"`
	Make                   *string `json:"make"`
	Model                  *string `json:"model"`
	VIN                    *string `json:"vin"`
	LicensePlate           *string `json:"license_plate"`
	Color                  *string `json:"color"`
	Notes                  *string `json:"notes"`
	PurchasePrice          *string `json:"purchase_price"`
	PurchaseDate           *string `json:"purchase_date"`
	CurrentValue           *string `json:"current_value"`
	LoanAmount             *string `json:"loan_amount"`
	InsuranceCost          *string `json:"insurance_cost"`
	RegistrationCost       *string `json:"registration_cost"`
	Lender                 *string `json:"lender"`
	LoanTermMonths         *int    `json:"loan_term_months"`
	MonthlyPayment         *string `json:"monthly_payment"`
	RegistrationRenewalMonth *int  `json:"registration_renewal_month"`
	InsuranceProvider      *string `json:"insurance_provider"`
}

type updateVehicleRequest struct {
	Year                   *int    `json:"year"`
	Make                   *string `json:"make"`
	Model                  *string `json:"model"`
	VIN                    *string `json:"vin"`
	LicensePlate           *string `json:"license_plate"`
	Color                  *string `json:"color"`
	Notes                  *string `json:"notes"`
	PurchasePrice          *string `json:"purchase_price"`
	PurchaseDate           *string `json:"purchase_date"`
	CurrentValue           *string `json:"current_value"`
	LoanAmount             *string `json:"loan_amount"`
	InsuranceCost          *string `json:"insurance_cost"`
	RegistrationCost       *string `json:"registration_cost"`
	Lender                 *string `json:"lender"`
	LoanTermMonths         *int    `json:"loan_term_months"`
	MonthlyPayment         *string `json:"monthly_payment"`
	RegistrationRenewalMonth *int  `json:"registration_renewal_month"`
	InsuranceProvider      *string `json:"insurance_provider"`
}

type vehicleResponse struct {
	ID                     string  `json:"id"`
	HouseholdID            string  `json:"household_id"`
	Year                   *int    `json:"year,omitempty"`
	Make                   *string `json:"make,omitempty"`
	Model                  *string `json:"model,omitempty"`
	VIN                    *string `json:"vin,omitempty"`
	LicensePlate           *string `json:"license_plate,omitempty"`
	Color                  *string `json:"color,omitempty"`
	Notes                  *string `json:"notes,omitempty"`
	PurchasePrice          *string `json:"purchase_price"`
	PurchaseDate           *string `json:"purchase_date"`
	CurrentValue           *string `json:"current_value"`
	LoanAmount             *string `json:"loan_amount"`
	InsuranceCost          *string `json:"insurance_cost"`
	RegistrationCost       *string `json:"registration_cost"`
	Lender                 *string `json:"lender"`
	LoanTermMonths         *int    `json:"loan_term_months"`
	MonthlyPayment         *string `json:"monthly_payment"`
	RegistrationRenewalMonth *int  `json:"registration_renewal_month"`
	InsuranceProvider      *string `json:"insurance_provider"`
	CreatedAt              string  `json:"created_at"`
	UpdatedAt              string  `json:"updated_at"`
}

func toVehicleResponse(v *Vehicle) vehicleResponse {
	return vehicleResponse{
		ID:                     v.ID.String(),
		HouseholdID:            v.HouseholdID.String(),
		Year:                   v.Year,
		Make:                   v.Make,
		Model:                  v.Model,
		VIN:                    v.VIN,
		LicensePlate:           v.LicensePlate,
		Color:                  v.Color,
		Notes:                  v.Notes,
		PurchasePrice:          v.PurchasePrice,
		PurchaseDate:           v.PurchaseDate,
		CurrentValue:           v.CurrentValue,
		LoanAmount:             v.LoanAmount,
		InsuranceCost:          v.InsuranceCost,
		RegistrationCost:       v.RegistrationCost,
		Lender:                 v.Lender,
		LoanTermMonths:         v.LoanTermMonths,
		MonthlyPayment:         v.MonthlyPayment,
		RegistrationRenewalMonth: v.RegistrationRenewalMonth,
		InsuranceProvider:      v.InsuranceProvider,
		CreatedAt:              v.CreatedAt.Format(time.RFC3339),
		UpdatedAt:              v.UpdatedAt.Format(time.RFC3339),
	}
}

func toVehicleResponses(vehicles []*Vehicle) []vehicleResponse {
	out := make([]vehicleResponse, 0, len(vehicles))
	for _, v := range vehicles {
		out = append(out, toVehicleResponse(v))
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

// List returns all vehicles for the authenticated household.
// GET /api/v1/vehicles
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	vehicles, err := h.repo.List(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toVehicleResponses(vehicles),
	})
}

// Get returns a single vehicle by ID.
// GET /api/v1/vehicles/:id
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid vehicle id")
		return
	}

	vehicle, err := h.repo.Get(r.Context(), hid, id)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if vehicle == nil {
		apierr.NotFound(w, "vehicle not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toVehicleResponse(vehicle),
	})
}

// Create inserts a new vehicle for the authenticated household.
// POST /api/v1/vehicles
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	var req createVehicleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	v := &Vehicle{
		Year:                   req.Year,
		Make:                   req.Make,
		Model:                  req.Model,
		VIN:                    req.VIN,
		LicensePlate:           req.LicensePlate,
		Color:                  req.Color,
		Notes:                  req.Notes,
		PurchasePrice:          req.PurchasePrice,
		PurchaseDate:           req.PurchaseDate,
		CurrentValue:           req.CurrentValue,
		LoanAmount:             req.LoanAmount,
		InsuranceCost:          req.InsuranceCost,
		RegistrationCost:       req.RegistrationCost,
		Lender:                 req.Lender,
		LoanTermMonths:         req.LoanTermMonths,
		MonthlyPayment:         req.MonthlyPayment,
		RegistrationRenewalMonth: req.RegistrationRenewalMonth,
		InsuranceProvider:      req.InsuranceProvider,
	}

	created, err := h.repo.Create(r.Context(), hid, v)

	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	h.indexVehicle(r.Context(), created)

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": toVehicleResponse(created),
	})
}

// Update modifies an existing vehicle.
// PUT /api/v1/vehicles/:id
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid vehicle id")
		return
	}

	var req updateVehicleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	v := &Vehicle{
		Year:                   req.Year,
		Make:                   req.Make,
		Model:                  req.Model,
		VIN:                    req.VIN,
		LicensePlate:           req.LicensePlate,
		Color:                  req.Color,
		Notes:                  req.Notes,
		PurchasePrice:          req.PurchasePrice,
		PurchaseDate:           req.PurchaseDate,
		CurrentValue:           req.CurrentValue,
		LoanAmount:             req.LoanAmount,
		InsuranceCost:          req.InsuranceCost,
		RegistrationCost:       req.RegistrationCost,
		Lender:                 req.Lender,
		LoanTermMonths:         req.LoanTermMonths,
		MonthlyPayment:         req.MonthlyPayment,
		RegistrationRenewalMonth: req.RegistrationRenewalMonth,
		InsuranceProvider:      req.InsuranceProvider,
	}

	updated, err := h.repo.Update(r.Context(), hid, id, v)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if updated == nil {
		apierr.NotFound(w, "vehicle not found")
		return
	}

	h.indexVehicle(r.Context(), updated)

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toVehicleResponse(updated),
	})
}

// Delete removes a vehicle.
// DELETE /api/v1/vehicles/:id
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid vehicle id")
		return
	}

	if err := h.repo.Delete(r.Context(), hid, id); err != nil {
		apierr.NotFound(w, "vehicle not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
