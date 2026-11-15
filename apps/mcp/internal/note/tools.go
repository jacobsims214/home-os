// Package note provides the MCP note tools of catalog v2 (KB
// architecture/mcp-tool-catalog.md): note_find, note_get, note_create,
// note_update and note_delete. A note is about the family (the default) or
// about one row of the same family, named by about_type and about_id. Every
// result is built with internal/respond, and every tool declares its
// annotations and outputSchema. All operations are scoped to the caller's
// household.
package note

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
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
)

// noteColumns is the SELECT list scanNote reads. It needs
// "notes n LEFT JOIN users u ON u.id = n.author_id JOIN households h ON
// h.id = n.household_id" in the FROM clause (n may be a CTE with the notes
// columns). Local dates and weekdays are computed in the family's timezone.
const noteColumns = `n.id, n.entity_type, n.entity_id, n.household_id, n.title, n.body,
	n.author_id, u.name, n.created_at, n.updated_at,
	to_char(n.created_at AT TIME ZONE h.timezone, 'YYYY-MM-DD'),
	to_char(n.created_at AT TIME ZONE h.timezone, 'Dy'),
	to_char(n.updated_at AT TIME ZONE h.timezone, 'YYYY-MM-DD'),
	to_char(n.updated_at AT TIME ZONE h.timezone, 'Dy')`

// noteJoins completes a FROM clause that starts with "<table or CTE> n".
const noteJoins = ` LEFT JOIN users u ON u.id = n.author_id JOIN households h ON h.id = n.household_id`

// exampleNoteID is the note_id used in error examples.
const exampleNoteID = "3f2b8c1e-5d4a-4b7e-9c2a-1e6f0a9b8d7c"

// noteResult is one note in full, as returned by note_get, note_create,
// note_update and note_delete. IDs are strings, not uuid.UUID: the
// outputSchema is inferred with jsonschema-go, which sees uuid.UUID as a
// [16]byte array and would reject the string UUIDs that are on the wire.
type noteResult struct {
	ID               string    `json:"id" jsonschema_description:"The note's id; pass it as note_id."`
	AboutType        string    `json:"about_type" jsonschema_description:"What the note is about: family, person, property, vehicle, ..."`
	AboutID          *string   `json:"about_id" jsonschema_description:"Id of the row it is about; null for family notes."`
	Title            *string   `json:"title"`
	Body             string    `json:"body"`
	AuthorID         *string   `json:"author_id"`
	AuthorName       *string   `json:"author_name"`
	CreatedAt        time.Time `json:"created_at"`
	CreatedLocalDate string    `json:"created_local_date" jsonschema_description:"created_at as YYYY-MM-DD in the family timezone."`
	CreatedWeekday   string    `json:"created_weekday" jsonschema_description:"Weekday of created_local_date, e.g. Thu."`
	UpdatedAt        time.Time `json:"updated_at"`
	UpdatedLocalDate string    `json:"updated_local_date" jsonschema_description:"updated_at as YYYY-MM-DD in the family timezone."`
	UpdatedWeekday   string    `json:"updated_weekday" jsonschema_description:"Weekday of updated_local_date, e.g. Thu."`
	Duplicate        bool      `json:"duplicate,omitempty" jsonschema_description:"true when note_create returned an identical note saved minutes ago instead of a copy."`
}

// noteSummary is one note_find item: a preview, not the whole body.
type noteSummary struct {
	ID               string    `json:"id" jsonschema_description:"The note's id; pass it as note_id."`
	AboutType        string    `json:"about_type"`
	AboutID          *string   `json:"about_id" jsonschema_description:"Id of the row it is about; null for family notes."`
	Title            *string   `json:"title"`
	Preview          string    `json:"preview" jsonschema_description:"The first 100 characters of the body. Read all of it with note_get."`
	AuthorName       *string   `json:"author_name"`
	UpdatedAt        time.Time `json:"updated_at"`
	UpdatedLocalDate string    `json:"updated_local_date" jsonschema_description:"updated_at as YYYY-MM-DD in the family timezone."`
	UpdatedWeekday   string    `json:"updated_weekday" jsonschema_description:"Weekday of updated_local_date, e.g. Thu."`
}

