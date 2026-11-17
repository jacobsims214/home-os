import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn, pressOpener } from "./fixtures";

/**
 * Recipes (#4567): the recipe box, one recipe with cook mode, and create/edit
 * with structured ingredients.
 *
 * Built to the approved mockup (artifact #106 v3) and the manager decisions on
 * #4519. Viewports and colour schemes are pinned per test (desktop-chrome, and
 * phone widths on iphone-390 too, per #4757); screenshots are attached for the
 * visual check.
 *
 * The API is stubbed per test with page.route, so each allergy payload is
 * exact. Three of these tests exist for ONE reason — #4874:
 *   - a `checks` line must be VISIBLE (never collapsed, never reworded);
 *   - a recipe with NO allergy data must NOT read as safe when the family has
 *     allergies saved;
 *   - "nothing matched" may only be claimed when the server actually said so.
 * If someone later "simplifies" the banner to `warnings.length === 0 && "safe"`,
 * those tests fail, which is the point.
 */

const PEOPLE_WITH_ALLERGY = [
  { id: "p1", household_id: "h1", display_name: "Nora", allergies: ["peanuts"] },
  { id: "p2", household_id: "h1", display_name: "Theo", allergies: [] },
];
const PEOPLE_NO_ALLERGY = [{ id: "p2", household_id: "h1", display_name: "Theo", allergies: [] }];

const UNCHECKED_LINE =
  'Nora has an allergy saved as "deathly peanuts" that could not be checked against recipes; confirm the meal is safe.';
const NO_INGREDIENTS_LINE = "no ingredients saved, can't check allergies";

const RECIPE = {
  id: "r1",
  household_id: "h1",
  title: "Sheet-pan chicken fajitas",
  description: "Let everyone build their own.",
  servings: 5,
  prep_minutes: 10,
  cook_minutes: 25,
  ingredients: [
    { name: "chicken thighs", quantity: 1.5, unit: "lb", section: "Meat" },
    { name: "bell peppers", quantity: 3, unit: null, section: "Produce" },
    { name: "olive oil", quantity: 3, unit: "tbsp", section: null },
  ],
  steps: "Heat the oven to 425°F.\nToss everything on a sheet pan.\nRoast 30 min, stirring once.",
  tags: ["weeknight"],
  source_url: null,
  created_at: "2026-11-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z",
};

/** A recipe with a real allergen hit (the server checked and found one). */
const RECIPE_WARNED = {
  ...RECIPE,
  id: "r2",
  title: "Satay noodles",
  allergy_warnings: [{ person: "Nora", person_id: "p1", allergy: "peanuts", ingredient: "peanut sauce" }],
  checks: [],
};

const LIST = [
  { id: "r1", household_id: "h1", title: RECIPE.title, ingredients: RECIPE.ingredients, tags: ["weeknight"], prep_minutes: 10, cook_minutes: 25, servings: 5 },
  {
    id: "r2",
    household_id: "h1",
    // The long-title case from the mockup: it must wrap, not push the row sideways.
    title: "Grandma Ruth's Sunday sauce with meatballs, the long version she dictated over the phone in 2019",
    ingredients: [{ name: "ground beef" }],
    tags: ["weekend"],
    prep_minutes: 45,
    cook_minutes: 150,
    servings: 6,
  },
  { id: "r3", household_id: "h1", title: "Peanut brittle", ingredients: [], tags: [], prep_minutes: null, cook_minutes: null, servings: null },
];

type Routes = {
  people?: unknown[];
  list?: unknown[];
  recipe?: Record<string, unknown>;
  plans?: unknown[];
  /** Records every write the page made. */
  writes?: Array<{ method: string; url: string; body: unknown }>;
};

