package loan

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// duplicateWindow is the loan_create loop guard (catalog §1 "Loop guard").
const duplicateWindow = "10 minutes"

// fieldAliases are other names models use for the create/update fields.
var fieldAliases = map[string]string{
	"rate": "interest_rate", "apr": "interest_rate", "interest": "interest_rate",
	"payment": "monthly_payment", "monthly": "monthly_payment", "monthly_amount": "monthly_payment",
	"balance": "remaining_balance", "owed": "remaining_balance", "remaining": "remaining_balance",
	"amount": "original_amount", "principal": "original_amount", "loan_amount": "original_amount",
	"bank": "lender", "servicer": "lender",
	"due": "due_day", "payment_day": "due_day",
	"end_date": "term_end", "maturity_date": "term_end", "payoff_date": "term_end",
	"term": "term_months", "start": "start_date", "note": "notes",
}

// writeAliases are old (S1) and other parameter names for loan_update /
// loan_delete: the field aliases plus id/loan for loan_id.
var writeAliases = func() map[string]string {
	m := map[string]string{"id": "loan_id", "loan": "loan_id"}
	for k, v := range fieldAliases {
		m[k] = v
	}
	return m
}()

// createExample is a valid loan_create call for error examples.
var createExample = map[string]any{
	"name": "Mortgage", "original_amount": 320000, "remaining_balance": 251340.12,
	"interest_rate": 6.25, "monthly_payment": 1850, "due_day": 1,
}

// updateExample is a valid loan_update call for error examples.
var updateExample = map[string]any{"loan_id": exampleLoanID, "remaining_balance": 249500}

// loanFields is the validated create/update fields. Each pointer is nil when
// the param was absent; set records which params were given (a blank text
// value clears that column on update).
type loanFields struct {
	set              map[string]bool
	name             *string
	lender           *string
	originalAmount   *float64
	remainingBalance *float64
	interestRate     *float64
	termMonths       *int
	monthlyPayment   *float64
	dueDay           *int
	startDate        *time.Time
	termEnd          *time.Time
	notes            *string
	// subject is set when any of property_id / vehicle_id / asset_id was
	// given: the three columns are then replaced together.
	subject    bool
	propertyID *uuid.UUID
	vehicleID  *uuid.UUID
	assetID    *uuid.UUID
}

// fieldParams are the create/update params in the order summaries list them.
var fieldParams = []string{"name", "lender", "original_amount", "remaining_balance", "interest_rate",
	"term_months", "monthly_payment", "due_day", "start_date", "term_end", "notes", "property_id", "vehicle_id", "asset_id"}

// parseDate reads a YYYY-MM-DD (or YYYY-MM, the 1st of that month) param.
func parseDate(args map[string]any, key, example string) (*time.Time, error) {
	s, err := optionalString(args, key)
	if err != nil || s == nil {
		return nil, err
	}
	for _, layout := range []string{time.DateOnly, "2006-01"} {
		if d, perr := time.Parse(layout, *s); perr == nil {
			return &d, nil
		}
	}
	return nil, badArg(key, fmt.Sprintf("%s %q is not a date.", key, *s), fmt.Sprintf("Pass %s as YYYY-MM-DD, e.g. %s.", key, example))
}

