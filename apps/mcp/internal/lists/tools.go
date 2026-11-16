// Package lists provides the MCP list tools of catalog v2 (KB
// architecture/mcp-tool-catalog.md, "list: shared checklists"): list_find,
// list_get, list_create, list_add_items, list_check_items, list_update_item,
// list_remove_items and list_update. Lists and their items live in the
// migration 046 tables (one row per item).
//
// The tools follow the same rules as the api's lists package (#4472, KB
// apis/lists.md):
//   - every item change touches only the affected rows, so two people
//     checking off different items never overwrite each other;
//   - each call is one transaction and all-or-nothing;
//   - adds take a FOR NO KEY UPDATE lock on the list row, which serialises
//     adders (distinct positions, race-free duplicate check) without
//     blocking check-offs, which lock only their items, in id order;
//   - items of an archived list are frozen.
//
// They are built for small models: list_id takes a list's id, its name, or
// its kind ("grocery"), so "add milk and eggs to the grocery list" is one
// list_add_items call, and list_check_items/list_remove_items match items by
// text ("turkey"). Every result is built with internal/respond and every
// call is scoped to the caller's household.
package lists

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/sqlutil"
)

// handlerFunc is the signature of every tool handler.
type handlerFunc = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// querier is what pgxpool.Pool and pgx.Tx have in common.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// kinds is lists_kind_check (migration 046), in catalog order.
var kinds = []string{"grocery", "packing", "shopping", "todo", "chores", "other"}

// kindWords maps what people call a kind of list to the kind. A list_id
// that normalizes to one of these words means "the family's <kind> list".
var kindWords = map[string]string{
	"grocery": "grocery", "groceries": "grocery", "grocery shopping": "grocery", "food": "grocery",
	"packing": "packing", "pack": "packing",
	"shopping": "shopping",
	"todo":     "todo", "to-do": "todo", "to do": "todo", "todos": "todo", "to-dos": "todo", "tasks": "todo",
	"chores": "chores", "chore": "chores",
	"other": "other",
}

// kindTitle is the name list_add_items gives a list it creates for a kind.
var kindTitle = map[string]string{
	"grocery": "Grocery", "packing": "Packing", "shopping": "Shopping",
	"todo": "To-do", "chores": "Chores", "other": "List",
}

const (
	defaultFindLimit = 25
	maxFindLimit     = 100
	// maxBatch is the most items one call adds, checks or removes (the api
	// allows 500; a model never needs that many in one turn).
	maxBatch = 200
	// maxGetItems caps list_get; a family list never comes close.
	maxGetItems = 500
	// maxNameRunes is lists.name (VARCHAR(255)).
	maxNameRunes = 255
	// maxTextRunes caps an item's text (TEXT in the DB).
	maxTextRunes = 500
	// maxUnitRunes and maxSectionRunes are list_items.unit/section.
	maxUnitRunes    = 50
	maxSectionRunes = 100
	// createDedupeWindow is the catalog loop guard for list_create.
	createDedupeWindow = "10 minutes"
)

// exampleListID is the list id used in error examples.
const exampleListID = "7c1e2a9b-4d3f-4e8a-b6c5-2f9d0e1a3b4c"

// ---------------------------------------------------------------------------
// Output types. IDs are strings: the outputSchema is inferred with
// jsonschema-go, which sees uuid.UUID as a [16]byte array.
// ---------------------------------------------------------------------------

// listInfo is one list without its items.
type listInfo struct {
	ID               string    `json:"id" jsonschema_description:"The list's id; pass it as list_id."`
	Name             string    `json:"name"`
	Kind             string    `json:"kind" jsonschema_description:"grocery, packing, shopping, todo, chores or other."`
	AboutType        string    `json:"about_type" jsonschema_description:"What the list is about: family, person, property, vehicle, ..."`
	AboutID          *string   `json:"about_id" jsonschema_description:"Id of the row it is about; null for family lists."`
	IsTemplate       bool      `json:"is_template" jsonschema_description:"true for a template: copy it with list_create from_template_id."`
	Archived         bool      `json:"archived" jsonschema_description:"true when archived: readable, but items can't change."`
	ItemCount        int       `json:"item_count"`
	OpenCount        int       `json:"open_count" jsonschema_description:"Items not checked off yet."`
	UpdatedAt        time.Time `json:"updated_at"`
	UpdatedLocalDate string    `json:"updated_local_date" jsonschema_description:"updated_at as YYYY-MM-DD in the family timezone."`
	UpdatedWeekday   string    `json:"updated_weekday" jsonschema_description:"Weekday of updated_local_date, e.g. Thu."`
	Duplicate        bool      `json:"duplicate,omitempty" jsonschema_description:"true when list_create returned the same list made minutes ago instead of a copy."`
}

// item is one list item.
type item struct {
	ID               string   `json:"id" jsonschema_description:"The item's id; pass it as item_id or in item_ids."`
	ListID           string   `json:"list_id"`
	Text             string   `json:"text"`
	Quantity         *float64 `json:"quantity"`
	Unit             *string  `json:"unit"`
	Section          *string  `json:"section" jsonschema_description:"Aisle or room, e.g. Produce."`
	Checked          bool     `json:"checked"`
	AssigneePersonID *string  `json:"assignee_person_id"`
	AssigneeName     *string  `json:"assignee_name"`
	DueDate          *string  `json:"due_date" jsonschema_description:"YYYY-MM-DD."`
	DueWeekday       *string  `json:"due_weekday" jsonschema_description:"Weekday of due_date, e.g. Thu."`
	AddedBy          *string  `json:"added_by" jsonschema_description:"Name of the member who added it, when known."`
}

// listDetail is list_get's data.
type listDetail struct {
	List      listInfo `json:"list"`
	Showing   string   `json:"showing" jsonschema_description:"Which items are included: all, open or done."`
	Items     []item   `json:"items"`
	OpenCount int      `json:"open_count"`
	DoneCount int      `json:"done_count"`
	Truncated bool     `json:"truncated,omitempty"`
}

// createResult is list_create's data.
type createResult struct {
	List         listInfo `json:"list"`
	Items        []item   `json:"items" jsonschema_description:"The list's items in order."`
	FromTemplate *string  `json:"from_template,omitempty" jsonschema_description:"Name of the template the items were copied from."`
}

// addResult is list_add_items' data.
type addResult struct {
	List          listInfo `json:"list"`
	CreatedList   bool     `json:"created_list,omitempty" jsonschema_description:"true when the list did not exist and was created by this call."`
	Added         []item   `json:"added"`
	Reopened      []item   `json:"reopened" jsonschema_description:"Items that were checked off and are back on the list."`
	AlreadyOnList []item   `json:"already_on_list" jsonschema_description:"Items already open on the list; not added twice."`
}

// checkResult is list_check_items' data.
type checkResult struct {
	List      listInfo `json:"list"`
	Checked   bool     `json:"checked" jsonschema_description:"true: items were checked off; false: unchecked."`
	Changed   []item   `json:"changed"`
	Unchanged []item   `json:"unchanged" jsonschema_description:"Items that were already in that state."`
	Left      []string `json:"left" jsonschema_description:"Texts of the items still open (first 20)."`
}

// removeResult is list_remove_items' data.
type removeResult struct {
	List    listInfo `json:"list"`
	Removed []item   `json:"removed"`
}

// itemResult is list_update_item's data.
type itemResult struct {
	List listInfo `json:"list"`
	Item item     `json:"item"`
}

// ---------------------------------------------------------------------------
// SQL fragments
// ---------------------------------------------------------------------------

// listCols is what scanList reads. FROM must be listFrom.
const listCols = `l.id, l.household_id, l.name, l.kind, l.entity_type::text, l.entity_id,
	l.is_template, l.archived,
	(SELECT count(*) FROM list_items i WHERE i.list_id = l.id)::int,
	(SELECT count(*) FROM list_items i WHERE i.list_id = l.id AND NOT i.checked)::int,
	l.updated_at,
	to_char(l.updated_at AT TIME ZONE h.timezone, 'YYYY-MM-DD'),
	to_char(l.updated_at AT TIME ZONE h.timezone, 'Dy')`

const listFrom = ` FROM lists l JOIN households h ON h.id = l.household_id`

// itemCols is what scanItem reads. FROM must be itemFrom.
const itemCols = `i.id, i.list_id, i.text, i.quantity::float8, i.unit, i.section, i.checked,
	i.assignee_person_id, pe.display_name, i.due_date::text, to_char(i.due_date, 'Dy'), u.name`

const itemFrom = ` FROM list_items i
	LEFT JOIN people pe ON pe.id = i.assignee_person_id
	LEFT JOIN users u ON u.id = i.created_by`

// itemOrder is the canonical item order: position is not unique.
const itemOrder = ` ORDER BY i.position, i.created_at, i.id`

func scanList(row pgx.Row, extra ...any) (listInfo, error) {
	var (
		l                 listInfo
		id, hid, entityID uuid.UUID
		entityType        string
	)
	dest := append([]any{&id, &hid, &l.Name, &l.Kind, &entityType, &entityID,
		&l.IsTemplate, &l.Archived, &l.ItemCount, &l.OpenCount,
		&l.UpdatedAt, &l.UpdatedLocalDate, &l.UpdatedWeekday}, extra...)
	if err := row.Scan(dest...); err != nil {
		return listInfo{}, err
	}
	l.ID = id.String()
	l.AboutType, l.AboutID = aboutFromDB(entityType, entityID, hid)
	return l, nil
}

func getList(ctx context.Context, q querier, hid, id uuid.UUID) (listInfo, error) {
	return scanList(q.QueryRow(ctx, `SELECT `+listCols+listFrom+` WHERE l.id = $1 AND l.household_id = $2`, id, hid))
}

func scanItems(rows pgx.Rows) ([]item, error) {
	defer rows.Close()
	out := []item{}
	for rows.Next() {
		var (
			it           item
			id, listID   uuid.UUID
			assignee     *uuid.UUID
			dueDate, dow *string
		)
		if err := rows.Scan(&id, &listID, &it.Text, &it.Quantity, &it.Unit, &it.Section, &it.Checked,
			&assignee, &it.AssigneeName, &dueDate, &dow, &it.AddedBy); err != nil {
			return nil, err
		}
		it.ID, it.ListID = id.String(), listID.String()
		if assignee != nil {
			s := assignee.String()
			it.AssigneePersonID = &s
		}
		it.DueDate, it.DueWeekday = dueDate, dow
		out = append(out, it)
	}
	return out, rows.Err()
}

// itemsByID returns the given items in list order.
func itemsByID(ctx context.Context, q querier, ids []uuid.UUID) ([]item, error) {
	if len(ids) == 0 {
		return []item{}, nil
	}
	rows, err := q.Query(ctx, `SELECT `+itemCols+itemFrom+` WHERE i.id = ANY($1)`+itemOrder, ids)
	if err != nil {
		return nil, err
	}
	return scanItems(rows)
}

