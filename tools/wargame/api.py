#!/usr/bin/env python3
"""REST call to the Home OS API as one household member.

usage: api.py <METHOD> <path> ['<json-body>']     e.g. api.py GET /api/v1/auth/me

The REST API accepts Dex-issued identity tokens ONLY. Agent tokens (`hos_...`) are
MCP-scoped by design: the API answers them 401, so an agent token can never mint or
revoke credentials. This script therefore refuses a `hos_` token outright instead of
sending it and printing a 401 that looks like a broken deployment.

Configuration (env vars, never arguments, so nothing lands in shell history or `ps`):
  HOMEOS_API_URL        API base URL, e.g. http://home-os-api.home-os:8080
  HOMEOS_API_TOKEN      a Dex id_token. Optional if the three Dex vars below are set.
  HOMEOS_DEX_TOKEN_URL  e.g. http://home-os-envoy.home-os:8000/dex/token
                        (through Envoy; /token is an Envoy-only alias and 404s against
                        the Dex pod, so always use /dex/token)
  HOMEOS_USER           the member's login email
  HOMEOS_PASSWORD       the member's password
  HOMEOS_DEX_CLIENT_ID  optional, default home-os-mcp

Minting a Dex id_token is INTERIM: the password grant is retired by #4540. See
tools/wargame/README.md ("Two kinds of token") and project KB
architecture/dex-password-grant-interim.md.
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


def api_token():
    """The bearer for /api/v1/*: a Dex id_token, never an agent token."""
    tok = os.environ.get("HOMEOS_API_TOKEN")
    if tok:
        if tok.startswith("hos_"):
            sys.exit(
                "api.py: HOMEOS_API_TOKEN holds a hos_ agent token. Agent tokens are "
                "MCP-scoped by design and the REST API answers them 401, so an agent "
                "token can never mint or revoke credentials. Use a Dex id_token in "
                "HOMEOS_API_TOKEN (see tools/wargame/README.md, 'Two kinds of token'), "
                "or unset it and set HOMEOS_DEX_TOKEN_URL + HOMEOS_USER + HOMEOS_PASSWORD."
            )
        return tok
    body = urllib.parse.urlencode({
        "grant_type": "password",
        "client_id": os.environ.get("HOMEOS_DEX_CLIENT_ID", "home-os-mcp"),
        "scope": "openid email profile",
        "username": env("HOMEOS_USER"),
        "password": env("HOMEOS_PASSWORD"),
    }).encode()
    return json.load(urllib.request.urlopen(env("HOMEOS_DEX_TOKEN_URL"), body, timeout=15))["id_token"]


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
