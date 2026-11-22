-- 079_notification_vocabularies.up.sql  (#5516, schema review F10 + F3)
--
-- Two unconstrained vocabularies, one defect shape: a string column standing
-- in for an enum, with nothing in the schema saying what the enum is.
--
-- 1. notifications.type and notifications.channel (F10). 015 declared both as
--    bare VARCHAR. 070 gave notifications.entity_type the entity_kind domain
--    but left `type` -- which decides what the notification IS -- free text.
--    Proven on the live catalogue at 078:
--
--      INSERT ... type = 'bill_du'            -> ACCEPTED
--      INSERT ... channel = 'carrier_pigeon'  -> ACCEPTED
--      INSERT ... type = ''                   -> ACCEPTED
--
--    Why it matters: a notification whose type is misspelled is delivered, is
--    unreadable by every client that filters on type (the MCP notification_find
--    `type` filter, apps/mcp/internal/notification/find.go), and cannot be
--    repaired by a query because nothing lists the types that exist -- they
--    lived only in Go string literals and three SQL functions. Worse,
--    idx_notifications_item_changed is a partial index hard-coded on the
--    literal 'item_changed', so a typo'd type both escapes the constraint and
--    drops out of its own index.
--
-- 2. document_links.link_type (F3) was described in the review as having no
--    constraint. It HAS one -- document_links_link_type_check, added by 056
--    (056_domain_v2_cleanup.up.sql:262) -- and enforce_entity_owner() (038)
--    already refuses a non-UUID or missing link_id for link_type in
--    ('file','secret'). Probes at 078:
--
--      link_type='bogus'                       -> REJECTED (056's check)
--      link_type='file', link_id='not-a-uuid'  -> REJECTED (038's trigger)
--      link_type='file', link_id NULL          -> REJECTED (038's trigger)
--      link_type='url',  url NULL              -> ACCEPTED  <-- the real gap
--
--    So this half of the ticket is one genuine hole: the url-vs-link_id half of
--    the polymorphic pair is unchecked. A 'url' link with no url is a row that
--    renders as a blank entry. The CHECK below closes it, and restates the
--    file/secret rule as a constraint so it holds even if the trigger is ever
--    dropped -- a row-shape rule belongs in a CHECK, not in a tenancy trigger.
--
-- THE SHAPE: domains, following entity_kind (070) rather than vendor_kind (080).
-- Both precedents are in the tree and both are legitimate; the difference is
-- whether the vocabulary is expected to grow.
--
--   * vendor_kind (080) is a native enum for a CLOSED three-way discriminator
--     ("what is this vendors row for?") that is conceptually complete.
--   * entity_kind (070) is a VARCHAR + CHECK(VALUE IN ...) that every migration
--     which adds a kind re-declares in full -- 038, 044, 046, 047, 049, 070.
--
-- notifications.type is a GROWING vocabulary: every new background job that
-- notifies adds one (bill_due, bill_digest, maintenance_due, calendar_alarm and
-- item_changed today; the bill_digest case was added in 081 two migrations ago).
-- Adding a member of a native enum is `ALTER TYPE ... ADD VALUE`, which cannot
-- be used in the same transaction that adds it, and every migration file in this
-- repo runs in one transaction (080's header says so). A domain's CHECK is
-- extended with DROP CONSTRAINT / ADD CONSTRAINT, which is the idiom this tree
-- already uses five times. So: a domain, for both columns.
--
-- THE VOCABULARY IS THE MEASURED ONE, not a guess from migration text. Every
-- writer of notifications.type in the repository:
--
--   bill_due         packages/billdue/policy.go NotificationTypeBill
--                    -> apps/worker/cmd/worker/billreminders.go:237
--   bill_digest      packages/billdue/policy.go NotificationTypeDigest
--                    -> apps/worker/cmd/worker/billreminders.go:277
--   maintenance_due  apps/worker/cmd/worker/main.go:200 (bare SQL literal --
--                    there is no Go constant for it; see below)
--   calendar_alarm   apps/worker/internal/alarms/alarms.go NotificationType
--   item_changed     SQL only: notify_entity_creator() (054, 061, 070)
--
-- and every writer of notifications.channel omits the column (DEFAULT 'in_app')
-- or writes the literal 'in_app' (054, 061, 070). No Go code sets channel
-- anywhere. The delivery layer (apps/api/internal/notification/service.go) sends
-- an email as a side effect of an in_app row for bill_due/maintenance_due; it
-- never writes a row with channel='email'. So 'email' is deliberately NOT in the
-- vocabulary: nothing writes it, and inventing it from the prose would be the
-- same mistake this migration exists to stop. A real second channel is a one-line
-- re-declaration here when a writer appears.
--
-- ROWS THAT CANNOT SATISFY THE VOCABULARY: report, then refuse. No silent
-- rewrite (078's policy, and this ticket's own instruction: "a notification with
-- an unknown type should be investigated, not silently rewritten"). A type
-- nobody can name is a bug in a writer, and rewriting 'bill_du' to 'bill_due'
-- would be a guess written into the record; deleting the row would throw away a
-- notification the family may still need. So section 1 RAISEs every
-- out-of-vocabulary value with its row count and stops, changing nothing, with
-- the repair command in the message. A database restored from an older dump gets
-- the exact values to add or fix.
--
-- LOCKING: the validation scan must not hold ACCESS EXCLUSIVE on a growing
-- table. Each column gets a CHECK ... NOT VALID, then VALIDATE CONSTRAINT
-- (SHARE UPDATE EXCLUSIVE -- reads and writes continue), and only then
-- ALTER COLUMN TYPE. PG16 proves the domain's own constraint from the validated
-- table check and skips its own scan (078 measured 254 ms vs 783 ms on 400k
-- rows). The pre-check constraint is dropped afterwards so the domain is the
-- single source of truth.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Report anything that cannot be constrained, then refuse
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    v_bad   TEXT;
    v_n     BIGINT;
BEGIN
    SELECT string_agg(format('%s x%s', quote_literal(t), c), ', ' ORDER BY t)
      INTO v_bad
      FROM (SELECT type::text AS t, count(*) AS c
              FROM notifications
             GROUP BY 1
            HAVING type::text NOT IN ('bill_due', 'bill_digest', 'maintenance_due',
                                      'calendar_alarm', 'item_changed')) s;
    IF v_bad IS NOT NULL THEN
        SELECT count(*) INTO v_n FROM notifications
         WHERE type::text NOT IN ('bill_due', 'bill_digest', 'maintenance_due',
                                  'calendar_alarm', 'item_changed');
        RAISE EXCEPTION
            '079: % notification(s) have a type outside the vocabulary: %', v_n, v_bad
            USING HINT = 'A type nobody can name is a bug in a writer. Add it to '
                         'notification_type below if it is real, or repair the row '
                         'to the type it meant, then re-run. Nothing has been changed. '
                         'Inspect with: SELECT type, count(*) FROM notifications GROUP BY 1 ORDER BY 1;',
                  ERRCODE = 'check_violation';
    END IF;
    RAISE NOTICE '079: notifications.type -- every row is in the vocabulary';

    SELECT string_agg(format('%s x%s', quote_literal(t), c), ', ' ORDER BY t)
      INTO v_bad
      FROM (SELECT channel::text AS t, count(*) AS c
              FROM notifications
             GROUP BY 1
            HAVING channel::text <> 'in_app') s;
    IF v_bad IS NOT NULL THEN
        SELECT count(*) INTO v_n FROM notifications WHERE channel::text <> 'in_app';
        RAISE EXCEPTION
            '079: % notification(s) have a channel nothing delivers: %', v_n, v_bad
            USING HINT = 'No writer in the repository sets a channel other than '
                         'in_app, so a row with one was written by hand or by a '
                         'retired path. Add it to notification_channel below if it '
                         'is real, then re-run. Nothing has been changed. '
                         'Inspect with: SELECT channel, count(*) FROM notifications GROUP BY 1 ORDER BY 1;',
                  ERRCODE = 'check_violation';
    END IF;
    RAISE NOTICE '079: notifications.channel -- every row is in the vocabulary';
END $$;

-- The document_links rows the new link_target check would refuse. Counted
-- separately so the message names which half of the pair is wrong.
DO $$
DECLARE
    v_n BIGINT;
BEGIN
    SELECT count(*) INTO v_n FROM document_links
     WHERE link_type = 'url' AND (url IS NULL OR btrim(url) = '');
    IF v_n > 0 THEN
        RAISE EXCEPTION
            '079: % document_links row(s) are link_type=url with no url', v_n
            USING HINT = 'A url link with no url renders as a blank row. Repair the '
                         'row (set url) or delete it, then re-run. Nothing has been '
                         'changed. Inspect with: SELECT id, entity_type, entity_id FROM document_links WHERE link_type = ''url'' AND (url IS NULL OR btrim(url) = '''');',
                  ERRCODE = 'check_violation';
    END IF;

    SELECT count(*) INTO v_n FROM document_links
     WHERE link_type IN ('file', 'secret')
       AND (link_id IS NULL
            OR link_id !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$');
    IF v_n > 0 THEN
        RAISE EXCEPTION
            '079: % document_links row(s) are link_type=file/secret with a link_id that is not a UUID', v_n
            USING HINT = 'enforce_entity_owner() has refused these since 038, so a '
                         'row like this was written with the trigger disabled or '
                         'restored from an older dump. Repair or delete it, then '
                         're-run. Nothing has been changed.',
                  ERRCODE = 'check_violation';
    END IF;
    RAISE NOTICE '079: document_links -- every row satisfies the link target rule';
END $$;

-- ---------------------------------------------------------------------------
-- 2. The vocabularies
-- ---------------------------------------------------------------------------

CREATE DOMAIN notification_type AS VARCHAR(100) CHECK (VALUE IN (
    -- packages/billdue/policy.go: a single reminder, and the one digest that
    -- stands in for a burst of them (081).
    'bill_due', 'bill_digest',
    -- apps/worker/cmd/worker/main.go: the maintenance due job.
    'maintenance_due',
    -- apps/worker/internal/alarms: an iCal VALARM reminder.
    'calendar_alarm',
    -- notify_entity_creator() (054): someone else changed what you created.
    'item_changed'
));

COMMENT ON DOMAIN notification_type IS
    'What a notification IS. The closed list of types any writer emits; a type not in it is a bug, not a new feature. #5516';

CREATE DOMAIN notification_channel AS VARCHAR(50) CHECK (VALUE IN (
    -- The only channel any writer sets, and 015's DEFAULT.
    'in_app'
));

COMMENT ON DOMAIN notification_channel IS
    'How a notification reaches its person. in_app is the only channel written; the API sends email as a side effect of an in_app bill_due/maintenance_due row rather than as a channel of its own. #5516';

-- ---------------------------------------------------------------------------
-- 3. Convert the two columns, without an ACCESS EXCLUSIVE validation scan
-- ---------------------------------------------------------------------------

-- The pre-checks carry the vocabulary so VALIDATE does the scan under SHARE
-- UPDATE EXCLUSIVE. They are dropped at the end of this section: the domain is
-- the single source of truth, and two copies could drift.
ALTER TABLE notifications ADD CONSTRAINT notifications_type_vocabulary
    CHECK (type::text IN ('bill_due', 'bill_digest', 'maintenance_due',
                          'calendar_alarm', 'item_changed')) NOT VALID;
ALTER TABLE notifications VALIDATE CONSTRAINT notifications_type_vocabulary;

ALTER TABLE notifications ADD CONSTRAINT notifications_channel_vocabulary
    CHECK (channel::text IN ('in_app')) NOT VALID;
ALTER TABLE notifications VALIDATE CONSTRAINT notifications_channel_vocabulary;

ALTER TABLE notifications ALTER COLUMN type    TYPE notification_type;
ALTER TABLE notifications ALTER COLUMN channel TYPE notification_channel;

ALTER TABLE notifications DROP CONSTRAINT notifications_type_vocabulary;
ALTER TABLE notifications DROP CONSTRAINT notifications_channel_vocabulary;

COMMENT ON COLUMN notifications.type IS
    'What this notification is (notification_type). The partial index idx_notifications_item_changed and the 10-minute dedupe in notify_entity_creator() both key on item_changed. #5516';
COMMENT ON COLUMN notifications.channel IS
    'How it reaches its person (notification_channel). DEFAULT in_app, the only value any writer sets. #5516';

-- ---------------------------------------------------------------------------
-- 4. document_links: the url-vs-link_id half of the polymorphic pair
-- ---------------------------------------------------------------------------
--
-- The row is TWO links with different guarantees. entity_type/entity_id (what
-- the link hangs off) is entity_kind plus the same-family trigger. link_type/
-- link_id (what the link IS) had 056's vocabulary check but no relation to the
-- two columns that hold its target, so a 'url' link could carry no url and a
-- 'file' link a link_id that is not a UUID.
--
-- A real foreign key needs the table split into one FK per target type; the
-- CHECK plus the existing same-family trigger in enforce_entity_owner() is the
-- right shape (the ticket says so, and it is right). This is the CHECK half.
--
-- NOT VALID then VALIDATE for the same locking reason: document_links grows with
-- every attachment.
ALTER TABLE document_links ADD CONSTRAINT document_links_link_target_check
    CHECK (
        CASE link_type
            -- An external resource: the url is the target, link_id is unused.
            WHEN 'url' THEN url IS NOT NULL AND btrim(url) <> ''
            -- A record in this family: link_id is its UUID, and the trigger
            -- resolves it to prove it is same-family and still there. url is
            -- not the target, but it is allowed to carry a cached display href.
            WHEN 'file' THEN link_id IS NOT NULL
                              AND link_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
            WHEN 'secret' THEN link_id IS NOT NULL
                              AND link_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
            -- Any other link_type: this constraint has nothing to say. The
            -- VOCABULARY is 056's document_links_link_type_check, and 056's down
            -- drops it; a test that walks the schema below 056 (and the 056
            -- backfill test does) inserts legacy rows like link_type='paperless'
            -- in exactly that window, which must keep working. Re-asserting
            -- 056's list here would make 079 the owner of a decision it did not
            -- make, and would break the down-walk that proves 056 reversible.
            ELSE TRUE
        END
    ) NOT VALID;
ALTER TABLE document_links VALIDATE CONSTRAINT document_links_link_target_check;

COMMENT ON CONSTRAINT document_links_link_target_check ON document_links IS
    'link_type decides which column is the target: url requires a non-blank url, file/secret require a UUID link_id (resolved same-family by enforce_entity_owner). #5516';

-- end 079

COMMIT;
