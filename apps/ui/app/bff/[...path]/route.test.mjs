// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
// CSRF guard on the BFF (#4646): cross-site writes never reach Dex or the API.
import { test, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";
import "../../../lib/auth/node-ts-resolve.mjs";

const { NextRequest } = await import("next/server");
const route = await import("./route.ts");

const API = "http://api.test";
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
// Unsigned is fine: the BFF only reads exp; the API (stubbed here) validates.
const TOKEN = `${b64({ alg: "none" })}.${b64({ exp: Math.floor(Date.now() / 1000) + 3600 })}.sig`;

let calls;
let savedFetch;
let savedEnv;

beforeEach(() => {
  calls = [];
  savedFetch = globalThis.fetch;
  savedEnv = { API_INTERNAL_URL: process.env.API_INTERNAL_URL, PUBLIC_URL: process.env.PUBLIC_URL };
  process.env.API_INTERNAL_URL = API;
  delete process.env.PUBLIC_URL;
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), method: init?.method });
    return new Response(JSON.stringify({ data: { id: "p1" } }), {
      status: init?.method === "POST" ? 201 : 200,
      headers: { "content-type": "application/json" },
    });
  };
});

afterEach(() => {
  globalThis.fetch = savedFetch;
  for (const [k, v] of Object.entries(savedEnv)) {
    if (v === undefined) delete process.env[k];
    else process.env[k] = v;
  }
});

// A request carrying a live session plus a refresh cookie, so a refresh would
// be possible if the guard let the request get that far.
function bff(method, headers = {}, path = ["properties"]) {
  const hasBody = method !== "GET" && method !== "HEAD";
  const request = new NextRequest(`https://home.example/bff/${path.join("/")}`, {
    method,
    headers: {
      host: "home.example",
      cookie: `home-os-token=${TOKEN}; home-os-refresh=r1`,
      ...(hasBody ? { "content-type": "text/plain" } : {}),
      ...headers,
    },
    body: hasBody ? JSON.stringify({ name: "csrf-probe" }) : undefined,
  });
  return route[method](request, { params: { path } });
}

const CROSS_SITE = [
  { origin: "https://evil.example", "sec-fetch-site": "cross-site" },
  { origin: "https://app.home.example", "sec-fetch-site": "same-site" },
  { origin: "https://evil.example" },
  { "sec-fetch-site": "cross-site" },
  { origin: "null" },
  {}, // neither header: not a browser, refused by default
];

test("cross-site writes get 403 and never reach the API or Dex", async () => {
  for (const method of ["POST", "PUT", "PATCH", "DELETE"]) {
    for (const headers of CROSS_SITE) {
      const res = await bff(method, headers);
      assert.equal(res.status, 403, `${method} ${JSON.stringify(headers)}`);
      assert.deepEqual(await res.json(), { error: { code: "FORBIDDEN", message: "cross-site request refused" } });
      assert.equal(res.headers.get("cache-control"), "no-store");
    }
  }
  assert.deepEqual(calls, [], "no upstream call");
});

test("a cross-site write is refused before the refresh, even with no id token", async () => {
  const request = new NextRequest("https://home.example/bff/properties", {
    method: "POST",
    headers: { host: "home.example", cookie: "home-os-refresh=r1", origin: "https://evil.example" },
    body: "{}",
  });
  const res = await route.POST(request, { params: { path: ["properties"] } });
  assert.equal(res.status, 403);
  assert.deepEqual(calls, [], "Dex not called");
  assert.deepEqual(res.headers.getSetCookie(), []);
});

test("same-origin writes are proxied", async () => {
  const res = await bff("POST", { origin: "https://home.example", "sec-fetch-site": "same-origin" });
  assert.equal(res.status, 201);
  assert.deepEqual(calls, [{ url: `${API}/api/v1/properties`, method: "POST" }]);

  const del = await bff("DELETE", { origin: "https://home.example", "sec-fetch-site": "same-origin" }, ["properties", "p1"]);
  assert.equal(del.status, 200);
  assert.equal(calls.length, 2);
});

test("behind a proxy, Origin is checked against PUBLIC_URL", async () => {
  process.env.PUBLIC_URL = "https://home.example";
  const ok = await bff("PATCH", { host: "home-os-ui:3000", origin: "https://home.example", "sec-fetch-site": "same-origin" });
  assert.equal(ok.status, 200);
  const spoofed = await bff("PATCH", { host: "evil.example", origin: "https://evil.example" });
  assert.equal(spoofed.status, 403);
  assert.equal(calls.length, 1);
});

test("reads stay allowed from anywhere (GET/HEAD are not state-changing)", async () => {
  for (const method of ["GET", "HEAD"]) {
    const res = await bff(method, { origin: "https://evil.example", "sec-fetch-site": "cross-site" });
    assert.equal(res.status, 200, method);
  }
  const plain = await bff("GET", {});
  assert.equal(plain.status, 200);
  assert.equal(calls.length, 3);
});
