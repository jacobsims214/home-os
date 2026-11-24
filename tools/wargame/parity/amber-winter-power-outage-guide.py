#!/usr/bin/env python3
"""Parity probe for `amber-winter-power-outage-guide` (January, guides).

The corpus's parity fact:

    the outage guidance: the hours the reply states and the hours in the guide's own
    body are the same number, and the guide's address is the one guide_find returns

## The fact, made mechanical

Two things have to match, and they are checked independently because they fail
independently:

1. **The address.** The guide the page opens is the guide `guide_find` returned. An
   agent that constructs a plausible link is the failure the scenario names, and a link
   that merely *looks* right passes a human read — so the comparison is on the path.
2. **The number of hours.** A fridge/freezer guidance statement contains "N hours".
   The guide's own body and the page must state the same N.

A guide that does not exist is not a parity mismatch: it is a finding about the
household (`SETUP.md` lists the power-outage KB doc as a seed precondition the year
needs). This probe says which of the two it is, so the scorecard can tell a missing
document from two channels disagreeing about a document that is there.
"""

PARITY = ("the outage guide's path, and the hours its fridge/freezer guidance states, "
          "as guide_find/kb_get return them and as the guides page renders them")

import json
import re

HOURS_RE = re.compile(r"(\d{1,3})\s*hours?", re.IGNORECASE)

# The corpus's `open` step names /dashboard/guides (the list). The fact — the guide's
# own body and the hours in it — renders on the reference viewer, which is the page the
# scenario's own assertion talks about ("the reference page renders that same doc at
# the same path").
UI_PATH = "/dashboard/guides/reference"


def _payloads(agent):
    for tool, raw in (agent.get("payloads") or {}).items():
        try:
            yield tool, json.loads(raw)
        except Exception:
            continue


def _guide(agent):
    """(path, body) of the guide the agent's own payloads point at."""
    path = body = None
    for tool, doc in _payloads(agent):
        data = doc.get("data") or {}
        if tool == "guide_find":
            items = doc.get("items") or []
            if items and path is None:
                path = items[0].get("path") or items[0].get("slug")
                body = body or items[0].get("body") or items[0].get("excerpt")
        elif tool == "kb_get":
            path = path or data.get("path")
            body = body or data.get("body")
        elif tool == "kb_search":
            items = doc.get("items") or []
            if items and path is None:
                path = items[0].get("path")
    return path, body


def _hours(*texts):
    for t in texts:
        m = HOURS_RE.search(t or "")
        if m:
            return int(m.group(1)), (t or "")[max(0, m.start() - 40):m.end() + 20].replace("\n", " ")
    return None, ""


def probe(agent, ui):
    path, body = _guide(agent)
    text = (ui or {}).get("text") or ""
    if not path and not body:
        return (None, None,
                "the agent's payloads returned no guide (guide_find/kb_get/kb_search all "
                "empty) — the household has no outage document to compare")

    # The address half. The page's URL is what a person would copy; the guide's own path
    # is what the tool returned. The page may be a list (that is what /dashboard/guides
    # is), so the path has to appear in the rendered text OR be what the page opened.
    ui_path_ok = bool(path) and (path in text or path in (ui or {}).get("final_url", ""))
    agent_hours, agent_ctx = _hours(body)
    ui_hours, ui_ctx = _hours(text)

    detail_bits = []
    if path:
        detail_bits.append("guide_find path %r; %s" % (
            path, "the page shows it" if ui_path_ok else "the page does NOT show it"))
    if agent_hours is not None or ui_hours is not None:
        detail_bits.append("hours: guide body says %r, page says %r"
                           % (agent_hours, ui_hours))
    detail = "; ".join(detail_bits) or "no comparable fact"

    if agent_hours is None and ui_hours is None:
        # The path is still comparable, and it is the half the scenario calls
        # load-bearing (an invented link is the failure mode).
        if not path:
            return (None, None, detail)
        return (path, path if ui_path_ok else None, detail)

    # Compare the two halves as one tuple: address AND number. Either disagreeing is a
    # mismatch, which is what the corpus asks for ("... and the guide's address is the
    # one guide_find returns").
    agent_val = {"path": path, "hours": agent_hours}
    ui_val = {"path": path if ui_path_ok else (ui or {}).get("final_url"), "hours": ui_hours}
    return (agent_val, ui_val, detail)


def same_pair(a, b):
    return a == b
