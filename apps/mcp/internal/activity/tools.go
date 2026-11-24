// Package activity provides the MCP change-feed tool (catalog v2, KB
// architecture/mcp-tool-catalog.md "activity"): activity_find answers "what
// changed since ..., and who did it" ("what did Amber add this week?") from
// the activity_log rows that migration 050's triggers write for every
// create, update and delete of a family record, plus credential_reveal's
// 'read' rows (057).
//
// It mirrors the API feed (KB apis/activity-feed.md, #4505): newest first by
// the bigint id, actor named from people.display_name in this family, else
// users.name (never an email), agent named from agent_tokens.name only
// (never its prefix or hash), "Someone" for an unknown actor that used an
// agent and "System" for no actor at all. The log holds no record values: a
// summary carries a label (a title or name) and changed_fields carries column
// names only, so credential rows show nothing but the credential's name.
//
// Roles (KB decisions/2026-11-15-role-policy.md, #4689), re-read from
// memberships on every call: owner, family_manager and family_member see
// everything; a viewer sees everything except credentials, which drops every
// row about a secret ("revealed secret" lines included) and every row that
// sits inside one (a note or document on a secret); housesitter, vendor and
// any unknown role get no activity feed.
package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// Pagination limits shared by every catalog find tool.
const (
	defaultLimit = 25
	maxLimit     = 100
	// defaultSince is how far back activity_find looks without since.
	defaultSince = 7 * 24 * time.Hour
	// maxPeople caps by_person; the rest are folded into one "others" row.
	maxPeople = 8
	// maxFields caps the changed fields shown on one line.
	maxFields = 4
	// cursorPrefix marks an activity_find cursor (keyset: the last id seen).
	cursorPrefix = "act:"
)

// toolHandler is the signature every activity tool handler has.
type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// fullRoles see the whole feed; viewerRoles see it without credentials.
// Everyone else (housesitter, vendor, unknown roles) gets no feed, so a new
// role fails closed. Keep in step with apps/api/internal/auth/roles.go.
var (
	fullRoles   = map[string]bool{"owner": true, "family_manager": true, "family_member": true}
	viewerRoles = map[string]bool{"viewer": true}
)

// kind is how one activity_log.entity_type reads in output.
type kind struct {
	word  string // in a line: "list item"
	about string // the about_type it belongs to: "list"
}

// kinds maps every entity type 050/057 log to words. Types not listed (a
// kind added later) read as their raw name with about_type equal to it.
var kinds = map[string]kind{
	"household":            {"family", "family"},
	"property":             {"property", "property"},
	"room":                 {"room", "room"},
	"asset":                {"asset", "asset"},
	"vehicle":              {"vehicle", "vehicle"},
	"pet":                  {"pet", "pet"},
	"vendor":               {"contact", "contact"},
	"bill":                 {"bill", "bill"},
	"loan":                 {"loan", "loan"},
	"maintenance_task":     {"maintenance task", "maintenance"},
	"maintenance_schedule": {"maintenance schedule", "maintenance"},
	"calendar":             {"calendar", "event"},
	"calendar_event":       {"event", "event"},
	"note":                 {"note", "note"},
	"file":                 {"document", "document"},
	"document_link":        {"document link", "document"},
	"secret":               {"credential", "credential"},
	"person":               {"person", "person"},
	"person_preference":    {"preference", "person"},
	"budget":               {"budget category", "budget"},
	"list":                 {"list", "list"},
	"list_item":            {"list item", "list"},
	"recipe":               {"recipe", "recipe"},
	"meal_plan":            {"meal plan", "meal"},
	"meal_feedback":        {"meal rating", "meal"},
	"guide":                {"guide", "guide"},
}

// aboutTypes is the about_type enum: the catalog's about words plus the
// record kinds the feed also covers.
var aboutTypes = []string{
	"family", "person", "property", "room", "asset", "vehicle", "pet", "contact",
	"bill", "loan", "maintenance", "event", "list", "recipe", "guide",
	"note", "document", "credential", "budget", "meal",
}

// entityTypes returns the activity_log entity types an about_type covers.
func entityTypes(about string) []string {
	var out []string
	for et, k := range kinds {
		if k.about == about {
			out = append(out, et)
		}
	}
	sort.Strings(out)
	return out
}

// actions maps the action enum (and words small models use) to the DB value.
var actions = map[string]string{
	"created": "create", "create": "create", "added": "create", "add": "create", "new": "create",
	"updated": "update", "update": "update", "changed": "update", "edited": "update",
	"deleted": "delete", "delete": "delete", "removed": "delete",
}

// pastTense is how each DB action reads in output.
var pastTense = map[string]string{"create": "created", "update": "updated", "delete": "deleted", "read": "revealed"}

// actionOrder sorts actions in summaries.
var actionOrder = map[string]int{"create": 0, "update": 1, "delete": 2, "read": 3}

