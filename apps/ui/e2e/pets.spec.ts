import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * Pets pages (#4556), built to the approved People & House mockup (artifact #89
 * v1: the Pets section and the pet screen) inside the approved shell (#75 v1),
 * on the card + DetailPageLayout patterns the properties (#4553), assets
 * (#4554) and vehicles (#4555) pages use.
 *
 * What this spec proves, and why each part is worth a test:
 *  1. MOBILE-FIRST. At 390 px the list is cards (one tappable link each, no
 *     clipping at 375 px) and the pet's page is the layout's sticky title bar
 *     with Details open; at 1440 px the list is the table and nothing scrolls
 *     sideways.
 *  2. THE DATE IS THE DATE. date_of_birth is a DATE, and
 *     new Date("2020-05-12") renders as May 11 in New York (the #4550 bug
 *     class). "May 12, 2020" is asserted literally from a stubbed value.
 *  3. THE VET IS THE CONTACT. Its name comes from the API's read-only
 *     vet_name; a save sends vet_vendor_id only and never vet_name /
 *     vet_phone (#4700, #4705). A pet with no vet says so and never invents a
 *     contact.
 *
 * The API is stubbed per test with page.route against /bff (the UI talks to
 * its own BFF), so each payload is exact. The clock is pinned so an age cannot
 * drift: the household's today is 2026-11-18 and Biscuit (born 2020-05-12) is
 * 6 years old.
 */

/** The mockup's Biscuit: a golden retriever with a vet and care notes. */
const BISCUIT = {
  id: "p1",
  household_id: "h1",
  name: "Biscuit",
  species: "Dog",
  breed: "Golden retriever",
  date_of_birth: "2020-05-12T00:00:00Z",
  vet_vendor_id: "v1",
  vet_name: "Springfield Animal Hospital",
  vet_phone: "555-0100",
  notes: "Allergic to chicken. Salmon kibble only, 1 cup at 7 AM and 6 PM. Apoquel with dinner, hidden in a bit of cheese.",
  created_at: "2020-06-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

/** Pepper: a cat with no vet chosen and no notes. */
const PEPPER = {
  id: "p2",
  household_id: "h1",
  name: "Pepper",
  species: "Cat",
  breed: null,
  date_of_birth: "2017-01-02T00:00:00Z",
  vet_vendor_id: null,
  vet_name: null,
  vet_phone: null,
  notes: null,
  created_at: "2017-02-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const VETS = [{ id: "v1", household_id: "h1", name: "Springfield Animal Hospital", phone: "555-0100", trades: ["vet"] }];

type Captured = { puts: Record<string, unknown>[]; posts: Record<string, unknown>[]; deletes: string[] };

/** Answers the reads these pages make, and records the writes. */
async function stubPetsApi(
  page: Page,
  opts: { pets?: unknown[]; pet?: Record<string, unknown>; role?: string } = {},
): Promise<Captured> {
  const writes: Captured = { puts: [], posts: [], deletes: [] };
  const role = opts.role ?? "owner";
  const pets = opts.pets ?? [BISCUIT, PEPPER];

  await page.route("**/bff/me/households*", (route) =>
    route.fulfill({
      json: {
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
      },
    }),
  );

  await page.route(/\/bff\/pets(\?.*)?$/, async (route) => {
    const req = route.request();
    if (req.method() === "POST") {
      const body = req.postDataJSON() as Record<string, unknown>;
      writes.posts.push(body);
      return route.fulfill({ status: 201, json: { data: { id: "p9", ...body } } });
    }
    return route.fulfill({ json: { data: pets } });
  });

  await page.route(/\/bff\/pets\/[^/?]+$/, async (route) => {
    const req = route.request();
    if (req.method() === "PUT") {
      const body = req.postDataJSON() as Record<string, unknown>;
      writes.puts.push(body);
      // What #4700's API returns: vet_name / vet_phone come from the contact.
      const vet = VETS.find((v) => v.id === body.vet_vendor_id);
      const { vet_name: _n, vet_phone: _p, ...rest } = body;
      return route.fulfill({
        json: { data: { ...(opts.pet ?? BISCUIT), ...rest, vet_name: vet?.name ?? null, vet_phone: vet?.phone ?? null } },
      });
    }
    if (req.method() === "DELETE") {
      writes.deletes.push(req.url());
      return route.fulfill({ status: 204, body: "" });
    }
    return route.fulfill({ json: { data: opts.pet ?? BISCUIT } });
  });

  // The vet picker's contact list, and the Resources section's own reads.
  await page.route(/\/bff\/vendors(\?.*)?$/, (route) => route.fulfill({ json: { data: VETS } }));
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

    test("phone 390: pets are cards with breed, age and vet; nothing clips", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.clock.setFixedTime(new Date("2026-11-18T15:00:00Z"));
      await page.setViewportSize({ width: 390, height: 844 });
      await stubPetsApi(page);
      await page.goto("/dashboard/pets");
      const main = page.locator("main");
      await expect(main.getByRole("heading", { level: 2, name: /Pets/ })).toBeVisible();

      const cards = main.locator("[data-pet-card]");
      await expect(cards).toHaveCount(2);
      await expect(main.locator("[data-pet-table]")).toBeHidden();

      // One tappable link per card, 44 px+, inside the viewport.
      for (let i = 0; i < 2; i++) {
        const box = (await cards.nth(i).boundingBox())!;
        expect(box.height).toBeGreaterThanOrEqual(43.5);
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width).toBeLessThanOrEqual(390);
      }

      // Biscuit: the mockup's line — breed and age, then the vet's name.
      const biscuit = cards.first();
      await expect(biscuit).toContainText("Biscuit");
      await expect(biscuit).toContainText("Golden retriever \u00b7 6 \u00b7 Springfield Animal Hospital");
      await expect(biscuit).toHaveAttribute("href", "/dashboard/pets/p1");

      // Pepper: the species stands in for a missing breed, and a pet with no
      // vet says nothing rather than inventing a contact.
      const pepper = cards.nth(1);
      await expect(pepper).toContainText("Cat \u00b7 9");
      await expect(pepper).toHaveAttribute("href", "/dashboard/pets/p2");

      // Token colours, so dark mode stays readable.
      const colours = await biscuit.evaluate((card) => {
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
      await snap(page, testInfo, `pets-390-${colorScheme}`, true);

      // Add opens the sheet, never a /new route (the #4509 audit bug).
      const add = main.getByRole("button", { name: "Add pet" });
      expect((await add.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);
      await add.click();
      const dialog = page.getByRole("dialog", { name: "Add pet" });
      await expect(dialog).toBeVisible();
      expect(page.url()).not.toContain("/new");
      await snap(page, testInfo, `pets-390-${colorScheme}-add`);
    });

    test("phone 390: the pet's page leads with care notes and the vet", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.clock.setFixedTime(new Date("2026-11-18T15:00:00Z"));
      await page.setViewportSize({ width: 390, height: 844 });
      await stubPetsApi(page);
      await page.goto("/dashboard/pets/p1");
      const main = page.locator("main");

      // The layout's title bar: back to the list, the name as an h2, actions.
      await expect(main.getByRole("heading", { level: 2, name: "Biscuit" })).toBeVisible();
      const back = main.getByRole("link", { name: "Back to Pets" });
      await expect(back).toHaveAttribute("href", "/dashboard/pets");
      expect((await back.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);
      const actions = main.getByRole("button", { name: "Actions for Biscuit" });
      expect((await actions.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);

      // The head: the paw, the name, breed and age. The vet is the Vet
      // section's job, not the head's.
      const head = main.locator("[data-pet-head]");
      await expect(head).toBeVisible();
      await expect(head).toContainText("Golden retriever \u00b7 6");
      await expect(head).not.toContainText("Springfield Animal Hospital");

      // Care notes, above everything, with the warning in words (never colour alone).
      const notes = main.locator("[data-pet-notes]");
      await expect(notes).toBeVisible();
      await expect(notes).toContainText("Allergic to chicken.");
      await expect(notes).toContainText("Salmon kibble only");
      const warning = notes.locator("p", { hasText: "Allergic to chicken." });
      const colours = await warning.evaluate((el) => {
        const probe = (v: string) => {
          const probeEl = document.createElement("span");
          probeEl.style.color = `var(${v})`;
          document.body.appendChild(probeEl);
          const c = getComputedStyle(probeEl).color;
          probeEl.remove();
          return c;
        };
        return { text: getComputedStyle(el).color, danger: probe("--danger") };
      });
      expect(colours.text).toBe(colours.danger);

      // The vet, and one tap to call.
      const vetRow = main.locator('[data-field="vet_vendor_id"]');
      await expect(vetRow).toContainText("Springfield Animal Hospital");
      const call = vetRow.getByRole("link", { name: /Call Springfield Animal Hospital/ });
      await expect(call).toHaveAttribute("href", "tel:5550100");
      expect((await call.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);

      // The birthday is the date the API sent, never a day earlier (#4550).
      await expect(main.locator('[data-field="date_of_birth"] dd')).toContainText("May 12, 2020");

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `pet-390-${colorScheme}`, true);
    });

    test("phone 390: editing the vet saves vet_vendor_id only, never the contact's name or phone", async ({
      loggedInPage: page,
    }, testInfo) => {
      onlyOn("phone");
      await page.clock.setFixedTime(new Date("2026-11-18T15:00:00Z"));
      await page.setViewportSize({ width: 390, height: 844 });
      const writes = await stubPetsApi(page);
      await page.goto("/dashboard/pets/p1");
      const main = page.locator("main");
      await expect(main.getByRole("heading", { level: 2, name: "Biscuit" })).toBeVisible();

      await main.getByRole("button", { name: "Actions for Biscuit" }).click();
      await page.getByRole("menuitem", { name: "Edit" }).click();

      // The form: the pet's own fields and the vet picker, 44 px targets.
      const name = main.getByRole("textbox", { name: "Name" });
      await expect(name).toHaveValue("Biscuit");
      expect((await name.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);
      const picker = main.locator("[data-vet-picker]");
      await expect(picker).toContainText("Springfield Animal Hospital");
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `pet-390-${colorScheme}-edit`);

      // Clear the breed and save: the PUT carries the owned fields and the vet id.
      await main.getByRole("textbox", { name: "Breed" }).fill("");
      await main.getByRole("button", { name: "Save" }).click();
      await expect.poll(() => writes.puts.length).toBe(1);
      const body = writes.puts[0];
      expect(body.name).toBe("Biscuit");
      expect(body.breed).toBeNull();
      expect(body.vet_vendor_id).toBe("v1");
      expect(body).not.toHaveProperty("vet_name");
      expect(body).not.toHaveProperty("vet_phone");
      // A date-only birthday, still the same day.
      expect(body.date_of_birth).toBe("2020-05-12");
    });

    test("phone 390: a pet with no vet says so rather than inventing one", async ({ loggedInPage: page }) => {
      onlyOn("phone");
      await page.clock.setFixedTime(new Date("2026-11-18T15:00:00Z"));
      await page.setViewportSize({ width: 390, height: 844 });
      await stubPetsApi(page, { pet: PEPPER });
      await page.goto("/dashboard/pets/p2");
      const main = page.locator("main");
      await expect(main.getByRole("heading", { level: 2, name: "Pepper" })).toBeVisible();
      await expect(main.locator('[data-field="vet_vendor_id"]')).toContainText("No vet chosen");
      await expect(main.getByRole("link", { name: /^Call/ })).toHaveCount(0);
      await expect(main.locator("[data-pet-notes]")).toHaveCount(0);
      await expect(main.getByText("No care notes yet", { exact: false })).toBeVisible();
    });

    test("1440: the list is a table and the pet's page is two columns", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("desktop");
      await page.clock.setFixedTime(new Date("2026-11-18T15:00:00Z"));
      await page.setViewportSize({ width: 1440, height: 900 });
      await stubPetsApi(page);
      await page.goto("/dashboard/pets");
      const main = page.locator("main");
      await expect(main.getByRole("heading", { level: 2, name: /Pets/ })).toBeVisible();

      // The table, not the cards.
      const table = main.locator("[data-pet-table]");
      await expect(table).toBeVisible();
      await expect(main.locator("[data-pet-card]").first()).toBeHidden();
      await expect(table).toContainText("Springfield Animal Hospital");
      await expect(table).toContainText("Golden retriever");
      // The first cell is a real link, so keyboard and middle-click work.
      await expect(table.getByRole("link", { name: "Biscuit" })).toHaveAttribute("href", "/dashboard/pets/p1");
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `pets-1440-${colorScheme}`, true);

      await page.goto("/dashboard/pets/p1");
      await expect(main.getByRole("heading", { level: 2, name: "Biscuit" })).toBeVisible();
      // Two columns from 1024 px: the detail sheet and Resources side by side,
      // so the Resources heading is a real h2 on the right rather than a toggle.
      await expect(main.getByRole("button", { name: /^Resources/ })).toBeHidden();
      // The phone-only affordances are gone; Edit is a real button.
      await expect(main.getByRole("link", { name: "Back to Pets" })).toBeVisible();
      await expect(main.getByRole("button", { name: "Edit", exact: true })).toBeVisible();
      await expect(main.locator("[data-pet-notes]")).toBeVisible();
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `pet-1440-${colorScheme}`, true);
    });
  });
}

test("375: adding a pet posts a name and no vet, and opens the new pet", async ({ loggedInPage: page }) => {
  onlyOn("phone");
  await page.clock.setFixedTime(new Date("2026-11-18T15:00:00Z"));
  await page.setViewportSize({ width: 375, height: 812 });
  const writes = await stubPetsApi(page);
  await page.goto("/dashboard/pets");
  const main = page.locator("main");
  await main.getByRole("button", { name: "Add pet" }).click();

  const dialog = page.getByRole("dialog", { name: "Add pet" });
  await expect(dialog).toBeVisible();

  // A name is required; nothing is sent without one.
  await dialog.getByRole("button", { name: "Add pet" }).click();
  await expect(dialog.getByText("A name is required")).toBeVisible();
  expect(writes.posts).toHaveLength(0);

  // No horizontal overflow with the sheet open at the smallest phone width.
  await expectNoHorizontalOverflow(page);

  await dialog.getByRole("textbox", { name: "Name" }).fill("Nugget");
  await dialog.getByRole("textbox", { name: "Species" }).fill("Cat");
  await dialog.getByRole("button", { name: "Add pet" }).click();
  await expect.poll(() => writes.posts.length).toBe(1);
  // Only what was filled: no empty strings, and no vet (that is the pet page's).
  expect(writes.posts[0]).toEqual({ name: "Nugget", species: "Cat" });
  await expect(page).toHaveURL(/\/dashboard\/pets\/p9$/);
});
