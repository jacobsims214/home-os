package loan

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/middleware"
	"home-os/api/internal/subject"
	"home-os/api/pkg/apierr"
	"home-os/api/pkg/jsonopt"
)

// Handler holds the dependencies needed by loan HTTP handlers.
type Handler struct {
	repo *Repo
}

// NewHandler creates a new loan handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

// --- request / response types ---

// loanRequest is the JSON body for create and update loan requests.
// All fields use *string so that null in JSON maps to NULL in the DB,
// and numeric values are carried as strings to avoid float precision loss.
type createLoanRequest struct {
	subject.Fields
	Name             *string `json:"name"`
	Lender           *string `json:"lender"`
	OriginalAmount   *string `json:"original_amount"`
	InterestRate     *string `json:"interest_rate"`
	TermMonths       *int    `json:"term_months"`
	MonthlyPayment   *string `json:"monthly_payment"`
	RemainingBalance *string `json:"remaining_balance"`
	StartDate        *string `json:"start_date"`
	Notes            *string `json:"notes"`
	// Migration 044 budgeting fields.
	DueDay           *int    `json:"due_day"`
	TermEnd          *string `json:"term_end"`
	AccountNumber    *string `json:"account_number"`
	BudgetCategoryID *string `json:"budget_category_id"`
}

// updateLoanRequest: PUT replaces the loan's original fields, as before. The
// 044 fields (due_day, term_end, account_number, budget_category_id) are
// only changed when sent (null clears), so a client written before they
// existed does not wipe them.
type updateLoanRequest struct {
	subject.Fields
	Name             *string               `json:"name"`
	Lender           *string               `json:"lender"`
	OriginalAmount   *string               `json:"original_amount"`
	InterestRate     *string               `json:"interest_rate"`
	TermMonths       *int                  `json:"term_months"`
	MonthlyPayment   *string               `json:"monthly_payment"`
	RemainingBalance *string               `json:"remaining_balance"`
	StartDate        *string               `json:"start_date"`
	Notes            *string               `json:"notes"`
	DueDay           jsonopt.Field[int]    `json:"due_day"`
	TermEnd          jsonopt.Field[string] `json:"term_end"`
	AccountNumber    jsonopt.Field[string] `json:"account_number"`
	BudgetCategoryID jsonopt.Field[string] `json:"budget_category_id"`
}

