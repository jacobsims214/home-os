# Home OS — Schema review, November 2026

**Scope:** all 75 migrations (`packages/db/migrations/001` … `074`), 39 tables.
**Ticket:** #5507 (epic #243, story #675). **Remediation:** story #679.

**Method.** Rather than read 7,554 lines of SQL and reason about them, I applied all 75
migrations in order onto a scratch PostgreSQL cluster and queried the result. Every claim
below was checked against the live catalogue, and the interesting ones were checked by
attempting the bad write. Where a probe is quoted, it is the actual statement and the actual
result.

Two notes on the method:

- The scratch cluster is PostgreSQL 14 (Docker's overlay2 store on this host is corrupted,
  so `postgres:16` would not start). PostgreSQL 15 added the column-list form of
  `ON DELETE SET NULL (col)` used by 043/044/046/058/064/066/067/072, so those clauses were
  rewritten to plain `ON DELETE SET NULL` **in a scratch copy only** before applying. No
  other change was made to any migration. Everything in this document is 14-vs-16 neutral:
  I did not evaluate anything that depends on a 16-only feature.
- `032_loans` contains `GRANT ALL ON TABLE loans TO app;`, so the `app` role must exist in
  the target database before migrations run or 032 aborts. Worth knowing; the scratch
  cluster was created with it.

---

## Summary

The schema is in **substantially better shape than the ticket's framing assumes**. The
domain-model-v2 work (038–058, 070) is real, thorough, and enforced in the database rather
than in handler code. Three of the seven areas under review are **fine** and I say so
below. The genuine defects cluster in two places: **the audit trail's own vocabulary** (the
one polymorphic link nobody constrained), and **`bills`** (the oldest table, pre-dating
every convention that 038+ established, and never retrofitted).

| # | Area | Verdict | Worst finding |
|---|---|---|---|
| 1 | Polymorphic links | **Mostly fine** | `activity_log.entity_type` accepts any string; no `parent_type` constraint |
| 2 | Tenancy | **Fine, with one hole** | 15 single-column FKs where 20 equivalents are composite |
| 3 | Missing relationships | **Real gaps** | no payment ledger; bill→payee only via `vendor_id` |
| 4 | Type/naming consistency | **Weakest area** | "when a bill is due" stored 3 ways, no CHECK; `updated_at` unmaintained on 22 of 25 tables |
| 5 | Indexes | **Good, two gaps** | `calendar_objects` has no `(entity_type, entity_id)` index; `vehicles` has one index total |
| 6 | Structural duplication | **Fine** | the denormalised `property_id` copies are trigger-maintained |
| 7 | Audit trail | **Two mechanisms, deliberately** | `activity_log` trigger-based ✓; `outbox_events` handler-based ✗ |

**Severity scale.** `critical` = data is wrong or leakable and nothing stops it. `high` = an
agent or person gets a wrong answer, or a class of query is impossible. `medium` =
consistency or performance cost that will bite as data grows. `low` = papercut.

---

## 1. Polymorphic links

### What is actually there

This is the area I expected to be worst and it is the best-defended. The schema has:

- **A domain, `entity_kind`**, from 038, extended by 044/046/047/049/070. It is a
  `VARCHAR(50)` with a `CHECK (VALUE IN (...))` over 23 kinds. It is applied to `notes`,
  `files`, `secrets`, `lists`, `document_links`, `notifications` and `calendar_objects`.
- **`entity_household(kind, id)`** — a registry function returning the owning household of
  any `(kind, id)` pair, or NULL if the kind is unknown or the row is gone.
- **`enforce_entity_owner()`** — a BEFORE INSERT/UPDATE trigger on all seven tables, which
  raises `23503` when the target is in another family.
- **`rehome_entity_refs(kind)`** — an AFTER DELETE trigger on **22 tables**, so deletion does
  not orphan polymorphic rows: files and secrets are re-pointed to `('household',
  household_id)`, notes and document_links are deleted, calendar events and notifications
  lose the reference.

I verified the enforcement rather than trusting the source. For a note in family B pointing
at family A's property:

```
insert into notes(id,household_id,entity_type,entity_id,body) values
  ('…','22222222-…','property','aaaaaaaa-…','cross');
ERROR:  notes: property aaaaaaaa-… does not exist in this family
HINT:  entity_type must be a known kind and entity_id a row of the same household;
       use ('household', household_id) for family-level rows
```

That is real enforcement, in the database, caller-independent. **The
`entity_type`/`entity_id` mechanism as used by notes, files, secrets, lists,
document_links, notifications and calendar_objects is correct and needs no change.**

I also checked that the three vocabularies which must agree do agree for these tables: every
`entity_kind` member has an `entity_household()` branch, and `entity_household()` has no
branch that `entity_kind` rejects. They diverge elsewhere — see F1.

### F1 — `activity_log.entity_type` and `parent_type` are unconstrained text

**Where:** `050_authorship.up.sql:180` (`activity_log`): `entity_type VARCHAR(50) NOT NULL`,
`parent_type VARCHAR(50)`. Confirmed in the live catalogue: these are the **only**
`entity_type` columns in the database that are not the `entity_kind` domain.

**What is wrong:** every other polymorphic link is constrained to 23 kinds. The audit trail —
the one place where a wrong value is unrecoverable, because it is a historical record —
accepts any string at all. Its only constraint is
`activity_log_entity_type_check CHECK (btrim(entity_type) <> '')`, i.e. "not blank".

Probe:

```
insert into activity_log(household_id,entity_type,entity_id,action,summary)
values ('11111111-…','not_a_real_table','aaaaaaaa-…','create','x');
INSERT 0 1        -- accepted
```

The same shape on `notes` is rejected. So the audit log is the weakest link in the strongest
chain.

**Why it matters:** `activity_log.entity_type` is what the activity feed, the entity detail
pages and the MCP `activity` tools resolve against. A row whose `entity_type` is a typo, a
renamed kind, or a kind that only ever existed in one code path is **invisible**: no join
finds it, no page lists it, no agent can fetch it. The record that a change happened exists
and cannot be read. `parent_type` is worse — a second unconstrained polymorphic pair on the
same row, where `activity_log_parent_check` only enforces `(parent_type IS NULL) =
(parent_id IS NULL)`, i.e. that the pair is consistent with itself, never that the type is
real.

The three vocabularies are also genuinely out of sync today, which is exactly the drift the
absence of a constraint permits:

