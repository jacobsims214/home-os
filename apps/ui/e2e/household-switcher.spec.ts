import type { Locator, Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import {
  test,
  expect,
  expectNoHorizontalOverflow,
  onlyOn,
  pressOpener,
  seedHouseholds,
} from "./fixtures";

/**
 * Family switcher (#4574). A person can belong to several households, and the
 * point of this ticket is the security-relevant half: switching must change what
 * the API returns AND must never leave the previous family's rows on screen, and
 * the role has to be re-read per household (owner here, housesitter there).
 *
 * The stub is household-aware per session (POST /e2e/households), so these specs
 * drive the real BFF: the assertion on /e2e/stats' householdHeaders is the API's
 * view — every request after a switch carried the new X-Household-ID.
 *
 * Phone width only (390 px, the layout this ticket is mobile-first for), in
 * desktop-chrome and on iphone-390 (WebKit, touch), like the other shell specs.
 */
const SIMS = "0b9d4f1e-6a3c-4b8e-9f52-1c7d2a5e8b10";
const OAK = "f3a7c2d1-8e4b-4c9f-a1d6-5b2e7c9f0a34";

const SIMS_BILLS = [
  { id: "b1", name: "Duke Energy electric", amount: "182.40", due_day: 12, category: "Utilities" },
  { id: "b2", name: "Internet", amount: "70", due_day: 3, category: "Utilities" },
];
const OAK_BILLS = [{ id: "b3", name: "Oak Street water", amount: "41.10", due_day: 9, category: "Utilities" }];

type Stats = { householdHeaders: Array<{ sid: string | null; path: string; household: string | null }> };
const stubUrl = `http://127.0.0.1:${process.env.E2E_STUB_PORT || 3901}`;

/** Gives the signed-in stub session the two families this ticket is about. */
async function seedTwoFamilies(
  request: Parameters<typeof seedHouseholds>[0],
  sid: string,
  roles = { sims: "owner", oak: "housesitter" },
) {
  await seedHouseholds(request, sid, [
    { id: SIMS, name: "Sims family", role: roles.sims, bills: SIMS_BILLS },
    { id: OAK, name: "Oak Street", role: roles.oak, bills: OAK_BILLS },
  ]);
}

/** What the API saw for THIS session (the stub is shared by every worker). */
async function householdHeaders(request: Parameters<typeof seedHouseholds>[0], sid: string, path: string) {
  const res = await request.get(`${stubUrl}/e2e/stats`);
  const body = (await res.json()) as Stats;
  return body.householdHeaders.filter((h) => h.sid === sid && h.path === path);
}

const header = (page: Page) => page.locator("header");
const chip = (page: Page) => header(page).getByRole("button", { name: /^Family:/ });
const tabBar = (page: Page) => page.getByTestId("tab-bar");

/**
 * The picker, whichever surface the width uses (390 px is the sheet). Addressed
 * by its dialog role, not by a testid on Mantine's Drawer root: that root is a
 * zero-size wrapper whose children are fixed-position, so Playwright reads it as
 * hidden even while the sheet is on screen.
 */
const picker = (page: Page): Locator => page.getByRole("dialog", { name: "Switch family" });

async function openPicker(page: Page) {
  await pressOpener(chip(page));
  const sheet = picker(page);
  await expect(sheet).toBeVisible();
  return sheet;
}

async function gotoBills(page: Page) {
  await page.goto("/dashboard/bills");
  await expect(page.locator("main").getByRole("heading", { level: 2, name: "Bills" })).toBeVisible();
}

/**
 * The bill rows on screen once the list has rendered, by the id each row
 * carries (`data-bill-row`, #4551's rebuilt bills list).
 *
 * This used to read `[data-entity-card] h3`, which the rebuilt page never
 * renders — `data-entity-card` is EntityList's, and EntityList is orphaned — so
 * the selector matched nothing and every wait below timed out.
 *
 * Polled: the page paints its skeleton first, and an empty read would pass a
 * "no old rows" assertion that actually ran too early.
 */
async function billRowIds(page: Page): Promise<string[]> {
  await expect.poll(() => page.locator("main [data-bill-row]").count()).toBeGreaterThan(0);
  return page
    .locator("main [data-bill-row]")
    .evaluateAll((rows) => rows.map((r) => r.getAttribute("data-bill-row")!));
}

/**
 * A role with no money read gets the no-access card and no rows at all.
 *
 * This is the CORRECT product behaviour for a housesitter, not a gap: the API
 * answers 403 on every money route for that role
 * (decisions/2026-11-15-role-policy.md, auth.CanSeeMoney #4806), so the page
 * must not even ask. The money isolation is deliberate and this spec asserts it
 * rather than working around it.
 */
async function expectMoneyNoAccess(page: Page) {
  const card = page.locator('main [data-testid="money-no-access"]');
  await expect(card).toBeVisible();
  await expect(card.getByRole("heading", { name: "Money isn't shared with housesitters" })).toBeVisible();
  await expect(page.locator("main [data-bill-row]")).toHaveCount(0);
}

test.describe("family switcher, phone 390", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test.beforeEach(() => onlyOn("phone"));

  test("the header names the active family and opens the picker with both families and the role in each", async ({
    loggedInPage: page,
    request,
    stubSid,
  }, testInfo) => {
    await seedTwoFamilies(request, stubSid!);
    await gotoBills(page);

    await expect(chip(page)).toContainText("Sims family");
    const sheet = await openPicker(page);

    const group = sheet.getByRole("radiogroup", { name: "Your families" });
    const rows = group.getByRole("radio");
    await expect(rows).toHaveCount(2);
    // The role is per household, and the selected row says so in words as well
    // as colour.
    await expect(rows.filter({ hasText: "Sims family" })).toHaveAttribute("aria-checked", "true");
    await expect(rows.filter({ hasText: "Sims family" })).toContainText(/You're here · Owner/);
    await expect(rows.filter({ hasText: "Oak Street" })).toHaveAttribute("aria-checked", "false");
    await expect(rows.filter({ hasText: "Oak Street" })).toContainText(/Housesitter/);

    // 44 px+ rows and nothing clipped sideways on a 390 px screen.
    for (const row of await rows.all()) {
      expect((await row.boundingBox())!.height).toBeGreaterThanOrEqual(63.5);
    }
    await expectNoHorizontalOverflow(page);
    await page.screenshot({ path: testInfo.outputPath("switcher-390-open.png") });
    await testInfo.attach("switcher-390-open", { path: testInfo.outputPath("switcher-390-open.png"), contentType: "image/png" });

    // Esc closes it and focus returns to the chip.
    await page.keyboard.press("Escape");
    await expect(sheet).toBeHidden();
    await expect(chip(page)).toBeFocused();
  });

  test("switching changes the visible data, and no previous-household row survives", async ({ loggedInPage: page, request, stubSid }, testInfo) => {
    // Viewer in Oak Street, not housesitter: this test is about the DATA
    // changing with the household, so both sides need a money read. A viewer is
    // the role that reads money and cannot write it
    // (decisions/2026-11-15-role-policy.md), so the page renders and the rows
    // are the other family's. The housesitter case — no money at all — is
    // asserted in the role test below, and it is the product's deliberate
    // behaviour, not a gap this test may paper over.
    await seedTwoFamilies(request, stubSid!, { sims: "owner", oak: "viewer" });
    await gotoBills(page);

    // The Sims family's bills, on screen.
    expect((await billRowIds(page)).sort()).toEqual(["b1", "b2"]);
    await expect(page.locator("main")).toContainText("Duke Energy electric");

    const sheet = await openPicker(page);
    await sheet.getByRole("radio", { name: /Oak Street/ }).click();

    // The sheet closes on success and the header names the new family.
    await expect(sheet).toBeHidden();
    await expect(chip(page)).toContainText("Oak Street");

    // The other family's bills, and NOT one row from the previous household.
    await expect(page.locator("main")).toContainText("Oak Street water");
    await expect(page.locator("main")).not.toContainText("Duke Energy electric");
    await expect(page.locator("main")).not.toContainText("Internet");
    expect((await billRowIds(page)).sort()).toEqual(["b3"]);
    // And nothing from the old family is still cached behind the page: exactly
    // one row, the one bill Oak Street has.
    await expect(page.locator("main [data-bill-row]")).toHaveCount(1);
    await expect(page.locator('main [data-bill-row="b1"], main [data-bill-row="b2"]')).toHaveCount(0);

    // The API's own view: the request that fetched what is now on screen carried
    // the Oak Street id — this is the half a screenshot cannot show.
    const billsCalls = await householdHeaders(request, stubSid!, "bills");
    // Before anyone switches, no household is chosen: the BFF sends no header
    // and the API falls back to the default (oldest) membership — the Sims
    // family, which is what was on screen.
    expect(billsCalls[0].household).toBeNull();
    // After the switch, every bills request carried Oak Street's id.
    expect(billsCalls.at(-1)!.household).toBe(OAK);
    expect(billsCalls.filter((c) => c.household === OAK).length).toBeGreaterThanOrEqual(1);

    await page.screenshot({ path: testInfo.outputPath("switcher-390-after-switch.png") });
    await testInfo.attach("switcher-390-after-switch", { path: testInfo.outputPath("switcher-390-after-switch.png"), contentType: "image/png" });

    // It survives a reload (the choice is the cookie, not component state).
    await page.reload();
    await expect(page.locator("main")).toContainText("Oak Street water");
    await expect(page.locator("main")).not.toContainText("Duke Energy electric");
    await expect(chip(page)).toContainText("Oak Street");

    // And back again: the first family returns, with its own rows only.
    await (await openPicker(page)).getByRole("radio", { name: /Sims family/ }).click();
    await expect(page.locator("main")).toContainText("Duke Energy electric");
    await expect(page.locator("main")).not.toContainText("Oak Street water");
  });

  test("the role is re-read per household: Money leaves the nav for a housesitter", async ({
    loggedInPage: page,
    request,
    stubSid,
  }) => {
    // Owner in one family, housesitter in the other (the ticket's own example).
    await seedTwoFamilies(request, stubSid!, { sims: "owner", oak: "housesitter" });
    await gotoBills(page);

    // The drawer lists every section, so it shows the role-filtered nav in full.
    // Every row is a link now (#5199): a section with no page is not in the nav.
    const navInDrawer = async () => {
      await header(page).getByRole("button", { name: "Open menu" }).click();
      const drawer = page.getByRole("dialog", { name: /Home OS menu/ });
      await expect(drawer).toBeVisible();
      const row = (label: string) => drawer.getByText(label, { exact: true }).count();
      const counts = {
        money: await row("Money"),
        documents: await row("Documents"),
        people: await row("People & House"),
        calendar: await row("Calendar"),
      };
      await page.keyboard.press("Escape");
      await expect(drawer).toBeHidden();
      return counts;
    };

    // Owner: money, documents, people and calendar are all reachable.
    expect(await navInDrawer()).toEqual({ money: 1, documents: 1, people: 1, calendar: 1 });

    await (await openPicker(page)).getByRole("radio", { name: /Oak Street/ }).click();
    await expect(chip(page)).toContainText("Oak Street");

    // And the page itself: a housesitter gets no money AT ALL, so the bills page
    // says so instead of listing the family's bills. This is the correct
    // behaviour since the role policy (#4806): the API answers 403 on every
    // money route for a sitter, so the page must not even ask. Asserting the
    // family's bills here would be asserting money isolation away.
    await expectMoneyNoAccess(page);
    await expect(page.locator("main")).not.toContainText("Oak Street water");

    // Housesitter: no money and no documents (#4569 put Documents in the nav
    // for readers; files read is family + viewer, so a sitter loses both).
    // The nav must never keep the previous family's role on screen, so this is
    // asserted as soon as the switch's data has landed.
    expect(await navInDrawer()).toEqual({ money: 0, documents: 0, people: 1, calendar: 1 });

    // And it follows the household back: nothing is cached from the last role.
    await (await openPicker(page)).getByRole("radio", { name: /Sims family/ }).click();
    await expect(chip(page)).toContainText("Sims family");
    await expect(page.locator("main")).toContainText("Duke Energy electric");
    expect(await navInDrawer()).toEqual({ money: 1, documents: 1, people: 1, calendar: 1 });
  });

  test("switching off a record page lands on that section's list, not on a 404 for the other family's row", async ({
    loggedInPage: page,
    request,
    stubSid,
  }) => {
    // A viewer, so the landing page has rows to look at: the point of this test
    // is that the URL becomes the SECTION's list for the new household (its own
    // rows), never a 404 for the old household's record id.
    await seedTwoFamilies(request, stubSid!, { sims: "owner", oak: "viewer" });
    await page.goto("/dashboard/bills/b1");
    await expect(page.locator("main")).toContainText("Duke Energy electric");

    await (await openPicker(page)).getByRole("radio", { name: /Oak Street/ }).click();
    await expect(page).toHaveURL(/\/dashboard\/bills$/);
    await expect(page.locator("main")).toContainText("Oak Street water");
    await expect(page.locator("main")).not.toContainText("Duke Energy electric");
    // The old family's record is not on screen and its id is not in the URL.
    await expect(page.locator("main [data-bill-row]")).toHaveCount(1);
    await expect(page.locator('main [data-bill-row="b1"]')).toHaveCount(0);
  });

  test("the shell is reachable from the More sheet as well, and the picker is accessible", async ({ loggedInPage: page, request, stubSid }, testInfo) => {
    await seedTwoFamilies(request, stubSid!);
    await gotoBills(page);

    // The thumb path: More tab → Switch family row → picker.
    const nav = tabBar(page);
    await nav.getByRole("button", { name: "More" }).click();
    const more = page.getByRole("dialog", { name: "More" });
    await expect(more).toBeVisible();
    await expect(more.getByRole("button", { name: /^Family: .*Switch family/ })).toBeVisible();
    await more.getByRole("button", { name: /^Family: .*Switch family/ }).click();
    await expect(more).toBeHidden();
    const sheet = picker(page);
    await expect(sheet).toBeVisible();

    // axe on the open picker: a radiogroup with named radios and no violations.
    const results = await new AxeBuilder({ page }).include('[data-testid="household-switcher-panel"]').analyze();
    expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
    await expectNoHorizontalOverflow(page);
  });

  test("a single-family account gets no switcher: the chip is a label, the More row goes to Members", async ({
    loggedInPage: page,
  }) => {
    // The default stub session: one seed household, no POST /e2e/households.
    await page.goto("/dashboard/bills");
    await expect(page.locator("main").getByRole("heading", { level: 2, name: "Bills" })).toBeVisible();

    // The name is still there (where am I), but there is nothing to open.
    const c = header(page).getByText("Your family", { exact: true });
    await expect(c).toBeVisible();
    await expect(header(page).getByRole("button", { name: /^Family:/ })).toHaveCount(0);

    await tabBar(page).getByRole("button", { name: "More" }).click();
    const more = page.getByRole("dialog", { name: "More" });
    await expect(more).toBeVisible();
    await expect(more.getByRole("button", { name: /Switch family/ })).toHaveCount(0);
    await expect(more.getByRole("link", { name: /^Family members/ })).toBeVisible();
  });
});
