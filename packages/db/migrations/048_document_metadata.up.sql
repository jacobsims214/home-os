-- 048_document_metadata.up.sql
--
-- Stage 2: document metadata (KB decisions/domain-model-v2.md, "Contracts for
-- the Stage 2 migrations" -> 048 document metadata; ticket #4456). A file in
-- the family's document hub gets the facts people search and sort by: what
-- kind of paper it is, when it is dated, when it runs out, and who it is for.
--
-- The metadata lives on `files` itself, not in a new table: ownership
-- (household_id) and "what is this about" (entity_type/entity_id, with
-- enforce_entity_owner and rehome_entity_refs from 038) are already settled
-- there. So this migration does not touch entity_kind, entity_household() or
-- rehome_entity_refs().
--
-- Columns added to files (all nullable except category):
--   title          a human title ("2026 homeowners policy"). NULL means the
--                  UI shows files.name (the uploaded file name). Blank is
--                  rejected so "no title" has one spelling.
--   category       receipt, tax, insurance, medical, manual, warranty,
--                  school, legal, vehicle, home or other. text + CHECK (like
--                  044/046/047), not a Postgres enum, so a later migration
--                  widens it with DROP/ADD CONSTRAINT. NOT NULL DEFAULT
--                  'other': existing files become 'other' (a constant
--                  default, so Postgres fills it without rewriting the
--                  table).
--   document_date  the date on the document (receipt date, policy start,
--                  report date). Not the upload time; that is created_at.
--   expires_on     when it lapses (warranty end, policy end, passport
--                  expiry). Must not be before document_date when both are
--                  set.
--   description    free text.
--   person_id      the family member the document is for (a kid's report
--                  card, someone's medical record). Same-family composite FK
--                  to people (041), ON DELETE SET NULL (person_id): the file
--                  stays with the family when the person is removed. This is
--                  "whose document", separate from the entity subject, so a
--                  file can be about a vehicle and also be Jo's.
--
-- Indexes:
--   (household_id, category)            the hub's category filter
--   (expires_on) WHERE expires_on set   "what expires in the next 30 days"
--                                       sweeps across all families
--   (person_id) WHERE person_id set     "Jo's documents"; also serves the
--                                       FK's SET NULL on person delete
--
-- Runs in one transaction.

BEGIN;

ALTER TABLE files
    ADD COLUMN title         TEXT,
    ADD COLUMN category      TEXT NOT NULL DEFAULT 'other',
    ADD COLUMN document_date DATE,
    ADD COLUMN expires_on    DATE,
    ADD COLUMN description   TEXT,
    ADD COLUMN person_id     UUID;

ALTER TABLE files
    ADD CONSTRAINT files_category_check CHECK (category IN (
        'receipt', 'tax', 'insurance', 'medical', 'manual', 'warranty',
        'school', 'legal', 'vehicle', 'home', 'other'
    )),
    ADD CONSTRAINT files_title_not_blank CHECK (title IS NULL OR btrim(title) <> ''),
    ADD CONSTRAINT files_expires_after_document_date CHECK (
        expires_on IS NULL OR document_date IS NULL OR expires_on >= document_date
    ),
    ADD CONSTRAINT files_person_fkey FOREIGN KEY (person_id, household_id)
        REFERENCES people (id, household_id) ON DELETE SET NULL (person_id);

CREATE INDEX idx_files_household_category ON files (household_id, category);
CREATE INDEX idx_files_expires_on ON files (expires_on) WHERE expires_on IS NOT NULL;
CREATE INDEX idx_files_person ON files (person_id) WHERE person_id IS NOT NULL;

COMMIT;
