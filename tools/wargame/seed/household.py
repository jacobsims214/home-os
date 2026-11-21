#!/usr/bin/env python3
"""Seed a realistic American household for the war game (#5504).

The family-hub war game is only as good as the household it plays against. The
handful of bills the game started with cannot find the failures that matter: a
bill nobody set up, an invoice that quietly went overdue, a payee you cannot
reach. This seeds one household that can.

What it creates, in the WAR-GAME household only:

  bills        11, a deliberate autopay mix
                 5 autopay ON with the account it comes out of
                 5 manual with an account set
                 1 manual with NO account set  <- the trap (the dental bill)
                 1 already overdue             <- escalation (the plumber invoice)
                 1 payee that is a known vendor and 1 free-text payee (medical)
  payment accounts  3, last-4 only (checking ..4821, credit card ..3312,
                    savings ..7704) -- see SCHEMA GAP below
  contacts     7 vendors with phone / email / website, so tap-to-contact works
  guides       4 KB docs: router reset, pediatrician after-hours, the
               babysitter's guide, insurance claim process
  people       5 (two parents, three kids), 2 pets, 2 vehicles, 1 property,
               3 assets -- enough that every bill links to a real thing

Constraints it keeps:

  * IDEMPOTENT. Everything it writes carries an id derived from
    uuid5(namespace, household_id + "/" + key), and one run clears the
    war-game household's own seed rows before writing them again. Re-running
    gives the same rows with the same ids, never a second copy.
  * NEVER TOUCHES A REAL HOUSEHOLD. The target is a household whose
    settings->>'wargame_seed' is this script's marker. A household passed with
    --household-id that does not carry the marker is refused unless --adopt is
    given AND it looks like the war-game one (name match, or every login is an
    @example.com test account).
  * NO REAL DATA. Emails are @example.com, phones are 555-01xx, account
    numbers are last-4 only and fictional, addresses and names are invented.

SCHEMA GAP (checked against origin/feat/family-hub, 2026-11-21):
#5497 (payment_accounts, bills.autopay_account_id, bills.payee_vendor_id,
bills.payee_name) has NOT landed. bills today carries is_autopay BOOLEAN and
account_number TEXT (migration 020) and vendor_id. So this script seeds what
exists -- is_autopay, account_number = the last-4 ("..4821"), vendor_id, and
notes naming a free-text payee -- and, at the end, runs ONE guarded block that
upgrades itself if #5497's table and columns are present:
  * rows in payment_accounts for the three accounts, and
  * bills.autopay_account_id linked by last-4, bills.payee_name filled.
Re-run the seed after #5497 lands and the new columns fill in; nothing here
has to be rewritten.

usage:
  DATABASE_URL=postgres://... ./household.py                 # seed / re-seed
  DATABASE_URL=postgres://... ./household.py --household-id <uuid> [--adopt]
  ./household.py --print-sql                                 # SQL to stdout only
  ./household.py --print-sql --household-id <uuid> | psql ...

Needs psql on PATH to apply (the repo's own images ship it); --print-sql needs
nothing but python3.
"""
import argparse
import os
import subprocess
import sys
import uuid

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
         notes="In the hall closet, on the top shelf. The reset steps are a guide."),
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

# Payment accounts. Last-4 only, by construction: there is no field here that
# could hold a full number.
PAYMENT_ACCOUNTS = [
    dict(key="checking", name="Joint checking ..4821", institution="Hudson Valley Bank",
         account_type="checking", last_four="4821",
         notes="Both paychecks land here. Bills autopay from this one."),
    dict(key="credit", name="Credit card ..3312", institution="Hudson Valley Bank",
         account_type="credit_card", last_four="3312",
         notes="Paid in full on the 20th. Subscriptions and phone are on it."),
    dict(key="savings", name="Savings ..7704", institution="Hudson Valley Bank",
         account_type="savings", last_four="7704",
         notes="Emergency fund. Nothing autopays from here."),
]

