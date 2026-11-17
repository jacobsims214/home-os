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
 * The bill names on screen once the list has rendered. Polled: the page paints
 * its skeleton first, and an empty read would pass a "no old rows" assertion
 * that actually ran too early.
 */
async function billNames(page: Page): Promise<string[]> {
  await expect.poll(() => page.locator("main [data-entity-card] h3").allInnerTexts()).not.toEqual([]);
  return page.locator("main [data-entity-card] h3").allInnerTexts();
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
    await seedTwoFamilies(request, stubSid!);
    await gotoBills(page);

    // The Sims family's bills, on screen.
    expect((await billNames(page)).sort()).toEqual(["Duke Energy electric", "Internet"]);
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
    expect((await billNames(page)).sort()).toEqual(["Oak Street water"]);
    // And nothing from the old family is still cached behind the page.
    await expect(page.locator("main [data-entity-card]")).toHaveCount(1);

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

  test("the role is re-read per household: Money and Documents leave the nav for a housesitter", async ({
    loggedInPage: page,
    request,
    stubSid,
  }) => {
    // Owner in one family, housesitter in the other (the ticket's own example).
    await seedTwoFamilies(request, stubSid!, { sims: "owner", oak: "housesitter" });
    await gotoBills(page);

    // The drawer lists every section, so it shows the role-filtered nav in full.
    // Rows are counted by their label text: Documents has no page yet, so it
    // renders as a disabled span, not a link (#4536).
    const navInDrawer = async () => {
      await header(page).getByRole("button", { name: "Open menu" }).click();
      const drawer = page.getByRole("dialog", { name: /Home OS menu/ });
      await expect(drawer).toBeVisible();
      const row = (label: string) => drawer.getByText(label, { exact: true }).count();
      const counts = {
        money: await row("Money"),
        documents: await row("Documents"),
        people: await row("People & House"),
      };
      await page.keyboard.press("Escape");
      await expect(drawer).toBeHidden();
      return counts;
    };

    // Owner: Money and Documents are both reachable.
    expect(await navInDrawer()).toEqual({ money: 1, documents: 1, people: 1 });

    await (await openPicker(page)).getByRole("radio", { name: /Oak Street/ }).click();
    await expect(chip(page)).toContainText("Oak Street");
    await expect(page.locator("main")).toContainText("Oak Street water");

    // Housesitter: neither. The nav must never keep the previous family's role
    // on screen, so this is asserted as soon as the switch's data has landed.
    expect(await navInDrawer()).toEqual({ money: 0, documents: 0, people: 1 });

    // And it follows the household back: nothing is cached from the last role.
    await (await openPicker(page)).getByRole("radio", { name: /Sims family/ }).click();
    await expect(chip(page)).toContainText("Sims family");
    await expect(page.locator("main")).toContainText("Duke Energy electric");
    expect(await navInDrawer()).toEqual({ money: 1, documents: 1, people: 1 });
  });

  test("switching off a record page lands on that section's list, not on a 404 for the other family's row", async ({
    loggedInPage: page,
    request,
    stubSid,
  }) => {
    await seedTwoFamilies(request, stubSid!);
    await page.goto("/dashboard/bills/b1");
    await expect(page.locator("main")).toContainText("Duke Energy electric");

    await (await openPicker(page)).getByRole("radio", { name: /Oak Street/ }).click();
    await expect(page).toHaveURL(/\/dashboard\/bills$/);
    await expect(page.locator("main")).toContainText("Oak Street water");
    await expect(page.locator("main")).not.toContainText("Duke Energy electric");
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
