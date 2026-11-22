package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// --- shape helpers (pattern from internal/pet tests) ---

func toolDef(name string) mcp.Tool {
	var tool mcp.Tool
	switch name {
	case "notification_find":
		_, tool, _ = NewNotificationFindTool(nil)
	case "notification_get":
		_, tool, _ = NewNotificationGetTool(nil)
	case "notification_mark_read":
		_, tool, _ = NewNotificationMarkReadTool(nil)
	}
	return tool
}

func outputSchema(t *testing.T, name string) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(toolDef(name))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || len(raw.OutputSchema) == 0 {
		t.Fatalf("%s declares no outputSchema (%v)", name, err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw.OutputSchema, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve outputSchema: %v", err)
	}
	return rs
}

type wireError struct {
	Message string         `json:"message"`
	Fix     string         `json:"fix"`
	Field   string         `json:"field"`
	Example map[string]any `json:"example"`
}

type envelope[T any] struct {
	OK      bool       `json:"ok"`
	Summary string     `json:"summary"`
	Data    *T         `json:"data"`
	Next    []string   `json:"next"`
	Error   *wireError `json:"error"`
}

// call runs a handler and checks the respond invariants: one text block
// equal to structuredContent, valid against the advertised outputSchema
// (errors included), ok == !isError.
func call[T any](t *testing.T, ctx context.Context, name string, h toolHandler, args map[string]any) envelope[T] {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("%s returned a Go error (protocol fault): %v", name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("want 1 content item, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	sc, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structuredContent: %v", err)
	}
	if string(sc) != text.Text {
		t.Fatalf("text differs from structuredContent:\n%s\n%s", text.Text, sc)
	}
	var instance map[string]any
	if err := json.Unmarshal(sc, &instance); err != nil {
		t.Fatalf("structuredContent is not an object: %v", err)
	}
	if err := outputSchema(t, name).Validate(instance); err != nil {
		t.Fatalf("%s result does not match its outputSchema: %v\n%s", name, err, text.Text)
	}
	var env envelope[T]
	if err := json.Unmarshal(sc, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.OK == res.IsError {
		t.Fatalf("ok=%v but isError=%v", env.OK, res.IsError)
	}
	return env
}

func (e envelope[T]) data(t *testing.T) T {
	t.Helper()
	if !e.OK || e.Data == nil {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	return *e.Data
}

func wantError[T any](t *testing.T, e envelope[T], field string) {
	t.Helper()
	if e.OK || e.Error == nil {
		t.Fatalf("want isError on %q, got success: %s", field, e.Summary)
	}
	if e.Error.Field != field {
		t.Fatalf("error field = %q, want %q (%s)", e.Error.Field, field, e.Error.Message)
	}
	if e.Error.Fix == "" {
		t.Errorf("error lacks a fix: %+v", e.Error)
	}
}

func insertID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("insert: %v\n%s", err, sql)
	}
	return id.String()
}

