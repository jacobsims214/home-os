package bill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// Column limits from the bills table (migrations 007 and 020).
const (
	maxNameLen       = 255
	maxCategoryLen   = 100
	maxEntityTypeLen = 50
	// NUMERIC(12,2) holds at most 9,999,999,999.99.
	maxAmount = 9_999_999_999.99
)

// duplicateWindow is the bill_create loop guard (catalog §1 "Loop guard"):
// the same bill created again within it returns the first one.
const duplicateWindow = "10 minutes"

// paramAliases maps old (S1) parameter names to their catalog v2 names.
// They are accepted but not advertised, so older callers keep working.
var paramAliases = map[string]string{
	"is_autopay": "autopay",
	"vendor_id":  "contact_id",
}

// normalizeArgs returns a copy of args with old parameter names moved to
// their catalog names. A catalog name that is present wins.
func normalizeArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	for old, name := range paramAliases {
		if v, ok := out[old]; ok {
			if !given(out, name) {
				out[name] = v
			}
			delete(out, old)
		}
	}
	return out
}

// createBillInput is the validated bill_create arguments. Optional text
// fields are nil when absent or blank so they are stored as NULL.
type createBillInput struct {
	name       string
	amount     float64
	dueDay     int
	category   *string
	autopay    bool
	contactID  *uuid.UUID
	entityType *string
	entityID   *uuid.UUID
	paymentURL *string
	notes      *string
}

// createExample is a valid bill_create call for error examples.
var createExample = map[string]any{"name": "Electric", "amount": 142.5, "due_day": 20}

// writeAnnotations sets all four hints for a write tool: class C (create),
// U (update/pay) or X (delete), catalog §1.
func writeAnnotations(title string, destructive, idempotent bool) []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithTitleAnnotation(title),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(destructive),
		mcp.WithIdempotentHintAnnotation(idempotent),
		mcp.WithOpenWorldHintAnnotation(false),
	}
}

// contactMissing is the result for a contact_id outside the household.
func contactMissing(id uuid.UUID, example map[string]any) *mcp.CallToolResult {
	return respond.Invalid("contact_id", fmt.Sprintf("No contact with id %q in this family.", id),
		"Call contact_find to get a contact_id, or leave contact_id out.", example)
}

// contactExists reports whether contact (vendor) id belongs to the household.
func contactExists(ctx context.Context, tx pgx.Tx, id uuid.UUID, householdID string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM vendors WHERE id = $1 AND household_id = $2)`,
		id, householdID,
	).Scan(&exists)
	return exists, err
}

// writeOutbox records the event the API writes for the same change, so
// outbox consumers see agent changes too.
func writeOutbox(ctx context.Context, tx pgx.Tx, eventType, householdID string, payload map[string]string) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s event: %w", eventType, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO outbox_events (type, payload, household_id) VALUES ($1, $2, $3)`,
		eventType, b, householdID,
	); err != nil {
		return fmt.Errorf("write %s event: %w", eventType, err)
	}
	return nil
}

