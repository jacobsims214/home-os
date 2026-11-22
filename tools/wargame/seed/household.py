#!/usr/bin/env python3
"""Seed a realistic American household for the war game (#5504, #5544).

The family-hub war game is only as good as the household it plays against. The
handful of bills the game started with cannot find the failures that matter: a
bill nobody set up, an invoice that quietly went overdue, a payee you cannot
reach. This seeds one household that can.

What it creates, in the WAR-GAME household only:

  bills        12, a deliberate autopay mix -- see them through
               billdue.AttentionFor, the ONE rule (#5498), and all four states
               are present:
                 automatic   6 autopay ON, each linked to a payment account
                             through bills.autopay_account_id. One of them
                             (homeowners insurance) is due in 2 days, so
                             "autopay means silence" has a bill to be silent
                             about at the moment a manual one would shout.
                 due_soon    4 manual bills with a paying account on record,
                             two of them inside the 3-day reminder lead
                             (water, the school field trip).
                 needs_setup 1 manual bill with NO account, due in 3 days --
                             inside the 7-day needs_setup lead. THE TRAP (the
                             dental crown balance).
                 overdue     1 already past its due date and unpaid (the
                             plumber's invoice), with created_at backdated so
                             the rule can see that it existed when the date
                             passed.
                 1 payee that is a known vendor and 1 free-text payee (medical)
  payment accounts  3, each with a name, an institution, an account_type and a
                    real 4-digit last_four (checking ..4821, credit card
                    ..3312, savings ..7704). Last-4 only, by construction:
                    there is no field here that could hold a full number.
  contacts     13 vendors with phone / email / website, so tap-to-contact works
  kb docs      4 rows in kb_docs -- NOT in guides, which is a different feature
               (a how-to attached to a property, asset or vehicle):
                 how-to/pets/vet-after-hours               <- the acceptance case
                 how-to/networking/router-reset
                 sitters/what-the-babysitter-needs-to-know
                 how-to/money/insurance-claim
  people       5 (two parents, three kids), 2 pets, 2 vehicles, 1 property,
               3 assets -- enough that every bill links to a real thing

## Why this ticket exists (#5544)

#5498's acceptance criteria were unverifiable on the deployed household, and
#5496 measured exactly why: every bill had is_autopay = f, no
autopay_account_id, payment_accounts was empty and kb_docs was empty. This
script was written before migrations 075 (autopay + payment_accounts) and 076
(kb_docs) existed, so it set none of it, and it wrote its "family knowledge"
into `guides`. Both are fixed here: the real columns and the real table are
written, and there is no guarded fallback left that silently does nothing.

## The two facts that decide a bill's state (read from the code, not guessed)

1. **A manual bill can only have a paying account through the legacy
   `bills.account_number`.** 075's `bills_autopay_account_check` refuses
   `autopay_account_id` on a bill with `is_autopay = false`, and
   `apps/api/internal/bill.hasPaymentSource` (same rule in apps/mcp and in the
   worker) counts `autopay_account_id IS NOT NULL OR btrim(account_number) <> ''`.
   So `due_soon` is reachable only through `account_number`: the manual bills
   below set `..<last4>` there, and only the autopay bills get a
   `payment_accounts` link.

2. **`created_at` is part of the overdue test.** `billdue.overdue` refuses to
   call a bill late if the bill was created after the due date it missed, which
   is right for a paper bill imported after the fact and is why an "overdue"
   row inserted with the table default is never overdue at all. The plumber
   invoice therefore carries an explicit `created_at` before its due date;
   every other bill keeps NOW(), so a monthly bill whose day has already passed
   this month is not mislabelled late on the day it is seeded.

## Dates move with the day you run it

`AttentionFor` answers from (autopay, paying account, schedule, today), so a
frozen date is "due soon" on the day it was written and something else a
fortnight later. Every non-monthly `due_date` here is therefore computed at run
time from **today in the household's timezone** -- `due_in` is days from
today, negative for the past -- so a round replayed on any day exercises the
same four states. `--today` pins it for a reproducible round.

## Constraints it keeps

  * IDEMPOTENT. Every row carries an id derived from
    uuid5(namespace, household_id + "/" + key), and one run clears the war-game
    household's own seed rows before writing them again, in one transaction.
    Re-running gives the same rows with the same ids, never a second copy.
    kb_docs has its own UNIQUE (household_id, path); it is cleared the same way
    and written with ON CONFLICT (household_id, path) DO UPDATE, so it is
    idempotent on the key the table itself uses.
  * NEVER TOUCHES A REAL HOUSEHOLD BY ACCIDENT. The gate is EXPLICIT INTENT, and
    there are exactly two ways to pass it: the household already carries
    settings->>'wargame_seed' = this script's marker (a previous explicit run),
    or the operator passed --allow-household <that id> --adopt --show-scope.
    There is no name match, no "every login is an @example.com test account",
    and no "the only household in the database": those were guesses about what a
    household looks like, and #5607 showed the guess is wrong on the deployed
    war game (real @simstech.cloud Dex personas, household named "Sims Family
    (war game)"), which made the seed unrunnable there. A list of logins on a
    foreign domain is now a LOUD WARNING, not a refusal.
  * NO REAL DATA. Emails are @example.com, phones are 555-01xx, account numbers
    are last-4 only and fictional, addresses and names are invented.

usage:
  # re-seed the household a previous run created (it carries the marker)
  DATABASE_URL=postgres://... ./household.py

  # seed a household that does not carry the marker: all three, or nothing
  DATABASE_URL=postgres://... ./household.py \
      --allow-household <uuid> --adopt --show-scope
        --allow-household <uuid>  WHICH household (the permission itself)
        --adopt                   you accept its current contents being replaced
        --show-scope              print what gets deleted and what survives first

  # create a household from scratch (touches nothing that exists)
  DATABASE_URL=postgres://... ./household.py --new-household

  DATABASE_URL=postgres://... ./household.py --today 2026-11-22
  ./household.py --print-sql                                 # SQL to stdout only
  ./household.py --print-sql --household-id <uuid> | psql ...

Needs psql on PATH to apply (the repo's own images ship it); --print-sql needs
nothing but python3.
"""
import argparse
import datetime
import os
import subprocess
import sys
import uuid
import zoneinfo

# --------------------------------------------------------------------------
# Identity of the war-game household.
# --------------------------------------------------------------------------

# The marker written into households.settings. A household without it is not
# the war game's, and this script will not write to or clear one.
MARKER_KEY = "wargame_seed"
MARKER_VALUE = "wargame-household-v1"

HOUSEHOLD_NAME = "The War Game Family"
HOUSEHOLD_TZ = "America/New_York"

# Deterministic ids: id = uuid5(SEED_NAMESPACE, household_id + "/" + key), so a
# re-seed keeps every id and a scenario script can rely on one.
SEED_NAMESPACE = uuid.UUID("8b6b0e37-6a2c-5b3f-9d1e-0b6a5c4d3e21")

# Placeholder household id for --print-sql with no --household-id: the output is
# the "create a fresh household" form and the reader replaces this one token.
PRINT_SQL_PLACEHOLDER = "00000000-0000-0000-0000-000000000000"

# Test-account domain. A household whose logins are all here is a war-game
# household, nobody's real family.
TEST_EMAIL_DOMAIN = "example.com"

# --------------------------------------------------------------------------
# The household. Money is in dollars as strings; SQL casts to numeric.
# --------------------------------------------------------------------------

