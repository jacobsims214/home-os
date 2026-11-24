#!/usr/bin/env python3
"""Assertion check for `jacob-winter-subscription-found` (February, settings/bills).

The scenario asks: there is a recurring charge I cannot place — find it, tell me what
it is and what it costs. Its assertion is about what the tools say, so this reads
ground truth rather than a screenshot.

## Why this check asserts 17.99 and not the corpus's 19.99

The scenario prose says "$19.99" **three times**, and the household has no 19.99 bill:
the seeded recurring charge is `Streamly subscription` at **17.99**. `SETUP.md` already
lists "the $19.99 subscription bill" among the seed preconditions the year needs and
does not have, so the number in the prose is the thing that is out of date, not the
product. Asserting 19.99 here would make this check green-into-red for a reason that
has nothing to do with the code, and a false FAIL is worse than no check
(standing instructions §5, and SETUP.md rule 1).

So the check asserts the shape the scenario is really about — *a recurring subscription
exists, is findable by name, is stated with an amount and a payee* — and prints the
household's own number in its verdict. When the seed gains a 19.99 charge (or the prose
is corrected), this check keeps working: it does not encode either number.

Exits 0 PASS, 1 FAIL, 2 BLOCKED (could not tell), per scenario_driver.py's contract.
"""

import argparse
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
WG = os.path.dirname(HERE)
SUB = "Streamly"
failures = []


class Untellable(Exception):
    """Could not read what it needed: BLOCKED, not FAIL."""


def ok(cond, msg):
    if not cond:
        failures.append(msg)
    return cond


def mcp(tool, args):
    argv = [sys.executable, os.path.join(WG, "mcpcall.py"), "--text", tool, json.dumps(args)]
    p = subprocess.run(argv, capture_output=True, text=True, timeout=90)
    if p.returncode != 0:
        blob = (p.stderr or p.stdout).strip()
        # Exit 2 is transport/usage; "could not tell" is BLOCKED, never a FAIL (§7).
        if p.returncode == 2:
            raise Untellable("mcpcall.py %s could not reach the server: %s" % (tool, blob[:160]))
        failures.append("mcpcall.py %s exited %d: %s" % (tool, p.returncode, blob[:200]))
        return None
    try:
        return json.loads(p.stdout)
    except json.JSONDecodeError:
        failures.append("mcpcall.py %s did not return JSON" % tool)
        return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--evidence-dir", required=True)
    ap.add_argument("--persona", default="jacob")
    args = ap.parse_args()

    # The check reads ground truth through the MCP, which needs this persona's token.
    # scenario_driver.py resolves it from the Secret before invoking a check.
    if not os.environ.get("HOMEOS_AGENT_TOKEN"):
        print("BLOCKED: HOMEOS_AGENT_TOKEN is not set; cannot read the household's bills")
        return 2

    try:
        found = mcp("bill_find", {"query": "subscription"})
    except Untellable as exc:
        print("BLOCKED: %s" % exc)
        return 2
    if found is None:
        print("FAIL: " + failures[-1])
        return 1

    items = found.get("items") or []
    ok(len(items) >= 1, "bill_find query 'subscription' returned no bill; the recurring "
                        "charge cannot be found by the word a person would use")
    if not items:
        print("FAIL: " + "; ".join(failures))
        return 1

    bill = items[0]
    ok(SUB.lower() in (bill.get("name") or "").lower(),
       "the subscription bill is %r, expected it to name %s" % (bill.get("name"), SUB))

    # The recurring shape: a frequency that is not "once", and an amount.
    freq = (bill.get("frequency") or "").lower()
    ok(freq and freq != "once",
       "frequency is %r; a recurring charge cannot be a one-off" % (bill.get("frequency"),))
    amount = bill.get("amount")
    ok(isinstance(amount, (int, float)) and amount > 0,
       "amount is %r; the reply cannot state a figure the bill does not have" % (amount,))

    # The payee is what makes it placeable — "I cannot place this charge" is answered
    # by the name on it, so a bill with no payee fails the scenario's purpose.
    payee = ((bill.get("payee") or {}).get("name") or bill.get("payee_name") or "")
    ok(bool(payee), "the bill names no payee, so the charge is still unplaced")

    # The key it must be findable by — people search for the word on the statement.
    again = mcp("bill_find", {"query": SUB})
    again_items = (again or {}).get("items") or []
    ok(any((i.get("id") == bill.get("id")) for i in again_items),
       "bill_find by %r did not return this bill; it is not findable by its own name" % SUB)

    if failures:
        print("FAIL: " + "; ".join(failures))
        return 1
    print("PASS: %s is %d cents, payee %r, %s"
          % (bill.get("name"), round(float(amount) * 100), payee, freq))
    return 0


if __name__ == "__main__":
    sys.exit(main())
