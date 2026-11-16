import type { Response } from "@playwright/test";
import {
  test,
  expect,
  expectNoHorizontalOverflow,
  signInWithStub,
  SESSION_COOKIE,
  REFRESH_COOKIE,
} from "./fixtures";

/**
 * Smoke test (#4510): sign in, land on the home screen, and make sure it fits
 * a phone. Runs on every project in playwright.config.ts (iPhone 15, 375,
 * 390, Pixel 7, desktop Chrome).
 *
 * The home screen is /dashboard until the Family Hub "Today" page replaces it.
 */
const HOME = "/dashboard";

test("signed out: home sends the browser to Dex with a PKCE challenge", async ({ page, baseURL, browserName }) => {
  // /dashboard -> /login -> (JS) /api/auth/login -> 307 /dex/auth?... In a real
  // deploy the ingress serves /dex/* from Dex. With the stub nothing does, so
  // check the authorize redirect itself rather than where it lands. The stub's
  // /dex/auth would fall through to the UI middleware and loop back here, so
  // only the first login start goes to the server.
  if (!process.env.E2E_BASE_URL) {
    // Under next dev, StrictMode runs the login page's effect twice, so two
    // starts can be in flight at once (the second may abort the first). Both
    // reach the server until one has answered; later loops get a 204.
    let answered = false;
    page.on("response", (r) => {
      if (new URL(r.url()).pathname === "/api/auth/login" && r.status() === 307) answered = true;
    });
    await page.route(
      (u) => u.pathname === "/api/auth/login",
      (route) => (answered ? route.fulfill({ status: 204 }) : route.continue()),
    );
  }
  const loginStart = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/auth/login" && r.status() !== 204);
  // "commit": the page navigates on its own from here; loginStart is the wait.
  await page.goto(HOME, { waitUntil: "commit" });
  const res = await loginStart;
  expect(res.status()).toBe(307);
  const authorize = new URL(res.headers()["location"] ?? "", baseURL);
  expect(authorize.pathname).toBe("/dex/auth");
  const params = authorize.searchParams;
  expect(params.get("client_id")).toBe("home-os-ui");
  expect(params.get("code_challenge_method")).toBe("S256");
  expect(params.get("code_challenge")).toMatch(/^[A-Za-z0-9_-]{43}$/);
  expect(params.get("redirect_uri")).toMatch(/\/api\/auth\/callback$/);

  // Only that first login start reached the server, so the jar holds its cookie.
  // A production build marks it Secure, which WebKit drops over plain http.
  const secureOverHttp = process.env.E2E_UI === "standalone" && baseURL!.startsWith("http:");
  if (!(secureOverHttp && browserName === "webkit")) {
    const pkce = () => page.context().cookies().then((cs) => cs.find((c) => c.name === "home-os-pkce"));
    await expect.poll(async () => (await pkce())?.httpOnly, { message: "PKCE cookie is httpOnly" }).toBe(true);
    expect((await pkce())?.value.startsWith(`${params.get("state")}.`), "PKCE cookie carries this state").toBe(true);
  }
});

test("login error page fits the screen", async ({ page }) => {
  await page.goto("/login?error=e2e");
  await expect(page.getByText("Sign-in didn't complete")).toBeVisible();
  await expect(page.getByRole("link", { name: "Try again" })).toBeVisible();
  await expectNoHorizontalOverflow(page);
});

test("signed in: home renders through the BFF with no horizontal scroll", async ({ loggedInPage: page }) => {
  const bff: Response[] = [];
  page.on("response", (r) => {
    if (new URL(r.url()).pathname.startsWith("/bff/")) bff.push(r);
  });

  await page.goto(HOME);
  await expect(page).toHaveURL(new RegExp(`${HOME}$`));
  await expect(page.getByRole("heading", { name: "Dashboard", level: 2 })).toBeVisible();
  // Data has arrived (skeletons are gone) once a summary section renders.
  await expect(page.getByRole("heading", { name: "Financial Summary" })).toBeVisible();

  if (!process.env.E2E_BASE_URL) {
    // Seeded stub content proves the BFF forwarded an accepted Bearer token.
    await expect(page.getByText("Replace HVAC filter in upstairs hallway return")).toBeVisible();
    expect(bff.length, "dashboard made /bff calls").toBeGreaterThan(0);
    for (const r of bff) expect(r.status(), r.url()).toBe(200);
  }

  // No session token is ever readable from client JS (#4412).
  const jsCookies = await page.evaluate(() => document.cookie);
  expect(jsCookies).not.toContain(SESSION_COOKIE);
  expect(jsCookies).not.toContain(REFRESH_COOKIE);

  await expectNoHorizontalOverflow(page);
});

test("overflow check catches a too-wide element", async ({ page }) => {
  // Guards the guard: the assertion above must not pass vacuously.
  await page.setContent(
    `<meta name="viewport" content="width=device-width,initial-scale=1"><div style="width:2000px">wide</div>`,
  );
  await expect(expectNoHorizontalOverflow(page)).rejects.toThrow(/horizontal overflow[\s\S]*div/);
});

// Regression (#4626): with only home-os-refresh, middleware and the dashboard
// layout both let the page render, and the first /bff call renews the session
// through Dex's refresh_token grant instead of bouncing the user to /dex/auth.
test("refresh-only session renews silently on the home screen", async ({ page, baseURL, stubStats, browserName }) => {
  test.skip(!!process.env.E2E_BASE_URL, "needs the stub");
  // The production build sets the renewed cookie with Secure, and WebKit
  // drops Secure cookies on http://localhost (Chromium keeps them). The
  // standalone harness has no TLS, so WebKit runs this only against next dev.
  test.skip(
    browserName === "webkit" && process.env.E2E_UI === "standalone",
    "Secure cookies need https on WebKit; covered by Chromium and by next dev",
  );
  await signInWithStub(page, baseURL!, { idToken: false });
  const before = (await stubStats()).dexRefreshes;
  await page.goto(HOME);
  await expect(page.getByRole("heading", { name: "Financial Summary" })).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`${HOME}$`));
  expect((await stubStats()).dexRefreshes).toBeGreaterThan(before);
  const names = (await page.context().cookies()).map((c) => c.name);
  expect(names).toEqual(expect.arrayContaining([SESSION_COOKIE, REFRESH_COOKIE]));
});