// scanNote reads noteColumns from row, then any extra columns into extra.
func scanNote(row pgx.Row, extra ...any) (noteResult, error) {
	var (
		n                         noteResult
		id, entityID, householdID uuid.UUID
		entityType                string
		authorID                  *uuid.UUID
	)
	dest := append([]any{&id, &entityType, &entityID, &householdID, &n.Title, &n.Body,
		&authorID, &n.AuthorName, &n.CreatedAt, &n.UpdatedAt,
		&n.CreatedLocalDate, &n.CreatedWeekday, &n.UpdatedLocalDate, &n.UpdatedWeekday}, extra...)
	if err := row.Scan(dest...); err != nil {
		return noteResult{}, err
	}
	n.ID = id.String()
	n.AboutType, n.AboutID = aboutFromDB(entityType, entityID, householdID)
	if authorID != nil {
		s := authorID.String()
		n.AuthorID = &s
	}
	return n, nil
}

func (n noteResult) summary() noteSummary {
	return noteSummary{
		ID: n.ID, AboutType: n.AboutType, AboutID: n.AboutID, Title: n.Title,
		Preview: truncate(n.Body, 100), AuthorName: n.AuthorName,
		UpdatedAt: n.UpdatedAt, UpdatedLocalDate: n.UpdatedLocalDate, UpdatedWeekday: n.UpdatedWeekday,
	}
}

// displayTitle is the title for summaries; NULL or empty is "(untitled)".
func (n noteResult) displayTitle() string {
	if n.Title != nil && *n.Title != "" {
		return *n.Title
	}
	return "(untitled)"
}

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
// list, recipe and guide have no registered finder yet; the ticket that
// registers list_find, recipe_find or guide_find adds it back here.
var aboutFinder = map[string]string{
	"person": "person_find", "property": "property_find", "room": "property_find",
	"asset": "asset_find", "vehicle": "vehicle_find", "pet": "pet_find",
	"contact": "contact_find", "bill": "bill_find", "loan": "loan_find",
	"maintenance": "maintenance_find", "event": "event_find",
}

// findIDWith says how to get an about_type's id: its finder tool, or the
// user when no finder is registered yet.
func findIDWith(t string) string {
	if tool, ok := aboutFinder[t]; ok {
		return "find it with " + tool
	}
	return "ask the user for it; looking up a " + t + " by name is coming soon"
}

// aboutFromDB maps a stored (entity_type, entity_id) to the catalog's
// (about_type, about_id). Family notes have no about_id.
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

// about is a parsed about_type/about_id pair.
type about struct {
	Type string    // catalog name, e.g. "contact"
	Kind string    // DB entity_kind, e.g. "vendor"
	ID   uuid.UUID // the row; uuid.Nil when only the type was given (find)
}

func validAboutType(t string) bool {
	for _, a := range aboutTypes {
		if a == t {
			return true
		}
	}
	return false
}

// parseAbout reads about_type and about_id. requireID is true for
// note_create, where any about_type but family needs an about_id. An empty
// about_type with no about_id returns ok with a zero about (the caller's
// default). A failed parse returns the isError result to send.
func parseAbout(args map[string]any, requireID bool, example map[string]any) (about, *mcp.CallToolResult) {
	t := strings.ToLower(strings.TrimSpace(str(args, "about_type")))
	rawID := strings.TrimSpace(str(args, "about_id"))
	if t == "" && rawID == "" {
		return about{}, nil
	}
	if t == "" {
		return about{}, respond.Invalid("about_type", "about_id was given without about_type.",
			"Pass about_type too (e.g. vehicle), or leave both out for a family note.", example)
	}
	if !validAboutType(t) {
		return about{}, respond.Invalid("about_type", fmt.Sprintf("about_type %q is not one of the known kinds.", t),
			"Use one of: "+strings.Join(aboutTypes, ", ")+".", example)
	}
	a := about{Type: t, Kind: t}
	if k, ok := aboutToDB[t]; ok {
		a.Kind = k
	}
	if t == "family" {
		return a, nil // the family's id comes from the caller's household
	}
	if rawID == "" {
		if !requireID {
			return a, nil
		}
		return about{}, respond.Invalid("about_id", fmt.Sprintf("about_type %s needs about_id.", t),
			fmt.Sprintf("Pass the %s's id as about_id (%s), or leave about_type out for a family note.", t, findIDWith(t)),
			example)
	}
	id, err := uuid.Parse(rawID)
	if err != nil {
		return about{}, respond.Invalid("about_id", fmt.Sprintf("about_id %q is not a UUID.", rawID),
			fmt.Sprintf("Pass the %s's id (a UUID), not its name (%s).", t, findIDWith(t)), example)
	}
	a.ID = id
	return a, nil
}