// openTexts returns the texts of the list's open items, in order, at most n.
func openTexts(ctx context.Context, q querier, listID uuid.UUID, n int) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT i.text FROM list_items i WHERE i.list_id = $1 AND NOT i.checked`+itemOrder+` LIMIT $2`, listID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// About links (same mapping as the note and document tools)
// ---------------------------------------------------------------------------

// aboutTypes is the catalog's about_type enum, in catalog order.
var aboutTypes = []string{
	"family", "person", "property", "room", "asset", "vehicle", "pet", "contact",
	"bill", "loan", "maintenance", "event", "list", "recipe", "guide",
}

// aboutToDB maps the catalog names that differ from the DB entity_kind.
var aboutToDB = map[string]string{
	"family":      "household",
	"contact":     "vendor",
	"maintenance": "maintenance_task",
}

// aboutFinder names the registered tool that finds ids for each about_type.
var aboutFinder = map[string]string{
	"person": "person_find", "property": "property_find", "room": "property_find",
	"asset": "asset_find", "vehicle": "vehicle_find", "pet": "pet_find",
	"contact": "contact_find", "bill": "bill_find", "loan": "loan_find",
	"maintenance": "maintenance_find", "event": "event_find", "list": "list_find",
}

func findIDWith(t string) string {
	if tool, ok := aboutFinder[t]; ok {
		return "find it with " + tool
	}
	return "ask the user for it; looking up a " + t + " by name is coming soon"
}

func aboutFromDB(entityType string, entityID, householdID uuid.UUID) (string, *string) {
	for about, kind := range aboutToDB {
		if kind == entityType {
			entityType = about
			break
		}
	}
	if entityType == "family" && entityID == householdID {
		return entityType, nil
	}
	s := entityID.String()
	return entityType, &s
}

type about struct {
	Type string
	Kind string
	ID   uuid.UUID
}

// parseAbout reads about_type and about_id for list_create. An empty pair is
// a family list (zero about).
func parseAbout(args map[string]any, example map[string]any) (about, *mcp.CallToolResult) {
	t := strings.ToLower(strings.TrimSpace(str(args, "about_type")))
	rawID := strings.TrimSpace(str(args, "about_id"))
	if t == "" && rawID == "" {
		return about{}, nil
	}
	if t == "" {
		return about{}, respond.Invalid("about_type", "about_id was given without about_type.",
			"Pass about_type too (e.g. vehicle), or leave both out for a family list.", example)
	}
	valid := false
	for _, a := range aboutTypes {
		valid = valid || a == t
	}
	if !valid {
		return about{}, respond.Invalid("about_type", fmt.Sprintf("about_type %q is not one of the known kinds.", t),
			"Use one of: "+strings.Join(aboutTypes, ", ")+".", example)
	}
	a := about{Type: t, Kind: t}
	if k, ok := aboutToDB[t]; ok {
		a.Kind = k
	}
	if t == "family" {
		return a, nil
	}
	if rawID == "" {
		return about{}, respond.Invalid("about_id", fmt.Sprintf("about_type %s needs about_id.", t),
			fmt.Sprintf("Pass the %s's id as about_id (%s), or leave about_type out for a family list.", t, findIDWith(t)), example)
	}
	id, err := uuid.Parse(rawID)
	if err != nil {
		return about{}, respond.Invalid("about_id", fmt.Sprintf("about_id %q is not a UUID.", rawID),
			fmt.Sprintf("Pass the %s's id (a UUID), not its name (%s).", t, findIDWith(t)), example)
	}
	a.ID = id
	return a, nil
}

// ---------------------------------------------------------------------------
// Argument helpers
// ---------------------------------------------------------------------------

func str(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}

// householdOf reads the household id from the caller's claims.
func householdOf(ctx context.Context) (uuid.UUID, bool) {
	claims := server.ClaimsFromContext(ctx)
	if claims == nil {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(claims.HouseholdID)
	return id, err == nil
}

// intArg reads a whole number sent as a JSON number or a numeric string.
func intArg(args map[string]any, key string) (n int, given, ok bool) {
	switch v := args[key].(type) {
	case nil:
		return 0, false, true
	case float64:
		return int(v), true, v == float64(int(v))
	case int:
		return v, true, true
	case string:
		if strings.TrimSpace(v) == "" {
			return 0, false, true
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, true, err == nil
	}
	return 0, true, false
}

// boolArg reads a boolean sent as a JSON bool or "true"/"false".
func boolArg(args map[string]any, key string) (b, given, ok bool) {
	switch v := args[key].(type) {
	case nil:
		return false, false, true
	case bool:
		return v, true, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "":
			return false, false, true
		case "true", "yes", "1":
			return true, true, true
		case "false", "no", "0":
			return false, true, true
		}
	}
	return false, true, false
}

// numArg reads a number sent as a JSON number or a numeric string.
func numArg(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

// stringList reads an array of strings, also accepting one string with
// commas or newlines between the values. Blank values are dropped.
func stringList(v any) ([]string, bool) {
	var raw []string
	switch x := v.(type) {
	case nil:
		return nil, true
	case string:
		raw = splitList(x)
	case []any:
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			raw = append(raw, s)
		}
	case []string:
		raw = x
	default:
		return nil, false
	}
	out := []string{}
	for _, s := range raw {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out, true
}

var listSep = regexp.MustCompile(`[\n,;]+`)

func splitList(s string) []string { return listSep.Split(s, -1) }

var spaces = regexp.MustCompile(`\s+`)

// normText is the comparison form of an item's text: lower case with
// single spaces.
func normText(s string) string {
	return strings.ToLower(strings.TrimSpace(spaces.ReplaceAllString(s, " ")))
}

// singular strips a plural ending, so "eggs" matches "egg" and "tomatoes"
// matches "tomato". Only used for matching, never stored.
func singular(s string) string {
	switch {
	case (strings.HasSuffix(s, "sses") || strings.HasSuffix(s, "xes") ||
		strings.HasSuffix(s, "ches") || strings.HasSuffix(s, "shes")) && len(s) > 4:
		return strings.TrimSuffix(s, "es") // glasses, boxes, peaches, dishes
	case strings.HasSuffix(s, "oes") && len(s) > 4:
		return strings.TrimSuffix(s, "es")
	case strings.HasSuffix(s, "ies") && len(s) > 4:
		return strings.TrimSuffix(s, "ies") + "y"
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") && len(s) > 3:
		return strings.TrimSuffix(s, "s")
	}
	return s
}

// normListRef is the comparison form of a list_id given as a name: lower
// case, without "the"/"my"/"our" in front or "list" at the end.
func normListRef(s string) string {
	s = normText(s)
	for _, p := range []string{"the ", "my ", "our ", "family ", "family's "} {
		s = strings.TrimPrefix(s, p)
	}
	for _, suf := range []string{" lists", " list"} {
		s = strings.TrimSuffix(s, suf)
	}
	return strings.TrimSpace(s)
}

// parseKind reads a kind, accepting the words in kindWords ("groceries").
func parseKind(raw string) (string, bool) {
	k := normListRef(raw)
	if v, ok := kindWords[k]; ok {
		return v, true
	}
	return "", false
}

func joinTexts(texts []string) string {
	switch len(texts) {
	case 0:
		return ""
	case 1:
		return texts[0]
	case 2:
		return texts[0] + " and " + texts[1]
	}
	return strings.Join(texts[:len(texts)-1], ", ") + " and " + texts[len(texts)-1]
}

func itemTexts(items []item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Text
	}
	return out
}

// quoted renders texts for a hint: ["milk","eggs"].
func quoted(texts []string) string {
	parts := make([]string, len(texts))
	for i, t := range texts {
		parts[i] = strconv.Quote(t)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// leftLine is the catalog's check hint: "3 left: milk, eggs, bread."
func leftLine(open int, texts []string) string {
	if open == 0 {
		return "Nothing left; everything is checked off."
	}
	more := ""
	if open > len(texts) {
		more = fmt.Sprintf(" and %d more", open-len(texts))
	}
	return fmt.Sprintf("%d left: %s%s.", open, strings.Join(texts, ", "), more)
}

// ---------------------------------------------------------------------------
// List resolution (list_id = id, name or kind)
// ---------------------------------------------------------------------------

type listCand struct {
	ID         uuid.UUID
	Name       string
	Kind       string
	IsTemplate bool
	Archived   bool
}

func (c listCand) String() string {
	extra := ""
	if c.IsTemplate {
		extra = ", template"
	}
	if c.Archived {
		extra += ", archived"
	}
	return fmt.Sprintf("%q (%s%s)", c.Name, c.ID, extra)
}

// resolution is the outcome of resolveList. Exactly one of List and Bad is
// set; MissingKind is set (with Bad) when list_id named a kind the family
// has no active list of, so list_add_items can create one.
type resolution struct {
	List        listCand
	Bad         *mcp.CallToolResult
	MissingKind string
}

func familyLists(ctx context.Context, q querier, hid uuid.UUID) ([]listCand, error) {
	rows, err := q.Query(ctx,
		`SELECT id, name, kind, is_template, archived FROM lists WHERE household_id = $1
		 ORDER BY archived, is_template, updated_at DESC, id`, hid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []listCand
	for rows.Next() {
		var c listCand
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.IsTemplate, &c.Archived); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// prefer narrows several matches to active lists, then non-templates (or
// templates when wantTemplate), when any exist.
func prefer(cands []listCand, wantTemplate bool) []listCand {
	filter := func(in []listCand, keep func(listCand) bool) []listCand {
		var out []listCand
		for _, c := range in {
			if keep(c) {
				out = append(out, c)
			}
		}
		if len(out) == 0 {
			return in
		}
		return out
	}
	cands = filter(cands, func(c listCand) bool { return !c.Archived })
	return filter(cands, func(c listCand) bool { return c.IsTemplate == wantTemplate })
}

func ambiguous(field, ref string, cands []listCand, example map[string]any) *mcp.CallToolResult {
	parts := make([]string, 0, len(cands))
	for i, c := range cands {
		if i == 10 {
			parts = append(parts, fmt.Sprintf("and %d more", len(cands)-10))
			break
		}
		parts = append(parts, c.String())
	}
	return respond.Invalid(field,
		fmt.Sprintf("%d lists match %q: %s.", len(cands), ref, strings.Join(parts, ", ")),
		fmt.Sprintf("Pass the id of the one you mean as %s.", field), example)
}

// resolveList turns list_id (an id, a name, or a kind such as "grocery")
// into one of the family's lists. Archived lists and templates are found by
// id or exact name; by kind or partial name only active, non-template lists
// count.
func resolveList(ctx context.Context, q querier, hid uuid.UUID, ref string, example map[string]any) (resolution, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return resolution{Bad: respond.Invalid("list_id", "list_id is required.",
			"Pass the list's name (e.g. Grocery), its kind (e.g. grocery) or its id from list_find.", example)}, nil
	}
	if id, err := uuid.Parse(ref); err == nil {
		var c listCand
		err := q.QueryRow(ctx,
			`SELECT id, name, kind, is_template, archived FROM lists WHERE id = $1 AND household_id = $2`,
			id, hid).Scan(&c.ID, &c.Name, &c.Kind, &c.IsTemplate, &c.Archived)
		if errors.Is(err, pgx.ErrNoRows) {
			return resolution{Bad: respond.Invalid("list_id", fmt.Sprintf("No list with id %s was found in this family.", ref),
				"Find the family's lists with list_find, or pass the list's name.", example)}, nil
		}
		return resolution{List: c}, err
	}

	all, err := familyLists(ctx, q, hid)
	if err != nil {
		return resolution{}, err
	}
	raw, norm := normText(ref), normListRef(ref)

	// 1. Exact name, any case.
	var exact []listCand
	for _, c := range all {
		if n := normText(c.Name); n == raw || n == norm || normListRef(c.Name) == norm {
			exact = append(exact, c)
		}
	}
	if len(exact) > 0 {
		if p := prefer(exact, false); len(p) == 1 {
			return resolution{List: p[0]}, nil
		} else {
			return resolution{Bad: ambiguous("list_id", ref, p, example)}, nil
		}
	}

	active := func(keep func(listCand) bool) []listCand {
		var out []listCand
		for _, c := range all {
			if !c.Archived && !c.IsTemplate && keep(c) {
				out = append(out, c)
			}
		}
		return out
	}

	// 2. A kind: "grocery", "groceries", "the to-do list".
	if kind, ok := kindWords[norm]; ok {
		m := active(func(c listCand) bool { return c.Kind == kind })
		switch len(m) {
		case 1:
			return resolution{List: m[0]}, nil
		case 0:
			return resolution{MissingKind: kind, Bad: respond.Invalid("list_id",
				fmt.Sprintf("The family has no %s list yet.", kind),
				fmt.Sprintf("Start one with list_create name=%q kind=%q, or add items with list_add_items list_id=%q, which creates it.", kindTitle[kind], kind, kind),
				map[string]any{"name": kindTitle[kind], "kind": kind})}, nil
		default:
			return resolution{Bad: ambiguous("list_id", ref, m, example)}, nil
		}
	}

	// 3. Part of a name: "costco" finds "Costco run".
	if utf8.RuneCountInString(norm) >= 3 {
		m := active(func(c listCand) bool { return strings.Contains(normText(c.Name), norm) })
		switch len(m) {
		case 1:
			return resolution{List: m[0]}, nil
		case 0:
		default:
			return resolution{Bad: ambiguous("list_id", ref, m, example)}, nil
		}
	}

	names := []string{}
	for _, c := range active(func(listCand) bool { return true }) {
		if len(names) == 8 {
			break
		}
		names = append(names, strconv.Quote(c.Name))
	}
	fix := "The family has no active lists; start one with list_create."
	if len(names) > 0 {
		fix = "Active lists: " + strings.Join(names, ", ") + ". Pass one of these names, or find others (archived, templates) with list_find."
	}
	return resolution{Bad: respond.Invalid("list_id", fmt.Sprintf("No list named %q was found in this family.", ref), fix, example)}, nil
}

// resolveTemplate turns from_template_id (an id or a name, e.g. "Beach
// trip") into a list to copy. Templates are preferred over other lists.
func resolveTemplate(ctx context.Context, q querier, hid uuid.UUID, ref string, example map[string]any) (listCand, *mcp.CallToolResult, error) {
	const field = "from_template_id"
	if id, err := uuid.Parse(ref); err == nil {
		var c listCand
		err := q.QueryRow(ctx,
			`SELECT id, name, kind, is_template, archived FROM lists WHERE id = $1 AND household_id = $2`,
			id, hid).Scan(&c.ID, &c.Name, &c.Kind, &c.IsTemplate, &c.Archived)
		if errors.Is(err, pgx.ErrNoRows) {
			return listCand{}, respond.Invalid(field, fmt.Sprintf("No list or template with id %s was found in this family.", ref),
				"Find templates with list_find templates_only=true.", example), nil
		}
		return c, nil, err
	}
	all, err := familyLists(ctx, q, hid)
	if err != nil {
		return listCand{}, nil, err
	}
	norm := normListRef(strings.TrimSuffix(normText(ref), " template"))
	pick := func(keep func(listCand) bool) []listCand {
		var out []listCand
		for _, c := range all {
			if keep(c) {
				out = append(out, c)
			}
		}
		return out
	}
	steps := []func(listCand) bool{
		func(c listCand) bool { return normListRef(c.Name) == norm },
		func(c listCand) bool { return c.IsTemplate && strings.Contains(normText(c.Name), norm) },
	}
	if kind, ok := kindWords[norm]; ok {
		steps = append(steps, func(c listCand) bool { return c.IsTemplate && c.Kind == kind })
	}
	for _, keep := range steps {
		m := pick(keep)
		if len(m) == 0 {
			continue
		}
		if p := prefer(m, true); len(p) == 1 {
			return p[0], nil, nil
		} else {
			return listCand{}, ambiguous(field, ref, p, example), nil
		}
	}
	names := []string{}
	for _, c := range pick(func(c listCand) bool { return c.IsTemplate }) {
		names = append(names, strconv.Quote(c.Name))
	}
	fix := "The family has no templates; leave from_template_id out and pass items instead."
	if len(names) > 0 {
		fix = "Templates: " + strings.Join(names, ", ") + ". Pass one of these names or its id."
	}
	return listCand{}, respond.Invalid(field, fmt.Sprintf("No template named %q was found in this family.", ref), fix, example), nil
}

// ---------------------------------------------------------------------------
// Item input
// ---------------------------------------------------------------------------

// newItem is one item to add, as parsed from items[].
type newItem struct {
	Text        string
	Quantity    *float64
	Unit        *string
	Section     *string
	AssigneeRef string
	Assignee    *uuid.UUID
	DueDate     *string
}

// parseItems reads items[]: objects {text, quantity?, unit?, section?,
// assignee_person_id?, due_date?}, plain strings, or one string with commas
// or newlines between items.
func parseItems(v any, example map[string]any) ([]newItem, *mcp.CallToolResult) {
	bad := func(msg string) ([]newItem, *mcp.CallToolResult) {
		return nil, respond.Invalid("items", msg,
			`Pass items as a list of objects with text, e.g. [{"text":"milk"},{"text":"eggs","quantity":12}], or plain strings ["milk","eggs"].`, example)
	}
	var raw []any
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		for _, s := range splitList(x) {
			raw = append(raw, s)
		}
	case []any:
		raw = x
	case map[string]any:
		raw = []any{x}
	default:
		return bad("items must be a list.")
	}
	out := []newItem{}
	for i, e := range raw {
		var it newItem
		switch x := e.(type) {
		case string:
			it.Text = x
		case map[string]any:
			it.Text = str(x, "text")
			if it.Text == "" {
				it.Text = str(x, "name") // hidden aliases
			}
			if it.Text == "" {
				it.Text = str(x, "item")
			}
			if q, ok := x["quantity"]; ok && q != nil && q != "" {
				f, ok := numArg(q)
				if !ok || f <= 0 {
					return bad(fmt.Sprintf("items[%d].quantity must be a number above 0.", i))
				}
				it.Quantity = &f
			}
			if s := strings.TrimSpace(str(x, "unit")); s != "" {
				if utf8.RuneCountInString(s) > maxUnitRunes {
					return bad(fmt.Sprintf("items[%d].unit is longer than %d characters.", i, maxUnitRunes))
				}
				it.Unit = &s
			}
			if s := strings.TrimSpace(str(x, "section")); s != "" {
				if utf8.RuneCountInString(s) > maxSectionRunes {
					return bad(fmt.Sprintf("items[%d].section is longer than %d characters.", i, maxSectionRunes))
				}
				it.Section = &s
			}
			it.AssigneeRef = strings.TrimSpace(str(x, "assignee_person_id"))
			if s := strings.TrimSpace(str(x, "due_date")); s != "" {
				if _, err := time.Parse("2006-01-02", s); err != nil {
					return bad(fmt.Sprintf("items[%d].due_date %q is not a date as YYYY-MM-DD.", i, s))
				}
				it.DueDate = &s
			}
		default:
			return bad(fmt.Sprintf("items[%d] is neither text nor an object with text.", i))
		}
		it.Text = strings.TrimSpace(spaces.ReplaceAllString(it.Text, " "))
		if it.Text == "" {
			if _, isStr := e.(string); isStr {
				continue // "milk,,eggs" or a trailing comma
			}
			return bad(fmt.Sprintf("items[%d] has no text.", i))
		}
		if utf8.RuneCountInString(it.Text) > maxTextRunes {
			return bad(fmt.Sprintf("items[%d].text is longer than %d characters.", i, maxTextRunes))
		}
		out = append(out, it)
	}
	if len(out) > maxBatch {
		return bad(fmt.Sprintf("%d items is more than %d in one call.", len(out), maxBatch))
	}
	return out, nil
}

// resolvePerson turns a person reference (a people.id, the person's user id,
// or an exact display name, any case) into a person of the household.
func resolvePerson(ctx context.Context, q querier, hid uuid.UUID, field, ref string, example map[string]any) (uuid.UUID, *mcp.CallToolResult, error) {
	fix := "Find people with person_find, or pass the person's exact name, e.g. Nora."
	var rows pgx.Rows
	var err error
	if id, perr := uuid.Parse(ref); perr == nil {
		rows, err = q.Query(ctx,
			`SELECT id, display_name FROM people WHERE household_id = $1 AND (id = $2 OR user_id = $2)
			 ORDER BY (id = $2) DESC LIMIT 1`, hid, id)
	} else {
		rows, err = q.Query(ctx,
			`SELECT id, display_name FROM people WHERE household_id = $1 AND lower(btrim(display_name)) = lower(btrim($2))
			 ORDER BY display_name, id`, hid, ref)
	}
	if err != nil {
		return uuid.Nil, nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	var names []string
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return uuid.Nil, nil, err
		}
		ids, names = append(ids, id), append(names, fmt.Sprintf("%s (%s)", name, id))
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, nil, err
	}
	switch len(ids) {
	case 0:
		return uuid.Nil, respond.Invalid(field, fmt.Sprintf("No person %q was found in this family.", ref), fix, example), nil
	case 1:
		return ids[0], nil, nil
	}
	return uuid.Nil, respond.Invalid(field, fmt.Sprintf("%d people are named %q: %s.", len(ids), ref, strings.Join(names, ", ")),
		"Pass the person_id of the one you mean.", example), nil
}

// ---------------------------------------------------------------------------
// Text matching (list_check_items, list_remove_items)
// ---------------------------------------------------------------------------

type itemKey struct {
	ID      uuid.UUID
	Text    string
	Checked bool
}

func listItemKeys(ctx context.Context, q querier, listID uuid.UUID) ([]itemKey, error) {
	rows, err := q.Query(ctx, `SELECT i.id, i.text, i.checked FROM list_items i WHERE i.list_id = $1`+itemOrder, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []itemKey
	for rows.Next() {
		var k itemKey
		if err := rows.Scan(&k.ID, &k.Text, &k.Checked); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// wordSep splits item text into words: anything that is not a letter or a
// digit ("Turkey, 14 lb" is turkey/14/lb).
var wordSep = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// leadingFiller are words dropped from the front of a text before matching,
// so "the oat milk" is oat/milk and "some eggs" is egg.
var leadingFiller = map[string]bool{"the": true, "a": true, "an": true, "some": true, "my": true, "our": true}

// matchTokens is the word form of an item text used for matching: lower
// case words, each made singular, without leading filler words.
func matchTokens(s string) []string {
	var out []string
	for _, w := range wordSep.Split(normText(s), -1) {
		if w == "" || (len(out) == 0 && leadingFiller[w]) {
			continue
		}
		out = append(out, singular(w))
	}
	return out
}

// matchText finds the items one text refers to. It compares whole words,
// never characters, so "tea" can't hit "Steak" and "eggplant" can't hit
// "Egg". An exact match (the same words, any case, singular or plural,
// ignoring a leading the/a/an/some/my) beats a partial one, where every word
// of the text is a word of the item ("turkey" in "Turkey, 14 lb"). An item
// whose words are only part of the text never matches ("the oat milk" is not
// "Milk"), for check and remove alike. prefer, when not nil, keeps only items
// whose checked state differs from *prefer if any do (checking off "milk"
// means the open milk). Several exact matches are all returned; several
// partial ones are ambiguous.
func matchText(items []itemKey, text string, prefer *bool) (matches []itemKey, ambiguousMatches []itemKey) {
	q := matchTokens(text)
	if len(q) == 0 {
		return nil, nil
	}
	narrow := func(in []itemKey) []itemKey {
		if prefer == nil {
			return in
		}
		var out []itemKey
		for _, k := range in {
			if k.Checked != *prefer {
				out = append(out, k)
			}
		}
		if len(out) == 0 {
			return in
		}
		return out
	}
	var exact, partial []itemKey
	for _, k := range items {
		t := matchTokens(k.Text)
		switch {
		case slices.Equal(t, q):
			exact = append(exact, k)
		case containsAll(t, q):
			partial = append(partial, k)
		}
	}
	if len(exact) > 0 {
		return narrow(exact), nil
	}
	partial = narrow(partial)
	if len(partial) == 1 {
		return partial, nil
	}
	return nil, partial
}

// containsAll reports whether every word of want is a word of have.
func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// resolveItems turns item_ids[] and texts[] into item ids of the list. Any
// id not on the list, or text that matches nothing or several items, is an
// isError result naming it (nothing is changed).
func resolveItems(ctx context.Context, q querier, listID uuid.UUID, listName string, ids, texts []string, prefer *bool, example map[string]any) ([]uuid.UUID, *mcp.CallToolResult, error) {
	keys, err := listItemKeys(ctx, q, listID)
	if err != nil {
		return nil, nil, err
	}
	byID := map[uuid.UUID]bool{}
	for _, k := range keys {
		byID[k.ID] = true
	}
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	add := func(id uuid.UUID) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, raw := range ids {
		id, err := uuid.Parse(raw)
		if err != nil || !byID[id] {
			return nil, respond.Invalid("item_ids", fmt.Sprintf("%q is not an item of list %q.", raw, listName),
				fmt.Sprintf("Get the item ids with list_get list_id=%s, or pass texts instead, e.g. texts=[\"milk\"].", listID), example), nil
		}
		add(id)
	}
	for _, text := range texts {
		m, amb := matchText(keys, text, prefer)
		if len(amb) > 0 {
			parts := make([]string, 0, len(amb))
			for _, k := range amb {
				parts = append(parts, fmt.Sprintf("%q (%s)", k.Text, k.ID))
			}
			return nil, respond.Invalid("texts", fmt.Sprintf("%q matches %d items on %q: %s.", text, len(amb), listName, strings.Join(parts, ", ")),
				"Pass the full text of the one you mean, or its id in item_ids.", example), nil
		}
		if len(m) == 0 {
			open := []string{}
			for _, k := range keys {
				if !k.Checked && len(open) < 15 {
					open = append(open, fmt.Sprintf("%q (%s)", k.Text, k.ID))
				}
			}
			fix := fmt.Sprintf("List %q has no items.", listName)
			if len(keys) > 0 {
				fix = fmt.Sprintf("Open items on %q: %s. Pass the full text of the one you mean, or its id in item_ids; list_get list_id=%s shows all items.", listName, orNone(open), listID)
			}
			return nil, respond.Invalid("texts", fmt.Sprintf("No item on %q matches %q.", listName, text), fix, example), nil
		}
		for _, k := range m {
			add(k.ID)
		}
	}
	if len(out) > maxBatch {
		return nil, respond.Invalid("texts", fmt.Sprintf("%d items is more than %d in one call.", len(out), maxBatch),
			"Split the call in two.", example), nil
	}
	return out, nil, nil
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

// lockItems row-locks the given items of the list in id order (so
// overlapping batches can't deadlock) and fails unless every one is there.
func lockItems(ctx context.Context, tx pgx.Tx, listID uuid.UUID, ids []uuid.UUID) (bool, error) {
	var n int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM (
		  SELECT id FROM list_items WHERE list_id = $1 AND id = ANY($2)
		   ORDER BY id FOR NO KEY UPDATE) t`, listID, ids).Scan(&n); err != nil {
		return false, err
	}
	return n == len(ids), nil
}

