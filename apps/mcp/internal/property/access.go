package property

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// duplicateWindow is the property_create loop guard (catalog §1 "Loop guard").
const duplicateWindow = "10 minutes"

// Column sizes (002_create_properties, 020_family_model).
const (
	maxNameLen     = 255
	maxTypeLen     = 100
	maxAddressLen  = 1000
	maxRoomsInCall = 50
	// maxMoney is the NUMERIC(12,2) limit of purchase_price/current_value.
	maxMoney = 9_999_999_999.99
)

// ---------------------------------------------------------------------------
// Access
// ---------------------------------------------------------------------------

// caller is who is calling, with the role re-read from memberships.
type caller struct {
	householdID uuid.UUID
	role        string
}

// callerOf resolves the caller's current role the way the people and pet
// tools do: re-read from memberships for the claims' user and household, so
// a demoted member loses write access at once and a removed one gets
// nothing. A non-nil result is the isError to return.
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

// canWrite reports whether role may create, change and delete properties and
// rooms (API ResProperties write: the family roles). Unknown roles may not,
// so a new role fails closed.
func canWrite(role string) bool {
	return role == "owner" || role == "family_manager" || role == "family_member"
}

// cannotWrite is the refusal for a role that may not change properties. The
// server middleware refuses these roles first; this is the second check.
func cannotWrite(role, tool string) *mcp.CallToolResult {
	label := role
	if label == "" {
		label = "current"
	}
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't change properties or rooms (tool %s): only an owner, family manager or family member can.", label, tool),
		Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
	})
}

