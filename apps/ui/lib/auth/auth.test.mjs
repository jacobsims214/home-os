// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import "./node-ts-resolve.mjs";

const { resolveDexConfig, dexTokenUrl, DEFAULT_DEX_TOKEN_URL } = await import("./dex-config.ts");
const { returnPath, returnUrl, completionUrl, AUTH_COMPLETE_PATH } = await import("./return-path.ts");
const { encodePkceCookie, decodePkceCookie } = await import("./pkce-cookie.ts");

const fromRequest = () => "http://localhost:3000";
const neverCalled = () => assert.fail("request origin must not be consulted when env is set");

test("env set: redirect_uri is DEX_UI_REDIRECT_URI verbatim, origin is PUBLIC_URL", () => {
  const cfg = resolveDexConfig(neverCalled, {
    DEX_UI_REDIRECT_URI: "https://h.example/api/auth/callback",
    PUBLIC_URL: "https://h.example/",
    DEX_TOKEN_URL: "http://dex.local:5556/dex/token",
  });
  assert.deepEqual(cfg, {
    publicOrigin: "https://h.example",
    redirectUri: "https://h.example/api/auth/callback",
    tokenUrl: "http://dex.local:5556/dex/token",
  });
});

test("a custom redirect URI is kept even when it differs from PUBLIC_URL", () => {
  const cfg = resolveDexConfig(neverCalled, {
    DEX_UI_REDIRECT_URI: "https://auth.example/custom/cb",
    PUBLIC_URL: "https://h.example",
  });
  assert.equal(cfg.redirectUri, "https://auth.example/custom/cb");
  assert.equal(cfg.publicOrigin, "https://h.example");
});

test("only PUBLIC_URL: redirect_uri is derived from it", () => {
  const cfg = resolveDexConfig(neverCalled, { PUBLIC_URL: "https://h.example" });
  assert.equal(cfg.redirectUri, "https://h.example/api/auth/callback");
});

test("only DEX_UI_REDIRECT_URI: its origin is the public origin", () => {
  const cfg = resolveDexConfig(neverCalled, { DEX_UI_REDIRECT_URI: "https://h.example:8443/api/auth/callback" });
  assert.equal(cfg.publicOrigin, "https://h.example:8443");
});

test("env unset: request origin and the in-cluster Dex default", () => {
  const cfg = resolveDexConfig(fromRequest, {});
  assert.deepEqual(cfg, {
    publicOrigin: "http://localhost:3000",
    redirectUri: "http://localhost:3000/api/auth/callback",
    tokenUrl: DEFAULT_DEX_TOKEN_URL,
  });
  assert.equal(DEFAULT_DEX_TOKEN_URL, "http://home-os-dex:5556/dex/token");
});

test("malformed or non-http env values are ignored", () => {
  for (const bad of ["", "  ", "not a url", "javascript:alert(1)", "ftp://h.example/cb"]) {
    const cfg = resolveDexConfig(fromRequest, { DEX_UI_REDIRECT_URI: bad, PUBLIC_URL: bad });
    assert.equal(cfg.publicOrigin, "http://localhost:3000", JSON.stringify(bad));
    assert.equal(cfg.redirectUri, "http://localhost:3000/api/auth/callback", JSON.stringify(bad));
  }
  assert.equal(dexTokenUrl({ DEX_TOKEN_URL: " " }), DEFAULT_DEX_TOKEN_URL);
});

test("returnPath keeps same-origin paths and rejects the rest", () => {
  const cases = [
    [null, "/dashboard"],
    ["", "/dashboard"],
    ["/", "/dashboard"],
    ["/dashboard/bills?month=2026-11", "/dashboard/bills?month=2026-11"],
    ["//evil.example/x", "/evil.example/x"],
    ["/\\evil.example", "/evil.example"],
    ["\\\\evil.example", "/evil.example"],
    ["https://evil.example/", "/dashboard"],
    ["javascript:alert(1)", "/dashboard"],
    ["/\t/evil.example", "/dashboard"],
    ["/\n/evil.example", "/dashboard"],
    ["/login?redirect=/x", "/dashboard"],
    ["/api/auth/login", "/dashboard"],
    ["/auth/complete?to=/dashboard", "/dashboard"],
    ["/auth/complete", "/dashboard"],
    ["/authx", "/authx"],
    ["/loginx", "/loginx"],
    ["/" + "a".repeat(3000), "/dashboard"],
  ];
  for (const [input, want] of cases) assert.equal(returnPath(input), want, JSON.stringify(input));
});

test("returnUrl never leaves the public origin", () => {
  const origin = "https://h.example";
  for (const input of ["//evil.example", "/\\evil.example", "/\t/evil.example", "https://evil.example", "/%2F/evil.example"]) {
    assert.equal(returnUrl(origin, input).origin, origin, JSON.stringify(input));
  }
  assert.equal(returnUrl(origin, "/bills?x=1").href, "https://h.example/bills?x=1");
  assert.equal(returnUrl(origin, null).href, "https://h.example/dashboard");
});

test("completionUrl always lands on the app's own completion page", () => {
  const origin = "https://h.example";
  for (const input of [null, "", "/", "//evil.example", "/\\evil.example", "https://evil.example", "/login", "/api/auth/callback"]) {
    const url = completionUrl(origin, input);
    assert.equal(url.origin, origin, JSON.stringify(input));
    assert.equal(url.pathname, AUTH_COMPLETE_PATH, JSON.stringify(input));
    // ?to= is always a same-origin path: the completion page forwards to it.
    const to = url.searchParams.get("to");
    assert.ok(to && to.startsWith("/") && !to.startsWith("//"), `${JSON.stringify(input)} -> ${to}`);
  }
});

test("completionUrl keeps the destination, query and hash, and never the code", () => {
  const url = completionUrl("https://h.example", "/dashboard/calendar?view=week#today");
  assert.equal(url.pathname, "/auth/complete");
  assert.equal(url.searchParams.get("to"), "/dashboard/calendar?view=week#today");
  // The callback's query is not carried over: no code, state or verifier is in
  // the URL the browser keeps in history (#5304 security requirement).
  for (const param of ["code", "state", "code_verifier"]) {
    assert.equal(url.searchParams.get(param), null, param);
  }
});

test("completionUrl cannot be aimed at another origin through ?to=", () => {
  for (const evil of ["//evil.example/x", "/\\evil.example", "https://evil.example/", "/\t/evil.example"]) {
    const to = completionUrl("https://h.example", evil).searchParams.get("to");
    assert.equal(new URL(to, "https://h.example").origin, "https://h.example", JSON.stringify(evil));
  }
});

test("PKCE cookie round-trips, with and without a return path", () => {
  const withPath = { state: "s1", verifier: "v-_1", returnPath: "/bills?a=1.2" };
  assert.deepEqual(decodePkceCookie(encodePkceCookie(withPath)), withPath);
  const without = { state: "s1", verifier: "v1", returnPath: null };
  assert.equal(encodePkceCookie(without), "s1.v1"); // pre-#4617 format
  assert.deepEqual(decodePkceCookie("s1.v1"), without);
  for (const bad of [undefined, "", "s1", ".v1", "s1."]) assert.equal(decodePkceCookie(bad), null, JSON.stringify(bad));
});
