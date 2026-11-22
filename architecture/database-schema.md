# Database schema

PostgreSQL 16. 39 tables across `packages/db/migrations/` (001 → 082). This
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
