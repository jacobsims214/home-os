// Package loan provides the MCP loan tools (catalog v2, KB
// architecture/mcp-tool-catalog.md "loan"): loan_find, loan_create,
// loan_update and loan_delete. Every result is built with internal/respond
// and every tool declares all four annotation hints and an outputSchema.
// All operations are scoped to the authenticated household via JWT claims.
//
// Subjects follow domain model v2 (migrations 038/039): a loan is tied to a
// house, car or thing through the typed property_id / vehicle_id / asset_id
// columns, never the legacy entity_type/entity_id (which these tools no
// longer write; 056 drops them). The old entity_type/entity_id params are
// still accepted, unadvertised, and mapped onto the typed columns.
package loan

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// Pagination limits shared by every catalog find tool.
const (
	defaultLimit = 25
	maxLimit     = 100
)

// Column limits from the loans table (032, 044).
const (
	maxNameLen = 255
	// NUMERIC(12,2) holds at most 9,999,999,999.99.
	maxAmount = 9_999_999_999.99
	// NUMERIC(5,3) holds at most 99.999.
	maxInterestRate = 99.999
	maxTermMonths   = 600
)

// exampleLoanID is the UUID used in error examples.
const exampleLoanID = "7c1e4b2a-9d3f-4e8a-b6c5-2f0a1d9e8b7c"

// toolHandler is the signature every loan tool handler has.
type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// Subject types reported in subject_type.
const (
	subjectProperty = "property"
	subjectVehicle  = "vehicle"
	subjectAsset    = "asset"
)

// loanSelect loads loans with the name of their subject. An asset loan also
// has the asset's property_id (derive_subject_property), so the asset is
// checked first. Scanned by scanLoan.
const loanSelect = `SELECT l.id, l.name, l.lender, l.original_amount::float8, l.remaining_balance::float8,
	       l.interest_rate::float8, l.term_months, l.monthly_payment::float8, l.due_day,
	       l.start_date, l.term_end, l.property_id, l.vehicle_id, l.asset_id, l.notes,
	       l.created_at, l.updated_at,
	       COALESCE(a.name, NULLIF(concat_ws(' ', v.year, v.make, v.model), ''), p.name)
	FROM loans l
	LEFT JOIN properties p ON p.id = l.property_id
	LEFT JOIN vehicles v ON v.id = l.vehicle_id
	LEFT JOIN assets a ON a.id = l.asset_id`

// loanRow is a loans row as the tools load it.
type loanRow struct {
	ID               uuid.UUID
	Name             string
	Lender           *string
	OriginalAmount   float64
	RemainingBalance float64
	InterestRate     *float64
	TermMonths       *int
	MonthlyPayment   *float64
	DueDay           *int
	StartDate        *time.Time
	TermEnd          *time.Time
	PropertyID       *uuid.UUID
	VehicleID        *uuid.UUID
	AssetID          *uuid.UUID
	Notes            *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	SubjectName      *string
}

// scanLoan reads one row of loanSelect.
func scanLoan(row pgx.Row) (loanRow, error) {
	var r loanRow
	err := row.Scan(&r.ID, &r.Name, &r.Lender, &r.OriginalAmount, &r.RemainingBalance,
		&r.InterestRate, &r.TermMonths, &r.MonthlyPayment, &r.DueDay,
		&r.StartDate, &r.TermEnd, &r.PropertyID, &r.VehicleID, &r.AssetID, &r.Notes,
		&r.CreatedAt, &r.UpdatedAt, &r.SubjectName)
	return r, err
}

// loadLoan loads one loan of the household, pgx.ErrNoRows when there is none.
func loadLoan(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, id uuid.UUID, householdID string) (loanRow, error) {
	return scanLoan(q.QueryRow(ctx, loanSelect+` WHERE l.id = $1 AND l.household_id = $2`, id, householdID))
}

