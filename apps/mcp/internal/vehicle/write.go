package vehicle

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

// The write tools never read or write the legacy finance columns on
// vehicles (lender, loan_amount, loan_term_months, monthly_payment,
// registration_cost, registration_renewal_month, insurance_cost,
// insurance_provider): a car loan is a loan and insurance and registration
// are bills, tied to the vehicle by vehicle_id (#4704; #4707 drops the
// columns). vehicles has no nickname or odometer column, so neither is a
// param; a call that sends one is told to put it in notes.

// writeAliases are other names small models use for the write params. A
// catalog name that is present wins.
var writeAliases = map[string]string{
	"id": "vehicle_id", "vehicle": "vehicle_id", "car": "vehicle_id", "car_id": "vehicle_id",
	"manufacturer": "make", "brand": "make",
	"model_year": "year",
	"plate":      "license_plate", "plate_number": "license_plate", "license": "license_plate", "tag": "license_plate",
	"vin_number": "vin",
	"colour":     "color",
	"value":      "current_value", "estimated_value": "current_value", "worth": "current_value",
	"price": "purchase_price", "bought_for": "purchase_price",
	"bought_on": "purchase_date", "purchased": "purchase_date",
	"note": "notes",
}

// unstoredParams are things small models send that vehicles has no column
// for. They are not written; the result's next hint says to use notes.
var unstoredParams = []string{"odometer", "mileage", "miles", "nickname", "name"}

// Examples for error results.
var (
	createExample = map[string]any{"make": "Honda", "model": "Odyssey", "year": 2019, "license_plate": "ABC123"}
	updateExample = map[string]any{"vehicle_id": "Honda Odyssey", "current_value": 18500}
	deleteExample = map[string]any{"vehicle_id": "Honda Odyssey"}
)

// vehicleFields is the validated create/update fields.
type vehicleFields struct {
	make_, model, vin, plate, color, notes, purchaseDate textField
	year                                                 yearField
	purchasePrice, currentValue                          moneyField
	unstored                                             []string // params sent that are not stored
}

func (f vehicleFields) given() bool {
	return f.make_.given() || f.model.given() || f.vin.given() || f.plate.given() || f.color.given() ||
		f.notes.given() || f.purchaseDate.given() || f.year.given() || f.purchasePrice.given() || f.currentValue.given()
}

// parseFields validates every create/update field present in args.
func parseFields(args map[string]any) (vehicleFields, error) {
	var f vehicleFields
	var err error
	for _, t := range []struct {
		key string
		max int
		dst *textField
	}{
		{"make", maxMakeLen, &f.make_}, {"model", maxModelLen, &f.model}, {"vin", maxVINLen, &f.vin},
		{"license_plate", maxPlateLen, &f.plate}, {"color", maxColorLen, &f.color}, {"notes", maxNotesLen, &f.notes},
		{"purchase_date", 10, &f.purchaseDate},
	} {
		if *t.dst, err = readText(args, t.key, t.max); err != nil {
			if t.key == "purchase_date" {
				return f, badPurchaseDate(fmt.Sprint(args[t.key]))
			}
			return f, err
		}
	}
	if f.vin.set { // VINs are upper case without spaces
		f.vin.val = strings.ToUpper(strings.Join(strings.Fields(f.vin.val), ""))
	}
	if f.purchaseDate.set {
		d, perr := time.Parse(time.DateOnly, f.purchaseDate.val)
		if perr != nil {
			return f, badPurchaseDate(f.purchaseDate.val)
		}
		if d.Year() < minYear {
			return f, badArg("purchase_date", fmt.Sprintf("purchase_date %s is before %d.", f.purchaseDate.val, minYear),
				"Pass the date the family bought it as YYYY-MM-DD; leave it out if not known.")
		}
	}
	if f.year, err = readYear(args); err != nil {
		return f, err
	}
	if f.purchasePrice, err = readMoney(args, "purchase_price"); err != nil {
		return f, err
	}
	if f.currentValue, err = readMoney(args, "current_value"); err != nil {
		return f, err
	}
	for _, k := range unstoredParams {
		if v, ok := args[k]; ok && v != nil && v != "" {
			f.unstored = append(f.unstored, k)
		}
	}
	return f, nil
}

func badPurchaseDate(v string) error {
	return badArg("purchase_date", fmt.Sprintf("purchase_date %q is not a date.", v),
		"Pass purchase_date as YYYY-MM-DD, e.g. 2019-06-14; leave it out if not known.")
}

