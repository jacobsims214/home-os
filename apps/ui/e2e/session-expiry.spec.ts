import type { Page, Request } from "@playwright/test";
import { test, expect, SESSION_COOKIE, REFRESH_COOKIE } from "./fixtures";

/**
 * Session gone mid-use (#4620): when /bff answers 401 (the BFF already tried a
 * silent refresh and Dex refused it), the client sends the browser to
 * /login?redirect=<path+query> exactly once, even though the page fires
 * several /bff requests that all fail together. A 503 AUTH_UNAVAILABLE (Dex
 * down) keeps the session and must not redirect.
 */

const PAGE = "/dashboard/bills?month=2026-11";
const WANT_REDIRECT = "/dashboard/bills?month=2026-11";

/**
 * Records top-level navigations in order: "page" for the page under test,
 * "login" for /login. Also stops the chain at /login.
 */
async function watchNavigations(page: Page): Promise<{ events: string[]; logins: Request[] }> {
  const events: string[] = [];
  const logins: Request[] = [];
  page.on("request", (r) => {
    if (!r.isNavigationRequest() || r.frame() !== page.mainFrame()) return;
    const path = new URL(r.url()).pathname;
    if (path === "/login") {
      events.push("login");
      logins.push(r);
    } else if (path === "/dashboard/bills") {
      events.push("page");
    }
  });
  // /login hands off to /api/auth/login -> Dex. Nothing serves Dex here, so
  // end the chain once the login start is reached. Match on the path: since
  // #4617 the login start carries ?redirect=.
  await page.route(
    (u) => u.pathname === "/api/auth/login",
    (route) => route.fulfill({ status: 204 }),
  );
  return { events, logins };
}

test("a /bff 401 sends the browser to /login once, with the return path", async ({ page, baseURL }) => {
  test.skip(!!process.env.E2E_BASE_URL, "needs the stub");
  // Cookies the BFF cannot use: the stub API rejects the id token, and Dex
  // (stub) rejects the refresh token, so /bff answers a real 401.
  const url = new URL(baseURL!);
  const cookie = { domain: url.hostname, path: "/", httpOnly: true, secure: false, sameSite: "Lax" as const };
  await page.context().addCookies([
    { ...cookie, name: SESSION_COOKIE, value: "expired.e2e.token" },
    { ...cookie, name: REFRESH_COOKIE, value: "revoked-e2e-refresh" },
  ]);
  // Compile /login up front: under `next dev` a first compile mid-test can
  // full-reload the page, which starts a new page (and a new redirect).
  await page.request.get("/login");
  const { events, logins } = await watchNavigations(page);
  const bff401 = page.waitForResponse((r) => new URL(r.url()).pathname.startsWith("/bff/") && r.status() === 401);

  await page.goto(PAGE, { waitUntil: "commit" });
  await bff401;
  await expect.poll(() => logins.length, { message: "navigated to /login" }).toBeGreaterThan(0);

  const target = new URL(logins[0].url());
  expect(target.origin).toBe(url.origin);
  expect(target.searchParams.get("redirect")).toBe(WANT_REDIRECT);

  // Give the other failing /bff calls time to land: still one navigation per
  // page load. (A dev-server reload of the page may repeat the pair; it can
  // never produce two /login navigations from one page.)
  await page.waitForTimeout(1500);
  expect(events.join(" "), "one /login navigation per page load").toMatch(/^(page login ?)+$/);
});

test("a /bff 503 AUTH_UNAVAILABLE keeps the user on the page", async ({ loggedInPage: page }) => {
  test.skip(!!process.env.E2E_BASE_URL, "needs page.route on /bff");
  const { logins } = await watchNavigations(page);
  await page.route("**/bff/**", (route) =>
    route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "AUTH_UNAVAILABLE", message: "auth provider unavailable" } }),
    }),
  );
  const bff503 = page.waitForResponse((r) => new URL(r.url()).pathname.startsWith("/bff/") && r.status() === 503);

  await page.goto(PAGE);
  await bff503;
  await page.waitForTimeout(1500);
  expect(logins.length, "no /login navigation on 503").toBe(0);
  await expect(page).toHaveURL(new RegExp(`/dashboard/bills\\?month=2026-11$`));
});
