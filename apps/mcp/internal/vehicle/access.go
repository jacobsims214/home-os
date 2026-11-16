package vehicle

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// duplicateWindow is the vehicle_create loop guard (catalog §1 "Loop guard").
const duplicateWindow = "10 minutes"

// Column sizes (005_vehicles, 020_family_model, 031).
const (
	maxMakeLen  = 100
	maxModelLen = 100
	maxVINLen   = 50
	maxPlateLen = 50
	maxColorLen = 50
	maxNotesLen = 10000
	// minYear is the first car (Benz Patent-Motorwagen, 1886).
	minYear = 1886
	// maxMoney is the NUMERIC(12,2) limit of purchase_price/current_value.
	maxMoney = 9_999_999_999.99
)

// querier is what both the pool and a transaction offer.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ---------------------------------------------------------------------------
// Access
// ---------------------------------------------------------------------------

// caller is who is calling, with the role re-read from memberships.
type caller struct {
	householdID uuid.UUID
	role        string
}

// callerOf resolves the caller's current role the way the people, pet and
// property tools do: re-read from memberships for the claims' user and
// household, so a demoted member loses write access at once and a removed
// one gets nothing. A non-nil result is the isError to return.
func callerOf(ctx context.Context, q querier) (caller, *mcp.CallToolResult) {
	c := server.ClaimsFromContext(ctx)
	if c == nil {
		return caller{}, notSignedIn()
	}
	hid, err := uuid.Parse(c.HouseholdID)
	if err != nil {
		return caller{}, notSignedIn()
	}
	out := caller{householdID: hid, role: c.Role}
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

// canWrite reports whether role may create, change and delete vehicles (API
// ResVehicles write: the family roles). Unknown roles may not, so a new role
// fails closed.
func canWrite(role string) bool {
	return role == "owner" || role == "family_manager" || role == "family_member"
}

// cannotWrite is the refusal for a role that may not change vehicles. The
// server middleware refuses these roles first; this is the second check.
func cannotWrite(role, tool string) *mcp.CallToolResult {
	label := role
	if label == "" {
		label = "current"
	}
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't change vehicles (tool %s): only an owner, family manager or family member can.", label, tool),
		Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
	})
}

// ---------------------------------------------------------------------------
// Argument helpers for the write tools
// ---------------------------------------------------------------------------

// given reports whether args has a non-null value for key.
func given(args map[string]any, key string) bool {
	v, present := args[key]
	return present && v != nil
}

// withAliases returns a copy of args with other parameter names moved to
// their catalog names (a catalog name that is present wins).
func withAliases(args map[string]any, aliases map[string]string) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	for old, name := range aliases {
		if v, ok := out[old]; ok {
			if !given(out, name) {
				out[name] = v
			}
			delete(out, old)
		}
	}
	return out
}

// textField is one optional text param. An absent param or "" is not given
// (small models send "" for unused params); JSON null asks to clear it.
type textField struct {
	set   bool // given with a value
	clear bool // JSON null
	val   string
}

func (f textField) given() bool { return f.set || f.clear }

// ptr is the value to store: nil when not set.
func (f textField) ptr() *string {
	if !f.set {
		return nil
	}
	v := f.val
	return &v
}

func readText(args map[string]any, key string, maxLen int) (textField, error) {
	v, present := args[key]
	if !present {
		return textField{}, nil
	}
	if v == nil {
		return textField{clear: true}, nil
	}
	s, ok := v.(string)
	if !ok {
		return textField{}, badArg(key, key+" must be text.", fmt.Sprintf("Pass %s as text in quotes.", key))
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return textField{}, nil
	}
	if utf8.RuneCountInString(s) > maxLen {
		return textField{}, badArg(key, fmt.Sprintf("%s must be at most %d characters.", key, maxLen),
			fmt.Sprintf("Pass a shorter %s.", key))
	}
	return textField{set: true, val: s}, nil
}

// moneyField is one optional amount param, with the same absent/""/null
// rules as textField.
type moneyField struct {
	set, clear bool
	val        float64
}

func (f moneyField) given() bool { return f.set || f.clear }

