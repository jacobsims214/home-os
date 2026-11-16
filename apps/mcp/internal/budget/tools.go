// Package budget provides the family's budget categories and the monthly
// obligations view for MCP (catalog v2, KB architecture/mcp-tool-catalog.md
// "budget"; ticket #4470):
//
//   - budget_set_category: set the monthly budget of a category, creating
//     the category if it is new.
//   - Load: every budget category with what bills and loan payments commit
//     against it each month, line by line with each line's source. It is
//     budget_summary's "budget" section (finance).
//   - LoadDueSoon: what falls due in the next N days. It is budget_summary's
//     "due_soon" section.
//   - CallerOf / CanRead / CanWrite: the money role policy shared with
//     budget_summary.
//
// Budgeting metadata only: no transactions and no ledger. The monthly math
// is the API's (apps/api/internal/budget Repo.Obligations, KB
// api/budget-categories-obligations.md), so the app and the agent give the
// same numbers. Amounts are decimal strings ("1850.00") with currency "USD",
// summed as int64 cents.
package budget

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/bill"
	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// Currency is the currency of every amount. Households have no currency
// column yet (same constant as the loan tools).
const Currency = "USD"

// querier is what both the pool and a transaction offer.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ---------------------------------------------------------------------------
// Access (household role policy, KB decisions/2026-11-15-role-policy.md)
// ---------------------------------------------------------------------------

// Caller is who is calling, with the role re-read from memberships.
type Caller struct {
	HouseholdID string
	Role        string
}

// CallerOf resolves the caller's current role the way the loan and vehicle
// tools do: re-read from memberships on every call, so a demoted member
// loses access at once and a removed one gets "not signed in". A non-nil
// result is the isError to return.
func CallerOf(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}) (Caller, *mcp.CallToolResult) {
	c := server.ClaimsFromContext(ctx)
	if c == nil {
		return Caller{}, notSignedIn()
	}
	hid, err := uuid.Parse(c.HouseholdID)
	if err != nil {
		return Caller{}, notSignedIn()
	}
	out := Caller{HouseholdID: hid.String(), Role: c.Role}
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.AgentTokenID != "" { // an agent token always resolves to a users.id
			return Caller{}, notSignedIn()
		}
		// Local-dev claims without a user: keep the claims' role.
		var ok bool
		err = q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM households WHERE id = $1)`, hid).Scan(&ok)
		if err == nil && !ok {
			return Caller{}, notSignedIn()
		}
	} else {
		err = q.QueryRow(ctx, `SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
			hid, uid).Scan(&out.Role)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Caller{}, notSignedIn()
	}
	if err != nil {
		return Caller{}, respond.Failed("check your access", err)
	}
	return out, nil
}

// CanRead reports whether role may see budgets and other money: the family
// roles and viewer. housesitter, vendor and unknown roles may not (fails
// closed), as in the API's budget.DefaultPolicy.
func CanRead(role string) bool { return CanWrite(role) || role == "viewer" }

// CanWrite reports whether role may change budgets: the family roles.
func CanWrite(role string) bool {
	return role == "owner" || role == "family_manager" || role == "family_member"
}

func roleLabel(role string) string {
	if role == "" {
		return "current"
	}
	return role
}

// CannotRead is the refusal for a role that may not see money.
func CannotRead(role string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't see the budget, bills, loans or other money: only an owner, family manager, family member or viewer can.", roleLabel(role)),
		Fix:     "Tell the user money is not shared with their role; retrying will not help.",
	})
}

func cannotWrite(role, tool string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't change the budget (tool %s): only an owner, family manager or family member can.", roleLabel(role), tool),
		Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
	})
}

func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a family.",
		Fix:     "Reconnect with a valid Home OS token, then call the tool again.",
	})
}

// ---------------------------------------------------------------------------
// Money
// ---------------------------------------------------------------------------

// FormatCents is the API's decimal string: 185000 -> "1850.00".
func FormatCents(c int64) string {
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
	s := FormatCents(*c)
	return &s
}

// Money is how summaries print an amount: 185000 -> "$1,850.00".
func Money(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	whole := strconv.FormatInt(c/100, 10)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return fmt.Sprintf("%s$%s.%02d", sign, b.String(), c%100)
}

// maxAmountCents is the largest NUMERIC(12,2).
const maxAmountCents = 999_999_999_999

// parseCents reads a money argument as cents: a number, or text such as
// "$1,850.00", "1850" or "600 USD". Rounded to the cent. ok is false when
// the argument is absent, null or blank.
func parseCents(raw any) (cents int64, ok bool, err error) {
	var v float64
	switch n := raw.(type) {
	case nil:
		return 0, false, nil
	case float64:
		v = n
	case int:
		v = float64(n)
	case int64:
		v = float64(n)
	case string:
		s := strings.TrimSpace(strings.NewReplacer("$", "", ",", "", "USD", "", "usd", "", " ", "").Replace(n))
		if s == "" {
			return 0, false, nil
		}
		f, perr := strconv.ParseFloat(s, 64)
		if perr != nil {
			return 0, false, fmt.Errorf("%q is not an amount", n)
		}
		v = f
	default:
		return 0, false, fmt.Errorf("%v is not an amount", raw)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false, errors.New("the amount must be a finite number")
	}
	c := int64(math.Round(v * 100))
	if c < 0 {
		return 0, false, errors.New("the amount can't be negative")
	}
	if c > maxAmountCents {
		return 0, false, errors.New("the amount is too large")
	}
	return c, true, nil
}

