-- 057_secret_agent_share (#4446): per-credential "shared with agents".
--
-- Decision (KB decisions/2026-11-15-agent-credential-access.md, #4439):
-- every credential stays zero-knowledge by default (encrypted in the browser
-- with the household master password; no server process can read it). An
-- owner or family_manager may mark ONE credential "shared with agents". The
-- browser then sends its plaintext once over TLS and the API stores a second,
-- server-encrypted copy here:
--
--   secrets.agent_ciphertext  0x01 || 12-byte random nonce || AES-256-GCM
--                             ciphertext+tag. The key is a per-household key
--                             derived (HKDF-SHA256) from a server master key
--                             held in a Kubernetes Secret (env
--                             AGENT_CREDENTIALS_KEY). The master key is never
--                             stored in Postgres. The AAD binds the blob to
--                             its household and secret id, so it can't be
--                             copied to another row.
--   secrets.agent_shared_at   when sharing was turned on.
--   secrets.agent_shared_by   who turned it on (users.id).
--
-- NULL agent_ciphertext means "not shared" (the default). Turning sharing off
-- sets all three back to NULL, which deletes the server copy immediately.
--
-- activity_log: the MCP writes one row per agent read with action 'read'
-- (who, which agent token, which credential, never the value). 050's CHECK
-- allowed only create/update/delete, so it is widened here. 054's
-- notify_entity_creator ignores every action except update/delete, so a read
-- raises no notification.
--
-- 050's secrets activity trigger logs share/unshare as an ordinary update
-- whose changed_fields are column names (agent_ciphertext, ...), and whose
-- summary carries the credential name only, never a value.

BEGIN;

ALTER TABLE secrets
    ADD COLUMN agent_ciphertext BYTEA,
    ADD COLUMN agent_shared_at  TIMESTAMPTZ,
    ADD COLUMN agent_shared_by  UUID REFERENCES users(id) ON DELETE SET NULL;

-- Shared rows always say when; unshared rows carry no leftovers. A nonce
-- (12) plus a GCM tag (16) plus the version byte is the minimum length.
ALTER TABLE secrets
    ADD CONSTRAINT secrets_agent_share_consistent
        CHECK ((agent_ciphertext IS NULL) = (agent_shared_at IS NULL)
               AND (agent_ciphertext IS NOT NULL OR agent_shared_by IS NULL)),
    ADD CONSTRAINT secrets_agent_ciphertext_len
        CHECK (agent_ciphertext IS NULL OR octet_length(agent_ciphertext) >= 29);

ALTER TABLE activity_log DROP CONSTRAINT activity_log_action_check;
ALTER TABLE activity_log
    ADD CONSTRAINT activity_log_action_check
        CHECK (action IN ('create', 'update', 'delete', 'read'));

COMMIT;
