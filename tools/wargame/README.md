# Family-hub war game

A small harness for war-gaming Home OS as the family's agent hub: simulated parents talk to
their own AI agents (moltis instances with the Home OS MCP attached as that member), and
you check what actually landed in Home OS with direct MCP calls.

| File | What it does |
|---|---|
| `chat.sh` | Sends one persona turn to a moltis agent, waits for the reply, prints the tool calls it made and its answer. `--history` dumps a whole session. |
| `mcpcall.py` | Ground truth: calls one Home OS MCP tool as a household member and prints the result. |
| `api.py` | Same idea for the REST API (`/api/v1/...`). |
| `personas.md` | The household, the two personas (Jacob, Amber), their scenarios and sample turns, and the agent system-prompt snippet. |

Requirements: `bash`, `curl`, `python3` (standard library only). Run `mcpcall.py`/`api.py`
from somewhere that can reach the Home OS services, for example a throwaway pod in the
`home-os` namespace, or through a port-forward.

## Configuration

Everything comes from environment variables. Nothing is hard-coded, and credentials are
never passed as arguments. Keep the values in a local file you don't commit (for example
`tools/wargame/.env`, then `set -a; . ./.env; set +a`).

**Personas (`chat.sh`).** One pair per persona; the persona name is upper-cased:

```bash
export WG_JACOB_URL=http://<jacob-moltis-host>:13131
export WG_JACOB_PASSWORD=...            # moltis login for that instance
export WG_AMBER_URL=http://<amber-moltis-host>:13131
export WG_AMBER_PASSWORD=...
# optional: WG_TIMEOUT_S (default 600), WG_COOKIE_DIR (default $TMPDIR)
```

**Home OS (`mcpcall.py`, `api.py`).**

```bash
export HOMEOS_MCP_URL=http://home-os-mcp.home-os:8082
export HOMEOS_API_URL=http://home-os-api.home-os:8080

# Either a bearer token for the member...
export HOMEOS_AGENT_TOKEN=...
# ...or mint a Dex id_token with the password grant
# (needs `oauth2.passwordConnector: local` in the Dex config):
export HOMEOS_DEX_TOKEN_URL=http://home-os-dex.home-os:5556/dex/token
export HOMEOS_USER=<member login email>
export HOMEOS_PASSWORD=...
```

To check as the other member, change the token or user/password for that one command:

```bash
HOMEOS_AGENT_TOKEN="$AMBER_TOKEN" ./mcpcall.py list_notes
```

The agents themselves need the Home OS MCP attached with that member's credentials
(Streamable HTTP at `<HOMEOS_MCP_URL>/mcp`, `Authorization: Bearer <token>`), and the
snippet at the end of `personas.md` in their system prompt.

## Running a round

1. **Baseline.** Record what's in the household before play, so you can tell the agents'
   writes apart from what was already there:

   ```bash
   ./mcpcall.py -list                                   # what tools exist right now
   ./mcpcall.py list_calendars
   ./mcpcall.py --text list_events '{"start_date":"2026-11-01","end_date":"2026-12-31"}' | jq length
   ./mcpcall.py list_bills
   ./mcpcall.py list_notes
   ```

2. **Play a persona turn.** Pick a scenario from `personas.md`. The session name is the
   topic; reuse it for follow-ups, use a new one when the topic changes:

   ```bash
   ./chat.sh jacob cal "Kid B soccer every Tue/Thu 5:30-6:30 through mid Dec, family calendar"
   ```

   Output looks like:

   ```text
   == jacob/cal (69s)
     tool ok  mcp__home-os__list_calendars {}
     tool ok  mcp__home-os__create_event {"calendar_id": "...", "title": "Soccer", ...}
   REPLY: Done - 9 soccer practices on the Family calendar, Tue/Thu 5:30-6:30pm ...
   ```

   `TIMEOUT` means the agent wrote no reply within `WG_TIMEOUT_S`. It may still have
   written data, so check with `./chat.sh jacob cal --history` and with ground truth.

3. **Check ground truth.** Verify what the agent said it did, as this member and, for
   anything shared, as the other member:

   ```bash
   ./mcpcall.py --text list_events '{"start_date":"2026-11-17","end_date":"2026-11-17"}' | jq '.[] | {title,start,end}'
   ./mcpcall.py check_conflicts '{"start_time":"2026-11-19T17:45:00-05:00","end_time":"2026-11-19T18:15:00-05:00"}'
   ./mcpcall.py get_daily_briefing '{"date":"2026-11-16"}'
   ./api.py GET /api/v1/auth/me
   ```

   Argument names change as the MCP grows; `./mcpcall.py -list` shows the current ones.

4. **Score it.** Note PASS / PARTIAL / FAIL for each scenario, the tool calls, the
   ground-truth result, the reply, and the time taken. Write the round up in the project KB
   (`discoveries/family-hub-war-game-<date>.md`), with gaps as Home OS vs agent/harness issues.

5. **Clean up.** Delete the test events and notes, or reset the household, before the next
   round. Remove any probe pod you created, because it may hold credentials.

## Rules

- Never commit `.env` files, tokens, passwords, kubeconfigs or cluster IPs. Use placeholders
  in docs and examples.
- Don't use real kids' names in personas or write-ups.
- Ground truth is read-only unless you are deliberately setting up a scenario. Writes as a
  member show up for the whole household.