PEOPLE = [
    # key, display name, relationship, birthdate, school, grade, allergies, notes
    dict(key="jacob", name="Jacob", relationship="parent", birthdate="1984-03-12",
         notes="Handles money, the house and the cars."),
    dict(key="amber", name="Amber", relationship="parent", birthdate="1986-09-30",
         notes="Handles the kids' schedules, health and the house paperwork."),
    dict(key="maya", name="Maya", relationship="child", birthdate="2017-06-18",
         school="Riverside Elementary", grade="4", allergies=["peanuts"],
         notes="Piano Mondays 4:15. EpiPen in the front pocket of her backpack."),
    dict(key="noah", name="Noah", relationship="child", birthdate="2020-02-02",
         school="Riverside Elementary", grade="1",
         notes="Soccer Tue/Thu 5:30."),
    dict(key="lily", name="Lily", relationship="child", birthdate="2023-04-27",
         school="Riverside Preschool", notes="Preschool M/W/F mornings."),
]

PETS = [
    dict(key="biscuit", name="Biscuit", species="dog", breed="Golden Retriever",
         date_of_birth="2021-05-04", vet_key="vet",
         notes="40 lb. Thunderstorms: the vet says the crate, not the closet."),
    dict(key="mochi", name="Mochi", species="cat", breed="Domestic shorthair",
         date_of_birth="2019-11-11", vet_key="vet",
         notes="Indoor only. Hates the carrier."),
]

VEHICLES = [
    dict(key="minivan", year=2019, make="Honda", model="Odyssey", color="Silver",
         license_plate="WG 4821",
         notes="The family car. Oil change every 5,000 miles; last one at 44,100."),
    dict(key="camry", year=2017, make="Toyota", model="Camry", color="Blue",
         license_plate="WG 3312", notes="Jacob's commute car."),
]

PROPERTY = dict(
    key="house", name="18 Orchard Lane", address="18 Orchard Lane, Warwick, NY 10990",
    property_type="single_family",
    notes="Built 1978. Oil heat; septic pumped 2024.",
)

ASSETS = [
    dict(key="hvac", name="Furnace", category="HVAC", manufacturer="Carrier",
         model="59SC2B", purchase_date="2019-10-02",
         notes="Filter 20x25x1, every 90 days."),
    dict(key="water-heater", name="Water heater", category="Plumbing",
         manufacturer="Rheem", model="PROG50", purchase_date="2021-03-15",
         notes="50 gal gas. Flush annually."),
    dict(key="router", name="Router", category="Network", manufacturer="Netgear",
         model="RAX50", purchase_date="2023-01-08",
         notes="In the hall closet, on the top shelf. The reset steps are in the KB."),
]

# Contacts (vendors). Every one has a phone and most an email, so a person (and
# an agent) can tap to contact. 555-01xx and example.com only.
VENDORS = [
    dict(key="electric", name="Hudson Valley Electric", specialty="Electric utility",
         phone="555-0142", email="billing@example.com",
         website="https://www.example.com/hudson-valley-electric",
         trades=["utility", "electric"],
         notes="Account 55-0188-42. Outages: 555-0143. Budget billing available."),
    dict(key="water", name="Warwick Water District", specialty="Water utility",
         phone="555-0151", email="accounts@example.com",
         website="https://www.example.com/warwick-water",
         trades=["utility", "water"],
         notes="Billed quarterly; the amount moves with the season."),
    dict(key="trash", name="Hudson Valley Waste", specialty="Trash and recycling",
         phone="555-0129", email="service@example.com",
         website="https://www.example.com/hv-waste", trades=["utility", "trash"],
         notes="Tuesday pickup, 6am. Bulk pickup needs a call two days ahead."),
    dict(key="internet", name="Northlight Fiber", specialty="Internet",
         phone="555-0175", email="support@example.com",
         website="https://www.example.com/northlight-fiber", trades=["internet"],
         notes="1 Gb. Router is ours, not theirs."),
    dict(key="phone", name="CellOne Wireless", specialty="Mobile phone",
         phone="555-0186", email="billing@example.com",
         website="https://www.example.com/cellone", trades=["phone"],
         notes="Family plan, 4 lines. Amber is the account holder."),
    dict(key="streaming", name="Streamly", specialty="Streaming subscription",
         email="help@example.com", website="https://www.example.com/streamly",
         trades=["subscription", "streaming"],
         notes="Cancel online; there is no phone number."),
    dict(key="insurance", name="Hudson Insurance Group", specialty="Home insurance",
         phone="555-0124", email="claims@example.com",
         website="https://www.example.com/hudson-insurance",
         trades=["insurance", "finance"],
         notes="Policy 8812-4. Home and auto on one bill, quarterly."),
    dict(key="plumber", name="Orchard Plumbing", specialty="Plumber",
         phone="555-0117", email="office@example.com",
         website="https://www.example.com/orchard-plumbing", trades=["plumber"],
         notes="Ask for Dave. Emergency line rings through after 6pm."),
    dict(key="dentist", name="Maple Family Dental", specialty="Dentist",
         phone="555-0163", email="frontdesk@example.com",
         website="https://www.example.com/maple-family-dental", trades=["dentist"],
         notes="Maya and Noah go here. Billing questions: press 2."),
    dict(key="school", name="Riverside Elementary", specialty="Elementary school",
         phone="555-0108", email="office@example.com",
         website="https://www.example.com/riverside-elementary", trades=["school"],
         notes="Front office 8am-3:30pm. Absence line: 555-0109."),
    dict(key="pediatrician", name="Riverside Pediatrics", specialty="Pediatrician",
         phone="555-0134", email="billing@example.com",
         website="https://www.example.com/riverside-pediatrics",
         trades=["medical", "pediatrician"],
         notes="After-hours nurse line: 555-0135. Billing: press 3."),
    dict(key="vet", name="Warwick Animal Hospital", specialty="Veterinarian",
         phone="555-0195", email="frontdesk@example.com",
         website="https://www.example.com/warwick-animal", trades=["vet"],
         notes="Biscuit and Mochi. Closed Sundays."),
    dict(key="mortgage", name="Hudson Mortgage Servicing", specialty="Mortgage servicer",
         phone="555-0122", email="payments@example.com",
         website="https://www.example.com/hudson-mortgage",
         trades=["mortgage", "finance"],
         notes="Loan 4402-88. Escrow covers taxes and insurance."),
]

# Payment accounts (migration 075). Every field the ticket asks for -- name,
# institution, account_type, a real 4-digit last_four -- and nothing that could
# hold a full account or routing number, because payment_accounts has no such
# column and last_four is CHECKed to exactly four digits.
PAYMENT_ACCOUNTS = [
    dict(key="checking", name="Joint checking", institution="Hudson Valley Bank",
         account_type="checking", last_four="4821",
         notes="Both paychecks land here. Most bills autopay from this one."),
    dict(key="credit", name="Credit card", institution="Hudson Valley Bank",
         account_type="credit_card", last_four="3312",
         notes="Paid in full on the 20th. Subscriptions and phone are on it."),
    dict(key="savings", name="Emergency savings", institution="Hudson Valley Bank",
         account_type="savings", last_four="7704",
         notes="Emergency fund. Nothing autopays from here."),
]

