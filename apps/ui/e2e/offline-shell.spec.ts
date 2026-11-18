import type { Page } from "@playwright/test";
import { appendFileSync, existsSync } from "node:fs";
import { test, expect } from "./fixtures";

/**
 * The offline shell and the caching behaviour around it (#4538).
 *
 * Runs on ONE project only — iphone-390, WebKit with isMobile and hasTouch, the
 * engine the phone layout targets (run it with
 * `E2E_UI=standalone npx playwright test e2e/offline-shell.spec.ts --project=iphone-390`).
 * The full suite is not run per project here: these are service-worker and
 * network behaviours, identical at every width, and each test loads pages.
 *
 * Needs the production build, because next-pwa is disabled under `next dev`:
 * `npm run build` then E2E_UI=standalone. The spec skips (with a reason) when
 * /sw.js is absent rather than passing silently.
 *
 * What this proves:
 *  1. a navigation with no network loads the app's own offline shell — the app
 *     frame and an honest "you're offline", NOT the browser's dinosaur page;
 *  2. an authenticated /bff or /api request is never served from Cache Storage,
 *     so nobody is shown stale household data — #4648's rule, still intact once
 *     the worker is actually registered (it never was before this ticket, so the
 *     guarantee had never been exercised in a real browser with a real worker);
 *  3. a new build's worker WAITS and the app offers "Reload to update", so a
 *     deploy neither swaps the shell under someone mid-task nor serves a stale
 *     one forever.
 */

const PROJECT = "iphone-390";

test.beforeEach(({}, testInfo) => {
  test.skip(testInfo.project.name !== PROJECT, `offline shell: ${PROJECT} only`);
});

/** Registers the built worker and waits until it controls this page. */
async function ensureControlled(page: Page): Promise<void> {
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
    await expect
      .poll(() => page.evaluate(() => !!navigator.serviceWorker.controller), { timeout: 20_000 })
      .toBe(true);
  }
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

/** The shell is precached at build time; wait for it so a "no shell" failure is
 *  not really a "the worker had not installed yet" flake. */
async function waitForPrecachedShell(page: Page): Promise<void> {
  await expect
    .poll(() => page.evaluate(async () => !!(await caches.match("/offline.html", { ignoreSearch: true }))), {
      timeout: 30_000,
      message: "the offline shell is in the precache",
    })
    .toBe(true);
}

