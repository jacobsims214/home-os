import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * Bills pages (#4551), built to the approved money mockup (artifact #102 v1).
 *
 * The three things this spec exists to prove, each from the R1 war games and
 * the role policy, plus the mobile-first layout:
 *
 *  1. A PAID BILL DOES NOT RENDER AS DUE. In R1 an agent told the family
 *     "Electric $180 due, still unpaid" about a bill paid that morning. The
 *     list and the detail read `status` from GET /budget/due-soon and nothing
 *     else, so the paid bill says "Paid" and carries no due treatment while an
 *     unpaid one beside it is plainly due — both facts, asserted separately.
 *  2. MARK PAID SHOWS THE NEXT DUE DATE. The sheet names the date the bill
 *     moves to before the person commits, and after saving it reports the
 *     API's new due date, not a guess.
 *  3. A ROLE WITHOUT ACCESS SEES NO MONEY. A housesitter gets the no-access
 *     card, no amounts, no rows, and no Money entry in the nav.
 *
 * The API is stubbed per test with page.route, so each payload is exact. Phone
 * widths run on iphone-390 (WebKit + touch), per #4757.
 */

const HOUSEHOLDS = (role: string) => ({
  data: [{ id: "h1", name: "Sims", timezone: "America/New_York", role, joined_at: "2026-01-01T00:00:00Z", is_default: true, current: true }],
});

/** Electric is PAID for Nov 22; Water is UNPAID and due in two days. */
/**
 * Electric is PAID for Nov 22 and pays itself from the joint account; Water is
 * UNPAID and a person has to pay it. The `attention` field is what tells the UI
 * which is which (#5499) — `is_autopay` is only the switch, and the row must
 * never read it to decide. The payees carry the contact details the family
 * needs to reach them.
 */
const BILLS = [
  {
    id: "b-electric", name: "Electric", amount: "164.00", due_day: 22, frequency: "monthly", due_date: null,
    category: "Electric", is_autopay: true, autopay_enabled: true, property_id: null, vehicle_id: null, asset_id: null,
    account_number: null, payment_url: null, notes: null,
    paid_date: "2026-11-18", paid_for_due_date: "2026-11-22",
    attention: "automatic",
    payment_account: { id: "pa1", name: "Joint checking", institution: "Riverside Credit Union", last_four: "4242", account_type: "checking" },
    payee: { vendor_id: "v1", name: "Riverside Power", phones: ["555-0142"], email: "billing@riversidepower.example", website: "https://riversidepower.example" },
  },
  {
    id: "b-water", name: "Water & sewer", amount: "186.00", due_day: null, frequency: "quarterly", due_date: "2026-11-20",
    category: "Water", is_autopay: false, autopay_enabled: false, property_id: null, vehicle_id: null, asset_id: null,
    account_number: null, payment_url: null, notes: null,
    paid_date: null, paid_for_due_date: null,
    attention: "due_soon",
    payee: { vendor_id: "v2", name: "Riverside Water Authority", phones: ["555-0177"], email: null, website: null },
  },
];

const DUE_SOON = {
  from: "2026-11-18",
  to: "2027-11-18",
  days: 365,
  total: "350.00",
  bill_count: 2,
  loan_count: 0,
  items: [
    {
      source: "bill", id: "b-water", name: "Water & sewer", amount: "186.00",
      due_date: "2026-11-20", due_weekday: "Fri", days_until_due: 2, status: "due", autopay: false, subject: null,
    },
    {
      // The paid one: the API says so, and the next date is a month on.
      source: "bill", id: "b-electric", name: "Electric", amount: "164.00",
      due_date: "2026-12-22", due_weekday: "Tue", days_until_due: 34, status: "paid", autopay: false, subject: null,
    },
  ],
  coverage: { detail: "items", items_listed: 2, without_due_date: 0 },
};

/** Answers the reads every bills page makes, as `role`, with the payloads above. */
async function stubBillsApi(
  page: Page,
  opts: { role?: string; bills?: unknown[]; dueSoon?: unknown; onPut?: (body: Record<string, unknown>) => void } = {},
) {
  const role = opts.role ?? "owner";
  const writes: { method: string; url: string; body: unknown }[] = [];
  await page.route("**/bff/me/households*", (route) => route.fulfill({ json: HOUSEHOLDS(role) }));
  await page.route("**/bff/budget/due-soon*", (route) => route.fulfill({ json: { data: opts.dueSoon ?? DUE_SOON } }));
  await page.route("**/bff/bills", async (route) => {
    const req = route.request();
    if (req.method() !== "GET") {
      writes.push({ method: req.method(), url: req.url(), body: req.postDataJSON() });
      return route.fulfill({ status: 201, json: { data: { id: "b-new", ...(req.postDataJSON() as object) } } });
    }
    return route.fulfill({ json: { data: opts.bills ?? BILLS } });
  });
  await page.route("**/bff/bills/*", async (route) => {
    const req = route.request();
    const id = new URL(req.url()).pathname.split("/").pop()!;
    const bill = (opts.bills ?? BILLS).find((b) => (b as { id: string }).id === id);
    if (req.method() === "PUT") {
      const body = req.postDataJSON() as Record<string, unknown>;
      writes.push({ method: "PUT", url: req.url(), body });
      opts.onPut?.(body);
      // This build's API has no payment field: it echoes the bill unchanged
      // unless the stub is told to accept the payment (recordedPayments).
      return route.fulfill({ json: { data: { ...(bill ?? {}), ...(opts as { echoPaid?: boolean }).echoPaid ? body : {} } } });
    }
    return route.fulfill({ json: { data: bill ?? BILLS[0] } });
  });
  // The "For" names and the Resources tabs: empty is enough here.
  await page.route("**/bff/properties**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/assets**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/vehicles**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/notes**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/files**", (route) => route.fulfill({ json: { data: [] } }));
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

    test("phone 390: the list gives a paid bill no due treatment and an unpaid one plainly", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubBillsApi(page);
      await page.goto("/dashboard/bills");
      const main = page.locator("main");
      await expect(main.getByRole("heading", { level: 2, name: "Bills" })).toBeVisible();

      // The paid bill: marked paid, no "Next … in N days", and not counted as due.
      const paid = main.locator('[data-bill-row="b-electric"]');
      await expect(paid).toHaveAttribute("data-due-state", "paid");
      await expect(paid).toContainText("Paid");
      // The API's weekday and date, never one the browser worked out.
      await expect(paid).toContainText("next Tue, Dec 22");
      await expect(paid).not.toContainText("in 34 days");

      // The unpaid one beside it is due, with the API's weekday and countdown.
      const due = main.locator('[data-bill-row="b-water"]');
      await expect(due).toHaveAttribute("data-due-state", "due");
      await expect(due).toContainText("Next Fri, Nov 20 · in 2 days");

      // One due now, and the header says so.
      await expect(main.getByTestId("bills-context")).toContainText("1 due now");
      await expect(main.getByTestId("bills-context")).toContainText("sorted by next due date");
      // Earliest first: Water (2 days) before Electric (34).
      const order = await main.locator("[data-bill-row]").evaluateAll((rows) => rows.map((r) => r.getAttribute("data-bill-row")));
      expect(order).toEqual(["b-water", "b-electric"]);

      // 44 px rows, nothing clipped, no sideways scroll.
      for (const row of ["b-water", "b-electric"]) {
        const box = (await main.locator(`[data-bill-row="${row}"]`).boundingBox())!;
        expect(box.height).toBeGreaterThanOrEqual(43.5);
      }

      // THE HEADLINE NUMBER, split by what the family has to do: the monthly
      // total, and how much of it is on autopay. Electric $164 monthly is
      // automatic; Water $186 quarterly spreads to $62/month and is manual.
      const split = main.getByTestId("bills-split");
      await expect(split).toContainText("$226.00/month");
      await expect(split).toContainText("of which $164.00 is on autopay");
      await expect(main.getByTestId("bills-actions")).toContainText("1 needs you");

      // AUTOMATIC VS MANUAL, AT A GLANCE: the badge is the API's `attention`
      // field, not a guess from `is_autopay`.
      const auto = main.locator('[data-bill-row="b-electric"]');
      await expect(auto).toHaveAttribute("data-attention", "automatic");
      await expect(auto.getByText("Automatic", { exact: true })).toBeVisible();
      const manual = main.locator('[data-bill-row="b-water"]');
      await expect(manual).toHaveAttribute("data-attention", "due_soon");
      await expect(manual.getByText("Due soon", { exact: true })).toBeVisible();

      // WHICH ACCOUNT, name + last four and never a full number.
      await expect(auto).toContainText("Joint checking ····4242");
      await expect(auto).not.toContainText("4242424242424242");
      // The manual bill has no autopay account, so its row shows none.
      await expect(manual).not.toContainText("····");

      // WHO WE PAY, and tap-to-call on a phone.
      await expect(auto).toContainText("Riverside Power");
      await expect(manual).toContainText("Riverside Water Authority");
      await expect(manual.getByRole("link", { name: /Call Riverside Water Authority/ })).toHaveAttribute("href", "tel:5550177");
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `bills-390-${colorScheme}`, true);
    });

    test("phone 390: mark paid names the next due date before and after saving", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      const writes = await stubBillsApi(page);
      await page.goto("/dashboard/bills/b-water");
      const main = page.locator("main");

      // The reading: Fri, Nov 20, in 2 days — the API's weekday.
      await expect(main.getByTestId("bill-due-date")).toHaveText("Fri, Nov 20");
      await expect(main.getByTestId("bill-due-when")).toContainText("in 2 days");

      await main.getByRole("button", { name: "Mark paid" }).click();
      const sheet = page.getByRole("dialog", { name: "Mark paid" });
      await expect(sheet).toBeVisible();

      // It says what the next date becomes BEFORE the person commits:
      // a quarterly bill due Nov 20 moves three months on.
      await expect(sheet).toContainText("moves to");
      await expect(sheet).toContainText("Feb 20, 2027");

      // Both due dates the MCP's bill_pay accepts are offered.
      const radios = sheet.getByRole("radio");
      await expect(radios).toHaveCount(2);
      await expect(radios.nth(0)).toContainText("Nov 20, 2026");
      await expect(radios.nth(1)).toContainText("Feb 20, 2027");
      await expect(sheet.getByText("The next one")).toBeVisible();
      await snap(page, testInfo, `mark-paid-sheet-${colorScheme}`);

      // Save: the payment columns go with the PUT, and the sheet's date input
      // never asks for a future "paid on".
      await sheet.getByRole("button", { name: "Mark paid" }).click();
      await expect.poll(() => writes.length).toBe(1);
      expect(writes[0].method).toBe("PUT");
      expect(writes[0].url).toContain("/bills/b-water");
      expect(writes[0].body).toMatchObject({ paid_date: expect.any(String), paid_for_due_date: "2026-11-20" });

      // This build's API has no payment field, so the honest outcome is that
      // the payment was not stored. The page says exactly that and never shows
      // a tick on a payment that did not happen.
      const result = main.getByTestId("paid-result");
      await expect(result).toBeVisible();
      await expect(result).toContainText("wasn't stored");
      await expect(result).not.toContainText("Marked paid.");
      await snap(page, testInfo, `mark-paid-not-stored-${colorScheme}`, true);
    });

    test("phone 390: housesitter and vendor get no money at all", async ({ loggedInPage: page }, testInfo: TestInfo) => {
      // The nav drawer is a phone surface; pin the width so this runs the same
      // way in desktop-chrome and on WebKit.
      await page.setViewportSize({ width: 390, height: 844 });
      for (const role of ["housesitter", "vendor"]) {
        await page.unrouteAll({ behavior: "ignoreErrors" });
        await stubBillsApi(page, { role });
        await page.goto("/dashboard/bills");
        const main = page.locator("main");
        await expect(main.getByTestId("money-no-access")).toBeVisible();
        await expect(main.getByTestId("money-no-access")).toContainText(/Money isn't shared/);
        // No amount reaches the page, and no bill row exists.
        await expect(main.locator("[data-bill-row]")).toHaveCount(0);
        await expect(main).not.toContainText("$164");
        await expect(main).not.toContainText("164.00");
        await expect(main.getByRole("button", { name: "Add" })).toHaveCount(0);
        await snap(page, testInfo, `bills-no-access-${role}-${colorScheme}`);
        // And no Money entry in the nav for that role.
        await page.getByRole("button", { name: "Open menu" }).click();
        const drawer = page.getByRole("dialog", { name: /menu/i });
        await expect(drawer).toBeVisible();
        await expect(drawer.getByRole("link", { name: "Money" })).toHaveCount(0);
        await page.keyboard.press("Escape");
      }
    });

    test("phone 390: the bill page itself refuses a role without money", async ({ loggedInPage: page }) => {
      await page.setViewportSize({ width: 390, height: 844 });
      await stubBillsApi(page, { role: "housesitter" });
      await page.goto("/dashboard/bills/b-water");
      await expect(page.locator("main").getByTestId("money-no-access")).toBeVisible();
      await expect(page.locator("main")).not.toContainText("186.00");
      await expectNoHorizontalOverflow(page);
    });
  });
}

