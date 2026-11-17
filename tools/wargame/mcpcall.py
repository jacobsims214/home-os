#!/usr/bin/env python3
"""Ground-truth reads (and writes) against the Home OS MCP, as one household member.

usage:
  mcpcall.py -list                       list the MCP tools and their argument names
  mcpcall.py <tool> ['<json-args>']      call one tool; prints the JSON-RPC result (truncated to 3000 chars)
  mcpcall.py --text <tool> ['<json-args>']  print only the tool's text content, untruncated (pipe to jq)
  mcpcall.py --selftest                  run the transport tests and exit (no network)

Configuration (env vars, never arguments, so nothing lands in shell history or `ps`):
  HOMEOS_MCP_URL        MCP base URL, e.g. http://home-os-mcp.home-os:8082 — the script POSTs to
                        <base>/mcp (a URL that already ends in /mcp is used as-is)
  HOMEOS_MCP_TIMEOUT_S  optional, default 60 — per-request timeout in seconds
  Auth, one of:
    HOMEOS_AGENT_TOKEN  a bearer token for the member (agent token or Dex id_token)
    or, to mint a Dex id_token with the password grant (needs Dex `oauth2.passwordConnector`):
    HOMEOS_DEX_TOKEN_URL  e.g. http://home-os-dex.home-os:5556/dex/token
    HOMEOS_USER           the member's login email
    HOMEOS_PASSWORD       the member's password
    HOMEOS_DEX_CLIENT_ID  optional, default home-os-mcp

Transport: **Streamable HTTP, POST <base>/mcp** — the only MCP transport Home OS serves
since #4361. The legacy HTTP+SSE transport (`GET /mcp/sse` and the `/mcp/message` endpoint
it announced) was removed and now 404s, so this script never tries it: it was the stock
tool's one and only path until #4949, and its 404 used to surface as a bare
`queue.Empty`. A 404 here names the URL it posted to and says what a 404 means.

The server is stateless: no `Mcp-Session-Id` is minted or required (one is echoed back on
later requests if a build ever starts sending one), so nothing here keeps a session. The
`initialize` handshake is still the right opening for a 2025-era client, and the deployed
server answers `protocolVersion: 2025-03-26` (its supported set is 2026-07-28, 2025-11-25,
2025-06-18, 2025-03-26 and 2024-11-05; it negotiates down to what the client asked for).

Standard library only.

Exit status: 0 success; 1 the server answered with a JSON-RPC error, or the tool itself
returned `isError` (so a failed ground-truth read never looks like a pass); 2 usage or
transport error, with the URL and HTTP status in the message.
"""
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

# What a 2025-era client asks for. A 2026-07-28 client sends no initialize at all (it
# carries the version in params._meta plus MCP-Protocol-Version/Mcp-Method/Mcp-Name
# headers); this script deliberately speaks the older handshake because that is what
# moltis, opencode and the MCP inspector use and what the server tests cover.
PROTOCOL_VERSION = "2025-03-26"
CLIENT_INFO = {"name": "home-os-wargame", "version": "2"}


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


def rpc_url(base):
    """The MCP endpoint: <base>/mcp, or base itself when it already names /mcp."""
    u = base.strip().rstrip("/")
    return u if u.endswith("/mcp") else u + "/mcp"


class McpError(Exception):
    """A transport or JSON-RPC failure, already phrased for a person to read."""


def diagnose(url, status, body):
    """One message per failure mode, each naming the URL that was attempted."""
    hint = {
        401: "the token was refused: check HOMEOS_AGENT_TOKEN (an expired Dex id_token is the "
             "usual cause). Agent tokens are MCP-scoped by design, so this is not an auth bug",
        403: "authenticated but refused: a household role or the Origin rule said no "
             "(browser Origins are checked against MCP_ALLOWED_ORIGINS)",
        404: "there is no MCP endpoint at that path. Home OS serves Streamable HTTP at "
             "POST <HOMEOS_MCP_URL>/mcp; the legacy SSE endpoints /mcp/sse and /mcp/message "
             "were removed in #4361 and answer 404. Check HOMEOS_MCP_URL: it is a base URL "
             "(http://host:8082), not .../mcp/sse. If the host itself is behind a port-forward "
             "that has ended, restart it",
        405: "the MCP endpoint accepts POST only (GET was the removed standalone SSE stream)",
        413: "the request body is larger than the server accepts",
        429: "rate limited: wait and retry",
    }.get(status, "unexpected status from the MCP endpoint")
    detail = " ".join(body.split())[:400]
    return (f"POST {url} -> HTTP {status}: {hint}"
            + (f"\n  server said: {detail}" if detail else ""))


