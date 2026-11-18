package loan

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// memberCtx adds a member with role to hh and returns claims for them.
func memberCtx(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) (context.Context, uuid.UUID) {
	t.Helper()
	bg := context.Background()
	email := role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(bg, `INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`, email, role).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hh.HouseholdID, uid, role); err != nil {
		t.Fatal(err)
	}
	return server.ContextWithClaims(bg, &auth.Claims{
		Identity: auth.Identity{Subject: uid.String(), Email: email}, UserID: uid.String(),
		HouseholdID: hh.HouseholdID.String(), Role: role,
	}), uid
}

// TestLoanQuestionsInOneCall: "when is the mortgage due, how much, what's
// the rate?" and "the Odyssey loan" are each one loan_find call, whatever
// the loan is named; the summary alone answers the question.
func TestLoanQuestionsInOneCall(t *testing.T) {
	f := setup(t, "America/Chicago")
	house := f.property(t, f.hh.HouseholdID, "Maple St")
	van := f.vehicle(t, f.hh.HouseholdID)
	// Named after the bank: only its link says it is the mortgage.
	mortgage := call(t, f.ctx, f.create, "loan_create", map[string]any{
		"name": "Chase home loan 2021", "lender": "Chase", "original_amount": "$320,000", "balance": "251,340.12",
		"rate": "6.25%", "payment": "$1,850.00", "due_day": 1.0, "start_date": "2021-06-01", "term_months": 360.0,
		"property_id": house,
	}).loan(t)
	odyssey := call(t, f.ctx, f.create, "loan_create", map[string]any{
		"name": "Honda Financial", "original_amount": 38000.0, "remaining_balance": 21450.5, "apr": 4.9,
		"monthly_payment": 612.0, "due_day": 31.0, "term_end": "2028-03-31", "vehicle_id": van,
	}).loan(t)
	call(t, f.ctx, f.create, "loan_create", map[string]any{"name": "Student loan", "original_amount": 40000.0, "remaining_balance": 9000.0})

	if mortgage.InterestRate == nil || *mortgage.InterestRate != 6.25 || mortgage.OriginalAmount != "320000.00" ||
		mortgage.RemainingBalance != "251340.12" || deref(mortgage.MonthlyPayment) != "1850.00" {
		t.Fatalf("aliases and money text not parsed: %+v", mortgage)
	}
	if deref(odyssey.TermEnd) != "2028-03-31" || deref(odyssey.TermEndWeekday) != "Fri" {
		t.Errorf("explicit term_end = %v %v", deref(odyssey.TermEnd), deref(odyssey.TermEndWeekday))
	}

	for _, q := range []string{"mortgage", "the mortgage", "When is the mortgage due?", "Maple St", "chase", "home loan"} {
		t.Run(q, func(t *testing.T) {
			env := call(t, f.ctx, f.find, "loan_find", map[string]any{"query": q})
			l := env.loans(t)
			if len(l) != 1 || l[0].ID != mortgage.ID {
				t.Fatalf("got %d loans: %v", len(l), env.Summary)
			}
			d := l[0]
			if d.NextDueDate == nil || d.NextDueWeekday == nil || deref(d.DueDay) != 1 || d.Currency != "USD" {
				t.Errorf("due fields = %+v", d)
			}
			for _, want := range []string{"$1,850.00/month due ", "(the 1st each month)", "at 6.25%", "with Chase", "$251,340.12 left", "term ends 2051-06-01"} {
				if !strings.Contains(env.Summary, want) {
					t.Errorf("summary %q lacks %q", env.Summary, want)
				}
			}
		})
	}
	for _, q := range []string{"Odyssey", "the Odyssey loan", "what's the rate on the Odyssey loan", "car loan", "honda van"} {
		t.Run(q, func(t *testing.T) {
			env := call(t, f.ctx, f.find, "loan_find", map[string]any{"query": q})
			if l := env.loans(t); len(l) != 1 || l[0].ID != odyssey.ID {
				t.Fatalf("got %v", env.Summary)
			}
			if !strings.Contains(env.Summary, "$612.00/month due ") || !strings.Contains(env.Summary, "(the 31st each month)") ||
				!strings.Contains(env.Summary, "at 4.9%") || !strings.Contains(env.Summary, "$21,450.50 left") {
				t.Errorf("summary = %q", env.Summary)
			}
		})
	}
	if l := call(t, f.ctx, f.find, "loan_find", map[string]any{"query": "loans"}).loans(t); len(l) != 3 {
		t.Errorf("filler-only query = %d loans, want all 3", len(l))
	}
	none := call(t, f.ctx, f.find, "loan_find", map[string]any{"query": "boat"})
	if len(none.loans(t)) != 0 || none.Summary != "No loans match." {
		t.Errorf("boat = %q", none.Summary)
	}

	// A HELOC on the same house: "the mortgage" matches both by kind alone,
	// so both come back (loan_update would list them); "heloc" is one.
	heloc := call(t, f.ctx, f.create, "loan_create", map[string]any{
		"name": "HELOC", "original_amount": 50000.0, "remaining_balance": 10000.0, "property_id": house,
	}).loan(t)
	if l := call(t, f.ctx, f.find, "loan_find", map[string]any{"query": "the mortgage"}).loans(t); len(l) != 2 {
		t.Errorf("two property loans, kind match = %d", len(l))
	}
	if l := call(t, f.ctx, f.find, "loan_find", map[string]any{"query": "heloc"}).loans(t); len(l) != 1 || l[0].ID != heloc.ID {
		t.Errorf("heloc = %v", l)
	}

	// loan_update by name; ambiguity lists candidates with ids.
	u := call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": "the Odyssey loan", "balance": "$21,000"}).loan(t)
	if u.ID != odyssey.ID || u.RemainingBalance != "21000.00" {
		t.Errorf("update by name = %+v", u)
	}
	amb := call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": "house", "remaining_balance": 1.0})
	if amb.OK || amb.Error == nil || amb.Error.Field != "loan_id" ||
		!strings.Contains(amb.Error.Message, "2 loans match") || !strings.Contains(amb.Error.Message, mortgage.ID) ||
		!strings.Contains(amb.Error.Message, heloc.ID) {
		t.Errorf("ambiguous = %+v", amb.Error)
	}
	miss := call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": "boat", "remaining_balance": 1.0})
	if miss.OK || miss.Error == nil || !strings.Contains(miss.Error.Message, "No loan matches") || !strings.Contains(miss.Error.Message, odyssey.ID) {
		t.Errorf("no match = %+v", miss.Error)
	}
	// Explicit term_end on update wins over the computed one; before
	// start_date is refused.
	u = call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": mortgage.ID, "term_months": 180.0, "term_end": "2040-01-01"}).loan(t)
	if deref(u.TermEnd) != "2040-01-01" {
		t.Errorf("term_end = %v", deref(u.TermEnd))
	}
	wantError(t, call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": mortgage.ID, "term_end": "2020-01-01"}), "term_end")
	wantError(t, call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": mortgage.ID, "term_end": "soon"}), "term_end")

	// loan_delete by name.
	del := call(t, f.ctx, f.del, "loan_delete", map[string]any{"loan_id": "Student loan"})
	if del.Summary != "Deleted loan Student loan." {
		t.Errorf("delete by name = %q", del.Summary)
	}
}

// TestLoanRoles: money is hidden from housesitter and vendor; viewer reads;
// only family roles write; the role is re-read from memberships each call.
func TestLoanRoles(t *testing.T) {
	f := setup(t, "UTC")
	l := call(t, f.ctx, f.create, "loan_create", map[string]any{"name": "Mortgage", "original_amount": 1.0, "remaining_balance": 1.0}).loan(t)

	for _, role := range []string{"family_manager", "family_member", "viewer"} {
		ctx, _ := memberCtx(t, f.pool, f.hh, role)
		if got := call(t, ctx, f.find, "loan_find", nil).loans(t); len(got) != 1 {
			t.Errorf("%s sees %d loans", role, len(got))
		}
	}
	for _, role := range []string{"housesitter", "vendor"} {
		ctx, _ := memberCtx(t, f.pool, f.hh, role)
		env := call(t, ctx, f.find, "loan_find", map[string]any{"query": "mortgage"})
		if env.OK || !strings.Contains(env.Error.Message, "The "+role+" role can't see loans") {
			t.Errorf("%s loan_find = %s", role, env.Summary)
		}
	}
	for _, role := range []string{"viewer", "housesitter", "vendor"} {
		ctx, _ := memberCtx(t, f.pool, f.hh, role)
		for name, h := range map[string]toolHandler{"loan_update": f.upd, "loan_delete": f.del} {
			env := call(t, ctx, h, name, map[string]any{"loan_id": l.ID, "remaining_balance": 2.0})
			if env.OK || !strings.Contains(env.Error.Message, "The "+role+" role can't change loans") {
				t.Errorf("%s %s = %s", role, name, env.Summary)
			}
		}
		env := call(t, ctx, f.create, "loan_create", map[string]any{"name": "X", "original_amount": 1.0, "remaining_balance": 1.0})
		if env.OK {
			t.Errorf("%s created a loan", role)
		}
	}

	// Demoted at once: the claims still say family_member.
	ctx, uid := memberCtx(t, f.pool, f.hh, "family_member")
	if _, err := f.pool.Exec(context.Background(), `UPDATE memberships SET role = 'housesitter' WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if call(t, ctx, f.find, "loan_find", nil).OK {
		t.Errorf("demoted member still reads loans")
	}
	// Removed: not signed in.
	if _, err := f.pool.Exec(context.Background(), `DELETE FROM memberships WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if env := call(t, ctx, f.find, "loan_find", nil); env.OK || !strings.Contains(env.Error.Message, "not signed in") {
		t.Errorf("removed member = %s", env.Summary)
	}
}

// TestMatchLoans pins the matching rules without a database.
func TestMatchLoans(t *testing.T) {
	id := func() *uuid.UUID { u := uuid.New(); return &u }
	s := func(v string) *string { return &v }
	mortgage := loanRow{ID: uuid.New(), Name: "Wells Fargo", PropertyID: id(), SubjectName: s("Maple St")}
	heloc := loanRow{ID: uuid.New(), Name: "Mortgage line", PropertyID: id(), SubjectName: s("Maple St")}
	van := loanRow{ID: uuid.New(), Name: "Van", Lender: s("Honda Financial"), VehicleID: id(), SubjectName: s("2022 Honda Odyssey")}
	sofa := loanRow{ID: uuid.New(), Name: "Sofa plan", AssetID: id(), PropertyID: id(), SubjectName: s("Sofa")}
	rows := []loanRow{mortgage, heloc, van, sofa}
	names := func(rs []loanRow) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Name)
		}
		return strings.Join(out, ",")
	}
	for q, want := range map[string]string{
		"mortgage":                  "Mortgage line", // text beats kind
		"wells fargo":               "Wells Fargo",
		"VAN":                       "Van", // exact name
		"Odyssey's loan":            "Van",
		"car":                       "Van",
		"auto loans":                "Van",
		"house":                     "Wells Fargo,Mortgage line",
		"sofa":                      "Sofa plan",
		"maple":                     "Wells Fargo,Mortgage line",
		"what is the loan balance?": "Wells Fargo,Mortgage line,Van,Sofa plan",
		"boat":                      "",
	} {
		if got := names(matchLoans(rows, q)); got != want {
			t.Errorf("matchLoans(%q) = %q, want %q", q, got, want)
		}
	}
}

// TestOrdinal pins due-day wording.
func TestOrdinal(t *testing.T) {
	for n, want := range map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd", 31: "31st"} {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q", n, got)
		}
	}
}
