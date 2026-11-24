#!/usr/bin/env python3
"""Parity probe for `amber-winter-members-invite` (February, invites).

The corpus's parity fact:

    the pending invite: its address and role in the invites payload are the address and
    role rendered on the members page, and the pending count matches

## Why this probe reads `family_overview`, not `invite_find`

The scenario's own step says `api.py GET /api/v1/invites`, and there is **no
`invite_find` MCP tool** (`tools/call` answers `-32602 tool not found`). The agent
channel's reachable read of "who is invited" is `family_overview`, which is what the
scenario's *other* step calls. Reading the payload the agent can actually get is the
point — comparing against a call nobody can make would report CANNOT_COMPARE forever.

If the household has no pending invite (which is the seeded state: `amber-winter-
members-invite` is a WRITE scenario — Amber invites grandma), there is nothing for
either channel to state and the probe says so. That is honest: an invite that has not
been created is not a cross-channel disagreement.

**A finding from trying to play this one (2026-11-24):** there is no `invite_*` MCP tool
at all — `mcpcall.py -list` shows no invite tool, and `invite_find` answers
`-32602 tool not found`. Inviting is a **UI/REST-only** action (`api.py GET
/api/v1/invites` needs a Dex id_token, which an agent token cannot be). So a scenario
that declares `channel: both` and whose write step is the invite cannot have its parity
fact produced by the agent channel as written. This probe therefore reports
CANNOT_COMPARE until an invite exists, rather than passing on an empty comparison. That
is a gap in the corpus/API surface worth its own ticket, not something to paper over.
"""

PARITY = ("the pending invite's address and role, and the pending count, as the "
          "household payload reports them and as the members page renders them")

UI_PATH = "/dashboard/members"

import json
import re


def _payloads(agent):
    for tool, raw in (agent.get("payloads") or {}).items():
        try:
            yield tool, json.loads(raw)
        except Exception:
            continue


def _invites(agent):
    """[(email, role)] from whatever the agent's payloads call pending invitations."""
    for tool, doc in _payloads(agent):
        data = doc.get("data") if isinstance(doc.get("data"), dict) else doc
        for key in ("invites", "invitations", "pending_invites"):
            rows = data.get(key)
            if isinstance(rows, list):
                out = []
                for r in rows:
                    if isinstance(r, dict):
                        out.append((r.get("email") or r.get("address") or "",
                                    r.get("role") or ""))
                return out
        # family_overview nests members; an invitee may appear with no joined_at
        for key in ("members", "people"):
            rows = data.get(key)
            if isinstance(rows, list):
                out = [(r.get("email") or "", r.get("role") or "")
                       for r in rows
                       if isinstance(r, dict) and str(r.get("status") or "").lower()
                       in ("invited", "pending")]
                if out:
                    return out
    return []


EMAIL_RE = re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}")


def probe(agent, ui):
    invites = _invites(agent)
    text = (ui or {}).get("text") or ""
    page_emails = sorted(set(EMAIL_RE.findall(text)))

    if not invites:
        return (None, None,
                "the agent's payloads list no pending invitation, so there is no invite for "
                "either channel to state. This scenario is a WRITE (Amber invites grandma): "
                "until the invite exists the parity fact has no subject.")

    agent_val = sorted((e, r) for e, r in invites if e)
    # The UI side must show the same addresses. Role text is matched case-insensitively
    # because a page renders "Adult"/"Member" while a payload carries a role enum.
    lower = text.lower()
    matched = sorted((e, r) for e, r in agent_val
                     if e.lower() in lower and (not r or r.lower() in lower))
    detail = ("the household payload lists %s; the members page renders %s"
              % ([f"{e} ({r})" for e, r in agent_val][:6], page_emails[:6]))
    return (agent_val, matched, detail)
