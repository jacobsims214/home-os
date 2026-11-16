package loan

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// querier is what both the pool and a transaction offer.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ---------------------------------------------------------------------------
// Access (household role policy, KB decisions/2026-11-15-role-policy.md)
// ---------------------------------------------------------------------------

// caller is who is calling, with the role re-read from memberships.
type caller struct {
	householdID string
	role        string
}

// callerOf resolves the caller's current role the way the vehicle, pet and
// property tools do: re-read from memberships for the claims' user and
// household on every call, so a demoted member loses access at once and a
// removed one gets nothing. A non-nil result is the isError to return.
func callerOf(ctx context.Context, q querier) (caller, *mcp.CallToolResult) {
	c := server.ClaimsFromContext(ctx)
	if c == nil {
		return caller{}, notSignedIn()
	}
	hid, err := uuid.Parse(c.HouseholdID)
	if err != nil {
		return caller{}, notSignedIn()
	}
	out := caller{householdID: hid.String(), role: c.Role}
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.AgentTokenID != "" { // an agent token always resolves to a users.id
			return caller{}, notSignedIn()
		}
		// Local-dev claims without a user: keep the claims' role.
		var ok bool
		err = q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM households WHERE id = $1)`, hid).Scan(&ok)
		if err == nil && !ok {
			return caller{}, notSignedIn()
		}
	} else {
		err = q.QueryRow(ctx, `SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
			hid, uid).Scan(&out.role)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return caller{}, notSignedIn()
	}
	if err != nil {
		return caller{}, respond.Failed("check your access", err)
	}
	return out, nil
}

// canRead reports whether role may see loans (API: money is readable by the
// family roles and viewer; housesitter and vendor see no money). Unknown
// roles may not, so a new role fails closed.
func canRead(role string) bool {
	return canWrite(role) || role == "viewer"
}

// canWrite reports whether role may create, change and delete loans (the
// family roles).
func canWrite(role string) bool {
	return role == "owner" || role == "family_manager" || role == "family_member"
}

func roleLabel(role string) string {
	if role == "" {
		return "current"
	}
	return role
}

// cannotRead is the refusal for a role that may not see money.
func cannotRead(role string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't see loans or other money: only an owner, family manager, family member or viewer can.", roleLabel(role)),
		Fix:     "Tell the user loans are not shared with their role; retrying will not help.",
	})
}

// cannotWrite is the refusal for a role that may not change loans. The
// server middleware refuses read-only roles first; this is the second check.
func cannotWrite(role, tool string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't change loans (tool %s): only an owner, family manager or family member can.", roleLabel(role), tool),
		Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
	})
}

// ---------------------------------------------------------------------------
// Matching loans by what people call them
// ---------------------------------------------------------------------------

// fillerWords are dropped from a query: "what's the rate on the Odyssey
// loan" matches like "odyssey".
var fillerWords = map[string]bool{
	"a": true, "an": true, "the": true, "our": true, "my": true, "your": true, "his": true, "her": true,
	"their": true, "on": true, "for": true, "of": true, "to": true, "is": true, "what": true, "whats": true,
	"when": true, "how": true, "much": true, "and": true, "with": true, "at": true, "in": true, "about": true,
	"loan": true, "loans": true, "payment": true, "payments": true, "rate": true, "balance": true,
	"due": true, "day": true, "interest": true, "monthly": true, "owe": true, "owed": true, "left": true,
	"note": true, "financing": true, "finance": true,
}

// kindWords map words people use for a loan's purpose to the subject type
// it is linked to: "the mortgage" is the loan on a property, "the car
// loan" the loan on a vehicle, even when the loan is named after the bank.
var kindWords = map[string]string{
	"mortgage": subjectProperty, "home": subjectProperty, "house": subjectProperty, "heloc": subjectProperty,
	"property": subjectProperty, "land": subjectProperty,
	"car": subjectVehicle, "auto": subjectVehicle, "vehicle": subjectVehicle, "van": subjectVehicle,
	"minivan": subjectVehicle, "truck": subjectVehicle, "suv": subjectVehicle, "motorcycle": subjectVehicle,
}

