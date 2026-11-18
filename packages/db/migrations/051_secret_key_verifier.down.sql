-- 051_secret_key_verifier down (#4440).
--
-- Pre-051 code needs key_hash NOT NULL, but an upgraded row only has an
-- argon2id verifier and the old unsalted hash cannot be recomputed from it.
-- Down therefore:
--   * parks every verifier in secret_key_verifier_051_backup (re-applying 051
--     up restores them and drops the table, so down then up loses nothing);
--   * sets key_hash to an empty value on those rows. Pre-051 code compares it
--     to the 32 bytes the browser sends, so it never matches: those households
--     cannot unlock until 051 is applied again. Their encrypted secrets and
--     PBKDF2 salt are untouched. Legacy rows (never upgraded) work as before.
--
-- The backup table deliberately has NO foreign key to secret_keys (#4699).
-- It must outlive this file for the lossless down/up above, so it cannot be
-- dropped here; but an FK would make 026 down (DROP TABLE secret_keys) fail,
-- blocking every rollback below 051. Without it the table is plain parked
-- data: 051 up joins it on secret_key_id (orphans match nothing) and then
-- drops it, and a row whose key was deleted meanwhile just stays unmatched.
-- A database that ran the earlier version of this file has the FK; it is
-- dropped below.
--
-- Runs in one transaction.

BEGIN;

ALTER TABLE secret_keys
    DROP CONSTRAINT secret_keys_one_verifier,
    DROP CONSTRAINT secret_keys_verifier_argon2id;

CREATE TABLE IF NOT EXISTS secret_key_verifier_051_backup (
    secret_key_id UUID PRIMARY KEY, -- secret_keys.id; no FK, see header
    verifier      TEXT NOT NULL
);
ALTER TABLE secret_key_verifier_051_backup
    DROP CONSTRAINT IF EXISTS secret_key_verifier_051_backup_secret_key_id_fkey;

INSERT INTO secret_key_verifier_051_backup (secret_key_id, verifier)
SELECT id, verifier FROM secret_keys WHERE verifier IS NOT NULL
ON CONFLICT (secret_key_id) DO UPDATE SET verifier = EXCLUDED.verifier;

UPDATE secret_keys SET key_hash = ''::bytea WHERE key_hash IS NULL;

ALTER TABLE secret_keys DROP COLUMN verifier;
ALTER TABLE secret_keys ALTER COLUMN key_hash SET NOT NULL;

COMMENT ON COLUMN secret_keys.key_hash IS NULL;

COMMIT;
