import { test, expect, expectNoHorizontalOverflow } from "./fixtures";

/**
 * The auth pages (#4562): login, register and reset-password, rebuilt to the
 * approved settings/onboarding mockup (artifact #103 v1, "standalone screens:
 * auth") at phone width.
 *
 * WHY THESE TESTS. The look is checked by comparing screenshots with the
 * artifact; what a screenshot cannot show is whether the front door still
 * opens. So the first test is the whole login flow end-to-end at a 390 px
 * viewport -- /login -> /api/auth/login (real: PKCE cookie + redirect to Dex)
 * -> Dex (played by this test, as Dex does after a password) -> /api/auth/callback
 * (real: code exchange at the stub's DEX_TOKEN_URL) -> the page that asked for
 * the session. A layout change that breaks that chain is worse than an ugly
 * page. The rest are the phone rules the mockup sets and a page can lose
 * silently in a diff: nothing scrolls sideways at 390 px, one h1 per screen,
 * and a 44 px+ tap target on every action.
 *
 * The round trip runs on desktop-chrome at a 390 px viewport, not on
 * iphone-390: WebKit (Safari) refuses `route.fulfill` with a 3xx status
 * ("Cannot fulfill with redirect status"), and the only way to follow a
 * redirect chain from a test is to fulfil one. e2e/login.spec.ts is
 * desktop-chrome for the same reason. The phone LAYOUT assertions run on the
 * real iPhone project, where they belong.
 */

/** The one WebKit phone project; the other three phones repeat it. */
function onlyIPhone(): void {
  test.skip(test.info().project.name !== "iphone-390", "iPhone-only spec (#4562): iphone-390 runs it");
}

function onlyChromiumPhone(): void {
  test.skip(test.info().project.name !== "desktop-chrome", "needs route.fulfill with a 3xx (WebKit refuses it)");
}

test.beforeEach(() => {
  // Nothing here needs the real Dex.
  test.skip(!!process.env.E2E_BASE_URL, "intercepts /api/auth/login and plays Dex; stub mode only");
});

/**
 * The sign-in screen itself at 390 px. /login hands off to Dex as soon as it
 * mounts, so the hand-off is answered with 204 to leave the page on screen --
 * which is also the state a slow Dex leaves a person looking at, so it is
 * worth asserting rather than working around.
 */
test("the sign-in screen fits 390 px and says what it is doing", async ({ page }) => {
  onlyIPhone();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route(
    (u) => u.pathname === "/api/auth/login",
    (route) => route.fulfill({ status: 204 }),
  );

  await page.goto("/login");

  await expect(page.getByRole("heading", { name: "Sign in", level: 1 })).toBeVisible();
  await expect(page.getByText("Redirecting to login...")).toBeVisible();
  // A live region, so a screen reader is told the page is working rather than
  // silently waiting on a redirect.
  await expect(page.locator('[data-auth-page="login"]')).toHaveAttribute("aria-live", "polite");
  // The layout's brand row names the product, once, above the heading.
  await expect(page.getByText("Home OS", { exact: true })).toBeVisible();
  await expect(page.locator("h1")).toHaveCount(1);
  await expectNoHorizontalOverflow(page);
});

/**
 * The full round trip the ticket calls "login e2e", at 390 x 844. Same
 * mechanism as e2e/login.spec.ts: Playwright cannot route the requests a
 * redirect leads to, so the /api/auth/login hop is intercepted, its redirect to
 * Dex is read for redirect_uri + state, and Dex's job (redirect back with a
 * code) is done from there. The callback, the code exchange and the cookie are
 * the real code.
 */
test("login completes end-to-end at 390 px and lands on the page that asked for it", async ({
  page,
  request,
  baseURL,
}) => {
  onlyChromiumPhone();
  await page.setViewportSize({ width: 390, height: 844 });

  const stubUrl = `http://127.0.0.1:${process.env.E2E_STUB_PORT || 3901}`;
  const origin = new URL(baseURL!).origin;
  let redirectUri = "";
  let sawAuthorize = false;

  await page.route("**/api/auth/login?**", async (route) => {
    const res = await route.fetch({ maxRedirects: 0 });
    const authorize = new URL(res.headers()["location"] ?? "", origin);
    // The auth protocol is out of scope for #4562: this asserts the rebuild did
    // not quietly change it (PKCE stays S256, the callback URI is unchanged).
    expect(authorize.pathname).toBe("/dex/auth");
    expect(authorize.searchParams.get("code_challenge_method")).toBe("S256");
    expect(authorize.searchParams.get("code_challenge")).toBeTruthy();
    sawAuthorize = true;
    redirectUri = authorize.searchParams.get("redirect_uri") ?? "";
    const callback = new URL(redirectUri);
    callback.searchParams.set("code", "e2e-code");
    callback.searchParams.set("state", authorize.searchParams.get("state") ?? "");
    const headers = { ...res.headers(), location: callback.toString() };
    await route.fulfill({ status: 302, headers });
  });

  await page.goto(`/login?redirect=${encodeURIComponent("/dashboard/bills")}`);

  // Poll rather than waitForURL: under next dev StrictMode runs the page's
  // effect twice, so the first login start is aborted by the second.
  await expect.poll(() => new URL(page.url()).pathname, { timeout: 30_000 }).toBe("/dashboard/bills");
  expect(new URL(page.url()).origin).toBe(origin);
  expect(sawAuthorize, "the page reached Dex's authorize endpoint").toBe(true);

  // The callback sent Dex the same redirect_uri the authorize request did, and
  // the session cookie is the httpOnly one (#4412), not something a script set.
  expect(redirectUri).toBe(`${origin}/api/auth/callback`);
  const stats = (await (await request.get(`${stubUrl}/e2e/stats`)).json()) as { lastRedirectUri: string };
  expect(stats.lastRedirectUri).toBe(redirectUri);
  const cookies = await page.context().cookies();
  expect(cookies.find((c) => c.name === "home-os-token")?.httpOnly).toBe(true);
});

