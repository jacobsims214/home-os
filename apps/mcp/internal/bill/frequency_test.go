package bill

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/testutil"
)

// dayAtMost28 is the first date on or after today+offset whose day of the
// month is at most 28, so anchors moved back by whole months keep their day.
func dayAtMost28(today time.Time, offset int) time.Time {
	d := today.AddDate(0, 0, offset)
	for d.Day() > 28 {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// createBill calls bill_create and returns the bill, failing on a tool error.
func createBill(t *testing.T, ctx context.Context, pool *pgxpool.Pool, args map[string]any) billData {
	t.Helper()
	_, _, create := NewBillCreateTool(pool)
	res, text := callTool(t, ctx, create, "bill_create", args)
	if res.IsError {
		t.Fatalf("bill_create(%v): %s", args, text)
	}
	var b billData
	if err := unmarshalData(text, &b); err != nil {
		t.Fatalf("decode %s: %v", text, err)
	}
	return b
}

// storedSchedule reads a bill's frequency, due_day, due_date and
// paid_for_due_date as "frequency/due_day/due_date/paid_for" ("-" = NULL).
func storedSchedule(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var freq string
	var day *int
	var date, paidFor *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT frequency, due_day, due_date, paid_for_due_date FROM bills WHERE id = $1`, id,
	).Scan(&freq, &day, &date, &paidFor); err != nil {
		t.Fatalf("select bill: %v", err)
	}
	s := func(d *time.Time) string {
		if d == nil {
			return "-"
		}
		return d.Format(time.DateOnly)
	}
	ds := "-"
	if day != nil {
		ds = fmt.Sprint(*day)
	}
	return fmt.Sprintf("%s/%s/%s/%s", freq, ds, s(date), s(paidFor))
}

// TestBillFind_Frequencies creates quarterly, annual and once bills with
// bill_create and checks bill_find due_within_days lists them at the right
// next due dates (and leaves out the ones further away), with frequency and
// due_date in the output, and that the frequency filter works.
func TestBillFind_Frequencies(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTZ(t, pool, hh.HouseholdID, "UTC")
	today := householdTodayIn(t, "UTC")

	quarterNext := dayAtMost28(today, 5)
	annualNext := dayAtMost28(today, 12)
	onceDue := today.AddDate(0, 0, 2)

	q := createBill(t, ctx, pool, map[string]any{"name": "Water quarterly", "amount": 90.0,
		"frequency": "quarterly", "due_date": quarterNext.AddDate(0, -3, 0).Format(time.DateOnly)})
	a := createBill(t, ctx, pool, map[string]any{"name": "Car registration", "amount": 240.0,
		"frequency": "Yearly", "due_date": annualNext.AddDate(-1, 0, 0).Format(time.DateOnly)})
	o := createBill(t, ctx, pool, map[string]any{"name": "Roof repair", "amount": 1500.0,
		"frequency": "once", "due_date": onceDue.Format(time.DateOnly)})
	createBill(t, ctx, pool, map[string]any{"name": "Home insurance", "amount": 1200.0,
		"frequency": "annual", "due_date": today.AddDate(0, 0, 200).Format(time.DateOnly)})
	createBill(t, ctx, pool, map[string]any{"name": "Sewer", "amount": 60.0,
		"frequency": "quarterly", "due_date": dayAtMost28(today, 50).AddDate(0, -3, 0).Format(time.DateOnly)})

	if a.Frequency != FrequencyAnnual || a.DueDay != nil || strOrNil(a.DueDate) != annualNext.AddDate(-1, 0, 0).Format(time.DateOnly) {
		t.Errorf("annual bill = frequency %q due_day %v due_date %s", a.Frequency, a.DueDay, strOrNil(a.DueDate))
	}
	if a.MonthlyAmount == nil || *a.MonthlyAmount != 20 || q.MonthlyAmount == nil || *q.MonthlyAmount != 30 || o.MonthlyAmount != nil {
		t.Errorf("monthly_amount: annual %v quarterly %v once %v", a.MonthlyAmount, q.MonthlyAmount, o.MonthlyAmount)
	}

	bills, _ := findBills(t, ctx, pool, map[string]any{"due_within_days": 30.0})
	if got := names(bills); got != "Roof repair,Water quarterly,Car registration" {
		t.Fatalf("due within 30 = %s", got)
	}
	want := []struct {
		next time.Time
		freq string
	}{{onceDue, FrequencyOnce}, {quarterNext, FrequencyQuarterly}, {annualNext, FrequencyAnnual}}
	for i, w := range want {
		b := bills[i]
		if strOrNil(b.NextDueDate) != w.next.Format(time.DateOnly) || b.DaysUntilDue == nil ||
			*b.DaysUntilDue != daysBetween(today, w.next) || b.Frequency != w.freq || b.Status != statusDue {
			t.Errorf("%s: next %s days %v frequency %s status %s, want %s %s",
				b.Name, strOrNil(b.NextDueDate), b.DaysUntilDue, b.Frequency, b.Status, w.next.Format(time.DateOnly), w.freq)
		}
	}

	annual, _ := findBills(t, ctx, pool, map[string]any{"frequency": "annual"})
	if got := names(annual); got != "Car registration,Home insurance" {
		t.Errorf("frequency=annual = %s", got)
	}
	_, _, find := NewBillFindTool(pool)
	if res, text := callTool(t, ctx, find, "bill_find", map[string]any{"frequency": "weekly"}); !res.IsError ||
		!strings.Contains(text, "is not one of") {
		t.Errorf("frequency=weekly: %s", text)
	}
}

// TestBillCreate_ScheduleRules checks 044's rule on bill_create: monthly
// needs due_day and no due_date, other frequencies need due_date and no
// due_day. A rejected call inserts nothing.
func TestBillCreate_ScheduleRules(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, _, create := NewBillCreateTool(pool)

	base := func(extra map[string]any) map[string]any {
		args := map[string]any{"name": "Tax", "amount": 900.0}
		for k, v := range extra {
			args[k] = v
		}
		return args
	}
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"no schedule at all", base(nil), "due_day is required"},
		{"due_date without frequency", base(map[string]any{"due_date": "2027-04-15"}), "due_date needs a frequency"},
		{"monthly with due_date", base(map[string]any{"frequency": "monthly", "due_day": 5.0, "due_date": "2027-04-15"}), "due_date is only for bills that are not monthly"},
		{"annual with due_day", base(map[string]any{"frequency": "annual", "due_day": 5.0, "due_date": "2027-04-15"}), "due_day is only for monthly bills"},
		{"annual without due_date", base(map[string]any{"frequency": "annual"}), "due_date is required for annual bills"},
		{"unknown frequency", base(map[string]any{"frequency": "weekly", "due_date": "2027-04-15"}), "is not one of monthly, quarterly"},
		{"bad due_date", base(map[string]any{"frequency": "once", "due_date": "2027-13-01"}), "due_date must be YYYY-MM-DD"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, text := callTool(t, ctx, create, "bill_create", tc.args)
			if !res.IsError || !strings.Contains(text, tc.want) {
				t.Errorf("got error=%v %s, want %q", res.IsError, text, tc.want)
			}
		})
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM bills WHERE household_id = $1`, hh.HouseholdID).Scan(&n); err != nil || n != 0 {
		t.Errorf("rejected calls inserted %d bills (%v)", n, err)
	}

	// The loop guard compares the schedule too: same name and amount with a
	// different due_date is a second bill.
	first := createBill(t, ctx, pool, base(map[string]any{"frequency": "semiannual", "due_date": "2027-01-10"}))
	again := createBill(t, ctx, pool, base(map[string]any{"frequency": "semiannual", "due_date": "2027-01-10"}))
	other := createBill(t, ctx, pool, base(map[string]any{"frequency": "semiannual", "due_date": "2027-02-10"}))
	if !again.Duplicate || again.ID != first.ID || other.Duplicate || other.ID == first.ID {
		t.Errorf("loop guard: again dup=%v same=%v, other dup=%v", again.Duplicate, again.ID == first.ID, other.Duplicate)
	}
}

// TestBillUpdate_Frequency switches a bill between monthly and annual and
// checks the 044 rule, the carried-over value being dropped, and
// paid_for_due_date being cleared only when the schedule really changes.
func TestBillUpdate_Frequency(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTZ(t, pool, hh.HouseholdID, "UTC")
	_, _, update := NewBillUpdateTool(pool)
	_, _, pay := NewBillPayTool(pool)

	b := createBill(t, ctx, pool, map[string]any{"name": "Insurance", "amount": 100.0, "due_day": 10.0})
	payBill(t, ctx, pay, map[string]any{"bill_id": b.ID})
	before := storedSchedule(t, pool, b.ID)
	if !strings.HasPrefix(before, "monthly/10/-/") || strings.HasSuffix(before, "/-") {
		t.Fatalf("after pay = %s, want monthly with paid_for set", before)
	}

	upd := func(args map[string]any) (bool, string) {
		args["bill_id"] = b.ID
		res, text := callTool(t, ctx, update, "bill_update", args)
		return res.IsError, text
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"to annual without due_date", map[string]any{"frequency": "annual"}, "Changing monthly to annual needs due_date."},
		{"due_date on a monthly bill", map[string]any{"due_date": "2027-03-01"}, "due_date needs a frequency"},
	} {
		if isErr, text := upd(tc.args); !isErr || !strings.Contains(text, tc.want) {
			t.Errorf("%s: error=%v %s, want %q", tc.name, isErr, text, tc.want)
		}
	}
	if got := storedSchedule(t, pool, b.ID); got != before {
		t.Errorf("rejected updates changed the bill: %s, was %s", got, before)
	}

	// Same due_day again is not a schedule change: paid_for_due_date stays.
	if isErr, text := upd(map[string]any{"due_day": 10.0}); isErr {
		t.Fatalf("same due_day: %s", text)
	}
	if got := storedSchedule(t, pool, b.ID); got != before {
		t.Errorf("same due_day changed the schedule: %s, was %s", got, before)
	}

	isErr, text := upd(map[string]any{"frequency": "annual", "due_date": "2027-03-01"})
	if isErr {
		t.Fatalf("to annual: %s", text)
	}
	if got := storedSchedule(t, pool, b.ID); got != "annual/-/2027-03-01/-" {
		t.Errorf("to annual stored %s, want annual/-/2027-03-01/- (due_day dropped, paid_for cleared)", got)
	}
	var got billData
	if err := unmarshalData(text, &got); err != nil {
		t.Fatal(err)
	}
	if got.Frequency != FrequencyAnnual || strOrNil(got.DueDate) != "2027-03-01" || got.DueDay != nil {
		t.Errorf("to annual reply: %+v", got)
	}

	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"due_day on an annual bill", map[string]any{"due_day": 5.0}, "due_day is only for monthly bills"},
		{"to monthly without due_day", map[string]any{"frequency": "monthly"}, "Changing annual to monthly needs due_day."},
	} {
		if isErr, text := upd(tc.args); !isErr || !strings.Contains(text, tc.want) {
			t.Errorf("%s: error=%v %s, want %q", tc.name, isErr, text, tc.want)
		}
	}

	if isErr, text := upd(map[string]any{"due_date": "2027-04-01"}); isErr {
		t.Fatalf("move due_date: %s", text)
	}
	if isErr, text := upd(map[string]any{"frequency": "monthly", "due_day": 12.0}); isErr {
		t.Fatalf("back to monthly: %s", text)
	}
	if got := storedSchedule(t, pool, b.ID); got != "monthly/12/-/-" {
		t.Errorf("back to monthly stored %s, want monthly/12/-/- (due_date dropped)", got)
	}
}

