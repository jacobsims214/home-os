package searchrecon_test

// #5298 acceptance test: search finds events and bills the family can see.
//
// THE BUG THIS PINS DOWN. /dashboard/search promised "events, notes, documents,
// bills, people, …" and returned nothing for "dentist", "soccer" and
// "mortgage", while Postgres held those events and those bills. Two holes, both
// indexing rather than querying:
//
//   - calendar_objects had no indexer in any binary;
//   - the MCP's write tools (bill_create and the rest) never index, and the R2
//     household was created through agents, so bills and notes were missing too.
//
// WHY THE TEST LOOKS LIKE THIS. It goes through the two things that were
// actually wrong and were never exercised together before: the reconcile step
// that derives the index from Postgres, and the real search.Client query the
// UI's endpoint runs. Nothing here calls IndexDocument by hand — doing that
// would reproduce only the handlers that already worked, and would have passed
// on the buggy tree.
//
// IT MUST FAIL BEFORE THE FIX. Before searchrecon existed there was no way to
// put a bill or an event in the index from Postgres at all, so every assertion
// below returns zero hits (revert searchrecon + the sweep and this test fails
// on "dentist", "soccer", "mortgage", "preschool" and "electric").
//
// Needs TEST_DATABASE_URL (a migrated database) and TYPESENSE_TEST_ADDR/
// TYPESENSE_TEST_KEY, like the other Typesense integration tests; it is skipped
// without them, and CI runs it in the must-PASS Typesense step.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/search"
	"home-os/api/internal/search/searchtest"
	"home-os/api/internal/searchrecon"
)

// r2Terms are the three terms the R2 war game reported, plus the two from the
// same finding, each with the record it must reach.
var r2Terms = []struct {
	term     string
	entity   string // the entity_type the hit must carry
	wantText string // text the hit's title or body must contain
}{
	{"dentist", "calendar_event", "dentist"},
	{"soccer", "calendar_event", "soccer"},
	{"mortgage", "bill", "Mortgage"},
	{"preschool", "calendar_event", "preschool"},
	{"electric", "bill", "Electric"},
}

func TestSearchFindsEventsAndBillsIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	client, _ := searchtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	require.NoError(t, client.InitCollection(ctx))

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// A household of its own, so the reconcile pass cannot mix in another
	// family's rows (searchtest gives each test its own collection, this gives
	// it its own household).
	var hh uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name, timezone) VALUES ('Search R2 test', 'America/New_York') RETURNING id`).Scan(&hh))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hh)
	})

	exec := func(sql string, args ...any) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		require.NoError(t, pool.QueryRow(ctx, sql, args...).Scan(&id), sql)
		return id
	}

	// The calendars and bills the R2 finding names. A bill needs a Bills
	// calendar for its mirror event, exactly as the real repositories create it.
	cal := exec(`INSERT INTO calendars (household_id, name, type, caldav_uid)
	             VALUES ($1, 'Family', 'family', $2) RETURNING id`, hh, uuid.NewString())
	billsCal := exec(`INSERT INTO calendars (household_id, name, type, caldav_uid)
	                  VALUES ($1, 'Bills', 'bills', $2) RETURNING id`, hh, uuid.NewString())

	// Ordinary family events: the ones search could not see at all.
	for _, ev := range []struct{ title, description, start string }{
		{"Liam: dentist", "Liam — checkup, 3pm", "2026-11-24T21:00:00Z"},
		{"Liam: soccer practice", "Kit at the field", "2026-11-19T17:30:00-05:00"},
		{"Nora: preschool", "Pickup at 11:30", "2026-11-20T08:30:00-05:00"},
	} {
		ical := fmt.Sprintf(`{"title":%q,"description":%q,"start":%q,"end":%q,"all_day":false,"location":"","color":"","event_type":""}`,
			ev.title, ev.description, ev.start, ev.start)
		_, err := pool.Exec(ctx,
			`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type, starts_at)
			 VALUES ($1, $2, $3, 'e', 'event', $4)`,
			cal, uuid.NewString(), ical, ev.start)
		require.NoError(t, err)
	}

	// Bills. Their mirror events are deliberately NOT inserted here: the point
	// of the event exclusion in searchrecon is that a bill is found as a bill,
	// and the reported leak (a mirror's "Amount: $X" description) never becomes
	// a search hit.
	bills := []struct{ name, category, amount string }{
		{"Mortgage", "Housing", "2340.00"},
		{"Electric", "Utilities", "180.00"},
	}
	for _, b := range bills {
		exec(`INSERT INTO bills (household_id, name, category, amount, due_day, frequency)
		      VALUES ($1, $2, $3, $4::numeric, 1, 'monthly') RETURNING id`, hh, b.name, b.category, b.amount)
		// The mirror the bill repository writes, with the money in the
		// description — the exact row that must not become an event hit.
		ical := fmt.Sprintf(`{"title":%q,"description":%q,"start":"2026-12-01T00:00:00Z","end":"2026-12-01T00:00:00Z","all_day":true,"location":"","color":"","event_type":""}`,
			b.name+" due", "Amount: $"+b.amount)
		_, err := pool.Exec(ctx,
			`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
			 VALUES ($1, $2, $3, 'e', 'bill')`, billsCal, uuid.NewString(), ical)
		require.NoError(t, err)
	}

	// The reconcile is the fix: before it, none of the rows above was in the
	// index, because no writer for them indexes.
	stats, err := searchrecon.Reconcile(ctx, pool, client, hh.String())
	require.NoError(t, err)
	require.Positive(t, stats.Indexed, "the reconcile must derive documents from Postgres")

	// Every reported term now answers through the real query path the UI's
	// endpoint uses (search.Client.Search: household_search + files, hybrid).
	for _, tc := range r2Terms {
		t.Run(tc.term, func(t *testing.T) {
			results, err := client.Search(ctx, hh.String(), tc.term, search.SearchFilters{})
			require.NoError(t, err)

			var found bool
			for _, r := range results {
				if r.EntityType == tc.entity {
					found = true
					text := r.Title + " " + r.Body
					assert.Contains(t, text, tc.wantText,
						"%q hit %s/%s should carry the matched text", tc.term, r.EntityType, r.EntityID)
					break
				}
			}
			assert.True(t, found, "search %q must find a %s; got %d hits: %v",
				tc.term, tc.entity, len(results), titles(results))
		})
	}

	// The bill mirror must not appear as an event: its description carries the
	// amount (#4807's leak), and the bill is already a bill hit.
	t.Run("bill mirror is not an event hit", func(t *testing.T) {
		results, err := client.Search(ctx, hh.String(), "due", search.SearchFilters{})
		require.NoError(t, err)
		for _, r := range results {
			assert.NotEqual(t, "calendar_event", r.EntityType,
				"the bills calendar mirror must not be indexed as an event: %s", r.Title)
		}
	})

	// A record deleted from Postgres leaves the index on the next reconcile,
	// so search never offers a record that is gone.
	t.Run("deleted record is pruned", func(t *testing.T) {
		var billID uuid.UUID
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT id FROM bills WHERE household_id = $1 AND name = 'Electric'`, hh).Scan(&billID))
		_, err := pool.Exec(ctx, `DELETE FROM bills WHERE id = $1`, billID)
		require.NoError(t, err)

		_, err = searchrecon.Reconcile(ctx, pool, client, hh.String())
		require.NoError(t, err)

		results, err := client.Search(ctx, hh.String(), "electric", search.SearchFilters{})
		require.NoError(t, err)
		for _, r := range results {
			assert.False(t, r.EntityType == "bill" && r.EntityID == billID.String(),
				"a bill deleted from Postgres must not still be searchable")
		}
	})
}

func titles(results []search.SearchResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.EntityType+":"+r.Title)
	}
	return out
}
