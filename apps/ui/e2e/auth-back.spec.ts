import { test, expect } from "./fixtures";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";

/**
 * Back after signing in must land in the app, never on Dex (#5304).
 *
 * WHY THIS SPEC IS SHAPED THIS WAY. The bug is about the browser's history
 * stack, so this test presses the real Back button and asserts what the page
 * becomes. Two things must be faithful or the test proves nothing:
 *
 * 1. DEX MUST BE A REAL DOCUMENT IN HISTORY, like the real Dex login form. A
 *    Playwright `page.route` stands in for `/api/auth/login` because Playwright
 *    cannot route the request a 3xx leads to (e2e/login.spec.ts documents
 *    this); the route is fetched for real, so the PKCE cookie and the authorize
 *    redirect are genuine. But the Dex PAGES come from a real local HTTP
 *    server, because the form has to be submitted (a POST) and the browser has
 *    to follow its redirect chain -- neither of which a route handler can do.
 *    That server plays Dex exactly: a form, a submit that spends the state, and
 *    a redirect back through an `/api/auth/callback?code=&state=` URL, which is
 *    the real callback (exchange, session cookie) with the code in it.
 *
 * 2. THE STATE MUST BE SINGLE-USE. The stand-in answers 400 with "Requested
 *    resource does not exist" for a spent state -- the dead end the ticket
 *    reports, with the text taken from the live Dex. A stand-in that re-rendered
 *    its form on a second visit would make this test pass on the broken build.
 *
 * WHY IT FAILS ON THE OLD BEHAVIOUR. With the callback redirecting straight to
 * the destination, the history entry behind it is the Dex form, so Back
 * re-requests a spent state, Dex answers 400, and the "we are on an app page"
 * assertions fail. With the in-app completion page between them, Back lands on
 * /auth/complete.
 */

const DEX_BAD_REQUEST = "Requested resource does not exist";

/** Dex's two documents: the login form, and what a spent state gets. */
function dexFormHtml(action: string): string {
  return `<!doctype html><html><head><title>dex</title></head><body>
<h1>Log in to Your Account</h1>
<form method="post" action="${action}">
  <label for="login">Email Address</label>
  <input id="login" name="login" value="amber@homeos.test">
  <button id="submit-login" type="submit">Login</button>
</form></body></html>`;
}
const DEX_BAD_REQUEST_HTML = `<!doctype html><html><head><title>dex</title></head><body>
<h1>Bad Request</h1><p>${DEX_BAD_REQUEST}</p></body></html>`;

type FakeDex = {
  /** Base URL the login redirect points at. */
  authorizeUrl: URL;
  /** How many times Dex was asked for a spent state (must stay 0). */
  badRequests: () => number;
  /** The Dex page URLs the browser actually visited. */
  visited: string[];
  close: () => Promise<void>;
};

/**
 * Starts a real Dex stand-in. `appOrigin` is the app's origin: Dex hands the
 * browser back to `<appOrigin>/api/auth/callback`, as the real Dex's
 * redirectURI does, and both live behind one host in production.
 */
async function startFakeDex(appOrigin: string): Promise<FakeDex> {
  const spent = new Set<string>();
  const reqToState = new Map<string, string>();
  const visited: string[] = [];
  let badRequests = 0;
  let nextReq = 0;

  const server: Server = createServer((req, res) => {
    const url = new URL(req.url ?? "/", "http://127.0.0.1");
    const state = url.searchParams.get("state") ?? "";
    visited.push(`${req.method} ${url.pathname}`);

    if (url.pathname === "/login") {
      if (spent.has(state)) {
        badRequests += 1;
        res.writeHead(400, { "Content-Type": "text/html" }).end(DEX_BAD_REQUEST_HTML);
        return;
      }
      if (req.method === "POST") {
        // The submit spends the state, exactly as Dex's does.
        spent.add(state);
        const id = `req-${++nextReq}`;
        reqToState.set(id, state);
        res.writeHead(303, { Location: `/approve?req=${id}` }).end();
        return;
      }
      res.writeHead(200, { "Content-Type": "text/html" }).end(dexFormHtml(`/login?state=${encodeURIComponent(state)}`));
      return;
    }

    if (url.pathname === "/approve") {
      const reqState = reqToState.get(url.searchParams.get("req") ?? "");
      if (!reqState) {
        badRequests += 1;
        res.writeHead(400, { "Content-Type": "text/html" }).end(DEX_BAD_REQUEST_HTML);
        return;
      }
      const callback = new URL("/api/auth/callback", appOrigin);
      callback.searchParams.set("code", "e2e-code");
      callback.searchParams.set("state", reqState);
      res.writeHead(302, { Location: callback.toString() }).end();
      return;
    }

    res.writeHead(404).end("not dex");
  });

  const port: number = await new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => resolve((server.address() as AddressInfo).port));
  });

  return {
    authorizeUrl: new URL(`http://127.0.0.1:${port}/login`),
    badRequests: () => badRequests,
    visited,
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
  };
}