// loanData is one loan in every loan tool's output. IDs are strings (a
// uuid.UUID would be advertised as a byte array in the outputSchema).
type loanData struct {
	ID               string    `json:"id" jsonschema_description:"The loan_id to pass to loan_update or loan_delete."`
	Name             string    `json:"name"`
	Lender           *string   `json:"lender"`
	OriginalAmount   float64   `json:"original_amount" jsonschema_description:"Amount originally borrowed."`
	RemainingBalance float64   `json:"remaining_balance" jsonschema_description:"What is still owed."`
	InterestRate     *float64  `json:"interest_rate" jsonschema_description:"Annual rate in percent; 6.25 means 6.25%."`
	TermMonths       *int      `json:"term_months"`
	MonthlyPayment   *float64  `json:"monthly_payment"`
	DueDay           *int      `json:"due_day" jsonschema_description:"Day of the month the payment is due (1-31); a short month uses its last day."`
	StartDate        *string   `json:"start_date" jsonschema_description:"YYYY-MM-DD."`
	TermEnd          *string   `json:"term_end" jsonschema_description:"Last month of the term, YYYY-MM-DD (start_date + term_months)."`
	PropertyID       *string   `json:"property_id" jsonschema_description:"The house or other property the loan is for."`
	VehicleID        *string   `json:"vehicle_id" jsonschema_description:"The car or other vehicle the loan is for."`
	AssetID          *string   `json:"asset_id" jsonschema_description:"The thing the loan is for (its property_id follows the asset)."`
	SubjectType      *string   `json:"subject_type" jsonschema_description:"property, vehicle or asset; null for a loan not tied to anything."`
	SubjectName      *string   `json:"subject_name" jsonschema_description:"Name of the property, vehicle or asset."`
	Notes            *string   `json:"notes"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	PaidOff          bool      `json:"paid_off" jsonschema_description:"true when remaining_balance is 0 or the term has ended."`
	NextDueDate      *string   `json:"next_due_date" jsonschema_description:"Next payment date, YYYY-MM-DD in the family's timezone; null without due_day."`
	NextDueWeekday   *string   `json:"next_due_weekday" jsonschema_description:"Weekday of next_due_date, e.g. Thu."`
	DaysUntilDue     *int      `json:"days_until_due" jsonschema_description:"Whole days from today (family timezone) to next_due_date."`
	Duplicate        bool      `json:"duplicate,omitempty" jsonschema_description:"loan_create only: true when the same loan was created minutes ago and was returned instead."`
}

// data builds the output for r as of today (household-local midnight UTC).
func (r loanRow) data(today time.Time) loanData {
	d := loanData{
		ID: r.ID.String(), Name: r.Name, Lender: r.Lender,
		OriginalAmount: r.OriginalAmount, RemainingBalance: r.RemainingBalance,
		InterestRate: r.InterestRate, TermMonths: r.TermMonths, MonthlyPayment: r.MonthlyPayment,
		DueDay: r.DueDay, PropertyID: uuidString(r.PropertyID), VehicleID: uuidString(r.VehicleID),
		AssetID: uuidString(r.AssetID), SubjectName: r.SubjectName, Notes: r.Notes,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.StartDate != nil {
		d.StartDate = dateOnly(*r.StartDate)
	}
	if r.TermEnd != nil {
		d.TermEnd = dateOnly(*r.TermEnd)
	}
	switch {
	case r.VehicleID != nil:
		d.SubjectType = strPtr(subjectVehicle)
	case r.AssetID != nil:
		d.SubjectType = strPtr(subjectAsset)
	case r.PropertyID != nil:
		d.SubjectType = strPtr(subjectProperty)
	default:
		d.SubjectName = nil
	}
	d.PaidOff = r.RemainingBalance <= 0 || (r.TermEnd != nil && r.TermEnd.Before(today))
	if d.PaidOff || r.DueDay == nil || *r.DueDay < 1 || *r.DueDay > 31 {
		return d
	}
	next := nextDueDate(today, *r.DueDay)
	if r.StartDate != nil && next.Before(*r.StartDate) {
		next = nextDueDate(*r.StartDate, *r.DueDay)
	}
	days := int(next.Sub(today).Hours() / 24)
	d.NextDueDate, d.NextDueWeekday, d.DaysUntilDue = dateOnly(next), weekday(next), &days
	return d
}

// describe is a short phrase for summaries, e.g. "$1,850.00/month due Thu
// Dec 1 at 6.25% with Chase, $250,000.00 left".
func (d loanData) describe() string {
	var parts []string
	if d.MonthlyPayment != nil {
		s := money(*d.MonthlyPayment) + "/month"
		if d.NextDueDate != nil {
			if t, err := time.Parse(time.DateOnly, *d.NextDueDate); err == nil {
				s += " due " + t.Format("Mon Jan 2")
			}
		}
		parts = append(parts, s)
	} else if d.NextDueDate != nil {
		if t, err := time.Parse(time.DateOnly, *d.NextDueDate); err == nil {
			parts = append(parts, "due "+t.Format("Mon Jan 2"))
		}
	}
	if d.InterestRate != nil {
		parts = append(parts, "at "+strconv.FormatFloat(*d.InterestRate, 'f', -1, 64)+"%")
	}
	if d.Lender != nil {
		parts = append(parts, "with "+*d.Lender)
	}
	if d.PaidOff {
		parts = append(parts, "paid off")
	} else {
		parts = append(parts, money(d.RemainingBalance)+" left")
	}
	return strings.Join(parts, ", ")
}

// nextDueDate returns the first date on or after today that falls on dueDay
// of its month, clamped to the month's last day (31 is Nov 30). Same rule as
// bills, so loans and bills agree on what "due on the 31st" means.
func nextDueDate(today time.Time, dueDay int) time.Time {
	y, m, d := today.Date()
	today = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	candidate := dueDateInMonth(y, m, dueDay)
	if candidate.Before(today) {
		candidate = dueDateInMonth(y, m+1, dueDay)
	}
	return candidate
}

// dueDateInMonth returns dueDay of the month, clamped to its last day.
func dueDateInMonth(year int, month time.Month, dueDay int) time.Time {
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if dueDay > lastDay {
		dueDay = lastDay
	}
	return time.Date(year, month, dueDay, 0, 0, 0, 0, time.UTC)
}

// householdToday returns today's date in the household's timezone as
// midnight UTC, so it compares with dates scanned from DATE columns. An
// empty or unknown timezone falls back to UTC.
func householdToday(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, householdID string) (time.Time, error) {
	var tz string
	if err := q.QueryRow(ctx, `SELECT COALESCE(timezone, '') FROM households WHERE id = $1`, householdID).Scan(&tz); err != nil {
		return time.Time{}, fmt.Errorf("load household timezone: %w", err)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	y, m, d := time.Now().In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC), nil
}

func dateOnly(t time.Time) *string {
	s := t.Format(time.DateOnly)
	return &s
}

func weekday(t time.Time) *string {
	s := t.Weekday().String()[:3]
	return &s
}

func strPtr(s string) *string { return &s }

func uuidString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// money formats an amount for summaries with thousands separators, e.g.
// "$250,000.00".
func money(v float64) string {
	neg := v < 0
	s := strconv.FormatFloat(math.Abs(v), 'f', 2, 64)
	whole, frac := s[:len(s)-3], s[len(s)-3:]
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	out := "$" + b.String() + frac
	if neg {
		out = "-" + out
	}
	return out
}

// notSignedIn is the result for a call without household claims.
func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}

// annotations sets all four hints (catalog §1): class R (find), C (create),
// U (update) or X (delete). openWorldHint is always false.
func annotations(title string, readOnly, destructive, idempotent bool) []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithTitleAnnotation(title),
		mcp.WithReadOnlyHintAnnotation(readOnly),
		mcp.WithDestructiveHintAnnotation(destructive),
		mcp.WithIdempotentHintAnnotation(idempotent),
		mcp.WithOpenWorldHintAnnotation(false),
	}
}

// --- argument parsing ---

// argError is a bad argument: which field, what is wrong, how to fix it.
type argError struct {
	field, message, fix string
}

func (e *argError) Error() string { return e.message }

func badArg(field, message, fix string) error {
	return &argError{field: field, message: message, fix: fix}
}

// invalid turns a parse error into an isError result with a fix and a
// valid example.
func invalid(err error, example map[string]any) *mcp.CallToolResult {
	var ae *argError
	if errors.As(err, &ae) {
		return respond.Invalid(ae.field, ae.message, ae.fix, example)
	}
	return respond.Invalid("", err.Error(), "Correct the arguments and call again.", example)
}

// given reports whether args has a non-null value for key.
func given(args map[string]any, key string) bool {
	v, present := args[key]
	return present && v != nil
}

// withAliases returns a copy of args with old parameter names moved to their
// catalog names (a catalog name that is present wins), and the legacy
// entity_type/entity_id pair mapped onto property_id / vehicle_id /
// asset_id. entity_type "household" (or blank) means "not tied to
// anything", which clears the subject on update.
func withAliases(args map[string]any, aliases map[string]string) (map[string]any, error) {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	for old, name := range aliases {
		if v, ok := out[old]; ok {
			if !given(out, name) {
				out[name] = v
			}
			delete(out, old)
		}
	}
	if !given(out, "entity_type") && !given(out, "entity_id") {
		delete(out, "entity_type")
		delete(out, "entity_id")
		return out, nil
	}
	kind, err := optionalString(out, "entity_type")
	if err != nil {
		return nil, err
	}
	id, err := optionalString(out, "entity_id")
	if err != nil {
		return nil, err
	}
	delete(out, "entity_type")
	delete(out, "entity_id")
	if subjectGiven(out) {
		return out, nil // the typed params win
	}
	k := ""
	if kind != nil {
		k = strings.ToLower(*kind)
	}
	switch k {
	case subjectProperty, subjectVehicle, subjectAsset:
		if id == nil {
			return nil, badArg(k+"_id", "entity_type needs entity_id.",
				fmt.Sprintf("Pass %s_id with the %s's id instead.", k, k))
		}
		out[k+"_id"] = *id
	case "", "household", "family":
		if id != nil && kind == nil {
			return nil, badArg("property_id", "entity_id was given without entity_type.",
				"Pass property_id, vehicle_id or asset_id instead.")
		}
		out["property_id"] = "" // clear
	default:
		return nil, badArg("property_id", fmt.Sprintf("A loan can't be tied to a %q.", *kind),
			"Pass property_id, vehicle_id or asset_id, or leave them out.")
	}
	return out, nil
}

// subjectGiven reports whether any typed subject param is present (a blank
// string counts: it clears the subject).
func subjectGiven(args map[string]any) bool {
	return given(args, "property_id") || given(args, "vehicle_id") || given(args, "asset_id")
}

// optionalString returns the trimmed string argument, or nil when it is
// absent, null or blank. A non-string value is an error.
func optionalString(args map[string]any, key string) (*string, error) {
	v, present := args[key]
	if !present || v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, badArg(key, key+" must be a string.", fmt.Sprintf("Pass %s as text in quotes.", key))
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	return &s, nil
}

// uuidFixes says where to get each id parameter.
var uuidFixes = map[string]string{
	"loan_id":     "Pass the loan's id (a UUID) from loan_find, not its name.",
	"id":          "Pass a loan's id (a UUID) from loan_find, or use query to search by name.",
	"property_id": "Pass the property's id (a UUID) from property_find, not its name.",
	"vehicle_id":  "Pass the vehicle's id (a UUID) from vehicle_find, not its name.",
	"asset_id":    "Pass the asset's id (a UUID) from asset_find, not its name.",
}

// optionalUUID parses an optional UUID string argument.
func optionalUUID(args map[string]any, key string) (*uuid.UUID, error) {
	s, err := optionalString(args, key)
	if err != nil || s == nil {
		return nil, err
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		fix := uuidFixes[key]
		if fix == "" {
			fix = "Pass a UUID."
		}
		return nil, badArg(key, fmt.Sprintf("%s must be a UUID; got %q.", key, *s), fix)
	}
	return &id, nil
}

// number reads a numeric argument; ok is false when absent or null.
// Numbers sent as numeric strings ("1850.00") are accepted, because small
// models often quote them.
func number(args map[string]any, key string) (val float64, ok bool, err error) {
	v, present := args[key]
	if !present || v == nil {
		return 0, false, nil
	}
	notNumber := badArg(key, key+" must be a number.", fmt.Sprintf("Pass %s as a number without quotes or $.", key))
	switch n := v.(type) {
	case float64:
		val = n
	case int:
		val = float64(n)
	case int64:
		val = float64(n)
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return 0, false, nil
		}
		f, perr := strconv.ParseFloat(s, 64)
		if perr != nil {
			return 0, false, notNumber
		}
		val = f
	default:
		return 0, false, notNumber
	}
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return 0, false, badArg(key, key+" must be a finite number.", fmt.Sprintf("Pass %s as a plain number.", key))
	}
	return val, true, nil
}

// wholeNumber reads an integer argument in [lo, hi].
func wholeNumber(args map[string]any, key string, lo, hi int) (int, bool, error) {
	n, ok, err := number(args, key)
	if err != nil || !ok {
		return 0, ok, err
	}
	if n != math.Trunc(n) || n < float64(lo) || n > float64(hi) {
		return 0, false, badArg(key, fmt.Sprintf("%s must be a whole number from %d to %d.", key, lo, hi),
			fmt.Sprintf("Pass %s as a whole number between %d and %d.", key, lo, hi))
	}
	return int(n), true, nil
}

// escapeLike escapes ILIKE wildcards so query matches literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
