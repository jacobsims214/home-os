package property

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// The write tools never read or write the legacy finance columns
// (properties.mortgage_*, property_tax_*, insurance_*, hoa_fee_monthly):
// mortgages, tax, insurance and HOA live in loans and bills about the
// property (#4704; #4707 drops the columns). Rooms follow domain model v2:
// a room belongs to one property, and an asset's property follows its room
// (038 assets_derive_property), so linking a thing to a room is
// asset_update room_id=….

// writeAliases are other names small models use for the write params. A
// catalog name that is present wins.
var writeAliases = map[string]string{
	"id": "property_id", "property": "property_id",
	"property_name": "name",
	"type":          "property_type", "kind": "property_type",
	"value": "current_value", "estimated_value": "current_value", "worth": "current_value",
	"price": "purchase_price", "bought_for": "purchase_price",
	"bought_on": "purchase_date", "purchased": "purchase_date",
	"room_names": "rooms",
	"new_rooms":  "add_rooms",
}

// Examples for error results.
var (
	createExample = map[string]any{"name": "Lake House", "address": "1 Shore Rd", "property_type": "house",
		"rooms": []string{"Kitchen", "Living room", "Primary bedroom"}}
	updateExample = map[string]any{"property_id": "Lake House", "current_value": 450000, "add_rooms": []string{"Garage"}}
	deleteExample = map[string]any{"property_id": "Lake House", "confirm": true}
)

// propertyFields is the validated create/update fields.
type propertyFields struct {
	name, address, propertyType, purchaseDate textField
	purchasePrice, currentValue               moneyField
}

func (f propertyFields) given() bool {
	return f.name.given() || f.address.given() || f.propertyType.given() || f.purchaseDate.given() ||
		f.purchasePrice.given() || f.currentValue.given()
}

// parseFields validates every create/update field present in args.
func parseFields(args map[string]any) (propertyFields, error) {
	var f propertyFields
	var err error
	for _, t := range []struct {
		key string
		max int
		dst *textField
	}{
		{"name", maxNameLen, &f.name}, {"address", maxAddressLen, &f.address},
		{"property_type", maxTypeLen, &f.propertyType}, {"purchase_date", 10, &f.purchaseDate},
	} {
		if *t.dst, err = readText(args, t.key, t.max); err != nil {
			if t.key == "purchase_date" {
				return f, badPurchaseDate(fmt.Sprint(args[t.key]))
			}
			return f, err
		}
	}
	if f.purchaseDate.set {
		d, perr := time.Parse(time.DateOnly, f.purchaseDate.val)
		if perr != nil {
			return f, badPurchaseDate(f.purchaseDate.val)
		}
		if d.Year() < 1800 {
			return f, badArg("purchase_date", fmt.Sprintf("purchase_date %s is before 1800.", f.purchaseDate.val),
				"Pass the date the family bought it as YYYY-MM-DD; leave it out if not known.")
		}
	}
	if f.purchasePrice, err = readMoney(args, "purchase_price"); err != nil {
		return f, err
	}
	if f.currentValue, err = readMoney(args, "current_value"); err != nil {
		return f, err
	}
	return f, nil
}

func badPurchaseDate(v string) error {
	return badArg("purchase_date", fmt.Sprintf("purchase_date %q is not a date.", v),
		"Pass purchase_date as YYYY-MM-DD, e.g. 2019-06-14; leave it out if not known.")
}

// fieldParams are the optional property params shared by create and update.
func fieldParams() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString("address", mcp.Description("Street address, e.g. 1 Shore Rd, Lakeville")),
		mcp.WithString("property_type", mcp.Description("e.g. house, condo, cabin, rental, land")),
		mcp.WithString("purchase_date", mcp.Description("Date bought, YYYY-MM-DD")),
		mcp.WithNumber("purchase_price", mcp.Description("Price paid, in the family's currency, e.g. 320000")),
		mcp.WithNumber("current_value", mcp.Description("Estimated value today, e.g. 450000")),
	}
}

