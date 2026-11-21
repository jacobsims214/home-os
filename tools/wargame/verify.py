#!/usr/bin/env python3
"""verify.py — check the war-game environment against verify.yaml and print a pass/fail table.

usage:
  ./verify.py                 human table, exit 0 only if every check passes
  ./verify.py --json          the same result as JSON (for a script or a ticket)
  ./verify.py --only <id>     run just one check (repeatable, prefix match)
  ./verify.py --list          list the checks and exit

No credential is ever printed. Every password and token is read from the Secret
verify.yaml names, used in memory, and dropped; the table shows whether it
worked, never what it was. That is deliberate — see README "Never print a
secret" — and it is why a failing check names the Secret to look at instead of
showing you the value.

Standard library only (no PyYAML, no requests, no jq): it parses the small YAML
subset verify.yaml uses, and shells out to `kubectl` and `curl`. Run it from
anywhere; it finds its own verify.yaml next to itself.
"""

import argparse
import base64
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
SPEC = os.path.join(HERE, "verify.yaml")

# Seconds for any single network/subprocess operation. Nothing here should take
# longer; a hang is a FAIL with a timeout message, never a stalled run.
TIMEOUT = 20

# How much of a response body to read. A tools/call result for a whole household
# is comfortably inside this; the cap only stops a runaway endpoint filling memory.
MAX_BODY = 4 * 1024 * 1024


# --------------------------------------------------------------------------- #
# A very small YAML subset reader. Not a YAML implementation: block maps,
# block sequences, `- key: value` items, plain scalars, full-line comments and
# ${VAR:-default} expansion. Anything outside that is rejected, loudly.
# --------------------------------------------------------------------------- #

ENV_RE = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}")
KV_RE = re.compile(r"^([A-Za-z0-9_.\-]+):(?:\s+(.*))?$")


class SpecError(Exception):
    pass


def expand(value):
    if not isinstance(value, str):
        return value

    def sub(m):
        name, default = m.group(1), m.group(2)
        got = os.environ.get(name)
        if got:
            return got
        if default is not None:
            return default
        raise SpecError(f"verify.yaml uses ${{{name}}} but {name} is not set and has no default")

    return ENV_RE.sub(sub, value)


def scalar(text):
    text = text.strip()
    if text == "":
        return ""
    if text[0] in "{[":
        raise SpecError(f"flow collections are not supported in verify.yaml: {text!r}")
    if text[0] in "\"'":
        quote = text[0]
        end = text.find(quote, 1)
        if end < 0:
            raise SpecError(f"unterminated quoted scalar: {text!r}")
        return expand(text[1:end])
    return expand(text)


def tokenize(source):
    lines = []
    for raw in source.splitlines():
        stripped = raw.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if "\t" in raw[: len(raw) - len(raw.lstrip())]:
            raise SpecError("verify.yaml is indented with tabs; use spaces")
        indent = len(raw) - len(raw.lstrip(" "))
        lines.append((indent, stripped))
    return lines


def split_kv(content):
    m = KV_RE.match(content)
    if not m:
        raise SpecError(f"expected 'key: value' in verify.yaml, found {content!r}")
    return m.group(1), (m.group(2) or "")


def parse_block(lines, i, indent):
    if lines[i][1].startswith("- "):
        return parse_seq(lines, i, indent)
    return parse_map(lines, i, indent)


def parse_map(lines, i, indent, into=None):
    out = into if into is not None else {}
    while i < len(lines) and lines[i][0] == indent and not lines[i][1].startswith("- "):
        key, value = split_kv(lines[i][1])
        i += 1
        if value == "":
            if i < len(lines) and lines[i][0] > indent:
                out[key], i = parse_block(lines, i, lines[i][0])
            else:
                out[key] = None
        else:
            out[key] = scalar(value)
    return out, i


