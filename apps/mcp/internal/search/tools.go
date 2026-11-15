// Package search provides search_all (KB architecture/mcp-tool-catalog.md,
// "search: one box over everything"): one case-insensitive text search across
// every area of the family's data, with results grouped by area. Each hit
// has a title, a short snippet around the match, and an `open` hint that
// names the tool and id that read the whole record. Calendar events also carry
// their start in the family timezone, local_date, weekday and calendar_name.
//
// Matching is a substring match in SQL (ILIKE). LIKE wildcards in the query
// are escaped, so "%" or "_" match literally. The catalog row does not call
// for semantic search; the Typesense hybrid index is meant for document_find
// (S2 #4477). All queries are scoped to the caller's household.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/calendar"
	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/sqlutil"
)

const (
	toolName = "search_all"

	defaultPerType = 5
	maxPerType     = 20

	// snippetBefore and snippetLen size the snippet window around the first
	// match, in characters.
	snippetBefore = 60
	snippetLen    = 200
)

// area is one searchable area of the catalog. The SQL fragments refer to the
// searched table as t. Title and Doc must yield text; Doc is the searchable
// text (its NULL parts are skipped by concat_ws). OpenRef is the value that
// goes into Open, usually the row id.
type area struct {
	Name    string // catalog area name, the value accepted in types[]
	Plural  string // used in summaries: "2 notes"
	From    string // FROM clause, aliasing the searched table as t
	Scope   string // the household id column the query filters on
	Title   string
	Doc     string
	OpenRef string
	Open    string // fmt template with one %s for OpenRef
	Sort    string // newest first within the same title/text rank
}

