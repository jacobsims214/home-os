-- Migration 026: Down — revert native secrets manager.
-- Reverse order of up migration (no cross-table FKs, but drop secrets first for clarity
-- since secret_keys is the key material table).

DROP TABLE IF EXISTS secrets;
DROP TABLE IF EXISTS secret_keys;