# Bills. The MIX is the test, so each row states its intent in `why`, and the
# fields that decide the state are EXPLICIT:
#
#   autopay          whether the bill pays itself (bills.is_autopay, NOT NULL)
#   autopay_account  a PAYMENT_ACCOUNTS key: the account it comes out of, which
#                    becomes bills.autopay_account_id. Required when autopay is
#                    on, and illegal when it is off (075's CHECK).
#   manual_account   the last-4 a MANUAL bill is paid from, as
#                    bills.account_number. The only way a manual bill can have
#                    "an account on record" -- see the module docstring.
#   payee_vendor     a VENDORS key, or None when the payee is free text
#   payee_name       the free-text payee, used when payee_vendor is None
#   subject          ("property"|"vehicle"|"asset", key), or None
#   frequency        monthly | quarterly | semiannual | annual | once
#   due_day          monthly bills: day of the month
#   due_in           every other bill: days from TODAY (negative = past)
#   created_days_ago backdate created_at (default 0 = now). Only the overdue
#                    bill needs it; see the docstring.
BILLS = [
    # ---- autopay ON, linked to a payment account -> attention 'automatic' ----
    dict(key="mortgage", name="Mortgage", amount="2340.00", category="Housing",
         frequency="monthly", due_day=1, autopay=True, autopay_account="checking",
         payee_vendor="mortgage", subject=("property", "house"),
         why="The one bill that must never be missed. Autopay on joint checking."),
    dict(key="electric", name="Electric", amount="182.00", category="Utilities",
         frequency="monthly", due_day=18, autopay=True, autopay_account="checking",
         payee_vendor="electric", subject=("property", "house"),
         notes="Varies with the season: about $240 in July and August, about $150 "
               "in March and April. Budget billing is off.",
         why="Autopay on, but the amount moves -- a reminder that autopay is not "
             "the same as predictable."),
    dict(key="internet", name="Internet", amount="75.00", category="Utilities",
         frequency="monthly", due_day=22, autopay=True, autopay_account="checking",
         payee_vendor="internet", subject=("property", "house"),
         why="Flat, autopay, boring -- the control case."),
    dict(key="phone", name="Phone family plan", amount="140.00",
         category="Utilities", frequency="monthly", due_day=12, autopay=True,
         autopay_account="credit", payee_vendor="phone",
         notes="Four lines: Jacob, Amber, Maya and the house line.",
         why="Autopay off the credit card, not checking: the paying account is "
             "not always the same one."),
    dict(key="streaming", name="Streamly subscription", amount="17.99",
         category="Subscriptions", frequency="monthly", due_day=5, autopay=True,
         autopay_account="credit", payee_vendor="streaming",
         why="Small, silent, autopay. The bill a household forgets it has."),
    dict(key="insurance", name="Homeowners insurance", amount="486.00",
         category="Insurance", frequency="quarterly", due_in=2, autopay=True,
         autopay_account="checking", payee_vendor="insurance",
         subject=("property", "house"),
         notes="Home and auto on one policy, billed quarterly. Renews in March.",
         why="THE SILENCE CASE (#5498): autopay on and an account linked, due in "
             "two days -- inside the window where a manual bill would earn a "
             "reminder -- so 'autopay means silence' has something to be silent "
             "about. Nothing should be notified for this bill."),
    # ---- manual WITH a paying account -> attention 'due_soon' ----
    dict(key="water", name="Water", amount="96.40", category="Utilities",
         frequency="quarterly", due_in=2, autopay=False, manual_account="4821",
         payee_vendor="water", subject=("property", "house"),
         notes="Quarterly, and the amount changes every quarter (summer watering).",
         why="Manual, an account on record, and due in two days: 'due_soon' "
             "inside the 3-day reminder lead, so a reminder fires. The mirror "
             "image of the insurance bill above."),
    dict(key="field-trip", name="Field trip fee - Noah", amount="45.00",
         category="School", frequency="once", due_in=3, autopay=False,
         manual_account="4821", payee_vendor="school", subject=None,
         notes="Permission slip due back tomorrow. Check or the school portal.",
         why="A one-off a school sends home, on a known vendor, due in three "
             "days: due_soon with a reminder due today."),
    dict(key="trash", name="Trash and recycling", amount="63.00",
         category="Utilities", frequency="quarterly", due_in=75, autopay=False,
         manual_account="4821", payee_vendor="trash", subject=("property", "house"),
         why="Manual with an account, but months out: still due_soon (the state "
             "does not depend on the distance) and NOT yet reminded -- the case "
             "that separates the state from the notification policy."),
    dict(key="copay", name="Pediatrician copay", amount="35.00", category="Medical",
         frequency="once", due_in=60, autopay=False, manual_account="3312",
         payee_vendor=None, payee_name="Riverside Pediatrics (billing: 555-0134)",
         subject=None,
         notes="Payee is free text: this one came from the statement Amber brought "
               "home, not from a saved contact. Statement 11/10.",
         why="A manual bill whose payee is NOT a vendor -- contact routing has to "
             "fall back to the payee text."),
    # ---- the trap: manual, NO account, inside the needs_setup lead ----
    dict(key="dental", name="Maple Family Dental - crown balance", amount="412.00",
         category="Dental", frequency="once", due_in=3, autopay=False,
         manual_account=None, payee_vendor="dentist", subject=None,
         notes="Amber entered this from the statement and never finished setting "
               "it up: no autopay, no account. Nobody knows how it gets paid.",
         why="THE TRAP this epic exists for: autopay off, no paying account, and "
             "a real amount, due in three days -- inside the 7-day needs_setup "
             "lead, so the family is told early that something is MISSING."),
    # ---- already overdue ----
    dict(key="plumber", name="Orchard Plumbing invoice 1042", amount="285.00",
         category="Home repair", frequency="once", due_in=-8, autopay=False,
         manual_account="4821", payee_vendor="plumber",
         subject=("property", "house"), created_days_ago=25,
         notes="Kitchen shutoff valve replaced last month. Invoice 1042, net 30 "
               "from the visit -- it is already past due.",
         why="Already past its due date and nothing paid it: the escalation "
             "case. created_at is backdated before the due date, because "
             "billdue.overdue refuses to call a bill late when the bill was "
             "entered after the date it missed."),
]

