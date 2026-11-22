# Database schema

PostgreSQL 16. 39 tables across `packages/db/migrations/` (001 → 087). This
doc covers the conventions and the tables where the schema is not
self-evident; the catalogue is the authority for columns.

For the audit that produced the current set of fixes, see
[`schema-review-2026-11.md`](schema-review-2026-11.md).

---

## Conventions

**Money is integer cents** (`*_cents BIGINT`) or `NUMERIC(12,2)` where a table
predates that rule. Never `float`.

**Dates that are calendar days are `DATE`** (`due_date`, `paid_for_due_date`,
`start_date`); timestamps are `TIMESTAMPTZ`. A `DATE` is a civil date, not an
instant: Go reads it as midnight UTC (`billdue.Midnight`) and a household's
"today" comes from `households.timezone` (`billdue.HouseholdToday`), never from
the server's zone.

**`updated_at`** is maintained by one shared trigger (`set_updated_at()`,
migration 077) on every table that has the column. A writer does not set it.

**Constraints state the rule the code already follows.** When a fact can be
read from more than one column, the migration that makes them consistent adds
the `CHECK` that keeps them consistent, and `COMMENT ON COLUMN` says which
column wins. See `bills` below for the worked example: a rule that lives only
in Go is a rule that two consumers can implement differently.

**Enums are `CHECK (... IN (...))`** on a `VARCHAR`, not Postgres `ENUM`, so
adding a value is a migration and not a type change. `frequency`, `role`,
`event_type` and `entity_kind` are the important ones.

---

## `bills` — which column says when a bill is due

A bill's schedule was once stored three ways (`due_day`, `rrule`, `due_date`)
with nothing relating them, so two consumers could disagree about "when is
this due". Migration **082** settles it. The rule:

> **`frequency` selects the authoritative column. The other must be NULL.**

| `frequency` | Authoritative column | Must be NULL | Meaning |
|---|---|---|---|
| `monthly` | **`due_day`** | `due_date` | Due on this day (1-31) of every month; a short month uses its last day (the 31st is due 30 April). |
| `quarterly`, `semiannual`, `annual` | **`due_date`** | `due_day` | Anchored on this date, repeating every 3 / 6 / 12 months on its day of the month (clamped to short months). |
| `once` | **`due_date`** | `due_day` | Due on this date and never again. |
| any | — | — | Both NULL means **no schedule**: the bill is due nowhere, and every consumer reports "no due date". This is legal and common (`legacyfinance` writes it). |

**`rrule` is not part of the answer.** `bills.rrule` is a write-only legacy
column from migration 020: the REST API accepts it on create/update and echoes
it back, and **nothing computes a due date from it**. The RRULE that consumers
actually read is `calendar_objects.rrule` — a different table — which
`packages/billcal` derives from `due_day`/`due_date` and which
`TestBillRuleMatchesDueRule` pins to the due rule. Do not read `bills.rrule`.
It is scheduled for removal.

### Enforcement

| Constraint | Refuses |
|---|---|
| `bills_frequency_check` (044) | a frequency outside the five |
| `bills_due_day_monthly_check` (044) | `due_day` on a non-monthly bill |
| `bills_schedule_one_representation_check` (**082**) | `due_date` on a monthly bill |
| `bills_due_day_range_check` (**082**) | `due_day` outside 1-31 |

So `frequency = 'monthly', due_day = 5, due_date = '2026-12-28'` is refused by
`bills_schedule_one_representation_check`. It used to be accepted, and nothing
read the `due_date` it accepted.

### The one rule that resolves a schedule

`packages/billdue.Schedule` → `cadence()` is the single implementation, and
every consumer calls it rather than re-deriving:

