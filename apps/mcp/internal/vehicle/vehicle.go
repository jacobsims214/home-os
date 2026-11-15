// Package vehicle provides the MCP vehicle tools (catalog v2, KB
// architecture/mcp-tool-catalog.md "vehicle"). Stage 1 ships the read tool
// vehicle_find; vehicle_create, vehicle_update and vehicle_delete are #4460.
// Every result is built with internal/respond and every tool declares all
// four annotation hints and an outputSchema. All queries are scoped to the
// authenticated household via JWT claims.
//
// Vehicles belong to the family, with no property (domain model v2). Their
// money and upkeep live in loans, bills and maintenance_tasks, linked by the
// typed vehicle_id column. The legacy finance columns on vehicles (lender,
// loan_amount, monthly_payment, insurance_cost, registration_cost, ...) were
// copied into loans and bills by migration 044 and are deliberately not
// returned, so nothing is reported twice.
package vehicle

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

// toolHandler is the signature every vehicle tool handler has.
type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// vehicleLink matches a loans or bills row (alias t) tied to vehicle v by
// its typed vehicle_id (domain model v2). The legacy entity_type/entity_id
// pair is no longer read: 039 backfilled it and 056 drops it.
const vehicleLink = `(t.vehicle_id = v.id)`

// vehicleSelect loads vehicles with counts of what is linked to them.
// Scanned by scanVehicle.
var vehicleSelect = `SELECT v.id, v.year, v.make, v.model, v.vin, v.license_plate, v.color, v.notes,
	       v.purchase_price::float8, v.purchase_date, v.current_value::float8, v.created_at, v.updated_at,
	       (SELECT count(*) FROM loans t WHERE t.household_id = v.household_id AND ` + vehicleLink + `),
	       (SELECT count(*) FROM bills t WHERE t.household_id = v.household_id AND ` + vehicleLink + `),
	       (SELECT count(*) FROM maintenance_tasks m WHERE m.vehicle_id = v.id AND m.household_id = v.household_id
	           AND m.status IN ('pending', 'in_progress'))
	FROM vehicles v`

// vehicleRow is a vehicles row as the tools load it.
type vehicleRow struct {
	ID            uuid.UUID
	Year          *int
	Make          *string
	Model         *string
	VIN           *string
	LicensePlate  *string
	Color         *string
	Notes         *string
	PurchasePrice *float64
	PurchaseDate  *time.Time
	CurrentValue  *float64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Loans         int
	Bills         int
	OpenUpkeep    int
}

// scanVehicle reads one row of vehicleSelect.
func scanVehicle(row pgx.Row) (vehicleRow, error) {
	var r vehicleRow
	err := row.Scan(&r.ID, &r.Year, &r.Make, &r.Model, &r.VIN, &r.LicensePlate, &r.Color, &r.Notes,
		&r.PurchasePrice, &r.PurchaseDate, &r.CurrentValue, &r.CreatedAt, &r.UpdatedAt,
		&r.Loans, &r.Bills, &r.OpenUpkeep)
	return r, err
}

// vehicleData is one vehicle in vehicle_find's output. IDs are strings (a
// uuid.UUID would be advertised as a byte array in the outputSchema).
type vehicleData struct {
	ID              string          `json:"id" jsonschema_description:"The vehicle_id to pass to loan_find, loan_create, maintenance_find or maintenance_create."`
	Name            string          `json:"name" jsonschema_description:"Year, make and model, e.g. 2022 Honda Odyssey."`
	Year            *int            `json:"year"`
	Make            *string         `json:"make"`
	Model           *string         `json:"model"`
	VIN             *string         `json:"vin"`
	LicensePlate    *string         `json:"license_plate"`
	Color           *string         `json:"color"`
	Notes           *string         `json:"notes"`
	PurchasePrice   *float64        `json:"purchase_price"`
	PurchaseDate    *string         `json:"purchase_date" jsonschema_description:"YYYY-MM-DD."`
	CurrentValue    *float64        `json:"current_value" jsonschema_description:"What it is worth now, in the family's currency."`
	LoanCount       int             `json:"loan_count" jsonschema_description:"Loans tied to it; list them with loan_find vehicle_id."`
	BillCount       int             `json:"bill_count" jsonschema_description:"Bills tied to it (insurance, registration), listed in related."`
	OpenUpkeepCount int             `json:"open_upkeep_count" jsonschema_description:"Pending upkeep tasks; list them with maintenance_find vehicle_id."`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Related         *vehicleRelated `json:"related,omitempty" jsonschema_description:"Only when id is given: its loans, bills and pending upkeep."`
}