// findAliases are parameter names small models reach for, accepted but not
// advertised.
var findAliases = map[string]string{
	"member": "person_id", "person": "person_id", "who": "person_id", "user_id": "person_id",
	"entity_type": "about_type", "type": "about_type", "from": "since", "after": "since",
}

// findExample is a valid activity_find call for error examples.
var findExample = map[string]any{"person_id": "Amber", "since": "2026-11-09", "action": "created"}

// change is one activity row in output, kept small for small models.
type change struct {
	When   string `json:"when" jsonschema_description:"When, in the family timezone, e.g. Thu 2026-11-12 19:30."`
	Who    string `json:"who" jsonschema_description:"Who did it: a family member's name, Someone (unknown, via an agent) or System."`
	Action string `json:"action" jsonschema_description:"created, updated, deleted or revealed (a credential read by an agent)."`
	What   string `json:"what" jsonschema_description:"The kind of record, e.g. event, list item, bill."`
	Line   string `json:"line" jsonschema_description:"Ready-to-say line, e.g. Amber created event \"Dentist\" via agent \"Claude\"."`
	// Changes is the before/after VALUE of the columns migration 072 records
	// them for (money, dates and a bill's source document): {"amount":
	// {"from": 5, "to": 35}}. Absent for every other change, so most updates
	// still carry no record values.
	Changes map[string]valueChange `json:"changes,omitempty" jsonschema_description:"Before/after values of an update, for money and date fields only, e.g. amount: {from: 5, to: 35}."`
	Open    string                 `json:"open,omitempty" jsonschema_description:"The tool call that reads the record; absent when it is deleted or has no tool."`
}

// valueChange is one column's before and after value.
type valueChange struct {
	From any `json:"from" jsonschema_description:"The value before the change; null when it was unset."`
	To   any `json:"to" jsonschema_description:"The value after the change; null when it was cleared."`
}

// personChanges is one person's changes in the whole match, not only this page.
type personChanges struct {
	Who     string `json:"who"`
	Changes int    `json:"changes"`
	Detail  string `json:"detail" jsonschema_description:"What they did, e.g. created 3 list items, 2 events; updated 1 bill."`
}

// findResult is activity_find's data.
type findResult struct {
	Since      string          `json:"since" jsonschema_description:"The start of the window, ISO 8601 in the family timezone."`
	Total      int             `json:"total" jsonschema_description:"How many changes match in all."`
	Returned   int             `json:"returned" jsonschema_description:"How many are in changes."`
	Truncated  bool            `json:"truncated" jsonschema_description:"true when older matches remain; pass next_cursor as cursor."`
	NextCursor string          `json:"next_cursor,omitempty" jsonschema_description:"Pass as cursor, with the same filters, for older changes."`
	ByPerson   []personChanges `json:"by_person" jsonschema_description:"Every match grouped by who, most active first."`
	Changes    []change        `json:"changes" jsonschema_description:"Newest first."`
}

// findInput is the validated activity_find arguments.
type findInput struct {
	since     *time.Time // nil: defaultSince before now
	sinceDate *string    // a YYYY-MM-DD since, read in the family timezone
	person    *string
	about     *string
	action    *string
	limit     int
	before    int64 // keyset cursor: id < before
}

// NewActivityFindTool creates the activity_find MCP tool (catalog v2,
// class R).
func NewActivityFindTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription(`What changed since a time, newest first, with who did it: "what did Amber add this week?". Covers events, bills, notes, lists and more.`),
		mcp.WithString("since", mcp.Description("Start: YYYY-MM-DD or ISO 8601 with offset (default 7 days ago)")),
		mcp.WithString("person_id", mcp.Description("Only changes this member made: id or exact name, e.g. Amber")),
		mcp.WithString("about_type", mcp.Description("Only this kind of record, e.g. event, list, bill"), mcp.Enum(aboutTypes...)),
		mcp.WithString("action", mcp.Description("Only created, updated or deleted"), mcp.Enum("created", "updated", "deleted")),
		mcp.WithNumber("limit", mcp.Description("Max changes to return, 1-100 (default 25)"), mcp.Min(1), mcp.Max(maxLimit)),
		mcp.WithString("cursor", mcp.Description("next_cursor from the previous result, for older changes")),
		respond.Schema[findResult](),
	}
	tool := mcp.NewTool("activity_find", append(opts,
		mcp.WithTitleAnnotation("Find recent changes"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}
		in, err := parseFindInput(req.GetArguments())
		if err != nil {
			return invalid(err), nil
		}
		hid, err := uuid.Parse(claims.HouseholdID)
		if err != nil {
			return notSignedIn(), nil
		}
		role, fail := callerRole(ctx, pool, hid, claims)
		if fail != nil {
			return fail, nil
		}
		var hidden []string
		switch {
		case fullRoles[role], viewerRoles[role]:
			// What this role may not see, from the ONE policy table, not a
			// hand-written list. The list was ["secret"] and stayed that way
			// when 086 (#5518) added the membership audit -- which a viewer and
			// a family_member must not read, because a demotion is not family
			// news. Deriving it means a kind added to the policy is hidden
			// here without anyone remembering this line.
			hidden = auth.HiddenEntityKinds(role)
		default:
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("The %s role can't see the family's change history.", roleLabel(role)),
				Fix:     "Tell the user only family members and viewers can see what changed; retrying will not help.",
			}), nil
		}
		return find(ctx, pool, hid, in, hidden)
	}
	return "activity_find", tool, handler
}

