import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

/**
 * Back after signing in never lands anyone on a raw error body (#5407), and the
 * session survives the Back press.
 *
 * THIS SPEC EXISTS BECAUSE #5304 LOOKED FIXED AND WAS NOT. #5304 put an app page
 * (/auth/complete) between Dex and the destination, so Back stopped landing on
 * Dex's 400. But that page then forwarded to the destination UNCONDITIONALLY,
 * and the destination's guard answers 307 /login for a visitor with no session
 * -- whose page `location.replace()`s into /api/auth/login and on to Dex. So for
 * a session that did not survive the Back press, "Back lands in the app" still
 * meant "and is immediately thrown back into the login chain", where the Dex
 * login rate limiter answers with its own bare 429 body. The shape changed; the
 * dead end did not. Three R3 reports hit it independently.
 *
 * So the assertion is deliberately NOT "Back lands on /auth/complete" (that
 * passed while the bug shipped). It is the two things the ticket actually
 * demands:
 *
 *   1. whatever Back lands on is an APP-OWNED, RENDERED page -- never a raw
 *      error body, never a /dex URL, never the callback hop;
 *   2. the session still works afterwards: a navigation from there reaches the
 *      destination instead of bouncing to the login chain.
 *
 * The second half is what catches the regression. A page that renders nicely and
 * then bounces you to Dex fails it.
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
  test.skip(!!process.env.E2E_BASE_URL, "intercepts /api/auth/login; stub mode only");
  test.skip(test.info().project.name !== "desktop-chrome", "history assertions; runs once");
});

/**
 * Signs in from a deep link through the stub, leaving the browser on the
 * destination with a real session -- the exact starting point the repro names.
 */
async function signInFromDeepLink(page: Page, origin: string, destination: string): Promise<void> {
  await page.route("**/api/auth/login?**", async (route) => {
    const res = await route.fetch({ maxRedirects: 0 });
    const authorize = new URL(res.headers()["location"] ?? "", origin);
    const redirectUri = authorize.searchParams.get("redirect_uri") ?? "";
    const callback = new URL(redirectUri);
    callback.searchParams.set("code", "e2e-code");
    callback.searchParams.set("state", authorize.searchParams.get("state") ?? "");
    await route.fulfill({ status: 302, headers: { ...res.headers(), location: callback.toString() } });
  });

  await page.goto(destination);
  await expect.poll(() => new URL(page.url()).pathname, { timeout: 30_000 }).toBe(destination);
  await page.unroute("**/api/auth/login?**");
}

test("Back after signing in from a deep link never shows a raw error body, and the session survives", async ({
  page,
  baseURL,
}) => {
  const origin = new URL(baseURL!).origin;
  const destination = "/dashboard/bills";
  await signInFromDeepLink(page, origin, destination);

  // --- Back. On the regression this landed on a raw body (locally, Envoy's
  // 429; on the pre-#5304 build, Dex's spent-state 400) and killed the session.
  await page.goBack();

  // 1. Whatever it landed on is an app page a person can read.
  await expectNotRawErrorBody(page);
  await expectAppOwned(page, origin);
  // It is one of the app's own auth surfaces, and it says something useful.
  const landed = page.locator('[data-auth-page="auth-complete"], [data-auth-page="login"], [data-auth-page="rate-limited"]');
  await expect(landed.first()).toBeVisible({ timeout: 15_000 });
  // There is always a way forward -- never a dead end with no action.
  await expect(page.getByRole("link", { name: /continue|sign in|try again|sign in again/i }).first()).toBeVisible();

  // 2. The session survives the Back press: a fresh navigation to the deep-link
  // destination reaches it, rather than bouncing into the login chain. This is
  // the assertion #5304 could not make, and the one the reports were really
  // about -- "the app session is ALSO dead".
  await page.goto(destination);
  await expect.poll(() => new URL(page.url()).pathname, { timeout: 30_000 }).toBe(destination);
  await expectNotRawErrorBody(page);
  expect(new URL(page.url()).origin, "the session still works").toBe(origin);
});

test("Back after the session is gone lands on a sign-in page, never a raw error body", async ({
  page,
  baseURL,
}) => {
  // Amber's report, exactly: sign in from a deep link, press Back, and the
  // session is ALREADY DEAD by the time the next navigation happens -- "the app
  // session is ALSO dead: the next navigation bounces to the Dex login form".
  // That is the state the regression shipped in, and it is the one the other
  // two tests cannot reach on their own: with the session intact, /auth/complete
  // forwards and the bug hides.
  //
  // This test is therefore the one that FAILS on the #5304 build, because that
  // build forwarded /auth/complete unconditionally: with no cookie the forward
  // is 307 /login -> location.replace into /api/auth/login -> Dex, which is a
  // bounce and, once throttled, a raw 429 body.
  const origin = new URL(baseURL!).origin;
  const destination = "/dashboard/bills";
  await signInFromDeepLink(page, origin, destination);

  // The session is spent (the real repro's outcome: the cookies are gone).
  await page.context().clearCookies();

  await page.goBack();

  // Whatever Back shows is an app page a person can read -- never Dex, never
  // Envoy's 429 body, never the callback hop.
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
  baseURL,
}) => {
  // The other half of the same bug, without any login at all: /auth/complete is
  // reachable by anyone (it has to be), so a signed-out visitor must get the
  // sign-in card. #5304 forwarded them to the destination, whose guard answers
  // 307 /login -- spending a request against the Dex login limiter on behalf of
  // someone who is not signed in, and dropping them wherever that answers.
  const origin = new URL(baseURL!).origin;
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
