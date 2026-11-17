// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import "../../../../lib/auth/node-ts-resolve.mjs";

const { GET, POST } = await import("./route.ts");
// A route module cannot export it (the build rejects unknown exports), so the
// header value lives next door and both the route and this test import it.
const { CLEAR_SITE_DATA } = await import("../../../../lib/auth/clear-site-data.ts");

function post(headers = {}) {
  return new Request("https://home.example/api/auth/logout", {
    method: "POST",
    headers: { host: "home.example", ...headers },
  });
}

test("POST expires every session cookie and 303s to /login", async () => {
  const res = await POST(post({ origin: "https://home.example", "sec-fetch-site": "same-origin" }));
  assert.equal(res.status, 303);
  assert.equal(res.headers.get("location"), "/login");
  assert.equal(res.headers.get("cache-control"), "no-store");

  const cookies = res.headers.getSetCookie();
  for (const name of ["home-os-token", "home-os-refresh", "home-os-token-client"]) {
    const c = cookies.find((s) => s.startsWith(`${name}=;`));
    assert.ok(c, `${name} is expired`);
    assert.match(c, /; Path=\/;/);
    assert.match(c, /Max-Age=0/);
    assert.match(c, /HttpOnly/);
  }
  const pkce = cookies.find((s) => s.startsWith("home-os-pkce=;"));
  assert.match(pkce, /Path=\/api\/auth;/);
});

test("POST behind a proxy compares Origin with X-Forwarded-Host", async () => {
  const res = await POST(
    post({ host: "home-os-ui:3000", "x-forwarded-host": "home.example", origin: "https://home.example" }),
  );
  assert.equal(res.status, 303);
});

test("POST without Origin (non-browser client) still logs out", async () => {
  const res = await POST(post());
  assert.equal(res.status, 303);
});

test("cross-site POST is refused and clears nothing", async () => {
  for (const headers of [
    { origin: "https://evil.example" },
    { origin: "null" },
    { "sec-fetch-site": "cross-site" },
    { "sec-fetch-site": "same-site", origin: "https://home.example" },
  ]) {
    const res = await POST(post(headers));
    assert.equal(res.status, 403, JSON.stringify(headers));
    assert.deepEqual(res.headers.getSetCookie(), []);
  }
});

test("GET never logs out", async () => {
  const res = await GET();
  assert.equal(res.status, 405);
  assert.equal(res.headers.get("allow"), "POST");
  assert.deepEqual(res.headers.getSetCookie(), []);
});

test("with PUBLIC_URL set, Origin must match it (shared helper, #4646)", async () => {
  const saved = process.env.PUBLIC_URL;
  process.env.PUBLIC_URL = "https://home.example";
  try {
    assert.equal((await POST(post({ origin: "https://home.example" }))).status, 303);
    // Host header alone no longer decides: the configured origin wins.
    const spoofed = post({ host: "evil.example", origin: "https://evil.example" });
    assert.equal((await POST(spoofed)).status, 403);
  } finally {
    if (saved === undefined) delete process.env.PUBLIC_URL;
    else process.env.PUBLIC_URL = saved;
  }
});

// ── #4648: what a logout leaves behind on a shared device ───────────────────

test("POST clears Cache Storage and DOM storage with Clear-Site-Data", async () => {
  const res = await POST(post({ origin: "https://home.example", "sec-fetch-site": "same-origin" }));
  assert.equal(res.status, 303);
  // MDN's grammar requires every directive to be a quoted string.
  assert.equal(res.headers.get("clear-site-data"), '"cache", "storage"');
  assert.equal(res.headers.get("clear-site-data"), CLEAR_SITE_DATA);
  // "cache" is HTTP cache + Cache Storage (+ bfcache); "storage" is
  // localStorage, sessionStorage, IndexedDB and the service worker
  // registrations. The cookies are expired by Set-Cookie instead, so "*" (which
  // would also mean "cookies") is deliberately not used.
  const directives = res.headers.get("clear-site-data").split(",").map((d) => d.trim());
  assert.deepEqual(directives, ['"cache"', '"storage"']);
  assert.ok(!directives.includes('"cookies"'), "cookies are dropped by Set-Cookie, not Clear-Site-Data");
  assert.ok(!directives.includes('"*"'), "no wildcard: it would duplicate the cookie clearing");
});

test("a refused cross-site logout clears nothing, Clear-Site-Data included", async () => {
  const res = await POST(post({ origin: "https://evil.example" }));
  assert.equal(res.status, 403);
  assert.equal(res.headers.get("clear-site-data"), null);
  assert.deepEqual(res.headers.getSetCookie(), []);
});

test("GET never carries Clear-Site-Data", async () => {
  const res = await GET();
  assert.equal(res.headers.get("clear-site-data"), null);
});

test("CLEAR_SITE_DATA is exactly cache+storage (no cookies, no wildcard)", () => {
  assert.equal(CLEAR_SITE_DATA, '"cache", "storage"');
});