// NewBillCreateTool creates the bill_create MCP tool (catalog v2, class C),
// replacing the S1 working name create_bill.
//
// It inserts a monthly bill (due on due_day each month; rrule is left unset)
// for the authenticated household and returns it with its next due date.
// Invalid input returns an isError result and inserts nothing. Loop guard:
// the same name (any case), amount and due_day created in the household in
// the last 10 minutes returns that bill with duplicate true instead of a
// second row (bills have no author column, so the guard is household-wide).
// frequency, due_date and estimate arrive with #4468; typed subjects with
// #4450 (entity_type/entity_id stay accepted as hidden aliases until then).
func NewBillCreateTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Add a recurring monthly bill (electric, internet, Netflix) due on due_day each month. " +
			"Use this, not a calendar event. Then mark payments with bill_pay."),
		mcp.WithString("name", mcp.Description("Bill name, e.g. Electric"), mcp.Required()),
		mcp.WithNumber("amount", mcp.Description("Amount due each month, e.g. 142.50"), mcp.Required(), mcp.Min(0)),
		mcp.WithNumber("due_day", mcp.Description("Day of the month due, 1-31; a short month uses its last day"),
			mcp.Required(), mcp.Min(1), mcp.Max(31)),
		mcp.WithString("category", mcp.Description("Category, e.g. Utilities, Insurance, Subscription")),
		mcp.WithBoolean("autopay", mcp.Description("true when it is paid automatically (default false)")),
		mcp.WithString("contact_id", mcp.Description("Contact (vendor) id it is paid to, from contact_find")),
		mcp.WithString("notes", mcp.Description("Free-text notes, e.g. account number hint")),
		respond.Schema[billData](),
	}
	tool := mcp.NewTool("bill_create", append(opts, writeAnnotations("Create bill", false, false)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}

		in, err := parseCreateBillInput(normalizeArgs(req.GetArguments()))
		if err != nil {
			return invalid(err, createExample), nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("create the bill", err), nil
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		if in.contactID != nil {
			ok, err := contactExists(ctx, tx, *in.contactID, claims.HouseholdID)
			if err != nil {
				return respond.Failed("create the bill", err), nil
			}
			if !ok {
				return contactMissing(*in.contactID, createExample), nil
			}
		}

		today, err := householdToday(ctx, tx, claims.HouseholdID)
		if err != nil {
			return respond.Failed("create the bill", err), nil
		}

		dup, err := scanBill(tx.QueryRow(ctx,
			`SELECT `+billColumns+`
			 FROM bills
			 WHERE household_id = $1 AND lower(name) = lower($2) AND amount = $3 AND due_day = $4
			   AND created_at > NOW() - $5::interval
			 ORDER BY created_at DESC
			 LIMIT 1`,
			claims.HouseholdID, in.name, in.amount, in.dueDay, duplicateWindow,
		))
		switch {
		case err == nil:
			b := dup.data(today)
			b.Duplicate = true
			return respond.OK(
				fmt.Sprintf("Bill %s (%s, %s) was already created a few minutes ago; returned it instead of adding a second one.",
					b.Name, money(in.amount), b.dueText()),
				b,
				fmt.Sprintf("Change it with bill_update bill_id=%s instead of creating it again.", b.ID),
			), nil
		case !errors.Is(err, pgx.ErrNoRows):
			return respond.Failed("create the bill", err), nil
		}

		r, err := scanBill(tx.QueryRow(ctx,
			`INSERT INTO bills (household_id, name, amount, due_day, category, is_autopay,
			                    vendor_id, entity_type, entity_id, payment_url, notes)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			 RETURNING `+billColumns,
			claims.HouseholdID, in.name, in.amount, in.dueDay, in.category, in.autopay,
			in.contactID, in.entityType, in.entityID, in.paymentURL, in.notes,
		))
		if err != nil {
			return respond.Failed("create the bill", err), nil
		}

		if err := writeOutbox(ctx, tx, "bill.created", claims.HouseholdID,
			map[string]string{"id": r.ID.String(), "name": r.Name}); err != nil {
			return respond.Failed("create the bill", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("create the bill", err), nil
		}

		b := r.data(today)
		return respond.OK(
			fmt.Sprintf("Created bill %s, %s %s.", b.Name, money(in.amount), b.dueText()),
			b,
			fmt.Sprintf("Mark it paid with bill_pay bill_id=%s.", b.ID),
			"See monthly totals by category with budget_summary.",
		), nil
	}

	return "bill_create", tool, handler
}

// billUpdate is the validated bill_update arguments: the bill to change and
// the columns to set, in a fixed order. Column names come only from
// parseUpdateBillInput, never from the caller.
type billUpdate struct {
	billID    uuid.UUID
	columns   []string
	values    []any
	params    []string   // the catalog parameter names that changed, for the summary
	contactID *uuid.UUID // set when contact_id is being set (not cleared)
}

func (u *billUpdate) set(param, column string, value any) {
	u.params = append(u.params, param)
	u.columns = append(u.columns, column)
	u.values = append(u.values, value)
}

// NewBillUpdateTool creates the bill_update MCP tool (catalog v2, class U).
// It changes only the fields given for a bill in the authenticated household
// and returns the updated bill with its next due date (BillDue, honouring
// paid_date, as bill_find reports it). A bill in another household is
// reported as not found. Invalid input changes nothing.
func NewBillUpdateTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Change a bill; only the given fields change. An empty string clears category, contact_id or notes. " +
			"Returns the bill with its next due date."),
		mcp.WithString("bill_id", mcp.Description("The bill's id (UUID), from bill_find"), mcp.Required()),
		mcp.WithString("name", mcp.Description("New name, e.g. Electric")),
		mcp.WithNumber("amount", mcp.Description("New amount due each month, e.g. 142.50"), mcp.Min(0)),
		mcp.WithNumber("due_day", mcp.Description("Day of the month due, 1-31; a short month uses its last day"),
			mcp.Min(1), mcp.Max(31)),
		mcp.WithString("category", mcp.Description("Category, e.g. Utilities; empty string clears it")),
		mcp.WithBoolean("autopay", mcp.Description("true when it is paid automatically")),
		mcp.WithString("contact_id", mcp.Description("Contact (vendor) id from contact_find; empty string clears it")),
		mcp.WithString("notes", mcp.Description("Free-text notes; empty string clears them")),
		respond.Schema[billData](),
	}
	tool := mcp.NewTool("bill_update", append(opts, writeAnnotations("Update bill", false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}

		example := map[string]any{"bill_id": exampleBillID, "amount": 150.0}
		in, err := parseUpdateBillInput(normalizeArgs(req.GetArguments()))
		if err != nil {
			return invalid(err, example), nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("update the bill", err), nil
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		if in.contactID != nil {
			ok, err := contactExists(ctx, tx, *in.contactID, claims.HouseholdID)
			if err != nil {
				return respond.Failed("update the bill", err), nil
			}
			if !ok {
				return contactMissing(*in.contactID, example), nil
			}
		}

		// $1 is the bill id, $2 the household; the SET values follow.
		args := []any{in.billID, claims.HouseholdID}
		sets := make([]string, 0, len(in.columns)+1)
		for i, col := range in.columns {
			sets = append(sets, fmt.Sprintf("%s = $%d", col, i+3))
			args = append(args, in.values[i])
		}
		sets = append(sets, "updated_at = NOW()")

		today, err := householdToday(ctx, tx, claims.HouseholdID)
		if err != nil {
			return respond.Failed("update the bill", err), nil
		}

		r, err := scanBill(tx.QueryRow(ctx,
			`UPDATE bills SET `+strings.Join(sets, ", ")+`
			 WHERE id = $1 AND household_id = $2
			 RETURNING `+billColumns,
			args...,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("bill", in.billID.String(), "Call bill_find to get the bill_id."), nil
		}
		if err != nil {
			return respond.Failed("update the bill", err), nil
		}

		if err := writeOutbox(ctx, tx, "bill.updated", claims.HouseholdID,
			map[string]string{"id": r.ID.String(), "name": r.Name}); err != nil {
			return respond.Failed("update the bill", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("update the bill", err), nil
		}

		b := r.data(today)
		summary := fmt.Sprintf("Updated bill %s (%s).", b.Name, strings.Join(in.params, ", "))
		if d := b.dueText(); d != "" {
			summary = fmt.Sprintf("Updated bill %s (%s); next %s.", b.Name, strings.Join(in.params, ", "), d)
		}
		return respond.OK(summary, b,
			fmt.Sprintf("Mark it paid with bill_pay bill_id=%s.", b.ID)), nil
	}

	return "bill_update", tool, handler
}

// NewBillDeleteTool creates the bill_delete MCP tool (catalog v2, class X).
// It deletes one bill in the authenticated household and returns it as it
// was. A bill in another household, or one that doesn't exist (including a
// repeat call), is reported as not found and nothing is deleted. Bill
// calendar entries are derived from the bills table, so they go with the row.
func NewBillDeleteTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Delete a bill and its future calendar entries. " +
			"Use when a bill is cancelled, not when it's paid (use bill_pay)."),
		mcp.WithString("bill_id", mcp.Description("The bill's id (UUID), from bill_find"), mcp.Required()),
		respond.Schema[billData](),
	}
	tool := mcp.NewTool("bill_delete", append(opts, writeAnnotations("Delete bill", true, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}

		example := map[string]any{"bill_id": exampleBillID}
		id, err := requiredBillID(req.GetArguments())
		if err != nil {
			return invalid(err, example), nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("delete the bill", err), nil
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		today, err := householdToday(ctx, tx, claims.HouseholdID)
		if err != nil {
			return respond.Failed("delete the bill", err), nil
		}

		r, err := scanBill(tx.QueryRow(ctx,
			`DELETE FROM bills WHERE id = $1 AND household_id = $2 RETURNING `+billColumns,
			id, claims.HouseholdID,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("bill", id.String(),
				"Call bill_find to get the bill_id. If you just deleted it, it is already gone."), nil
		}
		if err != nil {
			return respond.Failed("delete the bill", err), nil
		}

		// Same event type the API writes on bill delete; name added for
		// consumers that want it without a lookup (the row is gone).
		if err := writeOutbox(ctx, tx, "bill.deleted", claims.HouseholdID,
			map[string]string{"id": r.ID.String(), "name": r.Name}); err != nil {
			return respond.Failed("delete the bill", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("delete the bill", err), nil
		}

		b := r.data(today)
		return respond.OK(fmt.Sprintf("Deleted bill %s.", b.Name), b,
			"See the remaining bills with bill_find."), nil
	}

	return "bill_delete", tool, handler
}

// lateGraceDays is how late a payment can be and still pay the due date it
// follows. A payment more than this many days after a due date pays the next
// one instead.
const lateGraceDays = 7

// billPayInput is the validated bill_pay arguments. paidDate is nil when the
// caller left it to default to the household's today.
type billPayInput struct {
	billID   uuid.UUID
	paidDate *time.Time
	amount   *float64
}

// NewBillPayTool creates the bill_pay MCP tool (catalog v2, class U),
// replacing the S1 working name mark_bill_paid.
//
// It records a bill in the authenticated household as paid by setting
// bills.paid_date (there is no payments table yet, so no history is kept and
// amount is echoed back as amount_paid, not stored). paid_date defaults to
// today in the household's timezone. The due date a payment covers is derived
// from paid_date alone (see paidForDueDate), so paying again for a due date
// that is already covered changes nothing and returns already_paid true.
// for_due_date is #4655.
func NewBillPayTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Record a bill as paid and move it to its next due date. " +
			"Calling it again for the same due date changes nothing. Returns the due date paid and the next one."),
		mcp.WithString("bill_id", mcp.Description("The bill's id (UUID), from bill_find"), mcp.Required()),
		mcp.WithString("paid_date", mcp.Description("Date paid, YYYY-MM-DD, e.g. 2026-11-20; default today")),
		mcp.WithNumber("amount", mcp.Description("Amount paid, e.g. 142.50; default the bill's amount"), mcp.Min(0)),
		respond.Schema[billData](),
	}
	tool := mcp.NewTool("bill_pay", append(opts, writeAnnotations("Pay bill", false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}

		example := map[string]any{"bill_id": exampleBillID}
		in, err := parseBillPayInput(req.GetArguments())
		if err != nil {
			return invalid(err, example), nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("record the payment", err), nil
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		today, err := householdToday(ctx, tx, claims.HouseholdID)
		if err != nil {
			return respond.Failed("record the payment", err), nil
		}
		paid := today
		if in.paidDate != nil {
			paid = *in.paidDate
		}
		if paid.After(today) {
			return respond.Invalid("paid_date",
				fmt.Sprintf("paid_date cannot be in the future (today is %s in the family's timezone).", today.Format(time.DateOnly)),
				"Leave paid_date out to use today, or pass the date it was actually paid.",
				map[string]any{"bill_id": exampleBillID, "paid_date": today.Format(time.DateOnly)}), nil
		}

		// Lock the row so two concurrent payments can't both move it.
		r, err := scanBill(tx.QueryRow(ctx,
			`SELECT `+billColumns+` FROM bills WHERE id = $1 AND household_id = $2 FOR UPDATE`,
			in.billID, claims.HouseholdID,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("bill", in.billID.String(), "Call bill_find to get the bill_id."), nil
		}
		if err != nil {
			return respond.Failed("record the payment", err), nil
		}

		hasDueDay := r.validDueDay()
		alreadyPaid := false
		if r.PaidDate != nil {
			if hasDueDay {
				// Already paid when the stored payment covers this due date
				// or a later one; coverage never moves backwards.
				alreadyPaid = !paidForDueDate(*r.PaidDate, *r.DueDay).Before(paidForDueDate(paid, *r.DueDay))
			} else {
				alreadyPaid = !r.PaidDate.Before(paid)
			}
		}

		effective := paid
		if alreadyPaid {
			effective = *r.PaidDate
		} else {
			if err := tx.QueryRow(ctx,
				`UPDATE bills SET paid_date = $3, updated_at = NOW()
				 WHERE id = $1 AND household_id = $2
				 RETURNING updated_at`,
				r.ID, claims.HouseholdID, paid,
			).Scan(&r.UpdatedAt); err != nil {
				return respond.Failed("record the payment", err), nil
			}
			r.PaidDate = &paid

			// The row changed, so write the event the API writes on any bill
			// update; paid_date tells consumers what changed.
			if err := writeOutbox(ctx, tx, "bill.updated", claims.HouseholdID, map[string]string{
				"id": r.ID.String(), "name": r.Name, "paid_date": paid.Format(time.DateOnly),
			}); err != nil {
				return respond.Failed("record the payment", err), nil
			}
		}

		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("record the payment", err), nil
		}

		b := r.data(today)
		b.PaidDate = dateOnly(effective)
		b.AlreadyPaid = &alreadyPaid
		b.AmountPaid = in.amount
		if b.AmountPaid == nil {
			b.AmountPaid = r.Amount
		}
		// The reply's next due date follows the covered one (unchanged rule;
		// #4653 aligns back-dated payments with bill_find).
		b.NextDueDate, b.NextDueWeekday, b.DaysUntilDue, b.PaidForDueDate = nil, nil, nil, nil
		b.Status = ""
		if hasDueDay {
			covered := paidForDueDate(effective, *r.DueDay)
			next := dueDateInMonth(covered.Year(), covered.Month()+1, *r.DueDay)
			days := daysBetween(today, next)
			b.PaidForDueDate, b.NextDueDate, b.NextDueWeekday, b.DaysUntilDue = dateOnly(covered), dateOnly(next), weekday(next), &days
		}

		return respond.OK(paySummary(b, alreadyPaid), b,
			"See what's due next with bill_find due_within_days=30."), nil
	}

	return "bill_pay", tool, handler
}

// paySummary is bill_pay's one-line summary.
func paySummary(b billData, alreadyPaid bool) string {
	covered := ""
	if b.PaidForDueDate != nil {
		if d, err := time.Parse(time.DateOnly, *b.PaidForDueDate); err == nil {
			covered = " for " + d.Format("Jan 2")
		}
	}
	next := ""
	if d := b.dueText(); d != "" {
		next = "; next " + d
	}
	if alreadyPaid {
		return fmt.Sprintf("%s was already paid%s; nothing changed%s.", b.Name, covered, next)
	}
	amount := ""
	if b.AmountPaid != nil {
		amount = " " + money(*b.AmountPaid)
	}
	return fmt.Sprintf("Paid %s%s%s%s.", b.Name, amount, covered, next)
}

// requiredBillID reads the required bill_id argument.
func requiredBillID(args map[string]any) (uuid.UUID, error) {
	id, err := optionalUUID(args, "bill_id")
	if err != nil {
		return uuid.Nil, err
	}
	if id == nil {
		return uuid.Nil, badArg("bill_id", "bill_id is required.", "Pass the bill's id as bill_id. Call bill_find to find it.")
	}
	return *id, nil
}

// parseBillPayInput validates bill_pay arguments.
func parseBillPayInput(args map[string]any) (billPayInput, error) {
	var in billPayInput
	var err error
	if in.billID, err = requiredBillID(args); err != nil {
		return in, err
	}

	paidDate, err := optionalString(args, "paid_date")
	if err != nil {
		return in, err
	}
	if paidDate != nil {
		d, err := time.Parse(time.DateOnly, *paidDate)
		if err != nil {
			return in, badArg("paid_date", fmt.Sprintf("paid_date %q is not a date: paid_date must be YYYY-MM-DD.", *paidDate),
				"Pass paid_date as YYYY-MM-DD, e.g. 2026-11-20, or leave it out for today.")
		}
		in.paidDate = &d
	}

	amount, ok, err := parseAmount(args)
	if err != nil {
		return in, err
	}
	if ok {
		in.amount = &amount
	}
	return in, nil
}

// Due is where a monthly bill stands on a given day, as every bill tool
// (bill_find, bill_create, bill_update) reports it.
type Due struct {
	// Next is the next due date that still needs paying (midnight UTC).
	Next time.Time
	// DaysUntil is the whole days from today to Next.
	DaysUntil int
	// Status is "paid" when paid_date covers the current due date (so Next
	// is the one after it), otherwise "due".
	Status string
	// PaidFor is the due date paid_date covers, nil when never paid.
	PaidFor *time.Time
}

// BillDue is the single rule for when a bill is due: billDue (due_day
// clamped to the month, paid_date moving the bill to the following due date)
// as of today, which should come from HouseholdToday. Exported so other
// packages (e.g. the daily briefing) give the same answer as the bill tools.
func BillDue(today time.Time, dueDay int, paidDate *time.Time) Due {
	info := billDue(today, dueDay, paidDate)
	return Due{Next: info.next, DaysUntil: daysBetween(today, info.next), Status: info.status, PaidFor: info.paidFor}
}

// HouseholdToday is householdToday for other packages: today's date in the
// household's timezone as midnight UTC.
func HouseholdToday(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, householdID string) (time.Time, error) {
	return householdToday(ctx, q, householdID)
}

// householdToday returns today's date in the household's timezone, as
// midnight UTC so it compares with dates scanned from DATE columns. An empty
// or unknown timezone falls back to UTC, as the calendar tools do.
func householdToday(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, householdID string) (time.Time, error) {
	var tz string
	if err := q.QueryRow(ctx, `SELECT timezone FROM households WHERE id = $1`, householdID).Scan(&tz); err != nil {
		return time.Time{}, fmt.Errorf("load household timezone: %w", err)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	y, m, d := time.Now().In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC), nil
}

// paidForDueDate returns the due date a payment made on paid pays: the most
// recent due date (on or before paid) when the payment is at most
// lateGraceDays late, otherwise the first due date on or after paid. It
// depends only on paid and dueDay because bills.paid_date is the only thing
// stored about a payment.
func paidForDueDate(paid time.Time, dueDay int) time.Time {
	y, m, d := paid.Date()
	paid = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	last := dueDateInMonth(y, m, dueDay)
	if last.After(paid) {
		last = dueDateInMonth(y, m-1, dueDay)
	}
	if paid.Sub(last) <= lateGraceDays*24*time.Hour {
		return last
	}
	return nextDueDate(paid, dueDay)
}

// parseUpdateBillInput validates bill_update arguments (already normalized).
// A field that is absent or null is left unchanged. name, amount, due_day
// and autopay cannot be cleared; a blank string clears the other text fields.
func parseUpdateBillInput(args map[string]any) (billUpdate, error) {
	var u billUpdate
	var err error
	if u.billID, err = requiredBillID(args); err != nil {
		return u, err
	}

	if given(args, "name") {
		name, err := parseName(args)
		if err != nil {
			return u, err
		}
		if name == nil {
			return u, badArg("name", "name cannot be blank.", "Pass a new name, or leave name out to keep it.")
		}
		u.set("name", "name", *name)
	}

	if amount, ok, err := parseAmount(args); err != nil {
		return u, err
	} else if ok {
		u.set("amount", "amount", amount)
	}

	if dueDay, ok, err := parseDueDay(args); err != nil {
		return u, err
	} else if ok {
		u.set("due_day", "due_day", dueDay)
	}

	if given(args, "category") {
		category, err := parseCategory(args)
		if err != nil {
			return u, err
		}
		u.set("category", "category", category)
	}

	if autopay, ok, err := optionalBool(args, "autopay"); err != nil {
		return u, err
	} else if ok {
		u.set("autopay", "is_autopay", autopay)
	}

	if given(args, "contact_id") {
		contactID, err := optionalUUID(args, "contact_id")
		if err != nil {
			return u, err
		}
		u.contactID = contactID
		u.set("contact_id", "vendor_id", contactID)
	}

	if given(args, "entity_type") || given(args, "entity_id") {
		if !given(args, "entity_type") || !given(args, "entity_id") {
			return u, entityPairError()
		}
		entityType, entityID, err := parseEntity(args)
		if err != nil {
			return u, err
		}
		if (entityType == nil) != (entityID == nil) {
			return u, badArg("entity_type", "entity_type and entity_id must be set or cleared together.",
				"Pass both as values, or both as empty strings to clear them.")
		}
		u.set("entity_type", "entity_type", entityType)
		u.set("entity_id", "entity_id", entityID)
	}

	if given(args, "payment_url") {
		paymentURL, err := parsePaymentURL(args)
		if err != nil {
			return u, err
		}
		u.set("payment_url", "payment_url", paymentURL)
	}

	if given(args, "notes") {
		notes, err := optionalString(args, "notes")
		if err != nil {
			return u, err
		}
		u.set("notes", "notes", notes)
	}

	if len(u.columns) == 0 {
		return u, badArg("", "No fields to update.",
			"Pass bill_id plus at least one of name, amount, due_day, category, autopay, contact_id, notes.")
	}
	return u, nil
}

// given reports whether args has a non-null value for key.
func given(args map[string]any, key string) bool {
	v, present := args[key]
	return present && v != nil
}

// entityPairError is the error for entity_type without entity_id or the
// reverse (hidden aliases kept until domain model v2).
func entityPairError() error {
	return badArg("entity_type", "entity_type and entity_id must be provided together.",
		"Pass both entity_type and entity_id, or neither.")
}

// parseCreateBillInput validates bill_create arguments (already normalized).
func parseCreateBillInput(args map[string]any) (createBillInput, error) {
	var in createBillInput

	name, err := parseName(args)
	if err != nil {
		return in, err
	}
	if name == nil {
		return in, badArg("name", "name is required.", "Pass the bill's name, e.g. Electric.")
	}
	in.name = *name

	amount, ok, err := parseAmount(args)
	if err != nil {
		return in, err
	}
	if !ok {
		return in, badArg("amount", "amount is required.", "Pass the amount due each month as a number, e.g. 142.50.")
	}
	in.amount = amount

	dueDay, ok, err := parseDueDay(args)
	if err != nil {
		return in, err
	}
	if !ok {
		return in, badArg("due_day", "due_day is required.", "Pass the day of the month it is due as a number from 1 to 31.")
	}
	in.dueDay = dueDay

	if in.category, err = parseCategory(args); err != nil {
		return in, err
	}
	if in.autopay, _, err = optionalBool(args, "autopay"); err != nil {
		return in, err
	}
	if in.contactID, err = optionalUUID(args, "contact_id"); err != nil {
		return in, err
	}
	if in.entityType, in.entityID, err = parseEntity(args); err != nil {
		return in, err
	}
	if (in.entityType == nil) != (in.entityID == nil) {
		return in, entityPairError()
	}
	if in.paymentURL, err = parsePaymentURL(args); err != nil {
		return in, err
	}
	if in.notes, err = optionalString(args, "notes"); err != nil {
		return in, err
	}
	return in, nil
}

// Field parsers shared by bill_create and bill_update so both tools apply
// the same rules and return the same messages. Each returns nil (or ok=false)
// when the argument is absent, null or blank.

func parseName(args map[string]any) (*string, error) {
	name, err := optionalString(args, "name")
	if err != nil {
		return nil, err
	}
	if name != nil && utf8.RuneCountInString(*name) > maxNameLen {
		return nil, badArg("name", fmt.Sprintf("name must be at most %d characters.", maxNameLen), "Pass a shorter name.")
	}
	return name, nil
}

func parseAmount(args map[string]any) (float64, bool, error) {
	amount, ok, err := number(args, "amount")
	if err != nil || !ok {
		return 0, ok, err
	}
	if amount < 0 || amount > maxAmount {
		return 0, false, badArg("amount", fmt.Sprintf("amount must be between 0 and %.2f.", maxAmount),
			"Pass the amount as a plain number in the family's currency, e.g. 142.50.")
	}
	return math.Round(amount*100) / 100, true, nil
}

func parseDueDay(args map[string]any) (int, bool, error) {
	return wholeNumber(args, "due_day", 1, 31)
}

func parseCategory(args map[string]any) (*string, error) {
	category, err := optionalString(args, "category")
	if err != nil {
		return nil, err
	}
	if category != nil && utf8.RuneCountInString(*category) > maxCategoryLen {
		return nil, badArg("category", fmt.Sprintf("category must be at most %d characters.", maxCategoryLen),
			"Pass a short category, e.g. Utilities.")
	}
	return category, nil
}

// optionalBool reads a boolean argument; ok is false when absent or null.
func optionalBool(args map[string]any, key string) (val, ok bool, err error) {
	v, present := args[key]
	if !present || v == nil {
		return false, false, nil
	}
	b, isBool := v.(bool)
	if !isBool {
		return false, false, badArg(key, key+" must be a boolean.", fmt.Sprintf("Pass %s as true or false, not as text.", key))
	}
	return b, true, nil
}

// parseEntity parses entity_type and entity_id. The caller checks that they
// are given together.
func parseEntity(args map[string]any) (*string, *uuid.UUID, error) {
	entityType, err := optionalString(args, "entity_type")
	if err != nil {
		return nil, nil, err
	}
	entityID, err := optionalUUID(args, "entity_id")
	if err != nil {
		return nil, nil, err
	}
	if entityType != nil && utf8.RuneCountInString(*entityType) > maxEntityTypeLen {
		return nil, nil, badArg("entity_type", fmt.Sprintf("entity_type must be at most %d characters.", maxEntityTypeLen),
			"Pass property, vehicle or pet.")
	}
	return entityType, entityID, nil
}

func parsePaymentURL(args map[string]any) (*string, error) {
	paymentURL, err := optionalString(args, "payment_url")
	if err != nil || paymentURL == nil {
		return nil, err
	}
	u, err := url.Parse(*paymentURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, badArg("payment_url", "payment_url must be an http or https URL.",
			"Pass a full web address, e.g. https://example.com/pay, or leave it out.")
	}
	return paymentURL, nil
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
	"bill_id":    "Pass the bill's id (a UUID) from bill_find, not its name.",
	"id":         "Pass a bill's id (a UUID) from bill_find, or use query to search by name.",
	"contact_id": "Pass a contact's id (a UUID) from contact_find, not its name.",
	"entity_id":  "Pass the id (a UUID) of the property, vehicle or pet.",
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

// number reads a numeric argument. JSON-decoded arguments arrive as float64;
// integer types are accepted for direct callers. ok is false when the
// argument is absent or null.
func number(args map[string]any, key string) (val float64, ok bool, err error) {
	v, present := args[key]
	if !present || v == nil {
		return 0, false, nil
	}
	notNumber := badArg(key, key+" must be a number.", fmt.Sprintf("Pass %s as a number without quotes.", key))
	switch n := v.(type) {
	case float64:
		val = n
	case int:
		val = float64(n)
	case int64:
		val = float64(n)
	case json.Number:
		f, perr := n.Float64()
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

// nextDueDate returns the first date on or after today that falls on dueDay
// of its month. When a month is shorter than dueDay, the bill is due on that
// month's last day (due_day 31 is due on 30 April).
func nextDueDate(today time.Time, dueDay int) time.Time {
	y, m, _ := today.Date()
	today = time.Date(y, m, today.Day(), 0, 0, 0, 0, time.UTC)
	candidate := dueDateInMonth(y, m, dueDay)
	if candidate.Before(today) {
		candidate = dueDateInMonth(y, m+1, dueDay)
	}
	return candidate
}

// dueDateInMonth returns dueDay of the given month, clamped to the month's
// last day. time.Date normalises a month of 13 into January of the next year.
func dueDateInMonth(year int, month time.Month, dueDay int) time.Time {
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if dueDay > lastDay {
		dueDay = lastDay
	}
	return time.Date(year, month, dueDay, 0, 0, 0, 0, time.UTC)
}