// str returns args[key] as a string ("" when missing or not a string).
func str(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

// notSignedIn is the result for calls without household claims.
func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}

const (
	defaultFindLimit = 25
	maxFindLimit     = 100
)

// encodeCursor and decodeCursor make the opaque note_find cursor: the offset
// of the next page.
func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(offset)))
}

func decodeCursor(c string) (int, bool) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || !strings.HasPrefix(string(b), "o:") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(string(b), "o:"))
	return n, err == nil && n >= 0
}

// intArg reads a numeric argument sent as a JSON number or a numeric string.
func intArg(args map[string]any, key string) (int, bool, bool) {
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

// NewNoteFindTool creates note_find (catalog v2, class R). It lists the
// household's notes, newest change first, optionally filtered by text
// (query, matched case-insensitively in title and body) and by what they
// are about. Items carry a 100-character preview; note_get reads a whole
// note. Paging uses limit (default 25, max 100) and an opaque cursor that a
// truncated result hands back in narrow and next.
func NewNoteFindTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("note_find",
		mcp.WithDescription("List or search notes by text or what they're about. Returns titles and short previews; read a whole note with note_get."),
		mcp.WithString("query",
			mcp.Description("Words to find in the title or body, e.g. wifi"),
		),
		mcp.WithString("about_type",
			mcp.Description("Only notes about this kind of thing; family = family-wide notes"),
			mcp.Enum(aboutTypes...),
		),
		mcp.WithString("about_id",
			mcp.Description("Only notes about this row (UUID); needs about_type"),
		),
		mcp.WithNumber("limit",
			mcp.Description("How many notes to return (default 25, max 100)"),
			mcp.Min(1), mcp.Max(maxFindLimit),
		),
		mcp.WithString("cursor",
			mcp.Description("Paging cursor from the previous note_find result"),
		),
		mcp.WithTitleAnnotation("Find notes"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.ListSchema[noteSummary](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}
		householdID, err := uuid.Parse(claims.HouseholdID)
		if err != nil {
			return notSignedIn(), nil
		}

		args := req.GetArguments()
		example := map[string]any{"query": "wifi", "about_type": "property"}
		query := strings.TrimSpace(str(args, "query"))
		a, bad := parseAbout(args, false, example)
		if bad != nil {
			return bad, nil
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
			if offset, ok = decodeCursor(c); !ok {
				return respond.Invalid("cursor", "cursor is not one note_find returned.",
					"Pass the cursor exactly as the previous note_find result gave it, or leave it out to start over.",
					map[string]any{"query": "wifi"}), nil
			}
		}

		var kind *string
		var aboutID *uuid.UUID
		if a.Type != "" {
			kind = &a.Kind
			switch {
			case a.Type == "family":
				aboutID = &householdID
			case a.ID != uuid.Nil:
				aboutID = &a.ID
			}
		}

		// entity_type is compared as text: a kind the DB does not know yet
		// (e.g. guide) simply matches nothing instead of failing the cast.
		rows, err := pool.Query(ctx,
			`SELECT `+noteColumns+`, COUNT(*) OVER ()
			 FROM notes n`+noteJoins+`
			 WHERE n.household_id = $1
			   AND ($2::text IS NULL OR n.entity_type::text = $2::text)
			   AND ($3::uuid IS NULL OR n.entity_id = $3::uuid)
			   AND ($4::text = '' OR position(lower($4::text) IN lower(coalesce(n.title, '') || ' ' || n.body)) > 0)
			 ORDER BY n.updated_at DESC, n.id
			 LIMIT $5 OFFSET $6`,
			householdID, kind, aboutID, query, limit, offset,
		)
		if err != nil {
			return respond.Failed("list notes", err), nil
		}
		defer rows.Close()

		items := []noteSummary{}
		total := 0
		for rows.Next() {
			n, err := scanNote(rows, &total)
			if err != nil {
				return respond.Failed("list notes", err), nil
			}
			items = append(items, n.summary())
		}
		if err := rows.Err(); err != nil {
			return respond.Failed("list notes", err), nil
		}

		if len(items) == 0 {
			if offset > 0 {
				return respond.List("No more notes past this cursor.", items, respond.Page{},
					"Leave cursor out to start from the newest note."), nil
			}
			return respond.List("No notes found.", items, respond.Page{},
				"Save one with note_create.",
				"Try note_find without filters, or search_all to look everywhere."), nil
		}

		next := []string{fmt.Sprintf("Read one in full with note_get note_id=%s.", items[0].ID)}
		remaining := total - offset - len(items)
		summary := fmt.Sprintf("Found %d notes.", total)
		if offset > 0 || remaining > 0 {
			summary = fmt.Sprintf("Found %d notes; showing %d–%d.", total, offset+1, offset+len(items))
		}
		if total == 1 {
			summary = fmt.Sprintf("Found 1 note: %q.", noteResult{Title: items[0].Title}.displayTitle())
		}
		page := respond.Page{}
		if remaining > 0 {
			c := encodeCursor(offset + len(items))
			page = respond.Page{
				Total:  len(items) + remaining,
				Narrow: fmt.Sprintf("%d more. Pass cursor=%q with the same filters for the next page, or add query or about_type to narrow.", remaining, c),
			}
			next = append(next, fmt.Sprintf("Next page: note_find cursor=%s with the same filters.", c))
		}
		return respond.List(summary, items, page, next...), nil
	}

	return "note_find", tool, handler
}

// maxNoteTitleRunes is the notes.title column limit (VARCHAR(500)).
const maxNoteTitleRunes = 500

// createDedupeWindow is how far back note_create looks for an identical
// note by the same member (catalog v2 loop guard).
const createDedupeWindow = "10 minutes"

// titleTooLong is the isError result for a title over maxNoteTitleRunes.
func titleTooLong(title string, example map[string]any) *mcp.CallToolResult {
	return respond.Invalid("title",
		fmt.Sprintf("title is %d characters; the limit is %d.", utf8.RuneCountInString(title), maxNoteTitleRunes),
		"Shorten the title and put the details in the body.", example)
}

// looksLikeChecklist is true for a body of at least 3 short non-empty lines.
func looksLikeChecklist(body string) bool {
	lines := 0
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if utf8.RuneCountInString(l) > 60 {
			return false
		}
		lines++
	}
	return lines >= 3
}

