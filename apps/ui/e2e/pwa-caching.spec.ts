import type { Page } from "@playwright/test";
import { test, expect, onlyOn } from "./fixtures";

/**
 * Service worker caching, and what a logout leaves behind (#4648).
 *
 * The threat model is a shared family device: a tablet on the kitchen counter.
 * Before this ticket the worker's catch-all NetworkFirst rule cached every
 * `/bff/*` GET (authenticated household data) and logout left those responses
 * plus the persisted zustand profile in place, so the next person could be
 * served the previous person's data.
 *
 * Each test isolates one mechanism:
 *  1. the page purges caches + the persisted user BEFORE it submits the logout
 *     form — proven by stripping Clear-Site-Data from the response, so only the
 *     app-side purge can be responsible;
 *  2. the real logout route sends Clear-Site-Data;
 *  3. the login page sweeps what an EXPIRED session left before handing off;
 *  4. the built service worker never caches a /bff or /api response
 *     (E2E_UI=standalone only: next-pwa is off under `next dev`).
 *
 * Desktop-chrome only: nothing here is layout-dependent, and each test loads
 * pages.
 */

const PAGE = "/dashboard";

test.beforeEach(({}, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chrome", "not layout-dependent");
});

/** Writes caches that look like the pre-#4648 worker's, plus a signed-in profile. */
async function plantPreviousSession(page: Page): Promise<void> {
  await page.evaluate(async () => {
    // Exactly what the old catch-all wrote: authenticated responses, by name.
    const others = await caches.open("others");
    await others.put("/bff/members", new Response(JSON.stringify({ data: [{ name: "Mum" }] }), {
      headers: { "Content-Type": "application/json" },
    }));
    await others.put("/bff/bills?month=2026-11", new Response(JSON.stringify({ data: [{ amount: 42 }] })));
    const staticCache = await caches.open("home-os-static");
    await staticCache.put(
      new Request(`${location.origin}/_next/static/chunks/planted.js`),
      new Response("// static", { headers: { "Content-Type": "application/javascript" } }),
    );
    localStorage.setItem(
      "home-os-auth",
      JSON.stringify({ state: { user: { id: "u1", email: "mum@home.example", name: "Mum" } }, version: 1 }),
    );
    localStorage.setItem(
      "home-os-recent",
      JSON.stringify({
        state: { items: [{ entity_type: "bill", entity_id: "b1", title: "Dentist", timestamp: 1 }] },
        version: 0,
      }),
    );
    sessionStorage.setItem("scratch", "left over");
  });
}

/** Every path+query stored in every cache on this origin. */
async function cachedUrls(page: Page): Promise<string[]> {
  const urls = await page.evaluate(async () => {
    const out: string[] = [];
    for (const name of await caches.keys()) {
      const cache = await caches.open(name);
      for (const request of await cache.keys()) {
        const url = new URL(request.url);
        out.push(url.pathname + url.search);
      }
    }
    return out;
  });
  return urls.sort();
}

test("logout purges the caches and the persisted user from the page itself", async ({ loggedInPage: page }) => {
  test.skip(!!process.env.E2E_BASE_URL, "needs page.route on the logout route");
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(PAGE);

  // Strip Clear-Site-Data (and the real cookie expiry redirect) so ONLY the
  // app-side purge can clean up. That is the path that runs on a plain-http
  // origin, where browsers ignore Clear-Site-Data, and it is what covers a
  // worker built before this fix.
  await page.route("**/api/auth/logout", (route) =>
    route.fulfill({ status: 303, headers: { Location: "/login", "Cache-Control": "no-store" } }),
  );

  await page.goto(PAGE);
  await plantPreviousSession(page);
  expect(await cachedUrls(page)).toContain("/bff/members");

  // Logout lands on /login, and /login does not stop there: its mount sends the
  // browser on to /api/auth/login and then to Dex. That second navigation tore
  // the execution context out from under cachedUrls() below whenever it started
  // while the evaluate was in flight (`page.evaluate: Execution context was
  // destroyed`, #5189). Stop the chain at the login start, the same trick the
  // expired-session test below uses, so /login is the live document while the
  // purge is inspected. Nothing this test asserts lives past /api/auth/login.
  await page.route((u) => u.pathname === "/api/auth/login", (route) => route.fulfill({ status: 204 }));

  // The session cookies go too, so this asserts the route was really reached.
  // `waitUntil: "commit"`, never the default "load": /login's hand-off is
  // superseded by the 204 above, and a superseded document never fires `load`
  // (waiting for it hangs for the full timeout — seen under E2E_UI=standalone).
  await Promise.all([
    page.waitForURL(/\/login/, { timeout: 20_000, waitUntil: "commit" }),
    page.getByRole("navigation", { name: "Main navigation" }).getByRole("button", { name: /^Account menu for/ }).click()
      .then(() => page.getByRole("dialog", { name: "Account menu" }).getByRole("button", { name: "Log out" }).click()),
  ]);

  // The login page has rendered, so it has mounted and its sweep has run, and
  // the 204 stops it from ever navigating on. From here the document is settled
  // and nothing can destroy the context under the evaluates below.
  await expect(page.getByText("Redirecting to login...")).toBeVisible({ timeout: 20_000 });

  expect(await cachedUrls(page), "no cache entry survives a logout").toEqual([]);
  const leftovers = await page.evaluate(() => ({
    auth: localStorage.getItem("home-os-auth"),
    recent: localStorage.getItem("home-os-recent"),
    session: "scratch" in sessionStorage,
  }));
  expect(leftovers.auth, "the persisted user profile is gone").toBeNull();
  expect(leftovers.recent, "recently viewed household titles are gone").toBeNull();
  expect(leftovers.session, "sessionStorage is emptied").toBe(false);
});

