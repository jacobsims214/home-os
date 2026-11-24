import type { Page } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn, pressOpener } from "./fixtures";

/**
 * Phone bottom tab bar and More sheet (#4536), per KB design/app-shell.md:
 * Today / Calendar / Meals / More below 768 px, nothing from 768 up.
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

      // Pinned to the bottom of the viewport, 56 px tall, one cell per tab.
      const box = (await page.getByTestId("tab-bar").boundingBox())!;
      expect(Math.round(box.height)).toBe(56);
      expect(Math.round(box.y + box.height)).toBe(844);
      await expect(nav.getByRole("link", { name: "Today" })).toHaveAttribute("aria-current", "page");
      await expect(nav.getByRole("link", { name: "Calendar" })).not.toHaveAttribute("aria-current", "page");
      await expect(nav.getByRole("button", { name: "More" })).toBeVisible();

      // #5199: every tab is a real destination and a link. A section with no
      // page is not in the nav at all, so no tab is disabled or labelled
      // "coming soon".
      await expect(nav.getByRole("link", { name: "Meals" })).toBeVisible();
      await expect(nav.getByRole("button", { name: /^Lists/ })).toHaveCount(0);
      await expect(page.getByText(/coming soon/i)).toHaveCount(0);

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
      // The Household tiles, each a real destination with descriptive copy
      // (#5199 — no "Coming soon" subtitles).
      for (const name of ["People & House", "Money"]) {
        await expect(sheet.getByText(name, { exact: true })).toBeVisible();
      }
      await expect(sheet.getByText(/coming soon/i)).toHaveCount(0);
      await expect(sheet.getByText("Family, home, cars, pets, vendors")).toBeVisible();
      await expect(sheet.getByRole("button", { name: /^Notifications/ })).toBeVisible();
      // One family in the stub: the row names it and goes to Members, rather
      // than offering a switch there is nothing to switch to (#4574).
      await expect(sheet.getByRole("link", { name: /^Family members/ })).toBeVisible();
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

      // A tile navigates and closes the sheet.
      // #5704: Money's href is the OVERVIEW now, not Bills. /dashboard/money
      // has existed since #4571 and no surface linked to it — the section
      // pointed past a page nobody could reach.
      await more.click();
      await sheet.getByRole("link", { name: "Money" }).click();
      await expect(page).toHaveURL(/\/dashboard\/money$/);
      await expect(sheet).toBeHidden();

      // The account row opens the account menu — the phone's bottom sheet — with
      // the identity header and the logout form (#5198).
      await more.click();
      await sheet.getByRole("button", { name: /Account & log out/ }).click();
      await expect(sheet).toBeHidden();
      const account = page.getByRole("dialog", { name: /Account menu for/ });
      await expect(account).toBeVisible();
      await expect(account.getByRole("button", { name: "Log out" })).toBeVisible();
      await expect(account.getByRole("link", { name: "Profile" })).toBeVisible();
    });
  });
}

/**
 * #5408 defect 2: a page with a floating action button must leave room for it at
 * the bottom. The tab bar was allowed for (Mantine pads <main> by the footer
 * height) and the FAB was not, so its 56 px square sat on the last card and a
 * tap there hit the button instead of the card.
 *
 * Asserted as measured geometry, not as a class name: scroll to the end, then
 * require that nothing in <main> is left under the FAB and that a hit test at
 * the lowest content corner does not land on the button. Runs at 320 px too,
 * where the ticket says it was worst.
 */
for (const width of [390, 320]) {
  test.describe(`FAB clearance ${width}px`, () => {
    test.use({ viewport: { width, height: 844 } });
    test.beforeEach(() => onlyOn("phone"));

    test("the last card clears the FAB and the tab bar", async ({ loggedInPage: page }) => {
      // Lists is the clearer case: eight list cards in three kind sections, so
      // the page scrolls well past one screen and the FAB sits over the last one.
      await page.route(/\/bff\/lists(\?.*)?$/, (route) =>
        route.fulfill({
          json: {
            data: Array.from({ length: 8 }, (_, i) => ({
              id: `lst-${i}`,
              household_id: "h1",
              name: `List number ${i + 1}`,
              kind: ["grocery", "chores", "todo"][i % 3],
              entity_type: null,
              entity_id: null,
              is_template: false,
              archived: false,
              item_count: 4,
              open_count: (i % 4) + 1,
              created_at: "2026-11-10T00:00:00Z",
              updated_at: "2026-11-18T16:00:00Z",
            })),
          },
        }),
      );
      await page.goto("/dashboard/lists");
      const fab = page.getByTestId("lists-fab");
      await expect(fab).toBeVisible();

      const report = await page.evaluate(async () => {
        for (let i = 0; i < 4; i++) {
          window.scrollTo(0, document.documentElement.scrollHeight);
          await new Promise((r) => setTimeout(r, 60));
        }
        const main = document.querySelector("main")!;
        const fabEl = main.querySelector("[data-floating-action]")!;
        const fabBtn = (fabEl.querySelector("button") ?? fabEl) as HTMLElement;
        const f = fabBtn.getBoundingClientRect();
        const bar = document.querySelector('[data-testid="tab-bar"]')!.getBoundingClientRect();

        // Every content box still overlapping either overlay.
        const covered: string[] = [];
        let lowest: DOMRect | null = null;
        for (const el of Array.from(main.querySelectorAll("a, li, article, div, section"))) {
          const r = el.getBoundingClientRect();
          if (r.width < 120 || r.height < 40) continue;
          if (fabEl === el || fabEl.contains(el) || el.contains(fabEl)) continue;
          if (!(el.textContent || "").trim()) continue;
          if (!lowest || r.bottom > lowest.bottom) lowest = r;
          if (r.bottom > f.top + 0.5 && r.right > f.left && r.top < f.bottom) {
            covered.push(`under the FAB: ${(el.textContent || "").trim().slice(0, 30)}`);
          }
          if (r.bottom > bar.top + 0.5) {
            covered.push(`under the bar: ${(el.textContent || "").trim().slice(0, 30)}`);
          }
        }
        // And what a tap at the lowest content corner would actually hit.
        let tapHits = "nothing";
        if (lowest) {
          const px = Math.min(lowest.right - 10, f.x + f.width / 2);
          const py = Math.min(lowest.bottom - 6, f.y + f.height / 2);
          const top = document.elementFromPoint(px, py) as HTMLElement | null;
          tapHits = top === fabBtn || fabBtn.contains(top) ? "the FAB" : (top?.tagName ?? "nothing");
        }
        return {
          covered,
          tapHits,
          gapBelowLowest: lowest ? Math.round(f.top - lowest.bottom) : null,
        };
      });

      expect(report.covered, report.covered.join("; ")).toEqual([]);
      expect(report.tapHits, "a tap at the last card's corner must not hit the FAB").not.toBe("the FAB");
      // There is real room, not a 1 px graze.
      expect(report.gapBelowLowest!).toBeGreaterThan(0);
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
