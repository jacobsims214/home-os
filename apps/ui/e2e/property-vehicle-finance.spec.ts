import type { Page, Route, TestInfo } from "@playwright/test";
import { test, expect, onlyOn } from "./fixtures";

/**
 * Property/vehicle finance (#4706). Since #4704 the mortgage, tax, insurance
 * and HOA (property) and the loan, registration and insurance (vehicle) are
 * loans and bills with property_id / vehicle_id. The API still returns the
 * old fields, derived from those rows, but the UI must never send them
 * (#4707 drops the columns). The pages list the linked loans and bills, link
 * to each, and add new ones with the FK already set.
 *
 * Viewports are pinned per test: desktop-chrome, and the 390 tests on
 * iphone-390 too (WebKit, touch; #4757).
 */

type Body = Record<string, unknown>;

const LEGACY_PROPERTY = [
  "mortgage_amount", "mortgage_rate", "mortgage_term_months", "mortgage_start_date", "mortgage_lender",
  "mortgage_account_number", "property_tax_annual", "property_tax_due_months", "insurance_annual",
  "insurance_provider", "hoa_fee_monthly",
];
const LEGACY_VEHICLE = [
  "lender", "loan_amount", "loan_term_months", "monthly_payment", "registration_cost",
  "registration_renewal_month", "insurance_cost", "insurance_provider",
];

// What the #4704 API returns: the legacy fields filled from the linked rows.
const PROPERTY = {
  id: "p9", household_id: "h1", name: "Lake Cabin", address: "12 Shore Rd", property_type: "Single Family", notes: null,
  purchase_price: "250000.00", purchase_date: "2019-06-01", current_value: "320000.00", down_payment: "50000.00",
  mortgage_amount: "200000.00", mortgage_rate: "4.250", mortgage_term_months: "360", mortgage_start_date: "2019-06-01",
  mortgage_lender: "Wells Fargo", mortgage_account_number: null, property_tax_annual: "1800.00", property_tax_due_months: "12",
  insurance_annual: null, insurance_provider: null, hoa_fee_monthly: null,
};
const VEHICLE = {
  id: "v9", household_id: "h1", year: 2021, make: "Ford", model: "F-150", vin: "1FTFW1E50MFA00000", license_plate: "ABC123",
  color: "Blue", notes: null, purchase_price: "42000.00", current_value: "35000.00", purchase_date: "2021-04-10",
  lender: "Ford Credit", loan_amount: "12000.00", loan_term_months: 60, monthly_payment: "230.00",
  registration_cost: "120.00", registration_renewal_month: 3, insurance_cost: "900.00", insurance_provider: "Progressive",
};
const LOANS = [
  { id: "l9", name: "Lake Cabin mortgage", property_id: "p9", lender: "Wells Fargo", original_amount: "200000.00", remaining_balance: "171000.00", interest_rate: "4.250", term_months: 360, monthly_payment: "983.88" },
  { id: "l8", name: "F-150 loan", vehicle_id: "v9", lender: "Ford Credit", original_amount: "12000.00", remaining_balance: "7400.00", term_months: 60, monthly_payment: "230.00" },
  // An asset's loan carries its property_id too, but is not the property's.
  { id: "l7", name: "Dock lift financing", property_id: "p9", asset_id: "a1", original_amount: "6000.00" },
];
const BILLS = [
  { id: "b9", name: "Lake Cabin property tax", property_id: "p9", amount: "1800.00", frequency: "annual", category: "Property Tax", due_date: "2026-12-15" },
  { id: "b8", name: "2021 Ford F-150 insurance", vehicle_id: "v9", amount: "900.00", frequency: "annual", category: "Insurance", due_date: "2027-01-01" },
  { id: "b7", name: "2021 Ford F-150 registration", vehicle_id: "v9", amount: "120.00", frequency: "annual", category: "Vehicle", due_date: "2027-03-01" },
  { id: "b6", name: "Main house electric", property_id: "p1", amount: "180.00", frequency: "monthly" },
  { id: "b5", name: "Dock lift service plan", property_id: "p9", asset_id: "a1", amount: "20.00", frequency: "monthly" },
];

