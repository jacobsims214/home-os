import { test, expect } from "./fixtures";
import type { CDPSession, Page } from "@playwright/test";

/**
 * Back after signing in never lands anyone on a raw or Dex body (#5407), and the
 * landing is DETERMINISTIC (#5481).
 *
 * WHY THIS SPEC WAS REBUILT (#5481). The #5407 version of this file signed in by
 * intercepting /api/auth/login and jumping straight to the callback. That is not
 * the reported path: it leaves the browser with two history entries FEWER than a
 * real sign-in, because it never renders Dex's login form and never performs the
 * form SUBMIT -- and the form submit is the one navigation that adds an entry to
 * the login chain. Measured live (#5463), the real repro fails 3/6 runs while
 * that version of the test passed every time. A test that does not reproduce the
 * reported path protects nothing.
 *
 * So this spec reproduces the topology the reports name, entry for entry:
 *
 *   [about:blank] -> [Dex form] -> [form submit (303)] -> [callback 307]
 *                 -> [/auth/complete] -> [destination]
 *
 * THE FAKE DEX RUNS ON CDP, NOT page.route. Playwright's route interception does
 * not see a navigation that a redirect points AT: `GET /api/auth/login` answers
 * 307 /dex/auth and that second request is issued by the network stack, below
 * the routing layer (measured on 1.65.1: the /dex request fires, `page.route`
 * never matches it, and a plain node server behaves the same). With no fake Dex
 * the request reaches the app, where /dex is not public and the middleware
 * answers 307 /login -- an endless bounce that never renders a form. CDP's
 * Fetch domain sits at the network layer, so it does see redirect targets, and
 * that is what lets this spec drive a real sign-in.
 *
 * The fake Dex behaves like the real one where it matters: the authorize URL
 * 302s to the local login form, the state is single-use, and re-requesting a
 * spent one answers Dex's actual 400 body (`Bad Request — "Requested resource
 * does not exist."`). So a Back press that lands on the login chain reproduces
 * BOTH failure modes the ticket measured: the form bouncing back in, and the raw
 * 400 page.
 *
 * THE ASSERTION is the outcome, not the page name. Whatever Back lands on must
 * be an app-owned, rendered page -- never a raw error body, never /dex, never
 * the callback hop -- and the session must still work afterwards.
 */

// The bodies the login chain can serve on a spent or throttled state. Both are
// real text: `local_rate_limited` is Envoy's 429 local-reply body (verified in
// the chart's envoy ConfigMap), `Requested resource does not exist` is Dex's
// spent-state 400 (verified against live Dex in #5304).
const RAW_BODIES = [/local_rate_limited/, /Requested resource does not exist/];

/** Fails if the visible page is one of the raw error bodies the ticket reports. */
async function expectNotRawErrorBody(page: Page): Promise<void> {
  const text = (await page.locator("body").innerText().catch(() => "")) || "";
  for (const raw of RAW_BODIES) {
    expect(text, "the page must never be a raw error body").not.toMatch(raw);
  }
  // A raw body is also SHORT: these strings arrive as one line on a blank page.
  // A rendered app page always carries the brand row and a heading.
  expect(text.trim().length, "a rendered page, not a bare string").toBeGreaterThan(80);
}

/** Fails if Back landed anywhere outside the app. */
async function expectAppOwned(page: Page, origin: string): Promise<void> {
  const url = new URL(page.url());
  expect(url.origin, "Back stays on the app origin").toBe(origin);
  expect(url.pathname, "Back never lands on Dex").not.toMatch(/^\/dex/);
  expect(url.pathname, "Back never re-enters the OIDC callback").not.toBe("/api/auth/callback");
}

test.beforeEach(() => {
  test.skip(!!process.env.E2E_BASE_URL, "serves a fake Dex; stub mode only");
  test.skip(test.info().project.name !== "desktop-chrome", "history assertions; runs once");
});

