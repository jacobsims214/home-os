import type { Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { sectionsForRole, type NavSection } from "../components/shared/nav-config";
import { test, expect, expectNoHorizontalOverflow, onlyOn, pressOpener, SEED_HOUSEHOLD_NAME } from "./fixtures";

/**
 * Responsive app shell (#4528), per KB design/app-shell.md:
 * phone < 768 header + drawer, tablet 768-991 icon rail, desktop >= 992
 * 260 px sidebar. Viewports are pinned here. Phone-width tests run in
 * desktop-chrome and on iphone-390 (WebKit, touch); tablet and desktop
 * widths only in desktop-chrome (#4757, see onlyOn in fixtures.ts).
 */
const HOME = "/dashboard";

async function gotoHome(page: Page) {
  await page.goto(HOME);
  await expect(page.getByRole("heading", { name: /^(Good morning|Good afternoon|Good evening|Today)/, level: 2 })).toBeVisible();
}

/** The topmost element at the centre of the page heading is inside <main>. */
async function expectHeadingUncovered(page: Page) {
  const covered = await page.getByRole("heading", { name: /^(Good morning|Good afternoon|Good evening|Today)/, level: 2 }).evaluate((el) => {
    const r = el.getBoundingClientRect();
    const top = document.elementFromPoint(r.left + Math.min(20, r.width / 2), r.top + r.height / 2);
    return !top || !top.closest("main");
  });
  expect(covered, "page content is covered by navigation").toBe(false);
}

for (const width of [375, 390]) {
  test.describe(`phone ${width}px`, () => {
    test.use({ viewport: { width, height: 844 } });
    test.beforeEach(() => onlyOn("phone"));

    test("header, no sidebar over the content, drawer opens and closes", async ({ loggedInPage: page }) => {
      await gotoHome(page);
      const header = page.locator("header");
      await expect(header.getByRole("heading", { level: 1, name: "Today" })).toBeVisible();
      await expect(header.getByRole("button", { name: /^Notifications/ })).toBeVisible();
      await expect(header.getByRole("button", { name: /^Account menu/ })).toBeVisible();
      // The family chip (#4574): this account has one family in the stub, so it
      // is a plain label naming it, not a switcher button. It names the SEED
      // household: "Your family" is the unknown-household placeholder, so
      // asserting that would pass on an empty GET /me/households (#5322).
      await expect(header.getByText(SEED_HOUSEHOLD_NAME, { exact: true })).toBeVisible();
      await expect(header.getByRole("button", { name: /^Family:/ })).toHaveCount(0);

      // The bug from the audit: the navbar rendered open over every page.
      // (The phone's "Main navigation" is the bottom tab bar, tab-bar.spec.ts.)
      await expect(page.locator(".mantine-AppShell-navbar")).toBeHidden();
      await expectHeadingUncovered(page);
      await expectNoHorizontalOverflow(page);

      // Open with the menu button, close with Esc; focus goes back to the opener.
      const menu = header.getByRole("button", { name: "Open menu" });
      await pressOpener(menu);
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
  test.beforeEach(() => onlyOn("desktop"));

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
  test.beforeEach(() => onlyOn("desktop"));

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

    // Sidebar content from nav-config (#4543): every section, Today current.
    expect(await sectionNames(nav)).toEqual(ALL_SECTIONS);
    await expect(nav.getByRole("link", { name: "Today" })).toHaveAttribute("aria-current", "page");
    await expect(nav.getByRole("search")).toBeVisible();

    // "/" focuses the sidebar search.
    await page.locator("body").click({ position: { x: 900, y: 600 } });
    await page.keyboard.press("/");
    await expect(nav.getByRole("searchbox", { name: "Search" })).toBeFocused();

    // The user button opens the account menu upward (#5198: a card popover with
    // the identity header, Profile / Theme / Notification settings and Log out).
    await nav.getByRole("button", { name: /^Account menu for/ }).click();
    const account = page.getByRole("dialog", { name: "Account menu" });
    await expect(account).toBeVisible();
    await expect(account.getByRole("button", { name: "Log out" })).toBeVisible();
    // The native logout form is inside the menu, wrapped around its button.
    await expect(account.locator('form[method="post"][action="/api/auth/logout"]')).toHaveCount(1);
    await expect(account.getByRole("radiogroup", { name: "Theme" })).toBeVisible();
    await expect(account.getByRole("link", { name: "Profile" })).toBeVisible();
    await expect(account.getByRole("link", { name: "Notification settings" })).toBeVisible();
  });
});

// ─── Sidebar / drawer content (#4543) ─────────────────────────

// #5199: only sections with a real page are in the nav, so each row is a link
// and there is no "Soon" tag to strip.
//
// #5313: the expected labels come FROM the nav config, never a copy of it. The
// hand-kept list here had drifted (#4566 restored Lists; this fixture kept
// asserting seven sections), which left these cases red for good. A section
// added to nav-config.ts now appears here without an edit, and one removed
// fails loudly instead of quietly leaving a stale expectation behind.
const navLabels = (sections: readonly NavSection[]) => sections.map((s) => s.label);

/** Every section in nav order: what an owner — or an unknown role — sees. */
const ALL_SECTIONS = navLabels(sectionsForRole(null));

/** Section names in a nav surface, in order. */
async function sectionNames(scope: ReturnType<Page["locator"]>) {
  const rows = scope.locator("ul li > a, ul li > span[aria-disabled=\"true\"]");
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
  test.beforeEach(() => onlyOn("phone"));

  test("the drawer shows the same sections as the sidebar, and logs out through the logout route", async ({ loggedInPage: page }) => {
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
  // Resizes across all three breakpoints in one page: desktop-chrome only.
  test.beforeEach(() => onlyOn("desktop"));
  // #4569: Documents joined the Household group (READERS only), so a family
  // member sees it and a housesitter does not, the same as Money.
  test("housesitter: no Money or Documents in the sidebar, drawer, rail or More sheet", async ({ loggedInPage: page }) => {
    await asRole(page, "housesitter");
    // Also from nav-config (#5313): Lists is SITTERS, like Calendar and Meals.
    const expected = navLabels(sectionsForRole("housesitter"));

    await page.setViewportSize({ width: 1440, height: 900 });
    await gotoHome(page);
    const nav = page.getByRole("navigation", { name: "Main navigation" });
    await expect.poll(() => sectionNames(nav)).toEqual(expected);

    await page.setViewportSize({ width: 768, height: 1024 });
    await expect(nav.getByRole("link", { name: "Money" })).toHaveCount(0);
    await expect(nav.getByRole("link", { name: "Documents" })).toHaveCount(0);
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
    // Right after the 1440 -> 390 resize the header can be 2 px wide for a frame
    // while Mantine's media queries switch (flaky before #4752 too); let it settle.
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
    await expectNoHorizontalOverflow(page);
  });
});

// ─── Header bell and axe (#4752) ──────────────────────────────

/**
 * Runs axe-core (WCAG 2.x A/AA rules) over the whole page (the shell's header,
 * navigation, popovers and dialogs, plus the /dashboard content in <main>, which
 * has been included since the badge contrast fix in #4754) and fails on any
 * critical or serious violation, listing the rule ids and offending elements.
 */
async function expectNoSeriousAxeViolations(page: Page) {
  const { violations } = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  const bad = violations
    .filter((v) => v.impact === "critical" || v.impact === "serious")
    .map((v) => `${v.impact} ${v.id}: ${v.nodes.map((n) => `${n.target.join(" ")} ${n.html.slice(0, 160)}`).join(" | ")}`);
  expect(bad, "critical/serious axe violations").toEqual([]);
}

for (const width of [390, 1440]) {
  test.describe(`header bell ${width}px`, () => {
    test.use({ viewport: { width, height: width < 768 ? 844 : 900 } });
    test.beforeEach(() => onlyOn(width < 768 ? "phone" : "desktop"));

    test("the bell button owns the popover ARIA, opens and closes, and axe finds nothing critical/serious", async ({ loggedInPage: page }) => {
      await page.route("**/bff/notifications", (route) =>
        route.fulfill({
          json: { data: [{ id: "n1", title: "Water bill due Friday", is_read: false, created_at: "2026-11-16T09:00:00Z" }] },
        }),
      );
      await gotoHome(page);
      const header = page.locator("header");
      const bell = header.getByRole("button", { name: "Notifications, 1 unread" });
      await expect(bell).toBeVisible();

      // Badge still renders on the bell's top-right corner.
      const badge = header.locator(".mantine-Indicator-indicator");
      await expect(badge).toHaveText("1");
      const [bb, ib] = [await bell.boundingBox(), await badge.boundingBox()];
      expect(ib!.x + ib!.width / 2).toBeGreaterThan(bb!.x + bb!.width / 2);
      expect(ib!.y + ib!.height / 2).toBeLessThan(bb!.y + bb!.height / 2);

      // Only the button carries the popup attributes (the Indicator's <div> did, axe aria-allowed-attr).
      await expect(bell).toHaveAttribute("aria-haspopup", "dialog");
      await expect(bell).toHaveAttribute("aria-expanded", "false");
      await expect(header.locator("[aria-haspopup]:not(button)")).toHaveCount(0);
      await expect(header.locator("[aria-expanded]:not(button)")).toHaveCount(0);
      await expectNoSeriousAxeViolations(page);

      // Opens (from the keyboard, see pressOpener); aria-controls points at the dialog.
      await pressOpener(bell);
      const dialog = page.getByRole("dialog", { name: "Notifications" });
      await expect(dialog).toBeVisible();
      await expect(bell).toHaveAttribute("aria-expanded", "true");
      const controls = await bell.getAttribute("aria-controls");
      expect(controls).toBeTruthy();
      expect(await dialog.getAttribute("id")).toBe(controls);
      await expect(dialog.getByText("Water bill due Friday")).toBeVisible();
      // Wait out the fade-in: axe reads partly transparent text as low contrast.
      await expect.poll(() => dialog.evaluate((el) => getComputedStyle(el).opacity)).toBe("1");
      await expectNoSeriousAxeViolations(page);

      // Esc closes it and focus returns to the bell.
      await page.keyboard.press("Escape");
      await expect(dialog).toBeHidden();
      await expect(bell).toBeFocused();
      await expect(bell).toHaveAttribute("aria-expanded", "false");

      // #4563: the bell opens a POPOVER from 768 px up, where clicking it again
      // toggles the panel closed. On phones it opens a bottom SHEET with a
      // scrim, and that scrim covers the header, so the panel is closed by the
      // scrim, Esc or its own close button — not by a second bell click.
      await bell.click();
      await expect(dialog).toBeVisible();
      if (width >= 768) {
        await bell.click();
        await expect(dialog).toBeHidden();
      } else {
        await page.mouse.click(8, 8); // the scrim, above the sheet
        await expect(dialog).toBeHidden();
        await expect(bell).toHaveAttribute("aria-expanded", "false");
      }
    });
  });
}