// archivedResult is the isError result for an item write on an archived list.
func archivedResult(c listCand) *mcp.CallToolResult {
	return respond.Invalid("list_id", fmt.Sprintf("List %q is archived, so its items can't change.", c.Name),
		fmt.Sprintf("Unarchive it with list_update list_id=%s archived=false, or start a new list from it with list_create from_template_id=%s.", c.ID, c.ID),
		map[string]any{"list_id": c.ID.String(), "archived": false})
}

// writableList re-reads the list's archived flag inside tx (optionally with
// the adders' row lock) and returns the isError result when it is archived
// or gone.
func writableList(ctx context.Context, tx pgx.Tx, hid uuid.UUID, c listCand, lock bool) (*mcp.CallToolResult, error) {
	q := `SELECT archived FROM lists WHERE id = $1 AND household_id = $2`
	if lock {
		q += ` FOR NO KEY UPDATE`
	}
	var archived bool
	err := tx.QueryRow(ctx, q, c.ID, hid).Scan(&archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return respond.Invalid("list_id", fmt.Sprintf("List %q was just deleted.", c.Name),
			"Find the family's lists with list_find.", nil), nil
	}
	if err != nil {
		return nil, err
	}
	if archived {
		c.Archived = true
		return archivedResult(c), nil
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Shared annotations and params
// ---------------------------------------------------------------------------

func classR(title string) []mcp.ToolOption {
	return []mcp.ToolOption{mcp.WithTitleAnnotation(title), mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false), mcp.WithIdempotentHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false)}
}

