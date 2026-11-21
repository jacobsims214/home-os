#!/usr/bin/env python3
"""Load and validate the war-game scenario corpus in `tools/wargame/scenarios/`.

One YAML file per scenario. This module is the only reader: rounds (R4 onward) run
`--check` before play and `--report` after, so "which scenarios exist" and "how many
per persona" are answered from data rather than from prose in a ticket brief.

    ./scenarios_load.py                 # validate + summary report
    ./scenarios_load.py --check         # validate only; exit 1 on any error
    ./scenarios_load.py --report        # the deliverable count (persona/surface/round)
    ./scenarios_load.py --ids           # every id, one per line (feed a driver)
    ./scenarios_load.py --json          # the whole corpus as JSON
    ./scenarios_load.py --persona amber # only that persona (with --report/--json/--ids)
    ./scenarios_load.py --round r3      # only scenarios whose origin round is r3
    ./scenarios_load.py --selftest      # parser + validator tests, no corpus needed

Exit status: 0 ok, 1 the corpus is invalid, 2 usage or a corpus directory problem.

**No PyYAML.** `tools/wargame` runs on a throwaway pod with `bash`, `curl` and `python3`
(standard library only — see README), so this file carries a small, strict YAML-subset
parser instead. It accepts exactly what the corpus uses — nested maps, block lists,
`|` literal blocks, single/double quoted and bare scalars, comments — and raises on
anything else rather than guessing. A scenario file that needs more YAML than this is a
signal to keep the format simple, not to add a dependency.
"""

import argparse
import glob
import json
import os
import re
import sys

# ---------------------------------------------------------------------------
# YAML subset parser
# ---------------------------------------------------------------------------


class YamlError(Exception):
    pass


def _strip_comment(line):
    """Remove a trailing comment, respecting quotes."""
    out = []
    quote = None
    i = 0
    while i < len(line):
        c = line[i]
        if quote:
            out.append(c)
            if c == "\\" and quote == '"' and i + 1 < len(line):
                out.append(line[i + 1])
                i += 2
                continue
            if c == quote:
                quote = None
        else:
            if c in "\"'":
                quote = c
                out.append(c)
            elif c == "#" and (i == 0 or line[i - 1] in " \t"):
                break
            else:
                out.append(c)
        i += 1
    return "".join(out).rstrip()


def _unquote(text):
    text = text.strip()
    if len(text) >= 2 and text[0] == '"' and text[-1] == '"':
        body = text[1:-1]
        out = []
        i = 0
        while i < len(body):
            if body[i] == "\\" and i + 1 < len(body):
                nxt = body[i + 1]
                out.append({"n": "\n", "t": "\t", '"': '"', "\\": "\\"}.get(nxt, "\\" + nxt))
                i += 2
                continue
            out.append(body[i])
            i += 1
        return "".join(out)
    if len(text) >= 2 and text[0] == "'" and text[-1] == "'":
        return text[1:-1].replace("''", "'")
    return text


def _scalar(text, where):
    text = text.strip()
    if text == "":
        raise YamlError("%s: empty value" % where)
    if text[0] in "\"'":
        if len(text) < 2 or text[-1] != text[0]:
            raise YamlError("%s: unterminated quote" % where)
        return _unquote(text)
    if text[0] in "[{":
        raise YamlError("%s: flow collections are not supported; use block style" % where)
    if text[0] in "&*!":
        raise YamlError("%s: unsupported YAML construct %r" % (where, text[0]))
    if text in ("|", ">", "|-", ">-"):
        raise YamlError("%s: block scalar marker must follow 'key:'" % where)
    return text


def _split_key(text, where):
    """Split 'key: value' at the first unquoted ': ' or trailing ':'."""
    quote = None
    for i, c in enumerate(text):
        if quote:
            if c == quote:
                quote = None
            continue
        if c in "\"'":
            quote = c
            continue
        if c == ":" and (i + 1 == len(text) or text[i + 1] == " "):
            key = text[:i].strip()
            if not key:
                raise YamlError("%s: empty key" % where)
            return _unquote(key), text[i + 1:].strip()
    return None, None


