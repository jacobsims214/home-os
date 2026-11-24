// The shared fixtures, not @playwright/test: `loggedInPage` is the
// stub-signed-in page every phone spec in this app starts from (#4510).
import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

/**
 * #5696 — the owner's two reports, verified at 390 px on the phone.
 *
 * WHAT WAS ACTUALLY WRONG, measured before this spec existed:
 *  - Pets was NOT missing: /dashboard/pets existed, its Add dialog worked, and
 *    `pet_create` was a live MCP tool. It sat in `people`'s `matches` in
 *    nav-config.ts, and a `matches` entry only decides which section a route
 *    HIGHLIGHTS. A grep for "/dashboard/pets" outside the pets pages found no
 *    link at all, so on every breakpoint the only way in was typing the URL —
 *    #5408's defect (Guides unreachable) one level lower.
 *  - The account/record distinction was NOT missing either: GET /people has
 *    returned `has_login` since the people API landed (people/handler.go:135),
 *    and it survives read_basic so a housesitter can tell a child from an
 *    adult. `PersonLike.has_login` was already typed. Nothing rendered it.
 *
 * Revert-check: removing the `pets` section from nav-config fails "a pet is two
 * taps from the phone's More tab"; removing the badge from the People card
 * fails "the People list says which people can sign in".
 */

const HOUSEHOLD = { id: "h1", name: "Sims family", timezone: "America/New_York", role: "owner" };
const ME = { id: "u-owner", name: "Jacob Sims", email: "jacob@example.test" };

const PEOPLE = [
  { id: "p1", display_name: "Jacob Sims", relationship: "self", birthdate: "1985-04-12", school: null, grade: null, allergies: [], notes: null, color: "cyan", user_id: "u-owner", has_login: true },
  { id: "p2", display_name: "Nora Sims", relationship: "daughter", birthdate: "2012-03-04", school: "Springfield Elementary", grade: "8th", allergies: ["peanuts (carries an EpiPen)"], notes: null, color: "pink", user_id: null, has_login: false },
  { id: "p3", display_name: "Rosa Alvarez", relationship: "nanny", birthdate: "1979-09-01", school: null, grade: null, allergies: [], notes: null, color: "teal", user_id: null, has_login: false },
];

const PETS = [{ id: "pet1", name: "Biscuit", species: "Dog", breed: "Golden retriever", date_of_birth: "2019-06-01", vet_vendor_id: null, vet_name: "Springfield Animal Hospital", vet_phone: "+15550100", notes: null }];

async function stub(page: Page, role = "owner") {
  await page.route("**/bff/me/households", (route) => route.fulfill({ json: { data: [{ ...HOUSEHOLD, role }] } }));
  await page.route("**/bff/auth/me", (route) => route.fulfill({ json: ME }));
  await page.route("**/bff/people", (route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      return route.fulfill({ status: 201, json: { data: { id: "p-new", ...body } } });
    }
    return route.fulfill({ json: { data: PEOPLE } });
  });
  await page.route("**/bff/people/*", (route) => route.fulfill({ json: { data: PEOPLE[1] } }));
  await page.route("**/bff/pets", (route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      return route.fulfill({ status: 201, json: { data: { id: "pet-new", ...body } } });
    }
    return route.fulfill({ json: { data: PETS } });
  });
  await page.route("**/bff/pets/*", (route) => route.fulfill({ json: { data: PETS[0] } }));
  await page.route("**/bff/bills**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/notifications**", (route) => route.fulfill({ json: { data: [], unread: 0 } }));
  await page.route("**/bff/lists**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/meals**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/events**", (route) => route.fulfill({ json: { data: [] } }));
}

test.use({ viewport: { width: 390, height: 844 } });

