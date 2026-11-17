// Run: npm test. The service worker's caching policy (#4648): authenticated
// responses must never be cached, and only static assets may be. The built
// worker is checked separately, in app/pwa-caching.test.mjs.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { createRequire } from "node:module";

const here = new URL(".", import.meta.url).pathname;
const ui = join(here, "..", "..");
const policy = createRequire(import.meta.url)("./caching-policy.js");
const { NEVER_CACHED_PREFIXES, CACHE_NAMES, isNeverCachedPath, runtimeCaching, matchingRule, cacheNameFor } = policy;

const ORIGIN = "https://home.example";
const url = (path) => `${ORIGIN}${path}`;

/** Every URL class the app actually issues, plus the neighbours of each. */
const AUTHENTICATED = [
  // GET /bff — the whole authenticated API (the BFF attaches the session).
  "/bff/auth/me",
  "/bff/members",
  "/bff/bills?month=2026-11",
  "/bff/notes",
  "/bff/secrets/key",
  "/bff/files/3f8a/content",
  "/bff/files/3f8a/content?download=1",
  "/bff/notifications",
  "/bff", // the bare prefix must match too
  // /api — function routes, including the session endpoints themselves.
  "/api/auth/login",
  "/api/auth/callback?code=x&state=y",
  "/api/auth/logout",
  "/api",
];
const PAGE_URLS = [
  "/",
  "/dashboard",
  "/dashboard/bills?month=2026-11",
  "/dashboard/settings",
  "/login?redirect=%2Fdashboard%2Fbills",
  "/manifest.webmanifest",
  "/sw.js",
];
const STATIC = [
  "/_next/static/chunks/main-abc123.js",
  "/_next/static/css/app-def456.css",
  "/_next/static/media/FiraSans.woff2",
];
const PUBLIC_ASSETS = [
  "/icons/icon-192.png",
  "/icons/splash/apple-splash-1170x2532.png",
  "/favicon.ico",
  "/icons/icon.svg",
  "/manifest.json",
];

test("no /bff or /api URL is ever written to a cache", () => {
  for (const path of AUTHENTICATED) {
    const found = matchingRule(url(path));
    assert.ok(found, `${path} must match a rule (NetworkOnly), not fall through uncached-by-accident`);
    assert.equal(found.rule.handler, "NetworkOnly", `${path} must be NetworkOnly, got ${found.rule.handler}`);
    assert.equal(found.rule.options && found.rule.options.cacheName, undefined, `${path} must name no cache`);
    assert.equal(cacheNameFor(url(path)), null, `${path} must never be put in a cache`);
  }
});

test("the first rule is the /bff + /api NetworkOnly guard", () => {
  // If a caching rule is ever added above it, a future miss in that rule's own
  // URL test could reach /bff. Keep the guard first.
  const first = runtimeCaching[0];
  assert.equal(first.handler, "NetworkOnly");
  for (const path of ["/bff/x", "/bff", "/api/x", "/api"]) {
    assert.equal(matchingRule(url(path)).rule, first, `${path} resolves to rule 0`);
  }
});

test("the guard is repeated in every caching rule, so reordering cannot expose /bff", () => {
  const caching = runtimeCaching.filter((rule) => rule.handler !== "NetworkOnly");
  assert.ok(caching.length > 0);
  for (const rule of caching) {
    for (const path of AUTHENTICATED) {
      const parsed = new URL(url(path));
      assert.equal(
        !!rule.urlPattern({ url: parsed, request: { method: "GET", url: parsed.href }, sameOrigin: true }),
        false,
        `${rule.options.cacheName} must not match ${path} even on its own`,
      );
    }
  }
});

test("prefix matching is exact: /bfffoo and /apis are not the API", () => {
  // The guard must not swallow a legitimate static path by loose prefixing...
  assert.equal(isNeverCachedPath("/bff/x"), true);
  assert.equal(isNeverCachedPath("/bff"), true);
  assert.equal(isNeverCachedPath("/api/auth/logout"), true);
  assert.equal(isNeverCachedPath("/bfffoo"), false);
  assert.equal(isNeverCachedPath("/apis/chunk.js"), false);
});