def parse_yaml(text, name="<string>"):
    """Parse the corpus's YAML subset into dicts, lists and strings."""
    raw = text.split("\n")
    # (indent, stripped, line number) for every non-blank, non-comment line.
    lines = []
    for n, line in enumerate(raw, 1):
        if "\t" in line[: len(line) - len(line.lstrip())]:
            raise YamlError("%s:%d: tab indentation" % (name, n))
        stripped = _strip_comment(line)
        if not stripped.strip():
            continue
        if stripped.strip() in ("---", "..."):
            continue
        lines.append([len(stripped) - len(stripped.lstrip()), stripped.strip(), n])

    pos = [0]

    def where(idx):
        return "%s:%d" % (name, lines[idx][2])

    def parse_block(indent):
        if pos[0] >= len(lines):
            return None
        if lines[pos[0]][1].startswith("- "):
            return parse_list(indent)
        return parse_map(indent)

    def parse_list(indent):
        items = []
        while pos[0] < len(lines):
            ind, body, _n = lines[pos[0]]
            if ind < indent:
                break
            if ind > indent:
                raise YamlError("%s: unexpected indentation" % where(pos[0]))
            if not body.startswith("- "):
                break
            rest = body[2:].strip()
            pos[0] += 1
            if not rest:
                # A nested block under the dash.
                items.append(parse_block(indent + 2) if pos[0] < len(lines)
                             and lines[pos[0]][0] > indent else None)
                continue
            key, val = _split_key(rest, where(pos[0] - 1))
            if key is None:
                items.append(_scalar(rest, where(pos[0] - 1)))
                continue
            # A map item whose first key is on the dash line: "- round: r1".
            item = {}
            if val in ("|", "|-"):
                item[key] = _block_scalar(indent + 2)
            elif val == "":
                pos[0] -= 1
                lines[pos[0]][1] = "  " + lines[pos[0]][1]
                lines[pos[0]][0] += 2
                pos[0] += 1
                item[key] = parse_block(indent + 2)
            else:
                item[key] = _scalar(val, where(pos[0] - 1))
            while pos[0] < len(lines):
                ind2, body2, _n2 = lines[pos[0]]
                if ind2 <= indent or body2.startswith("- "):
                    break
                k2, v2 = _split_key(body2, where(pos[0]))
                if k2 is None:
                    raise YamlError("%s: expected 'key: value'" % where(pos[0]))
                pos[0] += 1
                if v2 in ("|", "|-"):
                    item[k2] = _block_scalar(ind2 + 2)
                elif v2 == "":
                    item[k2] = parse_block(ind2 + 2) if pos[0] < len(lines) \
                        and lines[pos[0]][0] > ind2 else {}
                else:
                    item[k2] = _scalar(v2, where(pos[0] - 1))
            items.append(item)
        return items

    def _block_scalar(indent):
        chunks = []
        start = pos[0]
        while pos[0] < len(lines) and lines[pos[0]][0] >= indent:
            chunks.append(lines[pos[0]][1])
            pos[0] += 1
        if not chunks:
            raise YamlError("%s: empty block scalar" % where(start))
        # Literal block scalar with clip chomping, as YAML defines it: line breaks are
        # kept, and exactly one trailing newline is added.
        return "\n".join(chunks) + "\n"

    def parse_map(indent):
        result = {}
        while pos[0] < len(lines):
            ind, body, _n = lines[pos[0]]
            if ind < indent:
                break
            if ind > indent:
                raise YamlError("%s: unexpected indentation" % where(pos[0]))
            if body.startswith("- "):
                break
            key, val = _split_key(body, where(pos[0]))
            if key is None:
                raise YamlError("%s: expected 'key: value'" % where(pos[0]))
            if key in result:
                raise YamlError("%s: duplicate key %r" % (where(pos[0]), key))
            pos[0] += 1
            if val in ("|", "|-"):
                result[key] = _block_scalar(indent + 2)
            elif val == "":
                nxt = pos[0]
                if nxt < len(lines) and lines[nxt][0] > indent:
                    result[key] = parse_block(lines[nxt][0])
                elif nxt < len(lines) and lines[nxt][0] == indent and lines[nxt][1].startswith("- "):
                    result[key] = parse_list(indent)
                else:
                    result[key] = {}
            else:
                result[key] = _scalar(val, where(pos[0] - 1))
        return result

    if not lines:
        raise YamlError("%s: empty document" % name)
    doc = parse_block(lines[0][0])
    if pos[0] != len(lines):
        raise YamlError("%s: trailing content at line %d" % (name, lines[pos[0]][2]))
    if not isinstance(doc, dict):
        raise YamlError("%s: top level must be a mapping" % name)
    return doc


