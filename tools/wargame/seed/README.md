# War-game household seed

`household.py` puts a realistic American household into **the war-game household
only** — the bills a real family juggles, the accounts they come out of, the
people you call when one goes wrong, and the four things the family actually
knows how to do. Run it before a war-game round; re-run it whenever a round has
written garbage into the household.

```bash
# from a pod in the home-os namespace, or through a port-forward (same as mcpcall.py)
DATABASE_URL=postgres://... ./tools/wargame/seed/household.py     # seed / re-seed + report
DATABASE_URL=postgres://... ./tools/wargame/seed/household.py --allow-household <uuid> --adopt --show-scope
DATABASE_URL=postgres://... ./tools/wargame/seed/household.py --new-household   # create one
DATABASE_URL=postgres://... ./tools/wargame/seed/household.py --today 2026-11-22   # pin the date
./tools/wargame/seed/household.py --print-sql | psql "$DATABASE_URL"   # no DB needed
```

It needs `psql` on PATH to apply. `--print-sql` needs only `python3`.

## Why a seed at all

The war-game household used to be a handful of bills, so the game could not find
the failures that matter. A realistic mix finds them: the dental bill nobody
finished setting up is the trap the autopay epic exists for, the plumber's
invoice that is already a week past due is the escalation case, and the
pediatrician copay whose payee is free text rather than a saved contact is the
case that makes payee-contact routing fall back instead of reaching for a phone
number that is not there.

## What it contains

Twelve bills in a deliberate mix. Read through `billdue.AttentionFor` — the ONE
rule that the API response, the MCP bill tools and the notification worker all
call — **all four attention states are present**:

| State | Count | Which bills |
|---|---|---|
| `automatic` | **6** | Mortgage, electric, internet, phone, Streamly, **homeowners insurance**, each with a `payment_accounts` link. Insurance is due in **2 days**, so "autopay means silence" has a bill to be silent about at the moment a manual one would shout. |
| `due_soon` | **4** | Water (due in 2 days), the school field trip (3 days), trash (75 days), the pediatrician copay (60 days) — manual, each with a paying account on record. |
| `needs_setup` | **1** | **Maple Family Dental — crown balance, $412**, due in 3 days, no account. The trap. |
| `overdue` | **1** | **Orchard Plumbing invoice 1042, $285**, 8 days past due, entered 25 days ago so the rule can see it existed when the date passed. |

Supporting data: **3 payment accounts** by last-4 only (Joint checking `..4821`,
Credit card `..3312`, Emergency savings `..7704`), each with a name, an
institution and an account type; **13 contacts** with phone, email and website;
**4 KB docs in `kb_docs`** (`how-to/pets/vet-after-hours`,
`how-to/networking/router-reset`, `sitters/what-the-babysitter-needs-to-know`,
`how-to/money/insurance-claim`); five people (two parents and three kids); two
pets linked to their vet contact; two vehicles; one property; three assets.

Every phone is `555-01xx` and every email is `@example.com`, so nothing in it is
reachable. There are no VINs, no full account numbers and no routing numbers —
the script has no field that could hold one.

### The knowledge goes in `kb_docs`, not `guides`

