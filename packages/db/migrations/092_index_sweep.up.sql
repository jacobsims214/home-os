-- 092_index_sweep.up.sql (#5520, schema review F8, F20, F21, F22)
--
-- Index sweep only. Inventory taken from the live catalogue of 001..090 on
-- PostgreSQL 16 (pg_index/pg_attribute/pg_constraint), never from the review's
-- list; the review's findings were treated as hypotheses and each was checked.
--
-- THE COMPOSITE FKs DO NOT CLOSE THESE GAPS.
-- 091_family_composite_fks converts 18 columns to (col, household_id) FKs. The
-- premise that this hands us a child-side index for free (#5519's "check
-- afterwards before adding separate indexes") is wrong: PostgreSQL indexes the
-- REFERENCED side of a foreign key (the parent's UNIQUE (id, household_id)),
-- never the REFERENCING side. The child gets no index at all. 091 says so
-- itself and adds seven child-side indexes by hand — idx_bills_property and
-- idx_maintenance_{schedules,tasks}_{property,asset,vehicle} — which is the
-- proof: had the constraint provided them, 091 would not have needed that
-- section. Verified against the catalogue: bill_payments (bill_id,
-- household_id) -> bills still reports no usable index after 091. So every gap
-- below is real, and 091 closes none of them incidentally.
--
-- What 091 DOES change for this migration: assets.room_id becomes a composite
-- FK and is still unindexed by that (see 4), so it stays in scope.
--
-- Could not be tested on top of 091 directly: 091 is still on
-- fh/5519-composite-fks, not merged into feat/family-hub, so the up→down→up
-- proof below runs on 001..090. 092 touches no object 091 alters except
-- assets.room_id's constraint (and only its index, not the constraint), so the
-- two compose.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. memberships.user_id — the login path (F21), the one that is not a nicety
-- ---------------------------------------------------------------------------
-- memberships' only other index is UNIQUE (household_id, user_id), which leads
-- with household_id, so "which families does this user belong to" scans every
-- membership row in the database. Read on every authenticated request:
--   apps/mcp/internal/auth/auth.go:459
--     SELECT household_id::text, role::text FROM memberships
--      WHERE user_id = $1 ORDER BY created_at ASC, id ASC LIMIT 1
--   apps/calendar/internal/db/repo.go:119      (ListHouseholdIDsForUser)
--   apps/calendar/internal/db/access.go:114    (ListMembershipsForUser)
-- Those two ORDER BY created_at ASC, household_id ASC. A single-column
-- (user_id) index leaves a sort of that result. It is left single-column
-- deliberately: a user belongs to a handful of families, so the sort is over a
-- few rows, and widening the index to (user_id, created_at, household_id) would
-- be write cost on every membership change for no measurable gain.
CREATE INDEX idx_memberships_user ON memberships (user_id);

-- ---------------------------------------------------------------------------
-- 2. household_id leading column on the omitted family tables (F20)
-- ---------------------------------------------------------------------------
-- The live catalogue confirms exactly eight tables with a household_id column
-- and no index leading with it, so the review's list is accurate. Two of the
-- eight are NOT indexed here, on purpose; see 6. Only these six are read as
-- "this family's rows".

-- pets: apps/mcp/internal/pet/find.go:51
--   FROM pets p ... WHERE p.household_id = $1
--    ... ORDER BY lower(p.name), p.created_at, p.id
-- lower(name) is the first sort key, so it belongs in the index: the same shape,
-- for the same reason, as idx_guides_household_title and
-- idx_recipes_household_title.
CREATE INDEX idx_pets_household_name ON pets (household_id, lower(name));

-- vehicles: apps/mcp/internal/vehicle/vehicle.go:62 (vehicleSelect), also
-- apps/mcp/internal/vehicle/access.go:349 and apps/mcp/internal/guides/tools.go:739.
--   FROM vehicles v WHERE v.household_id = $1 ...
-- The review suggested (household_id, name) "since household + order-by-name is
-- the list query". vehicles has NO name column — it is make/model/year — and the
-- list orders by lower(make), lower(model), year DESC. A column index cannot
-- serve those expression and descending keys, so this is the filter alone.
CREATE INDEX idx_vehicles_household ON vehicles (household_id);

-- agent_tokens: apps/api/internal/agenttoken/repo.go:72
--   FROM agent_tokens WHERE household_id = $1 AND user_id = $2
--    ORDER BY created_at DESC, id
-- idx_agent_tokens_user leads with user_id, so it cannot serve this.
CREATE INDEX idx_agent_tokens_household ON agent_tokens (household_id);

-- invitations: apps/api/internal/invite/repo.go:59
--   FROM invitations WHERE household_id = $1 ORDER BY created_at
CREATE INDEX idx_invitations_household ON invitations (household_id);

-- meal_feedback: apps/api/internal/meals/repo.go:534
--   DELETE FROM meal_feedback
--    WHERE household_id = $1 AND meal_plan_id = $2 AND person_id = $3
-- The delete path. meal_feedback_meal_person_key leads with meal_plan_id and
-- idx_meal_feedback_person with person_id, so neither serves a household filter.
CREATE INDEX idx_meal_feedback_household ON meal_feedback (household_id);

-- guide_revisions: apps/api/internal/guides/repo.go:312
--   SELECT body FROM guide_revisions
--    WHERE id = $1 AND guide_id = $2 AND household_id = $3
-- household_id is the third predicate here, not the leading one, so this is the
-- weakest of the six: the row is already found by primary key and household_id
-- only confirms it. It is indexed because guide_revisions is an unbounded
-- per-guide revision history carrying a household FK, and leaving one of the
-- eight named tables out for a predicate it does carry would be inconsistent.
-- If a later review wants to trim index count, this is the first candidate.
CREATE INDEX idx_guide_revisions_household ON guide_revisions (household_id);

-- ---------------------------------------------------------------------------
-- 3. calendar_objects (entity_type, entity_id) — the one polymorphic table
--    without one (F8)
-- ---------------------------------------------------------------------------
-- files, notes, secrets and lists all have idx_<table>_entity; calendar_objects
-- alone does not, so the mirror reverse query ("what is on the calendar about
-- this record") reads every event in the instance:
--   apps/api/internal/calendar/repo.go:695
--     FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id
--      WHERE c.household_id = $1 AND co.entity_id = $2
--        AND co.entity_type = 'maintenance_task'
--   apps/api/internal/calendar/repo.go:776  (the DELETE ... USING form)
-- The shape matches idx_files_entity exactly: (entity_type, entity_id), no
-- predicate.
CREATE INDEX idx_calendar_objects_entity ON calendar_objects (entity_type, entity_id);

-- ---------------------------------------------------------------------------
-- 4. assets.room_id — "assets in this room" (F21, re-checked after 091)
-- ---------------------------------------------------------------------------
-- 091 turns assets.room_id into a composite FK to rooms (id, household_id) and
-- does not add an index for it (its seven indexes cover bills and maintenance
-- only), so the gap survives the FK conversion. Read as a filter:
--   apps/mcp/internal/asset/find.go:92      AND ($7::uuid IS NULL OR a.room_id = $7)
--   apps/api/internal/property/room.go:127  SELECT count(*) FROM assets WHERE room_id = $1
--   apps/mcp/internal/property/find.go:73   (SELECT count(*) FROM assets a WHERE a.room_id = r.id)
-- Named idx_assets_room to match the existing idx_assets_property beside it.
CREATE INDEX idx_assets_room ON assets (room_id);

-- ---------------------------------------------------------------------------
-- 5. Exact duplicate indexes on the auth tables (F22)
-- ---------------------------------------------------------------------------
-- Both pairs were verified in the catalogue as identical in key column, opclass,
-- collation, access method and having no predicate, differing only in
-- uniqueness. Postgres uses a UNIQUE btree for every lookup the plain one serves,
-- so the plain index is entirely redundant and costs a second index write on
-- every password reset and every family invitation.
--   password_reset_tokens: password_reset_tokens_token_key UNIQUE (token)  [030]
--                        + idx_password_reset_tokens_token    (token)        [030]
--   invitations:           invitations_token_key           UNIQUE (token)  [029]
--                        + idx_invitations_token            (token)        [029]
-- The UNIQUE indexes are KEPT — they enforce the constraints.
DROP INDEX idx_password_reset_tokens_token;
DROP INDEX idx_invitations_token;

COMMIT;

-- ---------------------------------------------------------------------------
-- 6. Deliberately NOT added (recorded so the next review does not re-propose it)
-- ---------------------------------------------------------------------------
-- outbox_events.household_id and file_search_deletions.household_id are the two
-- of the review's eight that are not indexed here. Both are queues, and an index
-- nobody reads is a write cost on the highest-write tables in the schema.
--
--   outbox_events — append-only log, no reader selects by household.
--     088_outbox_trigger.up.sql:72 says it outright: "no non-test code SELECTs
--     from outbox_events". Its only access path is the drain predicate, already
--     served by idx_outbox_unprocessed (created_at) WHERE processed_at IS NULL
--     AND failed_at IS NULL. An index here would be maintained on every write in
--     the system and read by nothing.
--
--   file_search_deletions — the worker drains it in deleted_at order, not by
--     family: apps/worker/internal/files/repo.go:366
--       SELECT file_id, household_id, deleted_at FROM file_search_deletions
--        ORDER BY deleted_at, file_id LIMIT $1
--     It has no FOREIGN KEY to households at all (only the PK on file_id), so
--     there is not even a cascade to justify one. household_id is carried for
--     the search engine's benefit, never filtered on.
--
-- Also not added:
--   * No index on any created_by / updated_by / invited_by / edited_by
--     provenance column. 77 FK columns lack an index and most are correctly
--     unindexed write-only provenance.
--   * calendar_objects.superseded_by is SELECTed (apps/mcp/internal/calendar/
--     tools.go:573) but never filtered or joined on, so it gets no index.
--   * No index on people.user_id or notifications.user_id: both are only ever
--     read alongside household_id, which already leads people_household_user_key
--     and idx_notifications_household.