# ---------------------------------------------------------------------------
# Schema
# ---------------------------------------------------------------------------

PERSONAS = ("amber", "jacob", "designer")
VERDICTS = ("pass", "partial", "fail", "untested")
ROUNDS = ("r1", "r2", "r3")
ID_RE = re.compile(r"^[a-z][a-z0-9]*(-[a-z0-9]+)*$")

REQUIRED = ("id", "persona", "surface", "intent", "steps", "assertion", "tags", "origin")
ORIGIN_REQUIRED = ("round", "source", "ref", "ticket", "date", "verdict")
OPTIONAL = ("notes",)
TAG_MIN, TAG_MAX = 3, 6


def validate_scenario(doc, filename):
    """Return a list of error strings (empty means valid)."""
    errs = []
    for key in REQUIRED:
        if key not in doc:
            errs.append("missing field %r" % key)
    for key in doc:
        if key not in REQUIRED and key not in OPTIONAL:
            errs.append("unknown field %r (allowed: %s)"
                        % (key, ", ".join(REQUIRED + OPTIONAL)))
    if errs:
        return errs

    sid = doc["id"]
    if not isinstance(sid, str) or not sid:
        errs.append("id must be a non-empty string")
    else:
        if not ID_RE.match(sid):
            errs.append("id %r must be lowercase kebab-case (e.g. amber-store-checkoff)" % sid)
        expected = os.path.basename(filename)[: -len(".yaml")]
        if sid != expected:
            errs.append("id %r does not match filename %r" % (sid, expected))

    if doc["persona"] not in PERSONAS:
        errs.append("persona %r must be one of %s" % (doc["persona"], ", ".join(PERSONAS)))
    for field in ("surface", "intent", "assertion"):
        if not isinstance(doc[field], str) or not doc[field].strip():
            errs.append("%s must be a non-empty string" % field)
    if isinstance(doc["surface"], str) and re.search(r"[\s]", doc["surface"].strip()):
        errs.append("surface %r must be a single token (no spaces)" % doc["surface"])

    steps = doc["steps"]
    if not isinstance(steps, list) or not steps:
        errs.append("steps must be a non-empty list")
    else:
        for i, step in enumerate(steps, 1):
            if not isinstance(step, str) or not step.strip():
                errs.append("step %d must be a non-empty string" % i)

    tags = doc["tags"]
    if not isinstance(tags, list):
        errs.append("tags must be a list")
    elif not TAG_MIN <= len(tags) <= TAG_MAX:
        errs.append("tags must have %d-%d entries, has %d" % (TAG_MIN, TAG_MAX, len(tags)))
    else:
        for tag in tags:
            if not isinstance(tag, str) or not tag.strip() or " " in tag:
                errs.append("tag %r must be a single non-empty token" % (tag,))
        if len(set(tags)) != len(tags):
            errs.append("tags contain duplicates")

    origin = doc["origin"]
    if not isinstance(origin, dict):
        errs.append("origin must be a mapping")
    else:
        for key in ORIGIN_REQUIRED:
            if key not in origin:
                errs.append("origin missing %r" % key)
        if "round" in origin and origin["round"] not in ROUNDS:
            errs.append("origin.round %r must be one of %s"
                        % (origin["round"], ", ".join(ROUNDS)))
        if "verdict" in origin and origin["verdict"] not in VERDICTS:
            errs.append("origin.verdict %r must be one of %s"
                        % (origin["verdict"], ", ".join(VERDICTS)))
        if "source" in origin and not str(origin["source"]).startswith("discoveries/"):
            errs.append("origin.source %r must point into discoveries/" % (origin["source"],))
        if "date" in origin and not re.match(r"^\d{4}-\d{2}-\d{2}$", str(origin["date"])):
            errs.append("origin.date %r must be YYYY-MM-DD" % (origin["date"],))

    reruns = origin.get("reruns", []) if isinstance(origin, dict) else []
    if reruns and not isinstance(reruns, list):
        errs.append("reruns must be a list")
    elif isinstance(reruns, list):
        for i, rerun in enumerate(reruns, 1):
            if not isinstance(rerun, dict):
                errs.append("reruns[%d] must be a mapping" % i)
                continue
            for key in ("round", "source", "ref", "verdict"):
                if key not in rerun:
                    errs.append("reruns[%d] missing %r" % (i, key))
            if rerun.get("round") not in ROUNDS:
                errs.append("reruns[%d].round %r must be one of %s"
                            % (i, rerun.get("round"), ", ".join(ROUNDS)))
            if rerun.get("verdict") not in VERDICTS:
                errs.append("reruns[%d].verdict %r must be one of %s"
                            % (i, rerun.get("verdict"), ", ".join(VERDICTS)))
    return errs


