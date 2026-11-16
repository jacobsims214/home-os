import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow } from "./fixtures";

/**
 * Entity list pattern (#4530): components/entity/EntityList.tsx.
 * Phone (< 768): stacked cards, each one tappable link, no horizontal scroll.
 * ≥ 768: a table in a horizontal scroller, so columns never clip.
 * "Add <type>" opens the create modal (no /new route) and POSTs.
 * Viewports and colour schemes are pinned per test, so this runs once, in the
 * desktop-chrome project. Screenshots are attached for a visual check.
 */
const BILLS = [
  { id: "b1", name: "Duke Energy electric — Main Residence (time-of-use plan, autopay)", amount: "182.40", due_day: 12, category: "Electric", property_id: "p1" },
  { id: "b2", name: "Internet", amount: "70", due_day: 3, category: "Internet", property_id: "p1" },
  { id: "b3", name: "Homeowners insurance (State Farm policy 88-C4-1192)", amount: "1840", due_day: 28, category: "Insurance", property_id: "p2" },
];
const PROPERTIES = [
  { id: "p1", name: "Main house on Longwood Avenue" },
  { id: "p2", name: "Lake cabin" },
];

type Captured = { posts: unknown[] };

async function stubBills(page: Page, opts: { fail?: boolean } = {}): Promise<Captured> {
  const captured: Captured = { posts: [] };
  await page.route("**/bff/properties", (route) => route.fulfill({ json: { data: PROPERTIES } }));
  await page.route("**/bff/bills", async (route) => {
    const req = route.request();
    if (req.method() === "POST") {
      const body = req.postDataJSON();
      captured.posts.push(body);
      return route.fulfill({ status: 201, json: { data: { id: "b9", ...body } } });
    }
    if (opts.fail) return route.fulfill({ status: 500, json: { error: "boom" } });
    return route.fulfill({ json: { data: BILLS } });
  });
  await page.route("**/bff/bills/b9", (route) =>
    route.fulfill({ json: { data: { id: "b9", name: "Water", amount: "45.10", due_day: 9 } } }),
  );
  return captured;
}

async function gotoBills(page: Page) {
  await page.goto("/dashboard/bills");
  await expect(page.locator("main").getByRole("heading", { level: 2, name: "Bills" })).toBeVisible();
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = false) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