# The family's own knowledge, as rows in kb_docs (076) -- NOT guides.
#
# `guides` is a different feature: a how-to attached to a property, an asset or
# a vehicle, with its own page and its own revisions table. The family's
# "how we do this here" belongs in the KB, which is path-addressed, readable by
# every role (a housesitter included) and indexed for search by meaning
# (#5502). Writing the same knowledge to both would be two homes for one fact.
#
# paths must match kb_docs' CHECK: lower-case segments joined by '/'.
KB_DOCS = [
    dict(key="vet-after-hours", path="how-to/pets/vet-after-hours",
         title="Vet: what to do when the dog or cat is sick after hours",
         tags=["pets", "vet", "dog", "cat", "emergency", "after-hours"],
         body="""\
## After hours (at night, at weekends, on holidays)

Our regular vet -- Warwick Animal Hospital, 555-0195 -- closes at 6pm and is shut
on Sundays, so anything that goes wrong at night or at the weekend and cannot
wait until morning goes to the emergency clinic:

**Northside Veterinary Emergency, 555-0176.** Open 24 hours, walk-ins taken,
ring the bell twice if the door is locked. It is 22 minutes away, past the
route 17 on-ramp.

## Go in immediately, do not wait for morning

- Biscuit (the dog) vomiting and unable to keep water down for more than a few
  hours, or a swollen belly with dry retching -- bloat is an emergency, go now.
- Any trouble breathing, or gums that look pale, white or blue.
- A cat that has not passed urine all day, especially Mochi, a male cat.
- A swollen face after a sting, or a known toxin swallowed (chocolate, raisins,
  lilies for the cat, antifreeze).
- A wound that will not stop bleeding, or a suspected broken bone.

## If it can wait until the vet opens in the morning

A single episode of vomiting, a mild limp, a skipped meal, or a scratch that is
not deep: call Warwick Animal Hospital at 8am on 555-0195 and describe what
happened. Their front desk decides whether to squeeze us in that day.

## Money

Being seen at the emergency clinic is roughly $200 before anything is done.
Biscuit and Mochi are on the pet policy in the fireproof box, folder "Pets";
the policy number is in the vault, not in this doc. Call Hudson Insurance Group
(555-0124) the next morning and open a claim before paying the balance.
"""),
    dict(key="router-reset", path="how-to/networking/router-reset",
         title="Reset the router when the wifi is out",
         tags=["wifi", "network", "internet", "house"],
         body="""\
## Reset the router

The router is in the hall closet, top shelf -- a Netgear RAX50, on the shelf,
not on the floor.

1. Unplug the power, wait 30 seconds, plug it back in.
2. Wait until the light on the front stops blinking (about 2 minutes).
3. Try the wifi again. The network name is `OrchardLane`; the password is on the
   card taped inside the closet door.

## Still out?

- Check the fiber box downstairs, beside the boiler: its status light must be green.
- Reboot that box the same way and wait 3 minutes.
- Then call Northlight Fiber on 555-0175. Their support asks for the phone
  number on the account, not the router's serial number.

## Do not

Do not factory-reset the router (the pinhole on the back, held 10 seconds): it
wipes the network name, the password and the port forwards, and nobody has
written those down. Reset means unplug it, not erase it.
"""),
    dict(key="babysitter", path="sitters/what-the-babysitter-needs-to-know",
         title="What the babysitter (or any sitter) needs to know",
         tags=["kids", "babysitter", "sitter", "bedtime", "wifi", "house"],
         body="""\
## For the babysitter

Whether it is our usual sitter or someone new for one evening, this is the whole
brief: the kids, the doors, and who to reach.

**The kids.** Maya (9) reads on her own, Noah (6) needs a story at bedtime, Lily
(3) is in bed by 7:15 and will ask for water twice. Dinner is in the fridge --
5 minutes in the microwave. No peanuts anywhere near Maya: her EpiPen is in the
front pocket of her backpack, and a second one is in the kitchen drawer by the
fridge. Use it, then call 911.

**Biscuit.** Biscuit goes out one last time at 9 and is fine in his crate if a
storm scares him; do not let him upstairs.

**The house.** The front door deadbolts from the inside and the key goes back in
the lock box by the side door. Leave the thermostat alone. The trash goes out on
Tuesdays and nobody has to do it during an evening sitting.

**Reaching us.** Jacob 555-0181, Amber 555-0182. If it is not an emergency,
text -- we will not hear the phone over the noise. The pediatrician's after-hours
nurse line is 555-0135 for anything medical about the kids.
"""),
    dict(key="insurance-claim", path="how-to/money/insurance-claim",
         title="Filing a home or car insurance claim",
         tags=["insurance", "money", "paperwork", "house"],
         body="""\
## Filing a claim (home or car)

1. **Stop the damage first.** Emergency repairs are covered up to a limit --
   keep the receipt. Photograph everything before you clean up.
2. **Call Hudson Insurance Group, 555-0124,** within 72 hours. Give the policy
   number (fireproof box, folder "House") and the date of loss.
3. **Keep every document.** Photos, receipts, the adjuster's name and the claim
   number, in one folder in Home OS under Documents.
4. **The adjuster's estimate is an offer**, not a verdict. Compare it with the
   contractor's before signing anything.
5. **Track it.** A claim nobody follows up sits. Ask for the next step and the
   date it is due, every time.

Do not sign a release before the work is done and paid.
"""),
]

# The four docs the previous version of this seed wrote into `guides` (#5504).
# They are deleted by id on a re-seed, so a household seeded by the old script
# does not keep a second copy of knowledge that now lives in kb_docs. Deleting
# by these exact ids -- never by household alone -- so a guide the family wrote
# itself is never touched.
LEGACY_GUIDE_KEYS = ["router-reset", "pediatrician-after-hours", "babysitter-guide",
                     "insurance-claim"]


def q(value):
    """A SQL string literal. Doubles quotes; None is NULL."""
    if value is None:
        return "NULL"
    return "'" + str(value).replace("'", "''") + "'"


def arr(values):
    """A SQL text[] literal from a list of strings."""
    if not values:
        return "'{}'"
    inner = ",".join('"' + v.replace("\\", "\\\\").replace('"', '\\"') + '"' for v in values)
    return q("{" + inner + "}")


def today_in(tz):
    """Today's date in the household's timezone.

    The same conversion every reader makes (billdue.HouseholdToday /
    householdToday): a bill's state is decided on the FAMILY's calendar day, not
    the server's, so the dates this seed writes are relative to the family's
    today too. An unknown timezone falls back to UTC, as the Go helpers do.
    """
    try:
        zone = zoneinfo.ZoneInfo(tz)
    except (zoneinfo.ZoneInfoNotFoundError, ValueError):
        zone = datetime.timezone.utc
    return datetime.datetime.now(zone).date()


