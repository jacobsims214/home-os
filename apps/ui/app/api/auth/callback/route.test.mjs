// Login start + Dex callback (#4617): redirect_uri, public origin and token URL
// come from DEX_UI_REDIRECT_URI / PUBLIC_URL / DEX_TOKEN_URL when set, and from
// the request (validated Host) plus the in-cluster Dex default when not.
// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
import { test, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";
import "../../../../lib/auth/node-ts-resolve.mjs";

const { NextRequest } = await import("next/server");
const login = await import("../login/route.ts");
const callback = await import("./route.ts");

const ENV_KEYS = ["DEX_UI_REDIRECT_URI", "PUBLIC_URL", "DEX_TOKEN_URL"];
let savedEnv;
let realFetch;
let dexCalls;

beforeEach(() => {
  savedEnv = Object.fromEntries(ENV_KEYS.map((k) => [k, process.env[k]]));
  for (const k of ENV_KEYS) delete process.env[k];
  realFetch = globalThis.fetch;
  dexCalls = [];
  globalThis.fetch = async (url, init) => {
    dexCalls.push({ url: String(url), body: new URLSearchParams(String(init.body)) });
    const exp = Math.floor(Date.now() / 1000) + 3600;
    const idToken = `h.${Buffer.from(JSON.stringify({ exp })).toString("base64url")}.s`;
    return Response.json({ id_token: idToken, refresh_token: "r1", expires_in: 3600 });
  };
});

afterEach(() => {
  for (const k of ENV_KEYS) {
    if (savedEnv[k] === undefined) delete process.env[k];
    else process.env[k] = savedEnv[k];
  }
  globalThis.fetch = realFetch;
});

// The standalone server's request.url carries the bind address, not the public host.
function req(path, headers = {}) {
  return new NextRequest(new URL(path, "http://0.0.0.0:3000"), { headers });
}

function pkceCookie(res) {
  const c = res.headers.getSetCookie().find((s) => s.startsWith("home-os-pkce="));
  return c.slice("home-os-pkce=".length).split(";")[0];
}

/** Runs /api/auth/login then the callback with the cookie it set. */
async function loginRoundTrip(headers, loginQuery = "") {
  const start = await login.GET(req(`/api/auth/login${loginQuery}`, headers));
  const authorize = new URL(start.headers.get("location"));
  const state = authorize.searchParams.get("state");
  const done = await callback.GET(
    req(`/api/auth/callback?code=c1&state=${state}`, { ...headers, cookie: `home-os-pkce=${pkceCookie(start)}` }),
  );
  return { authorize, done };
}

test("env set: redirect_uri is DEX_UI_REDIRECT_URI despite a different Host, and the callback sends the same", async () => {
  process.env.DEX_UI_REDIRECT_URI = "https://h.example/api/auth/callback";
  process.env.PUBLIC_URL = "https://h.example";
  process.env.DEX_TOKEN_URL = "http://dex.test:5556/dex/token";
  const spoofed = { host: "attacker.example", "x-forwarded-host": "attacker.example", "x-forwarded-proto": "https" };

  const { authorize, done } = await loginRoundTrip(spoofed);
  assert.equal(authorize.origin, "https://h.example");
  assert.equal(authorize.pathname, "/dex/auth");
  assert.equal(authorize.searchParams.get("redirect_uri"), "https://h.example/api/auth/callback");

  assert.equal(dexCalls.length, 1);
  assert.equal(dexCalls[0].url, "http://dex.test:5556/dex/token");
  assert.equal(dexCalls[0].body.get("redirect_uri"), "https://h.example/api/auth/callback");
  assert.equal(dexCalls[0].body.get("grant_type"), "authorization_code");
  assert.equal(done.headers.get("location"), "https://h.example/dashboard");
});

test("env set: callback error redirects go to PUBLIC_URL, not the Host header", async () => {
  process.env.PUBLIC_URL = "https://h.example";
  const res = await callback.GET(req("/api/auth/callback", { host: "attacker.example" }));
  assert.equal(res.headers.get("location"), "https://h.example/login?error=no_code");
  const noCookie = await callback.GET(req("/api/auth/callback?code=c&state=s", { host: "attacker.example" }));
  assert.equal(noCookie.headers.get("location"), "https://h.example/login?error=missing_verifier");
  assert.equal(dexCalls.length, 0);
});

test("env unset: header-derived origin and the home-os-dex default (unchanged behaviour)", async () => {
  const headers = { host: "home-os-ui:3000", "x-forwarded-host": "home.example", "x-forwarded-proto": "https" };
  const { authorize, done } = await loginRoundTrip(headers);
  assert.equal(authorize.origin, "https://home.example");
  assert.equal(authorize.searchParams.get("redirect_uri"), "https://home.example/api/auth/callback");
  assert.equal(dexCalls[0].url, "http://home-os-dex:5556/dex/token");
  assert.equal(dexCalls[0].body.get("redirect_uri"), "https://home.example/api/auth/callback");
  assert.equal(done.headers.get("location"), "https://home.example/dashboard");
});

test("env unset: a malformed Host is not trusted", async () => {
  const start = await login.GET(req("/api/auth/login", { host: "evil.example/@x" }));
  assert.equal(new URL(start.headers.get("location")).origin, "http://0.0.0.0:3000");
});

test("?redirect= survives the round trip when it is a same-origin path", async () => {
  process.env.PUBLIC_URL = "https://h.example";
  const { done } = await loginRoundTrip({}, `?redirect=${encodeURIComponent("/dashboard/bills?month=2026-11")}`);
  assert.equal(done.headers.get("location"), "https://h.example/dashboard/bills?month=2026-11");
});

test("?redirect= cannot send the browser off-origin", async () => {
  process.env.PUBLIC_URL = "https://h.example";
  for (const evil of ["https://evil.example/", "//evil.example/x", "/\\evil.example", "/\t/evil.example"]) {
    const { done } = await loginRoundTrip({}, `?redirect=${encodeURIComponent(evil)}`);
    assert.equal(new URL(done.headers.get("location")).origin, "https://h.example", JSON.stringify(evil));
  }
});

test("a tampered return path in the PKCE cookie is re-validated in the callback", async () => {
  process.env.PUBLIC_URL = "https://h.example";
  const start = await login.GET(req("/api/auth/login"));
  const state = new URL(start.headers.get("location")).searchParams.get("state");
  const [, verifier] = pkceCookie(start).split(".");
  const forged = `${state}.${verifier}.${Buffer.from("//evil.example").toString("base64url")}`;
  const done = await callback.GET(req(`/api/auth/callback?code=c&state=${state}`, { cookie: `home-os-pkce=${forged}` }));
  assert.equal(new URL(done.headers.get("location")).origin, "https://h.example");
});

test("a pre-#4617 PKCE cookie (state.verifier) still completes login", async () => {
  process.env.PUBLIC_URL = "https://h.example";
  const done = await callback.GET(req("/api/auth/callback?code=c&state=s1", { cookie: "home-os-pkce=s1.v1" }));
  assert.equal(done.headers.get("location"), "https://h.example/dashboard");
  assert.equal(dexCalls[0].body.get("code_verifier"), "v1");
});
