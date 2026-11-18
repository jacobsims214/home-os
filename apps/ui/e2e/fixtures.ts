import { test as base, expect, type APIRequestContext, type Locator, type Page } from "@playwright/test";

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
  /**
   * The stub session's id, so a spec can seed per-session data for the page it
   * signed in (POST /e2e/households, #4574). Null in external mode.
   */
  stubSid: string | null;
  /** Reads the stub's counters. Stub mode only. */
  stubStats: () => Promise<StubStats>;
};

type Tokens = { id_token: string; refresh_token: string; expires_in: number; sid?: string };

/** Signs a context in with stub-minted tokens, set as the callback sets them. */
export async function signInWithStub(
  page: Page,
  baseURL: string,
  opts: { idToken?: boolean } = {},
): Promise<Tokens> {
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
  // The stub's sign-in id: POST /e2e/households seeds a multi-family account
  // with it (#4574), the way the secrets endpoints use it per session.
  return tokens;
}

/**
 * Gives the signed-in session a list of families (#4574). Without this, every
 * session is the single seed household, which is what the single-family specs
 * (and the "no switcher for one family" spec) rely on.
 */
export async function seedHouseholds(
  request: APIRequestContext,
  sid: string,
  households: Array<{ id: string; name: string; role: string; timezone?: string; bills?: unknown[] }>,
): Promise<void> {
  const res = await request.post(`${stubUrl}/e2e/households`, { data: { sid, households } });
  expect(res.ok(), "seed households").toBeTruthy();
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
  // One sign-in per test, shared by both fixtures: loggedInPage is the page with
  // the session cookies, stubSid is the stub-side id of that same session.
  stubSid: async ({ page, baseURL }, use) => {
    if (external) {
      await use(null);
      return;
    }
    const tokens = await signInWithStub(page, baseURL!);
    await use(tokens.sid ?? null);
  },
  loggedInPage: async ({ page, baseURL, stubSid }, use) => {
    if (external) await signInWithDex(page);
    // stubSid's sign-in already set the cookies on this page's context.
    void stubSid;
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
 * Activates a button from the keyboard (focus + Enter), for the checks that
 * focus returns to the opener when a dialog closes. Focus return is for
 * keyboard users, and WebKit (Safari) never focuses a button on click or tap,
 * so a clicked opener has nothing to return focus to there (#4757).
 */
export async function pressOpener(opener: Locator): Promise<void> {
  await opener.focus();
  await opener.page().keyboard.press("Enter");
}

/**
 * Projects that run the viewport-pinned shell/component specs (#4757).
 * Phone-width tests run in desktop Chromium and on iphone-390, which is
 * WebKit with isMobile and hasTouch: the iOS engine (safe areas,
 * visualViewport, history back, sticky, dvh) the phone layouts target. Only
 * one iPhone project, to keep CI time down. Tablet/desktop widths run once,
 * in desktop-chrome.
 */
export const PHONE_PROJECTS = ["desktop-chrome", "iphone-390"];

/**
 * Skips the current test unless this project runs tests of `kind`. Call it
 * first thing in a test body or a describe-level beforeEach.
 */
export function onlyOn(kind: "phone" | "desktop"): void {
  const project = test.info().project.name;
  if (kind === "phone") test.skip(!PHONE_PROJECTS.includes(project), "phone-width test: desktop-chrome and iphone-390 only");
  else test.skip(project !== "desktop-chrome", "viewport is pinned to tablet/desktop: desktop-chrome only");
}

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