async function stubApi(page: Page, routes: Routes = {}) {
  const writes = routes.writes ?? [];
  await page.route("**/bff/me/households", (route) =>
    route.fulfill({
      json: {
        data: [
          { id: "h1", name: "Sims", timezone: "UTC", role: "owner", joined_at: "2026-01-01T00:00:00Z", is_default: true, current: true },
        ],
      },
    }),
  );
  await page.route("**/bff/people", (route) => route.fulfill({ json: { data: routes.people ?? PEOPLE_WITH_ALLERGY } }));
  await page.route("**/bff/meal-plans**", (route) =>
    route.fulfill({ json: { data: routes.plans ?? [], from: "2026-11-16", to: "2027-01-15" } }),
  );
  await page.route("**/bff/recipes**", async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname;
    if (req.method() !== "GET") {
      const body = req.postDataJSON();
      writes.push({ method: req.method(), url: path, body });
      const saved = { ...RECIPE, ...body, id: "r-new" };
      return route.fulfill({ status: req.method() === "POST" ? 201 : 200, json: { data: saved } });
    }
    // /bff/recipes/r1 -> one recipe
    const id = path.replace(/^.*\/recipes\/?/, "");
    if (id) {
      const one = routes.recipe ?? RECIPE;
      return route.fulfill({ json: { data: { ...one, id } } });
    }
    return route.fulfill({ json: { data: routes.list ?? LIST } });
  });
  return writes;
}

async function gotoRecipes(page: Page) {
  await page.goto("/dashboard/recipes");
  await expect(page.getByRole("heading", { level: 2, name: "Recipes" })).toBeVisible();
}