func (f moneyField) ptr() *float64 {
	if !f.set {
		return nil
	}
	v := f.val
	return &v
}

// readMoney reads a decimal amount in the family's currency. Numbers sent as
// text are accepted, with "$", "," and spaces stripped ("$18,500").
func readMoney(args map[string]any, key string) (moneyField, error) {
	v, present := args[key]
	if !present {
		return moneyField{}, nil
	}
	if v == nil {
		return moneyField{clear: true}, nil
	}
	bad := badArg(key, fmt.Sprintf("%s must be an amount of money, e.g. 18500 or 1250.50.", key),
		fmt.Sprintf("Pass %s as a plain number without currency signs, 0 or more.", key))
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case int:
		n = float64(x)
	case int64:
		n = float64(x)
	case string:
		s := strings.NewReplacer("$", "", ",", "", " ", "").Replace(strings.TrimSpace(x))
		if s == "" {
			return moneyField{}, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return moneyField{}, bad
		}
		n = f
	default:
		return moneyField{}, bad
	}
	if n != n || n < 0 || n > maxMoney { // n != n: NaN
		return moneyField{}, bad
	}
	return moneyField{set: true, val: roundCents(n)}, nil
}

func roundCents(v float64) float64 {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	r, _ := strconv.ParseFloat(s, 64)
	return r
}

// yearField is the optional model year, with the same absent/""/null rules.
type yearField struct {
	set, clear bool
	val        int
}

func (f yearField) given() bool { return f.set || f.clear }

func (f yearField) ptr() *int {
	if !f.set {
		return nil
	}
	v := f.val
	return &v
}

// readYear reads the model year: a whole number from 1886 to next year + 1
// (dealers sell next year's models early). "2019" as text is accepted.
func readYear(args map[string]any) (yearField, error) {
	v, present := args["year"]
	if !present {
		return yearField{}, nil
	}
	if v == nil {
		return yearField{clear: true}, nil
	}
	hi := time.Now().Year() + 2
	n, ok, err := wholeNumber(args, "year", minYear, hi)
	if err != nil {
		return yearField{}, badArg("year", fmt.Sprintf("year must be a model year from %d to %d, e.g. 2019.", minYear, hi),
			"Pass the model year as a 4-digit number, e.g. 2019; leave it out if not known.")
	}
	if !ok {
		return yearField{}, nil
	}
	return yearField{set: true, val: n}, nil
}

// boolArg reads a boolean param; "true"/"false" text is accepted.
func boolArg(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(v))
		return b
	}
	return false
}

// ---------------------------------------------------------------------------
// Lookups
// ---------------------------------------------------------------------------

// vehicleCandidate is a vehicle for name-or-id resolution.
type vehicleCandidate struct {
	id                uuid.UUID
	year              *int
	make_, model, vin *string
}

func (c vehicleCandidate) row() vehicleRow {
	return vehicleRow{ID: c.id, Year: c.year, Make: c.make_, Model: c.model}
}

// names are the ways a family refers to the vehicle: "2019 Honda Odyssey",
// "Honda Odyssey", "Odyssey", and its VIN.
func (c vehicleCandidate) names() []string {
	out := []string{c.row().displayName()}
	trim := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.TrimSpace(*p)
	}
	if mm := strings.TrimSpace(trim(c.make_) + " " + trim(c.model)); mm != "" {
		out = append(out, mm)
	}
	if m := trim(c.model); m != "" {
		out = append(out, m)
	}
	if v := trim(c.vin); v != "" {
		out = append(out, v)
	}
	return out
}