// addMember adds a second member to the household and returns a context
// acting as them.
func addMember(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, name string) (context.Context, string) {
	t.Helper()
	email := fmt.Sprintf("%s-%s@test.homeos.local", strings.ToLower(name), uuid.NewString()[:8])
	uid := insertID(t, pool, `INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`, email, name)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'family_member')`, hh.HouseholdID, uid); err != nil {
		t.Fatalf("membership: %v", err)
	}
	claims := &auth.Claims{Identity: auth.Identity{Subject: uid, Email: email, Name: name},
		UserID: uid, HouseholdID: hh.HouseholdID.String(), Role: "family_member"}
	return server.ContextWithClaims(context.Background(), claims), uid
}

// notify inserts a notification created ago before now. userID "" makes it
// family-wide.
func notify(t *testing.T, pool *pgxpool.Pool, household uuid.UUID, userID, typ, title, entityType string, entityID any, ago time.Duration) string {
	t.Helper()
	var uid, et any
	if userID != "" {
		uid = userID
	}
	if entityType != "" {
		et = entityType
	}
	return insertID(t, pool, `INSERT INTO notifications (household_id, user_id, type, title, body, entity_type, entity_id, created_at)
		VALUES ($1, $2::uuid, $3, $4::varchar, 'body of ' || $4::varchar, $5::varchar, $6::uuid, NOW() - $7::interval) RETURNING id`,
		household, uid, typ, title, et, entityID, fmt.Sprintf("%d seconds", int(ago.Seconds())))
}

type findEnv = envelope[findResult]
type markEnv = envelope[markResult]

// --- tests ---

// TestToolDefinitions pins both tools to the catalog: names, params, all four
// hints (R for find, U for mark read), description lengths, string ids in
// the outputSchema, and unread_count always declared.
func TestToolDefinitions(t *testing.T) {
	cases := []struct {
		name, desc, title string
		params, required  []string
		readOnly          bool
	}{
		{"notification_find", "List your notifications (reminders, changes). Poll unread_only=true to watch for new ones; needs_you tells you which need your person. Always includes unread_count.",
			"Find notifications", []string{"unread_only", "type", "since", "limit", "cursor"}, nil, true},
		{"notification_get", "Get one notification in full: its whole body and the call that opens what it is about. needs_you says whether it still needs a person.",
			"Get a notification", []string{"notification_id"}, []string{"notification_id"}, true},
		{"notification_mark_read", "Mark the notifications you just told the user about read, by id. There is no mark-all.",
			"Mark notifications read", []string{"notification_ids"}, []string{"notification_ids"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := toolDef(tc.name)
			if tool.Name != tc.name {
				t.Fatalf("name = %q", tool.Name)
			}
			if tool.Description != tc.desc {
				t.Errorf("description differs from the catalog: %q", tool.Description)
			}
			if fmt.Sprint(tool.InputSchema.Required) != fmt.Sprint(tc.required) {
				t.Errorf("required = %v, want %v", tool.InputSchema.Required, tc.required)
			}
			if len(tool.InputSchema.Properties) != len(tc.params) {
				t.Errorf("got %d params, want %v", len(tool.InputSchema.Properties), tc.params)
			}
			for _, p := range tc.params {
				schema, ok := tool.InputSchema.Properties[p]
				if !ok {
					t.Errorf("missing param %q", p)
					continue
				}
				d, _ := schema.(map[string]any)["description"].(string)
				if d == "" || len(d) > 80 {
					t.Errorf("%s description is %d chars, want 1-80", p, len(d))
				}
			}
			a := tool.Annotations
			if a.Title != tc.title {
				t.Errorf("title = %q", a.Title)
			}
			if a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
				t.Fatalf("annotations not all explicit: %+v", a)
			}
			if *a.ReadOnlyHint != tc.readOnly || *a.DestructiveHint || !*a.IdempotentHint || *a.OpenWorldHint {
				t.Errorf("hints = ro %v d %v i %v ow %v", *a.ReadOnlyHint, *a.DestructiveHint, *a.IdempotentHint, *a.OpenWorldHint)
			}
			outputSchema(t, tc.name)
		})
	}

	b, _ := json.Marshal(toolDef("notification_find"))
	var raw struct {
		OutputSchema map[string]any `json:"outputSchema"`
	}
	_ = json.Unmarshal(b, &raw)
	data := raw.OutputSchema["properties"].(map[string]any)["data"].(map[string]any)
	if req := fmt.Sprint(data["required"]); !strings.Contains(req, "unread_count") {
		t.Errorf("unread_count is not required in the schema: %s", req)
	}
	item := data["properties"].(map[string]any)["notifications"].(map[string]any)["items"].(map[string]any)
	props := item["properties"].(map[string]any)
	for _, f := range []string{"id", "about_id"} {
		if s := fmt.Sprint(props[f].(map[string]any)["type"]); !strings.Contains(s, "string") {
			t.Errorf("%s schema type = %s, want string", f, s)
		}
	}
	mb, _ := json.Marshal(toolDef("notification_mark_read"))
	if !strings.Contains(string(mb), `"notification_ids":{"description"`) || !strings.Contains(string(mb), `"items":{"type":"string"}`) {
		t.Errorf("notification_ids is not an array of strings: %s", mb)
	}
}

// TestNotificationFind covers visibility (family-wide + own, never another
// member's or another family's), unread_count, unread_only, about links per
// worker kind, family-timezone times, paging and validation.
func TestNotificationFind(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	if _, err := pool.Exec(context.Background(), `UPDATE households SET timezone = 'America/New_York' WHERE id = $1`, hh.HouseholdID); err != nil {
		t.Fatal(err)
	}
	_, _, find := NewNotificationFindTool(pool)
	amberCtx, amber := addMember(t, pool, hh, "Amber")

	// Empty.
	env := call[findResult](t, ctx, "notification_find", find, map[string]any{})
	if d := env.data(t); d.UnreadCount != 0 || d.Total != 0 || len(d.Notifications) != 0 || d.Notifications == nil || env.Summary != "No notifications yet." {
		t.Fatalf("empty: %+v %s", d, env.Summary)
	}

	bill := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Electric', 142.50, 20) RETURNING id`, hh.HouseholdID)
	// A real task: notifications.entity_type is entity_kind with an owner
	// check since migration 070, so a made-up id is refused.
	task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name) VALUES ($1, 'Furnace filter') RETURNING id`, hh.HouseholdID)
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, hh.HouseholdID)
	event := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, 'dentist', '{}', 'e') RETURNING id`, cal)
	billN := notify(t, pool, hh.HouseholdID, "", "bill_due", "Electric due soon", "bill", bill, 4*time.Hour)
	maintN := notify(t, pool, hh.HouseholdID, "", "maintenance_due", "Change furnace filter due soon", "maintenance_task", task, 3*time.Hour)
	// As the worker writes them since 056: about the event, firing key in dedupe_key.
	alarmMine := notify(t, pool, hh.HouseholdID, hh.UserID.String(), "calendar_alarm", "Dentist in 30 minutes", "calendar_event", event, 2*time.Hour)
	alarmAmber := notify(t, pool, hh.HouseholdID, amber, "calendar_alarm", "Dentist in 30 minutes", "calendar_event", event, 2*time.Hour)
	if _, err := pool.Exec(context.Background(), `UPDATE notifications SET dedupe_key = 'firing-1' WHERE id IN ($1, $2)`, alarmMine, alarmAmber); err != nil {
		t.Fatal(err)
	}
	readN := notify(t, pool, hh.HouseholdID, "", "item_changed", "Welcome", "", nil, 48*time.Hour)
	if _, err := pool.Exec(context.Background(), `UPDATE notifications SET read_at = NOW() - interval '1 day' WHERE id = $1`, readN); err != nil {
		t.Fatal(err)
	}
	_, other := testutil.SeedHousehold(t, pool)
	foreign := notify(t, pool, other.HouseholdID, "", "bill_due", "Other family bill", "", nil, time.Hour)

	// Owner: family-wide + own alarm, newest first; never Amber's or the other family's.
	env = call[findResult](t, ctx, "notification_find", find, map[string]any{})
	d := env.data(t)
	ids := make([]string, len(d.Notifications))
	for i, n := range d.Notifications {
		ids[i] = n.ID
	}
	if want := []string{alarmMine, maintN, billN, readN}; fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("owner ids = %v, want %v", ids, want)
	}
	if strings.Contains(fmt.Sprint(ids), alarmAmber) || strings.Contains(fmt.Sprint(ids), foreign) {
		t.Fatal("leaked another member's or family's notification")
	}
	if d.UnreadCount != 3 || d.Total != 4 || d.Returned != 4 || d.Truncated || d.NextCursor != "" {
		t.Errorf("counts = %+v", d)
	}
	if env.Summary != "Found 4 notifications; you have 3 unread notifications." {
		t.Errorf("summary = %q", env.Summary)
	}
	alarm, maint, b, read := d.Notifications[0], d.Notifications[1], d.Notifications[2], d.Notifications[3]
	if alarm.Scope != scopePersonal || alarm.AboutType == nil || *alarm.AboutType != "event" ||
		alarm.AboutID == nil || *alarm.AboutID != event || alarm.Open == nil || *alarm.Open != "event_find id="+event {
		t.Errorf("calendar_alarm must open its event: %+v", alarm)
	}
	if maint.Scope != scopeFamily || *maint.AboutType != "maintenance" || *maint.AboutID != task || *maint.Open != "maintenance_find id="+task {
		t.Errorf("maintenance_due = %+v", maint)
	}
	if *b.AboutType != "bill" || *b.AboutID != bill || *b.Open != "bill_find id="+bill || b.Read || b.ReadAt != nil || *b.Body != "body of Electric due soon" {
		t.Errorf("bill_due = %+v", b)
	}
	if !read.Read || read.ReadAt == nil || read.AboutType != nil {
		t.Errorf("read = %+v", read)
	}
	// Times are in the family timezone, with local_date and weekday.
	ct, err := time.Parse(time.RFC3339, b.CreatedAt)
	if err != nil {
		t.Fatalf("created_at %q: %v", b.CreatedAt, err)
	}
	ny, _ := time.LoadLocation("America/New_York")
	_, wantOff := ct.In(ny).Zone()
	if _, off := ct.Zone(); off != wantOff {
		t.Errorf("created_at %q not in family offset", b.CreatedAt)
	}
	if b.LocalDate != ct.In(ny).Format(time.DateOnly) || b.Weekday != ct.In(ny).Format("Mon") {
		t.Errorf("local_date/weekday = %s %s", b.LocalDate, b.Weekday)
	}
	next := strings.Join(env.Next, " | ")
	if !strings.Contains(next, "event_find id="+event) || !strings.Contains(next, "notification_mark_read notification_ids=[\""+alarmMine+"\"]") {
		t.Errorf("next = %v", env.Next)
	}

	// Amber sees the family-wide ones and her own alarm only.
	env = call[findResult](t, amberCtx, "notification_find", find, map[string]any{"unread_only": true})
	d = env.data(t)
	if d.UnreadCount != 3 || d.Total != 3 || len(d.Notifications) != 3 || d.Notifications[0].ID != alarmAmber {
		t.Fatalf("amber = %+v", d)
	}

	// unread_only, quoted bool and the "unread" alias.
	for _, args := range []map[string]any{{"unread_only": true}, {"unread_only": "true"}, {"unread": true}} {
		d = call[findResult](t, ctx, "notification_find", find, args).data(t)
		if d.Total != 3 || len(d.Notifications) != 3 || d.UnreadCount != 3 {
			t.Errorf("%v: %+v", args, d)
		}
		for _, n := range d.Notifications {
			if n.Read {
				t.Errorf("%v returned a read notification", args)
			}
		}
	}

	// Paging.
	env = call[findResult](t, ctx, "notification_find", find, map[string]any{"limit": 3})
	d = env.data(t)
	if d.Returned != 3 || d.Total != 4 || !d.Truncated || d.NextCursor != respond.EncodeCursor(3) {
		t.Fatalf("page 1 = %+v", d)
	}
	if env.Summary != "Showing 1-3 of 4 notifications; you have 3 unread notifications." || env.Next[0] != "Pass next_cursor as cursor for the next page." {
		t.Errorf("page 1 summary/next = %q %v", env.Summary, env.Next)
	}
	d = call[findResult](t, ctx, "notification_find", find, map[string]any{"limit": "3", "cursor": d.NextCursor}).data(t)
	if d.Returned != 1 || d.Notifications[0].ID != readN || d.Truncated || d.NextCursor != "" {
		t.Errorf("page 2 = %+v", d)
	}
	env = call[findResult](t, ctx, "notification_find", find, map[string]any{"cursor": "40"})
	if d := env.data(t); d.Returned != 0 || d.Total != 4 || !strings.Contains(env.Summary, "past the end") {
		t.Errorf("past end = %+v %q", d, env.Summary)
	}

	// Validation.
	wantError(t, call[findResult](t, ctx, "notification_find", find, map[string]any{"limit": 0}), "limit")
	wantError(t, call[findResult](t, ctx, "notification_find", find, map[string]any{"limit": 101}), "limit")
	wantError(t, call[findResult](t, ctx, "notification_find", find, map[string]any{"cursor": "abc"}), "cursor")
	wantError(t, call[findResult](t, ctx, "notification_find", find, map[string]any{"unread_only": "maybe"}), "unread_only")

	// No claims.
	if e := call[findResult](t, context.Background(), "notification_find", find, nil); e.OK {
		t.Error("want isError without claims")
	}

	// UnreadCount agrees with the tool (family_overview #4385 uses it).
	if n, err := UnreadCount(context.Background(), pool, hh.HouseholdID.String(), hh.UserID.String()); err != nil || n != 3 {
		t.Errorf("UnreadCount = %d, %v", n, err)
	}
	// A token without a user UUID (local dev) sees only family-wide ones, as
	// far as the claims' role allows (#4794; role_test.go has the rest).
	dev := server.ContextWithClaims(context.Background(), &auth.Claims{UserID: "local-dev", HouseholdID: hh.HouseholdID.String(), Role: "owner"})
	if n, err := UnreadCount(dev, pool, hh.HouseholdID.String(), "local-dev"); err != nil || n != 2 {
		t.Errorf("UnreadCount(local-dev) = %d, %v", n, err)
	}
}

