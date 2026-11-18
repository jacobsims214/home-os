package budget

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"home-os/api/pkg/apierr"
	"home-os/billdue"
)

// GET /api/v1/budget/due-soon — what falls due next (#4819).
//
// The list is not computed here. home-os/billdue.LoadDueSoonRows is the one
// implementation of "what's due soon": the same function fills MCP
// budget_summary's due_soon section, and the due dates inside it come from
// home-os/billdue.BillDueFor, the rule bill_find, the calendar agenda, the
// Bills calendar mirror (home-os/billcal) and family_overview all use. The
// API endpoint therefore cannot disagree with the agent about a due date.
//
// The response follows the small-payload pattern of #4809 (KB
// mcp/small-model-payloads.md): a small default that still answers the
// question, a `detail` parameter for more, and a `coverage` block first in the
// payload so a reader cannot mistake a summary for the whole picture.

// currency is the currency of every amount. Households have no currency column
// yet, so one constant, the same as the MCP budget and loan tools.
const currency = "USD"

// Due-soon listing bounds. The window and the depth both have defaults that
// keep the payload small; both are reachable from the query string.
const (
	// defaultDueSoonDays is the default window: "what's due in the next
	// month", the same default as MCP's due_within_days.
	defaultDueSoonDays = 30
	// maxDueSoonDays bounds the window to a year and a bit, as MCP does.
	maxDueSoonDays = 365
	// maxDueSoonItems caps `detail=full` so one request cannot return an
	// unbounded list. It is far above any real family's month.
	maxDueSoonItems = 500
)

// dueSoonDetail is how many items the response lists. The counts and the total
// always cover every item in the window at every depth; only the item list
// changes, so a summary is complete in its numbers, not a sample (#4809).
type dueSoonDetail int

const (
	// dueSoonSummary lists the first 5 items: an answer to "what's next?"
	// that a UI or a small model can read in one go.
	dueSoonSummary dueSoonDetail = iota
	// dueSoonItems lists up to maxDueSoonItems.
	dueSoonItems
)

// dueSoonSummaryItems is how many items dueSoonSummary lists.
const dueSoonSummaryItems = 5

// maxItems is how many items this depth lists.
func (d dueSoonDetail) maxItems() int {
	if d == dueSoonSummary {
		return dueSoonSummaryItems
	}
	return maxDueSoonItems
}

// parseDueSoonDetail reads the detail parameter: "summary" (the default) or
// "items", in any case.
func parseDueSoonDetail(raw string) (dueSoonDetail, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "summary":
		return dueSoonSummary, nil
	case "items", "full", "lines":
		return dueSoonItems, nil
	}
	return 0, fmt.Errorf("detail must be summary or items")
}

// dueSoonResponse is the whole payload. Coverage comes first in the struct so
// a reader meets the scope before any number.
type dueSoonResponse struct {
	Coverage dueSoonCoverage `json:"coverage"`
	// From and To are the window, in the family's timezone.
	From string `json:"from"`
	To   string `json:"to"`
	// Days is the window width.
	Days int `json:"days"`
	// Currency is the currency of every amount ("USD": households have no
	// currency column yet).
	Currency string `json:"currency"`
	// Total is the sum of every listed item's amount, as a decimal string. It
	// covers all items in the window at every detail level.
	Total string `json:"total"`
	// BillCount and LoanCount count every item in the window, listed or not.
	BillCount int `json:"bill_count"`
	LoanCount int `json:"loan_count"`
	// Items is the next due items, earliest first.
	Items []dueSoonItem `json:"items"`
	// MoreItems is how many items are in the window but not listed.
	MoreItems int `json:"more_items,omitempty"`
}

// dueSoonCoverage says what the payload covers, so a summary cannot be
// mistaken for everything and "nothing is due" cannot be concluded from it.
type dueSoonCoverage struct {
	// Detail is the listing depth that produced this response.
	Detail string `json:"detail"`
	// Scope is a sentence naming what the numbers cover.
	Scope string `json:"scope"`
	// ItemsListed and ItemsNotListed count the list, not the totals: the
	// totals and counts always cover the whole window.
	ItemsListed    int `json:"items_listed"`
	ItemsNotListed int `json:"items_not_listed,omitempty"`
	// WithoutDueDate is how many bills and active loans have no usable due
	// date (no due_day / due_date), so they cannot appear in the window.
	WithoutDueDate int `json:"without_due_date"`
	// HowToGetDetail is one sentence naming the parameter that returns what
	// was left out. It only claims an omission that exists.
	HowToGetDetail string `json:"how_to_get_detail"`
}