| Vocabulary | Members absent from `entity_kind` |
|---|---|
| `activity_log.entity_type` (the 25 `log_activity('…')` args) | `list_item`, `meal_feedback`, `person_preference` |
| `role_can_see_entity(kind)` (022/…) | `maintenance`, `document`, `person_preference`, `list_item`, `meal_feedback` |

`person_preference`, `list_item` and `meal_feedback` are logged as entities but are **not
kinds a note, file or secret can attach to**, and have no `entity_household()` branch. That
is a defensible design (you attach a file to a *person*, not to a *preference*) — but it
means `activity_log.entity_type` is genuinely a different vocabulary from `entity_kind`, and
nothing in the schema says so. Two tables reading "the kind of a thing" and disagreeing is
the structural version of the one-rule-in-two-places problem this project keeps hitting.

**Severity:** high. The audit trail is the one table where a wrong write is permanent.

**Shape of the fix:** a separate `activity_entity_kind` domain (the union: `entity_kind`
members plus `list_item`, `meal_feedback`, `person_preference`), applied to both
`entity_type` and `parent_type`. Keep it distinct from `entity_kind` rather than widening
that domain — widening `entity_kind` would silently let a note attach to a
`person_preference`. Add a test asserting every `log_activity('…')` argument in the
migrations is a member of the new domain and has a `role_can_see_entity` branch.

### F2 — A bill's subject is three nullable FKs where it should be one

**Where:** `007_bills.up.sql` (`property_id`), `028_add_vehicle_to_maintenance.up.sql`
(`vehicle_id`), `038` (`asset_id`). The guard is
`bills_subject_check CHECK (vehicle_id IS NULL OR (property_id IS NULL AND asset_id IS
NULL))`.

**What is wrong:** the CHECK is asymmetric and permissive. It says only that a vehicle and a
property cannot coexist; it says nothing about `asset_id` alongside `property_id`, nor
`asset_id` alongside `vehicle_id`. Only `derive_subject_property()` fills `property_id` from
`asset_id`, so a direct insert can set both. The table can hold a bill simultaneously about
an asset and about a different property.

This is not a one-off: the same three-column pattern and the same asymmetric CHECK are
repeated on `maintenance_schedules` (008/028), `maintenance_tasks` (009/028), `loans` (032)
and `guides` (049) — `maintenance_schedules_subject_check`, `maintenance_tasks_subject_
check`, `loans_subject_check`, `guides_subject_check` all read `vehicle_id IS NULL OR
(property_id IS NULL AND asset_id IS NULL)`. Five tables, one flawed rule, copied.

**Why it matters:** "which bills are for this property?" must be written as `property_id =
$1 OR asset_id IN (SELECT id FROM assets WHERE property_id = $1)`, which is what the code
does. An agent asked "what do we spend on the house?" has to know that rule; nothing in the
schema states it. And a row with `asset_id` plus a *stale* `property_id` is double-counted
by exactly that query.

**Severity:** medium. Not currently producing wrong data, because the trigger is the only
writer and maintains the invariant — but the invariant is not stated in the table.

**Shape of the fix:** tighten the CHECK to `num_nonnulls(property_id, asset_id, vehicle_id) <=
1` on all five tables. `derive_subject_property()` still fills `property_id` from the asset,
so the rule becomes "at most one subject; `property_id` is also set when the subject is an
asset". Then `asset_id` implies `property_id` and "spend on the house" collapses to
`property_id = $1`.

### F3 — `document_links.link_id` is `VARCHAR` with no target constraint

**Where:** `011_create_document_links.up.sql` (extended by 019, 070).
`link_type VARCHAR(50) NOT NULL CHECK IN ('file','secret','url')`, `link_id VARCHAR(200)`,
`url TEXT`.

**What is wrong:** `document_links` has *two* polymorphic links on one row with different
guarantees. Its `entity_type`/`entity_id` pair is domain-constrained and trigger-enforced
(above). Its `link_type`/`link_id` pair — pointing at the file or secret the link *is* — is a
`VARCHAR` holding what is really a UUID, with no FK, no CHECK, and no conditional constraint
tying `link_type` to `url` vs `link_id`.

`enforce_entity_owner()` does check that a `link_type='file'` link points at a file of the
same family, so *tenancy* is enforced. *Existence* is not: `link_id` can name a deleted row,
or a string that was never a UUID.

**Why it matters:** `link_id` is `VARCHAR` while every real id in the system is `UUID`. A
consumer joining `document_links` to `files` must cast, and a cast is where a typo'd value
becomes a runtime error instead of an empty result. Same class of defect as F1: a
polymorphic pointer with no constraint on its target.

**Severity:** medium.

**Shape of the fix:** `document_links_link_target_check`: `link_type = 'url'` requires `url
IS NOT NULL`; `link_type IN ('file','secret')` requires `link_id IS NOT NULL` and `link_id ~
'^[0-9a-f]{8}-[0-9a-f]{4}-'`. A real FK needs the table split (one FK per target type), so
the CHECK plus the existing same-family trigger is the right shape.

---

## 2. Tenancy

### What is fine

**Every household-scoped table carries `household_id NOT NULL`.** The only six tables without
it are `households` (the root), `users` (global identities), `password_reset_tokens` (global,
by `user_id`), `calendar_changes` (byte-level sync journal, by `calendar_id`),
`calendar_objects` (by `calendar_id`) and `file_blobs` (content-addressed bytes, deliberately
family-free so blobs dedupe). Each of the six is defensible. **Tenancy by `household_id` is
consistent.**

`058_same_family_fks.up.sql` is the high-water mark and it is good work: it repaired
cross-family rows, then replaced five single-column FKs with composite `(col, household_id)
-> (id, household_id)` FKs. The 20 composite FKs now in the database (`bills.vendor_id`,
`bills.budget_category_id`, `bills.source_document_id`, `files.person_id`,
`meal_plans.recipe_id`, `list_items.list_id`, …) make a cross-family reference
**structurally impossible**, not merely discouraged.

### F4 — 15 family-scoped FKs are single-column where 20 equivalents are composite

**Where:** the composite-FK pattern of 058 was applied to 15 relationships and **skipped 15
family-scoped columns**. Live catalogue — these point at their parent by id alone, with no
`household_id` in the FK:

