package calendar

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/testutil"
)

func decodeCalendar(t *testing.T, text string) Calendar {
	t.Helper()
	var c Calendar
	if err := json.Unmarshal(data(t, text), &c); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return c
}

func decodeError(t *testing.T, text string) respond.Error {
	t.Helper()
	var e respond.Error
	if err := json.Unmarshal(field(t, text, "error"), &e); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return e
}

func countCalendars(t *testing.T, pool *pgxpool.Pool, householdID uuid.UUID, name string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM calendars WHERE household_id = $1 AND lower(name) = lower($2)`,
		householdID, name).Scan(&n); err != nil {
		t.Fatalf("count calendars: %v", err)
	}
	return n
}

// caldavListing is the query apps/calendar's db.Repo.ListCalendars runs for
// CalDAV discovery (PROPFIND on the principal and calendar home set). Every
// column must scan into non-nullable fields there, so a NULL caldav_uid or
// ctag would break discovery for the whole family.
const caldavListing = `SELECT id::text, name, type::text, caldav_uid, ctag
	FROM calendars WHERE household_id = $1 ORDER BY created_at ASC`

func TestCalendarCreate(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	seedNamedCalendar(t, pool, hh.HouseholdID, "Family", "family")
	create := bind(t, NewCalendarCreateTool, pool)
	find := bind(t, NewCalendarFindTool, pool)

	text, isErr := callTool(t, create, ctx, map[string]any{"name": "  Kids   Sports ", "color": "#10b981"})
	if isErr {
		t.Fatalf("calendar_create failed: %s", text)
	}
	cal := decodeCalendar(t, text)
	if cal.Name != "Kids Sports" || cal.Type != "custom" || cal.Color == nil || *cal.Color != "#10B981" || cal.PropertyID != nil {
		t.Errorf("created calendar = %+v", cal)
	}
	if !isUUID(cal.ID) {
		t.Errorf("id %q is not a UUID string", cal.ID)
	}
	if !strings.Contains(text, `event_create calendar_id=\"Kids Sports\"`) {
		t.Errorf("next hints should point to event_create with the name: %s", text)
	}

	t.Run("listed by calendar_find", func(t *testing.T) {
		text, isErr := callTool(t, find, ctx, map[string]any{})
		if isErr {
			t.Fatalf("calendar_find failed: %s", text)
		}
		var cals []Calendar
		if err := json.Unmarshal(items(t, text), &cals); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range cals {
			found = found || (c.ID == cal.ID && c.Name == "Kids Sports")
		}
		if !found || len(cals) != 2 {
			t.Errorf("calendar_find = %+v, want Family and Kids Sports", cals)
		}
	})

	t.Run("listed by CalDAV discovery", func(t *testing.T) {
		rows, err := pool.Query(context.Background(), caldavListing, hh.HouseholdID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		found := false
		for rows.Next() {
			var id, name, typ, uid, ctag string // non-nullable, as in ListCalendars
			if err := rows.Scan(&id, &name, &typ, &uid, &ctag); err != nil {
				t.Fatalf("scan as CalDAV does: %v", err)
			}
			if id == cal.ID {
				found = true
				if uid == "" || ctag == "" || typ != "custom" {
					t.Errorf("caldav_uid=%q ctag=%q type=%q", uid, ctag, typ)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Error("new calendar missing from the CalDAV listing")
		}
	})

	t.Run("event_create accepts its name", func(t *testing.T) {
		ev := bind(t, NewEventCreateTool, pool)
		text, isErr := callTool(t, ev, ctx, map[string]any{
			"title": "Practice", "calendar_id": "kids sports",
			"start": "2026-11-20T17:00:00Z", "end": "2026-11-20T18:00:00Z",
		})
		if isErr {
			t.Fatalf("event_create failed: %s", text)
		}
		if got := writtenEvent(t, text).Event.CalendarID; got != cal.ID {
			t.Errorf("event calendar = %s, want %s", got, cal.ID)
		}
	})

	t.Run("duplicate name is refused with the existing id", func(t *testing.T) {
		for _, name := range []string{"Kids Sports", "kids sports", "FAMILY"} {
			text, isErr := callTool(t, create, ctx, map[string]any{"name": name})
			if !isErr {
				t.Fatalf("duplicate %q was created: %s", name, text)
			}
			e := decodeError(t, text)
			if e.Field != "name" || !strings.Contains(e.Message, "already has a calendar named") ||
				!strings.Contains(e.Fix, "event_create") || e.Example["name"] == nil {
				t.Errorf("duplicate %q error = %+v", name, e)
			}
		}
		if !strings.Contains(decodeError(t, mustCall(t, create, ctx, "kids sports")).Message, cal.ID) {
			t.Error("duplicate error should name the existing calendar's id")
		}
		if n := countCalendars(t, pool, hh.HouseholdID, "Kids Sports"); n != 1 {
			t.Errorf("%d Kids Sports calendars, want 1", n)
		}
	})

	t.Run("same name in another family is allowed", func(t *testing.T) {
		otherCtx, other := testutil.SeedHousehold(t, pool)
		text, isErr := callTool(t, create, otherCtx, map[string]any{"name": "Kids Sports"})
		if isErr {
			t.Fatalf("other family: %s", text)
		}
		if decodeCalendar(t, text).Color != nil {
			t.Error("color should be null when not given")
		}
		if n := countCalendars(t, pool, other.HouseholdID, "Kids Sports"); n != 1 {
			t.Errorf("other family has %d, want 1", n)
		}
	})
}

func mustCall(t *testing.T, h handlerFunc, ctx context.Context, name string) string {
	t.Helper()
	text, _ := callTool(t, h, ctx, map[string]any{"name": name})
	return text
}

// TestCalendarCreateConcurrent: parallel creates of one name in one family
// make exactly one calendar; the rest get the duplicate error.
func TestCalendarCreateConcurrent(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, create := NewCalendarCreateTool(pool)

	const n = 8
	var wg sync.WaitGroup
	results := make([]bool, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var req mcp.CallToolRequest
			req.Params.Arguments = map[string]any{"name": "Pets"}
			res, err := create(ctx, req)
			results[i] = err == nil && res != nil && !res.IsError
		}()
	}
	wg.Wait()
	ok := 0
	for _, r := range results {
		if r {
			ok++
		}
	}
	if ok != 1 {
		t.Errorf("%d creates succeeded, want 1", ok)
	}
	if c := countCalendars(t, pool, hh.HouseholdID, "Pets"); c != 1 {
		t.Errorf("%d Pets calendars, want 1", c)
	}
}

func TestCalendarCreateValidation(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	create := bind(t, NewCalendarCreateTool, pool)

	tests := []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"missing name", map[string]any{}, "name"},
		{"blank name", map[string]any{"name": "   "}, "name"},
		{"too long", map[string]any{"name": strings.Repeat("x", 256)}, "name"},
		{"uuid name", map[string]any{"name": uuid.NewString()}, "name"},
		{"short color", map[string]any{"name": "Kids", "color": "#abc"}, "color"},
		{"named color", map[string]any{"name": "Kids", "color": "green"}, "color"},
		{"no hash", map[string]any{"name": "Kids", "color": "10B981"}, "color"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, isErr := callTool(t, create, ctx, tt.args)
			if !isErr {
				t.Fatalf("want isError, got %s", text)
			}
			if e := decodeError(t, text); e.Field != tt.field || e.Fix == "" {
				t.Errorf("error = %+v, want field %q with a fix", e, tt.field)
			}
		})
	}
	if c := countCalendars(t, pool, hh.HouseholdID, "Kids"); c != 0 {
		t.Errorf("invalid calls created %d calendars", c)
	}

	t.Run("255 characters is accepted", func(t *testing.T) {
		if text, isErr := callTool(t, create, ctx, map[string]any{"name": strings.Repeat("é", 255)}); isErr {
			t.Fatalf("255-rune name refused: %s", text)
		}
	})

	t.Run("no claims", func(t *testing.T) {
		text, isErr := callTool(t, create, context.Background(), map[string]any{"name": "Kids"})
		if !isErr || !strings.Contains(text, "Authentication required") {
			t.Errorf("unauthenticated call = %s", text)
		}
	})
}