test("phone 390: a viewer reads every bill and can write none", async ({ loggedInPage: page }, testInfo: TestInfo) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  await stubBillsApi(page, { role: "viewer" });
  await page.goto("/dashboard/bills");
  const main = page.locator("main");

  // Reads money…
  await expect(main.locator('[data-bill-row="b-water"]')).toContainText("$186.00");
  await expect(main.getByTestId("view-only")).toContainText("View only");
  // …and has no way to change it.
  await expect(main.getByRole("button", { name: "Add" })).toHaveCount(0);
  await snap(page, testInfo, "bills-viewer-390", true);

  await page.goto("/dashboard/bills/b-water");
  await expect(main.getByTestId("view-only-note")).toContainText("View only");
  await expect(main.getByRole("button", { name: "Mark paid" })).toHaveCount(0);
  await expect(main.getByRole("button", { name: "Edit", exact: true })).toHaveCount(0);
});

test("phone 390: an empty list explains what will appear here", async ({ loggedInPage: page }, testInfo: TestInfo) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  await stubBillsApi(page, { bills: [], dueSoon: { ...DUE_SOON, items: [], bill_count: 0, coverage: { items_listed: 0 } } });
  await page.goto("/dashboard/bills");
  const empty = page.locator("main").getByTestId("bills-empty");
  await expect(empty).toBeVisible();
  await expect(empty).toContainText("No bills yet");
  await expect(empty).toContainText("what they come to each month");
  await expect(empty).toContainText("how often it recurs");
  await expect(empty.getByRole("button", { name: "Add a bill" })).toBeVisible();
  await snap(page, testInfo, "bills-empty-390", true);
});

