/**
 * Home OS service worker — the part that is not Workbox (#4648).
 *
 * next-pwa imports this file at the top of public/sw.js (importScripts, before
 * the Workbox routes), so the listeners below are registered alongside them.
 * Plain JS with no imports on purpose: next-pwa compiles this file with
 * webpack + next/babel, and a bare relative import of the CommonJS policy
 * module is not part of that build.
 *
 * Two jobs, both about caches the app should not be holding:
 *
 * 1. activate: delete every Cache Storage cache whose name is not in the
 *    allowlist. Before #4648 the Workbox defaults wrote authenticated `/bff/*`
 *    responses into caches called "apis" and "others", and those survived
 *    logout. A device that upgrades and stays signed in never runs the page's
 *    purge, so the worker removes them itself as soon as it takes over
 *    (skipWaiting + clientsClaim, both on in next.config.js).
 * 2. message PURGE_CACHES: same deletion on demand, answered with what went.
 *    The app does not depend on this — the page can do it itself, and must, for
 *    a logout (lib/session-teardown.ts) — but it makes the worker's half
 *    inspectable.
 *
 * KEEP IN STEP: this list must match CACHE_NAMES in lib/pwa/caching-policy.js.
 * lib/pwa/caching-policy.test.mjs reads this file and fails if they differ.
 */

// eslint-disable-next-line no-undef
const ALLOWED_CACHES = ["home-os-static", "home-os-assets", "home-os-font-styles", "home-os-font-files"];

// eslint-disable-next-line no-undef
self.addEventListener("activate", (event) => {
  event.waitUntil(purgeUnknownCaches());
});

// eslint-disable-next-line no-undef
self.addEventListener("message", (event) => {
  const data = event.data || {};
  if (data.type !== "PURGE_CACHES") return;
  const done = purgeUnknownCaches().then((deleted) => {
    if (event.ports && event.ports[0]) event.ports[0].postMessage({ deleted });
  });
  if (event.waitUntil) event.waitUntil(done);
});

/**
 * Deletes caches we do not own and returns their names. Never rejects: a
 * failed delete must not stop the worker activating.
 */
async function purgeUnknownCaches() {
  const deleted = [];
  try {
    const names = await caches.keys();
    for (const name of names) {
      if (ALLOWED_CACHES.indexOf(name) !== -1) continue;
      try {
        if (await caches.delete(name)) deleted.push(name);
      } catch (err) {
        // Keep going: one stuck cache does not excuse the rest.
      }
    }
  } catch (err) {
    // No Cache Storage (or it is blocked): nothing to clean.
  }
  return deleted;
}
