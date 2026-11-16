package asset

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// findAliases are old list_assets/get_asset parameter names.
var findAliases = map[string]string{"asset_id": "id"}

// findExample is a valid asset_find call for error examples.
var findExample = map[string]any{"query": "dishwasher"}

// findInput is the validated asset_find arguments.
type findInput struct {
	id         *uuid.UUID
	query      string
	category   string
	propertyID *uuid.UUID
	roomID     *uuid.UUID
	limit      int
	offset     int
}

func (in findInput) filtered() bool {
	return in.id != nil || in.query != "" || in.category != "" || in.propertyID != nil || in.roomID != nil
}

// NewAssetFindTool creates the asset_find MCP tool (catalog v2, class R),
// replacing list_assets and get_asset. It lists the household's assets
// sorted by name, each with its property and room names and its warranty
// status in the family's timezone. query matches name, maker, model, serial
// number, category, room and property; category matches exactly, any case.
// Pagination is limit + the opaque next_cursor (respond.EncodeCursor),
// passed back as cursor.
func NewAssetFindTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("List or search things the family owns by name, maker, model, room or property. " +
			"Pass id for full details incl. warranty."),
		mcp.WithString("id", mcp.Description("An asset_id (UUID) to get exactly that asset")),
		mcp.WithString("query", mcp.Description("Text in name, maker, model, serial, room or property, e.g. Bosch")),
		mcp.WithString("category", mcp.Description("Exact category, any case, e.g. appliance")),
		mcp.WithString("property_id", mcp.Description("Only assets at this property (UUID from property_find)")),
		mcp.WithString("room_id", mcp.Description("Only assets in this room (UUID from property_find id=...)")),
		mcp.WithNumber("limit", mcp.Description("Max assets to return, 1-100 (default 25)"), mcp.Min(1), mcp.Max(maxLimit)),
		mcp.WithString("cursor", mcp.Description("next_cursor from the previous result, for the next page")),
		respond.ListSchema[assetData](),
	}
	tool := mcp.NewTool("asset_find", append(opts, annotations("Find assets", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		household := householdOf(ctx)
		if household == nil {
			return notSignedIn(), nil
		}
		in, err := parseFindInput(withAliases(req.GetArguments(), findAliases))
		if err != nil {
			return invalid(err, findExample), nil
		}
		today, err := householdToday(ctx, pool, *household)
		if err != nil {
			return respond.Failed("find assets", err), nil
		}

		rows, err := pool.Query(ctx, assetSelect+`
			WHERE a.household_id = $1
			  AND ($2::uuid IS NULL OR a.id = $2)
			  AND ($3 = '' OR a.name ILIKE '%' || $3 || '%' OR a.manufacturer ILIKE '%' || $3 || '%'
			       OR a.model ILIKE '%' || $3 || '%' OR a.serial_number ILIKE '%' || $3 || '%'
			       OR a.category ILIKE '%' || $3 || '%' OR r.name ILIKE '%' || $3 || '%'
			       OR p.name ILIKE '%' || $3 || '%'
			       OR concat_ws(' ', a.manufacturer, a.model) ILIKE '%' || $3 || '%')
			  AND ($4 = '' OR lower(a.category) = lower($4))
			  AND ($5::uuid IS NULL OR a.property_id = $5)
			  AND ($6::uuid IS NULL OR a.room_id = $6)
			ORDER BY lower(a.name), a.created_at, a.id`,
			*household, in.id, escapeLike(in.query), in.category, in.propertyID, in.roomID,
		)
		if err != nil {
			return respond.Failed("find assets", err), nil
		}
		loaded, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (assetRow, error) { return scanAsset(row) })
		if err != nil {
			return respond.Failed("find assets", err), nil
		}
		if in.id != nil && len(loaded) == 0 {
			return respond.NotFound("asset", in.id.String(), "Call asset_find without id (or with query) to list assets and their ids."), nil
		}

		total := len(loaded)
		if in.offset > total {
			in.offset = total
		}
		end := min(in.offset+in.limit, total)
		page := make([]assetData, 0, end-in.offset)
		for _, r := range loaded[in.offset:end] {
			page = append(page, r.data(today))
		}
		next := findNext(in, page)
		if len(page) == 1 && total == 1 {
			if h := guideHint(ctx, pool, page[0].ID); h != "" {
				next = append(next, h)
			}
		}
		return respond.List(findSummary(in, page, total), page, respond.Page{
			Total:      total - in.offset,
			NextCursor: respond.CursorAfter(end, total),
			Narrow:     "Pass next_cursor as cursor for the next page, or add query, category, property_id or room_id.",
		}, next...), nil
	}
	return "asset_find", tool, handler
}

// findSummary is asset_find's one-line summary.
func findSummary(in findInput, page []assetData, total int) string {
	noun := "assets"
	if total == 1 {
		noun = "asset"
	}
	switch {
	case total == 0 && !in.filtered():
		return "The family has no assets recorded yet."
	case total == 0:
		return "No assets match."
	case len(page) == 1 && total == 1:
		return fmt.Sprintf("Found asset %s.", page[0].describe())
	case len(page) < total-in.offset || in.offset > 0:
		return fmt.Sprintf("Found %d %s; showing %d-%d.", total, noun, in.offset+1, in.offset+len(page))
	default:
		return fmt.Sprintf("Found %d %s.", total, noun)
	}
}

// findNext is asset_find's next-step hints.
func findNext(in findInput, page []assetData) []string {
	if len(page) == 0 {
		if !in.filtered() {
			return []string{"Add one with asset_create (name, category; room_id or property_id optional)."}
		}
		return []string{"Call asset_find with fewer filters, or add the asset with asset_create."}
	}
	first := page[0]
	next := []string{fmt.Sprintf("Change one with asset_update asset_id=%s.", first.ID)}
	if len(page) == 1 {
		next = append(next, fmt.Sprintf("Plan upkeep with maintenance_create asset_id=%s.", first.ID))
	}
	return next
}

// parseFindInput validates asset_find arguments.
func parseFindInput(args map[string]any) (findInput, error) {
	in := findInput{limit: defaultLimit}
	var err error
	if in.id, err = optionalUUID(args, "id"); err != nil {
		return in, err
	}
	if q, err := optionalString(args, "query"); err != nil {
		return in, err
	} else if q != nil {
		in.query = *q
	}
	if c, err := optionalString(args, "category"); err != nil {
		return in, err
	} else if c != nil {
		in.category = *c
	}
	if in.propertyID, err = optionalUUID(args, "property_id"); err != nil {
		return in, err
	}
	if in.roomID, err = optionalUUID(args, "room_id"); err != nil {
		return in, err
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
			return in, badArg("cursor", fmt.Sprintf("cursor %q is not a cursor from asset_find.", *c),
				"Pass next_cursor from the previous result as cursor, or leave cursor out for the first page.")
		}
		in.offset = off
	}
	return in, nil
}