const FORM = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Log in to Your Account</title></head>
<body><h1>Log in to Your Account</h1>
<form method="post" action="/dex/auth/local?state=__STATE__">
<input id="login" name="login" value="amber@wargame.test" autocomplete="username">
<input id="password" name="password" type="password" value="pw" autocomplete="current-password">
<button id="submit-login" type="submit">Login</button>
</form></body></html>`;

/**
 * Serves a Dex login form on the app origin and 303s its submit into the real
 * callback -- the shape of a real sign-in, with the form submit as its own
 * history entry. A spent state answers Dex's actual 400 body on a re-request.
 */
async function installFakeDex(page: Page): Promise<CDPSession> {
  const spent = new Set<string>();
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Fetch.enable", { patterns: [{ urlPattern: "*://*/dex/*", requestStage: "Request" }] });

  const send = (
    requestId: string,
    responseCode: number,
    headers: Array<{ name: string; value: string }>,
    body = "",
  ) =>
    cdp
      .send("Fetch.fulfillRequest", {
        requestId,
        responseCode,
        responseHeaders: [{ name: "cache-control", value: "no-store" }, ...headers],
        body: Buffer.from(body, "utf8").toString("base64"),
      })
      .catch(() => undefined); // the navigation may already be gone

  cdp.on("Fetch.requestPaused", (event) => {
    const url = new URL(event.request.url);
    const state = url.searchParams.get("state") ?? "";
    const html = [{ name: "content-type", value: "text/html; charset=utf-8" }];
    const text = (code: number, body: string) =>
      send(event.requestId, code, [{ name: "content-type", value: "text/plain; charset=utf-8" }], body);

    // /dex/auth -> connector choice -> the local login form (a 302 chain).
    if (url.pathname === "/dex/auth") {
      void send(event.requestId, 302, [{ name: "location", value: `/dex/auth/local/login?state=${encodeURIComponent(state)}` }]);
      return;
    }

    if (url.pathname === "/dex/auth/local/login") {
      // Single-use state, as Dex has it: a second GET is a spent state.
      if (spent.has(state)) return void text(400, 'Bad Request — "Requested resource does not exist."');
      return void send(event.requestId, 200, html, FORM.replaceAll("__STATE__", encodeURIComponent(state)));
    }

    // The submit: consume the state and 303 through to the real callback.
    if (url.pathname === "/dex/auth/local" && event.request.method === "POST") {
      spent.add(state);
      return void send(event.requestId, 303, [
        { name: "location", value: `/api/auth/callback?code=e2e-code&state=${encodeURIComponent(state)}` },
      ]);
    }

    void send(event.requestId, 404, [{ name: "content-type", value: "text/plain" }], "not found");
  });

  return cdp;
}

/** Waits until `path` is the location AND its page has painted a heading. */
async function expectDestination(page: Page, path: string): Promise<void> {
  await expect.poll(() => new URL(page.url()).pathname, { timeout: 30_000 }).toBe(path);
  await expect(page.getByRole("heading").first(), "the destination has rendered").toBeVisible({ timeout: 30_000 });
}

/**
 * Signs in from a deep link exactly as the reports do: open the destination
 * signed out, submit Dex's login form, and LAND ON THE DESTINATION -- rendered,
 * not merely addressed. The history this leaves behind is the reported one,
 * including the form submit's entry.
 *
 * Waiting for the render matters (#5481): the reports press Back from a page
 * they are looking at. Back pressed while the destination is still loading (it
 * is a client-rendered page whose data arrives after the URL changes) is a
 * different navigation from the one people make, and it sampled a real but
 * unrelated behaviour when this spec first did it: clearCookies() landing
 * mid-flight made the destination's own guard redirect, and Back then returned
 * to the re-requested Dex form.
 */
async function signInFromDeepLink(page: Page, destination: string): Promise<void> {
  await installFakeDex(page);
  await page.goto(destination);

  // The signed-out chain: /login -> /api/auth/login -> /dex/auth -> the form.
  const form = page.locator("#submit-login");
  await expect(form).toBeVisible({ timeout: 30_000 });
  await form.click();

  await expectDestination(page, destination);
}

/**
 * Repeated ON PURPOSE (#5481). The failure is a race: the review measured 3/6
 * runs landing back on Dex or on a raw body. Every Playwright test gets a fresh
 * browser context, so each iteration is a fresh profile with no cookies and an
 * empty history -- the repro's starting state.
 */
const DEEP_LINK_RUNS = Number(process.env.E2E_DEEP_LINK_RUNS || 6);

for (let run = 1; run <= DEEP_LINK_RUNS; run++) {
  test(`deep-link sign-in, run ${run}/${DEEP_LINK_RUNS}: Back lands inside the app, never on Dex or a raw body`, async ({
    page,
    baseURL,
  }) => {
    const origin = new URL(baseURL!).origin;
    const destination = "/dashboard/calendar";

    await signInFromDeepLink(page, destination);

    // --- Back, from the destination, as the reports do.
    await page.goBack();

    // 1. Whatever it landed on is an app page a person can read.
    await expectNotRawErrorBody(page);
    await expectAppOwned(page, origin);

    // 2. It is the login's own completion screen, not the Dex form bouncing
    //    back at them. #5407 asserted only "not a raw body", which is satisfied
    //    by Dex re-serving its form -- the exact half-failure the review found.
    const landed = page.locator(
      '[data-auth-page="auth-complete"], [data-auth-page="login"], [data-auth-page="rate-limited"]',
    );
    await expect(landed.first()).toBeVisible({ timeout: 15_000 });
    expect(new URL(page.url()).pathname, "Back returns to the login's last app page").toBe("/auth/complete");
    // There is always a way forward -- never a dead end with no action.
    await expect(page.getByRole("link", { name: /continue|sign in|try again|sign in again/i }).first()).toBeVisible();

    // 3. The session survives the Back press: a fresh navigation to the
    //    deep-link destination reaches it, rather than bouncing into the login
    //    chain.
    await page.goto(destination);
    await expectDestination(page, destination);
    await expectNotRawErrorBody(page);
    expect(new URL(page.url()).origin, "the session still works").toBe(origin);
  });
}

test("Back after the session is gone lands on a sign-in page, never a raw error body", async ({
  page,
  baseURL,
}) => {
  // Amber's report, exactly: sign in from a deep link, press Back, and the
  // session is ALREADY DEAD by the time the next navigation happens -- "the app
  // session is ALSO dead: the next navigation bounces to the Dex login form".
  // That is the state the regression shipped in: with no cookie, a stop at the
  // destination's guard answers 307 /login -> location.replace into
  // /api/auth/login -> Dex, which is a bounce and, once throttled, a raw 429.
  const origin = new URL(baseURL!).origin;
  const destination = "/dashboard/bills";
  // signInFromDeepLink already waited for the destination to render, which is
  // what makes clearing the cookies below mean "the session is spent", and not
  // "the page I was still loading lost its session".
  await signInFromDeepLink(page, destination);

  // The session is spent (the real repro's outcome: the cookies are gone).
  await page.context().clearCookies();

  // Stop the page we are standing on from starting its OWN login redirect.
  // With the cookies gone, the destination's next /bff call answers 401 and
  // lib/api.ts sends the browser to /login -- a document navigation that races
  // the Back press this test is about (and once, under load, won it). That
  // 401-to-login path is somebody else's test; here it would only blur which
  // navigation is being measured. Aborting the fetches leaves exactly the
  // question the ticket asks: the session is gone, now press Back.
  await page.route("**/bff/**", (route) => route.abort());

  await page.goBack();

  await expectNotRawErrorBody(page);
  await expectAppOwned(page, origin);
  await expect(page.locator('[data-auth-page="auth-complete"]')).toBeVisible({ timeout: 15_000 });
  // It does not throw the signed-out visitor back into the login chain: the URL
  // stays put, and the card offers a sign-in that keeps the destination.
  await page.waitForTimeout(1500);
  expect(new URL(page.url()).pathname, "no bounce into /login or Dex").toBe("/auth/complete");
  await expect(page.getByRole("heading", { name: /sign in again/i })).toBeVisible();
  const retry = page.locator('[data-auth-action="sign-in-again"]');
  await expect(retry).toBeVisible();
  expect(decodeURIComponent((await retry.getAttribute("href")) ?? "")).toContain(destination);
});

test("a signed-out visitor at /auth/complete is offered sign-in, not thrown back into Dex", async ({
  page,
}) => {
  // The other half of the same bug, without any login at all: /auth/complete is
  // reachable by anyone (it has to be), so a signed-out visitor must get the
  // sign-in card. #5304 forwarded them to the destination, whose guard answers
  // 307 /login -- spending a request against the Dex login limiter on behalf of
  // someone who is not signed in, and dropping them wherever that answers.
  await page.goto(`/auth/complete?to=${encodeURIComponent("/dashboard/bills")}`);

  // It stays put instead of navigating into the login chain.
  await page.waitForTimeout(1500);
  expect(new URL(page.url()).pathname, "no forward into the destination's login bounce").toBe("/auth/complete");

  await expect(page.locator('[data-auth-complete="signed-out"]')).toBeVisible();
  await expect(page.getByRole("heading", { name: /sign in again/i })).toBeVisible();

  // The one action keeps the destination, so retrying is one click.
  const action = page.locator('[data-auth-action="sign-in-again"]');
  await expect(action).toBeVisible();
  const href = await action.getAttribute("href");
  expect(href, "the retry carries the destination").toContain("redirect=");
  expect(decodeURIComponent(href ?? ""), "and it is the page they wanted").toContain("/dashboard/bills");
});

test("a rate-limited sign-in renders a styled page, never the raw local_rate_limited string", async ({
  page,
  baseURL,
}) => {
  // The login surface's own 429 state. The production 429 is served by Envoy,
  // not by us -- deploy/helm/home-os/tests/envoy-local-reply.sh asserts that
  // page's markup on the rendered chart -- so what is asserted here is the
  // app-side surface that must agree with it: `?error=rate_limited` renders an
  // explanation and retry guidance, and does NOT bounce straight back into Dex
  // (a bounce would spend another request against the limiter and loop).
  const origin = new URL(baseURL!).origin;
  await page.route("**/api/auth/login**", async (route) => {
    // If the page bounces, the request lands here and the assertion below fails
    // with a clear reason rather than a mystery timeout.
    await route.fulfill({ status: 500, contentType: "text/plain", body: "the rate-limited page must not redirect" });
  });

  await page.goto("/login?error=rate_limited");

  const card = page.locator('[data-auth-page="rate-limited"]');
  await expect(card).toBeVisible();
  await expect(page.getByRole("heading", { name: /too many sign-in attempts/i })).toBeVisible();
  // An explanation and retry guidance, both. Scoped to the card's own warning
  // block: Next's route announcer is also role="alert", so a bare [role=alert]
  // matches two elements.
  const warning = card.locator('[role="alert"]');
  await expect(warning).toContainText(/wait/i);
  await expect(warning).toContainText(/\d+ seconds/);
  // Never the raw literal.
  await expectNotRawErrorBody(page);
  await expect(page.locator("body")).not.toContainText("local_rate_limited");
  // And it did not auto-redirect into Dex.
  expect(new URL(page.url()).origin, "stayed put").toBe(origin);
  expect(new URL(page.url()).pathname).toBe("/login");
});
