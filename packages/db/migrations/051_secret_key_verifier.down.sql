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
-- Runs in one transaction.

BEGIN;

ALTER TABLE secret_keys
    DROP CONSTRAINT secret_keys_one_verifier,
    DROP CONSTRAINT secret_keys_verifier_argon2id;

CREATE TABLE IF NOT EXISTS secret_key_verifier_051_backup (
    secret_key_id UUID PRIMARY KEY REFERENCES secret_keys(id) ON DELETE CASCADE,
    verifier      TEXT NOT NULL
);

INSERT INTO secret_key_verifier_051_backup (secret_key_id, verifier)
SELECT id, verifier FROM secret_keys WHERE verifier IS NOT NULL
ON CONFLICT (secret_key_id) DO UPDATE SET verifier = EXCLUDED.verifier;

UPDATE secret_keys SET key_hash = ''::bytea WHERE key_hash IS NULL;

ALTER TABLE secret_keys DROP COLUMN verifier;
ALTER TABLE secret_keys ALTER COLUMN key_hash SET NOT NULL;

COMMENT ON COLUMN secret_keys.key_hash IS NULL;

COMMIT;
