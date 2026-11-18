import type { Page, Route, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * Vehicles pages (#4555), mobile-first per the People & House mockup
 * (artifact #89, House > Vehicles) on the list-page and detail-page patterns
 * (KB architecture/entity-list-pattern.md, detail-page-layout.md).
 *
 * Phone (< 768): stacked cards, one tappable link each, 44 px+ targets, no
 * horizontal scroll; the detail page is a 2x2 summary under a sticky title bar.
 * >= 768: the list is a table in a horizontal scroller and the summary is 4-up.
 *
 * A vehicle has no `name` column, so every screen labels it year + make +
 * model ("2021 Ford F-150"): that is the regression this spec holds down,
 * together with the fact that the list never shows the property filter (it
 * emptied vehicles, the #4530 note on #4555) and that the detail page sends
 * none of the legacy finance fields (#4706) — those live in the linked loan
 * and bills.
 */

type Body = Record<string, unknown>;

const VEHICLES = [
  {
    id: "v1", household_id: "h1", year: 2021, make: "Ford", model: "F-150",
    vin: "1FTFW1E50MFA00000", license_plate: "6XRT902", color: "Agate Black", notes: null,
    purchase_price: "55000.00", purchase_date: "2021-04-10", current_value: "42000.00",
    loan_amount: "12000.00", lender: "Ford Credit", loan_term_months: 60, monthly_payment: "230.00",
    registration_cost: "120.00", registration_renewal_month: 1, insurance_cost: "900.00",
    insurance_provider: "Progressive", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "v2", household_id: "h1", year: 2019, make: "Honda", model: "Odyssey",
    vin: null, license_plate: "8KJD214", color: "Silver", notes: null,
    purchase_price: null, purchase_date: null, current_value: "24000.00",
    loan_amount: null, lender: null, loan_term_months: null, monthly_payment: null,
    registration_cost: "120.00", registration_renewal_month: 12, insurance_cost: null,
    insurance_provider: null, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
  },
];

/** A vehicle-only plate: no name, no colour, no registration month. */
const SPARE = {
  id: "v3", household_id: "h1", year: null, make: null, model: null,
  vin: null, license_plate: "SPARE01", color: null, notes: null,
  purchase_price: null, purchase_date: null, current_value: null,
  registration_renewal_month: null, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
};

const TASKS = [
  { id: "t1", household_id: "h1", property_id: null, asset_id: null, vehicle_id: "v1",
    name: "Oil change and tire rotation", status: "pending", due_date: "2026-12-03",
    created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" },
  // Another subject's task: it must never appear on the vehicle.
  { id: "t2", household_id: "h1", property_id: "p1", asset_id: null, vehicle_id: null,
    name: "Replace furnace filter", status: "pending", due_date: "2026-11-20",
    created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" },
];

const LOANS = [
  { id: "l8", name: "2021 Ford F-150 loan", vehicle_id: "v1", lender: "Ford Credit",
    original_amount: "12000.00", remaining_balance: "7400.00", term_months: 60, monthly_payment: "230.00" },
];

const BILLS = [
  { id: "b8", name: "2021 Ford F-150 insurance", vehicle_id: "v1", amount: "900.00",
    frequency: "annual", category: "Insurance", due_date: "2027-01-01" },
];

const LEGACY_VEHICLE = [
  "lender", "loan_amount", "loan_term_months", "monthly_payment",
  "registration_cost", "registration_renewal_month", "insurance_cost", "insurance_provider",
];

function expectNoLegacy(body: Body) {
  for (const f of LEGACY_VEHICLE) expect(body, `request body must not carry ${f}`).not.toHaveProperty(f);
}

async function stubVehicles(
  page: Page,
  opts: { fail?: boolean; rows?: unknown[]; detail?: Body } = {},
): Promise<{ puts: Body[]; posts: Body[] }> {
  const writes = { puts: [] as Body[], posts: [] as Body[] };
  const detail = opts.detail ?? { ...VEHICLES[0] };

  await page.route(/\/bff\/vehicles(\?.*)?$/, async (route: Route) => {
    const req = route.request();
    if (req.method() === "POST") {
      const body = req.postDataJSON() as Body;
      writes.posts.push(body);
      return route.fulfill({ status: 201, json: { data: { id: "v9", ...body } } });
    }
    if (opts.fail) return route.fulfill({ status: 500, json: { error: "boom" } });
    return route.fulfill({ json: { data: opts.rows ?? VEHICLES } });
  });

  await page.route("**/bff/vehicles/v1", async (route: Route) => {
    if (route.request().method() === "PUT") {
      const body = route.request().postDataJSON() as Body;
      writes.puts.push(body);
      return route.fulfill({ json: { data: { ...detail, ...body } } });
    }
    return route.fulfill({ json: { data: detail } });
  });

  await page.route(/\/bff\/maintenance\/tasks(\?.*)?$/, (route) => route.fulfill({ json: { data: TASKS } }));
  await page.route(/\/bff\/loans(\?.*)?$/, (route) => route.fulfill({ json: { data: LOANS } }));
  await page.route(/\/bff\/bills(\?.*)?$/, (route) => route.fulfill({ json: { data: BILLS } }));
  return writes;
}

async function gotoVehicles(page: Page) {
  await page.goto("/dashboard/vehicles");
  await expect(page.locator("main").getByRole("heading", { level: 2, name: /Vehicles/ })).toBeVisible();
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = false) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme}`, () => {
    test.use({ colorScheme });

    test("phone 390: cards named by year make model, 44 px targets, no property filter", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubVehicles(page);
      await gotoVehicles(page);
      const main = page.locator("main");

      const cards = main.locator("[data-vehicle-card]");
      await expect(cards).toHaveCount(VEHICLES.length);
      await expect(main.locator("[data-vehicle-table]")).toBeHidden();
      // #4530's property filter emptied this list; it must not be here at all.
      await expect(main.getByText("Filter by property")).toHaveCount(0);

      for (let i = 0; i < VEHICLES.length; i++) {
        const box = (await cards.nth(i).boundingBox())!;
        expect(box.height).toBeGreaterThanOrEqual(43.5);
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width).toBeLessThanOrEqual(390);
      }

      // A vehicle has no name column: the heading is its year, make and model.
      await expect(cards.first()).toContainText("2021 Ford F-150");
      await expect(cards.first()).toContainText("6XRT902 · Agate Black");
      await expect(cards.first()).toHaveAttribute("href", "/dashboard/vehicles/v1");
      await expect(cards.first()).toContainText("Renews in January");
      await expect(cards.nth(1)).toContainText("2019 Honda Odyssey");
      await expect(cards.nth(1)).toContainText("Renews in December");

      // Theme tokens, so the heading reads in dark mode too.
      const colour = await cards.first().evaluate((card) => {
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
      expect(colour.heading).toBe(colour.ink);

      const add = main.getByRole("button", { name: "Add vehicle" });
      expect((await add.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `vehicles-390-${colorScheme}`, true);
    });

    test("phone 390: a car with no name still gets a heading, and Add opens the sheet", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      const writes = await stubVehicles(page, { rows: [SPARE] });
      await gotoVehicles(page);
      const main = page.locator("main");

      // No year/make/model: the plate names it, never an empty card.
      await expect(main.locator("[data-vehicle-card]")).toContainText("SPARE01");
      await expect(main.locator("[data-vehicle-card]")).not.toContainText("Renews in");
      await expectNoHorizontalOverflow(page);

      await main.getByRole("button", { name: "Add vehicle" }).click();
      const dialog = page.getByRole("dialog");
      await expect(dialog).toBeVisible();
      expect(page.url()).not.toContain("/new");
      await dialog.getByRole("textbox", { name: "Year" }).fill("2018");
      await dialog.getByRole("textbox", { name: "Make" }).fill("Toyota");
      await dialog.getByRole("textbox", { name: "Model" }).fill("Sienna");
      await dialog.getByRole("textbox", { name: "License plate" }).fill("TOY123");
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `vehicles-390-${colorScheme}-add`);
      await dialog.getByRole("button", { name: "Add vehicle" }).click();

      await expect.poll(() => writes.posts.length).toBe(1);
      // year is *int in the API: a string here would be a 400.
      expect(writes.posts[0]).toEqual({ year: 2018, make: "Toyota", model: "Sienna", license_plate: "TOY123" });
    });

    test("phone 390: detail is a 2x2 summary under the sticky title bar", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubVehicles(page);
      await page.goto("/dashboard/vehicles/v1");
      const main = page.locator("main");

      await expect(main.getByRole("heading", { level: 2, name: "2021 Ford F-150" })).toBeVisible();
      const summary = main.locator("[data-vehicle-summary]");
      await expect(summary).toBeVisible();
      await expect(summary).toContainText("6XRT902");
      await expect(summary).toContainText("Agate Black");
      await expect(summary).toContainText("Renews in January");
      await expect(summary).toContainText("1 open");

      // 2x2: the four facts sit on two rows of two, all inside the viewport.
      const facts = summary.locator("div");
      await expect(facts).toHaveCount(4);
      const boxes = await facts.evaluateAll((els) =>
        els.map((el) => {
          const r = el.getBoundingClientRect();
          return { x: Math.round(r.x), y: Math.round(r.y), right: Math.round(r.right) };
        }),
      );
      expect(new Set(boxes.map((b) => b.y)).size).toBe(2);
      for (const b of boxes) {
        expect(b.x).toBeGreaterThanOrEqual(0);
        expect(b.right).toBeLessThanOrEqual(390);
      }

      // The fields a phone reads, and the plate in the summary but not as a row twice.
      await expect(main.locator('[data-field="make"]')).toContainText("Ford");
      await expect(main.locator('[data-field="vin"]')).toContainText("1FTFW1E50MFA00000");

      // Upkeep is filtered to this vehicle: the property's task is not here.
      const upkeep = main.locator("[data-vehicle-upkeep]");
      await expect(upkeep).toContainText("Oil change and tire rotation");
      await expect(upkeep).not.toContainText("Replace furnace filter");

      // The linked finances block is the page's money, not the legacy fields.
      const finance = main.locator('[data-linked-finances="vehicle"]');
      await expect(finance.getByRole("link", { name: /F-150 loan/ })).toHaveAttribute("href", "/dashboard/loans/l8");
      await expect(finance.getByRole("link", { name: /F-150 insurance/ })).toHaveAttribute("href", "/dashboard/bills/b8");
      for (const f of LEGACY_VEHICLE) await expect(main.locator(`[data-field="${f}"]`)).toHaveCount(0);

      // The phone back target is 44 px and goes to the list.
      const back = main.getByRole("link", { name: "Back to Vehicles" });
      expect((await back.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);
      await expect(back).toHaveAttribute("href", "/dashboard/vehicles");

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `vehicle-detail-390-${colorScheme}`, true);
    });

    test("desktop 1440: table with every column, rows open the record", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("desktop");
      await page.setViewportSize({ width: 1440, height: 900 });
      await stubVehicles(page);
      await gotoVehicles(page);
      const main = page.locator("main");

      const table = main.locator("[data-vehicle-table]");
      await expect(table).toBeVisible();
      await expect(main.locator("[data-vehicle-card]").first()).toBeHidden();
      for (const heading of ["Vehicle", "Plate", "Colour", "Registration"]) {
        await expect(table.getByRole("columnheader", { name: heading })).toBeVisible();
      }
      await expect(table.getByRole("link", { name: "2021 Ford F-150" })).toHaveAttribute("href", "/dashboard/vehicles/v1");
      await expect(table).toContainText("6XRT902");
      await expect(table).toContainText("Renews in December");
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `vehicles-1440-${colorScheme}`);
    });

    test("desktop 1440: detail summary is 4-up and editing sends no legacy field", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("desktop");
      await page.setViewportSize({ width: 1440, height: 900 });
      const writes = await stubVehicles(page);
      await page.goto("/dashboard/vehicles/v1");
      const main = page.locator("main");

      const boxes = await main.locator("[data-vehicle-summary] div").evaluateAll((els) =>
        els.map((el) => Math.round(el.getBoundingClientRect().y)),
      );
      expect(new Set(boxes).size).toBe(1); // one row of four
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `vehicle-detail-1440-${colorScheme}`);

      await main.getByRole("button", { name: "Edit" }).click();
      // The finance inputs are gone, not merely hidden: they are the loan and bills.
      for (const label of ["Lender", "Loan Amount", "Monthly Payment", "Insurance Provider", "Registration Cost"]) {
        await expect(main.getByRole("textbox", { name: label })).toHaveCount(0);
      }
      await main.getByRole("textbox", { name: "Color" }).fill("Red");
      await main.getByRole("button", { name: "Save" }).click();

      await expect.poll(() => writes.puts.length).toBe(1);
      const body = writes.puts[0];
      expectNoLegacy(body);
      expect(body).toMatchObject({ color: "Red", make: "Ford", model: "F-150" });
      // year is a number in the PUT, as the Go *int field requires.
      expect(body.year).toBe(2021);
    });
  });
}

test("error state offers Try again and never reads as an empty list", async ({ loggedInPage: page }) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  await stubVehicles(page, { fail: true });
  await page.goto("/dashboard/vehicles");
  const main = page.locator("main");
  await expect(main.getByRole("alert")).toContainText("Couldn't load vehicles");
  await expect(main.getByText("No vehicles yet")).toHaveCount(0);
  await expect(main.getByRole("button", { name: "Try again" })).toBeVisible();
  await expectNoHorizontalOverflow(page);
});

test("empty state offers Add instead of a dead end", async ({ loggedInPage: page }) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  await stubVehicles(page, { rows: [] });
  await gotoVehicles(page);
  const main = page.locator("main");
  await expect(main.getByText("No vehicles yet")).toBeVisible();
  // Header and empty state both offer Add.
  await expect(main.getByRole("button", { name: "Add vehicle" })).toHaveCount(2);
  await main.getByRole("button", { name: "Add vehicle" }).last().click();
  await expect(page.getByRole("dialog")).toBeVisible();
  expect(page.url()).not.toContain("/new");
});