async function stubFinance(page: Page) {
  const writes = { propertyPuts: [] as Body[], propertyPosts: [] as Body[], vehiclePuts: [] as Body[], loanPosts: [] as Body[], billPosts: [] as Body[], loanQueries: [] as string[] };
  let property: Body = { ...PROPERTY };
  let vehicle: Body = { ...VEHICLE };
  const bills = [...BILLS];

  await page.route("**/bff/properties/p9", async (route: Route) => {
    if (route.request().method() === "PUT") {
      const body = route.request().postDataJSON() as Body;
      writes.propertyPuts.push(body);
      property = { ...property, ...body };
    }
    return route.fulfill({ json: { data: property } });
  });
  await page.route(/\/bff\/properties(\?.*)?$/, async (route: Route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON() as Body;
      writes.propertyPosts.push(body);
      return route.fulfill({ status: 201, json: { data: { id: "p10", ...body } } });
    }
    return route.fulfill({ json: { data: [property] } });
  });
  await page.route("**/bff/vehicles/v9", async (route: Route) => {
    if (route.request().method() === "PUT") {
      const body = route.request().postDataJSON() as Body;
      writes.vehiclePuts.push(body);
      vehicle = { ...vehicle, ...body };
    }
    return route.fulfill({ json: { data: vehicle } });
  });
  await page.route(/\/bff\/loans(\?.*)?$/, async (route: Route) => {
    const req = route.request();
    if (req.method() === "POST") {
      writes.loanPosts.push(req.postDataJSON() as Body);
      return route.fulfill({ status: 201, json: { data: { id: "l99", ...(req.postDataJSON() as Body) } } });
    }
    const q = new URL(req.url()).searchParams;
    writes.loanQueries.push(q.toString());
    // Like the API's ?property_id= / ?vehicle_id= filter (asset rows included, as the API does).
    const rows = LOANS.filter((l) => (q.get("property_id") ? l.property_id === q.get("property_id") : q.get("vehicle_id") ? l.vehicle_id === q.get("vehicle_id") : true));
    return route.fulfill({ json: { data: rows } });
  });
  await page.route(/\/bff\/bills(\?.*)?$/, async (route: Route) => {
    const req = route.request();
    if (req.method() === "POST") {
      const body = req.postDataJSON() as Body;
      writes.billPosts.push(body);
      const created = { id: "b99", ...body } as (typeof BILLS)[number];
      bills.push(created);
      return route.fulfill({ status: 201, json: { data: created } });
    }
    return route.fulfill({ json: { data: bills } });
  });
  return writes;
}

function expectNoLegacy(body: Body, legacy: string[]) {
  for (const f of legacy) expect(body, `request body must not carry ${f}`).not.toHaveProperty(f);
}

async function snap(page: Page, testInfo: TestInfo, name: string) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage: true });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

const noHorizontalScroll = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);

test("property: linked loans and bills are listed; editing sends no legacy finance field", async ({ loggedInPage: page }, testInfo) => {
  onlyOn("desktop");
  const writes = await stubFinance(page);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("/dashboard/properties/p9");
  const main = page.locator("main");
  const card = main.locator('[data-linked-finances="property"]');
  await expect(card.getByRole("link", { name: /Lake Cabin mortgage/ })).toHaveAttribute("href", "/dashboard/loans/l9");
  await expect(card.getByRole("link", { name: /Lake Cabin property tax/ })).toHaveAttribute("href", "/dashboard/bills/b9");
  // Asset rows and other properties' bills are not this property's.
  await expect(card).not.toContainText("Dock lift");
  await expect(card).not.toContainText("Main house electric");
  expect(writes.loanQueries).toContain("property_id=p9");
  // The read-only derived values still show, with a link to the loan.
  await expect(main.getByText("Wells Fargo").first()).toBeVisible();
  await expect(main.getByRole("link", { name: "View loan" })).toHaveAttribute("href", "/dashboard/loans/l9");
  await snap(page, testInfo, "property-1280");

  await main.getByRole("button", { name: "Edit" }).click();
  await main.getByText("Financial Details").first().click();
  for (const label of ["Mortgage Amount", "Mortgage Rate (%)", "Property Tax (annual)", "Insurance (annual)", "Insurance Provider", "HOA Fee (monthly)"]) {
    await expect(main.getByText(label, { exact: true })).toHaveCount(0);
  }
  const nameInput = main.getByPlaceholder("Main Residence");
  await nameInput.fill("Lake Cabin North");
  await main.getByRole("button", { name: "Save Changes" }).click();

  await expect.poll(() => writes.propertyPuts.length).toBe(1);
  const body = writes.propertyPuts[0];
  expectNoLegacy(body, LEGACY_PROPERTY);
  expect(body).toMatchObject({ name: "Lake Cabin North", purchase_price: "250000.00", current_value: "320000.00" });
  expect(writes.loanPosts).toHaveLength(0);
  expect(writes.billPosts).toHaveLength(0);
});