test.describe("with the service worker enabled", () => {
  // A worker would serve cached pages past page.route(); this spec is about the
  // worker itself.
  test.use({ serviceWorkers: "allow" });

  test("a failed navigation loads the app's shell — not the browser's error page", async ({ loggedInPage: page }) => {
    test.skip(!!process.env.E2E_BASE_URL, "needs the local standalone build");
    // The worker only exists in the production build.
    const sw = await page.request.get("/sw.js");
    test.skip(sw.status() !== 200, "no service worker under `next dev`: run with E2E_UI=standalone after npm run build");

    await page.goto("/dashboard");
    await ensureControlled(page);

    // CUT THE NETWORK. `context.setOffline(true)` is the only mechanism that
    // reaches the WORKER: it emulates the network layer, so the worker's own
    // `fetch(request)` fails and its catch branch serves the shell. `page.route`
    // does NOT work here — it intercepts the page's requests, not the worker's,
    // so the worker would fetch the real page and this test would look at the
    // live /dashboard/bills instead of the shell. (Tried it; that is exactly what
    // happened.) The assertions below are what make this a real proof: /dashboard
    // /bills is a protected page no policy rule may store, so a rendered shell can
    // only have come from the fallback.
    // The shell is genuinely on the device first, so a pass below cannot be the
    // browser showing its own error page with our text hard-coded anywhere.
    await waitForPrecachedShell(page);

    await page.context().setOffline(true);
    await page.goto("/dashboard/bills").catch(() => {
      // WebKit may report the aborted navigation itself; the assertions are the
      // real check.
    });

    // The shell, by its own content: the app frame and the honest message.
    await expect(page.getByRole("heading", { name: /can’t reach the house|can't reach the house/i })).toBeVisible({
      timeout: 20_000,
    });
    await expect(page.getByText(/you’re offline|you're offline/i).first()).toBeVisible();
    // The app frame, not a browser error: the brand is there and a way back in.
    await expect(page.getByText("Home OS").first()).toBeVisible();
    await expect(page.getByRole("link", { name: /go to today/i })).toBeVisible();
    // The two honest claims, and NOT the spec's stale-copy.
    await expect(page.getByText(/no saved household data is shown/i)).toBeVisible();
    await expect(page.getByText(/showing what was saved/i)).toHaveCount(0);

    // It is OUR document, not WebKit's error page (which has no such text).
    const title = await page.title();
    expect(title).toMatch(/offline/i);
    const body = await page.evaluate(() => document.body.innerText);
    expect(body).not.toMatch(/cannot open the page|server can|not connected to the internet|Safari couldn’t/i);

    // The page is usable: the shell is a document with the safe-area viewport.
    const viewport = await page.evaluate(
      () => document.querySelector('meta[name="viewport"]')?.getAttribute("content") ?? "",
    );
    expect(viewport).toContain("viewport-fit=cover");

    await page.context().setOffline(false);
  });

  test("an authenticated API response is never served from Cache Storage", async ({ loggedInPage: page }) => {
    test.skip(!!process.env.E2E_BASE_URL, "needs the local standalone build");
    const sw = await page.request.get("/sw.js");
    test.skip(sw.status() !== 200, "no service worker under `next dev`: run with E2E_UI=standalone");

    await page.goto("/dashboard");
    await ensureControlled(page);

    // Hit the real API through the worker, with the session cookie, the way
    // every page does. A 401/404 is fine — the request still went through it.
    await page.evaluate(async () => {
      for (const path of ["/bff/members", "/bff/bills?month=2026-11", "/bff/auth/me", "/api/auth/session"]) {
        try {
          await fetch(path, { cache: "no-store" });
        } catch {
          /* still went through the worker */
        }
      }
      await new Promise((resolve) => setTimeout(resolve, 2000));
    });

    const urls = await cachedUrls(page);
    const apiUrls = urls.filter(
      (u) => u === "/bff" || u.startsWith("/bff/") || u === "/api" || u.startsWith("/api/"),
    );
    expect(apiUrls, `no /bff or /api response may be in Cache Storage (had: ${urls.join(", ")})`).toEqual([]);

    // And the worker is in control: the empty list above is not "no worker yet".
    expect(await page.evaluate(() => !!navigator.serviceWorker.controller)).toBe(true);
    // The shell IS precached while the API is not — so the empty list above is a
    // real policy result, not an empty Cache Storage. Workbox keys precache
    // entries with ?__WB_REVISION__=<hash>, hence the prefix match.
    expect(urls.some((u) => u.startsWith("/offline.html"))).toBe(true);
  });

  test("offline, an API call fails instead of returning a cached page or a stale answer", async ({ loggedInPage: page }) => {
    test.skip(!!process.env.E2E_BASE_URL, "needs the local standalone build");
    const sw = await page.request.get("/sw.js");
    test.skip(sw.status() !== 200, "no service worker under `next dev`: run with E2E_UI=standalone");

    await page.goto("/dashboard");
    await ensureControlled(page);

    await page.context().setOffline(true);
    const outcome = await page.evaluate(async () => {
      try {
        const res = await fetch("/bff/bills", { cache: "no-store" });
        // A cached page served for an API URL would land here: it would parse as
        // HTML, not JSON, and its content-type is the tell.
        return { ok: res.ok, type: res.headers.get("content-type") ?? "", status: res.status };
      } catch {
        return { threw: true };
      }
    });
    await page.context().setOffline(false);

    // Either the fetch threw (the worker's NetworkOnly rule refuses to answer) or
    // an HTTP error came back from a live-but-failing network. What must NOT
    // happen is a 200.
    expect(outcome).not.toHaveProperty("ok", true);
    expect(outcome).not.toHaveProperty("status", 200);
    if ("type" in outcome) expect(outcome.type).not.toContain("text/html");
  });
});

/**
 * The update flow (#4538, requirement 4). A deploy must reach the person, but
 * not by swapping the shell under them: `skipWaiting: false` makes the new
 * worker wait, the app offers a reload, and the tap is what applies it.
 *
 * The "new build" is simulated by appending a byte to the SERVED worker script
 * (the standalone server serves .next/standalone/public/, which Playwright copies
 * at startup — editing public/sw.js would change nothing the browser fetches).
 */
test.describe("the update flow", () => {
  test.use({ serviceWorkers: "allow" });

  const SERVED_SW = ".next/standalone/public/sw.js";

  test("a new build's worker waits, and the app offers a reload", async ({ loggedInPage: page }) => {
    test.skip(!!process.env.E2E_BASE_URL, "needs the local standalone build");
    test.skip(!existsSync(SERVED_SW), "no standalone build: run npm run build and E2E_UI=standalone");

    await page.goto("/dashboard");
    // The APP registered it (no manual register here): that is the path this
    // ticket fixed — next-pwa's own registration never ran in an App Router app.
    await expect
      .poll(() => page.evaluate(async () => !!(await navigator.serviceWorker.getRegistration())?.active), {
        timeout: 30_000,
        message: "the app registered the worker by itself",
      })
      .toBe(true);
    if (!(await page.evaluate(() => !!navigator.serviceWorker.controller))) {
      await page.reload();
      await expect.poll(() => page.evaluate(() => !!navigator.serviceWorker.controller), { timeout: 20_000 }).toBe(true);
    }
    // Nothing to offer on a fresh build.
    await expect(page.getByTestId("update-bar")).toHaveCount(0);

    appendFileSync(SERVED_SW, `\n// e2e update ${Date.now()}\n`);
    await page.evaluate(async () => {
      const registration = await navigator.serviceWorker.getRegistration();
      await registration?.update();
    });

    // The offer appears...
    await expect(page.getByTestId("update-bar")).toBeVisible({ timeout: 30_000 });
    await expect(page.getByRole("button", { name: /reload to update/i })).toBeVisible();
    // ...and the new worker is WAITING, not in control: nothing was swapped
    // under the person.
    const lifecycle = await page.evaluate(async () => {
      const registration = await navigator.serviceWorker.getRegistration();
      return { waiting: !!registration?.waiting, active: registration?.active?.state ?? null };
    });
    expect(lifecycle.waiting, "the new worker waits for the person").toBe(true);
    expect(lifecycle.active, "the old worker is still in control").not.toBe("redundant");

    // The tap applies it.
    await page.getByRole("button", { name: /reload to update/i }).click();
    await expect
      .poll(() => page.evaluate(async () => !!(await navigator.serviceWorker.getRegistration())?.waiting), {
        timeout: 25_000,
        message: "the waiting worker took over after the tap",
      })
      .toBe(false);
  });
});
