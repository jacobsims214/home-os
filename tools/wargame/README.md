# The war game: everything you need, in the order you need it

*Last verified end to end on 2026-11-20: `./verify.py` 7/7, `chat.sh --selftest`
22/22, `mcpcall.py -list` 90 tools. Facts below were re-checked live against the
cluster and the deploy on that date, not copied from an earlier round's notes.*

This is the single source of truth for running a **war-game round** of Home OS: a
simulated household whose two adults each talk to their own AI agent (a moltis
instance with the Home OS MCP attached as that member), while you check what
actually landed in Home OS with direct MCP calls.

**Read this file top to bottom before your first turn.** It is written for a
worker with **zero prior context** — no memory of R1, R2 or R3, no idea where
anything lives. If you follow it in order you will not have to ask anyone
anything. If you *do* get stuck, that is a bug in this document: fix the document.

The reason it exists, in one line: **three times in one week a worker reported
"the credentials expired" when the credentials were fine and the only problem was
that nobody had written down where they live.** A credential that is not
discoverable is as good as a missing one. Nothing in this file is a secret; every
secret is named, and the naming is the point.

| What you want | Where it is |
|---|---|
| Am I ready to play? | [Verifying the environment](#verifying-the-environment) — `./verify.py` |
| The cluster / kubectl | [Cluster access](#cluster-access) |
| A persona's password | [War-game accounts](#war-game-accounts-the-dex-identities) |
| An agent's MCP token | [Agent tokens](#agent-tokens-and-why-a-rest-401-is-correct) |
| Which moltis is Jacob's | [Moltis instances](#moltis-instances) |
| Driving the UI at 390px | [Browser driving](#browser-driving) |
| What would get me fired | [Never-do list](#never-do-list) |
| Playing a round | [Running a round](#running-a-round) |
| Hermetic shell quoting (money!) | [Never put a dollar amount through your shell](#never-put-a-dollar-amount-through-your-shell) |

| File | What it does |
|---|---|
| `verify.yaml` | The environment, declared: cluster, site, Dex, every persona, and the Secret each credential comes from. No values. |
| `verify.py` | Runs `verify.yaml` and prints a pass/fail table. Standard library only. |
| `chat.sh` | Sends one persona turn to a moltis agent, waits for the reply, prints the tool calls it made and its answer. `--history` dumps a session; `--stdin` / `--file` take the message where the shell cannot expand it (**see the quoting section**); `--selftest` runs the message-guard tests. |
| `mcpcall.py` | Ground truth: calls one Home OS MCP tool as a household member, over Streamable HTTP at `POST <HOMEOS_MCP_URL>/mcp`. `--selftest` runs its transport tests with no network. |
| `api.py` | Same idea for the REST API (`/api/v1/...`). **Dex id_token only** — a `hos_` agent token 401s there by design; see [Agent tokens](#agent-tokens-and-why-a-rest-401-is-correct). |
| `personas.md` | The household, the two personas (Jacob, Amber), their scenarios and sample turns, and the agent system-prompt snippet. |
| `README.md` | This file. |

---

## Verifying the environment

Before you spend a turn, prove the environment is what this document says it is:

```bash
cd tools/wargame
./verify.py            # a pass/fail table; exit 0 only if everything passed
./verify.py --list     # what it checks, without running anything
./verify.py --json     # the same result, for a script or a ticket comment
./verify.py --only moltis   # just the checks whose id contains "moltis"
```

Seven checks, in the order they are worth failing in:

| Check | Passes when |
|---|---|
| `cluster-reachable` | `kubectl get nodes` returns **5 Ready** with the kubeconfig in `verify.yaml` |
| `site-up` | `https://homeos.simstech.cloud` answers 200 (following the login redirect) |
| `dex-reachable` | Dex's `/.well-known/openid-configuration` answers 200 |
| `persona-jacob-mcp` | Jacob's `hos_` token from `home-os-token-wg-jacob` calls `family_overview` over MCP |
| `persona-amber-mcp` | Amber's `hos_` token from `home-os-token-wg-amber` does the same |
| `moltis-jacob-up` | `moltis-wg-jacob` accepts its own login password |
| `moltis-amber-up` | `moltis-wg-amber` accepts its own login password |

Every failure prints **where to look**, not what the value was. `verify.py` reads
each password and token from the Secret `verify.yaml` names, uses it in memory,
and never writes it anywhere — so its output is safe to paste into a ticket.

**Reading a failure.** The `->` line under each FAIL is the diagnosis. Two of them
are worth memorising because they are the ones that get misdiagnosed:

- `cluster-reachable` FAIL with `connect: connection refused` on
  `localhost:8080` — kubectl did not get your kubeconfig. It fell back to the
  local default because the path in `verify.yaml` (`$KUBECONFIG`, else the 5319
  path) does not exist. See [Cluster access](#cluster-access).
- `persona-*-mcp` FAIL with `-> 401: the agent token … was refused` — the
  token is missing or revoked. That is **not** the transport, **not** the MCP
  being down, and **not** a REST auth problem; it is one Secret. See
  [Agent tokens](#agent-tokens-and-why-a-rest-401-is-correct).

To change what is checked, edit `verify.yaml` — not `verify.py`. The spec is a
deliberately small YAML subset (block maps, lists, `${ENV:-default}`); anchors,
flow maps and multi-line strings are rejected loudly rather than parsed wrongly.

---

## Cluster access

Everything runs on one Talos homelab cluster. Its control plane is
**`https://192.168.8.226:6443`** — not the dead `.176`.

```bash
export KUBECONFIG=/Volumes/external-data/development/simstech/talos/pve-1/amp-work/5319/kube/admin.kubeconfig
kubectl get nodes          # must return 5 Ready
kubectl -n moltis get pods # 5 moltis pods, all Running
```

| Fact | Value |
|---|---|
| Control plane | `https://192.168.8.226:6443`, context `admin@talos-homelab` |
| Credential | EC P-256 client cert, `CN=admin, O=system:masters`, 1-year |
| **Expires** | **2027-11-18 22:20 UTC** |
| Nodes | 5 — `talos-bbq-l1x`, `talos-h1j-0mp` (control-plane) and `talos-1b7-arm`, `talos-5t4-xse`, `talos-x3h-5tq` |

### ⚠ That kubeconfig path is fragile — it is a setup step, not a constant

`amp-work/5319/kube/admin.kubeconfig` lives in **another ticket's scratch
folder**. It is where #5319 minted the 1-year admin cert, and it works today, but
a scratch folder can be cleaned up at any time. Two things follow:

1. **Copy it somewhere of your own before you rely on it**, and export *your*
   copy — do not edit the 5319 folder, and do not edit the shared
   `~/.kube/config` (the `talos-homelab` context there and `talosctl`'s certs are
   both expired and are known infrastructure debt):
   ```bash
   mkdir -p ~/.kube/wargame && chmod 700 ~/.kube/wargame
   SRC=/Volumes/external-data/development/simstech/talos/pve-1/amp-work/5319/kube
   cp "$SRC"/{admin.kubeconfig,admin.crt,admin.key,ca.crt} ~/.kube/wargame/ \
     && chmod 600 ~/.kube/wargame/*
   export KUBECONFIG=$HOME/.kube/wargame/admin.kubeconfig
   ```
   The kubeconfig refers to `admin.crt`/`admin.key` **by relative path**, so all
   four files must sit in the same directory.
2. **When it is gone, re-mint it** — do not re-run #5319's folder out of
   existence, and do not mint a short credential. Full runbook:
   `amp-work/5319/kube/renew-admin-cert.sh`, project KB
   `ops/talos-admin-cert-and-migration-notices.md` and
   `ops/cluster-admin-access.md`. Always `-days 365`. A **24-hour** cert is
   exactly why cluster access rotted three times in November 2026 (#4887).

A later ticket is expected to move this credential to a stable, documented
location; when that lands, update `verify.yaml`'s `cluster.kubeconfig` and this
section, and neither worker nor document should be clever about the old path.

---

## War-game accounts: the Dex identities

Two simulated adults share one Home OS household, **"Sims Family (war game)"**.
Both are household owners. They sign in to the *product* through Dex, the same
way a real family member does — there are no test-only back doors.

| Persona | Dex email | Who they are | Dex password lives in |
|---|---|---|---|
| **Jacob** | `jacob.sims@simstech.cloud` | Setup, money, house, cars; heavy agent use, phone-first | `home-os` ns → Secret `home-os-secrets` → key `adminPassword` |
| **Amber** | `amber.wargame@simstech.cloud` | Kids' schedules, health, Thanksgiving, lists | `moltis` ns → Secret `home-os-amber-wargame-password` → key `password` |

> **The namespaces are not interchangeable, and this exact mistake cost two runs.**
> The war-game personas' passwords live in namespace **`moltis`**, not `home-os`.
> A worker who runs `kubectl -n home-os get secret home-os-amber-wargame-password`
> gets NotFound and reports "the wargame password was rotated". It was not; they
> were looking in the wrong namespace. (There is no `jacob.wargame@` account:
> Jacob's identity *is* `jacob.sims@simstech.cloud`.)

Read a password into a file, never onto a command line and never into a comment:

```bash
umask 077; mkdir -p ~/.wargame && chmod 700 ~/.wargame
kubectl -n moltis get secret home-os-amber-wargame-password \
  -o jsonpath='{.data.password}' | base64 -d > ~/.wargame/amber.pw
```

then `read -rs WG_AMBER_PASSWORD < ~/.wargame/amber.pw`, and scrub the file when
the round is over (`shred -u`). The persona **moltis instance** passwords are a
different credential again — see [Moltis instances](#moltis-instances).

**Signing in interactively** (browser, for 390px UI work): `https://homeos.simstech.cloud/login`
→ the Dex local form (`input[name=login]`, `input[name=password]`) → the member's
email and Dex password above. Then see [Browser driving](#browser-driving) about
the rate limiter, because **every fresh browser profile is a fresh login** and
Dex rate-limits repeated logins.

---

## Agent tokens, and why a REST 401 is correct

Each persona's moltis instance talks to Home OS with that member's **agent
token**: `hos_` + base64url(32 bytes), 47 characters.

```bash
kubectl -n moltis get secret home-os-token-wg-jacob -o jsonpath='{.data.token}' | base64 -d
kubectl -n moltis get secret home-os-token-wg-amber -o jsonpath='{.data.token}' | base64 -d
export HOMEOS_AGENT_TOKEN=...        # mcpcall.py reads this
```

`mcpcall.py` needs `HOMEOS_AGENT_TOKEN` and `HOMEOS_MCP_URL` (a **base** URL; the
script appends `/mcp`). The MCP is public, so a port-forward is usually
unnecessary:

```bash
export HOMEOS_MCP_URL=https://homeos.simstech.cloud
./mcpcall.py -list               # 90 tools
./mcpcall.py family_overview
```

### The token names, and the one that is dead

| Instance | Secret holding its MCP token | Works? |
|---|---|---|
| `moltis-wg-jacob` (war game) | `moltis/home-os-token-wg-jacob` | ✅ yes |
| `moltis-wg-amber` (war game) | `moltis/home-os-token-wg-amber` | ✅ yes |
| `moltis-jacob` (real household) | `moltis/home-os-token-jacob` | ✅ yes |
| `moltis-amber` (real household) | `moltis/home-os-token-amber` | ❌ **dead — 401** |

`home-os-token-amber` is stale. If a round needs Amber's token, use
**`home-os-token-wg-amber`**. `verify.yaml` encodes this so nobody has to
remember it. To see the actual wiring rather than trusting this table:

```bash
kubectl -n moltis get deploy -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.template.spec.containers[0].env[?(@.name=="MOLTIS_MCP_HOME_OS_TOKEN")].valueFrom.secretKeyRef.name}{"\n"}{end}'
```

### A `hos_` token is MCP-scoped **by design**. A REST 401 is CORRECT.

This has been misdiagnosed as a defect **twice**. It is not a defect, and it must
not be "fixed" in middleware.

| Credential | MCP `POST /mcp` | REST `/api/v1/*` | How you get it |
|---|---|---|---|
| `hos_…` agent token | **200** | **401 — always, by design** | Created by the member in a browser (`how-to/agent-tokens.md`); stored in the Secret above |
| Dex `id_token` (24h) | 200 | 200 | Copy the httpOnly `home-os-token` cookie from a signed-in browser |

**Why the boundary exists:** the REST API is where credentials are created and
destroyed — `POST/DELETE /api/v1/agent-tokens`, `/api/v1/auth/change-password`,
the admin routes. An agent token sits in an agent's own environment
(`MOLTIS_MCP_HOME_OS_TOKEN`), exactly where a prompt injection or one careless
tool call can reach it. If the API accepted one, a leaked agent token could mint
its own replacement or revoke a family member's credentials: a contained leak
would become a permanent one. Scoping agent tokens to the MCP is a hard ceiling on
the blast radius — household data yes, credentials never.
`apps/api/internal/middleware/auth.go` verifies Dex-issued OIDC tokens only, and
its 401 to a `hos_` token **is that ceiling working**.

So: **`mcpcall.py` takes an agent token; `api.py` takes a Dex id_token and
refuses a `hos_` one.** Port ground truth does not use REST at all.

**Getting a Dex id_token — the password grant is RETIRED (#4540).**
`POST /dex/token` with `grant_type=password` now answers
**`400 unsupported_grant_type`**, on purpose. Do not re-enable it to unblock a
round. Instead: sign in at `https://homeos.simstech.cloud` in a browser, DevTools
→ Application → Cookies → copy the httpOnly `home-os-token` cookie, then

```bash
read -rs HOMEOS_API_TOKEN           # paste; nothing echoed, nothing in history
export HOMEOS_API_URL=https://homeos.simstech.cloud
./api.py GET /api/v1/auth/me
```

**`/dex/token`, never `/token`:** against the Dex *pod* directly, the bare
`/token` is a **404** — that is the wrong path, not a broken deployment. The root
alias exists only as an Envoy `prefix_rewrite`.

**Several households.** A caller in more than one picks one per request with
`X-Household-ID: <household uuid>`. It must be one of their own memberships (else
403); with no header the oldest membership is used, and a malformed value is a
400. Both war-game personas are in the same household, **"Sims Family (war
game)"** (`family_overview` returns its id), so most rounds never need the header.

---

## Moltis instances

A **moltis instance** is one persona's agent: a web UI on port `13131` where you
type as the parent, in front of an LLM with the Home OS MCP attached as that
member. Each instance has **its own** login password, separate from the Dex
password in the table above.

| Persona | Instance (use this) | Service → address | Instance password Secret (`moltis` ns) |
|---|---|---|---|
| Jacob | **`moltis-wg-jacob`** | `moltis-wg-jacob` → `192.168.8.56:13131` | `moltis-wg-jacob-password` |
| Amber | **`moltis-wg-amber`** | `moltis-wg-amber` → `192.168.8.54:13131` | `moltis-wg-amber-password` |

(Addresses as of 2026-11-20. A LoadBalancer IP can be re-issued; if one does not
answer, read it rather than assuming the cluster is broken:)

```bash
kubectl -n moltis get svc moltis-wg-jacob moltis-wg-amber \
  -o custom-columns=NAME:.metadata.name,IP:.status.loadBalancer.ingress[0].ip,PORT:.spec.ports[0].port
```

**Do not play the war game on `moltis-jacob` / `moltis-amber` / `moltis-uitest`.**
Those are the real household's instances and the UI-test one; writing as them
puts war-game data in front of a real family. The war-game instances are the
`-wg-` ones. (`moltis-amber` additionally has the dead token.)

### Invoking an agent turn

```bash
export WG_JACOB_URL=http://192.168.8.56:13131
export WG_AMBER_URL=http://192.168.8.54:13131
read -rs WG_JACOB_PASSWORD      # moltis instance password, from the Secret above
read -rs WG_AMBER_PASSWORD
# optional: WG_TIMEOUT_S (default 600, the moltis run limit), WG_COOKIE_DIR

./chat.sh jacob cal 'Kid B soccer every Tue/Thu 5:30-6:30 through mid Dec, family calendar'
./chat.sh jacob cal --history          # what moltis actually stored and did
```

`chat.sh` logs in, sends the turn, waits for the reply, and prints each tool call
(`tool ok  mcp__home-os__event_create {...}`) followed by `REPLY:`. `TIMEOUT`
means no reply arrived in `WG_TIMEOUT_S` — the agent may still have written data,
so check `--history` **and** ground truth before scoring it.

No dependencies beyond `bash`, `curl` and `python3` (standard library). `./chat.sh --selftest` runs the
message-guard tests (22 of them).

### Run agent turns at most **2-wide**

Five concurrent turns degraded R2: 2 of 5 turns died and the survivors'
timings were useless. Two-wide has never degraded (R3: median ~75s, 0 failures in
5 turns). So: **at most two `chat.sh` processes alive at once**, and treat a
third as a measurement error you introduced. Sequence Amber and Jacob in pairs
(`&` … `wait`), and expect ~60–110s per turn.

---

## Browser driving

Product UI work at phone size is the other half of a round. Two ways, both fine:

- **Playwright** (if the repo's `e2e/` deps are installed):
  `chromium.launch()`, then `browser.newContext({ ...devices['iPhone 13'], viewport: { width: 390, height: 844 } })`.
- **Raw CDP, no packages** — this is what R3 used. A fresh
  `chrome --headless --remote-debugging-port=...` plus
  `Emulation.setDeviceMetricsOverride`:

  ```json
  {"device": {"width": 390, "height": 844, "deviceScaleFactor": 3, "mobile": true}}
  ```

  Send it **before** the first navigation, on the page session. Without the
  override you get a 800×600 desktop viewport and every mobile finding you write
  is fiction. Assert `document.documentElement.scrollWidth === 390` on each page —
  that is how horizontal overflow gets caught.

**Caveat — CDP-synthetic `Enter` does not submit the drawer search.** The mobile
More-drawer search is an implicit form submit, and
`Input.dispatchKeyEvent` with `key: "Enter"` fires `keydown`/`keyup` but never the
implicit submission. You will see the text in the box, no navigation, and if you
are not careful you will file it as a product bug. It is a **harness artifact**
(re-confirmed in R2 and R3). Either call `form.requestSubmit()` in
`Runtime.evaluate`, or click the submit affordance, or verify by hand on a real
phone. Anything you cannot drive this way is **unverified**, not failed — say so.

**Caveat — pace your logins, or reuse one profile.** Every fresh headless
profile is a fresh Dex login, and Dex rate-limits repeated logins per identity:
you get a bare `local_rate_limited` page. **Use one persistent browser profile
(`--user-data-dir`) for a whole batch of routes**, and if you must re-login, pace
them **≥30s apart**. The limiter is correct defence, not a defect to route
around; a session cookie that is still valid means you should not be logging in
again at all.

---

## Never-do list

1. **Never touch `avware-prod`.** Not that kube context, not that namespace. It
   is production for someone else; nothing in this game needs it. Never modify the
   user's default `~/.kube/config` either.
2. **Never use the GPU box.** It is not part of this game; agent turns run on the
   cluster's moltis instances.
3. **Never print a secret** — not a password, token, kubeconfig client key, Dex
   `id_token`, or `local_rate_limited`-adjacent cookie — to stdout, into a ticket
   comment, into a KB document, or into a file you commit. That includes
   `kubectl get secret -o yaml` pasted "for debugging", and includes a scratch
   JSON step-file that carries a password (this has actually happened: R3 found
   a plaintext Dex password in `amp-work/4585/steps-b1.json`). Read into a 0600
   file or a shell variable, use it, scrub it.
4. **Never `helm upgrade` / `helm uninstall` / `helm rollback` moltis, and never
   `kubectl edit`/`delete` its Deployment, Service or Secrets.** Moltis in the
   `moltis` namespace is **Argo CD-managed**: a manual Helm change is reverted by
   Argo (or worse, fights it), and a `helm uninstall` takes the `-wg-` instances
   and their tokens with it. If the war game needs an instance change, that is a
   ticket against the Argo `Application`, not a live `helm` command.
5. **Never sign in as the real household's personas** (`moltis-jacob`,
   `moltis-amber`) to play a round. War-game data goes in the war-game household
   only; the household is shared between the two simulated adults and anything you
   write is visible to both.
6. **Never re-enable the Dex password grant** (#4540) or weaken the agent-token
   REST boundary to get a call through. Both are deliberate security decisions;
   file a ticket instead.
7. **Never put a `$` amount through your shell** on the argv path — see the next
   section. It has already corrupted real war-game data.

*What is fine to write down:* these are RFC1918 LAN addresses in a private
homelab (`192.168.8.226`, `192.168.8.54`, …) and they are documented here
deliberately, because a control-plane address nobody can find is the failure this
file exists to end. Secret **values** never are.

---

## Never put a dollar amount through your shell

**Read this before your first turn that mentions money.** A `chat.sh` message
typed in **double** quotes is expanded by *your* shell before `chat.sh` starts,
and the damage is permanent: moltis stores what arrived, and the agent writes that
to Home OS.

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

Watch the heredoc delimiter: `<<'EOF'` (quoted) is safe, `<<EOF` (unquoted)
expands exactly like double quotes. `--stdin` fed from a double-quoted
`printf "%s" "copay $35"` is just as broken as the argv form — quoting discipline
applies to whatever builds the text, not only to the final command.

**This has already corrupted real data.** In R1, Jacob's `$2,340 / $180 / $75`
turn arrived as `,340 / 80 / 5` (and the agent rightly refused to guess), and a
concurrent Amber turn filed the Riverside Pediatrics $35 copay as a bill of
**`amount: 5`** with a document body `Well visit copay — 5.00` — the `$35` → `5`
signature. A `$` gone from the middle of the text is invisible: `electric 80`
looks exactly like a bill that really is 80.

### What `chat.sh` will and will not catch

It refuses and warns, and it cannot do more than that, because the expansion
happened before it ran:

| The message `chat.sh` receives | What it does |
|---|---|
| `mortgage is ,340 … 80 … 5` — a comma starting a 3-digit group with no digit before it | **refuses** (exit 2), sends nothing; proves a `$2,340` was eaten |
| `copay was 5` — a money word followed by a bare number and **no `$` anywhere** | warns: this is the shape left by an eaten `$` |
| `home is $HOME`, `$(date)`, `$[3]` … arriving as an **argument** | warns: this one survived, so it was quoted, but the path is unsafe |
| anything via `--stdin` / `--file` | silent — the shell never touched it |

`--allow-suspicious` sends a message the guard would refuse; `--allow-dollar`
stops the `$`-on-argv warning. Use them only when you are certain the text is
what you mean. **The guard is a backstop, not the fix.** After any turn that
mentioned an amount, read the `USER:` line back with
`./chat.sh jacob <session> --history` — that is the text the agent actually read.

---

## Running a round

Requirements: `bash`, `curl`, `python3` (standard library only), `kubectl`, `jq`
for the recipes below.

> **The MCP transport is Streamable HTTP at `POST <HOMEOS_MCP_URL>/mcp` — and
> nothing else.** Since #4361 the legacy HTTP+SSE transport (`GET /mcp/sse`,
> `POST /mcp/message`) is gone: both are **404s**. Don't count on SSE anywhere.
> `HOMEOS_MCP_URL` is a **base** URL (`https://homeos.simstech.cloud`), not the
> endpoint: the script appends `/mcp`. A value that already ends in `/mcp` is used
> as-is; anything else — notably `.../mcp/sse`, the old shape — becomes
> `<base>/mcp` and 404s. Off-cluster without public ingress, port-forward:
> `kubectl -n home-os port-forward svc/home-os-mcp 8082:8082 &` then
> `export HOMEOS_MCP_URL=http://127.0.0.1:8082`.
>
> Exit status is `0` ok, `1` the server refused the call (a JSON-RPC error, or the
> tool's own `isError`), `2` transport or usage — so a failed ground-truth read
> never looks like a pass.

### 0. Prove the environment

```bash
./verify.py          # 7/7 before you play; every FAIL says where to look
```

### 1. Baseline

Record what is in the household before play, so you can tell the agents' writes
apart from what was already there:

```bash
export HOMEOS_MCP_URL=https://homeos.simstech.cloud
export HOMEOS_AGENT_TOKEN=...            # jacob's, from home-os-token-wg-jacob
./mcpcall.py -list                       # what tools exist right now
./mcpcall.py family_overview              # people, calendars, counts, next 7 days
./mcpcall.py calendar_find
./mcpcall.py --text event_find '{"from":"2026-11-01","to":"2026-12-31"}' | jq '{returned, total}'
./mcpcall.py bill_find
./mcpcall.py note_find
```

### 2. Play a persona turn

Pick a scenario from `personas.md`. The session name is the topic; reuse it for
follow-ups, use a new one when the topic changes. **Two turns in flight, at most.**

```bash
./chat.sh jacob cal 'Kid B soccer every Tue/Thu 5:30-6:30 through mid Dec, family calendar'

# Anything with a $ in it: --stdin with a QUOTED heredoc (see the quoting section)
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

### 3. Check ground truth

Verify what the agent said it did, as this member and, for anything shared, as the
other member:

```bash
./mcpcall.py --text event_find '{"from":"2026-11-17","to":"2026-11-17"}' | jq '.items[] | {title,start,end}'
./mcpcall.py event_find '{"from":"2026-11-19T17:45:00-05:00","to":"2026-11-19T18:15:00-05:00"}'
./mcpcall.py calendar_find_free_time '{"from":"2026-11-19","duration_minutes":60,"start_hour":14,"end_hour":20}'
./mcpcall.py agenda_get '{"from":"2026-11-16"}'
./mcpcall.py bill_find '{"due_within_days":14}'
./mcpcall.py budget_summary '{"sections":["obligations"]}'

# as the other adult: change the token for one command
HOMEOS_AGENT_TOKEN="$(kubectl -n moltis get secret home-os-token-wg-amber -o jsonpath='{.data.token}' | base64 -d)" \
  ./mcpcall.py note_find
```

Tool names follow the v2 catalog (`<area>_<verb>`, project KB
`architecture/mcp-tool-catalog.md`); the exact registered set is `wantTools` in
`apps/mcp/cmd/mcp/protocol_test.go`. Argument names change as the MCP grows;
`./mcpcall.py -list` shows the current ones.

### 4. Score it

PASS / PARTIAL / FAIL per scenario, with the tool calls, the ground-truth result,
the reply, and the time taken. Write the round up in the project KB
(`discoveries/war-game-<round>-<persona>.md`), separating Home OS product gaps
from agent/harness artifacts — the CDP-Enter and `local_rate_limited` items above
are harness, not product.

### 5. Clean up

Delete your test events, notes and documents with ground truth, or leave them and
**list them** in the write-up (R3 deliberately left evidence behind). Remove any
probe pod you created — it may hold credentials. Scrub password files. Kill the
moltis sessions you opened? They are cheap; leave the names in your write-up so
the next round does not reuse them.

---

## Rules worth repeating

- **Never commit credential values.** `.env` files, tokens, passwords,
  kubeconfigs and secret bodies stay out of the repo — `tools/wargame/.gitignore`
  covers the usual scratch names. Secret **names** and paths are exactly what
  belongs in docs.
- Don't use real kids' names in personas or write-ups.
- Ground truth is read-only unless you are deliberately setting up a scenario.
  Writes as a member show up for the whole household.
- A turn you could not drive or could not verify is **unverified**, not a FAIL,
  and not a PASS.
- If something in this file is wrong or missing, **fix this file in the same
  round** — that is the whole point of it.