func classC(title string) []mcp.ToolOption {
	return []mcp.ToolOption{mcp.WithTitleAnnotation(title), mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false), mcp.WithIdempotentHintAnnotation(false), mcp.WithOpenWorldHintAnnotation(false)}
}

func classU(title string) []mcp.ToolOption {
	return []mcp.ToolOption{mcp.WithTitleAnnotation(title), mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false), mcp.WithIdempotentHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false)}
}

func classX(title string) []mcp.ToolOption {
	return []mcp.ToolOption{mcp.WithTitleAnnotation(title), mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true), mcp.WithIdempotentHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false)}
}

func listIDParam() mcp.ToolOption {
	return mcp.WithString("list_id",
		mcp.Description("List name, kind or id, e.g. Grocery or grocery"),
		mcp.Required(),
	)
}

// itemSchema is the JSON schema of one items[] entry.
var itemSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"text":               map[string]any{"type": "string", "description": "What to get or do, e.g. milk"},
		"quantity":           map[string]any{"type": "number", "description": "How many, e.g. 2"},
		"unit":               map[string]any{"type": "string", "description": "Unit for quantity, e.g. lb"},
		"section":            map[string]any{"type": "string", "description": "Aisle or room, e.g. Produce"},
		"assignee_person_id": map[string]any{"type": "string", "description": "Who does it: person name or id, e.g. Nora"},
		"due_date":           map[string]any{"type": "string", "description": "YYYY-MM-DD"},
	},
	"required": []string{"text"},
}

func itemsParam(required bool, desc string) mcp.ToolOption {
	opts := []mcp.PropertyOption{mcp.Description(desc), mcp.Items(itemSchema)}
	if required {
		opts = append(opts, mcp.Required())
	}
	return mcp.WithArray("items", opts...)
}

func txFailed(action string, err error) *mcp.CallToolResult { return respond.Failed(action, err) }

// ---------------------------------------------------------------------------
// list_find
// ---------------------------------------------------------------------------

// NewListFindTool creates list_find (class R): the family's lists and
// templates by kind or name (or an item's text), with open item counts.
// Active lists come first, then templates, then archived lists (only with
// include_archived).
func NewListFindTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("list_find", append([]mcp.ToolOption{
		mcp.WithDescription("List the family's checklists and templates by kind or name, with open item counts."),
		mcp.WithString("kind", mcp.Description("Only lists of this kind"), mcp.Enum(kinds...)),
		mcp.WithString("query", mcp.Description("Words in the list's name or an item, e.g. beach"), mcp.MaxLength(200)),
		mcp.WithBoolean("templates_only", mcp.Description("true for only templates (lists to copy with list_create)")),
		mcp.WithBoolean("include_archived", mcp.Description("true to include archived lists (default false)")),
		mcp.WithNumber("limit", mcp.Description("How many lists to return (default 25, max 100)"), mcp.Min(1), mcp.Max(maxFindLimit)),
		mcp.WithString("cursor", mcp.Description("next_cursor from the previous list_find result")),
		respond.ListSchema[listInfo](),
	}, classR("Find lists")...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		hid, ok := householdOf(ctx)
		if !ok {
			return notSignedIn(), nil
		}
		args := req.GetArguments()
		example := map[string]any{"kind": "grocery"}
		kind := ""
		if raw := strings.TrimSpace(str(args, "kind")); raw != "" {
			if kind, ok = parseKind(raw); !ok {
				return respond.Invalid("kind", fmt.Sprintf("kind %q is not a list kind.", raw),
					"Use one of: "+strings.Join(kinds, ", ")+".", example), nil
			}
		}
		query := normListRef(str(args, "query"))
		templatesOnly, _, ok1 := boolArg(args, "templates_only")
		includeArchived, _, ok2 := boolArg(args, "include_archived")
		if !ok1 {
			return respond.Invalid("templates_only", "templates_only must be true or false.", "Pass templates_only=true or leave it out.", map[string]any{"templates_only": true}), nil
		}
		if !ok2 {
			return respond.Invalid("include_archived", "include_archived must be true or false.", "Pass include_archived=true or leave it out.", map[string]any{"include_archived": true}), nil
		}
		limit, given, ok := intArg(args, "limit")
		if !ok || (given && (limit < 1 || limit > maxFindLimit)) {
			return respond.Invalid("limit", fmt.Sprintf("limit must be a whole number from 1 to %d.", maxFindLimit),
				"Pass limit as a number, e.g. 25, or leave it out.", map[string]any{"limit": 25}), nil
		}
		if !given {
			limit = defaultFindLimit
		}
		offset := 0
		if c := strings.TrimSpace(str(args, "cursor")); c != "" {
			if offset, ok = respond.DecodeCursor(c); !ok {
				return respond.Invalid("cursor", "cursor is not one list_find returned.",
					"Pass next_cursor exactly as the previous list_find result gave it, or leave it out to start over.", example), nil
			}
		}
		pattern := ""
		if query != "" {
			pattern = sqlutil.ContainsPattern(query)
		}

		rows, err := pool.Query(ctx, `SELECT `+listCols+`, COUNT(*) OVER ()`+listFrom+`
			WHERE l.household_id = $1
			  AND ($2::text = '' OR l.kind = $2)
			  AND ($3::bool OR NOT l.archived)
			  AND (NOT $4::bool OR l.is_template)
			  AND ($5::text = '' OR l.name ILIKE $5 ESCAPE '\'
			       OR EXISTS (SELECT 1 FROM list_items i WHERE i.list_id = l.id AND i.text ILIKE $5 ESCAPE '\'))
			ORDER BY l.archived, l.is_template, ($5::text <> '' AND l.name ILIKE $5 ESCAPE '\') DESC,
			         l.updated_at DESC, l.id
			LIMIT $6 OFFSET $7`,
			hid, kind, includeArchived, templatesOnly, pattern, limit, offset)
		if err != nil {
			return respond.Failed("list the family's lists", err), nil
		}
		defer rows.Close()
		items := []listInfo{}
		total := 0
		for rows.Next() {
			l, err := scanList(rows, &total)
			if err != nil {
				return respond.Failed("list the family's lists", err), nil
			}
			items = append(items, l)
		}
		if err := rows.Err(); err != nil {
			return respond.Failed("list the family's lists", err), nil
		}

		if len(items) == 0 {
			if offset > 0 {
				return respond.List("No more lists past this cursor.", items, respond.Page{},
					"Leave cursor out to start from the first list."), nil
			}
			next := []string{"Start one with list_create name=… kind=…."}
			if kind != "" {
				next = []string{fmt.Sprintf("Start one with list_create name=%q kind=%q, or add items with list_add_items list_id=%q.", kindTitle[kind], kind, kind)}
			}
			if !templatesOnly {
				next = append(next, "See templates to copy with list_find templates_only=true.")
			}
			return respond.List("No lists found.", items, respond.Page{}, next...), nil
		}

		summary := fmt.Sprintf("Found %d lists.", total)
		if total == 1 {
			summary = fmt.Sprintf("Found 1 list: %q (%d open).", items[0].Name, items[0].OpenCount)
		} else if offset > 0 || offset+len(items) < total {
			summary = fmt.Sprintf("Found %d lists; showing %d–%d.", total, offset+1, offset+len(items))
		}
		first := items[0]
		next := []string{fmt.Sprintf("Read one with list_get list_id=%s.", first.ID)}
		if first.IsTemplate {
			next = append(next, fmt.Sprintf("Start a list from a template with list_create name=… kind=%s from_template_id=%s.", first.Kind, first.ID))
		} else if !first.Archived {
			next = append(next, fmt.Sprintf("Add items with list_add_items list_id=%s items=[…].", first.ID))
		}
		page := respond.Page{}
		if nc := respond.CursorAfter(offset+len(items), total); nc != "" {
			page = respond.Page{Total: total - offset, NextCursor: nc,
				Narrow: fmt.Sprintf("%d more. Pass next_cursor as cursor with the same filters for the next page, or add kind or query to narrow.", total-offset-len(items))}
		}
		return respond.List(summary, items, page, next...), nil
	}
	return "list_find", tool, handler
}