// unstoredHint is the next hint for params vehicles has no column for.
func unstoredHint(f vehicleFields, id string) string {
	if len(f.unstored) == 0 {
		return ""
	}
	return fmt.Sprintf("Vehicles have no %s field, so it was not saved; add it to notes with vehicle_update vehicle_id=%s notes=….",
		joinNames(f.unstored), id)
}

// fieldParams are the optional vehicle params shared by create and update.
func fieldParams() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithNumber("year", mcp.Description("Model year, e.g. 2019")),
		mcp.WithString("vin", mcp.Description("Vehicle identification number, e.g. 5FNRL6H78KB012345")),
		mcp.WithString("license_plate", mcp.Description("Plate number, e.g. ABC123")),
		mcp.WithString("color", mcp.Description("e.g. Blue")),
		mcp.WithString("purchase_date", mcp.Description("Date bought, YYYY-MM-DD")),
		mcp.WithNumber("purchase_price", mcp.Description("Price paid, in the family's currency, e.g. 32000")),
		mcp.WithNumber("current_value", mcp.Description("Estimated value today, e.g. 18500")),
		mcp.WithString("notes", mcp.Description("Anything else: nickname, mileage, tire size, e.g. Mom's van, 45k mi")),
	}
}

// summaryOf is "2019 Honda Odyssey: plate ABC123, worth $18,500.00", or just
// the name when there is nothing to add.
func summaryOf(d vehicleData) string {
	if s := d.describe(); s != "" {
		return d.Name + ": " + s
	}
	return d.Name
}

// ---------------------------------------------------------------------------
// vehicle_create
// ---------------------------------------------------------------------------

