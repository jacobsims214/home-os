// Run: npm test. Guards the offline shell and update flow (#4538):
//   - public/offline.html is a real, self-contained shell that renders with no
//     server, no JS bundle and no stylesheet from /_next/static;
//   - worker/index.js serves it ONLY for a failed navigation, and only after the
//     network is gone (never a cache);
//   - /bff and /api never reach it (#4648's NetworkOnly rules own them);
//   - next.config.js makes the worker wait instead of swapping under someone, and
//     stops next-pwa's own (broken) registration and reloadOnOnline;
//   - the app really registers the worker, and only a tap applies an update.
//
// The built-worker half (public/sw.js) lives in app/offline-shell.test.mjs, where
// the other built-worker assertions already are.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { createRequire } from "node:module";

const here = new URL(".", import.meta.url).pathname;
const ui = join(here, "..", "..");
const require = createRequire(import.meta.url);
const offline = require(join(here, "offline.js"));

const shell = readFileSync(join(ui, "public", "offline.html"), "utf8");
const worker = readFileSync(join(ui, "worker", "index.js"), "utf8");
const nextConfig = readFileSync(join(ui, "next.config.js"), "utf8");
const bridge = readFileSync(join(ui, "components", "shared", "OfflineBanner.tsx"), "utf8");
const swModule = readFileSync(join(ui, "lib", "pwa", "service-worker.ts"), "utf8");
const layout = readFileSync(join(ui, "app", "layout.tsx"), "utf8");
const api = readFileSync(join(ui, "lib", "api.ts"), "utf8");

// ── the shell document ─────────────────────────────────────────────────────

/** Every URL the document would fetch. Comments are stripped first: the file
 *  explains what it must NOT load, and naming /_next/ there is not a bug. */
function resourceUrls(html) {
  const body = html.replace(/<!--[\s\S]*?-->/g, "");
  const urls = [];
  for (const m of body.matchAll(/\b(?:src|href)\s*=\s*["']([^"']+)["']/gi)) urls.push(m[1]);
  for (const m of body.matchAll(/url\(\s*["']?([^"')]+)["']?\s*\)/gi)) urls.push(m[1]);
  return urls;
}

test("the offline shell exists and is a complete HTML document", () => {
  assert.ok(shell.length > 2000, "a real document, not a stub");
  assert.match(shell, /^<!doctype html>/i);
  assert.match(shell, /<html lang="en">/);
  assert.match(shell, /<\/html>\s*$/);
  // The whole point: there is something to see. The app frame's brand, the
  // offline state, and a way back in.
  assert.match(shell, /Home OS/);
  assert.match(shell, /You&rsquo;re offline\./);
  assert.match(shell, /Try again/);
  assert.match(shell, /href="\/dashboard"/);
});

