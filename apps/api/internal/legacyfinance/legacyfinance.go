// Package legacyfinance maps the old property and vehicle finance fields
// (mortgage_*, property_tax_*, insurance_*, hoa_fee_monthly on a property;
// lender, loan_*, monthly_payment, registration_*, insurance_* on a vehicle)
// onto the loans and bills about that property or vehicle (#4704).
//
// Domain model v2 keeps a family's money in loans and bills linked by the
// typed property_id / vehicle_id columns. Migration 044 copied the old
// columns into such rows; the columns themselves are dropped later (#4707),
// and apps/api no longer reads or writes them. The property and vehicle API
// still returns the old JSON field names, because the UI shows and edits
// them. This package:
//
//   - picks "the" loan or bill for each old field, with the same rules as
//     044's has_loan/has_bill (a same-family row about the subject whose name
//     or category matches the kind's pattern; asset rows, whose property_id
//     is derived from the asset, are left out), oldest first;
//   - builds the SQL that derives each old field from that row;
//   - applies the old fields sent in a request to that row, or creates one
//     when there is none and the value is worth a row (044's thresholds).
//
// Each field is a partial update: absent, null or "" leaves the stored value
// alone. Nothing here deletes a loan or bill; that is done through the loan
// and bill endpoints.
package legacyfinance

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is what the write helpers need: a pgx.Tx in practice, so the
// subject row and its loans/bills are written in one transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Subject is the property or vehicle the loans and bills are about.
type Subject struct {
	// Col is the typed FK on loans/bills: "property_id" or "vehicle_id".
	Col         string
	ID          uuid.UUID
	HouseholdID uuid.UUID
	// Name prefixes the name of a loan or bill created here, e.g.
	// "Main Residence" -> "Main Residence mortgage".
	Name string
}

func (s Subject) check() error {
	if s.Col != "property_id" && s.Col != "vehicle_id" {
		return fmt.Errorf("legacyfinance: unsupported subject column %q", s.Col)
	}
	return nil
}

// InvalidError is a legacy field whose value can't be stored. Handlers
// report it as 400.
type InvalidError struct {
	Field  string
	Reason string
}

func (e *InvalidError) Error() string { return e.Field + ": " + e.Reason }

// IsInvalid reports whether err is (or wraps) an *InvalidError.
func IsInvalid(err error) (*InvalidError, bool) {
	var ie *InvalidError
	ok := errors.As(err, &ie)
	return ie, ok
}

// ---------------------------------------------------------------------------
// Kinds
// ---------------------------------------------------------------------------

// LoanKind says which loan an old field set maps to.
type LoanKind struct {
	// Prefer: loans whose name matches one of these ILIKE patterns win over
	// other loans about the subject (a mortgage over a HELOC, say).
	Prefer []string
	// NameSuffix is appended to Subject.Name for a loan created here.
	NameSuffix string
}

// BillKind says which bill an old field maps to and how a new one looks.
type BillKind struct {
	// Patterns are ILIKE patterns matched against the bill's name or
	// category (044's has_bill patterns).
	Patterns []string
	// Category and Frequency of a bill created here.
	Category  string
	Frequency string
	// InputPeriods is how many periods per year the old field's amount is
	// for: 1 for an annual amount (property_tax_annual), 12 for a monthly
	// one (hoa_fee_monthly). The bill amount is converted to and from its
	// own frequency.
	InputPeriods int
	// NameSuffix is appended to Subject.Name for a bill created here.
	NameSuffix string
	// ProviderOnly: a provider alone (no amount) is enough to create the
	// bill, as 044 did for insurance.
	ProviderOnly bool
}

