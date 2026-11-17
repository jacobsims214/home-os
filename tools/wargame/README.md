# Family-hub war game

A small harness for war-gaming Home OS as the family's agent hub: simulated parents talk to
their own AI agents (moltis instances with the Home OS MCP attached as that member), and
you check what actually landed in Home OS with direct MCP calls.

| File | What it does |
|---|---|
| `chat.sh` | Sends one persona turn to a moltis agent, waits for the reply, prints the tool calls it made and its answer. `--history` dumps a whole session. |
| `mcpcall.py` | Ground truth: calls one Home OS MCP tool as a household member and prints the result. Still speaks the legacy SSE transport, which Stage 1 no longer serves — see the note under Requirements. |
| `api.py` | Same idea for the REST API (`/api/v1/...`). **Dex id_token only** — a `hos_` agent token 401s there by design; see [Two kinds of token](#two-kinds-of-token-agent-tokens-are-mcp-scoped). |
| `personas.md` | The household, the two personas (Jacob, Amber), their scenarios and sample turns, and the agent system-prompt snippet. |

Requirements: `bash`, `curl`, `python3` (standard library only), `jq` for the recipes
below. Run `mcpcall.py`/`api.py` from somewhere that can reach the Home OS services, for
example a throwaway pod in the `home-os` namespace, or through a port-forward.

> **`mcpcall.py` is not yet usable against Stage 1 (#4949).** It opens `/mcp/sse`, the
> legacy SSE transport, and the deployed MCP serves Streamable HTTP only at `/mcp`: a
> `GET /mcp/sse` is a **404**, and `./mcpcall.py -list` dies in `queue.Empty` rather than
> saying so. Until #4949 ports it, call the MCP directly:
>
> ```bash
> curl -sS -X POST "${HOMEOS_MCP_URL%/}/mcp" \
>   -H "Authorization: Bearer $HOMEOS_AGENT_TOKEN" \
>   -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
>   -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | jq '.result.tools | length'
> ```
>
> `api.py` has no such problem: it is plain REST and works as documented below.

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

**Home OS MCP — `mcpcall.py`.** Accepts either kind of token (see the next section). The
agent token is the normal one: one per member, minted by that member.

```bash
export HOMEOS_MCP_URL=http://home-os-mcp.home-os:8082
export HOMEOS_AGENT_TOKEN=...        # the member's hos_ agent token

# Instead of a token, mint a Dex id_token with the password grant
# (needs `dex.passwordConnector: local` in the chart values):
export HOMEOS_DEX_TOKEN_URL=http://home-os-dex.home-os:5556/dex/token
export HOMEOS_USER=<member login email>
export HOMEOS_PASSWORD=...
```

**Home OS REST API — `api.py`.** A Dex identity token **only**:

```bash
export HOMEOS_API_URL=http://home-os-envoy.home-os:8000
export HOMEOS_API_TOKEN=...          # a Dex id_token, not a hos_ agent token
```

`http://home-os-api.home-os:8080` reaches the API directly and is equally good; the Envoy
origin above is used in the examples because Dex's token endpoint is on the same host, so
one `curl` recipe mints a token and uses it. Off-cluster, port-forward both services and
use `http://127.0.0.1:<local port>` for each.

`api.py` reads `HOMEOS_API_TOKEN` and nothing else, so an agent token exported for the MCP
can never leak into a REST call. If `HOMEOS_API_TOKEN` holds a `hos_` token it refuses to
run and says why, instead of 401ing every call and looking like a broken API. With no
`HOMEOS_API_TOKEN` it mints one from the Dex variables above.

To check as the other member, change the token for that one command:

```bash
HOMEOS_AGENT_TOKEN="$AMBER_AGENT_TOKEN" ./mcpcall.py note_find
HOMEOS_API_TOKEN="$AMBER_DEX_JWT"       ./api.py GET /api/v1/bills
```

The agents themselves need the Home OS MCP attached with that member's credentials
(Streamable HTTP at `<HOMEOS_MCP_URL>/mcp`, `Authorization: Bearer <hos_ token>`), and the
snippet at the end of `personas.md` in their system prompt. **That `hos_` token is
MCP-only, by design — do not wire it into anything that calls `/api/v1/*`.**

## Two kinds of token: agent tokens are MCP-scoped

| Token | Works against | Gets 401 from |
|---|---|---|
| `hos_…` **agent token** — per member, scoped `read`/`write`, revocable, minted at `POST /api/v1/agent-tokens` | the MCP, `POST /mcp` | the REST API, `/api/v1/*` |
| **Dex id_token** — the member's Dex identity token, 24h | the MCP **and** the REST API | — |

This is the design, not a bug, and **not something to "fix" in the auth middleware.** The
REST API is where credentials are created and destroyed —
`POST/DELETE /api/v1/agent-tokens`, `POST /api/v1/auth/change-password`, the admin routes.
An agent token lives in an agent's own environment, where a prompt injection or one
careless tool call can reach it. If the API accepted one, a leaked agent token could mint
its own replacement or revoke a family member's credentials: a contained leak would become
a permanent one. Scoping agent tokens to the MCP is a hard ceiling on the blast radius —
an agent token can read and write household data, and can never touch credentials.
`apps/api/internal/middleware/auth.go` verifies Dex-issued OIDC tokens only, and its 401 to
a `hos_` token is that ceiling working as intended. Decided in R1 triage
(`discoveries/war-game-r1.md` §3).

Background: project KB `how-to/agent-tokens.md` and `architecture/agent-tokens.md`.

### Minting a Dex id_token (interim — #4540 retires this)

The password goes in from a prompt into an environment variable, and the token likewise;
never inline either on a command line (it lands in shell history and in `ps`).

```bash
read -rs HOMEOS_PASSWORD             # the member's Dex password; nothing echoed or saved
export HOMEOS_USER=<member login email>
DEX_JWT=$(curl -fsS -X POST http://home-os-envoy.home-os:8000/dex/token \
  --data-urlencode 'grant_type=password' \
  --data-urlencode 'client_id=home-os-mcp' \
  --data-urlencode 'scope=openid email profile' \
  --data-urlencode "username=$HOMEOS_USER" \
  --data-urlencode "password=$HOMEOS_PASSWORD" \
  | jq -r .id_token)
unset HOMEOS_PASSWORD
curl -fsS -H "Authorization: Bearer $DEX_JWT" \
  http://home-os-envoy.home-os:8000/api/v1/auth/me
```

`client_id` may be `home-os-mcp` or `home-os-ui`; the API accepts either as `aud`. The
response carries `id_token` and **no `refresh_token`** (correct for this grant), valid 24h
— mint one per run rather than storing it.

**`/dex/token`, never `/token`.** Dex mounts the token endpoint at `<issuer>/token`, which
here is `/dex/token`. The bare root `/token` exists only as an Envoy `prefix_rewrite` alias
(`deploy/helm/home-os/templates/envoy/configmap.yaml`), so
`POST http://home-os-dex.home-os:5556/token` against the Dex pod is a **404** — not a
broken deployment and not a bad credential, just the wrong path. Use `/dex/token`
everywhere: it is right both through Envoy and against the Dex pod.

What a failure means: `400 unsupported_grant_type` — the grant is off
(`dex.passwordConnector` unset, or Dex not restarted since it was set); `401 access_denied`
— wrong password (the connector ran, which is the cheapest positive signal); `404` — you
used `/token` against the Dex pod.

**INTERIM: #4540 retires this grant.** Per-member agent tokens (#4535) are the path meant to
last; a member's password turned into an unscoped 24h bearer token with no per-agent
revocation is strictly worse as a permanent design. The war game needs the password grant
only because REST ground truth requires a Dex identity and an agent token cannot be one.
Nothing durable should depend on it. After #4540, REST ground truth is `/api/v1/auth/me`
with a token taken from the browser you are already signed into (the httpOnly
`home-os-token` cookie), and the block above goes away. See project KB
`architecture/dex-password-grant-interim.md`.

Checked 2026-11-17 (#4946) against the deployed Stage 1 stack: a minted id_token returns
**200** on `/api/v1/auth/me` and `/api/v1/bills` for two members (jacob and amber); a `hos_`
agent token on the same paths returns **401**; that same agent token on MCP `POST /mcp`
`tools/list` returns **200** with 90 tools; and against the Dex pod directly, `/token`
returns **404** while `/dex/token` returns **401** (the connector ran).

**Several households.** A caller in more than one picks one per request with
`X-Household-ID: <household uuid>`. It must be one of their own memberships (else 403); with
no header the oldest membership is used, and a malformed value is a 400.

## Running a round

1. **Baseline.** Record what's in the household before play, so you can tell the agents'
   writes apart from what was already there:

   ```bash
   ./mcpcall.py -list                                   # what tools exist right now
   ./mcpcall.py family_overview                         # people, calendars, counts, next 7 days
   ./mcpcall.py calendar_find
   ./mcpcall.py --text event_find '{"from":"2026-11-01","to":"2026-12-31"}' | jq '{returned, total}'
   ./mcpcall.py bill_find
   ./mcpcall.py note_find
   ```

2. **Play a persona turn.** Pick a scenario from `personas.md`. The session name is the
   topic; reuse it for follow-ups, use a new one when the topic changes:

   ```bash
   ./chat.sh jacob cal "Kid B soccer every Tue/Thu 5:30-6:30 through mid Dec, family calendar"
   ```

   Output looks like:

   ```text
   == jacob/cal (69s)
     tool ok  mcp__home-os__calendar_find {}
     tool ok  mcp__home-os__event_create {"calendar_id": "...", "title": "Soccer", ...}
   REPLY: Done - 9 soccer practices on the Family calendar, Tue/Thu 5:30-6:30pm ...
   ```

   `TIMEOUT` means the agent wrote no reply within `WG_TIMEOUT_S`. It may still have
   written data, so check with `./chat.sh jacob cal --history` and with ground truth.

3. **Check ground truth.** Verify what the agent said it did, as this member and, for
   anything shared, as the other member:

   ```bash
   ./mcpcall.py --text event_find '{"from":"2026-11-17","to":"2026-11-17"}' | jq '.items[] | {title,start,end}'
   # conflicts: event_find with from/to = the proposed time
   ./mcpcall.py event_find '{"from":"2026-11-19T17:45:00-05:00","to":"2026-11-19T18:15:00-05:00"}'
   ./mcpcall.py calendar_find_free_time '{"from":"2026-11-19","duration_minutes":60,"start_hour":14,"end_hour":20}'
   ./mcpcall.py agenda_get '{"from":"2026-11-16"}'
   ./mcpcall.py bill_find '{"due_within_days":14}'
   ./mcpcall.py budget_summary '{"sections":["obligations"]}'
   ./api.py GET /api/v1/auth/me
   ```

   Tool names follow the v2 catalog (`<area>_<verb>`, project KB
   `architecture/mcp-tool-catalog.md`); the exact registered set is `wantTools` in
   `apps/mcp/cmd/mcp/protocol_test.go`. Argument names change as the MCP grows;
   `./mcpcall.py -list` shows the current ones. Lists, recipes, meals, guides and event
   reminders are coming in Stage 2: until then the agent should say it has no tool for them.

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