// ---------------------------------------------------------------------------
// list_get
// ---------------------------------------------------------------------------

// NewListGetTool creates list_get (class R): one list with its items in
// order, who added each, and what's checked.
func NewListGetTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("list_get", append([]mcp.ToolOption{
		mcp.WithDescription("Read a list with its items, who added each, and what's checked."),
		listIDParam(),
		mcp.WithString("checked", mcp.Description("Which items: all (default), open or done"), mcp.Enum("all", "open", "done")),
		respond.Schema[listDetail](),
	}, classR("Read list")...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		hid, ok := householdOf(ctx)
		if !ok {
			return notSignedIn(), nil
		}
		args := req.GetArguments()
		example := map[string]any{"list_id": "Grocery", "checked": "open"}
		show := strings.ToLower(strings.TrimSpace(str(args, "checked")))
		switch show {
		case "":
			show = "all"
		case "all", "open", "done":
		case "unchecked", "todo", "left", "remaining":
			show = "open"
		case "checked", "completed", "complete":
			show = "done"
		default:
			return respond.Invalid("checked", fmt.Sprintf("checked %q is not all, open or done.", show),
				"Pass checked=open for what's left, done for what's checked off, or leave it out.", example), nil
		}
		res, err := resolveList(ctx, pool, hid, str(args, "list_id"), example)
		if err != nil {
			return respond.Failed("find the list", err), nil
		}
		if res.Bad != nil {
			return res.Bad, nil
		}
		l, err := getList(ctx, pool, hid, res.List.ID)
		if err != nil {
			return respond.Failed("read the list", err), nil
		}
		filter := map[string]string{"all": "", "open": " AND NOT i.checked", "done": " AND i.checked"}[show]
		rows, err := pool.Query(ctx, `SELECT `+itemCols+itemFrom+` WHERE i.list_id = $1`+filter+itemOrder+` LIMIT $2`,
			res.List.ID, maxGetItems+1)
		if err != nil {
			return respond.Failed("read the list's items", err), nil
		}
		items, err := scanItems(rows)
		if err != nil {
			return respond.Failed("read the list's items", err), nil
		}
		d := listDetail{List: l, Showing: show, Items: items, OpenCount: l.OpenCount, DoneCount: l.ItemCount - l.OpenCount}
		if len(items) > maxGetItems {
			d.Items, d.Truncated = items[:maxGetItems], true
		}

		summary := fmt.Sprintf("%q has %d items, %d open.", l.Name, l.ItemCount, l.OpenCount)
		switch {
		case l.ItemCount == 0:
			summary = fmt.Sprintf("%q is empty.", l.Name)
		case show == "open":
			summary = fmt.Sprintf("%q: %d open of %d.", l.Name, l.OpenCount, l.ItemCount)
		case show == "done":
			summary = fmt.Sprintf("%q: %d checked off of %d.", l.Name, l.ItemCount-l.OpenCount, l.ItemCount)
		}
		if l.IsTemplate {
			summary += " It is a template."
		}
		if l.Archived {
			summary += " It is archived."
		}
		var next []string
		switch {
		case l.IsTemplate:
			next = append(next, fmt.Sprintf("Start a list from it with list_create name=… kind=%s from_template_id=%s.", l.Kind, l.ID))
		case l.Archived:
			next = append(next, fmt.Sprintf("Unarchive it with list_update list_id=%s archived=false to change items.", l.ID))
		case l.OpenCount > 0:
			open, _ := openTexts(ctx, pool, res.List.ID, 2)
			next = append(next, fmt.Sprintf("Check items off with list_check_items list_id=%s texts=%s.", l.ID, quoted(open)))
			next = append(next, fmt.Sprintf("Add more with list_add_items list_id=%s items=[…].", l.ID))
		default:
			next = append(next, fmt.Sprintf("Add items with list_add_items list_id=%s items=[…].", l.ID))
			if l.ItemCount > 0 {
				next = append(next, fmt.Sprintf("Everything is checked off; clear it with list_remove_items list_id=%s clear_checked=true, or archive it with list_update archived=true.", l.ID))
			}
		}
		return respond.OK(summary, d, next...), nil
	}
	return "list_get", tool, handler
}

// ---------------------------------------------------------------------------
// list_create
// ---------------------------------------------------------------------------