def parse_seq(lines, i, indent):
    items = []
    while i < len(lines) and lines[i][0] == indent and lines[i][1].startswith("- "):
        rest = lines[i][1][2:].strip()
        i += 1
        if rest == "":
            if i < len(lines) and lines[i][0] > indent:
                item, i = parse_block(lines, i, lines[i][0])
            else:
                item = None
        elif KV_RE.match(rest):
            # an item that is a map: '- key: value' then more keys one level in
            item = {}
            key, value = split_kv(rest)
            if value == "":
                if i < len(lines) and lines[i][0] > indent:
                    item[key], i = parse_block(lines, i, lines[i][0])
                else:
                    item[key] = None
            else:
                item[key] = scalar(value)
            if i < len(lines) and lines[i][0] > indent and not lines[i][1].startswith("- "):
                parse_map(lines, i, lines[i][0], into=item)
                # advance past the keys we just consumed
                while i < len(lines) and lines[i][0] > indent and not lines[i][1].startswith("- "):
                    i += 1
        else:
            item = scalar(rest)
        items.append(item)
    return items, i


def load_spec(path):
    with open(path, "r", encoding="utf-8") as fh:
        lines = tokenize(fh.read())
    if not lines:
        raise SpecError(f"{path} is empty")
    doc, i = parse_block(lines, 0, lines[0][0])
    if i != len(lines):
        raise SpecError(f"{path}: unexpected indentation at line content {lines[i][1]!r}")
    if not isinstance(doc, dict):
        raise SpecError(f"{path}: top level must be a map")
    return doc


# --------------------------------------------------------------------------- #
# Probes
# --------------------------------------------------------------------------- #

class Result:
    def __init__(self, cid, kind, ok, detail, why=""):
        self.id, self.kind, self.ok, self.detail, self.why = cid, kind, ok, detail, why

    def as_dict(self):
        return {"id": self.id, "kind": self.kind, "ok": self.ok,
                "detail": self.detail, "why": self.why}


def run(cmd, env=None, stdin=None):
    try:
        proc = subprocess.run(cmd, capture_output=True, timeout=TIMEOUT, env=env, input=stdin)
    except FileNotFoundError:
        return 127, b"", f"{cmd[0]}: not found on PATH".encode()
    except subprocess.TimeoutExpired:
        return 124, b"", f"timed out after {TIMEOUT}s".encode()
    return proc.returncode, proc.stdout, proc.stderr


def kube_env(cluster):
    env = dict(os.environ)
    env["KUBECONFIG"] = expand(cluster.get("kubeconfig") or "")
    return env


def kube(cluster, args, stdin=None):
    """Run kubectl with the documented kubeconfig. Returns (rc, stdout text, stderr text)."""
    rc, out, err = run(["kubectl", *args], env=kube_env(cluster), stdin=stdin)
    return rc, out.decode("utf-8", "replace"), err.decode("utf-8", "replace").strip()


def secret_value(cluster, spec):
    """Read one key out of a Secret. Returns (value, error). Never logs the value."""
    if not isinstance(spec, dict):
        return None, "secret reference is malformed in verify.yaml"
    ns, name, key = spec.get("namespace"), spec.get("name"), spec.get("key")
    if not (ns and name and key):
        return None, f"secret reference needs namespace/name/key, got {spec}"
    rc, out, err = kube(cluster, ["-n", ns, "get", "secret", name,
                                  "-o", "jsonpath={.data." + key + "}"])
    if rc != 0:
        return None, f"cannot read secret {ns}/{name}: {err.splitlines()[0] if err else rc}"
    if not out.strip():
        return None, f"secret {ns}/{name} has no key {key!r}"
    try:
        return base64.b64decode(out.strip()).decode("utf-8", "replace"), None
    except Exception as exc:  # noqa: BLE001 - any decode failure is one message
        return None, f"secret {ns}/{name} key {key!r} is not base64: {exc}"


def http_get(url, follow=False):
    req = urllib.request.Request(url, headers={"User-Agent": "home-os-wargame-verify"})
    opener = urllib.request.build_opener(
        urllib.request.HTTPRedirectHandler() if follow else urllib.request.HTTPHandler())
    try:
        with opener.open(req, timeout=TIMEOUT) as resp:
            return resp.status, ""
    except urllib.error.HTTPError as exc:
        return exc.code, ""
    except urllib.error.URLError as exc:
        return None, str(exc.reason)
    except Exception as exc:  # noqa: BLE001
        return None, str(exc)


