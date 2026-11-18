package budget

// #4819: budget_summary's due_soon section must be the API's GET
// /api/v1/budget/due-soon. Both now read home-os/billdue — the due rule and the
// due-soon query in one place — so this test fails if this package ever grows a
// second way of working out what is due.

import (
	"context"
	"testing"
	"time"

	"home-os/billdue"
)

// TestMCPDueSoonIsTheSharedLoader: LoadDueSoonDepth's items, counts and total
// are home-os/billdue.LoadDueSoonRows' for the same household and date, at
// every depth. Depth only decides how many items are listed (and so how big
// the payload is); the numbers always cover the whole window (#4809).
func TestMCPDueSoonIsTheSharedLoader(t *testing.T) {
	f := seed(t)
	bg := context.Background()
	today := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

	shared, err := billdue.LoadDueSoonRows(bg, f.pool, f.hh.HouseholdID.String(), today, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(shared.Items) == 0 {
		t.Fatal("the seeded household has no due items; the comparison would be vacuous")
	}

	for _, depth := range []Depth{DepthSummary, DepthLines, DepthFull} {
		got, err := LoadDueSoonDepth(bg, f.pool, f.hh.HouseholdID.String(), today, 30, depth)
		if err != nil {
			t.Fatalf("depth %d: %v", depth, err)
		}
		// The totals and counts are depth-independent and cover every item.
		if got.Total != FormatCents(shared.TotalCents) || got.TotalCents() != shared.TotalCents {
			t.Errorf("depth %d: total %s/%d, shared %s/%d", depth, got.Total, got.TotalCents(),
				FormatCents(shared.TotalCents), shared.TotalCents)
		}
		if got.BillCount != shared.BillCount || got.LoanCount != shared.LoanCount {
			t.Errorf("depth %d: counts %d bills/%d loans, shared %d/%d", depth,
				got.BillCount, got.LoanCount, shared.BillCount, shared.LoanCount)
		}
		if got.NoDueDate != shared.WithoutDueDate {
			t.Errorf("depth %d: without_due_date %d, shared %d", depth, got.NoDueDate, shared.WithoutDueDate)
		}
		// The listed items are the shared ones, in the shared order.
		if want := len(shared.Items) - got.MoreItems; len(got.Items) != min(depth.maxDueItemsAt(), len(shared.Items)) {
			t.Errorf("depth %d: listed %d items, want %d (more_items %d)", depth, len(got.Items), want, got.MoreItems)
		}
		for i, it := range got.Items {
			s := shared.Items[i]
			if it.ID != s.ID.String() || it.Source != s.Source || it.Name != s.Name ||
				it.DueDate != s.DueDate.Format(time.DateOnly) || it.DaysUntil != s.DaysUntil ||
				it.DueWeekday != s.DueDate.Weekday().String()[:3] || it.Autopay != s.Autopay {
				t.Errorf("depth %d item %d: %+v, shared %+v", depth, i, it, s)
			}
			if (it.Amount == nil) != (s.AmountCents == nil) {
				t.Errorf("depth %d item %d: amount presence %v vs %v", depth, i, it.Amount, s.AmountCents)
			} else if it.Amount != nil && *it.Amount != FormatCents(*s.AmountCents) {
				t.Errorf("depth %d item %d: amount %s, shared %s", depth, i, *it.Amount, FormatCents(*s.AmountCents))
			}
		}
	}
}