// parseFields validates every create/update field present in args.
func parseFields(args map[string]any) (loanFields, error) {
	f := loanFields{set: map[string]bool{}}
	for _, p := range fieldParams {
		if given(args, p) {
			f.set[p] = true
		}
	}

	var err error
	if f.name, err = optionalString(args, "name"); err != nil {
		return f, err
	}
	if f.name != nil && utf8.RuneCountInString(*f.name) > maxNameLen {
		return f, badArg("name", fmt.Sprintf("name must be at most %d characters.", maxNameLen), "Pass a shorter name.")
	}
	if f.lender, err = optionalString(args, "lender"); err != nil {
		return f, err
	}
	if f.lender != nil && utf8.RuneCountInString(*f.lender) > maxNameLen {
		return f, badArg("lender", fmt.Sprintf("lender must be at most %d characters.", maxNameLen), "Pass a shorter lender name.")
	}
	for _, m := range []struct {
		key string
		dst **float64
	}{{"original_amount", &f.originalAmount}, {"remaining_balance", &f.remainingBalance}, {"monthly_payment", &f.monthlyPayment}} {
		v, ok, err := number(args, m.key)
		if err != nil {
			return f, err
		}
		if !ok {
			continue
		}
		if v < 0 || v > maxAmount {
			return f, badArg(m.key, fmt.Sprintf("%s must be between 0 and %.2f.", m.key, maxAmount),
				fmt.Sprintf("Pass %s as a plain number in the family's currency, e.g. 1850.00.", m.key))
		}
		v = math.Round(v*100) / 100
		*m.dst = &v
	}
	if v, ok, err := number(args, "interest_rate"); err != nil {
		return f, err
	} else if ok {
		if v < 0 || v > maxInterestRate {
			return f, badArg("interest_rate", fmt.Sprintf("interest_rate must be a percent from 0 to %.3f.", maxInterestRate),
				"Pass the annual rate in percent: 6.25 means 6.25%.")
		}
		v = math.Round(v*1000) / 1000
		f.interestRate = &v
	}
	if n, ok, err := wholeNumber(args, "term_months", 1, maxTermMonths); err != nil {
		return f, err
	} else if ok {
		f.termMonths = &n
	}
	if n, ok, err := wholeNumber(args, "due_day", 1, 31); err != nil {
		return f, err
	} else if ok {
		f.dueDay = &n
	}
	if f.startDate, err = parseDate(args, "start_date", "2021-06-01"); err != nil {
		return f, err
	}
	if f.termEnd, err = parseDate(args, "term_end", "2051-06-01"); err != nil {
		return f, err
	}
	if f.startDate != nil && f.termEnd != nil && f.termEnd.Before(*f.startDate) {
		return f, badArg("term_end", "term_end is before start_date.", "Pass a term_end on or after start_date.")
	}
	if f.notes, err = optionalString(args, "notes"); err != nil {
		return f, err
	}

	if subjectGiven(args) {
		f.subject = true
		if f.propertyID, err = optionalUUID(args, "property_id"); err != nil {
			return f, err
		}
		if f.vehicleID, err = optionalUUID(args, "vehicle_id"); err != nil {
			return f, err
		}
		if f.assetID, err = optionalUUID(args, "asset_id"); err != nil {
			return f, err
		}
		if f.vehicleID != nil && (f.propertyID != nil || f.assetID != nil) {
			return f, badArg("vehicle_id", "A loan on a vehicle can't also be on a property or asset.",
				"Pass only vehicle_id, or only property_id and/or asset_id.")
		}
	}
	return f, nil
}

