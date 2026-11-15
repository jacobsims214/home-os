// Package note provides MCP tools for managing notes in Home OS.
// Notes are polymorphic — they can be attached to any entity type
// (property, vehicle, pet, asset, vendor, bill, etc.) via entity_type
// and entity_id. All operations are scoped to the authenticated household.
package note

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// noteColumns is the SELECT list shared by list_notes, create_note and note_get. It
// needs "notes n LEFT JOIN users u ON u.id = n.author_id" in the FROM clause.
const noteColumns = `n.id, n.entity_type, n.entity_id, n.title, n.body,
	n.author_id, u.name, n.created_at, n.updated_at`

// householdEntityType is the entity_type given to notes created without an
// entity: they are attached to the household itself (entity_id = household
// id), since the notes table requires both columns.
const householdEntityType = "household"

// noteResult is one note as returned by list_notes, create_note and
// note_get. IDs are strings, not uuid.UUID: the outputSchema is inferred
// with jsonschema-go, which sees uuid.UUID as a [16]byte array and would
// reject the string UUIDs that are actually on the wire.
type noteResult struct {
	ID             string    `json:"id"`
	EntityType     string    `json:"entity_type"`
	EntityID       string    `json:"entity_id"`
	Title          *string   `json:"title"`
	Body           string    `json:"body"`
	ContentPreview string    `json:"content_preview"`
	AuthorID       *string   `json:"author_id"`
	AuthorName     *string   `json:"author_name"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func scanNote(row pgx.Row) (noteResult, error) {
	var (
		n            noteResult
		id, entityID uuid.UUID
		authorID     *uuid.UUID
	)
	if err := row.Scan(&id, &n.EntityType, &entityID, &n.Title, &n.Body,
		&authorID, &n.AuthorName, &n.CreatedAt, &n.UpdatedAt); err != nil {
		return noteResult{}, err
	}
	n.ID, n.EntityID = id.String(), entityID.String()
	if authorID != nil {
		s := authorID.String()
		n.AuthorID = &s
	}
	n.ContentPreview = truncate(n.Body, 100)
	return n, nil
}

// NewListNotesTool creates the list_notes MCP tool.
// It returns each note's id, entity, title, full body, content_preview (first
// 100 chars), author (id + name) and timestamps for the authenticated
// household, optionally filtered by entity_type and entity_id.
func NewListNotesTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("list_notes",
		mcp.WithDescription("List notes for the household, optionally filtered by entity type and entity ID. Each note includes its full body and its author (author_id, author_name). Notes created without an entity have entity_type \"household\"."),
		mcp.WithString("entity_type",
			mcp.Description("Optional entity type filter (e.g. property, vehicle, pet, household)"),
		),
		mcp.WithString("entity_id",
			mcp.Description("Optional entity ID filter (UUID)"),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		args := req.GetArguments()
		entityType, _ := args["entity_type"].(string)
		entityIDStr, _ := args["entity_id"].(string)

		if (entityType == "") != (entityIDStr == "") {
			return mcp.NewToolResultText(`{"error":"entity_type and entity_id must be provided together"}`), nil
		}

		var rows pgx.Rows
		var err error

		if entityType != "" && entityIDStr != "" {
			entityID, parseErr := uuid.Parse(entityIDStr)
			if parseErr != nil {
				return mcp.NewToolResultText(fmt.Sprintf(`{"error":"invalid entity_id: %s"}`, parseErr.Error())), nil
			}
			rows, err = pool.Query(ctx,
				`SELECT `+noteColumns+`
				 FROM notes n LEFT JOIN users u ON u.id = n.author_id
				 WHERE n.household_id = $1 AND n.entity_type = $2 AND n.entity_id = $3
				 ORDER BY n.created_at DESC`,
				claims.HouseholdID, entityType, entityID,
			)
		} else {
			rows, err = pool.Query(ctx,
				`SELECT `+noteColumns+`
				 FROM notes n LEFT JOIN users u ON u.id = n.author_id
				 WHERE n.household_id = $1
				 ORDER BY n.created_at DESC`,
				claims.HouseholdID,
			)
		}
		if err != nil {
			return nil, fmt.Errorf("list notes: %w", err)
		}
		defer rows.Close()

		notes := []noteResult{}
		for rows.Next() {
			n, err := scanNote(rows)
			if err != nil {
				return nil, fmt.Errorf("scan note: %w", err)
			}
			notes = append(notes, n)
		}

		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate notes: %w", err)
		}

		result, _ := json.Marshal(notes)
		return mcp.NewToolResultText(string(result)), nil
	}

	return "list_notes", tool, handler
}

// NewCreateNoteTool creates the create_note MCP tool.
// It inserts a new note with the given title and body, authored by the
// caller, optionally associated with an entity via entity_type and
// entity_id. Without an entity the note is attached to the household
// (entity_type "household", entity_id = household id). The response is the
// stored note in the same shape list_notes returns.
func NewCreateNoteTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("create_note",
		mcp.WithDescription("Create a new note authored by the caller, optionally associated with an entity. Without an entity the note is attached to the household."),
		mcp.WithString("title",
			mcp.Description("Note title"),
			mcp.Required(),
		),
		mcp.WithString("content",
			mcp.Description("Note body content (markdown supported)"),
			mcp.Required(),
		),
		mcp.WithString("entity_type",
			mcp.Description("Optional entity type to associate the note with (requires entity_id)"),
		),
		mcp.WithString("entity_id",
			mcp.Description("Optional entity ID (UUID) to associate the note with (requires entity_type)"),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		args := req.GetArguments()
		title, _ := args["title"].(string)
		content, _ := args["content"].(string)
		entityType, _ := args["entity_type"].(string)
		entityIDStr, _ := args["entity_id"].(string)

		if title == "" || content == "" {
			return mcp.NewToolResultText(`{"error":"title and content are required"}`), nil
		}
		if (entityType == "") != (entityIDStr == "") {
			return mcp.NewToolResultText(`{"error":"entity_type and entity_id must be provided together"}`), nil
		}

		householdID, err := uuid.Parse(claims.HouseholdID)
		if err != nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		entityID := householdID
		if entityType == "" {
			entityType = householdEntityType
		} else {
			var parseErr error
			entityID, parseErr = uuid.Parse(entityIDStr)
			if parseErr != nil {
				return mcp.NewToolResultText(fmt.Sprintf(`{"error":"invalid entity_id: %s"}`, parseErr.Error())), nil
			}
		}

		// claims.UserID is the users.id UUID once enriched, but may be an
		// OIDC subject or "local-dev" otherwise. Only record an author that
		// is a real user, so the author_id foreign key can never fail.
		var authorID *uuid.UUID
		if id, parseErr := uuid.Parse(claims.UserID); parseErr == nil {
			authorID = &id
		}

		n, err := scanNote(pool.QueryRow(ctx,
			`WITH inserted AS (
				INSERT INTO notes (household_id, entity_type, entity_id, title, body, author_id)
				VALUES ($1, $2, $3, $4, $5, (SELECT id FROM users WHERE id = $6))
				RETURNING *
			)
			SELECT `+noteColumns+`
			FROM inserted n LEFT JOIN users u ON u.id = n.author_id`,
			householdID, entityType, entityID, title, content, authorID,
		))
		if err != nil {
			return nil, fmt.Errorf("create note: %w", err)
		}

		result, _ := json.Marshal(n)
		return mcp.NewToolResultText(string(result)), nil
	}

	return "create_note", tool, handler
}

// NewNoteGetTool creates the note_get MCP tool (catalog v2, class R).
// It returns one note in full: title, complete body, author (id + name),
// entity link and timestamps. A note that does not exist or belongs to
// another household is reported as not found, so ids from other households
// reveal nothing. Results are built with internal/respond.
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
			return respond.Fail(respond.Error{
				Message: "You are not signed in to a household.",
				Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
			}), nil
		}

		raw, _ := req.GetArguments()["note_id"].(string)
		example := map[string]any{"note_id": "3f2b8c1e-5d4a-4b7e-9c2a-1e6f0a9b8d7c"}
		if raw == "" {
			return respond.Invalid("note_id", "note_id is required.",
				"Pass the note's id as note_id. Call note_find to find it.", example), nil
		}
		noteID, err := uuid.Parse(raw)
		if err != nil {
			return respond.Invalid("note_id", fmt.Sprintf("note_id %q is not a UUID.", raw),
				"Pass the note's id (a UUID) from note_find, not its title.", example), nil
		}

		n, err := scanNote(pool.QueryRow(ctx,
			`SELECT `+noteColumns+`
			 FROM notes n LEFT JOIN users u ON u.id = n.author_id
			 WHERE n.id = $1 AND n.household_id = $2`,
			noteID, claims.HouseholdID,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("note", raw, "Call note_find to find the note_id."), nil
		}
		if err != nil {
			return respond.Failed("get the note", err), nil
		}

		title := "(untitled)"
		if n.Title != nil && *n.Title != "" {
			title = *n.Title
		}
		summary := fmt.Sprintf("Found note %q.", title)
		if n.AuthorName != nil && *n.AuthorName != "" {
			summary = fmt.Sprintf("Found note %q by %s.", title, *n.AuthorName)
		}
		return respond.OK(summary, n,
			fmt.Sprintf("Use note_update note_id=%s with append_text to add to it.", n.ID)), nil
	}

	return "note_get", tool, handler
}

// maxNoteTitleRunes is the notes.title column limit (VARCHAR(500)).
const maxNoteTitleRunes = 500

// appendDedupeWindow is how long a repeated, identical append_text is ignored
// (catalog v2: "A second identical append_text within 60 s is ignored").
const appendDedupeWindow = "60 seconds"

// NewNoteUpdateTool creates the note_update MCP tool (catalog v2, class U).
// Only the given fields change: title renames the note, content replaces
// the whole body, and append_text adds lines to the end of the body without
// overwriting what others wrote. The update is a single statement, so
// concurrent appends never lose each other's text. updated_at moves to now.
// A note that does not exist or belongs to another household is reported
// as not found. Empty strings count as "not given", because small models
// often send "" for optional params they don't mean to set.
//
// Duplicate guard: when append_text is the only change and the note already
// ends with that exact text from an update in the last 60 seconds, nothing
// is written and the unchanged note is returned (a looping agent cannot
// stack the same line). Results are built with internal/respond.
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
			return respond.Fail(respond.Error{
				Message: "You are not signed in to a household.",
				Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
			}), nil
		}

		args := req.GetArguments()
		raw, _ := args["note_id"].(string)
		title, _ := args["title"].(string)
		content, _ := args["content"].(string)
		appendText, _ := args["append_text"].(string)

		const exampleID = "3f2b8c1e-5d4a-4b7e-9c2a-1e6f0a9b8d7c"
		appendExample := map[string]any{"note_id": exampleID, "append_text": "- milk"}
		if raw == "" {
			return respond.Invalid("note_id", "note_id is required.",
				"Pass the note's id as note_id. Call note_find to find it.", appendExample), nil
		}
		noteID, err := uuid.Parse(raw)
		if err != nil {
			return respond.Invalid("note_id", fmt.Sprintf("note_id %q is not a UUID.", raw),
				"Pass the note's id (a UUID) from note_find, not its title.", appendExample), nil
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
			return respond.Invalid("title",
				fmt.Sprintf("title is %d characters; the limit is %d.", utf8.RuneCountInString(title), maxNoteTitleRunes),
				"Shorten the title and put the details in content or append_text.",
				map[string]any{"note_id": exampleID, "title": "Grocery"}), nil
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
				       AND right(body, char_length($5::text)) = $5::text)
				RETURNING *
			)
			SELECT `+noteColumns+`
			FROM updated n LEFT JOIN users u ON u.id = n.author_id`,
			noteID, claims.HouseholdID, title, mode, text, guard, appendDedupeWindow,
		))
		duplicate := false
		if errors.Is(err, pgx.ErrNoRows) && guard {
			// Either the note is not ours, or the guard skipped a repeat.
			n, err = scanNote(pool.QueryRow(ctx,
				`SELECT `+noteColumns+`
				 FROM notes n LEFT JOIN users u ON u.id = n.author_id
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

		name := "(untitled)"
		if n.Title != nil && *n.Title != "" {
			name = *n.Title
		}
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
