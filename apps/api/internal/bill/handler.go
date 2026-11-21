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

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
	"home-os/api/internal/subject"
	"home-os/api/pkg/apierr"
	"home-os/api/pkg/jsonopt"
	"home-os/api/pkg/money"
	"home-os/billdue"
)

type searchIndexer interface {
	IndexDocument(ctx context.Context, doc search.SearchDocument) error
	DeleteDocument(ctx context.Context, id string) error
}

// Handler holds the dependencies needed by bill HTTP handlers.
type Handler struct {
	repo   *Repo
	search searchIndexer
	now    func() time.Time
}

func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo, now: time.Now}
}

// WithNow replaces the clock, so a test can pin "today" (the attention state
// and every due date come from the household's date). Production uses
// time.Now.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// todayIn is the family's date in the household's timezone, as midnight UTC:
// the form every due date in the codebase uses (billdue.BillDueFor, the MCP's
// householdToday, budget's today). An unknown or empty zone falls back to UTC.
// A failed timezone read does too: a bill list must not 500 because the
// household row could not be read, and UTC is what every other fallback here
// uses. It is logged, because a wrong zone can move a bill across midnight.
func (h *Handler) todayIn(r *http.Request, householdID uuid.UUID) time.Time {
	tz, err := h.repo.HouseholdTimezone(r.Context(), householdID)
	loc := time.UTC
	if err == nil {
		if l, lerr := time.LoadLocation(tz); lerr == nil {
			loc = l
		}
	} else {
		slog.Warn("bill: household timezone unavailable, using UTC", "household_id", householdID, "error", err)
	}
	y, m, d := h.now().In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
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
	// AutopayAccountID links a payment_accounts row (075). The database
	// refuses one on a bill that is not autopay (bills_autopay_account_check).
	AutopayAccountID *string `json:"autopay_account_id,omitempty"`
	// PayeeName is the payee when it is not a vendors row; VendorID is the
	// payee when it is (075).
	PayeeName *string `json:"payee_name,omitempty"`
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
	// AutopayAccountID and PayeeName: absent keeps, null (or "") clears
	// (075). Clearing the account on a bill that is still autopay is exactly
	// the needs_setup state, so it is allowed.
	AutopayAccountID jsonopt.Field[string] `json:"autopay_account_id"`
	PayeeName        jsonopt.Field[string] `json:"payee_name"`
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
	// AutopayEnabled is bills.is_autopay under the name the UI and the agent
	// read (#5498, 075). Always present: the column is NOT NULL DEFAULT false.
	AutopayEnabled bool `json:"autopay_enabled"`
	// PaymentAccount is where an autopay bill is paid from: name, institution,
	// type and the LAST FOUR digits, never a full number. Absent for a role
	// without money (auth.CanSeeMoney) -- it is money-adjacent -- and absent
	// when the bill has no linked account. The account's secret link is never
	// serialized: "a login is stored" is a credential fact, not a bill field.
	PaymentAccount *paymentAccountResponse `json:"payment_account,omitempty"`
	// Payee is who the bill is paid to, with contact details. It is not money
	// (vendors are readable by housesitters), so every caller who may read the
	// bill gets it.
	Payee *payeeResponse `json:"payee,omitempty"`
	// Attention is the product's central derived rule: automatic, needs_setup,
	// due_soon or overdue. It comes from billdue.AttentionFor -- the ONE
	// derivation the UI (#5499), the MCP layer and the notification policy
	// (#5500) all key on. Nothing re-derives it, here or anywhere else.
	Attention string `json:"attention"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// paymentAccountResponse is a payment account as it appears on a bill.
type paymentAccountResponse struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Institution *string `json:"institution,omitempty"`
	// LastFour is exactly four digits. payment_accounts has no column for a
	// full number (075), so there is nothing longer to leak by accident.
	LastFour    *string `json:"last_four,omitempty"`
	AccountType string  `json:"account_type"`
}

// payeeResponse is who a bill is paid to.
type payeeResponse struct {
	// VendorID is present only when the payee is a vendors row.
	VendorID *string `json:"vendor_id,omitempty"`
	Name     string  `json:"name"`
	// Phone and Email are single values (vendors.phone/email, 004), not lists;
	// the JSON name is plural to leave room without renaming later.
	Phones  []string `json:"phones,omitempty"`
	Email   *string  `json:"email,omitempty"`
	Website *string  `json:"website,omitempty"`
}

// toResponse converts a Bill domain model to a JSON-safe response for a caller
// with role, as of today (the family's date as midnight UTC, from h.todayIn).
//
// SINGLE BILL PATH: List, Get, Create and Update all go through this one
// function, so a field added here cannot appear on one route and not another,
// and the attention state cannot be computed twice (handler_amount_test.go
// already pins the amount on this property).
//
// Role rules are the existing money policy, not a new one: a role without
// auth.CanSeeMoney (housesitter, vendor, unknown) loses the payment account --
// its name, institution, last-four and the fact that one is linked at all.
// Today the bills route already refuses those roles outright (ResBills read is
// rolesReaders, which is the family plus the viewer), so this is the second
// lock on the same door -- and it is the one that holds if the route rules are
// ever widened, exactly as #4806 and #4912 were side doors on properties and
// assets. `auth.CanSeeMoney` is asked, never a role name.
func toResponse(b *Bill, role string, today time.Time) billResponse {
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
		// A nil pointer means the pointer column was never read (a partial
		// update that said nothing): the column is NOT NULL, so read that as
		// the fail-safe false rather than a third state.
		AutopayEnabled: b.IsAutopay != nil && *b.IsAutopay,
	}

	// The payment account: only for money roles, and only when one is linked.
	if b.PaymentAccount != nil && auth.CanSeeMoney(role) {
		a := b.PaymentAccount
		inst, lastFour := a.Institution, a.LastFour
		resp.PaymentAccount = &paymentAccountResponse{
			ID: a.ID.String(), Name: a.Name, Institution: inst,
			LastFour: lastFour, AccountType: a.AccountType,
		}
	}
	// The payee: a vendors row when linked, else the free-text name.
	if p := b.Payee; p != nil {
		r := &payeeResponse{Name: p.Name, Email: p.Email, Website: p.Website}
		if p.Phone != nil && *p.Phone != "" {
			r.Phones = []string{*p.Phone}
		}
		if p.VendorID != nil {
			s := p.VendorID.String()
			r.VendorID = &s
		}
		resp.Payee = r
	}

	// The one derivation. A role without money is still told the bill is on
	// them -- that is not a money fact, it is the point of the app -- but the
	// answer uses only what that role may see, so a hidden account cannot leak
	// through the state it would have produced.
	resp.Attention = billdue.AttentionFor(today, b.schedule(), b.PaidDate, b.PaidForDueDate,
		b.CreatedAt, resp.AutopayEnabled, b.hasPaymentSource(auth.CanSeeMoney(role)))

	resp.EntityType, resp.EntityID = b.Subject.Legacy()
	if b.VendorID != nil {
		s := b.VendorID.String()
		resp.VendorID = &s
	}
	// The amount is a decimal string of DOLLARS ("2340.00"), never the
	// NUMERIC's mantissa: pgx stores 2340.00 as Int=234000, Exp=-2, and
	// Int.String() alone sent "234000", which rendered as $234,000 (#5297).
	// money.DecimalString keeps the exponent, so the wire carries the same
	// number the database does (#5312: this was the API's only such case).
	resp.Amount = money.DecimalString(b.Amount)
	return resp
}

// schedule is the row's due schedule, as home-os/billdue expresses it.
func (b *Bill) schedule() billdue.Schedule {
	return billdue.Schedule{Frequency: b.Frequency, DueDay: b.DueDay, DueDate: b.DueDate}
}

// hasPaymentSource reports whether a payment account is on record for this
// bill: the linked payment_accounts row, or the legacy free-text
// bills.account_number on a pre-075 row (075 kept that column; only
// payment_accounts.last_four is the new, masked home for it). visible says the
// caller may see account details; when it is false the answer is false, so the
// state cannot reveal that an account is linked.
func (b *Bill) hasPaymentSource(visible bool) bool {
	if !visible {
		return false
	}
	if b.AutopayAccountID != nil {
		return true
	}
	return b.AccountNumber != nil && *b.AccountNumber != ""
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

// emptyToNil turns "" into nil (the column's "not set"), keeping a real value.
// It is what a partial update's "null or empty string clears" means for the
// two payee/account links.
func emptyToNil(s *string) (*string, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	v := *s
	return &v, nil
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

	// One date for the whole page: two bills cannot be judged against two
	// different todays.
	today := h.todayIn(r, householdID)
	role := middleware.Role(r)
	var resp []billResponse
	for _, b := range bills {
		resp = append(resp, toResponse(b, role, today))
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

	apierr.JSON(w, http.StatusOK, map[string]any{"data": toResponse(bill, middleware.Role(r), h.todayIn(r, householdID))})
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
	if b.AutopayAccountID, err = parseOptionalUUID(req.AutopayAccountID); err != nil {
		apierr.BadRequest(w, "invalid autopay_account_id")
		return
	}
	if b.PayeeName, err = emptyToNil(req.PayeeName); err != nil {
		apierr.BadRequest(w, "invalid payee_name")
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

	apierr.JSON(w, http.StatusCreated, map[string]any{"data": toResponse(created, middleware.Role(r), h.todayIn(r, householdID))})
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
	if req.AutopayAccountID.Set {
		id, err := parseOptionalUUID(req.AutopayAccountID.Value)
		if err != nil {
			apierr.BadRequest(w, "invalid autopay_account_id")
			return
		}
		existing.AutopayAccountID = id
	}
	if req.PayeeName.Set {
		name, err := emptyToNil(req.PayeeName.Value)
		if err != nil {
			apierr.BadRequest(w, "invalid payee_name")
			return
		}
		existing.PayeeName = name
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

	apierr.JSON(w, http.StatusOK, map[string]any{"data": toResponse(updated, middleware.Role(r), h.todayIn(r, householdID))})
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