# ---------------------------------------------------------------------------
# Corpus
# ---------------------------------------------------------------------------


class Corpus:
    def __init__(self):
        self.scenarios = []
        self.errors = []
        self.warnings = []
        self.files = 0

    def ok(self):
        return not self.errors


def load_corpus(directory):
    """Load every *.yaml in `directory`, validating each. Never raises on bad files."""
    corpus = Corpus()
    if not os.path.isdir(directory):
        raise SystemExit("scenarios_load: not a directory: %s" % directory)
    files = sorted(glob.glob(os.path.join(directory, "*.yaml")))
    if not files:
        raise SystemExit("scenarios_load: no *.yaml files in %s" % directory)

    corpus.files = len(files)
    seen_ids = {}
    seen_fingerprints = {}
    for path in files:
        base = os.path.basename(path)
        try:
            with open(path, encoding="utf-8") as fh:
                doc = parse_yaml(fh.read(), base)
        except YamlError as exc:
            corpus.errors.append("%s: %s" % (base, exc))
            continue
        errs = validate_scenario(doc, base)
        # Collect the id even from a file that fails validation: a duplicate id is worth
        # reporting alongside whatever else is wrong with it.
        sid = doc.get("id")
        duplicate = False
        if isinstance(sid, str) and sid:
            if sid in seen_ids:
                corpus.errors.append("%s: duplicate id %r (also in %s)"
                                     % (base, sid, seen_ids[sid]))
                duplicate = True
            else:
                seen_ids[sid] = base
        for err in errs:
            corpus.errors.append("%s: %s" % (base, err))
        if errs or duplicate:
            continue
        # A same-persona, same-surface, same-intent scenario is a duplicate even with
        # a different id: the corpus would be counting one scenario twice.
        fingerprint = (doc["persona"], doc["surface"], " ".join(doc["intent"].split()).lower())
        if fingerprint in seen_fingerprints:
            corpus.warnings.append("%s: same persona/surface/intent as %s (duplicate scenario?)"
                                   % (base, seen_fingerprints[fingerprint]))
        else:
            seen_fingerprints[fingerprint] = base
        corpus.scenarios.append(doc)

    # Cross-file: a scenario rerun in a later round should exist as its own origin once.
    for doc in corpus.scenarios:
        for rerun in doc["origin"].get("reruns", []):
            ref = "%s %s" % (rerun.get("round"), rerun.get("ref"))
            if not str(rerun.get("source", "")).startswith("discoveries/"):
                corpus.errors.append("%s.yaml: rerun %s source must point into discoveries/"
                                     % (doc["id"], ref))
    corpus.scenarios.sort(key=lambda d: (PERSONAS.index(d["persona"]), d["surface"], d["id"]))
    return corpus


# ---------------------------------------------------------------------------
# Reports
# ---------------------------------------------------------------------------