def post_json(url, payload, headers=None):
    body = json.dumps(payload).encode()
    hdrs = {"Content-Type": "application/json", "User-Agent": "home-os-wargame-verify"}
    hdrs.update(headers or {})
    req = urllib.request.Request(url, data=body, headers=hdrs, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:
            return resp.status, resp.read(MAX_BODY)
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read(MAX_BODY)
    except urllib.error.URLError as exc:
        return None, str(exc.reason).encode()
    except Exception as exc:  # noqa: BLE001
        return None, str(exc).encode()


def persona(spec, name):
    for p in spec.get("personas") or []:
        if p.get("name") == name:
            return p
    raise SpecError(f"verify.yaml: no persona named {name!r}")


def moltis_base(cluster, persona_spec):
    """The instance's reachable base URL: its LoadBalancer IP and port, else clusterIP."""
    svc = persona_spec.get("moltis_service")
    rc, out, err = kube(cluster, ["-n", "moltis", "get", "svc", svc, "-o",
                                  "jsonpath={.status.loadBalancer.ingress[0].ip}|{.spec.clusterIP}|{.spec.ports[0].port}"])
    if rc != 0:
        return None, f"cannot read service moltis/{svc}: {err.splitlines()[0] if err else rc}"
    parts = out.split("|")
    ip = parts[0] or (parts[1] if len(parts) > 1 else "")
    port = parts[2] if len(parts) > 2 else "13131"
    if not ip:
        return None, f"service moltis/{svc} has no IP yet"
    return f"http://{ip}:{port}", ""


def check_nodes(cluster, check):
    expect = int(expand(str(check.get("expect", cluster.get("expect_nodes_ready", 5)))) or 5)
    rc, out, err = kube(cluster, ["get", "nodes", "--no-headers"])
    if rc != 0:
        return False, f"kubectl get nodes failed: {err.splitlines()[0] if err else rc}"
    names, ready = [], 0
    for line in out.splitlines():
        cols = line.split()
        if len(cols) < 2:
            continue
        names.append(cols[0])
        if cols[1] == "Ready":
            ready += 1
    ok = ready == expect
    detail = f"{ready}/{len(names)} nodes Ready (want {expect})"
    if not ok:
        not_ready = [l.split()[0] for l in out.splitlines() if len(l.split()) > 1 and l.split()[1] != "Ready"]
        if not_ready:
            detail += "; not Ready: " + ", ".join(not_ready[:6])
    return ok, detail


def check_http(check):
    url = expand(check.get("url") or "")
    want = int(expand(str(check.get("expect_status", 200))))
    follow = str(check.get("follow_redirects", "false")).lower() in ("true", "yes", "1")
    status, err = http_get(url, follow=follow)
    if status is None:
        return False, f"GET {url} failed: {err}"
    return status == want, f"GET {url} -> {status} (want {want})"


def check_mcp(spec, check):
    mcp = spec.get("mcp") or {}
    url = expand(mcp.get("url") or "")
    tool = expand(mcp.get("probe_tool") or "family_overview")
    p = persona(spec, check.get("persona"))
    token, err = secret_value(spec.get("cluster") or {}, p.get("token_secret"))
    if err:
        return False, err
    payload = {"jsonrpc": "2.0", "id": 1, "method": "tools/call",
               "params": {"name": tool, "arguments": {}}}
    status, body = post_json(url, payload, {"Authorization": f"Bearer {token}",
                                            "Accept": "application/json, text/event-stream"})
    if status is None:
        return False, f"POST {url} failed: {body.decode('utf-8', 'replace')[:160]}"
    if status != 200:
        return False, (f"POST {url} -> {status}: the agent token for {p.get('name')} was refused "
                       f"(401 here is the token, not the transport)")
    try:
        doc = json.loads(body.decode("utf-8", "replace"))
    except ValueError:
        return False, f"POST {url} -> 200 but the body is not JSON: {body[:120]!r}"
    if "error" in doc:
        return False, f"{tool} returned a JSON-RPC error: {str(doc['error'])[:160]}"
    result = doc.get("result") or {}
    if result.get("isError"):
        return False, f"{tool} reported isError for persona {p.get('name')}"
    if not result.get("content"):
        return False, f"{tool} returned no content for persona {p.get('name')}"
    summary = ""
    try:
        text = result["content"][0].get("text") or ""
        summary = (json.loads(text).get("summary") or "")[:70]
    except Exception:  # noqa: BLE001 - a non-JSON tool body is fine, not a failure
        pass
    detail = f"{tool} ok as {p.get('name')}" + (f" — {summary}" if summary else "")
    return True, detail


def check_moltis(spec, check):
    cluster = spec.get("cluster") or {}
    p = persona(spec, check.get("persona"))
    want = int(expand(str(check.get("expect_status", 200))))
    base, err = moltis_base(cluster, p)
    if err:
        return False, err
    password, err = secret_value(cluster, p.get("moltis_password_secret"))
    if err:
        return False, err
    status, _ = post_json(base + "/api/auth/login", {"password": password},
                          {"Origin": base})
    if status is None:
        return False, f"{base} did not answer (instance down, or not reachable from here)"
    return status == want, f"POST {base}/api/auth/login -> {status} (want {want})"


# --------------------------------------------------------------------------- #
# Main
# --------------------------------------------------------------------------- #

KINDS = {"nodes_ready": lambda spec, c: check_nodes(spec.get("cluster") or {}, c),
         "http": lambda spec, c: check_http(c),
         "mcp_tool": lambda spec, c: check_mcp(spec, c),
         "moltis_login": lambda spec, c: check_moltis(spec, c)}


def main(argv=None):
    ap = argparse.ArgumentParser(description="Check the war-game environment declared in verify.yaml")
    ap.add_argument("--json", action="store_true", help="print JSON instead of a table")
    ap.add_argument("--only", action="append", default=[], metavar="ID",
                    help="run only checks whose id contains ID (repeatable)")
    ap.add_argument("--list", action="store_true", help="list the checks and exit")
    ap.add_argument("--spec", default=SPEC, help="path to verify.yaml")
    args = ap.parse_args(argv)

    try:
        spec = load_spec(args.spec)
    except (OSError, SpecError) as exc:
        print(f"verify: {exc}", file=sys.stderr)
        return 2

    checks = spec.get("checks") or []
    if args.list:
        for c in checks:
            print(f"{c.get('id')}\t{c.get('kind')}")
        return 0
    if args.only:
        checks = [c for c in checks if any(o in str(c.get("id")) for o in args.only)]
        if not checks:
            print(f"verify: no check matches {args.only}", file=sys.stderr)
            return 2

    results = []
    for c in checks:
        cid, kind = str(c.get("id")), str(c.get("kind"))
        why = expand(c.get("why") or "")
        prober = KINDS.get(kind)
        if prober is None:
            results.append(Result(cid, kind, False, f"unknown check kind {kind!r}", why))
            continue
        try:
            ok, detail = prober(spec, c)
        except SpecError as exc:
            ok, detail = False, str(exc)
        except Exception as exc:  # noqa: BLE001 - a probe crash is a failed check, not a crashed run
            ok, detail = False, f"{type(exc).__name__}: {exc}"
        results.append(Result(cid, kind, ok, detail, why))

    if args.json:
        print(json.dumps({
            "ok": all(r.ok for r in results),
            "control_plane": expand(str((spec.get("cluster") or {}).get("control_plane") or "")),
            "checks": [r.as_dict() for r in results],
        }, indent=2))
    else:
        width = max([len(r.id) for r in results] + [7])
        print()
        print(f"  {'CHECK'.ljust(width)}  KIND            RESULT  DETAIL")
        print(f"  {'-' * width}  --------------  ------  {'-' * 40}")
        for r in results:
            print(f"  {r.id.ljust(width)}  {r.kind.ljust(14)}  {'PASS  ' if r.ok else 'FAIL  '}  {r.detail}")
            if not r.ok and r.why:
                print(f"  {' ' * width}  {'':14}  {'':6}  -> {r.why}")
        failed = [r for r in results if not r.ok]
        print()
        print(f"  {len(results) - len(failed)} passed, {len(failed)} failed")
        print()
    return 0 if all(r.ok for r in results) else 1


if __name__ == "__main__":
    sys.exit(main())
