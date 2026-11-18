import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * Assets ("things") pages (#4554), built to the approved People & House mockup
 * (artifact #89 v1, House > property > room > "thing") inside the approved
 * shell (#75 v1), on the EntityList / DetailPageLayout patterns
 * (KB architecture/entity-list-pattern.md, architecture/detail-page-layout.md).
 *
 * What this spec proves, and why each part is worth a test:
 *  1. MOBILE-FIRST. At 390 px the list is cards (one tappable link each, no
 *     clipping, 44 px+ targets) and the detail is the layout's sticky title bar
 *     with Details open; at 1440 px the list is the table and nothing scrolls
 *     sideways.
 *  2. MONEY ABSENT IS NOT MONEY ZERO (#4912). The API redacts current_value /
 *     purchase_price / purchase_date for a role without money read, so the
 *     pages must omit those facts entirely — never render "$0", and never show
 *     a summary strip of dashes.
 *  3. A FAMILY-OWNED THING HAS NO HOUSE. The mockup's "Family-owned things" are
 *     assets with no property; the detail says so instead of inventing a place.
 *
 * The API is stubbed per test with page.route against /bff (the UI talks to its
 * own BFF), so each payload is exact.
 */

/** The mockup's Furnace: a Carrier in the utility room at Maple Street, with money. */
const FURNACE = {
  id: "a1",
  household_id: "h1",
  property_id: "p1",
  room_id: "r1",
  name: "Furnace",
  category: "HVAC",
  manufacturer: "Carrier",
  model: "59SC5A080S17",
  serial_number: "2419A51234",
  purchase_date: "2019-10-02",
  purchase_price: "5100.00",
  current_value: "4200.00",
  warranty_expiry: "2029-10-01",
  notes: "Filter 16x25x1, MERV 11. Manual in the utility room.",
  created_at: "2019-10-02T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

/** A family-owned thing: no property, no room. */
const MACBOOK = {
  id: "a2",
  household_id: "h1",
  property_id: null,
  room_id: null,
  name: 'MacBook Pro 14"',
  category: "Electronics",
  manufacturer: "Apple",
  model: "M3 Pro",
  serial_number: null,
  purchase_date: null,
  purchase_price: null,
  current_value: "2400.00",
  warranty_expiry: "2027-08-14",
  notes: null,
  created_at: "2024-08-14T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const PROPERTIES = [{ id: "p1", name: "Maple Street" }];
const ROOMS = [{ id: "r1", property_id: "p1", name: "Utility room", floor: null, notes: null, created_at: "2026-01-01T00:00:00Z" }];

async function stubAssetsApi(
  page: Page,
  opts: { assets?: unknown[]; asset?: Record<string, unknown>; onPut?: (body: Record<string, unknown>) => void; onPost?: (body: Record<string, unknown>) => void } = {},
): Promise<void> {
  const assets = opts.assets ?? [FURNACE, MACBOOK];
  await page.route("**/bff/properties**", (route) => route.fulfill({ json: { data: PROPERTIES } }));
  await page.route("**/bff/assets", async (route) => {
    const req = route.request();
    if (req.method() === "POST") {
      opts.onPost?.(req.postDataJSON() as Record<string, unknown>);
      return route.fulfill({ status: 201, json: { data: { id: "a-new", ...(req.postDataJSON() as object) } } });
    }
    return route.fulfill({ json: { data: assets } });
  });
  await page.route("**/bff/assets/*", async (route) => {
    const req = route.request();
    if (req.method() === "PUT") {
      const body = req.postDataJSON() as Record<string, unknown>;
      opts.onPut?.(body);
      return route.fulfill({ json: { data: { ...(opts.asset ?? FURNACE), ...body } } });
    }
    if (req.method() === "DELETE") return route.fulfill({ status: 204, body: "" });
    return route.fulfill({ json: { data: opts.asset ?? FURNACE } });
  });
  // The Resources section's own reads: empty is enough here.
  await page.route("**/bff/properties/p1/rooms**", (route) => route.fulfill({ json: { data: ROOMS } }));
  await page.route("**/bff/notes**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/files**", (route) => route.fulfill({ json: { data: [] } }));
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = false) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme}`, () => {
    test.use({ colorScheme });

    test("phone 390: things are cards, one link each, no clipping", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubAssetsApi(page);
      await page.goto("/dashboard/assets");
      const main = page.locator("main");
      await expect(main.getByRole("heading", { level: 2, name: /Things/ })).toBeVisible();

      const cards = main.locator("[data-asset-card]");
      await expect(cards).toHaveCount(2);
      await expect(main.locator("[data-asset-table]")).toBeHidden();

      for (let i = 0; i < 2; i++) {
        const box = (await cards.nth(i).boundingBox())!;
        expect(box.height).toBeGreaterThanOrEqual(43.5); // 44 px target, sub-pixel allowed
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width).toBeLessThanOrEqual(390);
      }

      // The first card is the furnace: its name, its make+model line, its facts.
      const furnace = cards.first();
      await expect(furnace).toContainText("Furnace");
      await expect(furnace).toContainText("Carrier \u00b7 59SC5A080S17");
      await expect(furnace).toHaveAttribute("href", "/dashboard/assets/a1");
      await expect(furnace).toContainText("Maple Street");
      await expect(furnace).toContainText("$4,200");
      await expect(furnace).toContainText("Until Oct 2029");

      // A family-owned thing says nothing about a place, and no money it wasn't sent.
      const macbook = cards.nth(1);
      await expect(macbook).toContainText('MacBook Pro 14"');
      await expect(macbook).not.toContainText("Place");

      // Card text uses the theme tokens, so dark mode stays readable.
      const colours = await furnace.evaluate((card) => {
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
      await snap(page, testInfo, `assets-390-${colorScheme}`, true);

      // Add opens a sheet; it must not navigate to a /new route.
      const add = main.getByRole("button", { name: "Add thing" });
      expect((await add.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);
      await add.click();
      await expect(page.getByRole("dialog")).toBeVisible();
      expect(page.url()).not.toContain("/new");
      await snap(page, testInfo, `assets-390-${colorScheme}-add`);
    });

    test("phone 390: the detail opens on the title bar, summary and Where", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubAssetsApi(page);
      await page.goto("/dashboard/assets/a1");
      const main = page.locator("main");

      // The layout's own title bar: back to the list, the name as an h2, actions.
      await expect(main.getByRole("heading", { level: 2, name: "Furnace" })).toBeVisible();
      const back = main.getByRole("link", { name: "Back to Assets" });
      await expect(back).toHaveAttribute("href", "/dashboard/assets");
      expect((await back.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);
      const actions = main.getByRole("button", { name: "Actions for Furnace" });
      expect((await actions.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);

      // Details starts open; the summary strip is 2-up on a phone.
      await expect(main.getByRole("button", { name: "Details" })).toHaveAttribute("aria-expanded", "true");
      const summary = main.locator("[data-asset-summary]");
      await expect(summary).toBeVisible();
      await expect(summary).toContainText("$4,200");
      await expect(summary).toContainText("$5,100");
      await expect(summary).toContainText("Oct 2029");
      const statBoxes = await summary.locator("> div").evaluateAll((els) => (els as HTMLElement[]).map((e) => e.getBoundingClientRect().top));
      expect(new Set(statBoxes).size).toBeLessThan(statBoxes.length); // at least two share a row

      // Where: the room and the property are links into the tree.
      await expect(main.getByRole("link", { name: "Utility room" })).toHaveAttribute(
        "href",
        "/dashboard/properties/p1/rooms/r1",
      );
      await expect(main.getByRole("link", { name: /Maple Street/ })).toHaveAttribute("href", "/dashboard/properties/p1");
      // Make/model/serial are transcribed, so they are in the mono face.
      await expect(main.getByText("59SC5A080S17")).toHaveCSS("font-family", /mono/i);

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `assets-390-${colorScheme}-detail`, true);

      // Editing goes through the title bar: Edit -> Cancel/Save replace the menu.
      await actions.click();
      await page.getByRole("menuitem", { name: "Edit" }).click();
      await expect(main.getByRole("button", { name: "Save" })).toBeVisible();
      const cancel = main.getByRole("button", { name: "Cancel" });
      expect((await cancel.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);
      await snap(page, testInfo, `assets-390-${colorScheme}-edit`);
      await cancel.click();
      await expect(main.getByRole("heading", { level: 2, name: "Furnace" })).toBeVisible();
    });

    test("phone 390: a thing with no house says so, and no money means no strip", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      // #4912: the API omits purchase_* and nulls current_value for a role
      // without money read. The page must show no money fact and no $0.
      const redacted = { ...FURNACE, purchase_date: null, purchase_price: null, current_value: null, category: null };
      await stubAssetsApi(page, { asset: redacted });
      await page.goto("/dashboard/assets/a1");
      const main = page.locator("main");

      await expect(main.getByRole("heading", { level: 2, name: "Furnace" })).toBeVisible();
      // No money row at all: a redacted field is absent, not a dash and not $0.
      await expect(main.getByText("Paid", { exact: true })).toHaveCount(0);
      await expect(main.getByText("Current value", { exact: true })).toHaveCount(0);
      await expect(main).not.toContainText("$0");
      // Only the warranty date survives, and one fact is not a summary strip.
      await expect(main.getByText("Warranty until")).toBeVisible();
      await expect(main.locator("[data-asset-summary]")).toHaveCount(0);
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `assets-390-${colorScheme}-redacted`, true);

      // A thing with no property: the detail says the family owns it, and it
      // does not claim a room.
      await stubAssetsApi(page, { asset: MACBOOK });
      await page.goto("/dashboard/assets/a2");
      await expect(main.getByRole("heading", { level: 2, name: 'MacBook Pro 14"' })).toBeVisible();
      await expect(main.getByText("The whole family's, not a house")).toBeVisible();
      await expect(main.getByText("Not in a room")).toBeVisible();
    });

    test("desktop 1440: the list is a table and the detail is two columns", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("desktop");
      await page.setViewportSize({ width: 1440, height: 900 });
      await stubAssetsApi(page);
      await page.goto("/dashboard/assets");
      const main = page.locator("main");

      const table = main.locator("[data-asset-table]");
      await expect(table).toBeVisible();
      await expect(main.locator("[data-asset-card]").first()).toBeHidden();
      for (const heading of ["Name", "Make and model", "Place", "Value", "Warranty"]) {
        await expect(table.getByRole("columnheader", { name: heading })).toBeVisible();
      }
      await expect(table.getByRole("link", { name: "Furnace" })).toHaveAttribute("href", "/dashboard/assets/a1");
      await expect(table).toContainText("Carrier \u00b7 59SC5A080S17");
      await expect(table).toContainText("Maple Street");
      await expect(table).toContainText("$4,200");
      await expect(table).toContainText("Oct 2029");
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `assets-1440-${colorScheme}`);

      // The detail: sheets on the left, Resources on the right at >= 1024 px.
      await page.goto("/dashboard/assets/a1");
      await expect(main.getByRole("heading", { level: 2, name: "Furnace" })).toBeVisible();
      await expect(main.locator("[data-asset-summary]")).toBeVisible();
      // 4-up from 768 px: every stat shares one row.
      const tops = await main
        .locator("[data-asset-summary] > div")
        .evaluateAll((els) => (els as HTMLElement[]).map((e) => Math.round(e.getBoundingClientRect().top)));
      expect(new Set(tops).size).toBe(1);
      // Nothing collapses from md up: the Resources toggle is phone-only.
      await expect(main.getByRole("button", { name: "Resources" })).toBeHidden();
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `assets-1440-${colorScheme}-detail`, true);
    });
  });
}

test("error state offers Try again and never reads as an empty list", async ({ loggedInPage: page }) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route("**/bff/properties**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/assets", (route) => route.fulfill({ status: 500, json: { error: "boom" } }));
  await page.goto("/dashboard/assets");
  const main = page.locator("main");
  await expect(main.getByRole("alert")).toContainText("Couldn't load things");
  await expect(main.getByRole("button", { name: "Try again" })).toBeVisible();
  await expect(main.getByText("No things yet")).toHaveCount(0);
});

test("adding a thing sends the room, not a second property (a room derives it)", async ({ loggedInPage: page }) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  let posted: Record<string, unknown> | null = null;
  await stubAssetsApi(page, {
    assets: [],
    onPost: (body) => {
      posted = body;
    },
  });
  await page.goto("/dashboard/assets");
  const main = page.locator("main");
  await expect(main.getByText("No things yet")).toBeVisible();
  await main.getByRole("button", { name: "Add thing" }).last().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByLabel(/Name/).fill("Water heater");
  await dialog.getByLabel("Property").click();
  await page.getByRole("option", { name: "Maple Street" }).click();
  await dialog.getByLabel("Room").click();
  await page.getByRole("option", { name: "Utility room" }).click();
  await dialog.getByRole("button", { name: "Add thing" }).click();
  await expect.poll(() => posted).not.toBeNull();
  expect(posted!["name"]).toBe("Water heater");
  expect(posted!["room_id"]).toBe("r1");
  // The room carries its property; sending property_id too would be a second
  // source of truth for the same fact.
  expect(posted!["property_id"]).toBeUndefined();
});

test("editing sends the fields it changed and clears a room with an empty string", async ({ loggedInPage: page }) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  let put: Record<string, unknown> | null = null;
  await stubAssetsApi(page, {
    onPut: (body) => {
      put = body;
    },
  });
  await page.goto("/dashboard/assets/a1");
  const main = page.locator("main");
  await main.getByRole("button", { name: "Actions for Furnace" }).click();
  await page.getByRole("menuitem", { name: "Edit" }).click();
  await main.getByLabel(/Name/).fill("Furnace (basement)");
  await main.getByRole("button", { name: "Save" }).click();
  await expect.poll(() => put).not.toBeNull();
  expect(put!["name"]).toBe("Furnace (basement)");
  expect(put!["model"]).toBe("59SC5A080S17");
  // updateAssetRequest documents an empty room_id as the clear.
  expect(put!["room_id"]).toBe("r1");
  expect(put!["property_id"]).toBe("p1");
  // An untouched money value is re-sent as it was read, never as "".
  expect(put!["current_value"]).toBe("4200.00");
});