`guides` is a **different feature**: a how-to attached to a property, an asset or
a vehicle, with its own page and its own revisions table. The family's "how we do
this here" belongs in the knowledge base (migration 076), which is
path-addressed, readable by every role including a housesitter, and indexed for
search by meaning (#5502). Writing the same knowledge to both would be two homes
for one fact.

A re-seed **deletes the four guides the old version of this script wrote**
(#5504) — by their exact deterministic ids, never by household — so a household
seeded before #5544 does not keep a stale second copy. A guide the family wrote
itself is never touched.

### Two facts that decide a bill's state, and are not obvious

**A manual bill can only have a paying account through `bills.account_number`.**
Migration 075's `bills_autopay_account_check` refuses `autopay_account_id` on a
bill with `is_autopay = false`, and every reader of "has a payment source"
(`apps/api/internal/bill.hasPaymentSource`, its MCP twin, the worker) counts
`autopay_account_id IS NOT NULL OR btrim(account_number) <> ''`. So `due_soon`
is reachable **only** through the pre-075 `account_number` column: the manual
bills set `..<last4>` there, and only autopay bills get a `payment_accounts`
link. The seed refuses to write either combination that the database would
refuse.

**`created_at` is part of the overdue test.** `billdue.overdue` refuses to call a
bill late when it was entered after the due date it missed — right for a paper
bill imported after the fact, and the reason an "overdue" row inserted with the
table default is never overdue at all. The plumber invoice therefore carries an
explicit backdated `created_at`; every other bill keeps `NOW()`.

### Dates move with the day you run it

A frozen due date is "due soon" on the day it was written and something else a
fortnight later. Every non-monthly `due_date` is computed at run time from
**today in the household's timezone**, so a round replayed on any day exercises
the same four states. `--today YYYY-MM-DD` pins it for a reproducible round.

## Idempotency: how it works and why ids are stable

Every row's id is `uuid5(SEED_NAMESPACE, household_id + "/" + <key>)`, so the same
household always produces the same ids. A run clears the war-game household's own
bills, payment accounts, KB docs, contacts, people, pets, vehicles, properties and
assets first, in one transaction, then writes them again. `kb_docs` is also
written with `ON CONFLICT (household_id, path) DO UPDATE` — the table's own unique
key — so it is idempotent on the key the table itself uses even if the clear were
skipped.

Three consecutive runs against a freshly migrated Postgres give identical counts
**and identical row ids, md5 for md5**, and still exactly one household. That
matters because a scenario can capture a bill id and still find that bill after a
re-seed. Nothing outside the target household is read or written.

## Refusing a real household

The gate is **explicit intent**, and there are exactly two ways to pass it:

1. the household already carries `households.settings->>'wargame_seed' =
   'wargame-household-v1'` (a previous explicit run put it there), or
2. the operator passes **all three** of
   `--allow-household <id> --adopt --show-scope`.

```bash
# seed the war-game household (all three, or nothing)
./tools/wargame/seed/household.py \
    --allow-household <uuid> --adopt --show-scope
```

| flag | what it says |
|---|---|
| `--allow-household <id>` | **which** household. This *is* the permission. |
| `--adopt` | you accept its current contents being replaced. Requires `--allow-household`; it never names a household by itself. |
| `--show-scope` | print exactly what gets deleted and what survives, before writing. |

`--adopt` without an id is an error, and naming an id without `--show-scope` is an
error, because the destructive scope has to have been shown. **No id at all is
refused** — there is no "the only household in the database" and no name match;
the refusal prints the household list so you can name one. To seed a brand-new
household instead, pass `--new-household` (touches nothing that exists).
`--dry-run` runs the gate and prints the scope without writing, which is how to
check which ids are allowed.

### Why it is not a heuristic any more (#5607)

The old gate admitted a household that was named *The War Game Family* **or**
whose every login was an `@example.com` test account. On the deployed war game
that guess can never be true: the household is named `Sims Family (war game)`
and its personas are real Dex identities at `@simstech.cloud`. So the seed
refused the very household it exists for, and the only documented workaround was
to hand-stamp the marker in SQL — which is worse than useless, because the marker
check returns **before** every other check, so stamping it silently disarms the
gate for all later runs.

A login on a foreign domain is now a **loud warning**, not a refusal: on a
deployment with real personas it is expected, and the operator has already said
which household they mean. Run with `--dry-run` first if you want to see what
that household actually contains.

## Gotchas that cost time

**Delete pets before contacts.** `pets.vet_vendor_id` is a composite foreign key
to `(vendors.id, household_id)`. Deleting the vendors first makes the database
try to clear `pets.household_id`, which fails with a constraint error and rolls
the whole seed back. The order in the `DELETE` block is the fix.

**Delete a bill's calendar mirror by hand.** `billcal` writes a `calendar_objects`
row per bill, from application code, not from a trigger. Clearing only `bills`
leaves an orphan mirror event behind for every bill that was replaced, and the
next round reads bill-like entries that are not bill state. The seed deletes
`calendar_objects` where `entity_type = 'bill'` for the household's bills first.

**Delete the accounts after the bills.** `bills.autopay_account_id` is a composite
FK to `payment_accounts`; ON DELETE SET NULL means the order works either way, but
the delete order says what the dependency is.

Leave `people.color` out of the inserts: the `people_set_default_color()` trigger
(migration 071) fills it from the person's id, so the seed does not pin a palette
the UI may change.

**A monthly bill may not carry a `due_date`** (082). `frequency` selects which
column is authoritative: monthly reads `due_day`, everything else reads
`due_date`, and the other must be NULL. The seed's `due_sql` raises on a row that
sets both rather than letting the insert fail at the database.

## Verifying it, the way the ticket asks

After seeding, the script prints a report you can paste: bills by autopay state
and paying account, every bill with its due date and the date it was entered, the
three 075 constraints checked against the seeded rows (each must be 0 offenders),
the payment accounts with their last four and how many bills autopay from each,
and the KB docs by path.

The attention states and the `kb_search` behaviour are separate proofs — the
states come from the real `billdue.AttentionFor`, and search comes from the real
reconciler plus Typesense. Neither is something a SQL report can assert.

## Key files

- `tools/wargame/seed/household.py` — the seed: `PEOPLE`, `PETS`, `VEHICLES`,
  `PROPERTY`, `ASSETS`, `VENDORS`, `PAYMENT_ACCOUNTS`, `BILLS` (each with a `why`
  naming the failure it is there to find) and `KB_DOCS`; `Seed.build()` emits the
  SQL; `due_sql`, `bill_payee_sql` and `bill_paying_sql` refuse the rows the
  database would refuse; `find_target()` is the safety check;
  `print_verification()` is the report.
- `packages/db/migrations/075_bill_autopay_payment_account.up.sql` —
  `payment_accounts`, `bills.autopay_account_id`, `bills.payee_name`, and the
  CHECKs the seed obeys.
- `packages/db/migrations/076_kb_docs.up.sql` — `kb_docs`, its path CHECK and
  `UNIQUE (household_id, path)`.
- `packages/db/migrations/082_bill_due_representation.up.sql` — which due-date
  column a frequency makes authoritative.
- `packages/billdue/attention.go` — the rule the mix is built to exercise.
- `packages/billdue/policy.go` — what each state does about a reminder.
- `packages/billcal/billcal.go` — the app-side bill mirror the seed has to clean
  up itself.
