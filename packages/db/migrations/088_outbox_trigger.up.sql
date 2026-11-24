-- 088_outbox_trigger.up.sql (#5513, schema review F14/F23 of #5507)
--
-- THE DEFECT. outbox_events was written from HANDLER CODE, in the same
-- transaction, from three files: apps/api/internal/bill/repo.go (Create,
-- Update, Delete), apps/api/internal/asset/repo.go (Create, Update) and
-- apps/mcp/internal/bill/write.go (writeOutbox, called from bill_create,
-- bill_update, bill_delete and bill_pay). Side-effects in handler code fire
-- only when that handler runs -- #5314's finding, and the reason
-- activity_log (050), notifications (054) and file_search_deletions (053)
-- are already correct for every write path: they are triggers.
--
-- The outbox was the last handler-written one. What that cost:
--
--   * The MCP asset tools write the assets row directly and enqueued
--     NOTHING, so an asset created by a family agent left no outbox event.
--   * "Every bill change emits bill.updated" was FALSE. assets_propagate_
--     property() (038) UPDATEs bills.property_id when an asset moves
--     property. That UPDATE went through no handler, so the bills it moved
--     changed with no event -- a consumer could not know they had.
--   * Anyone consuming the outbox saw a partial history and had no way to
--     tell it was partial.
--
-- THE FIX. One trigger function, log_outbox_event(), in the shape the
-- codebase has already proved with log_activity(kind): AFTER INSERT OR
-- UPDATE OR DELETE, FOR EACH ROW, attached by CREATE TRIGGER to the tables
-- that declare events. Every hand-written INSERT goes, so there is ONE place
-- an event is produced -- not a trigger beside the old code, which would
-- double every REST write.
--
-- WHY A TRIGGER AND NOT A SHARED GO HELPER. The event is derivable from the
-- changed row alone: which table, which operation, and the columns already
-- on it. That is the test #5314 set. A shared helper would still depend on
-- every caller remembering to call it, which is the bug being fixed.
--
-- WHAT IT COVERS, STATED SO NOBODY HAS TO GUESS (F23). bills and assets are
-- the two entities whose events were already being emitted; they now cover
-- every row change from every path. The other 23 activity_log kinds --
-- notes, files, lists, maintenance_tasks, properties, people, secrets and
-- the rest -- emit NOTHING, deliberately: no consumer asks for them, and
-- activity_log is the complete per-entity log. This table is a delivery
-- queue for two entities, not a second audit log; if a third entity needs
-- events, attach this same function to it, do not write an INSERT. Recorded
-- in architecture/database-schema.md.
--
-- EVENT SHAPES ARE UNCHANGED. Both were already shipping, and changing a
-- payload is a compatibility decision, not this mechanism change:
--
--   bill    bill.created  {id, name}
--           bill.updated  {id, name}                     (+ paid_date when set)
--           bill.deleted  {id, name}
--   asset   asset.created {asset_id, name, category, household_id}
--           asset.updated {asset_id, name, category, household_id}
--
-- Keys come from the row's jsonb, so one function serves both tables with no
-- per-table column references. paid_date is included only when the row has
-- one: that is what bill_pay used to add by hand.
--
-- DELETES. bills emit bill.deleted from the trigger (it was hand-written in
-- two places before). assets emit nothing on delete: neither the API's asset
-- Delete nor the MCP asset tools ever wrote an asset.deleted, and inventing
-- one here would be a new contract rather than the same one from a new
-- place. Attaching assets to INSERT OR UPDATE only is what says that.
--
-- NO-OP UPDATES still enqueue, and that is deliberate: the API's bill Update
-- always ran its UPDATE and always inserted the row, so this keeps existing
-- behaviour rather than quietly changing it. It differs from log_activity,
-- which skips a no-op; the outbox is a delivery signal, not an audit line.
--
-- NO BACKFILL. outbox_events is a log of what happened when it happened;
-- replaying history would hand a future consumer events it never saw,
-- stamped with times it never had. Nothing is waiting on the missing rows:
-- no non-test code SELECTs from outbox_events (the worker's "polling
-- outbox..." log line is stale wording -- generateNotifications queries bills
-- and maintenance_tasks directly). The rows that are missing are MCP-created
-- assets before this migration and bills moved by assets_propagate_property();
-- both were covered by activity_log all along.
--
-- Runs in one transaction.

BEGIN;

-- AFTER INSERT OR UPDATE OR DELETE on a family-owned table, so the enqueue
-- lives in the caller's transaction: a write that rolls back leaves no
-- event, and an event can never be enqueued for a write that did not happen.
--
-- TG_ARGV[0] is the event prefix ('bill', 'asset'), the same argument style
-- log_activity takes its kind in.
CREATE OR REPLACE FUNCTION log_outbox_event() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    v_kind      TEXT := TG_ARGV[0];
    v_row       JSONB;
    v_household UUID;
    v_payload   JSONB;
BEGIN
    IF TG_OP = 'DELETE' THEN
        v_row := to_jsonb(OLD);
    ELSE
        v_row := to_jsonb(NEW);
    END IF;

    -- The household, from the row itself. An insert would fail the FK if it
    -- were gone, turning a harmless write into an error, so a row whose
    -- household is disappearing is skipped and the write still succeeds --
    -- the guard log_activity (050) uses for the same reason.
    --
    -- This is also the delete-cascade guard. Both tables cascade only from
    -- households, and by the time the cascade fires that household row is
    -- gone, so this returns NULL for it. activity_container_gone() (050) is
    -- not needed and is not called: bills.property_id and bills.asset_id are
    -- ON DELETE SET NULL (038), so a deleted property or asset UPDATEs its
    -- bills rather than deleting them, and no trigger in this schema deletes
    -- bills or assets.
    v_household := (v_row ->> 'household_id')::uuid;
    IF v_household IS NULL OR NOT EXISTS (SELECT 1 FROM households WHERE id = v_household) THEN
        RETURN NULL;
    END IF;

    -- Payload from the row's own jsonb: no column reference, so the one
    -- function serves every table it is attached to.
    IF v_kind = 'asset' THEN
        v_payload := jsonb_build_object(
            'asset_id',     v_row ->> 'id',
            'name',         v_row ->> 'name',
            'category',     v_row ->  'category',
            'household_id', v_row ->> 'household_id'
        );
    ELSE
        v_payload := jsonb_build_object(
            'id',   v_row ->> 'id',
            'name', v_row ->> 'name'
        );
        -- bill_pay added this key by hand; a payment is still an update of
        -- the bill row, so the trigger derives it.
        IF v_row ->> 'paid_date' IS NOT NULL THEN
            v_payload := v_payload || jsonb_build_object('paid_date', v_row ->> 'paid_date');
        END IF;
    END IF;

    INSERT INTO outbox_events (household_id, type, payload)
    VALUES (
        v_household,
        v_kind || '.' || CASE TG_OP WHEN 'INSERT' THEN 'created'
                                    WHEN 'UPDATE' THEN 'updated'
                                    ELSE 'deleted' END,
        v_payload
    );
    RETURN NULL;
END;
$$;

-- bills: INSERT, UPDATE and DELETE. AFTER triggers fire in name order, so
-- this runs after every BEFORE trigger (authorship, set_updated_at in 077)
-- has settled the row, and the payload carries what was actually stored.
CREATE TRIGGER bills_outbox
    AFTER INSERT OR UPDATE OR DELETE ON bills
    FOR EACH ROW EXECUTE FUNCTION log_outbox_event('bill');

-- assets: INSERT and UPDATE only (no asset.deleted has ever existed).
CREATE TRIGGER assets_outbox
    AFTER INSERT OR UPDATE ON assets
    FOR EACH ROW EXECUTE FUNCTION log_outbox_event('asset');

COMMIT;
