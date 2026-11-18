package activity

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// envelope is the wire shape of an activity_find result.
type envelope struct {
	OK      bool       `json:"ok"`
	Summary string     `json:"summary"`
	Data    findResult `json:"data"`
	Next    []string   `json:"next"`
	Error   *struct {
		Message string `json:"message"`
		Field   string `json:"field"`
		Fix     string `json:"fix"`
	} `json:"error"`
}

// call runs activity_find and checks the result the way a strict client
// would: one text block equal to structuredContent, valid against the
// tool's outputSchema. raw is the whole wire result, for leak checks.
func call(t *testing.T, ctx context.Context, pool *pgxpool.Pool, args map[string]any) (bool, envelope, string) {
	t.Helper()
	name, tool, handler := NewActivityFindTool(pool)
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := handler(ctx, req)
	if err != nil {
		t.Fatalf("activity_find returned a protocol error: %v", err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var w struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent map[string]any `json:"structuredContent"`
		IsError           bool           `json:"isError"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(w.Content) != 1 || w.Content[0].Type != "text" {
		t.Fatalf("want exactly one text block, got %+v", w.Content)
	}
	var fromText map[string]any
	if err := json.Unmarshal([]byte(w.Content[0].Text), &fromText); err != nil {
		t.Fatalf("text block is not JSON: %v", err)
	}
	sc, _ := json.Marshal(w.StructuredContent)
	tx, _ := json.Marshal(fromText)
	if string(sc) != string(tx) {
		t.Fatalf("text block != structuredContent:\n%s\n%s", tx, sc)
	}
	if err := outputSchema(t, tool).Validate(w.StructuredContent); err != nil {
		t.Fatalf("result does not match outputSchema: %v\n%s", err, sc)
	}
	var env envelope
	if err := json.Unmarshal([]byte(w.Content[0].Text), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return w.IsError, env, string(b)
}

func outputSchema(t *testing.T, tool mcp.Tool) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal tool: %v", err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || len(raw.OutputSchema) == 0 {
		t.Fatalf("tool %s declares no outputSchema (%v)", tool.Name, err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw.OutputSchema, &s); err != nil {
		t.Fatalf("unmarshal outputSchema: %v", err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve outputSchema: %v", err)
	}
	return rs
}

// findOK runs a successful activity_find call.
func findOK(t *testing.T, ctx context.Context, pool *pgxpool.Pool, args map[string]any) envelope {
	t.Helper()
	isErr, env, _ := call(t, ctx, pool, args)
	if isErr || !env.OK {
		t.Fatalf("activity_find %v failed: %+v", args, env)
	}
	return env
}

// family is a seeded household: the owner (Test Owner), Amber (a
// family_member with a login), Nora (a kid without one) and Amber's agent.
type family struct {
	pool     *pgxpool.Pool
	ctx      context.Context // the owner
	hh       testutil.Household
	amber    uuid.UUID
	amberCtx context.Context
	token    uuid.UUID
	calendar uuid.UUID
}

func newFamily(t *testing.T) *family {
	t.Helper()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	f := &family{pool: pool, ctx: ctx, hh: hh}
	bg := context.Background()
	email := "amber-" + uuid.NewString()[:8] + "@test.homeos.local"
	must(t, pool.QueryRow(bg, `INSERT INTO users (email, name, password_hash) VALUES ($1, 'Amber', 'x') RETURNING id`, email).Scan(&f.amber))
	exec(t, pool, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'family_member')`, hh.HouseholdID, f.amber)
	exec(t, pool, `INSERT INTO people (household_id, display_name, relationship) VALUES ($1, 'Nora', 'child')`, hh.HouseholdID)
	must(t, pool.QueryRow(bg, `INSERT INTO agent_tokens (user_id, household_id, name, token_prefix, token_hash)
		VALUES ($1, $2, 'Claude', 'hos_SECRETPFX', $3) RETURNING id`, f.amber, hh.HouseholdID, make([]byte, 32)).Scan(&f.token))
	must(t, pool.QueryRow(bg, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', $2) RETURNING id`,
		hh.HouseholdID, uuid.NewString()).Scan(&f.calendar))
	f.amberCtx = server.ContextWithClaims(bg, &auth.Claims{
		Identity: auth.Identity{Subject: f.amber.String(), Email: email, Name: "Amber"},
		UserID:   f.amber.String(), HouseholdID: hh.HouseholdID.String(), Role: "family_member",
	})
	// Fixture setup (memberships, people, the calendar) logs rows of its
	// own; start every test from an empty feed.
	f.clear(t)
	return f
}

func (f *family) clear(t *testing.T) {
	t.Helper()
	exec(t, f.pool, `DELETE FROM activity_log WHERE household_id = $1`, f.hh.HouseholdID)
}

// as runs fn in a transaction attributed to userID (and agent token, when
// not nil), the way the MCP's actor hook does for a real tool call. It
// returns the id fn's last statement scanned.
func (f *family) as(t *testing.T, userID uuid.UUID, agent *uuid.UUID, sql string, args ...any) uuid.UUID {
	t.Helper()
	bg := context.Background()
	tx, err := f.pool.Begin(bg)
	must(t, err)
	defer func() { _ = tx.Rollback(bg) }()
	_, err = tx.Exec(bg, `SELECT set_config('app.user_id', $1, true)`, userID.String())
	must(t, err)
	tok := ""
	if agent != nil {
		tok = agent.String()
	}
	_, err = tx.Exec(bg, `SELECT set_config('app.agent_token_id', $1, true)`, tok)
	must(t, err)
	var id uuid.UUID
	if strings.Contains(sql, "RETURNING") {
		must(t, tx.QueryRow(bg, sql, args...).Scan(&id))
	} else {
		_, err = tx.Exec(bg, sql, args...)
		must(t, err)
	}
	must(t, tx.Commit(bg))
	return id
}

func (f *family) setRole(t *testing.T, user uuid.UUID, role string) {
	t.Helper()
	exec(t, f.pool, `UPDATE memberships SET role = $3 WHERE household_id = $1 AND user_id = $2`, f.hh.HouseholdID, user, role)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func lines(env envelope) []string {
	var out []string
	for _, c := range env.Data.Changes {
		out = append(out, c.Line)
	}
	return out
}

func hasLine(env envelope, want string) bool {
	for _, l := range lines(env) {
		if l == want {
			return true
		}
	}
	return false
}

// TestActivityFind_WhatDidAmberAdd is the acceptance case: "What has Amber
// added this week?" is one call, and it includes her calendar events.
func TestActivityFind_WhatDidAmberAdd(t *testing.T) {
	f := newFamily(t)
	h := f.hh.HouseholdID

	event := f.as(t, f.amber, nil, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
		VALUES ($1, $2, '{"title":"Dentist","start":"2026-11-20T09:00:00-05:00"}', 'e0', 'event') RETURNING id`, f.calendar, uuid.NewString())
	list := f.as(t, f.amber, nil, `INSERT INTO lists (household_id, name, entity_type, entity_id) VALUES ($1, 'Grocery', 'household', $1) RETURNING id`, h)
	f.as(t, f.amber, nil, `INSERT INTO list_items (list_id, text) VALUES ($1, 'milk') RETURNING id`, list)
	f.as(t, f.amber, nil, `INSERT INTO list_items (list_id, text) VALUES ($1, 'eggs') RETURNING id`, list)
	bill := f.as(t, f.amber, nil, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Netflix', 15, 3) RETURNING id`, h)
	f.as(t, f.amber, nil, `UPDATE bills SET amount = 18 WHERE id = $1`, bill)
	// Someone else's change, and one of Amber's from before the window.
	f.as(t, f.hh.UserID, nil, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'household', $1, 'Owner note', 'b') RETURNING id`, h)
	exec(t, f.pool, `INSERT INTO activity_log (household_id, actor_user_id, entity_type, entity_id, action, summary, created_at)
		VALUES ($1, $2, 'bill', gen_random_uuid(), 'create', 'created bill "Old"', now() - interval '10 days')`, h, f.amber)

	env := findOK(t, f.ctx, f.pool, map[string]any{"person_id": "amber", "action": "created"})
	if env.Data.Total != 5 || env.Data.Returned != 5 {
		t.Fatalf("total/returned = %d/%d, want 5/5: %v", env.Data.Total, env.Data.Returned, lines(env))
	}
	for _, c := range env.Data.Changes {
		if c.Who != "Amber" || c.Action != "created" {
			t.Errorf("change %+v is not Amber creating", c)
		}
	}
	if !hasLine(env, `Amber created event "Dentist"`) {
		t.Errorf("missing the calendar event: %v", lines(env))
	}
	if env.Data.Changes[0].Line != `Amber created bill "Netflix"` {
		t.Errorf("newest first: got %q first", env.Data.Changes[0].Line)
	}
	for _, c := range env.Data.Changes {
		if c.What == "event" && c.Open != "event_find id="+event.String() {
			t.Errorf("event open = %q", c.Open)
		}
		if c.What == "list item" && c.Open != "list_get list_id="+list.String() {
			t.Errorf("list item open = %q", c.Open)
		}
	}
	if len(env.Data.ByPerson) != 1 || env.Data.ByPerson[0].Detail != "created 2 list items, 1 bill, 1 event, 1 list" {
		t.Errorf("by_person = %+v", env.Data.ByPerson)
	}
	if !strings.HasPrefix(env.Summary, "Amber made 5 changes since ") {
		t.Errorf("summary = %q", env.Summary)
	}

	// Without action, her update shows with its changed field AND the value it
	// recorded (migration 071, #4941): the amount went from 15 to 18.
	env = findOK(t, f.ctx, f.pool, map[string]any{"person_id": f.amber.String()})
	if env.Data.Total != 6 || !hasLine(env, `Amber updated bill "Netflix" (amount; amount $15.00 -> $18.00)`) {
		t.Errorf("all of Amber's changes: total %d, %v", env.Data.Total, lines(env))
	}
	// And the value itself is in the structured payload, for a client that
	// shows it as a before/after pair.
	var amountChange *struct {
		From *float64 `json:"from"`
		To   *float64 `json:"to"`
	}
	for _, c := range env.Data.Changes {
		if c.Action == "updated" && c.Changes != nil {
			if v, ok := c.Changes["amount"]; ok {
				amountChange = &struct {
					From *float64 `json:"from"`
					To   *float64 `json:"to"`
				}{}
				if f, ok := v.From.(float64); ok {
					amountChange.From = &f
				}
				if t, ok := v.To.(float64); ok {
					amountChange.To = &t
				}
			}
		}
	}
	if amountChange == nil || amountChange.From == nil || *amountChange.From != 15 || amountChange.To == nil || *amountChange.To != 18 {
		t.Errorf("the change payload should carry amount 15 -> 18: %+v", amountChange)
	}

	// about_type=event narrows to calendar records.
	env = findOK(t, f.ctx, f.pool, map[string]any{"person_id": "Amber", "about_type": "event"})
	if env.Data.Total != 1 || env.Data.Changes[0].What != "event" {
		t.Errorf("about_type=event: %v", lines(env))
	}

	// The whole family, grouped by who.
	env = findOK(t, f.ctx, f.pool, map[string]any{})
	if env.Data.Total != 7 || len(env.Data.ByPerson) != 2 || env.Data.ByPerson[0].Who != "Amber" || env.Data.ByPerson[1].Who != "Test Owner" {
		t.Errorf("family feed: total %d, by_person %+v", env.Data.Total, env.Data.ByPerson)
	}

	// An explicit since reaches the old row.
	env = findOK(t, f.ctx, f.pool, map[string]any{"person_id": "Amber", "since": time.Now().AddDate(0, 0, -11).Format(time.DateOnly)})
	if env.Data.Total != 7 {
		t.Errorf("since 11 days ago: total %d, want 7", env.Data.Total)
	}
}

// TestActivityFind_AgentAndSystem: agent writes say which agent, writes
// without an actor read System, and nothing secret reaches the output.
func TestActivityFind_AgentAndSystem(t *testing.T) {
	f := newFamily(t)
	h := f.hh.HouseholdID
	f.as(t, f.amber, &f.token, `INSERT INTO pets (household_id, name) VALUES ($1, 'Rex') RETURNING id`, h)
	exec(t, f.pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Plumber Joe')`, h)

	_, env, raw := call(t, f.amberCtx, f.pool, map[string]any{})
	if !hasLine(env, `Amber created pet "Rex" via agent "Claude"`) || !hasLine(env, `System created contact "Plumber Joe"`) {
		t.Fatalf("lines = %v", lines(env))
	}
	for _, leak := range []string{"hos_SECRETPFX", "@test.homeos.local", f.token.String()} {
		if strings.Contains(raw, leak) {
			t.Errorf("result leaks %q", leak)
		}
	}
}

// TestActivityFind_Roles: credentials are hidden from viewers (rows, notes on
// them, reveal lines), housesitters and vendors get no feed, and the role is
// re-read from memberships rather than trusted from the claims.
func TestActivityFind_Roles(t *testing.T) {
	f := newFamily(t)
	h := f.hh.HouseholdID
	secret := f.as(t, f.amber, nil, `INSERT INTO secrets (household_id, entity_type, entity_id, encrypted_data, iv, name, secret_type)
		VALUES ($1, 'household', $1, 'ENC-CIPHERTEXT', 'IV-VALUE', 'Wifi', 'login') RETURNING id`, h)
	f.as(t, f.amber, nil, `UPDATE secrets SET encrypted_data = 'ENC-CIPHERTEXT-2' WHERE id = $1`, secret)
	f.as(t, f.amber, nil, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'secret', $2, 'Router note', 'b') RETURNING id`, h, secret)
	exec(t, f.pool, `INSERT INTO activity_log (household_id, actor_user_id, actor_agent_token_id, entity_type, entity_id, action, summary)
		VALUES ($1, $2, $3, 'secret', $4, 'read', 'revealed secret "Wifi"')`, h, f.amber, f.token, secret)
	f.as(t, f.amber, nil, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Power', 90, 5) RETURNING id`, h)

	// A family member sees everything, values never.
	_, env, raw := call(t, f.amberCtx, f.pool, map[string]any{})
	if env.Data.Total != 5 || !hasLine(env, `Amber revealed credential "Wifi" via agent "Claude"`) ||
		!hasLine(env, `Amber updated credential "Wifi" (value)`) || !hasLine(env, `Amber created note "Router note"`) {
		t.Fatalf("family member: total %d, %v", env.Data.Total, lines(env))
	}
	for _, leak := range []string{"ENC-CIPHERTEXT", "IV-VALUE", "encrypted_data"} {
		if strings.Contains(raw, leak) {
			t.Errorf("result leaks %q", leak)
		}
	}

	// Demote the owner to viewer; the claims still say owner.
	f.setRole(t, f.hh.UserID, "viewer")
	_, env, raw = call(t, f.ctx, f.pool, map[string]any{})
	if env.Data.Total != 1 || env.Data.Changes[0].Line != `Amber created bill "Power"` {
		t.Fatalf("viewer sees %v", lines(env))
	}
	if strings.Contains(raw, "Wifi") || strings.Contains(raw, "Router") || strings.Contains(raw, "credential") {
		t.Errorf("viewer result mentions credentials: %s", raw)
	}
	env = findOK(t, f.ctx, f.pool, map[string]any{"about_type": "credential"})
	if env.Data.Total != 0 {
		t.Errorf("viewer about_type=credential: %v", lines(env))
	}

	for _, role := range []string{"housesitter", "vendor"} {
		f.setRole(t, f.hh.UserID, role)
		isErr, env, _ := call(t, f.ctx, f.pool, map[string]any{})
		if !isErr || env.Error == nil || !strings.Contains(env.Error.Message, role) {
			t.Errorf("%s: want an isError naming the role, got %+v", role, env)
		}
	}

	// A removed member gets nothing.
	exec(t, f.pool, `DELETE FROM memberships WHERE household_id = $1 AND user_id = $2`, h, f.hh.UserID)
	if isErr, _, _ := call(t, f.ctx, f.pool, map[string]any{}); !isErr {
		t.Error("removed member read the feed")
	}
}