class Seed:
    """Builds the SQL for one war-game household."""

    def __init__(self, household_id, household_created, name, tz, today):
        self.hid = household_id
        self.household_created = household_created
        self.name = name
        self.tz = tz
        self.today = today
        self.ids = {}
        self.lines = []

    # ---- id derivation -------------------------------------------------
    def id(self, key):
        if key not in self.ids:
            self.ids[key] = str(uuid.uuid5(SEED_NAMESPACE, f"{self.hid}/{key}"))
        return self.ids[key]

    def add(self, *lines):
        for line in lines:
            self.lines.append(line)

    # ---- the body ------------------------------------------------------
    def build(self):
        self.resolve_household()
        self.clear()
        self.insert_people()
        self.insert_pets()
        self.insert_vehicles()
        self.insert_property()
        self.insert_assets()
        self.insert_vendors()
        self.insert_payment_accounts()
        self.insert_bills()
        self.insert_kb_docs()
        return "\n".join(self.lines)

    def resolve_household(self):
        """Create the household, or stamp the marker on an adopted empty one."""
        if self.household_created:
            self.add(
                "-- The war-game household does not exist yet: create it with its marker.",
                f"INSERT INTO households (id, name, timezone, settings) VALUES (",
                f"    {q(self.hid)}, {q(self.name)}, {q(self.tz)},",
                f"    jsonb_build_object({q(MARKER_KEY)}, {q(MARKER_VALUE)}));",
            )
        else:
            # Keep any settings already there; stamp ours. Merging (||) is what
            # makes this safe to run twice.
            self.add(
                f"-- {self.name}: stamp the war-game marker (idempotent).",
                f"UPDATE households SET settings = settings || "
                f"jsonb_build_object({q(MARKER_KEY)}, {q(MARKER_VALUE)}),",
                f"    updated_at = NOW() WHERE id = {q(self.hid)};",
            )

    def clear(self):
        """Clear this household's seeded rows, and nothing else, before writing."""
        h = q(self.hid)
        legacy_guide_ids = ", ".join(q(self.id("guide/" + k)) for k in LEGACY_GUIDE_KEYS)
        self.add(
            "-- ------------------------------------------------------------------",
            "-- Clear the war-game household's own rows. Every predicate names the",
            "-- household; no other household is ever read or written.",
            "-- ------------------------------------------------------------------",
            # A bill's calendar mirror is written by the app (billcal), not by a
            # trigger, so clear it here too: otherwise a re-seed leaves an
            # orphan mirror event behind for every bill it replaced.
            f"DELETE FROM calendar_objects WHERE entity_type = 'bill' AND entity_id IN "
            f"(SELECT id FROM bills WHERE household_id = {h});",
            f"DELETE FROM bills WHERE household_id = {h};",
            # The family's knowledge (#5544): the KB is the one home for it.
            f"DELETE FROM kb_docs WHERE household_id = {h};",
            # #5504 wrote its four docs into `guides`. Drop exactly those ids, so
            # a household seeded by the old script does not keep two copies of
            # the router instructions. guides' revisions CASCADE.
            f"DELETE FROM guides WHERE household_id = {h} AND id IN ({legacy_guide_ids});",
            # The accounts must go after the bills that point at them (075's FK
            # is ON DELETE SET NULL, so this would work either way; the order
            # says what the dependency is).
            f"DELETE FROM payment_accounts WHERE household_id = {h};",
            # Pets before vendors: pets.vet_vendor_id is a composite FK to
            # (vendors.id, household_id), and whatever the database does on
            # delete, the seed must not depend on it. Order is the fix.
            f"DELETE FROM pets WHERE household_id = {h};",
            f"DELETE FROM vendors WHERE household_id = {h};",
            f"DELETE FROM vehicles WHERE household_id = {h};",
            f"DELETE FROM assets WHERE household_id = {h};",
            f"DELETE FROM rooms WHERE property_id IN (SELECT id FROM properties WHERE household_id = {h});",
            f"DELETE FROM properties WHERE household_id = {h};",
            f"DELETE FROM people WHERE household_id = {h};",
            "",
        )

    def insert_people(self):
        # color is left out on purpose: people_set_default_color() (071) fills it
        # from the id, so the seed does not pin a palette the UI may change.
        self.add("-- People: two parents and three kids.")
        for p in PEOPLE:
            self.add(
                "INSERT INTO people (id, household_id, display_name, relationship, "
                "birthdate, school, grade, allergies, notes) VALUES (",
                f"    {q(self.id('person/' + p['key']))}, {q(self.hid)}, {q(p['name'])}, "
                f"{q(p['relationship'])},",
                f"    {q(p.get('birthdate'))}, {q(p.get('school'))}, {q(p.get('grade'))},",
                f"    {arr(p.get('allergies', []))}, {q(p.get('notes'))});",
            )
        self.add("")

    def insert_pets(self):
        self.add("-- Pets. vet_vendor_id is set after the vendors exist (below).")
        for p in PETS:
            self.add(
                "INSERT INTO pets (id, household_id, name, species, breed, "
                "date_of_birth, notes) VALUES (",
                f"    {q(self.id('pet/' + p['key']))}, {q(self.hid)}, {q(p['name'])}, "
                f"{q(p['species'])}, {q(p['breed'])}, {q(p['date_of_birth'])}, "
                f"{q(p['notes'])});",
            )
        self.add("")

    def insert_vehicles(self):
        self.add("-- Vehicles. No VIN: a made-up VIN is worse than none.")
        for v in VEHICLES:
            self.add(
                "INSERT INTO vehicles (id, household_id, year, make, model, color, "
                "license_plate, notes) VALUES (",
                f"    {q(self.id('vehicle/' + v['key']))}, {q(self.hid)}, {v['year']}, "
                f"{q(v['make'])}, {q(v['model'])}, {q(v['color'])}, "
                f"{q(v['license_plate'])}, {q(v['notes'])});",
            )
        self.add("")

    def insert_property(self):
        p = PROPERTY
        self.add(
            "-- The house.",
            "INSERT INTO properties (id, household_id, name, address, property_type, notes) VALUES (",
            f"    {q(self.id('property/' + p['key']))}, {q(self.hid)}, {q(p['name'])}, "
            f"{q(p['address'])}, {q(p['property_type'])}, {q(p['notes'])});",
            f"UPDATE households SET default_property_id = {q(self.id('property/' + p['key']))} "
            f"WHERE id = {q(self.hid)};",
            "",
        )

    def insert_assets(self):
        self.add("-- Assets: the things maintenance hangs off.")
        for a in ASSETS:
            self.add(
                "INSERT INTO assets (id, household_id, property_id, name, category, "
                "manufacturer, model, purchase_date, notes) VALUES (",
                f"    {q(self.id('asset/' + a['key']))}, {q(self.hid)}, "
                f"{q(self.id('property/' + PROPERTY['key']))}, {q(a['name'])}, "
                f"{q(a['category'])}, {q(a['manufacturer'])}, {q(a['model'])}, "
                f"{q(a['purchase_date'])}, {q(a['notes'])});",
            )
        self.add("")

    def insert_vendors(self):
        self.add("-- Contacts the family can reach: phone, email and website.")
        for v in VENDORS:
            self.add(
                "INSERT INTO vendors (id, household_id, name, specialty, phone, email, "
                "website, trades, notes) VALUES (",
                f"    {q(self.id('vendor/' + v['key']))}, {q(self.hid)}, {q(v['name'])}, "
                f"{q(v['specialty'])}, {q(v.get('phone'))}, {q(v.get('email'))},",
                f"    {q(v.get('website'))}, {arr(v.get('trades', []))}, {q(v.get('notes'))});",
            )
        # The pets' vet is a vendor (pets.vet_vendor_id, 043).
        for p in PETS:
            if p.get("vet_key"):
                self.add(
                    f"UPDATE pets SET vet_vendor_id = {q(self.id('vendor/' + p['vet_key']))} "
                    f"WHERE id = {q(self.id('pet/' + p['key']))};",
                )
        self.add("")

    def insert_payment_accounts(self):
        """payment_accounts (075): the accounts bills are paid from.

        Written before the bills, because bills.autopay_account_id is a
        same-family composite FK to this table. Last-4 only: last_four is
        CHAR(4) CHECKed to four digits, and there is no column for anything
        longer. The bank login would be payment_accounts.secret_id, a link into
        `secrets`; the war game stores none, so the column stays NULL.
        """
        self.add(
            "-- ------------------------------------------------------------------",
            "-- Payment accounts (075): name + institution + account_type + a real",
            "-- 4-digit last_four, and nothing that could hold a full number.",
            "-- ------------------------------------------------------------------",
        )
        for a in PAYMENT_ACCOUNTS:
            self.add(
                "INSERT INTO payment_accounts (id, household_id, name, institution, "
                "account_type, last_four, notes) VALUES (",
                f"    {q(self.id('account/' + a['key']))}, {q(self.hid)}, {q(a['name'])}, "
                f"{q(a['institution'])}, {q(a['account_type'])}, {q(a['last_four'])}, "
                f"{q(a['notes'])});",
            )
        self.add("")

    def subject_sql(self, subject):
        """The typed subject columns (038): property_id / vehicle_id / asset_id."""
        if not subject:
            return "NULL, NULL, NULL"
        kind, key = subject
        ids = {"property": "property/", "vehicle": "vehicle/", "asset": "asset/"}
        slots = {"property": 0, "vehicle": 1, "asset": 2}
        out = ["NULL", "NULL", "NULL"]
        out[slots[kind]] = q(self.id(ids[kind] + key))
        return ", ".join(out)

    def due_sql(self, b):
        """(due_day, due_date) for one bill.

        082 makes frequency select which column is authoritative and requires
        the other to be NULL: a monthly bill is due on due_day and may not carry
        a due_date; every other bill is anchored on due_date and may not carry a
        due_day. Writing both would be the three-answers-to-one-question shape
        that migration exists to remove.
        """
        if b["frequency"] == "monthly":
            if b.get("due_in") is not None:
                raise SystemExit(f"seed bug: monthly bill {b['key']} has due_in; "
                                 "monthly bills use due_day")
            if b.get("due_day") is None:
                raise SystemExit(f"seed bug: monthly bill {b['key']} has no due_day")
            return str(b["due_day"]), "NULL"
        if b.get("due_day") is not None:
            raise SystemExit(f"seed bug: non-monthly bill {b['key']} has due_day; "
                             "only monthly bills use it")
        if b.get("due_in") is None:
            raise SystemExit(f"seed bug: non-monthly bill {b['key']} has no due_in")
        day = self.today + datetime.timedelta(days=b["due_in"])
        return "NULL", q(day.isoformat()) + "::date"

    def bill_payee_sql(self, b):
        """(vendor_id, payee_name): a vendor, or the free-text payee, never both."""
        if b.get("payee_vendor"):
            if b.get("payee_name"):
                raise SystemExit(f"seed bug: bill {b['key']} names both a vendor and "
                                 "a free-text payee; a bill has ONE payee")
            return q(self.id("vendor/" + b["payee_vendor"])), "NULL"
        return "NULL", q(b.get("payee_name"))

    def bill_paying_sql(self, b):
        """(autopay_account_id, account_number) for one bill.

        The pair is where 075's rule lives, so the seed states it explicitly and
        refuses the combinations the database would refuse:

          autopay ON   -> autopay_account_id set, account_number NULL (the
                          masked account is payment_accounts now).
          autopay OFF  -> autopay_account_id NULL (bills_autopay_account_check),
                          and account_number MAY hold the last-4 the bill is paid
                          from -- the only way a manual bill has an account on
                          record, and therefore the only way to reach 'due_soon'.
        """
        if b["autopay"]:
            key = b.get("autopay_account")
            if not key:
                raise SystemExit(f"seed bug: autopay bill {b['key']} has no "
                                 "autopay_account; an autopay bill must name the "
                                 "account it comes out of")
            if b.get("manual_account"):
                raise SystemExit(f"seed bug: autopay bill {b['key']} sets "
                                 "manual_account; that column is for manual bills")
            return q(self.id("account/" + key)), "NULL"
        if b.get("autopay_account"):
            raise SystemExit(f"seed bug: manual bill {b['key']} sets autopay_account; "
                             "075's bills_autopay_account_check refuses that")
        four = b.get("manual_account")
        if four is not None and (len(four) != 4 or not four.isdigit()):
            raise SystemExit(f"seed bug: bill {b['key']} manual_account {four!r} is "
                             "not four digits")
        return "NULL", q(".." + four) if four else "NULL"

    def insert_bills(self):
        self.add(
            "-- ------------------------------------------------------------------",
            "-- Bills: the deliberate mix. Read through billdue.AttentionFor, these",
            "-- rows are 6 automatic + 4 due_soon + 1 needs_setup + 1 overdue.",
            "-- ------------------------------------------------------------------",
        )
        for b in BILLS:
            due_day, due_date = self.due_sql(b)
            vendor, payee_name = self.bill_payee_sql(b)
            account_id, account_number = self.bill_paying_sql(b)
            created = "NOW()"
            if b.get("created_days_ago"):
                created = f"(NOW() - INTERVAL '{int(b['created_days_ago'])} days')"
            self.add(
                f"-- {b['key']}: {b['why']}",
                "INSERT INTO bills (id, household_id, name, amount, frequency, due_day, "
                "due_date, category, is_autopay, autopay_account_id, account_number, "
                "vendor_id, payee_name, notes, property_id, vehicle_id, asset_id, "
                "created_at) VALUES (",
                f"    {q(self.id('bill/' + b['key']))}, {q(self.hid)}, {q(b['name'])}, "
                f"{b['amount']}, {q(b['frequency'])}, {due_day}, {due_date},",
                f"    {q(b['category'])}, {'true' if b['autopay'] else 'false'}, "
                f"{account_id}, {account_number},",
                f"    {vendor}, {payee_name}, {q(b.get('notes'))},",
                f"    {self.subject_sql(b.get('subject'))}, {created});",
            )
        self.add("")

    def insert_kb_docs(self):
        """kb_docs (076): the family's knowledge base, addressable by path.

        ON CONFLICT (household_id, path) DO UPDATE -- the table's own key -- so
        even if the DELETE above were skipped (a partial run, a hand-applied
        fragment) a re-run replaces the doc instead of forking a second one.
        `deleted_at` is cleared too: a doc the family soft-deleted must come back
        live when the round is re-seeded, not stay a tombstone the search
        reconciler skips.
        """
        self.add(
            "-- ------------------------------------------------------------------",
            "-- The family's knowledge base (076, kb_docs) -- NOT guides. Keyed by",
            "-- the table's own UNIQUE (household_id, path), so a re-run replaces.",
            "-- ------------------------------------------------------------------",
        )
        for d in KB_DOCS:
            self.add(
                "INSERT INTO kb_docs (id, household_id, path, title, body, tags) VALUES (",
                f"    {q(self.id('kb/' + d['key']))}, {q(self.hid)}, {q(d['path'])}, "
                f"{q(d['title'])},",
                f"    {q(d['body'])}, {arr(d.get('tags', []))})",
                "ON CONFLICT (household_id, path) DO UPDATE SET",
                "    title = EXCLUDED.title, body = EXCLUDED.body, tags = EXCLUDED.tags,",
                "    deleted_at = NULL, updated_at = NOW();",
            )
        self.add("")