// areas lists every area search_all covers, in result order (the catalog's
// order: people, events, money, contacts, notes, lists, recipes, documents,
// then things). "event" has no SQL spec: events live as JSON in
// calendar_objects and are matched by searchEvents.
var areas = []area{
	{Name: "person", Plural: "people", From: "people t", Scope: "t.household_id",
		Title: "t.display_name", Doc: "concat_ws(' ', t.display_name, t.relationship, t.school, t.notes)",
		OpenRef: "t.id", Open: "person_find id=%s", Sort: "t.created_at"},
	{Name: "event", Plural: "events"},
	{Name: "bill", Plural: "bills", From: "bills t", Scope: "t.household_id",
		Title: "t.name", Doc: "concat_ws(' ', t.name, t.category, t.notes)",
		OpenRef: "t.id", Open: "bill_find id=%s", Sort: "t.created_at"},
	{Name: "loan", Plural: "loans", From: "loans t", Scope: "t.household_id",
		Title: "t.name", Doc: "concat_ws(' ', t.name, t.lender, t.notes)",
		OpenRef: "t.id", Open: "loan_find id=%s", Sort: "t.created_at"},
	{Name: "maintenance", Plural: "maintenance tasks", From: "maintenance_tasks t", Scope: "t.household_id",
		Title: "t.name", Doc: "concat_ws(' ', t.name, t.description, t.notes)",
		OpenRef: "t.id", Open: "maintenance_find id=%s", Sort: "t.created_at"},
	{Name: "contact", Plural: "contacts", From: "vendors t", Scope: "t.household_id",
		Title: "t.name", Doc: "concat_ws(' ', t.name, t.specialty, array_to_string(t.trades, ' '), t.notes)",
		OpenRef: "t.id", Open: "contact_find id=%s", Sort: "t.created_at"},
	{Name: "note", Plural: "notes", From: "notes t", Scope: "t.household_id",
		Title: "COALESCE(NULLIF(btrim(t.title), ''), '(untitled)')", Doc: "concat_ws(' ', t.title, t.body)",
		OpenRef: "t.id", Open: "note_get note_id=%s", Sort: "t.updated_at"},
	{Name: "list", Plural: "lists", From: "lists t", Scope: "t.household_id",
		Title: "t.name",
		Doc:   "concat_ws(' ', t.name, (SELECT string_agg(i.text, ' ' ORDER BY i.position) FROM list_items i WHERE i.list_id = t.id))",
		// No list tool is registered yet: plain text, like credential, until
		// list_get is (then restore "list_get list_id=%s" with OpenRef t.id).
		OpenRef: "t.name", Open: "Tell the user it is their list %q in Home OS; opening lists here is coming soon.", Sort: "t.updated_at"},
	{Name: "recipe", Plural: "recipes", From: "recipes t", Scope: "t.household_id",
		Title: "t.title",
		Doc: "concat_ws(' ', t.title, t.description, array_to_string(t.tags, ' '), " +
			"(SELECT string_agg(e->>'name', ' ') FROM jsonb_array_elements(t.ingredients) e))",
		// No recipe tool is registered yet: plain text until recipe_get is.
		OpenRef: "t.title", Open: "Tell the user it is their recipe %q in Home OS; opening recipes here is coming soon.", Sort: "t.created_at"},
	{Name: "document", Plural: "documents", From: "files t", Scope: "t.household_id",
		// 048 document metadata: title (falls back to the file name),
		// category and description are searched before the extracted text.
		Title:   "COALESCE(NULLIF(btrim(t.title), ''), t.name)",
		Doc:     "concat_ws(' ', t.title, t.name, t.category, t.description, array_to_string(t.tags, ' '), t.extracted_text)",
		OpenRef: "t.id", Open: "document_get document_id=%s", Sort: "t.created_at"},
	{Name: "asset", Plural: "things", From: "assets t", Scope: "t.household_id",
		Title: "t.name", Doc: "concat_ws(' ', t.name, t.category, t.manufacturer, t.model, t.notes)",
		OpenRef: "t.id", Open: "asset_find id=%s", Sort: "t.created_at"},
	{Name: "vehicle", Plural: "vehicles", From: "vehicles t", Scope: "t.household_id",
		Title:   "concat_ws(' ', t.year::text, t.make, t.model)",
		Doc:     "concat_ws(' ', t.year::text, t.make, t.model, t.color, t.license_plate, t.notes)",
		OpenRef: "t.id", Open: "vehicle_find id=%s", Sort: "t.created_at"},
	{Name: "pet", Plural: "pets", From: "pets t", Scope: "t.household_id",
		Title: "t.name", Doc: "concat_ws(' ', t.name, t.species, t.breed, t.notes)",
		OpenRef: "t.id", Open: "pet_find id=%s", Sort: "t.created_at"},
	{Name: "property", Plural: "properties", From: "properties t", Scope: "t.household_id",
		Title: "t.name", Doc: "concat_ws(' ', t.name, t.address, t.notes)",
		OpenRef: "t.id", Open: "property_find id=%s", Sort: "t.created_at"},
	// rooms.household_id stays nullable until migration 056, so rooms are
	// scoped through their property, which is authoritative. A room is opened
	// through its property (property_find returns its rooms).
	{Name: "room", Plural: "rooms", From: "rooms t JOIN properties p ON p.id = t.property_id", Scope: "p.household_id",
		Title: "t.name || ' (' || p.name || ')'", Doc: "concat_ws(' ', t.name, t.notes)",
		OpenRef: "t.property_id", Open: "property_find id=%s", Sort: "t.created_at"},
	// Credentials: only the name and type are searched or returned, never the
	// secret value (encrypted_data is not read).
	{Name: "credential", Plural: "credentials", From: "secrets t", Scope: "t.household_id",
		Title: "t.name", Doc: "concat_ws(' ', t.name, t.secret_type)",
		OpenRef: "t.name", Open: "Tell the user it is saved in Home OS as %q; agents never see the secret value.", Sort: "t.created_at"},
}

// noOpenTool is the types whose open is plain text for the user, not a tool
// call, so they are left out of the "Open … with …" hints.
var noOpenTool = map[string]bool{"credential": true, "list": true, "recipe": true}

// areaNames is the types[] enum, in result order.
var areaNames = func() []string {
	names := make([]string, len(areas))
	for i, a := range areas {
		names[i] = a.Name
	}
	return names
}()

// searchHit is one matching record.
type searchHit struct {
	ID           string `json:"id" jsonschema_description:"The record's id; see open for the tool that reads it."`
	Title        string `json:"title"`
	Snippet      string `json:"snippet" jsonschema_description:"Text around the first match; … marks cut text."`
	Open         string `json:"open" jsonschema_description:"The call that reads the whole record, e.g. note_get note_id=…"`
	Start        string `json:"start,omitempty" jsonschema_description:"Events only: start in the family timezone (RFC 3339)."`
	LocalDate    string `json:"local_date,omitempty" jsonschema_description:"Events only: the start day, YYYY-MM-DD, family timezone."`
	Weekday      string `json:"weekday,omitempty" jsonschema_description:"Events only: weekday of local_date, e.g. Wednesday."`
	CalendarName string `json:"calendar_name,omitempty" jsonschema_description:"Events only: the event's calendar."`
}