// insertRooms adds rooms to a property (already locked or just created) and
// returns them in the given order.
func insertRooms(ctx context.Context, tx pgx.Tx, hid, propertyID uuid.UUID, names []string) error {
	if len(names) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
INSERT INTO rooms (property_id, household_id, name)
SELECT $1, $2, n FROM unnest($3::text[]) WITH ORDINALITY AS t(n, i) ORDER BY i`, propertyID, hid, names)
	return err
}

// roomHint is a next step that puts a thing in one of the property's rooms.
func roomHint(d propertyData) string {
	if len(d.Rooms) == 0 {
		return fmt.Sprintf("Add rooms with property_update property_id=%s add_rooms=[…].", d.ID)
	}
	r := d.Rooms[0]
	for _, x := range d.Rooms {
		if len(d.RoomsAdded) > 0 && strings.EqualFold(x.Name, d.RoomsAdded[0]) {
			r = x
			break
		}
	}
	return fmt.Sprintf("Put a thing in the %s with asset_create name=… category=… room_id=%s, or move one there with asset_update asset_id=… room_id=%s.",
		r.Name, r.ID, r.ID)
}

// ---------------------------------------------------------------------------
// property_create
// ---------------------------------------------------------------------------

// NewPropertyCreateTool creates the property_create MCP tool (catalog v2,
// class C). "Add our lake house with a kitchen and two bedrooms" is one
// call: name is required and rooms[] adds the rooms. The loop guard returns
// a property with the same name added to the family in the last 10 minutes
// (duplicate:true) instead of a second row, under an advisory lock so
// parallel calls make one row.
func NewPropertyCreateTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := append([]mcp.ToolOption{
		mcp.WithDescription("Add a home or other property. Rooms can be added in the same call."),
		mcp.WithString("name", mcp.Description("What the family calls it, e.g. Lake House"), mcp.Required()),
	}, fieldParams()...)
	opts = append(opts,
		mcp.WithArray("rooms", mcp.Description(`Room names to add, e.g. ["Kitchen", "Primary bedroom"]`), mcp.WithStringItems()),
		respond.Schema[propertyData]())
	tool := mcp.NewTool("property_create", append(opts, annotations("Add property", false, false, false)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "property_create"), nil
		}
		args := withAliases(req.GetArguments(), writeAliases)
		f, err := parseFields(args)
		if err != nil {
			return invalid(err, createExample), nil
		}
		if !f.name.set {
			return respond.Invalid("name", "name is required.", "Pass what the family calls the property, e.g. Lake House.", createExample), nil
		}
		rooms, err := readRoomNames(args, "rooms")
		if err != nil {
			return invalid(err, createExample), nil
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("add the property", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		// Loop guard.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('property_create:' || $1::text || ':' || lower($2), 0))`,
			c.householdID, f.name.val); err != nil {
			return respond.Failed("add the property", err), nil
		}
		var dupID uuid.UUID
		err = tx.QueryRow(ctx, `
SELECT id FROM properties
 WHERE household_id = $1 AND lower(name) = lower($2) AND created_at > NOW() - $3::interval
 ORDER BY created_at DESC LIMIT 1`, c.householdID, f.name.val, duplicateWindow).Scan(&dupID)
		switch {
		case err == nil:
			d, err := loadProperty(ctx, tx, c.householdID, dupID)
			if err != nil {
				return respond.Failed("add the property", err), nil
			}
			d.Duplicate = true
			return respond.OK(fmt.Sprintf("%s was already added a few minutes ago; returned that property instead of a copy.", d.Name), d,
				fmt.Sprintf("Change it or add rooms with property_update property_id=%s instead of adding it again.", d.ID)), nil
		case !errors.Is(err, pgx.ErrNoRows):
			return respond.Failed("add the property", err), nil
		}

		var id uuid.UUID
		if err := tx.QueryRow(ctx, `
INSERT INTO properties (household_id, name, address, property_type, purchase_date, purchase_price, current_value)
VALUES ($1, $2, $3, $4, $5::date, $6, $7) RETURNING id`,
			c.householdID, f.name.val, f.address.ptr(), f.propertyType.ptr(), f.purchaseDate.ptr(),
			f.purchasePrice.ptr(), f.currentValue.ptr()).Scan(&id); err != nil {
			return respond.Failed("add the property", err), nil
		}
		if err := insertRooms(ctx, tx, c.householdID, id, rooms); err != nil {
			return respond.Failed("add the rooms", err), nil
		}
		var sameName int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM properties WHERE household_id = $1 AND lower(name) = lower($2) AND id <> $3`,
			c.householdID, f.name.val, id).Scan(&sameName); err != nil {
			return respond.Failed("add the property", err), nil
		}
		d, err := loadProperty(ctx, tx, c.householdID, id)
		if err != nil {
			return respond.Failed("add the property", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("add the property", err), nil
		}
		d.RoomsAdded = rooms

		next := []string{roomHint(d), fmt.Sprintf("Tie a bill to it with bill_create name=… amount=… property_id=%s.", d.ID)}
		if sameName > 0 {
			next = append(next, fmt.Sprintf("Another property is also named %s; pass property ids, not the name, from now on.", d.Name))
		}
		summary := fmt.Sprintf("Added %s: %s.", d.Name, d.describe())
		if len(rooms) > 0 {
			summary = fmt.Sprintf("Added %s with rooms %s: %s.", d.Name, joinNames(rooms), d.describe())
		}
		return respond.OK(summary, d, next...), nil
	}
	return "property_create", tool, handler
}

// ---------------------------------------------------------------------------
// property_update
// ---------------------------------------------------------------------------

// NewPropertyUpdateTool creates the property_update MCP tool (catalog v2,
// class U). Only the given fields change: an absent field or "" is left
// alone, and JSON null clears address, property_type, purchase_date,
// purchase_price or current_value. name cannot be cleared. add_rooms adds
// rooms the property doesn't have yet (a name it already has, in any case,
// is skipped), and rename_rooms renames rooms by id or current name. A call
// that changes nothing writes nothing, so a repeat is safe. property_id is
// the property's id or its exact name.
func NewPropertyUpdateTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := append([]mcp.ToolOption{
		mcp.WithDescription("Change a property, add rooms (add_rooms) or rename them (rename_rooms). Use room_delete to remove a room."),
		mcp.WithString("property_id", mcp.Description("The property's id from property_find, or its name, e.g. Lake House"), mcp.Required()),
		mcp.WithString("name", mcp.Description("New name")),
	}, fieldParams()...)
	opts = append(opts,
		mcp.WithArray("add_rooms", mcp.Description(`Room names to add, e.g. ["Garage"]`), mcp.WithStringItems()),
		mcp.WithArray("rename_rooms", mcp.Description(`e.g. [{"room_id": "Den", "name": "Office"}]; room_id is an id or name`),
			mcp.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"room_id": map[string]any{"type": "string", "description": "The room's id, or its current name"},
					"name":    map[string]any{"type": "string", "description": "The new name"},
				},
				"required": []string{"room_id", "name"},
			})),
		respond.Schema[propertyData]())
	tool := mcp.NewTool("property_update", append(opts, annotations("Update property", false, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "property_update"), nil
		}
		args := withAliases(req.GetArguments(), writeAliases)
		f, err := parseFields(args)
		if err != nil {
			return invalid(err, updateExample), nil
		}
		if f.name.clear {
			return respond.Invalid("name", "name cannot be cleared.", "Pass a new name, or leave name out to keep it.", updateExample), nil
		}
		add, err := readRoomNames(args, "add_rooms")
		if err != nil {
			return invalid(err, updateExample), nil
		}
		renames, err := readRenames(args)
		if err != nil {
			return invalid(err, updateExample), nil
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("update the property", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		id, bad, err := resolveProperty(ctx, tx, c.householdID, args, updateExample)
		if err != nil {
			return respond.Failed("update the property", err), nil
		} else if bad != nil {
			return bad, nil
		}
		if !f.given() && len(add) == 0 && len(renames) == 0 {
			return respond.Invalid("", "No fields to update.",
				"Pass property_id plus at least one field, e.g. current_value, address, add_rooms or rename_rooms.", updateExample), nil
		}
		cur, err := lockProperty(ctx, tx, c.householdID, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("property", id.String(), "Call property_find to list the family's properties and their ids."), nil
		}
		if err != nil {
			return respond.Failed("update the property", err), nil
		}

		// Columns come only from this list, never from the caller.
		vals := []any{id, c.householdID}
		var sets, changed []string
		set := func(param, col, cast string, v any) {
			vals = append(vals, v)
			sets = append(sets, fmt.Sprintf("%s = $%d%s", col, len(vals), cast))
			changed = append(changed, param)
		}
		text := func(param string, tf textField, now *string) {
			if tf.given() && !samePtr(tf.ptr(), now) {
				set(param, param, "", tf.ptr())
			}
		}
		curName := cur.name
		text("name", f.name, &curName)
		text("address", f.address, cur.address)
		text("property_type", f.propertyType, cur.propertyType)
		if f.purchaseDate.given() && !samePtr(f.purchaseDate.ptr(), cur.purchaseDate) {
			set("purchase_date", "purchase_date", "::date", f.purchaseDate.ptr())
		}
		if f.purchasePrice.given() && !sameMoney(f.purchasePrice.ptr(), cur.purchasePrice) {
			set("purchase_price", "purchase_price", "", f.purchasePrice.ptr())
		}
		if f.currentValue.given() && !sameMoney(f.currentValue.ptr(), cur.current) {
			set("current_value", "current_value", "", f.currentValue.ptr())
		}

		// Rooms: renames first, then additions checked against the renamed set.
		renamed, added, skipped, bad, err := applyRooms(ctx, tx, c.householdID, id, renames, add)
		if err != nil {
			return respond.Failed("update the rooms", err), nil
		} else if bad != nil {
			return bad, nil
		}
		if len(renamed) > 0 {
			changed = append(changed, "rename_rooms")
		}
		if len(added) > 0 {
			changed = append(changed, "add_rooms")
		}

		if len(changed) > 0 {
			sets = append(sets, "updated_at = NOW()")
			if _, err := tx.Exec(ctx, `UPDATE properties SET `+strings.Join(sets, ", ")+` WHERE id = $1 AND household_id = $2`, vals...); err != nil {
				return respond.Failed("update the property", err), nil
			}
		}
		d, err := loadProperty(ctx, tx, c.householdID, id)
		if err != nil {
			return respond.Failed("update the property", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("update the property", err), nil
		}
		d.Changed, d.RoomsAdded, d.RoomsSkipped = changed, added, skipped

		next := []string{roomHint(d)}
		if len(d.Rooms) > 0 {
			next = append(next, fmt.Sprintf("See %s with property_find id=%s.", d.Name, d.ID))
		}
		var extra []string
		if len(added) > 0 {
			extra = append(extra, "added "+joinNames(added))
		}
		if len(renamed) > 0 {
			extra = append(extra, "renamed "+joinNames(renamed))
		}
		if len(skipped) > 0 {
			extra = append(extra, "already had "+joinNames(skipped))
		}
		rooms := ""
		if len(extra) > 0 {
			rooms = " Rooms: " + strings.Join(extra, "; ") + "."
		}
		if len(changed) == 0 {
			return respond.OK(fmt.Sprintf("Nothing changed: %s already has those details (%s).%s", d.Name, d.describe(), rooms), d, next...), nil
		}
		return respond.OK(fmt.Sprintf("Updated %s (%s): %s.%s", d.Name, strings.Join(changed, ", "), d.describe(), rooms), d, next...), nil
	}
	return "property_update", tool, handler
}

// applyRooms renames and adds rooms of a locked property. renamed lists
// "Old → New" for renames that changed a name; added and skipped are the
// add_rooms names that were added and the ones the property already had.
func applyRooms(ctx context.Context, tx pgx.Tx, hid, propertyID uuid.UUID, renames []renameReq, add []string) (renamed, added, skipped []string, bad *mcp.CallToolResult, err error) {
	if len(renames) == 0 && len(add) == 0 {
		return nil, nil, nil, nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT id, name FROM rooms WHERE property_id = $1 ORDER BY lower(name), created_at, id FOR UPDATE`, propertyID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	rooms, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (namedRow, error) {
		var c namedRow
		err := r.Scan(&c.id, &c.name)
		c.label = c.name
		return c, err
	})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	names := make(map[uuid.UUID]string, len(rooms))
	for _, r := range rooms {
		names[r.id] = r.name
	}
	taken := func(name string, except uuid.UUID) bool {
		for id, n := range names {
			if id != except && strings.EqualFold(n, name) {
				return true
			}
		}
		return false
	}
	example := map[string]any{"property_id": propertyID.String(), "rename_rooms": []any{map[string]any{"room_id": "Den", "name": "Office"}}}

	for _, rn := range renames {
		var roomID uuid.UUID
		if id, perr := uuid.Parse(rn.ref); perr == nil {
			if _, ok := names[id]; !ok {
				return nil, nil, nil, respond.Invalid("rename_rooms", fmt.Sprintf("Room %s is not a room of this property.", id),
					"Use a room_id from this property's rooms (property_find id=…), or the room's current name.", example), nil
			}
			roomID = id
		} else {
			cands := make([]namedRow, 0, len(names))
			for id, n := range names {
				cands = append(cands, namedRow{id: id, name: n, label: n})
			}
			id, res := pickByName(cands, rn.ref, "rename_rooms", "room", "rooms",
				"Use a room_id or a current room name of this property (property_find id=… lists them).")
			if res != nil {
				return nil, nil, nil, res, nil
			}
			roomID = id
		}
		old := names[roomID]
		if old == rn.name {
			continue
		}
		if taken(rn.name, roomID) {
			return nil, nil, nil, respond.Invalid("rename_rooms", fmt.Sprintf("This property already has a room named %s.", rn.name),
				"Pick another name, or delete the other room first with room_delete.", example), nil
		}
		if _, err := tx.Exec(ctx, `UPDATE rooms SET name = $1 WHERE id = $2 AND property_id = $3`, rn.name, roomID, propertyID); err != nil {
			return nil, nil, nil, nil, err
		}
		names[roomID] = rn.name
		renamed = append(renamed, old+" → "+rn.name)
	}

	for _, n := range add {
		if taken(n, uuid.Nil) {
			skipped = append(skipped, n)
			continue
		}
		added = append(added, n)
	}
	if err := insertRooms(ctx, tx, hid, propertyID, added); err != nil {
		return nil, nil, nil, nil, err
	}
	return renamed, added, skipped, nil, nil
}

