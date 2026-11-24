#!/usr/bin/env python3
"""Execute ONE war-game scenario and write its result + evidence as JSON.

    scenario_driver.py --id amber-account-menu --evidence-dir DIR [--corpus DIR]

`run.sh` calls this once per selected scenario. It is deliberately not the entry
point for a round: the round is `run.sh`, which gates on `up.sh` first.

## What "execute" means, and what a verdict is allowed to claim

A scenario's `steps` are prose written for a human player, and its `assertion` is
prose too. This driver does the part that is mechanical and refuses to invent the
rest (standing instructions §5 and §7: a test that cannot fail is worse than none,
and a check that cannot tell must not print green).

Each step is classified and, where a driver exists, actually run:

| step shape                          | driver            |
|-------------------------------------|-------------------|
| `mcpcall.py <tool> ['<json>']`      | real MCP call     |
| `api.py <METHOD> <path>`            | real REST call    |
| `chat.sh <persona> <session> ...`   | one moltis turn   |
| `open <url> ... at 390[px|x844]`    | headless Chrome   |
| anything else (a tap, a read)       | none — BLOCKED    |

The verdict is then:

* **FAIL** — a step's driver ran and errored, or the scenario's own assertion
  check (`checks/<id>.py`) ran and failed. A failure is a failure.
* **PASS** — every step ran cleanly *and* the scenario has an assertion check
  that ran and passed. Nothing is PASS without something that could have failed.
* **BLOCKED** — anything else, naming the first reason: a step with no driver, a
  missing credential, or (the common one) *no assertion check exists for this
  scenario, so its prose assertion was not judged*.

BLOCKED is the honest default and it is not a pass. Writing `checks/<id>.py` is
how a scenario graduates from BLOCKED to a real verdict; the scorecard's delta
then shows that graduation as progress rather than hiding it.

Standard library only: this runs from the throwaway pod that has bash, curl and
python3 (see README "Requirements").
"""

import argparse
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import scenarios_load  # noqa: E402  (the only corpus reader — never re-parse YAML here)

PASS, FAIL, BLOCKED = "PASS", "FAIL", "BLOCKED"

NET_TIMEOUT = int(os.environ.get("WARGAME_STEP_TIMEOUT", "90"))
UI_TIMEOUT = int(os.environ.get("WARGAME_UI_TIMEOUT", "45"))
VIEWPORT_W, VIEWPORT_H = 390, 844

# ---------------------------------------------------------------------------
# Step classification. Order matters: the ground-truth shapes are exact, the UI
# shape is the permissive fallback that only claims an `open <url>`.
# ---------------------------------------------------------------------------

RE_MCPCALL = re.compile(r"^\s*mcpcall\.py\b(.*)$")
RE_API = re.compile(r"^\s*api\.py\b(.*)$")
RE_CHAT = re.compile(r"^\s*chat\.sh\b(.*)$")
# `open <url>` — and ONLY when the token is actually a URL. The corpus also says
# "open the Bills list as amber at 390x844", which is a human instruction, not a
# page to fetch: treating "the" as a URL made Chrome search the web for it and
# the step reported a PASS. A step is a UI step only if there is a real URL.
RE_OPEN = re.compile(r"^\s*open\s+(https?://\S+)", re.IGNORECASE)


def classify(step):
    """Return (kind, payload). kind is one of mcpcall/api/chat/ui/none."""
    for rx, kind in ((RE_MCPCALL, "mcpcall"), (RE_API, "api"), (RE_CHAT, "chat")):
        m = rx.match(step)
        if m:
            return kind, m.group(1).strip()
    m = RE_OPEN.match(step)
    if m:
        return "ui", m.group(1).rstrip(".,")
    return "none", None


