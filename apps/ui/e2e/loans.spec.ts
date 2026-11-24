import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * Loans (#4552), mobile-first.
 *
 * Phone (< 768 px): the list is stacked cards, each one link with a 44 px+ tap
 * target, showing the money facts the mockup asks for (balance, rate, payment,
 * due day); the detail page's summary is 2 x 2, not a fixed 2-column grid.
 * Nothing scrolls sideways. 768 px and up: a table in a horizontal scroller.
 *
 * A loan is about one property, vehicle or asset or the family (#4704, domain
 * model v2): requests carry the typed property_id / vehicle_id / asset_id and
 * never the pre-v2 entity_type / entity_id pair that migration 056 dropped
 * (KB architecture/linked-finances-ui.md).
 *
 * Viewports are pinned per test: desktop-chrome, and the phone tests on
 * iphone-390 too (WebKit, touch; #4757). Screenshots are attached for a visual
 * check.
 */

type Body = Record<string, unknown>;

const LOANS: Body[] = [
  {
    id: "l1",
    name: "First mortgage — Main house on Longwood Avenue",
    property_id: "p1",
    lender: "Wells Fargo",
    original_amount: "412000.00",
    remaining_balance: "318450.00",
    interest_rate: "6.250",
    term_months: 360,
    monthly_payment: "2537.10",
    due_day: 1,
  },
  {
    id: "l2",
    name: "Odyssey loan",
    vehicle_id: "v1",
    lender: "Honda Financial",
    original_amount: "31000.00",
    remaining_balance: "12480.00",
    interest_rate: "4.900",
    monthly_payment: "512.34",
    due_day: 15,
  },
  { id: "l3", name: "Family credit line", original_amount: "8000.00" },
];

const PROPERTIES = [{ id: "p1", name: "Main house on Longwood Avenue" }];
const VEHICLES = [{ id: "v1", year: 2019, make: "Honda", model: "Odyssey EX-L" }];
const ASSETS: Body[] = [];

async function stubLoans(page: Page): Promise<{ posts: Body[]; puts: Body[] }> {
  const captured = { posts: [] as Body[], puts: [] as Body[] };
  await page.route(/\/bff\/properties(\?.*)?$/, (route) => route.fulfill({ json: { data: PROPERTIES } }));
  await page.route(/\/bff\/vehicles(\?.*)?$/, (route) => route.fulfill({ json: { data: VEHICLES } }));
  await page.route(/\/bff\/assets(\?.*)?$/, (route) => route.fulfill({ json: { data: ASSETS } }));
  await page.route(/\/bff\/loans(\?.*)?$/, async (route) => {
    const req = route.request();
    if (req.method() === "POST") {
      const body = req.postDataJSON() as Body;
      captured.posts.push(body);
      return route.fulfill({ status: 201, json: { data: { id: "l9", ...body } } });
    }
    return route.fulfill({ json: { data: LOANS } });
  });
  await page.route(/\/bff\/loans\/l[0-9]+(\?.*)?$/, async (route) => {
    const req = route.request();
    const id = req.url().match(/loans\/(l[0-9]+)/)![1];
    const loan = LOANS.find((l) => l.id === id) ?? LOANS[0];
    if (req.method() === "PUT") {
      const body = req.postDataJSON() as Body;
      captured.puts.push(body);
      // The API answers with the stored record: the typed FKs it now has.
      return route.fulfill({ json: { data: { ...loan, ...body } } });
    }
    if (req.method() === "DELETE") return route.fulfill({ status: 204, body: "" });
    return route.fulfill({ json: { data: loan } });
  });
  return captured;
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = true) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

/** No request body may carry the pair migration 056 dropped. */
function expectNoLegacySubject(body: Body) {
  expect(body, "a loan request must not send entity_type").not.toHaveProperty("entity_type");
  expect(body, "a loan request must not send entity_id").not.toHaveProperty("entity_id");
}


/** Choose an option from a Mantine Select (its listbox is portalled). */
async function formSelect(page: Page, scope: ReturnType<Page["locator"]>, label: string, option: string) {
  await scope.getByLabel(label).click();
  await page.getByRole("option", { name: option, exact: true }).click();
}

async function gotoLoans(page: Page) {
  await page.goto("/dashboard/loans");
  await expect(page.locator("main").getByRole("heading", { level: 2, name: "Loans" })).toBeVisible();
}

test("phone 390: cards with balance, rate, payment and due day; no sideways scroll; Add loan posts typed FKs", async ({
  loggedInPage: page,
}, testInfo) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  const captured = await stubLoans(page);
  await gotoLoans(page);
  const main = page.locator("main");

  const cards = main.locator("[data-loan-card]");
  await expect(cards).toHaveCount(LOANS.length);
  await expect(main.locator("[data-loan-table]")).toBeHidden();

  for (let i = 0; i < LOANS.length; i++) {
    const box = (await cards.nth(i).boundingBox())!;
    expect(box.height).toBeGreaterThanOrEqual(43.5); // 44 px, allowing sub-pixel layout
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(390);
  }

  // The money facts, on the first card: a long name wraps, nothing is cut off.
  const first = cards.first();
  await expect(first).toContainText("First mortgage — Main house on Longwood Avenue");
  await expect(first).toContainText("$318,450");
  await expect(first).toContainText("6.25%");
  await expect(first).toContainText("$2,537.10");
  await expect(first).toContainText("Day 1");
  await expect(first).toHaveAttribute("href", "/dashboard/loans/l1");
  // The vehicle loan is named as the vehicle pages name it.
  await expect(cards.nth(1)).toContainText("2019 Honda Odyssey EX-L");

  await expectNoHorizontalOverflow(page);
  await snap(page, testInfo, "loans-list-phone-390");

  // Add loan: the bottom-sheet form opens and POSTs one typed FK.
  await main.getByRole("button", { name: "Add loan" }).first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByLabel("Name").fill("Boat loan");
  await dialog.getByLabel("Amount borrowed").fill("$12,000");
  await dialog.getByLabel("Rate (%)").fill("7.5");
  await dialog.getByLabel("Monthly payment").fill("240");
  await dialog.getByLabel("Due day of month").fill("20");
  await dialog.getByRole("button", { name: "Add loan" }).click();

  await expect.poll(() => captured.posts.length).toBe(1);
  const post = captured.posts[0];
  expectNoLegacySubject(post);
  expect(post.name).toBe("Boat loan");
  expect(post.original_amount).toBe("12000");
  expect(post.remaining_balance).toBe("12000"); // defaults to the amount
  expect(post.interest_rate).toBe("7.5");
  expect(post.monthly_payment).toBe("240");
  expect(post.due_day).toBe(20);
  // Family-level: the picker stayed on "the whole family", so all three are empty.
  expect(post.property_id).toBe("");
  expect(post.vehicle_id).toBe("");
  expect(post.asset_id).toBe("");
  await expect(dialog).toBeHidden();
});

test("phone 390: the detail summary is 2 x 2 with rate, payment, due day and balance", async ({ loggedInPage: page }, testInfo) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  await stubLoans(page);

  await page.goto("/dashboard/loans/l1");
  const main = page.locator("main");
  await expect(main.getByRole("heading", { level: 2 })).toContainText("First mortgage");
  const summary = page.locator("[data-loan-summary]");
  await expect(summary).toBeVisible();
  await expect(summary).toContainText("Balance");
  await expect(summary).toContainText("$318,450");
  await expect(summary).toContainText("Rate");
  await expect(summary).toContainText("6.25%");
  await expect(summary).toContainText("Monthly payment");
  await expect(summary).toContainText("$2,537.10");
  await expect(summary).toContainText("Day 1");
  // Two facts per row on a phone: two columns, not the desktop row of four.
  const columns = await summary.evaluate((el) => getComputedStyle(el).gridTemplateColumns.split(" ").length);
  expect(columns).toBe(2);
  // The rest of the page is stacked labels over values, not a fixed 2-col grid.
  await expect(main).toContainText("Wells Fargo");
  await expect(main).toContainText("360 months");
  await expect(main).toContainText("Day 1 of the month");
  await expectNoHorizontalOverflow(page);
  await snap(page, testInfo, "loan-detail-phone-390");
});

