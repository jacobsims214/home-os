package vehicle

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// findAliases are old get_vehicle / guessed parameter names for id.
var findAliases = []string{"vehicle_id"}

// findExample is a valid vehicle_find call for error examples.
var findExample = map[string]any{"query": "Honda"}

// findInput is the validated vehicle_find arguments.
type findInput struct {
	id     *uuid.UUID
	query  string
	limit  int
	offset int
}

func (in findInput) filtered() bool { return in.id != nil || in.query != "" }

// NewVehicleFindTool creates the vehicle_find MCP tool (catalog v2, class R),
// replacing list_vehicles and get_vehicle. It lists the family's vehicles by
// make, model and year, each with counts of the loans, bills and pending
// upkeep tied to it by vehicle_id. With id it also returns those records in
// data.related. Pagination is limit + the opaque next_cursor
// (respond.EncodeCursor), passed back as cursor.
func NewVehicleFindTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("List or search the family's vehicles. Pass id for full details: VIN, plate, value, " +
			"and its loans, bills and pending upkeep."),
		mcp.WithString("id", mcp.Description("A vehicle_id (UUID) to get that vehicle in full")),
		mcp.WithString("query", mcp.Description("Text in the make, model, year, VIN, plate, color or notes, e.g. Honda")),
		mcp.WithNumber("limit", mcp.Description("Max vehicles to return, 1-100 (default 25)"), mcp.Min(1), mcp.Max(maxLimit)),
		mcp.WithString("cursor", mcp.Description("next_cursor from the previous result, for the next page")),
		respond.ListSchema[vehicleData](),
	}
	tool := mcp.NewTool("vehicle_find", append(opts, annotations("Find vehicles", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}
		in, err := parseFindInput(req.GetArguments())
		if err != nil {
			return invalid(err, findExample), nil
		}

		// The role is re-read from memberships on every call (callerOf, as the
		// write tools do), so a demotion hides money at once. auth.CanSeeMoney
		// decides the money columns, the related loans and bills, and the hints.
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		money := auth.CanSeeMoney(c.role)

		rows, err := pool.Query(ctx, vehicleSelect+`
			WHERE v.household_id = $2
			  AND ($3::uuid IS NULL OR v.id = $3)
			  AND ($4 = '' OR concat_ws(' ', v.year, v.make, v.model) ILIKE '%' || $4 || '%'
			       OR v.vin ILIKE '%' || $4 || '%' OR v.license_plate ILIKE '%' || $4 || '%'
			       OR v.color ILIKE '%' || $4 || '%' OR v.notes ILIKE '%' || $4 || '%')
			ORDER BY lower(COALESCE(v.make, '')), lower(COALESCE(v.model, '')), v.year DESC NULLS LAST, v.created_at, v.id`,
			money, claims.HouseholdID, in.id, escapeLike(in.query),
		)
		if err != nil {
			return respond.Failed("find vehicles", err), nil
		}
		loaded, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (vehicleRow, error) { return scanVehicle(row) })
		if err != nil {
			return respond.Failed("find vehicles", err), nil
		}
		if in.id != nil && len(loaded) == 0 {
			return respond.NotFound("vehicle", in.id.String(), "Call vehicle_find without id to list vehicles and their ids."), nil
		}

		total := len(loaded)
		if in.offset > total {
			in.offset = total
		}
		end := min(in.offset+in.limit, total)
		page := make([]vehicleData, 0, end-in.offset)
		for _, r := range loaded[in.offset:end] {
			page = append(page, r.data(money))
		}
		if in.id != nil && len(page) == 1 {
			if page[0].Related, err = loadRelated(ctx, pool, *in.id, claims.HouseholdID, money); err != nil {
				return respond.Failed("find vehicles", err), nil
			}
		}

		return respond.List(findSummary(in, page, total), page, respond.Page{
			Total:      total - in.offset,
			NextCursor: respond.CursorAfter(end, total),
			Narrow:     "Pass next_cursor as cursor for the next page, or add query.",
		}, findNext(in, page, money)...), nil
	}
	return "vehicle_find", tool, handler
}

// findSummary is vehicle_find's one-line summary.
func findSummary(in findInput, page []vehicleData, total int) string {
	noun := "vehicles"
	if total == 1 {
		noun = "vehicle"
	}
	switch {
	case total == 0 && !in.filtered():
		return "No vehicles yet."
	case total == 0:
		return "No vehicles match."
	case len(page) == 1 && total == 1:
		s := fmt.Sprintf("Found vehicle %s", page[0].Name)
		if d := page[0].describe(); d != "" {
			s += ": " + d
		}
		return s + "."
	case len(page) < total-in.offset || in.offset > 0:
		return fmt.Sprintf("Found %d %s; showing %d-%d.", total, noun, in.offset+1, in.offset+len(page))
	default:
		return fmt.Sprintf("Found %d %s.", total, noun)
	}
}

// findNext is vehicle_find's next-step hints. withMoney is the caller's
// auth.CanSeeMoney: a role that may not see money is never pointed at
// loan_create or loan_find, which would only refuse it (and would tell it the
// car has a loan worth looking at).
func findNext(in findInput, page []vehicleData, withMoney bool) []string {
	if len(page) == 0 {
		if !in.filtered() {
			return []string{"Add one with vehicle_create make=… model=… year=…; then upkeep links to it by vehicle_id."}
		}
		return []string{"Call vehicle_find without query to list every vehicle."}
	}
	v := page[0]
	if len(page) > 1 {
		next := []string{fmt.Sprintf("Get one in full, with its upkeep, with vehicle_find id=%s.", v.ID)}
		if withMoney {
			next[0] = fmt.Sprintf("Get one in full, with its loans, bills and upkeep, with vehicle_find id=%s.", v.ID)
			next = append(next, "Pass a vehicle's id as vehicle_id to loan_create or maintenance_create to link it.")
		} else {
			next = append(next, "Pass a vehicle's id as vehicle_id to maintenance_create to link it.")
		}
		return next
	}
	next := []string{}
	if v.Related == nil {
		if withMoney {
			next = append(next, fmt.Sprintf("See its loans, bills and upkeep with vehicle_find id=%s.", v.ID))
		} else {
			next = append(next, fmt.Sprintf("See its upkeep with vehicle_find id=%s.", v.ID))
		}
	}
	next = append(next, fmt.Sprintf("Schedule upkeep (oil change, tires) with maintenance_create vehicle_id=%s.", v.ID))
	switch {
	case !withMoney:
	case v.LoanCount == nil || *v.LoanCount == 0:
		next = append(next, fmt.Sprintf("Record its car loan with loan_create vehicle_id=%s.", v.ID))
	default:
		next = append(next, fmt.Sprintf("See its loan details with loan_find vehicle_id=%s.", v.ID))
	}
	return next
}

// parseFindInput validates vehicle_find arguments.
func parseFindInput(args map[string]any) (findInput, error) {
	in := findInput{limit: defaultLimit}
	var err error
	idFix := "Pass a vehicle's id (a UUID) from vehicle_find, or use query to search by make or model."
	if in.id, err = optionalUUID(args, "id", idFix); err != nil {
		return in, err
	}
	for _, alias := range findAliases {
		if in.id != nil {
			break
		}
		if in.id, err = optionalUUID(args, alias, idFix); err != nil {
			return in, err
		}
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
			return in, badArg("cursor", fmt.Sprintf("cursor %q is not a cursor from vehicle_find.", *c),
				"Pass next_cursor from the previous result as cursor, or leave cursor out for the first page.")
		}
		in.offset = off
	}
	return in, nil
}