def run(argv, timeout, env=None, cwd=HERE):
    """Run a command, return (rc, stdout, stderr). Never raises on failure."""
    e = dict(os.environ)
    if env:
        e.update(env)
    try:
        p = subprocess.run(argv, capture_output=True, text=True, timeout=timeout,
                           env=e, cwd=cwd)
        return p.returncode, p.stdout, p.stderr
    except subprocess.TimeoutExpired:
        return 124, "", "timed out after %ss" % timeout
    except FileNotFoundError as exc:
        return 127, "", "not found: %s" % exc
    except OSError as exc:
        return 126, "", str(exc)


# ---------------------------------------------------------------------------
# Drivers
# ---------------------------------------------------------------------------

def mcpcall_args(payload):
    """Build argv for `mcpcall.py` from a step's tail.

    The corpus writes steps in prose — `mcpcall.py bill_find with query 'Maple
    Family Dental' as amber`, `mcpcall.py notification_find as amber once the
    bill-reminder tick has run`. `mcpcall.py` accepts a tool name and ONE JSON
    object, so this builds that object from what the prose actually says instead
    of forwarding prose as if it were JSON (which exits 2 and looks like a broken
    tool).

    Three rules, each learned from a real mis-parse:

    * `--text` is ALWAYS passed. Without it mcpcall.py prints the raw JSON-RPC
      envelope, whose payload is an escaped JSON *string* inside
      `content[0].text` — a naive `"ok":true` search then silently never matches.
    * split with `shlex`, not `str.split()`, or a quoted phrase with a space
      (`'Maple Family Dental'`) is torn apart and the query silently vanishes.
    * a query value becomes `{"query": ...}`; it is never passed as a bare
      argument, because mcpcall.py refuses that (correctly).
    """
    try:
        tokens = shlex.split(payload)
    except ValueError:
        tokens = payload.split()

    CONNECTORS = {"with", "query", "as", "the", "a", "an", "and", "then",
                  "once", "tick", "has", "run", "for", "to", "of", "on", "at"}

    tool, jsonarg, query, seen_query = None, None, None, False
    for t in tokens:
        low = t.lower()
        if low == "query":
            seen_query = True
            continue
        if t.startswith("{") or t.startswith("["):
            jsonarg = t
            continue
        if low in CONNECTORS or t in ("--text", "-list"):
            continue
        if tool is None:
            tool = t
            continue
        if seen_query and query is None:
            query = t
            seen_query = False
        # anything else is trailing prose and is dropped

    if jsonarg is None and query is not None:
        jsonarg = json.dumps({"query": query})

    argv = [sys.executable, os.path.join(HERE, "mcpcall.py"), "--text"]
    if tool:
        argv.append(tool)
    if jsonarg:
        argv.append(jsonarg)
    return argv


def parse_payload(out):
    """Return the tool's payload from mcpcall.py --text output, or None.

    `--text` prints the tool's text content, which is itself a JSON document.
    Anything unparseable is None and the caller treats that as a failure, never
    as an empty success (§7).
    """
    try:
        d = json.loads(out)
    except (json.JSONDecodeError, TypeError):
        return None
    if isinstance(d, dict) and "content" in d:
        # the envelope came through anyway; unwrap it
        for part in d.get("content") or []:
            if part.get("type") == "text":
                try:
                    return json.loads(part["text"])
                except (json.JSONDecodeError, KeyError, TypeError):
                    return None
    return d