test("phone 390: adding a bill posts the monthly schedule and no due_date", async ({ loggedInPage: page }, testInfo: TestInfo) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  const writes = await stubBillsApi(page);
  await page.goto("/dashboard/bills");
  await page.locator("main").getByRole("button", { name: "Add" }).click();

  const sheet = page.getByRole("dialog", { name: "Add bill" });
  await expect(sheet).toBeVisible();
  await sheet.getByRole("textbox", { name: "Name" }).fill("Internet");
  await sheet.getByRole("textbox", { name: "Amount" }).fill("89.99");
  await sheet.getByRole("textbox", { name: "Due day" }).fill("12");
  await snap(page, testInfo, "add-bill-sheet-390");
  await sheet.getByRole("button", { name: "Save" }).click();

  await expect.poll(() => writes.length).toBe(1);
  const body = writes[0].body as Record<string, unknown>;
  expect(body).toMatchObject({ name: "Internet", amount: "89.99", frequency: "monthly", due_day: 12 });
  // Migration 044's rule: a monthly bill carries no due_date.
  expect(body).not.toHaveProperty("due_date");
});

test("phone 390: a bill's detail says who pays it, from which account, and how to reach the payee", async ({ loggedInPage: page }, testInfo: TestInfo) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  await stubBillsApi(page);
  await page.goto("/dashboard/bills/b-electric");
  const main = page.locator("main");

  // The API's answer, in the hero, with the sentence that explains it.
  const attention = main.getByTestId("bill-attention");
  await expect(attention).toContainText("Automatic");
  await expect(attention).toContainText("nothing to do");

  // The switch, and the account it draws on — name + last four only.
  const autopay = main.getByTestId("bill-autopay");
  await expect(autopay).toContainText("Pays itself");
  await expect(autopay.getByTestId("bill-autopay-account")).toContainText("Joint checking ····4242");
  await expect(autopay.getByTestId("bill-autopay-account")).toContainText("Riverside Credit Union");
  await expect(main).not.toContainText("4242424242424242");
  await expect(autopay.getByRole("switch", { name: /Autopay for Electric/ })).toBeChecked();

  // The payee card: who, how often, how much, and how to reach them.
  const payee = main.getByTestId("payee-card");
  await expect(payee).toContainText("Riverside Power");
  await expect(payee).toContainText("$164.00");
  await expect(payee.getByRole("link", { name: /Call Riverside Power/ })).toHaveAttribute("href", "tel:5550142");
  await expect(payee.getByRole("link", { name: /Email Riverside Power/ })).toHaveAttribute("href", "mailto:billing@riversidepower.example");
  await expect(payee.getByRole("link", { name: /Open Riverside Power's website/ })).toHaveAttribute("href", "https://riversidepower.example");
  // Every action is a 44 px target on a phone.
  for (const action of ["call", "email", "website"]) {
    const a = payee.locator(`[data-payee-action="${action}"]`);
    const b = (await a.boundingBox())!;
    expect(b.height).toBeGreaterThanOrEqual(43.5);
  }
  await expectNoHorizontalOverflow(page);
  await snap(page, testInfo, "bill-detail-automatic-390", true);
});

test("phone 390: turning autopay off sends the switch and the account goes with it", async ({ loggedInPage: page }, testInfo: TestInfo) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  const writes = await stubBillsApi(page);
  await page.goto("/dashboard/bills/b-electric");
  const main = page.locator("main");
  const autopay = main.getByTestId("bill-autopay");
  await expect(autopay.getByRole("switch", { name: /Autopay for Electric/ })).toBeChecked();
  // The label is the hit area (Mantine's Switch wraps input + words in one
  // <label>), so a tap on the words toggles it — the 44 px target the phone
  // needs. The input itself is visually hidden, as all Mantine switches are.
  await autopay.getByTestId("autopay-label").click();
  // The write carries the switch, and clears the account with it: the database
  // refuses an account on a bill that does not pay itself
  // (bills_autopay_account_check), so leaving it behind would be a 400.
  await expect.poll(() => writes.length).toBeGreaterThan(0);
  const put = writes.find((w) => w.method === "PUT")!;
  expect(put.body).toMatchObject({ is_autopay: false, autopay_account_id: null });
});

test("phone 390: a manual bill's detail says a person pays it, and offers no account", async ({ loggedInPage: page }, testInfo: TestInfo) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  await stubBillsApi(page);
  await page.goto("/dashboard/bills/b-water");
  const main = page.locator("main");
  await expect(main.getByTestId("bill-attention")).toContainText("Due soon");
  await expect(main.getByTestId("bill-attention")).toContainText("You pay this one");
  const autopay = main.getByTestId("bill-autopay");
  await expect(autopay.getByTestId("bill-autopay-account")).toContainText("No account on record");
  await expect(autopay.getByRole("switch", { name: /Autopay for Water/ })).not.toBeChecked();
  await expect(main.getByTestId("payee-card")).toContainText("Riverside Water Authority");
  await expectNoHorizontalOverflow(page);
  await snap(page, testInfo, "bill-detail-manual-390", true);
});

