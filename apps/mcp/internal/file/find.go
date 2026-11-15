package file

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// NewDocumentFindTool creates document_find (catalog v2, class R; replaces
// list_files and search_files). Every param is optional. query is a
// case-insensitive substring match over title, file name, description,
// category, tags and extracted text (semantic search is S2 #4477). Results
// whose title or file name matches come first, then the newest document
// date. from/to filter on the document date (the upload day when a document
// has none), both inclusive. Items carry a snippet, never the whole text.
// Paging: limit (default 25, max 100) and an opaque cursor that a truncated
// result hands back as next_cursor.
func NewDocumentFindTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("document_find",
		mcp.WithDescription("Find documents by words in them (\"car insurance\"), category, person, what they're about, or date. Returns title, category, dates and a snippet; read one with document_get."),
		mcp.WithString("query",
			mcp.Description("Words to find in the title, file name, tags or text, e.g. insurance"),
		),
		mcp.WithString("category",
			mcp.Description("Only this kind of document, e.g. receipt"),
			mcp.Enum(categories...),
		),
		mcp.WithString("person_id",
			mcp.Description("Only documents for this person: id or exact name, e.g. Nora"),
		),
		mcp.WithString("about_type",
			mcp.Description("Only documents about this kind of thing; family = family-wide"),
			mcp.Enum(aboutTypes...),
		),
		mcp.WithString("about_id",
			mcp.Description("Only documents about this row (UUID); needs about_type"),
		),
		mcp.WithString("from",
			mcp.Description("Document date on or after, YYYY-MM-DD, e.g. 2026-01-01"),
		),
		mcp.WithString("to",
			mcp.Description("Document date on or before, YYYY-MM-DD, e.g. 2026-12-31"),
		),
		mcp.WithString("tag",
			mcp.Description("Only documents with this tag (any case), e.g. taxes-2026"),
		),
		mcp.WithNumber("limit",
			mcp.Description("How many documents to return (default 25, max 100)"),
			mcp.Min(1), mcp.Max(maxLimit),
		),
		mcp.WithString("cursor",
			mcp.Description("Paging cursor from the previous document_find result"),
		),
		mcp.WithTitleAnnotation("Find documents"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.ListSchema[documentItem](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		householdID, ok := householdOf(ctx)
		if !ok {
			return notSignedIn(), nil
		}
		args := req.GetArguments()
		example := map[string]any{"query": "insurance", "category": "insurance"}

		query := firstStr(args, "query", "search")
		category := ""
		if raw := str(args, "category"); raw != "" {
			c, ok := parseCategory(raw)
			if !ok {
				return badCategory(raw, example), nil
			}
			category = c
		}
		a, bad := parseAbout(args, false, map[string]any{"about_type": "vehicle", "about_id": "9a1c2b3d-4e5f-4a6b-8c7d-0e1f2a3b4c5d"})
		if bad != nil {
			return bad, nil
		}
		from, rawFrom, ok := parseDate(args, "from")
		if !ok {
			return badDate("from", rawFrom, map[string]any{"from": "2026-01-01", "to": "2026-12-31"}), nil
		}
		to, rawTo, ok := parseDate(args, "to")
		if !ok {
			return badDate("to", rawTo, map[string]any{"from": "2026-01-01", "to": "2026-12-31"}), nil
		}
		if from != nil && to != nil && to.Before(*from) {
			return respond.Invalid("to", fmt.Sprintf("to %s is before from %s.", rawTo, rawFrom),
				"Pass a to date on or after from.", map[string]any{"from": rawFrom, "to": rawFrom}), nil
		}
		tag := str(args, "tag")
		limit, given, ok := intArg(args, "limit")
		if !ok || (given && (limit < 1 || limit > maxLimit)) {
			return respond.Invalid("limit", fmt.Sprintf("limit must be a whole number from 1 to %d.", maxLimit),
				"Pass limit as a number, e.g. 25, or leave it out.", map[string]any{"limit": 25}), nil
		}
		if !given {
			limit = defaultLimit
		}
		offset := 0
		if c := str(args, "cursor"); c != "" {
			if offset, ok = respond.DecodeCursor(c); !ok {
				return respond.Invalid("cursor", "cursor is not one document_find returned.",
					"Pass the cursor exactly as the previous document_find result gave it, or leave it out to start over.",
					map[string]any{"query": "insurance"}), nil
			}
		}

		var personID *uuid.UUID
		personName := ""
		if ref := str(args, "person_id"); ref != "" {
			id, name, bad, err := resolvePerson(ctx, pool, householdID, ref, map[string]any{"person_id": "Nora"})
			if err != nil {
				return respond.Failed("find documents", err), nil
			}
			if bad != nil {
				return bad, nil
			}
			personID, personName = &id, name
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

		pattern := ""
		if query != "" {
			pattern = likePattern(query)
		}

		// Family documents may still have a NULL entity until 040, so
		// about_type=family also matches those. entity_type is compared as
		// text: a kind the DB does not know matches nothing instead of
		// failing the domain cast.
		rows, err := pool.Query(ctx,
			`SELECT `+documentColumns+`,
			        substr(s.src, st.start, `+fmt.Sprint(snippetLen)+`), st.start, char_length(s.src),
			        COUNT(*) OVER ()`+documentFrom+`
			 CROSS JOIN LATERAL (SELECT CASE
			     WHEN $3::text <> '' AND strpos(lower(COALESCE(f.description, '')), lower($3::text)) > 0 THEN f.description
			     WHEN $3::text <> '' AND strpos(lower(COALESCE(f.extracted_text, '')), lower($3::text)) > 0 THEN f.extracted_text
			     ELSE COALESCE(NULLIF(btrim(f.description), ''), f.extracted_text) END AS src) s
			 CROSS JOIN LATERAL (SELECT GREATEST(CASE WHEN $3::text <> '' THEN strpos(lower(s.src), lower($3::text)) ELSE 0 END - 60, 1) AS start) st
			 WHERE f.household_id = $1
			   AND ($2::text = '' OR (
			         COALESCE(f.title, '') ILIKE $2 ESCAPE '\' OR f.name ILIKE $2 ESCAPE '\'
			      OR COALESCE(f.description, '') ILIKE $2 ESCAPE '\' OR f.category ILIKE $2 ESCAPE '\'
			      OR array_to_string(COALESCE(f.tags, '{}'::text[]), ' ') ILIKE $2 ESCAPE '\'
			      OR COALESCE(f.extracted_text, '') ILIKE $2 ESCAPE '\'))
			   AND ($4::text = '' OR f.category = $4::text)
			   AND ($5::uuid IS NULL OR f.person_id = $5::uuid)
			   AND ($6::text IS NULL
			        OR ($6::text = 'household' AND (f.entity_type IS NULL OR (f.entity_type::text = 'household' AND f.entity_id = $1)))
			        OR ($6::text <> 'household' AND f.entity_type::text = $6::text AND ($7::uuid IS NULL OR f.entity_id = $7::uuid)))
			   AND ($8::date IS NULL OR COALESCE(f.document_date, (f.created_at AT TIME ZONE h.timezone)::date) >= $8::date)
			   AND ($9::date IS NULL OR COALESCE(f.document_date, (f.created_at AT TIME ZONE h.timezone)::date) <= $9::date)
			   AND ($10::text = '' OR EXISTS (SELECT 1 FROM unnest(f.tags) t WHERE lower(t) = lower($10::text)))
			 ORDER BY
			   CASE WHEN $2::text <> '' AND (COALESCE(f.title, '') ILIKE $2 ESCAPE '\' OR f.name ILIKE $2 ESCAPE '\') THEN 0 ELSE 1 END,
			   COALESCE(f.document_date, (f.created_at AT TIME ZONE h.timezone)::date) DESC,
			   f.created_at DESC, f.id
			 LIMIT $11 OFFSET $12`,
			householdID, pattern, query, category, personID, kind, aboutID, from, to, tag, limit, offset,
		)
		if err != nil {
			return respond.Failed("find documents", err), nil
		}
		defer rows.Close()

		items := []documentItem{}
		total := 0
		for rows.Next() {
			var (
				raw     *string
				start   int
				srcLen  *int
				snippet *string
			)
			r, err := scanDocument(rows, &raw, &start, &srcLen, &total)
			if err != nil {
				return respond.Failed("find documents", err), nil
			}
			if raw != nil {
				s := collapseSpace(*raw)
				if s != "" {
					if start > 1 {
						s = "…" + s
					}
					if srcLen != nil && start-1+snippetLen < *srcLen {
						s += "…"
					}
					snippet = &s
				}
			}
			items = append(items, r.item(snippet))
		}
		if err := rows.Err(); err != nil {
			return respond.Failed("find documents", err), nil
		}

		filtered := query != "" || category != "" || personID != nil || a.Type != "" || from != nil || to != nil || tag != ""
		if len(items) == 0 {
			if offset > 0 {
				return respond.List("No more documents past this cursor.", items, respond.Page{},
					"Leave cursor out to start from the first page."), nil
			}
			if !filtered {
				return respond.List("The family has no documents yet.", items, respond.Page{},
					"File one with document_upload."), nil
			}
			return respond.List("No documents matched.", items, respond.Page{},
				"Try fewer filters or a shorter query.",
				"Use search_all to look everywhere.",
				"File a new one with document_upload."), nil
		}

		what := "documents"
		if category != "" {
			what = category + " documents"
		}
		if personName != "" {
			what += " for " + personName
		}
		remaining := total - offset - len(items)
		summary := fmt.Sprintf("Found %d %s.", total, what)
		if offset > 0 || remaining > 0 {
			summary = fmt.Sprintf("Found %d %s; showing %d–%d.", total, what, offset+1, offset+len(items))
		}
		if total == 1 {
			summary = fmt.Sprintf("Found 1 document: %q.", items[0].Title)
		}
		next := []string{fmt.Sprintf("Read one with document_get document_id=%s.", items[0].ID)}
		page := respond.Page{}
		if remaining > 0 {
			page = respond.Page{
				NextCursor: respond.EncodeCursor(offset + len(items)),
				Total:      len(items) + remaining,
				Narrow:     fmt.Sprintf("%d more. Pass next_cursor as cursor with the same filters for the next page, or add query, category or from/to to narrow.", remaining),
			}
		}
		return respond.List(strings.TrimSpace(summary), items, page, next...), nil
	}

	return "document_find", tool, handler
}