def do_mcpcall(payload, persona):
    argv = mcpcall_args(payload)
    rc, out, err = run(argv, NET_TIMEOUT)
    blob = out or err
    detail = "%s as %s" % (payload.strip()[:90], persona)

    # mcpcall.py's exit codes: 0 ok, 1 the server REFUSED the call (a JSON-RPC
    # error or the tool's own isError), 2 transport/usage. The distinction is
    # load-bearing: a refused call is a finding about the product, while a
    # transport failure (429 rate limit, a dead port-forward) means the round
    # could not tell — and "could not tell" is BLOCKED, never FAIL (§7).
    if rc == 2:
        hint = "transport/usage failure"
        if "HTTP 429" in blob or "rate limit" in blob.lower():
            hint = ("rate limited by the MCP endpoint (HTTP 429) — this round could "
                    "not read ground truth for this step")
        elif "HTTP 401" in blob or "HTTP 403" in blob:
            hint = "the MCP refused this persona's token (HTTP 401/403)"
        return {"status": BLOCKED, "detail": "%s — %s" % (payload.strip()[:70], hint),
                "exit_code": rc, "output": blob[:4000]}

    doc = parse_payload(out) if rc == 0 else None
    # A ground-truth read is only PASS when it actually answered ok. An
    # unparseable reply is a FAIL: it must never be read as "nothing to report".
    ok = isinstance(doc, dict) and doc.get("ok") is True
    if rc == 0 and not ok:
        detail = "%s — reply was not a parseable ok payload" % payload.strip()[:80]
    return {
        "status": PASS if ok else FAIL,
        "detail": detail,
        "exit_code": rc,
        "output": blob[:4000],
    }


def do_api(payload):
    tokens = payload.split()
    if len(tokens) < 2:
        return {"status": BLOCKED, "detail": "api.py step names no METHOD and path",
                "exit_code": None, "output": ""}
    if not os.environ.get("HOMEOS_API_TOKEN"):
        # REST needs a Dex id_token; an agent token 401s by design. Absent one,
        # this step cannot run — say so rather than reporting a 401 as a defect.
        return {"status": BLOCKED,
                "detail": "HOMEOS_API_TOKEN (Dex id_token) not set; REST is not "
                          "agent-token reachable",
                "exit_code": None, "output": ""}
    argv = [sys.executable, os.path.join(HERE, "api.py")] + tokens[:2]
    if len(tokens) > 2 and tokens[2].startswith("{"):
        argv.append(tokens[2])
    rc, out, err = run(argv, NET_TIMEOUT)
    return {"status": PASS if rc == 0 else FAIL, "detail": payload.strip()[:90],
            "exit_code": rc, "output": (out or err)[:4000]}


def do_chat(payload, persona, evidence_dir):
    up = persona.upper()
    if not os.environ.get("WG_%s_URL" % up) or not os.environ.get("WG_%s_PASSWORD" % up):
        return {"status": BLOCKED,
                "detail": "WG_%s_URL / WG_%s_PASSWORD not set; no agent to talk to"
                          % (up, up),
                "exit_code": None, "output": ""}
    # A turn is sent with --file: the corpus prose contains `$` amounts, and the
    # README is explicit that argv would let a shell eat them.
    msg = re.sub(r"^\S+\s+\S+\s+", "", payload).strip() or payload.strip()
    stamp = int(time.time() * 1000)
    msgfile = os.path.join(evidence_dir, "turn-%d.txt" % stamp)
    with open(msgfile, "w", encoding="utf-8") as fh:
        fh.write(msg)
    tokens = payload.split()
    session = tokens[1] if len(tokens) > 1 else "wargame"
    argv = [os.path.join(HERE, "chat.sh"), persona, session, "--file", msgfile]
    rc, out, err = run(argv, NET_TIMEOUT)
    turnlog = os.path.join(evidence_dir, "turn-%d.log" % stamp)
    with open(turnlog, "w", encoding="utf-8") as fh:
        fh.write("$ %s\n--- stdout ---\n%s\n--- stderr ---\n%s\n"
                 % (" ".join(argv), out, err))
    return {"status": PASS if rc == 0 else FAIL, "detail": "agent turn (%s)" % session,
            "exit_code": rc, "output": (out or err)[:4000], "evidence": turnlog}


def find_chrome():
    for c in (os.environ.get("WARGAME_CHROME"),
              "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
              "/Applications/Chromium.app/Contents/MacOS/Chromium",
              shutil.which("google-chrome"), shutil.which("chromium"),
              shutil.which("chromium-browser")):
        if c and os.path.exists(c) and os.access(c, os.X_OK):
            return c
    return None


