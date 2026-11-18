-- 051_secret_key_verifier (#4440): salted, slow master-password verifier.
--
-- Before 051, secret_keys.key_hash held SHA-256 of the browser's PBKDF2-derived
-- key: the exact bytes the browser sends to POST /api/v1/secrets/verify. A
-- database leak therefore handed out a value that could be replayed to /verify,
-- and a fast, unsalted oracle for guessing master passwords offline.
--
-- 051 adds secret_keys.verifier: an argon2id PHC string
--   $argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt b64>$<hash b64>
-- computed by the API over the client-sent verifier, with a random 16-byte salt
-- per row. The cost parameters live in the string, so the API can raise them
-- later and rehash on the next successful unlock without another migration.
--
-- Upgrade path: existing rows keep key_hash and have verifier NULL. On the next
-- successful /verify the API checks key_hash (constant time), writes the
-- argon2id verifier and clears key_hash in one UPDATE. New setups store only a
-- verifier. Exactly one of the two is set on every row (CHECK below), so a row
-- can never keep the old unsalted hash next to the new verifier.
--
-- Scope: secret_keys only. The secrets table is untouched; #4446 ("shared with
-- agents") adds its own nullable server-encrypted column there, keyed by a
-- household key held in a Kubernetes Secret, never in this table.
--
-- activity_log: 050 puts no authorship or activity triggers on secret_keys
-- (see 050's "Not covered" list), and its secrets summary uses the credential
-- name only, so neither verifier material nor key_hash ever reaches the log.
--
-- Down (051_secret_key_verifier.down.sql) cannot recreate key_hash for an
-- upgraded row. It parks those verifiers in secret_key_verifier_051_backup, and
-- this file restores them if that table exists, so down then up is lossless.
--
-- Runs in one transaction.

BEGIN;

ALTER TABLE secret_keys ADD COLUMN verifier TEXT;
ALTER TABLE secret_keys ALTER COLUMN key_hash DROP NOT NULL;

-- Restore verifiers parked by a previous 051 down (no-op on a first run).
DO $$
BEGIN
    IF to_regclass('public.secret_key_verifier_051_backup') IS NOT NULL THEN
        UPDATE secret_keys k
           SET verifier = b.verifier,
               key_hash = NULL
          FROM secret_key_verifier_051_backup b
         WHERE b.secret_key_id = k.id;
        DROP TABLE secret_key_verifier_051_backup;
    END IF;
END
$$;

ALTER TABLE secret_keys
    ADD CONSTRAINT secret_keys_one_verifier
        CHECK (num_nonnulls(key_hash, verifier) = 1),
    ADD CONSTRAINT secret_keys_verifier_argon2id
        CHECK (verifier IS NULL OR verifier LIKE '$argon2id$%');

COMMENT ON COLUMN secret_keys.verifier IS
    'argon2id PHC string over the client-sent master-password verifier (salted per row). Never returned by the API.';
COMMENT ON COLUMN secret_keys.key_hash IS
    'LEGACY (pre-051): SHA-256 of the derived key. Cleared when the row upgrades to verifier on its next unlock.';

COMMIT;
