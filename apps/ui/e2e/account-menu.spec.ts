import type { Page, TestInfo } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { test, expect, expectNoHorizontalOverflow, onlyOn, pressOpener } from "./fixtures";

/**
 * The account menu (#5198), per KB design/app-shell.md ("Family switcher, user
 * menu, notifications") and artifact #75 v1:
 *
 *   phone  < 768 px    a bottom sheet — grab handle, radius 16 at the top,
 *                      the identity header (avatar, name, email), Profile /
 *                      Theme / Notification settings rows, full-width
 *                      ghost-danger Log out.
 *   >= 768 px          the same content in a 320 px card popover, opening under
 *                      the header avatar (tablet) or upward from the sidebar's
 *                      user button (desktop).
 *
 * The old build was a small anchored dropdown at every breakpoint with a static
 * "Account" label and no Profile / Notification settings rows — items D3/D4 of
 * the build-vs-design review (artifact #168).
 */
const HOME = "/dashboard";

async function gotoHome(page: Page) {
  await page.goto(HOME);
  await expect(page.getByRole("heading", { name: /^(Good morning|Good afternoon|Good evening|Today)/, level: 2 })).toBeVisible();
}

/** The menu's dialog, whichever surface it is. */
const menu = (page: Page) => page.getByRole("dialog", { name: /Account menu for/ });

/** Every row and control the artifact draws, in both surfaces. */
async function expectArtifactContent(page: Page, scope: ReturnType<typeof menu>) {
  // Identity header: name and email, from GET /auth/me (the stub's seed user).
  await expect(scope.getByText("E2E Tester")).toBeVisible();
  await expect(scope.getByText("e2e@homeos.test")).toBeVisible();
  // The three rows, in the artifact's order.
  await expect(scope.getByRole("link", { name: "Profile" })).toBeVisible();
  await expect(scope.getByRole("radiogroup", { name: "Theme" })).toBeVisible();
  await expect(scope.getByRole("link", { name: "Notification settings" })).toBeVisible();
  // Full-width ghost-danger Log out, inside the native logout form.
  const logout = scope.getByRole("button", { name: "Log out" });
  await expect(logout).toBeVisible();
  await expect(scope.locator('form[method="post"][action="/api/auth/logout"]')).toHaveCount(1);
  // Full width of its container, 48 px tall.
  const [button, body] = [await logout.boundingBox(), await scope.locator("[data-account-menu-body]").boundingBox()];
  expect(Math.round(button!.width)).toBeGreaterThanOrEqual(Math.round(body!.width) - 40);
  expect(Math.round(button!.height)).toBeGreaterThanOrEqual(44);
}

async function snap(page: Page, testInfo: TestInfo, name: string) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

async function expectNoSeriousAxeViolations(page: Page) {
  const { violations } = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  const bad = violations
    .filter((v) => v.impact === "critical" || v.impact === "serious")
    .map((v) => `${v.impact} ${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(" | ")}`);
  expect(bad, "critical/serious axe violations").toEqual([]);
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`phone 390 ${colorScheme}`, () => {
    test.use({ viewport: { width: 390, height: 844 }, colorScheme });
    test.beforeEach(() => onlyOn("phone"));

    test("the avatar opens a bottom sheet with the artifact's content, and Esc returns focus", async ({ loggedInPage: page }, testInfo) => {
      await gotoHome(page);
      const opener = page.locator("header").getByRole("button", { name: /^Account menu for/ });
      await expect(opener).toHaveAttribute("aria-haspopup", "dialog");
      await pressOpener(opener);

      const sheet = menu(page);
      await expect(sheet).toBeVisible();
      await expect(opener).toHaveAttribute("aria-expanded", "true");
      await expectArtifactContent(page, sheet);

      // A sheet, not a popover: it spans the viewport width, sits on the bottom
      // edge, and leaves the top of the screen uncovered. Its grab handle shows.
      await expect.poll(async () => Math.round((await sheet.boundingBox())!.width)).toBe(390);
      await expect
        .poll(async () => {
          const b = (await sheet.boundingBox())!;
          return Math.round(b.y + b.height);
        })
        .toBe(844);
      await expect(sheet.locator("[class*='handle']")).toBeVisible();
      // 52 px rows and a 44 px avatar, per the spec's touch sizes.
      const avatar = await sheet.locator(".mantine-Avatar-root").first().boundingBox();
      expect(Math.round(avatar!.width)).toBe(44);

      await page.waitForTimeout(400); // the 240 ms slide-up
      await snap(page, testInfo, `phone-390-${colorScheme}-account-sheet`);
      await expectNoSeriousAxeViolations(page);

      await page.keyboard.press("Escape");
      await expect(sheet).toBeHidden();
      await expect(opener).toBeFocused();
      await expect(opener).toHaveAttribute("aria-expanded", "false");
      await expectNoHorizontalOverflow(page);
    });
  });
}

