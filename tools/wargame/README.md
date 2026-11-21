# Family-hub war game

A small harness for war-gaming Home OS as the family's agent hub: simulated parents talk to
their own AI agents (moltis instances with the Home OS MCP attached as that member), and
you check what actually landed in Home OS with direct MCP calls.

| File | What it does |
|---|---|
| `chat.sh` | Sends one persona turn to a moltis agent, waits for the reply, prints the tool calls it made and its answer. `--history` dumps a whole session; `--stdin` / `--file` take the message where the shell cannot expand it — **see the quoting section immediately below**; `--selftest` runs the message-guard tests. |
| `mcpcall.py` | Ground truth: calls one Home OS MCP tool as a household member and prints the result, over Streamable HTTP at `POST <HOMEOS_MCP_URL>/mcp` — the only MCP transport Home OS serves. `--selftest` runs its transport tests with no network. |
| `api.py` | Same idea for the REST API (`/api/v1/...`). **Dex id_token only** — a `hos_` agent token 401s there by design; see [Two kinds of token](#two-kinds-of-token-agent-tokens-are-mcp-scoped). |
| `personas.md` | The household, the two personas (Jacob, Amber), their scenarios and sample turns, and the agent system-prompt snippet. |
| `REGRESSION-COVERAGE.md` | **Every R1–R3 finding, its regression test, and whether that test was proved to fail.** Read it before adding a check: a test never seen red is not a test. |
| `KNOWN-MANUAL.md` | The findings that genuinely cannot be automated, each with its reason (live-only / judgment / physical / agent-prose) and its manual procedure. A gap here is deliberate; a gap anywhere else is a bug. |

**A round's findings must leave the next round less work, not the same work.** When a
round produces a defect, the ticket that fixes it owes a regression test at the cheapest
faithful level (unit > integration > e2e > live-smoke) — and that test must be
**revert-checked**: apply the defect back, watch it go red, restore it, watch it go green.
#5304 is the cautionary case: fixed after R2, re-found by all three R3 reports, and fixed
again, because nothing executable guarded it. If a finding cannot be automated, it goes in
`KNOWN-MANUAL.md` with the reason, so the next round does not rediscover it as a mystery.

## Never put a `$` through your shell — always single-quote, or better use `--stdin`

**Read this before your first turn that mentions money.** A `chat.sh` message typed in
**double** quotes is expanded by *your* shell before `chat.sh` starts, and the damage is
permanent: moltis stores what arrived, and the agent writes that to Home OS.

```bash
# WRONG — bash eats $2, $1, $7 as positional parameters (usually empty)
./chat.sh jacob bills "mortgage is $2,340, electric $180, internet is $75"
#   moltis receives:  mortgage is ,340, electric 80, internet is 5

# RIGHT — single quotes: the shell passes the text through untouched
./chat.sh jacob bills 'mortgage is $2,340, electric $180, internet is $75'
#   moltis receives:  mortgage is $2,340, electric $180, internet is $75

# BEST — never let the text near a shell at all
./chat.sh jacob bills --stdin <<'EOF'
mortgage is $2,340, electric $180, internet is $75
EOF

printf '%s' 'Riverside copay was $35, due 12/10' | ./chat.sh jacob bills --stdin
./chat.sh jacob bills --file msg.txt
```

Watch the heredoc delimiter: `<<'EOF'` (quoted) is safe, `<<EOF` (unquoted) expands exactly
like double quotes. `--stdin` fed from a double-quoted `printf "%s" "copay $35"` is just as
broken as the argv form — quoting discipline applies to whatever builds the text, not only
to the final command.

**This has already corrupted real data.** In R1, Jacob's `$2,340 / $180 / $75` turn arrived
as `,340 / 80 / 5` (and the agent rightly refused to guess), and a concurrent Amber turn
filed the Riverside Pediatrics $35 copay as a bill of **`amount: 5`** with a document body
`Well visit copay — 5.00` — the `$35` → `5` signature. Both the bill and the document are
wrong in the war-game household until corrected (KB `discoveries/war-game-r1-jacob.md` G9).
A `$` gone from the middle of the text is invisible: `electric 80` looks exactly like a
bill that really is 80.

### What `chat.sh` will and will not catch

It refuses and warns, and it cannot do more than that, because the expansion happened
before it ran:

| The message `chat.sh` receives | What it does |
|---|---|
| `mortgage is ,340 … 80 … 5` — a comma starting a 3-digit group with no digit before it | **refuses** (exit 2), sends nothing; proves a `$2,340` was eaten |
| `copay was 5` — a money word followed by a bare number and **no `$` anywhere** | warns: this is the shape left by an eaten `$` |
| `home is $HOME`, `$(date)`, `$[3]` … arriving as an **argument** | warns: this one survived, so it was quoted, but the path is unsafe |
| anything via `--stdin` / `--file` | silent — the shell never touched it |

`--allow-suspicious` sends a message the guard would refuse; `--allow-dollar` stops the
`$`-on-argv warning. Use them only when you are certain the text is what you mean.

**The guard is a backstop, not the fix.** A message the shell already flattened to
`electric 80, internet 5` carries no trace of what it was, so no script can catch it. The
fix is the quoting above: `--stdin` with `<<'EOF'`, or `--file`. The guard exists because
this trap cost the household real data once already.

After any turn that mentioned an amount, check what was stored: `./chat.sh jacob bills
--history` shows the `USER:` line moltis recorded, which is the text the agent actually
read.

---

Requirements: `bash`, `curl`, `python3` (standard library only), `jq` for the recipes
below. Run `mcpcall.py`/`api.py` from somewhere that can reach the Home OS services, for
example a throwaway pod in the `home-os` namespace, or through a port-forward.

> **The MCP transport is Streamable HTTP at `POST <HOMEOS_MCP_URL>/mcp` — and nothing
> else.** Since #4361 the legacy HTTP+SSE transport (`GET /mcp/sse`, `POST /mcp/message`)
> is gone from Home OS: both are **404s**. Don't count on SSE anywhere: it was what the
> pre-#4361 build served, and the old line here — "the legacy SSE transport … which every
> MCP build serves" — stopped being true the moment this build dropped it (it cost two R1
> players an hour each, G8/G10). `mcpcall.py` now posts to `/mcp` with the `initialize`
> handshake and needs no session, because the server is stateless.
>
> Run it from somewhere that can reach the service — a throwaway pod in the `home-os`
> namespace, or a port-forward. Off-cluster:
>
> ```bash
> kubectl -n home-os port-forward svc/home-os-mcp 8082:8082 &
> export HOMEOS_MCP_URL=http://127.0.0.1:8082     # a BASE url: the script appends /mcp
> ./mcpcall.py -list                              # 90 tools on Stage 1
> ```
>
> `HOMEOS_MCP_URL` is a **base** URL (`http://host:8082`), not the endpoint: the script
> appends `/mcp`. A value that already ends in `/mcp` is used as-is; anything else —
> notably `.../mcp/sse`, the old shape — becomes `<base>/mcp` and 404s. Every failure
> names the URL it posted to and what the status means: a 404 says the endpoint does not
> exist and the SSE paths were removed, a 401 points at `HOMEOS_AGENT_TOKEN`, and an
> unreachable host says to start the port-forward. Exit status is `0` ok, `1` the server
> refused the call (a JSON-RPC error, or the tool's own `isError`), `2` transport or
> usage — so a failed ground-truth read never looks like a pass.
>
> `api.py` is plain REST and unaffected by any of this; it works as documented below.

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
export HOMEOS_MCP_URL=http://home-os-mcp.home-os:8082   # base; the script POSTs to /mcp
export HOMEOS_AGENT_TOKEN=...        # the member's hos_ agent token
# optional: HOMEOS_MCP_TIMEOUT_S (default 60)

# A Dex id_token also works here (the MCP accepts both).
# There is no unattended way to mint one any more — see "Getting a Dex id_token".
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
`HOMEOS_API_TOKEN` it stops and tells you to take one from a signed-in browser — the
password grant that used to mint one is retired (#4540).

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

### Getting a Dex id_token (the password grant is RETIRED — #4540)

**Do not mint one with the OAuth2 password grant: it is off.** #4886 restored
`oauth2.passwordConnector` as an interim chart value so a headless caller had some way in
before per-member agent tokens existed; #4535 then issued those tokens to every family
agent, and #4540 removed the grant. `POST /dex/token` with `grant_type=password` now
answers **`400 unsupported_grant_type`** on the deployed stack, on purpose. That is a
security improvement: a password grant turns any valid Dex password into an unscoped 24h
bearer token for the MCP audience, with no per-agent revocation and no rate limit at
Envoy (Envoy cannot tell a password grant from a refresh without reading the body).

**An agent token is the normal credential, and it covers the MCP** — which is where
almost all ground truth comes from (`mcpcall.py`). One per member, scoped
`["read","write"]`, revocable: see project KB `how-to/agent-tokens.md`.

**REST ground truth (`api.py`) needs a Dex id_token**, because the API is where credentials
are minted and revoked and it accepts Dex-issued tokens only (an agent token 401s there by
design — that is the blast-radius ceiling, see the section above). Get one from a browser
you are already signed in as that member:

1. Sign in to `https://homeos.simstech.cloud` as the member.
2. DevTools → Application → Cookies → copy the value of the httpOnly `home-os-token`
   cookie. It **is** the Dex id_token (24h, refreshed by the UI while you stay signed in).
3. `read -rs HOMEOS_API_TOKEN` and paste it — not on a command line, which lands in shell
   history and in `ps`.

```bash
read -rs HOMEOS_API_TOKEN
export HOMEOS_API_URL=https://homeos.simstech.cloud
./api.py GET /api/v1/auth/me
```

There is no unattended substitute on the deployed stack and none is planned: a headless
REST client would need a scoped Dex client, which is a different design change, not a
rewording of this one. If a round genuinely cannot use a browser, say so on the ticket
rather than re-enabling the grant.

**LOCAL DEV ONLY.** A developer Dex may still set `dex.passwordConnector` (the chart
value is empty in the deployed values, and stays a value so a re-add is visible in a
diff). Against such a Dex, and only there, `api.py` and `mcpcall.py` will mint a token if
you opt in explicitly with `HOMEOS_DEX_PASSWORD_GRANT=1` plus `HOMEOS_DEX_TOKEN_URL`,
`HOMEOS_USER` and `HOMEOS_PASSWORD`. Without the opt-in both scripts stop and say what to
set; they never silently try the grant, so a retired grant cannot show up as a mysterious
failure mid-round.

**`/dex/token`, never `/token`.** If you are debugging a local Dex, note that Dex mounts
the token endpoint at `<issuer>/token`, which here is `/dex/token`. The bare root `/token`
exists only as an Envoy `prefix_rewrite` alias
(`deploy/helm/home-os/templates/envoy/configmap.yaml`), so
`POST http://home-os-dex.home-os:5556/token` against the Dex pod is a **404** — not a
broken deployment and not a bad credential, just the wrong path.

Two Dex facts that still matter with the grant off: `400 unsupported_grant_type` means
the grant is off (now the expected answer, so treat it as "use a browser token");
`enablePasswordDB: true` stays on because **interactive login** uses the local connector —
removing it locks every family member out of the UI.

Checked 2026-11-17 (#4946, before the retirement) against the deployed Stage 1 stack: a
minted id_token returned **200** on `/api/v1/auth/me` and `/api/v1/bills` for two members
(jacob and amber); a `hos_` agent token on the same paths returned **401**; that same agent
token on MCP `POST /mcp` `tools/list` returned **200** with 90 tools; and against the Dex
pod directly, `/token` returned **404** while `/dex/token` returned **401** (the connector
ran). After #4540 the last of those becomes **400 unsupported_grant_type**; the agent-token
half is unchanged and is what agents actually use.

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
   ./chat.sh jacob cal 'Kid B soccer every Tue/Thu 5:30-6:30 through mid Dec, family calendar'

   # Anything with a $ in it: --stdin with a QUOTED heredoc (see the top of this file)
   ./chat.sh jacob bills --stdin <<'EOF'
   mortgage is $2,340, electric $180, internet is $75
   EOF
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

   **Single-quote every message; use `--stdin` with `<<'EOF'` or `--file` for anything with
   a `$` in it.** A double-quoted message is expanded by your shell before `chat.sh` runs,
   and the agent files whatever survived — that is how a $35 copay became a $5 bill. If a
   turn mentioned an amount, read the `USER:` line back with
   `./chat.sh jacob <session> --history` and confirm it is the text you meant to send.

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
- **Never put a `$` amount through your shell on the argv path.** Single quotes, or
  `--stdin` / `--file` — see "Never put a `$` through your shell" at the top of this file.
  `chat.sh` warns, and refuses on the provable case, but the shell expands before it runs, so
  the guard is a backstop and not a licence to be careless.
