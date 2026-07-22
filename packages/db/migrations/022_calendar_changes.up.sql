-- Migration 022: Calendar change tracking for CalDAV sync-collection
--
-- Apple Calendar and other CalDAV clients use the sync-collection REPORT
-- (RFC 6578) for incremental sync. The server must return only the
-- resources that changed since the client's last sync-token, including
-- deletions as 404 tombstones.
--
-- This table records every create/update/delete of a calendar object so
-- handleSyncCollection can compute the delta. Each row carries a
-- globally-monotonic revision drawn from a dedicated sequence; the
-- sync-token we hand back to the client encodes the latest revision the
-- client has seen for that calendar, so a subsequent sync-collection
-- request only returns rows with revision > that value. Global monotonic
-- revisions are also monotonic per-calendar, which is all sync-collection
-- requires, and using a sequence avoids the per-calendar MAX(revision)+1
-- locking race that two concurrent writes would otherwise hit.
--
-- The change row is written inside the SAME transaction as the event
-- mutation and the CTag bump, so a crash never leaves a change unrecorded
-- (and never records a change whose event write was rolled back).

CREATE SEQUENCE calendar_changes_revision_seq AS BIGINT;

CREATE TABLE calendar_changes (
    id BIGSERIAL PRIMARY KEY,
    calendar_id UUID NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    event_uid VARCHAR(500) NOT NULL,
    action VARCHAR(16) NOT NULL CHECK (action IN ('create', 'update', 'delete')),
    revision BIGINT NOT NULL DEFAULT nextval('calendar_changes_revision_seq'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The sync-collection query is "all rows for this calendar with
-- revision > $since"; this index makes that a range scan.
CREATE INDEX idx_calendar_changes_calendar_revision
    ON calendar_changes (calendar_id, revision);

-- Useful for pruning old change rows once every client has synced past
-- them.
CREATE INDEX idx_calendar_changes_created_at
    ON calendar_changes (created_at);
