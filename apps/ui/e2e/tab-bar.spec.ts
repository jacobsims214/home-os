import type { Page } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn, pressOpener } from "./fixtures";

/**
 * Phone bottom tab bar and More sheet (#4536), per KB design/app-shell.md:
 * Today / Calendar / Lists / Meals / More below 768 px, nothing from 768 up.
 * Viewports are pinned here. Phone widths run in desktop-chrome and on
 * iphone-390 (WebKit, touch); 768/1440 only in desktop-chrome (#4757).
 */

async function gotoHome(page: Page) {
  await page.goto("/dashboard");
  await expect(page.getByRole("heading", { name: /^(Good morning|Good afternoon|Good evening|Today)/, level: 2 })).toBeVisible();
}

const tabBar = (page: Page) => page.getByRole("navigation", { name: "Main navigation" });

/**
 * Scrolls to the end of the page, then checks that the last element in <main>
 * ends above the tab bar and that nothing else is drawn over it.
 */
async function expectLastElementUncovered(page: Page) {
  const report = await page.evaluate(() => {
    window.scrollTo(0, document.documentElement.scrollHeight);
    const main = document.querySelector("main")!;
    let last: Element | null = null;
    let lastBottom = -Infinity;
    for (const el of Array.from(main.querySelectorAll("*"))) {
      const r = el.getBoundingClientRect();
      if (r.width === 0 || r.height === 0 || el.children.length > 0) continue;
      if (r.bottom >= lastBottom) {
        last = el;
        lastBottom = r.bottom;
      }
    }
    if (!last) return { ok: false, why: "no element in main" };
    const r = last.getBoundingClientRect();
    const bar = document.querySelector('[data-testid="tab-bar"]')!.getBoundingClientRect();
    const top = document.elementFromPoint(r.left + Math.min(10, r.width / 2), r.bottom - Math.min(4, r.height / 2));
    const covered = !top || !(top === last || last.contains(top) || top.contains(last));
    return {
      ok: r.bottom <= bar.top + 0.5 && !covered,
      why: `last=${last.tagName}.${(last as HTMLElement).className} bottom=${r.bottom} barTop=${bar.top} top=${top?.tagName}`,
    };
  });
  expect(report.ok, report.why).toBe(true);
}

for (const width of [375, 390]) {
  test.describe(`phone ${width}px`, () => {
    test.use({ viewport: { width, height: 844 } });
    test.beforeEach(() => onlyOn("phone"));

    test("tab bar is visible, follows the route, and doesn't cover content", async ({ loggedInPage: page }) => {
      await gotoHome(page);
      const nav = tabBar(page);
      await expect(nav).toBeVisible();

      // Pinned to the bottom of the viewport, 56 px tall, five equal tabs.
      const box = (await page.getByTestId("tab-bar").boundingBox())!;
      expect(Math.round(box.height)).toBe(56);
      expect(Math.round(box.y + box.height)).toBe(844);
      await expect(nav.getByRole("link", { name: "Today" })).toHaveAttribute("aria-current", "page");
      await expect(nav.getByRole("link", { name: "Calendar" })).not.toHaveAttribute("aria-current", "page");
      await expect(nav.getByRole("button", { name: "More" })).toBeVisible();

      // Sections with no page yet aren't links (no 404). Meals left this list
      // in #4567: /dashboard/recipes is a real page, so the tab links to it.
      const soon = nav.getByRole("button", { name: /^Lists/ });
      await expect(soon).toHaveAttribute("aria-disabled", "true");
      await expect(nav.getByRole("link", { name: "Lists" })).toHaveCount(0);
      // aria-disabled (not disabled) so it still explains itself when tapped;
      // Playwright treats aria-disabled as not actionable, hence force.
      await soon.click({ force: true });
      await expect(page.getByText("Lists is coming soon.")).toBeVisible();
      await expect(page).toHaveURL(/\/dashboard$/);

      await expectLastElementUncovered(page);
      await expectNoHorizontalOverflow(page);

      // Active tab follows the route.
      await nav.getByRole("link", { name: "Calendar" }).click();
      await expect(page).toHaveURL(/\/dashboard\/calendar$/);
      await expect(nav.getByRole("link", { name: "Calendar" })).toHaveAttribute("aria-current", "page");
      await expect(nav.getByRole("link", { name: "Today" })).not.toHaveAttribute("aria-current", "page");
    });

    test("More opens the sheet with the other sections and account rows", async ({ loggedInPage: page }) => {
      await gotoHome(page);
      const nav = tabBar(page);
      const more = nav.getByRole("button", { name: "More" });
      await pressOpener(more); // Esc below returns focus here

      const sheet = page.getByRole("dialog", { name: "More" });
      await expect(sheet).toBeVisible();
      await expect(more).toHaveAttribute("aria-expanded", "true");
      // While the sheet is open, More is the active tab.
      await expect(nav.getByRole("link", { name: "Today" })).not.toHaveAttribute("aria-current", "page");

      await expect(sheet.getByRole("searchbox", { name: "Search" })).toBeVisible();
      for (const name of ["Documents", "People & House", "Money", "Guides"]) {
        await expect(sheet.getByText(name, { exact: true })).toBeVisible();
      }
      await expect(sheet.getByRole("button", { name: /^Notifications/ })).toBeVisible();
      await expect(sheet.getByRole("link", { name: /^Switch family/ })).toBeVisible();
      await expect(sheet.getByRole("link", { name: "Settings" })).toBeVisible();
      await expect(sheet.getByRole("button", { name: /Account & log out/ })).toBeVisible();

      // The sheet sits above the tab bar, which stays visible and usable.
      // (Polled: the sheet slides up for 240 ms.)
      const barBox = (await page.getByTestId("tab-bar").boundingBox())!;
      await expect
        .poll(async () => {
          const b = (await sheet.boundingBox())!;
          return Math.round(b.y + b.height);
        })
        .toBe(Math.round(barBox.y - 1));
      await expect(more).toBeVisible();
      await expectNoHorizontalOverflow(page);

      // Esc closes it; focus goes back to More.
      await page.keyboard.press("Escape");
      await expect(sheet).toBeHidden();
      await expect(more).toBeFocused();
      await expect(nav.getByRole("link", { name: "Today" })).toHaveAttribute("aria-current", "page");

      // A row navigates and closes the sheet.
      await more.click();
      await sheet.getByRole("link", { name: "Money" }).click();
      await expect(page).toHaveURL(/\/dashboard\/bills$/);
      await expect(sheet).toBeHidden();

      // The account row opens the account menu (with the logout form).
      await more.click();
      await sheet.getByRole("button", { name: /Account & log out/ }).click();
      await expect(sheet).toBeHidden();
      await expect(page.getByRole("menu")).toBeVisible();
      await expect(page.getByRole("menuitem", { name: "Log out" })).toBeVisible();
    });
  });
}

for (const width of [768, 1440]) {
  test.describe(`${width}px`, () => {
    test.use({ viewport: { width, height: 1024 } });
    test.beforeEach(() => onlyOn("desktop"));

    test("no tab bar", async ({ loggedInPage: page }) => {
      await gotoHome(page);
      await expect(page.getByTestId("tab-bar")).toBeHidden();
      await expect(page.getByRole("button", { name: "More" })).toHaveCount(0);
      // Main isn't padded for a bar that isn't there.
      const padding = await page.locator("main").evaluate((el) => getComputedStyle(el).paddingBottom);
      expect(padding).toBe("0px");
    });
  });
}