// NewVehicleCreateTool creates the vehicle_create MCP tool (catalog v2, class
// C). "Add our 2019 Honda Odyssey" is one call: make and model are required,
// everything else is optional. The loop guard returns a vehicle with the
// same make and model (and no conflicting year or VIN) added to the family
// in the last 10 minutes (duplicate:true) instead of a second row, under an
// advisory lock so parallel calls make one row.
func NewVehicleCreateTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := append([]mcp.ToolOption{
		mcp.WithDescription("Add a vehicle. Loans, bills (insurance, registration) and upkeep link to it by vehicle_id."),
		mcp.WithString("make", mcp.Description("Manufacturer, e.g. Honda"), mcp.Required()),
		mcp.WithString("model", mcp.Description("Model, e.g. Odyssey"), mcp.Required()),
	}, fieldParams()...)
	opts = append(opts, respond.Schema[vehicleData]())
	tool := mcp.NewTool("vehicle_create", append(opts, annotations("Add vehicle", false, false, false)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "vehicle_create"), nil
		}
		f, err := parseFields(withAliases(req.GetArguments(), writeAliases))
		if err != nil {
			return invalid(err, createExample), nil
		}
		switch {
		case !f.make_.set:
			return respond.Invalid("make", "make is required.", "Pass the manufacturer, e.g. Honda, and the model, e.g. Odyssey.", createExample), nil
		case !f.model.set:
			return respond.Invalid("model", "model is required.", "Pass the model, e.g. Odyssey, with the make.", createExample), nil
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("add the vehicle", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		// Loop guard.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('vehicle_create:' || $1::text || ':' || lower($2) || ':' || lower($3), 0))`,
			c.householdID, f.make_.val, f.model.val); err != nil {
			return respond.Failed("add the vehicle", err), nil
		}
		var dupID uuid.UUID
		err = tx.QueryRow(ctx, `
SELECT id FROM vehicles
 WHERE household_id = $1 AND lower(btrim(make)) = lower($2) AND lower(btrim(model)) = lower($3)
   AND ($4::int IS NULL OR year IS NULL OR year = $4)
   AND ($5::text IS NULL OR vin IS NULL OR btrim(vin) = '' OR upper(vin) = $5)
   AND created_at > NOW() - $6::interval
 ORDER BY created_at DESC LIMIT 1`, c.householdID, f.make_.val, f.model.val, f.year.ptr(), f.vin.ptr(), duplicateWindow).Scan(&dupID)
		switch {
		case err == nil:
			d, err := loadVehicle(ctx, tx, c.householdID, dupID)
			if err != nil {
				return respond.Failed("add the vehicle", err), nil
			}
			d.Duplicate = true
			return respond.OK(fmt.Sprintf("%s was already added a few minutes ago; returned that vehicle instead of a copy.", d.Name), d,
				fmt.Sprintf("Change details with vehicle_update vehicle_id=%s instead of adding it again.", d.ID)), nil
		case !errors.Is(err, pgx.ErrNoRows):
			return respond.Failed("add the vehicle", err), nil
		}

		var id uuid.UUID
		if err := tx.QueryRow(ctx, `
INSERT INTO vehicles (household_id, year, make, model, vin, license_plate, color, notes, purchase_date, purchase_price, current_value)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, $10, $11) RETURNING id`,
			c.householdID, f.year.ptr(), f.make_.val, f.model.val, f.vin.ptr(), f.plate.ptr(), f.color.ptr(), f.notes.ptr(),
			f.purchaseDate.ptr(), f.purchasePrice.ptr(), f.currentValue.ptr()).Scan(&id); err != nil {
			return respond.Failed("add the vehicle", err), nil
		}
		var sameName int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM vehicles WHERE household_id = $1 AND id <> $4
			AND lower(btrim(make)) = lower($2) AND lower(btrim(model)) = lower($3)`,
			c.householdID, f.make_.val, f.model.val, id).Scan(&sameName); err != nil {
			return respond.Failed("add the vehicle", err), nil
		}
		d, err := loadVehicle(ctx, tx, c.householdID, id)
		if err != nil {
			return respond.Failed("add the vehicle", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("add the vehicle", err), nil
		}

		next := []string{
			fmt.Sprintf("Add its insurance or registration with bill_create name=… amount=… frequency=annual due_date=… vehicle_id=%s.", d.ID),
			fmt.Sprintf("Schedule upkeep (oil change, tires) with maintenance_create name=… due_date=… vehicle_id=%s.", d.ID),
		}
		switch {
		case len(f.unstored) > 0:
			next = append(next, unstoredHint(f, d.ID))
		case sameName > 0:
			next = append(next, fmt.Sprintf("The family has another %s %s; pass vehicle ids, not the name, from now on.",
				f.make_.val, f.model.val))
		default:
			next = append(next, fmt.Sprintf("Record its car loan with loan_create name=… original_amount=… remaining_balance=… vehicle_id=%s.", d.ID))
		}
		return respond.OK(fmt.Sprintf("Added %s.", summaryOf(d)), d, next...), nil
	}
	return "vehicle_create", tool, handler
}

// ---------------------------------------------------------------------------
// vehicle_update
// ---------------------------------------------------------------------------

// NewVehicleUpdateTool creates the vehicle_update MCP tool (catalog v2, class
// U). Only the given fields change: an absent field or "" is left alone,
// and JSON null clears year, vin, license_plate, color, notes,
// purchase_date, purchase_price or current_value. make and model cannot be
// cleared. A call that changes nothing writes nothing, so a repeat is safe.
// vehicle_id is the vehicle's id or its name ("Honda Odyssey").
func NewVehicleUpdateTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := append([]mcp.ToolOption{
		mcp.WithDescription("Change a vehicle, e.g. its value or plate. Only the given fields change; null clears one."),
		mcp.WithString("vehicle_id", mcp.Description("The vehicle's id from vehicle_find, or its name, e.g. Honda Odyssey"), mcp.Required()),
		mcp.WithString("make", mcp.Description("Manufacturer, e.g. Honda")),
		mcp.WithString("model", mcp.Description("Model, e.g. Odyssey")),
	}, fieldParams()...)
	opts = append(opts, respond.Schema[vehicleData]())
	tool := mcp.NewTool("vehicle_update", append(opts, annotations("Update vehicle", false, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "vehicle_update"), nil
		}
		args := withAliases(req.GetArguments(), writeAliases)
		f, err := parseFields(args)
		if err != nil {
			return invalid(err, updateExample), nil
		}
		for _, p := range []struct {
			key string
			f   textField
		}{{"make", f.make_}, {"model", f.model}} {
			if p.f.clear {
				return respond.Invalid(p.key, p.key+" cannot be cleared.", "Pass a new value, or leave "+p.key+" out to keep it.", updateExample), nil
			}
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("update the vehicle", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		id, bad, err := resolveVehicle(ctx, tx, c.householdID, args, updateExample)
		if err != nil {
			return respond.Failed("update the vehicle", err), nil
		} else if bad != nil {
			return bad, nil
		}
		if !f.given() && len(f.unstored) == 0 {
			return respond.Invalid("", "No fields to update.",
				"Pass vehicle_id plus at least one field, e.g. current_value, license_plate or notes.", updateExample), nil
		}
		cur, err := lockVehicle(ctx, tx, c.householdID, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("vehicle", id.String(), "Call vehicle_find to list the family's vehicles and their ids."), nil
		}
		if err != nil {
			return respond.Failed("update the vehicle", err), nil
		}

		// Columns come only from this list, never from the caller.
		vals := []any{id, c.householdID}
		var sets, changed []string
		set := func(param, col, cast string, v any) {
			vals = append(vals, v)
			sets = append(sets, fmt.Sprintf("%s = $%d%s", col, len(vals), cast))
			changed = append(changed, param)
		}
		text := func(param, col, cast string, tf textField, now *string) {
			if tf.given() && !samePtr(tf.ptr(), now) {
				set(param, col, cast, tf.ptr())
			}
		}
		money := func(param, col string, mf moneyField, now *float64) {
			if mf.given() && !sameMoney(mf.ptr(), now) {
				set(param, col, "", mf.ptr())
			}
		}
		if f.year.given() && !sameInt(f.year.ptr(), cur.year) {
			set("year", "year", "", f.year.ptr())
		}
		text("make", "make", "", f.make_, cur.make_)
		text("model", "model", "", f.model, cur.model)
		text("vin", "vin", "", f.vin, cur.vin)
		text("license_plate", "license_plate", "", f.plate, cur.plate)
		text("color", "color", "", f.color, cur.color)
		text("purchase_date", "purchase_date", "::date", f.purchaseDate, cur.purchaseDate)
		money("purchase_price", "purchase_price", f.purchasePrice, cur.purchasePrice)
		money("current_value", "current_value", f.currentValue, cur.currentValue)
		text("notes", "notes", "", f.notes, cur.notes)

		if len(sets) > 0 {
			sets = append(sets, "updated_at = NOW()")
			if _, err := tx.Exec(ctx, `UPDATE vehicles SET `+strings.Join(sets, ", ")+` WHERE id = $1 AND household_id = $2`, vals...); err != nil {
				return respond.Failed("update the vehicle", err), nil
			}
		}
		d, err := loadVehicle(ctx, tx, c.householdID, id)
		if err != nil {
			return respond.Failed("update the vehicle", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("update the vehicle", err), nil
		}
		d.Changed = changed

		next := []string{fmt.Sprintf("See its loans, bills and upkeep with vehicle_find id=%s.", d.ID)}
		if h := unstoredHint(f, d.ID); h != "" {
			next = append(next, h)
		}
		if len(changed) == 0 {
			return respond.OK(fmt.Sprintf("Nothing changed: %s already has those details.", summaryOf(d)), d, next...), nil
		}
		return respond.OK(fmt.Sprintf("Updated %s (%s).", summaryOf(d), strings.Join(changed, ", ")), d, next...), nil
	}
	return "vehicle_update", tool, handler
}

// ---------------------------------------------------------------------------
// vehicle_delete
// ---------------------------------------------------------------------------

// vehicleRemoved is what is deleted with a vehicle.
type vehicleRemoved struct {
	UpkeepTasks     int `json:"upkeep_tasks" jsonschema_description:"Maintenance tasks for the vehicle, with their calendar entries."`
	UpkeepSchedules int `json:"upkeep_schedules" jsonschema_description:"Recurring maintenance schedules for the vehicle."`
	Events          int `json:"events" jsonschema_description:"Calendar entries of those tasks."`
	NotesDeleted    int `json:"notes_deleted" jsonschema_description:"Notes about the vehicle or its upkeep."`
}

// vehicleKept is what stays after a vehicle is deleted.
type vehicleKept struct {
	BillsUnlinked    int `json:"bills_unlinked" jsonschema_description:"Bills (insurance, registration) that stay, family-level."`
	LoansUnlinked    int `json:"loans_unlinked" jsonschema_description:"Loans (the car loan) that stay, family-level."`
	DocumentsMoved   int `json:"documents_moved" jsonschema_description:"Documents moved to the family."`
	CredentialsMoved int `json:"credentials_moved" jsonschema_description:"Saved credentials moved to the family."`
	ListsMoved       int `json:"lists_moved" jsonschema_description:"Lists moved to the family."`
}

// vehicleDeleted is vehicle_delete's data.
type vehicleDeleted struct {
	VehicleID string         `json:"vehicle_id"`
	Name      string         `json:"name" jsonschema_description:"Year, make and model of the deleted vehicle."`
	Deleted   bool           `json:"deleted"`
	Removed   vehicleRemoved `json:"removed"`
	Kept      vehicleKept    `json:"kept"`
}

// lost lists what is deleted with the vehicle: a non-empty list needs
// confirm:true.
func (d vehicleDeleted) lost() []string {
	var out []string
	for _, it := range []struct {
		n    int
		noun string
	}{
		{d.Removed.UpkeepTasks, "upkeep task"}, {d.Removed.UpkeepSchedules, "upkeep schedule"}, {d.Removed.NotesDeleted, "note"},
	} {
		if it.n > 0 {
			out = append(out, plural(it.n, it.noun))
		}
	}
	return out
}

// subjectsCTE is every entity that goes when the vehicle goes: the vehicle,
// its upkeep tasks and its upkeep schedules (FK cascades). rehome_entity_refs
// runs for each, so notes about them are deleted, files, secrets and lists
// about them move to the family, and a task's calendar entry is deleted.
const subjectsCTE = `
WITH tasks AS (
    SELECT m.id FROM maintenance_tasks m WHERE m.household_id = $2 AND m.vehicle_id = $1
), schedules AS (
    SELECT s.id FROM maintenance_schedules s WHERE s.household_id = $2 AND s.vehicle_id = $1
), subj(kind, id) AS (
    SELECT 'vehicle', $1::uuid
    UNION ALL SELECT 'maintenance_task', t.id FROM tasks t
    UNION ALL SELECT 'maintenance_schedule', s.id FROM schedules s
)`

// NewVehicleDeleteTool creates the vehicle_delete MCP tool (catalog v2, class
// X). Its loans and bills stay, family-level (FK SET NULL); its upkeep tasks
// and schedules are deleted (FK cascade) with the tasks' calendar entries;
// notes about them are deleted; documents, credentials and lists move to
// the family (rehome_entity_refs). When upkeep or notes would be deleted,
// confirm:true is required and the refusal gives the counts. A missing or
// already deleted vehicle is reported as not found, so a repeat is safe.
func NewVehicleDeleteTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Delete a vehicle. Its loans and bills stay, family-level; its upkeep is deleted (needs confirm:true if it has any)."),
		mcp.WithString("vehicle_id", mcp.Description("The vehicle's id from vehicle_find, or its name, e.g. Honda Odyssey"), mcp.Required()),
		mcp.WithBoolean("confirm", mcp.Description("true once the user agreed to delete its upkeep and notes too")),
		respond.Schema[vehicleDeleted](),
	}
	tool := mcp.NewTool("vehicle_delete", append(opts, annotations("Delete vehicle", false, true, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "vehicle_delete"), nil
		}
		args := withAliases(req.GetArguments(), writeAliases)
		confirm := boolArg(args, "confirm")

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("delete the vehicle", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		id, bad, err := resolveVehicle(ctx, tx, c.householdID, args, deleteExample)
		if err != nil {
			return respond.Failed("delete the vehicle", err), nil
		} else if bad != nil {
			return bad, nil
		}
		cur, err := lockVehicle(ctx, tx, c.householdID, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("vehicle", id.String(), "Call vehicle_find to list the family's vehicles and their ids."), nil
		}
		if err != nil {
			return respond.Failed("delete the vehicle", err), nil
		}
		name := vehicleRow{Year: cur.year, Make: cur.make_, Model: cur.model}.displayName()

		out := vehicleDeleted{VehicleID: id.String(), Name: name}
		if err := tx.QueryRow(ctx, subjectsCTE+`
SELECT (SELECT count(*) FROM tasks),
       (SELECT count(*) FROM schedules),
       (SELECT count(*) FROM calendar_objects co JOIN calendars cal ON cal.id = co.calendar_id
         WHERE cal.household_id = $2 AND co.entity_type = 'maintenance_task' AND co.entity_id IN (SELECT id FROM tasks)),
       (SELECT count(*) FROM notes n JOIN subj s ON n.entity_type = s.kind AND n.entity_id = s.id WHERE n.household_id = $2),
       (SELECT count(*) FROM bills WHERE household_id = $2 AND vehicle_id = $1),
       (SELECT count(*) FROM loans WHERE household_id = $2 AND vehicle_id = $1),
       (SELECT count(*) FROM files f JOIN subj s ON f.entity_type = s.kind AND f.entity_id = s.id WHERE f.household_id = $2),
       (SELECT count(*) FROM secrets x JOIN subj s ON x.entity_type = s.kind AND x.entity_id = s.id WHERE x.household_id = $2),
       (SELECT count(*) FROM lists l JOIN subj s ON l.entity_type = s.kind AND l.entity_id = s.id WHERE l.household_id = $2)`,
			id, c.householdID).Scan(
			&out.Removed.UpkeepTasks, &out.Removed.UpkeepSchedules, &out.Removed.Events, &out.Removed.NotesDeleted,
			&out.Kept.BillsUnlinked, &out.Kept.LoansUnlinked, &out.Kept.DocumentsMoved, &out.Kept.CredentialsMoved,
			&out.Kept.ListsMoved); err != nil {
			return respond.Failed("delete the vehicle", err), nil
		}
		lost := out.lost()
		if len(lost) > 0 && !confirm {
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("%s has %s; pass confirm:true to delete it.", name, joinNames(lost)),
				Fix: "Ask the user to confirm, then call vehicle_delete again with confirm:true. Its upkeep tasks, schedules and notes are " +
					"deleted; its loans and bills stay, family-level; documents and lists move to the family.",
				Field:   "confirm",
				Example: map[string]any{"vehicle_id": id.String(), "confirm": true},
			}), nil
		}

		// The tasks' calendar entries are deleted by the 038 rehome trigger
		// with their task; record each delete so phones sync, as
		// maintenance_delete and property_delete do.
		type event struct{ calendarID, uid string }
		rows, err := tx.Query(ctx, subjectsCTE+`
SELECT co.calendar_id::text, co.uid FROM calendar_objects co JOIN calendars cal ON cal.id = co.calendar_id
 WHERE cal.household_id = $2 AND co.entity_type = 'maintenance_task' AND co.entity_id IN (SELECT id FROM tasks)`, id, c.householdID)
		if err != nil {
			return respond.Failed("delete the vehicle", err), nil
		}
		events, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (event, error) {
			var e event
			return e, r.Scan(&e.calendarID, &e.uid)
		})
		if err != nil {
			return respond.Failed("delete the vehicle", err), nil
		}

		if _, err := tx.Exec(ctx, `DELETE FROM vehicles WHERE id = $1 AND household_id = $2`, id, c.householdID); err != nil {
			return respond.Failed("delete the vehicle", err), nil
		}
		bumped := map[string]bool{}
		for _, e := range events {
			if _, err := tx.Exec(ctx, `INSERT INTO calendar_changes (calendar_id, event_uid, action) VALUES ($1, $2, 'delete')`,
				e.calendarID, e.uid); err != nil {
				return respond.Failed("delete the vehicle", err), nil
			}
			if !bumped[e.calendarID] {
				bumped[e.calendarID] = true
				if _, err := tx.Exec(ctx, `UPDATE calendars SET ctag = gen_random_uuid()::text, updated_at = NOW() WHERE id = $1`,
					e.calendarID); err != nil {
					return respond.Failed("delete the vehicle", err), nil
				}
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("delete the vehicle", err), nil
		}
		out.Deleted = true

		summary := fmt.Sprintf("Deleted %s.", name)
		if len(lost) > 0 {
			summary = fmt.Sprintf("Deleted %s with %s.", name, joinNames(lost))
		}
		var kept []string
		if n := out.Kept.BillsUnlinked; n > 0 {
			kept = append(kept, plural(n, "bill"))
		}
		if n := out.Kept.LoansUnlinked; n > 0 {
			kept = append(kept, plural(n, "loan"))
		}
		if n := out.Kept.DocumentsMoved; n > 0 {
			kept = append(kept, plural(n, "document"))
		}
		if len(kept) > 0 {
			summary += fmt.Sprintf(" Its %s stay, family-level.", joinNames(kept))
		}
		next := []string{"List the vehicles that are left with vehicle_find."}
		if out.Kept.BillsUnlinked+out.Kept.LoansUnlinked > 0 {
			next = append(next, "Its bills and loans stay; see them with bill_find and loan_find, and delete any that ended (sold car: insurance, loan).")
		}
		return respond.OK(summary, out, next...), nil
	}
	return "vehicle_delete", tool, handler
}
