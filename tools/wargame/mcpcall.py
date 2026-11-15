#!/usr/bin/env python3
"""Ground-truth reads (and writes) against the Home OS MCP, as one household member.

usage:
  mcpcall.py -list                       list the MCP tools and their argument names
  mcpcall.py <tool> ['<json-args>']      call one tool; prints the JSON-RPC result (truncated to 3000 chars)
  mcpcall.py --text <tool> ['<json-args>']  print only the tool's text content, untruncated (pipe to jq)

Configuration (env vars, never arguments, so nothing lands in shell history or `ps`):
  HOMEOS_MCP_URL        MCP base URL, e.g. http://home-os-mcp.home-os:8082 (the script appends /mcp/sse)
  Auth, one of:
    HOMEOS_AGENT_TOKEN  a bearer token for the member (agent token or Dex id_token)
    or, to mint a Dex id_token with the password grant (needs Dex `oauth2.passwordConnector`):
    HOMEOS_DEX_TOKEN_URL  e.g. http://home-os-dex.home-os:5556/dex/token
    HOMEOS_USER           the member's login email
    HOMEOS_PASSWORD       the member's password
    HOMEOS_DEX_CLIENT_ID  optional, default home-os-mcp

Uses the legacy SSE transport (/mcp/sse + the endpoint it announces), which every MCP build serves.
Standard library only.
"""
import json
import os
import queue
import sys
import threading
import urllib.parse
import urllib.request


def env(name, default=None):
    v = os.environ.get(name, default)
    if v is None:
        sys.exit(f"mcpcall.py: set {name}")
    return v


def bearer():
    tok = os.environ.get("HOMEOS_AGENT_TOKEN")
    if tok:
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
    text_only = False
    if argv and argv[0] == "--text":
        text_only, argv = True, argv[1:]
    if not argv:
        sys.exit(__doc__)
    tool = argv[0]
    args = json.loads(argv[1]) if len(argv) > 1 else {}

    mcp = env("HOMEOS_MCP_URL").rstrip("/")
    headers = {"Authorization": "Bearer " + bearer()}
    replies, endpoint = queue.Queue(), queue.Queue()

    def reader():
        r = urllib.request.urlopen(urllib.request.Request(mcp + "/mcp/sse", headers=headers), timeout=60)
        ev = None
        for raw in r:
            line = raw.decode().rstrip("\n")
            if line.startswith("event:"):
                ev = line[6:].strip()
            elif line.startswith("data:"):
                d = line[5:].strip()
                if ev == "endpoint":
                    endpoint.put(d)
                else:
                    replies.put(json.loads(d))

    threading.Thread(target=reader, daemon=True).start()
    ep = endpoint.get(timeout=10)
    url = ep if ep.startswith("http") else mcp + ep

    def rpc(i, method, params=None):
        msg = {"jsonrpc": "2.0", "method": method}
        if i is not None:
            msg["id"] = i
        if params is not None:
            msg["params"] = params
        urllib.request.urlopen(urllib.request.Request(
            url, json.dumps(msg).encode(), {**headers, "Content-Type": "application/json"}), timeout=30)
        while i is not None:
            m = replies.get(timeout=30)
            if m.get("id") == i:
                return m

    rpc(1, "initialize", {"protocolVersion": "2024-11-05", "capabilities": {},
                          "clientInfo": {"name": "home-os-wargame", "version": "0"}})
    rpc(None, "notifications/initialized")

    if tool == "-list":
        for t in rpc(2, "tools/list")["result"]["tools"]:
            print(t["name"], "|", json.dumps(t["inputSchema"].get("properties", {}))[:160])
        return
    r = rpc(2, "tools/call", {"name": tool, "arguments": args})
    res = r.get("result", r)
    if text_only and "content" in res:
        print("\n".join(c.get("text", "") for c in res["content"]))
    else:
        print(json.dumps(res, indent=1)[:3000])


if __name__ == "__main__":
    main(sys.argv[1:])
