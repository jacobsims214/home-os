package loan

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// findAliases are old loan_find/get_loan parameter names.
var findAliases = map[string]string{"loan_id": "id", "loan": "query", "name": "query", "search": "query", "q": "query"}

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
// timezone) and its subject. query matches a loan by its name, lender or
// notes, by the vehicle/property it is linked to ("the Odyssey loan"), or by
// its kind ("mortgage" is the loan on a property, "car loan" the one on a
// vehicle); filler words are ignored and the best matches win (matchLoans).
// property_id matches loans on that property, including loans on its assets.
// Loans are money: housesitter and vendor are refused (role re-read per
// call). Pagination is limit + the opaque
// next_cursor (respond.EncodeCursor), passed back as cursor.
func NewLoanFindTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Find loans with rate, lender, monthly payment, due day, next due date, balance and term end. " +
			"One call answers \"when is the mortgage due, how much, what's the rate?\"."),
		mcp.WithString("id", mcp.Description("A loan_id (UUID) to get exactly that loan")),
		mcp.WithString("query", mcp.Description("Loan name, lender, or what it's for: mortgage, Odyssey, car loan")),
		mcp.WithString("property_id", mcp.Description("Only loans on this property (UUID from property_find)")),
		mcp.WithString("vehicle_id", mcp.Description("Only loans on this vehicle (UUID from vehicle_find)")),
		mcp.WithNumber("limit", mcp.Description("Max loans to return, 1-100 (default 25)"), mcp.Min(1), mcp.Max(maxLimit)),
		mcp.WithString("cursor", mcp.Description("next_cursor from the previous result, for the next page")),
		respond.ListSchema[loanData](),
	}
	tool := mcp.NewTool("loan_find", append(opts, annotations("Find loans", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, deny := callerOf(ctx, pool)
		if deny != nil {
			return deny, nil
		}
		if !canRead(c.role) {
			return cannotRead(c.role), nil
		}
		in, err := parseFindInput(req.GetArguments())
		if err != nil {
			return invalid(err, findExample), nil
		}
		today, err := householdToday(ctx, pool, c.householdID)
		if err != nil {
			return respond.Failed("find loans", err), nil
		}

		rows, err := pool.Query(ctx, loanSelect+`
			WHERE l.household_id = $1
			  AND ($2::uuid IS NULL OR l.id = $2)
			  AND ($3::uuid IS NULL OR l.property_id = $3)
			  AND ($4::uuid IS NULL OR l.vehicle_id = $4)
			  AND ($5::uuid IS NULL OR l.asset_id = $5)
			  AND ($6 = '' OR ($6 = 'property' AND l.property_id IS NOT NULL)
			       OR ($6 = 'vehicle' AND l.vehicle_id IS NOT NULL) OR ($6 = 'asset' AND l.asset_id IS NOT NULL))
			ORDER BY lower(l.name), l.created_at, l.id`,
			c.householdID, in.id, in.propertyID, in.vehicleID, in.assetID, in.subjectKind,
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
		if in.query != "" {
			loaded = matchLoans(loaded, in.query)
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
			Total:      total - in.offset,
			NextCursor: respond.CursorAfter(end, total),
			Narrow:     "Pass next_cursor as cursor for the next page, or add query, property_id or vehicle_id.",
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
		return []string{"Call loan_find without query to list every loan, or add the loan with loan_create."}
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
	if q, err := optionalString(args, "query"); err != nil {
		return in, err
	} else if q != nil {
		in.query = *q
	}
	// A loan name passed as id ("Mortgage") is searched for, not refused:
	// small models put whatever names the loan in the id param.
	if s, err := optionalString(args, "id"); err != nil {
		return in, err
	} else if s != nil {
		if id, perr := uuid.Parse(*s); perr == nil {
			in.id = &id
		} else if in.query == "" {
			in.query = *s
		} else {
			return in, badArg("id", fmt.Sprintf("id must be a UUID; got %q.", *s), uuidFixes["id"])
		}
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
		off, ok := respond.DecodeCursor(*c)
		if !ok {
			return in, badArg("cursor", fmt.Sprintf("cursor %q is not a cursor from loan_find.", *c),
				"Pass next_cursor from the previous result as cursor, or leave cursor out for the first page.")
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
