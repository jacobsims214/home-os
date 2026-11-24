-- 079_notification_vocabularies.down.sql  (#5516)
--
-- Reverses 079 exactly: the link-target CHECK, the two domains, and the two
-- columns back to the VARCHARs 015 declared.
--
-- ORDER MATTERS: the columns must stop being domain-typed before their domains
-- can be dropped, and DROP DOMAIN refuses while any column still uses it.
--
-- What is lost: nothing. The vocabulary is not data -- 079 derived it from the
-- writers in the repository, not from the rows -- so a later re-up reconstructs
-- the same domains from the same source. Unlike 080's vendors.kind, there is no
-- value a person can change by hand that the down would discard: every row that
-- survives the down was already inside the vocabulary (079 refuses to run
-- otherwise), so it is inside it again on the next up.
--
-- IF EXISTS throughout: safe to run twice, and safe against a database where the
-- up failed part-way. golang-migrate can leave a transaction undone, and a
-- retried down should never fail on absence.
--
-- NOT NULL and DEFAULT are restored explicitly: ALTER COLUMN TYPE to a domain
-- that is VARCHAR-backed keeps them, but the down must not depend on that
-- (a future PostgreSQL, or a hand-run down against a half-applied up).
--
-- Runs in one transaction.

BEGIN;

ALTER TABLE document_links DROP CONSTRAINT IF EXISTS document_links_link_target_check;

ALTER TABLE notifications ALTER COLUMN type    TYPE VARCHAR(100) USING type::text;
ALTER TABLE notifications ALTER COLUMN channel TYPE VARCHAR(50)  USING channel::text;

ALTER TABLE notifications ALTER COLUMN type SET NOT NULL;
ALTER TABLE notifications ALTER COLUMN channel SET NOT NULL;
ALTER TABLE notifications ALTER COLUMN channel SET DEFAULT 'in_app';

DROP DOMAIN IF EXISTS notification_type;
DROP DOMAIN IF EXISTS notification_channel;

COMMIT;