// TestActivityFind_Paging: a cap with total and next_cursor; by_person
// counts every match, not just the page.
func TestActivityFind_Paging(t *testing.T) {
	f := newFamily(t)
	list := f.as(t, f.amber, nil, `INSERT INTO lists (household_id, name, entity_type, entity_id) VALUES ($1, 'Todo', 'household', $1) RETURNING id`, f.hh.HouseholdID)
	for _, item := range []string{"a", "b", "c", "d"} {
		f.as(t, f.amber, nil, `INSERT INTO list_items (list_id, text) VALUES ($1, $2) RETURNING id`, list, item)
	}

	p1 := findOK(t, f.ctx, f.pool, map[string]any{"limit": 2})
	if p1.Data.Total != 5 || p1.Data.Returned != 2 || !p1.Data.Truncated || p1.Data.NextCursor == "" {
		t.Fatalf("page 1 = %+v", p1.Data)
	}
	if p1.Data.ByPerson[0].Changes != 5 {
		t.Errorf("by_person counts the page only: %+v", p1.Data.ByPerson)
	}
	p2 := findOK(t, f.ctx, f.pool, map[string]any{"limit": 2, "cursor": p1.Data.NextCursor})
	p3 := findOK(t, f.ctx, f.pool, map[string]any{"limit": 2, "cursor": p2.Data.NextCursor})
	got := append(append(lines(p1), lines(p2)...), lines(p3)...)
	want := []string{`Amber created list item "d"`, `Amber created list item "c"`, `Amber created list item "b"`,
		`Amber created list item "a"`, `Amber created list "Todo"`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("pages = %v, want %v", got, want)
	}
	if p3.Data.Truncated || p3.Data.NextCursor != "" {
		t.Errorf("last page still truncated: %+v", p3.Data)
	}
}