def _tally(scenarios, key):
    counts = {}
    for doc in scenarios:
        value = key(doc)
        counts[value] = counts.get(value, 0) + 1
    return counts


def report(corpus, out=sys.stdout):
    scenarios = corpus.scenarios
    print("War-game scenario corpus: %d scenarios" % len(scenarios), file=out)

    by_persona = _tally(scenarios, lambda d: d["persona"])
    print("\nBy persona:", file=out)
    for persona in PERSONAS:
        print("  %-9s %3d" % (persona, by_persona.get(persona, 0)), file=out)
    missing = [p for p in PERSONAS if not by_persona.get(p)]
    if missing:
        print("  MISSING PERSONA: %s" % ", ".join(missing), file=out)

    by_surface = _tally(scenarios, lambda d: d["surface"])
    print("\nBy surface:", file=out)
    for surface, count in sorted(by_surface.items(), key=lambda kv: (-kv[1], kv[0])):
        personas = sorted({d["persona"] for d in scenarios if d["surface"] == surface})
        print("  %-14s %3d  (%s)" % (surface, count, ", ".join(personas)), file=out)

    by_round = _tally(scenarios, lambda d: d["origin"]["round"])
    print("\nBy originating round:", file=out)
    for rnd in ROUNDS:
        print("  %-3s %3d" % (rnd, by_round.get(rnd, 0)), file=out)

    by_verdict = _tally(scenarios, lambda d: d["origin"]["verdict"])
    print("\nBy originating verdict (the R1-R3 result it records):", file=out)
    for verdict in VERDICTS:
        if by_verdict.get(verdict):
            print("  %-9s %3d" % (verdict, by_verdict.get(verdict, 0)), file=out)

    rerun_count = sum(len(d["origin"].get("reruns", [])) for d in scenarios)
    print("\nRe-tested across rounds: %d scenarios carry a rerun record (%d reruns)"
          % (sum(1 for d in scenarios if d["origin"].get("reruns")), rerun_count), file=out)

    latest = {}
    for doc in scenarios:
        state = doc["origin"]["verdict"]
        for rerun in doc["origin"].get("reruns", []):
            if ROUNDS.index(rerun["round"]) > ROUNDS.index(doc["origin"]["round"]):
                state = rerun["verdict"]
        latest[state] = latest.get(state, 0) + 1
    print("\nLatest known verdict per scenario:", file=out)
    for verdict in VERDICTS:
        if latest.get(verdict):
            print("  %-9s %3d" % (verdict, latest.get(verdict, 0)), file=out)


def check(corpus, out=sys.stdout):
    for warning in corpus.warnings:
        print("WARN  " + warning, file=out)
    for error in corpus.errors:
        print("ERROR " + error, file=out)
    if corpus.errors:
        print("\n%d error(s) across %d file(s); %d scenario(s) loaded"
              % (len(corpus.errors), corpus.files, len(corpus.scenarios)), file=out)
        return 1
    print("OK: %d scenarios valid, no duplicate or missing ids" % len(corpus.scenarios),
          file=out)
    return 0


# ---------------------------------------------------------------------------
# Selftest
# ---------------------------------------------------------------------------

GOOD = """\
# a scenario
id: amber-sample
persona: amber
surface: money
intent: |
  What do we owe?
steps:
  - open the Money page as amber
  - "read every amount on screen"
assertion: |
  Every amount matches bill_find.
tags:
  - money
  - ui
  - mobile
origin:
  round: r3
  source: discoveries/war-game-r3-amber.md
  ref: N1
  ticket: "#4596"
  date: 2026-11-20
  verdict: pass
  reruns:
    - round: r3
      source: discoveries/war-game-r3-jacob.md
      ref: 5297
      verdict: pass
      note: also checked at 390px
"""


def _expect(name, cond, failures):
    if not cond:
        failures.append(name)


