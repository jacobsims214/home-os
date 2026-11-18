import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * Properties list (#4553) — mobile-first per the People & House mockup
 * (artifact #89, House > Properties) on the EntityList pattern.
 *
 * Phone (< 768): stacked cards, each one tappable link, 44 px+ targets, no
 * horizontal scroll. ≥ 768: a table in a horizontal scroller.
 * Add opens the create modal (there is no /new route).
 * The monthly total is the regression for audit #4509: mortgage_rate is a
 * percent, so a $200k / 4.25% / 30y loan is ~$984/mo, not ~$98,388.
 */

function property(overrides: Record<string, unknown>) {
  return {
    id: "p1",
    household_id: "h1",
    name: "Maple Street",
    address: "1824 Maple St, Springfield",
    property_type: "single_family",
    notes: null,
    purchase_price: null,
    purchase_date: null,
    current_value: "512000.00",
    down_payment: null,
    mortgage_amount: null,
    mortgage_rate: null,
    mortgage_term_months: null,
    mortgage_start_date: null,
    mortgage_lender: null,
    mortgage_account_number: null,
    property_tax_annual: null,
    property_tax_due_months: null,
    insurance_annual: null,
    insurance_provider: null,
    hoa_fee_monthly: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

// Carrying costs: 983.88 + 150 + 100 + 50 = 1283.88 for Maple Street,
// and 100 + 28 = 128 for the paid-off cabin. Total $1,412/mo.
const PROPERTIES = [
  property({
    id: "p1",
    name: "Maple Street",
    address: "1824 Maple St, Springfield",
    mortgage_amount: "200000.00",
    mortgage_rate: "4.250",
    mortgage_term_months: "360",
    property_tax_annual: "1800.00",
    insurance_annual: "1200.00",
    hoa_fee_monthly: "50",
  }),
  property({
    id: "p2",
    name: "Lake cabin",
    address: "77 Shoreline Rd, Lake Geneva",
    property_type: "condo",
    current_value: "310000.00",
    property_tax_annual: "1200.00",
    insurance_annual: "336.00",
  }),
];

async function stubProperties(page: Page, opts: { fail?: boolean } = {}): Promise<void> {
  await page.route("**/bff/properties", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    if (opts.fail) return route.fulfill({ status: 500, json: { error: "boom" } });
    return route.fulfill({ json: { data: PROPERTIES } });
  });
}

async function gotoProperties(page: Page) {
  await page.goto("/dashboard/properties");
  await expect(page.locator("main").getByRole("heading", { level: 2, name: /Properties/ })).toBeVisible();
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = false) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme}`, () => {
    test.use({ colorScheme });

    test("phone 390: cards, no clipping, plausible monthly total", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubProperties(page);
      await gotoProperties(page);
      const main = page.locator("main");

      const cards = main.locator("[data-property-card]");
      await expect(cards).toHaveCount(PROPERTIES.length);
      await expect(main.locator("[data-property-table]")).toBeHidden();

      for (let i = 0; i < PROPERTIES.length; i++) {
        const box = (await cards.nth(i).boundingBox())!;
        expect(box.height).toBeGreaterThanOrEqual(43.5); // 44 px target, sub-pixel allowed
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width).toBeLessThanOrEqual(390);
      }

      await expect(cards.first()).toContainText("Maple Street");
      await expect(cards.first()).toContainText("1824 Maple St");
      await expect(cards.first()).toHaveAttribute("href", "/dashboard/properties/p1");
      // The mortgage monthly figure is a real payment, not the 100x bug.
      await expect(cards.first()).toContainText("$1,284/mo");
      await expect(cards.nth(1)).toContainText("$128/mo");

      // Audit #4509: the list total is ~$1,412, never ~$86,606.
      const totalCard = main.getByText("Monthly total").locator("..");
      await expect(totalCard).toContainText("$1,412");
      await expect(main).not.toContainText("86,606");
      await expect(main).not.toContainText("98,388");

      // Card text uses the theme tokens, so dark mode stays readable.
      const colours = await cards.first().evaluate((card) => {
        const probe = (v: string) => {
          const el = document.createElement("span");
          el.style.color = `var(${v})`;
          document.body.appendChild(el);
          const c = getComputedStyle(el).color;
          el.remove();
          return c;
        };
        return { heading: getComputedStyle(card.querySelector("h3")!).color, ink: probe("--ink") };
      });
      expect(colours.heading).toBe(colours.ink);

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `properties-390-${colorScheme}`, true);

      // Add opens a dialog; it must not navigate to a /new route.
      const add = main.getByRole("button", { name: "Add property" });
      const addBox = (await add.boundingBox())!;
      expect(addBox.height).toBeGreaterThanOrEqual(43.5);
      await add.click();
      await expect(page.getByRole("dialog")).toBeVisible();
      expect(page.url()).not.toContain("/new");
      await snap(page, testInfo, `properties-390-${colorScheme}-add`);
    });

    test("phone 390: empty state offers Add, not a dead end", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await page.route("**/bff/properties", (route) =>
        route.request().method() === "GET" ? route.fulfill({ json: { data: [] } }) : route.fallback(),
      );
      await gotoProperties(page);
      const main = page.locator("main");
      await expect(main.getByText("No properties yet")).toBeVisible();
      // Header and empty state both offer Add; either one opens the modal.
      await expect(main.getByRole("button", { name: "Add property" })).toHaveCount(2);
      await main.getByRole("button", { name: "Add property" }).last().click();
      await expect(page.getByRole("dialog")).toBeVisible();
      expect(page.url()).not.toContain("/new");
      await expect(main.getByText("Monthly total")).toBeHidden();
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `properties-390-${colorScheme}-empty`, true);
    });

    test("desktop 1440: table with every column, rows open the record", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("desktop");
      await page.setViewportSize({ width: 1440, height: 900 });
      await stubProperties(page);
      await gotoProperties(page);
      const main = page.locator("main");

      const table = main.locator("[data-property-table]");
      await expect(table).toBeVisible();
      await expect(main.locator("[data-property-card]").first()).toBeHidden();
      for (const heading of ["Property", "Address", "Type", "Value", "Monthly"]) {
        await expect(table.getByRole("columnheader", { name: heading })).toBeVisible();
      }
      await expect(table.getByRole("link", { name: "Maple Street" })).toHaveAttribute(
        "href",
        "/dashboard/properties/p1",
      );
      await expect(table).toContainText("Single family");
      await expect(table).toContainText("$512,000");
      await expect(table).toContainText("$1,284");
      await expect(main.getByText("Monthly total").locator("..")).toContainText("$1,412");
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `properties-1440-${colorScheme}`);
    });
  });
}

test("error state offers Try again and no total", async ({ loggedInPage: page }) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  await stubProperties(page, { fail: true });
  await page.goto("/dashboard/properties");
  const main = page.locator("main");
  await expect(main.getByRole("alert")).toContainText("Couldn't load properties");
  await expect(main.getByRole("button", { name: "Try again" })).toBeVisible();
  await expect(main.getByText("Monthly total")).toBeHidden();
});