// ---------------------------------------------------------------------------
// Load: budget categories and the month's obligations (budget section)
// ---------------------------------------------------------------------------

// Line is one bill or loan payment in a month.
type Line struct {
	Source        string  `json:"source" jsonschema:"enum=bill,enum=loan" jsonschema_description:"Where this line comes from: a bill (bill_find) or a loan payment (loan_find)"`
	ID            string  `json:"id" jsonschema_description:"The bill_id or loan_id"`
	Name          string  `json:"name"`
	BillCategory  *string `json:"bill_category,omitempty" jsonschema_description:"The bill's own category text (e.g. Utilities); absent for loans"`
	Frequency     string  `json:"frequency" jsonschema_description:"monthly, quarterly, semiannual, annual or once (loans are monthly)"`
	Amount        *string `json:"amount" jsonschema_description:"The bill amount per occurrence, or the loan's monthly payment, decimal string"`
	MonthlyAmount *string `json:"monthly_amount" jsonschema_description:"What it costs this month (quarterly /3, semiannual /6, annual /12, once in full in its due month). Null when the amount is unknown: listed but not counted"`
	DueDay        *int    `json:"due_day,omitempty" jsonschema_description:"Day of the month a monthly bill or loan payment is due"`
	DueDate       *string `json:"due_date,omitempty" jsonschema_description:"Anchor due date of a non-monthly bill, YYYY-MM-DD"`
}

// MaxLines is how many lines each category lists, largest monthly amount
// first; committed and the totals always cover every line.
const MaxLines = 8

// MaxDueItems is how many due_soon items are listed, earliest first; total
// and the counts always cover every item.
const MaxDueItems = 20

// CategoryLines is one budget category (or the no-category group) with its
// budget, what is committed against it this month, and the lines behind it.
type CategoryLines struct {
	BudgetCategoryID *string `json:"budget_category_id" jsonschema_description:"Null for the group of lines with no budget category"`
	Name             string  `json:"name"`
	Notes            *string `json:"notes"`
	Budgeted         *string `json:"budgeted" jsonschema_description:"Monthly budget, decimal string; null when no budget is set"`
	Committed        string  `json:"committed" jsonschema_description:"Sum of the lines' monthly_amount"`
	Remaining        *string `json:"remaining" jsonschema_description:"budgeted - committed (negative when over budget); null when no budget is set"`
	OverBudget       bool    `json:"over_budget"`
	LineCount        int     `json:"line_count" jsonschema_description:"How many bills and loan payments are in this category this month (whether or not their lines are listed)"`
	Lines            []Line  `json:"lines,omitempty" jsonschema_description:"The bill and loan lines behind committed: absent at detail=summary, the largest at detail=lines, all at detail=full. See more_lines"`
	MoreLines        int     `json:"more_lines,omitempty" jsonschema_description:"How many of line_count are not listed here. Their money is in committed and the totals; get them with detail=lines or detail=full, or bill_find/loan_find"`
}

// Totals adds every line (and every budget) of the month.
type Totals struct {
	Budgeted     string `json:"budgeted" jsonschema_description:"Sum of every category's monthly budget"`
	Committed    string `json:"committed" jsonschema_description:"bills + loan_payments: what the family owes each month"`
	Bills        string `json:"bills"`
	LoanPayments string `json:"loan_payments"`
	BillCount    int    `json:"bill_count"`
	LoanCount    int    `json:"loan_count"`
	Uncounted    int    `json:"uncounted" jsonschema_description:"Bills with no amount and loans with no monthly_payment: listed, not counted"`
}

// Report is the month's budget: every category in name order, the lines
// with no category, and totals.
type Report struct {
	Month         string          `json:"month" jsonschema_description:"YYYY-MM"`
	Currency      string          `json:"currency"`
	Categories    []CategoryLines `json:"categories" jsonschema_description:"Every budget category, by name, even with no lines"`
	Uncategorized CategoryLines   `json:"uncategorized" jsonschema_description:"Bills and loans not in a budget category"`
	Totals        Totals          `json:"totals"`

	// cents, for summaries and the set tool.
	committedCents int64
	budgetedCents  int64
	catCount       int

	// narrowedTo is the category Narrow kept, empty for a whole-household
	// report. Describe uses it so a one-category answer never reads as the
	// household's totals (#4809).
	narrowedTo string

	// narrowedDroppedLines is how many lines of the other categories Narrow
	// removed. It is what a caller must count as "not listed" so a
	// one-category answer never reports that nothing was left out (#4809).
	narrowedDroppedLines int
}

// CommittedCents is Totals.Committed in cents.
func (r *Report) CommittedCents() int64 { return r.committedCents }