# Bills. The MIX is the test, so each row states its intent in `why`:
#   autopay        True  -> is_autopay, with the account it comes out of
#   account        the last-4 of the account, or None (the trap)
#   payee_vendor   a VENDORS key, or None when the payee is free text
#   payee_name     the free-text payee, used when payee_vendor is None
#   subject        ("property"|"vehicle"|"asset", key), or None
#   frequency      monthly | quarterly | semiannual | annual | once
#   due_day        monthly bills
#   due_date       other bills (the anchor date); YYYY-MM-DD
BILLS = [
    # ---- autopay ON, account set (5) ----
    dict(key="mortgage", name="Mortgage", amount="2340.00", category="Housing",
         frequency="monthly", due_day=1, autopay=True, account="4821",
         payee_vendor="mortgage", subject=("property", "house"),
         why="The one bill that must never be missed. Autopay on joint checking."),
    dict(key="electric", name="Electric", amount="182.00", category="Utilities",
         frequency="monthly", due_day=18, autopay=True, account="4821",
         payee_vendor="electric", subject=("property", "house"),
         notes="Varies with the season: about $240 in July and August, about $150 "
               "in March and April. Budget billing is off.",
         why="Autopay on, but the amount moves -- a reminder that autopay is not "
             "the same as predictable."),
    dict(key="internet", name="Internet", amount="75.00", category="Utilities",
         frequency="monthly", due_day=22, autopay=True, account="4821",
         payee_vendor="internet", subject=("property", "house"),
         why="Flat, autopay, boring -- the control case."),
    dict(key="phone", name="Phone family plan", amount="140.00",
         category="Utilities", frequency="monthly", due_day=12, autopay=True,
         account="3312", payee_vendor="phone",
         notes="Four lines: Jacob, Amber, Maya and the house line.",
         why="Autopay off the credit card, not checking: the paying account is "
             "not always the same one."),
    dict(key="streaming", name="Streamly subscription", amount="17.99",
         category="Subscriptions", frequency="monthly", due_day=5, autopay=True,
         account="3312", payee_vendor="streaming",
         why="Small, silent, autopay. The bill a household forgets it has."),
    # ---- manual, account set (4) ----
    dict(key="water", name="Water", amount="96.40", category="Utilities",
         frequency="quarterly", due_date="2026-12-10", autopay=False, account="4821",
         payee_vendor="water", subject=("property", "house"),
         notes="Quarterly, and the amount changes every quarter (summer watering).",
         why="Manual and variable: the amount on the statement is never the "
             "amount from last time."),
    dict(key="trash", name="Trash and recycling", amount="63.00",
         category="Utilities", frequency="quarterly", due_date="2027-01-08",
         autopay=False, account="4821", payee_vendor="trash",
         subject=("property", "house"),
         why="Quarterly and manual, an easy one to let slide a month."),
    dict(key="copay", name="Pediatrician copay", amount="35.00", category="Medical",
         frequency="once", due_date="2026-12-10", autopay=False, account="3312",
         payee_vendor=None, payee_name="Riverside Pediatrics (billing: 555-0134)",
         subject=None,
         notes="Payee is free text: this one came from the statement Amber brought "
               "home, not from a saved contact. Statement 11/10.",
         why="A manual bill whose payee is NOT a vendor -- contact routing has to "
             "fall back to the payee text."),
    dict(key="field-trip", name="Field trip fee - Noah", amount="45.00",
         category="School", frequency="once", due_date="2026-12-01", autopay=False,
         account="4821", payee_vendor="school", subject=None,
         notes="Permission slip due back by 11/24. Check or the school portal.",
         why="A one-off a school sends home, on a known vendor."),
    # ---- the trap: manual, NO account set ----
    dict(key="dental", name="Maple Family Dental - crown balance", amount="412.00",
         category="Dental", frequency="once", due_date="2026-11-28", autopay=False,
         account=None, payee_vendor="dentist", subject=None,
         notes="Amber entered this from the statement and never finished setting "
               "it up: no autopay, no account. Nobody knows how it gets paid.",
         why="THE TRAP this epic exists for: autopay off, no paying account, and a "
             "real amount. Nothing in the household says who pays it or when."),
    # ---- already overdue ----
    dict(key="plumber", name="Orchard Plumbing invoice 1042", amount="285.00",
         category="Home repair", frequency="once", due_date="2026-11-14",
         autopay=False, account="4821", payee_vendor="plumber",
         subject=("property", "house"),
         notes="Kitchen shutoff valve replaced 11/07. Invoice 1042, net 30 from "
               "the visit -- it is already past due.",
         why="Already past its due date and nothing paid it: the escalation case."),
]

