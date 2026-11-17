-- 071_money_provenance.up.sql (#4941)
--
-- Make money recorded from a person's own text able to prove what happened.
--
-- War game R1 (KB discoveries/war-game-r1-amber.md, gap G1): a parent pasted
-- "Well visit copay — $35.00", the agent filed a bill of 5.00 and a document
-- whose body was re-typed as "Well visit copay — 5.00". A verification turn
-- then compared the bill, the document and the description, found them all
-- saying $5, and reported "nothing to reconcile". Nothing in storage held the
-- original text, so there was nothing to reconcile against — and the family
-- was told the record was verified.
--
-- Three things are missing for that not to happen again:
--
--   1. files.text_origin / files.text_sha256 — where a document's text came
--      from, and a fingerprint of it. A text document uploaded from a paste
--      (the MCP document_upload content_text path) is the SOURCE, not an
--      extraction: text_origin 'raw' says so, and text_sha256 is the SHA-256
--      of the exact bytes stored. Re-hashing the stored text later and
--      comparing proves the body is still the text that was pasted — a body
--      re-typed (or edited) no longer matches, which is exactly the signal
--      R1's false "verified" lacked. Text the worker extracts from a file's
--      bytes is text_origin 'extracted' and has no fingerprint: it is
--      derived, never the source.
--   2. bills.source_document_id — the document a bill was taken from, so the
--      amount can be diffed against that document's text instead of against
--      another artifact the same mistake wrote. Same-family composite FK
--      (domain model v2): a bill can never point at another family's
--      document. ON DELETE SET NULL (source_document_id) keeps the bill when
--      the document is deleted (and that change is logged, see 3).
--   3. activity_log.changes — the before/after VALUES of an update, for the
--      columns where a value is both safe to keep and worth keeping: money
--      and dates. 050 deliberately logs column NAMES only, so that no note
--      body, document text, URL or secret ever reaches the feed; that
--      invariant stays. changes is filled from an allowlist of columns
--      (activity_change_values(), below) and is NULL for every other table
--      and column. bills.amount is on it, so the pending $5 -> $35
--      correction is a before/after pair in the family's change feed.
--
-- What this does:
--   A. files: text_origin (NOT NULL DEFAULT 'extracted', CHECK 'raw' or
--      'extracted'), text_sha256 (BYTEA, NULL unless the text is the source)
--      and UNIQUE (id, household_id) as the target of the composite FK in B.
--      Existing rows keep 'extracted': the text the worker extracted from
--      the bytes of every file uploaded so far is derived text, and the
--      handful of pasted-text rows from before this migration cannot be told
--      apart from it, so they claim nothing.
--   B. bills.source_document_id, composite same-family FK to files.
--   C. activity_log.changes JSONB + activity_change_values() and 055's
--      log_activity() with one addition: it fills changes. The summary is
--      left byte-for-byte as 050/055 build it (054/070's notifications parse
--      its " (field, ...)" suffix), so notifications read as before.
--   D. A CHECK keeps changes an object, NULL on create/delete rows, and
--      small: the values are a handful of money/date scalars.
--
-- entity_kind, entity_household() and rehome_entity_refs() are not changed:
-- files.entity_type/entity_id is untouched, and a bill's source document is
-- an ordinary column, not an entity pair.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- A. files: where the text came from, and its fingerprint
-- ---------------------------------------------------------------------------

ALTER TABLE files
    ADD COLUMN text_origin VARCHAR(20) NOT NULL DEFAULT 'extracted',
    ADD COLUMN text_sha256 BYTEA;

ALTER TABLE files
    ADD CONSTRAINT files_text_origin_check CHECK (text_origin IN ('raw', 'extracted'));

COMMENT ON COLUMN files.text_origin IS
    'Where extracted_text came from: raw (the caller''s own text, stored unaltered: the source) or extracted (read from the file''s bytes by the worker). #4941';
COMMENT ON COLUMN files.text_sha256 IS
    'SHA-256 of extracted_text when text_origin is raw, so the stored body can be proven unchanged; NULL for derived text. #4941';

-- Existing rows: ADD COLUMN with a constant default already made every one
-- 'extracted' with no hash (see the header). No UPDATE: it would log a
-- spurious "updated document" activity row per file.

-- Target of bills' same-family composite FK (B). A file id is unique on its
-- own; the pair is what makes "same family" checkable. The key's own index
-- (id, household_id) also serves the FK, so no second index is created.
ALTER TABLE files
    ADD CONSTRAINT files_id_household_id_key UNIQUE (id, household_id);

-- ---------------------------------------------------------------------------
-- B. bills.source_document_id
-- ---------------------------------------------------------------------------

ALTER TABLE bills
    ADD COLUMN source_document_id UUID,
    ADD CONSTRAINT bills_source_document_fkey
        FOREIGN KEY (source_document_id, household_id)
        REFERENCES files (id, household_id)
        ON DELETE SET NULL (source_document_id);

COMMENT ON COLUMN bills.source_document_id IS
    'The document the bill''s amount and dates were taken from (same family). Diff the amount against that document''s extracted_text. #4941';

-- "Bills filed from this document".
CREATE INDEX idx_bills_source_document ON bills (source_document_id)
    WHERE source_document_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- C. activity_log.changes: the before/after values of a few columns
-- ---------------------------------------------------------------------------

ALTER TABLE activity_log ADD COLUMN changes JSONB;

ALTER TABLE activity_log
    ADD CONSTRAINT activity_log_changes_check CHECK (
        (action = 'update' OR changes IS NULL)
        AND (changes IS NULL OR jsonb_typeof(changes) = 'object')
        -- A handful of scalars; never a body. Guards a future allowlist edit.
        AND (changes IS NULL OR octet_length(changes::text) <= 4096)
    );

COMMENT ON COLUMN activity_log.changes IS
    'Before/after VALUES of the columns an update changed, for the money and date columns of activity_change_values() only; NULL for every other change. #4941';

-- activity_change_values(table, old row, new row, changed columns) returns
-- {"amount": {"from": 5.00, "to": 35.00}, ...} for the columns of that table
-- whose values are both safe to keep and worth keeping: money, dates and one
-- id reference (bills.source_document_id). Every table and column not
-- listed returns NULL, so the 050 invariant (the log never holds record
-- values) still holds everywhere else: no note body, document text, URL,
-- address or secret value can reach the log through this function.
CREATE FUNCTION activity_change_values(p_table TEXT, p_old JSONB, p_new JSONB, p_changed TEXT[])
RETURNS JSONB
LANGUAGE plpgsql IMMUTABLE
AS $$
DECLARE
    v_cols TEXT[];
    v_col  TEXT;
    v_out  JSONB := '{}'::jsonb;
    v_n    INT := 0;
BEGIN
    IF p_changed IS NULL OR p_old IS NULL OR p_new IS NULL THEN
        RETURN NULL;
    END IF;
    v_cols := CASE p_table
        WHEN 'bills' THEN
            -- source_document_id is a reference (a UUID), never a body: it
            -- says which document the bill came from, which is the thing a
            -- correction has to be traceable to.
            ARRAY['amount', 'frequency', 'due_day', 'due_date', 'paid_date', 'paid_for_due_date',
                  'source_document_id']
        WHEN 'loans' THEN
            ARRAY['monthly_payment', 'remaining_balance', 'interest_rate', 'term_months', 'due_day', 'term_end']
        WHEN 'maintenance_tasks' THEN
            ARRAY['cost', 'status', 'due_date']
        WHEN 'budget_categories' THEN
            ARRAY['monthly_amount']
        ELSE
            NULL
    END;
    IF v_cols IS NULL THEN
        RETURN NULL;
    END IF;
    FOREACH v_col IN ARRAY v_cols LOOP
        CONTINUE WHEN NOT (v_col = ANY (p_changed));
        v_out := v_out || jsonb_build_object(v_col,
                     jsonb_build_object('from', p_old -> v_col, 'to', p_new -> v_col));
        v_n := v_n + 1;
    END LOOP;
    IF v_n = 0 THEN
        RETURN NULL;
    END IF;
    RETURN v_out;
END;
$$;

-- 055's log_activity() body, with ONE addition (marked "#4941": v_changes is
-- computed for an update and written to activity_log.changes). Nothing else
-- changes: the summary, labels, parent, actor and creator logic are 055's
-- verbatim, so the feed and the notifications built from summary read
-- exactly as they did.
CREATE OR REPLACE FUNCTION log_activity() RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_kind        TEXT := TG_ARGV[0];
    v_row         JSONB;
    v_old         JSONB;
    v_action      TEXT;
    v_changed     TEXT[] := '{}';
    v_changes     JSONB;                                    -- #4941
    v_household   UUID;
    v_parent_type TEXT;
    v_parent_id   UUID;
    v_label       TEXT;
    v_creator     UUID;
    v_summary     TEXT;
    v_raw         TEXT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        -- A delete caused by another delete is covered by the delete that
        -- caused it: one made by a trigger (rehome, the 027 maintenance
        -- sync) runs at trigger depth > 1; an FK cascade runs at depth 1
        -- (Postgres fires it from the outer statement's trigger queue), so
        -- it is recognised by its container being gone.
        v_row := to_jsonb(OLD);
        IF pg_trigger_depth() > 1 OR activity_container_gone(v_row) THEN
            RETURN NULL;
        END IF;
        v_action := 'delete';
    ELSIF TG_OP = 'INSERT' THEN
        v_action := 'create';
        v_row := to_jsonb(NEW);
    ELSE
        v_action := 'update';
        v_row := to_jsonb(NEW);
        v_old := to_jsonb(OLD);
        SELECT COALESCE(array_agg(k ORDER BY k), '{}') INTO v_changed
          FROM jsonb_object_keys(v_row) AS k
         WHERE k NOT IN ('updated_at', 'updated_by', 'created_by', 'author_id',
                         'edited_by', 'ctag', 'etag')
           AND v_row -> k IS DISTINCT FROM v_old -> k;
        IF cardinality(v_changed) = 0 THEN
            RETURN NULL;
        END IF;
        -- #4941: the values, for the allowlisted money/date columns only.
        v_changes := activity_change_values(TG_TABLE_NAME, v_old, v_row, v_changed);
    END IF;

    -- Household: the row's own, else (calendar_objects) its calendar's, else
    -- the app.household_id setting.
    v_household := (v_row ->> 'household_id')::uuid;
    IF v_household IS NULL AND v_row ? 'calendar_id' THEN
        SELECT household_id INTO v_household
          FROM calendars WHERE id = (v_row ->> 'calendar_id')::uuid;
    END IF;
    IF v_household IS NULL THEN
        v_raw := NULLIF(btrim(current_setting('app.household_id', true)), '');
        IF v_raw IS NOT NULL THEN
            BEGIN
                v_household := v_raw::uuid;
            EXCEPTION WHEN invalid_text_representation THEN
                v_household := NULL;
            END;
        END IF;
    END IF;
    -- Unknown household, or the household is being deleted (its rows go by
    -- cascade): nothing to log, and an insert would fail the FK.
    IF v_household IS NULL OR NOT EXISTS (SELECT 1 FROM households WHERE id = v_household) THEN
        RETURN NULL;
    END IF;

    -- Parent: the record this one sits in, else the record it is about.
    CASE TG_TABLE_NAME
        WHEN 'rooms'              THEN v_parent_type := 'property';  v_parent_id := (v_row ->> 'property_id')::uuid;
        WHEN 'calendar_objects'   THEN v_parent_type := 'calendar';  v_parent_id := (v_row ->> 'calendar_id')::uuid;
        WHEN 'list_items'         THEN v_parent_type := 'list';      v_parent_id := (v_row ->> 'list_id')::uuid;
        WHEN 'meal_feedback'      THEN v_parent_type := 'meal_plan'; v_parent_id := (v_row ->> 'meal_plan_id')::uuid;
        WHEN 'person_preferences' THEN v_parent_type := 'person';    v_parent_id := (v_row ->> 'person_id')::uuid;
        ELSE
            IF v_row ? 'entity_type' AND (v_row ->> 'entity_type') <> 'household' THEN
                v_parent_type := v_row ->> 'entity_type';
                v_parent_id := (v_row ->> 'entity_id')::uuid;
            END IF;
    END CASE;
    IF v_parent_type IS NULL OR v_parent_id IS NULL THEN
        v_parent_type := NULL;
        v_parent_id := NULL;
    END IF;

    -- Label: a short human name for the record. Only label columns are
    -- read, so no body, document text, URL or secret value reaches the log.
    CASE TG_TABLE_NAME
        WHEN 'vehicles' THEN
            v_label := NULLIF(concat_ws(' ', v_row ->> 'year', v_row ->> 'make', v_row ->> 'model'), '');
        WHEN 'calendar_objects' THEN
            v_label := calendar_event_label(v_row ->> 'ical_data', v_row ->> 'ical_raw');
        WHEN 'person_preferences' THEN
            v_label := concat_ws(': ', v_row ->> 'category', v_row ->> 'value');
        WHEN 'meal_feedback' THEN
            v_label := CASE WHEN v_row ->> 'rating' IS NOT NULL
                            THEN 'rating ' || (v_row ->> 'rating') ELSE 'comment' END;
        WHEN 'document_links' THEN
            v_label := COALESCE(v_row ->> 'title', v_row ->> 'link_type');
        WHEN 'secrets' THEN
            v_label := v_row ->> 'name';
        WHEN 'notes' THEN
            v_label := v_row ->> 'title';
        WHEN 'list_items' THEN
            v_label := v_row ->> 'text';
        ELSE
            v_label := COALESCE(v_row ->> 'title', v_row ->> 'name', v_row ->> 'display_name');
    END CASE;
    v_label := NULLIF(left(regexp_replace(btrim(v_label), '\s+', ' ', 'g'), 80), '');

    v_summary := CASE v_action WHEN 'create' THEN 'created ' WHEN 'update' THEN 'updated ' ELSE 'deleted ' END
              || replace(v_kind, '_', ' ')
              || COALESCE(' "' || v_label || '"', '');
    IF v_action = 'update' THEN
        v_summary := v_summary || ' (' || array_to_string(v_changed[1:5], ', ')
                  || CASE WHEN cardinality(v_changed) > 5
                          THEN ', +' || (cardinality(v_changed) - 5) || ' more' ELSE '' END
                  || ')';
    END IF;
    v_summary := left(v_summary, 300);

    -- The creator, only while that user still exists (it may be mid-delete).
    v_creator := (v_row ->> 'created_by')::uuid;
    IF v_creator IS NOT NULL AND NOT EXISTS (SELECT 1 FROM users WHERE id = v_creator) THEN
        v_creator := NULL;
    END IF;

    INSERT INTO activity_log (household_id, actor_user_id, actor_agent_token_id,
                              entity_type, entity_id, parent_type, parent_id,
                              entity_created_by, action, changed_fields, summary,
                              changes)
    VALUES (v_household, current_actor_user_id(), current_actor_agent_token_id(),
            v_kind, (v_row ->> 'id')::uuid, v_parent_type, v_parent_id,
            v_creator, v_action, v_changed, v_summary,
            v_changes);
    RETURN NULL;
END;
$$;

COMMIT;
