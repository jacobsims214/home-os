/**
 * The service worker's Cache Storage policy (#4648).
 *
 * THE RULE: authenticated responses are never cached. `/bff/*` is the whole
 * authenticated API surface (the BFF attaches the httpOnly session token out of
 * the request's cookies) and `/api/*` carries the session routes, so both are
 * NetworkOnly: Workbox neither reads them from a cache nor writes them to one.
 *
 * Why this exists: next-pwa's default runtime caching (next-pwa/cache.js) ends
 * with a same-origin catch-all — cacheName "others", NetworkFirst, 24 h,
 * networkTimeoutSeconds 10 — that excludes only `/api/`. Every `/bff/*` GET
 * therefore landed in Cache Storage and was served from it whenever the network
 * took more than 10 s or the device was offline. Workbox ignores
 * `Cache-Control: no-store`, so household data (members, bills, notes,
 * `/bff/secrets/key`, file downloads, `/bff/auth/me`) survived logout on a
 * shared device: a tablet on the kitchen counter served the last person's data
 * to the next one.
 *
 * So: an explicit allowlist and no catch-all. Static assets only, plus the
 * public install manifest and the Google Fonts CDN. A URL that is not listed is
 * not intercepted at all — the browser's own HTTP cache still applies, which is
 * what `Cache-Control` on the response is for.
 *
 * WHY THE MATCHERS ARE SELF-CONTAINED. workbox-build copies each `urlPattern`
 * function into public/sw.js by calling `.toString()`, so a matcher may not
 * reference anything from this module (no imports, no closed-over constants):
 * the generated worker would throw a ReferenceError on its first fetch. Each
 * matcher therefore repeats the `/bff` + `/api` guard inline. That guard is
 * belt and braces on top of rule 1: if these rules are ever reordered, or a new
 * caching rule is added above them, a `/bff` URL still cannot be cached.
 *
 * Guards: lib/pwa/caching-policy.test.mjs (the rule list, every URL class) and
 * app/pwa-caching.test.mjs (the built public/sw.js). See KB
 * architecture/pwa-caching-policy.md.
 */

"use strict";

/**
 * Path prefixes that are never stored in Cache Storage. `/bff` is the
 * authenticated API; `/api` is the auth and app route surface (`/api/auth/*`
 * issues and ends sessions). next-pwa's own default excluded `/api/auth/*` from
 * its `apis` cache and then let the catch-all take it — so login and logout
 * responses were cached too.
 */
const NEVER_CACHED_PREFIXES = ["/bff", "/api"];

/** The only cache names the service worker may create. */
const CACHE_NAMES = [
  "home-os-static",
  "home-os-assets",
  "home-os-font-styles",
  "home-os-font-files",
];

/** True for a pathname under /bff or /api (or exactly those). */
function isNeverCachedPath(pathname) {
  return NEVER_CACHED_PREFIXES.some((p) => pathname === p || pathname.startsWith(`${p}/`));
}

/**
 * The array handed to next-pwa as `runtimeCaching`. Order matters: Workbox
 * tries routes in registration order and the first match wins.
 */
const runtimeCaching = [
  // 1. The authenticated API and every /api route: network only. No cacheName,
  //    no expiration — Workbox writes nothing and reads nothing.
  {
    urlPattern: ({ url }) => {
      const p = url.pathname;
      return p === "/bff" || p.startsWith("/bff/") || p === "/api" || p.startsWith("/api/");
    },
    handler: "NetworkOnly",
  },
  // 2. Next's content-hashed build output. Immutable: the filename changes with
  //    the content, so CacheFirst cannot serve a stale app.
  {
    urlPattern: ({ url, sameOrigin }) => {
      if (!sameOrigin) return false;
      const p = url.pathname;
      if (p === "/bff" || p.startsWith("/bff/") || p === "/api" || p.startsWith("/api/")) return false;
      return p.startsWith("/_next/static/");
    },
    handler: "CacheFirst",
    options: {
      cacheName: "home-os-static",
      expiration: { maxEntries: 64, maxAgeSeconds: 30 * 24 * 60 * 60 },
    },
  },
  // 3. Public image assets: the icon set and iOS launch screens (#4533), the
  //    favicon and the install manifest. No user data, and the paths are fixed.
  //    Same-origin only, so a third-party /manifest.json or /logo.png is not
  //    pulled into our cache.
  {
    urlPattern: ({ url, sameOrigin }) => {
      if (!sameOrigin) return false;
      const p = url.pathname;
      if (p === "/bff" || p.startsWith("/bff/") || p === "/api" || p.startsWith("/api/")) return false;
      if (p === "/manifest.json") return true;
      return /\.(?:png|jpe?g|gif|svg|ico|webp|avif)$/i.test(p);
    },
    handler: "StaleWhileRevalidate",
    options: {
      cacheName: "home-os-assets",
      expiration: { maxEntries: 64, maxAgeSeconds: 30 * 24 * 60 * 60 },
    },
  },
  // 4. The Google Fonts stylesheet app/layout.tsx links directly. Matched by
  //    hostname, so this is the only cross-origin rule — and it names one host
  //    and one resource type, not "anything not same-origin" (which is what
  //    next-pwa's `cross-origin` catch-all did).
  {
    urlPattern: ({ url }) => url.hostname === "fonts.googleapis.com",
    handler: "StaleWhileRevalidate",
    options: {
      cacheName: "home-os-font-styles",
      expiration: { maxEntries: 4, maxAgeSeconds: 7 * 24 * 60 * 60 },
    },
  },
  // 5. The font files themselves. Public, immutable per URL.
  {
    urlPattern: ({ url }) => url.hostname === "fonts.gstatic.com",
    handler: "CacheFirst",
    options: {
      cacheName: "home-os-font-files",
      expiration: { maxEntries: 8, maxAgeSeconds: 365 * 24 * 60 * 60 },
    },
  },
];

/**
 * The first rule matching `url`, or null when nothing does — and "nothing"
 * means the request is not intercepted at all, which is the right answer for
 * every page and data URL.
 *
 * Follows workbox-routing's Router: first match wins; a function capture is a
 * match when it returns something truthy. The Router passes `sameOrigin`
 * (url.origin === the worker's origin) to every matcher, so it is computed here
 * the same way against `origin`.
 */
function matchingRule(url, rules = runtimeCaching, origin = "https://home.example") {
  const parsed = typeof url === "string" ? new URL(url, origin) : url;
  const sameOrigin = parsed.origin === origin;
  for (const [index, rule] of rules.entries()) {
    const pattern = rule.urlPattern;
    let matched = false;
    if (typeof pattern === "function") {
      matched = !!pattern({ url: parsed, request: { method: "GET", url: parsed.href }, sameOrigin });
    } else if (pattern instanceof RegExp) {
      matched = pattern.test(parsed.href);
    } else if (typeof pattern === "string") {
      matched = parsed.href === new URL(pattern, parsed.origin).href;
    }
    if (matched) return { rule, index };
  }
  return null;
}

/** The cache a URL would be written to, or null when it is not cached at all. */
function cacheNameFor(url, rules = runtimeCaching, origin) {
  const found = matchingRule(url, rules, origin);
  if (!found || found.rule.handler === "NetworkOnly") return null;
  return (found.rule.options && found.rule.options.cacheName) || null;
}

module.exports = {
  NEVER_CACHED_PREFIXES,
  CACHE_NAMES,
  isNeverCachedPath,
  runtimeCaching,
  matchingRule,
  cacheNameFor,
};
