import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * Money overview (#4571), built to the approved money mockup (artifact #102 v1).
 *
 * What this spec exists to prove:
 *
 *  1. THE TOTAL IS THE API'S, NOT A SUM OF THE CARDS. The obligations payload
 *     below deliberately makes `totals.committed` disagree with the sum of the
 *     categories: the API's figure is the one on screen. A page that added the
 *     categories up itself would print a different number and pass a weaker
 *     test; this payload catches it.
 *  2. MONEY IS ABSENT, NOT ZERO. A bill with no amount says "No amount", and
 *     its name appears in the "isn't counted" line — never "$0.00".
 *  3. A ROLE WITHOUT ACCESS SEES NO MONEY. A housesitter gets the no-access
 *     card and no amounts at all.
 *
 * The API is stubbed per test with page.route, so each payload is exact. Phone
 * widths run on iphone-390 (WebKit + touch), per #4757.
 */

const HOUSEHOLDS = (role: string) => ({
  data: [
    {
      id: "h1",
      name: "Sims",
      timezone: "America/New_York",
      role,
      joined_at: "2026-01-01T00:00:00Z",
      is_default: true,
      current: true,
    },
  ],
});

/**
 * Two categories + one uncategorized bill. The totals are the API's:
 * committed $4,194.05 while the categories below carry 2624.75 + 457.99 =
 * 3082.74. A client that summed them would show $3,082.74.
 */
const OBLIGATIONS = {
  month: "2026-11",
  categories: [
    {
      budget_category_id: "c-housing",
      name: "Housing",
      budgeted: "2700.00",
      committed: "2624.75",
      remaining: "75.25",
      items: [
        {
          kind: "loan",
          id: "l-mortgage",
          name: "Maple Street mortgage",
          frequency: "monthly",
          amount: "2184.00",
          monthly_amount: "2184.00",
          due_day: 1,
          due_date: null,
        },
        {
          kind: "bill",
          id: "b-tax",
          name: "Cabin property tax",
          frequency: "annual",
          amount: "1800.00",
          monthly_amount: "150.00",
          due_day: null,
          due_date: "2026-12-15",
        },
      ],
    },
    {
      budget_category_id: "c-utilities",
      name: "Utilities",
      budgeted: "450.00",
      committed: "457.99",
      remaining: "-7.99",
      items: [
        {
          kind: "bill",
          id: "b-electric",
          name: "Electric",
          frequency: "monthly",
          amount: "164.00",
          monthly_amount: "164.00",
          due_day: 22,
          due_date: null,
        },
      ],
    },
  ],
  uncategorized: {
    budget_category_id: null,
    name: "No category",
    budgeted: null,
    committed: "0.00",
    remaining: null,
    items: [
      {
        // No amount: the API lists it and does not count it.
        kind: "bill",
        id: "b-lawn",
        name: "Lawn care",
        frequency: "monthly",
        amount: null,
        monthly_amount: null,
        due_day: 15,
        due_date: null,
      },
    ],
  },
  totals: {
    budgeted: "3150.00",
    committed: "4194.05",
    bills: "1412.05",
    loan_payments: "2782.00",
    uncounted: 1,
  },
};

const DUE_SOON = {
  from: "2026-11-18",
  to: "2026-12-18",
  days: 30,
  total: "2360.00",
  bill_count: 1,
  loan_count: 1,
  items: [
    {
      source: "loan",
      id: "l-mortgage",
      name: "Maple Street mortgage",
      amount: "2184.00",
      due_date: "2026-12-01",
      due_weekday: "Tue",
      days_until_due: 13,
      status: "due",
      autopay: false,
      subject: null,
    },
    {
      source: "bill",
      id: "b-water",
      name: "Water & sewer",
      amount: "176.00",
      due_date: "2026-11-20",
      due_weekday: "Fri",
      days_until_due: 2,
      status: "due",
      autopay: false,
      subject: null,
    },
    {
      // No amount: the row must say so, never $0.00.
      source: "bill",
      id: "b-lawn",
      name: "Lawn care",
      amount: null,
      due_date: "2026-11-25",
      due_weekday: "Wed",
      days_until_due: 7,
      status: "due",
      autopay: false,
      subject: null,
    },
  ],
  coverage: { detail: "items", items_listed: 3, without_due_date: 0 },
};

/** Answers every read the money page makes, as `role`, with the payloads above. */
async function stubMoneyApi(
  page: Page,
  opts: { role?: string; obligations?: unknown; dueSoon?: unknown; failObligations?: boolean } = {},
) {
  const role = opts.role ?? "owner";
  const writes: { method: string; url: string; body: unknown }[] = [];
  await page.route("**/bff/me/households*", (route) => route.fulfill({ json: HOUSEHOLDS(role) }));
  await page.route("**/bff/budget/obligations*", (route) =>
    opts.failObligations
      ? route.fulfill({ status: 500, json: { error: "boom" } })
      : route.fulfill({ json: { data: opts.obligations ?? OBLIGATIONS } }),
  );
  await page.route("**/bff/budget/due-soon*", (route) => route.fulfill({ json: { data: opts.dueSoon ?? DUE_SOON } }));
  await page.route("**/bff/budget/categories*", async (route) => {
    const req = route.request();
    writes.push({ method: req.method(), url: req.url(), body: req.postDataJSON() });
    return route.fulfill({ status: 201, json: { data: { id: "c-new", name: "New" } } });
  });
  return writes;
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = false) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme}`, () => {
    test.use({ colorScheme });

    test("phone 390: the committed total is the API's, and absent money reads as absent", async ({
      loggedInPage: page,
    }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubMoneyApi(page);
      await page.goto("/dashboard/money");
      const main = page.locator("main");
      await expect(main.getByRole("heading", { level: 2, name: "Money" })).toBeVisible();

      // The month label is read back from the response, not the browser's clock.
      await expect(main.getByTestId("money-month")).toHaveText("November 2026");

      // THE API'S TOTAL. Summing the two categories would give $3,082.74.
      await expect(main.getByTestId("money-committed")).toHaveText("$4,194.05");
      await expect(main.getByTestId("money-budgeted")).toContainText("of $3,150.00 budgeted");
      await expect(main.getByTestId("money-committed")).not.toContainText("3,082.74");

      // The splits are the API's fields, not recomputed.
      await expect(main.getByTestId("money-split-bills")).toContainText("$1,412.05");
      await expect(main.getByTestId("money-split-loans")).toContainText("$2,782.00");

      // Over budget is words + colour, never colour alone.
      await expect(main.locator('[data-money-category="c-utilities"]')).toHaveAttribute("data-over", "true");
      await expect(main.locator('[data-money-category="c-utilities"]')).toContainText("Over by $7.99");
      await expect(main.getByTestId("money-over-count")).toContainText("1 category over");

      // A record with no amount is named, not counted as zero.
      await expect(main.getByTestId("money-uncounted")).toContainText("Lawn care");
      await expect(main.getByTestId("money-uncounted")).toContainText("no amount");
      await expect(main).not.toContainText("$0.00");

      // The due-soon card: the API's own 30-day total and counts.
      await expect(main.getByTestId("money-due-total")).toHaveText("$2,360.00");
      await expect(main.getByTestId("money-due-count")).toHaveText("2 payments");

      // Rows carry the API's weekday and the API's countdown, and a null amount
      // says "No amount".
      const lawn = main.locator('[data-money-due="bill-b-lawn"]');
      await expect(lawn).toContainText("Lawn care");
      await expect(lawn).toContainText("Wed");
      await expect(lawn).toContainText("in 7 days");
      await expect(lawn.locator("[data-money-no-amount]")).toHaveText("No amount");
      await expect(lawn).not.toContainText("$0.00");

      // 44 px tap targets, nothing clipped sideways.
      for (const row of ["loan-l-mortgage", "bill-b-water", "bill-b-lawn"]) {
        const box = (await main.locator(`[data-money-due="${row}"] a`).boundingBox())!;
        expect(box.height).toBeGreaterThanOrEqual(43.5);
      }
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `money-390-${colorScheme}`, true);
    });

    for (const role of ["housesitter", "vendor"] as const) {
      test(`phone 390: a ${role} sees no money at all`, async ({ loggedInPage: page }, testInfo: TestInfo) => {
        onlyOn("phone");
        await page.setViewportSize({ width: 390, height: 844 });
        await stubMoneyApi(page, { role });
        await page.goto("/dashboard/money");
        const main = page.locator("main");
        await expect(main.getByTestId("money-no-access")).toBeVisible();
        await expect(main.getByTestId("money-no-access")).toContainText(/Money isn't shared/);
        // Nothing of the family's money leaks into the card.
        await expect(main).not.toContainText("$4,194.05");
        await expect(main.getByTestId("money-committed")).toHaveCount(0);
        await expectNoHorizontalOverflow(page);
        await snap(page, testInfo, `money-no-access-${role}-390`);
      });
    }
  });
}

test("phone 390: a viewer reads every figure and gets no edit controls", async ({ loggedInPage: page }, testInfo) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  await stubMoneyApi(page, { role: "viewer" });
  await page.goto("/dashboard/money");
  const main = page.locator("main");
  await expect(main.getByRole("heading", { level: 2, name: "Money" })).toBeVisible();

  // A viewer SEES the money: nothing is hidden by a client-side role check.
  await expect(main.getByTestId("money-committed")).toHaveText("$4,194.05");
  await expect(main.locator('[data-money-category="c-housing"]')).toContainText("$2,624.75");

  // …and gets no way to change it.
  await expect(main.getByTestId("money-add-category")).toHaveCount(0);
  await expect(main.getByRole("button", { name: /^Edit the .* budget$/ })).toHaveCount(0);
  await expectNoHorizontalOverflow(page);
  await snap(page, testInfo, "money-viewer-390", true);
});

test("phone 390: a failed obligations read is an error card, never an empty page", async ({
  loggedInPage: page,
}, testInfo) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  await stubMoneyApi(page, { failObligations: true });
  await page.goto("/dashboard/money");
  const main = page.locator("main");
  await expect(main.getByTestId("money-obligations-error")).toBeVisible();
  await expect(main.getByTestId("money-obligations-error")).toContainText("Couldn't load this month's numbers");
  // It must not claim the family has nothing.
  await expect(main.getByText("Nothing in Money yet")).toHaveCount(0);
  await snap(page, testInfo, "money-error-390");
});

test("phone 390: adding a category sends the API's own validation shape", async ({ loggedInPage: page }, testInfo) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  const writes = await stubMoneyApi(page);
  await page.goto("/dashboard/money");
  const main = page.locator("main");

  await main.getByTestId("money-add-category").click();
  const sheet = page.getByRole("dialog", { name: "Add category" });
  await expect(sheet).toBeVisible();

  await sheet.getByLabel("Name").fill("Kids");
  await sheet.getByLabel("Monthly budget").fill("300");
  await sheet.getByRole("button", { name: "Save" }).click();

  await expect.poll(() => writes.length).toBe(1);
  expect(writes[0].method).toBe("POST");
  expect(writes[0].body).toMatchObject({ name: "Kids", monthly_amount: "300" });
});

test("desktop 1440: the overview uses the two-column layout", async ({ loggedInPage: page }, testInfo) => {
  onlyOn("desktop");
  await page.setViewportSize({ width: 1440, height: 900 });
  await stubMoneyApi(page);
  await page.goto("/dashboard/money");
  const main = page.locator("main");
  await expect(main.getByRole("heading", { level: 2, name: "Money" })).toBeVisible();
  await expect(main.getByTestId("money-committed")).toHaveText("$4,194.05");

  // Due soon on the left, budget on the right.
  const due = (await main.getByRole("region", { name: "Due soon" }).boundingBox())!;
  const budget = (await main.locator('section[aria-labelledby="money-budget-heading"]').boundingBox())!;
  expect(budget.x).toBeGreaterThan(due.x + due.width - 1);
  await expectNoHorizontalOverflow(page);
  await snap(page, testInfo, "money-1440");
});
