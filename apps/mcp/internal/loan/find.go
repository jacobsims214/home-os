package loan

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// findAliases are old loan_find/get_loan parameter names.
var findAliases = map[string]string{"loan_id": "id"}

// findExample is a valid loan_find call for error examples.
var findExample = map[string]any{"query": "mortgage"}

// findInput is the validated loan_find arguments.
type findInput struct {
	id         *uuid.UUID
	query      string
	propertyID *uuid.UUID
	vehicleID  *uuid.UUID
	assetID    *uuid.UUID
	// subjectKind is the legacy list_loans entity_type filter on its own:
	// property, vehicle or asset (loans tied to any one of that kind).
	subjectKind string
	limit       int
	offset      int
}

func (in findInput) filtered() bool {
	return in.id != nil || in.query != "" || in.propertyID != nil || in.vehicleID != nil || in.assetID != nil || in.subjectKind != ""
}

// NewLoanFindTool creates the loan_find MCP tool (catalog v2, class R),
// replacing list_loans and get_loan. It lists the household's loans sorted
// by name, each with its next payment date (from due_day, in the family's
// timezone) and its subject. property_id matches loans on that property,
// including loans on its assets. Pagination is limit + an opaque offset
// cursor, given in the narrow hint because respond has no next_cursor.
func NewLoanFindTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("List or search loans with lender, rate, monthly payment, due day and balance. " +
			"Answers \"when is the mortgage due and what's the rate?\"."),
		mcp.WithString("id", mcp.Description("A loan_id (UUID) to get exactly that loan")),
		mcp.WithString("query", mcp.Description("Text in the name, lender, notes or what it's for, e.g. mortgage")),
		mcp.WithString("property_id", mcp.Description("Only loans on this property (UUID from property_find)")),
		mcp.WithString("vehicle_id", mcp.Description("Only loans on this vehicle (UUID from vehicle_find)")),
		mcp.WithNumber("limit", mcp.Description("Max loans to return, 1-100 (default 25)"), mcp.Min(1), mcp.Max(maxLimit)),
		mcp.WithString("cursor", mcp.Description("Cursor from a previous result's narrow hint, for the next page")),
		respond.ListSchema[loanData](),
	}
	tool := mcp.NewTool("loan_find", append(opts, annotations("Find loans", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}
		in, err := parseFindInput(req.GetArguments())
		if err != nil {
			return invalid(err, findExample), nil
		}
		today, err := householdToday(ctx, pool, claims.HouseholdID)
		if err != nil {
			return respond.Failed("find loans", err), nil
		}

		rows, err := pool.Query(ctx, loanSelect+`
			WHERE l.household_id = $1
			  AND ($2::uuid IS NULL OR l.id = $2)
			  AND ($3 = '' OR l.name ILIKE '%' || $3 || '%' OR l.lender ILIKE '%' || $3 || '%'
			       OR l.notes ILIKE '%' || $3 || '%' OR p.name ILIKE '%' || $3 || '%'
			       OR a.name ILIKE '%' || $3 || '%' OR concat_ws(' ', v.year, v.make, v.model) ILIKE '%' || $3 || '%')
			  AND ($4::uuid IS NULL OR l.property_id = $4)
			  AND ($5::uuid IS NULL OR l.vehicle_id = $5)
			  AND ($6::uuid IS NULL OR l.asset_id = $6)
			  AND ($7 = '' OR ($7 = 'property' AND l.property_id IS NOT NULL)
			       OR ($7 = 'vehicle' AND l.vehicle_id IS NOT NULL) OR ($7 = 'asset' AND l.asset_id IS NOT NULL))
			ORDER BY lower(l.name), l.created_at, l.id`,
			claims.HouseholdID, in.id, escapeLike(in.query), in.propertyID, in.vehicleID, in.assetID, in.subjectKind,
		)
		if err != nil {
			return respond.Failed("find loans", err), nil
		}
		loaded, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (loanRow, error) { return scanLoan(row) })
		if err != nil {
			return respond.Failed("find loans", err), nil
		}
		if in.id != nil && len(loaded) == 0 {
			return respond.NotFound("loan", in.id.String(), "Call loan_find without id to list loans and their ids."), nil
		}

		total := len(loaded)
		if in.offset > total {
			in.offset = total
		}
		end := min(in.offset+in.limit, total)
		page := make([]loanData, 0, end-in.offset)
		for _, r := range loaded[in.offset:end] {
			page = append(page, r.data(today))
		}

		return respond.List(findSummary(in, page, total), page, respond.Page{
			Total:  total - in.offset,
			Narrow: fmt.Sprintf("Pass cursor=%q for the next page, or add query, property_id or vehicle_id.", strconv.Itoa(end)),
		}, findNext(in, page)...), nil
	}
	return "loan_find", tool, handler
}