// TestBillPay_Frequencies pays annual, quarterly and once bills: the payment
// covers the bill's due date and the next one is a whole period later (none
// for once), matching bill_find; for_due_date is checked against the
// schedule.
func TestBillPay_Frequencies(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTZ(t, pool, hh.HouseholdID, "UTC")
	today := householdTodayIn(t, "UTC")
	_, _, pay := NewBillPayTool(pool)

	due := dayAtMost28(today, 10)
	annual := createBill(t, ctx, pool, map[string]any{"name": "Registration", "amount": 120.0,
		"frequency": "annual", "due_date": due.Format(time.DateOnly)})
	got := payBill(t, ctx, pay, map[string]any{"bill_id": annual.ID})
	if *got.AlreadyPaid || strOrNil(got.PaidForDueDate) != due.Format(time.DateOnly) ||
		strOrNil(got.NextDueDate) != due.AddDate(1, 0, 0).Format(time.DateOnly) || got.Status != statusPaid {
		t.Errorf("annual pay: already=%v paid_for=%s next=%s status=%s", *got.AlreadyPaid,
			strOrNil(got.PaidForDueDate), strOrNil(got.NextDueDate), got.Status)
	}
	assertPayMatchesFind(t, ctx, pool, got)
	if again := payBill(t, ctx, pay, map[string]any{"bill_id": annual.ID}); !*again.AlreadyPaid {
		t.Errorf("second annual pay not already_paid: %+v", again)
	}

	anchor := dayAtMost28(today, 0).AddDate(0, -1, 0)
	quarterly := createBill(t, ctx, pool, map[string]any{"name": "Water", "amount": 90.0,
		"frequency": "quarterly", "due_date": anchor.Format(time.DateOnly)})
	_, _, payTool := NewBillPayTool(pool)
	for _, tc := range []struct {
		name, forDue, want string
	}{
		{"not on the schedule", anchor.AddDate(0, 1, 0).Format(time.DateOnly), "which is due every 3 months from"},
		{"before the first due date", anchor.AddDate(0, -3, 0).Format(time.DateOnly), "is not a due date of Water"},
		{"more than a quarter ahead", anchor.AddDate(0, 6, 0).Format(time.DateOnly), "more than 3 months after today"},
	} {
		res, text := callTool(t, ctx, payTool, "bill_pay", map[string]any{"bill_id": quarterly.ID, "for_due_date": tc.forDue})
		if !res.IsError || !strings.Contains(text, tc.want) {
			t.Errorf("%s: error=%v %s, want %q", tc.name, res.IsError, text, tc.want)
		}
	}
	next := anchor.AddDate(0, 3, 0)
	q := payBill(t, ctx, pay, map[string]any{"bill_id": quarterly.ID, "for_due_date": next.Format(time.DateOnly)})
	if strOrNil(q.PaidForDueDate) != next.Format(time.DateOnly) || strOrNil(q.NextDueDate) != anchor.AddDate(0, 6, 0).Format(time.DateOnly) {
		t.Errorf("quarterly pay: paid_for=%s next=%s", strOrNil(q.PaidForDueDate), strOrNil(q.NextDueDate))
	}
	assertPayMatchesFind(t, ctx, pool, q)

	once := createBill(t, ctx, pool, map[string]any{"name": "Roof", "amount": 1500.0,
		"frequency": "once", "due_date": today.AddDate(0, 0, 3).Format(time.DateOnly)})
	o := payBill(t, ctx, pay, map[string]any{"bill_id": once.ID})
	if o.Status != statusPaid || o.NextDueDate != nil || o.DaysUntilDue != nil ||
		strOrNil(o.PaidForDueDate) != today.AddDate(0, 0, 3).Format(time.DateOnly) {
		t.Errorf("once pay: status=%s next=%s days=%v paid_for=%s", o.Status, strOrNil(o.NextDueDate), o.DaysUntilDue, strOrNil(o.PaidForDueDate))
	}
	soon, _ := findBills(t, ctx, pool, map[string]any{"due_within_days": 30.0})
	if findBill(soon, once.ID) != nil || findBill(soon, annual.ID) != nil {
		t.Errorf("paid once/annual bills still due soon: %s", names(soon))
	}
}

