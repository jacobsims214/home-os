-- 059_activity_client.up.sql (#4491)
--
-- activity_log names the actor (user, and agent token when one acted) but not
-- the OAuth client of an interactive MCP session. The agent credential
-- decision (decisions/2026-11-15-agent-credential-access.md, update after
-- review #4448) requires every credential_reveal audit row to identify the
-- client: the agent token id, or the OAuth client id (azp) for a Dex session
-- with no agent token.
--
-- actor_client_id holds that client id. Its default reads app.client_id,
-- which the MCP pool sets on every connection acquisition from the caller's
-- claims (alongside app.user_id, app.household_id and app.agent_token_id), so
-- rows written by 050's log_activity() trigger record the client too without
-- changing that function. Callers that never set app.client_id (the api,
-- worker, caldav, agent-token MCP calls) get NULL.

BEGIN;

ALTER TABLE activity_log
    ADD COLUMN actor_client_id VARCHAR(200)
        DEFAULT left(nullif(btrim(current_setting('app.client_id', true)), ''), 200);

COMMENT ON COLUMN activity_log.actor_client_id IS
    'OAuth client (azp) of the interactive MCP session that acted; NULL for agent tokens (see actor_agent_token_id), the web app and the system.';

COMMIT;
