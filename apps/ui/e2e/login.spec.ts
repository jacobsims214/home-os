import { test, expect } from "./fixtures";

/**
 * Login page hand-off to /api/auth/login (#4617). The page forwards
 * ?redirect= only after the same check as middleware.ts, so a crafted
 * /login?redirect= link can't carry another host into the login flow.
 * /api/auth/login is intercepted: Dex isn't part of the stub harness.
 */
test.beforeEach(({}, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chrome", "navigation check; runs once");
  test.skip(!!process.env.E2E_BASE_URL, "intercepts /api/auth/login; stub mode only");
});

const cases: Array<[string, string]> = [
  ["/dashboard/bills?month=2026-11", "/api/auth/login?redirect=%2Fdashboard%2Fbills%3Fmonth%3D2026-11"],
  ["//evil.example/x", "/api/auth/login?redirect=%2Fevil.example%2Fx"],
  ["https://evil.example/", "/api/auth/login"],
  ["/login", "/api/auth/login"],
];

for (const [redirect, want] of cases) {
  test(`?redirect=${redirect} starts login at ${want}`, async ({ page }) => {
    let started: URL | null = null;
    await page.route("**/api/auth/login**", async (route) => {
      started = new URL(route.request().url());
      await route.fulfill({ status: 200, contentType: "text/plain", body: "login start" });
    });
    await page.goto(`/login?redirect=${encodeURIComponent(redirect)}`);
    await expect.poll(() => started && started.pathname + started.search).toBe(want);
  });
}

/**
 * Full round trip with the stub standing in for Dex: /login?redirect= →
 * /api/auth/login (real: sets the PKCE cookie, answers 307 /dex/auth?...) →
 * Dex, played by this test: the redirect is followed straight to
 * /api/auth/callback?code&state, as Dex does after a sign-in → callback (real;
 * code exchange at the stub's DEX_TOKEN_URL) → the page the user asked for.
 * The hop happens on /api/auth/login because Playwright doesn't route the
 * requests a redirect leads to.
 */
test("/login?redirect=/dashboard/bills lands on /dashboard/bills after login", async ({ page, request, baseURL }) => {
  const stubUrl = `http://127.0.0.1:${process.env.E2E_STUB_PORT || 3901}`;
  const origin = new URL(baseURL!).origin;
  let redirectUri = "";
  await page.route("**/api/auth/login?**", async (route) => {
    const res = await route.fetch({ maxRedirects: 0 });
    const authorize = new URL(res.headers()["location"] ?? "", origin);
    expect(authorize.pathname).toBe("/dex/auth");
    redirectUri = authorize.searchParams.get("redirect_uri") ?? "";
    const callback = new URL(redirectUri);
    callback.searchParams.set("code", "e2e-code");
    callback.searchParams.set("state", authorize.searchParams.get("state") ?? "");
    const headers = { ...res.headers(), location: callback.toString() };
    await route.fulfill({ status: 302, headers });
  });

  await page.goto(`/login?redirect=${encodeURIComponent("/dashboard/bills")}`);
  // Poll rather than waitForURL: in next dev, StrictMode runs the login page's
  // effect twice, so the first login start is aborted (ERR_ABORTED) by the second.
  await expect.poll(() => new URL(page.url()).pathname, { timeout: 30_000 }).toBe("/dashboard/bills");
  expect(new URL(page.url()).origin).toBe(origin);

  // The callback sent Dex the same redirect_uri as the authorize request.
  expect(redirectUri).toBe(`${origin}/api/auth/callback`);
  const stats = (await (await request.get(`${stubUrl}/e2e/stats`)).json()) as { lastRedirectUri: string };
  expect(stats.lastRedirectUri).toBe(redirectUri);
  const cookies = await page.context().cookies();
  expect(cookies.find((c) => c.name === "home-os-token")?.httpOnly).toBe(true);
});