// checkSubjects returns an isError result when a given subject id is not a
// row of this family. A row of another family looks the same as a missing
// one.
func checkSubjects(ctx context.Context, tx pgx.Tx, f loanFields, householdID string, example map[string]any) (*mcp.CallToolResult, error) {
	for _, s := range []struct {
		param, table, what, finder string
		id                         *uuid.UUID
	}{
		{"property_id", "properties", "property", "property_find", f.propertyID},
		{"vehicle_id", "vehicles", "vehicle", "vehicle_find", f.vehicleID},
		{"asset_id", "assets", "asset", "asset_find", f.assetID},
	} {
		if s.id == nil {
			continue
		}
		var exists bool
		// s.table comes from the literal list above, never from the caller.
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM `+s.table+` WHERE id = $1 AND household_id = $2)`,
			*s.id, householdID,
		).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return respond.Invalid(s.param, fmt.Sprintf("No %s with id %q in this family.", s.what, s.id.String()),
				fmt.Sprintf("Call %s to get the %s, or leave it out.", s.finder, s.param), example), nil
		}
	}
	return nil, nil
}

// dbError maps a constraint violation the checks above could not see (a
// subject deleted meanwhile, a CHECK) to an isError result; nil otherwise.
func dbError(err error, termEndGiven bool, example map[string]any) *mcp.CallToolResult {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch pgErr.Code {
	case "23503":
		field := "property_id"
		for _, k := range []string{"vehicle", "asset"} {
			if strings.Contains(pgErr.ConstraintName, k) || strings.Contains(pgErr.Message, k) {
				field = k + "_id"
			}
		}
		return respond.Invalid(field, "That "+strings.TrimSuffix(field, "_id")+" does not exist in this family.",
			"Call "+strings.TrimSuffix(field, "_id")+"_find to get its id, or leave it out.", example)
	case "23514":
		switch pgErr.ConstraintName {
		case "loans_subject_check":
			return respond.Invalid("vehicle_id", "A loan on a vehicle can't also be on a property or asset.",
				"Pass only vehicle_id, or only property_id and/or asset_id.", example)
		case "loans_term_end_check":
			if termEndGiven {
				return respond.Invalid("term_end", "term_end is before the loan's start_date.",
					"Pass a later term_end, or change start_date too.", example)
			}
			return respond.Invalid("start_date", "start_date is after the end of the loan's term.",
				"Pass an earlier start_date, or a longer term_months.", example)
		}
	}
	return nil
}

// termEndSQL computes term_end from start and term SQL expressions with
// Postgres month arithmetic (Jan 31 + 1 month = Feb 28), as 044 backfilled
// it. Without both, an existing term_end is kept unless it would end before
// the start.
func termEndSQL(start, term, current string) string {
	return fmt.Sprintf(`CASE WHEN %[1]s IS NOT NULL AND %[2]s > 0 THEN (%[1]s + make_interval(months => %[2]s))::date
		WHEN %[1]s IS NOT NULL AND %[3]s < %[1]s THEN NULL ELSE %[3]s END`, start, term, current)
}

// NewLoanCreateTool creates the loan_create MCP tool (catalog v2, class C),
// replacing create_loan. It writes the typed subject columns (domain model
// v2), sets term_end from start_date + term_months, and applies the loop
// guard: the same name (any case), original_amount and remaining_balance
// created in the household in the last 10 minutes returns that loan with
// duplicate true instead of a second row (loans have no author column).
func NewLoanCreateTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Add a loan. interest_rate is a percent (6.25). Tie it to a house or car with property_id or vehicle_id."),
		mcp.WithString("name", mcp.Description("Loan name, e.g. Mortgage or Honda loan"), mcp.Required()),
		mcp.WithNumber("original_amount", mcp.Description("Amount originally borrowed, e.g. 320000"), mcp.Required(), mcp.Min(0)),
		mcp.WithNumber("remaining_balance", mcp.Description("What is still owed today, e.g. 251340.12"), mcp.Required(), mcp.Min(0)),
		mcp.WithString("lender", mcp.Description("Who the loan is with, e.g. Chase")),
		mcp.WithNumber("interest_rate", mcp.Description("Annual rate in percent, e.g. 6.25 for 6.25%"), mcp.Min(0), mcp.Max(maxInterestRate)),
		mcp.WithNumber("term_months", mcp.Description("Length of the loan in months, e.g. 360"), mcp.Min(1), mcp.Max(maxTermMonths)),
		mcp.WithNumber("monthly_payment", mcp.Description("Payment each month, e.g. 1850"), mcp.Min(0)),
		mcp.WithNumber("due_day", mcp.Description("Day of the month the payment is due, 1-31"), mcp.Min(1), mcp.Max(31)),
		mcp.WithString("start_date", mcp.Description("Date the loan started, YYYY-MM-DD")),
		mcp.WithString("term_end", mcp.Description("Last payment date, YYYY-MM-DD (default start_date + term_months)")),
		mcp.WithString("notes", mcp.Description("Free-text notes")),
		mcp.WithString("property_id", mcp.Description("House or other property it is for (UUID from property_find)")),
		mcp.WithString("vehicle_id", mcp.Description("Car or other vehicle it is for (UUID from vehicle_find)")),
		mcp.WithString("asset_id", mcp.Description("Thing it is for (UUID from asset_find)")),
		respond.Schema[loanData](),
	}
	tool := mcp.NewTool("loan_create", append(opts, annotations("Create loan", false, false, false)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, deny := callerOf(ctx, pool)
		if deny != nil {
			return deny, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "loan_create"), nil
		}
		args, err := withAliases(req.GetArguments(), fieldAliases)
		if err != nil {
			return invalid(err, createExample), nil
		}
		f, err := parseFields(args)
		if err != nil {
			return invalid(err, createExample), nil
		}
		switch {
		case f.name == nil:
			return respond.Invalid("name", "name is required.", "Pass the loan's name, e.g. Mortgage.", createExample), nil
		case f.originalAmount == nil:
			return respond.Invalid("original_amount", "original_amount is required.",
				"Pass the amount originally borrowed as a number, e.g. 320000.", createExample), nil
		case f.remainingBalance == nil:
			return respond.Invalid("remaining_balance", "remaining_balance is required.",
				"Pass what is still owed as a number, e.g. 251340.12.", createExample), nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("create the loan", err), nil
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		if res, err := checkSubjects(ctx, tx, f, c.householdID, createExample); err != nil {
			return respond.Failed("create the loan", err), nil
		} else if res != nil {
			return res, nil
		}
		today, err := householdToday(ctx, tx, c.householdID)
		if err != nil {
			return respond.Failed("create the loan", err), nil
		}

		dup, err := scanLoan(tx.QueryRow(ctx, loanSelect+`
			WHERE l.household_id = $1 AND lower(l.name) = lower($2)
			  AND l.original_amount = $3 AND l.remaining_balance = $4
			  AND l.created_at > NOW() - $5::interval
			ORDER BY l.created_at DESC
			LIMIT 1`,
			c.householdID, *f.name, *f.originalAmount, *f.remainingBalance, duplicateWindow,
		))
		switch {
		case err == nil:
			d := dup.data(today)
			d.Duplicate = true
			return respond.OK(
				fmt.Sprintf("Loan %s was already created a few minutes ago; returned it instead of adding a second one.", d.Name),
				d,
				fmt.Sprintf("Change it with loan_update loan_id=%s instead of creating it again.", d.ID),
			), nil
		case !errors.Is(err, pgx.ErrNoRows):
			return respond.Failed("create the loan", err), nil
		}

		var id uuid.UUID
		err = tx.QueryRow(ctx,
			`INSERT INTO loans (household_id, name, lender, original_amount, remaining_balance, interest_rate,
			                    term_months, monthly_payment, due_day, start_date, term_end, notes,
			                    property_id, vehicle_id, asset_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::date,
			         COALESCE($15::date, `+termEndSQL("$10::date", "$7::int", "NULL::date")+`),
			         $11, $12, $13, $14)
			 RETURNING id`,
			c.householdID, *f.name, f.lender, *f.originalAmount, *f.remainingBalance, f.interestRate,
			f.termMonths, f.monthlyPayment, f.dueDay, f.startDate, f.notes,
			f.propertyID, f.vehicleID, f.assetID, f.termEnd,
		).Scan(&id)
		if err != nil {
			if res := dbError(err, f.set["term_end"], createExample); res != nil {
				return res, nil
			}
			return respond.Failed("create the loan", err), nil
		}
		r, err := loadLoan(ctx, tx, id, c.householdID)
		if err != nil {
			return respond.Failed("create the loan", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("create the loan", err), nil
		}

		d := r.data(today)
		summary := fmt.Sprintf("Created loan %s: %s.", d.Name, d.describe())
		next := []string{"See monthly totals and net worth with budget_summary."}
		if d.MonthlyPayment == nil {
			next = append(next, fmt.Sprintf("Add the monthly_payment with loan_update loan_id=%s so budget_summary counts it.", d.ID))
		} else {
			next = append(next, fmt.Sprintf("Record a new balance later with loan_update loan_id=%s.", d.ID))
		}
		return respond.OK(summary, d, next...), nil
	}
	return "loan_create", tool, handler
}

// columns maps the given params to loans columns and values. Columns come only
// from this list, never from the caller.
func (f loanFields) columns() (cols []string, vals []any) {
	add := func(param, col string, v any) {
		if f.set[param] {
			cols, vals = append(cols, col), append(vals, v)
		}
	}
	add("name", "name", f.name)
	add("lender", "lender", f.lender)
	add("original_amount", "original_amount", f.originalAmount)
	add("remaining_balance", "remaining_balance", f.remainingBalance)
	add("interest_rate", "interest_rate", f.interestRate)
	add("term_months", "term_months", f.termMonths)
	add("monthly_payment", "monthly_payment", f.monthlyPayment)
	add("due_day", "due_day", f.dueDay)
	add("start_date", "start_date", f.startDate)
	add("term_end", "term_end", f.termEnd)
	add("notes", "notes", f.notes)
	if f.subject {
		cols = append(cols, "property_id", "vehicle_id", "asset_id")
		vals = append(vals, f.propertyID, f.vehicleID, f.assetID)
	}
	return cols, vals
}

// changed lists the params given, for the summary.
func (f loanFields) changed() []string {
	var out []string
	for _, p := range fieldParams {
		if f.set[p] {
			out = append(out, p)
		}
	}
	return out
}

// NewLoanUpdateTool creates the loan_update MCP tool (catalog v2, class U),
// replacing update_loan. Only the given fields change. name, the amounts,
// interest_rate, term_months and due_day cannot be cleared (null means
// unchanged); a blank lender, notes, start_date or term_end clears it.
// Giving any of property_id / vehicle_id / asset_id replaces the loan's
// subject (the others are cleared); a blank one unties it. term_end follows
// start_date and term_months unless it is given. loan_id is a UUID or what
// the family calls the loan ("Mortgage", "the Odyssey loan"): resolveLoan
// lists the candidates when that is ambiguous. A loan of another household
// is reported as not found. Family roles only (role re-read per call).
func NewLoanUpdateTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Change a loan; only the given fields change (e.g. a new remaining_balance after a payment)."),
		mcp.WithString("loan_id", mcp.Description("The loan's id from loan_find, or its name, e.g. Mortgage"), mcp.Required()),
		mcp.WithString("name", mcp.Description("New name, e.g. Mortgage")),
		mcp.WithNumber("original_amount", mcp.Description("Amount originally borrowed, e.g. 320000"), mcp.Min(0)),
		mcp.WithNumber("remaining_balance", mcp.Description("What is still owed now, e.g. 249500"), mcp.Min(0)),
		mcp.WithString("lender", mcp.Description("Who the loan is with; \"\" clears it")),
		mcp.WithNumber("interest_rate", mcp.Description("Annual rate in percent, e.g. 5.875"), mcp.Min(0), mcp.Max(maxInterestRate)),
		mcp.WithNumber("term_months", mcp.Description("Length of the loan in months, e.g. 360"), mcp.Min(1), mcp.Max(maxTermMonths)),
		mcp.WithNumber("monthly_payment", mcp.Description("Payment each month, e.g. 1850"), mcp.Min(0)),
		mcp.WithNumber("due_day", mcp.Description("Day of the month the payment is due, 1-31"), mcp.Min(1), mcp.Max(31)),
		mcp.WithString("start_date", mcp.Description("Date the loan started, YYYY-MM-DD; \"\" clears it")),
		mcp.WithString("term_end", mcp.Description("Last payment date, YYYY-MM-DD; \"\" clears it")),
		mcp.WithString("notes", mcp.Description("New notes; \"\" clears them")),
		mcp.WithString("property_id", mcp.Description("Move it to this property (UUID); \"\" unties it")),
		mcp.WithString("vehicle_id", mcp.Description("Move it to this vehicle (UUID); \"\" unties it")),
		mcp.WithString("asset_id", mcp.Description("Move it to this asset (UUID); \"\" unties it")),
		respond.Schema[loanData](),
	}
	tool := mcp.NewTool("loan_update", append(opts, annotations("Update loan", false, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, deny := callerOf(ctx, pool)
		if deny != nil {
			return deny, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "loan_update"), nil
		}
		args, err := withAliases(req.GetArguments(), writeAliases)
		if err != nil {
			return invalid(err, updateExample), nil
		}
		ref, err := optionalString(args, "loan_id")
		if err != nil {
			return invalid(err, updateExample), nil
		}
		if ref == nil {
			return respond.Invalid("loan_id", "loan_id is required.", "Pass the loan's id from loan_find, or its name.", updateExample), nil
		}
		f, err := parseFields(args)
		if err != nil {
			return invalid(err, updateExample), nil
		}
		for _, p := range []string{"name", "original_amount", "remaining_balance", "interest_rate", "term_months", "monthly_payment", "due_day"} {
			if f.set[p] && isBlank(args[p]) {
				return respond.Invalid(p, p+" cannot be cleared.", "Pass a new value, or leave "+p+" out to keep it.", updateExample), nil
			}
		}
		cols, vals := f.columns()
		if len(cols) == 0 {
			return respond.Invalid("", "No fields to update.",
				"Pass loan_id plus at least one field, e.g. remaining_balance.", updateExample), nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("update the loan", err), nil
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		target, res, err := resolveLoan(ctx, tx, c.householdID, *ref)
		if err != nil {
			return respond.Failed("update the loan", err), nil
		} else if res != nil {
			return res, nil
		}
		loanID := &target.ID
		if res, err := checkSubjects(ctx, tx, f, c.householdID, updateExample); err != nil {
			return respond.Failed("update the loan", err), nil
		} else if res != nil {
			return res, nil
		}

		args2 := []any{*loanID, c.householdID}
		sets := make([]string, 0, len(cols)+2)
		startExpr, termExpr := "start_date", "term_months"
		for i, c := range cols {
			args2 = append(args2, vals[i])
			ph := fmt.Sprintf("$%d", len(args2))
			switch c {
			case "term_end":
				ph += "::date"
			case "start_date":
				ph += "::date"
				startExpr = ph
			case "term_months":
				ph += "::int"
				termExpr = ph
			}
			sets = append(sets, c+" = "+ph)
		}
		if (f.set["start_date"] || f.set["term_months"]) && !f.set["term_end"] {
			sets = append(sets, "term_end = "+termEndSQL(startExpr, termExpr, "term_end"))
		}
		sets = append(sets, "updated_at = NOW()")

		var id uuid.UUID
		err = tx.QueryRow(ctx,
			`UPDATE loans SET `+strings.Join(sets, ", ")+` WHERE id = $1 AND household_id = $2 RETURNING id`,
			args2...,
		).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("loan", loanID.String(), "Call loan_find to get the loan_id."), nil
		}
		if err != nil {
			if res := dbError(err, f.set["term_end"], updateExample); res != nil {
				return res, nil
			}
			return respond.Failed("update the loan", err), nil
		}
		r, err := loadLoan(ctx, tx, id, c.householdID)
		if err != nil {
			return respond.Failed("update the loan", err), nil
		}
		today, err := householdToday(ctx, tx, c.householdID)
		if err != nil {
			return respond.Failed("update the loan", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("update the loan", err), nil
		}

		d := r.data(today)
		return respond.OK(
			fmt.Sprintf("Updated loan %s (%s): %s.", d.Name, strings.Join(f.changed(), ", "), d.describe()),
			d,
			"See monthly totals and net worth with budget_summary.",
		), nil
	}
	return "loan_update", tool, handler
}

// isBlank reports whether an argument value is a blank string.
func isBlank(v any) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// NewLoanDeleteTool creates the loan_delete MCP tool (catalog v2, class X),
// replacing delete_loan. It deletes one loan of the household (loan_id is a
// UUID or a name, resolved like loan_update) and returns it
// as it was. A missing or already deleted loan is reported as not found and
// nothing is deleted, so a repeat call is safe. Notes, files and links about
// the loan are rehomed by the loans_rehome_entity_refs trigger (038).
func NewLoanDeleteTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Delete a loan (paid off or entered by mistake)."),
		mcp.WithString("loan_id", mcp.Description("The loan's id from loan_find, or its name, e.g. Mortgage"), mcp.Required()),
		respond.Schema[loanData](),
	}
	tool := mcp.NewTool("loan_delete", append(opts, annotations("Delete loan", false, true, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, deny := callerOf(ctx, pool)
		if deny != nil {
			return deny, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "loan_delete"), nil
		}
		example := map[string]any{"loan_id": exampleLoanID}
		args, err := withAliases(req.GetArguments(), writeAliases)
		if err != nil {
			return invalid(err, example), nil
		}
		ref, err := optionalString(args, "loan_id")
		if err != nil {
			return invalid(err, example), nil
		}
		if ref == nil {
			return respond.Invalid("loan_id", "loan_id is required.", "Pass the loan's id from loan_find, or its name.", example), nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("delete the loan", err), nil
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		target, res, err := resolveLoan(ctx, tx, c.householdID, *ref)
		if err != nil {
			return respond.Failed("delete the loan", err), nil
		} else if res != nil {
			return res, nil
		}
		loanID := &target.ID

		r, err := scanLoan(tx.QueryRow(ctx, loanSelect+` WHERE l.id = $1 AND l.household_id = $2 FOR UPDATE OF l`,
			*loanID, c.householdID))
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("loan", loanID.String(), "Call loan_find to list loans and their ids."), nil
		}
		if err != nil {
			return respond.Failed("delete the loan", err), nil
		}
		today, err := householdToday(ctx, tx, c.householdID)
		if err != nil {
			return respond.Failed("delete the loan", err), nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM loans WHERE id = $1 AND household_id = $2`, *loanID, c.householdID); err != nil {
			return respond.Failed("delete the loan", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("delete the loan", err), nil
		}

		d := r.data(today)
		return respond.OK(fmt.Sprintf("Deleted loan %s.", d.Name), d,
			"See the updated totals with budget_summary.",
			"Add a loan with loan_create."), nil
	}
	return "loan_delete", tool, handler
}