// TestActivityFind_EventNamedFromLiveEvent: a pre-055 row with no name is
// named from the event while it exists.
func TestActivityFind_EventNamedFromLiveEvent(t *testing.T) {
	f := newFamily(t)
	var ev uuid.UUID
	must(t, f.pool.QueryRow(context.Background(), `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
		VALUES ($1, $2, '{"title":"Date night"}', 'e0', 'event') RETURNING id`, f.calendar, uuid.NewString()).Scan(&ev))
	f.clear(t)
	exec(t, f.pool, `INSERT INTO activity_log (household_id, actor_user_id, entity_type, entity_id, parent_type, parent_id, action, summary)
		VALUES ($1, $2, 'calendar_event', $3, 'calendar', $4, 'create', 'created calendar event')`, f.hh.HouseholdID, f.amber, ev, f.calendar)
	env := findOK(t, f.ctx, f.pool, map[string]any{"about_type": "event"})
	if !hasLine(env, `Amber created event "Date night"`) {
		t.Errorf("lines = %v", lines(env))
	}
}

func TestActivityFind_Arguments(t *testing.T) {
	f := newFamily(t)
	bad := []struct {
		args  map[string]any
		field string
	}{
		{map[string]any{"since": "2026-11-09T09:00:00"}, "since"},
		{map[string]any{"since": "last week"}, "since"},
		{map[string]any{"action": "exploded"}, "action"},
		{map[string]any{"about_type": "spaceship"}, "about_type"},
		{map[string]any{"cursor": "o:5"}, "cursor"},
		{map[string]any{"limit": 0}, "limit"},
		{map[string]any{"person_id": "Zed"}, "person_id"},
		{map[string]any{"person_id": 5}, "person_id"},
	}
	for _, tt := range bad {
		isErr, env, _ := call(t, f.ctx, f.pool, tt.args)
		if !isErr || env.Error == nil || env.Error.Field != tt.field || env.Error.Fix == "" {
			t.Errorf("%v: want isError on %s with a fix, got %+v", tt.args, tt.field, env)
		}
	}

	// Aliases and a person without a login.
	env := findOK(t, f.ctx, f.pool, map[string]any{"member": "Nora", "since": "2026-11-09T00:00:00-05:00"})
	if env.Data.Total != 0 || !strings.Contains(env.Summary, "Nora has no Home OS login") {
		t.Errorf("kid: %+v", env)
	}
	env = findOK(t, f.ctx, f.pool, map[string]any{})
	if env.Data.Total != 0 || !strings.HasPrefix(env.Summary, "No changes since ") || len(env.Next) == 0 {
		t.Errorf("empty feed: %+v", env)
	}
}

