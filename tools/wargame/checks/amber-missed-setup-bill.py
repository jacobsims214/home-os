#!/usr/bin/env python3
"""Assertion check for `amber-missed-setup-bill` (R4, household reality).

The scenario asks: the dental bill is due in days and nothing was ever set up to
pay it — how is it supposed to get paid? Its assertion is almost entirely about
what the tools say, so this check reads ground truth rather than trusting a
screenshot (standing instructions §7: a check that cannot tell must fail).

Two places where this check deliberately differs from the scenario's prose, and
why — both were found by running it, and both are the check being wrong rather
than the product:

* **`days_until_due` is not asserted to be exactly 3.** The seed plants the bill
  `due_in=3` relative to *the day the seed ran*, so the field drifts by one a day
  and a hardcoded 3 turns green-into-red with no code change. The rule the
  scenario is really about is that the bill sits *inside the needs_setup lead
  window and is not yet overdue*, which is what is asserted here.
* **The reminder is matched on its body, not `about_id`.** `notification_find`
  returns `data.notifications`, and the bill rows it returns (`bill_due`,
  `bill_digest`) carry `about_id: null` — the bill is named in the text, not
  linked by id. Asserting on `about_id` would fail against a correct product.

Exits 0 and prints one line when the assertion holds; exits 1 with the first
thing that did not, so a FAIL names a fact and not a mood.
"""

import argparse
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
WG = os.path.dirname(HERE)
BILL = "Maple Family Dental"
failures = []


class Untellable(Exception):
    """The check could not read what it needed: BLOCKED, not FAIL."""



def ok(cond, msg):
    if not cond:
        failures.append(msg)
    return cond


def mcp(tool, args):
    """Call mcpcall.py --text and return the parsed payload, or None."""
    argv = [sys.executable, os.path.join(WG, "mcpcall.py"), "--text", tool,
            json.dumps(args)]
    p = subprocess.run(argv, capture_output=True, text=True, timeout=90)
    if p.returncode != 0:
        blob = (p.stderr or p.stdout).strip()
        # Exit 2 is transport/usage, not the server refusing the call. A 429 or a
        # dead port-forward means this check could not tell — which is BLOCKED
        # (exit 2), never a FAIL that would read as a product defect (§7).
        if p.returncode == 2:
            raise Untellable("mcpcall.py %s could not reach the server: %s"
                             % (tool, blob[:160]))
        failures.append("mcpcall.py %s exited %d: %s"
                        % (tool, p.returncode, blob[:200]))
        return None
    try:
        return json.loads(p.stdout)
    except json.JSONDecodeError:
        failures.append("mcpcall.py %s did not return JSON" % tool)
        return None


def notifications(doc):
    """Rows out of notification_find, whichever shape the tool used."""
    if not isinstance(doc, dict):
        return []
    for holder in (doc.get("data"), doc):
        if isinstance(holder, dict):
            for key in ("notifications", "items"):
                if isinstance(holder.get(key), list):
                    return holder[key]
    return []


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--evidence-dir", required=True)
    ap.add_argument("--persona", default="amber")
    args = ap.parse_args()

    if not os.environ.get("HOMEOS_AGENT_TOKEN"):
        print("BLOCKED: HOMEOS_AGENT_TOKEN is not set; cannot read ground truth")
        return 2

    bills = mcp("bill_find", {"query": BILL})
    if bills is None:
        print("FAIL: " + failures[-1])
        return 1

    items = bills.get("items") or []
    ok(len(items) == 1, "expected exactly one %s bill, got %d" % (BILL, len(items)))
    bill = items[0] if items else {}
    if bill:
        # The seed plants this bill on purpose: $412, autopay off, no account.
        # `attention` is the answer AttentionFor gives; asserting on it rather
        # than on is_autopay is standing instructions §6.
        ok(bill.get("attention") == "needs_setup",
           "attention is %r, expected 'needs_setup'" % bill.get("attention"))
        ok(bill.get("autopay") is False,
           "autopay is %r, expected false" % bill.get("autopay"))
        ok(not bill.get("payment_account"),
           "payment_account is present (%r); nothing should be set up to pay it"
           % (bill.get("payment_account"),))
        ok(abs(float(bill.get("amount") or 0) - 412.00) < 0.005,
           "amount is %r, expected 412.00" % bill.get("amount"))
        days = bill.get("days_until_due")
        # See the module docstring: the contract is "inside the needs_setup lead
        # window and not overdue", not a frozen 3.
        ok(isinstance(days, int) and 0 <= days <= 7,
           "days_until_due is %r; expected 0..7 (inside the needs_setup lead, not "
           "overdue). The seed sets due_in=3 relative to the day it ran, so an "
           "exact 3 is not stable." % (days,))

    notes = mcp("notification_find", {"limit": 100})
    if notes is not None:
        rows = [n for n in notifications(notes) if BILL in json.dumps(n)]
        ok(len(rows) >= 1,
           "no notification mentions %r, so nothing told the family it needs "
           "setting up" % BILL)
        # The whole point of the scenario: the message names BOTH missing halves.
        # A message that only says "due soon" fails.
        naming = [n for n in rows
                  if "autopay is off" in (n.get("body") or "").lower()
                  and "no account is set" in (n.get("body") or "").lower()]
        ok(bool(naming),
           "no reminder names both missing halves ('autopay is off' and 'no "
           "account is set'); bodies were: %s"
           % [ (n.get("body") or "")[:80] for n in rows ][:3])
        for n in naming:
            ok(n.get("needs_you") is True,
               "the reminder about the bill has needs_you=%r, expected true"
               % (n.get("needs_you"),))
            body = (n.get("body") or "").lower()
            ok("412" in body or "412.00" in body,
               "the reminder does not carry the amount: %r" % body[:120])

    if failures:
        print("FAIL: " + failures[0] + (" (+%d more)" % (len(failures) - 1)
                                        if len(failures) > 1 else ""))
        return 1
    print("PASS: bill is needs_setup with no account, and the reminder names both "
          "missing halves with the amount")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Untellable as exc:
        print("BLOCKED: %s" % exc)
        sys.exit(2)
