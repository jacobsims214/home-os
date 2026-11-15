package household

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// checkReadOnly asserts class R annotations: all four hints set explicitly.
func checkReadOnly(t *testing.T, tool mcp.Tool, title string) {
	t.Helper()
	a := tool.Annotations
	if a.Title != title || a.ReadOnlyHint == nil || !*a.ReadOnlyHint ||
		a.DestructiveHint == nil || *a.DestructiveHint ||
		a.IdempotentHint == nil || !*a.IdempotentHint ||
		a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("%s annotations = %+v, want class R titled %q", tool.Name, a, title)
	}
	if len(tool.InputSchema.Required) != 0 {
		t.Errorf("%s required = %v, want none", tool.Name, tool.InputSchema.Required)
	}
	if n := len([]rune(tool.Description)); n > 200 {
		t.Errorf("%s description is %d characters, max 200", tool.Name, n)
	}
}

// personIDOf returns the people.id of userID in household hh.
func personIDOf(t *testing.T, pool *pgxpool.Pool, hh uuid.UUID, userID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`SELECT id::text FROM people WHERE household_id = $1 AND user_id = $2`, hh, userID,
	).Scan(&id); err != nil {
		t.Fatalf("person of %s: %v", userID, err)
	}
	return id
}

func insertCalendar(t *testing.T, pool *pgxpool.Pool, hh uuid.UUID, name, typ string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO calendars (household_id, name, type, color, caldav_uid)
		 VALUES ($1, $2, $3, '#336699', $4) RETURNING id::text`,
		hh, name, typ, uuid.NewString(),
	).Scan(&id); err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	return id
}

// insertKid adds a person without a login to hh.
func insertKid(t *testing.T, pool *pgxpool.Pool, hh uuid.UUID, name string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO people (household_id, display_name, relationship) VALUES ($1, $2, 'child') RETURNING id::text`,
		hh, name,
	).Scan(&id); err != nil {
		t.Fatalf("insert kid: %v", err)
	}
	return id
}

