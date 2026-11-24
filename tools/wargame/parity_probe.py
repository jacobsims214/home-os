#!/usr/bin/env python3
"""Compare what the AGENT reported to what the UI RENDERED, on one parity fact.

    ./parity_probe.py --id jacob-winter-subscription-found --evidence-dir DIR
    ./parity_probe.py --id ... --agent <path> --ui <path>     # replay saved snapshots

Exit: 0 PARITY_OK — both channels produced the fact and it is the same fact
      1 PARITY_MISMATCH — both produced a fact and they disagree (its own verdict,
        separate from FAIL: the product may be right in one channel and wrong in the
        other, which is a different bug with a different fix)
      2 CANNOT_COMPARE — one side produced no fact (a channel never ran, a BLOCKED
        month, no probe written for this scenario yet). Not a pass, not a mismatch.

## What a probe is, and why this file does not invent one

A scenario's `parity:` is English: "the 2026 interest amount: document_find's amount
for the 1098 equals the amount rendered on its documents row". English cannot be
compared, so each `channel: both` scenario needs a **probe** that names, in code, the
two things being compared: how to get the fact from the agent's side (a tool payload,
or the agent's own reply) and how to find it on the rendered page.

A probe lives at `parity/<scenario-id>.py` and defines one function:

    def probe(agent, ui):
        '''Return (agent_value, ui_value, detail).'''
        # `agent` is the scenario's step evidence: the tool payloads it captured.
        # `ui`    is the rendered text + final URL from ui_channel.mjs.
        # Returning None for either side means CANNOT_COMPARE, not a pass.

**No probe is not a pass.** A `channel: both` scenario with no probe reports
CANNOT_COMPARE naming the missing file, because the alternative — treating "we did not
compare" as "they agreed" — is how a corpus of 36 parity facts all reads green while
comparing nothing.

## Ground truth is a third opinion, not a tiebreaker

`--truth` cross-checks the *agent's* value against the database or the tool payload.
Two channels agreeing is not evidence they are right: an agent and a UI can both
render the same stale cache. When a probe supplies a `truth(...)` function it is run
too, and a truth disagreement is a separate finding (`TRUTH_MISMATCH`) — see run.sh.
"""

import argparse
import importlib.util
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
PARITY_OK, PARITY_MISMATCH, CANNOT_COMPARE, TRUTH_MISMATCH = (
    "PARITY_OK", "PARITY_MISMATCH", "CANNOT_COMPARE", "TRUTH_MISMATCH")


