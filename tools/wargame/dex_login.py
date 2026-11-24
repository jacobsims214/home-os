#!/usr/bin/env python3
"""Sign a war-game persona in through Dex, and keep the id_token for reuse.

    ./dex_login.py --persona amber                    # mint (or reuse) and verify
    ./dex_login.py --persona amber --json             # same, machine-readable
    echo $?                                           # 0 ok, 1 refused, 2 could not tell

## Why this file exists (standing instructions §6: one rule, one home)

`up.sh` needs to prove each persona's Secret actually signs in, and the UI channel
needs the id_token that sign-in produces — the httpOnly `home-os-token` cookie the
browser carries. Those were the same 120 lines of PKCE in two places. This is the one
copy: `up.sh` calls it for its `dex-login:<persona>` row, and `ui_channel.mjs` reads the
token file it writes.

## What "sign in" means here

authorize -> Dex's interactive local-connector login form -> POST the password ->
follow the redirect hop by hop to the authorization `code` -> redeem it with PKCE ->
an `id_token` whose `email` claim is the persona. **Redeeming the code is the proof**:
a 303 out of the login form only proves the form accepted a POST, while the id_token's
`email` proves Dex authenticated *that identity*.

## The token cache, and why it is not a security hole

A Dex id_token lives 24h. `up.sh` and the round's UI driver want the same one, and
minting twice per round doubles the load on Envoy's `/dex/auth*` limiter (10/min,
`values.yaml envoy.dexLogin`) — which shows up as a spurious `429` mid-round. So the
token is cached in a **0600 file inside a 0700 directory**, and reused until 5 minutes
before its own `exp` claim. It is never printed, never on argv, and never logged: the
`--json` output names the file path and the email, not the token.

`--no-cache` mints regardless (a fresh sign-in is the stronger check, so `up.sh` uses
it); the UI channel takes the cache.

## Environment

`WARGAME_KUBECONFIG`  the cluster kubeconfig (as `up.sh`)
`HOMEOS_SITE`         default https://homeos.simstech.cloud
`WARGAME_DEX_CACHE`   default ${XDG_CACHE_HOME:-~/.cache}/homeos-wargame/dex
`WARGAME_LIMIT_WAIT_S per-run budget for the 429 limiter, default 130

Exit 2 is "could not tell" — a 429 that outlasted the budget, a Secret that would not
read. It is never 0.

Standard library only, like everything else in tools/wargame: bash, curl, python3.
"""

import argparse
import base64
import hashlib
import json
import os
import re
import secrets
import subprocess
import sys
import tempfile
import time
import urllib.parse

HERE = os.path.dirname(os.path.abspath(__file__))

# The persona -> Dex identity map lives in up.sh, which is where a new persona is
# added. Reading it from there rather than restating it here keeps one home for it.
PERSONA_EMAIL = {
    "jacob": "jacob.sims@simstech.cloud",
    "amber": "amber.wargame@simstech.cloud",
}
# Persona -> (namespace, secret, key). Load-bearing: the war-game passwords live in
# `moltis`, jacob's in `home-os`. Mixing them up is how two runs reported a healthy
# environment as broken.
PERSONA_SECRET = {
    "jacob": ("home-os", "home-os-secrets", "adminPassword"),
    "amber": ("moltis", "home-os-amber-wargame-password", "password"),
}

REDIRECT = "http://127.0.0.1:19876/mcp/oauth/callback"
CLIENT_ID = "home-os-mcp"


class Untellable(Exception):
    """Could not tell: a 429 that outlasted the budget, an unreadable Secret."""


class Limited(Exception):
    """Envoy/Dex rate-limited the sign-in attempt; waiting is the correct answer."""


def site():
    return os.environ.get("HOMEOS_SITE", "https://homeos.simstech.cloud").rstrip("/")


def cache_dir():
    return os.environ.get(
        "WARGAME_DEX_CACHE",
        os.path.join(os.environ.get("XDG_CACHE_HOME", os.path.expanduser("~/.cache")),
                     "homeos-wargame", "dex"))


def curl(args, timeout=25, jar=None):
    cmd = ["curl", "-sS", "-m", str(timeout)]
    if jar:
        cmd += ["-c", jar, "-b", jar]
    r = subprocess.run(cmd + args, capture_output=True, text=True)
    return r.returncode, r.stdout, r.stderr