// TestActivityFind_SinceDateInFamilyTimezone: a YYYY-MM-DD since starts at
// local midnight.
func TestActivityFind_SinceDateInFamilyTimezone(t *testing.T) {
	f := newFamily(t)
	exec(t, f.pool, `UPDATE households SET timezone = 'America/Chicago' WHERE id = $1`, f.hh.HouseholdID)
	// 2026-11-10 05:30 UTC is 23:30 on Nov 9 in Chicago: before a Nov 10 since.
	exec(t, f.pool, `INSERT INTO activity_log (household_id, actor_user_id, entity_type, entity_id, action, summary, created_at)
		VALUES ($1, $2, 'bill', gen_random_uuid(), 'create', 'created bill "Late"', '2026-11-10T05:30:00Z'),
		       ($1, $2, 'bill', gen_random_uuid(), 'create', 'created bill "Early"', '2026-11-10T06:30:00Z')`, f.hh.HouseholdID, f.amber)
	env := findOK(t, f.ctx, f.pool, map[string]any{"since": "2026-11-10"})
	if env.Data.Total != 1 || env.Data.Changes[0].Line != `Amber created bill "Early"` || env.Data.Changes[0].When != "Tue 2026-11-10 00:30" {
		t.Errorf("since date: %+v", env.Data)
	}
	if env.Data.Since != "2026-11-10T00:00:00-06:00" {
		t.Errorf("since = %q", env.Data.Since)
	}
}

