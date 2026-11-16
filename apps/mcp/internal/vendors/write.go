package vendors

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// duplicateWindow is the contact_create loop guard (catalog §1 "Loop guard").
const duplicateWindow = "10 minutes"

// Column sizes (004_vendors). website and notes are TEXT; their limits keep
// a runaway model from storing megabytes.
const (
	maxNameLen      = 255
	maxSpecialtyLen = 255
	maxPhoneLen     = 50
	maxEmailLen     = 255
	maxWebsiteLen   = 2048
	maxAddressLen   = 500
	maxNotesLen     = 10000
)

// writeAliases are other names small models use for the write params. A
// catalog name that is present wins.
var writeAliases = map[string]string{
	"id": "contact_id", "vendor_id": "contact_id", "contact": "contact_id", "vendor": "contact_id",
	"contact_name": "name", "vendor_name": "name", "business_name": "name",
	"trade": "trades", "services": "trades", "categories": "trades",
	"phone_number": "phone", "tel": "phone", "mobile": "phone",
	"email_address": "email", "url": "website", "web": "website", "site": "website",
	"street_address": "address", "location": "address",
	"note": "notes",
}

// Examples for error results.
var (
	createExample = map[string]any{"name": "Joe's Plumbing", "trades": []string{"plumber"}, "phone": "555-0101"}
	updateExample = map[string]any{"contact_id": "Joe's Plumbing", "phone": "555-0199"}
	deleteExample = map[string]any{"contact_id": "Joe's Plumbing"}
)

// ---------------------------------------------------------------------------
// Access
// ---------------------------------------------------------------------------

// caller is who is calling, with the role re-read from memberships.
type caller struct {
	householdID uuid.UUID
	role        string
}

// callerOf resolves the caller's current role the way the people, pet and
// vehicle tools do: re-read from memberships for the claims' user and
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

// canRead reports whether role may see contacts (API ResVendors read). The
// vendor role and unknown roles may not, so a new role fails closed.
func canRead(role string) bool {
	return canWrite(role) || role == "viewer" || role == "housesitter"
}

// canWrite reports whether role may create, change and delete contacts (API
// ResVendors write: the family roles).
func canWrite(role string) bool {
	return role == "owner" || role == "family_manager" || role == "family_member"
}

func roleLabel(role string) string {
	if role == "" {
		return "current"
	}
	return role
}

// cannotRead is the refusal for a role that may not see contacts.
func cannotRead(role string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't see the family's contacts.", roleLabel(role)),
		Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
	})
}

// cannotWrite is the refusal for a role that may not change contacts. The
// server middleware refuses read-only roles first; this is the second check.
func cannotWrite(role, tool string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't change contacts (tool %s): only an owner, family manager or family member can.",
			roleLabel(role), tool),
		Fix: "Tell the user to ask an owner or family manager; retrying will not help.",
	})
}

// ---------------------------------------------------------------------------
// Arguments
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

// tradesField is the trades param: given (a list, possibly empty, or null
// meaning none) and the normalized list.
type tradesField struct {
	given bool
	list  []string
}

// readTrades reads trades as a list of strings. A single string is read as
// a comma-separated list ("plumber, handyman"), because small models send
// that as often as an array. JSON null and [] both mean no trades.
func readTrades(args map[string]any) (tradesField, error) {
	v, present := args["trades"]
	if !present {
		return tradesField{}, nil
	}
	var raw []string
	switch x := v.(type) {
	case nil:
	case string:
		if strings.TrimSpace(x) == "" {
			return tradesField{}, nil // "" is an unused param
		}
		raw = strings.Split(x, ",")
	case []string:
		raw = x
	case []any:
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return tradesField{}, badArg("trades", "trades must be a list of text values.",
					`Pass trades as a list of words, e.g. ["plumber", "handyman"].`)
			}
			raw = append(raw, s)
		}
	default:
		return tradesField{}, badArg("trades", "trades must be a list of text values.",
			`Pass trades as a list of words, e.g. ["plumber", "handyman"].`)
	}
	list, err := normalizeTrades(raw)
	if err != nil {
		return tradesField{}, err
	}
	return tradesField{given: true, list: list}, nil
}

// contactFields is the validated create/update fields.
type contactFields struct {
	name, specialty, phone, email, website, address, notes textField
	trades                                                 tradesField
}

func (f contactFields) given() bool {
	return f.name.given() || f.specialty.given() || f.phone.given() || f.email.given() || f.website.given() ||
		f.address.given() || f.notes.given() || f.trades.given
}

