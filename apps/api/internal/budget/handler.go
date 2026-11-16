package budget

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
	"home-os/api/pkg/jsonopt"
)

// Policy decides what a member with a given household role may do with the
// budget. Roles are re-read from memberships for the household the request
// resolved to (X-Household-ID aware). It is an interface so the household
// role policy (#4689) can replace DefaultPolicy without touching handlers.
type Policy interface {
	// CanRead: list/get categories and the monthly obligations.
	CanRead(role string) bool
	// CanWrite: create, update and delete categories.
	CanWrite(role string) bool
}

// DefaultPolicy is the budget policy until #4689 lands. Money is family
// business, so people from outside the family see none of it:
//   - owner, family_manager, family_member: read and write (anyone in the
//     family may already edit bills and loans);
//   - viewer: read only;
//   - housesitter, vendor: nothing.
type DefaultPolicy struct{}

// CanRead implements Policy.
func (DefaultPolicy) CanRead(role string) bool {
	return DefaultPolicy{}.CanWrite(role) || role == auth.RoleViewer
}

// CanWrite implements Policy.
func (DefaultPolicy) CanWrite(role string) bool {
	return auth.CanManageMembers(role) || role == auth.RoleFamilyMember
}

// Handler serves /api/v1/budget. Ids from another household answer 404.
type Handler struct {
	repo   *Repo
	policy Policy
	now    func() time.Time
}

// NewHandler creates a budget handler with DefaultPolicy.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo, policy: DefaultPolicy{}, now: time.Now}
}

// WithNow replaces the clock, so a test can pin "today" (the due-soon window
// and the month default both come from it). The production constructor uses
// time.Now.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// WithPolicy replaces the access policy (for #4689's household role policy).
func (h *Handler) WithPolicy(p Policy) *Handler {
	h.policy = p
	return h
}

// RegisterRoutes mounts the budget routes. The router must already run
// middleware.RequireAuth.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/budget/categories", h.ListCategories)
	r.Post("/api/v1/budget/categories", h.CreateCategory)
	r.Get("/api/v1/budget/categories/{id}", h.GetCategory)
	r.Put("/api/v1/budget/categories/{id}", h.UpdateCategory)
	r.Delete("/api/v1/budget/categories/{id}", h.DeleteCategory)
	r.Get("/api/v1/budget/obligations", h.Obligations)
	r.Get("/api/v1/budget/due-soon", h.DueSoon)
}

// --- request / response types ---

type createCategoryRequest struct {
	Name          string  `json:"name"`
	MonthlyAmount *string `json:"monthly_amount"`
	Notes         *string `json:"notes"`
}

// updateCategoryRequest changes only what is sent; null clears
// monthly_amount or notes.
type updateCategoryRequest struct {
	Name          *string               `json:"name"`
	MonthlyAmount jsonopt.Field[string] `json:"monthly_amount"`
	Notes         jsonopt.Field[string] `json:"notes"`
}

