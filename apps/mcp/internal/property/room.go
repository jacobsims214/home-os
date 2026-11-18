package property

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// roomAliases are other names small models use for room_delete's params.
var roomAliases = map[string]string{"id": "room_id", "room": "room_id", "room_name": "room_id"}

var roomDeleteExample = map[string]any{"room_id": "Den", "confirm": true}

// roomDeleted is room_delete's data.
type roomDeleted struct {
	RoomID         string `json:"room_id"`
	Name           string `json:"name"`
	PropertyID     string `json:"property_id"`
	PropertyName   string `json:"property_name"`
	Deleted        bool   `json:"deleted"`
	AssetsKept     int    `json:"assets_kept" jsonschema_description:"Things that were in the room; they stay in the property, without a room."`
	DocumentsMoved int    `json:"documents_moved" jsonschema_description:"Documents about the room, moved to the family."`
	ListsMoved     int    `json:"lists_moved" jsonschema_description:"Lists about the room, moved to the family."`
	NotesDeleted   int    `json:"notes_deleted" jsonschema_description:"Notes about the room, deleted with it."`
}

// resolveRoom matches room_id (a UUID, or a room's exact name, any case) in
// the caller's family. Rooms are scoped through their property's household.
// Room names repeat across properties ("Kitchen"), so several matches list
// each as "Kitchen in Home (id)".
func resolveRoom(ctx context.Context, q querier, hid uuid.UUID, args map[string]any) (uuid.UUID, *mcp.CallToolResult, error) {
	ref, err := optionalString(args, "room_id")
	if err != nil {
		return uuid.Nil, invalid(err, roomDeleteExample), nil
	}
	if ref == nil {
		return uuid.Nil, respond.Invalid("room_id", "room_id is required.",
			"Pass the room's id from property_find (rooms), or its name, e.g. Den.", roomDeleteExample), nil
	}
	if id, perr := uuid.Parse(*ref); perr == nil {
		return id, nil, nil
	}
	rows, err := q.Query(ctx, `
SELECT r.id, r.name, p.name FROM rooms r JOIN properties p ON p.id = r.property_id
 WHERE p.household_id = $1 ORDER BY lower(p.name), lower(r.name), r.id`, hid)
	if err != nil {
		return uuid.Nil, nil, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (namedRow, error) {
		var c namedRow
		var prop string
		err := r.Scan(&c.id, &c.name, &prop)
		c.label = c.name + " in " + prop
		return c, err
	})
	if err != nil {
		return uuid.Nil, nil, err
	}
	id, bad := pickByName(all, *ref, "room_id", "room", "rooms",
		"Pass the room_id from property_find (each property lists its rooms with ids).")
	return id, bad, nil
}

// NewRoomDeleteTool creates the room_delete MCP tool (catalog v2, class X).
// The room's assets stay in the property without a room (assets.room_id ON
// DELETE SET NULL; 038 keeps their property_id). Notes about the room are
// deleted, documents, credentials and lists about it move to the family
// (rehome_entity_refs). When the room holds things or has documents, notes
// or lists, confirm:true is required (catalog §1 "Destructive
// confirmations") and the refusal gives the counts. A missing or already
// deleted room is reported as not found, so a repeat call is safe.
func NewRoomDeleteTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Delete a room. Its assets stay in the property. Needs confirm:true if the room holds things."),
		mcp.WithString("room_id", mcp.Description("The room's id from property_find, or its name, e.g. Den"), mcp.Required()),
		mcp.WithBoolean("confirm", mcp.Description("true once the user agreed; its things stay in the property")),
		respond.Schema[roomDeleted](),
	}
	tool := mcp.NewTool("room_delete", append(opts, annotations("Delete room", false, true, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "room_delete"), nil
		}
		args := withAliases(req.GetArguments(), roomAliases)
		confirm := boolArg(args, "confirm")

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("delete the room", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		id, bad, err := resolveRoom(ctx, tx, c.householdID, args)
		if err != nil {
			return respond.Failed("delete the room", err), nil
		} else if bad != nil {
			return bad, nil
		}
		var (
			out    = roomDeleted{RoomID: id.String()}
			propID uuid.UUID
			creds  int
		)
		err = tx.QueryRow(ctx, `
SELECT r.name, p.id, p.name FROM rooms r JOIN properties p ON p.id = r.property_id
 WHERE r.id = $1 AND p.household_id = $2 FOR UPDATE OF r`, id, c.householdID).Scan(&out.Name, &propID, &out.PropertyName)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("room", id.String(), "Call property_find to list each property's rooms and their ids."), nil
		}
		if err != nil {
			return respond.Failed("delete the room", err), nil
		}
		out.PropertyID = propID.String()
		if err := tx.QueryRow(ctx, `
SELECT (SELECT count(*) FROM assets WHERE room_id = $1),
       (SELECT count(*) FROM files WHERE household_id = $2 AND entity_type = 'room' AND entity_id = $1),
       (SELECT count(*) FROM secrets WHERE household_id = $2 AND entity_type = 'room' AND entity_id = $1),
       (SELECT count(*) FROM lists WHERE household_id = $2 AND entity_type = 'room' AND entity_id = $1),
       (SELECT count(*) FROM notes WHERE household_id = $2 AND entity_type = 'room' AND entity_id = $1)`,
			id, c.householdID).Scan(&out.AssetsKept, &out.DocumentsMoved, &creds, &out.ListsMoved, &out.NotesDeleted); err != nil {
			return respond.Failed("delete the room", err), nil
		}
		var deps []string
		for _, it := range []struct {
			n    int
			noun string
		}{
			{out.AssetsKept, "asset"}, {out.DocumentsMoved, "document"}, {creds, "credential"},
			{out.ListsMoved, "list"}, {out.NotesDeleted, "note"},
		} {
			if it.n > 0 {
				deps = append(deps, plural(it.n, it.noun))
			}
		}
		if len(deps) > 0 && !confirm {
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("The %s in %s has %s; pass confirm:true to delete the room.", out.Name, out.PropertyName, joinNames(deps)),
				Fix: "Ask the user to confirm, then call room_delete again with confirm:true. Its things stay in the property without a room; " +
					"documents and lists move to the family; notes about the room are deleted.",
				Field:   "confirm",
				Example: map[string]any{"room_id": id.String(), "confirm": true},
			}), nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM rooms WHERE id = $1 AND property_id = $2`, id, propID); err != nil {
			return respond.Failed("delete the room", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("delete the room", err), nil
		}
		out.Deleted = true

		summary := fmt.Sprintf("Deleted the %s in %s.", out.Name, out.PropertyName)
		if out.AssetsKept > 0 {
			summary += fmt.Sprintf(" Its things (%s) stay in %s without a room.", plural(out.AssetsKept, "asset"), out.PropertyName)
		}
		next := []string{fmt.Sprintf("See the rooms that are left with property_find id=%s.", out.PropertyID)}
		if out.AssetsKept > 0 {
			next = append(next, fmt.Sprintf("Move its things to another room with asset_update asset_id=… room_id=… (asset_find property_id=%s lists them).", out.PropertyID))
		}
		return respond.OK(summary, out, next...), nil
	}
	return "room_delete", tool, handler
}
