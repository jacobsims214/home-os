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
