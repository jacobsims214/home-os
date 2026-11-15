package property

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// findAliases are old list_properties/get_property parameter names, still
// accepted but not advertised.
var findAliases = map[string]string{"property_id": "id", "name": "query", "search": "query"}

// findExample is a valid property_find call for error examples.
var findExample = map[string]any{"query": "lake"}

// findInput is the validated property_find arguments.
type findInput struct {
	id     *uuid.UUID
	query  string
	limit  int
	offset int
}

func (in findInput) filtered() bool { return in.id != nil || in.query != "" }

// propertySelect loads the household's properties with their counts, one
// page at a time, and the total before paging. query matches the name,
// address, type, notes or any room's name, so "kitchen" finds the house.
// Loans and bills on an asset carry the asset's property_id (038's
// derive_subject_property), so property_id alone counts them.
const propertySelect = `
	SELECT p.id, p.name, p.address, p.property_type, p.notes, p.purchase_date,
	       p.purchase_price::float8, p.current_value::float8, p.created_at, p.updated_at,
	       (SELECT count(*) FROM rooms r WHERE r.property_id = p.id),
	       (SELECT count(*) FROM assets a WHERE a.property_id = p.id),
	       (SELECT count(*) FROM bills b WHERE b.property_id = p.id),
	       (SELECT count(*) FROM loans l WHERE l.property_id = p.id),
	       (SELECT count(*) FROM maintenance_tasks m WHERE m.property_id = p.id
	           AND m.status IN ('pending', 'in_progress')),
	       count(*) OVER ()
	FROM properties p
	WHERE p.household_id = $1
	  AND ($2::uuid IS NULL OR p.id = $2)
	  AND ($3 = '' OR p.name ILIKE '%' || $3 || '%' OR p.address ILIKE '%' || $3 || '%'
	       OR p.property_type ILIKE '%' || $3 || '%' OR p.notes ILIKE '%' || $3 || '%'
	       OR EXISTS (SELECT 1 FROM rooms r WHERE r.property_id = p.id AND r.name ILIKE '%' || $3 || '%'))
	ORDER BY lower(p.name), p.created_at, p.id
	LIMIT $4 OFFSET $5`

// roomSelect loads the rooms of the given properties (already scoped to
// the household) with how many assets each holds.
const roomSelect = `
	SELECT r.property_id, r.id, r.name, r.floor,
	       (SELECT count(*) FROM assets a WHERE a.room_id = r.id)
	FROM rooms r
	WHERE r.property_id = ANY($1)
	ORDER BY r.floor NULLS LAST, lower(r.name), r.id`

// NewPropertyFindTool creates the property_find MCP tool (catalog v2,
// class R), replacing list_properties and get_property. Every property
// comes back with its rooms and counts, because search_all opens both
// property and room hits with property_find id=<property_id>. Pagination
// is limit + the opaque next_cursor (respond.EncodeCursor), passed back as
// cursor.
func NewPropertyFindTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("List the family's homes and other properties with value and rooms. " +
			"Pass id for one property with its rooms and counts."),
		mcp.WithString("id", mcp.Description("A property_id (UUID) to get exactly that property")),
		mcp.WithString("query", mcp.Description("Text in the name, address, type, notes or a room name, e.g. lake")),
		mcp.WithNumber("limit", mcp.Description("Max properties to return, 1-100 (default 25)"), mcp.Min(1), mcp.Max(maxLimit)),
		mcp.WithString("cursor", mcp.Description("next_cursor from the previous result, for the next page")),
		respond.ListSchema[propertyData](),
	}
	tool := mcp.NewTool("property_find", append(opts, annotations("Find properties", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}
		in, err := parseFindInput(req.GetArguments())
		if err != nil {
			return invalid(err, findExample), nil
		}
		page, total, err := findProperties(ctx, pool, claims.HouseholdID, in)
		if err != nil {
			return respond.Failed("find properties", err), nil
		}
		if in.id != nil && len(page) == 0 {
			return respond.NotFound("property", in.id.String(),
				"Call property_find without id to list the properties and their ids."), nil
		}
		if total == 0 && in.offset > 0 {
			// Past the end: count what there is so the summary is honest.
			if _, total, err = findProperties(ctx, pool, claims.HouseholdID, findInput{id: in.id, query: in.query, limit: 1}); err != nil {
				return respond.Failed("find properties", err), nil
			}
			in.offset = total
		}
		end := in.offset + len(page)
		return respond.List(findSummary(in, page, total), page, respond.Page{
			Total:      max(total-in.offset, len(page)),
			NextCursor: respond.CursorAfter(end, total),
			Narrow:     "Pass next_cursor as cursor for the next page, or add query.",
		}, findNext(in, page)...), nil
	}
	return "property_find", tool, handler
}