def selftest(out=sys.stdout):
    failures = []

    doc = parse_yaml(GOOD, "sample.yaml")
    _expect("scalar id", doc["id"] == "amber-sample", failures)
    _expect("block intent", doc["intent"].strip() == "What do we owe?", failures)
    # A literal block keeps its line breaks and gets one trailing newline (YAML clip
    # chomping) - a real YAML parser must read the same value back.
    _expect("block scalar keeps breaks", doc["assertion"] == "Every amount matches bill_find.\n",
            failures)
    wrapped = parse_yaml("a: |\n  one two\n  three four\n", "t.yaml")["a"]
    _expect("block scalar wraps", wrapped == "one two\nthree four\n", failures)
    _expect("list of strings", doc["steps"][1] == "read every amount on screen", failures)
    _expect("quoted scalar keeps colon", doc["origin"]["ticket"] == "#4596", failures)
    _expect("rerun map in list", doc["origin"]["reruns"][0]["round"] == "r3", failures)
    _expect("rerun note", doc["origin"]["reruns"][0]["note"] == "also checked at 390px", failures)
    _expect("valid sample", validate_scenario(doc, "amber-sample.yaml") == [], failures)

    # Comments are stripped, but a '#' inside quotes survives.
    doc2 = parse_yaml('id: x\nsurface: money\ntags:\n  - "a #b"\n', "t.yaml")
    _expect("hash in quotes", doc2["tags"][0] == "a #b", failures)

    # Round trips through JSON.
    _expect("json serialisable", json.loads(json.dumps(doc))["id"] == "amber-sample", failures)

    def fails(name, text, needle, filename="bad.yaml"):
        try:
            parsed = parse_yaml(text, filename)
        except YamlError as exc:
            _expect(name + " (yaml)", needle in str(exc), failures)
            return
        errs = " | ".join(validate_scenario(parsed, filename))
        _expect("%s (schema): %s" % (name, errs), needle in errs, failures)

    fails("tab indent", "id: x\n\tsurface: money\n", "tab indentation")
    fails("duplicate key", "id: x\nid: y\n", "duplicate key")
    fails("flow list", "id: [a, b]\n", "flow collections")
    fails("missing field", GOOD.replace("assertion: |\n  Every amount matches bill_find.\n", ""),
          "missing field 'assertion'")
    fails("bad persona", GOOD.replace("persona: amber", "persona: bob"), "persona 'bob'")
    fails("bad id case", GOOD.replace("id: amber-sample", "id: Amber_Sample"),
          "kebab-case", "Amber_Sample.yaml")
    fails("id/filename mismatch", GOOD, "does not match filename", "other.yaml")
    fails("empty steps", GOOD.replace("steps:\n  - open the Money page as amber\n"
                                     "  - \"read every amount on screen\"\n", "steps: []\n"),
          "flow collections")
    fails("no tags", GOOD.replace("tags:\n  - money\n  - ui\n  - mobile\n", "tags: []\n"),
          "flow collections")
    fails("two tags", GOOD.replace("  - money\n  - ui\n  - mobile\n", "  - money\n  - ui\n"),
          "tags must have 3-6 entries")
    fails("bad round", GOOD.replace("  round: r3\n  source:", "  round: r9\n  source:"),
          "origin.round 'r9'")
    fails("bad verdict", GOOD.replace("  verdict: pass", "  verdict: great"),
          "origin.verdict 'great'")
    fails("unknown field", GOOD + "colour: blue\n", "unknown field 'colour'")
    fails("surface with space", GOOD.replace("surface: money", "surface: the money page"),
          "single token")
    fails("empty block", GOOD.replace("intent: |\n  What do we owe?\n", "intent: |\n"),
          "empty block scalar")

    # Duplicate and missing ids, through a real directory.
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        def write(name, body):
            with open(os.path.join(tmp, name), "w", encoding="utf-8") as fh:
                fh.write(body)

        write("amber-sample.yaml", GOOD)
        write("amber-copy.yaml", GOOD.replace("id: amber-sample", "id: amber-copy"))
        write("jacob-sample.yaml", GOOD.replace("id: amber-sample", "id: jacob-sample")
              .replace("persona: amber", "persona: jacob"))
        corpus = load_corpus(tmp)
        _expect("three valid", len(corpus.scenarios) == 3, failures)
        _expect("no errors in clean corpus", corpus.ok(), failures)
        _expect("duplicate scenario warned",
                any("same persona/surface/intent" in w for w in corpus.warnings), failures)

        # A genuine id collision: two different files claiming the same id. The filename
        # rule means the second also fails on its name, and both problems are reported.
        write("collide.yaml", GOOD.replace("id: amber-sample", "id: collide"))
        write("zeta.yaml", GOOD.replace("id: amber-sample", "id: collide"))
        corpus = load_corpus(tmp)
        _expect("duplicate id reported",
                any("duplicate id 'collide'" in e for e in corpus.errors), failures)
        _expect("duplicate id is an error", not corpus.ok(), failures)
        _expect("collision still counted once",
                sum(1 for d in corpus.scenarios if d["id"] == "collide") == 1, failures)
        os.remove(os.path.join(tmp, "collide.yaml"))
        os.remove(os.path.join(tmp, "zeta.yaml"))

        write("broken.yaml", "id: broken\npersona: amber\n")
        corpus = load_corpus(tmp)
        _expect("missing fields reported",
                any("missing field 'surface'" in e for e in corpus.errors), failures)

    # The real corpus, if there is one next to this file.
    here = os.path.dirname(os.path.abspath(__file__))
    real = os.path.join(here, "scenarios")
    if os.path.isdir(real) and glob.glob(os.path.join(real, "*.yaml")):
        corpus = load_corpus(real)
        _expect("corpus loads", corpus.ok(), failures)
        _expect("corpus covers three personas",
                {d["persona"] for d in corpus.scenarios} == set(PERSONAS), failures)
        _expect("corpus ids unique",
                len({d["id"] for d in corpus.scenarios}) == len(corpus.scenarios), failures)

    for failure in failures:
        print("FAIL " + failure, file=out)
    if failures:
        print("\nselftest: %d failure(s)" % len(failures), file=out)
        return 1
    print("selftest: all checks passed", file=out)
    return 0