// vehicleRelated is what is linked to one vehicle by vehicle_id.
type vehicleRelated struct {
	Loans  []relatedLoan   `json:"loans"`
	Bills  []relatedBill   `json:"bills"`
	Upkeep []relatedUpkeep `json:"upkeep" jsonschema_description:"Pending upkeep tasks, soonest first."`
}

type relatedLoan struct {
	ID               string   `json:"id" jsonschema_description:"loan_id for loan_update."`
	Name             string   `json:"name"`
	Lender           *string  `json:"lender"`
	RemainingBalance float64  `json:"remaining_balance"`
	MonthlyPayment   *float64 `json:"monthly_payment"`
}

type relatedBill struct {
	ID        string   `json:"id" jsonschema_description:"bill_id for bill_update or bill_pay."`
	Name      string   `json:"name"`
	Amount    *float64 `json:"amount"`
	Frequency string   `json:"frequency" jsonschema_description:"weekly, monthly, quarterly, semiannual, annual or once."`
	DueDay    *int     `json:"due_day"`
	DueDate   *string  `json:"due_date" jsonschema_description:"YYYY-MM-DD, for bills that are not monthly."`
}

type relatedUpkeep struct {
	ID         string  `json:"id" jsonschema_description:"task_id for maintenance_complete."`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	DueDate    *string `json:"due_date" jsonschema_description:"YYYY-MM-DD in the family's timezone."`
	DueWeekday *string `json:"due_weekday" jsonschema_description:"Weekday of due_date, e.g. Thu."`
}

// displayName is "year make model", or "vehicle" when all three are empty.
func (r vehicleRow) displayName() string {
	var parts []string
	if r.Year != nil {
		parts = append(parts, strconv.Itoa(*r.Year))
	}
	for _, p := range []*string{r.Make, r.Model} {
		if p != nil && strings.TrimSpace(*p) != "" {
			parts = append(parts, strings.TrimSpace(*p))
		}
	}
	if len(parts) == 0 {
		return "Unnamed vehicle"
	}
	return strings.Join(parts, " ")
}