// TestSummarizeByCategory_Frequencies pins the monthly normalisation, the
// same math as the API's budget obligations (#4471): quarterly /3,
// semiannual /6, annual /12, once in full in its due month only, each bill
// rounded to the cent before adding.
func TestSummarizeByCategory_Frequencies(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ins := func(name, category, frequency string, amount *float64, dueDay *int, dueDate *string) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`INSERT INTO bills (household_id, name, category, frequency, amount, due_day, due_date)
			 VALUES ($1, $2, $3, $4, $5, $6, $7::date)`,
			hh.HouseholdID, name, category, frequency, amount, dueDay, dueDate); err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
	}
	ins("Electric", "Utilities", "monthly", ptr(100.0), ptr(5), nil)
	ins("Car insurance", "Insurance", "quarterly", ptr(300.0), nil, ptr("2026-01-10"))
	ins("Home insurance", "Insurance", "semiannual", ptr(600.0), nil, ptr("2026-02-10"))
	ins("Property tax", "Taxes", "annual", ptr(1200.10), nil, ptr("2026-04-01")) // 100.0083 -> 100.01
	ins("Unknown tax", "Taxes", "annual", nil, nil, ptr("2026-04-01"))           // counted, adds nothing
	ins("Magazine", "Subscriptions", "quarterly", ptr(100.0), nil, ptr("2026-01-01"))
	ins("Paper", "Subscriptions", "quarterly", ptr(100.0), nil, ptr("2026-02-01")) // 33.33 + 33.33
	ins("Roof repair", "Repairs", "once", ptr(50.0), nil, ptr("2026-11-20"))
	ins("Gutter repair", "Repairs", "once", ptr(70.0), nil, ptr("2026-12-05"))

	totals, grand, err := SummarizeByCategory(ctx, pool, hh.HouseholdID.String(), time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(totals); got != "[{Insurance 2 200} {Taxes 2 100.01} {Utilities 1 100} {Subscriptions 2 66.66} {Repairs 1 50}]" || grand != 516.67 {
		t.Errorf("November = %s grand %v", got, grand)
	}

	totals, grand, err = SummarizeByCategory(ctx, pool, hh.HouseholdID.String(), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(totals); got != "[{Insurance 2 200} {Taxes 2 100.01} {Utilities 1 100} {Repairs 1 70} {Subscriptions 2 66.66}]" || grand != 536.67 {
		t.Errorf("December = %s grand %v", got, grand)
	}

	totals, grand, err = SummarizeByCategory(ctx, pool, hh.HouseholdID.String(), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range totals {
		if c.Category == "Repairs" {
			t.Errorf("January counts once bills from other months: %v", totals)
		}
	}
	if grand != 466.67 {
		t.Errorf("January grand = %v, want 466.67", grand)
	}
}