// filter is the WHERE clause every activity_find query shares.
type filter struct {
	where []string
	args  []any
}

func (f *filter) add(cond string, v any) {
	f.args = append(f.args, v)
	f.where = append(f.where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(f.args))))
}

func (f *filter) sql() string { return strings.Join(f.where, " AND ") }

// actorJoins resolve the actor's and agent's names for a row a.
const actorJoins = `
LEFT JOIN users u ON u.id = a.actor_user_id
LEFT JOIN people p ON p.household_id = a.household_id AND p.user_id = a.actor_user_id`

// whoSQL names the actor the way the API feed does.
const whoSQL = `CASE WHEN a.actor_user_id IS NOT NULL THEN COALESCE(NULLIF(btrim(p.display_name), ''), NULLIF(btrim(u.name), ''), 'Someone')
	WHEN a.actor_agent_token_id IS NOT NULL THEN 'Someone' ELSE 'System' END`

// find runs the group and page queries in one read-only snapshot, so
// by_person, total and the page agree.
func find(ctx context.Context, pool *pgxpool.Pool, hid uuid.UUID, in findInput, hidden []string) (*mcp.CallToolResult, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return respond.Failed("find changes", err), nil
	}
	defer func() { _ = tx.Rollback(ctx) }()

	loc, err := loadLocation(ctx, tx, hid)
	if err != nil {
		return respond.Failed("find changes", err), nil
	}
	since := time.Now().Add(-defaultSince)
	switch {
	case in.since != nil:
		since = *in.since
	case in.sinceDate != nil:
		d, _ := time.ParseInLocation(time.DateOnly, *in.sinceDate, loc)
		since = d
	}

	f := &filter{}
	f.add("a.household_id = ?", hid)
	f.add("a.created_at >= ?", since)

	var personName string
	if in.person != nil {
		name, userID, fail, err := resolvePerson(ctx, tx, hid, *in.person)
		if err != nil {
			return respond.Failed("find changes", err), nil
		}
		if fail != nil {
			return fail, nil
		}
		personName = name
		if userID == nil {
			// Kids and other people without a login never act themselves.
			res := findResult{Since: since.In(loc).Format(time.RFC3339), ByPerson: []personChanges{}, Changes: []change{}}
			return respond.OK(fmt.Sprintf("%s has no Home OS login, so %s made no changes.", name, name), res,
				fmt.Sprintf("To see changes to %s's own record, call activity_find with about_type=person.", name)), nil
		}
		f.add("a.actor_user_id = ?", *userID)
	}
	if in.about != nil {
		f.add("a.entity_type = ANY(?)", entityTypes(*in.about))
	}
	if in.action != nil {
		f.add("a.action = ?", *in.action)
	}
	if len(hidden) > 0 {
		f.add("a.entity_type <> ALL(?)", hidden)
		// A record inside a hidden one (a note on a secret) is hidden too.
		f.add("(a.parent_type IS NULL OR a.parent_type <> ALL(?))", hidden)
	}

	people, total, err := groups(ctx, tx, f)
	if err != nil {
		return respond.Failed("find changes", err), nil
	}
	page, more, err := pageRows(ctx, tx, f, in, loc)
	if err != nil {
		return respond.Failed("find changes", err), nil
	}
	if err := tx.Commit(ctx); err != nil {
		return respond.Failed("find changes", err), nil
	}

	res := findResult{
		Since:    since.In(loc).Format(time.RFC3339),
		Total:    total,
		Returned: len(page),
		ByPerson: people,
		Changes:  make([]change, len(page)),
	}
	for i, r := range page {
		res.Changes[i] = r.change
	}
	if more && len(page) > 0 {
		res.Truncated = true
		res.NextCursor = cursorPrefix + strconv.FormatInt(page[len(page)-1].id, 10)
	}
	local := since.In(loc)
	return respond.OK(findSummary(in, res, personName, local), res, findNext(in, res, personName, local)...), nil
}

// groupRow is one (who, entity type, action) count.
type groupRow struct {
	key, who, entityType, action string
	n                            int
}