// ---------------------------------------------------------------------------
// property_delete
// ---------------------------------------------------------------------------

// propertyRemoved is what was deleted with a property.
type propertyRemoved struct {
	Rooms        int `json:"rooms"`
	Assets       int `json:"assets"`
	UpkeepTasks  int `json:"upkeep_tasks" jsonschema_description:"Maintenance tasks of the property or its assets, with their calendar entries."`
	Calendars    int `json:"calendars" jsonschema_description:"Calendars that belonged to the property, with their events."`
	Events       int `json:"events" jsonschema_description:"Events on those calendars."`
	NotesDeleted int `json:"notes_deleted" jsonschema_description:"Notes about the property, its rooms or its assets."`
}

// propertyKept is what stays after a property is deleted.
type propertyKept struct {
	BillsUnlinked    int `json:"bills_unlinked" jsonschema_description:"Bills that stay, no longer tied to a property."`
	LoansUnlinked    int `json:"loans_unlinked" jsonschema_description:"Loans (e.g. the mortgage) that stay, no longer tied to a property."`
	DocumentsMoved   int `json:"documents_moved" jsonschema_description:"Documents moved to the family."`
	CredentialsMoved int `json:"credentials_moved" jsonschema_description:"Saved credentials moved to the family."`
	ListsMoved       int `json:"lists_moved" jsonschema_description:"Lists moved to the family."`
}