var (
	Mortgage    = LoanKind{Prefer: []string{"%mortgage%"}, NameSuffix: "mortgage"}
	VehicleLoan = LoanKind{Prefer: nil, NameSuffix: "loan"}

	PropertyTax = BillKind{Patterns: []string{"%tax%"}, Category: "Taxes", Frequency: "annual",
		InputPeriods: 1, NameSuffix: "property tax"}
	PropertyInsurance = BillKind{Patterns: []string{"%insur%"}, Category: "Insurance", Frequency: "annual",
		InputPeriods: 1, NameSuffix: "insurance", ProviderOnly: true}
	HOAFee = BillKind{Patterns: []string{"%hoa%", "%association%"}, Category: "Housing", Frequency: "monthly",
		InputPeriods: 12, NameSuffix: "HOA fee"}
	VehicleRegistration = BillKind{Patterns: []string{"%regist%"}, Category: "Vehicle", Frequency: "annual",
		InputPeriods: 1, NameSuffix: "registration"}
	VehicleInsurance = BillKind{Patterns: []string{"%insur%"}, Category: "Insurance", Frequency: "annual",
		InputPeriods: 1, NameSuffix: "insurance", ProviderOnly: true}
)

// ---------------------------------------------------------------------------
// Read side: SQL fragments
// ---------------------------------------------------------------------------

// sqlArray renders constant patterns as a text[] literal. Patterns are
// package constants, never request data.
func sqlArray(patterns []string) string {
	if len(patterns) == 0 {
		return "ARRAY[]::text[]"
	}
	q := make([]string, len(patterns))
	for i, p := range patterns {
		q[i] = "'" + strings.ReplaceAll(p, "'", "''") + "'"
	}
	return "ARRAY[" + strings.Join(q, ", ") + "]::text[]"
}

// periodsSQL is the number of times a year a bill with frequency f is paid.
func periodsSQL(f string) string {
	return "(CASE " + f + " WHEN 'weekly' THEN 52 WHEN 'monthly' THEN 12 WHEN 'quarterly' THEN 4" +
		" WHEN 'semiannual' THEN 2 ELSE 1 END)"
}

// LoanJoin is a LEFT JOIN LATERAL that exposes the subject's loan as alias,
// with columns original_amount, interest_rate, term_months, start_date,
// lender, account_number and monthly_payment. owner is the alias of the
// properties/vehicles row; col is the typed FK ("property_id"/"vehicle_id").
func LoanJoin(k LoanKind, alias, col, owner string) string {
	return fmt.Sprintf(`
  LEFT JOIN LATERAL (
       SELECT l.original_amount, l.interest_rate, l.term_months, l.start_date,
              l.lender, l.account_number, l.monthly_payment
         FROM loans l
        WHERE l.household_id = %[3]s.household_id AND l.%[2]s = %[3]s.id AND l.asset_id IS NULL
        ORDER BY (l.name ILIKE ANY (%[4]s)) DESC, l.created_at, l.id
        LIMIT 1) %[1]s ON true`, alias, col, owner, sqlArray(k.Prefer))
}

// BillJoin is a LEFT JOIN LATERAL that exposes the subject's bill of kind k
// as alias, with columns amount, frequency, due_date, notes, name and
// vendor_name (the bill's same-family vendor).
func BillJoin(k BillKind, alias, col, owner string) string {
	pats := sqlArray(k.Patterns)
	return fmt.Sprintf(`
  LEFT JOIN LATERAL (
       SELECT b.amount, b.frequency, b.due_date, b.notes, b.name, bv.name AS vendor_name
         FROM bills b
         LEFT JOIN vendors bv ON bv.id = b.vendor_id AND bv.household_id = b.household_id
        WHERE b.household_id = %[3]s.household_id AND b.%[2]s = %[3]s.id AND b.asset_id IS NULL
          AND (b.name ILIKE ANY (%[4]s) OR b.category ILIKE ANY (%[4]s))
        ORDER BY b.created_at, b.id
        LIMIT 1) %[1]s ON true`, alias, col, owner, pats)
}

