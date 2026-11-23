-- 093_households_default_property_family_fk.down.sql (#5521)
--
-- Restores exactly the constraint 017 created, read from pg_constraint at 092
-- before the change: a single-column FK on default_property_id with
-- ON DELETE SET NULL (confdeltype 'n'), NO ACTION on update ('a'), MATCH SIMPLE
-- ('s'). Same constraint name.
--
-- The single-column form is what 038 documents and what this schema had for 90
-- migrations; restoring it restores the cross-family hole by definition, which
-- is the point of a down migration. No data is touched: rows written under 093
-- are a subset of the rows this form accepts, so they all satisfy the weaker
-- constraint and the ALTER cannot fail on validity.
--
-- The column-list form of SET NULL is not valid for a one-column key, so the
-- plain form is used here, matching 017's original text.
--
-- Runs in one transaction.

BEGIN;

ALTER TABLE households
    DROP CONSTRAINT households_default_property_id_fkey;

ALTER TABLE households
    ADD CONSTRAINT households_default_property_id_fkey
    FOREIGN KEY (default_property_id)
    REFERENCES properties (id)
    ON DELETE SET NULL;

COMMIT;