func TestFamilyOverview(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, other := testutil.SeedHousehold(t, pool)
	_ = otherCtx

	name, tool, h := NewFamilyOverviewTool(pool)
	if name != "family_overview" || tool.Name != "family_overview" {
		t.Fatalf("tool name = %q/%q", name, tool.Name)
	}
	rs := outputSchema(t, tool)
	checkReadOnly(t, tool, "Family overview")

	familyCal := insertCalendar(t, pool, hh.HouseholdID, "Family", "family")
	billsCal := insertCalendar(t, pool, hh.HouseholdID, "Bills", "bills")
	insertCalendar(t, pool, other.HouseholdID, "Other family", "family")
	insertKid(t, pool, hh.HouseholdID, "Nora") // no login: person_find shows kids from #4464
	insertKid(t, pool, other.HouseholdID, "Someone else")

	decode := func(t *testing.T, env envelope) overview {
		t.Helper()
		var ov overview
		if err := json.Unmarshal(env.Data, &ov); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return ov
	}

	t.Run("basic sections for a new family", func(t *testing.T) {
		before := time.Now().UTC()
		env, res := callEnvelope(t, ctx, h, rs, map[string]any{})
		after := time.Now().UTC()
		if !env.OK || res.IsError {
			t.Fatalf("want ok, got %+v", env)
		}
		ov := decode(t, env)
		f := ov.Family
		if f.ID != hh.HouseholdID.String() || !strings.HasPrefix(f.Name, "Test Household") || f.Timezone != "UTC" {
			t.Errorf("family = %+v", f)
		}
		if f.Today != before.Format("2006-01-02") && f.Today != after.Format("2006-01-02") {
			t.Errorf("today = %q, want %s", f.Today, before.Format("2006-01-02"))
		}
		if f.Weekday != before.Format("Mon") && f.Weekday != after.Format("Mon") {
			t.Errorf("weekday = %q, want %s", f.Weekday, before.Format("Mon"))
		}
		if len(ov.People) != 1 {
			t.Fatalf("people = %+v, want only the owner (kids arrive with #4464)", ov.People)
		}
		p := ov.People[0]
		if p.ID != personIDOf(t, pool, hh.HouseholdID, hh.UserID.String()) || p.Name != "Test Owner" ||
			!p.HasAccount || p.UserID == nil || *p.UserID != hh.UserID.String() || p.Role == nil || *p.Role != "owner" {
			t.Errorf("person = %+v", p)
		}
		if len(ov.Calendars) != 2 || ov.Calendars[0].ID != billsCal || ov.Calendars[1].ID != familyCal ||
			ov.Calendars[1].Name != "Family" || ov.Calendars[1].Type != "family" ||
			ov.Calendars[1].Color == nil || *ov.Calendars[1].Color != "#336699" {
			t.Errorf("calendars = %+v", ov.Calendars)
		}
		if len(ov.HowTo) < 5 || len(ov.HowTo) > 8 {
			t.Errorf("how_to has %d lines, want 5-8", len(ov.HowTo))
		}
		if !strings.Contains(env.Summary, f.Today) || !strings.Contains(env.Summary, "1 person") || len(env.Next) == 0 {
			t.Errorf("summary %q next %v", env.Summary, env.Next)
		}
	})

	t.Run("today follows the family timezone", func(t *testing.T) {
		// Kiritimati is UTC+14 and Pago Pago UTC-11: 25 hours apart, so the
		// two families are never on the same date.
		for _, tz := range []string{"Pacific/Kiritimati", "Pacific/Pago_Pago"} {
			if _, err := pool.Exec(context.Background(), `UPDATE households SET timezone = $2 WHERE id = $1`, hh.HouseholdID, tz); err != nil {
				t.Fatal(err)
			}
			loc, _ := time.LoadLocation(tz)
			want := time.Now().In(loc)
			env, _ := callEnvelope(t, ctx, h, rs, map[string]any{"days": 7})
			ov := decode(t, env)
			if ov.Family.Timezone != tz || ov.Family.Today != want.Format("2006-01-02") || ov.Family.Weekday != want.Format("Mon") {
				t.Errorf("%s: family = %+v, want %s %s", tz, ov.Family, want.Format("2006-01-02"), want.Format("Mon"))
			}
		}
	})

	t.Run("new members show up", func(t *testing.T) {
		addMember(t, pool, hh, "family_member")
		env, _ := callEnvelope(t, ctx, h, rs, nil)
		if ov := decode(t, env); len(ov.People) != 2 || ov.People[1].Name != "Member family_member" {
			t.Errorf("people = %+v", ov.People)
		}
	})

	for _, days := range []any{0, 32, 2.5, "soon"} {
		t.Run("rejects days", func(t *testing.T) {
			env, res := callEnvelope(t, ctx, h, rs, map[string]any{"days": days})
			if env.OK || !res.IsError || env.Error == nil || env.Error.Field != "days" || env.Error.Fix == "" {
				t.Errorf("days=%v: want isError on days, got %+v", days, env)
			}
		})
	}

	t.Run("no claims", func(t *testing.T) {
		env, res := callEnvelope(t, context.Background(), h, rs, map[string]any{})
		if env.OK || !res.IsError {
			t.Fatalf("want isError, got %+v", env)
		}
	})
}

