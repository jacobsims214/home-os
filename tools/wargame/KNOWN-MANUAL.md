# Known findings that cannot be automated — the deliberate manual gap

**Read this before planning a war-game round or a release.** Every R1–R3 finding
is accounted for somewhere: either a regression test now fails if the defect
returns, or it is listed here with the reason automation is impossible. Nothing
is silently dropped.

The point of this file is that the gap is **deliberate**. A finding listed here
is not forgotten and not "probably fine" — it is a check a person has to make,
and it says which person and when.

**Companion:** `tools/wargame/REGRESSION-COVERAGE.md` is the full inventory with
each finding's level (`unit > integration > e2e > live-smoke`) and the test that
guards it.

---

## 1. Why some findings cannot be automated

Four reasons recur. They are named here so a future round can tell "we chose not
to" from "nobody got to it":

- **LIVE-ONLY** — the defect is only observable in a deployed environment: real
  Dex, real Envoy, real moltis. The repo's e2e runs against a stub server, so it
  cannot reach these by construction.
- **JUDGMENT** — there is no assertion to write. "The page feels long", "this
  label is cramped" has a threshold a person sets, and a test would only pin one
  arbitrary number.
- **PHYSICAL** — needs hardware the CI runner does not have: a real phone's
  touch stack, a phone camera, a CalDAV client (iOS/Android/macOS Calendar).
- **AGENT-PROSE** — the finding is about what a model *said*, not what code did.
  Guidance quality is not a unit test; the deterministic half (a tool refusing,
  a field being null) is tested instead, and the prose half is replayed by a
  round.

---

## 2. The manual-only findings

### 2.1 Back-after-login through REAL Dex — LIVE-ONLY (partly covered)

**Findings:** #5304 (R2), #5407 / Amber F1 / Jacob N2 (R3). The one that
regressed twice.

**What IS automated now** (this is the important part — do not re-do it):
- The *decision* is pinned at unit level: `apps/ui/lib/auth/complete-decision.ts`
  + `complete-decision.test.mjs`. Both historical defects are revert-checked: put
  the #5407 forward-a-signed-out-visitor back and 3 tests go red; put the #5304
  auto-forward-on-Back back and 4 go red.
- The *history behaviour* is pinned by `apps/ui/e2e/auth-back-session.spec.ts`
  against the stub (Back lands on an app page, the session survives, no raw error
  body).

**What is still manual, and why:** the real failure is the interaction between
the app and **Dex's** login rate limiter — Dex's own `local_rate_limited` 429
body, reached because a spent `state` or a dead session sends the browser back
through `/dex/auth/local`. The e2e suite replaces Dex with `e2e/stub-server.mjs`,
which has no rate limiter and no spent-state behaviour, so it cannot produce the
condition. The Envoy half of it (that the 429 is a readable PAGE) is automated in
`deploy/helm/home-os/tests/envoy-local-reply.sh`.

**Who checks it, when:** the release ticket's live re-test, on every deploy that
touches `/auth`, `middleware.ts` or the envoy config. Procedure: sign in from a
deep link (`/dashboard/calendar`), press Back, then navigate again. PASS = an
app-owned page with a way forward, and the next navigation reaches the
destination.

### 2.2 The account-menu popover in headless Chrome — JUDGMENT/harness

**Finding:** Jacob N4 (R3). The popover's contents could not be captured in
headless in three attempts; Amber and the designer both captured it fine.

**Why not automated:** this was diagnosed as a harness artifact, not a product
defect (`discoveries/war-game-r3-triage.md` §2.2), and the triage's ruling is
upheld. The *reachability* of every account action IS covered by
`apps/ui/e2e/account-menu.spec.ts` (three viewports, the theme picker, Profile).

**Manual check:** one tap on the avatar on a real phone, confirming the sheet
opens with its identity row. Only worth repeating if a report says otherwise.

### 2.3 CalDAV end-to-end on a real phone — PHYSICAL

**Finding:** R3 N7 / Jacob's CalDAV pass. Both rounds confirmed the *copy and
config* (server URL, app password, per-platform steps) but neither added an
account to a real iPhone/Android/macOS Calendar.

**What IS automated:** the server URL can never be `localhost` and never a
docker-compose port (`apps/ui/components/settings/caldav-server.test.mjs`, #5289,
revert-checked); the steps and fields render (`e2e/settings.spec.ts`); the API's
`server_url` comes from `PUBLIC_URL`.

**Why not automated:** the assertion that matters is "Apple Calendar accepts this
account and syncs", which needs the device. `apps/api`/`apps/calendar` CalDAV
protocol tests cover the server side.

