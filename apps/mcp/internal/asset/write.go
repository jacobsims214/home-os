package asset

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// duplicateWindow is the asset_create loop guard (catalog §1 "Loop guard").
const duplicateWindow = "10 minutes"

// writeAliases are old (S1) parameter names for asset_update / asset_delete.
var writeAliases = map[string]string{"id": "asset_id"}

// createExample is a valid asset_create call for error examples.
var createExample = map[string]any{
	"name": "Dishwasher", "category": "appliance", "manufacturer": "Bosch", "model": "SHX78",
	"purchase_date": "2024-03-15", "warranty_expiry": "2026-03-15",
}

// updateExample is a valid asset_update call for error examples.
var updateExample = map[string]any{"asset_id": exampleAssetID, "current_value": 450}

// assetFields is the validated create/update fields. Each pointer is nil
// when the param was absent or blank; set records which params were given (a
// blank value clears that column on update).
type assetFields struct {
	set            map[string]bool
	name           *string
	category       *string
	manufacturer   *string
	model          *string
	serialNumber   *string
	roomID         *uuid.UUID
	propertyID     *uuid.UUID
	purchaseDate   *time.Time
	purchasePrice  *float64
	currentValue   *float64
	warrantyExpiry *time.Time
}

// fieldParams are the create/update params in the order summaries list them.
var fieldParams = []string{"name", "category", "manufacturer", "model", "serial_number", "room_id", "property_id",
	"purchase_date", "purchase_price", "current_value", "warranty_expiry"}

// parseFields validates every create/update field present in args.
func parseFields(args map[string]any) (assetFields, error) {
	f := assetFields{set: map[string]bool{}}
	for _, p := range fieldParams {
		if given(args, p) {
			f.set[p] = true
		}
	}
	for _, s := range []struct {
		key string
		max int
		dst **string
	}{
		{"name", maxNameLen, &f.name}, {"category", maxCategoryLen, &f.category},
		{"manufacturer", maxNameLen, &f.manufacturer}, {"model", maxNameLen, &f.model},
		{"serial_number", maxNameLen, &f.serialNumber},
	} {
		v, err := optionalString(args, s.key)
		if err != nil {
			return f, err
		}
		if v != nil && utf8.RuneCountInString(*v) > s.max {
			return f, badArg(s.key, fmt.Sprintf("%s must be at most %d characters.", s.key, s.max),
				fmt.Sprintf("Pass a shorter %s.", s.key))
		}
		*s.dst = v
	}
	var err error
	if f.roomID, err = optionalUUID(args, "room_id"); err != nil {
		return f, err
	}
	if f.propertyID, err = optionalUUID(args, "property_id"); err != nil {
		return f, err
	}
	for _, d := range []struct {
		key string
		dst **time.Time
	}{{"purchase_date", &f.purchaseDate}, {"warranty_expiry", &f.warrantyExpiry}} {
		s, err := optionalString(args, d.key)
		if err != nil {
			return f, err
		}
		if s == nil {
			continue
		}
		t, perr := time.Parse(time.DateOnly, *s)
		if perr != nil {
			return f, badArg(d.key, fmt.Sprintf("%s %q is not a date.", d.key, *s),
				fmt.Sprintf("Pass %s as YYYY-MM-DD, e.g. 2026-03-15.", d.key))
		}
		*d.dst = &t
	}
	for _, m := range []struct {
		key string
		dst **float64
	}{{"purchase_price", &f.purchasePrice}, {"current_value", &f.currentValue}} {
		v, ok, err := number(args, m.key)
		if err != nil {
			return f, err
		}
		if !ok {
			continue
		}
		if v < 0 || v > maxAmount {
			return f, badArg(m.key, fmt.Sprintf("%s must be between 0 and %.2f.", m.key, maxAmount),
				fmt.Sprintf("Pass %s as a plain number in the family's currency, e.g. 899.99.", m.key))
		}
		v = math.Round(v*100) / 100
		*m.dst = &v
	}
	return f, nil
}

// changed lists the params given, for the summary.
func (f assetFields) changed() []string {
	var out []string
	for _, p := range fieldParams {
		if f.set[p] {
			out = append(out, p)
		}
	}
	return out
}

// place is where an asset ends up: room and property after the rules of
// domain model v2 (a room implies its property).
type place struct {
	roomID, propertyID *uuid.UUID
	// leftRoom is set when a move to another property took it out of its room.
	leftRoom bool
}