# The family's own knowledge. Four docs the personas actually need mid-round.
GUIDES = [
    dict(key="router-reset", title="Reset the router",
         tags=["wifi", "network", "house"],
         subject=("asset", "router"),
         summary="When the wifi is out, reset the router before calling Northlight.",
         body="""\
## Reset the router

The router is in the hall closet, top shelf (Netgear RAX50, on the shelf, not on
the floor).

1. Unplug the power, wait 30 seconds, plug it back in.
2. Wait for the light on the front to stop blinking (about 2 minutes).
3. Try the wifi again. The network name is `OrchardLane`, the password is on the
   card taped inside the closet door.

Still out?

- Check the fiber box in the basement: its status light must be green.
- Reboot that box the same way and wait 3 minutes.
- Then call Northlight Fiber at 555-0175. Their support asks for the account's
  phone number, not the router's serial number.
"""),
    dict(key="pediatrician-after-hours", title="Pediatrician: after hours and when to go in",
         tags=["health", "kids", "emergency"],
         subject=("vendor", "pediatrician"),
         summary="What to do when a kid is sick outside 9-5.",
         body="""\
## Riverside Pediatrics, after hours

**Call the nurse line first: 555-0135.** It answers 5pm-8am on weekdays and all
weekend. A nurse calls back within 30 minutes.

Go straight to the ER (St. Anthony's, 12 minutes) when:

- a fever over 100.4 F in Lily (under 3), or over 104 F in Maya or Noah
- trouble breathing, a rash that does not fade under a glass, a stiff neck
- a head injury with vomiting, or a seizure

Maya: **peanut allergy.** The EpiPen is in the front pocket of her backpack and a
second one is in the kitchen drawer by the fridge. Use it, then call 911.

Office hours: Mon-Fri 9-5, Sat 9-12 for sick visits only.
"""),
    dict(key="babysitter-guide", title="The babysitter's guide",
         tags=["kids", "babysitter", "house"],
         summary="Everything a sitter needs for an evening: kids, dog, doors, wifi.",
         body="""\
## For the babysitter

**The kids.** Maya (9) reads, Noah (6) needs a story, Lily (3) is in bed by 7:15
and will ask for water twice. Dinner is in the fridge, 5 minutes in the microwave.
No peanuts anywhere near Maya -- the EpiPen is in her backpack, front pocket.

**The dog.** Biscuit goes out one last time at 9. He is fine in the crate if he
gets scared of thunder; do not let him upstairs.

**The house.** Front door deadbolts from the inside, the key is back in the lock
box by the side door. Thermostat: leave it. Trash goes out Tuesday night only.

**Reaching us.** Jacob 555-0181, Amber 555-0182. If it is not an emergency, text.
Pediatrician after hours: 555-0135 (see the pediatrician guide).
"""),
    dict(key="insurance-claim", title="Filing an insurance claim",
         tags=["insurance", "money", "paperwork"],
         summary="How a claim actually moves, and what to keep.",
         body="""\
## Filing a claim (home or car)

1. **Stop the damage first.** Emergency repairs are covered to a limit -- keep
   the receipt. Photograph everything before you clean up.
2. **Call Hudson Insurance Group, 555-0122**, within 72 hours. Give the policy
   number (in the fireproof box, folder "House") and the date of loss.
3. **Keep every document.** Photos, receipts, the adjuster's name and the claim
   number, in one folder in Home OS under Documents.
4. **The adjuster's estimate is an offer**, not a verdict. Compare it with the
   contractor's before signing anything.
5. **Track it.** A claim that is not followed up sits. Ask for the next step
   and the date it is due every time.

DO NOT sign a "release" before the work is done and paid.
"""),
]


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