test.describe("#5696 people vs accounts, and Pets on the phone", () => {
  test("the People list says which people can sign in (#5696 A)", async ({ loggedInPage: page }) => {
    await stub(page);
    await page.goto("/dashboard/people");

    const list = page.getByRole("list", { name: "People" });
    await expect(list).toBeVisible();

    // Scoped to the phone list: the wide table is in the DOM at 390 px too
    // (hidden by CSS, not unmounted), so a page-wide count would see each
    // badge twice and prove nothing about the card.
    await expect(list.getByText("Signs in", { exact: true })).toHaveCount(1);
    await expect(list.getByText("No login", { exact: true })).toHaveCount(2);

    // The whole point of reading has_login rather than the relationship: the
    // nanny is an adult with a relationship and NO account.
    const nanny = list.getByRole("link", { name: /Rosa Alvarez/ });
    await expect(nanny.getByText("No login", { exact: true })).toBeVisible();
    const child = list.getByRole("link", { name: /Nora Sims/ });
    await expect(child.getByText("No login", { exact: true })).toBeVisible();
    await expect(child.getByText(/Allergic to peanuts/)).toBeVisible();
    const parent = list.getByRole("link", { name: /Jacob Sims/ });
    await expect(parent.getByText("Signs in", { exact: true })).toBeVisible();

    // The two words are defined on the page, not only coloured: the sentence
    // that answers "who here is a user?" before anyone taps a row.
    await expect(
      page.getByText(/means they have a login and a role/, { exact: false }).first(),
    ).toBeVisible();

    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  });

  test("a pet is two taps from the phone's More tab, and Add pet works (#5696 B)", async ({ loggedInPage: page }) => {
    await stub(page);
    await page.goto("/dashboard");

    await page.getByTestId("tab-bar").getByRole("button", { name: "More" }).click();
    const sheet = page.getByRole("dialog", { name: "More" });
    await expect(sheet).toBeVisible();

    const tile = sheet.getByRole("link", { name: /Pets/ });
    await expect(tile).toBeVisible();
    await expect(tile).toContainText("Vet, meds, feeding, appointments");
    expect((await tile.boundingBox())!.height).toBeGreaterThanOrEqual(44);

    await tile.click();
    await expect(page).toHaveURL(/\/dashboard\/pets$/);
    await expect(page.getByRole("link", { name: /Biscuit/ })).toBeVisible();

    const add = page.getByRole("button", { name: "Add pet" });
    await expect(add).toBeVisible();
    expect((await add.boundingBox())!.height).toBeGreaterThanOrEqual(44);
    await add.click();

    const dialog = page.getByRole("dialog", { name: "Add pet" });
    await expect(dialog).toBeVisible();
    await dialog.getByLabel("Name").fill("Pickles");
    const posted = page.waitForRequest((r) => r.url().includes("/bff/pets") && r.method() === "POST");
    await dialog.getByRole("button", { name: "Add pet" }).click();
    const req = await posted;
    expect(req.postDataJSON()).toMatchObject({ name: "Pickles" });
    await expect(page).toHaveURL(/\/dashboard\/pets\/pet-new$/);
  });

  test("a housesitter sees Pets (ResPets read) but not Money or Documents", async ({ loggedInPage: page }) => {
    await stub(page, "housesitter");
    await page.goto("/dashboard");
    await page.getByTestId("tab-bar").getByRole("button", { name: "More" }).click();
    const sheet = page.getByRole("dialog", { name: "More" });
    await expect(sheet.getByRole("link", { name: /Pets/ })).toBeVisible();
    await expect(sheet.getByText("Money", { exact: true })).toHaveCount(0);
    await expect(sheet.getByText("Documents", { exact: true })).toHaveCount(0);
  });

  test("role rules unchanged: a vendor has no Pets tile", async ({ loggedInPage: page }) => {
    await stub(page, "vendor");
    await page.goto("/dashboard");
    await page.getByTestId("tab-bar").getByRole("button", { name: "More" }).click();
    const sheet = page.getByRole("dialog", { name: "More" });
    await expect(sheet.getByRole("link", { name: /Pets/ })).toHaveCount(0);
    await expect(sheet.getByRole("link", { name: /People & House/ })).toBeVisible();
  });
});
