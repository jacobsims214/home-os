#!/usr/bin/env python3
"""Assertion check for `amber-payee-contact` (R4, household reality).

"The water bill is wrong — who do I call?" The assertion is that the payee's
number is the *same number* in the tool payload and in the contact record, and
that it is reachable in one tap. The tap itself needs a browser; the agreement
between the two payloads does not, and it is the part that catches the real
defect (a page that prints a number it typed itself). So this check asserts the
payload half and says plainly that the tel: href half needs the UI driver.
"""

import argparse
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
WG = os.path.dirname(HERE)
failures = []


class Untellable(Exception):
    """The check could not read what it needed: BLOCKED, not FAIL."""



def ok(cond, msg):
    if not cond:
        failures.append(msg)
    return cond


def digits(s):
    return "".join(ch for ch in str(s or "") if ch.isdigit())


def mcp(tool, args):
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


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--evidence-dir", required=True)
    ap.add_argument("--persona", default="amber")
    args = ap.parse_args()

    if not os.environ.get("HOMEOS_AGENT_TOKEN"):
        print("BLOCKED: HOMEOS_AGENT_TOKEN is not set; cannot read ground truth")
        return 2

    bills = mcp("bill_find", {"query": "Water"})
    if bills is None:
        print("FAIL: " + failures[-1])
        return 1
    rows = bills.get("items") or []
    ok(len(rows) >= 1, "no Water bill found in the seeded household")
    if not rows:
        print("FAIL: " + failures[0])
        return 1

    bill = rows[0]
    payee = bill.get("payee") or {}
    ok(bool(payee.get("name")), "the Water bill's payee object names nobody")
    ok(bool(payee.get("phone")),
       "the Water bill's payee has no phone, so there is nothing to tap")
    ok(bool(payee.get("vendor_id")),
       "the payee is not linked to a vendor, so contact routing has no record")

    contacts = mcp("contact_find", {"query": "Warwick Water"})
    if contacts is not None:
        crows = contacts.get("items") or []
        ok(len(crows) >= 1, "contact_find found no Warwick Water contact")
        if crows and payee.get("phone"):
            numbers = {digits(c.get("phone")) for c in crows if c.get("phone")}
            ok(digits(payee["phone"]) in numbers,
               "the bill's payee phone %r is not the contact record's %r — a page "
               "reading one and a person dialling the other would disagree"
               % (payee.get("phone"), sorted(numbers)))

    if failures:
        print("FAIL: " + failures[0] + (" (+%d more)" % (len(failures) - 1)
                                        if len(failures) > 1 else ""))
        return 1
    print("PASS: the payee's phone in bill_find is the contact record's number, "
          "vendor-linked (the tel: href itself needs the UI driver)")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Untellable as exc:
        print("BLOCKED: %s" % exc)
        sys.exit(2)