# ---------------------------------------------------------------------------


def main(argv=None):
    here = os.path.dirname(os.path.abspath(__file__))
    ap = argparse.ArgumentParser(description="Load and validate the war-game scenario corpus.")
    ap.add_argument("--dir", default=os.path.join(here, "scenarios"),
                    help="corpus directory (default: <this file>/scenarios)")
    ap.add_argument("--check", action="store_true", help="validate only; exit 1 on any error")
    ap.add_argument("--report", action="store_true", help="counts by persona, surface and round")
    ap.add_argument("--ids", action="store_true", help="print every scenario id")
    ap.add_argument("--json", action="store_true", help="print the corpus as JSON")
    ap.add_argument("--persona", choices=PERSONAS, help="restrict to one persona")
    ap.add_argument("--round", choices=ROUNDS, help="restrict to scenarios from one round")
    ap.add_argument("--selftest", action="store_true", help="run parser/validator tests")
    args = ap.parse_args(argv)

    if args.selftest:
        return selftest()

    try:
        corpus = load_corpus(args.dir)
    except SystemExit as exc:
        print(exc, file=sys.stderr)
        return 2

    scenarios = corpus.scenarios
    if args.persona:
        scenarios = [d for d in scenarios if d["persona"] == args.persona]
    if args.round:
        scenarios = [d for d in scenarios if d["origin"]["round"] == args.round]
    filtered = Corpus()
    filtered.scenarios = scenarios
    filtered.errors = corpus.errors
    filtered.warnings = corpus.warnings
    filtered.files = corpus.files

    status = 0
    if args.ids:
        for doc in scenarios:
            print(doc["id"])
        return 1 if corpus.errors else 0
    if args.json:
        print(json.dumps({"scenarios": scenarios, "errors": corpus.errors,
                          "warnings": corpus.warnings}, indent=2))
        return 1 if corpus.errors else 0
    if args.check:
        return check(corpus)
    if args.report:
        report(filtered)
        print()
    if not (args.report or args.check):
        report(filtered)
        print()
    status = check(corpus)
    return status


if __name__ == "__main__":
    sys.exit(main())
