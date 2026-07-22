-- Migration 022: Down — drops calendar change tracking table and sequence
DROP TABLE IF EXISTS calendar_changes;
DROP SEQUENCE IF EXISTS calendar_changes_revision_seq;