// findProperties loads one page of properties with their rooms, and the
// total number of matches.
func findProperties(ctx context.Context, pool *pgxpool.Pool, householdID string, in findInput) ([]propertyData, int, error) {
	rows, err := pool.Query(ctx, propertySelect, householdID, in.id, escapeLike(in.query), in.limit, in.offset)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	page, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (propertyData, error) {
		var (
			d  propertyData
			id uuid.UUID
		)
		var purchase *time.Time
		err := row.Scan(&id, &d.Name, &d.Address, &d.PropertyType, &d.Notes, &purchase,
			&d.PurchasePrice, &d.CurrentValue, &d.CreatedAt, &d.UpdatedAt,
			&d.Counts.Rooms, &d.Counts.Assets, &d.Counts.Bills, &d.Counts.Loans, &d.Counts.OpenMaintenance,
			&total)
		d.ID = id.String()
		if purchase != nil {
			ds := purchase.Format(time.DateOnly)
			d.PurchaseDate = &ds
		}
		d.Rooms = []roomData{}
		return d, err
	})
	if err != nil || len(page) == 0 {
		return page, total, err
	}

	ids := make([]uuid.UUID, 0, len(page))
	byID := make(map[string]int, len(page))
	for i, p := range page {
		ids = append(ids, uuid.MustParse(p.ID))
		byID[p.ID] = i
	}
	rows, err = pool.Query(ctx, roomSelect, ids)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			propID, roomID uuid.UUID
			r              roomData
		)
		if err := rows.Scan(&propID, &roomID, &r.Name, &r.Floor, &r.AssetCount); err != nil {
			return nil, 0, err
		}
		r.ID = roomID.String()
		i := byID[propID.String()]
		page[i].Rooms = append(page[i].Rooms, r)
	}
	return page, total, rows.Err()
}

// findSummary is property_find's one-line summary.
func findSummary(in findInput, page []propertyData, total int) string {
	switch {
	case total == 0 && !in.filtered():
		return "No properties yet."
	case total == 0:
		return "No properties match."
	case len(page) == 1 && total == 1:
		return fmt.Sprintf("Found property %s: %s.", page[0].Name, page[0].describe())
	case len(page) == 0:
		return fmt.Sprintf("Found %s; this page is past the end.", plural(total, "property"))
	case len(page) < total-in.offset || in.offset > 0:
		return fmt.Sprintf("Found %d properties; showing %d-%d.", total, in.offset+1, in.offset+len(page))
	default:
		names := ""
		for i, p := range page {
			if i > 0 {
				names += ", "
			}
			names += fmt.Sprintf("%s (%s)", p.Name, plural(p.Counts.Rooms, "room"))
		}
		return fmt.Sprintf("Found %d properties: %s.", total, names)
	}
}

// findNext is property_find's next-step hints.
func findNext(in findInput, page []propertyData) []string {
	if len(page) == 0 {
		if !in.filtered() {
			return []string{"The family has not added a property yet; the user can add one in the Home OS app."}
		}
		return []string{"Call property_find without query to list every property."}
	}
	p := page[0]
	next := []string{fmt.Sprintf("See what is in %s with asset_find property_id=%s (or room_id from rooms).", p.Name, p.ID)}
	if p.Counts.Loans > 0 {
		next = append(next, fmt.Sprintf("See its mortgage and other loans with loan_find property_id=%s.", p.ID))
	}
	if p.Counts.OpenMaintenance > 0 {
		next = append(next, fmt.Sprintf("See its open upkeep with maintenance_find property_id=%s.", p.ID))
	} else if len(next) < 2 {
		next = append(next, fmt.Sprintf("Tie a bill to it with bill_create property_id=%s.", p.ID))
	}
	return next
}

// parseFindInput validates property_find arguments.
func parseFindInput(raw map[string]any) (findInput, error) {
	in := findInput{limit: defaultLimit}
	args := withAliases(raw, findAliases)
	if s, err := optionalString(args, "id"); err != nil {
		return in, err
	} else if s != nil {
		id, perr := uuid.Parse(*s)
		if perr != nil {
			return in, badArg("id", fmt.Sprintf("id must be a UUID; got %q.", *s),
				"Pass a property's id (a UUID) from property_find, or use query to search by name.")
		}
		in.id = &id
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
			return in, badArg("cursor", fmt.Sprintf("cursor %q is not a cursor from property_find.", *c),
				"Pass next_cursor from the previous result as cursor, or leave cursor out for the first page.")
		}
		in.offset = off
	}
	return in, nil
}
