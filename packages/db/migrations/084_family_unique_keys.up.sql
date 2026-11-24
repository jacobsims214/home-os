-- 084_family_unique_keys.up.sql (#5512, schema review F4 step 1 of 2)
--
-- A composite foreign key of the form
--
--     FOREIGN KEY (col, household_id) REFERENCES parent (id, household_id)
--
-- requires the PARENT side to have a unique constraint over exactly
-- (id, household_id). 058 established that pattern and converted 15
-- relationships to it, but it could only convert those whose parent already
-- had the key — for the rest a composite FK is *impossible*, which is why 058
-- skipped them. This migration adds the missing keys; #5519 then converts the
-- 15 stray single-column FKs on top.
--
-- The 12 tables below were found by querying the live catalogue after 082, not
-- by reading migration text: tables in schema public with a household_id
-- column and no unique index over exactly (id, household_id). Two tables from
-- the review's list are NOT here, for reasons checked against that same
-- catalogue:
--
--   secrets          already has secrets_id_household_id_key, added by 075
--                    (payment_accounts.secret_id needed it first). Adding it
--                    again would only create a second, redundant index.
--   calendar_objects has no household_id column at all (013): it is scoped to
--                    a family only through calendars.calendar_id. A
--                    (id, household_id) key there would need a new column and
--                    a backfill, which is a different change from this one;
--                    it is flagged on the ticket rather than silently dropped.
--
-- Cost. Each UNIQUE adds one btree over (id, household_id). That key is
-- redundant over the primary key by construction — id alone is already unique
-- — so it cannot reject a row that was previously valid. It buys the composite
-- FK and nothing else. It is not free on write: every INSERT/UPDATE maintains
-- one more index. At the sizes we have (all 12 heaps are 0-8 KB here) that is
-- nothing, and it is the same price 043/044/046/058/072/075 already paid. No
-- table is skipped as already-covered: none of the 12 has any existing
-- two-column index over (id, household_id).
--
-- Two keys that look similar are deliberately left alone because they serve
-- different references:
--
--   rooms        rooms_id_property_id_key UNIQUE (id, property_id) — the
--                property-side FK and the (property_id, household_id) joins.
--                Different pair, both kept.
--   memberships  memberships_household_id_user_id_key UNIQUE
--                (household_id, user_id) — the 001 key that
--                list_items_checked_by_fkey uses. Different pair, both kept.
--
-- Naming follows 043/058/072/075 (<table>_id_household_id_key) rather than the
-- shorter <table>_id_household_key that 041/044/046 used. Both spellings exist
-- in the repo; the newer one is used so these 12 keys read alike.

BEGIN;

-- 1. The family tables that hold rows of their own and are pointed at by id.
ALTER TABLE assets
    ADD CONSTRAINT assets_id_household_id_key UNIQUE (id, household_id);

ALTER TABLE bills
    ADD CONSTRAINT bills_id_household_id_key UNIQUE (id, household_id);

ALTER TABLE calendars
    ADD CONSTRAINT calendars_id_household_id_key UNIQUE (id, household_id);

ALTER TABLE loans
    ADD CONSTRAINT loans_id_household_id_key UNIQUE (id, household_id);

ALTER TABLE maintenance_tasks
    ADD CONSTRAINT maintenance_tasks_id_household_id_key UNIQUE (id, household_id);

ALTER TABLE notes
    ADD CONSTRAINT notes_id_household_id_key UNIQUE (id, household_id);

ALTER TABLE pets
    ADD CONSTRAINT pets_id_household_id_key UNIQUE (id, household_id);

ALTER TABLE vehicles
    ADD CONSTRAINT vehicles_id_household_id_key UNIQUE (id, household_id);

ALTER TABLE rooms
    ADD CONSTRAINT rooms_id_household_id_key UNIQUE (id, household_id);

ALTER TABLE memberships
    ADD CONSTRAINT memberships_id_household_id_key UNIQUE (id, household_id);

ALTER TABLE guide_revisions
    ADD CONSTRAINT guide_revisions_id_household_id_key UNIQUE (id, household_id);

-- document_links is a join row; it is family-scoped and referenced by id, so it
-- needs the same key as the rest.
ALTER TABLE document_links
    ADD CONSTRAINT document_links_id_household_id_key UNIQUE (id, household_id);

COMMIT;