test.beforeEach(() => {
  test.skip(!!process.env.E2E_BASE_URL, "plays Dex locally; stub mode only");
  test.skip(test.info().project.name !== "desktop-chrome", "history assertions; runs once");
});

test("Back after signing in from a deep link stays in the app, never on Dex", async ({ page, baseURL }) => {
  const origin = new URL(baseURL!).origin;
  const dex = await startFakeDex(origin);

  try {
    // Point the real /api/auth/login 307 at the stand-in, keeping its PKCE
    // redirect and the cookie it sets. Playwright has already made the real
    // request; only the Location is rewritten.
    await page.route("**/api/auth/login?**", async (route) => {
      const res = await route.fetch({ maxRedirects: 0 });
      const authorize = new URL(res.headers()["location"] ?? "", origin);
      // #5304 must not touch the protocol: still a PKCE S256 authorize request.
      expect(authorize.pathname).toBe("/dex/auth");
      expect(authorize.searchParams.get("code_challenge_method")).toBe("S256");
      expect(authorize.searchParams.get("code_challenge")).toBeTruthy();
      const target = new URL(dex.authorizeUrl);
      target.searchParams.set("state", authorize.searchParams.get("state") ?? "");
      await route.fulfill({ status: 302, headers: { ...res.headers(), location: target.toString() } });
    });

    // Signed out, deep link to a protected page: middleware -> /login -> Dex form.
    await page.goto("/dashboard/calendar");
    const submit = page.locator("#submit-login");
    await expect(submit).toBeVisible({ timeout: 30_000 });
    const dexFormUrl = page.url();

    // Sign in as Amber. This is the ONE navigation that adds a history entry;
    // everything after it (303 /approve -> 302 /api/auth/callback?code&state ->
    // the app) is a redirect hop inside that entry.
    await page.locator("#login").fill("amber@homeos.test");
    await Promise.all([page.waitForURL((u) => u.pathname === "/dashboard/calendar", { timeout: 30_000 }), submit.click()]);

    // The deep-link destination is honoured, and no URL the browser kept has the
    // authorization code in it.
    expect(page.url(), "the one-time code never lands in a kept URL").not.toContain("code=");

    // --- The regression: Back. On the broken build this re-requests the spent
    // Dex state and the page becomes Dex's 400.
    await page.goBack();

    await expect(page.locator("body")).not.toContainText(DEX_BAD_REQUEST, { timeout: 15_000 });
    expect(new URL(page.url()).pathname, "Back never returns to Dex").not.toMatch(/^\/dex/);
    expect(new URL(page.url()).pathname, "Back never re-enters the OIDC callback").not.toBe("/api/auth/callback");

    // It lands on the app's own completion page...
    await expect(page.locator('[data-auth-page="auth-complete"]')).toBeVisible();
    await expect(page.getByRole("heading", { name: /signed in/i })).toBeVisible();

    // ...and Back is NOT trapped: the page does not bounce the person forward
    // again. (An auto-forward on revisit would make Back unusable.)
    await page.waitForTimeout(1500);
    expect(new URL(page.url()).pathname, "Back is not immediately redirected forward").toBe("/auth/complete");

    // Dex was never asked for a spent state by the Back press.
    expect(dex.badRequests(), "no Dex request for a spent state").toBe(0);
    expect(dex.visited.filter((v) => v.startsWith("POST")), "the form was submitted once").toHaveLength(1);

    // The way into the app is there, and leads to the deep-link destination.
    await page.getByRole("link", { name: "Continue" }).click();
    await expect(page).toHaveURL(/\/dashboard\/calendar$/, { timeout: 30_000 });
    expect(dexFormUrl).toContain("/login?state=");

    // A second Back from the app returns to the app again, not to Dex: the
    // completion page is a durable step, not a one-shot trick.
    await page.goBack();
    await expect(page.locator("body")).not.toContainText(DEX_BAD_REQUEST, { timeout: 15_000 });
    expect(new URL(page.url()).pathname).toBe("/auth/complete");
  } finally {
    await dex.close();
  }
});