// parseFields validates every create/update field present in args.
func parseFields(args map[string]any) (contactFields, error) {
	var f contactFields
	var err error
	for _, t := range []struct {
		key string
		max int
		dst *textField
	}{
		{"name", maxNameLen, &f.name}, {"specialty", maxSpecialtyLen, &f.specialty}, {"phone", maxPhoneLen, &f.phone},
		{"email", maxEmailLen, &f.email}, {"website", maxWebsiteLen, &f.website}, {"address", maxAddressLen, &f.address},
		{"notes", maxNotesLen, &f.notes},
	} {
		if *t.dst, err = readText(args, t.key, t.max); err != nil {
			return f, err
		}
	}
	if f.email.set && (!strings.Contains(f.email.val, "@") || strings.ContainsAny(f.email.val, " \t\n")) {
		return f, badArg("email", fmt.Sprintf("email %q is not an email address.", f.email.val),
			"Pass an address like joe@example.com, or leave email out.")
	}
	if f.address.set && strings.ContainsAny(f.address.val, "\r\n") {
		f.address.val = strings.Join(strings.Fields(f.address.val), " ")
	}
	if f.trades, err = readTrades(args); err != nil {
		return f, err
	}
	return f, nil
}

// ---------------------------------------------------------------------------
// Lookups
// ---------------------------------------------------------------------------