// groups counts every match by who, kind and action (cursor ignored, so the
// grouping covers the whole window) and folds them into one line per person.
func groups(ctx context.Context, tx pgx.Tx, f *filter) ([]personChanges, int, error) {
	q := `SELECT COALESCE(a.actor_user_id::text, CASE WHEN a.actor_agent_token_id IS NOT NULL THEN 'agent' ELSE 'system' END),
	             ` + whoSQL + `, a.entity_type, a.action, count(*)
	      FROM activity_log a` + actorJoins + `
	      WHERE ` + f.sql() + `
	      GROUP BY 1, 2, 3, 4`
	rows, err := tx.Query(ctx, q, f.args...)
	if err != nil {
		return nil, 0, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (groupRow, error) {
		var g groupRow
		err := r.Scan(&g.key, &g.who, &g.entityType, &g.action, &g.n)
		return g, err
	})
	if err != nil {
		return nil, 0, err
	}
	return foldGroups(all)
}

// foldGroups turns group rows into by_person, most active first, capped at
// maxPeople (the rest become one "N others" row), and the total.
func foldGroups(all []groupRow) ([]personChanges, int, error) {
	type person struct {
		who   string
		n     int
		parts []groupRow
	}
	byKey := map[string]*person{}
	var order []*person
	total := 0
	for _, g := range all {
		p := byKey[g.key]
		if p == nil {
			p = &person{who: g.who}
			byKey[g.key] = p
			order = append(order, p)
		}
		p.n += g.n
		p.parts = append(p.parts, g)
		total += g.n
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].n != order[j].n {
			return order[i].n > order[j].n
		}
		return order[i].who < order[j].who
	})
	out := []personChanges{}
	others := personChanges{}
	othersPeople := 0
	for i, p := range order {
		if i >= maxPeople {
			others.Changes += p.n
			othersPeople++
			continue
		}
		out = append(out, personChanges{Who: p.who, Changes: p.n, Detail: detail(p.parts)})
	}
	if othersPeople > 0 {
		others.Who = plural(othersPeople, "other person")
		others.Detail = "narrow with person_id to see theirs"
		out = append(out, others)
	}
	return out, total, nil
}

// detail is "created 3 list items, 2 events; updated 1 bill".
func detail(parts []groupRow) string {
	byAction := map[string][]groupRow{}
	for _, g := range parts {
		byAction[g.action] = append(byAction[g.action], g)
	}
	acts := make([]string, 0, len(byAction))
	for a := range byAction {
		acts = append(acts, a)
	}
	sort.Slice(acts, func(i, j int) bool { return actionRank(acts[i]) < actionRank(acts[j]) })
	var out []string
	for _, a := range acts {
		gs := byAction[a]
		sort.Slice(gs, func(i, j int) bool {
			if gs[i].n != gs[j].n {
				return gs[i].n > gs[j].n
			}
			return wordFor(gs[i].entityType) < wordFor(gs[j].entityType)
		})
		var counts []string
		for _, g := range gs {
			counts = append(counts, plural(g.n, wordFor(g.entityType)))
		}
		out = append(out, verbFor(a)+" "+strings.Join(counts, ", "))
	}
	return strings.Join(out, "; ")
}

// pageEntry is a change plus the id the cursor is built from.
type pageEntry struct {
	change
	id int64
}

// pageRows reads one page, newest first, LIMIT limit+1 to see whether more
// remain. Events whose summary carries no name (rows logged before 055) are
// named from the live event through 055's calendar_event_label().
func pageRows(ctx context.Context, tx pgx.Tx, f *filter, in findInput, loc *time.Location) ([]pageEntry, bool, error) {
	pf := &filter{where: append([]string{}, f.where...), args: append([]any{}, f.args...)}
	if in.before > 0 {
		pf.add("a.id < ?", in.before)
	}
	pf.args = append(pf.args, in.limit+1)
	q := `SELECT a.id, a.created_at, a.action, a.entity_type, a.entity_id, a.parent_type, a.parent_id,
	             a.changed_fields, a.summary, a.changes, ` + whoSQL + `, t.name, ev.label
	      FROM activity_log a` + actorJoins + `
	      LEFT JOIN agent_tokens t ON t.id = a.actor_agent_token_id
	      LEFT JOIN LATERAL (
	          SELECT calendar_event_label(co.ical_data, co.ical_raw) AS label
	          FROM calendar_objects co
	          JOIN calendars c ON c.id = co.calendar_id AND c.household_id = a.household_id
	          WHERE a.entity_type = 'calendar_event' AND a.summary NOT LIKE '%"%' AND co.id = a.entity_id
	      ) ev ON true
	      WHERE ` + pf.sql() + `
	      ORDER BY a.id DESC
	      LIMIT $` + strconv.Itoa(len(pf.args))
	rows, err := tx.Query(ctx, q, pf.args...)
	if err != nil {
		return nil, false, err
	}
	page, err := pgx.CollectRows(rows, func(cr pgx.CollectableRow) (pageEntry, error) {
		var r logRow
		err := cr.Scan(&r.id, &r.createdAt, &r.action, &r.entityType, &r.entityID, &r.parentType, &r.parentID,
			&r.changedFields, &r.summary, &r.changes, &r.who, &r.agent, &r.eventLabel)
		if err != nil {
			return pageEntry{}, err
		}
		return pageEntry{change: r.change(loc), id: r.id}, nil
	})
	if err != nil {
		return nil, false, err
	}
	if len(page) > in.limit {
		return page[:in.limit], true, nil
	}
	return page, false, nil
}