// propertyDeleted is property_delete's data.
type propertyDeleted struct {
	PropertyID string          `json:"property_id"`
	Name       string          `json:"name"`
	Deleted    bool            `json:"deleted"`
	Removed    propertyRemoved `json:"removed"`
	Kept       propertyKept    `json:"kept"`
}

// dependents lists what goes with the property, for the confirm refusal and
// the summary, e.g. "3 rooms, 14 assets and 2 upkeep tasks".
func (d propertyDeleted) dependents() []string {
	var out []string
	for _, it := range []struct {
		n    int
		noun string
	}{
		{d.Removed.Rooms, "room"}, {d.Removed.Assets, "asset"}, {d.Removed.UpkeepTasks, "upkeep task"},
		{d.Removed.Calendars, "calendar"}, {d.Removed.NotesDeleted, "note"},
		{d.Kept.BillsUnlinked, "bill"}, {d.Kept.LoansUnlinked, "loan"}, {d.Kept.DocumentsMoved, "document"},
		{d.Kept.CredentialsMoved, "credential"}, {d.Kept.ListsMoved, "list"},
	} {
		if it.n > 0 {
			out = append(out, plural(it.n, it.noun))
		}
	}
	return out
}

// subjectsCTE is every entity that goes when the property goes: the
// property, its rooms, its assets and its upkeep tasks (including those of
// its assets). rehome_entity_refs runs for each, so notes about them are
// deleted and files, secrets and lists about them move to the family.
const subjectsCTE = `
WITH tasks AS (
    SELECT m.id FROM maintenance_tasks m
     WHERE m.household_id = $2
       AND (m.property_id = $1 OR m.asset_id IN (SELECT a.id FROM assets a WHERE a.property_id = $1))
), subj(kind, id) AS (
    SELECT 'property', $1::uuid
    UNION ALL SELECT 'room', r.id FROM rooms r WHERE r.property_id = $1
    UNION ALL SELECT 'asset', a.id FROM assets a WHERE a.property_id = $1
    UNION ALL SELECT 'maintenance_task', t.id FROM tasks t
)`

