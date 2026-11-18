-- Compose-only: create the `app` role that migrations GRANT to
-- (032_loans, 034_agent_tokens). The compose services connect as `homeos`,
-- so `app` needs no login. Idempotent; the postgres image runs this only
-- when it initialises an empty data directory.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app') THEN
        CREATE ROLE app NOLOGIN;
    END IF;
END
$$;
