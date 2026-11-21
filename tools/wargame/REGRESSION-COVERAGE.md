# R1–R3 regression coverage — every finding, its guard, and how it was proved

**Ticket:** #5479 (the inventory), audited by **#5482**. **Written:** 2026-11-20,
**audited 2026-11-21**. **Base:** `feat/family-hub` @ 7ec984e, on
`wargame/5479-regression-suite`.

The rule this file exists to enforce: **a regression test that has never been
seen to fail is not a regression test.** This project has shipped at least five
that could not fail — #4875 (compound matching), #5140 (wrong fixture type), the
grep-only envoy check that passed the config which took the site down (#5420),
`api.py`'s selftest (which printed FAIL and exited 0), and **#5407's
`auth-back-session.spec.ts`, which was green while the live bug remained**
(#5463, corrected by #5482 — see §0).

Level preference: **unit > integration > e2e > live-smoke**.

---

## 0. The audit (#5482) — and the correction it makes

#5479 counted **25 executable**. That number was wrong, and not because of a
miscount: it asked *"does a test fail if the defect returns?"* and never asked
*"does this test start where the person started?"* #5463 showed the difference
live. `auth-back-session.spec.ts` was revert-checked and genuinely red on the
#5407 defect, and it **passed while the product was broken**, because the
product broke on a path the test does not take:

| | The reported repro (#5407, R2 G5, R3 F1) | What the e2e spec did |
|---|---|---|
| Starts at | `/dashboard/calendar` **signed out** (a deep link) -> middleware -> Dex | the same — `page.goto(destination)` |
| Sign-in | the **Dex form**, submitted, a real history document | a `page.route` intercept of `/api/auth/login` |
| Back lands on | Dex re-requesting a **spent `state`** (400) or Envoy's 429 body | `/auth/complete`, an app page |
| Session after Back | **dead** (the next navigation bounces to Dex) | alive |

Its second test does assert the dead-session state, and its first does assert
"a navigation from there reaches the destination" — so §2's row for #5407 is
**not** a false green today. But its headline test could not have caught the
deployed failure, and that is the thing this audit is for. **#5481 owns fixing
it** (sign in through the deep-link repro). #5482 records the dependency and
does not touch that file.

### The question every row now answers

For each guard: **"reproduced via `<exact starting point>`; fails on regression
because `<what it would catch>`."** Three verdicts, and only the first is a
regression test:

- **FAITHFUL** — same starting point, same path, asserts the reported symptom
  (or the wrong value the person saw). Would go red if the symptom came back.
- **SUBSYSTEM** — pins the code that was wrong, at a different entry point than
  the person used: a pure function, a tool handler, a serializer. It is a real
  guard for the *logic*, and it would not catch a regression in the *wiring*.
  Counted separately, never as coverage of the finding.
- **PROXY** — asserts a stand-in for the symptom (a header, a count, "the tile
  exists") and would still pass if the person's symptom returned. **Not a
  regression test.** Fixed or demoted, per row.

### What the audit changed

| # | What was wrong | What it is now |
|---|---|---|
| **#5408** | The guard asserted a **tile was visible** in the More sheet. A tile that leads nowhere passes. "Unreachable" was the finding, and *reachable* is a tap that arrives. | The phone case taps Guides from the More sheet and requires `/dashboard/guides` **and its h1**; the desktop case taps its nav row the same way. Revert-checked (§6). **PROXY -> FAITHFUL.** |
| **#4948** | The selftest called `guard_message` **directly**, so deleting the call site in `main()` left it green while the script sent `,340`. A guard the *run* does not run is the same defect as a guard *CI* does not run. | The selftest executes the **real script** on the argv path: refusal must arrive as exit 2 + the reason, and an ordinary message must not be refused. Revert-checked (§6). **SUBSYSTEM -> FAITHFUL.** |
| **#4949** | Same drift, unguarded: the cases test `parse_response`/`rpc_url` in isolation, so `Client.post` could stop calling them. | Two wiring cases read the module's own source: `Client.post` still parses with `parse_response`, `main` still builds its URL with `rpc_url`. Revert-checked (§6). |
| **#5303** | Covered. The drawer-search spec opens the drawer, types, and counts requests — the reported path. | Unchanged; re-classified FAITHFUL with its exact starting point. |
| **#5302, #5305, #5289, #5299, #5300, #5307, #5406, #5409** | Covered, and covered faithfully — each has an e2e that starts on the page the report names. | Unchanged; each now carries its one-line standard. |
| **#5297's UI half** | `lib/money-surfaces.test.mjs` is a unit test of three formatters, counted as covering the finding on its own. | The finding's own level is the **API**, and that guard is FAITHFUL and revert-checked (§2). The UI unit is recorded as a **SUBSYSTEM** guard beside it, and the rendered total is asserted end to end in `money.spec.ts`. The row's verdict is the API's. |
| **#5410(a)** | `jsx-text-escapes.test.mjs` parses JSX text and asserts no backslash escape appears. There is **no render assertion** for the middle dot. | **SUBSYSTEM (source-shape).** It catches the defect returning, and it does not catch "the string is right and the glyph is missing". Recorded as such rather than rounded up. |

---

## The number

**N = 41 findings from R1–R3.** Every one has a disposition:

- 14 R1 fix tickets (#4941–#4954)
- 13 R2 fix tickets (#5297–#5308, plus #5289)
- 5 R3 ticketed defects (#5406–#5410)
- 9 R3 findings the triage upheld as **not defects** (rulings, listed in §4)

**The old claim was M = 25 executable, K = 21 revert-checked.** The audit
replaces it:

| | #5479 claimed | #5482 after audit |
|---|---|---|
| Fix tickets with at least one guard | 25 | **25** (unchanged — nothing lost its guard) |
| — of which **FAITHFUL** (same starting point and path as the finding) | not measured | **21** |
| — of which **SUBSYSTEM** (right code, different entry point) | not measured | **4** |
| — **PROXY** (would pass on the symptom) | not measured | **0** (two were found — #5408's visible tile and #4948's never-called guard — and both are fixed here. #5407's partial case is #5481's; its row reads SUBSYSTEM until then) |
| Manual-only (J) | 7 | **7** (unchanged) |
| Revert-checked and red on the applied-back defect | 21 | **21**, plus the 3 rewired here (§6); 4 still unverified (§6) |

**Read the faithful number, not the guarded number.** 25 findings have a guard
that would go red if their logic regressed. **21 have a guard that reproduces
the finding's own starting point and path.** For the other 4 a wiring
regression — the fix present in the function and absent from the page — would
not be caught. That is exactly the #5407 failure.

So the honest headline is **21 of 32 fix tickets are covered from the same
starting point the person used**, not 25 of 32, and not "21 verified to fail"
shorthand without saying which kind of coverage that is. The four that are not:
**#5407** (its e2e cannot reach a spent state — #5481), **#5410(a)** (a
source-shape rule, no render assertion), **#5306** (the field, not the
sentence), **#4949** (transport functions; the wiring is now pinned but the
round went through the network).

The arithmetic, so it can be checked rather than believed:

```
N  41 findings = 32 fix tickets + 9 upheld-no-defect rulings           ✓
   32 fix tickets = 25 guarded + 7 manual-only (J)                     ✓
   25 guarded     = 21 FAITHFUL + 4 SUBSYSTEM + 0 PROXY                ✓
```

---

## 1. R1 — Stage 1 (14 findings)

| # | Finding | Level | Guard | Verdict | The one line |
|---|---|---|---|---|---|
| #4941 | `document_upload` re-typed a statement; a `$35` copay was filed as `$5` with no provenance | integration | `apps/mcp/internal/file/provenance_test.go`, `apps/mcp/internal/bill/provenance_test.go`, `apps/api/internal/file/provenance_test.go` | **FAITHFUL** (tool-level) | reproduced via `document_upload` with the R1 paste, then `document_get`; fails on regression because the returned `text_origin`/fingerprint stop matching the bytes stored — the $5-for-$35 corruption cannot round-trip. Not separately revert-checked (§6). |
| #4942 | Guidance: quote the source line; never claim "verbatim" for re-typed content | — | **manual-only** (agent prose, moltis repo) | — | — |
| #4943 | Stale round-1 bill calendar events duplicated the bills module | integration | `apps/calendar/internal/caldav/superseded_test.go`, `apps/mcp/internal/testutil/legacy_bill_events_test.go`, `apps/api/internal/bill/calendar_test.go` | **FAITHFUL** (DB trigger) | reproduced via a real `bills` INSERT (migration 073's reconcile), then a CalDAV read; fails on regression because the retired duplicate reappears in the CalDAV answer beside the bill's own projection. The UI half is `calendar.spec.ts` §4's unlinked-entry case. |
| #4944 | `event_find` with a bare query searched only the next 7 days | unit | `apps/mcp/internal/calendar/find_window_test.go` | **FAITHFUL** | reproduced via `event_find` with the bare word the round used ("Christmas"), no dates; fails on regression because the December event comes back absent — the confident wrong absence. **YES**, red at a 7-day window (5 tests). |
| #4945 | Near-duplicate note detection missing | integration | `apps/mcp/internal/note/duplicates_test.go` | **FAITHFUL** | reproduced via `note_create` with the second of the two pickup-rule titles, in order; fails on regression because `possible_duplicates` comes back empty and the two contradictory notes coexist. **YES**, red at threshold 1.01 (4 subtests). |
| #4946 | REST 401s `hos_` tokens while MCP accepts them → wargame `api.py` documented an unusable path | unit (tooling) | `tools/wargame/api.py --selftest` | **FAITHFUL** | reproduced via `api.py` with `HOMEOS_API_TOKEN=hos_...` (the round's own mistake); fails on regression because the token is sent and the answer is a bare 401 instead of a refusal naming the reason. **YES**, red when the refusal is dropped (2 cases). |
| #4947 | `notification_mark_read {all:true}` wiped another adult's unread state | integration | `apps/mcp/internal/notification/tools_test.go` (`TestMarkReadHasNoMarkAll`) | **FAITHFUL** | reproduced via the exact call from the round — `{"all": true, "notification_ids": null}` — against three notifications including another adult's; fails on regression because `read_at` moves on rows the caller did not name. **YES**, red when mark-all is reinstated. |
| #4948 | `chat.sh` double-quote expansion ate `$2,340` → `,340`; `$35` → `5` | unit (tooling) | `tools/wargame/chat.sh --selftest` | **FAITHFUL** (rewired by #5482) | reproduced via **the script itself**, invoked as the round invoked it (the message as an argv), with `,340`/`$HOME`/`$?` payloads; fails on regression because the message reaches the transport instead of exiting 2 with the reason. **YES**, red with the call site removed — the old cases stayed green there, which is why it was rewired. |
| #4949 | `mcpcall.py` spoke legacy SSE; the server serves Streamable HTTP only | unit (tooling) | `tools/wargame/mcpcall.py --selftest` | **SUBSYSTEM** | reproduced via `parse_response`/`rpc_url`/`diagnose` on the bodies and URLs the round produced; fails on regression because an SSE-only reply or a doubled `/mcp/mcp` stops parsing. The **wiring** is now pinned too (`Client.post` calls `parse_response`; `main` calls `rpc_url`) — without that it would be SUBSYSTEM in the strict sense. **YES**, red both ways. |
| #4950 | No enforced `max_iterations`; ~590 s degenerate search tail | — | **manual-only** (moltis, deployed cluster) — `mcp-bench` covers tool p95 | — | — |
| #4951 | Asserted payment status no tool result supports | — | **manual-only** (agent prose) | — | — |
| #4952 | Reminder claimed delivered while `reminder_minutes` was null | — | **manual-only** (agent prose + moltis cron) | — | — |
| #4953 | Unsupported schedule claims (invented dates and prices) | — | **manual-only** (agent prose) | — | — |
| #4954 | Guidance: present disagreeing sources side by side | — | **manual-only** (agent prose) | — | — |

## 2. R2 — Stage 2 (12 findings, plus #5289)

| # | Finding | Level | Guard | Verdict | The one line |
|---|---|---|---|---|---|
| #5297 | Every money amount rendered 100× too large | **unit, at the API serialization boundary** | `apps/api/pkg/money/money_test.go`, `apps/api/internal/bill/handler_amount_test.go`, `apps/api/internal/bill/amount_db_test.go` (**the finding**); `apps/ui/lib/money-surfaces.test.mjs` + `apps/ui/e2e/money.spec.ts` (**the UI's half**) | **FAITHFUL** — the API guard is the finding's own level; the UI unit beside it is a **SUBSYSTEM** guard | reproduced via `bill_find`'s own amounts (`2340.00`) through the API's serialization; fails on regression because `DecimalString` returns the mantissa and every case reads `$234,000.00`. The UI unit pins the *other* direction (a formatter that divides again); the rendered total is asserted end to end in `money.spec.ts` (`$4,194.05`). **YES**, red with the mantissa restored. |
| #5298 | Search found nothing for events/bills | integration (needs Typesense) | `apps/api/internal/searchrecon/search_recon_integration_test.go`, `apps/api/internal/searchsweep/searchsweep_test.go` | **FAITHFUL** | reproduced via the reconcile from Postgres plus the real `search.Client` the UI's endpoint runs, with the round's three terms ("dentist", "soccer", "mortgage"); fails on regression because all five terms return zero hits — which is what the person saw. Not re-run (needs a live Typesense; the header records the pre-fix result). |
| #5299 | Documents counted a card showing "Other" as "Not filed yet" | unit + e2e | `apps/ui/lib/documents/model.test.mjs` (**unit**) + `apps/ui/e2e/documents.spec.ts` ("a document filed as Other is filed…") | **FAITHFUL** | reproduced via `/dashboard/documents` with a document whose category is `other`. The e2e is the faithful one: it asserts the tiles, the cards and the absence of the "No category yet" line together — the contradiction the person reported. Fails on regression because the Other document is counted as unfiled again. **YES**, red treating `other` as unfiled. |
| #5300 | Members said "Time zone not set" / "a owner" | unit + e2e | `apps/ui/lib/members/household-card.test.mjs` (**unit**) + `apps/ui/e2e/members.spec.ts` | **FAITHFUL** | reproduced via `/dashboard/members` on a family whose zone is `America/New_York`. The unit pins the *cause* (the capitalized wire shape of `GET /households/me`); the e2e pins the sentence. Fails on regression because the card says "Time zone not set" and "a owner" again. **YES**, red blanking the zone read. |
| #5301 | Assistant offered to delete bill-sync projections | integration | `apps/mcp/internal/calendar/projection_test.go` | **FAITHFUL** | reproduced via `event_find` on the Bills calendar and then the `event_delete` the assistant offered; fails on regression because the bill's projection is offered as deletable again — the family's bill view destroyed by a "yes". **YES**, red making nothing own its entry (4 subtests). |
| #5302 | Today's "Add event" opened no form | e2e | `apps/ui/e2e/today-add-event.spec.ts` | **FAITHFUL** | reproduced via **Today -> "Add event"**, the exact tap; fails on regression because the tap navigates to `/dashboard/calendar` again and the sheet never appears (0 inputs). Asserts the sheet, its fields and the POST, so a form that draws but saves nothing also fails. Not revert-checked (§6). |
| #5303 | Drawer search box inert while typing | unit + e2e | `apps/ui/lib/search/results.test.mjs` (**unit**) + `apps/ui/e2e/drawer-search.spec.ts` | **FAITHFUL** | reproduced via **open the nav drawer, tap Search, type "mortgage", wait** — the round's own steps at 390 px; fails on regression because the request count stays at zero (it did on 89b6c4e). Asserts the results too, so a request with no answer fails. **YES** (unit), red renaming the href builder. |
| #5304 | Back after login dead-ended on Dex "Bad Request" | **unit** + e2e | `apps/ui/lib/auth/complete-decision.test.mjs` + `apps/ui/e2e/auth-back.spec.ts` | **FAITHFUL** (e2e) | reproduced via `apps/ui/e2e/auth-back.spec.ts`: signed out, `page.goto("/dashboard/calendar")` — the deep link — then the **Dex form is a real local HTTP server** with a single-use `state`, submitted, then the real Back. Fails on regression because Back re-requests the spent state and the page becomes Dex's 400. This is the faithful sibling of #5407's spec (§0); the decision is also pinned at unit level. **YES**, red auto-forwarding on Back (4 tests). |
| #5305 | Meals nagged to plan a past day | unit + e2e | `apps/ui/lib/today/days.test.mjs`, `apps/ui/lib/recipes/week.test.mjs` (**unit**) + `apps/ui/e2e/meals.spec.ts` | **FAITHFUL** | reproduced via `/dashboard/meals` on a Wednesday, in the week Nov 16–22. The e2e is the finding (the nag sentence for a day that has gone); the UTC case is its second half. Fails on regression because "Mon is open — November 16 has no dinner planned" is drawn again. **YES**, red breaking the day-position rule. |
| #5306 | Assistant said "all filed" with no document | integration | `apps/mcp/internal/bill/filing_test.go` | **SUBSYSTEM** | reproduced via `bill_create` with no document, then again with one; fails on regression because `document_filed` stops being reported, which is the field the sentence must come from. What it cannot catch is the *sentence* — that stays manual (§5, KNOWN-MANUAL §2.4). Not revert-checked (its header records the pre-fix shape). |
| #5307 | Calendar intermittently failed its first load after sign-in | unit + e2e | `apps/ui/lib/auth/session-readiness.test.mjs` (**unit**) + `apps/ui/e2e/calendar.spec.ts` ("first load after a fresh sign-in") | **FAITHFUL** | reproduced via **a fresh sign-in -> straight to `/dashboard/calendar`** with the household list slow and the first event read answering 503 — the round's ordering; fails on regression because the error card is painted instead of riding out one not-ready answer, and because a read goes out before the household is known. **YES**, red renaming the readiness rule. |
| #5308 | Assistant turns died "agent run failed" under concurrent load | — | **manual-only** (moltis + deployed load) | — | — |
| #5289 | CalDAV server URL was `http://localhost:8081` | unit + e2e | `apps/ui/components/settings/caldav-server.test.mjs` (**unit**) + `apps/ui/e2e/settings.spec.ts` | **FAITHFUL** | reproduced via Settings -> Phone calendar and reading the **Server field's value**; fails on regression because the field holds a loopback address a phone cannot reach. The unit also forbids the literal in the component source. **YES**, red reintroducing the constant. |

## 3. R3 — Stage 3 ticketed defects (5)

| # | Finding | Level | Guard | Verdict | The one line |
|---|---|---|---|---|---|
| #5406 | Double-tapping a checkbox checked off TWO items | unit + e2e | `apps/ui/lib/lists/tap-guard.test.mjs` (**unit**) + `apps/ui/e2e/lists.spec.ts` | **FAITHFUL** | reproduced via `/dashboard/lists/<grocery>` and **two taps at the same client point ~120 ms apart** (not `.click()` twice, which re-aims by id and passes on the bug); fails on regression because the API is told to check two items — asserted on the wire (`backend.writes`), so it cannot be passed by drawing a tick. **YES, three ways** — guard absent, naive time-window debounce (4 red), row-geometry test dropped. |
| #5407 | Throttled/expired Back-after-login landed on bare `local_rate_limited` and killed the session | unit + helm + e2e | `apps/ui/lib/auth/complete-decision.test.mjs`, `deploy/helm/home-os/tests/envoy-local-reply.sh`, `apps/ui/e2e/auth-back-session.spec.ts` | **SUBSYSTEM** for the e2e — **#5481 owns the fix**; the helm and unit halves are FAITHFUL | The e2e signs in through a `page.route` intercept of `/api/auth/login`, **not** the Dex form and not a spent state, so its headline test passes on the live symptom (§0). Its dead-session case does assert the report. **Do not count this row as coverage of the finding until #5481 lands.** The unit pins the decision and the helm script pins the 429 page — neither can reach the real Dex limiter, which is why KNOWN-MANUAL §2.1 keeps the manual check. |
| #5408 | Guides unreachable from mobile nav; FAB and tab bar covered content | unit + e2e | `apps/ui/components/shared/nav-config.test.mjs` (**unit**) + `apps/ui/e2e/nav-placeholder-copy.spec.ts`, `apps/ui/e2e/tab-bar.spec.ts` | **FAITHFUL** (rewired by #5482) | reproduced via **the phone's More sheet -> tap Guides**, the one tap the report describes; fails on regression because there is no tile to tap (the pre-fix build) **or** the tap lands nowhere. The FAB half is `tab-bar.spec.ts`'s hit test at the last card's corner, measured on screen. **YES** — red removing the Guides nav entry; the new tap case is revert-checked in §6. |
| #5409 | `/welcome` told a member they had no family; duplicate CTA; shared h1 | unit + e2e | `apps/ui/lib/welcome/membership.test.mjs` (**unit**) + `apps/ui/e2e/onboarding.spec.ts`, `apps/ui/e2e/a11y.spec.ts` | **FAITHFUL** | reproduced via `/welcome` as a member with a family (the wizard-seeded session) — the report's starting point; fails on regression because the sign-in is redirected to Today again instead of being told "you're not part of a family yet", and because the first screen draws two Start buttons. The per-route h1 is `a11y.spec.ts`'s one-h1 rule over `/welcome` and every other route. **YES** (unit), red always answering "newcomer". |
| #5410 | Meals literal `\u00b7`; account row's accessible name; CalDAV label monospace | **unit (source-shape)** | `apps/ui/lib/jsx-text-escapes.test.mjs`, `apps/ui/lib/ui-structure.test.mjs` | **SUBSYSTEM** | The escape rule reproduces the defect *in source*: `\u00b7` in JSX text is six literal characters, and the AST scan finds it, self-checking that it still matches. It does **not** assert what any page renders, so "the source is right and the glyph is still wrong" is not caught — and #5410(a) is precisely a rendering complaint. The accessible name is asserted structurally at all three openers, and functionally by `account-menu.spec.ts` (`getByRole("button", {name: /^Account menu for/})`). **YES, all three**, red putting each defect back. |

**#5410 had zero tests before #5479**, across all three of its defects, even
though the triage marked it landed.

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
  unescaped `%`, on a hand-written control AND on the shipped bytes.

**Revert-checked in #5482** (defect applied back, suite red, restored, green):

- **#5408's new tap case.** Removing Guides from `NAV_SECTIONS` turns the phone
  case red on `getByRole("link", {name: /Guides/})` and the desktop case red on
  its nav row — the tile is gone, so the tap cannot start.
- **#4948's end-to-end case.** Deleting `guard_message "$MSG_RAW" "$SRC"` from
  the run leaves `--- 23/24 passed` and exits **1** with `FAIL the run is not
  refusing a comma residue: exit=2 out=set WG_NOSUCHPERSONA_URL`. Restored:
  **24/24**, exit 0.
- **#4949's wiring cases.** Replacing `Client.post`'s `return
  parse_response(raw, content_type)` with `return raw` exits **1** with `FAIL
  Client.post still parses the reply with parse_response`. Restored: **14/14**,
  exit 0.

**The four still counted SUBSYSTEM, and deliberately not rounded up:**
#5407's e2e (§0, #5481), #5410(a)'s source-only escape rule (§3), #5306's field
instead of the sentence (§2), and #4949's transport functions (§1 — its wiring
is now pinned, but the round went through the network and the selftest cannot).

## 7. Reconciliation

| Bucket | Count |
|---|---|
| R1 fix tickets (#4941–#4954) | 14 |
| R2 fix tickets (#5297–#5308 + #5289) | 13 |
| R3 ticketed defects (#5406–#5410) | 5 |
| **Fix tickets (guarded or manual)** | **32** |
| of which **guarded** (M) | **25** |
| — of which **FAITHFUL**: same starting point and path | **21** |
| — of which **SUBSYSTEM**: right code, different entry point | **4** |
| — of which **PROXY**: would pass on the symptom | **0** |
| of which **manual-only** (J) | **7** |
| R3 findings ruled *not defects* (§4) | 9 |
| **N — total findings** | **41** |

The 7 manual-only, by name: **R1** #4942, #4950, #4951, #4952, #4953, #4954
(agent prose, except #4950 which is moltis run limits) and **R2** #5308 (moltis
under concurrent deploy load). The live-Dex half of #5304/#5407 is described in
KNOWN-MANUAL §2.1 as an *additional* manual check on top of an executable test —
#5304 is counted FAITHFUL (its e2e drives a real Dex stand-in with a single-use
state), and #5407 is counted SUBSYSTEM until #5481 lands.

## 8. CI

| Guard | Job in `.github/workflows/pr-checks.yml` |
|---|---|
| Go unit + integration (all `_test.go`) | `go-build` (calendar, mcp, worker, billcal, billdue) and `api` |
| UI unit (`node --test`, includes every `.test.mjs` above) | `ui-build` — `npm test` |
| Playwright e2e (Back-after-login, tap guard, nav, welcome, settings, drawer search, money, documents, members, meals) | `e2e` — `npx playwright test`, whole suite |
| Envoy local-reply page + **real envoy --mode validate** | `helm-template` |
| Dex config on the RENDERED chart | `helm-template` |
| wargame tooling selftests (`chat.sh`, `mcpcall.py`, `api.py`) | `wargame-tools` |
| MCP tool latency p95 | `mcp-bench` |

**Before #5479,** three of these guards existed and no job ran them: the
`chat.sh` and `mcpcall.py` selftests (R1 fixes #4948/#4949, green in CI by
absence), and `api.py` had no selftest at all. The `wargame-tools` job closes
that, and each of its three steps was revert-checked.

**#5482's addition to that table's logic:** a guard must be wired into the
*run*, not only into CI. #4948's cases were green with the guard disconnected —
the same defect one level up.

## 9. Adding a test after #5482 — the extra question

The #5479 checklist (level, revert-check, CI job, self-check, KNOWN-MANUAL) still
applies. One item joins it, and it is the one that would have caught #5407:

- [ ] **The test starts where the person started.** Name it in the row: the URL,
  the state, the tap. If the test begins from a stubbed sign-in, a seeded
  session, or a direct API call, say so, and say which part of the person's path
  it therefore cannot cover — **or fix it so it does.**
- [ ] **The assertion is the symptom, not a proxy.** "The tile exists" is not
  "Guides is reachable"; "the header is set" is not "the number is right"; "the
  request was made" is not "the answer appeared". If the only assertion you can
  write is a proxy, write the honest row: **SUBSYSTEM**, and say what wiring it
  does not cover.