// NewNoteCreateTool creates note_create (catalog v2, class C). It saves a
// note authored by the caller, about the family (the default) or about one
// row of the family named by about_type and about_id. The DB rejects rows
// of other families (migration 038, SQLSTATE 23503) and kinds it does not
// know yet (entity_kind domain, 23514); both come back as isError results
// naming about_id or about_type. Loop guard: an identical note (same title,
// body and subject) the same member saved in the last 10 minutes is
// returned with duplicate:true instead of a second row.
func NewNoteCreateTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("note_create",
		mcp.WithDescription("Save a note for the family, optionally about a person, house, car, etc. Checklists (grocery, chores, packing) are coming soon; for now save them as a note."),
		mcp.WithString("title",
			mcp.Description("Short title, max 500 characters, e.g. Wi-Fi password"),
			mcp.Required(),
		),
		mcp.WithString("content",
			mcp.Description("The note's body (markdown is fine)"),
			mcp.Required(),
		),
		mcp.WithString("about_type",
			mcp.Description("What it's about (default family); others need about_id"),
			mcp.Enum(aboutTypes...),
		),
		mcp.WithString("about_id",
			mcp.Description("Id (UUID) of the person, property, vehicle, ... it's about"),
		),
		mcp.WithTitleAnnotation("Create note"),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.Schema[noteResult](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}
		householdID, err := uuid.Parse(claims.HouseholdID)
		if err != nil {
			return notSignedIn(), nil
		}

		args := req.GetArguments()
		title := strings.TrimSpace(str(args, "title"))
		content := str(args, "content")
		example := map[string]any{"title": "Wi-Fi", "content": "Network HomeNet, password on the router"}
		if title == "" {
			return respond.Invalid("title", "title is required.",
				"Pass a short title, e.g. \"Wi-Fi\".", example), nil
		}
		if strings.TrimSpace(content) == "" {
			return respond.Invalid("content", "content is required.",
				"Pass the note's text as content.", example), nil
		}
		if utf8.RuneCountInString(title) > maxNoteTitleRunes {
			return titleTooLong(title, example), nil
		}
		aboutExample := map[string]any{"title": "Tires", "content": "Rotate every 6k miles",
			"about_type": "vehicle", "about_id": "9a1c2b3d-4e5f-4a6b-8c7d-0e1f2a3b4c5d"}
		a, bad := parseAbout(args, true, aboutExample)
		if bad != nil {
			return bad, nil
		}
		if a.Type == "" {
			a = about{Type: "family", Kind: "household"}
		}
		if a.Type == "family" {
			a.ID = householdID
		}

		// claims.UserID is the users.id UUID once enriched, but may be an
		// OIDC subject or "local-dev" otherwise. Only record an author that
		// is a real user, so the author_id foreign key can never fail.
		var authorID *uuid.UUID
		if id, parseErr := uuid.Parse(claims.UserID); parseErr == nil {
			authorID = &id
		}

		// Loop guard: the same member saving the same note again within the
		// window gets the existing row back.
		n, err := scanNote(pool.QueryRow(ctx,
			`SELECT `+noteColumns+`
			 FROM notes n`+noteJoins+`
			 WHERE n.household_id = $1 AND n.entity_type::text = $2 AND n.entity_id = $3
			   AND n.title = $4 AND n.body = $5
			   AND n.author_id IS NOT DISTINCT FROM (SELECT id FROM users WHERE id = $6)
			   AND n.created_at > NOW() - $7::interval
			 ORDER BY n.created_at DESC
			 LIMIT 1`,
			householdID, a.Kind, a.ID, title, content, authorID, createDedupeWindow,
		))
		switch {
		case err == nil:
			n.Duplicate = true
			return respond.OK(fmt.Sprintf("You already saved note %q in the last 10 minutes; returned it instead of saving a copy.",
				n.displayTitle()), n,
				fmt.Sprintf("To change it use note_update note_id=%s.", n.ID)), nil
		case !errors.Is(err, pgx.ErrNoRows):
			return respond.Failed("save the note", err), nil
		}

		n, err = scanNote(pool.QueryRow(ctx,
			`WITH inserted AS (
				INSERT INTO notes (household_id, entity_type, entity_id, title, body, author_id)
				VALUES ($1, $2, $3, $4, $5, (SELECT id FROM users WHERE id = $6))
				RETURNING *
			)
			SELECT `+noteColumns+`
			FROM inserted n`+noteJoins,
			householdID, a.Kind, a.ID, title, content, authorID,
		))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch {
			case pgErr.Code == "23503": // enforce_entity_owner: missing or another family's row
				return respond.Invalid("about_id",
					fmt.Sprintf("No %s with id %s was found in this family.", a.Type, a.ID),
					fmt.Sprintf("Check the %s's id (%s), or leave about_type and about_id out to save a family note.", a.Type, findIDWith(a.Type)),
					aboutExample), nil
			case pgErr.Code == "23514" && pgErr.ConstraintName == "entity_kind_check":
				return respond.Invalid("about_type",
					fmt.Sprintf("Notes about a %s can't be saved yet.", a.Type),
					fmt.Sprintf("Leave about_type and about_id out to save it as a family note, and name the %s in the title.", a.Type),
					example), nil
			}
		}
		if err != nil {
			return respond.Failed("save the note", err), nil
		}

		summary := fmt.Sprintf("Saved note %q for the family.", n.displayTitle())
		if a.Type != "family" {
			summary = fmt.Sprintf("Saved note %q about %s %s.", n.displayTitle(), a.Type, a.ID)
		}
		next := []string{fmt.Sprintf("Add to it later with note_update note_id=%s append_text=….", n.ID)}
		if looksLikeChecklist(content) {
			next = append(next, "This looks like a checklist; checklists you can tick off are coming soon, so it is saved as a note for now.")
		}
		return respond.OK(summary, n, next...), nil
	}

	return "note_create", tool, handler
}

