import { test as base, expect, type Page } from "@playwright/test";

/**
 * Shared E2E fixtures (#4510).
 *
 * `loggedInPage` is a page whose browser context holds a signed-in session
 * for the seeded test user:
 * - Stub mode (default): the token pair comes from e2e/stub-server.mjs
 *   (POST /e2e/login, standing in for the Dex code exchange) and is stored in
 *   the same httpOnly cookies the OIDC callback sets. Everything after that
 *   (middleware, dashboard layout guard, BFF, refresh) is the real UI code.
 * - External mode (E2E_BASE_URL set): signs in through /api/auth/login and
 *   the Dex password form with E2E_USER / E2E_PASSWORD.
 */

export const SESSION_COOKIE = "home-os-token";
export const REFRESH_COOKIE = "home-os-refresh";

const external = !!process.env.E2E_BASE_URL;
const stubUrl = `http://127.0.0.1:${process.env.E2E_STUB_PORT || 3901}`;
export const testUser = {
  email: process.env.E2E_USER || "e2e@homeos.test",
  password: process.env.E2E_PASSWORD || "",
};

export type StubStats = { dexRefreshes: number; apiRequests: number; apiUnauthorized: number; apiWrites: string[] };

type Fixtures = {
  /** Page signed in as the seeded test user. */
  loggedInPage: Page;
  /** Reads the stub's counters. Stub mode only. */
  stubStats: () => Promise<StubStats>;
};

type Tokens = { id_token: string; refresh_token: string; expires_in: number };

/** Signs a context in with stub-minted tokens, set as the callback sets them. */
export async function signInWithStub(
  page: Page,
  baseURL: string,
  opts: { idToken?: boolean } = {},
): Promise<void> {
  const res = await page.request.post(`${stubUrl}/e2e/login`, { data: { email: testUser.email } });
  expect(res.ok(), `stub login for ${testUser.email}`).toBeTruthy();
  const tokens = (await res.json()) as Tokens;
  const url = new URL(baseURL);
  const cookie = { domain: url.hostname, path: "/", httpOnly: true, secure: false, sameSite: "Lax" as const };
  const expires = Math.floor(Date.now() / 1000);
  const cookies = [{ ...cookie, name: REFRESH_COOKIE, value: tokens.refresh_token, expires: expires + 30 * 86400 }];
  if (opts.idToken !== false) {
    cookies.push({ ...cookie, name: SESSION_COOKIE, value: tokens.id_token, expires: expires + tokens.expires_in });
  }
  await page.context().addCookies(cookies);
}

/** Real Dex login (external mode). Dex's default theme: #login, #password, #submit-login. */
async function signInWithDex(page: Page): Promise<void> {
  if (!testUser.password) throw new Error("E2E_PASSWORD is required when E2E_BASE_URL is set");
  await page.goto("/api/auth/login");
  await page.locator("#login").fill(testUser.email);
  await page.locator("#password").fill(testUser.password);
  await page.locator("#submit-login").click();
  await page.waitForURL(/\/dashboard(\/|\?|$)/);
}

export const test = base.extend<Fixtures>({
  loggedInPage: async ({ page, baseURL }, use) => {
    if (external) await signInWithDex(page);
    else await signInWithStub(page, baseURL!);
    await use(page);
  },
  stubStats: async ({ request }, use) => {
    await use(async () => {
      const res = await request.get(`${stubUrl}/e2e/stats`);
      return (await res.json()) as StubStats;
    });
  },
});

export { expect };

/**
 * Asserts the page cannot scroll sideways. On failure the message lists the
 * elements that stick out past the viewport, so the culprit is obvious.
 */
export async function expectNoHorizontalOverflow(page: Page): Promise<void> {
  const report = await page.evaluate(() => {
    const vw = document.documentElement.clientWidth;
    const scrollWidth = Math.max(document.documentElement.scrollWidth, document.body.scrollWidth);
    const offenders: string[] = [];
    if (scrollWidth > vw) {
      for (const el of Array.from(document.body.querySelectorAll<HTMLElement>("*"))) {
        const r = el.getBoundingClientRect();
        if (r.width === 0 || r.right <= vw + 1) continue;
        // Skip children of an element that already clips them sideways.
        let clipped = false;
        for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
          const ox = getComputedStyle(p).overflowX;
          if (ox !== "visible" && p.getBoundingClientRect().right <= vw + 1) {
            clipped = true;
            break;
          }
        }
        if (clipped) continue;
        const cls = typeof el.className === "string" ? el.className.trim().split(/\s+/).slice(0, 3).join(".") : "";
        offenders.push(`${el.tagName.toLowerCase()}${el.id ? `#${el.id}` : ""}${cls ? `.${cls}` : ""} right=${Math.round(r.right)}`);
        if (offenders.length >= 10) break;
      }
    }
    return { vw, scrollWidth, offenders };
  });
  expect(
    report.scrollWidth,
    `horizontal overflow at ${report.vw}px (scrollWidth ${report.scrollWidth}). Offenders:\n  ${report.offenders.join("\n  ")}`,
  ).toBeLessThanOrEqual(report.vw);
}