RE_LOGIN = re.compile(r'(name="login"|id="login"|>\s*Sign in\s*<|/dex/auth|Please log in)',
                      re.IGNORECASE)


def do_ui(url, persona, evidence_dir, idx):
    chrome = find_chrome()
    if not chrome:
        return {"status": BLOCKED,
                "detail": "no Chrome/Chromium on PATH; set WARGAME_CHROME",
                "exit_code": None, "output": ""}
    stem = "ui-%02d" % idx
    png = os.path.join(evidence_dir, stem + ".png")
    domfile = os.path.join(evidence_dir, stem + ".html")
    base = [chrome, "--headless=new", "--disable-gpu", "--no-sandbox",
            "--hide-scrollbars", "--disable-dev-shm-usage",
            "--window-size=%d,%d" % (VIEWPORT_W, VIEWPORT_H)]
    prof = os.environ.get("WARGAME_CHROME_PROFILE")
    if prof and os.path.isdir(prof):
        # A signed-in profile makes authenticated pages reachable; without one
        # every page is the login screen and the step is BLOCKED, not a PASS.
        base.append("--user-data-dir=" + prof)
    else:
        base.append("--user-data-dir=" + os.path.join(evidence_dir, "chrome-profile"))
    rc1, _, err1 = run(base + ["--screenshot=" + png, "--virtual-time-budget=8000", url],
                       UI_TIMEOUT)
    rc2, dom, err2 = run(base + ["--dump-dom", "--virtual-time-budget=8000", url],
                         UI_TIMEOUT)
    try:
        with open(domfile, "w", encoding="utf-8") as fh:
            fh.write(dom or "")
    except OSError:
        pass
    if rc1 != 0 and not os.path.exists(png):
        return {"status": FAIL, "detail": "chrome failed on %s" % url,
                "exit_code": rc1, "output": (err1 or err2)[:2000]}
    ev = [png] if os.path.exists(png) else []
    if RE_LOGIN.search(dom or ""):
        return {"status": BLOCKED,
                "detail": "%s rendered the login screen; no signed-in profile "
                          "(set WARGAME_CHROME_PROFILE)" % url,
                "exit_code": 0, "output": "login redirect detected", "evidence": ev,
                "screenshot": png}
    return {"status": PASS,
            "detail": "rendered %s at %dx%d" % (url, VIEWPORT_W, VIEWPORT_H),
            "exit_code": 0, "output": "", "evidence": ev, "screenshot": png}


# ---------------------------------------------------------------------------
# Assertion checks: checks/<scenario-id>.py
# ---------------------------------------------------------------------------

def run_check(sid, persona, evidence_dir):
    """Run the scenario's own assertion check, if one exists.

    The check is a python3 script invoked with the evidence dir. Its exit code is
    the same three-valued answer the driver gives: 0 PASS, 1 FAIL (a fact about
    the product), 2 BLOCKED (it could not read what it needed — a 429, a missing
    credential — so it is telling us nothing either way). Anything else is a
    FAIL: a check that crashed must never be read as a pass.
    """
    path = os.path.join(HERE, "checks", sid + ".py")
    if not os.path.exists(path):
        return None
    rc, out, err = run([sys.executable, path, "--evidence-dir", evidence_dir,
                        "--persona", persona], NET_TIMEOUT)
    lines = (out.strip().splitlines() or [err.strip() or "check exited %d" % rc])
    if rc == 0:
        status = PASS
    elif rc == 2:
        status = BLOCKED
    else:
        status = FAIL
    return {"status": status,
            "detail": lines[-1][:300],
            "exit_code": rc, "output": (out + err)[:4000], "path": path}


# ---------------------------------------------------------------------------