test("phone 390: picking a vehicle sends vehicle_id only, never entity_type", async ({ loggedInPage: page }) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  const captured = await stubLoans(page);
  await gotoLoans(page);
  const main = page.locator("main");

  await main.getByRole("button", { name: "Add loan" }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill("Odyssey loan 2");
  await formSelect(page, dialog, "About", "2019 Honda Odyssey EX-L");
  await dialog.getByRole("button", { name: "Add loan" }).click();

  await expect.poll(() => captured.posts.length).toBe(1);
  const post = captured.posts[0];
  expectNoLegacySubject(post);
  expect(post.vehicle_id).toBe("v1");
  // The other typed FKs are cleared, not left as the family default sentinel.
  expect(post.property_id).toBe("");
  expect(post.asset_id).toBe("");
});

test("desktop: a table with the same facts, and editing posts one typed FK", async ({ loggedInPage: page }, testInfo) => {
  onlyOn("desktop");
  const captured = await stubLoans(page);
  await page.setViewportSize({ width: 1280, height: 900 });
  await gotoLoans(page);
  const main = page.locator("main");

  const table = main.locator("[data-loan-table]");
  await expect(table).toBeVisible();
  // The cards' list is `md:hidden`, so on desktop every card is hidden.
  await expect(main.locator("[data-loan-card]").first()).toBeHidden();
  await expect(table.locator("tbody tr")).toHaveCount(LOANS.length);
  await expect(table).toContainText("$318,450");
  await expect(table).toContainText("6.25%");
  await expect(table).toContainText("Day 1");
  await expect(table.locator('a[href="/dashboard/loans/l1"]')).toBeVisible();
  await snap(page, testInfo, "loans-list-desktop");

  // Edit on the vehicle loan: the picker shows the current vehicle, and a
  // change to the property clears vehicle_id in the same request.
  await page.goto("/dashboard/loans/l2");
  await expect(page.locator("main").getByRole("heading", { level: 2 })).toContainText("Odyssey loan");
  await page.getByRole("button", { name: "Edit" }).click();
  const form = page.locator("form#loan-edit");
  await expect(form.getByLabel("About")).toBeVisible();
  await form.getByLabel("About").click();
  await page.getByRole("option", { name: "Main house on Longwood Avenue" }).click();
  await page.getByRole("button", { name: "Save" }).click();

  await expect.poll(() => captured.puts.length).toBe(1);
  const put = captured.puts[0];
  expectNoLegacySubject(put);
  expect(put.property_id).toBe("p1");
  expect(put.vehicle_id).toBe(""); // switching subject is one change
  expect(put.asset_id).toBe("");
  expect(put.name).toBe("Odyssey loan");
  await expect(page.getByRole("button", { name: "Edit" })).toBeVisible(); // back in view mode
});
