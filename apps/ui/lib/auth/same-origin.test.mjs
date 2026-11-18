// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import { isSameOrigin } from "./same-origin.ts";

const req = (headers) => ({ headers: new Headers(headers) });
const noEnv = { env: {} };
const publicUrl = { env: { PUBLIC_URL: "https://home.example/" } };

test("same-origin browser requests pass", () => {
  assert.ok(isSameOrigin(req({ host: "home.example", origin: "https://home.example", "sec-fetch-site": "same-origin" }), noEnv));
  assert.ok(isSameOrigin(req({ host: "home.example", origin: "https://home.example" }), noEnv));
  assert.ok(isSameOrigin(req({ host: "home.example", "sec-fetch-site": "same-origin" }), noEnv));
  assert.ok(isSameOrigin(req({ host: "home.example", "sec-fetch-site": "none" }), noEnv));
});

test("Sec-Fetch-Site cross-site or same-site is refused, whatever Origin says", () => {
  for (const site of ["cross-site", "same-site", "bogus"]) {
    assert.equal(isSameOrigin(req({ host: "home.example", origin: "https://home.example", "sec-fetch-site": site }), noEnv), false, site);
  }
});

test("a foreign, null or malformed Origin is refused", () => {
  for (const origin of ["https://evil.example", "https://app.home.example", "null", "not a url"]) {
    assert.equal(isSameOrigin(req({ host: "home.example", origin }), noEnv), false, origin);
  }
});

test("no PUBLIC_URL: Origin host is compared with the first X-Forwarded-Host, else Host", () => {
  const behindProxy = { host: "home-os-ui:3000", "x-forwarded-host": "home.example, internal", origin: "https://home.example" };
  assert.ok(isSameOrigin(req(behindProxy), noEnv));
  assert.ok(isSameOrigin(req({ host: "Home.Example:8443", origin: "https://home.example:8443" }), noEnv));
  assert.equal(isSameOrigin(req({ host: "home.example:8443", origin: "https://home.example" }), noEnv), false);
  assert.equal(isSameOrigin(req({ origin: "https://home.example" }), noEnv), false, "no host at all");
});

test("PUBLIC_URL set: Origin must equal its origin; Host headers do not matter", () => {
  assert.ok(isSameOrigin(req({ host: "home-os-ui:3000", origin: "https://home.example" }), publicUrl));
  assert.equal(isSameOrigin(req({ host: "evil.example", origin: "https://evil.example" }), publicUrl), false);
  assert.equal(isSameOrigin(req({ host: "home.example", origin: "http://home.example" }), publicUrl), false, "scheme counts");
});

test("a malformed or non-http PUBLIC_URL is ignored", () => {
  for (const PUBLIC_URL of ["not a url", "ftp://home.example"]) {
    assert.ok(isSameOrigin(req({ host: "home.example", origin: "https://home.example" }), { env: { PUBLIC_URL } }), PUBLIC_URL);
  }
});

test("neither Origin nor Sec-Fetch-Site: refused by default, allowed only on request", () => {
  assert.equal(isSameOrigin(req({ host: "home.example" }), noEnv), false);
  assert.equal(isSameOrigin(req({ host: "home.example" }), { ...noEnv, allowHeaderless: true }), true);
});

test("allowHeaderless does not loosen the checks when a header is present", () => {
  const opts = { ...noEnv, allowHeaderless: true };
  assert.equal(isSameOrigin(req({ host: "home.example", origin: "https://evil.example" }), opts), false);
  assert.equal(isSameOrigin(req({ host: "home.example", "sec-fetch-site": "cross-site" }), opts), false);
});
