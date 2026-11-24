#!/usr/bin/env python3
"""Parity probe for `amber-winter-power-outage-guide` (January, guides).

The corpus's parity fact:

    the outage guidance: the hours the reply states and the hours in the guide's own
    body are the same number, and the guide's address is the one guide_find returns

## The fact, made mechanical — and the false positive this probe was written twice for

The first version of this probe searched the whole rendered reference page for
"N hours" and compared the first hit to the guide body. It reported PARITY_MISMATCH
on a healthy household: the page renders *every* doc's excerpt, and the first
"N hours" on it belongs to the **insurance-claim** doc ("call ... within 72 hours"),
not to the outage guide. The agent's guide body had no hours because the household
has no outage guide at all. Two different facts, compared as if they were one.

So the page half is now anchored: it reads only the rendered block for **the guide
the agent's own payloads name** (keyed on the doc's path, which the reference page
prints above each doc). If that guide is not on the page, the probe says the channels
disagree about a guide's *existence* — which is a real cross-channel finding — rather
than comparing this doc's number to a different doc's number.

## Two things compared, independently

1. **The address.** The guide the agent points at is a guide the page lists. An agent
   that constructs a plausible link is the failure the scenario names, and a link that
   merely *looks* right passes a human read, so the comparison is on the path.
2. **The number of hours** in that guide's fridge/freezer guidance, on both sides.

A guide that exists nowhere is not a parity mismatch: it is a finding about the
household (`SETUP.md` lists the power-outage KB doc as a seed precondition the year
needs). The probe distinguishes the two so the scorecard can tell "the doc is missing
from the seed" apart from "the two channels tell different stories about a doc".
"""

PARITY = ("the outage guide's path, and the hours its fridge/freezer guidance states, "
          "as guide_find/kb_get return them and as the guides page renders them")

# The corpus's `open` step names /dashboard/guides (the list). The guide's own body —
# what the assertion actually talks about ("the reference page renders that same doc at
# the same path") — renders on the reference viewer.
UI_PATH = "/dashboard/guides/reference"

import json
import re

HOURS_RE = re.compile(r"(\d{1,3})\s*hours?", re.IGNORECASE)


def _payloads(agent):
    for tool, raw in (agent.get("payloads") or {}).items():
        try:
            yield tool, json.loads(raw)
        except Exception:
            continue


def _guide(agent):
    """(path, body, guide_find_count) — the guide the agent's payloads point at.

    `guide_find` is the authority on whether the household HAS a guide. A `kb_search`
    hit is a knowledge-base doc, and on this household it is the router note: counting
    that as "the guide the agent found" is how the first version of this probe turned a
    missing seed doc into a cross-channel mismatch.
    """
    path = body = None
    found = 0
    for tool, doc in _payloads(agent):
        data = doc.get("data") or {}
        if tool == "guide_find":
            items = doc.get("items") or []
            found = max(found, int(doc.get("total") or len(items)))
            if items and path is None:
                path = items[0].get("path") or items[0].get("slug")
                body = body or items[0].get("body") or items[0].get("excerpt")
        elif tool == "kb_get":
            path = path or data.get("path")
            body = body or data.get("body")
    return path, body, found


def _block_for_path(text, path):
    """The rendered block belonging to `path`, or "" if the page does not list it.

    The reference page prints each doc's address on its own line, directly above the
    doc's title and excerpt. The block is that address's line plus the next few lines,
    and it stops at the next address so a neighbour's numbers cannot be read as this
    doc's — the exact bug this probe had first.
    """
    if not path:
        return ""
    lines = text.splitlines()
    starts = [i for i, l in enumerate(lines) if l.strip() == path.strip()]
    if not starts:
        return ""
    i = starts[0]
    block = [lines[i]]
    for line in lines[i + 1:]:
        # the next doc's address line ends this block
        if re.match(r"^[a-z0-9][a-z0-9._/-]*$", line.strip()) and line.strip() != path:
            break
        block.append(line)
        if len(block) > 14:
            break
    return "\n".join(block)


def _hours(text):
    m = HOURS_RE.search(text or "")
    return (int(m.group(1)) if m else None)


def probe(agent, ui):
    path, body, found = _guide(agent)
    text = (ui or {}).get("text") or ""
    if found == 0 and not path:
        # guide_find found no guide and nothing else named one. There is no guide on
        # the agent's side to compare, so there is no cross-channel fact — this is the
        # household gap SETUP.md predicts ("the power-outage KB doc" is a missing seed
        # precondition), not two channels disagreeing. Saying PARITY_MISMATCH here would
        # be the same class of error as a false FAIL: it blames the channels for a
        # document that does not exist on either side.
        return (None, None,
                "guide_find returned no guide at all (total=0) and no kb_get named one, so "
                "there is no guide to compare. This is the seed precondition SETUP.md lists "
                "as missing ('the power-outage KB doc'), not a channel disagreement: the "
                "household has no outage guide for either channel to state a fact about.")

    block = _block_for_path(text, path)
    page_has_doc = bool(block)
    agent_hours = _hours(body)
    ui_hours = _hours(block)

    detail = ("guide_find/kb_get path %r; the page %s; hours: guide body %r, page block %r"
              % (path, "lists it" if page_has_doc else "does NOT list it",
                 agent_hours, ui_hours))
    if page_has_doc and block.strip():
        detail += " | page block: %r" % block.replace("\n", " / ")[:150]
    else:
        detail += " | the page's own docs are: %s" % ", ".join(
            sorted({l.strip() for l in text.splitlines()
                    if re.match(r"^(how-to|money|guides)/[a-z0-9._/-]+$", l.strip())}))[:200]

    # Compare the two halves as one fact: the address must be one the page lists, and
    # the number the guide states must be the number on its own rendered block.
    agent_val = {"path": path, "hours": agent_hours}
    ui_val = {"path": path if page_has_doc else None,
              "hours": ui_hours if page_has_doc else None}

    if not page_has_doc:
        # The guide the agent names is not on the page. That is a genuine disagreement
        # about the fact the scenario calls load-bearing (the address), so it is
        # comparable and it is a mismatch — not "the page gave us nothing".
        return (agent_val, ui_val, detail)
    if agent_hours is None and ui_hours is None:
        return (agent_val, ui_val, detail)
    return (agent_val, ui_val, detail)