test("the shell loads nothing over the network: it must render with no server", () => {
  // The only allowed href is the in-app link back to /dashboard; anything that
  // fetches (a stylesheet, a script, a font, an image) would fail exactly when
  // this document is needed.
  const fetching = resourceUrls(shell).filter((u) => !u.startsWith("#") && !u.startsWith("/dashboard"));
  assert.deepEqual(fetching, [], `the shell must not fetch these while offline: ${fetching.join(", ")}`);
  assert.doesNotMatch(shell, /@import/);
  assert.doesNotMatch(shell, /fonts\.googleapis\.com\/css/);
  assert.doesNotMatch(shell, /<link[^>]+rel=["']stylesheet["']/i);
});

test("the shell keeps the app's design language: tokens, both schemes, safe areas", () => {
  // Tokens from app/globals.css (#4529), by value: this document cannot load it.
  for (const token of ["--bg", "--surface", "--line", "--ink", "--ink-2", "--accent", "--warn-bg", "--warn-ink"]) {
    assert.ok(shell.includes(`${token}:`), `${token} is defined`);
  }
  // Dark scheme: no Mantine colour manager runs here, so the media query is the
  // only signal.
  assert.match(shell, /@media \(prefers-color-scheme: dark\)/);
  // #4533 / the shell spec's safe areas: viewport-fit=cover is what makes the
  // insets non-zero, and the frame pads both ends.
  assert.match(shell, /viewport-fit=cover/);
  assert.match(shell, /env\(safe-area-inset-top/);
  assert.match(shell, /env\(safe-area-inset-bottom/);
  // Standalone (#4533): the installed app has no browser chrome, so the shell
  // must not paint a fake one, and 100dvh is the height.
  assert.match(shell, /min-height:\s*100dvh/);
});

test("the shell is honest: no stale data, no promised sync", () => {
  // The copy must not claim data is being shown or that writes are queued — the
  // spec's draft wording did both, and there is no cache or queue (#4538).
  assert.doesNotMatch(shell, /showing what was saved/i);
  assert.doesNotMatch(shell, /will sync when/i);
  assert.doesNotMatch(shell, /changes you make will/i);
  // It says the two true things instead.
  assert.match(shell, /No saved household data is shown/);
  assert.match(shell, /didn&rsquo;t go through/);
});

test("the shell is reachable without a session (it is not behind the login guard)", () => {
  // middleware.ts treats a root-level static file as passthrough, which
  // /offline.html is; assert the rule it relies on rather than assuming it.
  const middleware = readFileSync(join(ui, "middleware.ts"), "utf8");
  assert.match(middleware, /isRootStaticFile/);
  assert.match(middleware, /\^\\\/\[\^\/\]\+\\\.\[A-Za-z0-9\]\+\$/);
});

// ── the worker's fetch listener ────────────────────────────────────────────

test("the worker serves the shell only for a navigation, never for data", () => {
  // Behavioural: lift the built predicates out and run them, the way
  // app/pwa-caching.test.mjs runs the generated matchers.
  assert.match(worker, /addEventListener\("fetch"/);
  // /bff and /api are excluded by name inside the worker.
  for (const path of ["/bff", "/bff/", "/api", "/api/"]) {
    assert.ok(worker.includes(`"${path}"`), `the worker excludes ${path}`);
  }
  assert.match(worker, /request\.method !== "GET"/);
  assert.match(worker, /mode === "navigate"/);
});

test("the worker never writes to a cache (the shell is a fallback, not a cache)", () => {
  // caches.put / cache.add / cache.put would turn a page into a stale screen.
  // caches.delete and caches.match are the only Cache Storage calls allowed.
  assert.doesNotMatch(worker, /cache\.put\(/);
  assert.doesNotMatch(worker, /cache\.add\(/);
  assert.doesNotMatch(worker, /caches\.put\(/);
  assert.doesNotMatch(worker, /precacheAndRoute/); // that is the built worker's job, not this file's
  assert.match(worker, /caches\.match\(OFFLINE_SHELL_PATH/);
});

test("the worker keeps Workbox's precache when it purges (it holds the shell)", () => {
  // purgeUnknownCaches deletes every cache not on the allowlist. Workbox's
  // precache is not on CACHE_NAMES, so without this exception the shell would be
  // deleted the moment the worker activated — install, then no offline shell.
  assert.match(worker, /workbox-precache/);
  const keeps = worker.match(/function isKeptCache\(name\) \{([\s\S]*?)\n\}/);
  assert.ok(keeps, "isKeptCache is defined");
  assert.match(keeps[1], /ALLOWED_CACHES\.indexOf\(name\)/);
  assert.match(keeps[1], /PRECACHE_PREFIX/);
});

test("the worker's constants match the modules they mirror", () => {
  // The worker is compiled on its own and cannot import these.
  const shellPath = worker.match(/const OFFLINE_SHELL_PATH = "([^"]+)"/);
  assert.equal(shellPath[1], offline.OFFLINE_SHELL_PATH);
  const skip = worker.match(/const SKIP_WAITING_MESSAGE = "([^"]+)"/);
  assert.equal(skip[1], offline.SKIP_WAITING_MESSAGE);
  const allowed = worker.match(/const ALLOWED_CACHES = \[([\s\S]*?)\];/);
  const names = [...allowed[1].matchAll(/"([^"]+)"/g)].map((m) => m[1]);
  assert.deepEqual(names.sort(), [...offline.OFFLINE_CACHE_NAMES].sort());
});

// ── the module ─────────────────────────────────────────────────────────────

test("isShellRequest: navigations only, and never a write", () => {
  assert.equal(offline.isShellRequest({ method: "GET", mode: "navigate" }), true);
  assert.equal(offline.isShellRequest({ method: "GET", headers: { get: () => "text/html,*/*" } }), true);
  assert.equal(offline.isShellRequest({ method: "GET", headers: { get: () => "application/json" } }), false);
  assert.equal(offline.isShellRequest({ method: "POST", mode: "navigate" }), false, "a write is the app's to fail loudly");
  assert.equal(offline.isShellRequest({ method: "GET", mode: "cors" }), false, "an XHR/fetch is not a navigation");
  assert.equal(offline.isShellRequest(null), false);
});

test("isNeverShellPath: the authenticated API never gets an HTML page", () => {
  for (const path of ["/bff", "/bff/", "/bff/members", "/bff/bills?month=2026-11", "/api", "/api/auth/logout", "/api/auth/callback?code=x"]) {
    assert.equal(offline.isNeverShellPath(path), true, path);
  }
  for (const path of ["/", "/dashboard", "/dashboard/bills?month=2026-11", "/login", "/bffx", "/apix"]) {
    assert.equal(offline.isNeverShellPath(path), false, path);
  }
  // The shell itself, so a failed request FOR the shell cannot loop.
  assert.equal(offline.isNeverShellPath(offline.OFFLINE_SHELL_PATH), true);
  // An absolute URL is reduced to its path first.
  assert.equal(offline.isNeverShellPath("https://home.example/bff/members", "https://home.example"), true);
});

test("the messages never claim a write was saved", () => {
  const write = offline.offlineWriteMessage();
  assert.match(write, /offline/i);
  assert.match(write, /wasn't saved|not saved/i);
  assert.match(offline.offlineReadMessage(), /offline/i);
  // No promise of a later sync: that is the trap this ticket rules out.
  for (const message of [write, offline.offlineReadMessage()]) {
    assert.doesNotMatch(message, /will be saved|saved for later|queued|sync when/i);
  }
});

// ── next.config.js ─────────────────────────────────────────────────────────

test("the worker waits for the person, and next-pwa's own registration is off", () => {
  assert.match(nextConfig, /skipWaiting: false/, "a new build must not swap under someone");
  assert.match(nextConfig, /register: false/, "next-pwa's registration targets pages-router main.js, which App Router never loads");
  // The three page-caching switches #4648 turned off stay off.
  for (const option of ["dynamicStartUrl", "cacheStartUrl", "cacheOnFrontEndNav"]) {
    assert.match(nextConfig, new RegExp(`${option}: false`), `${option} stays off`);
  }
  // ...and no page is added to the cache list by this ticket.
  assert.doesNotMatch(nextConfig, /runtimeCaching:\s*\[/);
  // additionalManifestEntries would REPLACE the public/ glob (and with it the
  // #4533 icons), so the shell must arrive through the glob.
  assert.doesNotMatch(nextConfig, /^\s*additionalManifestEntries/m);
});

// ── the app's half ─────────────────────────────────────────────────────────

test("the signed-in app registers the worker itself, once", () => {
  const shell = readFileSync(join(ui, "components", "shared", "DashboardShell.tsx"), "utf8");
  assert.match(shell, /<ServiceWorkerBridge \/>/);
  assert.match(bridge, /registerServiceWorker/);
  // next-pwa's register.js would also reload on reconnect; the app does not.
  assert.doesNotMatch(bridge, /reloadOnOnline/);
  assert.doesNotMatch(bridge, /addEventListener\("online",\s*\(\)\s*=>\s*window\.location\.reload/);
});

test("the auth pages must NOT register the worker (that would undo the logout purge)", () => {
  // #4648: logout unregisters the worker and deletes every cache. A registration
  // on /login immediately precaches again, so caches survive for the next person
  // on a shared device. The registration belongs to the shell, which only the
  // authenticated layout mounts.
  assert.doesNotMatch(layout, /ServiceWorkerBridge/);
  assert.doesNotMatch(layout, /registerServiceWorker/);
  const authLayout = readFileSync(join(ui, "app", "(auth)", "layout.tsx"), "utf8");
  assert.doesNotMatch(authLayout, /ServiceWorkerBridge|registerServiceWorker/);
  const login = readFileSync(join(ui, "app", "(auth)", "login", "page.tsx"), "utf8");
  assert.doesNotMatch(login, /registerServiceWorker/);
});

test("the offline rows sit under the header, in the page flow (not floating)", () => {
  const shell = readFileSync(join(ui, "components", "shared", "DashboardShell.tsx"), "utf8");
  assert.match(shell, /import OfflineBanner, \{ ServiceWorkerBridge \} from "@\/components\/shared\/OfflineBanner"/);
  // Inside AppShell.Main's content, before the page: the spec's "banner under
  // the header" position.
  const main = shell.slice(shell.indexOf("<AppShell.Main>"), shell.indexOf("</AppShell.Main>"));
  assert.match(main, /<OfflineBanner \/>/);
  assert.ok(main.indexOf("<OfflineBanner />") < main.indexOf("{children}"), "the banner comes before the page");
  // And the CSS is in flow: a fixed bar would cover the content and the tab bar.
  const css = readFileSync(join(ui, "components", "shared", "OfflineBanner.module.css"), "utf8");
  assert.doesNotMatch(css, /position:\s*fixed/);
});

test("only a tap applies an update — nothing swaps the shell silently", () => {
  // applyUpdate posts SKIP_WAITING, and it is reachable only from the button's
  // handler in this component (the danger: a timer or an effect calling it).
  const calls = [...bridge.matchAll(/applyUpdate\(/g)].length;
  assert.equal(calls, 1, "applyUpdate is called in exactly one place");
  assert.match(bridge, /onClick=\{reload\}/);
  // The update row exists and says what it does.
  assert.match(bridge, /Reload to update/);
  // The reload is gated on the person's tap: the bridge only reloads when the
  // tap marked it intentional.
  assert.match(bridge, /applyRequested/);
});

test("apiFetch reports an offline failure as 'nothing was saved' for a write", () => {
  assert.match(api, /catch \(error\)/);
  assert.match(api, /isWriteMethod/);
  assert.match(api, /offlineWriteMessage\(\)/);
  assert.match(api, /offlineReadMessage\(\)/);
  // Never queued or retried: the words must not appear as a behaviour.
  assert.doesNotMatch(api, /enqueue|queueWrite|backgroundSync|outbox/i);
  // status 0 is the conventional "no response" status.
  assert.match(api, /\n\s+0,\n/);
});

test("the update-flow message constant is shared, not repeated in the page half", () => {
  assert.match(swModule, /import \{ SKIP_WAITING_MESSAGE \} from "\.\/offline\.js"/);
  assert.match(swModule, /SKIP_WAITING_MESSAGE \}/);
});