// searchGroup holds the hits of one area.
type searchGroup struct {
	Type      string      `json:"type" jsonschema_description:"The area: person, event, bill, note, ..."`
	Returned  int         `json:"returned"`
	Total     int         `json:"total" jsonschema_description:"How many records of this type match in all."`
	Truncated bool        `json:"truncated,omitempty" jsonschema_description:"true when total > returned; pass types=[this type] and a higher limit."`
	Items     []searchHit `json:"items"`
}

// searchResult is search_all's data. Groups holds only the areas with a
// match, in catalog order.
type searchResult struct {
	Query    string        `json:"query"`
	Searched []string      `json:"searched" jsonschema_description:"The areas that were searched."`
	Total    int           `json:"total" jsonschema_description:"Matches across all groups, including ones not returned."`
	Groups   []searchGroup `json:"groups"`
}

// NewSearchTool creates search_all (catalog v2, class R).
func NewSearchTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool(toolName,
		mcp.WithDescription("Search everything when you don't know where it lives: people, events, bills, contacts, "+
			"notes, lists, recipes, documents, things. Results grouped by type, each with a snippet and how to open it."),
		mcp.WithString("query",
			mcp.Description("Words to look for, matched anywhere in the text, e.g. plumber"),
			mcp.Required(),
		),
		mcp.WithArray("types",
			mcp.Description("Only search these areas (default all), e.g. [\"note\",\"event\"]"),
			mcp.WithStringEnumItems(areaNames),
		),
		mcp.WithNumber("limit",
			mcp.Description(fmt.Sprintf("Most results per type (default %d, max %d)", defaultPerType, maxPerType)),
			mcp.Min(1), mcp.Max(maxPerType),
		),
		mcp.WithTitleAnnotation("Search everything"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.Schema[searchResult](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil || claims.HouseholdID == "" {
			return respond.Fail(respond.Error{
				Message: "You are not signed in to a household.",
				Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
			}), nil
		}

		args := req.GetArguments()
		query, _ := args["query"].(string)
		query = strings.TrimSpace(query)
		if query == "" {
			return respond.Invalid("query", "query is required.",
				"Pass the words to look for, e.g. a name or a word from a note.",
				map[string]any{"query": "plumber"}), nil
		}
		types, err := parseTypes(args["types"])
		if err != nil {
			return respond.Invalid("types", err.Error(),
				"Pass types as a list of: "+strings.Join(areaNames, ", ")+"; or leave it out to search everything.",
				map[string]any{"query": query, "types": []string{"note", "event"}}), nil
		}
		limit, ok := limitArg(args["limit"])
		if !ok {
			return respond.Invalid("limit", fmt.Sprintf("limit must be a whole number from 1 to %d.", maxPerType),
				"Pass limit as a number, e.g. 5, or leave it out.",
				map[string]any{"query": query, "limit": defaultPerType}), nil
		}

		want := map[string]bool{}
		for _, t := range types {
			want[t] = true
		}
		out := searchResult{Query: query, Searched: types, Groups: []searchGroup{}}
		for _, a := range areas {
			if !want[a.Name] {
				continue
			}
			var g searchGroup
			if a.Name == "event" {
				g, err = searchEventGroup(ctx, pool, claims.HouseholdID, query, limit)
			} else {
				g, err = searchArea(ctx, pool, a, claims.HouseholdID, query, limit)
			}
			if err != nil {
				return respond.Failed("search "+a.Plural, err), nil
			}
			if g.Total == 0 {
				continue
			}
			out.Total += g.Total
			out.Groups = append(out.Groups, g)
		}

		return respond.OK(summarize(out), out, nextHints(out, limit)...), nil
	}

	return toolName, tool, handler
}

// searchArea runs the escaped ILIKE search over one SQL area. Records whose
// title matches come first, then the newest.
func searchArea(ctx context.Context, pool *pgxpool.Pool, a area, householdID, query string, limit int) (searchGroup, error) {
	sql := fmt.Sprintf(`
		SELECT s.id, s.title, s.open_ref,
		       substr(s.doc, greatest(m.pos - %[7]d, 1), %[8]d),
		       greatest(m.pos - %[7]d, 1),
		       length(s.doc),
		       count(*) OVER ()
		FROM (
			SELECT %[1]s::text AS id, COALESCE(%[2]s, '') AS title, %[3]s::text AS open_ref,
			       COALESCE(%[4]s, '') AS doc, %[5]s AS sort_at
			FROM %[6]s
			WHERE %[9]s = $1
		) s
		CROSS JOIN LATERAL (SELECT strpos(lower(s.doc), lower($3)) AS pos) m
		WHERE s.doc ILIKE $2 ESCAPE '\'
		ORDER BY (s.title ILIKE $2 ESCAPE '\') DESC, s.sort_at DESC, s.id
		LIMIT $4`,
		"t.id", a.Title, a.OpenRef, a.Doc, a.Sort, a.From, snippetBefore, snippetLen, a.Scope)

	rows, err := pool.Query(ctx, sql, householdID, sqlutil.ContainsPattern(query), query, limit)
	if err != nil {
		return searchGroup{}, fmt.Errorf("query %s: %w", a.Name, err)
	}
	defer rows.Close()

	g := searchGroup{Type: a.Name, Items: []searchHit{}}
	for rows.Next() {
		var (
			h                hitRow
			openRef, snippet string
		)
		if err := rows.Scan(&h.id, &h.title, &openRef, &snippet, &h.from, &h.docLen, &g.Total); err != nil {
			return searchGroup{}, fmt.Errorf("scan %s: %w", a.Name, err)
		}
		g.Items = append(g.Items, searchHit{
			ID:      h.id,
			Title:   h.title,
			Snippet: tidySnippet(snippet, h.from > 1, h.from-1+len([]rune(snippet)) < h.docLen),
			Open:    fmt.Sprintf(a.Open, openRef),
		})
	}
	if err := rows.Err(); err != nil {
		return searchGroup{}, fmt.Errorf("iterate %s: %w", a.Name, err)
	}
	g.Returned = len(g.Items)
	g.Truncated = g.Total > g.Returned
	return g, nil
}

// hitRow holds the scanned position data of one SQL hit.
type hitRow struct {
	id, title string
	from      int // 1-based start of the snippet in the searched text
	docLen    int // length of the searched text, in characters
}

// searchEventGroup searches calendar events and places each hit's start in
// the family timezone, with the same rules as the calendar tools.
func searchEventGroup(ctx context.Context, pool *pgxpool.Pool, householdID, query string, limit int) (searchGroup, error) {
	events, total, err := searchEvents(ctx, pool, householdID, query, limit)
	if err != nil {
		return searchGroup{}, err
	}
	g := searchGroup{Type: "event", Total: total, Items: make([]searchHit, 0, len(events))}
	if len(events) == 0 {
		return g, nil
	}
	loc := calendar.LoadHouseholdLocation(ctx, pool, householdID)
	for _, e := range events {
		start, localDate, weekday := calendar.LocalDay(e.Start, e.End, e.AllDay, loc)
		g.Items = append(g.Items, searchHit{
			ID: e.ID, Title: e.Title, Snippet: e.Snippet, Open: "event_find id=" + e.ID,
			Start: start, LocalDate: localDate, Weekday: weekday, CalendarName: e.CalendarName,
		})
	}
	g.Returned = len(g.Items)
	g.Truncated = g.Total > g.Returned
	return g, nil
}

// eventScanLimit bounds how many household calendar objects are read and
// decoded per search. Household calendars are small (hundreds of events), so
// this is a safety net rather than a real constraint.
const eventScanLimit = 5000

// eventHit is a calendar event matching a search.
type eventHit struct {
	ID           string
	Title        string
	Snippet      string
	Start        string
	End          string
	AllDay       bool
	CalendarName string
}

// eventFields is the subset of the ical_data JSON document that search reads.
type eventFields struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Location    string `json:"location"`
	Start       string `json:"start"`
	End         string `json:"end"`
	AllDay      bool   `json:"all_day"`
}