// NewPropertyDeleteTool creates the property_delete MCP tool (catalog v2,
// class X). Deleting a property deletes its rooms, assets, upkeep tasks and
// its own calendars (FK cascades); notes about them are deleted; documents,
// credentials and lists about them move to the family (038/046
// rehome_entity_refs); bills and loans stay without the property. When
// anything is attached, confirm:true is required and the refusal gives the
// counts. A missing or already deleted property is reported as not found,
// so a repeat call is safe.
func NewPropertyDeleteTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Delete a property with its rooms, assets and upkeep. Documents and credentials move to the family. Needs confirm:true."),
		mcp.WithString("property_id", mcp.Description("The property's id from property_find, or its name, e.g. Lake House"), mcp.Required()),
		mcp.WithBoolean("confirm", mcp.Description("true once the user agreed to delete everything listed")),
		respond.Schema[propertyDeleted](),
	}
	tool := mcp.NewTool("property_delete", append(opts, annotations("Delete property", false, true, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "property_delete"), nil
		}
		args := withAliases(req.GetArguments(), writeAliases)
		confirm := boolArg(args, "confirm")

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("delete the property", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		id, bad, err := resolveProperty(ctx, tx, c.householdID, args, deleteExample)
		if err != nil {
			return respond.Failed("delete the property", err), nil
		} else if bad != nil {
			return bad, nil
		}
		cur, err := lockProperty(ctx, tx, c.householdID, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("property", id.String(), "Call property_find to list the family's properties and their ids."), nil
		}
		if err != nil {
			return respond.Failed("delete the property", err), nil
		}

		out := propertyDeleted{PropertyID: id.String(), Name: cur.name}
		if err := tx.QueryRow(ctx, subjectsCTE+`
SELECT (SELECT count(*) FROM rooms WHERE property_id = $1),
       (SELECT count(*) FROM assets WHERE property_id = $1),
       (SELECT count(*) FROM tasks),
       (SELECT count(*) FROM calendars WHERE household_id = $2 AND property_id = $1),
       (SELECT count(*) FROM calendar_objects co JOIN calendars cal ON cal.id = co.calendar_id
         WHERE cal.household_id = $2 AND cal.property_id = $1),
       (SELECT count(*) FROM notes n JOIN subj s ON n.entity_type = s.kind AND n.entity_id = s.id WHERE n.household_id = $2),
       (SELECT count(*) FROM bills WHERE household_id = $2 AND property_id = $1),
       (SELECT count(*) FROM loans WHERE household_id = $2 AND property_id = $1),
       (SELECT count(*) FROM files f JOIN subj s ON f.entity_type = s.kind AND f.entity_id = s.id WHERE f.household_id = $2),
       (SELECT count(*) FROM secrets x JOIN subj s ON x.entity_type = s.kind AND x.entity_id = s.id WHERE x.household_id = $2),
       (SELECT count(*) FROM lists l JOIN subj s ON l.entity_type = s.kind AND l.entity_id = s.id WHERE l.household_id = $2)`,
			id, c.householdID).Scan(
			&out.Removed.Rooms, &out.Removed.Assets, &out.Removed.UpkeepTasks, &out.Removed.Calendars, &out.Removed.Events,
			&out.Removed.NotesDeleted, &out.Kept.BillsUnlinked, &out.Kept.LoansUnlinked, &out.Kept.DocumentsMoved,
			&out.Kept.CredentialsMoved, &out.Kept.ListsMoved); err != nil {
			return respond.Failed("delete the property", err), nil
		}
		deps := out.dependents()
		if len(deps) > 0 && !confirm {
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("%s has %s; pass confirm:true to delete it.", cur.name, joinNames(deps)),
				Fix: "Ask the user to confirm, then call property_delete again with confirm:true. Rooms, assets, upkeep and the property's calendars " +
					"are deleted; bills and loans stay without the property; documents, credentials and lists move to the family.",
				Field:   "confirm",
				Example: map[string]any{"property_id": id.String(), "confirm": true},
			}), nil
		}

		// Upkeep events on calendars that stay are deleted by the 038 rehome
		// trigger with their task; record each delete so phones sync, as
		// maintenance_delete does.
		type event struct{ calendarID, uid string }
		rows, err := tx.Query(ctx, subjectsCTE+`
SELECT co.calendar_id::text, co.uid FROM calendar_objects co JOIN calendars cal ON cal.id = co.calendar_id
 WHERE cal.household_id = $2 AND cal.property_id IS DISTINCT FROM $1
   AND co.entity_type = 'maintenance_task' AND co.entity_id IN (SELECT id FROM tasks)`, id, c.householdID)
		if err != nil {
			return respond.Failed("delete the property", err), nil
		}
		events, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (event, error) {
			var e event
			return e, r.Scan(&e.calendarID, &e.uid)
		})
		if err != nil {
			return respond.Failed("delete the property", err), nil
		}

		if _, err := tx.Exec(ctx, `DELETE FROM properties WHERE id = $1 AND household_id = $2`, id, c.householdID); err != nil {
			return respond.Failed("delete the property", err), nil
		}
		bumped := map[string]bool{}
		for _, e := range events {
			if _, err := tx.Exec(ctx, `INSERT INTO calendar_changes (calendar_id, event_uid, action) VALUES ($1, $2, 'delete')`,
				e.calendarID, e.uid); err != nil {
				return respond.Failed("delete the property", err), nil
			}
			if !bumped[e.calendarID] {
				bumped[e.calendarID] = true
				if _, err := tx.Exec(ctx, `UPDATE calendars SET ctag = gen_random_uuid()::text, updated_at = NOW() WHERE id = $1`,
					e.calendarID); err != nil {
					return respond.Failed("delete the property", err), nil
				}
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("delete the property", err), nil
		}
		out.Deleted = true

		summary := fmt.Sprintf("Deleted %s.", cur.name)
		if len(deps) > 0 {
			summary = fmt.Sprintf("Deleted %s (had %s).", cur.name, joinNames(deps))
		}
		next := []string{"List the properties that are left with property_find."}
		if out.Kept.BillsUnlinked+out.Kept.LoansUnlinked > 0 {
			next = append(next, "Its bills and loans stay; see them with bill_find and loan_find, and delete any that ended.")
		}
		return respond.OK(summary, out, next...), nil
	}
	return "property_delete", tool, handler
}
