// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  middleware,
  routeAccess,
  safeRedirectPath,
  publicOrigin,
  SESSION_COOKIE,
  REFRESH_COOKIE,
} from "./middleware.ts";

// Minimal stand-in for NextRequest: the middleware reads nextUrl, url, headers
// and cookies. `origin` is the host in request.url, which the standalone Next
// server fills from its bind address (HOSTNAME:PORT), not the browser's Host.
function req(path, cookies = {}, { origin = "https://home.example", headers } = {}) {
  const url = new URL(path, origin);
  return {
    url: url.toString(),
    nextUrl: url,
    headers: new Headers(headers ?? { host: url.host }),
    cookies: { get: (name) => (name in cookies ? { name, value: cookies[name] } : undefined) },
  };
}

const PUBLIC = [
  "/login",
  "/register",
  "/reset-password",
  "/reset-password/confirm",
  "/invite/x",
  "/invite/abc123",
  "/invites", // the accept page the API's accept_url points at (#4579)
  "/invites/accept",
  "/invites/accept?token=4f1c9a7e-2b8d",
  "/onboarding",
  "/onboarding/household",
  "/api/auth",
  "/api/auth/login",
  "/api/auth/callback",
];
const PASSTHROUGH = [
  "/bff/members",
  "/bff/files/123/download",
  "/_next/data/build/x.json",
  "/sw.js",
  "/workbox-1a2b3c.js",
  "/manifest.json",
  "/favicon.ico",
  "/icons/icon-192.png",
];
const PROTECTED = [
  "/",
  "/dashboard",
  "/dashboard/bills",
  "/dashboard/settings/profile",
  "/dashboard/report.pdf", // a dot no longer bypasses the guard
  "/loginx", // prefix must match a whole segment
  "/invitex",
  "/invitesomething",
  "/bffx",
  "/api/other",
];

test("routeAccess classifies paths", () => {
  for (const p of PUBLIC) assert.equal(routeAccess(p), "public", p);
  for (const p of PASSTHROUGH) assert.equal(routeAccess(p), "passthrough", p);
  for (const p of PROTECTED) assert.equal(routeAccess(p), "protected", p);
});

test("public and passthrough paths load without a session", () => {
  for (const p of [...PUBLIC, ...PASSTHROUGH]) assert.equal(middleware(req(p)), undefined, p);
});

test("/bff without a session is not redirected (its handler answers 401 JSON)", () => {
  assert.equal(middleware(req("/bff/me")), undefined);
});

// Each test that touches PUBLIC_URL restores it.
function withPublicUrl(value, fn) {
  const prev = process.env.PUBLIC_URL;
  if (value === undefined) delete process.env.PUBLIC_URL;
  else process.env.PUBLIC_URL = value;
  try {
    fn();
  } finally {
    if (prev === undefined) delete process.env.PUBLIC_URL;
    else process.env.PUBLIC_URL = prev;
  }
}
delete process.env.PUBLIC_URL;

const location = (res) => res.headers.get("location");

test("protected paths without a session redirect to /login with the return path", () => {
  const res = middleware(req("/dashboard/bills?month=2026-11"));
  assert.ok(res instanceof Response);
  assert.equal(res.status, 307);
  assert.equal(location(res), "https://home.example/login?redirect=%2Fdashboard%2Fbills%3Fmonth%3D2026-11");
  const loc = new URL(location(res));
  assert.equal(loc.pathname, "/login");
  assert.equal(loc.searchParams.get("redirect"), "/dashboard/bills?month=2026-11");

  for (const p of PROTECTED) assert.equal(middleware(req(p))?.status, 307, p);
});

// What the #4428 check saw: request.url had the bind address, not home.example.
const BIND_ORIGINS = ["https://localhost:3987", "http://127.0.0.1:3988", "http://home-os-ui-7d9f8c-abcde:3000"];