// BudgetedCents is Totals.Budgeted in cents.
func (r *Report) BudgetedCents() int64 { return r.budgetedCents }

// obligationsSQL is the API's Repo.Obligations query (apps/api/internal/budget
// repo.go) plus the bill's category text. Keep the two in step.
const obligationsSQL = `
SELECT 'bill', id, name, budget_category_id, frequency, category,
       ROUND(amount * 100)::bigint,
       ROUND(CASE frequency
               WHEN 'monthly'    THEN amount
               WHEN 'quarterly'  THEN amount / 3
               WHEN 'semiannual' THEN amount / 6
               WHEN 'annual'     THEN amount / 12
               WHEN 'once'       THEN CASE WHEN due_date >= $2 AND due_date < $3 THEN amount ELSE 0 END
             END * 100)::bigint,
       due_day, due_date
  FROM bills
 WHERE household_id = $1
   AND (frequency <> 'once' OR (due_date >= $2 AND due_date < $3))
UNION ALL
SELECT 'loan', id, name, budget_category_id, 'monthly', NULL::varchar,
       ROUND(monthly_payment * 100)::bigint,
       ROUND(monthly_payment * 100)::bigint,
       due_day, NULL::date
  FROM loans
 WHERE household_id = $1
   AND remaining_balance > 0
   AND (start_date IS NULL OR start_date < $3)
   AND (term_end IS NULL OR term_end >= $2)
ORDER BY 3, 1, 2`

type category struct {
	id     uuid.UUID
	name   string
	amount *int64
	notes  *string
}

