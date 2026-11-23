-- 092_index_sweep.down.sql (#5520)
--
-- Restores exactly what 092 dropped and removes exactly what it added.
--
-- The two duplicate indexes are recreated with the definition they had before
-- the drop (029_create_invitations.up.sql:13 and
-- 030_create_password_reset_tokens.up.sql:9 — plain btree, no predicate), so a
-- down-then-up cycle returns the catalogue to the 090 state and no further.
-- They carry no data; recreating them is a cheap rebuild of an index that is
-- redundant with the UNIQUE one on the same column.
--
-- Runs in one transaction.

BEGIN;

DROP INDEX IF EXISTS idx_memberships_user;

DROP INDEX IF EXISTS idx_pets_household_name;
DROP INDEX IF EXISTS idx_vehicles_household;
DROP INDEX IF EXISTS idx_agent_tokens_household;
DROP INDEX IF EXISTS idx_invitations_household;
DROP INDEX IF EXISTS idx_meal_feedback_household;
DROP INDEX IF EXISTS idx_guide_revisions_household;

DROP INDEX IF EXISTS idx_calendar_objects_entity;
DROP INDEX IF EXISTS idx_assets_room;

CREATE INDEX idx_password_reset_tokens_token ON password_reset_tokens (token);
CREATE INDEX idx_invitations_token ON invitations (token);

COMMIT;