test("behind a proxy the Location uses X-Forwarded-Host / Host, never the bind host", () => {
  const proxied = { host: "home-os-ui:3000", "x-forwarded-host": "home.example", "x-forwarded-proto": "https" };
  const envoy = { host: "home.example", "x-forwarded-proto": "https" }; // Envoy keeps :authority as Host
  for (const origin of BIND_ORIGINS) {
    for (const headers of [proxied, envoy]) {
      const loc = location(middleware(req("/dashboard?tab=bills", {}, { origin, headers })));
      assert.equal(loc, "https://home.example/login?redirect=%2Fdashboard%3Ftab%3Dbills", `${origin} ${JSON.stringify(headers)}`);
    }
  }
  // Proxy chains append: the first value is the client-facing one.
  const chained = { host: "x", "x-forwarded-host": "home.example, envoy.internal", "x-forwarded-proto": "https,http" };
  assert.equal(new URL(location(middleware(req("/", {}, { origin: BIND_ORIGINS[2], headers: chained })))).origin, "https://home.example");
});

test("PUBLIC_URL wins over request headers", () => {
  withPublicUrl("https://home.simstech.cloud/", () => {
    const headers = { host: "evil.example", "x-forwarded-host": "evil.example", "x-forwarded-proto": "http" };
    const loc = location(middleware(req("/dashboard", {}, { origin: BIND_ORIGINS[2], headers })));
    assert.equal(loc, "https://home.simstech.cloud/login?redirect=%2Fdashboard");
  });
  // Malformed or non-http PUBLIC_URL is ignored.
  for (const bad of ["not a url", "javascript:alert(1)", "ftp://home.example"]) {
    withPublicUrl(bad, () => {
      const r = req("/dashboard", {}, { origin: BIND_ORIGINS[0], headers: { host: "home.example" } });
      assert.equal(publicOrigin(r), "https://home.example", bad);
    });
  }
});

test("bad forwarded values fall back safely", () => {
  const origin = "https://localhost:3987";
  // Unknown proto: keep the request's scheme.
  assert.equal(publicOrigin(req("/", {}, { origin, headers: { host: "home.example", "x-forwarded-proto": "gopher" } })), "https://home.example");
  // Host that is not hostname[:port] (path, userinfo, spaces): ignored.
  for (const host of ["evil.example/x", "user@evil.example", "a b", "evil.example:99999999"]) {
    assert.equal(publicOrigin(req("/", {}, { origin, headers: { host } })), origin, host);
  }
  // No host headers at all: last resort is request.nextUrl.
  assert.equal(publicOrigin(req("/", {}, { origin, headers: {} })), origin);
  // IPv6 literal with port is fine.
  assert.equal(publicOrigin(req("/", {}, { origin, headers: { host: "[::1]:8443" } })), "https://[::1]:8443");
});

test("the return path is always a same-origin path (no open redirect)", () => {
  // A request path with a leading "//" must not become a protocol-relative redirect.
  const res = middleware(req("https://home.example//evil.example/x?y=1"));
  const redirect = new URL(location(res)).searchParams.get("redirect");
  assert.equal(redirect, "/evil.example/x?y=1");
  assert.equal(new URL(redirect, "https://home.example").origin, "https://home.example");

  const cases = {
    "/dashboard": "/dashboard",
    "/dashboard?a=1&b=/x": "/dashboard?a=1&b=/x",
    "//evil.example": "/evil.example",
    "///evil.example/a": "/evil.example/a",
    "/\\evil.example": "/evil.example",
    "\\\\evil.example": "/evil.example",
    "https://evil.example/": "/",
    "javascript:alert(1)": "/",
    "": "/",
  };
  for (const [input, want] of Object.entries(cases)) {
    const got = safeRedirectPath(input);
    assert.equal(got, want, JSON.stringify(input));
    assert.equal(new URL(got, "https://home.example").origin, "https://home.example", JSON.stringify(input));
  }
});

test("an empty session cookie counts as no session", () => {
  assert.equal(middleware(req("/dashboard", { [SESSION_COOKIE]: "" }))?.status, 307);
});

test("protected paths with a session pass", () => {
  for (const p of PROTECTED) {
    assert.equal(middleware(req(p, { [SESSION_COOKIE]: "jwt" })), undefined, p);
  }
});

test("a refresh cookie alone lets protected pages load (the BFF renews the session)", () => {
  assert.equal(middleware(req("/dashboard", { [REFRESH_COOKIE]: "rt" })), undefined);
  assert.equal(middleware(req("/dashboard", { [REFRESH_COOKIE]: "" }))?.status, 307);
});