test("/auth/complete cannot be aimed at another origin through ?to=", async ({ loggedInPage: page, baseURL }) => {
  // The completion page is reachable by anyone (it has to be, or the login
  // could not end on it), so its `?to=` is attacker-controlled and is the half
  // of #5304 that must not become an open redirect. Signed in here, so the
  // in-app destination it falls back to is a real page rather than a login
  // bounce; what matters is the ORIGIN the browser ends up on.
  const origin = new URL(baseURL!).origin;
  for (const evil of ["//evil.example/x", "/\\evil.example", "https://evil.example/"]) {
    await page.goto(`/auth/complete?to=${encodeURIComponent(evil)}`);
    await page.waitForTimeout(1200);
    const landed = new URL(page.url());
    expect(landed.origin, `${evil} stays on the app origin`).toBe(origin);
    // The query string still echoes the value we passed (it is our own page's
    // ?to=); what matters is that the browser never LEFT the origin for it.
    expect(landed.host, `${evil} never resolves to another host`).toBe(new URL(origin).host);
  }
});

test("/auth/complete does not auto-forward when it is reached by Back", async ({ loggedInPage: page }) => {
  // The revisit guard. Load the page with a destination, let it hand off, then
  // come back. If the revisit did not detect itself it would forward again and
  // Back would be unusable -- the person would be pinned forward, a worse dead
  // end than the Dex page.
  //
  // SIGNED IN (#5407). This must be a session-bearing page now: /auth/complete
  // only hands off for a signed-in visitor, because forwarding a signed-out one
  // sends them through the destination's 307 /login into the Dex login chain --
  // the dead end #5407 reports (see e2e/auth-back-session.spec.ts). Signed out
  // there is no first hand-off to come back from, so the guard under test would
  // never run.
  //
  // The destination is /register because it is a real page that loads without
  // its own guard doing anything interesting. A protected one (/dashboard)
  // would render the dashboard; a signed-out one would 307 to /login, whose
  // page location.replace()s into the Dex flow, and the Back under test would
  // then measure that bounce instead of this page's guard.
  const destination = "/register";
  const onDestination = (u: URL) => u.pathname === destination;

  await page.goto(`/auth/complete?to=${encodeURIComponent(destination)}`);
  await page.waitForURL(onDestination, { timeout: 15_000 });

  // Back via history.back(), NOT page.goBack(): goBack() waits for a "load"
  // event, which a bfcache restore never fires, so it aborts with ERR_ABORTED
  // and the test fails on the harness rather than on the app. The navigation
  // destroys the execution context while evaluate() awaits its result, so the
  // rejection is expected and swallowed.
  await page.evaluate(() => window.history.back()).catch(() => {});
  await page.waitForTimeout(1500);

  // Back must land on the app's own completion page, never on Dex.
  expect(new URL(page.url()).pathname, "Back lands on the app's own page, never on Dex").toBe(
    "/auth/complete",
  );

  // ...and it must NOT forward again, or Back would be unusable (pinned
  // forward). Give a re-forward the time it would need, then require the URL
  // to be unchanged and the page to be showing its revisited state.
  await page.waitForTimeout(1500);
  expect(new URL(page.url()).pathname, "Back is not trapped: no auto re-forward").toBe("/auth/complete");
  await expect(page.locator("[data-auth-complete]")).toHaveAttribute("data-auth-complete", "revisited");
  await expect(page.locator("body")).not.toContainText(DEX_BAD_REQUEST);
});