async function gotoRecipe(page: Page, id = "r1") {
  await page.goto(`/dashboard/recipes/${id}`);
  await expect(page.locator("main").getByRole("heading", { level: 2 }).first()).toBeVisible();
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = true) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme}`, () => {
    test.use({ colorScheme });

    test("phone 390: recipe list is cards, no clipping, long titles wrap", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubApi(page);
      await gotoRecipes(page);

      const cards = page.locator("[data-recipe-card]");
      await expect(cards).toHaveCount(3);
      // Every card is a link with a 44 px+ tap target.
      for (let i = 0; i < 3; i += 1) {
        const box = (await cards.nth(i).boundingBox())!;
        expect(Math.round(box.height)).toBeGreaterThanOrEqual(44);
      }
      // The long title is present in full (it wraps rather than truncating).
      await expect(page.getByText(/Grandma Ruth's Sunday sauce with meatballs/)).toBeVisible();
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, "recipes-list-phone");
    });

    test("phone 390: add a recipe with structured ingredients (paste parses to rows)", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      const writes = await stubApi(page);
      await gotoRecipes(page);

      await page.getByRole("button", { name: "Add recipe" }).first().click();
      const dialog = page.getByRole("dialog", { name: "Add a recipe" });
      await expect(dialog).toBeVisible();

      await dialog.getByLabel("Name").fill("Sheet-pan sausage & peppers");
      // The paste box is the primary path: it must parse the mockup's example.
      // (Mantine's SegmentedControl hides the radio input itself, so the label
      // is the click target.)
      await dialog.getByText("Paste a list", { exact: true }).click();
      await dialog
        .getByLabel("Paste the ingredients, one per line")
        .fill("1½ lb Italian sausage\n3 bell peppers, sliced\n2 tbsp olive oil");
      await expect(dialog.locator("[data-import-preview]")).toContainText("Will save 3 ingredients");
      await expect(dialog.locator("[data-import-preview]")).toContainText("Italian sausage");

      await dialog.getByLabel("Steps").fill("1. Heat the oven to 425°F.\n2. Toss everything on a sheet pan.");
      await expect(dialog.locator("[data-step-count]")).toContainText("2 steps");

      await snap(page, testInfo, "recipes-add-phone");
      await dialog.getByRole("button", { name: "Add recipe" }).click();

      // The POST carried STRUCTURED ingredients, not the raw lines.
      await expect.poll(() => writes.length).toBeGreaterThan(0);
      const post = writes.find((w) => w.method === "POST")!;
      const body = post.body as { title: string; ingredients: Array<{ name: string; quantity?: unknown; unit?: string }> };
      expect(body.title).toBe("Sheet-pan sausage & peppers");
      // The preparation note ("sliced") is kept in the name: the API's
      // ingredient shape has no note column (047: name/quantity/unit/section),
      // so folding it in is how "sliced" survives instead of being dropped.
      expect(body.ingredients).toEqual([
        { name: "Italian sausage", quantity: 1.5, unit: "lb" },
        { name: "bell peppers (sliced)", quantity: 3 },
        { name: "olive oil", quantity: 2, unit: "tbsp" },
      ]);
    });

    test("phone 390: cook mode is full-screen, readable, and steps check off", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubApi(page);
      await gotoRecipe(page);

      await page.getByRole("button", { name: "Cook mode" }).click();
      const cook = page.locator("[data-cook-mode]");
      await expect(cook).toBeVisible();

      // Full screen: it covers the viewport (the tab bar included).
      const box = (await cook.boundingBox())!;
      expect(Math.round(box.width)).toBe(390);
      expect(Math.round(box.height)).toBeGreaterThanOrEqual(840);

      // Readability: step text is 17 px (the mockup's .cstep p), not 14.
      const step = page.locator('[data-cook-step="0"]');
      const fontSize = await step.locator("span").nth(1).evaluate((el) => getComputedStyle(el).fontSize);
      expect(fontSize).toBe("17px");

      // The first step is the current one; tapping it marks it done and the
      // counter advances.
      await expect(page.locator("[data-cook-counter]")).toHaveText("Step 1 of 3");
      await expect(step).toHaveAttribute("data-step-state", "now");
      await step.click();
      await expect(step).toHaveAttribute("aria-checked", "true");
      await expect(page.locator("[data-cook-counter]")).toHaveText("Step 2 of 3");

      // Ingredients are collapsible and closed by default (steps get the screen).
      const toggle = page.locator("[data-cook-ingredients-toggle]");
      await expect(toggle).toHaveAttribute("aria-expanded", "false");
      await toggle.click();
      await expect(toggle).toHaveAttribute("aria-expanded", "true");

      await expectNoHorizontalOverflow(page);
      // Viewport-only: cook mode is a fixed full-screen overlay, so a full-page
      // shot would also capture the page scrolled behind it.
      await snap(page, testInfo, "recipes-cook-mode-phone", false);

      // Done closes it and the recipe page is back.
      await page.locator("[data-cook-close]").click();
      await expect(page.locator("[data-cook-mode]")).toHaveCount(0);
      await expect(page.getByRole("heading", { level: 2, name: RECIPE.title })).toBeVisible();
    });

    test("phone 390: the checks array is visible and the page never claims safe (#4874)", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      // The server could not read Nora's allergy: it fails CLOSED into `checks`.
      await stubApi(page, { recipe: { ...RECIPE, allergy_warnings: [], checks: [UNCHECKED_LINE] } });
      await gotoRecipe(page);

      const banner = page.locator('[data-allergy-state="unchecked"]');
      await expect(banner).toBeVisible();
      // The server's own sentence, verbatim, not summarised.
      await expect(page.locator("[data-allergy-checks]")).toContainText(UNCHECKED_LINE);
      await expect(page.locator("[data-allergy-headline]")).toHaveText("Some allergies couldn't be checked");
      // And it must NOT claim the recipe is safe.
      await expect(page.getByText(/Safe for everyone as written/i)).toHaveCount(0);
      await expect(page.getByText(/nothing matched/i)).toHaveCount(0);
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, "recipes-unchecked-phone");
    });

    test("phone 390: an unsafe recipe needs the hard confirm before it can be planned", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubApi(page, { recipe: RECIPE_WARNED });
      await gotoRecipe(page, "r2");

      // The warning names who, the allergen, and what matched.
      await expect(page.locator('[data-allergy-state="warnings"]')).toBeVisible();
      await expect(page.locator("[data-allergy-warnings]")).toContainText("Nora");
      await expect(page.locator("[data-allergy-warnings]")).toContainText("peanuts");
      await expect(page.locator("[data-allergy-warnings]")).toContainText("peanut sauce");

      // Manager decision #13039: ONE hard confirm, at the add-to-plan moment —
      // and no modal before that (nothing blocks reading the recipe).
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await page.getByRole("button", { name: "Plan it" }).click();
      const confirm = page.getByRole("dialog", { name: /Add .Satay noodles. anyway\?/ });
      await expect(confirm).toBeVisible();
      await expect(confirm).toContainText("Nora is allergic to peanuts");
      await expect(confirm).toContainText("peanut sauce");
      // Let the modal's entrance transition finish before capturing it.
      await page.waitForTimeout(350);
      await snap(page, testInfo, "recipes-add-confirm-phone", false);
      await confirm.getByRole("button", { name: "Don't plan it" }).click();
      await expect(confirm).toHaveCount(0);
      await expect(page).toHaveURL(/\/dashboard\/recipes\/r2$/);
    });

    test("phone 390: a safe recipe's Plan it goes straight through (no confirm)", async ({ loggedInPage: page }) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubApi(page, { recipe: { ...RECIPE, allergy_warnings: [], checks: [] } });
      await gotoRecipe(page);
      await expect(page.locator('[data-allergy-state="checked-clear"]')).toBeVisible();
      await page.getByRole("button", { name: "Plan it" }).click();
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await expect(page).toHaveURL(/\/dashboard\/meals$/);
    });

    test("phone 390: no allergy data and a family allergy reads 'Not allergy-checked'", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      // The REST API's shape today: no allergy_warnings, no checks. Because
      // Nora has an allergy saved, an empty banner would be a lie.
      await stubApi(page, { recipe: { ...RECIPE, allergy_warnings: undefined, checks: undefined } });
      await gotoRecipe(page);
      await expect(page.locator('[data-allergy-state="not-checked"]')).toBeVisible();
      await expect(page.locator("[data-allergy-headline]")).toHaveText("Not allergy-checked");
      await expect(page.locator('[data-allergy-state="not-checked"]')).toContainText("Nora (peanuts)");
      await expect(page.getByText(/nothing matched/i)).toHaveCount(0);
      await snap(page, testInfo, "recipes-not-checked-phone");
    });

    test("phone 390: a family with no allergies saved says so instead of implying a check", async ({ loggedInPage: page }) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubApi(page, { people: PEOPLE_NO_ALLERGY, recipe: { ...RECIPE, allergy_warnings: undefined, checks: undefined } });
      await gotoRecipe(page);
      // "none-saved" renders no banner at all (nothing to say), and never a
      // "checked" claim.
      await expect(page.locator("[data-allergy-state]")).toHaveCount(0);
      await expect(page.getByText(/nothing matched/i)).toHaveCount(0);
    });

    test("phone 390: an ingredient-less recipe is flagged, not silently safe", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubApi(page, {
        recipe: { ...RECIPE, id: "r3", title: "Peanut brittle", ingredients: [], steps: null, allergy_warnings: [], checks: [NO_INGREDIENTS_LINE] },
      });
      await gotoRecipe(page, "r3");
      await expect(page.locator("[data-allergy-checks]")).toContainText(NO_INGREDIENTS_LINE);
      await expect(page.getByText(/can't be checked for allergies/)).toBeVisible();
      await snap(page, testInfo, "recipes-no-ingredients-phone");
    });
  });
}

test.describe("desktop", () => {
  test.beforeEach(() => onlyOn("desktop"));

  test("1440: two-column recipe detail, and cook mode still fills the screen", async ({ loggedInPage: page }, testInfo) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await stubApi(page);
    await gotoRecipe(page);
    await expect(page.getByRole("heading", { level: 2, name: RECIPE.title })).toBeVisible();
    await expect(page.locator("[data-ingredient-list]")).toContainText("chicken thighs");
    await expect(page.locator("[data-step-list]")).toContainText("Roast 30 min");
    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "recipes-detail-desktop");

    await page.getByRole("button", { name: "Cook mode" }).click();
    await expect(page.locator("[data-cook-mode]")).toBeVisible();
    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "recipes-cook-mode-desktop", false);
  });

  test("a housesitter can read a recipe and its allergy warnings but gets no write actions", async ({ loggedInPage: page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    // stubApi first: Playwright tries the most recently added matching route
    // first, so a role override registered BEFORE it would be shadowed.
    await stubApi(page, { recipe: RECIPE_WARNED });
    await page.route("**/bff/me/households", (route) =>
      route.fulfill({
        json: {
          data: [
            { id: "h1", name: "Sims", timezone: "UTC", role: "housesitter", joined_at: "2026-01-01T00:00:00Z", is_default: true, current: true },
          ],
        },
      }),
    );
    await gotoRecipe(page, "r2");
    // Safety data is visible to a sitter — they may be the one cooking.
    await expect(page.locator("[data-allergy-warnings]")).toContainText("Nora");
    // Cook mode is read-only, so the sitter keeps it: they are the one cooking.
    await expect(page.getByRole("button", { name: "Cook mode" })).toBeVisible();
    // No planning, editing or deleting.
    await expect(page.getByRole("button", { name: "Plan it" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Edit" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Delete" })).toHaveCount(0);
  });

  test("edit sends every field, because PUT replaces the whole recipe", async ({ loggedInPage: page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    const writes = await stubApi(page);
    await gotoRecipe(page);

    await page.getByRole("button", { name: "Edit" }).click();
    const dialog = page.getByRole("dialog", { name: "Edit recipe" });
    await expect(dialog).toBeVisible();
    // The existing values are loaded, not blank.
    await expect(dialog.getByLabel("Name")).toHaveValue(RECIPE.title);
    await expect(dialog.locator("[data-ingredient-row]")).toHaveCount(3);

    await dialog.getByLabel("Name").fill("Sheet-pan chicken fajitas (updated)");
    await dialog.getByRole("button", { name: "Save recipe" }).click();

    await expect.poll(() => writes.length).toBeGreaterThan(0);
    const put = writes.find((w) => w.method === "PUT")!;
    const body = put.body as Record<string, unknown>;
    expect(body.title).toBe("Sheet-pan chicken fajitas (updated)");
    // Every field is present: a partial PUT would clear the rest.
    for (const key of ["description", "servings", "prep_minutes", "cook_minutes", "ingredients", "steps", "tags", "source_url"]) {
      expect(body).toHaveProperty(key);
    }
    expect(body.steps).toBe("Heat the oven to 425°F.\nToss everything on a sheet pan.\nRoast 30 min, stirring once.");
  });

  test("delete asks first, then goes back to the list", async ({ loggedInPage: page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    const writes = await stubApi(page);
    await gotoRecipe(page);
    await page.getByRole("button", { name: "Delete" }).click();
    const dialog = page.getByRole("dialog", { name: "Delete this recipe?" });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText("Planned meals keep their title");
    await dialog.getByRole("button", { name: "Delete recipe" }).click();
    await expect.poll(() => writes.length).toBeGreaterThan(0);
    expect(writes[0].method).toBe("DELETE");
    await expect(page).toHaveURL(/\/dashboard\/recipes$/);
  });

  test("the search box filters on the SERVER, not the client", async ({ loggedInPage: page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    const searches: string[] = [];
    // stubApi first, then this override on top of it (last route wins).
    await stubApi(page);
    await page.route("**/bff/recipes?*", (route) => {
      const q = new URL(route.request().url()).searchParams.get("q");
      if (q) searches.push(q);
      return route.fulfill({ json: { data: LIST.filter((r) => !q || r.title.toLowerCase().includes(q.toLowerCase())) } });
    });
    await gotoRecipes(page);
    await page.getByLabel("Search recipes").fill("satay");
    await expect.poll(() => searches.includes("satay")).toBe(true);
  });
});