// logRow is one activity_log row as scanned.
type logRow struct {
	id            int64
	createdAt     time.Time
	action        string
	entityType    string
	entityID      uuid.UUID
	parentType    *string
	parentID      *uuid.UUID
	changedFields []string
	summary       string
	// changes is activity_log.changes as JSON (migration 072), raw until the
	// row is rendered.
	changes    []byte
	who        string
	agent      *string
	eventLabel *string
}

// change builds the output line: `Amber updated bill "Netflix" (amount) via
// agent "Claude"`.
func (r logRow) change(loc *time.Location) change {
	what := wordFor(r.entityType)
	label := labelOf(r.summary)
	if label == "" && r.eventLabel != nil {
		label = strings.ReplaceAll(*r.eventLabel, `"`, "'")
	}
	var b strings.Builder
	b.WriteString(r.who + " " + verbFor(r.action) + " " + what)
	if label != "" {
		b.WriteString(` "` + label + `"`)
	}
	changes := r.values()
	if r.action == "update" {
		// One parenthetical, the field names with the values recorded for
		// them: `(amount $5.00 -> $35.00)` rather than `(amount) (amount ...)`.
		if fields := humanFields(r.changedFields); fields != "" {
			b.WriteString(" (" + fields)
			if s := changesText(changes); s != "" {
				b.WriteString("; " + s)
			}
			b.WriteString(")")
		}
	}
	if r.agent != nil && strings.TrimSpace(*r.agent) != "" {
		b.WriteString(` via agent "` + strings.TrimSpace(*r.agent) + `"`)
	}
	c := change{
		When:    r.createdAt.In(loc).Format("Mon 2006-01-02 15:04"),
		Who:     r.who,
		Action:  verbFor(r.action),
		What:    what,
		Changes: changes,
		Line:    b.String(),
	}
	if r.action != "delete" {
		c.Open = openCall(r.entityType, r.entityID, r.parentType, r.parentID)
	}
	return c
}

// values decodes activity_log.changes (migration 072) into the before/after
// map the output carries, or nil when the row has none. The JSON is small and
// written by the allowlist in activity_change_values(), so a row that does not
// decode is treated as having none rather than failing the feed.
func (r logRow) values() map[string]valueChange {
	if len(r.changes) == 0 {
		return nil
	}
	var raw map[string]struct {
		From any `json:"from"`
		To   any `json:"to"`
	}
	if err := json.Unmarshal(r.changes, &raw); err != nil || len(raw) == 0 {
		return nil
	}
	out := make(map[string]valueChange, len(raw))
	for k, v := range raw {
		out[k] = valueChange{From: v.From, To: v.To}
	}
	return out
}

// changesText lists the before/after values an update recorded, as
// "amount $5.00 -> $35.00, due date 2026-11-10 -> 2026-12-10", or "" when the
// row records none. Money, dates and a bill's source document are the only
// columns that reach it, so the formatting is narrow on purpose.
func changesText(changes map[string]valueChange) string {
	if len(changes) == 0 {
		return ""
	}
	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		v := changes[k]
		parts = append(parts, valueWord(k)+" "+valueText(k, v.From)+" -> "+valueText(k, v.To))
	}
	return strings.Join(parts, ", ")
}

// valueWord is a column's name in a change line: amount, due date, ...
func valueWord(column string) string {
	return strings.ReplaceAll(strings.TrimSuffix(column, "_id"), "_", " ")
}