func TestWords(t *testing.T) {
	for in, want := range map[string]string{
		"list item": "list items", "person": "people", "property": "properties",
		"budget category": "budget categories", "day": "days", "bus": "buses",
	} {
		if got := pluralNoun(in); got != want {
			t.Errorf("pluralNoun(%q) = %q, want %q", in, got, want)
		}
	}
	if got := humanFields([]string{"ical_data", "starts_at", "vendor_id", "encrypted_data", "iv", "due_day", "notes"}); got != "details, time, vendor, value, 2 more" {
		t.Errorf("humanFields = %q", got)
	}
	if got := labelOf(`updated bill "Net "flix"" (amount)`); got != `Net "flix"` {
		t.Errorf("labelOf = %q", got)
	}
	if labelOf("deleted calendar event") != "" {
		t.Error("labelOf found a label in a summary without one")
	}
	// Every entity type 050 logs has words and an about_type in the enum.
	about := map[string]bool{}
	for _, a := range aboutTypes {
		about[a] = true
	}
	for et, k := range kinds {
		if !about[k.about] {
			t.Errorf("kind %s maps to about_type %q, not in the enum", et, k.about)
		}
	}
	for _, a := range aboutTypes {
		if len(entityTypes(a)) == 0 {
			t.Errorf("about_type %q covers no entity type", a)
		}
	}
}