// searchEvents returns up to limit of the household's calendar events whose
// title, description or location contains query (case-insensitive), newest
// first, and how many match in all.
//
// Events are not an area spec because they need a JOIN for household scoping
// (calendar_objects has no household_id; it belongs to calendars) and their
// fields live in the ical_data TEXT column as JSON. The JSON is decoded in Go
// rather than with a ::jsonb cast so a single malformed row is skipped instead
// of failing the whole search, and so matching only looks at the three
// searchable fields (never JSON keys like "title" or other fields).
//
// When query contains no characters JSON would escape, a raw ILIKE over
// ical_data narrows the rows first; a substring of a field value then also
// appears verbatim in the encoded document.
func searchEvents(ctx context.Context, pool *pgxpool.Pool, householdID, query string, limit int) ([]eventHit, int, error) {
	sql := `
		SELECT co.id::text, co.ical_data, c.name
		FROM calendar_objects co
		JOIN calendars c ON c.id = co.calendar_id
		WHERE c.household_id = $1`
	args := []any{householdID}
	if !needsJSONEscape(query) {
		sql += ` AND co.ical_data ILIKE $2 ESCAPE '\'`
		args = append(args, sqlutil.ContainsPattern(query))
	}
	sql += fmt.Sprintf(` ORDER BY co.created_at DESC LIMIT %d`, eventScanLimit)

	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query calendar_objects: %w", err)
	}
	defer rows.Close()

	hits := []eventHit{}
	total := 0
	for rows.Next() {
		var id, raw, calendarName string
		if err := rows.Scan(&id, &raw, &calendarName); err != nil {
			return nil, 0, fmt.Errorf("scan calendar_objects: %w", err)
		}
		var f eventFields
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			continue // not a JSON event document; nothing searchable
		}
		doc := strings.Join(nonEmpty(f.Title, f.Description, f.Location), " ")
		snippet, ok := snippetAround(doc, query)
		if !ok {
			continue
		}
		total++
		if len(hits) < limit {
			hits = append(hits, eventHit{ID: id, Title: f.Title, Snippet: snippet, Start: f.Start, End: f.End, AllDay: f.AllDay, CalendarName: calendarName})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate calendar_objects: %w", err)
	}
	return hits, total, nil
}

