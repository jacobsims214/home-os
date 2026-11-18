/**
 * The offline shell and the update flow (#4538).
 *
 * Two separate problems, and they must not solve each other:
 *
 * 1. THE OFFLINE SHELL. With the network gone a navigation must still render
 *    something usable — the app frame and an honest "you're offline" message —
 *    instead of the browser's own error page. That shell is
 *    `public/offline.html`: a static, self-contained document that next-pwa
 *    precaches (so it is on the device after one visit) and that the worker
 *    serves when a navigation fails. See worker/index.js for the fetch
 *    listener, and the section below for why it is not a cached page.
 *
 * 2. THE UPDATE FLOW. A new build's worker must not take over under someone
 *    mid-task, and must not be ignored forever either: it waits, the app says
 *    "reload to update", and the person's tap is what swaps it
 *    (`skipWaiting: false` in next.config.js + applyUpdate() here).
 *
 * WHY THE SHELL IS A FALLBACK AND NOT A CACHED PAGE. #4648 established that no
 * page request is intercepted: `runtimeCaching` in caching-policy.js has no rule
 * that matches a navigation, and its last rule is static assets. A page
 * response that WAS stored would be the cached-and-maybe-wrong screen this
 * ticket explicitly rules out — "a cached 'Electric bill due' that has since
 * been paid is worse than no answer". So:
 *
 *   - no navigation is ever written to Cache Storage (the policy still holds);
 *   - `offline.html` is a PUBLIC ASSET on the precache list, which is a build
 *     artifact, not a household page;
 *   - the worker serves it only after the network has failed, and only for a
 *     navigation. Data requests (`/bff`, `/api`) never reach it: #4648's
 *     NetworkOnly guard owns them and no fallback applies to a non-navigation.
 *
 * WHAT IS DELIBERATELY NOT HERE. No data cache, no background sync, no write
 * queue. A queued write is the data-loss trap this ticket rules out: a note that
 * looks saved but never reached the server is worse between co-parents than an
 * honest failure. apiFetch() fails a write loudly while offline (lib/api.ts)
 * and nothing retries it behind the person's back.
 *
 * Plain CJS with no imports (like caching-policy.js next to it): webpack loads
 * it in the worker build and `node --test` loads it directly. Anything the
 * worker needs must also be self-contained there, which is why
 * OFFLINE_SHELL_PATH is repeated inside worker/index.js.
 *
 * Guards: lib/pwa/offline.test.mjs.
 */

"use strict";

/**
 * Where the offline shell lives. A public/ path, not a route: it has to be a
 * file next-pwa can precache at build time, and it must render with no server,
 * no JS bundle and no stylesheet from `/_next/static` (all of which are
 * unreachable when it is served).
 */
const OFFLINE_SHELL_PATH = "/offline.html";

/**
 * The caches the app owns. Mirrors CACHE_NAMES in caching-policy.js; the two are
 * asserted equal by lib/pwa/caching-policy.test.mjs, which reads this file too.
 * Kept here so the offline/update module can name them without importing the
 * policy module into a worker build.
 */
const OFFLINE_CACHE_NAMES = [
  "home-os-static",
  "home-os-assets",
  "home-os-font-styles",
  "home-os-font-files",
];

/**
 * True when a request is a page navigation. This is the ONLY class of request
 * the offline shell answers: a document request whose network attempt failed.
 * Everything else (fetch/XHR for `/bff`, images, scripts) either has a policy
 * rule or is not ours to handle.
 *
 * `request.mode === "navigate"` is what browsers set for a navigation; the
 * `Accept: text/html` branch covers a page fetched by hand, which Workbox's own
 * `createHandlerBoundToURL` classifies the same way.
 */
function isShellRequest(request) {
  if (!request) return false;
  if (request.method && request.method !== "GET") return false;
  if (request.mode === "navigate") return true;
  const headers = request.headers;
  const accept = (headers && headers.get && headers.get("accept")) || "";
  return /\btext\/html\b/i.test(accept);
}

/**
 * The update-flow message the app posts to a waiting worker. A constant so the
 * page half (lib/pwa/service-worker.ts) and worker/index.js cannot drift.
 */
const SKIP_WAITING_MESSAGE = "SKIP_WAITING";

/**
 * Whether `pathname` is something the worker must never answer with the shell.
 * Belt and braces on top of `isShellRequest`: a `/bff` URL is never a
 * navigation, and if it somehow were, the answer is the network's (or a 401),
 * never a page. Also keeps the shell from being served for itself.
 */
function isNeverShellPath(pathname, origin) {
  if (typeof pathname !== "string" || pathname === "") return false;
  let path = pathname;
  if (origin && path.startsWith(origin)) path = path.slice(origin.length) || "/";
  const q = path.indexOf("?");
  if (q !== -1) path = path.slice(0, q);
  return (
    path === "/bff" ||
    path.startsWith("/bff/") ||
    path === "/api" ||
    path.startsWith("/api/") ||
    path === OFFLINE_SHELL_PATH
  );
}

/**
 * What a write told the person when it could not be sent. It says BOTH halves:
 * that it failed, and that nothing was saved, so nobody goes looking for a note
 * that does not exist.
 */
function offlineWriteMessage() {
  return "You're offline, so that wasn't saved. Reconnect and try again.";
}

/** The message for a read that failed with the network down. */
function offlineReadMessage() {
  return "You're offline. Home OS can't load fresh data until you reconnect.";
}

module.exports = {
  OFFLINE_SHELL_PATH,
  OFFLINE_CACHE_NAMES,
  SKIP_WAITING_MESSAGE,
  isShellRequest,
  isNeverShellPath,
  offlineWriteMessage,
  offlineReadMessage,
};