**Manual check:** at a release, add the account on one real phone and confirm one
event syncs both ways. The setup page itself says the usual failure ("a missing
https://") — read that first.

### 2.4 Agent prose discipline — AGENT-PROSE

**Findings:** #4942, #4951, #4952, #4953, #4954 (R1) and the agent-side halves
of #5301/#5306 (R2). These are about a model *asserting beyond its tool result*:
claiming a payment status no tool returned, inventing a schedule, saying "all
filed" with no document.

**Why not automated here:** the fixes are moltis guidance (a different repo) plus
tool-level guards. The **deterministic** halves are tested, and those are the
ones that regress silently:
- a bill's `document_filed` comes from `source_document_id`, never prose
  (`apps/mcp/internal/bill/filing_test.go`, #5306);
- an owned calendar entry says who keeps it and `event_delete` refuses
  (`apps/mcp/internal/calendar/projection_test.go`, #5301, revert-checked);
- `notification_mark_read` has no mark-all (`tools_test.go`, #4947,
  revert-checked);
- `event_find` reports what it searched (`find_window_test.go`, #4944,
  revert-checked).

**What stays manual:** whether the agent's *sentence* matches those fields. That
is what a war-game round measures, and it is why rounds still exist. Do not try
to snapshot model prose in CI: it will pass on one model temperature and fail on
the next, which is the "test that creates false confidence" failure mode.

### 2.5 MCP/agent latency under concurrent load — LIVE-ONLY

**Findings:** #4950 (R1, ~590s degenerate tail), #5308 (R2, "agent run failed"
2-of-5 wide).

**What IS automated:** `mcp-bench` in CI runs every tool over the authed `/mcp`
handler with a p95 budget of 100 ms (KB `testing/mcp-latency-benchmark.md`). That
covers tool latency, which is this repo's half.

**Why not automated:** both findings are about **moltis** killing or degrading a
run at concurrency, in a deployed cluster with real model calls. The R2 triage
records the root cause as moltis's process-global `McpManager`
(KB `moltis/agent-run-failed-concurrency-shared-mcp-client.md`) — a different
repo. A CI job here cannot mount that.

**Manual check:** every round runs agent turns ≤2-wide and records wall time;
the R4 method notes say so. A round that sees a 5-wide run is measuring a
configuration nobody deploys.

### 2.6 UX judgment calls — JUDGMENT

Each was ruled "not a defect" by the triage. Listed so a future round does not
re-litigate them as regressions:

| Finding | Source | Why it stays a person's call |
|---|---|---|
| Today page is very long; People at the bottom | R3 designer | IA work, not a defect. Amber and Jacob both PASS the layout. |
| Bills list truncates names hard at 390 px | R3 designer | The harmful half (FAB overlap) is fixed and tested (#5408). The residual truncation has a working recourse: tapping opens the detail. |
| Today quick-actions row clips at 320 px | R3 designer | Below the 390 px target; the row scrolls. |
| Meals defaults to the week containing today, late in the week | Amber F4 (R3) | Mon-anchored "this week" is every calendar's behaviour. The real defect (nagging about past days, #5305) is fixed and revert-checked. |
| Agent-token prefix shows ~12 chars; "Never expires" has no visual weight | R3 designer | Reporter's own verdict: "taste-adjacent but defensible". A design pass, not a war-game fix. |
| CalDAV server URL truncates at 320 px; theme labels cramped | R3 designer | Reporter's own verdict "acceptable"; Copy exists. |
| Notification sheet shows a past event under "NEW" | R3 designer | Self-describing (relative timestamp + one-tap Mark read). A copy-policy tweak for R4 polish. |
| Dex's stock login form is unbranded and does not link `/reset-password` | R3 designer | Outside Home OS's own UI; the app has its own `/reset-password`. |
| `/onboarding` 404s; the real surface is `/welcome` | Jacob (R3) | Nothing links to it. Worth deleting the dead middleware allowlist entry in a future pass; not a defect. |
| UI and agent differ in event-creation defaults | Jacob (R3) | The report's own word: "not wrong". |

### 2.7 Non-product findings (recorded, not defects)

- **The `amp-work/4585/steps-b1.json` plaintext Dex password** (R3 triage §4.2):
  a harness-hygiene issue for that workspace's operator, not this repo.
- **Expired `~/.kube/config` talos-homelab context and talosctl certs** (R3
  triage §4.3): infrastructure debt. A working kubeconfig exists at
  `amp-work/5319/kube/admin.kubeconfig`. Out of scope for the product.
- **Dex's single-refresh-token behaviour (#4649)**: documented as expected. A
  real browser left signed in holds a revoked token until re-auth. Nobody should
  chase a phantom logout.
- **Concurrent smoke-test noise when two rounds share a household**: a method
  note. Rounds should serialize or use separate households.

---

## 3. Adding to this file

Add an entry when a finding cannot be automated, and say **which of the four
reasons** applies and **what the manual check is**. "Too hard" is not a reason;
"needs a real phone" is. If a finding here later becomes automatable (a stub
learns the behaviour, a runner gets a device), move it to a test and delete the
entry — this file should shrink over time, not grow.

Last reviewed: 2026-11-20 (#5479).
