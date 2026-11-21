# R1–R3 regression coverage — every finding, its guard, and how it was proved

**Ticket:** #5479. **Written:** 2026-11-20. **Base:** `feat/family-hub` @ 7ec984e.

The rule this file exists to enforce: **a regression test that has never been
seen to fail is not a regression test.** This project has shipped at least four
that could not fail — #4875 (compound matching), #5140 (wrong fixture type), the
grep-only envoy check that passed the config which took the site down (#5420),
and — found while writing this — `api.py`'s selftest, which printed FAIL and
exited 0. So every row below that says a test guards a finding was verified by
**applying the defect back and watching it go red**, then restoring it and
watching it go green. Rows that were not revert-checked say so.

Level preference: **unit > integration > e2e > live-smoke**. A live-smoke check
is used only where the defect is observable solely in a deployed environment.

---

## The number

**N = 41 findings from R1–R3.** Every one has a disposition:

- 14 R1 fix tickets (#4941–#4954)
- 13 R2 fix tickets (#5297–#5308, plus #5289 which the triage marked
  "already ticketed, do not double-ticket")
- 5 R3 ticketed defects (#5406–#5410)
- 9 R3 findings the triage upheld as **not defects** (they are rulings, so
  neither executable nor manual — they are listed in §4 so a future round does
  not re-litigate them)

**M = 25 of the 32 fix tickets are now executable** — a test fails if the defect
returns, at the cheapest faithful level.

**K = 21 of those 25 were verified to fail on regression**: the defect was
applied back, the suite went red, it was restored, green. The 4 that were not
revert-checked are named in §6 with the reason (three need a live
Postgres/Typesense this worktree does not have; one is e2e-only).

**J = 7 are documented as manual-only** in `tools/wargame/KNOWN-MANUAL.md`, each
with its reason and its procedure.

The arithmetic, so it can be checked rather than believed:

```
N  41 findings = 32 fix tickets + 9 upheld-no-defect rulings
M  25 executable + J 7 manual-only = 32 fix tickets              ✓
K  21 revert-checked + 4 not-revert-checked (§6) = 25 executable ✓
```

## 1. R1 — Stage 1 (14 findings)

R1's findings were mostly **agent honesty** and **MCP semantics**. The honesty
half is moltis guidance (a different repo) — see KNOWN-MANUAL §2.4. The MCP half
is code and is covered.

| # | Finding | Level | Guard | Revert-checked |
|---|---|---|---|---|
| #4941 | `document_upload` re-typed a statement; a `$35` copay was filed as `$5` with no provenance | integration | `apps/api/internal/file/provenance_test.go`, `apps/mcp/internal/file/provenance_test.go`, `apps/mcp/internal/bill/provenance_test.go` | not separately (its data layer predates this ticket; see §6) |
| #4942 | Guidance: quote the source line; never claim "verbatim" for re-typed content | — | **manual-only** (agent prose, moltis repo) | — |
| #4943 | Stale round-1 bill calendar events duplicated the bills module | integration | `apps/calendar/internal/caldav/superseded_test.go`, `apps/mcp/internal/testutil/legacy_bill_events_test.go`, `apps/api/internal/bill/calendar_test.go` | see #5301 row (same predicate) |
| #4944 | `event_find` with a bare query searched only the next 7 days | unit | `apps/mcp/internal/calendar/find_window_test.go` | **YES** — restore the 7-day window: 5 tests red |
| #4945 | Near-duplicate note detection missing | unit | `apps/mcp/internal/note/duplicates_test.go` | **YES** — threshold to 1.01: 4 subtests red |
| #4946 | REST 401s `hos_` tokens while MCP accepts them → wargame `api.py` documented an unusable path | unit (tooling) | `tools/wargame/api.py --selftest` (**new in #5479**) | **YES** — drop the refusal: exit 1, 2 cases red |
| #4947 | `notification_mark_read {all:true}` wiped another adult's unread state | integration | `apps/mcp/internal/notification/tools_test.go` (`TestMarkReadHasNoMarkAll`) | **YES** — reinstate mark-all: red |
| #4948 | `chat.sh` double-quote expansion ate `$2,340` → `,340`; `$35` → `5` | unit (tooling) | `tools/wargame/chat.sh --selftest` (existed; **wired into CI in #5479**) | **YES** — break the residue guard: exit 1, 5 FAIL |
| #4949 | `mcpcall.py` spoke legacy SSE; the server serves Streamable HTTP only | unit (tooling) | `tools/wargame/mcpcall.py --selftest` (existed; **wired into CI in #5479**) | **YES** — re-break the URL join and the SSE parse: 2 cases red |
| #4950 | No enforced `max_iterations`; ~590 s degenerate search tail | — | **manual-only** (moltis, deployed cluster) — `mcp-bench` covers tool p95 | — |
| #4951 | Asserted payment status no tool result supports | — | **manual-only** (agent prose) | — |
| #4952 | Reminder claimed delivered while `reminder_minutes` was null | — | **manual-only** (agent prose + moltis cron) | — |
| #4953 | Unsupported schedule claims (invented dates and prices) | — | **manual-only** (agent prose) | — |
| #4954 | Guidance: present disagreeing sources side by side | — | **manual-only** (agent prose) | — |

## 2. R2 — Stage 2 (12 findings)

| # | Finding | Level | Guard | Revert-checked |
|---|---|---|---|---|
| #5297 | Every money amount rendered 100× too large | **unit, at the API serialization boundary** | `apps/api/pkg/money/money_test.go`, `apps/api/internal/bill/handler_amount_test.go`, `apps/api/internal/bill/amount_db_test.go`, and the UI half `apps/ui/lib/money-surfaces.test.mjs` | **YES** — make `DecimalString` return the mantissa: every case red |
| #5298 | Search found nothing for events/bills | integration (needs Typesense) | `apps/api/internal/searchrecon/search_recon_integration_test.go`, `apps/api/internal/searchsweep/searchsweep_test.go` | not re-run (needs a live Typesense; the test's own header records the pre-fix failure) |
| #5299 | Documents counted a card showing "Other" as "Not filed yet" | unit | `apps/ui/lib/documents/model.test.mjs` | **YES** — treat `other` as unfiled: red |
| #5300 | Members said "Time zone not set" / "a owner" | unit | `apps/ui/lib/members/household-card.test.mjs`, `apps/ui/lib/members/roles.test.mjs` | **YES** — blank the zone read: red |
| #5301 | Assistant offered to delete bill-sync projections | integration | `apps/mcp/internal/calendar/projection_test.go` | **YES** — make nothing own its entry: 4 subtests red |
| #5302 | Today's "Add event" opened no form | e2e | `apps/ui/e2e/today-add-event.spec.ts` | not revert-checked (e2e; the spec asserts the sheet and a round-trip) |
| #5303 | Drawer search box inert while typing | unit + e2e | `apps/ui/lib/search/results.test.mjs`, `apps/ui/e2e/drawer-search.spec.ts` | **YES** (unit) — rename the href builder: red |
| #5304 | Back after login dead-ended on Dex "Bad Request" | **unit (new)** + e2e | `apps/ui/lib/auth/complete-decision.test.mjs`, `apps/ui/e2e/auth-back-session.spec.ts` | **YES** — auto-forward on Back: 4 tests red |
| #5305 | Meals nagged to plan a past day | unit | `apps/ui/lib/today/days.test.mjs`, `apps/ui/lib/recipes/week.test.mjs` | **YES** — break the day position rule: red |
| #5306 | Assistant said "all filed" with no document | integration | `apps/mcp/internal/bill/filing_test.go` | not revert-checked (its own header records the pre-fix shape) |
| #5307 | Calendar intermittently failed its first load after sign-in | unit | `apps/ui/lib/auth/session-readiness.test.mjs` | **YES** — rename the readiness rule: red |
| #5308 | Assistant turns died "agent run failed" under concurrent load | — | **manual-only** (moltis + deployed load) | — |

Also ticketed in R2, tracked elsewhere: **#5289** (CalDAV `http://localhost:8081`)
— `apps/ui/components/settings/caldav-server.test.mjs`, **YES** revert-checked.

## 3. R3 — Stage 3 ticketed defects (5)

| # | Finding | Level | Guard | Revert-checked |
|---|---|---|---|---|
| #5406 | Double-tapping a checkbox checked off TWO items | unit + e2e | `apps/ui/lib/lists/tap-guard.test.mjs`, `apps/ui/e2e/lists.spec.ts` | **YES, three ways** — the guard absent (red), the naive time-window debounce (4 red), the row-geometry test dropped (red). A naive debounce passes the reported case and fails 4 others; that is why the guard is position-aware. |
| #5407 | Throttled/expired Back-after-login landed on bare `local_rate_limited` and killed the session | **unit (new)** + helm + e2e | `apps/ui/lib/auth/complete-decision.test.mjs`, `deploy/helm/home-os/tests/envoy-local-reply.sh`, `e2e/auth-back-session.spec.ts` | **YES** — forward a signed-out visitor: 3 tests red |
| #5408 | Guides unreachable from mobile nav; FAB and tab bar covered content | unit + e2e | `apps/ui/components/shared/nav-config.test.mjs`, `apps/ui/e2e/tab-bar.spec.ts` | **YES** (unit) — remove the Guides nav entry: red |
| #5409 | `/welcome` told a member they had no family; duplicate CTA; shared h1 | unit + e2e | `apps/ui/lib/welcome/membership.test.mjs`, `e2e/onboarding.spec.ts`, `e2e/a11y.spec.ts` | **YES** (unit) — always answer "newcomer": red |
| #5410 | Meals literal `\u00b7`; account row's accessible name; CalDAV label monospace | **unit (new)** | `apps/ui/lib/jsx-text-escapes.test.mjs`, `apps/ui/lib/ui-structure.test.mjs` | **YES, all three** — each defect put back: red |

**#5410 had zero tests before this ticket**, across all three of its defects,
even though the triage marked it landed.

## 4. R3 — the 9 findings the triage upheld as no-ticket

Ruled correct by `discoveries/war-game-r3-triage.md` §2. Listed here so a future
round does not read them as regressions:

1. Empty-title Save "does nothing visible" — **contradicted by code**: the editor
   sets an inline error (`apps/ui/components/calendar/event-editor.tsx:138`).
   Diagnosed as a capture artifact. → manual live glance (KNOWN-MANUAL §2.1's
   companion check).
2. Account-menu popover uncapturable in headless — **manual** (KNOWN-MANUAL §2.2).
3. Today page very long — **judgment** (KNOWN-MANUAL §2.6).
4. Bills truncate at 390 px — harmful half fixed by #5408; residual is judgment.
5. Quick-actions clip at 320 px — judgment / below target.
6. Past event under "NEW" — copy policy, self-describing.
7. Three console 404s — verified not missing assets (all 17 icons + manifest
   present). A live console glance at release.
8. `/dashboard/today` 404s — no internal links; release note for #4512.
9. Meals defaults to this week late in the week — standard behaviour (#5305 is
   the real defect and is covered).

## 5. Non-product / ops (accounted, not defects)

The R3 harness-hygiene item (a plaintext Dex password in another workspace's step
file), the expired talos kubeconfig/talosctl certs, Dex's single-refresh-token
behaviour (#4649), and concurrent smoke-test noise. All four are in
KNOWN-MANUAL §2.7. They are not findings against this codebase and get no test.

## 6. What was NOT revert-checked, and why

Honesty about the suite's own gaps matters more than its headline number.

- **#4941, #5298, #5306** — integration tests needing a live Postgres (and, for
  #5298, Typesense). Their headers record the pre-fix failure in the words of the
  ticket, and #5298's says it goes to zero hits without `searchrecon`. Not
  re-run here because the harness has no migrated test database in this
  worktree; **CI runs them** (the `api` job with its Postgres service and the
  must-PASS Typesense step).
- **#5302** — e2e only. It asserts the sheet opens and an event round-trips, and
  is in the PR-gated `e2e` job, but the spec was not broken-and-restored.
- **The Envoy 429 page** (`envoy-local-reply.sh`) — its guard was revert-checked
  in its own ticket (#5407) and its *validator* in #5422/#5421, where the
  negative controls are permanent: the script fails if Envoy ever accepts an
  unescaped `%`, on a hand-written control AND on the shipped bytes. Re-checked
  again here for the `%%` escape (#5421): unescaping it exits 1 with
  `FAIL: envoy --mode validate REJECTS the rendered config`.

## 7. Reconciliation

| Bucket | Count |
|---|---|
| R1 fix tickets (#4941–#4954) | 14 |
| R2 fix tickets (#5297–#5308 + #5289) | 13 |
| R3 ticketed defects (#5406–#5410) | 5 |
| **Fix tickets (executable or manual)** | **32** |
| of which **executable** (M) | **25** |
| — of which **revert-checked red** (K) | **21** |
| — not revert-checked (§6) | 4 |
| of which **manual-only** (J) | **7** |
| R3 findings ruled *not defects* (§4) | 9 |
| **N — total findings** | **41** |

The 7 manual-only, by name: **R1** #4942, #4950, #4951, #4952, #4953, #4954
(agent prose, except #4950 which is moltis run limits) and **R2** #5308 (moltis
under concurrent deploy load). The live-Dex half of #5304/#5407 is described in
KNOWN-MANUAL §2.1 as an *additional* manual check on top of an executable test —
those two are counted as executable, because their decision logic is pinned at
unit level and revert-checked; only the real-Dex interaction stays manual.

## 8. CI

| Guard | Job in `.github/workflows/pr-checks.yml` |
|---|---|
| Go unit + integration (all `_test.go`) | `go-build` (calendar, mcp, worker, billcal, billdue) and `api` |
| UI unit (`node --test`, includes every `.test.mjs` above) | `ui-build` — `npm test` |
| Playwright e2e (Back-after-login, tap guard, nav, welcome, settings) | `e2e` |
| Envoy local-reply page + **real envoy --mode validate** | `helm-template` |
| Dex config on the RENDERED chart | `helm-template` |
| wargame tooling selftests (`chat.sh`, `mcpcall.py`, `api.py`) | `wargame-tools` (**new in #5479**) |
| MCP tool latency p95 | `mcp-bench` |

**Before #5479,** three of these guards existed and no job ran them: the
`chat.sh` and `mcpcall.py` selftests (R1 fixes #4948/#4949, green in CI by
absence), and `api.py` had no selftest at all. The `wargame-tools` job closes
that, and each of its three steps was revert-checked.
