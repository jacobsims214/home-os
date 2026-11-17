package bill

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
	"home-os/api/internal/subject"
	"home-os/api/pkg/apierr"
	"home-os/api/pkg/jsonopt"
)

type searchIndexer interface {
	IndexDocument(ctx context.Context, doc search.SearchDocument) error
	DeleteDocument(ctx context.Context, id string) error
}

// Handler holds the dependencies needed by bill HTTP handlers.
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

func (h *Handler) indexBill(ctx context.Context, b *Bill) {
	if h.search == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("bill: panic during search indexing", "panic", r)
		}
	}()
	body := ""
	if b.Category != nil {
		body = *b.Category
	}
	doc := search.SearchDocument{
		ID: "bill-" + b.ID.String(), HouseholdID: b.HouseholdID.String(),
		EntityType: "bill", EntityID: b.ID.String(),
		Title: b.Name, Body: body,
		CreatedAt: b.CreatedAt.Unix(), UpdatedAt: b.UpdatedAt.Unix(),
	}
	if b.PropertyID != nil {
		pid := b.PropertyID.String()
		doc.PropertyID = &pid
	}
	if err := h.search.IndexDocument(context.Background(), doc); err != nil {
		slog.Warn("bill: failed to index", "id", b.ID, "error", err)
	}
}

func (h *Handler) deleteBillIndex(ctx context.Context, id string) {
	if h.search == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("bill: panic during search deletion", "panic", r)
		}
	}()
	if err := h.search.DeleteDocument(context.Background(), "bill-"+id); err != nil {
		slog.Warn("bill: failed to delete from search", "id", id, "error", err)
	}
}

// --- request / response types ---

type createBillRequest struct {
	subject.Fields
	Name          string  `json:"name"`
	Amount        *string `json:"amount,omitempty"`
	DueDay        *int    `json:"due_day,omitempty"`
	Category      *string `json:"category,omitempty"`
	VendorID      *string `json:"vendor_id,omitempty"`
	Rrule         *string `json:"rrule,omitempty"`
	Notes         *string `json:"notes,omitempty"`
	IsAutopay     *bool   `json:"is_autopay,omitempty"`
	AccountNumber *string `json:"account_number,omitempty"`
	PaymentURL    *string `json:"payment_url,omitempty"`
	// Frequency: monthly (default), quarterly, semiannual, annual or once.
	Frequency *string `json:"frequency,omitempty"`
	// DueDate (YYYY-MM-DD): the next or only due date of a bill that is not
	// monthly. A monthly bill uses due_day instead.
	DueDate          *string `json:"due_date,omitempty"`
	BudgetCategoryID *string `json:"budget_category_id,omitempty"`
}

type updateBillRequest struct {
	subject.Fields
	Name          *string `json:"name,omitempty"`
	Amount        *string `json:"amount,omitempty"`
	DueDay        *int    `json:"due_day,omitempty"`
	Category      *string `json:"category,omitempty"`
	VendorID      *string `json:"vendor_id,omitempty"`
	Rrule         *string `json:"rrule,omitempty"`
	Notes         *string `json:"notes,omitempty"`
	IsAutopay     *bool   `json:"is_autopay,omitempty"`
	AccountNumber *string `json:"account_number,omitempty"`
	PaymentURL    *string `json:"payment_url,omitempty"`
	Frequency     *string `json:"frequency,omitempty"`
	// DueDate and BudgetCategoryID: absent keeps, null (or "") clears.
	DueDate          jsonopt.Field[string] `json:"due_date"`
	BudgetCategoryID jsonopt.Field[string] `json:"budget_category_id"`
}