// parseTypes accepts a list of area names, or a single (possibly
// comma-separated) string, which small models often send instead. Names are
// matched in any case, and a trailing plural "s" is forgiven ("notes"). The
// result is in catalog order without duplicates; nothing given means every
// area.
func parseTypes(raw any) ([]string, error) {
	var names []string
	switch v := raw.(type) {
	case nil:
	case string:
		names = strings.Split(v, ",")
	case []string:
		names = v
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("types must be a list of area names; %v is not a name.", item)
			}
			names = append(names, s)
		}
	default:
		return nil, fmt.Errorf("types must be a list of area names, not %T.", raw)
	}

	want := map[string]bool{}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		name, ok := areaAlias(n)
		if !ok {
			return nil, fmt.Errorf("%q is not a type search_all knows.", n)
		}
		want[name] = true
	}
	if len(want) == 0 {
		return append([]string{}, areaNames...), nil
	}
	out := []string{}
	for _, a := range areaNames {
		if want[a] {
			out = append(out, a)
		}
	}
	return out, nil
}

// areaAlias resolves an area name, its plural, or a common other word for it
// to the catalog area name.
func areaAlias(n string) (string, bool) {
	for _, a := range areas {
		if n == a.Name || n == a.Plural || n == a.Name+"s" {
			return a.Name, true
		}
	}
	switch n {
	case "thing", "things":
		return "asset", true
	case "vendor", "vendors":
		return "contact", true
	case "file", "files":
		return "document", true
	case "secret", "secrets", "password", "passwords":
		return "credential", true
	case "member", "members", "people":
		return "person", true
	}
	return "", false
}