func listCategories(ctx context.Context, q querier, householdID string) ([]category, error) {
	rows, err := q.Query(ctx,
		`SELECT id, name, ROUND(monthly_amount * 100)::bigint, notes
		   FROM budget_categories WHERE household_id = $1 ORDER BY lower(name), id`, householdID)
	if err != nil {
		return nil, fmt.Errorf("list budget categories: %w", err)
	}
	defer rows.Close()
	var out []category
	for rows.Next() {
		var c category
		if err := rows.Scan(&c.id, &c.name, &c.amount, &c.notes); err != nil {
			return nil, fmt.Errorf("scan budget category: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Depth is how much line detail a Report carries (#4809). The totals, the
// category rows and the committed-per-category figures are the same at every
// depth; only the lines behind them change, because they are what made the
// payload large.
type Depth int

const (
	// DepthSummary lists no lines: every category with budgeted, committed,
	// remaining, over_budget, line_count, and more_lines counting all of
	// them. It answers "what do we owe each month, and where?" for a small
	// model without the per-bill detail.
	DepthSummary Depth = iota
	// DepthLines lists up to MaxLines lines per category, the largest first.
	DepthLines
	// DepthFull lists every line of every category.
	DepthFull
)

// maxLinesAt is how many lines a category lists at depth.
func (d Depth) maxLinesAt() int {
	switch d {
	case DepthSummary:
		return 0
	case DepthFull:
		return math.MaxInt
	default:
		return MaxLines
	}
}

// Load builds the month's Report for the household with up to MaxLines lines
// per category. It is LoadDepth with DepthLines.
func Load(ctx context.Context, q querier, householdID string, month time.Time) (*Report, error) {
	return LoadDepth(ctx, q, householdID, month, DepthLines)
}

// LoadDepth builds the month's Report for the household at the given depth.
// month's year and month are used. Bills are spread to monthly amounts
// (x1, /3, /6, /12; a once bill in full in its due month only), each rounded
// to the cent; a loan counts while active in the month (balance left, started
// by month end, not ended before it). Same math as the API and
// budget_summary's obligations. The totals always cover every line at every
// depth.
func LoadDepth(ctx context.Context, q querier, householdID string, month time.Time, depth Depth) (*Report, error) {
	start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	cats, err := listCategories(ctx, q, householdID)
	if err != nil {
		return nil, err
	}

	type acc struct {
		group     CategoryLines
		budget    *int64
		committed int64
	}
	byID := map[uuid.UUID]*acc{}
	order := make([]*acc, 0, len(cats))
	for _, c := range cats {
		id := c.id.String()
		a := &acc{group: CategoryLines{BudgetCategoryID: &id, Name: c.name, Notes: c.notes, Lines: []Line{}}, budget: c.amount}
		byID[c.id] = a
		order = append(order, a)
	}
	none := &acc{group: CategoryLines{Name: "No category", Lines: []Line{}}}

	rows, err := q.Query(ctx, obligationsSQL, householdID, start, start.AddDate(0, 1, 0))
	if err != nil {
		return nil, fmt.Errorf("budget obligations: %w", err)
	}
	defer rows.Close()
	r := &Report{Month: start.Format("2006-01"), Currency: Currency, catCount: len(cats)}
	var bills, loans int64
	for rows.Next() {
		var (
			kind, name, freq string
			id               uuid.UUID
			catID            *uuid.UUID
			billCat          *string
			amount, monthly  *int64
			dueDay           *int
			dueDate          *time.Time
		)
		if err := rows.Scan(&kind, &id, &name, &catID, &freq, &billCat, &amount, &monthly, &dueDay, &dueDate); err != nil {
			return nil, fmt.Errorf("scan obligation: %w", err)
		}
		a := none
		if catID != nil {
			if found := byID[*catID]; found != nil {
				a = found
			}
		}
		line := Line{Source: kind, ID: id.String(), Name: name, BillCategory: billCat, Frequency: freq,
			Amount: centsPtr(amount), MonthlyAmount: centsPtr(monthly), DueDay: dueDay}
		if dueDate != nil {
			s := dueDate.Format(time.DateOnly)
			line.DueDate = &s
		}
		a.group.Lines = append(a.group.Lines, line)
		if kind == "loan" {
			r.Totals.LoanCount++
		} else {
			r.Totals.BillCount++
		}
		if monthly == nil {
			r.Totals.Uncounted++
			continue
		}
		a.committed += *monthly
		if kind == "loan" {
			loans += *monthly
		} else {
			bills += *monthly
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("budget obligations: %w", err)
	}

	finish := func(a *acc) CategoryLines {
		g := a.group
		g.Committed = FormatCents(a.committed)
		g.LineCount = len(g.Lines)
		max := depth.maxLinesAt()
		if max == 0 {
			// detail=summary: no lines at all, and no empty "lines": [] either
			// (#4809). more_lines still counts them.
			g.Lines, g.MoreLines = nil, len(g.Lines)
		} else {
			g.Lines, g.MoreLines = largestLines(g.Lines, max)
		}
		if a.budget != nil {
			g.Budgeted = centsPtr(a.budget)
			rem := *a.budget - a.committed
			g.Remaining = centsPtr(&rem)
			g.OverBudget = rem < 0
			r.budgetedCents += *a.budget
		}
		return g
	}
	r.Categories = make([]CategoryLines, 0, len(order))
	for _, a := range order {
		r.Categories = append(r.Categories, finish(a))
	}
	r.Uncategorized = finish(none)
	r.committedCents = bills + loans
	r.Totals.Budgeted = FormatCents(r.budgetedCents)
	r.Totals.Bills = FormatCents(bills)
	r.Totals.LoanPayments = FormatCents(loans)
	r.Totals.Committed = FormatCents(r.committedCents)
	return r, nil
}

// largestLines keeps the max lines with the largest monthly amount (lines
// that are not counted last), ties by name, and says how many were dropped.
func largestLines(lines []Line, max int) ([]Line, int) {
	monthly := func(l Line) int64 {
		if l.MonthlyAmount == nil {
			return -1
		}
		c, _, _ := parseCents(*l.MonthlyAmount)
		return c
	}
	sort.SliceStable(lines, func(i, j int) bool {
		a, b := monthly(lines[i]), monthly(lines[j])
		if a != b {
			return a > b
		}
		return strings.ToLower(lines[i].Name) < strings.ToLower(lines[j].Name)
	})
	if len(lines) <= max {
		return lines, 0
	}
	return lines[:max], len(lines) - max
}

// describeNarrowed is Describe for a report narrowed to one category: only
// that category's budget, committed and remaining, never the household's
// totals, so the sentence cannot be read as the whole picture (#4809).
func (r *Report) describeNarrowed() string {
	var g *CategoryLines
	if len(r.Categories) == 1 {
		g = &r.Categories[0]
	} else {
		g = &r.Uncategorized
	}
	committed, _, _ := parseCents(g.Committed)
	if g.Budgeted == nil {
		return fmt.Sprintf("%s: %s a month from %s, no monthly budget set.",
			g.Name, Money(committed), plural(g.LineCount, "bill or loan payment", "bills or loan payments"))
	}
	budgeted, _, _ := parseCents(*g.Budgeted)
	s := fmt.Sprintf("%s: budget %s, %s committed against it", g.Name, Money(budgeted), Money(committed))
	if g.OverBudget {
		rem, _, _ := parseCents(strings.TrimPrefix(*g.Remaining, "-"))
		s += fmt.Sprintf(", over budget by %s", Money(rem))
	} else {
		rem, _, _ := parseCents(*g.Remaining)
		s += fmt.Sprintf(", %s left", Money(rem))
	}
	return s + fmt.Sprintf(" (%s in the category).", plural(g.LineCount, "bill or loan payment", "bills or loan payments"))
}

// maxNamedOverBudget is how many over-budget categories Describe names before
// it says "and N more".
const maxNamedOverBudget = 3

// Describe is the report's summary phrase, e.g. "Budgets: $2,500.00 a month
// across 3 categories, $2,150.00 committed against them; over budget:
// Housing by $120.00." or "No budget categories set yet."
func (r *Report) Describe() string {
	if r.narrowedTo != "" {
		return r.describeNarrowed()
	}
	if r.catCount == 0 {
		return "No budget categories set yet."
	}
	var withBudget int
	var over []string
	var overCount int
	var inCents int64
	for _, c := range r.Categories {
		if c.Budgeted == nil {
			continue
		}
		withBudget++
		cc, _, _ := parseCents(c.Committed)
		inCents += cc
		if c.OverBudget {
			overCount++
			// Only the first few by name: the sentence stays readable for a
			// small model, and categories carries all of them (#4809).
			if len(over) < maxNamedOverBudget {
				rem, _, _ := parseCents(strings.TrimPrefix(*c.Remaining, "-"))
				over = append(over, fmt.Sprintf("%s by %s", c.Name, Money(rem)))
			}
		}
	}
	s := fmt.Sprintf("Budgets: %s a month across %s, %s committed against them",
		Money(r.budgetedCents), plural(withBudget, "budgeted category", "budgeted categories"), Money(inCents))
	if withBudget == 0 {
		s = fmt.Sprintf("%s, none with a monthly budget", plural(r.catCount, "budget category", "budget categories"))
	}
	if len(over) > 0 {
		s += "; over budget: " + strings.Join(over, ", ")
		if more := overCount - len(over); more > 0 {
			s += fmt.Sprintf(" and %d more", more)
		}
	}
	if n := r.Uncategorized.LineCount; n > 0 {
		s += fmt.Sprintf("; %s not in a budget category", plural(n, "line", "lines"))
	}
	return s + "."
}

// Narrow returns a copy of the report with only the named category kept (any
// case or spacing). The totals still cover the whole month: the other
// categories' money is what the totals are, and dropping it would answer a
// different question than the numbers claim to (#4809). A name that matches
// neither a category nor the no-category group returns nil, so the caller can
// refuse the call rather than report zero.
func (r *Report) Narrow(name string) *Report {
	key := normName(name)
	out := *r
	out.Categories = nil
	out.narrowedTo = name
	out.narrowedDroppedLines = 0
	found := false
	for _, c := range r.Categories {
		if normName(c.Name) == key {
			out.Categories = []CategoryLines{c}
			out.narrowedTo = c.Name // the stored spelling, for the scope sentence
			found = true
			continue
		}
		out.narrowedDroppedLines += c.LineCount
	}
	if normName(r.Uncategorized.Name) == key {
		// The no-category group: keep it whole and show no budget categories.
		out.Categories = []CategoryLines{}
		out.Uncategorized = r.Uncategorized
		return &out
	}
	if !found {
		return nil
	}
	// Keep the totals covering every line (they are the month's, not this
	// group's) and drop the other groups' lines from the payload.
	out.narrowedDroppedLines += r.Uncategorized.LineCount
	out.Uncategorized = CategoryLines{Name: r.Uncategorized.Name, Committed: FormatCents(0), Lines: []Line{}}
	return &out
}

// NarrowedTo is the category Narrow kept, empty for a whole-household report.
func (r *Report) NarrowedTo() string { return r.narrowedTo }

// DroppedLines is how many lines Narrow removed from the other categories.
// Zero for a whole-household report.
func (r *Report) DroppedLines() int { return r.narrowedDroppedLines }

// CategoryNames lists the categories the report has, in order, for error
// messages that tell a small model what names it may pass.
func (r *Report) CategoryNames() []string {
	names := make([]string, 0, len(r.Categories)+1)
	for _, c := range r.Categories {
		names = append(names, c.Name)
	}
	if r.Uncategorized.LineCount > 0 {
		names = append(names, r.Uncategorized.Name)
	}
	return names
}

// find returns the category with name (any case/spacing), or nil.
func (r *Report) find(name string) *CategoryLines {
	key := normName(name)
	for i := range r.Categories {
		if normName(r.Categories[i].Name) == key {
			return &r.Categories[i]
		}
	}
	return nil
}

func normName(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// ---------------------------------------------------------------------------
// LoadDueSoon: what falls due in the next N days (due_soon section)
// ---------------------------------------------------------------------------

// DueItem is one bill or loan payment that falls due in the window.
type DueItem struct {
	Source     string  `json:"source" jsonschema:"enum=bill,enum=loan"`
	ID         string  `json:"id" jsonschema_description:"The bill_id or loan_id"`
	Name       string  `json:"name"`
	Amount     *string `json:"amount" jsonschema_description:"What is due on that date, decimal string; null when unknown"`
	DueDate    string  `json:"due_date" jsonschema_description:"YYYY-MM-DD"`
	DueWeekday string  `json:"due_weekday" jsonschema_description:"Mon..Sun"`
	DaysUntil  int     `json:"days_until_due" jsonschema_description:"Whole days from today (family timezone)"`
	Autopay    bool    `json:"autopay"`
}

// DueSoon is everything that falls due from today through today + Days.
// Each bill and loan appears once, at its next unpaid due date (the same
// date bill_find and loan_find report).
type DueSoon struct {
	Days        int       `json:"days"`
	From        string    `json:"from" jsonschema_description:"Today in the family's timezone, YYYY-MM-DD"`
	To          string    `json:"to"`
	Currency    string    `json:"currency"`
	Total       string    `json:"total" jsonschema_description:"Sum of the items' amounts"`
	BillCount   int       `json:"bill_count"`
	LoanCount   int       `json:"loan_count"`
	Items       []DueItem `json:"items,omitempty" jsonschema_description:"By due date, then name. The first 5 at detail=summary, 20 at detail=lines, all at detail=full; see more_items"`
	MoreItems   int       `json:"more_items,omitempty" jsonschema_description:"Items not listed; find bills with bill_find due_within_days"`
	NoDueDate   int       `json:"without_due_date" jsonschema_description:"Bills and active loans with no usable due date (not checked)"`
	totalCents  int64
	uncountable int
}

// TotalCents is Total in cents.
func (d *DueSoon) TotalCents() int64 { return d.totalCents }

// MaxSummaryDueItems is how many due_soon items DepthSummary lists: enough to
// answer "what's due next?" in one small call (#4809).
const MaxSummaryDueItems = 5

// maxDueItemsAt is how many due_soon items are listed at depth.
func (d Depth) maxDueItemsAt() int {
	switch d {
	case DepthSummary:
		return MaxSummaryDueItems
	case DepthFull:
		return math.MaxInt
	default:
		return MaxDueItems
	}
}

// LoadDueSoon lists up to MaxDueItems of the household's bills and loan
// payments due in the window. It is LoadDueSoonDepth with DepthLines.
func LoadDueSoon(ctx context.Context, q querier, householdID string, today time.Time, days int) (*DueSoon, error) {
	return LoadDueSoonDepth(ctx, q, householdID, today, days, DepthLines)
}

// LoadDueSoonDepth lists the household's bills (by bill.BillDueFor, so paid
// bills move on to their next date and once bills that passed unpaid are
// left out, as bill_find due_within_days does) and active loans' payments
// (on due_day, clamped to short months) due from today to today + days.
// today is the family's date as midnight UTC (bill.HouseholdToday).
//
// The counts and the total always cover every item in the window at every
// depth; depth decides how many items are listed (and so how big the
// payload is) and MoreItems says how many were not.
func LoadDueSoonDepth(ctx context.Context, q querier, householdID string, today time.Time, days int, depth Depth) (*DueSoon, error) {
	end := today.AddDate(0, 0, days)
	out := &DueSoon{Days: days, From: today.Format(time.DateOnly), To: end.Format(time.DateOnly), Currency: Currency, Items: []DueItem{}}

	rows, err := q.Query(ctx,
		`SELECT id, name, ROUND(amount * 100)::bigint, frequency, due_day, due_date, paid_date, paid_for_due_date, COALESCE(is_autopay, false)
		   FROM bills WHERE household_id = $1`, householdID)
	if err != nil {
		return nil, fmt.Errorf("due soon bills: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id                     uuid.UUID
			name, freq             string
			amount                 *int64
			dueDay                 *int
			dueDate, paid, paidFor *time.Time
			autopay                bool
		)
		if err := rows.Scan(&id, &name, &amount, &freq, &dueDay, &dueDate, &paid, &paidFor, &autopay); err != nil {
			return nil, fmt.Errorf("scan bill: %w", err)
		}
		due, ok := bill.BillDueFor(today, bill.Schedule{Frequency: freq, DueDay: dueDay, DueDate: dueDate}, paid, paidFor)
		if !ok {
			out.NoDueDate++
			continue
		}
		if due.Next.IsZero() || due.DaysUntil < 0 || due.DaysUntil > days {
			continue
		}
		out.add(DueItem{Source: "bill", ID: id.String(), Name: name, Amount: centsPtr(amount), Autopay: autopay}, due.Next, today, amount)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("due soon bills: %w", err)
	}

	lrows, err := q.Query(ctx,
		`SELECT id, name, ROUND(monthly_payment * 100)::bigint, due_day, start_date, term_end
		   FROM loans
		  WHERE household_id = $1 AND remaining_balance > 0
		    AND (start_date IS NULL OR start_date <= $3)
		    AND (term_end IS NULL OR term_end >= $2)`, householdID, today, end)
	if err != nil {
		return nil, fmt.Errorf("due soon loans: %w", err)
	}
	defer lrows.Close()
	for lrows.Next() {
		var (
			id             uuid.UUID
			name           string
			amount         *int64
			dueDay         *int
			start, termEnd *time.Time
		)
		if err := lrows.Scan(&id, &name, &amount, &dueDay, &start, &termEnd); err != nil {
			return nil, fmt.Errorf("scan loan: %w", err)
		}
		if dueDay == nil || *dueDay < 1 || *dueDay > 31 {
			out.NoDueDate++
			continue
		}
		next := NextMonthlyDue(today, *dueDay)
		if start != nil && next.Before(*start) {
			next = NextMonthlyDue(*start, *dueDay)
		}
		if next.After(end) || (termEnd != nil && next.After(*termEnd)) {
			continue
		}
		out.add(DueItem{Source: "loan", ID: id.String(), Name: name, Amount: centsPtr(amount)}, next, today, amount)
	}
	if err := lrows.Err(); err != nil {
		return nil, fmt.Errorf("due soon loans: %w", err)
	}

	sort.SliceStable(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if a.DueDate != b.DueDate {
			return a.DueDate < b.DueDate
		}
		if a.Name != b.Name {
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		return a.ID < b.ID
	})
	out.Total = FormatCents(out.totalCents)
	if max := depth.maxDueItemsAt(); len(out.Items) > max {
		out.MoreItems = len(out.Items) - max
		out.Items = out.Items[:max]
	}
	return out, nil
}

func (d *DueSoon) add(it DueItem, next, today time.Time, amount *int64) {
	it.DueDate = next.Format(time.DateOnly)
	it.DueWeekday = next.Weekday().String()[:3]
	it.DaysUntil = int(next.Sub(today).Hours() / 24)
	if it.Source == "loan" {
		d.LoanCount++
	} else {
		d.BillCount++
	}
	if amount != nil {
		d.totalCents += *amount
	} else {
		d.uncountable++
	}
	d.Items = append(d.Items, it)
}

// Describe is the due-soon summary phrase, e.g. "Due in the next 30 days:
// $2,120.00 (3 bills, 1 loan payment), first Electric $120.00 on Tue Nov 18."
func (d *DueSoon) Describe() string {
	if len(d.Items) == 0 {
		return fmt.Sprintf("Nothing falls due in the next %d days.", d.Days)
	}
	first := d.Items[0]
	amt := ""
	if first.Amount != nil {
		c, _, _ := parseCents(*first.Amount)
		amt = " " + Money(c)
	}
	day, _ := time.Parse(time.DateOnly, first.DueDate)
	return fmt.Sprintf("Due in the next %d days: %s (%s, %s), first %s%s on %s.",
		d.Days, Money(d.totalCents), plural(d.BillCount, "bill", "bills"),
		plural(d.LoanCount, "loan payment", "loan payments"), first.Name, amt, day.Format("Mon Jan 2"))
}

// NextMonthlyDue is the first date on or after today that falls on dueDay of
// its month, clamped to the month's last day (31 is Nov 30): the bill and
// loan rule.
func NextMonthlyDue(today time.Time, dueDay int) time.Time {
	y, m, d := today.Date()
	today = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	c := dayInMonth(y, m, dueDay)
	if c.Before(today) {
		c = dayInMonth(y, m+1, dueDay)
	}
	return c
}

func dayInMonth(year int, month time.Month, day int) time.Time {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > last {
		day = last
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ---------------------------------------------------------------------------
// budget_set_category
// ---------------------------------------------------------------------------

// CategoryResult is budget_set_category's data: the category as stored and
// where it stands this month.
type CategoryResult struct {
	ID            string  `json:"id" jsonschema_description:"The budget category id"`
	Name          string  `json:"name"`
	MonthlyAmount *string `json:"monthly_amount" jsonschema_description:"Monthly budget, decimal string; null when the budget was removed (0)"`
	Notes         *string `json:"notes"`
	Currency      string  `json:"currency"`
	Created       bool    `json:"created" jsonschema_description:"true when the category was new"`
	Month         string  `json:"month" jsonschema_description:"This month in the family's timezone, YYYY-MM"`
	Committed     string  `json:"committed" jsonschema_description:"What bills and loan payments in this category commit this month"`
	Remaining     *string `json:"remaining" jsonschema_description:"monthly_amount - committed; null without a budget"`
	LineCount     int     `json:"line_count" jsonschema_description:"Bills and loans in this category this month"`
}

type setInput struct {
	name     string
	cents    int64
	notes    *string
	notesSet bool
}

// argAliases maps what small models send to the catalog's parameter names.
var argAliases = map[string]string{
	"name": "category", "category_name": "category", "budget_category": "category",
	"amount": "monthly_amount", "budget": "monthly_amount", "monthly": "monthly_amount",
	"monthly_budget": "monthly_amount", "limit": "monthly_amount",
	"note": "notes",
}

func normalizeArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	for alias, key := range argAliases {
		if v, ok := args[alias]; ok {
			if _, has := out[key]; !has {
				out[key] = v
			}
		}
	}
	return out
}

var setExample = map[string]any{"category": "Groceries", "monthly_amount": 600}

func parseSetInput(args map[string]any) (setInput, *mcp.CallToolResult) {
	var in setInput
	args = normalizeArgs(args)
	name, _ := args["category"].(string)
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return in, respond.Invalid("category", "category is required: the name of the budget category, e.g. Groceries.",
			"Pass category as text and monthly_amount as a number.", setExample)
	}
	if utf8.RuneCountInString(name) > 255 {
		return in, respond.Invalid("category", "category must be at most 255 characters.", "Use a short name such as Groceries.", setExample)
	}
	in.name = name

	c, ok, err := parseCents(args["monthly_amount"])
	if err != nil {
		return in, respond.Invalid("monthly_amount", "monthly_amount: "+err.Error()+".",
			"Pass monthly_amount as a number of dollars per month, e.g. 600 or \"$600.00\"; 0 removes the budget.", setExample)
	}
	if !ok {
		return in, respond.Invalid("monthly_amount", "monthly_amount is required: the monthly budget for "+name+".",
			"Pass monthly_amount as a number, e.g. 600; 0 removes the budget but keeps the category.", setExample)
	}
	in.cents = c

	if raw, has := args["notes"]; has {
		in.notesSet = true
		if raw != nil {
			s, isString := raw.(string)
			if !isString {
				return in, respond.Invalid("notes", "notes must be text.", "Pass notes as a string, or \"\" to clear them.", setExample)
			}
			if s = strings.TrimSpace(s); s != "" {
				in.notes = &s
			}
		}
	}
	return in, nil
}

// NewSetCategoryTool returns budget_set_category's name, definition and
// handler. It upserts by name (any case or spacing, the one-name-per-family
// index), keeps the stored spelling of an existing name, and is idempotent:
// the same call twice leaves the same row.
func NewSetCategoryTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("budget_set_category",
		mcp.WithDescription("Set the monthly budget for a category (creates the category if new). "+
			"A monthly_amount of 0 removes the budget but keeps the category. See all with budget_summary."),
		mcp.WithString("category", mcp.Required(), mcp.Description("Category name, e.g. Groceries (matched in any case)")),
		mcp.WithNumber("monthly_amount", mcp.Required(), mcp.Description("Monthly budget in dollars, e.g. 600; 0 removes the budget")),
		mcp.WithString("notes", mcp.Description("Notes about this budget; \"\" clears them, leave out to keep them")),
		respond.Schema[CategoryResult](),
		mcp.WithTitleAnnotation("Set budget category"),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, res := CallerOf(ctx, pool)
		if res != nil {
			return res, nil
		}
		if !CanWrite(c.Role) {
			return cannotWrite(c.Role, "budget_set_category"), nil
		}
		in, res := parseSetInput(req.GetArguments())
		if res != nil {
			return res, nil
		}

		var amount *string
		if in.cents > 0 {
			s := FormatCents(in.cents)
			amount = &s
		}
		var (
			id      uuid.UUID
			name    string
			stored  *string
			notes   *string
			created bool
		)
		err := pool.QueryRow(ctx,
			`INSERT INTO budget_categories (household_id, name, monthly_amount, notes)
			 VALUES ($1, $2, $3::numeric, $4)
			 ON CONFLICT (household_id, lower(btrim(name))) DO UPDATE
			    SET monthly_amount = EXCLUDED.monthly_amount,
			        notes = CASE WHEN $5 THEN EXCLUDED.notes ELSE budget_categories.notes END,
			        updated_at = NOW()
			 RETURNING id, name, monthly_amount::text, notes, (xmax = 0)`,
			c.HouseholdID, in.name, amount, in.notes, in.notesSet,
		).Scan(&id, &name, &stored, &notes, &created)
		if err != nil {
			return respond.Failed("save the budget category", err), nil
		}

		month, err := householdMonth(ctx, pool, c.HouseholdID)
		if err != nil {
			return respond.Failed("load the family's timezone", err), nil
		}
		rep, err := Load(ctx, pool, c.HouseholdID, month)
		if err != nil {
			return respond.Failed("total the category's bills and loan payments", err), nil
		}
		out := CategoryResult{ID: id.String(), Name: name, MonthlyAmount: stored, Notes: notes, Currency: Currency,
			Created: created, Month: rep.Month, Committed: FormatCents(0)}
		if g := rep.find(name); g != nil {
			out.Committed, out.Remaining, out.LineCount = g.Committed, g.Remaining, len(g.Lines)
		}
		return respond.OK(describeSet(out, in.cents), out, setHints(out)...), nil
	}

	return "budget_set_category", tool, handler
}

func describeSet(out CategoryResult, cents int64) string {
	committed, _, _ := parseCents(out.Committed)
	var s string
	switch {
	case cents == 0 && out.Created:
		s = fmt.Sprintf("Created budget category %s with no monthly budget", out.Name)
	case cents == 0:
		s = fmt.Sprintf("Removed the monthly budget of %s (the category stays)", out.Name)
	case out.Created:
		s = fmt.Sprintf("Created budget category %s at %s a month", out.Name, Money(cents))
	default:
		s = fmt.Sprintf("Set the %s budget to %s a month", out.Name, Money(cents))
	}
	s += fmt.Sprintf("; bills and loans in it commit %s this month", Money(committed))
	if out.Remaining != nil {
		rem, _, _ := parseCents(strings.TrimPrefix(*out.Remaining, "-"))
		if strings.HasPrefix(*out.Remaining, "-") {
			s += fmt.Sprintf(", %s over budget", Money(rem))
		} else {
			s += fmt.Sprintf(", leaving %s", Money(rem))
		}
	}
	return s + "."
}

func setHints(out CategoryResult) []string {
	next := []string{"See every category, its budget and what's committed with budget_summary sections=[\"budget\"]."}
	if out.LineCount == 0 {
		next = append(next, "No bills or loans are in "+out.Name+" yet; find them with bill_find or loan_find.")
	}
	return next
}

// householdMonth is the first day of the current month in the family's
// timezone (UTC when unset or unknown).
func householdMonth(ctx context.Context, q querier, householdID string) (time.Time, error) {
	today, err := bill.HouseholdToday(ctx, q, householdID)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC), nil
}