func TestPersonFind(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, other := testutil.SeedHousehold(t, pool)

	name, tool, h := NewPersonFindTool(pool)
	if name != "person_find" || tool.Name != "person_find" {
		t.Fatalf("tool name = %q/%q", name, tool.Name)
	}
	rs := outputSchema(t, tool)
	checkReadOnly(t, tool, "Find people")

	mgrCtx := addMember(t, pool, hh, "family_manager")
	mgrUser := server.ClaimsFromContext(mgrCtx).UserID
	ownerPerson := personIDOf(t, pool, hh.HouseholdID, hh.UserID.String())
	mgrPerson := personIDOf(t, pool, hh.HouseholdID, mgrUser)
	otherPerson := personIDOf(t, pool, other.HouseholdID, other.UserID.String())
	insertKid(t, pool, hh.HouseholdID, "Nora")
	birth := time.Now().UTC().AddDate(-10, 0, -1).Format("2006-01-02")
	if _, err := pool.Exec(context.Background(),
		`UPDATE people SET relationship = 'parent', birthdate = $2 WHERE id = $1`, ownerPerson, birth); err != nil {
		t.Fatal(err)
	}

	ids := func(items []personRow) []string {
		out := make([]string, len(items))
		for i, p := range items {
			out[i] = p.ID
		}
		return out
	}

	t.Run("lists members in join order", func(t *testing.T) {
		env, _ := callEnvelope(t, ctx, h, rs, map[string]any{})
		if !env.OK || env.Total != 2 || env.Truncated {
			t.Fatalf("got %+v", env)
		}
		if got := ids(env.Items); len(got) != 2 || got[0] != ownerPerson || got[1] != mgrPerson {
			t.Fatalf("ids = %v, want owner then manager", got)
		}
		o := env.Items[0]
		if o.Relationship == nil || *o.Relationship != "parent" || o.Age == nil || *o.Age != 10 || !o.HasAccount ||
			o.Email == nil || *o.Email != hh.Email || o.Role == nil || *o.Role != "owner" {
			t.Errorf("owner = %+v", o)
		}
		if m := env.Items[1]; m.Relationship != nil || m.Age != nil || m.Role == nil || *m.Role != "family_manager" {
			t.Errorf("manager = %+v", m)
		}
		if !strings.Contains(env.Summary, "2 people") || len(env.Next) == 0 {
			t.Errorf("summary %q next %v", env.Summary, env.Next)
		}
	})

	filters := []struct {
		name string
		args map[string]any
		want []string
	}{
		{"query by name, any case", map[string]any{"query": "MANAGER"}, []string{mgrPerson}},
		{"query by email", map[string]any{"query": hh.Email[:12]}, []string{ownerPerson}},
		{"relationship any case", map[string]any{"relationship": "Parent"}, []string{ownerPerson}},
		{"id is the person id", map[string]any{"id": mgrPerson}, []string{mgrPerson}},
		{"id may be the user id", map[string]any{"id": strings.ToUpper(mgrUser)}, []string{mgrPerson}},
		{"blank filters are ignored", map[string]any{"query": " ", "relationship": "", "cursor": "", "limit": ""}, []string{ownerPerson, mgrPerson}},
		{"no match", map[string]any{"query": "nobody-here"}, []string{}},
		{"kids without a login wait for #4464", map[string]any{"query": "Nora"}, []string{}},
	}
	for _, tc := range filters {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := callEnvelope(t, ctx, h, rs, tc.args)
			if !env.OK {
				t.Fatalf("want ok, got %+v", env)
			}
			if got := ids(env.Items); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("ids = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("pages with an opaque cursor", func(t *testing.T) {
		env, _ := callEnvelope(t, ctx, h, rs, map[string]any{"limit": 1})
		if !env.OK || !env.Truncated || env.Total != 2 || len(env.Items) != 1 || env.Items[0].ID != ownerPerson {
			t.Fatalf("page 1 = %+v", env)
		}
		if env.NextCursor != respond.EncodeCursor(1) {
			t.Fatalf("page 1 next_cursor = %q", env.NextCursor)
		}
		env, _ = callEnvelope(t, ctx, h, rs, map[string]any{"limit": 1, "cursor": env.NextCursor})
		if !env.OK || env.Truncated || env.NextCursor != "" || len(env.Items) != 1 || env.Items[0].ID != mgrPerson {
			t.Fatalf("page 2 = %+v", env)
		}
	})

	t.Run("another family's person is not found", func(t *testing.T) {
		env, res := callEnvelope(t, ctx, h, rs, map[string]any{"id": otherPerson})
		if env.OK || !res.IsError || env.Error == nil || !strings.Contains(env.Error.Fix, "person_find") {
			t.Fatalf("want not found, got %+v", env)
		}
		env, _ = callEnvelope(t, otherCtx, h, rs, map[string]any{})
		if got := ids(env.Items); len(got) != 1 || got[0] != otherPerson {
			t.Errorf("other family sees %v", got)
		}
	})

	invalid := []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"id not a uuid", map[string]any{"id": "Nora"}, "id"},
		{"limit 0", map[string]any{"limit": 0}, "limit"},
		{"limit 101", map[string]any{"limit": 101}, "limit"},
		{"limit fraction", map[string]any{"limit": 1.5}, "limit"},
		{"bad cursor", map[string]any{"cursor": "page2"}, "cursor"},
	}
	for _, tc := range invalid {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			env, res := callEnvelope(t, ctx, h, rs, tc.args)
			if env.OK || !res.IsError || env.Error == nil || env.Error.Field != tc.field || env.Error.Fix == "" {
				t.Errorf("want isError on %s, got %+v", tc.field, env)
			}
		})
	}

	t.Run("no claims", func(t *testing.T) {
		env, res := callEnvelope(t, context.Background(), h, rs, map[string]any{})
		if env.OK || !res.IsError {
			t.Fatalf("want isError, got %+v", env)
		}
	})
}