`bills.property_id`, `bills.asset_id`, `bills.vehicle_id`;
`maintenance_schedules.property_id`, `maintenance_schedules.asset_id`,
`maintenance_schedules.vehicle_id`; `maintenance_tasks.property_id`,
`maintenance_tasks.asset_id`, `maintenance_tasks.vehicle_id`; `loans.property_id`,
`loans.asset_id`, `loans.vehicle_id`; `guides.property_id`, `guides.asset_id`,
`guides.vehicle_id`.

**What is wrong:** these 15 references have no family check in the foreign key itself. Each is
caught by `derive_subject_property()` **today**. I proved the trigger is the *only* thing
catching them by disabling it:

```
ALTER TABLE bills DISABLE TRIGGER bills_derive_subject_property;
insert into bills(id,household_id,name,amount,property_id) values
  ('…','22222222-…','B bill on A house',10,'aaaaaaaa-…');   -- family A's property
INSERT 0 1        -- accepted: family B now points at family A's house
```

**Why it matters:** this is #5314's finding exactly. A trigger-based guard is
caller-independent; a guard that lives in one function is caller-independent only while
every writer goes through the trigger. The trigger is ours and does run — so the exposure is
not "an app bug today". It is that **15 references are protected by one mechanism while 20
equivalent ones are protected by two**, with nothing indicating which is which. A future
migration that drops or reorders the trigger (038's `rehome` work has already dropped and
replaced triggers) silently removes the protection from exactly these columns. And a `COPY`,
a `pg_restore`, or `session_replication_role = replica` bypasses triggers entirely.

The deeper structural blocker: **the target tables lack the `UNIQUE (id, household_id)` key a
composite FK requires.** Only 10 tables have it — `properties`, `vendors`, `people`, `files`,
`lists`, `budget_categories`, `maintenance_schedules`, `recipes`, `meal_plans`, `guides`.
Fourteen family-scoped tables do not: `assets`, `bills`, `calendars`, `calendar_objects`,
`document_links`, `loans`, `maintenance_tasks`, `notes`, `secrets`, `pets`, `vehicles`,
`rooms`, `memberships`, `guide_revisions`. For those a composite FK is **currently
impossible**, which is why 058 skipped them.

**Severity:** high. A correctness hole open in 15 places and closed in 20; the inconsistency
is itself the defect.

**Shape of the fix:** two steps. (1) Add `UNIQUE (id, household_id)` to the 14 tables that
lack it — redundant over the PK, purely additive, cheap at these sizes. (2) Replace the 15
single-column FKs with composite ones keeping every current delete action, exactly 058's
recipe. Then `derive_subject_property()` is a convenience, not the safety mechanism.

### F5 — `households.default_property_id` can point at another household's property

**Where:** `002_properties.up.sql` / 038. `households.default_property_id UUID REFERENCES
properties(id) ON DELETE SET NULL`. `households` has no `household_id` — the row *is* the
household.

**What is wrong:** nothing constrains `default_property_id` to a property of *this*
household. Because `households` has no `household_id`, the standard composite-FK trick
cannot be expressed the usual way.

**Why it matters:** `default_property_id` is what "the house" means for a family that owns
several. If it names another family's property, every query resolving "the default property"
reaches across a tenant boundary — the exact thing 058 spent a migration preventing
everywhere else. It is an admin-write column, so accidental occurrence is unlikely, but this
is a tenancy hole in the one table that defines tenancy.

**Severity:** medium.

**Shape of the fix:** `FOREIGN KEY (default_property_id, id) REFERENCES properties (id,
household_id) ON DELETE SET NULL` — works today because `properties` already has `UNIQUE (id,
household_id)`, and `households.id` is its PK. Declarative, no trigger needed. (A CHECK
cannot reference another table.)

---

## 3. Missing relationships

This is where the schema genuinely stops a consumer, and where the findings most directly
answer the ticket's "what must a consumer infer today" question.

### F6 — There is no payment ledger: a bill's payment history is one nullable date

**Where:** `020_family_model.up.sql:58` adds `bills.paid_date DATE`.
`052_bills_paid_for_due_date.up.sql` adds `paid_for_due_date DATE`.

**What is wrong:** a bill has exactly two columns for payment history — the date of "the last
payment" and the due date that payment covered. There is no payments table, no amount-paid
column; `bills.amount` is the amount *owed*. Every past payment but the most recent is
destroyed by the next one.

This is less an oversight than a decision never revisited, and 052's own header shows the
cost: "Without it a payment more than 7 days late was read as paying NEXT month, so the
overdue bill vanished and next month's reminder was never sent." That bug was a symptom of
storing payment state as a *single inferred date* rather than as events. 052 patched the
symptom by adding a second inferred date. The third bug will need a third column.

**Why it matters:** the questions the product exists to answer cannot be asked.

- "How much did we pay this provider last year?" — impossible; only the last payment survives.
- "Was December's bill paid, and did it pay December or November?" — only for the current row.
- "Show me the payment that cleared at $341 when the bill says $314" — impossible; nothing
  exists to reconcile against beyond the immediately preceding update.
- Any agent doing month-over-month spend analysis must **infer** it from `activity_log`'s
  before/after pairs (072) — which, per F1, may not be resolvable, and which are heavily
  partial (money/date columns on an allowlist only).

**Severity:** high. The largest missing relationship, and it blocks the epic's own thesis
("visibility is the product").

**Shape of the fix:** `bill_payments(id, household_id, bill_id, amount NUMERIC(12,2) NOT
NULL, paid_on DATE NOT NULL, covers_due_date DATE, method, payment_account_id,
source_document_id, created_at, created_by)`, composite same-family FK on `bill_id`, plus a
trigger maintaining `bills.paid_date`/`paid_for_due_date` as a **derived cache** of the
latest row so existing reads keep working. This is also where #5497's `payment_account` lands
naturally: a payment, not a bill, comes out of an account.

### F7 — bill→payee is `vendor_id` only, and a payee is not always a vendor

**Where:** `007_bills.up.sql` (`vendor_id`, repaired to a composite same-family FK by 058);
`004_vendors.up.sql`; 043 adds `vendors.trades`; 064 adds `memberships.vendor_id`; 065 adds
vendor addresses.

**What is wrong:** partly fine, and I want to be precise about the boundary. A bill *can*
name its payee through `vendor_id`, and that link is correctly constrained. What is missing is
that **the same real-world entity appears in two shapes**: `vendors` is the service provider
(a plumber, a gardener), and `memberships.vendor_id` (064) links a *login* to a vendor. But a
household's actual payees — the utility, the mortgage servicer, the insurer, the town — are
payees-of-record that have no trades, no address in the common case, and no relationship to
any membership.

