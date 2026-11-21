# War-game household seed

`household.py` puts a realistic American household into **the war-game household
only** — the bills a real family juggles, the accounts they come out of, the
people you call when one goes wrong, and the four things the family actually
knows how to do.

The old war-game household was a handful of bills. A handful of bills cannot
find the failures that matter: the invoice nobody set up, the one that quietly
went overdue, the payee with no phone number. This is the household the game
plays against instead.

```bash
# seed / re-seed (idempotent)
DATABASE_URL=postgres://... ./household.py

# what is in it, straight from the database
DATABASE_URL=postgres://... ./household.py      # prints the report after seeding

# no database: print the SQL
./household.py --print-sql [--household-id <uuid>] | psql "$DATABASE_URL"
```

Run it from a pod in the `home-os` namespace (or through a port-forward) the same
way `mcpcall.py` is run. It needs `psql` on PATH; `--print-sql` needs only
`python3`.

## What it creates

| | Count | What |
|---|---|---|
| Bills | **11** | The mix below. |
| Payment accounts | 3 | Joint checking ··4821, credit card ··3312, savings ··7704. **Last-4 only.** |
| Contacts | 12 | Utility, water, trash, internet, phone, streaming, plumber, dentist, school, pediatrician, vet, mortgage servicer. Phone + email + website. |
| Guides | 4 | Reset the router · pediatrician after-hours · the babysitter's guide · filing an insurance claim. |
| People | 5 | Jacob, Amber, Maya (9), Noah (6), Lily (3). |
| Pets | 2 | Biscuit the retriever, Mochi the cat — both linked to the vet contact. |
| Vehicles | 2 | A 2019 Odyssey and a 2017 Camry. No VIN. |
| Property | 1 | 18 Orchard Lane. |
| Assets | 3 | Furnace, water heater, router — the things guides hang off. |

### The bills, and why each one is there

The **mix** is the test. Every row exists to fail a different way.

| Bill | Amount | Autopay | Account | Payee | Why |
|---|---|---|---|---|---|
| Mortgage | $2,340 /mo, due 1st | **on** | ··4821 | Hudson Mortgage (vendor) | The one that must never be missed. |
| Electric | $182 /mo, due 18th | **on** | ··4821 | Hudson Valley Electric (vendor) | Autopay on, amount moves (≈$240 summer, ≈$150 spring). |
| Internet | $75 /mo, due 22nd | **on** | ··4821 | Northlight Fiber (vendor) | Flat, autopay, boring — the control case. |
| Phone family plan | $140 /mo, due 12th | **on** | ··3312 | CellOne Wireless (vendor) | Autopay off the **credit card**, not checking. |
| Streamly subscription | $17.99 /mo, due 5th | **on** | ··3312 | Streamly (vendor) | Small and silent: the bill a household forgets. |
| Water | $96.40, **quarterly**, due 12/10 | off | ··4821 | Warwick Water (vendor) | Manual and variable. |
| Trash and recycling | $63.00, **quarterly**, due 1/8 | off | ··4821 | Hudson Valley Waste (vendor) | Quarterly, manual, easy to let slide. |
| Pediatrician copay | $35.00, once, due 12/10 | off | ··3312 | **(free text)** | Payee is not a vendor: contact routing must fall back to the text. |
| Field trip fee — Noah | $45.00, once, due 12/1 | off | ··4821 | Riverside Elementary (vendor) | A school one-off on a known vendor. |
| **Maple Family Dental — crown balance** | **$412.00**, once, due 11/28 | **off** | **none** | Maple Family Dental (vendor) | **The trap this epic exists for.** Nobody knows how it gets paid. |
| **Orchard Plumbing invoice 1042** | **$285.00**, once, due **11/14** | off | ··4821 | Orchard Plumbing (vendor) | **Already overdue** — 7 days past when seeded. |

So the mix is 5 autopay + account, 4 manual + account, 1 manual with **no**
account, and 1 already overdue. One payee is a vendor, one is free text, and
most are vendors with a phone number — so tap-to-contact has something to work
with and something to fall back on.

## Idempotent: re-run it as often as you like

Every row's id is `uuid5(namespace, household_id + "/" + key)`, and a run clears
the war-game household's own bills, guides, contacts, people, pets, vehicles,
properties and assets before writing them again — in one transaction. So:

- a second run leaves the **same rows with the same ids** (a scenario that
  captured a bill id still finds that bill),
- there is never a duplicate,
- nothing outside the war-game household is read or written.

Verified by running it three times in a row against a migrated Postgres: counts
and bill ids identical every time.

## It will not touch a real household

The target must be a household whose `settings->>'wargame_seed'` is
`wargame-household-v1`. If you name a household by `--household-id` that does not
carry the marker, the script **refuses** and says why — it deletes bills and
contacts, so it does not run on a household it cannot prove is the war game's.

`--adopt` overrides that refusal, but only for a household that still looks like
the war game's: it is named *The War Game Family*, **or** every login on it is an
`@example.com` test account. A household with a real login is refused even with
`--adopt`. If two households carry the marker the script refuses and asks which.

## No real data

- Emails are `@example.com`, phones are `555-01xx` (nothing reachable).
- Payment accounts are **last-4 only**, and the script has no field that could
  hold a full number or a routing number.
- No VINs, no real addresses, no real names beyond the personas the war game
  already uses.

## Schema gap: #5497 has not landed

Checked against `origin/feat/family-hub` on 2026-11-21. `payment_accounts`,
`bills.autopay_account_id`, `bills.payee_vendor_id` and `bills.payee_name` (from
#5497) **do not exist yet**. `bills` today has `is_autopay` and `account_number`
(migration 020) plus `vendor_id`.

So the seed writes what exists:

- `is_autopay` — the flag
- `account_number` — the last-4, as `..4821` (the column's historical name; it
  cannot hold more than a last-4 in this seed)
- `vendor_id` — the payee, when it is a known contact
- the free-text payee in `notes`

and ends with **one guarded `DO` block** that upgrades itself the moment #5497's
columns exist: it inserts the three `payment_accounts` rows (last-4 only), links
each bill's `autopay_account_id` by last-4, sets `payee_vendor_id = vendor_id`,
and fills `payee_name` for the medical bill. Today that block matches nothing and
does nothing. After #5497 merges, **re-run the seed** and the new columns fill in
— no part of this script has to change.

## Safety checks worth knowing

- The seed runs in **one transaction**; a failure commits nothing (`psql -v
  ON_ERROR_STOP=1`, and a non-zero exit says so).
- It deletes a bill's **calendar mirror** (`calendar_objects` with
  `entity_type='bill'`) along with the bill. The mirror is written by the app
  (`billcal`), not by a trigger, so clearing it here is what stops a re-seed
  leaving orphan mirror events behind.
- **Pets are deleted before contacts.** `pets.vet_vendor_id` is a composite FK to
  `(vendors.id, household_id)`; deleting vendors first makes the database try to
  clear `pets.household_id`, which fails. Order is the fix, not a workaround.
- `people.color` is left for the database to default (071), so the seed does not
  pin a palette the UI may change.