// ---------------------------------------------------------------------------
// Argument helpers for the write tools
// ---------------------------------------------------------------------------

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
// text are accepted, with "$", "," and spaces stripped ("$450,000").
func readMoney(args map[string]any, key string) (moneyField, error) {
	v, present := args[key]
	if !present {
		return moneyField{}, nil
	}
	if v == nil {
		return moneyField{clear: true}, nil
	}
	bad := badArg(key, fmt.Sprintf("%s must be an amount of money, e.g. 450000 or 1250.50.", key),
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

// readRoomNames reads a list of room names: an array of names (an object
// with a name is accepted too) or one comma-separated string. Names are
// trimmed, blanks dropped and repeats (any case) kept once, in order.
func readRoomNames(args map[string]any, key string) ([]string, error) {
	v, present := args[key]
	if !present || v == nil {
		return nil, nil
	}
	bad := badArg(key, key+" must be a list of room names.",
		fmt.Sprintf(`Pass %s as an array of names, e.g. ["Kitchen", "Primary bedroom"].`, key))
	var raw []string
	switch x := v.(type) {
	case string:
		raw = strings.Split(x, ",")
	case []string:
		raw = x
	case []any:
		for _, it := range x {
			switch y := it.(type) {
			case string:
				raw = append(raw, y)
			case map[string]any:
				name, ok := y["name"].(string)
				if !ok {
					return nil, bad
				}
				raw = append(raw, name)
			default:
				return nil, bad
			}
		}
	default:
		return nil, bad
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" || seen[strings.ToLower(r)] {
			continue
		}
		if utf8.RuneCountInString(r) > maxNameLen {
			return nil, badArg(key, fmt.Sprintf("Room names must be at most %d characters.", maxNameLen), "Pass shorter room names.")
		}
		seen[strings.ToLower(r)] = true
		out = append(out, r)
	}
	if len(out) > maxRoomsInCall {
		return nil, badArg(key, fmt.Sprintf("At most %d rooms per call.", maxRoomsInCall),
			"Split the rooms over several property_update calls with add_rooms.")
	}
	return out, nil
}

// renameReq is one rename_rooms entry: the room (id or current name) and its
// new name.
type renameReq struct {
	ref, name string
}

// readRenames reads rename_rooms: an array of {room_id, name}.
func readRenames(args map[string]any) ([]renameReq, error) {
	v, present := args["rename_rooms"]
	if !present || v == nil {
		return nil, nil
	}
	bad := badArg("rename_rooms", "rename_rooms must be a list of {room_id, name}.",
		`Pass rename_rooms as an array, e.g. [{"room_id": "Den", "name": "Office"}]; room_id is the room's id or current name.`)
	items, ok := v.([]any)
	if !ok {
		if m, isMap := v.(map[string]any); isMap {
			items = []any{m}
		} else {
			return nil, bad
		}
	}
	var out []renameReq
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, bad
		}
		m = withAliases(m, map[string]string{"id": "room_id", "room": "room_id", "from": "room_id",
			"new_name": "name", "to": "name"})
		ref, _ := m["room_id"].(string)
		name, _ := m["name"].(string)
		ref, name = strings.TrimSpace(ref), strings.TrimSpace(name)
		if ref == "" || name == "" {
			return nil, bad
		}
		if utf8.RuneCountInString(name) > maxNameLen {
			return nil, badArg("rename_rooms", fmt.Sprintf("Room names must be at most %d characters.", maxNameLen), "Pass a shorter name.")
		}
		out = append(out, renameReq{ref: ref, name: name})
	}
	if len(out) > maxRoomsInCall {
		return nil, badArg("rename_rooms", fmt.Sprintf("At most %d renames per call.", maxRoomsInCall), "Split the renames over several calls.")
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Lookups
// ---------------------------------------------------------------------------

// namedRow is a candidate for name-or-id resolution.
type namedRow struct {
	id    uuid.UUID
	name  string
	label string // how to tell candidates apart, e.g. "Kitchen in Home"
}

// pickByName matches ref (exact, any case) among rows. A single hit is
// returned; several hits or none is an isError on field that lists the
// candidates (with ids) or the family's rows.
func pickByName(rows []namedRow, ref, field, what, plural, fix string) (uuid.UUID, *mcp.CallToolResult) {
	var hits []namedRow
	for _, r := range rows {
		if strings.EqualFold(strings.TrimSpace(r.name), ref) {
			hits = append(hits, r)
		}
	}
	switch {
	case len(hits) == 1:
		return hits[0].id, nil
	case len(hits) > 1:
		labels := make([]string, len(hits))
		for i, c := range hits {
			labels[i] = fmt.Sprintf("%s (%s)", c.label, c.id)
		}
		return uuid.Nil, respond.Fail(respond.Error{
			Message: fmt.Sprintf("%d %s are named %s: %s.", len(hits), plural, ref, strings.Join(labels, ", ")),
			Fix:     fmt.Sprintf("Pass the %s of the one you mean.", field),
			Field:   field,
		})
	}
	msg := fmt.Sprintf("No %s named %q in this family.", what, ref)
	if len(rows) == 0 {
		msg += fmt.Sprintf(" The family has no %s yet.", plural)
	} else {
		names := make([]string, 0, min(len(rows), 30))
		for _, c := range rows[:min(len(rows), 30)] {
			names = append(names, c.label)
		}
		msg += fmt.Sprintf(" The family's %s: %s.", plural, strings.Join(names, ", "))
	}
	return uuid.Nil, respond.Fail(respond.Error{Message: msg, Fix: fix, Field: field})
}

// resolveProperty matches property_id (a UUID, or the property's exact name,
// any case) in the caller's family. A UUID is returned as is; the caller's
// locked read reports a missing property.
func resolveProperty(ctx context.Context, q querier, hid uuid.UUID, args map[string]any, example map[string]any) (uuid.UUID, *mcp.CallToolResult, error) {
	ref, err := optionalString(args, "property_id")
	if err != nil {
		return uuid.Nil, invalid(err, example), nil
	}
	if ref == nil {
		return uuid.Nil, respond.Invalid("property_id", "property_id is required.",
			"Pass the property's id from property_find, or its name, e.g. Lake House.", example), nil
	}
	if id, perr := uuid.Parse(*ref); perr == nil {
		return id, nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id, name FROM properties WHERE household_id = $1 ORDER BY lower(name), created_at, id`, hid)
	if err != nil {
		return uuid.Nil, nil, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (namedRow, error) {
		var c namedRow
		err := r.Scan(&c.id, &c.name)
		c.label = c.name
		return c, err
	})
	if err != nil {
		return uuid.Nil, nil, err
	}
	id, bad := pickByName(all, *ref, "property_id", "property", "properties",
		"Pass one of the family's property names, or the property_id from property_find.")
	return id, bad, nil
}

// propertyRow is a property's stored fields, for update and delete.
type propertyRow struct {
	name                   string
	address, propertyType  *string
	purchaseDate           *string // YYYY-MM-DD
	purchasePrice, current *float64
}

// lockProperty reads and row-locks one property of the family.
func lockProperty(ctx context.Context, tx pgx.Tx, hid, id uuid.UUID) (propertyRow, error) {
	var r propertyRow
	err := tx.QueryRow(ctx,
		`SELECT name, address, property_type, to_char(purchase_date, 'YYYY-MM-DD'),
		        purchase_price::float8, current_value::float8
		   FROM properties WHERE id = $1 AND household_id = $2 FOR UPDATE`, id, hid).
		Scan(&r.name, &r.address, &r.propertyType, &r.purchaseDate, &r.purchasePrice, &r.current)
	return r, err
}

// loadProperty reads one property as property_find returns it.
func loadProperty(ctx context.Context, q querier, hid, id uuid.UUID) (propertyData, error) {
	page, _, err := findProperties(ctx, q, hid.String(), findInput{id: &id, limit: 1})
	if err != nil {
		return propertyData{}, err
	}
	if len(page) == 0 {
		return propertyData{}, pgx.ErrNoRows
	}
	return page[0], nil
}

func samePtr(a, b *string) bool {
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

// joinNames lists names for a summary: "Kitchen, Den and Loft".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