def parse_response(raw, content_type):
    """The JSON-RPC message inside a Streamable HTTP POST response body.

    The transport answers `Content-Type: application/json` for a single response, and
    upgrades to `text/event-stream` when the call emitted progress/log notifications; in
    the SSE case the final response follows any notifications, so the last data event that
    is a JSON object wins. Returns None for a body-less 202 (a notification's answer).
    """
    text = raw.decode("utf-8", "replace")
    if not text.strip():
        return None
    if "text/event-stream" in (content_type or ""):
        events, data = [], []
        for line in text.splitlines() + [""]:
            if line.startswith("data:"):
                data.append(line[5:].lstrip())
            elif not line.strip() and data:
                events.append("\n".join(data))
                data = []
        for payload in reversed(events):
            try:
                msg = json.loads(payload)
            except ValueError:
                continue
            if isinstance(msg, dict):
                return msg
        raise McpError(f"the SSE response carried no JSON-RPC message: {text[:400]}")
    try:
        return json.loads(text)
    except ValueError:
        raise McpError(f"the response was not JSON (Content-Type: {content_type or 'none'}): {text[:400]}")


class Client:
    """A stateless Streamable HTTP MCP client: POST JSON-RPC, read one reply per POST."""

    def __init__(self, url, token, timeout=60.0):
        self.url = url
        self.timeout = timeout
        self.headers = {
            "Authorization": "Bearer " + token,
            "Content-Type": "application/json",
            # The spec requires a client to accept both shapes; the server picks.
            "Accept": "application/json, text/event-stream",
            "User-Agent": "home-os-wargame/2",
        }
        self.version = None     # MCP-Protocol-Version header, once initialize has negotiated
        self.session_id = None  # stays None against a stateless server
        self.next_id = 0

    def post(self, payload, expect_reply=True):
        headers = dict(self.headers)
        if self.version:
            headers["MCP-Protocol-Version"] = self.version
        if self.session_id:
            headers["Mcp-Session-Id"] = self.session_id
        req = urllib.request.Request(self.url, json.dumps(payload).encode(), headers, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as r:
                if r.headers.get("Mcp-Session-Id"):
                    self.session_id = r.headers["Mcp-Session-Id"]
                raw, content_type = r.read(), r.headers.get("Content-Type", "")
        except urllib.error.HTTPError as e:
            raise McpError(diagnose(self.url, e.code, e.read().decode("utf-8", "replace"))) from None
        except urllib.error.URLError as e:
            raise McpError(
                f"cannot reach {self.url}: {e.reason}\n"
                "  Off-cluster, port-forward the MCP first (kubectl -n home-os port-forward "
                "svc/home-os-mcp 8082:8082) and set HOMEOS_MCP_URL=http://127.0.0.1:8082") from None
        if not expect_reply:
            return None
        return parse_response(raw, content_type)

    def rpc(self, method, params=None):
        self.next_id += 1
        msg = {"jsonrpc": "2.0", "id": self.next_id, "method": method}
        if params is not None:
            msg["params"] = params
        reply = self.post(msg)
        if reply is None:
            raise McpError(f"{method}: the server accepted the request but sent no response")
        if reply.get("error"):
            err = reply["error"]
            raise McpError(f"{method}: JSON-RPC error {err.get('code')}: {err.get('message')}")
        return reply.get("result") or {}

    def notify(self, method):
        # A notification's answer is 202 Accepted with an empty body.
        self.post({"jsonrpc": "2.0", "method": method}, expect_reply=False)

    def initialize(self):
        result = self.rpc("initialize", {
            "protocolVersion": PROTOCOL_VERSION,
            "capabilities": {},
            "clientInfo": CLIENT_INFO,
        })
        self.version = result.get("protocolVersion") or PROTOCOL_VERSION
        self.notify("notifications/initialized")
        return result

    def list_tools(self):
        """Every tool, following nextCursor — a page limit must not silently hide tools."""
        tools, cursor = [], None
        while True:
            result = self.rpc("tools/list", {"cursor": cursor} if cursor else {})
            tools.extend(result.get("tools") or [])
            cursor = result.get("nextCursor")
            if not cursor:
                return tools

    def call_tool(self, name, arguments):
        return self.rpc("tools/call", {"name": name, "arguments": arguments})


def _raises(fn):
    try:
        fn()
    except McpError:
        return True
    return False


def selftest():
    """Transport parsing and the URL/diagnosis rules, with no server involved."""
    sse = (b'event: message\ndata: {"jsonrpc":"2.0","method":"notifications/progress","params":{}}\n\n'
           b'event: message\ndata: {"jsonrpc":"2.0","id":2,"result":{"tools":[]}}\n\n')
    cases = [
        ("plain JSON body",
         lambda: parse_response(b'{"jsonrpc":"2.0","id":2,"result":{"ok":1}}',
                                "application/json")["result"]["ok"] == 1),
        ("SSE body: the last data event wins over notifications",
         lambda: parse_response(sse, "text/event-stream")["id"] == 2),
        ("SSE body with a split data line",
         lambda: parse_response(b'data: {"jsonrpc":"2.0",\ndata: "id":3,"result":{}}\n\n',
                                "text/event-stream")["id"] == 3),
        ("202 with an empty body (a notification's answer)",
         lambda: parse_response(b"", "") is None),
        ("JSON with a charset parameter",
         lambda: parse_response(b'{"id":4,"result":{}}', "application/json; charset=utf-8")["id"] == 4),
        ("a non-JSON body raises rather than being parsed",
         lambda: _raises(lambda: parse_response(b"<html>502 Bad Gateway</html>", "text/html"))),
        ("base URL gets /mcp",
         lambda: rpc_url("http://127.0.0.1:8082/") == "http://127.0.0.1:8082/mcp"),
        ("a URL already naming /mcp is not doubled",
         lambda: rpc_url("https://homeos.simstech.cloud/mcp") == "https://homeos.simstech.cloud/mcp"),
        ("a trailing slash and a stray /mcp/ do not break the endpoint",
         lambda: rpc_url(" http://h:8082/mcp/ ") == "http://h:8082/mcp"),
        ("404 names the URL it tried and explains the removed SSE path",
         lambda: "POST http://h/mcp -> HTTP 404" in diagnose("http://h/mcp", 404, "")
         and "/mcp/sse" in diagnose("http://h/mcp", 404, "")),
        ("401 names the token variable",
         lambda: "HOMEOS_AGENT_TOKEN" in diagnose("http://h/mcp", 401, "")),
        ("the server's own words are kept",
         lambda: "405 page" in diagnose("http://h/mcp", 418, "405 page")),
    ]
    failed = 0
    for name, case in cases:
        try:
            ok = case()
        except Exception as e:  # a case that raises is a failing case, not a crash
            ok, name = False, f"{name} ({e})"
        print(("ok   " if ok else "FAIL ") + name)
        failed += 0 if ok else 1
    print(f"--- {len(cases) - failed}/{len(cases)} passed")
    return 1 if failed else 0


def main(argv):
    text_only = False
    if argv and argv[0] == "--text":
        text_only, argv = True, argv[1:]
    if argv and argv[0] == "--selftest":
        return selftest()
    if not argv or argv[0] in ("-h", "--help"):
        print(__doc__.strip(), file=sys.stderr)
        return 0 if argv else 2
    tool = argv[0]
    try:
        args = json.loads(argv[1]) if len(argv) > 1 else {}
    except ValueError as e:
        print(f"mcpcall.py: the arguments must be one JSON object: {e}", file=sys.stderr)
        return 2

    client = Client(rpc_url(env("HOMEOS_MCP_URL")), bearer(),
                    timeout=float(os.environ.get("HOMEOS_MCP_TIMEOUT_S") or 60))
    try:
        client.initialize()
        if tool == "-list":
            for t in client.list_tools():
                props = (t.get("inputSchema") or {}).get("properties", {})
                print(t["name"], "|", json.dumps(props)[:160])
            return 0
        res = client.call_tool(tool, args)
    except McpError as e:
        print(f"mcpcall.py: {e}", file=sys.stderr)
        # A transport/HTTP failure (2) is not the same as the server refusing the call (1).
        return 2 if ("-> HTTP" in str(e) or "cannot reach" in str(e)) else 1

    if text_only and "content" in res:
        print("\n".join(c.get("text", "") for c in res["content"]))
    else:
        print(json.dumps(res, indent=1)[:3000])
    return 1 if res.get("isError") else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
