package vendors

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// findAliases are old list_vendors/get_vendor parameter names (and words
// small models reach for), still accepted but not advertised.
var findAliases = map[string]string{
	"contact_id": "id", "vendor_id": "id",
	"specialty": "trade",
	"name":      "query", "search": "query",
}

// findExample is a valid contact_find call for error examples.
var findExample = map[string]any{"trade": "plumber"}

// findInput is the validated contact_find arguments.
type findInput struct {
	id        *uuid.UUID
	trade     string // as given, trimmed (for summaries and specialty matching)
	tradeNorm string // stored form: lower case, underscores, synonyms mapped
	query     string
	limit     int
	offset    int
}

func (in findInput) filtered() bool { return in.id != nil || in.trade != "" || in.query != "" }

// contactSelect loads the household's contacts with their counts, one page
// at a time, and the total before paging.
//
// trade ($3-$5) matches a stored trade exactly (GIN index on trades), a
// stored trade containing the text ("plumb" finds plumber), or the free-text
// specialty containing it (vendors whose specialty 043 could not map).
// query ($6) matches the name, specialty, any trade, notes, email or phone.
// vendors.property_id is deliberately not selected (dropped in 040).
const contactSelect = `
	SELECT v.id, v.name, v.trades, v.specialty, v.phone, v.email, v.website, v.notes,
	       v.created_at, v.updated_at,
	       (SELECT count(*) FROM bills b WHERE b.vendor_id = v.id),
	       (SELECT count(*) FROM maintenance_tasks m WHERE m.vendor_id = v.id
	           AND m.status IN ('pending', 'in_progress')),
	       (SELECT count(*) FROM pets p WHERE p.vet_vendor_id = v.id),
	       count(*) OVER ()
	FROM vendors v
	WHERE v.household_id = $1
	  AND ($2::uuid IS NULL OR v.id = $2)
	  AND ($3::text = '' OR v.trades @> ARRAY[$3::text]
	       OR EXISTS (SELECT 1 FROM unnest(v.trades) tr WHERE tr ILIKE '%' || $5 || '%')
	       OR v.specialty ILIKE '%' || $4 || '%')
	  AND ($6 = '' OR v.name ILIKE '%' || $6 || '%' OR v.specialty ILIKE '%' || $6 || '%'
	       OR array_to_string(v.trades, ' ') ILIKE '%' || $6 || '%'
	       OR v.notes ILIKE '%' || $6 || '%' OR v.email ILIKE '%' || $6 || '%'
	       OR v.phone ILIKE '%' || $6 || '%')
	ORDER BY lower(v.name), v.created_at, v.id
	LIMIT $7 OFFSET $8`

// NewContactFindTool creates the contact_find MCP tool (catalog v2, class
// R), replacing list_vendors and get_vendor. Pagination is limit + the
// opaque next_cursor (respond.EncodeCursor), passed back as cursor.
func NewContactFindTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Find a contact by trade or name: \"who's the plumber?\" uses trade=plumber. " +
			"Returns phone, email and notes. Pass id for exactly one contact."),
		mcp.WithString("id", mcp.Description("A contact_id (UUID) to get exactly that contact")),
		mcp.WithString("trade", mcp.Description("What they do, e.g. plumber, pediatrician, vet, hvac")),
		mcp.WithString("query", mcp.Description("Text in the name, trade, notes, email or phone, e.g. joe")),
		mcp.WithNumber("limit", mcp.Description("Max contacts to return, 1-100 (default 25)"), mcp.Min(1), mcp.Max(maxLimit)),
		mcp.WithString("cursor", mcp.Description("next_cursor from the previous result, for the next page")),
		respond.ListSchema[contactData](),
	}
	tool := mcp.NewTool("contact_find", append(opts, annotations("Find contacts", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}
		in, err := parseFindInput(req.GetArguments())
		if err != nil {
			return invalid(err, findExample), nil
		}
		page, total, err := findContacts(ctx, pool, claims.HouseholdID, in)
		if err != nil {
			return respond.Failed("find contacts", err), nil
		}
		if in.id != nil && len(page) == 0 {
			return respond.NotFound("contact", in.id.String(),
				"Call contact_find without id (or with trade or query) to list contacts and their ids."), nil
		}
		if total == 0 && in.offset > 0 {
			// Past the end: count what there is so the summary is honest.
			probe := in
			probe.limit, probe.offset = 1, 0
			if _, total, err = findContacts(ctx, pool, claims.HouseholdID, probe); err != nil {
				return respond.Failed("find contacts", err), nil
			}
			in.offset = max(in.offset, total)
		}
		end := in.offset + len(page)
		return respond.List(findSummary(in, page, total), page, respond.Page{
			Total:      max(total-in.offset, len(page)),
			NextCursor: respond.CursorAfter(end, total),
			Narrow:     "Pass next_cursor as cursor for the next page, or add trade or query.",
		}, findNext(in, page)...), nil
	}
	return "contact_find", tool, handler
}

