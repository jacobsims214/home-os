// Package bill provides the MCP bill tools (catalog v2, KB
// architecture/mcp-tool-catalog.md "bill"): bill_find, bill_create,
// bill_update, bill_delete and bill_pay. Every result is built with
// internal/respond and every tool declares all four annotation hints and an
// outputSchema. All operations are scoped to the authenticated household via
// JWT claims. Bills are about a property, vehicle or asset through the
// typed subject columns (domain model v2, see subject.go).
package bill

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// Bill status values reported in bill output.
const (
	// statusDue means the bill's current due date has not been paid yet.
	statusDue = "due"
	// statusPaid means the last payment already covers the current due date,
	// so next_due_date is the following one.
	statusPaid = "paid"
)

// Pagination limits shared by every catalog find tool.
const (
	defaultLimit = 25
	maxLimit     = 100
	// maxDueWithinDays bounds due_within_days to a year and a bit.
	maxDueWithinDays = 366
)

// exampleBillID is the UUID used in error examples.
const exampleBillID = "3f2b8c1e-5d4a-4b7e-9c2a-1e6f0a9b8d7c"

// daysBetween returns the whole days from a to b (both midnight UTC dates).
func daysBetween(a, b time.Time) int {
	return int(b.Sub(a).Hours() / 24)
}

// billColumns is the column list every bill query selects or returns, in
// the order scanBill reads it.
// The subject name is a scalar subquery, not a join, so the same list works
// in RETURNING clauses.
const billColumns = `id, name, amount, due_day, category, COALESCE(is_autopay, false),
	vendor_id, property_id, vehicle_id, asset_id, payment_url, notes,
	created_at, updated_at, paid_date, paid_for_due_date, frequency, due_date,
	COALESCE((SELECT a.name FROM assets a WHERE a.id = bills.asset_id),
	         (SELECT NULLIF(concat_ws(' ', v.year, v.make, v.model), '') FROM vehicles v WHERE v.id = bills.vehicle_id),
	         (SELECT p.name FROM properties p WHERE p.id = bills.property_id))`

// billRow is a bills row as the tools load it. amount and due_day are
// pointers because API-created rows can leave them NULL.
type billRow struct {
	ID         uuid.UUID
	Name       string
	Amount     *float64
	DueDay     *int
	Category   *string
	AutoPay    bool
	VendorID   *uuid.UUID
	PropertyID *uuid.UUID
	VehicleID  *uuid.UUID
	AssetID    *uuid.UUID
	PaymentURL *string
	Notes      *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	PaidDate   *time.Time
	// PaidFor is bills.paid_for_due_date: the due date paid_date covers,
	// nil when not stored (coverage is then inferred from paid_date).
	PaidFor *time.Time
	// Frequency is bills.frequency (044): monthly, quarterly, semiannual,
	// annual or once.
	Frequency string
	// DueDate is bills.due_date: the anchor date of a bill that is not
	// monthly (044 allows due_day only on monthly bills).
	DueDate *time.Time
	// SubjectName is the name of the property, vehicle or asset.
	SubjectName *string
}

// scanBill reads one row of billColumns.
func scanBill(row pgx.Row) (billRow, error) {
	var r billRow
	err := row.Scan(&r.ID, &r.Name, &r.Amount, &r.DueDay, &r.Category, &r.AutoPay,
		&r.VendorID, &r.PropertyID, &r.VehicleID, &r.AssetID, &r.PaymentURL, &r.Notes,
		&r.CreatedAt, &r.UpdatedAt, &r.PaidDate, &r.PaidFor, &r.Frequency, &r.DueDate, &r.SubjectName)
	return r, err
}

// schedule is the row's due schedule (frequency with due_day or due_date).
func (r billRow) schedule() Schedule {
	return Schedule{Frequency: r.Frequency, DueDay: r.DueDay, DueDate: r.DueDate}
}

// hasSchedule reports whether the bill has a usable due schedule: a monthly
// bill with a due_day of 1-31, or another bill with a due_date.
func (r billRow) hasSchedule() bool {
	_, ok := r.schedule().cadence()
	return ok
}