// AmountSQL is the bill's amount converted to the old field's period (an
// annual or monthly figure), as text with two decimals; NULL when the bill
// or its amount is missing.
func AmountSQL(k BillKind, alias string) string {
	return fmt.Sprintf("(round(%[1]s.amount * %[2]s / %[3]d.0, 2))::text",
		alias, periodsSQL(alias+".frequency"), k.InputPeriods)
}

// DueMonthSQL is the month (1-12) of the bill's due_date, as an int.
func DueMonthSQL(alias string) string {
	return fmt.Sprintf("(EXTRACT(MONTH FROM %s.due_date))::int", alias)
}

// DueMonthsTextSQL is the old free-text "due months": the month of the
// bill's due_date, else the "Due months: ..." line of its notes (written by
// 044 and by ApplyBill for anything that is not a single month).
func DueMonthsTextSQL(alias string) string {
	return fmt.Sprintf(`COALESCE((EXTRACT(MONTH FROM %[1]s.due_date))::int::text,
	       substring(%[1]s.notes from 'Due months: ([^\n]*)'))`, alias)
}

// ProviderSQL is the insurance provider: the bill's vendor, else the
// "(Provider)" suffix 044 put in the names of the bills it copied.
func ProviderSQL(alias string) string {
	return fmt.Sprintf(`COALESCE(%[1]s.vendor_name, substring(%[1]s.name from '\(([^()]+)\)\s*$'))`, alias)
}

// ---------------------------------------------------------------------------
// Request parsing
// ---------------------------------------------------------------------------

func clean(p *string) *string {
	if p == nil {
		return nil
	}
	s := strings.TrimSpace(*p)
	if s == "" {
		return nil
	}
	return &s
}

// Amount parses a money value ("1,800", "$300000.50"). nil/"" -> nil.
func Amount(field string, p *string) (*string, error) {
	s := clean(p)
	if s == nil {
		return nil, nil
	}
	v := strings.NewReplacer("$", "", ",", "", " ", "").Replace(*s)
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f >= 1e10 {
		return nil, &InvalidError{Field: field, Reason: "must be a non-negative amount"}
	}
	return &v, nil
}

// Rate parses an interest rate in percent (0 <= rate < 100, as 044).
func Rate(field string, p *string) (*string, error) {
	s := clean(p)
	if s == nil {
		return nil, nil
	}
	v := strings.TrimSuffix(strings.TrimSpace(*s), "%")
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f < 0 || f >= 100 {
		return nil, &InvalidError{Field: field, Reason: "must be a rate between 0 and 100"}
	}
	v = strings.TrimSpace(v)
	return &v, nil
}

// Months parses a positive whole number of months given as text.
func Months(field string, p *string) (*int, error) {
	s := clean(p)
	if s == nil {
		return nil, nil
	}
	n, err := strconv.Atoi(*s)
	if err != nil || n <= 0 {
		return nil, &InvalidError{Field: field, Reason: "must be a positive number of months"}
	}
	return &n, nil
}

// PositiveInt drops a zero or negative int (the vehicle form sends numbers).
func PositiveInt(field string, p *int) (*int, error) {
	if p == nil || *p == 0 {
		return nil, nil
	}
	if *p < 0 {
		return nil, &InvalidError{Field: field, Reason: "must be positive"}
	}
	return p, nil
}

// Date parses YYYY-MM-DD.
func Date(field string, p *string) (*string, error) {
	s := clean(p)
	if s == nil {
		return nil, nil
	}
	if _, err := time.Parse("2006-01-02", *s); err != nil {
		return nil, &InvalidError{Field: field, Reason: "must be a date (YYYY-MM-DD)"}
	}
	return s, nil
}

// Text trims a free-text value; "" -> nil.
func Text(p *string) *string { return clean(p) }

var singleMonth = regexp.MustCompile(`^[0-9]{1,2}$`)