type billResponse struct {
	ID          string  `json:"id"`
	HouseholdID string  `json:"household_id"`
	PropertyID  *string `json:"property_id,omitempty"`
	VehicleID   *string `json:"vehicle_id,omitempty"`
	AssetID     *string `json:"asset_id,omitempty"`
	Name        string  `json:"name"`
	Amount      *string `json:"amount,omitempty"`
	DueDay      *int    `json:"due_day,omitempty"`
	Category    *string `json:"category,omitempty"`
	VendorID    *string `json:"vendor_id,omitempty"`
	Rrule       *string `json:"rrule,omitempty"`
	Notes       *string `json:"notes,omitempty"`
	// Deprecated: derived from the typed subject for pre-v2 clients.
	EntityType *string `json:"entity_type,omitempty"`
	// Deprecated: derived from the typed subject for pre-v2 clients.
	EntityID         *string `json:"entity_id,omitempty"`
	IsAutopay        *bool   `json:"is_autopay,omitempty"`
	AccountNumber    *string `json:"account_number,omitempty"`
	PaymentURL       *string `json:"payment_url,omitempty"`
	Frequency        string  `json:"frequency"`
	DueDate          *string `json:"due_date"`
	BudgetCategoryID *string `json:"budget_category_id"`
	// PaidDate and PaidForDueDate are written by the MCP bill_pay (the API
	// does not record payments): the last payment and the due date it
	// covers. Next-due computation stays in the MCP/worker BillDue rule.
	PaidDate       *string `json:"paid_date"`
	PaidForDueDate *string `json:"paid_for_due_date"`
	// SourceDocumentID is the document the amount came from (#4941); read it
	// with GET /api/v1/files/{id} to check the amount against its text.
	SourceDocumentID *string `json:"source_document_id"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

// toResponse converts a Bill domain model to a JSON-safe response.
func toResponse(b *Bill) billResponse {
	resp := billResponse{
		ID:               b.ID.String(),
		HouseholdID:      b.HouseholdID.String(),
		Name:             b.Name,
		DueDay:           b.DueDay,
		Category:         b.Category,
		Rrule:            b.Rrule,
		Notes:            b.Notes,
		PropertyID:       subject.String(b.PropertyID),
		VehicleID:        subject.String(b.VehicleID),
		AssetID:          subject.String(b.AssetID),
		IsAutopay:        b.IsAutopay,
		AccountNumber:    b.AccountNumber,
		PaymentURL:       b.PaymentURL,
		Frequency:        b.Frequency,
		DueDate:          dateString(b.DueDate),
		BudgetCategoryID: subject.String(b.BudgetCategoryID),
		PaidDate:         dateString(b.PaidDate),
		PaidForDueDate:   dateString(b.PaidForDueDate),
		SourceDocumentID: subject.String(b.SourceDocumentID),
		CreatedAt:        b.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:        b.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}

	resp.EntityType, resp.EntityID = b.Subject.Legacy()
	if b.VendorID != nil {
		s := b.VendorID.String()
		resp.VendorID = &s
	}
	if b.Amount.Valid {
		s := b.Amount.Int.String()
		resp.Amount = &s
	}
	return resp
}

// dateLayout is the wire format of DATE columns.
const dateLayout = "2006-01-02"

func dateString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(dateLayout)
	return &s
}

// parseDate parses an optional YYYY-MM-DD; "" means no date.
func parseDate(s *string) (*time.Time, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	t, err := time.Parse(dateLayout, *s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// parseOptionalUUID parses an optional id; "" means none.
func parseOptionalUUID(s *string) (*uuid.UUID, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// normalizeSchedule validates b's frequency, due_day and due_date (the
// bills_frequency_check and bills_due_day_monthly_check rules of migration
// 044) and returns a client error message, or "". A monthly bill is due on
// due_day each month and has no due_date; any other bill is due on due_date
// and has no due_day. A due_day or due_date carried over from the stored row
// (dayGiven/dateGiven false) is dropped when the frequency no longer uses
// it; one given in the same request is an error.
func normalizeSchedule(b *Bill, dayGiven, dateGiven bool) string {
	if b.Frequency == "" {
		b.Frequency = FrequencyMonthly
	}
	if !ValidFrequency(b.Frequency) {
		return "frequency must be monthly, quarterly, semiannual, annual or once"
	}
	if b.DueDay != nil && (*b.DueDay < 1 || *b.DueDay > 31) {
		return "due_day must be between 1 and 31"
	}
	if b.Frequency == FrequencyMonthly {
		if b.DueDate != nil {
			if dateGiven {
				return "due_date is for bills that are not monthly; a monthly bill uses due_day"
			}
			b.DueDate = nil
		}
		return ""
	}
	if b.DueDay != nil {
		if dayGiven {
			return "due_day is only for monthly bills; use due_date for a " + b.Frequency + " bill"
		}
		b.DueDay = nil
	}
	return ""
}

// extractHouseholdID extracts the household UUID from the JWT claims in the
// request context. Returns uuid.Nil if no claims are present.
func extractHouseholdID(r *http.Request) uuid.UUID {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		return uuid.Nil
	}
	id, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		return uuid.Nil
	}
	return id
}

// List returns all bills for the authenticated user's household.
// GET /api/v1/bills
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	householdID := extractHouseholdID(r)
	if householdID == uuid.Nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	bills, err := h.repo.List(r.Context(), householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	var resp []billResponse
	for _, b := range bills {
		resp = append(resp, toResponse(b))
	}
	if resp == nil {
		resp = []billResponse{}
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": resp})
}

// Get returns a single bill by ID, scoped to the authenticated household.
// GET /api/v1/bills/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid bill id")
		return
	}

	householdID := extractHouseholdID(r)
	if householdID == uuid.Nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	bill, err := h.repo.Get(r.Context(), id, householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if bill == nil {
		apierr.NotFound(w, "bill not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{"data": toResponse(bill)})
}

// Create creates a new bill for the authenticated household.
// POST /api/v1/bills
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	householdID := extractHouseholdID(r)
	if householdID == uuid.Nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	var req createBillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}

	b := &Bill{
		HouseholdID:   householdID,
		Name:          req.Name,
		DueDay:        req.DueDay,
		Category:      req.Category,
		Rrule:         req.Rrule,
		Notes:         req.Notes,
		IsAutopay:     req.IsAutopay,
		AccountNumber: req.AccountNumber,
		PaymentURL:    req.PaymentURL,
	}
	if req.Frequency != nil {
		b.Frequency = *req.Frequency
	}
	var err error
	if b.DueDate, err = parseDate(req.DueDate); err != nil {
		apierr.BadRequest(w, "due_date must be YYYY-MM-DD")
		return
	}
	if b.BudgetCategoryID, err = parseOptionalUUID(req.BudgetCategoryID); err != nil {
		apierr.BadRequest(w, "invalid budget_category_id")
		return
	}
	if msg := normalizeSchedule(b, req.DueDay != nil, b.DueDate != nil); msg != "" {
		apierr.BadRequest(w, msg)
		return
	}

	if err := req.Fields.Apply(&b.Subject); err != nil {
		apierr.BadRequest(w, err.Error())
		return
	}
	if req.VendorID != nil {
		vid, err := uuid.Parse(*req.VendorID)
		if err != nil {
			apierr.BadRequest(w, "invalid vendor_id")
			return
		}
		b.VendorID = &vid
	}
	if req.Amount != nil {
		if err := b.Amount.Scan(*req.Amount); err != nil {
			apierr.BadRequest(w, "invalid amount")
			return
		}
	}

	created, err := h.repo.Create(r.Context(), b)
	if err != nil {
		apierr.DB(w, err)
		return
	}

	h.indexBill(r.Context(), created)

	apierr.JSON(w, http.StatusCreated, map[string]any{"data": toResponse(created)})
}

// Update updates an existing bill, scoped to the authenticated household.
// PUT /api/v1/bills/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid bill id")
		return
	}

	householdID := extractHouseholdID(r)
	if householdID == uuid.Nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	var req updateBillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	existing, err := h.repo.Get(r.Context(), id, householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if existing == nil {
		apierr.NotFound(w, "bill not found")
		return
	}

	// Merge: only overwrite fields that were provided in the request.
	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.DueDay != nil {
		existing.DueDay = req.DueDay
	}
	if req.Category != nil {
		existing.Category = req.Category
	}
	if req.Rrule != nil {
		existing.Rrule = req.Rrule
	}
	if req.Notes != nil {
		existing.Notes = req.Notes
	}
	if err := req.Fields.Apply(&existing.Subject); err != nil {
		apierr.BadRequest(w, err.Error())
		return
	}
	if req.VendorID != nil {
		if *req.VendorID == "" {
			existing.VendorID = nil
		} else {
			vid, err := uuid.Parse(*req.VendorID)
			if err != nil {
				apierr.BadRequest(w, "invalid vendor_id")
				return
			}
			existing.VendorID = &vid
		}
	}
	if req.Amount != nil {
		if *req.Amount == "" {
			existing.Amount.Valid = false
		} else {
			if err := existing.Amount.Scan(*req.Amount); err != nil {
				apierr.BadRequest(w, "invalid amount")
				return
			}
		}
	}
	if req.IsAutopay != nil {
		existing.IsAutopay = req.IsAutopay
	}
	if req.AccountNumber != nil {
		existing.AccountNumber = req.AccountNumber
	}
	if req.PaymentURL != nil {
		existing.PaymentURL = req.PaymentURL
	}
	if req.Frequency != nil {
		existing.Frequency = *req.Frequency
	}
	dateGiven := false
	if req.DueDate.Set {
		d, err := parseDate(req.DueDate.Value)
		if err != nil {
			apierr.BadRequest(w, "due_date must be YYYY-MM-DD")
			return
		}
		existing.DueDate = d
		dateGiven = d != nil
	}
	if req.BudgetCategoryID.Set {
		id, err := parseOptionalUUID(req.BudgetCategoryID.Value)
		if err != nil {
			apierr.BadRequest(w, "invalid budget_category_id")
			return
		}
		existing.BudgetCategoryID = id
	}
	if msg := normalizeSchedule(existing, req.DueDay != nil, dateGiven); msg != "" {
		apierr.BadRequest(w, msg)
		return
	}

	updated, err := h.repo.Update(r.Context(), existing)
	if err != nil {
		apierr.DB(w, err)
		return
	}
	if updated == nil {
		apierr.NotFound(w, "bill not found")
		return
	}

	h.indexBill(r.Context(), updated)

	apierr.JSON(w, http.StatusOK, map[string]any{"data": toResponse(updated)})
}

// Delete removes a bill, scoped to the authenticated household.
// DELETE /api/v1/bills/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid bill id")
		return
	}

	householdID := extractHouseholdID(r)
	if householdID == uuid.Nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	if err := h.repo.Delete(r.Context(), id, householdID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			apierr.NotFound(w, "bill not found")
			return
		}
		apierr.InternalError(w, err)
		return
	}

	h.deleteBillIndex(r.Context(), id.String())

	w.WriteHeader(http.StatusNoContent)
}