// NewListCreateTool creates list_create (class C): a new list, optionally
// copied from a template (its items unchecked) and with first items. The
// catalog loop guard returns the same list (duplicate:true) when the family
// made an active list with the same name and kind in the last 10 minutes.
func NewListCreateTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("list_create", append([]mcp.ToolOption{
		mcp.WithDescription(`Start a list, optionally copied from a template ("Beach trip" from the packing template) and with first items.`),
		mcp.WithString("name", mcp.Description("List name, e.g. Beach trip"), mcp.Required(), mcp.MaxLength(maxNameRunes)),
		mcp.WithString("kind", mcp.Description("What kind of list"), mcp.Required(), mcp.Enum(kinds...)),
		mcp.WithString("from_template_id", mcp.Description("Template to copy: its id or name, e.g. Beach trip packing")),
		itemsParam(false, `First items, e.g. [{"text":"sunscreen"}]`),
		mcp.WithBoolean("is_template", mcp.Description("true to save it as a reusable template")),
		mcp.WithString("about_type", mcp.Description("What it is about (default family)"), mcp.Enum(aboutTypes...)),
		mcp.WithString("about_id", mcp.Description("Id of the row it is about (UUID); needs about_type")),
		respond.Schema[createResult](),
	}, classC("Create list")...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		hid, ok := householdOf(ctx)
		if !ok {
			return notSignedIn(), nil
		}
		args := req.GetArguments()
		example := map[string]any{"name": "Beach trip", "kind": "packing", "from_template_id": "Beach trip packing"}
		name := strings.TrimSpace(spaces.ReplaceAllString(str(args, "name"), " "))
		if utf8.RuneCountInString(name) > maxNameRunes {
			return respond.Invalid("name", fmt.Sprintf("name is %d characters; the limit is %d.", utf8.RuneCountInString(name), maxNameRunes),
				"Use a shorter name.", example), nil
		}
		if _, err := uuid.Parse(name); err == nil {
			return respond.Invalid("name", "name looks like an id.", "Pass a name people would say, e.g. Grocery.", example), nil
		}
		kind := ""
		if raw := strings.TrimSpace(str(args, "kind")); raw != "" {
			if kind, ok = parseKind(raw); !ok {
				return respond.Invalid("kind", fmt.Sprintf("kind %q is not a list kind.", raw),
					"Use one of: "+strings.Join(kinds, ", ")+".", example), nil
			}
		}
		isTemplate, _, ok := boolArg(args, "is_template")
		if !ok {
			return respond.Invalid("is_template", "is_template must be true or false.", "Pass is_template=true or leave it out.", example), nil
		}
		items, bad := parseItems(args["items"], example)
		if bad != nil {
			return bad, nil
		}
		a, bad := parseAbout(args, example)
		if bad != nil {
			return bad, nil
		}
		templateRef := strings.TrimSpace(str(args, "from_template_id"))
		if name == "" && templateRef == "" {
			return respond.Invalid("name", "name is required.", "Pass a name, e.g. Grocery.", example), nil
		}
		if kind == "" && templateRef == "" {
			return respond.Invalid("kind", "kind is required.", "Pass kind: one of "+strings.Join(kinds, ", ")+".", example), nil
		}

		var (
			out       createResult
			badResult *mcp.CallToolResult
			duplicate bool
		)
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var tmpl *listCand
			if templateRef != "" {
				c, bad, err := resolveTemplate(ctx, tx, hid, templateRef, example)
				if err != nil || bad != nil {
					badResult = bad
					return err
				}
				tmpl = &c
				if name == "" {
					name = c.Name
				}
				if kind == "" {
					kind = c.Kind
				}
			}
			for i := range items {
				if items[i].AssigneeRef == "" {
					continue
				}
				id, bad, err := resolvePerson(ctx, tx, hid, fmt.Sprintf("items[%d].assignee_person_id", i), items[i].AssigneeRef, example)
				if err != nil || bad != nil {
					badResult = bad
					return err
				}
				items[i].Assignee = &id
			}

			// Loop guard. The advisory lock makes two identical creates in
			// parallel return one list.
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('list_create:' || $1::text || ':' || lower($2), 0))`,
				hid, name); err != nil {
				return err
			}
			var dupID uuid.UUID
			err := tx.QueryRow(ctx, `SELECT id FROM lists
				WHERE household_id = $1 AND lower(name) = lower($2) AND kind = $3 AND is_template = $4
				  AND NOT archived AND created_at > NOW() - $5::interval
				ORDER BY created_at DESC LIMIT 1`, hid, name, kind, isTemplate, createDedupeWindow).Scan(&dupID)
			switch {
			case err == nil:
				duplicate = true
				l, err := getList(ctx, tx, hid, dupID)
				if err != nil {
					return err
				}
				l.Duplicate = true
				rows, err := tx.Query(ctx, `SELECT `+itemCols+itemFrom+` WHERE i.list_id = $1`+itemOrder, dupID)
				if err != nil {
					return err
				}
				its, err := scanItems(rows)
				out = createResult{List: l, Items: its}
				return err
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}

			var entityType *string
			var entityID *uuid.UUID
			if a.Type != "" && a.Type != "family" {
				entityType, entityID = &a.Kind, &a.ID
			}
			var id uuid.UUID
			if err := tx.QueryRow(ctx,
				`INSERT INTO lists (household_id, name, kind, entity_type, entity_id, is_template)
				 VALUES ($1, $2, $3, $4::entity_kind, $5, $6) RETURNING id`,
				hid, name, kind, entityType, entityID, isTemplate).Scan(&id); err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, it := range items {
				k := normText(it.Text)
				if seen[k] {
					continue
				}
				seen[k] = true
				if err := insertItem(ctx, tx, id, hid, it); err != nil {
					return err
				}
			}
			if tmpl != nil {
				// position NULL lets the 046 trigger append after the given
				// items, in the template's order (one INSERT ... SELECT runs
				// row by row; see KB apis/lists.md gotchas).
				if _, err := tx.Exec(ctx, `
					INSERT INTO list_items (list_id, household_id, text, quantity, unit, section,
					                        assignee_person_id, due_date)
					SELECT $1, i.household_id, i.text, i.quantity, i.unit, i.section,
					       i.assignee_person_id, i.due_date
					  FROM list_items i
					 WHERE i.list_id = $2 AND i.household_id = $3
					   AND NOT (lower(btrim(i.text)) = ANY($4))`+itemOrder,
					id, tmpl.ID, hid, keys(seen)); err != nil {
					return err
				}
				n := tmpl.Name
				out.FromTemplate = &n
			}
			l, err := getList(ctx, tx, hid, id)
			if err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT `+itemCols+itemFrom+` WHERE i.list_id = $1`+itemOrder, id)
			if err != nil {
				return err
			}
			out.List = l
			out.Items, err = scanItems(rows)
			return err
		})
		if badResult != nil {
			return badResult, nil
		}
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				switch {
				case pgErr.Code == "23503" && a.Type != "":
					return respond.Invalid("about_id", fmt.Sprintf("No %s with id %s was found in this family.", a.Type, a.ID),
						fmt.Sprintf("Pass a %s of this family (%s), or leave about_type out for a family list.", a.Type, findIDWith(a.Type)), example), nil
				case pgErr.Code == "23514" && pgErr.ConstraintName == "entity_kind_check":
					return respond.Invalid("about_type", fmt.Sprintf("Lists about a %s can't be saved yet.", a.Type),
						fmt.Sprintf("Leave about_type and about_id out to save it as a family list, and name the %s in the list name.", a.Type), example), nil
				}
			}
			return respond.Failed("create the list", err), nil
		}

		l := out.List
		if duplicate {
			return respond.OK(fmt.Sprintf("List %q (%s) was already created minutes ago; returning it instead of a copy.", l.Name, l.Kind), out,
				fmt.Sprintf("Add items with list_add_items list_id=%s items=[…].", l.ID),
				fmt.Sprintf("Rename or archive it with list_update list_id=%s.", l.ID)), nil
		}
		summary := fmt.Sprintf("Created %s list %q", l.Kind, l.Name)
		if out.FromTemplate != nil {
			summary += fmt.Sprintf(" from %q", *out.FromTemplate)
		}
		if l.IsTemplate {
			summary = strings.Replace(summary, "Created", "Created template", 1)
		}
		summary += fmt.Sprintf(" with %d items.", l.ItemCount)
		if l.ItemCount == 0 {
			summary = strings.TrimSuffix(summary, fmt.Sprintf(" with %d items.", 0)) + "."
		}
		next := []string{fmt.Sprintf("Add items with list_add_items list_id=%s items=[…].", l.ID)}
		if l.ItemCount > 0 {
			next = append(next, fmt.Sprintf("Check items off with list_check_items list_id=%s texts=[…].", l.ID))
		}
		return respond.OK(summary, out, next...), nil
	}
	return "list_create", tool, handler
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func insertItem(ctx context.Context, tx pgx.Tx, listID, hid uuid.UUID, it newItem) error {
	_, err := insertItemID(ctx, tx, listID, hid, it)
	return err
}

func insertItemID(ctx context.Context, tx pgx.Tx, listID, hid uuid.UUID, it newItem) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO list_items (list_id, household_id, text, quantity, unit, section, assignee_person_id, due_date)
		VALUES ($1, $2, $3, $4::numeric, $5, $6, $7, $8::date) RETURNING id`,
		listID, hid, it.Text, it.Quantity, it.Unit, it.Section, it.Assignee, it.DueDate).Scan(&id)
	return id, err
}

// ---------------------------------------------------------------------------
// list_add_items
// ---------------------------------------------------------------------------

// NewListAddItemsTool creates list_add_items (class U): several items in
// one call. list_id may name a kind ("grocery"); when the family has no
// active list of that kind, one is created. An item already open on the
// list is not added again, and one that was checked off is put back
// (unchecked) instead of duplicated, so a repeated call changes nothing.
func NewListAddItemsTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("list_add_items", append([]mcp.ToolOption{
		mcp.WithDescription(`Add one or more items in one call, e.g. milk and eggs to "Grocery". Items already open on the list are not duplicated.`),
		listIDParam(),
		itemsParam(true, `Items to add, e.g. [{"text":"milk"},{"text":"eggs","quantity":12}]`),
		respond.Schema[addResult](),
	}, classU("Add list items")...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		hid, ok := householdOf(ctx)
		if !ok {
			return notSignedIn(), nil
		}
		args := req.GetArguments()
		example := map[string]any{"list_id": "Grocery", "items": []any{map[string]any{"text": "milk"}, map[string]any{"text": "eggs"}}}
		items, bad := parseItems(args["items"], example)
		if bad != nil {
			return bad, nil
		}
		if len(items) == 0 {
			return respond.Invalid("items", "items is empty.", `Pass at least one item, e.g. items=[{"text":"milk"}].`, example), nil
		}

		var (
			out       addResult
			badResult *mcp.CallToolResult
		)
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			res, err := resolveList(ctx, tx, hid, str(args, "list_id"), example)
			if err != nil {
				return err
			}
			if res.Bad != nil && res.MissingKind == "" {
				badResult = res.Bad
				return nil
			}
			if res.MissingKind != "" {
				// "add milk to the grocery list" with no grocery list yet:
				// make it. The advisory lock stops two parallel adds from
				// making two lists.
				kind := res.MissingKind
				if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('list_kind:' || $1::text || ':' || $2, 0))`, hid, kind); err != nil {
					return err
				}
				var c listCand
				err := tx.QueryRow(ctx, `SELECT id, name, kind, is_template, archived FROM lists
					WHERE household_id = $1 AND kind = $2 AND NOT archived AND NOT is_template
					ORDER BY updated_at DESC, id LIMIT 1`, hid, kind).Scan(&c.ID, &c.Name, &c.Kind, &c.IsTemplate, &c.Archived)
				switch {
				case errors.Is(err, pgx.ErrNoRows):
					c = listCand{Name: kindTitle[kind], Kind: kind}
					if err := tx.QueryRow(ctx, `INSERT INTO lists (household_id, name, kind) VALUES ($1, $2, $3) RETURNING id`,
						hid, c.Name, kind).Scan(&c.ID); err != nil {
						return err
					}
					out.CreatedList = true
				case err != nil:
					return err
				}
				res.List = c
			}
			list := res.List
			if bad, err := writableList(ctx, tx, hid, list, true); err != nil || bad != nil {
				badResult = bad
				return err
			}
			for i := range items {
				if items[i].AssigneeRef == "" {
					continue
				}
				id, bad, err := resolvePerson(ctx, tx, hid, fmt.Sprintf("items[%d].assignee_person_id", i), items[i].AssigneeRef, example)
				if err != nil || bad != nil {
					badResult = bad
					return err
				}
				items[i].Assignee = &id
			}

			// Under the list lock no other adder runs, so this duplicate
			// check is race-free.
			existing, err := listItemKeys(ctx, tx, list.ID)
			if err != nil {
				return err
			}
			open, done := map[string]uuid.UUID{}, map[string]uuid.UUID{}
			for _, k := range existing {
				n := normText(k.Text)
				if k.Checked {
					if _, ok := done[n]; !ok {
						done[n] = k.ID
					}
				} else if _, ok := open[n]; !ok {
					open[n] = k.ID
				}
			}
			var added, reopened, already []uuid.UUID
			seen := map[string]bool{}
			for _, it := range items {
				n := normText(it.Text)
				if seen[n] {
					continue
				}
				seen[n] = true
				if id, ok := open[n]; ok {
					already = append(already, id)
					continue
				}
				if id, ok := done[n]; ok {
					// Back on the list: one row, unchecked, with any new
					// details. Only that row is written.
					if _, err := tx.Exec(ctx, `UPDATE list_items SET checked = false,
						  quantity = COALESCE($2::numeric, quantity), unit = COALESCE($3, unit),
						  section = COALESCE($4, section), assignee_person_id = COALESCE($5, assignee_person_id),
						  due_date = COALESCE($6::date, due_date)
						WHERE id = $1`, id, it.Quantity, it.Unit, it.Section, it.Assignee, it.DueDate); err != nil {
						return err
					}
					reopened = append(reopened, id)
					continue
				}
				id, err := insertItemID(ctx, tx, list.ID, hid, it)
				if err != nil {
					return err
				}
				added = append(added, id)
			}
			if out.Added, err = itemsByID(ctx, tx, added); err != nil {
				return err
			}
			if out.Reopened, err = itemsByID(ctx, tx, reopened); err != nil {
				return err
			}
			if out.AlreadyOnList, err = itemsByID(ctx, tx, already); err != nil {
				return err
			}
			out.List, err = getList(ctx, tx, hid, list.ID)
			return err
		})
		if badResult != nil {
			return badResult, nil
		}
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				return respond.Invalid("items", "An assignee is not a person of this family.",
					"Find people with person_find, or leave assignee_person_id out.", example), nil
			}
			return respond.Failed("add the items", err), nil
		}

		l := out.List
		var parts []string
		if len(out.Added) > 0 {
			parts = append(parts, fmt.Sprintf("Added %s to %q", joinTexts(itemTexts(out.Added)), l.Name))
		}
		if len(out.Reopened) > 0 {
			parts = append(parts, fmt.Sprintf("put %s back on %q", joinTexts(itemTexts(out.Reopened)), l.Name))
		}
		if len(out.AlreadyOnList) > 0 {
			verb := "is"
			if len(out.AlreadyOnList) > 1 {
				verb = "are"
			}
			parts = append(parts, fmt.Sprintf("%s %s already on %q", joinTexts(itemTexts(out.AlreadyOnList)), verb, l.Name))
		}
		summary := strings.Join(parts, "; ")
		summary = strings.ToUpper(summary[:1]) + summary[1:] + fmt.Sprintf(" (%d open).", l.OpenCount)
		if out.CreatedList {
			summary = fmt.Sprintf("Created the %s list %q. ", l.Kind, l.Name) + summary
		}
		first := out.Added
		if len(first) == 0 {
			first = append(out.Reopened, out.AlreadyOnList...)
		}
		next := []string{
			fmt.Sprintf("Check items off with list_check_items list_id=%s texts=%s.", l.ID, quoted([]string{first[0].Text})),
			fmt.Sprintf("See the whole list with list_get list_id=%s.", l.ID),
		}
		return respond.OK(summary, out, next...), nil
	}
	return "list_add_items", tool, handler
}