// dueSoonItem is one bill or loan payment in the window.
type dueSoonItem struct {
	// Source is "bill" or "loan".
	Source string `json:"source"`
	// ID is the bill id or the loan id.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Amount is what is due on that date as a decimal string, null when the
	// record has none.
	Amount *string `json:"amount"`
	// DueDate is YYYY-MM-DD in the family's timezone; DueWeekday is Mon..Sun.
	DueDate    string `json:"due_date"`
	DueWeekday string `json:"due_weekday"`
	// DaysUntil is the whole days from today to DueDate.
	DaysUntil int `json:"days_until_due"`
	// Status is "due" when that occurrence still needs paying, "paid" when it
	// is already covered and DueDate is the next one.
	Status  string `json:"status"`
	Autopay bool   `json:"autopay"`
	// Subject is what the bill or loan is about, null when it is family-level.
	Subject *dueSoonSubject `json:"subject"`
}

// dueSoonSubject is the property, vehicle or asset a due item belongs to, so
// a UI can group the list ("the car", "the house").
type dueSoonSubject struct {
	// Type is "property", "vehicle" or "asset".
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DueSoon handles GET /api/v1/budget/due-soon.
//
// Query parameters: days (1-365, default 30) and detail (summary|items,
// default summary).
func (h *Handler) DueSoon(w http.ResponseWriter, r *http.Request) {
	c, ok := h.moneyReader(w, r)
	if !ok {
		return
	}
	days, err := dueSoonDays(r)
	if err != nil {
		apierr.BadRequest(w, err.Error())
		return
	}
	detail, err := parseDueSoonDetail(r.URL.Query().Get("detail"))
	if err != nil {
		apierr.BadRequest(w, "detail must be summary (default) or items")
		return
	}
	today, err := h.today(r, c)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	due, err := h.repo.DueSoon(r.Context(), c.householdID, today, days)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": buildDueSoon(due, detail)})
}

// dueSoonDays reads and validates the days parameter.
func dueSoonDays(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("days")
	if raw == "" {
		return defaultDueSoonDays, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("days must be a whole number of days, 1-%d", maxDueSoonDays)
	}
	if n < 1 || n > maxDueSoonDays {
		return 0, fmt.Errorf("days must be between 1 and %d", maxDueSoonDays)
	}
	return n, nil
}

// today is the family's date as midnight UTC: the household's timezone decides
// which day "today" is, exactly as every other due date does (the MCP's
// householdToday). An unknown zone falls back to UTC.
func (h *Handler) today(r *http.Request, c caller) (time.Time, error) {
	name, err := h.repo.HouseholdTimezone(r.Context(), c.householdID)
	if err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = time.UTC
	}
	y, m, d := h.now().In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC), nil
}

// buildDueSoon maps the shared loader's result onto the response, applying the
// detail depth. Everything except the item list is depth-independent.
func buildDueSoon(d *billdue.DueSoon, detail dueSoonDetail) dueSoonResponse {
	items := make([]dueSoonItem, 0, len(d.Items))
	for _, it := range d.Items {
		item := dueSoonItem{
			Source:     it.Source,
			ID:         it.ID.String(),
			Name:       it.Name,
			Amount:     centsPtr(it.AmountCents),
			DueDate:    it.DueDate.Format(time.DateOnly),
			DueWeekday: it.DueDate.Weekday().String()[:3],
			DaysUntil:  it.DaysUntil,
			Status:     it.Status,
			Autopay:    it.Autopay,
		}
		if it.SubjectKind != "" && it.SubjectID != nil {
			name := ""
			if it.SubjectName != nil {
				name = *it.SubjectName
			}
			item.Subject = &dueSoonSubject{Type: it.SubjectKind, ID: it.SubjectID.String(), Name: name}
		}
		items = append(items, item)
	}
	var more int
	if max := detail.maxItems(); len(items) > max {
		more = len(items) - max
		items = items[:max]
	}
	detailName := "summary"
	if detail != dueSoonSummary {
		detailName = "items"
	}
	return dueSoonResponse{
		Coverage: dueSoonCoverage{
			Detail: detailName,
			Scope: "The household's own bills and loan payments falling due from " +
				d.From.Format(time.DateOnly) + " through " + d.To.Format(time.DateOnly) +
				" in the family's timezone. No transactions, bank accounts, other people's records or other households.",
			ItemsListed:    len(items),
			ItemsNotListed: more,
			WithoutDueDate: d.WithoutDueDate,
			HowToGetDetail: dueSoonHowTo(detail, more, len(items)),
		},
		From:      d.From.Format(time.DateOnly),
		To:        d.To.Format(time.DateOnly),
		Days:      d.Days,
		Currency:  currency,
		Total:     formatCents(d.TotalCents),
		BillCount: d.BillCount,
		LoanCount: d.LoanCount,
		Items:     items,
		MoreItems: more,
	}
}

// dueSoonHowTo is the one sentence that says how to see what was left out. It
// claims an omission only when there is one (#4809's rule).
func dueSoonHowTo(detail dueSoonDetail, notListed, listed int) string {
	switch {
	case notListed == 0:
		return "Nothing is left out: every item in the window is listed."
	case detail == dueSoonSummary:
		return fmt.Sprintf("%d items in the window are not listed (their money is in total): pass detail=items to list them.", notListed)
	default:
		return fmt.Sprintf("%d items in the window are not listed (their money is in total): narrow the window with days to list them.", notListed)
	}
}