// NewNoteGetTool creates the note_get MCP tool (catalog v2, class R).
// It returns one note in full: title, complete body, author (id + name),
// what it is about and timestamps. A note that does not exist or belongs to
// another household is reported as not found, so ids from other households
// reveal nothing.
func NewNoteGetTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("note_get",
		mcp.WithDescription("Read one note in full, with who wrote it and when."),
		mcp.WithString("note_id",
			mcp.Description("The note's id (UUID), e.g. from note_find"),
			mcp.Required(),
		),
		mcp.WithTitleAnnotation("Get note"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.Schema[noteResult](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}

		noteID, raw, bad := parseNoteID(req.GetArguments(), map[string]any{"note_id": exampleNoteID})
		if bad != nil {
			return bad, nil
		}

		n, err := scanNote(pool.QueryRow(ctx,
			`SELECT `+noteColumns+`
			 FROM notes n`+noteJoins+`
			 WHERE n.id = $1 AND n.household_id = $2`,
			noteID, claims.HouseholdID,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("note", raw, "Call note_find to find the note_id."), nil
		}
		if err != nil {
			return respond.Failed("get the note", err), nil
		}

		summary := fmt.Sprintf("Found note %q.", n.displayTitle())
		if n.AuthorName != nil && *n.AuthorName != "" {
			summary = fmt.Sprintf("Found note %q by %s.", n.displayTitle(), *n.AuthorName)
		}
		return respond.OK(summary, n,
			fmt.Sprintf("Use note_update note_id=%s with append_text to add to it.", n.ID)), nil
	}

	return "note_get", tool, handler
}

// parseNoteID reads the required note_id argument.
func parseNoteID(args map[string]any, example map[string]any) (uuid.UUID, string, *mcp.CallToolResult) {
	raw := strings.TrimSpace(str(args, "note_id"))
	if raw == "" {
		return uuid.Nil, raw, respond.Invalid("note_id", "note_id is required.",
			"Pass the note's id as note_id. Call note_find to find it.", example)
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, raw, respond.Invalid("note_id", fmt.Sprintf("note_id %q is not a UUID.", raw),
			"Pass the note's id (a UUID) from note_find, not its title.", example)
	}
	return id, raw, nil
}

// appendDedupeWindow is how long a repeated, identical append_text is ignored
// (catalog v2: "A second identical append_text within 60 s is ignored").
const appendDedupeWindow = "60 seconds"

// NewNoteUpdateTool creates the note_update MCP tool (catalog v2, class U).
// Only the given fields change: title renames the note, content replaces
// the whole body, and append_text adds a line to the end of the body without
// overwriting what others wrote. The update is a single statement, so
// concurrent appends never lose each other's text. updated_at moves to now.
// A note that does not exist or belongs to another household is reported
// as not found. Empty strings count as "not given", because small models
// often send "" for optional params they don't mean to set.
//
// Duplicate guard: when append_text is the only change, the note's last
// line is exactly that text (the whole body, or the text after the last
// newline) and the note changed in the last 60 seconds, nothing is written
// and the unchanged note is returned, so a looping agent cannot stack the
// same line. A text that only matches the end of the last line is appended.
func NewNoteUpdateTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("note_update",
		mcp.WithDescription("Change a note. Use append_text to add lines without overwriting what others wrote."),
		mcp.WithString("note_id",
			mcp.Description("The note's id (UUID), e.g. from note_find"),
			mcp.Required(),
		),
		mcp.WithString("title",
			mcp.Description("New title (max 500 characters)"),
		),
		mcp.WithString("content",
			mcp.Description("New body; replaces the whole body. Prefer append_text to add to it"),
		),
		mcp.WithString("append_text",
			mcp.Description("Text added on a new line at the end of the body, e.g. \"- milk\""),
		),
		mcp.WithTitleAnnotation("Update note"),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.Schema[noteResult](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}

		args := req.GetArguments()
		title := strings.TrimSpace(str(args, "title"))
		content := str(args, "content")
		appendText := str(args, "append_text")

		appendExample := map[string]any{"note_id": exampleNoteID, "append_text": "- milk"}
		noteID, raw, bad := parseNoteID(args, appendExample)
		if bad != nil {
			return bad, nil
		}
		if title == "" && content == "" && appendText == "" {
			return respond.Invalid("", "Nothing to change: pass title, content or append_text.",
				"Pass append_text to add to the note, content to replace its body, or title to rename it.",
				appendExample), nil
		}
		if content != "" && appendText != "" {
			return respond.Invalid("append_text", "Pass content or append_text, not both.",
				"Use content to replace the whole body, or append_text to add to the end.",
				appendExample), nil
		}
		if utf8.RuneCountInString(title) > maxNoteTitleRunes {
			return titleTooLong(title, map[string]any{"note_id": exampleNoteID, "title": "Grocery"}), nil
		}

		// mode: 0 keep body, 1 replace with content, 2 append.
		mode, text := 0, ""
		switch {
		case content != "":
			mode, text = 1, content
		case appendText != "":
			mode, text = 2, appendText
		}
		guard := mode == 2 && title == ""

		n, err := scanNote(pool.QueryRow(ctx,
			`WITH updated AS (
				UPDATE notes SET
					title = CASE WHEN $3::text <> '' THEN $3::text ELSE title END,
					body = CASE $4::int
						WHEN 1 THEN $5::text
						WHEN 2 THEN CASE WHEN body = '' THEN $5::text ELSE body || E'\n' || $5::text END
						ELSE body END,
					updated_at = NOW()
				WHERE id = $1 AND household_id = $2
				  AND NOT ($6::boolean
				       AND updated_at > NOW() - $7::interval
				       AND (body = $5::text
				            OR right(body, char_length($5::text) + 1) = E'\n' || $5::text))
				RETURNING *
			)
			SELECT `+noteColumns+`
			FROM updated n`+noteJoins,
			noteID, claims.HouseholdID, title, mode, text, guard, appendDedupeWindow,
		))
		duplicate := false
		if errors.Is(err, pgx.ErrNoRows) && guard {
			// Either the note is not ours, or the guard skipped a repeat.
			n, err = scanNote(pool.QueryRow(ctx,
				`SELECT `+noteColumns+`
				 FROM notes n`+noteJoins+`
				 WHERE n.id = $1 AND n.household_id = $2`,
				noteID, claims.HouseholdID,
			))
			duplicate = err == nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("note", raw, "Call note_find to find the note_id."), nil
		}
		if err != nil {
			return respond.Failed("update the note", err), nil
		}

		name := n.displayTitle()
		readHint := fmt.Sprintf("Read it in full with note_get note_id=%s.", n.ID)
		if duplicate {
			return respond.OK(fmt.Sprintf("Note %q already ends with that text; nothing was added.", name), n,
				readHint), nil
		}
		var changed []string
		if title != "" {
			changed = append(changed, "renamed it")
		}
		switch mode {
		case 1:
			changed = append(changed, "replaced its body")
		case 2:
			changed = append(changed, "added text to the end")
		}
		summary := fmt.Sprintf("Updated note %q: %s.", name, joinAnd(changed))
		return respond.OK(summary, n, readHint,
			fmt.Sprintf("Add more with note_update note_id=%s append_text=….", n.ID)), nil
	}

	return "note_update", tool, handler
}

