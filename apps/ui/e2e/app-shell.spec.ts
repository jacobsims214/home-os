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
      // (The phone's "Main navigation" is the bottom tab bar, tab-bar.spec.ts.)
      await expect(page.locator(".mantine-AppShell-navbar")).toBeHidden();
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
    await expect(page.getByRole("button", { name: "Open menu" })).toHaveCount(0);
    await expect(page.locator("header").getByRole("heading", { level: 1, name: "Today" })).toBeVisible();
    await expectHeadingUncovered(page);
    await expectNoHorizontalOverflow(page);

    // Sidebar content from nav-config (#4543): all nine sections, Today current.
    expect(await sectionNames(nav)).toEqual(ALL_SECTIONS);
    await expect(nav.getByRole("link", { name: "Today" })).toHaveAttribute("aria-current", "page");
    await expect(nav.getByRole("search")).toBeVisible();

    // "/" focuses the sidebar search.
    await page.locator("body").click({ position: { x: 900, y: 600 } });
    await page.keyboard.press("/");
    await expect(nav.getByRole("searchbox", { name: "Search" })).toBeFocused();

    // The user button opens the account menu upward, with the logout form.
    await nav.getByRole("button", { name: /^Account menu for/ }).click();
    await expect(page.getByRole("menuitem", { name: "Log out" })).toBeVisible();
    await expect(page.locator('form[method="post"][action="/api/auth/logout"]').filter({ has: page.getByRole("menuitem", { name: "Log out" }) })).toHaveCount(1);
    await expect(page.getByRole("radiogroup", { name: "Theme" })).toBeVisible();
  });
});

// ─── Sidebar / drawer content (#4543) ─────────────────────────

const ALL_SECTIONS = ["Today", "Calendar", "Lists", "Meals", "Documents", "People & House", "Money", "Guides", "Settings"];

/** Section names in a nav surface, in order ("Soon" tags stripped). */
async function sectionNames(scope: ReturnType<Page["locator"]>) {
  const rows = scope.locator('ul li > a, ul li > span[aria-disabled="true"]');
  const texts = await rows.evaluateAll((els) =>
    els.map((el) => (el.querySelector('[class*="rowLabel"]')?.textContent ?? el.textContent ?? "").trim()),
  );
  return texts;
}

/** Answers GET /bff/me/households as a member with `role` in the current household. */
async function asRole(page: Page, role: string) {
  await page.route("**/bff/me/households", (route) =>
    route.fulfill({
      json: { data: [{ id: "h1", name: "Sims", timezone: "UTC", role, joined_at: "2026-01-01T00:00:00Z", is_default: true, current: true }] },
    }),
  );
}

test.describe("phone 390px drawer content", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("the drawer shows the same nine sections as the sidebar, and logs out through the logout route", async ({ loggedInPage: page }) => {
    await gotoHome(page);
    await page.locator("header").getByRole("button", { name: "Open menu" }).click();
    const drawer = page.getByRole("dialog", { name: /Home OS menu/ });
    await expect(drawer).toBeVisible();
    expect(await sectionNames(drawer)).toEqual(ALL_SECTIONS);
    await expect(drawer.getByRole("link", { name: "Today" })).toHaveAttribute("aria-current", "page");
    await expect(drawer.getByRole("searchbox", { name: "Search" })).toBeVisible();
    // 16 px so iOS doesn't zoom on focus.
    expect(await drawer.getByRole("searchbox", { name: "Search" }).evaluate((el) => getComputedStyle(el).fontSize)).toBe("16px");

    const logout = drawer.getByRole("button", { name: "Log out" });
    await expect(logout).toBeVisible();
    const action = await logout.evaluate((el) => {
      const f = el.closest("form");
      return f ? `${f.method} ${f.getAttribute("action")}` : null;
    });
    expect(action).toBe("post /api/auth/logout");
    await expectNoHorizontalOverflow(page);
  });
});

test.describe("role-aware navigation", () => {
  test("housesitter: no Money or Documents in the sidebar, drawer, rail or More sheet", async ({ loggedInPage: page }) => {
    await asRole(page, "housesitter");
    const expected = ["Today", "Calendar", "Lists", "Meals", "People & House", "Guides", "Settings"];

    await page.setViewportSize({ width: 1440, height: 900 });
    await gotoHome(page);
    const nav = page.getByRole("navigation", { name: "Main navigation" });
    await expect.poll(() => sectionNames(nav)).toEqual(expected);

    await page.setViewportSize({ width: 768, height: 1024 });
    await expect(nav.getByRole("link", { name: "Money" })).toHaveCount(0);
    await expect(nav.getByRole("link", { name: "People & House" })).toBeVisible();
    await nav.getByRole("button", { name: "Open menu" }).click();
    const drawer = page.getByRole("dialog", { name: /Home OS menu/ });
    await expect(drawer).toBeVisible();
    expect(await sectionNames(drawer)).toEqual(expected);
    await page.keyboard.press("Escape");

    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByTestId("tab-bar").getByRole("button", { name: "More" }).click();
    const sheet = page.getByRole("dialog", { name: "More" });
    await expect(sheet).toBeVisible();
    await expect(sheet.getByText("Money", { exact: true })).toHaveCount(0);
    await expect(sheet.getByText("Documents", { exact: true })).toHaveCount(0);
    await expect(sheet.getByText("People & House", { exact: true })).toBeVisible();
  });

  test("vendor: Today, People & House and Settings only; tab bar keeps Today and More", async ({ loggedInPage: page }) => {
    await asRole(page, "vendor");
    await page.setViewportSize({ width: 1440, height: 900 });
    await gotoHome(page);
    const nav = page.getByRole("navigation", { name: "Main navigation" });
    await expect.poll(() => sectionNames(nav)).toEqual(["Today", "People & House", "Settings"]);

    await page.setViewportSize({ width: 390, height: 844 });
    const bar = page.getByTestId("tab-bar");
    await expect(bar.getByRole("link", { name: "Today" })).toBeVisible();
    await expect(bar.getByRole("button", { name: "More" })).toBeVisible();
    await expect(bar.getByText("Calendar")).toHaveCount(0);
    await expectNoHorizontalOverflow(page);
  });
});