def print_verification(hid, today):
    """The counts the completion reports, straight from the database."""
    h = q(hid)
    return f"""
-- ------------------------------------------------------------------
-- What the household now holds. today = {today.isoformat()} (household timezone)
-- ------------------------------------------------------------------
\\echo '== war-game household =='
SELECT h.id, h.name, h.timezone, h.settings->>'{MARKER_KEY}' AS marker
  FROM households h WHERE h.id = {h};

\\echo '== bills by autopay / paying account =='
SELECT CASE WHEN b.is_autopay THEN 'autopay' ELSE 'manual' END AS autopay,
       CASE WHEN b.autopay_account_id IS NOT NULL THEN 'linked ' || pa.name
            WHEN NULLIF(btrim(b.account_number), '') IS NOT NULL
                 THEN 'account ' || b.account_number || ' (account_number, pre-075 column)'
            ELSE '**** NO ACCOUNT ****' END AS paying_account,
       count(*) AS bills,
       to_char(sum(b.amount), 'FM999999.00') AS total
  FROM bills b LEFT JOIN payment_accounts pa ON pa.id = b.autopay_account_id
 WHERE b.household_id = {h}
 GROUP BY 1, 2 ORDER BY 1, 2;

\\echo '== the bills: autopay, paying account, due, when entered =='
SELECT name, to_char(amount, 'FM999999.00') AS amount, frequency,
       COALESCE(due_day::text, due_date::text) AS due,
       is_autopay,
       CASE WHEN autopay_account_id IS NOT NULL THEN 'linked'
            WHEN NULLIF(btrim(account_number), '') IS NOT NULL THEN account_number
            ELSE 'none' END AS paying_account,
       COALESCE((SELECT v.name FROM vendors v WHERE v.id = bills.vendor_id), payee_name) AS payee,
       created_at::date AS entered
  FROM bills WHERE household_id = {h}
 ORDER BY is_autopay DESC, name;

\\echo '== 075 constraints hold on the seeded rows (every count must be 0) =='
SELECT 'autopay bill with no linked account' AS check, count(*) AS offenders
  FROM bills WHERE household_id = {h} AND is_autopay AND autopay_account_id IS NULL
UNION ALL
SELECT 'manual bill holding an autopay account', count(*)
  FROM bills WHERE household_id = {h} AND NOT is_autopay AND autopay_account_id IS NOT NULL
UNION ALL
SELECT 'bill with both a vendor and a free-text payee', count(*)
  FROM bills WHERE household_id = {h} AND vendor_id IS NOT NULL AND payee_name IS NOT NULL;

\\echo '== payment accounts (name / institution / type / last four) =='
SELECT pa.name, pa.institution, pa.account_type, pa.last_four,
       (SELECT count(*) FROM bills b WHERE b.autopay_account_id = pa.id) AS bills_autopaid
  FROM payment_accounts pa WHERE pa.household_id = {h} ORDER BY pa.name;

\\echo '== kb docs (kb_docs -- the family knowledge base) =='
SELECT path, title, array_to_string(tags, ',') AS tags, length(body) AS body_chars
  FROM kb_docs WHERE household_id = {h} AND deleted_at IS NULL ORDER BY path;
\\echo '   guides rows for this household (must be 0: knowledge lives in kb_docs, not guides)'
SELECT count(*) AS guides FROM guides WHERE household_id = {h};

\\echo '== supporting data =='
SELECT 'people' AS what, count(*) FROM people WHERE household_id = {h}
UNION ALL SELECT 'pets', count(*) FROM pets WHERE household_id = {h}
UNION ALL SELECT 'vehicles', count(*) FROM vehicles WHERE household_id = {h}
UNION ALL SELECT 'properties', count(*) FROM properties WHERE household_id = {h}
UNION ALL SELECT 'assets', count(*) FROM assets WHERE household_id = {h}
UNION ALL SELECT 'vendors', count(*) FROM vendors WHERE household_id = {h}
UNION ALL SELECT 'vendors with a phone', count(*) FROM vendors WHERE household_id = {h} AND phone IS NOT NULL
UNION ALL SELECT 'vendors with an email', count(*) FROM vendors WHERE household_id = {h} AND email IS NOT NULL
UNION ALL SELECT 'kb_docs', count(*) FROM kb_docs WHERE household_id = {h} AND deleted_at IS NULL
UNION ALL SELECT 'payment_accounts', count(*) FROM payment_accounts WHERE household_id = {h};
"""