// roomProperty returns the property of a room of this family; found is false
// for a missing room or another family's room.
func roomProperty(ctx context.Context, tx pgx.Tx, roomID uuid.UUID, householdID string) (prop uuid.UUID, found bool, err error) {
	err = tx.QueryRow(ctx,
		`SELECT r.property_id FROM rooms r JOIN properties p ON p.id = r.property_id
		 WHERE r.id = $1 AND p.household_id = $2`, roomID, householdID).Scan(&prop)
	if errors.Is(err, pgx.ErrNoRows) {
		return prop, false, nil
	}
	return prop, err == nil, err
}

// propertyExists reports whether the property belongs to this family.
func propertyExists(ctx context.Context, tx pgx.Tx, propertyID uuid.UUID, householdID string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM properties WHERE id = $1 AND household_id = $2)`,
		propertyID, householdID).Scan(&ok)
	return ok, err
}

// resolvePlace works out the asset's room and property from the given
// params, starting from current (zero for a new asset). It returns an
// isError result when a room or property is not the family's, or when
// room_id and property_id disagree.
func resolvePlace(ctx context.Context, tx pgx.Tx, f assetFields, current place, householdID string, example map[string]any) (place, *mcp.CallToolResult, error) {
	out := current
	out.leftRoom = false

	if f.propertyID != nil {
		ok, err := propertyExists(ctx, tx, *f.propertyID, householdID)
		if err != nil {
			return out, nil, err
		}
		if !ok {
			return out, respond.Invalid("property_id", fmt.Sprintf("No property with id %q in this family.", f.propertyID.String()),
				"Call property_find to get the property_id, or leave it out (it is optional).", example), nil
		}
	}

	switch {
	case f.roomID != nil:
		prop, ok, err := roomProperty(ctx, tx, *f.roomID, householdID)
		if err != nil {
			return out, nil, err
		}
		if !ok {
			return out, respond.Invalid("room_id", fmt.Sprintf("No room with id %q in this family.", f.roomID.String()),
				"Call property_find id=<property_id> to list its rooms and their ids, or leave room_id out.", example), nil
		}
		if f.propertyID != nil && *f.propertyID != prop {
			return out, respond.Invalid("property_id",
				fmt.Sprintf("Room %s is in property %s, not %s.", f.roomID.String(), prop.String(), f.propertyID.String()),
				"Leave property_id out: an asset's property follows its room.", example), nil
		}
		out.roomID, out.propertyID = f.roomID, &prop
	case f.set["room_id"]: // blank: take it out of its room
		out.roomID = nil
		if f.set["property_id"] {
			out.propertyID = f.propertyID
		}
	case f.set["property_id"]:
		if f.propertyID == nil || current.propertyID == nil || *f.propertyID != *current.propertyID {
			out.leftRoom = current.roomID != nil
			out.roomID = nil
		}
		out.propertyID = f.propertyID
	}
	return out, nil, nil
}

// dbError maps a constraint violation the checks above could not see (a room
// or property deleted meanwhile) to an isError result; nil otherwise.
func dbError(err error, example map[string]any) *mcp.CallToolResult {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		return nil
	}
	if strings.Contains(pgErr.Message, "room") || strings.Contains(pgErr.ConstraintName, "room") {
		return respond.Invalid("room_id", "That room does not exist in this family.",
			"Call property_find id=<property_id> to get a room_id, or leave room_id out.", example)
	}
	return respond.Invalid("property_id", "That property does not exist in this family.",
		"Call property_find to get a property_id, or leave it out.", example)
}

// createNext is asset_create's next-step hints (catalog "Next").
func createNext(d assetData) []string {
	return []string{
		fmt.Sprintf("Save its manual or receipt with document_upload about_type=asset about_id=%s.", d.ID),
		fmt.Sprintf("Plan upkeep with maintenance_create asset_id=%s.", d.ID),
	}
}

// NewAssetCreateTool creates the asset_create MCP tool (catalog v2, class
// C), replacing create_asset. Room and property are optional (domain model
// v2); with a room, the property is the room's. The loop guard returns an asset with the
// same name and category created in the household in the last 10 minutes
// (assets have no author column) with duplicate true instead of a second row.
func NewAssetCreateTool(pool *pgxpool.Pool, idx searchIndexer) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Add a thing the family owns. Room or property is optional (jewelry, a laptop). " +
			"Its property follows from the room."),
		mcp.WithString("name", mcp.Description("What it is, e.g. Dishwasher or Kitchen TV"), mcp.Required()),
		mcp.WithString("category", mcp.Description("Kind of thing, e.g. appliance, electronics, furniture"), mcp.Required()),
		mcp.WithString("manufacturer", mcp.Description("Maker, e.g. Bosch")),
		mcp.WithString("model", mcp.Description("Model name or number, e.g. SHX78")),
		mcp.WithString("serial_number", mcp.Description("Serial number")),
		mcp.WithString("room_id", mcp.Description("Room it is in (UUID from property_find id=...); sets the property")),
		mcp.WithString("property_id", mcp.Description("Property it is at (UUID from property_find); optional")),
		mcp.WithString("purchase_date", mcp.Description("Date bought, YYYY-MM-DD")),
		mcp.WithNumber("purchase_price", mcp.Description("Price paid, e.g. 899.99"), mcp.Min(0)),
		mcp.WithNumber("current_value", mcp.Description("What it is worth now, e.g. 450"), mcp.Min(0)),
		mcp.WithString("warranty_expiry", mcp.Description("Last day of the warranty, YYYY-MM-DD")),
		respond.Schema[assetData](),
	}
	tool := mcp.NewTool("asset_create", append(opts, annotations("Create asset", false, false, false)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		household := householdOf(ctx)
		if household == nil {
			return notSignedIn(), nil
		}
		f, err := parseFields(req.GetArguments())
		if err != nil {
			return invalid(err, createExample), nil
		}
		switch {
		case f.name == nil:
			return respond.Invalid("name", "name is required.", "Pass what the thing is, e.g. Dishwasher.", createExample), nil
		case f.category == nil:
			return respond.Invalid("category", "category is required.",
				"Pass a kind of thing, e.g. appliance, electronics, furniture or jewelry.", createExample), nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("create the asset", err), nil
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		where, res, err := resolvePlace(ctx, tx, f, place{}, *household, createExample)
		if err != nil {
			return respond.Failed("create the asset", err), nil
		} else if res != nil {
			return res, nil
		}
		today, err := householdToday(ctx, tx, *household)
		if err != nil {
			return respond.Failed("create the asset", err), nil
		}

		dup, err := scanAsset(tx.QueryRow(ctx, assetSelect+`
			WHERE a.household_id = $1 AND lower(a.name) = lower($2) AND lower(COALESCE(a.category, '')) = lower($3)
			  AND a.created_at > NOW() - $4::interval
			ORDER BY a.created_at DESC
			LIMIT 1`,
			*household, *f.name, *f.category, duplicateWindow,
		))
		switch {
		case err == nil:
			d := dup.data(today)
			d.Duplicate = true
			return respond.OK(
				fmt.Sprintf("Asset %s was already added a few minutes ago; returned it instead of adding a second one.", d.Name),
				d,
				fmt.Sprintf("Change it with asset_update asset_id=%s instead of creating it again.", d.ID),
			), nil
		case !errors.Is(err, pgx.ErrNoRows):
			return respond.Failed("create the asset", err), nil
		}

		var id uuid.UUID
		err = tx.QueryRow(ctx,
			`INSERT INTO assets (household_id, property_id, room_id, name, category, manufacturer, model,
			                     serial_number, purchase_date, purchase_price, current_value, warranty_expiry)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			 RETURNING id`,
			*household, where.propertyID, where.roomID, *f.name, *f.category, f.manufacturer, f.model,
			f.serialNumber, f.purchaseDate, f.purchasePrice, f.currentValue, f.warrantyExpiry,
		).Scan(&id)
		if err != nil {
			if res := dbError(err, createExample); res != nil {
				return res, nil
			}
			return respond.Failed("create the asset", err), nil
		}
		r, err := loadAsset(ctx, tx, id, *household)
		if err != nil {
			return respond.Failed("create the asset", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("create the asset", err), nil
		}
		indexAsset(idx, r)

		d := r.data(today)
		return respond.OK(fmt.Sprintf("Added %s.", d.describe()), d, createNext(d)...), nil
	}
	return "asset_create", tool, handler
}

// NewAssetUpdateTool creates the asset_update MCP tool (catalog v2, class
// U), replacing update_asset. Only the given fields change. name and
// category cannot be cleared; a blank text or date field clears it. Moving
// it to a room also sets its property; a blank room_id takes it out of its
// room (it stays at the property); a new property_id takes it out of a room
// in another property; a blank property_id unties it from any place. An
// asset of another household is reported as not found.
func NewAssetUpdateTool(pool *pgxpool.Pool, idx searchIndexer) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Change an asset; only the given fields change. Moving it to a room also sets its property."),
		mcp.WithString("asset_id", mcp.Description("The asset's id (UUID) from asset_find"), mcp.Required()),
		mcp.WithString("name", mcp.Description("New name, e.g. Dishwasher")),
		mcp.WithString("category", mcp.Description("New category, e.g. appliance")),
		mcp.WithString("manufacturer", mcp.Description("Maker; \"\" clears it")),
		mcp.WithString("model", mcp.Description("Model; \"\" clears it")),
		mcp.WithString("serial_number", mcp.Description("Serial number; \"\" clears it")),
		mcp.WithString("room_id", mcp.Description("Move it to this room (UUID); \"\" takes it out of its room")),
		mcp.WithString("property_id", mcp.Description("Move it to this property (UUID); \"\" unties it from any place")),
		mcp.WithString("purchase_date", mcp.Description("Date bought, YYYY-MM-DD; \"\" clears it")),
		mcp.WithNumber("purchase_price", mcp.Description("Price paid, e.g. 899.99"), mcp.Min(0)),
		mcp.WithNumber("current_value", mcp.Description("What it is worth now, e.g. 450"), mcp.Min(0)),
		mcp.WithString("warranty_expiry", mcp.Description("Last day of the warranty, YYYY-MM-DD; \"\" clears it")),
		respond.Schema[assetData](),
	}
	tool := mcp.NewTool("asset_update", append(opts, annotations("Update asset", false, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		household := householdOf(ctx)
		if household == nil {
			return notSignedIn(), nil
		}
		args := withAliases(req.GetArguments(), writeAliases)
		assetID, err := optionalUUID(args, "asset_id")
		if err != nil {
			return invalid(err, updateExample), nil
		}
		if assetID == nil {
			return respond.Invalid("asset_id", "asset_id is required.", "Pass the asset's id from asset_find.", updateExample), nil
		}
		f, err := parseFields(args)
		if err != nil {
			return invalid(err, updateExample), nil
		}
		for _, p := range []string{"name", "category"} {
			if f.set[p] && isBlank(args[p]) {
				return respond.Invalid(p, p+" cannot be cleared.", "Pass a new value, or leave "+p+" out to keep it.", updateExample), nil
			}
		}
		if len(f.set) == 0 {
			return respond.Invalid("", "No fields to update.",
				"Pass asset_id plus at least one field, e.g. current_value or room_id.", updateExample), nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("update the asset", err), nil
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		cur, err := scanAsset(tx.QueryRow(ctx, assetSelect+` WHERE a.id = $1 AND a.household_id = $2 FOR UPDATE OF a`,
			*assetID, *household))
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("asset", assetID.String(), "Call asset_find to get the asset_id."), nil
		}
		if err != nil {
			return respond.Failed("update the asset", err), nil
		}
		where, res, err := resolvePlace(ctx, tx, f, place{roomID: cur.RoomID, propertyID: cur.PropertyID}, *household, updateExample)
		if err != nil {
			return respond.Failed("update the asset", err), nil
		} else if res != nil {
			return res, nil
		}

		// Columns come only from this list, never from the caller.
		vals := []any{*assetID, *household}
		var sets []string
		add := func(param, col string, v any) {
			if f.set[param] {
				vals = append(vals, v)
				sets = append(sets, fmt.Sprintf("%s = $%d", col, len(vals)))
			}
		}
		add("name", "name", f.name)
		add("category", "category", f.category)
		add("manufacturer", "manufacturer", f.manufacturer)
		add("model", "model", f.model)
		add("serial_number", "serial_number", f.serialNumber)
		add("purchase_date", "purchase_date", f.purchaseDate)
		add("purchase_price", "purchase_price", f.purchasePrice)
		add("current_value", "current_value", f.currentValue)
		add("warranty_expiry", "warranty_expiry", f.warrantyExpiry)
		if f.set["room_id"] || f.set["property_id"] {
			vals = append(vals, where.roomID, where.propertyID)
			sets = append(sets, fmt.Sprintf("room_id = $%d", len(vals)-1), fmt.Sprintf("property_id = $%d", len(vals)))
		}
		sets = append(sets, "updated_at = NOW()")

		if _, err := tx.Exec(ctx, `UPDATE assets SET `+strings.Join(sets, ", ")+` WHERE id = $1 AND household_id = $2`, vals...); err != nil {
			if res := dbError(err, updateExample); res != nil {
				return res, nil
			}
			return respond.Failed("update the asset", err), nil
		}
		r, err := loadAsset(ctx, tx, *assetID, *household)
		if err != nil {
			return respond.Failed("update the asset", err), nil
		}
		today, err := householdToday(ctx, tx, *household)
		if err != nil {
			return respond.Failed("update the asset", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("update the asset", err), nil
		}
		indexAsset(idx, r)

		d := r.data(today)
		summary := fmt.Sprintf("Updated asset %s (%s): %s.", d.Name, strings.Join(f.changed(), ", "), d.describe())
		if where.leftRoom {
			summary += " It moved to another property, so it is no longer in a room; pass room_id to place it."
		}
		return respond.OK(summary, d, fmt.Sprintf("See it with asset_find id=%s.", d.ID)), nil
	}
	return "asset_update", tool, handler
}

// isBlank reports whether an argument value is a blank string.
func isBlank(v any) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// NewAssetDeleteTool creates the asset_delete MCP tool (catalog v2, class
// X), replacing delete_asset. It deletes one asset of the household and
// returns it as it was, with what went with it: its upkeep tasks are deleted
// (FK cascade), its documents move to the family and notes about it are
// deleted (rehome_entity_refs, 038). Bills and loans on it stay, untied. A
// missing or already deleted asset is reported as not found and nothing is
// deleted, so a repeat call is safe.
func NewAssetDeleteTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Delete an asset. Its documents move to the family; its upkeep tasks are deleted."),
		mcp.WithString("asset_id", mcp.Description("The asset's id (UUID) from asset_find"), mcp.Required()),
		respond.Schema[assetData](),
	}
	tool := mcp.NewTool("asset_delete", append(opts, annotations("Delete asset", false, true, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		household := householdOf(ctx)
		if household == nil {
			return notSignedIn(), nil
		}
		example := map[string]any{"asset_id": exampleAssetID}
		args := withAliases(req.GetArguments(), writeAliases)
		assetID, err := optionalUUID(args, "asset_id")
		if err != nil {
			return invalid(err, example), nil
		}
		if assetID == nil {
			return respond.Invalid("asset_id", "asset_id is required.", "Pass the asset's id from asset_find.", example), nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("delete the asset", err), nil
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		r, err := scanAsset(tx.QueryRow(ctx, assetSelect+` WHERE a.id = $1 AND a.household_id = $2 FOR UPDATE OF a`,
			*assetID, *household))
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("asset", assetID.String(), "Call asset_find to list assets and their ids."), nil
		}
		if err != nil {
			return respond.Failed("delete the asset", err), nil
		}
		var upkeep, docs, notes int
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM maintenance_tasks WHERE asset_id = $1),
			(SELECT count(*) FROM files WHERE entity_type = 'asset' AND entity_id = $1),
			(SELECT count(*) FROM notes WHERE entity_type = 'asset' AND entity_id = $1)`, *assetID,
		).Scan(&upkeep, &docs, &notes); err != nil {
			return respond.Failed("delete the asset", err), nil
		}
		today, err := householdToday(ctx, tx, *household)
		if err != nil {
			return respond.Failed("delete the asset", err), nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM assets WHERE id = $1 AND household_id = $2`, *assetID, *household); err != nil {
			return respond.Failed("delete the asset", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("delete the asset", err), nil
		}

		d := r.data(today)
		d.UpkeepTasksDeleted, d.DocumentsMoved, d.NotesDeleted = &upkeep, &docs, &notes
		summary := fmt.Sprintf("Deleted asset %s%s.", d.Name, d.where())
		var extra []string
		if upkeep > 0 {
			extra = append(extra, plural(upkeep, "upkeep task")+" deleted")
		}
		if docs > 0 {
			extra = append(extra, plural(docs, "document")+" moved to the family")
		}
		if notes > 0 {
			extra = append(extra, plural(notes, "note")+" deleted")
		}
		if len(extra) > 0 {
			summary += " " + strings.Join(extra, "; ") + "."
		}
		return respond.OK(summary, d, "Add an asset with asset_create.", "List what is left with asset_find."), nil
	}
	return "asset_delete", tool, handler
}

// plural formats a count with its noun, e.g. "1 note", "3 notes".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