// month returns m when s is a single month number 1-12.
func month(s string) (int, bool) {
	if !singleMonth.MatchString(s) {
		return 0, false
	}
	m, _ := strconv.Atoi(s)
	return m, m >= 1 && m <= 12
}

func positive(s *string) bool {
	if s == nil {
		return false
	}
	f, err := strconv.ParseFloat(*s, 64)
	return err == nil && f > 0
}

// ---------------------------------------------------------------------------
// Write side
// ---------------------------------------------------------------------------

// LoanFields are the old loan fields of one request, already parsed. nil
// means "not sent": the stored value is kept.
type LoanFields struct {
	Lender         *string
	OriginalAmount *string
	InterestRate   *string
	TermMonths     *int
	StartDate      *string
	AccountNumber  *string
	MonthlyPayment *string
}

func (f LoanFields) empty() bool {
	return f.Lender == nil && f.OriginalAmount == nil && f.InterestRate == nil && f.TermMonths == nil &&
		f.StartDate == nil && f.AccountNumber == nil && f.MonthlyPayment == nil
}

// ApplyLoan writes f to the subject's loan of kind k. With no such loan, one
// is created when there is an amount, a monthly payment or a lender (044's
// rule); its remaining_balance starts at the original amount. term_end is
// recomputed from start_date + term_months when either is sent.
func ApplyLoan(ctx context.Context, q Querier, s Subject, k LoanKind, f LoanFields) error {
	if f.empty() {
		return nil
	}
	if err := s.check(); err != nil {
		return err
	}
	var id uuid.UUID
	err := q.QueryRow(ctx,
		`SELECT id FROM loans
		  WHERE household_id = $1 AND `+s.Col+` = $2 AND asset_id IS NULL
		  ORDER BY (name ILIKE ANY ($3::text[])) DESC, created_at, id
		  LIMIT 1 FOR UPDATE`,
		s.HouseholdID, s.ID, nonNil(k.Prefer),
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if !positive(f.OriginalAmount) && !positive(f.MonthlyPayment) && f.Lender == nil {
			return nil
		}
		_, err = q.Exec(ctx,
			`INSERT INTO loans (household_id, name, `+s.Col+`, lender, original_amount, remaining_balance,
			                   interest_rate, term_months, monthly_payment, start_date, term_end, account_number)
			 VALUES ($1, $2, $3, $4, COALESCE($5::numeric, 0), COALESCE($5::numeric, 0),
			         $6::numeric, $7::int, $8::numeric, $9::date,
			         CASE WHEN $9::date IS NOT NULL AND $7::int > 0
			              THEN ($9::date + make_interval(months => $7::int))::date END,
			         $10)`,
			s.HouseholdID, joinName(s.Name, k.NameSuffix), s.ID, f.Lender, f.OriginalAmount,
			f.InterestRate, f.TermMonths, f.MonthlyPayment, f.StartDate, f.AccountNumber,
		)
		if err != nil {
			return fmt.Errorf("create %s: %w", k.NameSuffix, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("find %s: %w", k.NameSuffix, err)
	}
	_, err = q.Exec(ctx,
		`UPDATE loans
		    SET lender          = COALESCE($2, lender),
		        original_amount = COALESCE($3::numeric, original_amount),
		        interest_rate   = COALESCE($4::numeric, interest_rate),
		        term_months     = COALESCE($5::int, term_months),
		        monthly_payment = COALESCE($6::numeric, monthly_payment),
		        start_date      = COALESCE($7::date, start_date),
		        account_number  = COALESCE($8, account_number),
		        term_end = CASE
		            WHEN ($5::int IS NOT NULL OR $7::date IS NOT NULL)
		             AND COALESCE($7::date, start_date) IS NOT NULL
		             AND COALESCE($5::int, term_months) > 0
		            THEN (COALESCE($7::date, start_date)
		                  + make_interval(months => COALESCE($5::int, term_months)))::date
		            ELSE term_end END,
		        updated_at = NOW()
		  WHERE id = $1`,
		id, f.Lender, f.OriginalAmount, f.InterestRate, f.TermMonths, f.MonthlyPayment, f.StartDate, f.AccountNumber,
	)
	if err != nil {
		return fmt.Errorf("update %s: %w", k.NameSuffix, err)
	}
	return nil
}

// BillFields are the old fields for one bill, already parsed. nil = not sent.
type BillFields struct {
	// Amount is in the old field's period (BillKind.InputPeriods).
	Amount *string
	// DueMonths: a single month 1-12 becomes the bill's due_date (the first
	// of that month, today or later) when the bill is not monthly; anything
	// else is kept as a "Due months: ..." line in its notes.
	DueMonths *string
	// Provider is resolved to a same-family vendor, linked as vendor_id.
	Provider *string
}

func (f BillFields) empty() bool {
	return f.Amount == nil && f.DueMonths == nil && f.Provider == nil
}

// nextMonthStartSQL: the first day of month $n that is today or later (044's
// pg_temp.next_month_start).
const nextMonthStartSQL = `(CASE WHEN make_date(EXTRACT(YEAR FROM CURRENT_DATE)::int, %[1]s::int, 1) >= CURRENT_DATE
	      THEN make_date(EXTRACT(YEAR FROM CURRENT_DATE)::int, %[1]s::int, 1)
	      ELSE make_date(EXTRACT(YEAR FROM CURRENT_DATE)::int + 1, %[1]s::int, 1) END)`

var dueMonthsLine = regexp.MustCompile(`(?m)^Due months: .*$\n?`)

// withDueMonths returns notes with its "Due months:" line set to v, or
// removed when v is "".
func withDueMonths(notes *string, v string) *string {
	cur := ""
	if notes != nil {
		cur = *notes
	}
	out := strings.TrimRight(dueMonthsLine.ReplaceAllString(cur, ""), "\n")
	if v != "" {
		if out != "" {
			out += "\n"
		}
		out += "Due months: " + v
	}
	if out == "" {
		return nil
	}
	return &out
}

// ApplyBill writes f to the subject's bill of kind k. With no such bill, one
// is created when the amount is above zero (or, for ProviderOnly kinds, when
// a provider is given), as 044 did. An amount equal to what the bill already
// shows is not rewritten, so a form that sends back what it was shown never
// changes a bill through rounding.
func ApplyBill(ctx context.Context, q Querier, s Subject, k BillKind, f BillFields) error {
	if f.empty() {
		return nil
	}
	if err := s.check(); err != nil {
		return err
	}
	var vendorID *uuid.UUID
	if f.Provider != nil && k.ProviderOnly {
		id, err := resolveProvider(ctx, q, s.HouseholdID, *f.Provider)
		if err != nil {
			return err
		}
		vendorID = &id
	}

	var (
		id        uuid.UUID
		frequency string
		notes     *string
		dueMonth  *int
	)
	err := q.QueryRow(ctx,
		`SELECT id, frequency, notes, (EXTRACT(MONTH FROM due_date))::int
		   FROM bills
		  WHERE household_id = $1 AND `+s.Col+` = $2 AND asset_id IS NULL
		    AND (name ILIKE ANY ($3::text[]) OR category ILIKE ANY ($3::text[]))
		  ORDER BY created_at, id
		  LIMIT 1 FOR UPDATE`,
		s.HouseholdID, s.ID, k.Patterns,
	).Scan(&id, &frequency, &notes, &dueMonth)

	if errors.Is(err, pgx.ErrNoRows) {
		if !positive(f.Amount) && !(k.ProviderOnly && vendorID != nil) {
			return nil
		}
		var m *int
		var newNotes *string
		if f.DueMonths != nil {
			if mm, ok := month(*f.DueMonths); ok && k.Frequency != "monthly" {
				m = &mm
			} else {
				newNotes = withDueMonths(nil, *f.DueMonths)
			}
		}
		// The new bill's frequency is k.Frequency, so its amount is the
		// old value converted from InputPeriods to that frequency.
		_, err = q.Exec(ctx,
			fmt.Sprintf(`INSERT INTO bills (household_id, name, amount, category, frequency, due_date,
			                    `+s.Col+`, vendor_id, notes)
			 VALUES ($1, $2, round($3::numeric * $4::numeric / %s, 2), $5, $6,
			         CASE WHEN $7::int IS NOT NULL THEN `+fmt.Sprintf(nextMonthStartSQL, "$7")+` END,
			         $8, $9, $10)`, periodsSQL("$6::text")),
			s.HouseholdID, joinName(s.Name, k.NameSuffix), f.Amount, k.InputPeriods, k.Category, k.Frequency,
			m, s.ID, vendorID, newNotes,
		)
		if err != nil {
			return fmt.Errorf("create %s bill: %w", k.NameSuffix, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("find %s bill: %w", k.NameSuffix, err)
	}

	// Due months: a single month on a non-monthly bill moves due_date
	// (unless it is already in that month) and resets paid_for_due_date,
	// as the bill API does when due_date changes. Anything else goes to
	// the notes line.
	var setMonth *int
	notesChanged := false
	newNotes := notes
	if f.DueMonths != nil {
		if mm, ok := month(*f.DueMonths); ok && frequency != "monthly" {
			if dueMonth == nil || *dueMonth != mm {
				setMonth = &mm
			}
			newNotes = withDueMonths(notes, "")
		} else {
			newNotes = withDueMonths(notes, *f.DueMonths)
		}
		notesChanged = !sameText(notes, newNotes)
	}

	_, err = q.Exec(ctx,
		fmt.Sprintf(`UPDATE bills
		    SET amount = CASE
		            WHEN $2::numeric IS NULL THEN amount
		            WHEN amount IS NOT NULL AND round(amount * %[1]s / $3::numeric, 2) = round($2::numeric, 2) THEN amount
		            ELSE round($2::numeric * $3::numeric / %[1]s, 2) END,
		        due_date = CASE WHEN $4::int IS NULL THEN due_date ELSE `+fmt.Sprintf(nextMonthStartSQL, "$4")+` END,
		        paid_for_due_date = CASE WHEN $4::int IS NULL THEN paid_for_due_date END,
		        notes = CASE WHEN $5::bool THEN $6 ELSE notes END,
		        vendor_id = COALESCE($7, vendor_id),
		        updated_at = NOW()
		  WHERE id = $1`, periodsSQL("frequency")),
		id, f.Amount, k.InputPeriods, setMonth, notesChanged, newNotes, vendorID,
	)
	if err != nil {
		return fmt.Errorf("update %s bill: %w", k.NameSuffix, err)
	}
	return nil
}

// resolveProvider finds the family vendor named name (trimmed,
// case-insensitive), preferring one with trade "insurance", then the oldest;
// otherwise it creates {name, specialty "Insurance", trades {insurance}}.
func resolveProvider(ctx context.Context, q Querier, householdID uuid.UUID, name string) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx,
		`SELECT id FROM vendors
		  WHERE household_id = $1 AND lower(btrim(name)) = lower($2)
		  ORDER BY ('insurance' = ANY (trades)) DESC, created_at, id
		  LIMIT 1`,
		householdID, name,
	).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("find insurance vendor: %w", err)
	}
	if err := q.QueryRow(ctx,
		`INSERT INTO vendors (household_id, name, specialty, trades)
		 VALUES ($1, $2, 'Insurance', ARRAY['insurance'])
		 RETURNING id`,
		householdID, name,
	).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("create insurance vendor: %w", err)
	}
	return id, nil
}

func joinName(subject, suffix string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return strings.ToUpper(suffix[:1]) + suffix[1:]
	}
	return subject + " " + suffix
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func sameText(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
