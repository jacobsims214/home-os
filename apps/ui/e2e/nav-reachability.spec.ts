// The shared fixtures, not @playwright/test: `loggedInPage` is the
// stub-signed-in page every phone spec in this app starts from (#4510).
import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

/**
 * #5704 — every House page is on the phone, verified at 390 px.
 *
 * WHAT WAS WRONG. `properties`, `assets`, `vehicles`, `vendors`, `maintenance`
 * and the Money overview existed, worked, and were listed in `nav-config.ts` —
 * as `matches` entries inside People and Money. A `matches` entry only decides
 * which section HIGHLIGHTS a route. It draws no link, so the whole "House" half
 * of People & House was reachable only by typing the URL, on every breakpoint,
 * while looking perfectly configured. (#5408 shipped the same defect with
 * Guides, #5696 with Pets.)
 *
 * WHAT THIS PROVES, at 390 px, for each of them: tab bar → More (tap 1) → the
 * tile (tap 2) lands on the page, the tile is a ≥44 px target, and the sheet
 * carries no horizontal overflow.
 *
 * Revert-check: remove a section from nav-config.ts and the tile locator here
 * fails for that page — same as `nav-config.test.mjs`, which fails first and
 * cheaper (no browser).
 */

const HOUSEHOLD = { id: "h1", name: "Sims family", timezone: "America/New_York", role: "owner" };
const ME = { id: "u-owner", name: "Jacob Sims", email: "jacob@example.test" };

/**
 * One entry per section #5704 added or repointed: the tile's visible name, the
 * route it must land on, and the hint that has to appear on the tile.
 */
const HOUSE_PAGES = [
  { tile: "Properties", href: "/dashboard/properties", hint: "Rooms, mortgages, upkeep" },
  { tile: "Assets", href: "/dashboard/assets", hint: "Everything in the house" },
  { tile: "Vehicles", href: "/dashboard/vehicles", hint: "Cars, service, mileage" },
  { tile: "Vendors", href: "/dashboard/vendors", hint: "Plumber, cleaner, sitter" },
  { tile: "Maintenance", href: "/dashboard/maintenance", hint: "Upkeep and repairs" },
  { tile: "Money", href: "/dashboard/money", hint: "Bills and loans" },
];

async function stub(page: Page, role = "owner") {
  await page.route("**/bff/me/households", (route) => route.fulfill({ json: { data: [{ ...HOUSEHOLD, role }] } }));
  await page.route("**/bff/auth/me", (route) => route.fulfill({ json: ME }));
  // The House pages each read their list; every one of them must render, not
  // just return 200 — the assertion after each tap is on the page's own h1.
  for (const path of ["properties", "assets", "vehicles", "vendors", "maintenance"]) {
    await page.route(`**/bff/${path}**`, (route) => route.fulfill({ json: { data: [] } }));
  }
  await page.route("**/bff/budget**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/bills**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/loans**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/notifications**", (route) => route.fulfill({ json: { data: [], unread: 0 } }));
  await page.route("**/bff/lists**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/meals**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/events**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/people**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/pets**", (route) => route.fulfill({ json: { data: [] } }));
}

test.use({ viewport: { width: 390, height: 844 } });

test.describe("#5704 every House page is two taps from the phone", () => {
  for (const { tile, href, hint } of HOUSE_PAGES) {
    test(`${tile}: More → ${tile} lands on ${href}`, async ({ loggedInPage: page }) => {
      await stub(page);
      await page.goto("/dashboard");

      // Tap 1: More, in the tab bar.
      await page.getByTestId("tab-bar").getByRole("button", { name: "More" }).click();
      const sheet = page.getByRole("dialog", { name: "More" });
      await expect(sheet).toBeVisible();

      // Tap 2: the tile, which is a LINK — that is the whole point of #5704.
      // Before the fix there was no tile to tap and this locator found nothing.
      const link = sheet.getByRole("link", { name: new RegExp(tile) });
      await expect(link).toBeVisible();
      await expect(link).toContainText(hint);

      // ≥44 px target (the More sheet's tiles are 92 px; assert the floor).
      const box = (await link.boundingBox())!;
      expect(box.height, `${tile} tile height`).toBeGreaterThanOrEqual(44);
      expect(box.width, `${tile} tile width`).toBeGreaterThanOrEqual(44);

      // The sheet does not overflow a 390 px phone.
      await expect
        .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
        .toBeLessThanOrEqual(390);

      await link.click();
      await expect(page).toHaveURL(new RegExp(`${href}$`));
      // And the page is the page, not an error card: the shell's h1 names it.
      await expect(page.getByRole("heading", { level: 1 })).toContainText(tile);
    });
  }

  test("the House tiles are absent for a role the API refuses (#5704 keeps role rules)", async ({
    loggedInPage: page,
  }) => {
    // ResVehicles read is rolesReaders and ResMaintenance read is rolesFabric,
    // so a housesitter has neither grant. The UI hides; the API enforces.
    await stub(page, "housesitter");
    await page.goto("/dashboard");
    await page.getByTestId("tab-bar").getByRole("button", { name: "More" }).click();
    const sheet = page.getByRole("dialog", { name: "More" });
    await expect(sheet.getByRole("link", { name: /Properties/ })).toBeVisible();
    await expect(sheet.getByRole("link", { name: /Vehicles/ })).toHaveCount(0);
    await expect(sheet.getByRole("link", { name: /Maintenance/ })).toHaveCount(0);
  });

  test("a vendor keeps the house pages it may read and not the ones it may not (#5704)", async ({
    loggedInPage: page,
  }) => {
    await stub(page, "vendor");
    await page.goto("/dashboard");
    await page.getByTestId("tab-bar").getByRole("button", { name: "More" }).click();
    const sheet = page.getByRole("dialog", { name: "More" });
    for (const name of [/Properties/, /Assets/, /Maintenance/]) {
      await expect(sheet.getByRole("link", { name })).toBeVisible();
    }
    for (const name of [/Vehicles/, /Vendors/, /Pets/]) {
      await expect(sheet.getByRole("link", { name })).toHaveCount(0);
    }
  });
});