test("login error state fits 390 px and offers one 44 px+ way forward", async ({ page }) => {
  onlyIPhone();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/login?error=access_denied");

  // Scoped to the page: Next also renders an empty route announcer with
  // role="alert", which a bare getByRole("alert") would match too.
  const alert = page.locator('[data-auth-page="login"]').getByRole("alert");
  await expect(alert).toContainText("Sign-in didn't complete");
  await expect(alert).toContainText("access_denied");
  const retry = page.getByRole("link", { name: "Try again" });
  await expect(retry).toBeVisible();
  expect((await retry.boundingBox())!.height, "a 44 px tap target").toBeGreaterThanOrEqual(44);
  // Exactly one h1: the (auth) layout draws no heading of its own.
  await expect(page.locator("h1")).toHaveCount(1);
  await expectNoHorizontalOverflow(page);
});

test("register explains that accounts come from an admin, at 390 px", async ({ page }) => {
  onlyIPhone();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/register");

  await expect(page.getByRole("heading", { name: "Registration", level: 1 })).toBeVisible();
  await expect(page.getByText(/only be created by a household administrator/)).toBeVisible();
  const back = page.getByRole("link", { name: "Back to sign in" });
  await expect(back).toHaveAttribute("href", "/login");
  expect((await back.boundingBox())!.height).toBeGreaterThanOrEqual(44);
  await expect(page.locator("h1")).toHaveCount(1);
  // The layout's brand row names the product, once.
  await expect(page.getByText("Home OS", { exact: true })).toBeVisible();
  await expectNoHorizontalOverflow(page);
});

test("reset-password: a dead link and the form both fit 390 px", async ({ page }) => {
  onlyIPhone();
  await page.setViewportSize({ width: 390, height: 844 });

  // No token: the mockup's dead-link screen, not a form that cannot work.
  await page.goto("/reset-password");
  await expect(page.getByRole("heading", { name: "Invalid reset link", level: 1 })).toBeVisible();
  await expect(page.getByRole("link", { name: "Back to sign in" })).toHaveAttribute("href", "/login");
  await expectNoHorizontalOverflow(page);

  // With a token: two 48 px password fields and the submit action. The fields
  // are asserted at 16 px text as well -- below that iOS zooms the whole
  // viewport the moment one takes focus, which is the phone bug this rebuild
  // exists to avoid.
  await page.goto("/reset-password?token=e2e-token");
  await expect(page.getByRole("heading", { name: "Choose a new password", level: 1 })).toBeVisible();
  for (const name of ["New password", "Type it again"]) {
    const input = page.getByRole("textbox", { name });
    const box = await input.boundingBox();
    expect(box!.height, `${name} is a 48 px field`).toBeGreaterThanOrEqual(44);
    const fontSize = await input.evaluate((el) => parseFloat(getComputedStyle(el).fontSize));
    expect(fontSize, `${name} does not trigger iOS zoom`).toBeGreaterThanOrEqual(16);
  }
  const submit = page.getByRole("button", { name: "Save password and sign in" });
  expect((await submit.boundingBox())!.height).toBeGreaterThanOrEqual(44);
  await expect(page.locator("h1")).toHaveCount(1);
  await expectNoHorizontalOverflow(page);

  // Mismatched passwords are caught before the request: no token is spent.
  await page.getByRole("textbox", { name: "New password" }).fill("maple-otter-river");
  await page.getByRole("textbox", { name: "Type it again" }).fill("maple-otter-rivr");
  await submit.click();
  await expect(page.getByText("Passwords do not match")).toBeVisible();
  await expectNoHorizontalOverflow(page);
});