class Seed:
    """Builds the SQL for one war-game household."""

    def __init__(self, household_id, household_created, name, tz):
        self.hid = household_id
        self.household_created = household_created
        self.name = name
        self.tz = tz
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
        self.insert_bills()
        self.insert_guides()
        self.upgrade_for_5497()
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
            f"DELETE FROM guides WHERE household_id = {h};",   # revisions CASCADE
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
        self.add("-- Assets: the things guides and maintenance hang off.")
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

    def subject_sql(self, subject):
        """The typed subject columns (038): property_id / vehicle_id / asset_id."""
        if not subject:
            return "NULL, NULL, NULL"
        kind, key = subject
        ids = {
            "property": ("property/" + key, "property_id"),
            "vehicle": ("vehicle/" + key, "vehicle_id"),
            "asset": ("asset/" + key, "asset_id"),
        }
        ident, _col = ids[kind]
        slots = {"property": 0, "vehicle": 1, "asset": 2}
        out = ["NULL", "NULL", "NULL"]
        out[slots[kind]] = q(self.id(ident))
        return ", ".join(out)

    def insert_bills(self):
        self.add(
            "-- ------------------------------------------------------------------",
            "-- Bills: the deliberate mix. autopay on/off x account set/unset,",
            "-- one overdue, one free-text payee, one known-vendor payee each way.",
            "-- ------------------------------------------------------------------",
        )
        for b in BILLS:
            vendor = q(self.id("vendor/" + b["payee_vendor"])) if b["payee_vendor"] else "NULL"
            due_day = "NULL" if b.get("due_day") is None else str(b["due_day"])
            due_date = q(b.get("due_date")) + "::date" if b.get("due_date") else "NULL"
            # account_number is last-4 only (SCHEMA GAP: #5497 replaces it with
            # autopay_account_id; the guarded block at the end links both).
            acct = q(".." + b["account"]) if b.get("account") else "NULL"
            notes = b.get("notes")
            if b["autopay"] and not b.get("account"):
                raise SystemExit(f"seed bug: autopay bill {b['key']} has no account")
            self.add(
                f"-- {b['key']}: {b['why']}",
                "INSERT INTO bills (id, household_id, name, amount, frequency, due_day, "
                "due_date, category, is_autopay, account_number, vendor_id, notes, "
                "property_id, vehicle_id, asset_id) VALUES (",
                f"    {q(self.id('bill/' + b['key']))}, {q(self.hid)}, {q(b['name'])}, "
                f"{b['amount']}, {q(b['frequency'])}, {due_day}, {due_date},",
                f"    {q(b['category'])}, {'true' if b['autopay'] else 'false'}, {acct}, "
                f"{vendor}, {q(notes)},",
                f"    {self.subject_sql(b.get('subject'))});",
            )
        self.add("")

    def insert_guides(self):
        self.add("-- The family's own knowledge (guides = the household KB).")
        for g in GUIDES:
            subj = g.get("subject")
            prop = veh = asset = "NULL"
            if subj:
                kind, key = subj
                if kind == "property":
                    prop = q(self.id("property/" + key))
                elif kind == "vehicle":
                    veh = q(self.id("vehicle/" + key))
                elif kind == "asset":
                    asset = q(self.id("asset/" + key))
                elif kind == "vendor":
                    # A guide can be about a property, asset or vehicle -- not a
                    # vendor. Keep the link in the body and leave the subject be.
                    pass
            self.add(
                "INSERT INTO guides (id, household_id, title, summary, body, tags, "
                "property_id, vehicle_id, asset_id) VALUES (",
                f"    {q(self.id('guide/' + g['key']))}, {q(self.hid)}, {q(g['title'])}, "
                f"{q(g['summary'])},",
                f"    {q(g['body'])}, {arr(g.get('tags', []))}, {prop}, {veh}, {asset});",
            )
            # The newest revision equals guides.body (049).
            self.add(
                "INSERT INTO guide_revisions (guide_id, household_id, body) VALUES (",
                f"    {q(self.id('guide/' + g['key']))}, {q(self.hid)}, {q(g['body'])});",
            )
        self.add("")

    def upgrade_for_5497(self):
        """Fill #5497's columns if that migration has landed. No-op otherwise.

        This is the whole SCHEMA GAP handling: today the table and the columns
        do not exist, so the block does nothing; after #5497 they do, and the
        same seed run links every bill to its payment account and payee without
        a single line of this script changing.
        """
        rows = ", ".join(
            "(" + ", ".join([
                q(a["key"]), q(a["name"]), q(a["institution"]), q(a["account_type"]),
                q(a["last_four"]), q(a["notes"]),
            ]) + ")"
            for a in PAYMENT_ACCOUNTS
        )
        self.add(
            "-- ------------------------------------------------------------------",
            "-- #5497 upgrade, guarded. payment_accounts / bills.autopay_account_id /",
            "-- bills.payee_name are NOT in this branch yet (checked 2026-11-21); when",
            "-- they are, this fills them from what was seeded above and links each",
            "-- bill by its last-4. Silently does nothing until then.",
            "-- ------------------------------------------------------------------",
            "DO $seed5497$",
            "DECLARE",
            f"    h uuid := {q(self.hid)};",
            "    n int;",
            "BEGIN",
            "    IF to_regclass('public.payment_accounts') IS NOT NULL THEN",
            f"        INSERT INTO payment_accounts (id, household_id, name, institution, "
            f"account_type, last_four, notes) VALUES",
            f"        {', '.join('(' + q(self.id('account/' + a['key'])) + ', h, ' + q(a['name']) + ', ' + q(a['institution']) + ', ' + q(a['account_type']) + ', ' + q(a['last_four']) + ', ' + q(a['notes']) + ')' for a in PAYMENT_ACCOUNTS)}",
            "        ON CONFLICT (id) DO NOTHING;",
            "        IF EXISTS (SELECT 1 FROM information_schema.columns WHERE "
            "table_name = 'bills' AND column_name = 'autopay_account_id') THEN",
            "            EXECUTE 'UPDATE bills b SET autopay_account_id = pa.id "
            "FROM payment_accounts pa WHERE b.household_id = $1 "
            "AND pa.household_id = $1 AND pa.last_four = b.account_number "
            "AND b.autopay_account_id IS NULL' USING h;",
            "        END IF;",
            "    END IF;",
            "    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE "
            "table_name = 'bills' AND column_name = 'payee_name') THEN",
            # The one bill seeded with a free-text payee.
            "        UPDATE bills SET payee_name = COALESCE(payee_name, "
            "'Riverside Pediatrics (billing: 555-0134)') "
            "WHERE household_id = h AND name = 'Pediatrician copay';",
            "    END IF;",
            "    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE "
            "table_name = 'bills' AND column_name = 'payee_vendor_id') THEN",
            "        UPDATE bills SET payee_vendor_id = vendor_id "
            "WHERE household_id = h AND vendor_id IS NOT NULL "
            "AND payee_vendor_id IS NULL;",
            "    END IF;",
            "END",
            "$seed5497$;",
            "",
        )