// limitArg reads limit (per type) sent as a JSON number or a numeric string.
func limitArg(raw any) (int, bool) {
	var n int
	switch v := raw.(type) {
	case nil:
		return defaultPerType, true
	case float64:
		if v != float64(int(v)) {
			return 0, false
		}
		n = int(v)
	case int:
		n = v
	case string:
		if strings.TrimSpace(v) == "" {
			return defaultPerType, true
		}
		var err error
		if n, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return n, n >= 1 && n <= maxPerType
}

// summarize is the one-line summary: what matched, per type.
func summarize(r searchResult) string {
	if r.Total == 0 {
		return fmt.Sprintf("Nothing matched %q.", r.Query)
	}
	parts := make([]string, 0, len(r.Groups))
	for _, g := range r.Groups {
		parts = append(parts, fmt.Sprintf("%d %s", g.Total, countNoun(g)))
	}
	noun := "matches"
	if r.Total == 1 {
		noun = "match"
	}
	return fmt.Sprintf("Found %d %s for %q: %s.", r.Total, noun, r.Query, strings.Join(parts, ", "))
}

// countNoun is the group's area noun, singular for one match.
func countNoun(g searchGroup) string {
	for _, a := range areas {
		if a.Name != g.Type {
			continue
		}
		if g.Total != 1 {
			return a.Plural
		}
		if a.Name == "asset" {
			return "thing"
		}
		return a.Name
	}
	return g.Type
}

// nextHints gives 1–3 next steps that name real tools and ids.
func nextHints(r searchResult, limit int) []string {
	if r.Total == 0 {
		return []string{
			"Try one shorter word or another spelling; matching is by substring.",
			"Use family_overview to see what the family has saved.",
		}
	}
	next := []string{}
	for _, g := range r.Groups {
		if len(next) == 2 {
			break
		}
		if len(g.Items) > 0 && !noOpenTool[g.Type] {
			next = append(next, fmt.Sprintf("Open %q with %s.", g.Items[0].Title, g.Items[0].Open))
		}
	}
	for _, g := range r.Groups {
		if g.Truncated {
			more := limit * 2
			if more > maxPerType {
				more = maxPerType
			}
			next = append(next, fmt.Sprintf("More %s match: call search_all with types=[%q] and limit=%d, or a longer query.", countNoun(searchGroup{Type: g.Type}), g.Type, more))
			break
		}
	}
	if len(next) == 0 {
		next = append(next, r.Groups[0].Items[0].Open)
	}
	return next
}

// needsJSONEscape reports whether encoding s as a JSON string might change any
// of its characters (some writers escape non-ASCII as \uXXXX), in which case a
// raw substring match on the stored JSON could miss a real hit.
func needsJSONEscape(s string) bool {
	for _, r := range s {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '<' || r == '>' || r == '&' {
			return true
		}
	}
	return false
}

// snippetAround returns the snippet window around the first case-insensitive
// occurrence of query in doc, and whether query occurs at all.
func snippetAround(doc, query string) (string, bool) {
	d, q := []rune(doc), []rune(strings.ToLower(query))
	lower := []rune(strings.ToLower(doc))
	if len(lower) != len(d) { // lowering changed the length; fall back to rune-wise
		lower = make([]rune, len(d))
		for i, r := range d {
			lower[i] = unicode.ToLower(r)
		}
		q = []rune(query)
		for i, r := range q {
			q[i] = unicode.ToLower(r)
		}
	}
	pos := indexRunes(lower, q)
	if pos < 0 {
		return "", false
	}
	from := max(pos-snippetBefore, 0)
	to := min(from+snippetLen, len(d))
	return tidySnippet(string(d[from:to]), from > 0, to < len(d)), true
}

// indexRunes is strings.Index over rune slices.
func indexRunes(s, sub []rune) int {
	if len(sub) == 0 {
		return 0
	}
outer:
	for i := 0; i+len(sub) <= len(s); i++ {
		for j := range sub {
			if s[i+j] != sub[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

// tidySnippet collapses whitespace (newlines included) and marks cut ends
// with "…". A cut start drops the partial first word; a cut end drops the
// partial last word.
func tidySnippet(s string, cutStart, cutEnd bool) string {
	words := strings.Fields(s)
	if cutStart && len(words) > 1 && !unicode.IsSpace(firstRune(s)) {
		words = words[1:]
	}
	if cutEnd && len(words) > 1 && !unicode.IsSpace(lastRune(s)) {
		words = words[:len(words)-1]
	}
	out := strings.Join(words, " ")
	if cutStart {
		out = "…" + out
	}
	if cutEnd {
		out += "…"
	}
	return out
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return ' '
}

func lastRune(s string) rune {
	r := []rune(s)
	if len(r) == 0 {
		return ' '
	}
	return r[len(r)-1]
}

// nonEmpty returns the non-empty strings among ss.
func nonEmpty(ss ...string) []string {
	out := ss[:0:0]
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