func (r vehicleRow) data() vehicleData {
	d := vehicleData{
		ID: r.ID.String(), Name: r.displayName(), Year: r.Year, Make: r.Make, Model: r.Model,
		VIN: r.VIN, LicensePlate: r.LicensePlate, Color: r.Color, Notes: r.Notes,
		PurchasePrice: r.PurchasePrice, CurrentValue: r.CurrentValue,
		LoanCount: r.Loans, BillCount: r.Bills, OpenUpkeepCount: r.OpenUpkeep,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.PurchaseDate != nil {
		d.PurchaseDate = dateOnly(*r.PurchaseDate)
	}
	return d
}

// describe is a short phrase for summaries, e.g. "plate ABC123, worth
// $22,000.00, 1 loan".
func (d vehicleData) describe() string {
	var parts []string
	if d.LicensePlate != nil {
		parts = append(parts, "plate "+*d.LicensePlate)
	}
	if d.CurrentValue != nil {
		parts = append(parts, "worth "+money(*d.CurrentValue))
	}
	if d.LoanCount > 0 {
		parts = append(parts, plural(d.LoanCount, "loan"))
	}
	if d.BillCount > 0 {
		parts = append(parts, plural(d.BillCount, "bill"))
	}
	if d.OpenUpkeepCount > 0 {
		parts = append(parts, plural(d.OpenUpkeepCount, "pending upkeep task"))
	}
	return strings.Join(parts, ", ")
}

// loadRelated loads the loans, bills and pending upkeep tied to one vehicle.
func loadRelated(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, vehicleID uuid.UUID, householdID string) (*vehicleRelated, error) {
	rel := &vehicleRelated{Loans: []relatedLoan{}, Bills: []relatedBill{}, Upkeep: []relatedUpkeep{}}

	rows, err := q.Query(ctx, `SELECT t.id, t.name, t.lender, t.remaining_balance::float8, t.monthly_payment::float8
		FROM loans t JOIN vehicles v ON v.id = $1 AND v.household_id = t.household_id
		WHERE t.household_id = $2 AND `+vehicleLink+` ORDER BY lower(t.name), t.id`, vehicleID, householdID)
	if err != nil {
		return nil, fmt.Errorf("load vehicle loans: %w", err)
	}
	rel.Loans, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (relatedLoan, error) {
		var l relatedLoan
		var id uuid.UUID
		err := row.Scan(&id, &l.Name, &l.Lender, &l.RemainingBalance, &l.MonthlyPayment)
		l.ID = id.String()
		return l, err
	})
	if err != nil {
		return nil, fmt.Errorf("load vehicle loans: %w", err)
	}

	rows, err = q.Query(ctx, `SELECT t.id, t.name, t.amount::float8, t.frequency, t.due_day, t.due_date
		FROM bills t JOIN vehicles v ON v.id = $1 AND v.household_id = t.household_id
		WHERE t.household_id = $2 AND `+vehicleLink+` ORDER BY lower(t.name), t.id`, vehicleID, householdID)
	if err != nil {
		return nil, fmt.Errorf("load vehicle bills: %w", err)
	}
	rel.Bills, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (relatedBill, error) {
		var b relatedBill
		var id uuid.UUID
		var due *time.Time
		err := row.Scan(&id, &b.Name, &b.Amount, &b.Frequency, &b.DueDay, &due)
		b.ID = id.String()
		if due != nil {
			b.DueDate = dateOnly(*due)
		}
		return b, err
	})
	if err != nil {
		return nil, fmt.Errorf("load vehicle bills: %w", err)
	}

	rows, err = q.Query(ctx, `SELECT id, name, status::text, due_date
		FROM maintenance_tasks
		WHERE vehicle_id = $1 AND household_id = $2 AND status IN ('pending', 'in_progress')
		ORDER BY due_date NULLS LAST, lower(name), id`, vehicleID, householdID)
	if err != nil {
		return nil, fmt.Errorf("load vehicle upkeep: %w", err)
	}
	rel.Upkeep, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (relatedUpkeep, error) {
		var m relatedUpkeep
		var id uuid.UUID
		var due *time.Time
		err := row.Scan(&id, &m.Name, &m.Status, &due)
		m.ID = id.String()
		if due != nil {
			m.DueDate, m.DueWeekday = dateOnly(*due), weekday(*due)
		}
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("load vehicle upkeep: %w", err)
	}
	return rel, nil
}

// --- small helpers ---

func dateOnly(t time.Time) *string {
	s := t.Format(time.DateOnly)
	return &s
}

func weekday(t time.Time) *string {
	s := t.Weekday().String()[:3]
	return &s
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// money formats an amount for summaries, e.g. "$22,000.00".
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

// optionalUUID parses an optional UUID string argument.
func optionalUUID(args map[string]any, key, fix string) (*uuid.UUID, error) {
	s, err := optionalString(args, key)
	if err != nil || s == nil {
		return nil, err
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil, badArg(key, fmt.Sprintf("%s must be a UUID; got %q.", key, *s), fix)
	}
	return &id, nil
}

// wholeNumber reads an integer argument in [lo, hi]. Numbers sent as
// numeric strings ("10") are accepted, because small models often quote
// them.
func wholeNumber(args map[string]any, key string, lo, hi int) (int, bool, error) {
	v, present := args[key]
	if !present || v == nil {
		return 0, false, nil
	}
	bad := badArg(key, fmt.Sprintf("%s must be a whole number from %d to %d.", key, lo, hi),
		fmt.Sprintf("Pass %s as a whole number between %d and %d.", key, lo, hi))
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case int:
		n = float64(x)
	case int64:
		n = float64(x)
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, false, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false, bad
		}
		n = f
	default:
		return 0, false, bad
	}
	if math.IsNaN(n) || n != math.Trunc(n) || n < float64(lo) || n > float64(hi) {
		return 0, false, bad
	}
	return int(n), true, nil
}

// escapeLike escapes ILIKE wildcards so query matches literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