// NewNoteDeleteTool creates the note_delete MCP tool (catalog v2, class X).
// It deletes one note in the authenticated household and returns the note as
// it was, so the model can tell the user what was removed. A note that does
// not exist or belongs to another household is reported as not found and
// nothing is deleted. A repeat of the call finds nothing and changes nothing
// more (idempotent). Notes write no outbox events; the AFTER DELETE trigger
// of migration 038 removes rows that were about the note.
func NewNoteDeleteTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("note_delete",
		mcp.WithDescription("Delete a note."),
		mcp.WithString("note_id",
			mcp.Description("The note's id (UUID), e.g. from note_find"),
			mcp.Required(),
		),
		mcp.WithTitleAnnotation("Delete note"),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.Schema[noteResult](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}

		noteID, raw, bad := parseNoteID(req.GetArguments(), map[string]any{"note_id": exampleNoteID})
		if bad != nil {
			return bad, nil
		}

		n, err := scanNote(pool.QueryRow(ctx,
			`WITH deleted AS (
				DELETE FROM notes
				WHERE id = $1 AND household_id = $2
				RETURNING *
			)
			SELECT `+noteColumns+`
			FROM deleted n`+noteJoins,
			noteID, claims.HouseholdID,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("note", raw,
				"Call note_find to find the note_id; it may already be deleted."), nil
		}
		if err != nil {
			return respond.Failed("delete the note", err), nil
		}

		return respond.OK(fmt.Sprintf("Deleted note %q.", n.displayTitle()), n,
			"It cannot be restored. Save it again with note_create if this was a mistake."), nil
	}

	return "note_delete", tool, handler
}

// joinAnd joins parts as "a", "a and b" or "a, b and c".
func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	out := parts[0]
	for _, p := range parts[1 : len(parts)-1] {
		out += ", " + p
	}
	return out + " and " + parts[len(parts)-1]
}

// truncate returns the first n characters of s, respecting Unicode boundaries.
// If s is shorter than n, it returns s unchanged.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	var count int
	for i := range s {
		if count >= n {
			return s[:i] + "..."
		}
		count++
	}
	return s
}
