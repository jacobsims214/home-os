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

	"home-os/mcp/internal/budgetcat"
	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// Column limits from the bills table (migrations 007 and 020).
const (
	maxNameLen     = 255
	maxCategoryLen = 100
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
	// bills.category is the bill's own label; the budget category has its
	// own param (#4702).
	"budget_category_id": "budget_category",
	"budget":             "budget_category",
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
	name      string
	amount    float64
	frequency string
	dueDay    *int       // monthly bills only
	dueDate   *time.Time // every other frequency
	category  *string
	// budgetCategory is the budget category ref (name or UUID), nil when
	// absent or blank.
	budgetCategory *string
	autopay        bool
	contactID      *uuid.UUID
	subject        subject
	paymentURL     *string
	notes          *string
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
// It inserts a bill for the authenticated household and returns it with its
// next due date. frequency defaults to monthly, which needs due_day; any
// other frequency (quarterly, semiannual, annual, once) needs due_date and
// no due_day (044's bills_due_day_monthly_check, #4468). rrule is left unset. The bill
// gets a repeating all-day event on the Bills calendar (syncBillEvent).
// Invalid input returns an isError result and inserts nothing. Loop guard:
// the same name (any case), amount and schedule created in the household in
// the last 10 minutes returns that bill with duplicate true instead of a
// second row (bills have no author column, so the guard is household-wide).
// The bill may be about
// one property, vehicle or asset (typed subject columns, domain model v2);
// the legacy entity_type/entity_id params are mapped onto them.
func NewBillCreateTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Add a bill (electric, Netflix, property tax). Give due_day for monthly bills, or frequency " +
			"plus due_date for quarterly, annual or one-time ones. Use this, not a calendar event."),
		mcp.WithString("name", mcp.Description("Bill name, e.g. Electric"), mcp.Required()),
		mcp.WithNumber("amount", mcp.Description("Amount due each time, e.g. 142.50"), mcp.Required(), mcp.Min(0)),
		mcp.WithString("frequency", mcp.Description("How often it is due (default monthly)"), mcp.Enum(frequencies...)),
		mcp.WithNumber("due_day", mcp.Description("Monthly bills: day of the month due, 1-31"), mcp.Min(1), mcp.Max(31)),
		mcp.WithString("due_date", mcp.Description("Other bills: next due date, YYYY-MM-DD; repeats every 3/6/12 months")),
		mcp.WithString("category", mcp.Description("Category, e.g. Utilities, Insurance, Subscription")),
		mcp.WithString("budget_category", mcp.Description("Budget category name or id (default: one named like category)")),
		mcp.WithBoolean("autopay", mcp.Description("true when it is paid automatically (default false)")),
		mcp.WithString("contact_id", mcp.Description("Contact (vendor) id it is paid to, from contact_find")),
		mcp.WithString("property_id", mcp.Description("House or other property it is for (UUID from property_find)")),
		mcp.WithString("vehicle_id", mcp.Description("Car or other vehicle it is for (UUID from vehicle_find)")),
		mcp.WithString("asset_id", mcp.Description("Thing it is for (UUID from asset_find)")),
		mcp.WithString("notes", mcp.Description("Free-text notes, e.g. account number hint")),
		respond.Schema[billData](),
	}
	tool := mcp.NewTool("bill_create", append(opts, writeAnnotations("Create bill", false, false)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}

		args := normalizeArgs(req.GetArguments())
		if err := subjectAliases(args); err != nil {
			return invalid(err, createExample), nil
		}
		in, err := parseCreateBillInput(args)
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
		if res, err := checkSubject(ctx, tx, in.subject, claims.HouseholdID, createExample); err != nil {
			return respond.Failed("create the bill", err), nil
		} else if res != nil {
			return res, nil
		}

		today, err := householdToday(ctx, tx, claims.HouseholdID)
		if err != nil {
			return respond.Failed("create the bill", err), nil
		}
		cat, res, err := createBudgetCategory(ctx, tx, in, claims.HouseholdID)
		if err != nil {
			return respond.Failed("create the bill", err), nil
		} else if res != nil {
			return res, nil
		}
		var categoryID *uuid.UUID
		if cat != nil {
			categoryID = &cat.ID
		}

		dup, err := scanBill(tx.QueryRow(ctx,
			`SELECT `+billColumns+`
			 FROM bills
			 WHERE household_id = $1 AND lower(name) = lower($2) AND amount = $3
			   AND frequency = $4 AND due_day IS NOT DISTINCT FROM $5::int AND due_date IS NOT DISTINCT FROM $6::date
			   AND created_at > NOW() - $7::interval
			 ORDER BY created_at DESC
			 LIMIT 1`,
			claims.HouseholdID, in.name, in.amount, in.frequency, in.dueDay, in.dueDate, duplicateWindow,
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
			                    vendor_id, property_id, vehicle_id, asset_id, payment_url, notes, frequency, due_date,
			                    budget_category_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
			 RETURNING `+billColumns,
			claims.HouseholdID, in.name, in.amount, in.dueDay, in.category, in.autopay,
			in.contactID, in.subject.propertyID, in.subject.vehicleID, in.subject.assetID, in.paymentURL, in.notes,
			in.frequency, in.dueDate, categoryID,
		))
		if err != nil {
			if res := subjectDBError(err, createExample); res != nil {
				return res, nil
			}
			return respond.Failed("create the bill", err), nil
		}

		if _, err := syncBillEvent(ctx, tx, claims.HouseholdID, r); err != nil {
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
			fmt.Sprintf("Created bill %s, %s %s.", b.Name, money(in.amount), b.dueText())+budgetText(cat),
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
	subject   subject    // the new subject when any subject param was given
	// The schedule params, nil when not given. They are checked against the
	// stored row (resolveSchedule) because 044 ties due_day to monthly.
	frequency *string
	dueDay    *int
	dueDate   *time.Time
	// budgetCategorySet is true when budget_category was given;
	// budgetCategory is then the ref, or nil to clear it.
	budgetCategorySet bool
	budgetCategory    *string
}

// scheduleGiven reports whether frequency, due_day or due_date was given.
func (u billUpdate) scheduleGiven() bool {
	return u.frequency != nil || u.dueDay != nil || u.dueDate != nil
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
// reported as not found. Invalid input changes nothing. Giving any of
// property_id / vehicle_id / asset_id replaces the bill's subject (the
// others are cleared); a blank one makes it family-level.
func NewBillUpdateTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Change a bill; only the given fields change. An empty string clears category, contact_id or notes. " +
			"Returns the bill with its next due date."),
		mcp.WithString("bill_id", mcp.Description("The bill's id (UUID), from bill_find"), mcp.Required()),
		mcp.WithString("name", mcp.Description("New name, e.g. Electric")),
		mcp.WithNumber("amount", mcp.Description("New amount due each time, e.g. 142.50"), mcp.Min(0)),
		mcp.WithString("frequency", mcp.Description("How often it is due; monthly needs due_day, others due_date"), mcp.Enum(frequencies...)),
		mcp.WithNumber("due_day", mcp.Description("Monthly bills: day of the month due, 1-31"), mcp.Min(1), mcp.Max(31)),
		mcp.WithString("due_date", mcp.Description("Other bills: next due date, YYYY-MM-DD")),
		mcp.WithString("category", mcp.Description("Category, e.g. Utilities; empty string clears it")),
		mcp.WithString("budget_category", mcp.Description("Budget category name or id; a new name creates it; \"\" clears it")),
		mcp.WithBoolean("autopay", mcp.Description("true when it is paid automatically")),
		mcp.WithString("contact_id", mcp.Description("Contact (vendor) id from contact_find; empty string clears it")),
		mcp.WithString("property_id", mcp.Description("Move it to this property (UUID); \"\" unties it")),
		mcp.WithString("vehicle_id", mcp.Description("Move it to this vehicle (UUID); \"\" unties it")),
		mcp.WithString("asset_id", mcp.Description("Move it to this asset (UUID); \"\" unties it")),
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
		raw := normalizeArgs(req.GetArguments())
		if err := subjectAliases(raw); err != nil {
			return invalid(err, example), nil
		}
		in, err := parseUpdateBillInput(raw)
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
		if res, err := checkSubject(ctx, tx, in.subject, claims.HouseholdID, example); err != nil {
			return respond.Failed("update the bill", err), nil
		} else if res != nil {
			return res, nil
		}

		if in.scheduleGiven() {
			// Lock the row and merge the schedule params with what is stored.
			var stored Schedule
			err := tx.QueryRow(ctx,
				`SELECT frequency, due_day, due_date FROM bills WHERE id = $1 AND household_id = $2 FOR UPDATE`,
				in.billID, claims.HouseholdID,
			).Scan(&stored.Frequency, &stored.DueDay, &stored.DueDate)
			if errors.Is(err, pgx.ErrNoRows) {
				return respond.NotFound("bill", in.billID.String(), "Call bill_find to get the bill_id."), nil
			}
			if err != nil {
				return respond.Failed("update the bill", err), nil
			}
			next, err := resolveSchedule(stored, in)
			if err != nil {
				return invalid(err, example), nil
			}
			in.columns = append(in.columns, "frequency", "due_day", "due_date")
			in.values = append(in.values, next.Frequency, next.DueDay, next.DueDate)
			if !sameSchedule(stored, next) {
				// A stored paid_for_due_date is a date on the old schedule:
				// when the schedule really changes, clear it so coverage is
				// inferred from paid_date for the new one (#4655), as the
				// API's Repo.Update does.
				in.columns = append(in.columns, "paid_for_due_date")
				in.values = append(in.values, nil)
			}
		}

		var cat *budgetcat.Category
		if in.budgetCategorySet {
			var categoryID *uuid.UUID
			if in.budgetCategory != nil {
				var res *mcp.CallToolResult
				if cat, res, err = resolveBudgetCategory(ctx, tx, *in.budgetCategory, claims.HouseholdID, example); err != nil {
					return respond.Failed("update the bill", err), nil
				} else if res != nil {
					return res, nil
				}
				categoryID = &cat.ID
			}
			in.columns = append(in.columns, "budget_category_id")
			in.values = append(in.values, categoryID)
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
			if res := subjectDBError(err, example); res != nil {
				return res, nil
			}
			return respond.Failed("update the bill", err), nil
		}

		if _, err := syncBillEvent(ctx, tx, claims.HouseholdID, r); err != nil {
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
		summary += budgetText(cat)
		return respond.OK(summary, b,
			fmt.Sprintf("Mark it paid with bill_pay bill_id=%s.", b.ID)), nil
	}

	return "bill_update", tool, handler
}

// NewBillDeleteTool creates the bill_delete MCP tool (catalog v2, class X).
// It deletes one bill in the authenticated household and returns it as it
// was. A bill in another household, or one that doesn't exist (including a
// repeat call), is reported as not found and nothing is deleted. The bill's
// event on the Bills calendar (syncBillEvent) is deleted with it.
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

		// The mirror event goes first: the 038 rehome trigger would only
		// unlink it from the deleted bill. Rolled back if the bill is not
		// found.
		if err := deleteBillEvent(ctx, tx, claims.HouseholdID, id); err != nil {
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

// billPayInput is the validated bill_pay arguments. paidDate is nil when the
// caller left it to default to the household's today.
type billPayInput struct {
	billID     uuid.UUID
	paidDate   *time.Time
	forDueDate *time.Time
	amount     *float64
}

// NewBillPayTool creates the bill_pay MCP tool (catalog v2, class U),
// replacing the S1 working name mark_bill_paid.
//
// It records a bill in the authenticated household as paid by setting
// bills.paid_date and bills.paid_for_due_date (there is no payments table
// yet, so no history is kept and amount is echoed back as amount_paid, not
// stored). paid_date defaults to today in the household's timezone. The due
// date the payment covers is for_due_date when given (#4655: it must be one
// of the bill's due dates and at most a month after today), otherwise
// inferred from paid_date (paidForDueDate), as before for_due_date existed.
// Paying again for a due date that is already covered (or an earlier one)
// changes nothing and returns already_paid true.
func NewBillPayTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Record a bill as paid. " +
			"Calling it again for the same due date changes nothing. Returns the due date paid and the next unpaid due date, as bill_find reports it."),
		mcp.WithString("bill_id", mcp.Description("The bill's id (UUID), from bill_find"), mcp.Required()),
		mcp.WithString("paid_date", mcp.Description("Date paid, YYYY-MM-DD, e.g. 2026-11-20; default today")),
		mcp.WithString("for_due_date", mcp.Description("Due date being paid, YYYY-MM-DD; pass it when paid over 7 days late")),
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

		c, hasSchedule := cadence(r.schedule())
		if in.forDueDate != nil {
			if res := checkForDueDate(r, *in.forDueDate, today); res != nil {
				return res, nil
			}
		}

		// target is the due date this payment covers: for_due_date, else
		// inferred from the payment date by the bill's frequency. nil for a
		// bill with no schedule.
		var target *time.Time
		if hasSchedule {
			target = coveredDueDate(c, &paid, in.forDueDate)
		}
		alreadyPaid := false
		if hasSchedule {
			// Already paid when the stored coverage (paid_for_due_date, or
			// inferred from paid_date for rows written before it existed)
			// is this due date or a later one; coverage never moves back.
			if stored := coveredDueDate(c, r.PaidDate, r.PaidFor); stored != nil {
				alreadyPaid = !stored.Before(*target)
			}
		} else if r.PaidDate != nil {
			alreadyPaid = !r.PaidDate.Before(paid)
		}

		// Already paid: r keeps its stored payment, which is the effective
		// one. Otherwise record this payment and the due date it covers.
		if !alreadyPaid {
			if err := tx.QueryRow(ctx,
				`UPDATE bills SET paid_date = $3, paid_for_due_date = $4, updated_at = NOW()
				 WHERE id = $1 AND household_id = $2
				 RETURNING updated_at`,
				r.ID, claims.HouseholdID, paid, target,
			).Scan(&r.UpdatedAt); err != nil {
				return respond.Failed("record the payment", err), nil
			}
			r.PaidDate, r.PaidFor = &paid, target

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

		// r.PaidDate and r.PaidFor are now the effective payment, so data
		// applies BillDue exactly as bill_find does: next_due_date is the
		// next unpaid due date as of today (never in the past, even for a
		// back-dated payment), status is due/paid, and paid_for_due_date is
		// the due date this payment covers (#4653).
		b := r.data(today)
		b.AlreadyPaid = &alreadyPaid
		b.AmountPaid = in.amount
		if b.AmountPaid == nil {
			b.AmountPaid = r.Amount
		}

		return respond.OK(paySummary(b, alreadyPaid), b,
			"See what's due next with bill_find due_within_days=30."), nil
	}

	return "bill_pay", tool, handler
}

// checkForDueDate validates bill_pay's for_due_date against the bill: it must
// be one of the bill's due dates (due_day of its month for a monthly bill,
// the anchored date every 3/6/12 months from due_date, or due_date itself
// for a once bill, clamped to short months) and no more than one period
// (one month for a monthly bill) after today, so a typo can't mark a year
// of bills paid. It returns the tool error, or nil when valid.
func checkForDueDate(r billRow, forDue, today time.Time) *mcp.CallToolResult {
	given := forDue.Format(time.DateOnly)
	c, ok := cadence(r.schedule())
	if !ok {
		return respond.Invalid("for_due_date",
			fmt.Sprintf("Bill %s has no due day or due date, so for_due_date %s cannot be checked.", r.Name, given),
			"Leave for_due_date out, or set the bill's due_day (monthly) or due_date with bill_update first.",
			map[string]any{"bill_id": r.ID.String()})
	}
	if last, ok := c.OnOrBefore(forDue); !ok || !last.Equal(forDue) {
		var want time.Time
		var which string
		if c.Step == 1 {
			want = dueDateInMonth(forDue.Year(), forDue.Month(), c.Day)
			which = fmt.Sprintf("due on day %d of each month (%s that month)", c.Day, want.Format(time.DateOnly))
		} else {
			if want, ok = c.OnOrAfter(forDue); !ok {
				want = c.Anchor
			}
			which = fmt.Sprintf("%s (next %s)", scheduleText(r.schedule()), want.Format(time.DateOnly))
		}
		return respond.Invalid("for_due_date",
			fmt.Sprintf("for_due_date %s is not a due date of %s, which is %s.", given, r.Name, which),
			fmt.Sprintf("Pass the due date being paid, e.g. %s, or leave for_due_date out.", want.Format(time.DateOnly)),
			map[string]any{"bill_id": r.ID.String(), "for_due_date": want.Format(time.DateOnly)})
	}
	if c.Step == 0 {
		return nil // a once bill has one due date; paying it early is fine
	}
	if limit := today.AddDate(0, c.Step, 0); forDue.After(limit) {
		next, _ := c.OnOrAfter(today)
		period := "a month"
		if c.Step > 1 {
			period = fmt.Sprintf("%d months", c.Step)
		}
		return respond.Invalid("for_due_date",
			fmt.Sprintf("for_due_date %s is more than %s after today (%s in the family's timezone).",
				given, period, today.Format(time.DateOnly)),
			fmt.Sprintf("Pay one due date at a time: pass the next one, %s, or leave for_due_date out.", next.Format(time.DateOnly)),
			map[string]any{"bill_id": r.ID.String(), "for_due_date": next.Format(time.DateOnly)})
	}
	return nil
}

// scheduleText describes a valid schedule for messages, e.g. "due on day 20
// of each month", "due every 3 months from 2026-01-15" or "due once, on
// 2026-03-01".
func scheduleText(s Schedule) string {
	c, ok := cadence(s)
	switch {
	case !ok:
		return "not scheduled"
	case c.Step == 1:
		return fmt.Sprintf("due on day %d of each month", c.Day)
	case c.Step == 0:
		return "due once, on " + c.Anchor.Format(time.DateOnly)
	default:
		return fmt.Sprintf("due every %d months from %s", c.Step, c.Anchor.Format(time.DateOnly))
	}
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

	forDue, err := optionalString(args, "for_due_date")
	if err != nil {
		return in, err
	}
	if forDue != nil {
		d, err := time.Parse(time.DateOnly, *forDue)
		if err != nil {
			return in, badArg("for_due_date", fmt.Sprintf("for_due_date %q is not a date: for_due_date must be YYYY-MM-DD.", *forDue),
				"Pass the due date being paid as YYYY-MM-DD, e.g. 2026-11-20, or leave it out.")
		}
		in.forDueDate = &d
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

	if f, err := parseFrequency(args); err != nil {
		return u, err
	} else if f != nil {
		u.frequency = f
		u.params = append(u.params, "frequency")
	}
	if dueDay, ok, err := parseDueDay(args); err != nil {
		return u, err
	} else if ok {
		u.dueDay = &dueDay
		u.params = append(u.params, "due_day")
	}
	if d, err := parseDueDate(args); err != nil {
		return u, err
	} else if d != nil {
		u.dueDate = d
		u.params = append(u.params, "due_date")
	}

	if given(args, "category") {
		category, err := parseCategory(args)
		if err != nil {
			return u, err
		}
		u.set("category", "category", category)
	}
	if given(args, "budget_category") {
		if u.budgetCategory, err = parseBudgetCategory(args); err != nil {
			return u, err
		}
		u.budgetCategorySet = true
		u.params = append(u.params, "budget_category")
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

	if subjectGiven(args) {
		if u.subject, err = parseSubject(args); err != nil {
			return u, err
		}
		for _, p := range subjectParams {
			if given(args, p) {
				u.params = append(u.params, p)
			}
		}
		u.columns = append(u.columns, "property_id", "vehicle_id", "asset_id")
		u.values = append(u.values, u.subject.propertyID, u.subject.vehicleID, u.subject.assetID)
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

	if len(u.columns) == 0 && !u.scheduleGiven() && !u.budgetCategorySet {
		return u, badArg("", "No fields to update.",
			"Pass bill_id plus at least one of name, amount, frequency, due_day, due_date, category, budget_category, autopay, contact_id, property_id, vehicle_id, asset_id, notes.")
	}
	return u, nil
}

// given reports whether args has a non-null value for key.
func given(args map[string]any, key string) bool {
	v, present := args[key]
	return present && v != nil
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

	if in.frequency, in.dueDay, in.dueDate, err = parseCreateSchedule(args); err != nil {
		return in, err
	}

	if in.category, err = parseCategory(args); err != nil {
		return in, err
	}
	if in.budgetCategory, err = parseBudgetCategory(args); err != nil {
		return in, err
	}
	if in.autopay, _, err = optionalBool(args, "autopay"); err != nil {
		return in, err
	}
	if in.contactID, err = optionalUUID(args, "contact_id"); err != nil {
		return in, err
	}
	if in.subject, err = parseSubject(args); err != nil {
		return in, err
	}
	if in.paymentURL, err = parsePaymentURL(args); err != nil {
		return in, err
	}
	if in.notes, err = optionalString(args, "notes"); err != nil {
		return in, err
	}
	return in, nil
}

// parseCreateSchedule reads bill_create's frequency (default monthly),
// due_day and due_date, applying 044's rule: a monthly bill needs due_day
// and has no due_date; any other bill needs due_date and has no due_day.
func parseCreateSchedule(args map[string]any) (frequency string, dueDay *int, dueDate *time.Time, err error) {
	f, err := parseFrequency(args)
	if err != nil {
		return "", nil, nil, err
	}
	day, hasDay, err := parseDueDay(args)
	if err != nil {
		return "", nil, nil, err
	}
	if hasDay {
		dueDay = &day
	}
	if dueDate, err = parseDueDate(args); err != nil {
		return "", nil, nil, err
	}
	frequency = FrequencyMonthly
	if f != nil {
		frequency = *f
	}
	if err := checkSchedule(Schedule{Frequency: frequency, DueDay: dueDay, DueDate: dueDate}, f != nil); err != nil {
		return "", nil, nil, err
	}
	return frequency, dueDay, dueDate, nil
}

// checkSchedule applies 044's rule to a bill's final schedule: a monthly
// bill needs due_day and no due_date, any other bill needs due_date and no
// due_day. frequencyGiven says whether the caller passed frequency (it
// changes the advice for a due_date on a monthly bill).
func checkSchedule(s Schedule, frequencyGiven bool) error {
	if s.Frequency == FrequencyMonthly {
		if s.DueDate != nil {
			if frequencyGiven {
				return badArg("due_date", "due_date is only for bills that are not monthly; a monthly bill uses due_day.",
					"Pass due_day (1-31) for a monthly bill, or a frequency such as annual with due_date.")
			}
			return badArg("due_date", "due_date needs a frequency: monthly bills use due_day.",
				"Pass frequency (quarterly, semiannual, annual or once) with due_date, or due_day for a monthly bill.")
		}
		if s.DueDay == nil {
			return badArg("due_day", "due_day is required for a monthly bill.",
				"Pass the day of the month it is due as a number from 1 to 31, or a frequency with due_date.")
		}
		return nil
	}
	if s.DueDay != nil {
		return badArg("due_day", fmt.Sprintf("due_day is only for monthly bills; %s bills use due_date.", s.Frequency),
			fmt.Sprintf("Pass due_date (YYYY-MM-DD) for %s bills, or frequency monthly with due_day.", s.Frequency))
	}
	if s.DueDate == nil {
		return badArg("due_date", fmt.Sprintf("due_date is required for %s bills.", s.Frequency),
			"Pass the next due date as YYYY-MM-DD, e.g. 2027-04-15.")
	}
	return nil
}

// resolveSchedule merges bill_update's schedule params with the stored row.
// A value carried over from the row that no longer fits the frequency is
// dropped (switching annual to monthly drops due_date), as the API does; a
// conflicting value the caller passed is an error.
func resolveSchedule(stored Schedule, u billUpdate) (Schedule, error) {
	next := stored
	if next.Frequency == "" {
		next.Frequency = FrequencyMonthly
	}
	if u.frequency != nil {
		next.Frequency = *u.frequency
	}
	if u.dueDay != nil {
		next.DueDay = u.dueDay
	}
	if u.dueDate != nil {
		next.DueDate = u.dueDate
	}
	if next.Frequency == FrequencyMonthly {
		if u.dueDate != nil {
			return next, checkSchedule(next, u.frequency != nil)
		}
		next.DueDate = nil
	} else {
		if u.dueDay != nil {
			return next, checkSchedule(next, u.frequency != nil)
		}
		next.DueDay = nil
	}
	if err := checkSchedule(next, u.frequency != nil); err != nil {
		var ae *argError
		if errors.As(err, &ae) && u.frequency != nil {
			ae.message = fmt.Sprintf("Changing %s to %s needs %s.", frequencyName(stored), next.Frequency, ae.field)
		}
		return next, err
	}
	return next, nil
}


// sameSchedule reports whether two schedules are the same stored values.
func sameSchedule(a, b Schedule) bool {
	sameDay := (a.DueDay == nil) == (b.DueDay == nil) && (a.DueDay == nil || *a.DueDay == *b.DueDay)
	sameDate := (a.DueDate == nil) == (b.DueDate == nil) && (a.DueDate == nil || midnight(*a.DueDate).Equal(midnight(*b.DueDate)))
	return frequencyName(a) == frequencyName(b) && sameDay && sameDate
}

// frequencyAliases maps common words to bills.frequency values.
var frequencyAliases = map[string]string{
	"month": FrequencyMonthly, "every month": FrequencyMonthly,
	"quarter": FrequencyQuarterly, "every 3 months": FrequencyQuarterly,
	"semi-annual": FrequencySemiannual, "semi_annual": FrequencySemiannual, "semi annual": FrequencySemiannual,
	"semiannually": FrequencySemiannual, "biannual": FrequencySemiannual, "half-yearly": FrequencySemiannual,
	"twice a year": FrequencySemiannual, "every 6 months": FrequencySemiannual,
	"yearly": FrequencyAnnual, "annually": FrequencyAnnual, "year": FrequencyAnnual, "every year": FrequencyAnnual,
	"one-time": FrequencyOnce, "one time": FrequencyOnce, "one_time": FrequencyOnce, "onetime": FrequencyOnce, "one-off": FrequencyOnce,
}

// parseFrequency reads frequency (any case, common words accepted); nil when
// absent, null or blank.
func parseFrequency(args map[string]any) (*string, error) {
	f, err := optionalString(args, "frequency")
	if err != nil || f == nil {
		return nil, err
	}
	v := strings.ToLower(*f)
	if a, ok := frequencyAliases[v]; ok {
		v = a
	}
	if _, ok := frequencyMonths(v); !ok {
		return nil, badArg("frequency", fmt.Sprintf("frequency %q is not one of %s.", *f, strings.Join(frequencies, ", ")),
			"Pass monthly, quarterly, semiannual, annual or once.")
	}
	return &v, nil
}

// parseDueDate reads due_date as YYYY-MM-DD; nil when absent, null or blank.
func parseDueDate(args map[string]any) (*time.Time, error) {
	s, err := optionalString(args, "due_date")
	if err != nil || s == nil {
		return nil, err
	}
	d, err := time.Parse(time.DateOnly, *s)
	if err != nil {
		return nil, badArg("due_date", fmt.Sprintf("due_date %q is not a date: due_date must be YYYY-MM-DD.", *s),
			"Pass due_date as YYYY-MM-DD, e.g. 2027-04-15.")
	}
	return &d, nil
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
// parseBudgetCategory reads budget_category: a budget category name or id,
// nil when absent or blank.
func parseBudgetCategory(args map[string]any) (*string, error) {
	c, err := optionalString(args, "budget_category")
	if err != nil {
		return nil, err
	}
	if c != nil && utf8.RuneCountInString(*c) > budgetcat.MaxNameLen {
		return nil, badArg("budget_category", fmt.Sprintf("budget_category must be at most %d characters.", budgetcat.MaxNameLen),
			"Pass a short budget category, e.g. Utilities.")
	}
	return c, nil
}

// resolveBudgetCategory resolves a budget_category ref (a name, created when
// the family has none, or a UUID of one of its categories).
func resolveBudgetCategory(ctx context.Context, tx pgx.Tx, ref, householdID string, example map[string]any) (*budgetcat.Category, *mcp.CallToolResult, error) {
	c, err := budgetcat.Resolve(ctx, tx, householdID, ref)
	switch {
	case errors.Is(err, budgetcat.ErrNotFound):
		return nil, respond.Invalid("budget_category", fmt.Sprintf("No budget category with id %q in this family.", ref),
			"Pass the category's name instead, e.g. Utilities; a new name creates it.", example), nil
	case err != nil:
		return nil, nil, err
	}
	return &c, nil, nil
}

// createBudgetCategory picks bill_create's budget category: budget_category
// when given (resolved or created), else an existing budget category named
// like the bill's category (never created from it, so a free-text label
// does not add budget categories), else none.
func createBudgetCategory(ctx context.Context, tx pgx.Tx, in createBillInput, householdID string) (*budgetcat.Category, *mcp.CallToolResult, error) {
	if in.budgetCategory != nil {
		return resolveBudgetCategory(ctx, tx, *in.budgetCategory, householdID, createExample)
	}
	if in.category == nil {
		return nil, nil, nil
	}
	c, ok, err := budgetcat.Lookup(ctx, tx, householdID, *in.category)
	if err != nil || !ok {
		return nil, nil, err
	}
	return &c, nil, nil
}

// budgetText is the summary sentence for a bill filed under c, "" for nil.
func budgetText(c *budgetcat.Category) string {
	if c == nil {
		return ""
	}
	return " Filed under " + c.Describe() + "."
}

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
	"bill_id":     "Pass the bill's id (a UUID) from bill_find, not its name.",
	"id":          "Pass a bill's id (a UUID) from bill_find, or use query to search by name.",
	"contact_id":  "Pass a contact's id (a UUID) from contact_find, not its name.",
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