test("vehicle: linked loan and bills are listed; editing sends no legacy finance field", async ({ loggedInPage: page }, testInfo) => {
  onlyOn("desktop");
  const writes = await stubFinance(page);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("/dashboard/vehicles/v9");
  const main = page.locator("main");
  const block = main.locator('[data-linked-finances="vehicle"]');
  await expect(block.getByRole("link", { name: /F-150 loan/ })).toHaveAttribute("href", "/dashboard/loans/l8");
  await expect(block.getByRole("link", { name: /F-150 insurance/ })).toHaveAttribute("href", "/dashboard/bills/b8");
  await expect(block.getByRole("link", { name: /F-150 registration/ })).toHaveAttribute("href", "/dashboard/bills/b7");
  expect(writes.loanQueries).toContain("vehicle_id=v9");
  // No legacy inputs or rows on the sheet.
  for (const f of LEGACY_VEHICLE) await expect(main.locator(`[data-field="${f}"]`)).toHaveCount(0);
  await snap(page, testInfo, "vehicle-1280");

  await main.getByRole("button", { name: "Edit" }).click();
  await expect(main.getByRole("textbox", { name: "Lender" })).toHaveCount(0);
  await expect(main.getByRole("textbox", { name: "Insurance Provider" })).toHaveCount(0);
  const color = main.getByRole("textbox", { name: "Color" });
  await color.fill("Red");
  await main.getByRole("button", { name: "Save" }).click();

  await expect.poll(() => writes.vehiclePuts.length).toBe(1);
  const body = writes.vehiclePuts[0];
  expectNoLegacy(body, LEGACY_VEHICLE);
  expect(body).not.toHaveProperty("linked_finances");
  expect(body).toMatchObject({ color: "Red", make: "Ford", model: "F-150" });
});

test("390: Add bill from a property creates it with property_id and stays on the page", async ({ loggedInPage: page }, testInfo) => {
  onlyOn("phone");
  const writes = await stubFinance(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/dashboard/properties/p9");
  const card = page.locator('main [data-linked-finances="property"]');
  await card.getByRole("button", { name: "Add bill" }).click();
  const dialog = page.getByRole("dialog", { name: "Add bill" });
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("For Lake Cabin");
  await dialog.getByRole("button", { name: "Insurance" }).click();
  await expect(dialog.getByRole("textbox", { name: "Name" })).toHaveValue("Lake Cabin insurance");
  await dialog.getByRole("textbox", { name: "Amount" }).fill("$1,450");
  await dialog.getByLabel("Next due date").fill("2027-05-01");
  expect(await noHorizontalScroll(page)).toBe(true);
  await snap(page, testInfo, "add-bill-390");
  await dialog.getByRole("button", { name: "Add bill" }).click();
  await expect(dialog).toBeHidden();

  expect(writes.billPosts).toEqual([
    { name: "Lake Cabin insurance", frequency: "annual", property_id: "p9", amount: "1450", due_date: "2027-05-01", category: "Insurance" },
  ]);
  await expect(page).toHaveURL(/\/dashboard\/properties\/p9$/);
  await expect(card.getByRole("link", { name: /Lake Cabin insurance/ })).toBeVisible();
  expect(writes.propertyPuts).toHaveLength(0);
});

test("390: Add loan from a vehicle creates it with vehicle_id", async ({ loggedInPage: page }) => {
  onlyOn("phone");
  const writes = await stubFinance(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/dashboard/vehicles/v9");
  const block = page.locator('main [data-linked-finances="vehicle"]');
  await block.getByRole("button", { name: "Add loan" }).click();
  const dialog = page.getByRole("dialog", { name: "Add loan" });
  await expect(dialog.getByRole("textbox", { name: "Name" })).toHaveValue("2021 Ford F-150 loan");
  await dialog.getByRole("textbox", { name: "Lender" }).fill("Ford Credit");
  await dialog.getByRole("textbox", { name: "Amount borrowed" }).fill("12000");
  await dialog.getByRole("button", { name: "Add loan" }).click();
  await expect(dialog).toBeHidden();
  expect(writes.loanPosts).toEqual([
    { name: "2021 Ford F-150 loan", vehicle_id: "v9", lender: "Ford Credit", original_amount: "12000", remaining_balance: "12000" },
  ]);
  expect(writes.vehiclePuts).toHaveLength(0);
});

test("Add property sends no legacy finance field", async ({ loggedInPage: page }) => {
  onlyOn("desktop");
  const writes = await stubFinance(page);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("/dashboard/properties");
  await page.getByRole("button", { name: "Add Property" }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Property Name").fill("Beach House");
  await dialog.getByText("Financial Details").click();
  await expect(dialog.getByLabel("Mortgage Amount")).toHaveCount(0);
  await dialog.getByLabel("Purchase Price").fill("500000");
  await dialog.getByRole("button", { name: "Add Property" }).click();
  await expect.poll(() => writes.propertyPosts.length).toBe(1);
  expectNoLegacy(writes.propertyPosts[0], LEGACY_PROPERTY);
  expect(writes.propertyPosts[0]).toMatchObject({ name: "Beach House", purchase_price: "500000" });
});
