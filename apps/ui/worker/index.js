/**
 * Home OS service worker — the part that is not Workbox (#4648, #4538).
 *
 * next-pwa imports this file at the top of public/sw.js (importScripts, before
 * the Workbox routes), so the listeners below are registered alongside them.
 * Plain JS with no imports on purpose: next-pwa compiles this file with
 * webpack + next/babel, and a bare relative import of the CommonJS policy
 * module is not part of that build.
 *
 * Jobs:
 *
 * 1. activate: delete every Cache Storage cache whose name is neither ours nor
 *    Workbox's precache. Before #4648 the Workbox defaults wrote authenticated
 *    `/bff/*` responses into caches called "apis" and "others", and those
 *    survived logout. A device that upgrades and stays signed in never runs the
 *    page's purge, so the worker removes them itself as soon as it takes over.
 *    Workbox's precache is KEPT (#4538): it is a build artifact — the offline
 *    shell and the static build output — and deleting it would leave the device
 *    with no shell the moment the worker took over.
 * 2. message PURGE_CACHES: same deletion on demand, answered with what went.
 *    The app does not depend on this — the page can do it itself, and must, for
 *    a logout (lib/session-teardown.ts) — but it makes the worker's half
 *    inspectable.
 * 3. message SKIP_WAITING (#4538): the update flow's second half. A new build's
 *    worker waits (skipWaiting is off in next.config.js) instead of replacing
 *    the shell under someone mid-task; the app shows "reload to update" and
 *    posts this when the person taps it.
 * 4. fetch (#4538): the OFFLINE SHELL. A navigation that cannot reach the
 *    network is answered with the precached public/offline.html — the app frame
 *    and an honest "you're offline", not the browser's error page. It is a
 *    fallback, never a cache: no navigation is ever written to Cache Storage,
 *    and `/bff` + `/api` are not navigations, so they never come near it
 *    (#4648's NetworkOnly rules still own them).
 *
 * KEEP IN STEP: ALLOWED_CACHES must match CACHE_NAMES in lib/pwa/caching-policy.js,
 * and OFFLINE_SHELL_PATH must match OFFLINE_SHELL_PATH in lib/pwa/offline.js.
 * lib/pwa/caching-policy.test.mjs reads this file and fails if they differ.
 * Both strings are repeated here rather than imported: webpack compiles this
 * file on its own, so it cannot require the CommonJS modules beside the app.
 */

// eslint-disable-next-line no-undef
const ALLOWED_CACHES = ["home-os-static", "home-os-assets", "home-os-font-styles", "home-os-font-files"];

/**
 * Workbox's precache (v6 names it `workbox-precache-v2-<scope>`). Not ours to
 * write, but it IS ours to keep: it holds the build's static output and the
 * offline shell, and next-pwa regenerates it on every deploy.
 */
const PRECACHE_PREFIX = "workbox-precache";

/** The offline shell, precached from public/ (#4538). */
const OFFLINE_SHELL_PATH = "/offline.html";

/** Must match SKIP_WAITING_MESSAGE in lib/pwa/offline.js. */
const SKIP_WAITING_MESSAGE = "SKIP_WAITING";

/** Caches the purge must leave alone: ours, plus Workbox's precache. */
function isKeptCache(name) {
  return ALLOWED_CACHES.indexOf(name) !== -1 || name.indexOf(PRECACHE_PREFIX) === 0;
}

// eslint-disable-next-line no-undef
self.addEventListener("activate", (event) => {
  event.waitUntil(purgeUnknownCaches());
});

// eslint-disable-next-line no-undef
self.addEventListener("message", (event) => {
  const data = event.data || {};

  if (data.type === SKIP_WAITING_MESSAGE) {
    // The update flow's second half (#4538). Called only from the app's
    // "Reload to update" button, so the swap happens when the person chose it,
    // never silently mid-task. activate then takes control of the open clients
    // (clientsClaim is on in next.config.js).
    // eslint-disable-next-line no-undef
    self.skipWaiting();
    return;
  }

  if (data.type !== "PURGE_CACHES") return;
  const done = purgeUnknownCaches().then((deleted) => {
    if (event.ports && event.ports[0]) event.ports[0].postMessage({ deleted });
  });
  if (event.waitUntil) event.waitUntil(done);
});

/**
 * Deletes caches we do not own and returns their names. Never rejects: a failed
 * delete must not stop the worker activating.
 */
async function purgeUnknownCaches() {
  const deleted = [];
  try {
    const names = await caches.keys();
    for (const name of names) {
      if (isKeptCache(name)) continue;
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

/**
 * The offline shell (#4538).
 *
 * Registered before Workbox's routes and deliberately NOT a Workbox route:
 * Workbox's routing table is the caching policy (#4648), and a route there
 * would look like a rule that caches pages. This is a plain fetch listener that
 * is a no-op unless the request is a navigation.
 *
 *   GET navigation -> network. Nothing is stored on the way through.
 *                     On a network error, the precached shell.
 *
 * What it must never do, and does not: handle a non-GET (a write is the app's
 * to fail loudly), handle a `/bff` or `/api` URL (NetworkOnly owns them), or
 * put anything in a cache (`caches.put` is not called).
 */
// eslint-disable-next-line no-undef
self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (!request || request.method !== "GET") return;
  if (!isNavigation(request)) return;
  if (isDataPath(request.url)) return;
  event.respondWith(networkThenShell(request));
});

/** A top-level page navigation. Browsers set mode "navigate"; the Accept header
 *  is the same fallback Workbox's createHandlerBoundToURL classifies on. */
function isNavigation(request) {
  if (request.mode === "navigate") return true;
  const accept = (request.headers && request.headers.get && request.headers.get("accept")) || "";
  return /\btext\/html\b/i.test(accept);
}

/** True for /bff or /api (with or without a query), the authenticated API. */
function isDataPath(href) {
  let path = href;
  try {
    path = new URL(href).pathname;
  } catch (err) {
    const q = path.indexOf("?");
    if (q !== -1) path = path.slice(0, q);
  }
  return (
    path === "/bff" ||
    path.indexOf("/bff/") === 0 ||
    path === "/api" ||
    path.indexOf("/api/") === 0 ||
    path === OFFLINE_SHELL_PATH
  );
}

/**
 * Fetches the navigation; on a network error serves the precached shell. A real
 * HTTP answer (including a 4xx/5xx or a redirect) is returned untouched — this
 * is a fallback for "the network is gone", not an error page for the app.
 */
async function networkThenShell(request) {
  try {
    return await fetch(request);
  } catch (err) {
    const shell = await matchShell();
    if (shell) return shell;
    throw err;
  }
}

/**
 * The precached shell. Searched across every cache (Workbox's precache name is
 * versioned and includes the scope, so it cannot be named here), ignoring the
 * query: `/dashboard?x=1` offline and `/dashboard` offline want the same
 * document. Returns null when nothing is cached, so the caller rethrows the
 * network error instead of inventing a response.
 */
async function matchShell() {
  try {
    const direct = await caches.match(OFFLINE_SHELL_PATH, { ignoreSearch: true });
    if (direct) return direct;
    const origin = self.origin;
    const names = await caches.keys();
    for (const name of names) {
      if (name.indexOf(PRECACHE_PREFIX) !== 0) continue;
      const cache = await caches.open(name);
      const hit = await cache.match(new URL(OFFLINE_SHELL_PATH, origin).href, { ignoreSearch: true });
      if (hit) return hit;
    }
  } catch (err) {
    // No Cache Storage, or the shell never made it into a precache: fall through
    // to the network error, which is honest.
  }
  return null;
}