| Consumer | Answers |
|---|---|
| `packages/billdue.BillDueFor` | next unpaid due date, status, days until |
| `packages/billdue.CoveredDueDate` | which due date a payment covers (052's `paid_for_due_date`) |
| `packages/billdue.LoadDueSoonRows` | MCP `budget_summary`, API `GET /api/v1/budget/due-soon` |
| `packages/billcal.Rule` | the Bills-calendar mirror event's RRULE |
| `apps/worker` `bill_due` job | which bills to notify |
| `apps/mcp/internal/bill` | `bill_find`, `bill_create`, `bill_update`, `bill_pay` |
| `apps/mcp/internal/calendar` agenda, `apps/mcp/internal/household` overview | what is due in a window |
| `apps/ui` | what to display |

`apps/worker/cmd/worker/billdue.go` is a **deliberate copy** of the same rule
(the worker cannot import the MCP module); its tests and the MCP's pin them
together.

### Reading a bill safely

```sql
-- WRONG: two answers, and one of them may be stale
SELECT due_day, due_date FROM bills;

-- RIGHT: frequency decides, so ask for the one that counts
SELECT CASE WHEN frequency = 'monthly' THEN due_day::text
            ELSE due_date::text END AS due
  FROM bills;
```

Better still: read `frequency, due_day, due_date` together and pass all three
to `billdue.Schedule`, which is what every Go consumer does.

---

## The subject pattern: five tables, one rule

**Five tables share one three-column "subject" shape** — `bills`, `loans`,
`maintenance_schedules`, `maintenance_tasks` and `guides`. Every one carries the
same `*_subject_check`:

```sql
CHECK (num_nonnulls(asset_id, vehicle_id)  <= 1
   AND num_nonnulls(property_id, vehicle_id) <= 1)
```

### The rule, stated once

> **A record has at most one SUBJECT: `asset_id` or `vehicle_id`.
> `property_id` is not a third subject** — it is the property the record is
> about. When the subject is an **asset**, `property_id` is a
> **denormalisation of that asset's property**, filled in automatically.

So `asset_id` and `property_id` being set **together is correct and expected**;
`vehicle_id` excludes both. This is the one thing to know before writing a
query against any of the five tables:

- A row about an **asset** has `asset_id` **and** `property_id` set.
- A row about a **vehicle** has `vehicle_id` only.
- A row about a **property itself** has `property_id` only.
- A row with **no subject** is legal and common.

### Reading it: "what happened at this property?"

Because `property_id` is filled from the asset, everything about a property
*including its assets* is **one indexed predicate**:

```sql
-- RIGHT: property_id already includes the property's assets
WHERE property_id = $1
```

**`property_id = $1` alone is correct, and as of migration 087 it is enforced.**
Before 087 the trigger that fills `property_id` from the asset did so by
*silently overwriting* whatever the caller supplied, and a CHECK cannot tell a
derived `property_id` from a caller-supplied one that disagrees. So a row could
be about an asset at House A while its `property_id` said House B, and nothing
objected. In that database no single query was right:

```sql
-- WRONG since 087, and never actually right: it finds a contradicted row
-- under House A via its asset AND under House B via property_id, so it is
-- counted once under each.
WHERE property_id = $1 OR asset_id IN (SELECT id FROM assets WHERE property_id = $1)
```

`packages/db/testdata/subject_contradiction/verify_query_drift.sql` reproduces
that on a pre-087 database: 227.00 summed over the two houses against 127.00 of
actual bills, because one 100.00 row is counted under both. `verify_query.sql`
shows the same database after 087: `property_id = $1` returns 120.00 and the OR
form returns the same rows — **0 extra rows**, so it is redundant, not merely
equal.

087 removes the possibility rather than papering over it: a contradiction is
refused, so a stored `property_id` can never disagree with its asset, and the
OR form buys nothing. Do not write it; it pays for a second plan branch to
rediscover denormalised data.

**The one exception, and why it does not matter here.** `property_id` can be
NULL on an asset-subject row when the *asset itself* belongs to no property
(`assets.property_id IS NULL`) — `derive_subject_property` faithfully copies the
NULL. Such a row is not "at" any property, so `property_id = $1` and the
`OR asset_id IN (...)` form both correctly return it for no property. Where the
two forms *do* differ is asking "everything about this **asset**", which is a
different question and belongs to `asset_id = $1`.

### Who keeps `property_id` honest

Both from migration 038, and **caller-independent** — REST, MCP and a direct
`psql` insert all get the same behaviour because this is in the database, not
in handler code (see `write-paths-and-side-effects.md`):

| Trigger | Does |
|---|---|
| `derive_subject_property()` — BEFORE INSERT OR UPDATE on all five tables | sets `property_id` from `asset_id`; **refuses a contradictory subject** (087); refuses an asset, property or vehicle belonging to another family |
| `assets_propagate_property` — AFTER UPDATE OF property_id ON `assets` | when an asset moves to another property, realigns `property_id` on every row that names it, in all five tables |

### A contradiction is refused, not corrected (087)

`derive_subject_property()` used to correct a disagreeing `property_id`
silently. It now refuses it, because a silent correction is a caller's value
discarded without a word: the caller believes the record is filed against
property P when it is not. Two shapes are contradictions, and both are
`check_violation` (23514) so a caller can tell them apart:

| Shape | Error | Constraint name |
|---|---|---|
| `asset_id` set, and the caller **presented** a `property_id` that is not the asset's property | `…: property_id X contradicts asset Y , which is at property Z` | `<table>_subject_property_conflict` |
| `vehicle_id` set alongside a `property_id` or `asset_id` | `…: vehicle_id V cannot be combined with property_id or asset_id` | `<table>_subject_check` |

**"Presented" is the crux, and it is why this belongs in the trigger.** A CHECK
cannot distinguish a `property_id` the trigger derived from one the caller
supplied; only a trigger sees the row before it lands, and only a trigger sees
`OLD`. The rule counts a value as presented when:

- **INSERT** — `property_id` is not NULL. A row being inserted states every
  value in it.
- **UPDATE** — `property_id` changed (`IS DISTINCT FROM OLD.property_id`). A
  value carried over from the stored row is not a claim about this write; it is
  the previous derivation, and it is re-derived from the asset.
- **either** — NULL is never a claim. NULL means "derive it" (and the asset FKs'
  `ON DELETE SET NULL` produces one when an asset leaves its property).

That distinction is what keeps the ordinary update path working: changing a
bill's asset from A to B without naming `property_id` — what
`apps/api/internal/subject.Fields.Apply` does — carries the old value forward,
so the trigger re-derives it to B's property instead of refusing the write. The
trigger cannot see intent, so an UPDATE that *names* the value the row already
holds is indistinguishable from one that carries it; that is stated here rather
than pretended away.

### Why the CHECK is not `num_nonnulls(a, b, c) <= 1`

`architecture/schema-review-2026-11.md` (F2) proposed tightening all five to
`num_nonnulls(property_id, asset_id, vehicle_id) <= 1`. **That expression is
wrong and was not shipped**, because it forbids exactly the asset-subject row
described above:

```
asset-subject bill after derive_subject_property:
  property set = t, asset set = t, vehicle set = f  ->  num_nonnulls = 2  ->  REJECTED
```

It cannot be installed on a database holding a single asset-subject bill, and
it would make every asset-subject insert impossible. The two-clause form above
is **provably equivalent** to the pre-083 expression on all eight null
combinations, so 083 changes no row's accept/reject outcome — it states the
same rule in `num_nonnulls` terms and puts the rule on each constraint as a
`COMMENT ON CONSTRAINT`, where a schema reader will find it.

**The CHECK is kept, and it is now provably incomplete-but-safe.** It cannot see
the contradiction 087 refuses — a CHECK has no `OLD` and no knowledge of which
value the trigger derived. It stays as the backstop for a writer that reaches
the table without the trigger: a bulk load with the trigger disabled, a
replication apply, or a future trigger list that loses a column. A CHECK cannot
be forgotten; a trigger can.

---

## Other tables worth knowing

**`calendar_objects`** is the one place a recurrence rule is authoritative:
`rrule`, `ical_raw` and `ical_data` are written together from one `icalx`
master, so CalDAV's staleness rule holds. A bill's mirror event has
`entity_type = 'bill'` and `event_type = 'bill'`; `packages/billcal` is the
only writer.

**`notifications`** has `entity_id UUID` (not text) and `entity_kind`
(migration 070), so `entity_id = b.id` compares UUID to UUID and
`entity_id::text = b.id::text` is a bug.

**`households.timezone`** is an IANA name and is the only source of "today" for
a household. `AT TIME ZONE` on a bad value fails the whole query, which is why
the due rule does date arithmetic in Go after reading the value.

**`activity_log`** is written by `log_activity(kind)` triggers on 26 tables
(`memberships` added by 086, #5518), so it does not depend on the caller
remembering -- which is why a REST write and an MCP write of the same
membership change produce identical rows. `outbox_events` is the opposite: it is
written by handler code, so its coverage is only as good as the handlers.

**Membership changes are audited, and that audit is managers-only.**
`memberships` is the one table added to the activity feed at 086, because a
grant, a demotion or a removal decides what a person can see (money,
credentials, documents) and left no record before. `activity_log` has no
per-row visibility, so the restriction lives in `role_can_see_entity`: the
`membership` kind maps to a `member_audit` resource granted to `owner` and
`family_manager` only (not `members`, which stays `rolesSitter`). A demotion is
therefore not readable in the shared feed by the person demoted -- they find out
when their permissions change, not by reading the log.