// findContacts loads one page of contacts and the total number of matches.
func findContacts(ctx context.Context, pool *pgxpool.Pool, householdID string, in findInput) ([]contactData, int, error) {
	rows, err := pool.Query(ctx, contactSelect, householdID, in.id,
		in.tradeNorm, escapeLike(in.trade), escapeLike(in.tradeNorm), escapeLike(in.query),
		in.limit, in.offset)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	page, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (contactData, error) {
		var (
			d  contactData
			id uuid.UUID
		)
		err := row.Scan(&id, &d.Name, &d.Trades, &d.Specialty, &d.Phone, &d.Email, &d.Website, &d.Notes,
			&d.CreatedAt, &d.UpdatedAt,
			&d.Counts.Bills, &d.Counts.OpenMaintenance, &d.Counts.Pets, &total)
		d.ID = id.String()
		if d.Trades == nil {
			d.Trades = []string{}
		}
		return d, err
	})
	return page, total, err
}

// findSummary is contact_find's one-line summary.
func findSummary(in findInput, page []contactData, total int) string {
	switch {
	case total == 0 && !in.filtered():
		return "No contacts saved yet."
	case total == 0 && in.trade != "" && in.query == "":
		return fmt.Sprintf("No %s saved.", in.trade)
	case total == 0:
		return "No contacts match."
	case len(page) == 1 && total == 1:
		return fmt.Sprintf("Found %s: %s.", page[0].label(), page[0].reach())
	case len(page) == 0:
		return fmt.Sprintf("Found %s; this page is past the end.", plural(total, "contact"))
	case len(page) < total-in.offset || in.offset > 0:
		return fmt.Sprintf("Found %d contacts; showing %d-%d.", total, in.offset+1, in.offset+len(page))
	default:
		labels := make([]string, len(page))
		for i, c := range page {
			labels[i] = c.label()
		}
		return fmt.Sprintf("Found %d contacts: %s.", total, strings.Join(labels, ", "))
	}
}

// findNext is contact_find's next-step hints (catalog "Next": an empty
// trade search would suggest contact_create with that trade; until contact_create
// is registered it says adding contacts is coming soon).
func findNext(in findInput, page []contactData) []string {
	if len(page) == 0 {
		switch {
		case in.trade != "":
			return []string{
				"Adding contacts is coming soon.",
				fmt.Sprintf("Or search names and notes with contact_find query=%s.", in.trade),
			}
		case in.filtered():
			return []string{"Call contact_find without query to list every contact."}
		default:
			return []string{"Adding contacts is coming soon."}
		}
	}
	c := page[0]
	next := []string{fmt.Sprintf("Have %s do an upkeep job with maintenance_create contact_id=%s.", c.Name, c.ID)}
	if c.Counts.OpenMaintenance > 0 {
		next = append(next, fmt.Sprintf("%s is on %s; list upkeep with maintenance_find (items show contact_name).",
			c.Name, plural(c.Counts.OpenMaintenance, "open upkeep task")))
	} else {
		next = append(next, fmt.Sprintf("Record a bill paid to them with bill_create contact_id=%s.", c.ID))
	}
	return next
}

// parseFindInput validates contact_find arguments.
func parseFindInput(raw map[string]any) (findInput, error) {
	in := findInput{limit: defaultLimit}
	args := withAliases(raw, findAliases)
	if s, err := optionalString(args, "id"); err != nil {
		return in, err
	} else if s != nil {
		id, perr := uuid.Parse(*s)
		if perr != nil {
			return in, badArg("id", fmt.Sprintf("id must be a UUID; got %q.", *s),
				"Pass a contact's id (a UUID) from contact_find, or use query to search by name or trade to search by what they do.")
		}
		in.id = &id
	}
	if t, err := optionalString(args, "trade"); err != nil {
		return in, err
	} else if t != nil {
		in.trade = *t
		in.tradeNorm = normalizeTrade(*t)
	}
	if q, err := optionalString(args, "query"); err != nil {
		return in, err
	} else if q != nil {
		in.query = *q
	}
	if n, ok, err := wholeNumber(args, "limit", 1, maxLimit); err != nil {
		return in, err
	} else if ok {
		in.limit = n
	}
	if c, err := optionalString(args, "cursor"); err != nil {
		return in, err
	} else if c != nil {
		off, ok := respond.DecodeCursor(*c)
		if !ok {
			return in, badArg("cursor", fmt.Sprintf("cursor %q is not a cursor from contact_find.", *c),
				"Pass next_cursor from the previous result as cursor, or leave cursor out for the first page.")
		}
		in.offset = off
	}
	return in, nil
}