test("static build output is cached, immutably", () => {
  for (const path of STATIC) {
    assert.equal(cacheNameFor(url(path)), "home-os-static", `${path} served from home-os-static`);
  }
  assert.equal(matchingRule(url(STATIC[0])).rule.handler, "CacheFirst");
});

test("public image assets and the install manifest are cached", () => {
  for (const path of PUBLIC_ASSETS) {
    assert.equal(cacheNameFor(url(path)), "home-os-assets", `${path} served from home-os-assets`);
  }
});

test("the Google Fonts CDN is cached; other cross-origin requests are not", () => {
  assert.equal(cacheNameFor("https://fonts.googleapis.com/css2?family=Fira+Sans"), "home-os-font-styles");
  assert.equal(cacheNameFor("https://fonts.gstatic.com/s/firasans/v1/a.woff2"), "home-os-font-files");
  assert.equal(cacheNameFor("https://analytics.example/collect"), null);
  assert.equal(cacheNameFor("https://evil.example/manifest.json"), null);
});

test("pages and documents are not cached at all (that is the offline shell, #4538)", () => {
  for (const path of PAGE_URLS) {
    assert.equal(matchingRule(url(path)), null, `${path} must not be intercepted`);
    assert.equal(cacheNameFor(url(path)), null, `${path} must not be cached`);
  }
});

test("every cacheName is on the allowlist, and nothing writes to next-pwa's default caches", () => {
  // "others" and "apis" are where the pre-#4648 build leaked /bff responses;
  // they must not be usable by name any more.
  assert.deepEqual([...CACHE_NAMES].sort(), [
    "home-os-assets",
    "home-os-font-files",
    "home-os-font-styles",
    "home-os-static",
  ]);
  for (const rule of runtimeCaching) {
    const name = rule.options && rule.options.cacheName;
    if (name === undefined) continue;
    assert.ok(CACHE_NAMES.includes(name), `${name} is on the allowlist`);
    assert.ok(!["others", "apis", "start-url", "next-data", "cross-origin"].includes(name), `${name} is not a next-pwa default`);
  }
});

test("no NetworkOnly rule leaves a cacheName behind (an unused cache is still a cache)", () => {
  for (const rule of runtimeCaching.filter((r) => r.handler === "NetworkOnly")) {
    assert.equal(rule.options, undefined, "NetworkOnly takes no options");
  }
});

test("every rule restricts itself to GET (a write is never cached)", () => {
  // Workbox's runtimeCaching entries default to GET, and next-pwa passes them
  // through untouched, so a rule cannot accidentally cache a POST/PATCH.
  for (const rule of runtimeCaching) {
    assert.ok(rule.method === undefined || rule.method === "GET", `${rule.handler} is GET-only`);
  }
});

test("the worker's allowlist matches CACHE_NAMES (worker/index.js runs in the SW)", () => {
  // worker/index.js is plain JS compiled by next-pwa with webpack + next/babel,
  // so it cannot import this CommonJS module; the two lists have to be kept in
  // step by hand and this is the reminder.
  const worker = readFileSync(join(ui, "worker", "index.js"), "utf8");
  const match = worker.match(/const ALLOWED_CACHES = \[([\s\S]*?)\];/);
  assert.ok(match, "worker/index.js declares ALLOWED_CACHES");
  const names = [...match[1].matchAll(/"([^"]+)"/g)].map((m) => m[1]);
  assert.deepEqual(names.sort(), [...CACHE_NAMES].sort());
});

test("NEVER_CACHED_PREFIXES is what the rules guard on", () => {
  assert.deepEqual(NEVER_CACHED_PREFIXES, ["/bff", "/api"]);
});
