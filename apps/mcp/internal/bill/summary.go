package bill

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

// CategoryTotal is one category of bills: how many count in the month and
// their total monthly amount (see SummarizeByCategory).
type CategoryTotal struct {
	Category string  `json:"category"`
	Count    int     `json:"count"`
	Total    float64 `json:"total"`
}

// monthlyAmount is what a bill costs per month, spread as the tool catalog
// and the API's budget obligations (#4471) do: monthly x1, quarterly /3,
// semiannual /6, annual /12, rounded to the cent. nil for a once bill (it
// counts in full in its due month only) or an unknown amount.
func monthlyAmount(amount *float64, frequency string) *float64 {
	months, ok := frequencyMonths(frequency)
	if amount == nil || !ok || months == 0 {
		return nil
	}
	v := math.Round(math.Round(*amount*100)/float64(months)) / 100
	return &v
}

// SummarizeByCategory totals the household's bills by category (NULL is
// "Uncategorized") as monthly amounts for the month starting at month (a
// date; only its year and month are used), largest total first, and returns
// the grand total. budget_summary's obligations section (finance, #4372)
// uses it.
//
// Bills are normalised to monthly exactly like the API's budget obligations
// (apps/api/internal/budget Repo.Obligations, KB
// api/budget-categories-obligations.md): monthly x1, quarterly /3,
// semiannual /6, annual /12; a once bill counts in full in the month its
// due_date falls in and is left out (not counted at all) in every other
// month. Each bill is rounded to the cent and the totals add the rounded
// cents. A bill with no amount is counted but adds nothing.
func SummarizeByCategory(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, householdID string, month time.Time) ([]CategoryTotal, float64, error) {
	start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	rows, err := q.Query(ctx,
		`SELECT COALESCE(category, 'Uncategorized') AS category,
		        COUNT(*) AS count,
		        (COALESCE(SUM(ROUND(CASE frequency
		                WHEN 'monthly'    THEN amount
		                WHEN 'quarterly'  THEN amount / 3
		                WHEN 'semiannual' THEN amount / 6
		                WHEN 'annual'     THEN amount / 12
		                WHEN 'once'       THEN amount
		              END * 100)), 0) / 100)::float8 AS total
		 FROM bills
		 WHERE household_id = $1
		   AND (frequency <> 'once' OR (due_date >= $2::date AND due_date < $3::date))
		 GROUP BY 1
		 ORDER BY total DESC, category`,
		householdID, start, start.AddDate(0, 1, 0),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("summarize bills: %w", err)
	}
	totals, err := pgx.CollectRows(rows, pgx.RowToStructByPos[CategoryTotal])
	if err != nil {
		return nil, 0, fmt.Errorf("summarize bills: %w", err)
	}
	var cents int64
	for _, c := range totals {
		cents += int64(math.Round(c.Total * 100))
	}
	if totals == nil {
		totals = []CategoryTotal{}
	}
	return totals, float64(cents) / 100, nil
}
