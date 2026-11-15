// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
import { test, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { apiFetch, ApiError, redirectToLogin, resetLoginRedirect, isBffPath } from "./api.ts";

// Minimal browser stand-ins: window.location with a recorded assign(), and a
// fetch that answers with a fixed status.
let assigned;
function setLocation(path) {
  const url = new URL(path, "https://home.example");
  globalThis.window = {
    location: {
      origin: url.origin,
      pathname: url.pathname,
      search: url.search,
      assign: (to) => assigned.push(to),
    },
  };
}
function answer(status, body = { error: { code: "X" } }) {
  const calls = [];
  globalThis.fetch = async (url) => {
    calls.push(new URL(url).pathname);
    return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
  };
  return calls;
}

beforeEach(() => {
  assigned = [];
  resetLoginRedirect();
  setLocation("/dashboard/bills?month=2026-11");
});

test("a /bff 401 sends the browser to /login with the current path+query, then throws", async () => {
  const calls = answer(401, { error: { code: "UNAUTHORIZED" } });
  await assert.rejects(apiFetch("/api/v1/bills"), (e) => e instanceof ApiError && e.status === 401);
  assert.deepEqual(calls, ["/bff/bills"]);
  assert.deepEqual(assigned, ["/login?redirect=%2Fdashboard%2Fbills%3Fmonth%3D2026-11"]);
});

test("several 401s at once navigate only once", async () => {
  answer(401);
  const results = await Promise.allSettled([apiFetch("/bff/a"), apiFetch("/bff/b"), apiFetch("/api/v1/c")]);
  assert.ok(results.every((r) => r.status === "rejected" && r.reason.status === 401));
  assert.equal(assigned.length, 1);
});

test("503 AUTH_UNAVAILABLE does not redirect", async () => {
  answer(503, { error: { code: "AUTH_UNAVAILABLE" } });
  await assert.rejects(apiFetch("/bff/members"), (e) => e.status === 503);
  assert.deepEqual(assigned, []);
});

test("a 401 from a public pre-login path does not redirect", async () => {
  const calls = answer(401);
  await assert.rejects(apiFetch("/api/v1/auth/reset-password", { method: "POST", body: {} }), (e) => e.status === 401);
  assert.deepEqual(calls, ["/api/v1/auth/reset-password"]);
  assert.deepEqual(assigned, []);
});

test("no redirect when already on /login", async () => {
  setLocation("/login?redirect=%2Fdashboard");
  answer(401);
  await assert.rejects(apiFetch("/bff/me"), (e) => e.status === 401);
  assert.equal(redirectToLogin(), false);
  assert.deepEqual(assigned, []);
});

test("other statuses and success do not redirect", async () => {
  answer(403);
  await assert.rejects(apiFetch("/bff/x"), (e) => e.status === 403);
  answer(200, { ok: true });
  assert.deepEqual(await apiFetch("/bff/x"), { ok: true });
  assert.deepEqual(assigned, []);
});

test("the return path uses the middleware's same-origin rule", () => {
  // A path like //evil.example can arise from the browser URL; it must not
  // become a protocol-relative redirect after login.
  setLocation("https://home.example//evil.example/x?y=1");
  window.location.pathname = "//evil.example/x";
  assert.equal(redirectToLogin(), true);
  assert.deepEqual(assigned, ["/login?redirect=%2Fevil.example%2Fx%3Fy%3D1"]);
});

test("no-op on the server", () => {
  delete globalThis.window;
  assert.equal(redirectToLogin(), false);
});

test("isBffPath matches /bff and its subpaths only", () => {
  assert.ok(isBffPath("/bff"));
  assert.ok(isBffPath("/bff/files/upload"));
  assert.ok(!isBffPath("/bffx"));
  assert.ok(!isBffPath("/api/v1/auth/forgot-password"));
});