test("desktop 1440: the list and the detail use the wider layout", async ({ loggedInPage: page }, testInfo: TestInfo) => {
  onlyOn("desktop");
  await page.setViewportSize({ width: 1440, height: 900 });
  await stubBillsApi(page);
  await page.goto("/dashboard/bills");
  const main = page.locator("main");
  await expect(main.getByRole("heading", { level: 2, name: "Bills" })).toBeVisible();
  // The same reading at desktop width, in the mockup's table: paid is paid,
  // due is due, and the columns a wide screen has room for are there.
  await expect(main.getByTestId("bills-table")).toBeVisible();
  await expect(main.locator('[data-bill-table-row="b-electric"]')).toHaveAttribute("data-due-state", "paid");
  await expect(main.locator('[data-bill-table-row="b-water"]')).toContainText("Nov 20");
  // The attention column says who pays each one, in the table as on the phone.
  await expect(main.locator('[data-bill-table-row="b-electric"]')).toHaveAttribute("data-attention", "automatic");
  await expect(main.locator('[data-bill-table-row="b-water"]')).toHaveAttribute("data-attention", "due_soon");
  await expect(main.locator('[data-bill-table-row="b-electric"]')).toContainText("Joint checking ····4242");
  await expect(main.locator('[data-bill-table-row="b-water"]')).toContainText("in 2 days");
  // Earliest first in the table too.
  const tableOrder = await main
    .locator("[data-bill-table-row]")
    .evaluateAll((rows) => rows.map((r) => r.getAttribute("data-bill-table-row")));
  expect(tableOrder).toEqual(["b-water", "b-electric"]);
  await expectNoHorizontalOverflow(page);
  await snap(page, testInfo, "bills-1440");

  await page.goto("/dashboard/bills/b-water");
  // Desktop: the actions are buttons in the title bar, not a menu — and Edit
  // appears ONCE (the in-card copy is a phone-only affordance, as in the mockup).
  await expect(main.getByRole("button", { name: "Edit", exact: true })).toHaveCount(1);
  await expect(main.getByRole("button", { name: "Edit", exact: true })).toBeVisible();
  await expect(main.getByRole("button", { name: "Delete", exact: true })).toBeVisible();
  await expect(main.getByRole("button", { name: /^Actions for/ })).toHaveCount(0);
  // Two columns: the reading on the left, the last payment on the right.
  const fact = (await main.getByTestId("bill-next-due").boundingBox())!;
  const last = (await main.getByRole("region", { name: "Last payment" }).boundingBox())!;
  expect(last.x).toBeGreaterThan(fact.x + fact.width - 1);
  await expectNoHorizontalOverflow(page);
  await snap(page, testInfo, "bill-1440");
});