// ---------------------------------------------------------------------------
// list_check_items
// ---------------------------------------------------------------------------

// NewListCheckItemsTool creates list_check_items (class U): check items off
// (or uncheck them with checked=false) by id or by text. Only the matched
// items' rows are locked and written, so people checking off different
// items at once never overwrite each other. Items already in the wanted
// state are left alone.
func NewListCheckItemsTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("list_check_items", append([]mcp.ToolOption{
		mcp.WithDescription(`Check off (or uncheck) items by id or by text match, e.g. texts=["turkey"]. Several at once.`),
		listIDParam(),
		mcp.WithArray("item_ids", mcp.Description("Item ids from list_get"), mcp.WithStringItems()),
		mcp.WithArray("texts", mcp.Description(`Item texts to match, e.g. ["turkey","milk"]`), mcp.WithStringItems()),
		mcp.WithBoolean("checked", mcp.Description("true to check off (default), false to uncheck")),
		respond.Schema[checkResult](),
	}, classU("Check off list items")...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		hid, ok := householdOf(ctx)
		if !ok {
			return notSignedIn(), nil
		}
		args := req.GetArguments()
		example := map[string]any{"list_id": "Grocery", "texts": []string{"milk"}}
		ids, texts, bad := idsAndTexts(args, example)
		if bad != nil {
			return bad, nil
		}
		if len(ids) == 0 && len(texts) == 0 {
			return respond.Invalid("texts", "Pass item_ids or texts.", `Name the items to check off, e.g. texts=["milk"].`, example), nil
		}
		checked, given, ok := boolArg(args, "checked")
		if !ok {
			return respond.Invalid("checked", "checked must be true or false.", "Pass checked=false to uncheck, or leave it out to check off.", example), nil
		}
		if !given {
			checked = true
		}

		var (
			out       checkResult
			badResult *mcp.CallToolResult
		)
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			res, err := resolveList(ctx, tx, hid, str(args, "list_id"), example)
			if err != nil || res.Bad != nil {
				badResult = res.Bad
				return err
			}
			list := res.List
			// No list lock: check-offs must not queue behind adders.
			if bad, err := writableList(ctx, tx, hid, list, false); err != nil || bad != nil {
				badResult = bad
				return err
			}
			itemIDs, bad, err := resolveItems(ctx, tx, list.ID, list.Name, ids, texts, &checked, example)
			if err != nil || bad != nil {
				badResult = bad
				return err
			}
			ok, err := lockItems(ctx, tx, list.ID, itemIDs)
			if err != nil {
				return err
			}
			if !ok {
				badResult = respond.Invalid("item_ids", "Some of those items were just removed from the list.",
					fmt.Sprintf("Read the list again with list_get list_id=%s and retry.", list.ID), example)
				return nil
			}
			rows, err := tx.Query(ctx, `UPDATE list_items SET checked = $3
				WHERE list_id = $1 AND id = ANY($2) AND checked <> $3 RETURNING id`, list.ID, itemIDs, checked)
			if err != nil {
				return err
			}
			changed := map[uuid.UUID]bool{}
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				changed[id] = true
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			all, err := itemsByID(ctx, tx, itemIDs)
			if err != nil {
				return err
			}
			out.Changed, out.Unchanged = []item{}, []item{}
			for _, it := range all {
				if changed[uuid.MustParse(it.ID)] {
					out.Changed = append(out.Changed, it)
				} else {
					out.Unchanged = append(out.Unchanged, it)
				}
			}
			out.Checked = checked
			if out.Left, err = openTexts(ctx, tx, list.ID, 20); err != nil {
				return err
			}
			out.List, err = getList(ctx, tx, hid, list.ID)
			return err
		})
		if badResult != nil {
			return badResult, nil
		}
		if err != nil {
			return respond.Failed("check off the items", err), nil
		}

		l := out.List
		verb, state := "Checked off", "checked off"
		if !checked {
			verb, state = "Unchecked", "unchecked"
		}
		var parts []string
		if len(out.Changed) > 0 {
			parts = append(parts, fmt.Sprintf("%s %s on %q.", verb, joinTexts(itemTexts(out.Changed)), l.Name))
		}
		if len(out.Unchanged) > 0 {
			parts = append(parts, fmt.Sprintf("%s was already %s.", joinTexts(itemTexts(out.Unchanged)), state))
		}
		left := leftLine(l.OpenCount, firstN(out.Left, 5))
		summary := strings.Join(parts, " ") + " " + left
		next := []string{left}
		if l.OpenCount == 0 {
			next = append(next, fmt.Sprintf("Clear checked items with list_remove_items list_id=%s clear_checked=true, or archive the list with list_update list_id=%s archived=true.", l.ID, l.ID))
		} else if checked {
			next = append(next, fmt.Sprintf("Undo with list_check_items list_id=%s texts=%s checked=false.", l.ID, quoted(itemTexts(out.Changed))))
		}
		if len(out.Changed) == 0 {
			next = next[:1]
		}
		return respond.OK(summary, out, next...), nil
	}
	return "list_check_items", tool, handler
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// idsAndTexts reads item_ids[] and texts[] (arrays, or one comma string).
func idsAndTexts(args map[string]any, example map[string]any) ([]string, []string, *mcp.CallToolResult) {
	ids, ok := stringList(args["item_ids"])
	if !ok {
		return nil, nil, respond.Invalid("item_ids", "item_ids must be a list of item ids.", `Pass item_ids as ["<id>"], or use texts instead.`, example)
	}
	if v, has := args["item_id"]; has && len(ids) == 0 { // hidden alias
		ids, _ = stringList(v)
	}
	texts, ok := stringList(args["texts"])
	if !ok {
		return nil, nil, respond.Invalid("texts", "texts must be a list of item texts.", `Pass texts as ["milk","eggs"].`, example)
	}
	if v, has := args["text"]; has && len(texts) == 0 { // hidden alias
		texts, _ = stringList(v)
	}
	if len(ids)+len(texts) > maxBatch {
		return nil, nil, respond.Invalid("texts", fmt.Sprintf("More than %d items in one call.", maxBatch), "Split the call in two.", example)
	}
	return ids, texts, nil
}

// ---------------------------------------------------------------------------
// list_update_item
// ---------------------------------------------------------------------------

// NewListUpdateItemTool creates list_update_item (class U): change one
// item's text, quantity, unit, section, assignee or due date. Only the given
// fields change (an empty string means "not given"; null clears a field),
// in a single-row UPDATE.
func NewListUpdateItemTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("list_update_item", append([]mcp.ToolOption{
		mcp.WithDescription("Change one item's text, quantity, section, assignee or due date."),
		mcp.WithString("item_id", mcp.Description("Item id from list_get"), mcp.Required()),
		mcp.WithString("text", mcp.Description("New text, e.g. oat milk"), mcp.MaxLength(maxTextRunes)),
		mcp.WithNumber("quantity", mcp.Description("How many, e.g. 2; null clears it")),
		mcp.WithString("unit", mcp.Description("Unit for quantity, e.g. lb; null clears it")),
		mcp.WithString("section", mcp.Description("Aisle or room, e.g. Dairy; null clears it")),
		mcp.WithString("assignee_person_id", mcp.Description("Who does it: person name or id; null clears it")),
		mcp.WithString("due_date", mcp.Description("YYYY-MM-DD; null clears it")),
		respond.Schema[itemResult](),
	}, classU("Update list item")...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		hid, ok := householdOf(ctx)
		if !ok {
			return notSignedIn(), nil
		}
		args := req.GetArguments()
		example := map[string]any{"item_id": exampleListID, "quantity": 2}
		rawID := strings.TrimSpace(str(args, "item_id"))
		if rawID == "" {
			rawID = strings.TrimSpace(str(args, "id")) // hidden alias
		}
		itemID, err := uuid.Parse(rawID)
		if err != nil {
			return respond.Invalid("item_id", fmt.Sprintf("item_id %q is not an item id.", rawID),
				"Get item ids with list_get list_id=<list name>.", example), nil
		}

		// field state: absent/"" = keep, null = clear, value = set.
		type field struct {
			set   bool
			value any
		}
		fields := map[string]field{}
		textField := func(name string, max int) *mcp.CallToolResult {
			v, has := args[name]
			if !has {
				return nil
			}
			if v == nil {
				fields[name] = field{set: true}
				return nil
			}
			s, ok := v.(string)
			if !ok {
				return respond.Invalid(name, name+" must be text.", "Pass "+name+" as a string, or null to clear it.", example)
			}
			s = strings.TrimSpace(s)
			if s == "" {
				return nil
			}
			if utf8.RuneCountInString(s) > max {
				return respond.Invalid(name, fmt.Sprintf("%s is longer than %d characters.", name, max), "Shorten it.", example)
			}
			fields[name] = field{set: true, value: s}
			return nil
		}
		for name, max := range map[string]int{"unit": maxUnitRunes, "section": maxSectionRunes, "assignee_person_id": 200, "due_date": 10} {
			if bad := textField(name, max); bad != nil {
				return bad, nil
			}
		}
		if f, ok := fields["due_date"]; ok && f.value != nil {
			if _, err := time.Parse("2006-01-02", f.value.(string)); err != nil {
				return respond.Invalid("due_date", fmt.Sprintf("due_date %q is not a date as YYYY-MM-DD.", f.value), "Pass due_date as YYYY-MM-DD, e.g. 2026-11-26.", example), nil
			}
		}
		var text *string
		if s := strings.TrimSpace(spaces.ReplaceAllString(str(args, "text"), " ")); s != "" {
			if utf8.RuneCountInString(s) > maxTextRunes {
				return respond.Invalid("text", fmt.Sprintf("text is longer than %d characters.", maxTextRunes), "Shorten it.", example), nil
			}
			text = &s
		}
		if v, has := args["quantity"]; has {
			switch {
			case v == nil:
				fields["quantity"] = field{set: true}
			case v == "":
			default:
				f, ok := numArg(v)
				if !ok || f <= 0 {
					return respond.Invalid("quantity", "quantity must be a number above 0.", "Pass quantity as a number, e.g. 2, or null to clear it.", example), nil
				}
				fields["quantity"] = field{set: true, value: f}
			}
		}
		if text == nil && len(fields) == 0 {
			return respond.Invalid("text", "Nothing to change.", "Pass at least one of text, quantity, unit, section, assignee_person_id or due_date.", example), nil
		}

		var (
			out       itemResult
			badResult *mcp.CallToolResult
		)
		err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var c listCand
			err := tx.QueryRow(ctx, `SELECT l.id, l.name, l.kind, l.is_template, l.archived
				FROM list_items i JOIN lists l ON l.id = i.list_id
				WHERE i.id = $1 AND i.household_id = $2`, itemID, hid).Scan(&c.ID, &c.Name, &c.Kind, &c.IsTemplate, &c.Archived)
			if errors.Is(err, pgx.ErrNoRows) {
				badResult = respond.Invalid("item_id", fmt.Sprintf("No list item with id %s was found in this family.", itemID),
					"Get item ids with list_get list_id=<list name>.", example)
				return nil
			}
			if err != nil {
				return err
			}
			if c.Archived {
				badResult = archivedResult(c)
				return nil
			}
			var assignee *uuid.UUID
			if f, ok := fields["assignee_person_id"]; ok && f.value != nil {
				id, bad, err := resolvePerson(ctx, tx, hid, "assignee_person_id", f.value.(string), example)
				if err != nil || bad != nil {
					badResult = bad
					return err
				}
				assignee = &id
			}
			val := func(name string) any { return fields[name].value }
			set := func(name string) bool { return fields[name].set }
			if _, err := tx.Exec(ctx, `
				UPDATE list_items SET
				  text               = COALESCE($3, text),
				  quantity           = CASE WHEN $5  THEN $4::numeric ELSE quantity END,
				  unit               = CASE WHEN $7  THEN $6::text ELSE unit END,
				  section            = CASE WHEN $9  THEN $8::text ELSE section END,
				  assignee_person_id = CASE WHEN $11 THEN $10::uuid ELSE assignee_person_id END,
				  due_date           = CASE WHEN $13 THEN $12::date ELSE due_date END
				WHERE id = $1 AND household_id = $2`,
				itemID, hid, text,
				val("quantity"), set("quantity"), val("unit"), set("unit"), val("section"), set("section"),
				assignee, set("assignee_person_id"), val("due_date"), set("due_date")); err != nil {
				return err
			}
			its, err := itemsByID(ctx, tx, []uuid.UUID{itemID})
			if err != nil {
				return err
			}
			out.Item = its[0]
			out.List, err = getList(ctx, tx, hid, c.ID)
			return err
		})
		if badResult != nil {
			return badResult, nil
		}
		if err != nil {
			return respond.Failed("update the item", err), nil
		}
		return respond.OK(fmt.Sprintf("Updated %q on %q.", out.Item.Text, out.List.Name), out,
			fmt.Sprintf("See the whole list with list_get list_id=%s.", out.List.ID),
			fmt.Sprintf("Check it off with list_check_items list_id=%s item_ids=[%q].", out.List.ID, out.Item.ID)), nil
	}
	return "list_update_item", tool, handler
}

