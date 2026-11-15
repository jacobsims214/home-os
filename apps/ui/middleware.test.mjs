// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import { middleware, routeAccess, SESSION_COOKIE, REFRESH_COOKIE } from "./middleware.ts";

// Minimal stand-in for NextRequest: the middleware reads nextUrl, url and cookies.
function req(path, cookies = {}) {
  const url = new URL(path, "https://home.example");
  return {
    url: url.toString(),
    nextUrl: url,
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

test("protected paths without a session redirect to /login with the return path", () => {
  const res = middleware(req("/dashboard/bills?month=2026-11"));
  assert.ok(res instanceof Response);
  assert.equal(res.status, 307);
  const loc = new URL(res.headers.get("location"));
  assert.equal(loc.origin, "https://home.example");
  assert.equal(loc.pathname, "/login");
  assert.equal(loc.searchParams.get("redirect"), "/dashboard/bills?month=2026-11");

  for (const p of PROTECTED) assert.equal(middleware(req(p))?.status, 307, p);
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