// TestNotificationMarkRead covers marking by id and all, idempotency,
// family-wide read for everyone, refusing another member's or family's ids
// atomically, input forms and validation.
func TestNotificationMarkRead(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, _, mark := NewNotificationMarkReadTool(pool)
	_, _, find := NewNotificationFindTool(pool)
	amberCtx, amber := addMember(t, pool, hh, "Amber")

	billN := notify(t, pool, hh.HouseholdID, "", "bill_due", "Electric due soon", "", nil, 3*time.Hour)
	maintN := notify(t, pool, hh.HouseholdID, "", "maintenance_due", "Filter due soon", "", nil, 2*time.Hour)
	mine := notify(t, pool, hh.HouseholdID, hh.UserID.String(), "calendar_alarm", "Dentist", "", nil, time.Hour)
	amberN := notify(t, pool, hh.HouseholdID, amber, "calendar_alarm", "Dentist", "", nil, time.Hour)
	_, other := testutil.SeedHousehold(t, pool)
	foreign := notify(t, pool, other.HouseholdID, "", "bill_due", "Other", "", nil, time.Hour)

	readAt := func(id string) *time.Time {
		var ts *time.Time
		if err := pool.QueryRow(context.Background(), `SELECT read_at FROM notifications WHERE id = $1`, id).Scan(&ts); err != nil {
			t.Fatal(err)
		}
		return ts
	}

	// Another member's personal notification or another family's: refused, nothing marked.
	for _, bad := range []string{amberN, foreign} {
		env := call[markResult](t, ctx, "notification_mark_read", mark, map[string]any{"notification_ids": []any{billN, bad}})
		wantError(t, env, "notification_ids")
		if !strings.Contains(env.Error.Message, bad) || readAt(billN) != nil || readAt(bad) != nil {
			t.Fatalf("refusal = %+v; billN read %v", env.Error, readAt(billN))
		}
	}

	// By id (string form with a duplicate): marks one family-wide notification.
	env := call[markResult](t, ctx, "notification_mark_read", mark, map[string]any{"notification_ids": billN + ", " + billN})
	d := env.data(t)
	if d.Marked != 1 || d.FamilyWide != 1 || d.AlreadyRead != 0 || d.UnreadCount != 2 || fmt.Sprint(d.NotificationIDs) != "["+billN+"]" {
		t.Fatalf("mark one = %+v", d)
	}
	if env.Summary != "Marked 1 notification read; 2 unread notifications left. Family-wide ones are now read for everyone in the family." {
		t.Errorf("summary = %q", env.Summary)
	}
	first := readAt(billN)
	if first == nil {
		t.Fatal("bill notification not marked")
	}
	// Family-wide: Amber sees it read too.
	if d := call[findResult](t, amberCtx, "notification_find", find, map[string]any{}).data(t); d.UnreadCount != 2 {
		t.Errorf("amber unread = %d, want 2 (maintN + her alarm)", d.UnreadCount)
	}

	// Repeat: idempotent, read_at unchanged.
	env = call[markResult](t, ctx, "notification_mark_read", mark, map[string]any{"notification_id": billN})
	d = env.data(t)
	if d.Marked != 0 || d.AlreadyRead != 1 || d.UnreadCount != 2 || len(d.NotificationIDs) != 0 || d.NotificationIDs == nil {
		t.Fatalf("repeat = %+v", d)
	}
	if env.Summary != "That notification was already read; nothing changed. 2 unread notifications left." || !readAt(billN).Equal(*first) {
		t.Errorf("repeat summary %q / read_at moved", env.Summary)
	}

	// The rest by id, in one call: the two unread ones the caller can see.
	env = call[markResult](t, ctx, "notification_mark_read", mark, map[string]any{"notification_ids": []any{maintN, mine}})
	d = env.data(t)
	if d.Marked != 2 || d.FamilyWide != 1 || d.UnreadCount != 0 || readAt(maintN) == nil || readAt(mine) == nil {
		t.Fatalf("mark the rest = %+v", d)
	}
	if readAt(amberN) != nil || readAt(foreign) != nil {
		t.Fatal("marking ids touched another member's or family's notification")
	}
	if len(env.Next) != 1 || !strings.Contains(env.Next[0], "agenda_get") {
		t.Errorf("next = %v", env.Next)
	}
	// Repeating the same ids changes nothing more.
	env = call[markResult](t, ctx, "notification_mark_read", mark, map[string]any{"notification_ids": []any{maintN, mine}})
	if d := env.data(t); d.Marked != 0 || d.AlreadyRead != 2 || !strings.Contains(env.Summary, "Those notifications were already read") {
		t.Errorf("repeat = %+v %q", d, env.Summary)
	}

	// Validation.
	wantError(t, call[markResult](t, ctx, "notification_mark_read", mark, map[string]any{}), "notification_ids")
	wantError(t, call[markResult](t, ctx, "notification_mark_read", mark, map[string]any{"notification_ids": []any{}}), "notification_ids")
	wantError(t, call[markResult](t, ctx, "notification_mark_read", mark, map[string]any{"notification_ids": []any{"nope"}}), "notification_ids")
	wantError(t, call[markResult](t, ctx, "notification_mark_read", mark, map[string]any{"notification_ids": []any{7}}), "notification_ids")
	many := make([]any, maxMarkIDs+1)
	for i := range many {
		many[i] = uuid.NewString()
	}
	wantError(t, call[markResult](t, ctx, "notification_mark_read", mark, map[string]any{"notification_ids": many}), "notification_ids")
	if e := call[markResult](t, context.Background(), "notification_mark_read", mark, map[string]any{"notification_ids": []any{billN}}); e.OK {
		t.Error("want isError without claims")
	}
}