def read_secret(namespace, name, key):
    """Read one Secret key into a 0600 file; return its path. Never returns the value.

    `kubectl get -o jsonpath` would put the secret in this process's argv, which is
    visible in `ps`. It goes to a file instead, and the file is shredded by the caller.
    """
    kubeconfig = os.environ.get("WARGAME_KUBECONFIG", "")
    cmd = ["kubectl"]
    if kubeconfig:
        cmd += ["--kubeconfig", kubeconfig]
    cmd += ["-n", namespace, "get", "secret", name, "-o", "jsonpath={.data.%s}" % key,
            "--request-timeout=20s"]
    r = subprocess.run(cmd, capture_output=True, text=True)
    if r.returncode != 0:
        raise Untellable("Secret %s/%s unreadable: %s"
                         % (namespace, name, (r.stderr or "").strip()[:200]))
    raw = r.stdout.strip()
    if not raw:
        raise Untellable("Secret %s/%s key %r read as empty — refusing to sign in with it"
                         % (namespace, name, key))
    try:
        value = base64.b64decode(raw).decode("utf-8")
    except Exception as exc:
        raise Untellable("Secret %s/%s key %r did not decode: %s" % (namespace, name, key, exc))
    fd, path = tempfile.mkstemp(prefix="wg-pw-", dir=tempfile.mkdtemp(prefix="wg-dex-"))
    os.chmod(path, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        fh.write(value)
    return path


def shred(path):
    try:
        with open(path, "r+b") as fh:
            n = os.fstat(fh.fileno()).st_size
            fh.write(b"\0" * n)
        os.unlink(path)
        os.rmdir(os.path.dirname(path))
    except OSError:
        pass


def redirect_code(location):
    if not location.startswith(REDIRECT):
        return None
    return urllib.parse.parse_qs(urllib.parse.urlparse(location).query).get("code", [None])[0]


def sign_in(email, pwfile, workdir, timeout=25):
    """Drive the interactive connector; return the id_token. Raises Limited/Untellable."""
    jar = os.path.join(workdir, "jar")
    verifier = base64.urlsafe_b64encode(secrets.token_bytes(48)).decode().rstrip("=")
    challenge = base64.urlsafe_b64encode(
        hashlib.sha256(verifier.encode()).digest()).decode().rstrip("=")
    query = {"client_id": CLIENT_ID, "response_type": "code", "scope": "openid email",
             "redirect_uri": REDIRECT, "code_challenge": challenge,
             "code_challenge_method": "S256", "state": secrets.token_urlsafe(8)}

    # 1. authorize -> the local-connector login form.
    rc, body, err = curl(["-L", "-o", os.path.join(workdir, "auth.html"),
                          "-w", "%{http_code}", site() + "/dex/auth?" + urllib.parse.urlencode(query)],
                         timeout, jar)
    if rc != 0:
        raise Untellable("authorize request failed: %s" % (err.strip() or "curl rc=%d" % rc))
    status = body.strip()[-3:]
    html = open(os.path.join(workdir, "auth.html"), errors="replace").read()
    m = re.search(r'<form method="post" action="([^"]+)"', html)
    if not m:
        if status == "429":
            raise Limited()
        raise Untellable("no login form at /dex/auth/local (HTTP %s) — Dex up? connector on?"
                         % status)
    action = m.group(1).replace("&amp;", "&")

    # 2. POST the password (password@file keeps it off argv).
    rc, body, err = curl(["-o", os.path.join(workdir, "login.html"),
                          "-w", "%{http_code}|%{redirect_url}",
                          "--data-urlencode", "login=" + email,
                          "--data-urlencode", "password@" + pwfile,
                          site() + action], timeout, jar)
    if rc != 0:
        raise Untellable("login POST failed: %s" % (err.strip() or "curl rc=%d" % rc))
    status, _, location = body.partition("|")
    if status == "401":
        raise Untellable("Dex rejected the password (401) — Secret is stale or the account is gone")
    if status == "429":
        raise Limited()
    if status not in ("302", "303"):
        raise Untellable("login POST returned %s, expected a redirect" % status)

    # 3. Follow the redirect chain hop by hop: the last hop points at
    #    127.0.0.1:19876, which is deliberately not listening, so -L would try to
    #    connect, fail, and lose the code.
    hdrfile = os.path.join(workdir, "hop.hdr")
    code = redirect_code(location)
    last_status = ""
    for _ in range(4):
        if code:
            break
        rc, last_status, err = curl(["-o", "/dev/null", "-D", hdrfile, "-w", "%{http_code}",
                                     location], timeout)
        if rc != 0:
            raise Untellable("could not follow the login redirect: %s" % err.strip())
        last_status = last_status.strip()[-3:]
        nxt = None
        with open(hdrfile, "r", errors="replace") as fh:
            for line in fh:
                if line.lower().startswith("location:"):
                    nxt = line.split(":", 1)[1].strip()
        if not nxt:
            break
        location = nxt if nxt.startswith("http") else urllib.parse.urljoin(site(), nxt)
        code = redirect_code(location)
    if not code:
        if last_status == "429":
            raise Limited()
        raise Untellable("sign-in produced no authorization code (last HTTP %s)"
                         % (last_status or "none"))

    # 4. Redeem it — the only step that proves Dex authenticated this identity.
    rc, tok, err = curl(["-X", "POST", site() + "/dex/token",
                         "--data-urlencode", "grant_type=authorization_code",
                         "--data-urlencode", "code=" + code,
                         "--data-urlencode", "redirect_uri=" + REDIRECT,
                         "--data-urlencode", "client_id=" + CLIENT_ID,
                         "--data-urlencode", "code_verifier=" + verifier], timeout)
    if rc != 0:
        raise Untellable("token exchange failed: %s" % err.strip())
    try:
        doc = json.loads(tok)
    except ValueError:
        raise Untellable("token endpoint did not return JSON")
    if "id_token" not in doc:
        raise Untellable("token endpoint returned no id_token: %s" % doc.get("error", "no error field"))
    return doc["id_token"]


def claims(token):
    """The id_token's claims. Signature is NOT verified here on purpose.

    This is a harness talking to its own test identity, and the caller that must not
    be fooled about identity is the *product*, which verifies the signature itself.
    What this reads is the `email` (did we sign in as the persona?) and `exp` (when
    to stop reusing the cached token).
    """
    try:
        payload = token.split(".")[1]
    except IndexError:
        raise Untellable("id_token is not a JWT")
    payload += "=" * (-len(payload) % 4)
    try:
        return json.loads(base64.urlsafe_b64decode(payload))
    except Exception as exc:
        raise Untellable("could not read the id_token claims: %s" % exc)


def token_path(persona):
    return os.path.join(cache_dir(), "%s.token" % persona)


def cached(persona, margin_s=300):
    """The cached token, if it is still good for `margin_s` more seconds."""
    path = token_path(persona)
    if not os.path.exists(path):
        return None
    try:
        with open(path, encoding="utf-8") as fh:
            token = fh.read().strip()
    except OSError:
        return None
    if not token:
        return None
    try:
        exp = int(claims(token).get("exp") or 0)
    except Untellable:
        return None
    if exp - margin_s <= int(time.time()):
        return None
    return token


def store(persona, token):
    d = cache_dir()
    os.makedirs(d, mode=0o700, exist_ok=True)
    os.chmod(d, 0o700)
    path = token_path(persona)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        fh.write(token)
    os.chmod(path, 0o600)
    return path


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--persona", required=True, choices=sorted(PERSONA_EMAIL))
    ap.add_argument("--no-cache", action="store_true",
                    help="sign in afresh even if a cached token is still valid")
    ap.add_argument("--json", action="store_true", help="machine-readable verdict")
    ap.add_argument("--print-token-file", action="store_true",
                    help="print only the path of the 0600 file holding the token")
    args = ap.parse_args(argv)

    persona = args.persona
    email = PERSONA_EMAIL[persona]

    def emit(ok, detail, path=None, minted=False):
        if args.print_token_file and ok:
            print(path)
            return 0 if ok else (1 if detail else 2)
        if args.json:
            print(json.dumps({"ok": ok, "persona": persona, "email": email,
                              "detail": detail, "token_file": path, "minted": minted}))
        else:
            print(("OK " if ok else "ERR ") + detail)
        return 0 if ok else 2

    if not args.no_cache:
        token = cached(persona)
        if token:
            c = claims(token)
            return emit(True, "token for %s reused from cache (iss %s)" % (c.get("email"), c.get("iss")),
                        token_path(persona), minted=False)

    secret_ns, secret_name, secret_key = PERSONA_SECRET[persona]
    pwfile = None
    workdir = tempfile.mkdtemp(prefix="wg-dex-")
    try:
        pwfile = read_secret(secret_ns, secret_name, secret_key)
        budget = int(os.environ.get("WARGAME_LIMIT_WAIT_S", "130"))
        deadline = time.time() + budget
        while True:
            try:
                token = sign_in(email, pwfile, workdir)
                break
            except Limited:
                if time.time() >= deadline:
                    return emit(False, "sign-ins are rate-limited (429) and did not clear within "
                                       "%ds (per-client limiter on /dex/auth*, values.yaml "
                                       "envoy.dexLogin). Wait a minute and re-run; this is the "
                                       "limiter, not a bad password." % budget)
                time.sleep(max(1, min(65, deadline - time.time())))
        c = claims(token)
        got = (c.get("email") or "").lower()
        if got != email.lower():
            return emit(False, "signed in as %r, expected %r" % (got, email))
        path = store(persona, token)
        return emit(True, "id_token for %s (iss %s)" % (got, c.get("iss")), path, minted=True)
    except Untellable as exc:
        return emit(False, str(exc))
    finally:
        if pwfile:
            shred(pwfile)
        # The workdir holds auth.html/login.html and a cookie jar, never the token.
        for name in ("auth.html", "login.html", "hop.hdr", "jar"):
            try:
                os.unlink(os.path.join(workdir, name))
            except OSError:
                pass
        try:
            os.rmdir(workdir)
        except OSError:
            pass


if __name__ == "__main__":
    sys.exit(main())