test.beforeEach(async ({}, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chrome", "viewports are pinned per test");
});

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme}`, () => {
    test.use({ colorScheme });

    test("phone 390: cards, no clipping, Add opens the create modal and POSTs", async ({ loggedInPage: page }, testInfo) => {
      await page.setViewportSize({ width: 390, height: 844 });
      const captured = await stubBills(page);
      await gotoBills(page);
      const main = page.locator("main");

      const cards = main.locator("[data-entity-card]");
      await expect(cards).toHaveCount(BILLS.length);
      await expect(main.locator("[data-entity-table]")).toBeHidden();
      for (let i = 0; i < BILLS.length; i++) {
        const box = (await cards.nth(i).boundingBox())!;
        expect(box.height).toBeGreaterThanOrEqual(43.5) // 44 px, allowing sub-pixel layout;
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width).toBeLessThanOrEqual(390);
      }
      // The long name wraps inside its card, and the details are all there.
      await expect(cards.first()).toContainText(BILLS[0].name);
      await expect(cards.first()).toContainText("$182");
      await expect(cards.first()).toContainText("Electric");
      await expect(cards.first()).toHaveAttribute("href", "/dashboard/bills/b1");
      // Card text uses the tokens, so it stays readable in dark mode.
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
      await snap(page, testInfo, `bills-390-${colorScheme}`, true);

      // Add: a dialog, not a /new navigation.
      const add = main.getByRole("button", { name: "Add bill" });
      const addBox = (await add.boundingBox())!;
      expect(addBox.height).toBeGreaterThanOrEqual(43.5) // 44 px, allowing sub-pixel layout;
      await add.click();
      const dialog = page.getByRole("dialog", { name: "Add bill" });
      await expect(dialog).toBeVisible();
      expect(page.url()).not.toContain("/new");
      // Full screen on phones, 44 px inputs with 16 px text.
      const dialogBox = (await dialog.boundingBox())!;
      expect(dialogBox.width).toBeGreaterThanOrEqual(389);
      const nameInput = dialog.getByRole("textbox", { name: "Name" });
      const inputBox = (await nameInput.boundingBox())!;
      expect(inputBox.height).toBeGreaterThanOrEqual(43.5) // 44 px, allowing sub-pixel layout;
      expect(await nameInput.evaluate((el) => getComputedStyle(el).fontSize)).toBe("16px");

      // Validation: empty name is rejected without a request.
      await dialog.getByRole("button", { name: "Add bill" }).click();
      await expect(dialog.getByText("Name is required")).toBeVisible();
      expect(captured.posts).toHaveLength(0);
      await snap(page, testInfo, `bills-390-${colorScheme}-create`);

      await nameInput.fill("Water");
      await dialog.getByRole("textbox", { name: "Amount" }).fill("$45.10");
      await dialog.getByRole("textbox", { name: "Due day of month" }).fill("9");
      await dialog.getByRole("button", { name: "Add bill" }).click();
      await page.waitForURL("**/dashboard/bills/b9");
      expect(captured.posts).toEqual([{ name: "Water", amount: "45.10", due_day: 9 }]);
    });

    test("desktop 1440: table with every column visible, rows open the record", async ({ loggedInPage: page }, testInfo) => {
      await page.setViewportSize({ width: 1440, height: 900 });
      await stubBills(page);
      await gotoBills(page);
      const main = page.locator("main");

      const table = main.locator("[data-entity-table]");
      await expect(table).toBeVisible();
      await expect(main.locator("[data-entity-card]").first()).toBeHidden();
      const wrap = (await table.boundingBox())!;
      for (const label of ["Name", "Amount", "Due", "Category"]) {
        const th = (await table.getByRole("columnheader", { name: label }).boundingBox())!;
        expect(th.x).toBeGreaterThanOrEqual(wrap.x);
        expect(th.x + th.width).toBeLessThanOrEqual(wrap.x + wrap.width + 1);
      }
      await expect(table.getByRole("row")).toHaveCount(BILLS.length + 1);
      await expect(main.getByText("Monthly total")).toBeVisible();
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `bills-1440-${colorScheme}`);

      // Desktop Add opens a centred modal.
      await main.getByRole("button", { name: "Add bill" }).click();
      const dialog = page.getByRole("dialog", { name: "Add bill" });
      await expect(dialog).toBeVisible();
      expect((await dialog.boundingBox())!.width).toBeLessThan(700);
      await snap(page, testInfo, `bills-1440-${colorScheme}-create`);
      await dialog.getByRole("button", { name: "Cancel" }).click();
      await expect(dialog).toBeHidden();

      // Clicking a non-link cell opens the record.
      await table.getByRole("cell", { name: "Insurance", exact: true }).click();
      await page.waitForURL("**/dashboard/bills/b3");
    });
  });
}

test("tablet 800: the table scrolls inside its frame instead of clipping", async ({ loggedInPage: page }) => {
  await page.setViewportSize({ width: 800, height: 900 });
  await stubBills(page);
  await gotoBills(page);
  const table = page.locator("main [data-entity-table]");
  await expect(table).toBeVisible();
  expect(await table.evaluate((el) => getComputedStyle(el).overflowX)).toBe("auto");
  await expect(table.getByRole("columnheader", { name: "Category" })).toBeAttached();
  await expectNoHorizontalOverflow(page);
});

test("API failure shows an error with retry, not an empty list", async ({ loggedInPage: page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await stubBills(page, { fail: true });
  await page.goto("/dashboard/bills");
  const alert = page.locator("main").getByRole("alert");
  await expect(alert).toContainText("Couldn't load bills", { timeout: 20_000 });
  await expect(alert.getByRole("button", { name: "Try again" })).toBeVisible();
  await expect(page.locator("main").getByText("No bills yet.")).toBeHidden();
});