So `bills.vendor_id` is `NULL` for most bills in practice, because the family has no vendor
row for "Eversource". A consumer asking "who do we pay, how often, and how do I reach them
when it goes wrong?" (epic #243's own gap 3) must walk: bill → (maybe vendor) → maybe
membership → maybe a secret holding a login. Four optional hops, each possibly absent.

**Why it matters:** the schema's answer today is "a payee is a vendor if someone made one",
which is a convention, not a relationship.

**Severity:** medium. The link exists and is correct; it is the *payee* concept that is
under-modelled, and #5497 is already in this area.

**Shape of the fix:** decide whether `vendors` becomes the payee-of-record by gaining a `kind`
discriminator (`service_provider`, `payee`, `both`), or whether bills gain a separate
`payee` block. Given 043/064/065 all extend `vendors`, extending it is the cheaper path — but
it must be a `kind`, not a second table, or §6's structural duplication comes back. **This
one needs a person to decide, not an agent to infer.**

### F8 — A calendar event's link to what it is about is queryable but unindexed

**Where:** `013_create_calendar_objects.up.sql`; `calendar_objects.entity_type`/`entity_id`
(domain-constrained by 070, enforced by `calendar_objects_enforce_entity_owner`); `038` adds
`calendar_objects.superseded_by -> bills(id)`; 073/074 add bill-like-event reconciliation.

**Verdict: mostly fine — and this is a correction to my own first reading.** The direction is
event → subject, and the reverse lookup *is* possible without a new column: a bill's mirror
event is marked `entity_type='bill'`, so `SELECT … FROM calendar_objects WHERE
entity_type='bill' AND entity_id=$1` is the contract. `superseded_by -> bills(id)` is a
*different, additional* fact (this event was absorbed by that bill), not a substitute.

**What is wrong:** narrowly, **`calendar_objects` has no `(entity_type, entity_id)` index**,
while every other polymorphic table does (`idx_files_entity`, `idx_notes_entity`,
`idx_secrets_entity`, `idx_lists_entity`). Live catalogue — the only indexes on
`calendar_objects` touching these columns are `calendar_objects_calendar_id_uid_key`,
`idx_calendar_objects_calendar_starts` and `idx_calendar_objects_superseded`
(`(calendar_id) WHERE superseded_at IS NOT NULL`). No entity index at all.

**Why it matters:** "what's on the calendar about this bill?" — the reverse query the mirror
relationship exists to answer — is a sequential scan of every calendar event in the database,
not just the family's. 073's reconciliation (`calendar_absorb_bill_events`) *is* the
title-and-amount heuristic the ticket worried about, but it exists for a different job
(matching legacy entries that mirror nothing); the keyed mirror relationship is fine. The
symptom 073 documents — a stale event read instead of the bill, so a parent was told a paid
bill was unpaid — came from unmatched *legacy* rows, not from a missing relationship.

**Severity:** low-medium (performance, not correctness).

**Shape of the fix:** `CREATE INDEX idx_calendar_objects_entity ON calendar_objects
(entity_type, entity_id)`; document the mirror query as the contract. No schema change to the
relationship itself.

### F9 — `document → what it documents` is well modelled

**This is a FINE verdict, so it carries no What-is-wrong / Shape-of-the-fix pair by design.**

**Where:** `023_create_files.up.sql` + `048_document_metadata.up.sql` +
`072_money_provenance.up.sql`.

**Verdict: fine.** A document knows what it is about (`entity_type`/`entity_id`, domain +
enforced), who it is about (`person_id`, composite FK in 072), what kind it is (`category`,
CHECK over 11 values), and its dates (`document_date`/`expires_on` with
`files_expires_after_document_date`). 072 added `text_origin`/`text_sha256` so a document's
body can prove it was not re-typed. This is the best-modelled area in the schema and deserves
saying so.

### F10 — `notifications.type` and `channel` are unconstrained

**Where:** `015_create_notifications.up.sql`: `type VARCHAR(100) NOT NULL`, `channel
VARCHAR(… ) NOT NULL` — neither with a CHECK. `notifications.entity_type`/`entity_id` gained
the `entity_kind` domain and `enforce_entity_owner` in 070.

**What is wrong:** the *entity* link is now correct (070's work). The `type` column — which
decides what the notification *is* (`bill_due`, `item_changed`, …) — is free text with no
CHECK and no enum. Worse, `idx_notifications_item_changed` is a partial index hard-coded on
the literal `'item_changed'`, so a typo'd type both escapes constraint and drops out of its
own index. `channel` is unconstrained too, and nothing ties it to a known delivery mechanism.

**Why it matters:** a notification whose `type` is misspelled is delivered, is unreadable by
any client filtering on type, and cannot be repaired by query because there is no list of
what types exist — they live in Go string literals only.

**Severity:** medium.

**Shape of the fix:** `CHECK (type IN (…))` covering the types actually emitted (grep for
`"bill_due"`, `"item_changed"`, …), same for `channel`; add a test that the Go
notification-type constants are a subset of the constraint.

### F11 — `notification → what it refers to` is now correct

**This is a FINE verdict, so it carries no What-is-wrong / Shape-of-the-fix pair by design.**

Called out separately because the ticket named it. `notifications.entity_type`/`entity_id`
was plain `VARCHAR(100)`/`UUID` in 015; 070 constrained `entity_type` to `entity_kind`,
added `notifications_enforce_entity_owner`, repaired wrong-kind rows
(`maintenance` → `maintenance_task`, `list_item` → `list`, `meal_feedback` → `meal_plan`,
`person_preference` → `person`) and cleared cross-family ones. **Verdict: fine.** Only the
`type`/`channel` columns (F10) remain unconstrained.

---

## 4. Type and naming consistency

The weakest area, and the most findings. The schema has three coherent conventions and never
picked one, depending on which iteration a table was born in.

### F12 — "When is this bill due" is stored three ways with no constraint between them

**Where:** `007_bills.up.sql` (`due_day INTEGER`), `020_family_model.up.sql` (adds `rrule
TEXT`), `038`/`069` (adds `due_date DATE`). All three are live columns on `bills` today.

**What is wrong:** the same fact — a bill's schedule — has three representations: `due_day`
(day-of-month, monthly bills), `rrule` (a recurrence rule), and `due_date` (an absolute
next-due date). The only constraint relating them is `bills_due_day_monthly_check CHECK
(frequency = 'monthly' OR due_day IS NULL)`, which constrains `due_day` alone and says
**nothing** about the relationship between the three. Probe:

```
insert into bills(household_id,name,amount,frequency,due_day,due_date) values
  ('11111111-…','contradictory bill',10,'monthly',5,'2026-12-28');
INSERT 0 1        -- due_day says the 5th, due_date says the 28th
```

**Why it matters:** any consumer answering "is this bill due before Friday?" must pick a
representation and know which wins. The answer is in Go, not in the schema. An agent reading
`bills` gets two disagreeing answers with no way to tell which is stale. Every notification
and every calendar mirror depends on getting this right, and 052's bug (payments read as
covering the wrong month) came from exactly this ambiguity.

