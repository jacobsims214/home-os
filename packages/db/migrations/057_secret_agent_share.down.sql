-- 057_secret_agent_share down: drops every server-held shared copy (the
-- zero-knowledge browser copies are untouched) and the agent read audit rows,
-- which 050's narrower CHECK can't hold.

BEGIN;

DELETE FROM activity_log WHERE action = 'read';
ALTER TABLE activity_log DROP CONSTRAINT activity_log_action_check;
ALTER TABLE activity_log
    ADD CONSTRAINT activity_log_action_check
        CHECK (action IN ('create', 'update', 'delete'));

ALTER TABLE secrets
    DROP CONSTRAINT secrets_agent_ciphertext_len,
    DROP CONSTRAINT secrets_agent_share_consistent,
    DROP COLUMN agent_shared_by,
    DROP COLUMN agent_shared_at,
    DROP COLUMN agent_ciphertext;

COMMIT;
