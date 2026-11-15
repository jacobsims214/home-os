import type { Page } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow } from "./fixtures";

/**
 * Responsive app shell (#4528), per KB design/app-shell.md:
 * phone < 768 header + drawer, tablet 768-991 icon rail, desktop >= 992
 * 260 px sidebar. Viewports are pinned here, so these run once, in the
 * desktop-chrome project (the phone projects already cover the device
 * emulation in smoke.spec.ts).
 */
const HOME = "/dashboard";

test.beforeEach(({}, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chrome", "viewports are pinned per test");
});

async function gotoHome(page: Page) {
  await page.goto(HOME);
  await expect(page.getByRole("heading", { name: "Dashboard", level: 2 })).toBeVisible();
}

/** The topmost element at the centre of the page heading is inside <main>. */
async function expectHeadingUncovered(page: Page) {
  const covered = await page.getByRole("heading", { name: "Dashboard", level: 2 }).evaluate((el) => {
    const r = el.getBoundingClientRect();
    const top = document.elementFromPoint(r.left + Math.min(20, r.width / 2), r.top + r.height / 2);
    return !top || !top.closest("main");
  });
  expect(covered, "page content is covered by navigation").toBe(false);
}

for (const width of [375, 390]) {
  test.describe(`phone ${width}px`, () => {
    test.use({ viewport: { width, height: 844 } });

    test("header, no sidebar over the content, drawer opens and closes", async ({ loggedInPage: page }) => {
      await gotoHome(page);
      const header = page.locator("header");
      await expect(header.getByRole("heading", { level: 1, name: "Today" })).toBeVisible();
      await expect(header.getByRole("button", { name: /^Notifications/ })).toBeVisible();
      await expect(header.getByRole("button", { name: /^Account menu/ })).toBeVisible();
      await expect(header.getByRole("link", { name: /^Family:/ })).toBeVisible();

      // The bug from the audit: the navbar rendered open over every page.
      await expect(page.getByRole("navigation", { name: "Main navigation" })).toBeHidden();
      await expectHeadingUncovered(page);
      await expectNoHorizontalOverflow(page);

      // Open with the menu button, close with Esc; focus goes back to the opener.
      const menu = header.getByRole("button", { name: "Open menu" });
      await menu.click();
      const drawer = page.getByRole("dialog");
      await expect(drawer).toBeVisible();
      await expect(drawer.getByRole("link", { name: "Calendar" })).toBeVisible();
      await expect(drawer.locator('form[method="post"][action="/api/auth/logout"]')).toHaveCount(1);
      await expectNoHorizontalOverflow(page);
      await page.keyboard.press("Escape");
      await expect(drawer).toBeHidden();
      await expect(menu).toBeFocused();

      // Close button.
      await menu.click();
      await expect(drawer).toBeVisible();
      await drawer.getByRole("button", { name: "Close menu" }).click();
      await expect(drawer).toBeHidden();

      // Navigating from the drawer closes it.
      await menu.click();
      await drawer.getByRole("link", { name: "Calendar" }).click();
      await expect(page).toHaveURL(/\/dashboard\/calendar$/);
      await expect(drawer).toBeHidden();
      await expect(header.getByRole("heading", { level: 1, name: "Calendar" })).toBeVisible();
    });
  });
}

test.describe("tablet 768px", () => {
  test.use({ viewport: { width: 768, height: 1024 } });

  test("icon rail beside the content; its menu button opens the drawer", async ({ loggedInPage: page }) => {
    await gotoHome(page);
    const nav = page.getByRole("navigation", { name: "Main navigation" });
    await expect(nav).toBeVisible();
    const box = await nav.boundingBox();
    expect(Math.round(box!.width)).toBe(80);
    await expect(nav.getByRole("link", { name: "Today" })).toHaveAttribute("aria-current", "page");
    await expect(nav.getByRole("link", { name: "Calendar" })).toBeVisible();
    await expect(page.locator("header").getByRole("button", { name: "Open menu" })).toBeHidden();
    await expectHeadingUncovered(page);
    await expectNoHorizontalOverflow(page);

    await nav.getByRole("button", { name: "Open menu" }).click();
    const drawer = page.getByRole("dialog");
    await expect(drawer).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(drawer).toBeHidden();

    await nav.getByRole("link", { name: "Calendar" }).click();
    await expect(page).toHaveURL(/\/dashboard\/calendar$/);
    await expect(nav.getByRole("link", { name: "Calendar" })).toHaveAttribute("aria-current", "page");
  });
});

test.describe("desktop 1440px", () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test("260px sidebar always open, no menu button", async ({ loggedInPage: page }) => {
    await gotoHome(page);
    const nav = page.getByRole("navigation", { name: "Main navigation" });
    await expect(nav).toBeVisible();
    const box = await nav.boundingBox();
    expect(Math.round(box!.width)).toBe(260);
    await expect(nav.getByRole("button", { name: "Logout" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Open menu" })).toHaveCount(0);
    await expect(page.locator("header").getByRole("heading", { level: 1, name: "Today" })).toBeVisible();
    await expectHeadingUncovered(page);
    await expectNoHorizontalOverflow(page);
  });
});