// valueText formats one value of a change: money to the cent for the money
// columns, a date as written, and an id as the id it is.
func valueText(column string, v any) string {
	if v == nil {
		return "nothing"
	}
	switch column {
	case "amount", "cost", "monthly_amount", "monthly_payment", "remaining_balance":
		switch n := v.(type) {
		case float64:
			return fmt.Sprintf("$%.2f", n)
		case string:
			return "$" + n
		}
	case "interest_rate":
		if n, ok := v.(float64); ok {
			return strconv.FormatFloat(n, 'f', -1, 64) + "%"
		}
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return string(b)
}

// openCall is the tool call that reads a record, or "" when no registered
// tool reads it by id.
func openCall(entityType string, id uuid.UUID, parentType *string, parentID *uuid.UUID) string {
	parent := func(want string) *uuid.UUID {
		if parentType != nil && *parentType == want && parentID != nil {
			return parentID
		}
		return nil
	}
	switch entityType {
	case "calendar_event":
		return "event_find id=" + id.String()
	case "calendar":
		return "calendar_find"
	case "bill":
		return "bill_find id=" + id.String()
	case "loan":
		return "loan_find id=" + id.String()
	case "maintenance_task":
		return "maintenance_find id=" + id.String()
	case "asset":
		return "asset_find id=" + id.String()
	case "vehicle":
		return "vehicle_find id=" + id.String()
	case "pet":
		return "pet_find id=" + id.String()
	case "property":
		return "property_find id=" + id.String()
	case "room":
		if p := parent("property"); p != nil {
			return "property_find id=" + p.String()
		}
	case "vendor":
		return "contact_find id=" + id.String()
	case "note":
		return "note_get note_id=" + id.String()
	case "file":
		return "document_get document_id=" + id.String()
	case "secret":
		return "credential_find id=" + id.String()
	case "person":
		return "person_get person_id=" + id.String()
	case "person_preference":
		if p := parent("person"); p != nil {
			return "person_get person_id=" + p.String()
		}
	case "list":
		return "list_get list_id=" + id.String()
	case "list_item":
		if p := parent("list"); p != nil {
			return "list_get list_id=" + p.String()
		}
	case "household":
		return "family_overview"
	}
	return ""
}

// labelOf returns the quoted label in a 050 summary (`updated bill "Netflix"
// (amount)` -> Netflix), or "" when it has none.
func labelOf(summary string) string {
	i := strings.Index(summary, `"`)
	j := strings.LastIndex(summary, `"`)
	if i < 0 || j <= i {
		return ""
	}
	return summary[i+1 : j]
}

// humanFields turns changed column names into words, without duplicates and
// at most maxFields: ical_data -> details, encrypted_data -> value,
// vendor_id -> vendor. Only names, never values, are ever logged.
func humanFields(cols []string) string {
	rename := map[string]string{
		"ical_data": "details", "ical_raw": "details", "starts_at": "time", "ends_at": "time",
		"encrypted_data": "value", "iv": "value", "agent_ciphertext": "agent sharing",
		"agent_shared_at": "agent sharing", "agent_shared_by": "agent sharing",
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range cols {
		w, ok := rename[c]
		if !ok {
			w = strings.ReplaceAll(strings.TrimSuffix(c, "_id"), "_", " ")
		}
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	if len(out) > maxFields {
		out = append(out[:maxFields], fmt.Sprintf("%d more", len(out)-maxFields))
	}
	return strings.Join(out, ", ")
}

// findSummary is activity_find's one-line summary.
func findSummary(in findInput, res findResult, personName string, since time.Time) string {
	window := "since " + since.Format("Mon Jan 2")
	what := "changes"
	if in.action != nil {
		what = pastTense[*in.action] + " records"
	}
	if in.about != nil {
		what += " (" + *in.about + ")"
	}
	switch {
	case res.Total == 0 && personName != "":
		return fmt.Sprintf("%s made no %s %s.", personName, what, window)
	case res.Total == 0:
		return fmt.Sprintf("No %s %s.", what, window)
	case personName != "" && len(res.ByPerson) > 0:
		s := fmt.Sprintf("%s made %s %s: %s.", personName, plural(res.Total, "change"), window, res.ByPerson[0].Detail)
		if res.Truncated {
			s += fmt.Sprintf(" Showing the newest %d.", res.Returned)
		}
		return s
	}
	var who []string
	for _, p := range res.ByPerson {
		who = append(who, fmt.Sprintf("%s %d", p.Who, p.Changes))
	}
	s := fmt.Sprintf("%s %s by %s (%s).", plural(res.Total, "change"), window,
		plural(len(res.ByPerson), "person"), strings.Join(who, ", "))
	if res.Total == 1 {
		s = fmt.Sprintf("1 change %s: %s.", window, res.Changes[0].Line)
	}
	if res.Truncated || in.before > 0 {
		s += fmt.Sprintf(" Showing %d.", res.Returned)
	}
	return s
}

// findNext is activity_find's next-step hints (at most three).
func findNext(in findInput, res findResult, personName string, since time.Time) []string {
	var next []string
	if res.NextCursor != "" {
		next = append(next, "Pass next_cursor as cursor for older changes.")
	}
	for _, p := range res.Changes {
		if p.Open != "" {
			next = append(next, fmt.Sprintf("Read the newest record with %s.", p.Open))
			break
		}
	}
	if res.Total == 0 {
		earlier := since.AddDate(0, 0, -30).Format(time.DateOnly)
		next = append(next, fmt.Sprintf("Look further back with since=%q.", earlier))
		if personName != "" || in.about != nil || in.action != nil {
			next = append(next, "Drop person_id, about_type or action to see every change.")
		}
	} else if personName == "" && len(res.ByPerson) > 1 {
		next = append(next, fmt.Sprintf("See one member's changes with person_id=%q.", res.ByPerson[0].Who))
	}
	if len(next) > 3 {
		next = next[:3]
	}
	return next
}

// --- words ---

func wordFor(entityType string) string {
	if k, ok := kinds[entityType]; ok {
		return k.word
	}
	return strings.ReplaceAll(entityType, "_", " ")
}

func verbFor(action string) string {
	if v, ok := pastTense[action]; ok {
		return v
	}
	return action
}

func actionRank(a string) int {
	if r, ok := actionOrder[a]; ok {
		return r
	}
	return len(actionOrder)
}

// plural is "1 list item", "3 list items", "2 people", "2 properties".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + pluralNoun(noun)
}

func pluralNoun(noun string) string {
	head, last := "", noun
	if i := strings.LastIndex(noun, " "); i >= 0 {
		head, last = noun[:i+1], noun[i+1:]
	}
	switch {
	case last == "person":
		last = "people"
	case strings.HasSuffix(last, "y") && len(last) > 1 && !strings.ContainsRune("aeiou", rune(last[len(last)-2])):
		last = last[:len(last)-1] + "ies"
	case strings.HasSuffix(last, "s") || strings.HasSuffix(last, "sh") || strings.HasSuffix(last, "ch"):
		last += "es"
	default:
		last += "s"
	}
	return head + last
}

// --- lookups ---

// querier is the subset of pgx.Tx and pgxpool.Pool the lookups use.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// resolvePerson turns a person reference (a people.id, the person's user id,
// or an exact display name, any case) into the person's name and login user
// id (nil when they have no login). A non-nil result is the isError.
func resolvePerson(ctx context.Context, q querier, hid uuid.UUID, ref string) (string, *uuid.UUID, *mcp.CallToolResult, error) {
	var rows pgx.Rows
	var err error
	if id, perr := uuid.Parse(ref); perr == nil {
		rows, err = q.Query(ctx,
			`SELECT id, display_name, user_id FROM people WHERE household_id = $1 AND (id = $2 OR user_id = $2)
			 ORDER BY (id = $2) DESC LIMIT 1`, hid, id)
	} else {
		rows, err = q.Query(ctx,
			`SELECT id, display_name, user_id FROM people WHERE household_id = $1 AND lower(btrim(display_name)) = lower(btrim($2))
			 ORDER BY display_name, id`, hid, ref)
	}
	if err != nil {
		return "", nil, nil, err
	}
	type match struct {
		id     uuid.UUID
		name   string
		userID *uuid.UUID
	}
	ms, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (match, error) {
		var m match
		err := r.Scan(&m.id, &m.name, &m.userID)
		return m, err
	})
	if err != nil {
		return "", nil, nil, err
	}
	switch len(ms) {
	case 0:
		return "", nil, respond.Invalid("person_id", fmt.Sprintf("No person %q was found in this family.", ref),
			"Find people with person_find, or pass the person's exact name, e.g. Nora.", findExample), nil
	case 1:
		return ms[0].name, ms[0].userID, nil, nil
	}
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = fmt.Sprintf("%s (%s)", m.name, m.id)
	}
	return "", nil, respond.Invalid("person_id", fmt.Sprintf("%d people are named %q: %s.", len(ms), ref, strings.Join(names, ", ")),
		"Pass the person_id of the one you mean.", findExample), nil
}