// resolveVehicle matches vehicle_id (a UUID, or the vehicle's name: "2019
// Honda Odyssey", "Honda Odyssey", "Odyssey" or its VIN, any case) in the
// caller's family. A UUID is returned as is; the caller's locked read
// reports a missing vehicle. Several matches, or none, is an isError that
// lists the candidates with their ids, or the family's vehicles.
func resolveVehicle(ctx context.Context, q querier, hid uuid.UUID, args map[string]any, example map[string]any) (uuid.UUID, *mcp.CallToolResult, error) {
	ref, err := optionalString(args, "vehicle_id")
	if err != nil {
		return uuid.Nil, invalid(err, example), nil
	}
	if ref == nil {
		return uuid.Nil, respond.Invalid("vehicle_id", "vehicle_id is required.",
			"Pass the vehicle's id from vehicle_find, or its name, e.g. Honda Odyssey.", example), nil
	}
	if id, perr := uuid.Parse(*ref); perr == nil {
		return id, nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id, year, make, model, vin FROM vehicles WHERE household_id = $1
		ORDER BY lower(COALESCE(make, '')), lower(COALESCE(model, '')), year DESC NULLS LAST, created_at, id`, hid)
	if err != nil {
		return uuid.Nil, nil, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (vehicleCandidate, error) {
		var c vehicleCandidate
		return c, r.Scan(&c.id, &c.year, &c.make_, &c.model, &c.vin)
	})
	if err != nil {
		return uuid.Nil, nil, err
	}
	var hits []vehicleCandidate
	for _, c := range all {
		for _, n := range c.names() {
			if strings.EqualFold(n, *ref) {
				hits = append(hits, c)
				break
			}
		}
	}
	switch {
	case len(hits) == 1:
		return hits[0].id, nil, nil
	case len(hits) > 1:
		labels := make([]string, len(hits))
		for i, c := range hits {
			labels[i] = fmt.Sprintf("%s (%s)", c.row().displayName(), c.id)
		}
		return uuid.Nil, respond.Fail(respond.Error{
			Message: fmt.Sprintf("%d vehicles match %s: %s.", len(hits), *ref, strings.Join(labels, ", ")),
			Fix:     "Pass the vehicle_id of the one you mean.",
			Field:   "vehicle_id",
		}), nil
	}
	msg := fmt.Sprintf("No vehicle named %q in this family.", *ref)
	if len(all) == 0 {
		msg += " The family has no vehicles yet."
	} else {
		names := make([]string, 0, min(len(all), 30))
		for _, c := range all[:min(len(all), 30)] {
			names = append(names, c.row().displayName())
		}
		msg += " The family's vehicles: " + strings.Join(names, ", ") + "."
	}
	return uuid.Nil, respond.Fail(respond.Error{
		Message: msg,
		Fix:     "Pass one of the family's vehicles by name (e.g. Honda Odyssey), or the vehicle_id from vehicle_find.",
		Field:   "vehicle_id",
	}), nil
}

// storedVehicle is a vehicle's stored fields, for update and delete. The
// legacy finance columns are deliberately not read (#4704).
type storedVehicle struct {
	year                                   *int
	make_, model, vin, plate, color, notes *string
	purchaseDate                           *string // YYYY-MM-DD
	purchasePrice, currentValue            *float64
}

// lockVehicle reads and row-locks one vehicle of the family.
func lockVehicle(ctx context.Context, tx pgx.Tx, hid, id uuid.UUID) (storedVehicle, error) {
	var r storedVehicle
	err := tx.QueryRow(ctx,
		`SELECT year, make, model, vin, license_plate, color, notes, to_char(purchase_date, 'YYYY-MM-DD'),
		        purchase_price::float8, current_value::float8
		   FROM vehicles WHERE id = $1 AND household_id = $2 FOR UPDATE`, id, hid).
		Scan(&r.year, &r.make_, &r.model, &r.vin, &r.plate, &r.color, &r.notes, &r.purchaseDate,
			&r.purchasePrice, &r.currentValue)
	return r, err
}

// loadVehicle reads one vehicle as vehicle_find lists it.
func loadVehicle(ctx context.Context, q querier, hid, id uuid.UUID) (vehicleData, error) {
	r, err := scanVehicle(q.QueryRow(ctx, vehicleSelect+` WHERE v.id = $1 AND v.household_id = $2`, id, hid))
	if err != nil {
		return vehicleData{}, err
	}
	return r.data(), nil
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameMoney(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return roundCents(*a) == roundCents(*b)
}

// joinNames lists names for a summary: "2 upkeep tasks, 1 note and 1 bill".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