// resolveContact matches a contact_id (a UUID, or the contact's exact name,
// any case) in the caller's family. A UUID is returned as is; the caller's
// locked read reports a missing contact. Several contacts with the name, or
// none, is an isError listing the candidates or pointing at contact_find.
func resolveContact(ctx context.Context, q querier, hid uuid.UUID, args map[string]any, example map[string]any) (uuid.UUID, *mcp.CallToolResult, error) {
	ref, err := optionalString(args, "contact_id")
	if err != nil {
		return uuid.Nil, invalid(err, example), nil
	}
	if ref == nil {
		return uuid.Nil, respond.Invalid("contact_id", "contact_id is required.",
			"Pass the contact's id from contact_find, or its exact name, e.g. Joe's Plumbing.", example), nil
	}
	if id, perr := uuid.Parse(*ref); perr == nil {
		return id, nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id, name, trades FROM vendors
		WHERE household_id = $1 AND lower(btrim(name)) = lower($2) ORDER BY created_at, id LIMIT 10`, hid, *ref)
	if err != nil {
		return uuid.Nil, nil, err
	}
	type cand struct {
		id     uuid.UUID
		name   string
		trades []string
	}
	hits, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cand, error) {
		var c cand
		return c, r.Scan(&c.id, &c.name, &c.trades)
	})
	if err != nil {
		return uuid.Nil, nil, err
	}
	switch {
	case len(hits) == 1:
		return hits[0].id, nil, nil
	case len(hits) > 1:
		labels := make([]string, len(hits))
		for i, c := range hits {
			labels[i] = fmt.Sprintf("%s (%s)", contactData{Name: c.name, Trades: c.trades}.label(), c.id)
		}
		return uuid.Nil, respond.Fail(respond.Error{
			Message: fmt.Sprintf("%d contacts are named %s: %s.", len(hits), *ref, strings.Join(labels, ", ")),
			Fix:     "Pass the contact_id of the one you mean.",
			Field:   "contact_id",
		}), nil
	}
	return uuid.Nil, respond.Fail(respond.Error{
		Message: fmt.Sprintf("No contact named %q in this family.", *ref),
		Fix:     fmt.Sprintf("Find it with contact_find query=%s (or trade=…), then pass its id as contact_id.", *ref),
		Field:   "contact_id",
		Example: map[string]any{"query": *ref},
	}), nil
}

// contactRow is a contact's stored fields, for update and delete.
type contactRow struct {
	name                                    string
	trades                                  []string
	specialty, phone, email, website, notes *string
}

// lockContact reads and row-locks one contact of the family.
func lockContact(ctx context.Context, tx pgx.Tx, hid, id uuid.UUID) (contactRow, error) {
	var r contactRow
	err := tx.QueryRow(ctx,
		`SELECT name, trades, specialty, phone, email, website, notes FROM vendors
		  WHERE id = $1 AND household_id = $2 FOR UPDATE`, id, hid).
		Scan(&r.name, &r.trades, &r.specialty, &r.phone, &r.email, &r.website, &r.notes)
	if r.trades == nil {
		r.trades = []string{}
	}
	return r, err
}

// loadContact reads one contact as contact_find returns it.
func loadContact(ctx context.Context, q querier, hid, id uuid.UUID) (contactData, error) {
	page, _, err := findContacts(ctx, q, hid.String(), findInput{id: &id, limit: 1})
	if err != nil {
		return contactData{}, err
	}
	if len(page) == 0 {
		return contactData{}, pgx.ErrNoRows
	}
	return page[0], nil
}

// ---------------------------------------------------------------------------
// Shared tool params
// ---------------------------------------------------------------------------

// fieldParams are the optional params of contact_create (and contact_update).
func fieldParams() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithArray("trades", mcp.WithStringItems(), mcp.Description(`What they do, e.g. ["plumber"] or ["vet"]; max 20`)),
		mcp.WithString("phone", mcp.Description("Phone number, e.g. 555-0101")),
		mcp.WithString("email", mcp.Description("Email address, e.g. joe@example.com")),
		mcp.WithString("website", mcp.Description("Website, e.g. joesplumbing.com")),
		mcp.WithString("address", mcp.Description("Street address, e.g. 12 Main St, Springfield")),
		mcp.WithString("notes", mcp.Description("Anything else, e.g. ask for Joe; did the water heater 2025")),
	}
}

// describe is the contact's label and how to reach it, e.g. "Joe's
// Plumbing (plumber), phone 555-0101".
func (d contactData) describe() string {
	return d.label() + ", " + d.reach()
}

// writeNext is the next-step hints after a contact was saved.
func writeNext(d contactData) []string {
	next := []string{fmt.Sprintf("Have %s do an upkeep job with maintenance_create contact_id=%s.", d.Name, d.ID)}
	for _, t := range d.Trades {
		if t == "vet" {
			return append(next, fmt.Sprintf("Make them a pet's vet with pet_update pet_id=… vet_contact_id=%s.", d.ID))
		}
	}
	return append(next, fmt.Sprintf("Record a bill paid to them with bill_create contact_id=%s.", d.ID))
}

// ---------------------------------------------------------------------------
// contact_create
// ---------------------------------------------------------------------------

// NewContactCreateTool creates the contact_create MCP tool (catalog v2, class
// C). "Save Joe the plumber, 555-0101" is one call: name is required, trades
// are normalized like the REST API ("Plumbing" -> plumber). The loop guard
// returns a contact with the same name added to the family in the last 10
// minutes (duplicate:true) instead of a second row, under an advisory lock
// so parallel calls make one row.
func NewContactCreateTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := append([]mcp.ToolOption{
		mcp.WithDescription("Add a vendor, provider or service with one or more trades (plumber, dentist, babysitter)."),
		mcp.WithString("name", mcp.Description("Person or business name, e.g. Joe's Plumbing"), mcp.Required()),
	}, fieldParams()...)
	opts = append(opts, respond.Schema[contactData]())
	tool := mcp.NewTool("contact_create", append(opts, annotations("Add contact", false, false, false)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "contact_create"), nil
		}
		f, err := parseFields(withAliases(req.GetArguments(), writeAliases))
		if err != nil {
			return invalid(err, createExample), nil
		}
		if !f.name.set {
			return respond.Invalid("name", "name is required.", "Pass the person's or business's name, e.g. Joe's Plumbing.", createExample), nil
		}
		trades := f.trades.list
		if trades == nil {
			trades = []string{}
		}
		notes := joinAddress(f.address.ptr(), f.notes.ptr())
		if notes != nil && utf8.RuneCountInString(*notes) > maxNotesLen {
			return respond.Invalid("notes", fmt.Sprintf("notes and address together must be at most %d characters.", maxNotesLen),
				"Pass shorter notes.", createExample), nil
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("add the contact", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		// Loop guard.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('contact_create:' || $1::text || ':' || lower($2), 0))`,
			c.householdID, f.name.val); err != nil {
			return respond.Failed("add the contact", err), nil
		}
		var dupID uuid.UUID
		err = tx.QueryRow(ctx, `
SELECT id FROM vendors
 WHERE household_id = $1 AND lower(btrim(name)) = lower($2) AND created_at > NOW() - $3::interval
 ORDER BY created_at DESC LIMIT 1`, c.householdID, f.name.val, duplicateWindow).Scan(&dupID)
		switch {
		case err == nil:
			d, err := loadContact(ctx, tx, c.householdID, dupID)
			if err != nil {
				return respond.Failed("add the contact", err), nil
			}
			d.Duplicate = true
			return respond.OK(fmt.Sprintf("%s was already added a few minutes ago; returned that contact instead of a copy.", d.Name), d,
				fmt.Sprintf("Change details with contact_update contact_id=%s instead of adding it again.", d.ID)), nil
		case !errors.Is(err, pgx.ErrNoRows):
			return respond.Failed("add the contact", err), nil
		}

		var id uuid.UUID
		if err := tx.QueryRow(ctx, `
INSERT INTO vendors (household_id, name, trades, specialty, phone, email, website, notes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
			c.householdID, f.name.val, trades, f.specialty.ptr(), f.phone.ptr(), f.email.ptr(), f.website.ptr(), notes).Scan(&id); err != nil {
			return respond.Failed("add the contact", err), nil
		}
		var sameName int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM vendors WHERE household_id = $1 AND lower(btrim(name)) = lower($2) AND id <> $3`,
			c.householdID, f.name.val, id).Scan(&sameName); err != nil {
			return respond.Failed("add the contact", err), nil
		}
		d, err := loadContact(ctx, tx, c.householdID, id)
		if err != nil {
			return respond.Failed("add the contact", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("add the contact", err), nil
		}

		next := writeNext(d)
		if sameName > 0 {
			next = append(next, fmt.Sprintf("Another contact is also named %s; pass contact ids, not the name, from now on.", d.Name))
		} else if len(d.Trades) == 0 {
			next = append(next, fmt.Sprintf("Add what they do with contact_update contact_id=%s trades=[…] so contact_find trade=… finds them.", d.ID))
		}
		return respond.OK(fmt.Sprintf("Added %s.", d.describe()), d, next...), nil
	}
	return "contact_create", tool, handler
}