// TestMarkReadHasNoMarkAll is #4947: the blanket form is gone, and asking for
// it does not silently mark anything. all:true was how a war-game agent
// "tidying up" consumed a whole household's unread state, Amber's bill-change
// notice included, with no evidence it had seen any of it.
func TestMarkReadHasNoMarkAll(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, _, mark := NewNotificationMarkReadTool(pool)
	amberCtx, amber := addMember(t, pool, hh, "Amber")

	// A family-wide bill notice and a personal one: the shapes G6 wiped.
	billN := notify(t, pool, hh.HouseholdID, "", "bill_due", "Internet due soon", "", nil, 3*time.Hour)
	mine := notify(t, pool, hh.HouseholdID, hh.UserID.String(), "calendar_alarm", "Dentist", "", nil, time.Hour)
	amberN := notify(t, pool, hh.HouseholdID, amber, "item_changed", `Jacob paid bill "Internet"`, "", nil, 2*time.Hour)
	readAt := func(id string) *time.Time {
		var ts *time.Time
		if err := pool.QueryRow(context.Background(), `SELECT read_at FROM notifications WHERE id = $1`, id).Scan(&ts); err != nil {
			t.Fatal(err)
		}
		return ts
	}

	// The exact call from the war game: all:true with a null id list. It used
	// to succeed silently; now it is an isError that names the field and says
	// what to do instead, and nothing is marked.
	env := call[markResult](t, ctx, "notification_mark_read", mark, map[string]any{"all": true, "notification_ids": nil})
	wantError(t, env, "all")
	for _, w := range []string{"no mark-all", "not allowed"} {
		if !strings.Contains(env.Error.Message, w) {
			t.Errorf("error message %q lacks %q", env.Error.Message, w)
		}
	}
	if !strings.Contains(env.Error.Fix, "notification_ids") || !strings.Contains(env.Error.Fix, "notification_find") {
		t.Errorf("fix does not name the right argument: %q", env.Error.Fix)
	}
	if readAt(billN) != nil || readAt(mine) != nil || readAt(amberN) != nil {
		t.Fatal("all:true marked something read")
	}
	// The truthy spellings a small model reaches for are refused the same way.
	for _, args := range []map[string]any{
		{"all": true}, {"all": "true"}, {"all": "yes"}, {"all": 1},
		{"all": true, "notification_ids": []any{billN}},
		{"mark_all": true}, {"all_unread": true},
	} {
		env := call[markResult](t, ctx, "notification_mark_read", mark, args)
		if env.OK || env.Error == nil || !slices.Contains([]string{"all", "mark_all", "all_unread"}, env.Error.Field) {
			t.Errorf("%v: want isError on the blanket parameter, got ok=%v error=%+v", args, env.OK, env.Error)
		}
		if readAt(billN) != nil || readAt(amberN) != nil {
			t.Fatalf("%v marked something read", args)
		}
	}
	// all:false alone is not a blanket request; it is simply no ids.
	wantError(t, call[markResult](t, ctx, "notification_mark_read", mark, map[string]any{"all": false}), "notification_ids")

	// The tool advertises ids only, and requires them, so a model that reads
	// tools/list cannot reach the blanket form by accident.
	tool := toolDef("notification_mark_read")
	if _, ok := tool.InputSchema.Properties["all"]; ok {
		t.Error("the all parameter is still advertised")
	}
	if fmt.Sprint(tool.InputSchema.Required) != "[notification_ids]" {
		t.Errorf("required = %v, want [notification_ids]", tool.InputSchema.Required)
	}

	// Amber's own row is untouched by Jacob's refusal, and she can still mark
	// her own notification read, which is the one read state that is hers.
	for _, id := range []string{billN, mine, amberN} {
		if readAt(id) != nil {
			t.Fatalf("%s was marked read", id)
		}
	}
	if d := call[markResult](t, amberCtx, "notification_mark_read", mark, map[string]any{"notification_ids": []any{amberN}}).data(t); d.Marked != 1 {
		t.Fatalf("amber could not mark her own notification read: %+v", d)
	}

	// A member cannot mark another member's personal notification read, with
	// or without a blanket flag: it is not found for them, nothing is marked,
	// and Amber's notice stays unread for Amber.
	amberN2 := notify(t, pool, hh.HouseholdID, amber, "item_changed", `Jacob updated bill "Electric"`, "", nil, time.Minute)
	env = call[markResult](t, ctx, "notification_mark_read", mark, map[string]any{"notification_ids": []any{billN, amberN2}})
	wantError(t, env, "notification_ids")
	if !strings.Contains(env.Error.Message, amberN2) {
		t.Errorf("refusal does not name Amber's id: %q", env.Error.Message)
	}
	if readAt(billN) != nil || readAt(amberN2) != nil {
		t.Fatal("a refused call marked something read")
	}
	// Nor can she mark Jacob's.
	mine2 := notify(t, pool, hh.HouseholdID, hh.UserID.String(), "item_changed", `Amber updated bill "Internet"`, "", nil, time.Minute)
	wantError(t, call[markResult](t, amberCtx, "notification_mark_read", mark, map[string]any{"notification_ids": []any{mine2}}), "notification_ids")
	if readAt(mine2) != nil {
		t.Fatal("amber marked jacob's notification read")
	}
}