def load_probe(sid):
    path = os.path.join(HERE, "parity", sid + ".py")
    if not os.path.exists(path):
        return None, path
    spec = importlib.util.spec_from_file_location("parity_" + sid.replace("-", "_"), path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod, path


def load_json(path):
    try:
        with open(path, encoding="utf-8") as fh:
            return json.load(fh)
    except (OSError, json.JSONDecodeError):
        return None


def agent_side(evidence_dir, sid):
    """What the agent channel produced: its step results, and the first ok tool payload."""
    res = load_json(os.path.join(evidence_dir, sid, "result.json"))
    if res is None:
        return None
    payloads = {}
    for step in res.get("steps") or []:
        if step.get("kind") == "mcpcall" and step.get("status") == "PASS":
            # The driver saved the raw payload; key by the tool named in the step.
            for tok in (step.get("step") or "").split():
                if tok and not tok.endswith(".py") and tok not in ("mcpcall",):
                    payloads.setdefault(tok, step.get("output") or "")
                    break
    return {"result": res, "payloads": payloads}


def ui_side(evidence_dir, sid):
    """What the UI channel rendered."""
    return load_json(os.path.join(evidence_dir, sid, "ui-channel.json"))


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("--id", required=True)
    ap.add_argument("--evidence-dir", default="")
    ap.add_argument("--json", action="store_true")
    ap.add_argument("--ui-path", action="store_true",
                    help="print the page this scenario's parity fact renders on (the probe's "
                         "UI_PATH), if the probe names one; else nothing. run.sh uses it so "
                         "the round renders the page the fact is on, not only the starting "
                         "page the corpus's `open` step names.")
    ap.add_argument("--agent", help="replay: a saved agent snapshot (json)")
    ap.add_argument("--ui", help="replay: a saved ui-channel.json")
    args = ap.parse_args(argv)

    sid = args.id
    mod, probe_path = load_probe(sid)

    if args.ui_path:
        # Loaded without needing any evidence: the round asks this before it renders.
        if mod is not None and getattr(mod, "UI_PATH", None):
            print(mod.UI_PATH)
            return 0
        return 1
    agent = load_json(args.agent) if args.agent else agent_side(args.evidence_dir, sid)
    ui = load_json(args.ui) if args.ui else ui_side(args.evidence_dir, sid)

    out = {"id": sid, "verdict": None, "parity_fact": None, "agent_value": None,
           "ui_value": None, "detail": "", "probe": probe_path}

    if mod is None:
        out.update(verdict=CANNOT_COMPARE,
                   detail="no probe at parity/%s.py — the scenario's parity fact was not "
                          "compared, so this round claims nothing about it" % sid)
        return emit(out, args.json)
    if not hasattr(mod, "probe"):
        out.update(verdict=CANNOT_COMPARE,
                   detail="parity/%s.py defines no probe(agent, ui)" % sid)
        return emit(out, args.json)

    out["parity_fact"] = (getattr(mod, "PARITY", "") or "").strip() or None
    if agent is None:
        out.update(verdict=CANNOT_COMPARE, detail="the agent channel produced no result.json")
        return emit(out, args.json)
    if ui is None:
        out.update(verdict=CANNOT_COMPARE, detail="the UI channel produced no ui-channel.json")
        return emit(out, args.json)
    if not ui.get("ok"):
        # The UI half never rendered (login screen, timeout). Comparing against it
        # would manufacture a mismatch out of a channel that reported nothing.
        out.update(verdict=CANNOT_COMPARE,
                   detail="the UI channel reported no page: %s" % (ui.get("detail") or "")[:200])
        return emit(out, args.json)

    try:
        av, uv, detail = mod.probe(agent, ui)
    except Exception as exc:                       # a probe that crashed tells us nothing
        out.update(verdict=CANNOT_COMPARE, detail="the probe raised: %s" % exc)
        return emit(out, args.json)

    out["agent_value"], out["ui_value"] = av, uv
    if av is None and uv is None:
        out.update(verdict=CANNOT_COMPARE, detail=detail or "neither channel produced the fact")
    elif av is None:
        out.update(verdict=CANNOT_COMPARE,
                   detail=(detail or "") + " — the agent channel did not produce it")
    elif uv is None:
        out.update(verdict=CANNOT_COMPARE,
                   detail=(detail or "") + " — the UI channel did not produce it")
    elif same(av, uv):
        out.update(verdict=PARITY_OK, detail=detail or "both channels state the same fact")
    else:
        out.update(verdict=PARITY_MISMATCH,
                   detail=(detail or "") + " — agent says %r, the UI renders %r" % (av, uv))

    # Ground truth: a third opinion. Only when the probe offers one.
    #
    # The case worth naming is the one cross-channel comparison cannot catch: both
    # channels say the same thing AND the database says something else. That is not a
    # parity finding (the channels do agree) and not a plain FAIL (neither channel is
    # demonstrably the liar) — it is the product, a channel and the store disagreeing,
    # so it gets its own verdict and its own line in the scorecard.
    if hasattr(mod, "truth") and out["verdict"] in (PARITY_OK, PARITY_MISMATCH):
        try:
            tv, tdetail = mod.truth(agent, ui)
        except Exception as exc:
            out["truth_detail"] = "the truth probe raised: %s" % exc
        else:
            out["truth_value"] = tv
            out["truth_detail"] = tdetail
            if tv is not None and out.get("agent_value") is not None and not same(out["agent_value"], tv):
                out["truth_disagrees_with_agent"] = True
                if out["verdict"] == PARITY_OK:
                    # Both channels agreed, and both are wrong.
                    out["verdict"] = TRUTH_MISMATCH
                    out["detail"] = ("the two channels agree (%r) and ground truth "
                                     "disagrees: %s" % (out["agent_value"], tdetail))

    return emit(out, args.json)


def same(a, b):
    """Equality that is not fooled by 1999 vs '19.99' vs '$19.99' vs Decimal('19.99').

    The comparison is the point of the file, so it is deliberately strict about type
    but forgiving about *formatting*: money is compared as a number in cents, dates as
    an ISO day, everything else after whitespace normalisation. A probe hands over
    values already in the shape it wants; this only rescues the trivial mismatch of
    `1999` vs `19.99` so a real disagreement is not buried under a formatting one.
    """
    if a == b:
        return True
    if a is None or b is None:
        return False
    # A probe may return a structured fact (a path AND a number that both have to
    # match). Compare it key by key with the same tolerance, rather than stringifying
    # a dict and comparing reprs — reprs differ over key order and hide nothing useful.
    if isinstance(a, dict) and isinstance(b, dict):
        keys = set(a) | set(b)
        return all(same(a.get(k), b.get(k)) for k in keys)
    if isinstance(a, (list, tuple)) and isinstance(b, (list, tuple)):
        return len(a) == len(b) and all(same(x, y) for x, y in zip(a, b))
    try:
        fa, fb = float(str(a).replace("$", "").replace(",", "")), \
                 float(str(b).replace("$", "").replace(",", ""))
        if fa == fb:
            return True
        # 1999 (cents) vs 19.99 (dollars) — the same money, stated in two units.
        if abs(fa - fb * 100) < 0.005 or abs(fa * 100 - fb) < 0.5:
            return True
    except (TypeError, ValueError):
        pass
    return " ".join(str(a).split()).lower() == " ".join(str(b).split()).lower()


def emit(out, as_json):
    if out["verdict"] != PARITY_OK:
        print(json.dumps(out, indent=1))
        return 1 if out["verdict"] in (PARITY_MISMATCH, TRUTH_MISMATCH) else 2
    print(json.dumps(out))
    return 0


if __name__ == "__main__":
    sys.exit(main())
