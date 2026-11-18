#!/usr/bin/env python3
"""REST call to the Home OS API as one household member.

usage: api.py <METHOD> <path> ['<json-body>']     e.g. api.py GET /api/v1/auth/me

The REST API accepts Dex-issued identity tokens ONLY. Agent tokens (`hos_...`) are
MCP-scoped by design: the API answers them 401, so an agent token can never mint or
revoke credentials. This script therefore refuses a `hos_` token outright instead of
sending it and printing a 401 that looks like a broken deployment.

Configuration (env vars, never arguments, so nothing lands in shell history or `ps`):
  HOMEOS_API_URL        API base URL, e.g. http://home-os-api.home-os:8080
  HOMEOS_API_TOKEN      a Dex id_token — the normal, only supported path.

LOCAL DEV ONLY, not needed on the deployed stack (the Dex password grant is retired,
#4540): with HOMEOS_DEX_PASSWORD_GRANT=1 the script mints an id_token instead of
reading one. That needs a Dex that has oauth2.passwordConnector set, which the
deployed instance deliberately does NOT.
  HOMEOS_DEX_PASSWORD_GRANT  1 to enable minting through the password grant
  HOMEOS_DEX_TOKEN_URL  e.g. http://home-os-envoy.home-os:8000/dex/token
                        (through Envoy; /token is an Envoy-only alias and 404s against
                        the Dex pod, so always use /dex/token)
  HOMEOS_USER           the member's login email
  HOMEOS_PASSWORD       the member's password
  HOMEOS_DEX_CLIENT_ID  optional, default home-os-mcp

How to get a Dex id_token without the grant: sign in to Home OS in a browser and copy
the httpOnly `home-os-token` cookie (devtools > Application > Cookies). It is the same
id_token. See tools/wargame/README.md ("Two kinds of token") and project KB
how-to/wargame-ground-truth-auth.md.
"""
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request


def env(name, default=None):
    v = os.environ.get(name, default)
    if v is None:
        sys.exit(f"api.py: set {name}")
    return v


NEEDS_ID_TOKEN = """api.py: no Dex id_token to send.

The REST API accepts Dex-issued identity tokens only (an agent token 401s by design).

Set HOMEOS_API_TOKEN to a Dex id_token: sign in to Home OS in a browser and copy the
httpOnly `home-os-token` cookie (devtools > Application > Cookies). Mint one per run
rather than storing it.

The Dex password grant that used to mint one from a username and password is RETIRED
(#4540): the deployed Dex has no oauth2.passwordConnector, so /dex/token answers 400
unsupported_grant_type. The old env-var recipe is not coming back on the deployed
stack. It still works against a LOCAL dev Dex that sets dex.passwordConnector (see
tools/wargame/README.md); set HOMEOS_DEX_PASSWORD_GRANT=1 to use that.
"""


def api_token():
    """The bearer for /api/v1/*: a Dex id_token, never an agent token."""
    tok = os.environ.get("HOMEOS_API_TOKEN")
    if tok:
        if tok.startswith("hos_"):
            sys.exit(
                "api.py: HOMEOS_API_TOKEN holds a hos_ agent token. Agent tokens are "
                "MCP-scoped by design and the REST API answers them 401, so an agent "
                "token can never mint or revoke credentials. Use a Dex id_token in "
                "HOMEOS_API_TOKEN (see tools/wargame/README.md, 'Two kinds of token')."
            )
        return tok
    if os.environ.get("HOMEOS_DEX_PASSWORD_GRANT") != "1":
        sys.exit(NEEDS_ID_TOKEN)
    # LOCAL DEV ONLY, behind an explicit opt-in: the deployed Dex has the grant off.
    body = urllib.parse.urlencode({
        "grant_type": "password",
        "client_id": os.environ.get("HOMEOS_DEX_CLIENT_ID", "home-os-mcp"),
        "scope": "openid email profile",
        "username": env("HOMEOS_USER"),
        "password": env("HOMEOS_PASSWORD"),
    }).encode()
    try:
        return json.load(urllib.request.urlopen(env("HOMEOS_DEX_TOKEN_URL"), body, timeout=15))["id_token"]
    except urllib.error.HTTPError as e:
        detail = e.read().decode("utf-8", "replace")[:400]
        sys.exit(
            f"api.py: POST {os.environ['HOMEOS_DEX_TOKEN_URL']} -> HTTP {e.code}: {detail}\n"
            "A 400 unsupported_grant_type here means the Dex password grant is off, which "
            "is the DEPLOYED state since #4540 retired it. Set HOMEOS_API_TOKEN to a Dex "
            "id_token from a signed-in browser instead (see above)."
        )


def main(argv):
    if len(argv) < 2:
        sys.exit(__doc__)
    method, path = argv[0], argv[1]
    body = argv[2].encode() if len(argv) > 2 else None
    req = urllib.request.Request(env("HOMEOS_API_URL").rstrip("/") + path, body, method=method,
                                 headers={"Authorization": "Bearer " + api_token(),
                                          "Content-Type": "application/json"})
    try:
        r = urllib.request.urlopen(req, timeout=30)
        print(r.status, r.read().decode()[:2000])
    except urllib.error.HTTPError as e:
        print(e.code, e.read().decode()[:2000])


if __name__ == "__main__":
    main(sys.argv[1:])
