#!/usr/bin/env python3
"""The seed's gate: which households it will write to, and which it refuses.

Run with:  python3 tools/wargame/seed/household_gate_test.py

Why this test exists (#5607). The gate used to be a *guess about the household's
appearance*: `name == "The War Game Family" or every login ends @example.com`.
On the deployed war game neither half can ever hold -- the household is named
"Sims Family (war game)" and its personas are real Dex identities at
@simstech.cloud -- so the seed refused the one household it exists for. The
tempting fix was to hand-stamp the marker in SQL, which is worse than useless:
find_target returns at the marker test BEFORE any other check, so a stamped
marker silently disarms every safety check for all later runs.

What is asserted here is the property that makes the stamp unnecessary and keeps
it unsafe: the gate follows EXPLICIT INTENT only, and every incomplete or
ambiguous request refuses. These cases start from the deployed reality (a
household named "Sims Family (war game)" with @simstech.cloud memberships),
because that is where the person starts and that is the state that used to fail.

No database: find_target's two reads are replaced with fixtures, so the test
runs anywhere and cannot touch a real household.
"""
import importlib.util
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
TARGET = "0c5890b2-dfe5-44d3-8dea-287288e7de94"
OTHER = "aaaa1111-bbbb-2222-cccc-333344445555"


def load_seed():
    spec = importlib.util.spec_from_file_location("household", os.path.join(HERE, "household.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class GateTest(unittest.TestCase):
    def setUp(self):
        self.hh = load_seed()
        # The deployed reality: one household, no marker, real Dex logins.
        self.rows = [
            (TARGET, "Sims Family (war game)", ""),
            (OTHER, "Some Other Family", ""),
        ]
        self.logins = {
            TARGET: ["amber.wargame@simstech.cloud", "jacob.sims@simstech.cloud"],
            OTHER: ["someone@simstech.cloud"],
        }
        self.hh.psql_rows = self.fake_rows

    def fake_rows(self, dsn, sql):
        if "memberships" in sql:
            for hid, emails in self.logins.items():
                if hid in sql:
                    return [(e,) for e in emails]
            return []
        return list(self.rows)

    def find(self, household_id=None, allow=None, adopt=False):
        return self.hh.find_target("dsn://x", household_id, allow, adopt)

    # ---- the one request that must work -------------------------------

    def test_naming_the_household_proceeds(self):
        """The whole ticket: the deployed war-game household IS seedable now."""
        hid, created, name = self.find(allow=TARGET, adopt=True)
        self.assertEqual(hid, TARGET)
        self.assertFalse(created)
        self.assertEqual(name, "Sims Family (war game)")

    def test_real_logins_are_a_warning_not_a_refusal(self):
        """@simstech.cloud memberships must not block it -- the old defect."""
        hid, _, _ = self.find(allow=TARGET, adopt=True)
        self.assertEqual(hid, TARGET)

    def test_the_marker_alone_is_enough(self):
        """A previous explicit run left the marker: no flags needed."""
        self.rows[0] = (TARGET, "Sims Family (war game)", "wargame-household-v1")
        self.assertEqual(self.find()[0], TARGET)

    # ---- everything incomplete or ambiguous refuses -------------------

    def test_no_id_at_all_refuses(self):
        """No implicit 'the only household'. This is the case that wipes a real one."""
        with self.assertRaises(SystemExit):
            self.find()

    def test_other_id_without_allow_refuses(self):
        with self.assertRaises(SystemExit):
            self.find(household_id=OTHER, adopt=True)

    def test_allow_without_adopt_refuses(self):
        """Naming the household is half the intent; --adopt is the other half."""
        with self.assertRaises(SystemExit):
            self.find(allow=TARGET, adopt=False)

    def test_unknown_id_refuses(self):
        with self.assertRaises(SystemExit):
            self.find(allow="11111111-2222-3333-4444-555555555555", adopt=True)

    def test_two_would_be_permissions_must_agree(self):
        with self.assertRaises(SystemExit):
            self.find(household_id=OTHER, allow=TARGET, adopt=True)

    def test_two_marked_households_refuse(self):
        self.rows[0] = (TARGET, "Sims Family (war game)", "wargame-household-v1")
        self.rows[1] = (OTHER, "Other", "wargame-household-v1")
        with self.assertRaises(SystemExit):
            self.find()

    # ---- the name is no longer a permission ---------------------------

    def test_the_magic_name_alone_is_not_a_permission(self):
        """The old gate would let this through; it must not any more."""
        self.rows.append(("cccc1111-bbbb-2222-cccc-333344445555", "The War Game Family", ""))
        with self.assertRaises(SystemExit):
            self.find()

    def test_an_all_example_com_household_is_not_a_permission(self):
        """The other half of the old guess, gone too."""
        self.logins[TARGET] = ["amber@example.com", "jacob@example.com"]
        with self.assertRaises(SystemExit):
            self.find(household_id=TARGET, adopt=True)

    # ---- the scope report is honest about what dies -------------------

    def test_scope_report_lists_what_is_deleted_and_what_survives(self):
        report = self.hh.scope_report(TARGET, "Sims Family (war game)")
        for table, _ in self.hh.CLEARED_TABLES:
            self.assertIn(table, report)
        # The two things an operator most needs to see are NOT in the delete list.
        self.assertIn("memberships and users", report)
        self.assertIn("every other household", report)


if __name__ == "__main__":
    unittest.main(verbosity=2)