test("the real logout route sends Clear-Site-Data for cache and storage", async ({ loggedInPage: page }) => {
  test.skip(!!process.env.E2E_BASE_URL, "POSTs to the stub-mode origin");
  const res = await page.request.post("/api/auth/logout", { maxRedirects: 0 });
  expect(res.status()).toBe(303);
  expect(res.headers()["clear-site-data"]).toBe('"cache", "storage"');
  expect(res.headers()["location"]).toBe("/login");
});

test("an EXPIRED session's cached /bff data is swept before sign-in", async ({ page }) => {
  test.skip(!!process.env.E2E_BASE_URL, "uses the stub and /login");
  // No session cookies at all: the state a device reaches once the id token and
  // the refresh token have both expired.
  // Plant from a non-app document on the same origin: /login redirects to
  // /api/auth/login the moment it mounts, which tears down the execution
  // context mid-evaluate. Cache Storage and localStorage are per-origin, so a
  // static asset document can seed them.
  await page.goto("/manifest.json");
  await plantPreviousSession(page);
  // Stop the chain at the login start, so Dex is not needed
  // (the same trick as session-expiry.spec.ts).
  await page.route((u) => u.pathname === "/api/auth/login", (route) => route.fulfill({ status: 204 }));

  // A protected page with no session: middleware 307s to /login, and the login
  // page sweeps before it redirects to Dex. The URL alone is not the signal —
  // the 307 lands on /login before the page's JS runs — so poll the caches.
  await page.goto("/dashboard/bills", { waitUntil: "commit" });
  await expect
    .poll(() => cachedUrls(page), { timeout: 20_000, message: "the expired session's cached /bff data is swept" })
    .toEqual([]);
  await expect.poll(() => page.evaluate(() => localStorage.getItem("home-os-auth")), { timeout: 20_000 }).toBeNull();
});

/**
 * The config blocks service workers for every other spec (`serviceWorkers:
 * "block"` in playwright.config.ts), because a worker would serve cached pages
 * past page.route(). These tests are about the worker, so they allow it.
 */
test.describe("with the service worker enabled", () => {
  test.use({ serviceWorkers: "allow" });

  test("the built service worker never caches a /bff or /api response", async ({ loggedInPage: page }) => {
  test.skip(!!process.env.E2E_BASE_URL, "needs the local build");
  // next-pwa is disabled under `next dev`, so this needs the standalone build:
  //   E2E_UI=standalone npx playwright test e2e/pwa-caching.spec.ts
  const swResponse = await page.request.get("/sw.js");
  test.skip(swResponse.status() !== 200, "no service worker under `next dev`: run with E2E_UI=standalone");

  await page.goto(PAGE);
  // Register the built worker explicitly. next-pwa's `register: true` injects
  // its registration into the PAGES-router main.js entry, which an App Router
  // app never loads, so the worker does not register itself (found while
  // testing this ticket; giving it a real registration is #4538's offline
  // shell). This test is about what the generated worker does at runtime, so it
  // starts the worker by hand.
  await page.evaluate(async () => {
    await navigator.serviceWorker.register("/sw.js", { scope: "/" });
  });
  await expect
    .poll(() => page.evaluate(async () => !!(await navigator.serviceWorker.getRegistration())?.active), {
      timeout: 30_000,
      message: "the built service worker installed and activated",
    })
    .toBe(true);
  if (!(await page.evaluate(() => !!navigator.serviceWorker.controller))) {
    await page.reload();
    await expect.poll(() => page.evaluate(() => !!navigator.serviceWorker.controller), { timeout: 20_000 }).toBe(true);
  }

  // Hit the authenticated API through the worker, with the session cookie, the
  // way every page does.
  await page.evaluate(async () => {
    for (const path of ["/bff/members", "/bff/bills?month=2026-11", "/bff/secrets/key", "/api/auth/session"]) {
      try {
        await fetch(path, { cache: "no-store" });
      } catch {
        // A 401/404/405 is fine: the request still went through the worker.
      }
    }
    // Give the worker's handler time to run and (wrongly) cache anything.
    await new Promise((resolve) => setTimeout(resolve, 2000));
  });

  const urls = await cachedUrls(page);
  const apiUrls = urls.filter(
    (u) => u === "/bff" || u.startsWith("/bff/") || u === "/api" || u.startsWith("/api/"),
  );
  expect(apiUrls, `no /bff or /api response may be in Cache Storage (had: ${urls.join(", ")})`).toEqual([]);
  // The worker is in control and created only its own caches.
  const names = await page.evaluate(() => caches.keys());
  expect(names).not.toContain("others");
  expect(names).not.toContain("apis");
  const allowed = ["home-os-static", "home-os-assets", "home-os-font-styles", "home-os-font-files"];
  for (const name of names) {
    if (name.startsWith("workbox-precache")) continue; // next-pwa's precache, of build assets
    expect(allowed, `${name} is a cache the policy allows`).toContain(name);
  }
  });
});