type categoryResponse struct {
	ID            string  `json:"id"`
	HouseholdID   string  `json:"household_id"`
	Name          string  `json:"name"`
	MonthlyAmount *string `json:"monthly_amount"`
	Notes         *string `json:"notes"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

func toCategoryResponse(c *Category) categoryResponse {
	return categoryResponse{
		ID:            c.ID.String(),
		HouseholdID:   c.HouseholdID.String(),
		Name:          c.Name,
		MonthlyAmount: c.MonthlyAmount,
		Notes:         c.Notes,
		CreatedAt:     c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// --- caller and access ---

type caller struct {
	householdID uuid.UUID
	userID      uuid.UUID
	role        string
}

// reader resolves the household the request is for and the caller's role
// there, and checks the policy lets them read.
func (h *Handler) reader(w http.ResponseWriter, r *http.Request) (caller, bool) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing household context")
		return caller{}, false
	}
	hid, err1 := uuid.Parse(claims.HouseholdID)
	uid, err2 := uuid.Parse(claims.UserID)
	if err1 != nil || err2 != nil {
		apierr.Forbidden(w, "missing household context")
		return caller{}, false
	}
	role, err := h.repo.MemberRole(r.Context(), hid, uid)
	if err != nil {
		apierr.InternalError(w, err)
		return caller{}, false
	}
	if role == "" {
		apierr.Forbidden(w, "not a member of this household")
		return caller{}, false
	}
	if !h.policy.CanRead(role) {
		apierr.Forbidden(w, "your role can't see the budget")
		return caller{}, false
	}
	return caller{householdID: hid, userID: uid, role: role}, true
}

// moneyReader is reader plus the money grant, for a response that carries
// amounts, rates or account numbers. The route rule already names the
// narrowest action that can succeed; this is the record-level half, asked the
// same way wherever a record a role may otherwise read carries money
// (auth.CanSeeMoney, #4806). The refusal names the role and the resource, as
// every other policy refusal does.
func (h *Handler) moneyReader(w http.ResponseWriter, r *http.Request) (caller, bool) {
	c, ok := h.reader(w, r)
	if !ok {
		return caller{}, false
	}
	if !auth.CanSeeMoney(c.role) {
		apierr.Forbidden(w, "the "+c.role+" role cannot see bills, loans or the budget")
		return caller{}, false
	}
	return c, true
}

// writer resolves the caller and checks they may change the budget.
func (h *Handler) writer(w http.ResponseWriter, r *http.Request, action string) (caller, bool) {
	c, ok := h.reader(w, r)
	if !ok {
		return c, false
	}
	if !h.policy.CanWrite(c.role) {
		slog.Warn("budget: refused "+action, "user_id", c.userID, "household_id", c.householdID, "role", c.role)
		apierr.Forbidden(w, "your role can only view the budget")
		return c, false
	}
	return c, true
}

func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid budget category id")
		return uuid.Nil, false
	}
	return id, true
}

// amountRE matches a NUMERIC(12,2) amount that is not negative.
var amountRE = regexp.MustCompile(`^[0-9]{1,10}(\.[0-9]{1,2})?$`)

// normalizeCategory trims and validates c, returning a client error message
// or "". An empty monthly_amount means "no budget set".
func normalizeCategory(c *Category) string {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return "name is required"
	}
	if utf8.RuneCountInString(c.Name) > 255 {
		return "name must be at most 255 characters"
	}
	if c.MonthlyAmount != nil {
		a := strings.TrimSpace(*c.MonthlyAmount)
		if a == "" {
			c.MonthlyAmount = nil
		} else if !amountRE.MatchString(a) {
			return "monthly_amount must be a non-negative amount like 450 or 450.00"
		} else {
			c.MonthlyAmount = &a
		}
	}
	return ""
}

// writeDBError maps a write failure: the one-name-per-family index to 409,
// everything else as apierr.DB does.
func writeDBError(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		apierr.Conflict(w, "the family already has a budget category with that name")
		return
	}
	apierr.DB(w, err)
}

// --- categories ---

// ListCategories handles GET /api/v1/budget/categories.
func (h *Handler) ListCategories(w http.ResponseWriter, r *http.Request) {
	c, ok := h.reader(w, r)
	if !ok {
		return
	}
	cats, err := h.repo.List(r.Context(), c.householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	out := make([]categoryResponse, 0, len(cats))
	for _, cat := range cats {
		out = append(out, toCategoryResponse(cat))
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": out})
}

// GetCategory handles GET /api/v1/budget/categories/{id}.
func (h *Handler) GetCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, ok := h.reader(w, r)
	if !ok {
		return
	}
	cat, err := h.repo.Get(r.Context(), id, c.householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if cat == nil {
		apierr.NotFound(w, "budget category not found")
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toCategoryResponse(cat)})
}

// CreateCategory handles POST /api/v1/budget/categories.
func (h *Handler) CreateCategory(w http.ResponseWriter, r *http.Request) {
	c, ok := h.writer(w, r, "create budget category")
	if !ok {
		return
	}
	var req createCategoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	cat := &Category{HouseholdID: c.householdID, Name: req.Name, MonthlyAmount: req.MonthlyAmount, Notes: req.Notes}
	if msg := normalizeCategory(cat); msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	created, err := h.repo.Create(r.Context(), cat)
	if err != nil {
		writeDBError(w, err)
		return
	}
	apierr.JSON(w, http.StatusCreated, map[string]any{"data": toCategoryResponse(created)})
}

// UpdateCategory handles PUT /api/v1/budget/categories/{id}. Only the fields
// sent change.
func (h *Handler) UpdateCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, ok := h.writer(w, r, "update budget category")
	if !ok {
		return
	}
	var req updateCategoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	cat, err := h.repo.Get(r.Context(), id, c.householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if cat == nil {
		apierr.NotFound(w, "budget category not found")
		return
	}
	if req.Name != nil {
		cat.Name = *req.Name
	}
	req.MonthlyAmount.Apply(&cat.MonthlyAmount)
	req.Notes.Apply(&cat.Notes)
	if msg := normalizeCategory(cat); msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	updated, err := h.repo.Update(r.Context(), cat)
	if err != nil {
		writeDBError(w, err)
		return
	}
	if updated == nil {
		apierr.NotFound(w, "budget category not found")
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toCategoryResponse(updated)})
}

// DeleteCategory handles DELETE /api/v1/budget/categories/{id}. Its bills and
// loans stay, with no category.
func (h *Handler) DeleteCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, ok := h.writer(w, r, "delete budget category")
	if !ok {
		return
	}
	found, err := h.repo.Delete(r.Context(), id, c.householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if !found {
		apierr.NotFound(w, "budget category not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- monthly obligations ---

type obligationItem struct {
	Kind          string  `json:"kind"`
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Frequency     string  `json:"frequency"`
	Amount        *string `json:"amount"`
	MonthlyAmount *string `json:"monthly_amount"`
	DueDay        *int    `json:"due_day"`
	DueDate       *string `json:"due_date"`
}

type obligationGroup struct {
	// Category is nil for the "no category" group.
	BudgetCategoryID *string          `json:"budget_category_id"`
	Name             string           `json:"name"`
	Budgeted         *string          `json:"budgeted"`
	Committed        string           `json:"committed"`
	Remaining        *string          `json:"remaining"`
	Items            []obligationItem `json:"items"`
}

type obligationTotals struct {
	Budgeted     string `json:"budgeted"`
	Committed    string `json:"committed"`
	Bills        string `json:"bills"`
	LoanPayments string `json:"loan_payments"`
	// Uncounted: bills with no amount and loans with no monthly_payment.
	Uncounted int `json:"uncounted"`
}

type obligationsResponse struct {
	Month      string            `json:"month"`
	Categories []obligationGroup `json:"categories"`
	// Uncategorized holds obligations with no budget category.
	Uncategorized obligationGroup  `json:"uncategorized"`
	Totals        obligationTotals `json:"totals"`
}

// Obligations handles GET /api/v1/budget/obligations?month=YYYY-MM (default:
// this month in the household's timezone). Every category is listed with
// its budget, what bills and loan payments commit against it each month,
// and the difference. Amounts are decimal strings.
func (h *Handler) Obligations(w http.ResponseWriter, r *http.Request) {
	c, ok := h.reader(w, r)
	if !ok {
		return
	}
	month, err := h.month(r, c)
	if err != nil {
		if errors.Is(err, errBadMonth) {
			apierr.BadRequest(w, "month must be YYYY-MM")
			return
		}
		apierr.InternalError(w, err)
		return
	}
	cats, err := h.repo.List(r.Context(), c.householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	obs, err := h.repo.Obligations(r.Context(), c.householdID, month)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": buildObligations(month, cats, obs)})
}

var errBadMonth = errors.New("bad month")

func (h *Handler) month(r *http.Request, c caller) (time.Time, error) {
	if q := r.URL.Query().Get("month"); q != "" {
		t, err := time.Parse("2006-01", q)
		if err != nil {
			return time.Time{}, errBadMonth
		}
		return t, nil
	}
	name, err := h.repo.HouseholdTimezone(r.Context(), c.householdID)
	if err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		slog.Warn("budget: bad household timezone, using UTC", "household_id", c.householdID, "timezone", name)
		loc = time.UTC
	}
	y, m, _ := h.now().In(loc).Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC), nil
}

// buildObligations groups obs by category (in cats' order) and totals them
// in cents.
func buildObligations(month time.Time, cats []*Category, obs []Obligation) obligationsResponse {
	type acc struct {
		group     obligationGroup
		budget    *int64
		committed int64
	}
	byID := map[uuid.UUID]*acc{}
	order := make([]*acc, 0, len(cats))
	for _, cat := range cats {
		id := cat.ID.String()
		a := &acc{group: obligationGroup{BudgetCategoryID: &id, Name: cat.Name, Items: []obligationItem{}}}
		if cat.MonthlyAmount != nil {
			if cents, ok := parseCents(*cat.MonthlyAmount); ok {
				a.budget = &cents
			}
		}
		byID[cat.ID] = a
		order = append(order, a)
	}
	none := &acc{group: obligationGroup{Name: "No category", Items: []obligationItem{}}}

	var totals obligationTotals
	var bills, loans, budgeted int64
	for _, o := range obs {
		a := none
		if o.BudgetCategoryID != nil {
			if found := byID[*o.BudgetCategoryID]; found != nil {
				a = found
			}
		}
		item := obligationItem{
			Kind: o.Kind, ID: o.ID.String(), Name: o.Name, Frequency: o.Frequency,
			Amount: centsPtr(o.AmountCents), MonthlyAmount: centsPtr(o.MonthlyCents),
			DueDay: o.DueDay,
		}
		if o.DueDate != nil {
			s := o.DueDate.Format("2006-01-02")
			item.DueDate = &s
		}
		a.group.Items = append(a.group.Items, item)
		if o.MonthlyCents == nil {
			totals.Uncounted++
			continue
		}
		a.committed += *o.MonthlyCents
		if o.Kind == "loan" {
			loans += *o.MonthlyCents
		} else {
			bills += *o.MonthlyCents
		}
	}

	finish := func(a *acc) obligationGroup {
		g := a.group
		g.Committed = formatCents(a.committed)
		if a.budget != nil {
			g.Budgeted = centsPtr(a.budget)
			rem := *a.budget - a.committed
			g.Remaining = centsPtr(&rem)
			budgeted += *a.budget
		}
		return g
	}
	out := obligationsResponse{Month: month.Format("2006-01"), Categories: make([]obligationGroup, 0, len(order))}
	for _, a := range order {
		out.Categories = append(out.Categories, finish(a))
	}
	out.Uncategorized = finish(none)
	totals.Budgeted = formatCents(budgeted)
	totals.Bills = formatCents(bills)
	totals.LoanPayments = formatCents(loans)
	totals.Committed = formatCents(bills + loans)
	out.Totals = totals
	return out
}

// parseCents parses a validated decimal amount ("450", "450.5", "450.05").
func parseCents(s string) (int64, bool) {
	whole, frac, _ := strings.Cut(s, ".")
	var w, f int64
	if _, err := fmt.Sscanf(whole, "%d", &w); err != nil {
		return 0, false
	}
	if frac != "" {
		if len(frac) == 1 {
			frac += "0"
		}
		if len(frac) != 2 {
			return 0, false
		}
		if _, err := fmt.Sscanf(frac, "%d", &f); err != nil {
			return 0, false
		}
	}
	return w*100 + f, true
}

func formatCents(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

func centsPtr(c *int64) *string {
	if c == nil {
		return nil
	}
	s := formatCents(*c)
	return &s
}