// billData is one bill in every bill tool's output. IDs are strings (a
// uuid.UUID would be advertised as a byte array in the outputSchema).
// next_due_date is the next unpaid due date in the family's timezone, with
// its weekday; the due-date fields are null, and status omitted, when the
// bill has no schedule (a monthly bill without due_day, another bill without
// due_date). A paid once bill has status paid and no next_due_date. amount_paid and already_paid are set only by
// bill_pay, duplicate only by bill_create's loop guard.
type billData struct {
	ID             string    `json:"id" jsonschema_description:"The bill_id to pass to bill_update, bill_pay or bill_delete."`
	Name           string    `json:"name"`
	Amount         *float64  `json:"amount" jsonschema_description:"Amount due each time, in the family's currency."`
	Frequency      string    `json:"frequency" jsonschema_description:"monthly, quarterly, semiannual, annual or once."`
	DueDay         *int      `json:"due_day" jsonschema_description:"Monthly bills: day of the month it is due (1-31); a short month uses its last day."`
	DueDate        *string   `json:"due_date" jsonschema_description:"Other bills: a due date, YYYY-MM-DD; it repeats on that day every 3, 6 or 12 months."`
	MonthlyAmount  *float64  `json:"monthly_amount" jsonschema_description:"amount spread per month: quarterly /3, semiannual /6, annual /12; null for once."`
	Category       *string   `json:"category"`
	Autopay        bool      `json:"autopay"`
	ContactID      *string   `json:"contact_id" jsonschema_description:"The contact (vendor) the bill is paid to."`
	PropertyID     *string   `json:"property_id" jsonschema_description:"The house or other property the bill is for (an asset's property for an asset bill)."`
	VehicleID      *string   `json:"vehicle_id" jsonschema_description:"The car or other vehicle the bill is for."`
	AssetID        *string   `json:"asset_id" jsonschema_description:"The thing the bill is for."`
	SubjectType    *string   `json:"subject_type" jsonschema_description:"property, vehicle or asset; null for a family-level bill."`
	SubjectName    *string   `json:"subject_name" jsonschema_description:"Name of the property, vehicle or asset."`
	PaymentURL     *string   `json:"payment_url"`
	Notes          *string   `json:"notes"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	NextDueDate    *string   `json:"next_due_date" jsonschema_description:"Next unpaid due date, YYYY-MM-DD in the family's timezone."`
	NextDueWeekday *string   `json:"next_due_weekday" jsonschema_description:"Weekday of next_due_date, e.g. Thu."`
	DaysUntilDue   *int      `json:"days_until_due" jsonschema_description:"Whole days from today (family timezone) to next_due_date."`
	Status         string    `json:"status,omitempty" jsonschema_description:"due, or paid when the current due date is already paid."`
	PaidDate       *string   `json:"paid_date" jsonschema_description:"Date of the last recorded payment, YYYY-MM-DD."`
	PaidForDueDate *string   `json:"paid_for_due_date" jsonschema_description:"The due date the last payment covered, YYYY-MM-DD."`
	AmountPaid     *float64  `json:"amount_paid,omitempty" jsonschema_description:"bill_pay only: the amount paid."`
	AlreadyPaid    *bool     `json:"already_paid,omitempty" jsonschema_description:"bill_pay only: true when that due date was already paid and nothing changed."`
	Duplicate      bool      `json:"duplicate,omitempty" jsonschema_description:"bill_create only: true when the same bill was created minutes ago and was returned instead."`
}

// dateOnly formats t as YYYY-MM-DD.
func dateOnly(t time.Time) *string {
	s := t.Format(time.DateOnly)
	return &s
}

// weekday is t's short weekday name, e.g. "Thu".
func weekday(t time.Time) *string {
	s := t.Weekday().String()[:3]
	return &s
}

// uuidString is id as a string, nil when id is nil.
func uuidString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// data builds the output for r as of today, applying BillDue.
func (r billRow) data(today time.Time) billData {
	b := billData{
		ID: r.ID.String(), Name: r.Name, Amount: r.Amount, Frequency: r.Frequency, DueDay: r.DueDay,
		Category: r.Category, Autopay: r.AutoPay, ContactID: uuidString(r.VendorID),
		PropertyID: uuidString(r.PropertyID), VehicleID: uuidString(r.VehicleID), AssetID: uuidString(r.AssetID),
		SubjectType: subjectType(r.PropertyID, r.VehicleID, r.AssetID),
		PaymentURL:  r.PaymentURL, Notes: r.Notes,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if b.SubjectType != nil {
		b.SubjectName = r.SubjectName
	}
	if b.Frequency == "" {
		b.Frequency = FrequencyMonthly
	}
	if r.DueDate != nil {
		b.DueDate = dateOnly(*r.DueDate)
	}
	b.MonthlyAmount = monthlyAmount(r.Amount, b.Frequency)
	if r.PaidDate != nil {
		b.PaidDate = dateOnly(*r.PaidDate)
	}
	due, ok := BillDueFor(today, r.schedule(), r.PaidDate, r.PaidFor)
	if !ok {
		return b
	}
	b.Status = due.Status
	if !due.Next.IsZero() {
		days := due.DaysUntil
		b.NextDueDate, b.NextDueWeekday, b.DaysUntilDue = dateOnly(due.Next), weekday(due.Next), &days
	}
	if due.PaidFor != nil {
		b.PaidForDueDate = dateOnly(*due.PaidFor)
	}
	return b
}

// dueText describes when b is next due, e.g. "due Thu Nov 20", or "" when
// it has no due day.
func (b billData) dueText() string {
	if b.NextDueDate == nil {
		return ""
	}
	d, err := time.Parse(time.DateOnly, *b.NextDueDate)
	if err != nil {
		return ""
	}
	return "due " + d.Format("Mon Jan 2")
}

// money formats an amount for summaries, e.g. "$142.50".
func money(v float64) string { return fmt.Sprintf("$%.2f", v) }

// notSignedIn is the result for a call without household claims.
func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}

// readAnnotations are class R (catalog §1): read-only, safe to repeat.
func readAnnotations(title string) []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithTitleAnnotation(title),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	}
}

// findInput is the validated bill_find arguments.
type findInput struct {
	id            *uuid.UUID
	query         string
	category      string
	dueWithinDays *int
	autopay       *bool
	frequency     string
	propertyID    *uuid.UUID
	vehicleID     *uuid.UUID
	assetID       *uuid.UUID
	// subjectKind is the legacy entity_type filter on its own: property,
	// vehicle or asset (bills about any one of that kind).
	subjectKind string
	limit       int
	offset      int
}

// filtered reports whether any filter narrows the list.
func (in findInput) filtered() bool {
	return in.id != nil || in.query != "" || in.category != "" || in.dueWithinDays != nil ||
		in.autopay != nil || in.frequency != "" || in.propertyID != nil || in.vehicleID != nil || in.assetID != nil || in.subjectKind != ""
}

// NewBillFindTool creates the bill_find MCP tool (catalog v2, class R),
// replacing list_bills and get_upcoming_bills.
//
// It lists the household's bills, each with its next unpaid due date and
// paid status (BillDue against today in the family's timezone), sorted by
// that date (bills without a due day last), then name. due_within_days keeps
// bills whose next unpaid due date is within [today, today+N]; a bill
// already paid for its current due date counts under the following one.
// Pagination is limit + the opaque next_cursor (respond.EncodeCursor),
// passed back as cursor. property_id matches bills about that property,
// including bills about its assets (their property_id is derived).
func NewBillFindTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("List or search bills with amount, frequency and next due date. Use due_within_days for \"what's due soon?\". " +
			"Bills live here, not on the calendar."),
		mcp.WithString("id", mcp.Description("A bill_id (UUID) to get exactly that bill")),
		mcp.WithString("query", mcp.Description("Text in the name, category or notes, e.g. electric")),
		mcp.WithString("category", mcp.Description("Exact category, any case, e.g. Utilities")),
		mcp.WithNumber("due_within_days",
			mcp.Description("Only bills next due within this many days; 0 = today, e.g. 7"),
			mcp.Min(0), mcp.Max(maxDueWithinDays)),
		mcp.WithBoolean("autopay", mcp.Description("true = only autopay bills, false = only manual ones")),
		mcp.WithString("frequency", mcp.Description("Only bills due this often, e.g. annual"), mcp.Enum(frequencies...)),
		mcp.WithString("property_id", mcp.Description("Only bills for this property (UUID from property_find)")),
		mcp.WithString("vehicle_id", mcp.Description("Only bills for this vehicle (UUID from vehicle_find)")),
		mcp.WithNumber("limit", mcp.Description("Max bills to return, 1-100 (default 25)"), mcp.Min(1), mcp.Max(maxLimit)),
		mcp.WithString("cursor", mcp.Description("next_cursor from the previous result, for the next page")),
		respond.ListSchema[billData](),
	}
	tool := mcp.NewTool("bill_find", append(opts, readAnnotations("Find bills")...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}

		in, err := parseFindInput(normalizeArgs(req.GetArguments()))
		if err != nil {
			return invalid(err, map[string]any{"due_within_days": 7}), nil
		}

		today, err := householdToday(ctx, pool, claims.HouseholdID)
		if err != nil {
			return respond.Failed("find bills", err), nil
		}

		rows, err := pool.Query(ctx,
			`SELECT `+billColumns+`
			 FROM bills
			 WHERE household_id = $1
			   AND ($2::uuid IS NULL OR id = $2)
			   AND ($3 = '' OR name ILIKE '%' || $3 || '%' OR category ILIKE '%' || $3 || '%' OR notes ILIKE '%' || $3 || '%')
			   AND ($4 = '' OR lower(category) = lower($4))
			   AND ($5::boolean IS NULL OR COALESCE(is_autopay, false) = $5)
			   AND ($6::uuid IS NULL OR property_id = $6)
			   AND ($7::uuid IS NULL OR vehicle_id = $7)
			   AND ($8::uuid IS NULL OR asset_id = $8)
			   AND ($9 = '' OR ($9 = 'property' AND property_id IS NOT NULL)
			        OR ($9 = 'vehicle' AND vehicle_id IS NOT NULL) OR ($9 = 'asset' AND asset_id IS NOT NULL))
			   AND ($10 = '' OR frequency = $10)`,
			claims.HouseholdID, in.id, escapeLike(in.query), in.category, in.autopay,
			in.propertyID, in.vehicleID, in.assetID, in.subjectKind, in.frequency,
		)
		if err != nil {
			return respond.Failed("find bills", err), nil
		}
		loaded, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (billRow, error) { return scanBill(row) })
		if err != nil {
			return respond.Failed("find bills", err), nil
		}

		if in.id != nil && len(loaded) == 0 {
			return respond.NotFound("bill", in.id.String(), "Call bill_find without id to list bills and their ids."), nil
		}

		bills := make([]billData, 0, len(loaded))
		for _, r := range loaded {
			b := r.data(today)
			if in.dueWithinDays != nil && (b.DaysUntilDue == nil || *b.DaysUntilDue < 0 || *b.DaysUntilDue > *in.dueWithinDays) {
				continue
			}
			bills = append(bills, b)
		}
		sort.SliceStable(bills, func(i, j int) bool {
			a, b := bills[i].DaysUntilDue, bills[j].DaysUntilDue
			if (a == nil) != (b == nil) {
				return a != nil
			}
			if a != nil && *a != *b {
				return *a < *b
			}
			return strings.ToLower(bills[i].Name) < strings.ToLower(bills[j].Name)
		})

		total := len(bills)
		if in.offset > total {
			in.offset = total
		}
		end := min(in.offset+in.limit, total)
		page := bills[in.offset:end]

		return respond.List(findSummary(in, page, total), page, respond.Page{
			Total:      total - in.offset,
			NextCursor: respond.CursorAfter(end, total),
			Narrow:     "Pass next_cursor as cursor for the next page, or add category, query or due_within_days.",
		}, findNext(in, page)...), nil
	}

	return "bill_find", tool, handler
}

// findSummary is bill_find's one-line summary.
func findSummary(in findInput, page []billData, total int) string {
	window := ""
	if in.dueWithinDays != nil {
		switch *in.dueWithinDays {
		case 0:
			window = " due today"
		case 1:
			window = " due today or tomorrow"
		default:
			window = fmt.Sprintf(" due in the next %d days", *in.dueWithinDays)
		}
	}
	noun := "bills"
	if total == 1 {
		noun = "bill"
	}
	switch {
	case total == 0 && !in.filtered():
		return "No bills yet."
	case total == 0:
		return "No bills" + window + " match."
	case len(page) == 1 && total == 1:
		b := page[0]
		s := fmt.Sprintf("Found bill %s", b.Name)
		if b.Amount != nil {
			s += ", " + money(*b.Amount)
		}
		if d := b.dueText(); d != "" {
			s += " " + d
			if b.Status == statusPaid {
				s += " (current one paid)"
			}
		}
		return s + "."
	case len(page) < total-in.offset || in.offset > 0:
		return fmt.Sprintf("Found %d %s%s; showing %d-%d.", total, noun, window, in.offset+1, in.offset+len(page))
	default:
		return fmt.Sprintf("Found %d %s%s.", total, noun, window)
	}
}

// findNext is bill_find's next-step hints.
func findNext(in findInput, page []billData) []string {
	if len(page) == 0 {
		if !in.filtered() {
			return []string{"No bills yet. Add one with bill_create; don't put bills on the calendar as events."}
		}
		return []string{"Call bill_find with fewer filters, or add the bill with bill_create."}
	}
	first := page[0].ID
	return []string{
		fmt.Sprintf("Mark one paid with bill_pay bill_id=%s.", first),
		fmt.Sprintf("Change one with bill_update bill_id=%s.", first),
		"See monthly totals by category with budget_summary.",
	}
}

// parseFindInput validates bill_find arguments (already normalized, so
// args is a copy it may change).
func parseFindInput(args map[string]any) (findInput, error) {
	in := findInput{limit: defaultLimit}
	var err error
	// Legacy filter: entity_type alone means "bills about any property /
	// vehicle / asset"; with entity_id it maps onto the typed filters.
	if given(args, "entity_type") && !given(args, "entity_id") {
		kind, err := optionalString(args, "entity_type")
		if err != nil {
			return in, err
		}
		delete(args, "entity_type")
		if kind != nil {
			switch k := strings.ToLower(*kind); k {
			case subjectProperty, subjectVehicle, subjectAsset:
				in.subjectKind = k
			default:
				return in, badArg("entity_type", fmt.Sprintf("Bills can't be tied to a %q.", *kind),
					"Pass property_id or vehicle_id, or leave them out.")
			}
		}
	}
	if err := subjectAliases(args); err != nil {
		return in, err
	}
	if s, ok := args["property_id"].(string); ok && strings.TrimSpace(s) == "" {
		delete(args, "property_id") // "not about anything" is no filter
	}
	if in.id, err = optionalUUID(args, "id"); err != nil {
		return in, err
	}
	if q, err := optionalString(args, "query"); err != nil {
		return in, err
	} else if q != nil {
		in.query = *q
	}
	if c, err := optionalString(args, "category"); err != nil {
		return in, err
	} else if c != nil {
		in.category = *c
	}
	if n, ok, err := wholeNumber(args, "due_within_days", 0, maxDueWithinDays); err != nil {
		return in, err
	} else if ok {
		in.dueWithinDays = &n
	}
	if v, ok, err := optionalBool(args, "autopay"); err != nil {
		return in, err
	} else if ok {
		in.autopay = &v
	}
	if f, err := parseFrequency(args); err != nil {
		return in, err
	} else if f != nil {
		in.frequency = *f
	}
	if in.propertyID, err = optionalUUID(args, "property_id"); err != nil {
		return in, err
	}
	if in.vehicleID, err = optionalUUID(args, "vehicle_id"); err != nil {
		return in, err
	}
	// asset_id is not advertised on bill_find; it arrives via the legacy
	// entity_type=asset alias.
	if in.assetID, err = optionalUUID(args, "asset_id"); err != nil {
		return in, err
	}
	if n, ok, err := wholeNumber(args, "limit", 1, maxLimit); err != nil {
		return in, err
	} else if ok {
		in.limit = n
	}
	if c, err := optionalString(args, "cursor"); err != nil {
		return in, err
	} else if c != nil {
		off, ok := respond.DecodeCursor(*c)
		if !ok {
			return in, badArg("cursor", fmt.Sprintf("cursor %q is not a cursor from bill_find.", *c),
				"Pass next_cursor from the previous result as cursor, or leave cursor out for the first page.")
		}
		in.offset = off
	}
	return in, nil
}

// escapeLike escapes ILIKE wildcards so query matches literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// argError is a bad argument: which field, what is wrong, how to fix it.
type argError struct {
	field, message, fix string
}

func (e *argError) Error() string { return e.message }

// badArg returns an *argError.
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