def print_verification(hid):
    """The counts the completion reports, straight from the database."""
    h = q(hid)
    return f"""
-- ------------------------------------------------------------------
-- What the household now holds.
-- ------------------------------------------------------------------
\\echo '== war-game household =='
SELECT h.id, h.name, h.timezone, h.settings->>'{MARKER_KEY}' AS marker
  FROM households h WHERE h.id = {h};

\\echo '== bills by autopay / account / overdue =='
SELECT CASE WHEN is_autopay THEN 'autopay' ELSE 'manual' END AS kind,
       CASE WHEN account_number IS NULL THEN 'no account' ELSE 'account ' || account_number END AS account,
       count(*) AS bills, to_char(sum(amount), 'FM999999.00') AS total
  FROM bills WHERE household_id = {h}
 GROUP BY 1, 2 ORDER BY 1, 2;

\\echo '== the bills: name, amount, frequency, autopay, account, payee, due =='
SELECT name, to_char(amount, 'FM999999.00') AS amount, frequency, due_day, due_date,
       is_autopay, account_number,
       COALESCE((SELECT v.name FROM vendors v WHERE v.id = bills.vendor_id), '(free text)') AS payee
  FROM bills WHERE household_id = {h}
 ORDER BY is_autopay DESC, name;

\\echo '== the trap: manual bills with no paying account (must be exactly 1) =='
SELECT name, to_char(amount, 'FM999999.00') AS amount, due_date
  FROM bills WHERE household_id = {h} AND NOT is_autopay AND account_number IS NULL;

\\echo '== overdue: unpaid once bills past their date =='
SELECT name, to_char(amount, 'FM999999.00') AS amount, due_date,
       (CURRENT_DATE - due_date) AS days_overdue
  FROM bills WHERE household_id = {h} AND frequency = 'once'
   AND due_date < CURRENT_DATE AND paid_date IS NULL
 ORDER BY due_date;

\\echo '== supporting data =='
SELECT 'people' AS what, count(*) FROM people WHERE household_id = {h}
UNION ALL SELECT 'pets', count(*) FROM pets WHERE household_id = {h}
UNION ALL SELECT 'vehicles', count(*) FROM vehicles WHERE household_id = {h}
UNION ALL SELECT 'properties', count(*) FROM properties WHERE household_id = {h}
UNION ALL SELECT 'assets', count(*) FROM assets WHERE household_id = {h}
UNION ALL SELECT 'vendors with a phone', count(*) FROM vendors WHERE household_id = {h} AND phone IS NOT NULL
UNION ALL SELECT 'vendors with an email', count(*) FROM vendors WHERE household_id = {h} AND email IS NOT NULL
UNION ALL SELECT 'guides', count(*) FROM guides WHERE household_id = {h};

\\echo '== contacts (phone / email / website) =='
SELECT name, phone, email, website FROM vendors WHERE household_id = {h} ORDER BY name;
"""