// findSummary is loan_find's one-line summary.
func findSummary(in findInput, page []loanData, total int) string {
	noun := "loans"
	if total == 1 {
		noun = "loan"
	}
	switch {
	case total == 0 && !in.filtered():
		return "No loans yet."
	case total == 0:
		return "No loans match."
	case len(page) == 1 && total == 1:
		return fmt.Sprintf("Found loan %s: %s.", page[0].Name, page[0].describe())
	case len(page) < total-in.offset || in.offset > 0:
		return fmt.Sprintf("Found %d %s; showing %d-%d.", total, noun, in.offset+1, in.offset+len(page))
	default:
		return fmt.Sprintf("Found %d %s.", total, noun)
	}
}

// findNext is loan_find's next-step hints.
func findNext(in findInput, page []loanData) []string {
	if len(page) == 0 {
		if !in.filtered() {
			return []string{"Add one with loan_create (name, original_amount, remaining_balance)."}
		}
		return []string{"Call loan_find with fewer filters, or add the loan with loan_create."}
	}
	first := page[0]
	next := []string{fmt.Sprintf("Change one (e.g. a new remaining_balance) with loan_update loan_id=%s.", first.ID)}
	for _, l := range page {
		if l.MonthlyPayment == nil && !l.PaidOff {
			next = append(next, fmt.Sprintf("%s has no monthly_payment; set it with loan_update loan_id=%s.", l.Name, l.ID))
			break
		}
	}
	return append(next, "See monthly totals and net worth with budget_summary.")
}

// parseFindInput validates loan_find arguments.
func parseFindInput(raw map[string]any) (findInput, error) {
	in := findInput{limit: defaultLimit}
	// Legacy list_loans filter: entity_type alone means "loans on any
	// property / vehicle / asset".
	if given(raw, "entity_type") && !given(raw, "entity_id") {
		kind, err := optionalString(raw, "entity_type")
		if err != nil {
			return in, err
		}
		raw = withoutKeys(raw, "entity_type")
		if kind != nil {
			switch k := strings.ToLower(*kind); k {
			case subjectProperty, subjectVehicle, subjectAsset:
				in.subjectKind = k
			default:
				return in, badArg("entity_type", fmt.Sprintf("Loans can't be tied to a %q.", *kind),
					"Pass property_id or vehicle_id, or leave them out.")
			}
		}
	}
	args, err := withAliases(raw, findAliases)
	if err != nil {
		return in, err
	}
	if in.id, err = optionalUUID(args, "id"); err != nil {
		return in, err
	}
	if q, err := optionalString(args, "query"); err != nil {
		return in, err
	} else if q != nil {
		in.query = *q
	}
	if in.propertyID, err = optionalUUID(args, "property_id"); err != nil {
		return in, err
	}
	if in.vehicleID, err = optionalUUID(args, "vehicle_id"); err != nil {
		return in, err
	}
	// asset_id is not advertised on loan_find; it arrives via the legacy
	// entity_type=asset alias.
	if in.assetID, err = optionalUUID(args, "asset_id"); err != nil {
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
		off, convErr := strconv.Atoi(*c)
		if convErr != nil || off < 0 {
			return in, badArg("cursor", fmt.Sprintf("cursor %q is not a cursor from loan_find.", *c),
				"Pass the cursor value from the previous result's narrow hint, or leave cursor out for the first page.")
		}
		in.offset = off
	}
	return in, nil
}

// withoutKeys returns a copy of args without keys.
func withoutKeys(args map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	for _, k := range keys {
		delete(out, k)
	}
	return out
}
