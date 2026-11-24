import type { Page } from "@playwright/test";
import { test, expect, onlyOn } from "./fixtures";
import { NAV_SECTIONS } from "../components/shared/nav-config";

/**
 * How many section rows a nav surface draws, derived from nav-config rather than
 * hand-kept. The previous literal (7) had already drifted from the 8 sections
 * in the list and was red for good; #5408 added Guides, making it 9. A section
 * added or removed in nav-config now updates this with no edit here, which is
 * the rule app-shell.spec.ts states for its own fixture.
 */
const SECTION_ROW_COUNT = NAV_SECTIONS.length;

/**
 * No placeholder copy in the shell (#5199).
 *
 * The built shell used to ship "Soon" pills on Lists / Documents / Guides in
 * the sidebar, drawer and rail, and "Coming soon" subtitles on the Documents
 * and Guides tiles in the More sheet (artifact #168, D7/D8). That is
 * placeholder text advertising pages that do not exist: the approved shell
 * (artifact #75 v1) has no such badge and gives each tile a descriptive
 * subtitle instead.
 *
 * The fix removed those three sections from nav-config while they had no page;
 * each returned when its route landed (#4566 Lists, #4569 Documents, #5408
 * Guides). So this spec guards both halves of the rule:
 *   - no placeholder copy renders in any nav surface, in any state;
 *   - every nav entry is a real link (nothing is drawn as a disabled "not yet"
 *     row, which is the other way a shell says "unfinished").
 *
 * "Soon" is checked as a word that STARTS an element's own text (the pill read
 * exactly that), not as a bare substring: "Duke Energy due soon" is real bill
 * copy on Today and must keep working. "Coming soon" is checked as a phrase
 * anywhere, because nothing legitimate says it.
 *
 * Phone widths run in desktop-chrome and on iphone-390 (WebKit, touch);
 * 820/1440 only in desktop-chrome (see onlyOn in fixtures.ts).
 */

async function gotoHome(page: Page) {
  await page.goto("/dashboard");
  await expect(page.getByRole("heading", { name: /^(Good morning|Good afternoon|Good evening|Today)/, level: 2 })).toBeVisible();
}

/** Elements whose own text starts with "soon", plus any "coming soon" text anywhere. */
async function placeholderCopy(page: Page) {
  return page.evaluate(() => {
    const offenders: string[] = [];
    for (const el of Array.from(document.body.querySelectorAll<HTMLElement>("*"))) {
      if (el.tagName === "SCRIPT" || el.tagName === "STYLE") continue;
      const own = Array.from(el.childNodes)
        .filter((n) => n.nodeType === Node.TEXT_NODE)
        .map((n) => (n.textContent ?? "").trim())
        .filter(Boolean)
        .join(" ");
      if (/^soon\b/i.test(own)) {
        const cls = typeof el.className === "string" ? el.className.split(/\s+/)[0] : "";
        offenders.push(`${el.tagName.toLowerCase()}${cls ? `.${cls}` : ""}: "${own}"`);
      }
    }
    const phrase = /.{0,40}coming soon.{0,40}/gi;
    const text = document.body.innerText;
    let m: RegExpExecArray | null;
    while ((m = phrase.exec(text)) !== null) offenders.push(`text: "${m[0].trim()}"`);
    return offenders;
  });
}

test.describe("phone 390px", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test.beforeEach(() => onlyOn("phone"));

  test("tab bar, More sheet and drawer carry no 'Soon' / 'Coming soon' copy", async ({ loggedInPage: page }) => {
    await gotoHome(page);
    expect(await placeholderCopy(page), "placeholder copy on the default shell").toEqual([]);

    // Tab bar: every tab is a link, and the three tab sections are there (the
    // four phone tabs are fixed by the approved shell; More is the fifth).
    const bar = page.getByTestId("tab-bar");
    await expect(bar.getByRole("link")).toHaveCount(3);
    await expect(bar.getByRole("link", { name: "Today" })).toHaveAttribute("aria-current", "page");
    await expect(bar.getByRole("link", { name: "Calendar" })).toBeVisible();
    await expect(bar.getByRole("link", { name: "Meals" })).toBeVisible();
    await expect(bar.getByRole("button", { name: "More" })).toBeVisible();
    await expect(bar.locator('[aria-disabled="true"]')).toHaveCount(0);

    // More sheet: the Household tiles are real links with descriptive subtitles.
    await bar.getByRole("button", { name: "More" }).click();
    const sheet = page.getByRole("dialog", { name: "More" });
    await expect(sheet).toBeVisible();
    expect(await placeholderCopy(page), "placeholder copy with the More sheet open").toEqual([]);
    await expect(sheet.getByRole("link", { name: /Documents/ })).toBeVisible();
    await expect(sheet.getByRole("link", { name: /People & House/ })).toBeVisible();
    await expect(sheet.getByRole("link", { name: /Money/ })).toBeVisible();
    // #5408: Guides was in no nav surface at all; the tile is the fix.
    await expect(sheet.getByRole("link", { name: /Guides/ })).toBeVisible();
    await expect(sheet.getByText("Family, home, cars, pets, vendors")).toBeVisible();
    await expect(sheet.locator('[aria-disabled="true"]')).toHaveCount(0);
    await page.keyboard.press("Escape");
    await expect(sheet).toBeHidden();

    // Drawer: the same nav, as links, with no pills.
    await page.locator("header").getByRole("button", { name: "Open menu" }).click();
    const drawer = page.getByRole("dialog", { name: /Home OS menu/ });
    await expect(drawer).toBeVisible();
    expect(await placeholderCopy(page), "placeholder copy with the drawer open").toEqual([]);
    await expect(drawer.locator("ul li > a")).toHaveCount(SECTION_ROW_COUNT);
    await expect(drawer.locator("[aria-disabled]")).toHaveCount(0);
  });
});

for (const width of [820, 1440]) {
  test.describe(`${width}px`, () => {
    test.use({ viewport: { width, height: 1024 } });
    test.beforeEach(() => onlyOn("desktop"));

    test("rail/sidebar carries no 'Soon' / 'Coming soon' copy and every row links", async ({ loggedInPage: page }) => {
      await gotoHome(page);
      const nav = page.getByRole("navigation", { name: "Main navigation" });
      await expect(nav).toBeVisible();
      expect(await placeholderCopy(page), `placeholder copy at ${width}px`).toEqual([]);
      // The section rows only (the sidebar also links its family card).
      // Today, Calendar, Meals, Lists | Documents, People & House, Money,
      // Guides | Settings.
      const rows = nav.locator("ul li > a, ul li > span[aria-disabled=\"true\"]");
      await expect(rows).toHaveCount(SECTION_ROW_COUNT);
      await expect(nav.locator("[aria-disabled]")).toHaveCount(0);
      await expect(nav.getByRole("link", { name: "Meals" })).toBeVisible();
      // Every section has a page (#5408 completed the set: Guides was the last
      // one missing, and it is now a real row rather than an absence).
      for (const name of ["Lists", "Documents", "Guides"]) {
        await expect(nav.getByRole("link", { name, exact: true })).toBeVisible();
      }
    });
  });
}
