import type { Page, Route, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * Property detail (#4578): property → rooms → things, mobile-first, per the
 * People & House mockup (artifact #89, House > Maple Street).
 *
 * The acceptance test is the phone one: a person on a 390 px phone adds a
 * room, then a thing in that room, and both show up without a reload — the
 * POSTs carry the right ids, and the page never scrolls sideways.
 *
 * The money check is the #4553/#4509 regression: a $200k / 4.25% / 30y
 * mortgage is ~$984/mo. The page used to carry its own copy of the math; it
 * now imports components/property/property-finance.ts, so this asserts the
 * one implementation the whole app shares.
 */

type Body = Record<string, unknown>;

const PROPERTY = {
  id: "p1",
  household_id: "h1",
  name: "Maple Street",
  address: "1824 Maple St, Springfield",
  property_type: "single_family",
  notes: "Side gate code 4412",
  purchase_price: "410000.00",
  purchase_date: "2016-06-01",
  current_value: "512000.00",
  down_payment: "82000.00",
  // Derived by the API from the linked loan and bills (#4704).
  mortgage_amount: "200000.00",
  mortgage_rate: "4.250",
  mortgage_term_months: "360",
  mortgage_start_date: "2016-06-01",
  mortgage_lender: "Wells Fargo",
  mortgage_account_number: null,
  property_tax_annual: "1800.00",
  property_tax_due_months: "12",
  insurance_annual: "1200.00",
  insurance_provider: "State Farm",
  hoa_fee_monthly: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const ROOMS = [
  { id: "r1", property_id: "p1", name: "Kitchen", floor: 1, notes: null, created_at: "2026-01-01T00:00:00Z" },
  { id: "r2", property_id: "p1", name: "Utility room", floor: 0, notes: null, created_at: "2026-01-01T00:00:00Z" },
];

const THINGS = [
  { id: "a1", household_id: "h1", property_id: "p1", room_id: "r2", name: "Furnace", category: "HVAC", manufacturer: "Carrier", model: "Infinity 59SC5", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" },
  { id: "a2", household_id: "h1", property_id: "p1", room_id: null, name: "Roof", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" },
];

// Overdue (due 2026-11-13) and not (due 2027-03-10). The stub clock is not
// pinned, so the overdue one is dated far enough back to be overdue whenever
// the suite runs.
const TASKS = [
  { id: "t1", household_id: "h1", schedule_id: null, property_id: "p1", asset_id: "a1", name: "Replace furnace filter", description: null, status: "pending", due_date: "2026-11-13", completed_at: null, cost: null, vendor_id: null, notes: null, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" },
  { id: "t2", household_id: "h1", schedule_id: null, property_id: "p1", asset_id: "a1", name: "Yearly tune-up", description: null, status: "pending", due_date: "2027-03-10", completed_at: null, cost: null, vendor_id: null, notes: null, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" },
  { id: "t3", household_id: "h1", schedule_id: null, property_id: "p1", asset_id: "a1", name: "Cleaned already", description: null, status: "done", due_date: "2026-01-05", completed_at: "2026-01-05T00:00:00Z", cost: null, vendor_id: null, notes: null, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" },
];

type Writes = { rooms: Body[]; things: Body[] };

async function stubProperty(page: Page): Promise<Writes> {
  const writes: Writes = { rooms: [], things: [] };
  type Row = Record<string, unknown>;
  const rooms: Row[] = [...ROOMS];
  const things: Row[] = [...THINGS];

  await page.route(/\/bff\/properties\/p1$/, (route: Route) =>
    route.fulfill({ json: { data: PROPERTY } }),
  );
  await page.route(/\/bff\/properties\/p1\/rooms$/, async (route: Route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON() as Body;
      writes.rooms.push(body);
      const created = {
        id: `r${rooms.length + 1}`,
        property_id: "p1",
        name: String(body.name ?? ""),
        floor: body.floor ?? null,
        notes: null,
        created_at: "2026-11-18T00:00:00Z",
      };
      rooms.push(created);
      return route.fulfill({ status: 201, json: { data: created } });
    }
    return route.fulfill({ json: { data: rooms } });
  });
  await page.route(/\/bff\/assets(\?.*)?$/, async (route: Route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON() as Body;
      writes.things.push(body);
      const created = {
        id: `a${things.length + 1}`,
        household_id: "h1",
        property_id: "p1",
        room_id: body.room_id ?? null,
        name: String(body.name ?? ""),
        category: body.category ?? null,
        created_at: "2026-11-18T00:00:00Z",
        updated_at: "2026-11-18T00:00:00Z",
        ...body,
      };
      things.push(created);
      return route.fulfill({ status: 201, json: { data: created } });
    }
    const query = new URL(route.request().url()).searchParams;
    return route.fulfill({
      json: {
        data: query.get("property_id")
          ? things.filter((t) => t.property_id === query.get("property_id"))
          : things,
      },
    });
  });
  await page.route(/\/bff\/maintenance\/tasks(\?.*)?$/, (route: Route) =>
    route.fulfill({ json: { data: TASKS } }),
  );
  // The linked-finances card fetches these; empty is a real state.
  await page.route(/\/bff\/loans(\?.*)?$/, (route: Route) => route.fulfill({ json: { data: [] } }));
  await page.route(/\/bff\/bills(\?.*)?$/, (route: Route) => route.fulfill({ json: { data: [] } }));

  return writes;
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = false) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

test("phone 390: add a room, then a thing in it", async ({ loggedInPage: page }, testInfo) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  const writes = await stubProperty(page);
  await page.goto("/dashboard/properties/p1");
  const main = page.locator("main");

  await expect(main.getByRole("heading", { level: 1, name: "Maple Street" })).toBeVisible();
  // The rooms → things tree: each room card is one control at 44 px+.
  const kitchen = main.locator('[data-room-card="r1"]');
  const utility = main.locator('[data-room-card="r2"]');
  await expect(kitchen).toBeVisible();
  await expect(kitchen).toContainText("Kitchen");
  await expect(kitchen).toContainText("0 things");
  await expect(utility).toContainText("1 thing");
  // A thing that belongs to the house itself is kept, not dropped.
  await expect(main.getByRole("button", { name: /Not in a room, 1 thing/ })).toBeVisible();
  for (const card of [kitchen, utility]) {
    const box = (await card.boundingBox())!;
    expect(box.height).toBeGreaterThanOrEqual(43.5);
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(390);
  }
  await expectNoHorizontalOverflow(page);
  await snap(page, testInfo, "property-390", true);

  // ── Add a room ─────────────────────────────────────────────
  await main.getByRole("button", { name: "Add room" }).first().click();
  const roomDialog = page.getByRole("dialog", { name: "Add a room" });
  await expect(roomDialog).toBeVisible();
  await expect(roomDialog).toContainText("Maple Street");
  const nameField = roomDialog.getByRole("textbox", { name: "Room name" });
  const fieldBox = (await nameField.boundingBox())!;
  expect(fieldBox.height).toBeGreaterThanOrEqual(43.5);
  await nameField.fill("Backyard");
  await roomDialog.getByRole("textbox", { name: /Floor/ }).fill("0");
  await snap(page, testInfo, "property-390-add-room");
  await roomDialog.getByRole("button", { name: "Add room" }).click();
  await expect(roomDialog).toBeHidden();

  expect(writes.rooms).toEqual([{ name: "Backyard", floor: 0 }]);
  // The new room appears without a reload.
  const backyard = main.locator('[data-room-card="r3"]');
  await expect(backyard).toBeVisible();
  await expect(backyard).toContainText("Backyard");
  await expect(backyard).toContainText("0 things");
  await expectNoHorizontalOverflow(page);

  // ── Add a thing in it ──────────────────────────────────────
  await backyard.click();
  const roomSheet = page.getByRole("dialog", { name: "Backyard" });
  await expect(roomSheet).toBeVisible();
  await expect(roomSheet).toContainText("Nothing in here yet.");
  await roomSheet.getByRole("button", { name: /Add a thing to Backyard/ }).click();

  const thingDialog = page.getByRole("dialog", { name: "Add a thing" });
  await expect(thingDialog).toBeVisible();
  // The room is preselected from the card that was open.
  await expect(thingDialog.getByRole("textbox", { name: "Room" })).toHaveValue("Backyard");
  await thingDialog.getByRole("textbox", { name: "Name" }).fill("Greenhouse");
  await thingDialog.getByRole("textbox", { name: "Make" }).fill("Palram");
  await snap(page, testInfo, "property-390-add-thing");
  await thingDialog.getByRole("button", { name: "Add thing" }).click();
  await expect(thingDialog).toBeHidden();

  expect(writes.things).toEqual([
    { name: "Greenhouse", property_id: "p1", room_id: "r3", manufacturer: "Palram" },
  ]);
  // The room card counts it, and the sheet lists it.
  await expect(backyard).toContainText("1 thing");
  await backyard.click();
  const reopened = page.getByRole("dialog", { name: "Backyard" });
  await expect(reopened.getByRole("link", { name: /Greenhouse/ })).toHaveAttribute(
    "href",
    /\/dashboard\/assets\/a3$/,
  );
  await expectNoHorizontalOverflow(page);
  await snap(page, testInfo, "property-390-after", true);
});

test("phone 390: a thing with no room is added without room_id", async ({ loggedInPage: page }) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  const writes = await stubProperty(page);
  await page.goto("/dashboard/properties/p1");
  const main = page.locator("main").first();

  await main.getByRole("button", { name: "Add thing" }).first().click();
  const dialog = page.getByRole("dialog", { name: "Add a thing" });
  await expect(dialog.getByRole("textbox", { name: "Room" })).toHaveValue("Not in a room");
  await dialog.getByRole("textbox", { name: "Name" }).fill("Sump pump");
  await dialog.getByRole("button", { name: "Add thing" }).click();
  await expect(dialog).toBeHidden();

  // room_id absent, not "" — the API treats an empty string as a value.
  expect(writes.things).toEqual([{ name: "Sump pump", property_id: "p1" }]);
  await expect(main.getByRole("button", { name: /Not in a room, 2 things/ })).toBeVisible();
});

test("phone 390: a house with no rooms yet says how to start", async ({ loggedInPage: page }, testInfo) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route(/\/bff\/properties\/p1$/, (route: Route) => route.fulfill({ json: { data: PROPERTY } }));
  await page.route(/\/bff\/properties\/p1\/rooms$/, (route: Route) => route.fulfill({ json: { data: [] } }));
  await page.route(/\/bff\/assets(\?.*)?$/, (route: Route) => route.fulfill({ json: { data: [] } }));
  await page.route(/\/bff\/maintenance\/tasks(\?.*)?$/, (route: Route) => route.fulfill({ json: { data: [] } }));
  await page.route(/\/bff\/loans(\?.*)?$/, (route: Route) => route.fulfill({ json: { data: [] } }));
  await page.route(/\/bff\/bills(\?.*)?$/, (route: Route) => route.fulfill({ json: { data: [] } }));

  await page.goto("/dashboard/properties/p1");
  const main = page.locator("main");
  await expect(main.locator("[data-rooms-empty]")).toContainText("No rooms yet");
  await expect(main.locator("[data-attention-list]")).toHaveCount(0);
  await expectNoHorizontalOverflow(page);
  await snap(page, testInfo, "property-390-empty", true);
});

test("desktop 1440: the same tree, and the mortgage figure is not the 100x bug", async ({ loggedInPage: page }, testInfo) => {
  onlyOn("desktop");
  await page.setViewportSize({ width: 1440, height: 900 });
  await stubProperty(page);
  await page.goto("/dashboard/properties/p1");
  const main = page.locator("main");

  await expect(main.locator('[data-room-card="r1"]')).toBeVisible();
  await expect(main.locator('[data-room-card="r2"]')).toBeVisible();
  // 983.88 + 150 + 100 = 1,233.88/mo from the shared money math.
  const monthly = main.locator('[data-field="monthly_carrying_cost"]');
  await expect(monthly).toContainText("$1,234");
  await expect(main).not.toContainText("86,606");
  await expect(main).not.toContainText("98,388");
  // The overdue upkeep is named, and the clean one is not called overdue.
  const attention = main.locator("[data-attention-list]");
  await expect(attention).toContainText("Replace furnace filter");
  await expect(attention).not.toContainText("Yearly tune-up");
  await expect(attention).not.toContainText("Cleaned already");

  // Edit is a real button from md up and only sends the property's own fields.
  const puts: Body[] = [];
  await page.route(/\/bff\/properties\/p1$/, async (route: Route) => {
    if (route.request().method() === "PUT") {
      puts.push(route.request().postDataJSON() as Body);
      return route.fulfill({ json: { data: PROPERTY } });
    }
    return route.fulfill({ json: { data: PROPERTY } });
  });
  await main.getByRole("button", { name: "Edit" }).click();
  const name = main.getByRole("textbox", { name: "Name" });
  await name.fill("Maple Street house");
  await main.getByRole("button", { name: "Save" }).click();
  await expect.poll(() => puts.length).toBe(1);
  expect(puts[0]).toMatchObject({ name: "Maple Street house" });
  for (const legacy of ["mortgage_amount", "mortgage_rate", "property_tax_annual", "insurance_annual", "hoa_fee_monthly"]) {
    expect(puts[0]).not.toHaveProperty(legacy);
  }
  await expectNoHorizontalOverflow(page);
  await snap(page, testInfo, "property-1440");
});
