# The family year — coverage

Twelve months of one American household, in four seasonal arcs, at
`tools/wargame/scenarios/year/<season>/<NN-month>/`. This file answers one question: **which
platform surface does which month exercise?** A gap must be visible here, not assumed.

Generated against the corpus by `./scenarios_load.py --coverage --markdown` and
`--json`; `./scenarios_load.py --coverage` prints the same matrix as plain text and **exits
1** if any surface below has no month, if a month is empty, or if fewer than half the
scenarios run both channels. The selftest runs that gate too, so a year that silently loses
a surface (one scenario's `surface:` retyped) fails CI rather than showing a tidy table.

## Surface × month

A cell is the number of scenarios that month exercises that surface; `·` is none. **All 19
required surfaces appear.**

| surface | 03 Mar | 04 Apr | 05 May | 06 Jun | 07 Jul | 08 Aug | 09 Sep | 10 Oct | 11 Nov | 01 Jan | 02 Feb | 12 Dec | total |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| `bills` | 1 | · | 1 | · | · | · | · | 1 | · | · | 1 | 1 | 5 |
| `calendar` | · | 1 | · | · | 1 | · | 1 | · | · | · | · | · | 3 |
| `meals` | · | · | · | · | · | 1 | 1 | · | · | · | · | · | 2 |
| `lists` | · | · | 1 | · | 1 | · | · | · | · | · | · | · | 2 |
| `documents` | · | 1 | 1 | 1 | · | 1 | · | · | · | 2 | · | 1 | 7 |
| `people` | · | · | · | · | · | · | 1 | · | · | · | · | · | 1 |
| `properties` | · | · | · | · | · | · | · | · | 1 | · | · | · | 1 |
| `assets` | · | · | · | · | · | · | · | 1 | · | · | · | · | 1 |
| `vehicles` | · | · | · | · | 1 | · | · | · | · | · | · | · | 1 |
| `pets` | · | · | · | · | · | 1 | · | · | · | · | · | · | 1 |
| `vendors` | · | 1 | · | 1 | · | · | · | · | · | · | · | · | 2 |
| `maintenance` | 1 | · | · | · | · | · | · | 1 | · | · | · | · | 2 |
| `guides` | · | · | · | · | · | · | 1 | · | · | 1 | · | · | 2 |
| `kb` | · | · | · | · | · | · | · | · | · | 1 | · | · | 1 |
| `money` | · | · | · | 1 | 1 | · | · | · | 1 | · | · | 1 | 4 |
| `search` | · | · | 1 | · | · | · | · | · | · | · | · | · | 1 |
| `settings` | · | · | · | · | · | · | · | · | · | · | 1 | · | 1 |
| `notifications` | 1 | · | · | · | · | · | · | · | 1 | · | · | · | 2 |
| `invites` | · | · | · | · | · | · | · | · | · | · | 1 | · | 1 |
| **scenarios** | **3** | **3** | **4** | **3** | **4** | **3** | **4** | **3** | **3** | **4** | **3** | **3** | **40** |

Read it down a column to see what a month actually asks of the platform, and across a row to
see how often a surface is load-bearing. It is deliberately uneven: `documents` is exercised
in six months because filing a piece of paper is what a household does constantly, while
`vehicles` appears once because a registration renews once a year. A surface appearing once
is covered; a surface appearing zero times is a gap, and the loader fails on it.

## Seasons

| Season | Months | Scenarios | Life-events |
|---|---|---|---|
| Spring | March, April, May | 10 | property tax bill; furnace filter; fence quote + filed quote; sports registration deadline; lost septic inspection surfaces |
| Summer | June, July, August | 10 | plumbing emergency (caller, quote, paid, receipt filed); vehicle registration; cabin trip + packing list; dog's vet visit + vaccination record; trip meals |
| Fall | September, October, November | 10 | school forms, bus schedule, teacher guide; furnace inspection **fails** + asset flagged; insurance renewal and its budget effect; cabin winterization |
| Winter | December, January, February | 10 | burst-pipe claim + 3 documents attached; holiday budget; 2026 tax document; power outage via guide and KB; a subscription found and cancelled; a member invited |

**40 scenarios**, 36 of them `channel: both`.

## The two new fields

`channel` is `agent`, `ui` or `both`. All but four scenarios are `both`, because the point of
the year is that the two channels agree with each other; the four single-channel ones exist so
the field is genuinely a field (and because a family sometimes only uses one):

| id | channel | why only that channel |
|---|---|---|
| `jacob-summer-whats-due-agent` | agent | "five minutes before a call" — a read, answered in chat |
| `jacob-winter-tax-gather-agent` | agent | gathering 2026 documents is a sweep of what exists |
| `amber-spring-bill-mark-paid-ui` | ui | standing at a counter, ticking off a bill, no conversation |
| `amber-fall-meal-tonight-ui` | ui | 4:40pm, "what are we having" — a page, not a question |

`parity` is required whenever `channel: both`, and is **the fact that must match** between
what the agent reports and what the UI renders — an amount, a date, a status, a count. The
loader refuses `parity` on a single-channel scenario (one channel cannot disagree with
itself) and refuses `both` without one. The parity fact per scenario is the last column of
each month below; it is the one thing a cross-channel check compares, so a scenario whose
parity is "looks right" is not in this corpus.

Example: `jacob-summer-plumbing-invoice-paid` has parity "this month's obligations total:
`budget_summary.totals` and the total rendered on the Money page are the same amount, in
cents". A run of it either has both numbers and they match, or it fails.

## Spring

### March — 3 scenarios

| scenario | surface | channel | parity: the fact that must match |
|---|---|---|---|
| `amber-spring-tax-bill-reminder` | notifications | both | the reminder count: notification_find returns exactly 1 notification naming the School Tax bill, and the notifications page renders exactly 1 row for it with the same bill title and amount |
| `jacob-spring-furnace-filter` | maintenance | both | the next filter-change date: maintenance_find's next_due and the date the Furnace row renders are the same calendar day |
| `jacob-spring-property-tax-bill` | bills | both | the amount and due date of the School Tax row: bill_find's amount/due_date and the digits in the row's amount text and its due-date text are the same two values |

### April — 3 scenarios

| scenario | surface | channel | parity: the fact that must match |
|---|---|---|---|
| `amber-spring-fence-quote` | vendors | both | the contractor's phone number: contact_find's phone and the tel: href on the vendor page carry the same digits |
| `amber-spring-fence-quote-document` | documents | both | the quote amount and its filed-to target: document_find's amount (4850.00) and the property name render identically on the document row |
| `amber-spring-sports-registration-deadline` | calendar | both | the deadline date: event_find's start date, the calendar cell it renders in, and the date the reply names are all 2027-03-20 |

### May — 4 scenarios

| scenario | surface | channel | parity: the fact that must match |
|---|---|---|---|
| `amber-spring-bill-mark-paid-ui` | bills | ui **ui only** | — |
| `amber-spring-find-lost-document` | search | both | the result count: search_all's returned count and the number of result rows on the search page are the same integer, for the same query string |
| `amber-spring-sports-gear-list` | lists | both | the item count: list_get's item count and the number rendered in the list header are the same integer |
| `jacob-spring-septic-document-filed` | documents | both | the document's filed-to target: document_find's property name and the row's filed-to label are the same property |

## Summer

### June — 3 scenarios

| scenario | surface | channel | parity: the fact that must match |
|---|---|---|---|
| `amber-summer-plumbing-emergency` | vendors | both | the plumber's phone number: the digits in the reply, contact_find's phone and the tel: href all agree |
| `amber-summer-plumbing-receipt-filed` | documents | both | the receipt amount: document_find's amount for the receipt and the paid bill's amount are the same 640.00 |
| `jacob-summer-plumbing-invoice-paid` | money | both | this month's obligations total: budget_summary.totals and the total rendered on the Money page are the same amount, in cents |

### July — 4 scenarios

| scenario | surface | channel | parity: the fact that must match |
|---|---|---|---|
| `amber-summer-cabin-trip-calendar` | calendar | both | the trip's length and dates: event_find's start/end and the dates the calendar block spans are the same 2027-07-18 to 2027-07-25 |
| `amber-summer-trip-packing-list` | lists | both | the packing item count: list_get's item count and the number in the list header are the same integer |
| `jacob-summer-vehicle-registration-renewal` | vehicles | both | the van's registration expiry: vehicle_find's registration_expires and the date on the Odyssey card are the same day |
| `jacob-summer-whats-due-agent` | money | agent **agent only** | — |

### August — 3 scenarios

| scenario | surface | channel | parity: the fact that must match |
|---|---|---|---|
| `amber-summer-dog-vet-visit` | pets | both | the rabies expiry date: pet_find's vaccination date and the date rendered on Biscuit's card are the same day |
| `amber-summer-trip-meals` | meals | both | the filled dinner slots: the count of set meals meal_set reports for the week and the number of filled dinner cells on the meals page are the same integer |
| `amber-summer-vaccination-record-filed` | documents | both | the rabies expiry: the date in the filed certificate and pet_find's vaccination date are the same day |

## Fall

### September — 4 scenarios

| scenario | surface | channel | parity: the fact that must match |
|---|---|---|---|
| `amber-fall-bus-schedule-calendar` | calendar | both | the two bus times and their weekday count: event_find's start times and the occurrences over two school weeks match the times and count the calendar shows |
| `amber-fall-meal-tonight-ui` | meals | ui **ui only** | — |
| `amber-fall-school-forms-people` | people | both | the children's ages and grades: person_find's birthdate-derived age and grade for each child match what each child's page renders |
| `jacob-fall-teacher-contact-guide` | guides | both | the teacher's phone number: contact_find's phone for Riverside Elementary, the number in the guide's body and the number the guide page renders are the same digits |

### October — 3 scenarios

| scenario | surface | channel | parity: the fact that must match |
|---|---|---|---|
| `jacob-fall-furnace-asset-flags` | assets | both | the furnace's condition: the text asset_find returns for the Furnace and the condition shown on the asset page are the same string |
| `jacob-fall-furnace-inspection-fails` | maintenance | both | the furnace task's status and due date: maintenance_find's status and due date match the badge and the date the maintenance page renders |
| `jacob-fall-insurance-renewal` | bills | both | the renewed premium: bill_find's amount (1940.00) and the amount on the Insurance row are the same figure |

### November — 3 scenarios

| scenario | surface | channel | parity: the fact that must match |
|---|---|---|---|
| `amber-fall-insurance-budget-impact` | money | both | the three monthly obligation totals: budget_summary.totals and the three totals on the Money page are the same amounts, in cents |
| `jacob-fall-cabin-winterization` | properties | both | the cabin's winterization step count: the number of steps attached to the cabin and the number rendered on the cabin page are the same integer |
| `jacob-fall-furnace-followup-notification` | notifications | both | the furnace reminder count: notification_find's notifications for the furnace task and the rows rendered for it on the notifications page are the same number |

## Winter

### December — 3 scenarios

| scenario | surface | channel | parity: the fact that must match |
|---|---|---|---|
| `amber-winter-claim-documents-attached` | documents | both | the attached document count: document_find's three claim documents and the number of documents grouped under the claim on the documents page are the same integer |
| `amber-winter-holiday-budget` | money | both | the gifts budget and what is left: budget_summary's category limit and remaining amount match the two figures rendered on the Money page |
| `jacob-winter-burst-pipe-claim` | bills | both | the claim number and amount: bill_find's 44-2210 and 6400.00 appear identically on the bills row and in the reply |

### January — 4 scenarios

| scenario | surface | channel | parity: the fact that must match |
|---|---|---|---|
| `amber-winter-power-outage-guide` | guides | both | the outage guidance: the hours the reply states and the hours in the guide's own body are the same number, and the guide's address is the one guide_find returns |
| `amber-winter-power-outage-kb` | kb | both | the outage doc's path and its freezer window: kb_get's path and hours match the page the reference viewer opens and the hours it renders |
| `jacob-winter-tax-document` | documents | both | the 2026 interest amount: document_find's amount for the 1098 equals the amount rendered on its documents row |
| `jacob-winter-tax-gather-agent` | documents | agent **agent only** | — |

### February — 3 scenarios

| scenario | surface | channel | parity: the fact that must match |
|---|---|---|---|
| `amber-winter-members-invite` | invites | both | the pending invite: its address and role in the invites payload are the address and role rendered on the members page, and the pending count matches |
| `jacob-winter-subscription-cancelled` | bills | both | the change to this month's obligations: budget_summary's total drops by exactly 19.99, and the bills page shows one fewer row |
| `jacob-winter-subscription-found` | settings | both | the recurring charge's amount and payee: bill_find's 19.99 and the payee it names appear identically in the settings page and in the reply |

## What this corpus does not cover, on purpose

Every scenario exercises one surface primarily. Cross-surface chains (a document filed from a
vendor, which only exists because a contractor quoted) are the household's story, not a
surface, and are recorded in the `steps` rather than in a seventh coverage row for "workflow".

`onboarding` and `auth` are absent: a household living its twelfth month is not signing up or
logging in, and the R1–R3 corpus already carries five `auth` and three `onboarding` scenarios.
The year corpus is about a family that is already in, not about getting in.

`notes`, `recipes`, `loans`, `activity` and `app-shell` are absent for the same reason — this
year's story does not turn on them, and adding a scenario to close a cell in a table would be
coverage theatre rather than a test. If a later round needs them, they are added by the same
rule as everything else: a life-event that fails without them, and an assertion that can go
red.