// TestPersonFindLiteralWildcards: % and _ in query match literally (#4670).
func TestPersonFindLiteralWildcards(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, tool, h := NewPersonFindTool(pool)
	rs := outputSchema(t, tool)

	// viewer's email ("viewer-<hex>@test.homeos.local") and the owner's
	// contain no % or _, so only this display name holds them.
	viewerCtx := addMember(t, pool, hh, "viewer")
	viewerPerson := personIDOf(t, pool, hh.HouseholdID, server.ClaimsFromContext(viewerCtx).UserID)
	if _, err := pool.Exec(context.Background(),
		`UPDATE people SET display_name = 'Pat_O 100%' WHERE id = $1`, viewerPerson); err != nil {
		t.Fatal(err)
	}
	ownerPerson := personIDOf(t, pool, hh.HouseholdID, hh.UserID.String())

	cases := []struct {
		query string
		want  []string
	}{
		{"%", []string{viewerPerson}},   // literal % only in "100%"
		{"_", []string{viewerPerson}},   // literal _ only in "Pat_O"
		{"t_O", []string{viewerPerson}}, // literal match across _
		{"0%", []string{viewerPerson}},
		{"%%", []string{}},  // would match everyone as a wildcard
		{"P_t", []string{}}, // "_" as a wildcard would match "Pat"
		{"Test%Owner", []string{}},
		{"Test Owner", []string{ownerPerson}},
		{`\`, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			env, _ := callEnvelope(t, ctx, h, rs, map[string]any{"query": tc.query})
			if !env.OK {
				t.Fatalf("want ok, got %+v", env)
			}
			got := make([]string, len(env.Items))
			for i, p := range env.Items {
				got[i] = p.ID
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("query %q: ids = %v, want %v", tc.query, got, tc.want)
			}
		})
	}

	// With no literal % anywhere, "%" finds nobody.
	if _, err := pool.Exec(context.Background(),
		`UPDATE people SET display_name = 'Pat' WHERE id = $1`, viewerPerson); err != nil {
		t.Fatal(err)
	}
	env, _ := callEnvelope(t, ctx, h, rs, map[string]any{"query": "%"})
	if !env.OK || len(env.Items) != 0 {
		t.Fatalf(`query "%%" with no literal %%: got %+v`, env)
	}
}