test.describe("tablet 820", () => {
  test.use({ viewport: { width: 820, height: 1024 } });
  test.beforeEach(() => onlyOn("desktop"));

  test("the avatar opens the 320 px card popover under it", async ({ loggedInPage: page }, testInfo) => {
    await gotoHome(page);
    const opener = page.locator("header").getByRole("button", { name: /^Account menu for/ });
    await pressOpener(opener);
    const popover = menu(page);
    await expect(popover).toBeVisible();
    await expectArtifactContent(page, popover);

    // The card: 320 px wide, radius 12, a 1 px border, no grab handle.
    expect(Math.round((await popover.boundingBox())!.width)).toBe(320);
    await expect(popover).toHaveCSS("border-radius", "12px");
    await expect(popover.locator("[class*='handle']")).toHaveCount(0);
    // Anchored to the avatar: its top is just under the avatar's bottom.
    const [pb, ob] = [await popover.boundingBox(), await opener.boundingBox()];
    expect(pb!.y).toBeGreaterThan(ob!.y + ob!.height - 1);

    await snap(page, testInfo, "tablet-820-light-account-popover");
    await expectNoSeriousAxeViolations(page);
    await expectNoHorizontalOverflow(page);
  });
});

test.describe("desktop 1440", () => {
  test.use({ viewport: { width: 1440, height: 900 } });
  test.beforeEach(() => onlyOn("desktop"));

  test("the sidebar user button opens the popover upward", async ({ loggedInPage: page }, testInfo) => {
    await gotoHome(page);
    const nav = page.getByRole("navigation", { name: "Main navigation" });
    const opener = nav.getByRole("button", { name: /^Account menu for/ });
    await pressOpener(opener);
    const popover = menu(page);
    await expect(popover).toBeVisible();
    await expectArtifactContent(page, popover);

    // Opens upward from the sidebar foot, and stays inside the sidebar's column.
    const [pb, ob] = [await popover.boundingBox(), await opener.boundingBox()];
    expect(pb!.y + pb!.height).toBeLessThanOrEqual(ob!.y + 1);
    expect(pb!.x).toBeLessThan(300);

    await snap(page, testInfo, "desktop-1440-light-account-popover");
    await expectNoSeriousAxeViolations(page);
  });
});

test.describe("the rows go where they say", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test.beforeEach(() => onlyOn("phone"));

  test("Profile opens the Account settings section; the theme picker still works", async ({ loggedInPage: page }) => {
    await gotoHome(page);
    await page.locator("header").getByRole("button", { name: /^Account menu for/ }).click();
    const sheet = menu(page);
    await expect(sheet).toBeVisible();
    // The Theme row is the real #4529 picker, and choosing there changes the app.
    await sheet.getByRole("radiogroup", { name: "Theme" }).getByText("Dark", { exact: true }).click();
    await expect.poll(() => page.locator("html").getAttribute("data-mantine-color-scheme")).toBe("dark");

    await sheet.getByRole("link", { name: "Profile" }).click();
    await expect(page).toHaveURL(/\/dashboard\/settings\/account$/);
    await expect(sheet).toBeHidden();
  });
});