def find_target(dsn, household_id, adopt):
    """Resolve (household_id, created). Refuses anything that is not the war game's."""
    sql = (
        "SELECT id::text, name, COALESCE(settings->>'" + MARKER_KEY + "', '') "
        "FROM households ORDER BY created_at;"
    )
    rows = psql_rows(dsn, sql)
    marked = [r for r in rows if r[2] == MARKER_VALUE]

    if household_id:
        match = [r for r in rows if r[0] == household_id]
        if not match:
            raise SystemExit(f"--household-id {household_id}: no such household.")
        hid, name, marker = match[0]
        if marker == MARKER_VALUE:
            return hid, False, name
        if not adopt:
            raise SystemExit(
                f"REFUSED: household {hid} ({name}) does not carry the war-game marker "
                f"{MARKER_KEY}={MARKER_VALUE}.\n"
                "This seed clears the household's bills, contacts, guides, people, pets, "
                "vehicles and properties, so it will not run against a household it cannot "
                "prove is the war game's. If this really is the war-game household, re-run "
                "with --adopt; it then has to look like one (see the name and login checks)."
            )
        # --adopt: only a household that looks like the war game one.
        logins = psql_rows(
            dsn,
            f"SELECT u.email FROM memberships m JOIN users u ON u.id = m.user_id "
            f"WHERE m.household_id = {q(hid)};",
        )
        foreign = [e for (e,) in logins if not e.endswith("@" + TEST_EMAIL_DOMAIN)]
        looks_like = name == HOUSEHOLD_NAME or not foreign
        if not looks_like:
            raise SystemExit(
                f"REFUSED --adopt: household {hid} ({name}) has logins that are not "
                f"@{TEST_EMAIL_DOMAIN} test accounts ({', '.join(foreign[:5])}) and is not "
                "named " + q(HOUSEHOLD_NAME) + ". That is somebody's real household."
            )
        return hid, False, name

    if len(marked) > 1:
        raise SystemExit(
            "REFUSED: more than one household carries the war-game marker "
            f"({', '.join(r[0] for r in marked)}). Pass --household-id to say which."
        )
    if marked:
        return marked[0][0], False, marked[0][1]

    # No marked household. Creating one is safe: a brand-new household touches
    # nothing that exists. Refuse only if a household already has the name --
    # then the operator must say which one they mean.
    clash = [r for r in rows if r[1] == HOUSEHOLD_NAME]
    if clash:
        raise SystemExit(
            f"REFUSED: a household is already named {q(HOUSEHOLD_NAME)} but carries no "
            f"war-game marker (id {clash[0][0]}). Pass --household-id {clash[0][0]} --adopt "
            "if it is the war game's."
        )
    return str(uuid.uuid4()), True, HOUSEHOLD_NAME


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
                    help="seed this household instead of the marked one (needs --adopt "
                         "unless it already carries the marker)")
    ap.add_argument("--adopt", action="store_true",
                    help="write to --household-id even without the marker, if it looks "
                         "like the war-game household")
    ap.add_argument("--print-sql", action="store_true",
                    help="print the SQL and exit; needs no database")
    ap.add_argument("--name", default=HOUSEHOLD_NAME,
                    help=f"household name when creating it (default: {HOUSEHOLD_NAME!r})")
    ap.add_argument("--timezone", default=HOUSEHOLD_TZ)
    ap.add_argument("--no-verify", action="store_true",
                    help="skip printing the verification report")
    args = ap.parse_args(argv)

    if args.print_sql:
        # Offline: emit the SQL and exit. Without --household-id the output is the
        # "fresh household" form, with a fixed placeholder id that has to be
        # replaced (the header comment says so); with it, the re-seed form for
        # that household. Ids stay deterministic either way.
        hid = args.household_id or PRINT_SQL_PLACEHOLDER
        seed = Seed(hid, household_created=not args.household_id, name=args.name,
                    tz=args.timezone)
        print("BEGIN;")
        if not args.household_id:
            print(f"-- Replace {PRINT_SQL_PLACEHOLDER} below with the target household id,\n"
                  "-- or drop the INSERT and set --household-id when running the script.")
        print(seed.build())
        print("COMMIT;")
        return 0

    if not args.dsn:
        ap.error("--dsn or DATABASE_URL is required (or use --print-sql)")

    hid, created, name = find_target(args.dsn, args.household_id, args.adopt)
    seed = Seed(hid, household_created=created, name=name, tz=args.timezone)

    sql = "BEGIN;\n" + seed.build() + "\nCOMMIT;\n"
    psql(args.dsn, sql)
    print(f"{'created' if created else 're-seeded'} war-game household {hid} ({name})")

    if not args.no_verify:
        print(psql(args.dsn, print_verification(hid), quiet=False))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
