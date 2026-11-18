-- Per-member, revocable, scoped API tokens for each family member's agent.
-- Only a SHA-256 hash of the high-entropy secret is stored; token_prefix is a
-- short non-secret prefix shown in the UI so members can tell tokens apart.
CREATE TABLE agent_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    household_id UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    token_prefix VARCHAR(32) NOT NULL,
    token_hash BYTEA NOT NULL,
    scopes TEXT[] NOT NULL DEFAULT '{}',
    last_used_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT agent_tokens_token_hash_len CHECK (octet_length(token_hash) = 32)
);

CREATE UNIQUE INDEX agent_tokens_token_hash_key ON agent_tokens (token_hash);
CREATE INDEX idx_agent_tokens_user ON agent_tokens (user_id);

-- Grant to the app role when it exists (see 032_loans); skipped on databases
-- where the migration runner is the app user.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app') THEN
        GRANT ALL ON TABLE agent_tokens TO app;
    END IF;
END
$$;
