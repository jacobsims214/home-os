package bill

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// CategoryTotal is one category of bills: how many and their total amount
// (the sum of bills.amount; every bill is monthly until #4468).
type CategoryTotal struct {
	Category string  `json:"category"`
	Count    int     `json:"count"`
	Total    float64 `json:"total"`
}

// SummarizeByCategory totals the household's bills by category (NULL is
// "Uncategorized"), largest total first, and returns the grand total. It is
// what the old get_bill_summary tool returned; budget_summary's obligations
// section (finance, #4372) uses it so the numbers stay identical.
func SummarizeByCategory(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, householdID string) ([]CategoryTotal, float64, error) {
	rows, err := q.Query(ctx,
		`SELECT COALESCE(category, 'Uncategorized') AS category,
		        COUNT(*) AS count,
		        COALESCE(SUM(amount), 0)::float8 AS total
		 FROM bills
		 WHERE household_id = $1
		 GROUP BY 1
		 ORDER BY total DESC, category`,
		householdID,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("summarize bills: %w", err)
	}
	totals, err := pgx.CollectRows(rows, pgx.RowToStructByPos[CategoryTotal])
	if err != nil {
		return nil, 0, fmt.Errorf("summarize bills: %w", err)
	}
	grand := 0.0
	for _, c := range totals {
		grand += c.Total
	}
	if totals == nil {
		totals = []CategoryTotal{}
	}
	return totals, grand, nil
}