**Severity:** high. The clearest structural one-rule-in-two-places in the schema.

**Shape of the fix:** decide which column is authoritative (`due_date` + `rrule` is the
flexible pair; `due_day` is derivable), then either backfill and drop `due_day`, or make it a
generated value from `due_date`. `069_drop_finance_columns` shows the project already has the
appetite for dropping legacy columns once nothing reads them — the same play applies.

### F13 — `updated_at` is maintained by a trigger on 3 of 25 tables

**Where:** every `CREATE TABLE` sets `updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`. Only
**three** tables have a trigger maintaining it on UPDATE: `guides`
(`guides_touch_updated_at`, 049), `lists` (`lists_touch_updated_at`, 046), `recipes`
(`recipes_touch_updated_at`, 047).

**What is wrong:** for the other 22 tables, `updated_at` moves only if the writer remembers.
Probe:

```
select name, amount, updated_at from bills where name='manual bill';
update bills set amount = amount + 1 where name='manual bill';
select name, amount, updated_at from bills where name='manual bill';
-- amount 11.00, updated_at UNCHANGED
```

The Go code usually does remember — 72 occurrences of `updated_at = NOW()` across the repos.
But it is per-statement: of the ~12 dynamic `UPDATE … SET `+strings.Join(sets, ", ")` builders
I sampled (`bill/write.go:518`, `asset/write.go:468`, `loan/write.go:605`,
`vendors/write.go:580`, `vehicle/write.go:362`, `property/write.go:373`, `pet/write.go:730`,
`recipes.go:1878`, `file/update.go:395`, `lists/tools.go:2564`, `meals/plan.go:742`,
`guides/tools.go:1884`), several do not append `updated_at`, and those are field-dependent —
so the column moves for some edits of a row and not others. The API and MCP bill-update paths
do not agree.

**Why it matters:** `updated_at` is the sort key for "what changed recently" — `files` even
has `idx_files_household_created` and the activity feed sorts on timestamps. A row whose
`updated_at` never moves is invisible to "what changed this week". Worse, `log_activity()`
*excludes* `updated_at` from `changed_fields` precisely because it is supposed to be bumped —
so the audit log says a change happened and the row says it did not.

Twenty-two tables out of 25 having no trigger is the tell: someone knew this needed a trigger
(046/047/049) and it was never rolled back to the rest.

**Severity:** high, and cheap to fix.

**Shape of the fix:** one `set_updated_at()` trigger function attached to all 25 tables in one
migration (replacing the three bespoke ones), then delete the 72 `updated_at = NOW()` sites
from Go. Highest value-per-line fix in the review.

### F14 — No soft-delete convention; `lists.archived` is the only one

**Where:** `lists.archived BOOLEAN NOT NULL DEFAULT false` (046) is the only soft-delete flag.
`users.active BOOLEAN NOT NULL DEFAULT true` (037) is an account flag, not a soft delete.
`file_search_deletions.deleted_at` (053) is a search-index queue, not a soft delete — the row
is already hard-deleted when it is written.

**What is wrong:** this is a finding only because it is inconsistent, not because hard delete
is wrong. Some records are recoverable by design (`lists`); the rest are not. A family that
deletes a property loses its rooms, its assets, its bills' `property_id` and the notes
attached to it (`rehome_entity_refs` deletes those) — irreversibly, with the only trace in
`activity_log`, which by CHECK keeps `changes` NULL on delete.

**Why it matters:** "can you get that back?" has no consistent answer. And the activity log,
which *does* retain the delete, retains a summary and a column-name list — not enough to
reconstruct a row.

**Severity:** low-to-medium. Not a defect; a decision never made.

**Shape of the fix:** **a product decision, not a schema fix** — do not implement until
someone decides. If the answer is "no soft delete", record it in the schema doc so the next
reviewer does not raise it again. If "yes for household records", it needs `deleted_at` on the
entity tables plus `WHERE deleted_at IS NULL` in the policy layer — a large change.

### F15 — `is_autopay` is nullable, so `NOT is_autopay` silently drops rows

**Where:** `bills.is_autopay BOOLEAN DEFAULT false` (epic #243's own column). Note the
default and the **absence of `NOT NULL`**.

**What is wrong:** the default is `false`, but the column accepts NULL, and rows written
before the default existed are NULL. Three-valued logic then makes the obvious query wrong:

```
select count(*) filter (where is_autopay)        as autopay_true,
       count(*) filter (where not is_autopay)    as not_autopay,
       count(*) filter (where is_autopay is null) as null_rows
from bills;
 autopay_true | not_autopay | null_rows
             1 |           1 |         1        -- 1 + 1 ≠ 3