def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("--id", required=True)
    ap.add_argument("--corpus", default=os.path.join(HERE, "scenarios"))
    ap.add_argument("--evidence-dir", required=True)
    args = ap.parse_args(argv)

    corpus = scenarios_load.load_corpus(args.corpus)
    doc = next((s for s in corpus.scenarios if s["id"] == args.id), None)
    if doc is None:
        print(json.dumps({"id": args.id, "verdict": FAIL,
                          "reason": "scenario %r is not in the corpus" % args.id}))
        return 2

    evid = os.path.join(args.evidence_dir, args.id)
    os.makedirs(evid, exist_ok=True)
    persona = doc["persona"]

    step_results = []
    for idx, step in enumerate(doc["steps"], 1):
        kind, payload = classify(step)
        if kind == "mcpcall":
            r = do_mcpcall(payload, persona)
        elif kind == "api":
            r = do_api(payload)
        elif kind == "chat":
            r = do_chat(payload, persona, evid)
        elif kind == "ui":
            r = do_ui(payload, persona, evid, idx)
        else:
            r = {"status": BLOCKED,
                 "detail": "no driver for this step shape (a human tap/read); only "
                           "`open <url>`, mcpcall.py, api.py and chat.sh execute",
                 "exit_code": None, "output": ""}
        r.update({"n": idx, "step": step.strip()[:300], "kind": kind})
        step_results.append(r)

    check = run_check(args.id, persona, evid)

    # --- verdict -----------------------------------------------------------
    #
    # The assertion check is what judges a scenario. A step with no driver is
    # recorded (and counted) but does not by itself stop a PASS: the corpus's
    # steps are prose written for a human, and most of them are taps no driver
    # can perform. What must never happen is a PASS with nothing that could have
    # failed — so PASS requires a check that ran and passed.
    failed = [s for s in step_results if s["status"] == FAIL]
    blocked = [s for s in step_results if s["status"] == BLOCKED]
    unjudged = [s for s in blocked if s["kind"] == "none"]
    unrun = [s for s in blocked if s["kind"] != "none"]

    if failed:
        verdict = FAIL
        basis = "step %d failed: %s" % (failed[0]["n"], failed[0]["detail"])
    elif check is not None and check["status"] == FAIL:
        verdict = FAIL
        basis = "assertion check failed: %s" % check["detail"]
    elif check is not None and check["status"] == BLOCKED:
        verdict = BLOCKED
        basis = "assertion check could not tell: %s" % check["detail"]
    elif check is not None and check["status"] == PASS:
        verdict = PASS
        basis = "the assertion check passed"
        if unjudged:
            basis += " (the check reads ground truth; %d step(s) are human taps " \
                     "with no driver)" % len(unjudged)
    elif check is None:
        verdict = BLOCKED
        basis = ("no assertion check at checks/%s.py — the prose assertion was not "
                 "judged, so this round claims nothing about it" % args.id)
    else:
        # Unreachable by construction; kept so a future status cannot pass.
        verdict = BLOCKED
        basis = "assertion check produced no verdict"

    # A driver that was expected to run and could not is a BLOCKED in its own
    # right, even when the check passed: half the scenario was never exercised.
    if verdict == PASS and unrun:
        verdict = BLOCKED
        basis = ("the assertion check passed but step %d did not run: %s"
                 % (unrun[0]["n"], unrun[0]["detail"]))

    result = {
        "id": doc["id"],
        "persona": persona,
        "surface": doc["surface"],
        "tags": doc["tags"],
        "origin": doc["origin"],
        "verdict": verdict,
        "basis": basis,
        "has_check": check is not None,
        "unjudged_steps": len(unjudged),
        "steps": step_results,
        "check": check,
        "evidence_dir": evid,
        "screenshots": [s.get("screenshot") for s in step_results if s.get("screenshot")],
        "finished_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    with open(os.path.join(evid, "result.json"), "w", encoding="utf-8") as fh:
        json.dump(result, fh, indent=1)
    print(json.dumps({k: result[k] for k in ("id", "persona", "surface", "verdict", "basis")}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
