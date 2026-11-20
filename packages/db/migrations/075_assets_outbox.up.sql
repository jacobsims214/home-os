-- 075_assets_outbox.up.sql (#5320)
--
-- Every asset write must enqueue its outbox event, whoever the caller is.
--
-- Until now "asset.created" / "asset.updated" were INSERTed into
-- outbox_events by hand, in apps/api/internal/asset/repo.go (Create and
-- Update). The MCP tool layer (apps/mcp/internal/asset/write.go) writes the
-- assets row itself and never enqueued anything, so an asset created by a
-- family agent left no outbox event at all: work the family asked for that
-- silently does not happen downstream. #5314's write-path audit found this
-- as the last handler-code side-effect (KB
-- architecture/write-paths-and-side-effects.md); activity_log (050),
-- notifications (054) and file deletions (053) were already correct for both
-- paths because they are triggers.
--
-- So the enqueue moves into a trigger, and the two hand-written INSERTs are
-- deleted from the api repo (this is the ONE place it now happens -- not a
-- third copy beside them, which would double every REST write).
--
-- The event is derivable from the asset row alone, which is what makes a
-- trigger the right mechanism rather than a shared Go helper:
--
--   type    'asset.created' on INSERT, 'asset.updated' on UPDATE -- exactly
--           the two types the api repo wrote, so consumers see no new shape.
--   payload {asset_id, name, category, household_id}, the same four keys the
--           api repo built (asset_id and household_id as canonical UUID text,
--           category null when the asset has none).
--
-- An UPDATE enqueues an event even when only updated_at moved. That is what
-- the api repo did before this migration (its Update always ran the UPDATE
-- and always inserted the row), so the trigger keeps existing behaviour
-- rather than quietly changing it. An UPDATE caused by an FK cascade -- a
-- deleted room setting assets.room_id to NULL (038) -- enqueues too, which
-- matches activity_log (050), where such a change is already logged as an
-- asset update.
--
-- Deletes enqueue nothing, as before: the api's Delete never wrote an event
-- and this migration does not add one. The asset's documents are re-homed
-- and its upkeep deleted by assets_rehome_entity_refs (038).
--
-- Existing rows: assets created through the MCP before this migration have
-- no 'asset.created' event and are not backfilled here. outbox_events is a
-- log of what happened when it happened, and replaying history with
-- created_at values from the past would hand a future consumer events it
-- never saw. Nothing reads outbox_events today (no consumer in apps/, the
-- worker does not poll it), so nothing is waiting on those rows. A backfill
-- is therefore not needed; if one is ever wanted, the MCP-created rows are
-- identifiable through activity_log.actor_agent_token_id (050, #4491). See KB
-- architecture/write-paths-and-side-effects.md.
--
-- Runs in one transaction.

BEGIN;

-- AFTER INSERT OR UPDATE on the asset row, so the enqueue lives in the
-- caller's transaction: a write that rolls back leaves no event, and an
-- event can never be enqueued for a write that did not happen.
CREATE OR REPLACE FUNCTION assets_enqueue_outbox() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    -- Same guard as activity_log (050): if the household is gone from the
    -- snapshot, an insert would fail the FK and turn a harmless write into
    -- an error. Nothing is enqueued and the write still succeeds.
    IF NOT EXISTS (SELECT 1 FROM households WHERE id = NEW.household_id) THEN
        RETURN NULL;
    END IF;

    INSERT INTO outbox_events (household_id, type, payload)
    VALUES (
        NEW.household_id,
        CASE TG_OP WHEN 'INSERT' THEN 'asset.created' ELSE 'asset.updated' END,
        jsonb_build_object(
            'asset_id',     NEW.id::text,
            'name',         NEW.name,
            'category',     NEW.category,
            'household_id', NEW.household_id::text
        )
    );
    RETURN NULL;
END;
$$;

COMMENT ON FUNCTION assets_enqueue_outbox() IS
    'Enqueues asset.created / asset.updated in outbox_events for every asset write, whatever the caller (#5320).';

DROP TRIGGER IF EXISTS assets_enqueue_outbox ON assets;
CREATE TRIGGER assets_enqueue_outbox
    AFTER INSERT OR UPDATE ON assets
    FOR EACH ROW EXECUTE FUNCTION assets_enqueue_outbox();

COMMIT;