```

A consumer counting "bills that need me" with `WHERE NOT is_autopay` **under-counts** by
exactly the unknown rows — which are the ones nobody has looked at yet, i.e. the most likely
to need attention.

**Why it matters:** epic #243's thesis is "Autopay status is the single most important bit on
a bill: it decides whether a notification is helpful or noise." The one column carrying that
thesis has no `NOT NULL` and no explicit unknown state. `false` means both "a person confirmed
this is manual" and "nobody has said" — two very different facts.

**Severity:** high for the epic, low for the schema generally. A one-line fix, squarely in the
epic's own subject area.

**Shape of the fix:** decide the semantics. If unknown is real, make it explicit — leave
`is_autopay` nullable, document NULL as "nobody has said", and make every consumer handle it.
If not, `SET NOT NULL` after backfilling NULL → false and accept that "unknown" is lost.
**Recommendation: NOT NULL DEFAULT false** — a notification policy reasoning about three
states will get it wrong, and the epic's whole point is not missing a bill.

### F16 — Money columns: consistent scales, no currency, no domain

**This is a FINE verdict, so it carries no What-is-wrong / Shape-of-the-fix pair by design.**

**Where:** all 16 `NUMERIC` money columns across 7 tables.

**Verdict: fine.** Every money column is `NUMERIC(12,2)` — `assets.purchase_price`,
`assets.current_value`, `bills.amount`, `budget_categories.monthly_amount`,
`loans.original_amount`, `loans.monthly_payment`, `loans.remaining_balance`,
`maintenance_schedules.estimated_cost`, `maintenance_tasks.cost`, `properties.current_value`,
`properties.down_payment`, `properties.purchase_price`, `vehicles.current_value`,
`vehicles.purchase_price`. The two exceptions are correct exceptions:
`loans.interest_rate NUMERIC(5,3)` (a rate) and `list_items.quantity NUMERIC(12,3)` (not
money). No single precision, no unconstrained `NUMERIC`, no `FLOAT` anywhere. **Consistent;
needs no change.** (A `money` domain with a currency would be nicer, but it is not a finding.)

### F17 — Enum vs text-with-CHECK is chosen chronologically, not per concept

**Where:** six real enums (`membership_role`, `task_status`, `calendar_type`,
`person_preference_kind`, `integration_type`, `integration_status`) and ~20
CHECK-constrained `VARCHAR`s (`bills.frequency`, `lists.kind`, `meal_plans.slot`,
`people.color`, `files.category`, `document_links.link_type`, `calendar_changes.action`,
`activity_log.action`, …).

**What is wrong:** the split is not principled, it is chronological. Enums predate 020; after
that everything is text-with-CHECK. Both work. But the same concept is modelled both ways, and
the sets have drifted: `calendar_changes.action` is CHECK over
`create`/`update`/`delete` while `activity_log.action` is CHECK over those **plus** `read` —
two different definitions of "the actions a change has".

**Why it matters:** a consumer cannot assume "a status column is constrained" — it has to know
which mechanism this particular table used, and `calendar_changes.action` and
`activity_log.action` disagreeing on whether `read` is an action means a query written against
one is wrong against the other.

**Severity:** low. A genuine inconsistency with little cost.

**Shape of the fix:** none beyond F10. Converting enums to CHECKs (or back) is churn, and **I
am not ticketing a wholesale conversion.** The part worth fixing is the columns with *no*
constraint at all (F10, `outbox_events.type`) — no constraint is a different thing from
"constrained by the other mechanism".

### F18 — `created_at`/`updated_at` absent on 13 tables, and that is mostly right

**Where:** `calendar_changes`, `file_blobs`, `guide_revisions`, `memberships`,
`notifications`, `outbox_events`, `password_reset_tokens`, `secret_keys`, `activity_log`,
`agent_tokens`, `document_links`, `invitations`, `file_search_deletions` have `created_at`
only.

**What is wrong:** in itself, nothing — but one table is miscategorised. Every one of the 13
is an append-only record (`activity_log`,
`calendar_changes`, `guide_revisions`, `outbox_events`, `file_search_deletions`,
`document_links`), a credential (`secret_keys`, `agent_tokens`, `password_reset_tokens`,
`invitations`), or a notification (mutated only via `read_at`, which is itself the timestamp).
An `updated_at` on an append-only table is dead weight.

**Why it matters:** the one genuine omission is **`memberships`** (001). A membership's `role`
changes when an owner promotes someone, and nothing records when, nor who did it
(`memberships` has no `updated_by` either). Append-only is the right default here and this is
the one table the default is wrong for. A small audit gap on a security-relevant fact.

**Severity:** low.

**Shape of the fix:** add `updated_at` and `updated_by` to `memberships` only — not to the
other 12. Folded into #5518 (audit memberships), which already needs a trigger on that table.

### F19 — `memberships` vs `people`: correctly distinct

**This is a FINE verdict, so it carries no What-is-wrong / Shape-of-the-fix pair by design.**

Called out because it looks like duplication and is not. `people` is the family's roster (a
child has a person row and no user account); `memberships` is login access; `people.user_id`
links them when both exist. 041's `membership_ensure_person` / `membership_unlink_person`
triggers keep them in step. **Verdict: fine.**

---

## 5. Indexes

### Verdict: good on the queries that matter, with two specific gaps

Coverage is genuinely good. Every table queried by household plus a time range has the index:
`activity_log (household_id, created_at DESC, id DESC)`, `bills (household_id, due_date) WHERE
due_date IS NOT NULL`, `maintenance_tasks (household_id, due_date)`, `files (household_id,
created_at DESC)`, `notifications (household_id, created_at DESC)`. Every polymorphic pair has
`(entity_type, entity_id)` — `idx_files_entity`, `idx_notes_entity`, `idx_secrets_entity`,
`idx_lists_entity` — except `calendar_objects` (F8). Partial indexes are used where NULLs
dominate (`idx_bills_vendor_id … WHERE vendor_id IS NOT NULL`), which is the right call. **No
finding for household+time-range queries.**

### F20 — `household_id` unindexed on 8 tables; `vehicles` has one index total

**Where:** `agent_tokens`, `file_search_deletions`, `guide_revisions`, `invitations`,
`meal_feedback`, `outbox_events`, `pets`, `vehicles`.

**What is wrong:** the tenancy column every query filters on has no index on these eight.
`pets` and `vehicles` are the ones that matter: `SELECT … FROM vehicles WHERE household_id =
$1` is the vehicle-list query and it is a sequential scan. Live catalogue — `vehicles` has
**exactly one index**, its primary key; `pets` has its PK plus `idx_pets_vet_vendor_id` only.

**Why it matters:** cheap at today's row counts, but these are per-family lists, the most
common query shape in the product. Every other family table has `idx_<table>_household`; these
eight are the omissions, so it is a consistency defect as much as a performance one.

**Severity:** low-medium. Cheap, safe, additive.

**Shape of the fix:** `CREATE INDEX idx_<table>_household ON <table> (household_id)` for the
eight; for `pets`/`vehicles` prefer `(household_id, name)` since household + order-by-name is
the list query.

### F21 — 13 read FK columns unindexed; `memberships.user_id` is on the login path

**Where:** the live query for FK columns with no usable leading-column index returns 77 rows,
but most are `created_by`/`updated_by` (write-only provenance — correctly unindexed) or
`household_id` covered by a composite.

**What is wrong:** the columns that are genuinely **read as a filter** have no index:

- `memberships.user_id` — "which families does this user belong to", on every login
- `bills.property_id`, `bills.asset_id`
- `maintenance_tasks.property_id`, `maintenance_tasks.asset_id`
- `maintenance_schedules.property_id`, `maintenance_schedules.asset_id`
- `loans.property_id`, `loans.asset_id`
- `assets.room_id` — "assets in this room"
- `calendar_objects.superseded_by` (and the entity pair, F8)

`memberships.user_id` is the most surprising: `memberships` has `UNIQUE (household_id,
user_id)`, which indexes `household_id` first, so a lookup by `user_id` alone is uncovered.

**Why it matters:** `memberships.user_id` is read on every authenticated request to resolve
which families the caller belongs to. Uncovered, that is a sequential scan of every membership
row in the database for every request — the one index here that is not merely a scaling
nicety. The property/asset lookups are per-family list queries, the same shape.

**Severity:** medium — `memberships.user_id` is on the authentication path.

**Shape of the fix:** `idx_memberships_user ON memberships (user_id)` first. Then note that
**F4 fixes most of the rest for free**: a composite FK on `(property_id, household_id)` gives a
usable index with `property_id` leading. Ticket them together.

### F22 — Two duplicate indexes on auth tables

**Where:** `password_reset_tokens` has both `password_reset_tokens_token_key` (UNIQUE) and
`idx_password_reset_tokens_token` (plain) on the same column; `invitations` has both
`invitations_token_key` (UNIQUE) and `idx_invitations_token` (plain).

**What is wrong:** the plain index is fully redundant — Postgres uses the unique index for
every lookup the plain one serves.

**Why it matters:** every insert to `password_reset_tokens` or `invitations` — both touched on
every password reset and every family invitation — maintains two indexes where one would do.
Nothing breaks; it is pure write amplification on auth-path tables.

**Severity:** low.

**Shape of the fix:** `DROP INDEX idx_password_reset_tokens_token` and `DROP INDEX
idx_invitations_token`. Folded into #5520 (index sweep) rather than its own ticket.

---

## 6. Structural duplication

### Verdict: fine — this is the area the project has actually solved

The ticket warns to "look for the structural version" of one-rule-in-two-places, a fair
warning given the project's history. I looked for it specifically and the schema **mostly does
not have it**:

- **The denormalised `property_id`** on `bills`/`loans`/`maintenance_*`/`guides` (derived from
  `asset_id`) has a real trigger, `assets_propagate_property()`, which re-points all four child
  tables when an asset moves property. Kept honest.
- **`rooms.household_id`** (a copy of `properties.household_id`) is maintained by
  `rooms_set_household()` and derives from the property, which 038 documents as authoritative.
- **`bills.paid_date`/`paid_for_due_date`** are the exception, and they are not duplication —
  they are *truncation*. That is F6.
- **`people` vs `memberships`** — deliberately distinct (F19).
- **`activity_log` vs `outbox_events`** — different jobs (§7), not duplication.

**One residual:** `bills.category` (007, free text, no CHECK) and `bills.budget_category_id`
(044, same-family FK to `budget_categories`) both express "what kind of spend is this". In
practice `budget_category_id` is the real one and `category` is legacy. `069_drop_finance_
columns` was the migration for exactly this cleanup.

**Severity:** low. Same shape as F12: a legacy column to drop once nothing reads it, not a new
architectural problem.

---

## 7. Audit trail

### What is there, and why two mechanisms is the right answer

There are **two** mechanisms, and after this review I believe that is correct rather than
accidental:

1. **`activity_log`** — written by `log_activity(kind)` AFTER INSERT/UPDATE/DELETE triggers on
   **25 tables**. This is **trigger-based and therefore caller-independent**, exactly as #5314
   requires. It captures `action`, `changed_fields`, a `summary`, the actor
   (`current_actor_user_id()` / `current_actor_agent_token_id()`, from `app.*` settings), the
   agent token, the client id, and — since 072 — before/after *values* for an allowlist of money
   and date columns. It handles delete cascades correctly (`pg_trigger_depth() > 1` and
   `activity_container_gone()` suppress redundant child rows). 062 added `xact_id` so one
   transaction's rows group.
2. **`outbox_events`** — written from **handler code**, in the same transaction, for
   `bill.created`/`bill.updated`/`bill.deleted`/`asset.created`/`asset.updated`. This is
   **caller-dependent**: it fires only from `apps/api/internal/bill/repo.go`,
   `apps/api/internal/asset/repo.go` and `apps/mcp/internal/bill/write.go`.

### F23 — `outbox_events` is written by handler code, so its coverage is undefined

**Where:** `apps/api/internal/bill/repo.go:152,218,265`,
`apps/api/internal/asset/repo.go:144,254`, `apps/mcp/internal/bill/write.go:126`.

**What is wrong:** the same write emits an outbox event from some code paths and not others.
Five event types exist across three files, for two entities (`bill`, `asset`). No entity's
lifecycle is completely covered: `notes`, `files`, `lists`, `maintenance_tasks`, `properties`,
`people`, `secrets` emit nothing, and nothing in the schema says whether that is deliberate
(nothing consumes them) or a gap.

This is #5314's pattern exactly: a side effect in handler code fires only when that handler
runs. `activity_log` covers 25 entity tables *because* it is a trigger; `outbox_events` covers
two entities *because* it is not.

**Why it matters:** whoever consumes `outbox_events` (webhooks, integrations, a future event
bus) sees a partial history and cannot tell it is partial. "Every bill change emits
`bill.updated`" is false — a `bills` UPDATE from any other path (a migration, the calendar
absorb, `assets_propagate_property()` re-pointing `property_id`) emits nothing. And
`assets_propagate_property()` **does** modify `bills` rows, so a bill can change with no outbox
event and no way for a consumer to know.

**Severity:** high for anything consuming the outbox; currently latent.

**Shape of the fix:** either (a) promote the outbox to trigger-based for the entities that need
events — `log_outbox_event(kind)` alongside `log_activity`, same shape,
caller-independent — or (b) declare `outbox_events` an internal API-only queue and document
that it is not a complete log, using `activity_log` (which *is*) wherever completeness matters.
**(a) is the better answer** given the epic wants agents watching the household; it reuses a
pattern already proven in this codebase.

### F24 — `activity_log` covers 25 of 39 tables; the gap that matters is `memberships`

**Where:** trigger presence.

**What is wrong:** one table. The 14 without `log_activity`: `activity_log`, `agent_tokens`,
`calendar_changes`, `file_blobs`, `file_search_deletions`, `guide_revisions`, `households`,
`invitations`, `memberships`, `notifications`, `outbox_events`, `password_reset_tokens`,
`secret_keys`, `users`. Excluding the log, the outbox, the blob store and the sync journal is
obviously right; `guide_revisions` is itself an audit trail. Credentials (`agent_tokens`,
`secret_keys`, `password_reset_tokens`) are deliberately excluded — **and this is right**:
auditing a credential table's writes into a family-readable feed is a leak vector.

**Why it matters:** `memberships` is the one real gap. Granting or revoking a family member's access — and
changing a role from `viewer` to `owner` — is the most security-significant write in the
product, and it produces no `activity_log` row. So "who promoted whom, and when?" cannot be
answered from the family's own history.

**Severity:** medium. A genuine security-audit gap.

**Shape of the fix:** `log_activity('membership')` on `memberships`, plus a `membership` member
in the F1 domain. Note the summary builder must not put a demotion into a feed readable by the
member being demoted without a decision about visibility — small, but it is a decision.

---

## What I am NOT finding (and want to say so)

A review that finds a problem in everything is not credible. These are **fine** and should not
be re-litigated:

1. **The `entity_kind` domain + `entity_household` + `enforce_entity_owner` +
   `rehome_entity_refs` machinery.** The best-engineered part of this schema: constrained
   types, cross-family enforcement in the database, deletion cleanup across 22 tables. This is
   exactly what the ticket asked whether we had — we have it.
2. **Tenancy via `household_id`.** Complete and consistent on every household-scoped table; the
   six exceptions are each correct.
3. **Money type consistency.** Every money column `NUMERIC(12,2)`, with two correct exceptions.
   No floats, no unconstrained numerics.
4. **Index coverage for household + time-range.** Every such query indexed; several partial
   where NULLs dominate.
5. **The denormalised `property_id` copies.** Maintained by a real trigger
   (`assets_propagate_property`), kept honest.
6. **`people` vs `memberships`.** Deliberately distinct concepts, kept in sync by triggers,
   correctly linked by `people.user_id`.
7. **`activity_log` as a mechanism.** Trigger-based, caller-independent, cascade-aware,
   actor-attributed, with a thought-through policy: column *names* are logged, not values,
   except an allowlist of money/date scalars. 072's design there is careful work.
8. **`notifications.entity_type` after 070.** Domain-constrained, family-enforced, repaired.
9. **`files`/document modelling.** Entity pair + person + category + metadata + text
   provenance. Nothing missing.

---

## Findings index

| ID | Finding | Severity | Evidence |
|---|---|---|---|
| F1 | `activity_log.entity_type`/`parent_type` unconstrained; vocabularies drift | high | probe: bogus type accepted |
| F2 | Bill subject: 3 nullable FKs, asymmetric CHECK, copied to 4 tables | medium | `bills_subject_check` def |
| F3 | `document_links.link_id` VARCHAR, no target constraint | medium | column type |
| F4 | 15 single-column FKs where 20 are composite; 14 targets lack the key | high | trigger-disabled probe |
| F5 | `households.default_property_id` can cross families | medium | no constraint |
| F6 | No payment ledger — payment history is one nullable date | high | `paid_date`/`paid_for_due_date` |
| F7 | bill→payee is `vendor_id` only; payee under-modelled | medium | 007, 043, 064, 065 |
| F8 | `calendar_objects` has no `(entity_type, entity_id)` index | low-med | catalogue |
| F10 | `notifications.type`/`channel` unconstrained | medium | 015 |
| F12 | "When due" stored 3 ways, no constraint between them | high | probe: due_day=5 + due_date=28 |
| F13 | `updated_at` trigger on 3 of 25 tables; Go forgets it | high | probe: unchanged after UPDATE |
| F14 | No soft-delete convention (`lists.archived` alone) | low | — |
| F15 | `is_autopay` nullable → `NOT is_autopay` under-counts | high (epic) | probe: 1+1≠3 |
| F17 | enum vs CHECK chosen chronologically | low | — |
| F18 | `memberships` has no `updated_at`/`updated_by` | low | columns |
| F20 | `household_id` unindexed on 8 tables; `vehicles` has 1 index | low-med | catalogue |
| F21 | 13 read FK columns unindexed; `memberships.user_id` on login path | medium | catalogue |
| F22 | Duplicate indexes on auth tables | low | catalogue |
| F23 | `outbox_events` handler-written → partial coverage, caller-dependent | high | 3 files, 2 entities |
| F24 | `memberships` not audited (security-significant) | medium | no trigger |
| F9, F11, F16, F19 | `files`, `notifications.entity_type`, money scales, people/memberships | **fine** | — |

---

## Remediation order

Every remediation is a ticket in story #679 (Schema remediation). Numbers are AMP ticket ids.

1. **#5508** — F13 — `updated_at` trigger everywhere (+ delete the 72 Go sites). Cheapest, widest.
2. **#5509** — F1 — `activity_entity_kind` domain on `activity_log`; align the three vocabularies.
3. **#5510** — F15 — `is_autopay` NOT NULL, decide the unknown state (the epic's core column).
4. **#5511** — F12 — one representation of "when due".
5. **#5512** (keys) then **#5519** (FKs) — F4 — `UNIQUE (id, household_id)` on 14 tables, then composite FKs on the 15 strays (also
   closes most of F21 and F8).
6. **#5513** — F23 — decide outbox: trigger-based, or documented as an incomplete API queue.
7. **#5518** — F24 (needs #5509) — audit `memberships`.
8. **#5514** — F6 (needs #5512) — `bill_payments` table (largest; #5497-adjacent).
9. **#5515** — F2 — `num_nonnulls(...) <= 1` on the five subject tables.
10. **#5520** — F20/F21/F8/F22 (needs #5512) — indexes not fixed by F4; **F22** duplicate drop.
11. **#5516** — F10/F3 — `notifications.type`/`channel` CHECKs; **F3** `document_links.link_type` CHECK.
12. **#5521** — F5 (needs #5512) — `default_property_id` composite FK.
13. **#5517** — F7 (a person decides; holds its implementation ticket) — payee decision (needs a person).
14. **F14/F17/F18** — decisions and cleanup; no implementation until decided.
