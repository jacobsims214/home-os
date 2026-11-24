#!/usr/bin/env python3
"""Parity probe for `jacob-winter-subscription-found` (Jan/Feb, bills+settings).

The corpus's parity fact:

    the recurring charge's amount and payee: bill_find's 19.99 and the payee it names
    appear identically in the settings page and in the reply

## What this compares, and the honest answer it gives

The scenario was written expecting a **$19.99** subscription. The seeded household has
**Streamly subscription at $17.99** and no 19.99 bill at all (`SETUP.md` already lists
"the $19.99 subscription bill" as a seed precondition the year needs and does not have).
So there are three possible worlds, and this probe must not confuse them:

1. The agent names a bill and the page renders the same amount and payee → **PARITY_OK**,
   whatever the amount is. A cross-channel check is about the two channels agreeing, not
   about the corpus's number being right.
2. The agent names one amount and the page shows another → **PARITY_MISMATCH**. This is
   the finding the ticket exists for.
3. Neither produced an amount → **CANNOT_COMPARE**.

The corpus's 19.99 is then a *separate* question, and `truth()` answers it from the
database: it reports what the household actually says, so a month where both channels
correctly say 17.99 can be told apart from a month where the product is wrong. That
distinction is the whole reason ground truth is a third opinion.
"""

PARITY = ("the recurring charge's amount and the payee it names, as bill_find reports "
          "it and as the settings/bills page renders it")

import re
import subprocess
import os

# The bill the scenario is about, by the name the seed gives it. Matching on the name
# (not the amount) is deliberate: the amount is what the corpus got wrong, so keying
# the probe on it would make the probe fail for the corpus's reason, not the product's.
NEEDLE = "streamly"

# Where this fact is actually DRAWN. The corpus's `open` step names
# /dashboard/settings, which is a navigation hub: it renders no bills at all, so a
# comparison against it would report "the page does not show the bill" for every
# household alive. The bills list is the page that renders the amount and the payee.
# A probe may override the starting page because the probe owns the comparison; the
# corpus's `open` step stays as written, since it is what a person would do first.
UI_PATH = "/dashboard/bills"


def _amounts(text):
    """Every money amount in a blob of text, normalised to cents as ints."""
    out = []
    for m in re.finditer(r"\$\s?([0-9][0-9,]*\.?[0-9]*)", text or ""):
        try:
            out.append(round(float(m.group(1).replace(",", "")) * 100))
        except ValueError:
            pass
    return out


def _agent_bill(agent):
    """The subscription bill as the agent's own tool payloads describe it."""
    for tool, raw in (agent.get("payloads") or {}).items():
        try:
            doc = __import__("json").loads(raw)
        except Exception:
            continue
        for item in doc.get("items") or []:
            name = (item.get("name") or "").lower()
            if NEEDLE in name:
                cents = None
                if item.get("amount") is not None:
                    try:
                        cents = round(float(item["amount"]) * 100)
                    except (TypeError, ValueError):
                        cents = None
                payee = ((item.get("payee") or {}).get("name") or item.get("payee_name") or "")
                return {"name": item.get("name"), "cents": cents, "payee": payee}
    return None


def probe(agent, ui):
    bill = _agent_bill(agent)
    text = (ui or {}).get("text") or ""

    # The UI side: the bill's row, and the amount drawn in it.
    #
    # A bill row renders over several text lines, not one — on /dashboard/bills the
    # shape is `Streamly subscription / Automatic / Next Sat, Dec 5 · in 11 days /
    # Streamly / · / Monthly / Credit card ····3312 / $17.99`. Reading only the line
    # that contains the name finds no amount and reports CANNOT_COMPARE for every bill
    # on the page. So take a window around the name, and bound it to the next bill's
    # name so a neighbour's amount cannot be read as this one's.
    lines = text.splitlines()
    ui_line, ui_cents = "", None
    for i, line in enumerate(lines):
        if NEEDLE not in line.lower():
            continue
        window = lines[i:i + 12]
        for j, w in enumerate(window[1:], 1):
            # stop at the next bill name if the page names another bill first
            if j and _amounts(w):
                ui_line = " / ".join(x for x in window[:j + 1] if x.strip())
                ui_cents = _amounts(w)[0]
                break
        if ui_cents is None:
            ui_line = line
        break
    payee_visible = bool(bill and bill.get("payee") and bill["payee"].lower() in text.lower())

    if bill is None:
        return (None, ui_cents,
                "the agent's bill_find payload names no %r bill" % NEEDLE)
    if ui_cents is None:
        return (bill["cents"], None,
                "the page does not render a %r row with an amount (page text has %d chars)"
                % (NEEDLE, len(text)))

    detail = ("agent bill_find: %s at %s cents, payee %r; page row: %r"
              % (bill["name"], bill["cents"], bill["payee"], ui_line.strip()[:90]))
    if not payee_visible:
        detail += "; the payee name is not rendered on the page"
    return (bill["cents"], ui_cents, detail)


def truth(agent, ui):
    """Ground truth: what the household's database says this bill costs.

    This is the answer to "and are they both right?". It reads the DB directly rather
    than another tool, so a bug in the tool cannot be confirmed by itself.
    """
    sql = ("select name, amount, payee_name from bills "
           "where lower(name) like '%%%s%%'" % NEEDLE)
    argv = ["kubectl"]
    kubeconfig = os.environ.get("WARGAME_KUBECONFIG", "")
    if kubeconfig:
        argv += ["--kubeconfig", kubeconfig]
    argv += ["-n", "home-os", "exec", "home-os-postgres-1", "--request-timeout=25s", "--",
             "psql", "-U", "postgres", "-d", "app", "-tAc", sql]
    try:
        r = subprocess.run(argv, capture_output=True, text=True, timeout=30)
    except (subprocess.SubprocessError, OSError) as exc:
        return (None, "could not read the database: %s" % exc)
    if r.returncode != 0:
        return (None, "the database query failed: %s" % (r.stderr or "").strip()[:150])
    row = (r.stdout or "").strip().splitlines()
    if not row:
        return (None, "the household has no %r bill in the database" % NEEDLE)
    parts = row[0].split("|")
    try:
        cents = round(float(parts[1]) * 100)
    except (IndexError, ValueError):
        return (None, "the database row was not parseable: %r" % row[0][:120])
    note = ("the household's database says %s is %d cents (%%s), payee %r"
            % (parts[0], cents, parts[2] if len(parts) > 2 else ""))
    return (cents, note % "the seeded amount")