// words lowercases s, drops apostrophes ("what's" -> "whats") and splits it
// on anything that is not a letter or digit.
func words(s string) []string {
	s = strings.NewReplacer("'s ", " ", "’s ", " ", "'", "", "’", "").Replace(strings.ToLower(s) + " ")
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// queryTerms are the words of a query that are not filler.
func queryTerms(q string) []string {
	var out []string
	for _, w := range words(q) {
		if !fillerWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// kindOf is the subject type a word stands for ("" for none). A plural
// ("mortgages") counts.
func kindOf(w string) string {
	if k := kindWords[w]; k != "" {
		return k
	}
	return kindWords[strings.TrimSuffix(w, "s")]
}

// matchScore reports whether r matches every term and how many matched by
// text (name, lender, notes, linked subject) rather than by kind alone.
func matchScore(r loanRow, terms []string) (score int, ok bool) {
	hay := " " + strings.Join(words(strings.Join([]string{
		r.Name, str(r.Lender), str(r.Notes), str(r.SubjectName),
	}, " ")), " ") + " "
	kind := subjectTypeOf(r)
	for _, t := range terms {
		switch {
		case strings.Contains(hay, t):
			score++
		case kind != "" && kindOf(t) == kind:
		default:
			return 0, false
		}
	}
	return score, true
}

// matchLoans keeps the loans that match query, best matches only: a loan
// matched by text beats one matched only by its kind, so "mortgage" finds
// the loan named Mortgage and not the HELOC on the same house. An exact
// name (any case) beats everything. A query of only filler words ("the
// loan") matches every loan.
func matchLoans(rows []loanRow, query string) []loanRow {
	q := strings.Join(words(query), " ")
	var exact []loanRow
	for _, r := range rows {
		if strings.Join(words(r.Name), " ") == q {
			exact = append(exact, r)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	terms := queryTerms(query)
	if len(terms) == 0 {
		return rows
	}
	best, out := -1, []loanRow(nil)
	for _, r := range rows {
		score, ok := matchScore(r, terms)
		switch {
		case !ok || score < best:
		case score > best:
			best, out = score, []loanRow{r}
		default:
			out = append(out, r)
		}
	}
	return out
}

// subjectTypeOf is the loan's subject type, "" when it is tied to nothing.
func subjectTypeOf(r loanRow) string {
	switch {
	case r.VehicleID != nil:
		return subjectVehicle
	case r.AssetID != nil:
		return subjectAsset
	case r.PropertyID != nil:
		return subjectProperty
	}
	return ""
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// loadAll loads every loan of the household sorted by name.
func loadAll(ctx context.Context, q querier, householdID string) ([]loanRow, error) {
	rows, err := q.Query(ctx, loanSelect+` WHERE l.household_id = $1 ORDER BY lower(l.name), l.created_at, l.id`, householdID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (loanRow, error) { return scanLoan(row) })
}

// maxListed is how many loans an error lists.
const maxListed = 30

// listLoans is "Mortgage (id), Odyssey loan (id)" for error messages.
func listLoans(rows []loanRow) string {
	parts := make([]string, 0, min(len(rows), maxListed))
	for i, r := range rows {
		if i == maxListed {
			parts = append(parts, fmt.Sprintf("and %d more", len(rows)-maxListed))
			break
		}
		label := r.Name
		if r.SubjectName != nil && !strings.Contains(strings.ToLower(r.Name), strings.ToLower(*r.SubjectName)) {
			label += " on " + *r.SubjectName
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", label, r.ID))
	}
	return strings.Join(parts, ", ")
}

// resolveLoan finds the loan that ref names: a loan_id (UUID), or a loan's
// name or what it's for ("Mortgage", "the Odyssey loan", "car loan"). One
// match is returned; none or several give an isError that lists the
// candidates with their ids. A loan of another family is not found.
func resolveLoan(ctx context.Context, q querier, householdID, ref string) (loanRow, *mcp.CallToolResult, error) {
	if id, err := uuid.Parse(ref); err == nil {
		r, err := loadLoan(ctx, q, id, householdID)
		if errors.Is(err, pgx.ErrNoRows) {
			return r, respond.NotFound("loan", id.String(), "Call loan_find to get the loan_id."), nil
		}
		return r, nil, err
	}
	all, err := loadAll(ctx, q, householdID)
	if err != nil {
		return loanRow{}, nil, err
	}
	matches := matchLoans(all, ref)
	switch {
	case len(all) == 0:
		return loanRow{}, respond.Fail(respond.Error{
			Field:   "loan_id",
			Message: fmt.Sprintf("No loan matches %q: this family has no loans yet.", ref),
			Fix:     "Add it with loan_create first.",
		}), nil
	case len(matches) == 0:
		return loanRow{}, respond.Fail(respond.Error{
			Field:   "loan_id",
			Message: fmt.Sprintf("No loan matches %q. The family's loans: %s.", ref, listLoans(all)),
			Fix:     "Pass loan_id with one of these ids, or the loan's exact name.",
		}), nil
	case len(matches) > 1:
		sort.SliceStable(matches, func(i, j int) bool { return strings.ToLower(matches[i].Name) < strings.ToLower(matches[j].Name) })
		return loanRow{}, respond.Fail(respond.Error{
			Field:   "loan_id",
			Message: fmt.Sprintf("%d loans match %q: %s.", len(matches), ref, listLoans(matches)),
			Fix:     "Ask the user which one, then pass loan_id with that loan's id.",
		}), nil
	}
	return matches[0], nil, nil
}
