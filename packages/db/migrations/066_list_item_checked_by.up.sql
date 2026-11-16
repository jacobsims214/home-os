-- 066_list_item_checked_by.up.sql (#4800)
--
-- Records who checked a list item off ("Checked by Amber, 7:34 AM",
-- Lists mockup #88, decision on #4518).
--
-- list_items.checked_by: nullable users.id of the member who checked the
-- item off. Same family: the composite FK (household_id, checked_by) ->
-- memberships(household_id, user_id) (the 001 UNIQUE key) only accepts a
-- member of the item's own household. Removing that membership (or deleting
-- the user, which cascades to memberships) clears only checked_by (PG15+
-- column list on SET NULL, as in 046's assignee FK).
--
-- Filled by the trigger list_items_checked_by, so every write path (api
-- check/uncheck and item PATCH, MCP list_check_items, lists.AddItems
-- re-opening an item, template copies) behaves the same:
--   * checked false            -> checked_by NULL.
--   * becomes checked (INSERT with checked, or UPDATE from unchecked)
--                              -> the value written, else the acting user
--                                 (app.user_id, set per request by the api
--                                 and MCP pool hooks, #4491/#4492) when that
--                                 user is a member of the item's household,
--                                 else NULL ("the system", or an actor from
--                                 elsewhere such as an instance admin).
--   * stays checked            -> left as the statement leaves it (normally
--                                 the original checker), like checked_at.
-- Any member role counts, housesitters included (role policy lets them
-- check items off).
--
-- The trigger reads app.user_id itself rather than through 050's
-- current_actor_user_id(), so it keeps working in tests that undo 050.
-- Existing checked items keep checked_by NULL (unknown).
--
-- Runs in one transaction.

BEGIN;

ALTER TABLE list_items ADD COLUMN IF NOT EXISTS checked_by UUID;

ALTER TABLE list_items DROP CONSTRAINT IF EXISTS list_items_checked_by_fkey;
ALTER TABLE list_items ADD CONSTRAINT list_items_checked_by_fkey
    FOREIGN KEY (household_id, checked_by) REFERENCES memberships (household_id, user_id)
    ON DELETE SET NULL (checked_by);

-- Serves the FK check when a membership is removed.
CREATE INDEX IF NOT EXISTS idx_list_items_checked_by
    ON list_items (household_id, checked_by) WHERE checked_by IS NOT NULL;

CREATE OR REPLACE FUNCTION list_items_checked_by()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_raw   TEXT;
    v_actor UUID;
BEGIN
    IF NOT NEW.checked THEN
        NEW.checked_by := NULL;
        RETURN NEW;
    END IF;

    IF TG_OP = 'UPDATE' AND OLD.checked THEN
        RETURN NEW;
    END IF;

    IF NEW.checked_by IS NULL THEN
        v_raw := NULLIF(btrim(current_setting('app.user_id', true)), '');
        IF v_raw ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
            v_actor := v_raw::uuid;
            IF EXISTS (SELECT 1 FROM memberships m
                        WHERE m.household_id = NEW.household_id AND m.user_id = v_actor) THEN
                NEW.checked_by := v_actor;
            END IF;
        END IF;
    END IF;

    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS list_items_checked_by ON list_items;
CREATE TRIGGER list_items_checked_by
    BEFORE INSERT OR UPDATE ON list_items
    FOR EACH ROW EXECUTE FUNCTION list_items_checked_by();

COMMENT ON COLUMN list_items.checked_by IS
    'users.id of the household member who checked the item off; NULL when unchecked or unknown. Set by trigger list_items_checked_by (#4800).';

COMMIT;