# The tables this seed CLEARS on every run, and the ones it leaves alone. Kept
# next to the gate on purpose: the operator is asked to accept this list before
# anything is written, and clear() is the code that carries it out, so the two
# cannot drift apart without the file showing it.
CLEARED_TABLES = [
    ("calendar_objects", "the bill calendar mirrors this seed wrote"),
    ("bills", "all bills, autopay links and account numbers"),
    ("kb_docs", "the family knowledge base"),
    ("guides", "only the four legacy guide ids this seed used to write (#5504)"),
    ("payment_accounts", "all payment accounts"),
    ("pets", "all pets"),
    ("vendors", "all vendors and their contact details"),
    ("vehicles", "all vehicles"),
    ("assets", "all assets"),
    ("rooms", "rooms of this household's properties"),
    ("properties", "all properties"),
    ("people", "all people"),
]
SURVIVES = [
    "the household row itself (name, timezone, settings) — only the marker is stamped",
    "memberships and users (who can sign in) — never read past the identity warning",
    "every other household: no predicate in this script names one",
    "lists, notes, meal plans, loans, maintenance, files, notifications, activity log",
    "the database schema: this script writes rows, it runs no migration",
]


def scope_report(hid, name):
    """What --allow-household is accepting. Printed before the write, every run."""
    out = [f"  household: {hid}  ({name})",
           "",
           "  THIS SCRIPT WILL DELETE, for that household only:"]
    out += [f"    - {t:<18} {why}" for t, why in CLEARED_TABLES]
    out += ["", "  IT WILL NOT TOUCH:"]
    out += [f"    - {s}" for s in SURVIVES]
    return "\n".join(out)


def find_target(dsn, household_id, allow_household, adopt):
    """Resolve (household_id, created, name), or refuse.

    The gate is EXPLICIT INTENT, not resemblance. The old test was
    `name == HOUSEHOLD_NAME or every login is @example.com` -- a guess about what
    the household looks like. On this deployment every war-game persona is a real
    Dex identity at @simstech.cloud and the household is named "Sims Family (war
    game)", so that guess could never be true and the seed could never run. A
    deployment is not evidence of intent; the operator naming the id is.

    So a household is the war game's when one of two things is true:
      1. it already carries the marker this script writes (a previous seed did
         it), or
      2. the caller passed --allow-household <that exact id>.
    Nothing else. There is no "the only household in the database", no name
    match, and no demo-domain heuristic -- each of those is a way to clear a real
    family's data while believing you proved something.
    """
    sql = (
        "SELECT id::text, name, COALESCE(settings->>'" + MARKER_KEY + "', '') "
        "FROM households ORDER BY created_at;"
    )
    rows = psql_rows(dsn, sql)
    marked = [r for r in rows if r[2] == MARKER_VALUE]

    if allow_household and household_id and allow_household != household_id:
        raise SystemExit(
            f"REFUSED: --allow-household {allow_household} does not match "
            f"--household-id {household_id}. Name one household."
        )
    named = allow_household or household_id

    if named:
        match = [r for r in rows if r[0] == named]
        if not match:
            raise SystemExit(f"REFUSED: no household with id {named} exists.")
        hid, name, marker = match[0]

        if marker == MARKER_VALUE:
            # Intent is already on record in the row itself.
            print(f"allowed: household {hid} ({name}) carries {MARKER_KEY}={MARKER_VALUE}.")
            return hid, False, name

        if not allow_household:
            raise SystemExit(
                f"REFUSED: household {hid} ({name}) does not carry the war-game marker\n"
                f"  {MARKER_KEY}={MARKER_VALUE}\n"
                "and was not named explicitly. This seed DELETES the household's bills, "
                "payment accounts, KB docs, contacts, people, pets, vehicles and "
                "properties (full list: --show-scope).\n"
                f"If you mean it, pass --allow-household {hid} --adopt --show-scope."
            )

        # Explicit intent given. --adopt is how the operator says "and I accept
        # replacing what is in there", so it is required rather than implied.
        if not adopt:
            raise SystemExit(
                f"--allow-household {hid} ({name}) names this household, but --adopt is "
                "missing.\n"
                "--adopt is the second half of the intent: --allow-household says WHICH "
                "household, --adopt says you accept its current contents being replaced.\n"
                f"Re-run with --allow-household {hid} --adopt --show-scope."
            )

        # Loud warning, NOT a refusal: on a deployment whose personas are real
        # Dex identities (this one), refusing here would make the seed unrunnable
        # for the fourth time. The operator has already said what they mean.
        logins = psql_rows(
            dsn,
            f"SELECT u.email FROM memberships m JOIN users u ON u.id = m.user_id "
            f"WHERE m.household_id = {q(hid)} ORDER BY 1;",
        )
        addresses = [e for (e,) in logins]
        foreign = [e for e in addresses if not e.endswith("@" + TEST_EMAIL_DOMAIN)]
        print(f"allowed: --allow-household {hid} ({name}), --adopt given; "
              f"marker absent (this run will stamp {MARKER_KEY}={MARKER_VALUE}).")
        if foreign:
            print(
                "\n"
                "  ****************************************************************\n"
                "  * WARNING: this household has logins that are NOT "
                + "@" + TEST_EMAIL_DOMAIN + "\n"
                "  * test accounts: " + ", ".join(foreign[:8]) + "\n"
                "  * " + (f"({len(foreign)} in all)" if len(foreign) > 8 else "") + "\n"
                "  *\n"
                "  * That is expected on a deployment whose war-game personas are real\n"
                "  * Dex identities -- this deployment's are @simstech.cloud. It is\n"
                "  * also exactly what a real family's household looks like. You said\n"
                "  * you meant this id, so the seed proceeds. If you did NOT mean it,\n"
                "  * stop now: the next step deletes the rows listed by --show-scope.\n"
                "  ****************************************************************\n",
                file=sys.stderr,
            )
        return hid, False, name

    # No id at all. The marked household, if there is exactly one, is intent
    # enough -- the marker is a previous explicit run of this script. Anything
    # else refuses: guessing here is how a real household loses its data.
    if len(marked) > 1:
        raise SystemExit(
            "REFUSED: more than one household carries the war-game marker "
            f"({', '.join(r[0] for r in marked)}). "
            "Pass --allow-household <id> --adopt to say which."
        )
    if marked:
        print(f"allowed: household {marked[0][0]} ({marked[0][1]}) carries "
              f"{MARKER_KEY}={MARKER_VALUE}.")
        return marked[0][0], False, marked[0][1]

    raise SystemExit(
        "REFUSED: no --allow-household <id> was given and no household carries the\n"
        f"  war-game marker ({MARKER_KEY}={MARKER_VALUE}).\n"
        "This seed deletes a household's bills, payment accounts, KB docs, contacts,\n"
        "people, pets, vehicles and properties. It will not choose a household for you:\n"
        "there is no \"the only household in the database\" and no name match.\n"
        "  - to seed the war-game household that already exists, name it:\n"
        "      --allow-household <id> --adopt --show-scope\n"
        "  - to create a fresh one (touches nothing that exists), pass --new-household.\n"
        "Households:\n  " + "\n  ".join(f"{r[0]}  {r[1]}"
                                        + (f"  [marked]" if r[2] == MARKER_VALUE else "")
                                        for r in rows)
    )