type loanResponse struct {
	ID          string  `json:"id"`
	HouseholdID string  `json:"household_id"`
	Name        string  `json:"name"`
	PropertyID  *string `json:"property_id"`
	VehicleID   *string `json:"vehicle_id"`
	AssetID     *string `json:"asset_id"`
	// Deprecated: derived from the typed subject for pre-v2 clients.
	EntityType *string `json:"entity_type"`
	// Deprecated: derived from the typed subject for pre-v2 clients.
	EntityID         *string `json:"entity_id"`
	Lender           *string `json:"lender"`
	OriginalAmount   *string `json:"original_amount"`
	InterestRate     *string `json:"interest_rate"`
	TermMonths       *int    `json:"term_months"`
	MonthlyPayment   *string `json:"monthly_payment"`
	RemainingBalance *string `json:"remaining_balance"`
	StartDate        *string `json:"start_date"`
	Notes            *string `json:"notes"`
	DueDay           *int    `json:"due_day"`
	TermEnd          *string `json:"term_end"`
	AccountNumber    *string `json:"account_number"`
	BudgetCategoryID *string `json:"budget_category_id"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

// toLoanResponse converts a *Loan to a loanResponse.
func toLoanResponse(l *Loan) loanResponse {
	r := loanResponse{
		ID:               l.ID.String(),
		HouseholdID:      l.HouseholdID.String(),
		Name:             l.Name,
		PropertyID:       subject.String(l.PropertyID),
		VehicleID:        subject.String(l.VehicleID),
		AssetID:          subject.String(l.AssetID),
		Lender:           l.Lender,
		OriginalAmount:   l.OriginalAmount,
		InterestRate:     l.InterestRate,
		TermMonths:       l.TermMonths,
		MonthlyPayment:   l.MonthlyPayment,
		RemainingBalance: l.RemainingBalance,
		StartDate:        l.StartDate,
		Notes:            l.Notes,
		DueDay:           l.DueDay,
		TermEnd:          l.TermEnd,
		AccountNumber:    l.AccountNumber,
		BudgetCategoryID: subject.String(l.BudgetCategoryID),
		CreatedAt:        l.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:        l.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	r.EntityType, r.EntityID = l.Subject.Legacy()
	return r
}

func toLoanResponses(loans []*Loan) []loanResponse {
	out := make([]loanResponse, 0, len(loans))
	for _, l := range loans {
		out = append(out, toLoanResponse(l))
	}
	return out
}

// budgetFields validates and sets the migration 044 fields on l. It returns
// a client error message, or "".
func budgetFields(l *Loan, dueDay *int, termEnd, accountNumber, budgetCategoryID *string) string {
	if dueDay != nil && (*dueDay < 1 || *dueDay > 31) {
		return "due_day must be between 1 and 31"
	}
	l.DueDay = dueDay
	l.TermEnd = nil
	if termEnd != nil && *termEnd != "" {
		if _, err := time.Parse("2006-01-02", *termEnd); err != nil {
			return "term_end must be YYYY-MM-DD"
		}
		l.TermEnd = termEnd
	}
	l.AccountNumber = accountNumber
	l.BudgetCategoryID = nil
	if budgetCategoryID != nil && *budgetCategoryID != "" {
		id, err := uuid.Parse(*budgetCategoryID)
		if err != nil {
			return "invalid budget_category_id"
		}
		l.BudgetCategoryID = &id
	}
	return ""
}

// parseFilter reads ?entity_type= (kept for pre-v2 clients; also accepts
// "household"), ?property_id= and ?vehicle_id=.
func parseFilter(r *http.Request) (Filter, string) {
	q := r.URL.Query()
	f := Filter{Kind: q.Get("entity_type")}
	switch f.Kind {
	case "", "property", "vehicle", "asset", "household":
	default:
		return f, "entity_type must be property, vehicle, asset or household"
	}
	for name, dst := range map[string]**uuid.UUID{"property_id": &f.PropertyID, "vehicle_id": &f.VehicleID} {
		if v := q.Get(name); v != "" {
			id, err := uuid.Parse(v)
			if err != nil {
				return f, "invalid " + name
			}
			*dst = &id
		}
	}
	return f, ""
}

// --- handlers ---

// List handles GET /api/v1/loans.
// Supports optional ?entity_type= query parameter for filtering.
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

	filter, msg := parseFilter(r)
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}

	loans, err := h.repo.List(r.Context(), householdID, filter)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toLoanResponses(loans),
	})
}

// Create handles POST /api/v1/loans.
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

	var req createLoanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Name == nil || *req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}

	l := &Loan{
		Name:             *req.Name,
		Lender:           req.Lender,
		OriginalAmount:   req.OriginalAmount,
		InterestRate:     req.InterestRate,
		TermMonths:       req.TermMonths,
		MonthlyPayment:   req.MonthlyPayment,
		RemainingBalance: req.RemainingBalance,
		StartDate:        req.StartDate,
		Notes:            req.Notes,
	}
	if msg := budgetFields(l, req.DueDay, req.TermEnd, req.AccountNumber, req.BudgetCategoryID); msg != "" {
		apierr.BadRequest(w, msg)
		return
	}

	if err := req.Fields.Apply(&l.Subject); err != nil {
		apierr.BadRequest(w, err.Error())
		return
	}

	created, err := h.repo.Create(r.Context(), householdID, l)
	if err != nil {
		apierr.DB(w, err)
		return
	}

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": toLoanResponse(created),
	})
}

// Get handles GET /api/v1/loans/{id}.
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

	loanID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid loan id")
		return
	}

	loan, err := h.repo.Get(r.Context(), loanID, householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if loan == nil {
		apierr.NotFound(w, "loan not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toLoanResponse(loan),
	})
}

// Update handles PUT /api/v1/loans/{id}.
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

	loanID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid loan id")
		return
	}

	var req updateLoanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Name == nil || *req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}

	l := &Loan{
		Name:             *req.Name,
		Lender:           req.Lender,
		OriginalAmount:   req.OriginalAmount,
		InterestRate:     req.InterestRate,
		TermMonths:       req.TermMonths,
		MonthlyPayment:   req.MonthlyPayment,
		RemainingBalance: req.RemainingBalance,
		StartDate:        req.StartDate,
		Notes:            req.Notes,
	}

	// The 044 fields keep their stored values unless sent.
	existing, err := h.repo.Get(r.Context(), loanID, householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if existing == nil {
		apierr.NotFound(w, "loan not found")
		return
	}
	dueDay, termEnd, account := existing.DueDay, existing.TermEnd, existing.AccountNumber
	budgetID := subject.String(existing.BudgetCategoryID)
	req.DueDay.Apply(&dueDay)
	req.TermEnd.Apply(&termEnd)
	req.AccountNumber.Apply(&account)
	req.BudgetCategoryID.Apply(&budgetID)
	if msg := budgetFields(l, dueDay, termEnd, account, budgetID); msg != "" {
		apierr.BadRequest(w, msg)
		return
	}

	// PUT replaces the whole loan, subject included.
	if err := req.Fields.Apply(&l.Subject); err != nil {
		apierr.BadRequest(w, err.Error())
		return
	}

	updated, err := h.repo.Update(r.Context(), loanID, householdID, l)
	if err != nil {
		apierr.DB(w, err)
		return
	}
	if updated == nil {
		apierr.NotFound(w, "loan not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toLoanResponse(updated),
	})
}

// Delete handles DELETE /api/v1/loans/{id}.
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

	loanID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid loan id")
		return
	}

	if err := h.repo.Delete(r.Context(), loanID, householdID); err != nil {
		apierr.NotFound(w, "loan not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