// ---------------------------------------------------------------------------
// contact_update
// ---------------------------------------------------------------------------

// NewContactUpdateTool creates the contact_update MCP tool (catalog v2, class
// U). Only the given fields change: an absent field or "" is left alone,
// JSON null clears phone, email, website, address or notes, and trades
// replaces the whole list ([] or null clears it). name cannot be cleared.
// A call that changes nothing writes nothing, so a repeat is safe.
// contact_id is the contact's id or its exact name.
func NewContactUpdateTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := append([]mcp.ToolOption{
		mcp.WithDescription("Change a contact; only the given fields change. trades replaces the whole list."),
		mcp.WithString("contact_id", mcp.Description("The contact's id from contact_find, or its exact name"), mcp.Required()),
		mcp.WithString("name", mcp.Description("New name")),
	}, fieldParams()...)
	opts = append(opts, respond.Schema[contactData]())
	tool := mcp.NewTool("contact_update", append(opts, annotations("Update contact", false, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "contact_update"), nil
		}
		args := withAliases(req.GetArguments(), writeAliases)
		f, err := parseFields(args)
		if err != nil {
			return invalid(err, updateExample), nil
		}
		if f.name.clear {
			return respond.Invalid("name", "name cannot be cleared.", "Pass a new name, or leave name out to keep it.", updateExample), nil
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("update the contact", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		id, bad, err := resolveContact(ctx, tx, c.householdID, args, updateExample)
		if err != nil {
			return respond.Failed("update the contact", err), nil
		} else if bad != nil {
			return bad, nil
		}
		if !f.given() {
			return respond.Invalid("", "No fields to update.",
				"Pass contact_id plus at least one field, e.g. phone, trades or notes.", updateExample), nil
		}
		cur, err := lockContact(ctx, tx, c.householdID, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("contact", id.String(), "Call contact_find to list the family's contacts and their ids."), nil
		}
		if err != nil {
			return respond.Failed("update the contact", err), nil
		}

		// Columns come only from this list, never from the caller.
		vals := []any{id, c.householdID}
		var sets, changed []string
		set := func(param, col string, v any) {
			vals = append(vals, v)
			sets = append(sets, fmt.Sprintf("%s = $%d", col, len(vals)))
			changed = append(changed, param)
		}
		text := func(param, col string, tf textField, now *string) {
			if tf.given() && !samePtr(tf.ptr(), now) {
				set(param, col, tf.ptr())
			}
		}
		curName := cur.name
		text("name", "name", f.name, &curName)
		if f.trades.given && !sameList(f.trades.list, cur.trades) {
			set("trades", "trades", f.trades.list)
		}
		text("specialty", "specialty", f.specialty, cur.specialty)
		text("phone", "phone", f.phone, cur.phone)
		text("email", "email", f.email, cur.email)
		text("website", "website", f.website, cur.website)
		// address and notes share the notes column (splitAddress).
		curAddr, curRest := splitAddress(cur.notes)
		newAddr, newRest := curAddr, curRest
		if f.address.given() {
			newAddr = f.address.ptr()
		}
		if f.notes.given() {
			newRest = f.notes.ptr()
		}
		if notes := joinAddress(newAddr, newRest); !samePtr(notes, cur.notes) {
			if notes != nil && utf8.RuneCountInString(*notes) > maxNotesLen {
				return respond.Invalid("notes", fmt.Sprintf("notes and address together must be at most %d characters.", maxNotesLen),
					"Pass shorter notes.", updateExample), nil
			}
			vals = append(vals, notes)
			sets = append(sets, fmt.Sprintf("notes = $%d", len(vals)))
			if !samePtr(newAddr, curAddr) {
				changed = append(changed, "address")
			}
			if !samePtr(newRest, curRest) {
				changed = append(changed, "notes")
			}
		}

		if len(sets) > 0 {
			sets = append(sets, "updated_at = NOW()")
			if _, err := tx.Exec(ctx, `UPDATE vendors SET `+strings.Join(sets, ", ")+` WHERE id = $1 AND household_id = $2`, vals...); err != nil {
				return respond.Failed("update the contact", err), nil
			}
		}
		d, err := loadContact(ctx, tx, c.householdID, id)
		if err != nil {
			return respond.Failed("update the contact", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("update the contact", err), nil
		}
		d.Changed = changed

		next := []string{fmt.Sprintf("See %s with contact_find id=%s.", d.Name, d.ID)}
		if len(d.Trades) > 0 {
			next = append(next, fmt.Sprintf("They now show for contact_find trade=%s.", d.Trades[0]))
		}
		if len(changed) == 0 {
			return respond.OK(fmt.Sprintf("Nothing changed: %s already has those details (%s).", d.Name, d.describe()), d, next...), nil
		}
		return respond.OK(fmt.Sprintf("Updated %s (%s): %s.", d.Name, strings.Join(changed, ", "), d.describe()), d, next...), nil
	}
	return "contact_update", tool, handler
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// contact_delete
// ---------------------------------------------------------------------------

// contactUnlinked is what pointed at the deleted contact and now has no
// contact (058/043 composite FKs, ON DELETE SET NULL of the contact column).
type contactUnlinked struct {
	Bills           int `json:"bills" jsonschema_description:"Bills that stay, with no contact."`
	UpkeepTasks     int `json:"upkeep_tasks" jsonschema_description:"Upkeep tasks that stay, with no contact."`
	UpkeepSchedules int `json:"upkeep_schedules" jsonschema_description:"Upkeep schedules that stay, with no contact."`
	Pets            int `json:"pets" jsonschema_description:"Pets that stay, with no vet."`
}

// contactDeleted is contact_delete's data.
type contactDeleted struct {
	ContactID      string          `json:"contact_id"`
	Name           string          `json:"name" jsonschema_description:"Name of the deleted contact."`
	Trades         []string        `json:"trades"`
	Deleted        bool            `json:"deleted"`
	Unlinked       contactUnlinked `json:"unlinked"`
	NotesDeleted   int             `json:"notes_deleted" jsonschema_description:"Notes about the contact, deleted with it."`
	DocumentsMoved int             `json:"documents_moved" jsonschema_description:"Documents about the contact, moved to the family."`
}

// unlinked lists what keeps working without the contact, e.g. "1 bill".
func (d contactDeleted) unlinked() []string {
	var out []string
	for _, it := range []struct {
		n            int
		one, several string
	}{
		{d.Unlinked.Bills, "bill", "bills"}, {d.Unlinked.UpkeepTasks, "upkeep task", "upkeep tasks"},
		{d.Unlinked.UpkeepSchedules, "upkeep schedule", "upkeep schedules"}, {d.Unlinked.Pets, "pet (as vet)", "pets (as vet)"},
	} {
		switch {
		case it.n == 1:
			out = append(out, "1 "+it.one)
		case it.n > 1:
			out = append(out, fmt.Sprintf("%d %s", it.n, it.several))
		}
	}
	return out
}

// attached lists everything the delete touches beyond the contact itself:
// a non-empty list needs confirm:true.
func (d contactDeleted) attached() []string {
	out := d.unlinked()
	if d.NotesDeleted > 0 {
		out = append(out, plural(d.NotesDeleted, "note"))
	}
	return out
}

// boolArg reads a boolean argument; "true" as text counts.
func boolArg(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return false
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// NewContactDeleteTool creates the contact_delete MCP tool (catalog v2, class
// X). Bills, upkeep tasks and schedules that used the contact stay with no
// contact, and pets whose vet it was keep no vet (composite FKs ON DELETE
// SET NULL, 043/058); notes about it are deleted and documents move to the
// family (rehome_entity_refs, 038). When anything points at the contact,
// confirm:true is required and the refusal gives the counts. A missing or
// already deleted contact is reported as not found, so a repeat is safe.
func NewContactDeleteTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Delete a contact. Bills and upkeep that used them keep working without a contact " +
			"(needs confirm:true if any use them)."),
		mcp.WithString("contact_id", mcp.Description("The contact's id from contact_find, or its exact name"), mcp.Required()),
		mcp.WithBoolean("confirm", mcp.Description("true once the user agreed, when bills, upkeep or pets use the contact")),
		respond.Schema[contactDeleted](),
	}
	tool := mcp.NewTool("contact_delete", append(opts, annotations("Delete contact", false, true, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "contact_delete"), nil
		}
		args := withAliases(req.GetArguments(), writeAliases)
		confirm := boolArg(args, "confirm")

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("delete the contact", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		id, bad, err := resolveContact(ctx, tx, c.householdID, args, deleteExample)
		if err != nil {
			return respond.Failed("delete the contact", err), nil
		} else if bad != nil {
			return bad, nil
		}
		cur, err := lockContact(ctx, tx, c.householdID, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("contact", id.String(), "Call contact_find to list the family's contacts and their ids."), nil
		}
		if err != nil {
			return respond.Failed("delete the contact", err), nil
		}

		out := contactDeleted{ContactID: id.String(), Name: cur.name, Trades: cur.trades}
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM bills WHERE household_id = $2 AND vendor_id = $1),
			(SELECT count(*) FROM maintenance_tasks WHERE household_id = $2 AND vendor_id = $1),
			(SELECT count(*) FROM maintenance_schedules WHERE household_id = $2 AND vendor_id = $1),
			(SELECT count(*) FROM pets WHERE household_id = $2 AND vet_vendor_id = $1),
			(SELECT count(*) FROM notes WHERE household_id = $2 AND entity_type = 'vendor' AND entity_id = $1),
			(SELECT count(*) FROM files WHERE household_id = $2 AND entity_type = 'vendor' AND entity_id = $1)`,
			id, c.householdID).Scan(&out.Unlinked.Bills, &out.Unlinked.UpkeepTasks, &out.Unlinked.UpkeepSchedules,
			&out.Unlinked.Pets, &out.NotesDeleted, &out.DocumentsMoved); err != nil {
			return respond.Failed("delete the contact", err), nil
		}
		attached := out.attached()
		if len(attached) > 0 && !confirm {
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("%s has %s; pass confirm:true to delete it.", cur.name, joinNames(attached)),
				Fix: "Ask the user to confirm, then call contact_delete again with confirm:true. Bills, upkeep and pets stay " +
					"with no contact; notes about the contact are deleted; documents move to the family.",
				Field:   "confirm",
				Example: map[string]any{"contact_id": id.String(), "confirm": true},
			}), nil
		}

		if _, err := tx.Exec(ctx, `DELETE FROM vendors WHERE id = $1 AND household_id = $2`, id, c.householdID); err != nil {
			return respond.Failed("delete the contact", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("delete the contact", err), nil
		}
		out.Deleted = true

		summary := fmt.Sprintf("Deleted %s.", contactData{Name: cur.name, Trades: cur.trades}.label())
		if u := out.unlinked(); len(u) > 0 {
			summary += fmt.Sprintf(" Kept without a contact: %s.", joinNames(u))
		}
		if out.NotesDeleted > 0 {
			summary += fmt.Sprintf(" %s about them deleted.", plural(out.NotesDeleted, "note"))
		}
		if out.DocumentsMoved > 0 {
			summary += fmt.Sprintf(" %s moved to the family.", plural(out.DocumentsMoved, "document"))
		}
		next := []string{"List the contacts that are left with contact_find."}
		if len(cur.trades) > 0 {
			next = append(next, fmt.Sprintf("Save a new %s with contact_create name=… trades=[%s].", cur.trades[0], cur.trades[0]))
		}
		return respond.OK(summary, out, next...), nil
	}
	return "contact_delete", tool, handler
}