def psql(dsn, sql, quiet=True, tuples=False):
    cmd = ["psql", dsn, "-X", "-v", "ON_ERROR_STOP=1"]
    if quiet:
        cmd.append("-q")
    if tuples:
        # Unaligned, no header, no footer: one row per line, | between columns.
        cmd += ["-t", "-A", "-F", "|"]
    res = subprocess.run(cmd, input=sql, text=True, capture_output=True)
    if res.returncode != 0:
        sys.stderr.write(res.stdout)
        sys.stderr.write(res.stderr)
        raise SystemExit(f"psql failed ({res.returncode}); nothing was committed "
                         "(the seed runs in one transaction).")
    return res.stdout


def psql_rows(dsn, sql):
    out = psql(dsn, sql + "\n", quiet=True, tuples=True)
    return [tuple(cell for cell in line.split("|"))
            for line in out.splitlines() if line.strip()]


def main(argv):
    ap = argparse.ArgumentParser(
        description="Seed the war-game household (idempotent, war-game household only).")
    ap.add_argument("--dsn", default=os.environ.get("DATABASE_URL", ""),
                    help="Postgres DSN (default: $DATABASE_URL)")
    ap.add_argument("--household-id", default=None,
                    help="the household to seed (same as --allow-household; see there)")
    ap.add_argument("--allow-household", default=None, metavar="ID",
                    help="write to this household. This IS the permission: the seed no "
                         "longer guesses from the household's name or its users' email "
                         "domain. Also needs --adopt --show-scope unless the household "
                         "already carries the war-game marker.")
    ap.add_argument("--adopt", action="store_true",
                    help="accept replacing this household's current contents. Only "
                         "meaningful with --allow-household/--household-id, which it "
                         "requires: --allow-household says WHICH household, --adopt says "
                         "you accept losing what is in it.")
    ap.add_argument("--show-scope", action="store_true",
                    help="print exactly what will be deleted and what survives, before "
                         "writing. Required alongside --allow-household --adopt: the "
                         "destructive scope has to have been shown to the operator.")
    ap.add_argument("--dry-run", action="store_true",
                    help="run the gate and the scope report, print the decision, and "
                         "stop without writing anything. The way to prove which "
                         "household ids the gate lets through.")
    ap.add_argument("--new-household", action="store_true",
                    help="create a brand-new household instead of seeding an existing "
                         "one. Touches nothing that exists.")
    ap.add_argument("--print-sql", action="store_true",
                    help="print the SQL and exit; needs no database")
    ap.add_argument("--name", default=HOUSEHOLD_NAME,
                    help=f"household name when creating it (default: {HOUSEHOLD_NAME!r})")
    ap.add_argument("--timezone", default=HOUSEHOLD_TZ)
    ap.add_argument("--today", default=None,
                    help="YYYY-MM-DD to use as today when computing due dates "
                         "(default: today in the household's timezone)")
    ap.add_argument("--no-verify", action="store_true",
                    help="skip printing the verification report")
    args = ap.parse_args(argv)

    if args.today:
        try:
            today = datetime.date.fromisoformat(args.today)
        except ValueError:
            ap.error(f"--today {args.today!r} is not YYYY-MM-DD")
    else:
        today = today_in(args.timezone)

    if args.print_sql:
        # Offline: emit the SQL and exit. Without --household-id the output is the
        # "fresh household" form, with a fixed placeholder id that has to be
        # replaced (the header comment says so); with it, the re-seed form for
        # that household. Ids stay deterministic either way.
        named = args.allow_household or args.household_id
        hid = named or PRINT_SQL_PLACEHOLDER
        seed = Seed(hid, household_created=not named, name=args.name,
                    tz=args.timezone, today=today)
        print("BEGIN;")
        if not named:
            print(f"-- Replace {PRINT_SQL_PLACEHOLDER} below with the target household id,\n"
                  "-- or drop the INSERT and set --household-id when running the script.")
        print(seed.build())
        print("COMMIT;")
        return 0

    if not args.dsn:
        ap.error("--dsn or DATABASE_URL is required (or use --print-sql)")

    if args.adopt and not (args.allow_household or args.household_id):
        ap.error("--adopt needs --allow-household <id> (or --household-id <id>): it says "
                 "you accept replacing a household's contents, so it must say which one.")

    if args.new_household and (args.allow_household or args.household_id):
        ap.error("--new-household creates a household; it does not name an existing one. "
                 "Drop --allow-household/--household-id.")

    if args.new_household:
        hid, created, name = str(uuid.uuid4()), True, args.name
        print(f"allowed: --new-household; creating a fresh household named {name!r} "
              "(nothing that exists is touched).")
    else:
        named = args.allow_household or args.household_id
        # The destructive scope must have been SHOWN. This is checked before the
        # write, and it is a refusal rather than a warning because the whole point
        # is that the operator saw the list before saying yes.
        # --dry-run prints the same scope, so it satisfies "have it shown" too.
        if named and not (args.show_scope or args.dry_run):
            ap.error(f"--allow-household {named} needs --show-scope: run it once with "
                     "--show-scope to see exactly what gets deleted and what survives, "
                     "then again with the same flags to do it.")

        hid, created, name = find_target(
            args.dsn, args.household_id, args.allow_household, args.adopt)

        if args.show_scope or args.dry_run:
            print("\n-- destructive scope, shown before writing --")
            print(scope_report(hid, name))
            if args.dry_run:
                print("-- end of scope. --dry-run: nothing was written. --\n")
                print("gate: ALLOWED. Re-run with the same flags to do it.")
                return 0
            print("-- end of scope; writing now. This is the run that deletes it. --\n")
        else:
            print(f"marker present: household {hid} ({name}) is the war game's, so no "
                  "--show-scope is required.\n")

    seed = Seed(hid, household_created=created, name=name, tz=args.timezone, today=today)
    sql = "BEGIN;\n" + seed.build() + "\nCOMMIT;\n"
    psql(args.dsn, sql)
    print(f"{'created' if created else 're-seeded'} war-game household {hid} ({name}) "
          f"for {today.isoformat()}")

    if not args.no_verify:
        print(psql(args.dsn, print_verification(hid, today), quiet=False))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
