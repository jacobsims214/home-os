/** @type {import('next').NextConfig} */

// Service worker caching policy (#4648): an explicit allowlist in
// lib/pwa/caching-policy.js. The next-pwa defaults (next-pwa/cache.js) end with
// a same-origin catch-all that cached every `/bff/*` GET — authenticated
// household data — and served it again after logout on a shared device. Never
// fall back to them: `runtimeCaching` is always passed explicitly.
const { runtimeCaching } = require("./lib/pwa/caching-policy.js");

const withPWA = require("next-pwa")({
  dest: "public",
  disable: process.env.NODE_ENV === "development",
  // register: false (#4538). next-pwa's own registration is injected into the
  // PAGES-router `main.js` entry, which an App Router build never loads
  // (`main-app-*.js` does), so the worker was never registered at all — found
  // while testing #4648. lib/pwa/service-worker.ts registers it from the app
  // instead, which is also what lets the update flow be the app's: next-pwa's
  // register.js also adds `reloadOnOnline` (a `location.reload()` whenever the
  // connection returns), which would pull the app out from under someone
  // mid-task. The offline shell (#4538) needs the worker to be real.
  register: false,
  // skipWaiting: false (#4538). A new build's worker WAITS instead of taking
  // over. The app shows "Reload to update" and posts SKIP_WAITING when the
  // person taps it (lib/pwa/service-worker.ts), so a deploy never swaps the
  // shell under someone in the middle of a form. Deliberately not serving a
  // stale shell forever either — the offer is made as soon as the new worker
  // is installed.
  skipWaiting: false,
  // Only the rules above. No catch-all, so a page navigation or an API call is
  // not cached unless a rule names it.
  runtimeCaching,
  // next-pwa's register.js caches outside runtimeCaching, so all three of these
  // are off — whatever gets stored must come through the policy above:
  // - dynamicStartUrl on registers a NetworkFirst rule for "/" named start-url
  //   (unshifted ahead of every rule), and rides __PWA_START_URL__ into
  //   register.js, which put()s a fetched "/" response into the same cache;
  // - cacheStartUrl on adds "/" to the precache manifest by build id;
  // - cacheOnFrontEndNav on wraps pushState/replaceState and cache.add()s
  //   whatever URL was navigated to into a cache named "others".
  // All three store page URLs, none of them consults `runtimeCaching`, and
  // "others" is the very cache that was holding `/bff` responses (#4648).
  // (A cached page would also be the stale, possibly-wrong screen #4538 rules
  // out; the offline shell is a FALLBACK served by worker/index.js, not a cache.)
  dynamicStartUrl: false,
  cacheStartUrl: false,
  cacheOnFrontEndNav: false,
  // next-pwa 5.6 predates the App Router: it precaches every build asset
  // including `/_next/app-build-manifest.json`, which Next 14 does NOT serve
  // (only /_next/static/* is public; the file exists in .next/ but 404s over
  // HTTP). Workbox fails the WHOLE install on one bad precache URL
  // ("bad-precaching-response"), so the worker was discarded as redundant on
  // every load and #4648's caching policy could never have taken effect. This
  // keeps it out of the precache manifest; nothing in the app requests it.
  // (Found while testing #4648; #4538's offline shell needs the worker to
  // install at all.)
  buildExcludes: [/app-build-manifest\.json$/],
  // public/offline.html (#4538) is the offline shell, and it reaches the device
  // through next-pwa's default precache glob over public/ (it must: passing
  // `additionalManifestEntries` would REPLACE that glob, and with it the icon
  // set from #4533). It is not a route and not a cached page — worker/index.js
  // serves it only after a navigation's network attempt has failed.
  // app/offline-shell.test.mjs fails if the built worker's manifest drops it.
});

const nextConfig = {
  reactStrictMode: true,
  output: "standalone",
};

module.exports = withPWA(nextConfig);