// ---------------------------------------------------------------------------
// list_remove_items
// ---------------------------------------------------------------------------

// NewListRemoveItemsTool creates list_remove_items (class X): delete items
// by id or text, or every checked item with clear_checked. The matched
// items are locked in id order and deleted in one transaction.
func NewListRemoveItemsTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("list_remove_items", append([]mcp.ToolOption{
		mcp.WithDescription("Remove items from a list by id or text, or remove all checked items with clear_checked."),
		listIDParam(),
		mcp.WithArray("item_ids", mcp.Description("Item ids from list_get"), mcp.WithStringItems()),
		mcp.WithArray("texts", mcp.Description(`Item texts to match, e.g. ["eggs"]`), mcp.WithStringItems()),
		mcp.WithBoolean("clear_checked", mcp.Description("true to remove every checked-off item")),
		respond.Schema[removeResult](),
	}, classX("Remove list items")...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		hid, ok := householdOf(ctx)
		if !ok {
			return notSignedIn(), nil
		}
		args := req.GetArguments()
		example := map[string]any{"list_id": "Grocery", "texts": []string{"eggs"}}
		ids, texts, bad := idsAndTexts(args, example)
		if bad != nil {
			return bad, nil
		}
		clear, _, ok := boolArg(args, "clear_checked")
		if !ok {
			return respond.Invalid("clear_checked", "clear_checked must be true or false.", "Pass clear_checked=true or leave it out.", example), nil
		}
		if len(ids) == 0 && len(texts) == 0 && !clear {
			return respond.Invalid("texts", "Pass item_ids, texts or clear_checked=true.", `Name the items to remove, e.g. texts=["eggs"].`, example), nil
		}

		var (
			out       removeResult
			badResult *mcp.CallToolResult
		)
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			res, err := resolveList(ctx, tx, hid, str(args, "list_id"), example)
			if err != nil || res.Bad != nil {
				badResult = res.Bad
				return err
			}
			list := res.List
			if bad, err := writableList(ctx, tx, hid, list, false); err != nil || bad != nil {
				badResult = bad
				return err
			}
			itemIDs, bad, err := resolveItems(ctx, tx, list.ID, list.Name, ids, texts, nil, example)
			if err != nil || bad != nil {
				badResult = bad
				return err
			}
			if clear {
				rows, err := tx.Query(ctx, `SELECT id FROM list_items WHERE list_id = $1 AND checked ORDER BY id`, list.ID)
				if err != nil {
					return err
				}
				seen := map[uuid.UUID]bool{}
				for _, id := range itemIDs {
					seen[id] = true
				}
				for rows.Next() {
					var id uuid.UUID
					if err := rows.Scan(&id); err != nil {
						rows.Close()
						return err
					}
					if !seen[id] {
						itemIDs = append(itemIDs, id)
					}
				}
				rows.Close()
				if err := rows.Err(); err != nil {
					return err
				}
			}
			if len(itemIDs) > 0 {
				ok, err := lockItems(ctx, tx, list.ID, itemIDs)
				if err != nil {
					return err
				}
				if !ok {
					badResult = respond.Invalid("item_ids", "Some of those items were just removed by someone else.",
						fmt.Sprintf("Read the list again with list_get list_id=%s and retry.", list.ID), example)
					return nil
				}
				if out.Removed, err = itemsByID(ctx, tx, itemIDs); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `DELETE FROM list_items WHERE list_id = $1 AND id = ANY($2)`, list.ID, itemIDs); err != nil {
					return err
				}
			} else {
				out.Removed = []item{}
			}
			out.List, err = getList(ctx, tx, hid, list.ID)
			return err
		})
		if badResult != nil {
			return badResult, nil
		}
		if err != nil {
			return respond.Failed("remove the items", err), nil
		}
		l := out.List
		summary := fmt.Sprintf("Removed %s from %q (%d open, %d items left).", joinTexts(itemTexts(out.Removed)), l.Name, l.OpenCount, l.ItemCount)
		if len(out.Removed) == 0 {
			summary = fmt.Sprintf("Nothing to remove: %q has no checked items.", l.Name)
		} else if len(out.Removed) > 5 {
			summary = fmt.Sprintf("Removed %d items from %q (%d open, %d items left).", len(out.Removed), l.Name, l.OpenCount, l.ItemCount)
		}
		return respond.OK(summary, out, fmt.Sprintf("See what's left with list_get list_id=%s.", l.ID)), nil
	}
	return "list_remove_items", tool, handler
}

// ---------------------------------------------------------------------------
// list_update
// ---------------------------------------------------------------------------

// NewListUpdateTool creates list_update (class U): rename a list, change its
// kind, or archive/unarchive it. Archived lists stay readable; their items
// are frozen until unarchived.
func NewListUpdateTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("list_update", append([]mcp.ToolOption{
		mcp.WithDescription("Rename a list, change its kind, or archive it (archived:true). Archived lists stay readable."),
		listIDParam(),
		mcp.WithString("name", mcp.Description("New name, e.g. Thanksgiving groceries"), mcp.MaxLength(maxNameRunes)),
		mcp.WithString("kind", mcp.Description("New kind"), mcp.Enum(kinds...)),
		mcp.WithBoolean("archived", mcp.Description("true to archive, false to unarchive")),
		respond.Schema[listInfo](),
	}, classU("Update list")...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		hid, ok := householdOf(ctx)
		if !ok {
			return notSignedIn(), nil
		}
		args := req.GetArguments()
		example := map[string]any{"list_id": "Beach trip", "archived": true}
		var name, kind *string
		if s := strings.TrimSpace(spaces.ReplaceAllString(str(args, "name"), " ")); s != "" {
			if utf8.RuneCountInString(s) > maxNameRunes {
				return respond.Invalid("name", fmt.Sprintf("name is longer than %d characters.", maxNameRunes), "Use a shorter name.", example), nil
			}
			if _, err := uuid.Parse(s); err == nil {
				return respond.Invalid("name", "name looks like an id.", "Pass the new name people would say, e.g. Grocery.", example), nil
			}
			name = &s
		}
		if raw := strings.TrimSpace(str(args, "kind")); raw != "" {
			k, ok := parseKind(raw)
			if !ok {
				return respond.Invalid("kind", fmt.Sprintf("kind %q is not a list kind.", raw), "Use one of: "+strings.Join(kinds, ", ")+".", example), nil
			}
			kind = &k
		}
		archivedVal, given, ok := boolArg(args, "archived")
		if !ok {
			return respond.Invalid("archived", "archived must be true or false.", "Pass archived=true to archive, false to unarchive.", example), nil
		}
		var archived *bool
		if given {
			archived = &archivedVal
		}
		if name == nil && kind == nil && archived == nil {
			return respond.Invalid("name", "Nothing to change.", "Pass name, kind or archived.", example), nil
		}

		res, err := resolveList(ctx, pool, hid, str(args, "list_id"), example)
		if err != nil {
			return respond.Failed("find the list", err), nil
		}
		if res.Bad != nil {
			return res.Bad, nil
		}
		before := res.List
		if _, err := pool.Exec(ctx, `UPDATE lists SET name = COALESCE($3, name), kind = COALESCE($4, kind), archived = COALESCE($5, archived)
			WHERE id = $1 AND household_id = $2`, before.ID, hid, name, kind, archived); err != nil {
			return respond.Failed("update the list", err), nil
		}
		l, err := getList(ctx, pool, hid, before.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.Invalid("list_id", fmt.Sprintf("List %q was just deleted.", before.Name), "Find the family's lists with list_find.", example), nil
		}
		if err != nil {
			return respond.Failed("read the list", err), nil
		}

		var changes []string
		if name != nil && *name != before.Name {
			changes = append(changes, fmt.Sprintf("renamed it to %q", l.Name))
		}
		if kind != nil && *kind != before.Kind {
			changes = append(changes, "made it a "+l.Kind+" list")
		}
		if archived != nil && *archived != before.Archived {
			if l.Archived {
				changes = append(changes, "archived it")
			} else {
				changes = append(changes, "unarchived it")
			}
		}
		summary := fmt.Sprintf("List %q: %s.", before.Name, joinTexts(changes))
		if len(changes) == 0 {
			summary = fmt.Sprintf("List %q already looked like that; nothing changed.", l.Name)
		}
		next := []string{fmt.Sprintf("Read it with list_get list_id=%s.", l.ID)}
		if l.Archived {
			next = append(next, fmt.Sprintf("Unarchive it with list_update list_id=%s archived=false.", l.ID))
		}
		return respond.OK(summary, l, next...), nil
	}
	return "list_update", tool, handler
}