// callerRole re-reads the caller's role from memberships (the claims' role
// may be stale: a demoted member loses access at once, a removed one gets
// nothing). Only the auth-disabled local identity, which has no user UUID
// and no agent token, falls back to the claims' role.
func callerRole(ctx context.Context, pool *pgxpool.Pool, hid uuid.UUID, c *auth.Claims) (string, *mcp.CallToolResult) {
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.AgentTokenID != "" {
			return "", notSignedIn()
		}
		return c.Role, nil
	}
	var role string
	err = pool.QueryRow(ctx,
		`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`, hid, uid).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", notSignedIn()
	}
	if err != nil {
		return "", respond.Failed("check your access", err)
	}
	return role, nil
}

func roleLabel(role string) string {
	if role == "" {
		return "current"
	}
	return role
}

// loadLocation returns the family's timezone, or UTC when it is empty or
// invalid (the same fallback as the calendar tools).
func loadLocation(ctx context.Context, q querier, hid uuid.UUID) (*time.Location, error) {
	var tz *string
	if err := q.QueryRow(ctx, `SELECT timezone FROM households WHERE id = $1`, hid).Scan(&tz); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.UTC, nil
		}
		return nil, err
	}
	if tz == nil || *tz == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(*tz)
	if err != nil {
		return time.UTC, nil
	}
	return loc, nil
}

func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}

// --- argument parsing ---

// argError is a bad argument: which field, what is wrong, how to fix it.
type argError struct {
	field, message, fix string
}

func (e *argError) Error() string { return e.message }

func badArg(field, message, fix string) error {
	return &argError{field: field, message: message, fix: fix}
}

// invalid turns a parse error into an isError result with a fix and a
// valid example.
func invalid(err error) *mcp.CallToolResult {
	var ae *argError
	if errors.As(err, &ae) {
		return respond.Invalid(ae.field, ae.message, ae.fix, findExample)
	}
	return respond.Invalid("", err.Error(), "Correct the arguments and call again.", findExample)
}

// parseFindInput validates activity_find arguments.
func parseFindInput(raw map[string]any) (findInput, error) {
	in := findInput{limit: defaultLimit}
	args := withAliases(raw, findAliases)
	for _, key := range []string{"since", "person_id", "about_type", "action", "cursor"} {
		if v, present := args[key]; present && v != nil {
			if _, ok := v.(string); !ok {
				return in, badArg(key, key+" must be a string.", fmt.Sprintf("Pass %s as text in quotes.", key))
			}
		}
	}
	str := func(key string) *string {
		s, _ := args[key].(string)
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		return &s
	}
	if s := str("since"); s != nil {
		if _, err := time.Parse(time.DateOnly, *s); err == nil {
			in.sinceDate = s
		} else if t, err := time.Parse(time.RFC3339, *s); err == nil {
			in.since = &t
		} else if _, err := time.Parse("2006-01-02T15:04:05", *s); err == nil || looksNaive(*s) {
			return in, badArg("since", fmt.Sprintf("since %q has no timezone offset.", *s),
				"Add the offset, e.g. 2026-11-09T00:00:00-05:00, or pass just the date, e.g. 2026-11-09.")
		} else {
			return in, badArg("since", fmt.Sprintf("since %q is not a date or time.", *s),
				"Pass since as YYYY-MM-DD (e.g. 2026-11-09) or ISO 8601 with offset.")
		}
	}
	in.person = str("person_id")
	if s := str("about_type"); s != nil {
		a := strings.ToLower(*s)
		if a == "calendar_event" || a == "calendar" {
			a = "event"
		}
		if a == "vendor" {
			a = "contact"
		}
		if a == "secret" {
			a = "credential"
		}
		if a == "household" {
			a = "family"
		}
		ok := false
		for _, t := range aboutTypes {
			if t == a {
				ok = true
			}
		}
		if !ok {
			return in, badArg("about_type", fmt.Sprintf("about_type %q is not a kind of record.", *s),
				"Pass one of: "+strings.Join(aboutTypes, ", ")+".")
		}
		in.about = &a
	}
	if s := str("action"); s != nil {
		a, ok := actions[strings.ToLower(*s)]
		if !ok {
			return in, badArg("action", fmt.Sprintf("action %q is not created, updated or deleted.", *s),
				"Pass action as created, updated or deleted, or leave it out for all changes.")
		}
		in.action = &a
	}
	if n, ok, err := wholeNumber(args, "limit", 1, maxLimit); err != nil {
		return in, err
	} else if ok {
		in.limit = n
	}
	if s := str("cursor"); s != nil {
		id, err := strconv.ParseInt(strings.TrimPrefix(*s, cursorPrefix), 10, 64)
		if !strings.HasPrefix(*s, cursorPrefix) || err != nil || id <= 0 {
			return in, badArg("cursor", fmt.Sprintf("cursor %q is not a cursor from activity_find.", *s),
				"Pass next_cursor from the previous result, or leave cursor out for the newest changes.")
		}
		in.before = id
	}
	return in, nil
}

// looksNaive reports a date-time without an offset, e.g. 2026-11-09T09:00.
func looksNaive(s string) bool {
	if len(s) < 11 || s[4] != '-' || s[7] != '-' || (s[10] != 'T' && s[10] != ' ') {
		return false
	}
	rest := s[11:]
	return !strings.ContainsAny(rest, "Zz+") && !strings.Contains(rest, "-")
}

// withAliases returns a copy of args with old parameter names moved to their
// catalog names (a catalog name that is present wins).
func withAliases(args map[string]any, aliases map[string]string) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	for old, name := range aliases {
		if v, ok := out[old]; ok {
			if w, present := out[name]; !present || w == nil {
				out[name] = v
			}
			delete(out, old)
		}
	}
	return out
}

// wholeNumber reads an integer argument in [lo, hi]; ok is false when it is
// absent or null. Numbers sent as numeric strings are accepted.
func wholeNumber(args map[string]any, key string, lo, hi int) (int, bool, error) {
	v, present := args[key]
	if !present || v == nil {
		return 0, false, nil
	}
	bad := badArg(key, fmt.Sprintf("%s must be a whole number from %d to %d.", key, lo, hi),
		fmt.Sprintf("Pass %s as a whole number between %d and %d.", key, lo, hi))
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case int:
		n = float64(x)
	case int64:
		n = float64(x)
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, false, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false, bad
		}
		n = f
	default:
		return 0, false, bad
	}
	if math.IsNaN(n) || n != math.Trunc(n) || n < float64(lo) || n > float64(hi) {
		return 0, false, bad
	}
	return int(n), true, nil
}
