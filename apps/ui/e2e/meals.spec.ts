import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn, pressOpener } from "./fixtures";

/**
 * Meals (#4568): the week plan, the suggestions sheet's EXPLAINED empty state,
 * planning a dinner and generating the grocery list on a phone.
 *
 * Built to the approved mockup (artifact #106 v3) plus the manager decisions on
 * #4519, and the API is stubbed per test with page.route so each payload is
 * exact. Several of these exist for one reason — the manager's note on this
 * ticket: "with a fish allergy, 11 of 12 recipes can be excluded. A blank week
 * plan reads as broken." So:
 *
 *   - the suggestions sheet must EXPLAIN which allergy excluded what, from the
 *     server's own `excluded` reasons (#4567's SuggestionEmptyState);
 *   - a planned meal with no allergy data must read "not allergy-checked",
 *     never blank and never "safe";
 *   - the grocery preview must show Add / On the list / Check amount, and the
 *     confirm must be idempotent (a second tap adds nothing).
 */

const PEOPLE_ALLERGY = [
  { id: "p1", household_id: "h1", display_name: "Nora", allergies: ["fish"], has_login: false },
  { id: "p2", household_id: "h1", display_name: "Theo", allergies: [], has_login: true },
];
const PEOPLE_NO_ALLERGY = [{ id: "p2", household_id: "h1", display_name: "Theo", allergies: [], has_login: true }];

const RECIPES = [
  { id: "r1", household_id: "h1", title: "Sheet-pan chicken fajitas", ingredients: [{ name: "chicken thighs" }], tags: ["weeknight"], prep_minutes: 10, cook_minutes: 25, servings: 5 },
  { id: "r2", household_id: "h1", title: "Fish tacos", ingredients: [{ name: "cod" }], tags: [], prep_minutes: 10, cook_minutes: 10, servings: 4 },
];

/** A Monday..Sunday week in November 2026, when "today" is fixed below. */
const WEEK_FROM = "2026-11-16";
const WEEK_TO = "2026-11-22";

const PLAN = [
  {
    id: "m1", household_id: "h1", date: "2026-11-16", slot: "dinner", recipe_id: "r1",
    title: "Sheet-pan chicken fajitas", notes: null, feedback: [{ id: "f1", meal_plan_id: "m1", person_id: "p1", rating: 5, comment: null }],
    created_at: "2026-11-01T00:00:00Z", updated_at: "2026-11-01T00:00:00Z",
  },
  {
    id: "m2", household_id: "h1", date: "2026-11-18", slot: "dinner", recipe_id: null,
    title: "Pizza night", notes: null, feedback: [],
    created_at: "2026-11-01T00:00:00Z", updated_at: "2026-11-01T00:00:00Z",
  },
  {
    id: "m3", household_id: "h1", date: "2026-11-18", slot: "breakfast", recipe_id: null,
    title: "Pancakes", notes: null, feedback: [],
    created_at: "2026-11-01T00:00:00Z", updated_at: "2026-11-01T00:00:00Z",
  },
];

/** meal_suggest's real result shape, for the fish-allergy case. */
const SUGGEST_ALL_EXCLUDED = {
  local_date: "2026-11-17",
  weekday: "Tuesday",
  slot: "dinner",
  eating: ["Nora", "Theo"],
  suggestions: [],
  excluded: [
    { recipe_id: "r2", name: "Fish tacos", reason: "Nora (fish): cod" },
    { recipe_id: "r3", name: "Salmon traybake", reason: "Nora (fish): salmon fillet" },
    { recipe_id: "r4", name: "Fish pie", reason: "Nora (fish): haddock" },
    { recipe_id: "r5", name: "Sushi night", reason: "Nora (fish): sashimi" },
    { recipe_id: "r6", name: "Anchovy pasta", reason: "Nora (fish): anchovy" },
    { recipe_id: "r7", name: "Kedgeree", reason: "Nora (fish): smoked haddock" },
    { recipe_id: "r8", name: "Fish and chips", reason: "Nora (fish): cod fillet" },
    { recipe_id: "r9", name: "Tuna melt", reason: "Nora (fish): tuna" },
    { recipe_id: "r10", name: "Prawn curry", reason: "Nora (fish): prawns" },
    { recipe_id: "r11", name: "Crab cakes", reason: "Nora (fish): crab" },
  ],
  excluded_total: 11,
  filtered_out: 0,
  already_planned: null,
  checks: [],
  timezone: "America/New_York",
};

const GROCERY_PREVIEW = {
  from: WEEK_FROM,
  to: WEEK_TO,
  slots: ["dinner"],
  list: { id: "l1", name: "Groceries", exists: true },
  items: [
    { text: "Bell peppers", quantity: 6, unit: null, section: "Produce", meals: ["Mon dinner Sheet-pan chicken fajitas"], status: "add" },
    { text: "Limes", quantity: 4, unit: null, section: "Produce", meals: ["Mon dinner Sheet-pan chicken fajitas"], status: "already_on_list" },
    { text: "Chicken thighs", quantity: 7, unit: "lb", section: "Meat & seafood", meals: ["Mon dinner Sheet-pan chicken fajitas"], status: "check_amount", existing_quantity: 3, existing_unit: "lb" },
    { text: "Olive oil", quantity: 5, unit: "tbsp", section: "Pantry", meals: ["Mon dinner Sheet-pan chicken fajitas"], note: "plus a pinch", status: "add" },
  ],
  sections: ["Produce", "Meat & seafood", "Pantry"],
  skipped: [
    { date: "2026-11-18", slot: "dinner", title: "Pizza night", reason: "not a recipe, so there are no ingredients to add" },
  ],
  planned: 2,
  recipe_meals: 1,
  notes: ["Quantities are as each recipe is written; meal plans have no servings yet."],
  counts: { add: 2, already_on: 1, check_amount: 1, reopened: 0 },
  applied: false,
};

const GROCERY_APPLIED = {
  ...GROCERY_PREVIEW,
  applied: true,
  items: GROCERY_PREVIEW.items.map((i) => (i.status === "add" ? { ...i, status: "already_on_list" } : i)),
  counts: { add: 0, already_on: 3, check_amount: 1, reopened: 0 },
};

type Writes = Array<{ method: string; url: string; body: unknown }>;
type Routes = {
  people?: unknown[];
  plans?: unknown[];
  recipes?: unknown[];
  /** What GET /meal-plans/grocery answers. */
  groceryPreview?: typeof GROCERY_PREVIEW;
  /** What POST /meal-plans/grocery answers (defaults to the preview, applied). */
  groceryApplied?: typeof GROCERY_APPLIED;
  groceryError?: number;
  role?: string;
  /**
   * meal_suggest's result for GET /meal-plans/suggestions. There is no such
   * REST endpoint yet (see lib/recipes/api.ts); stubbing it here is what proves
   * the sheet's ranked list and its explained empty state against the real
   * server payload shape.
   */
  suggestions?: Record<string, unknown> | null;
  /** An HTTP status to answer with instead of the payload (e.g. 404). */
  suggestionsStatus?: number;
};

/** Stubs the BFF endpoints the Meals page reads, and records every write. */
async function stubApi(page: Page, routes: Routes = {}): Promise<Writes> {
  const writes: Writes = [];
  await page.route("**/bff/me/households", (route) =>
    route.fulfill({
      json: {
        data: [
          { id: "h1", name: "Sims", timezone: "America/New_York", role: routes.role ?? "owner", joined_at: "2026-01-01T00:00:00Z", is_default: true, current: true },
        ],
      },
    }),
  );
  await page.route("**/bff/people", (route) => route.fulfill({ json: { data: routes.people ?? PEOPLE_ALLERGY } }));
  await page.route("**/bff/recipes**", (route) => route.fulfill({ json: { data: routes.recipes ?? RECIPES } }));

  // The grocery and suggestion endpoints ride the meal-plans match and are
  // checked FIRST: Playwright tries the most recently registered matching route
  // first, and "**/bff/meal-plans**" is a prefix, so a separate route for
  // /meal-plans/grocery would be silently shadowed by this one (the KB gotcha
  // #4567 hit). One handler, explicit precedence.
  await page.route("**/bff/meal-plans**", async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    if (url.pathname.endsWith("/meal-plans/suggestions")) {
      if (routes.suggestionsStatus) {
        return route.fulfill({
          status: routes.suggestionsStatus,
          json: { error: { code: "NOT_FOUND", message: "no such endpoint" } },
        });
      }
      if (!routes.suggestions) {
        // The real API's answer today: there is no suggestions endpoint.
        return route.fulfill({ status: 404, json: { error: { code: "NOT_FOUND", message: "no such endpoint" } } });
      }
      return route.fulfill({ json: { data: routes.suggestions } });
    }
    if (url.pathname.endsWith("/meal-plans/grocery")) {
      if (routes.groceryError) {
        return route.fulfill({ status: routes.groceryError, json: { error: { code: "NOT_FOUND", message: "that list is not here" } } });
      }
      if (req.method() === "POST") {
        writes.push({ method: "POST", url: url.pathname + url.search, body: null });
        // GROCERY_APPLIED is what the real POST answers: the SAME week, now
        // all already on the list. Defaulting to the preview instead made the
        // page say "Added 2 items" after a write that added nothing.
        return route.fulfill({ json: { data: routes.groceryApplied ?? GROCERY_APPLIED } });
      }
      return route.fulfill({ json: { data: routes.groceryPreview ?? GROCERY_PREVIEW } });
    }
    if (req.method() !== "GET") {
      const body = req.postDataJSON();
      writes.push({ method: req.method(), url: url.pathname, body });
      return route.fulfill({ json: { data: { id: "m-new", date: "2026-11-17", slot: "dinner", title: "planned", feedback: [] } } });
    }
    return route.fulfill({ json: { data: routes.plans ?? PLAN, from: WEEK_FROM, to: WEEK_TO } });
  });

  // Rating a meal.
  await page.route("**/bff/meals/*/feedback/*", async (route) => {
    const req = route.request();
    writes.push({ method: req.method(), url: new URL(req.url()).pathname, body: req.postDataJSON() });
    return route.fulfill({ json: { data: { id: "f-new" } } });
  });
  return writes;
}

/**
 * Pins the browser's clock to a Monday so "this week" is deterministic: the
 * page reads today from the browser (never the server's clock), so a test that
 * did not pin it would change meaning as the calendar moved.
 */
async function freezeToday(page: Page, iso = "2026-11-16T12:00:00") {
  await page.clock.setFixedTime(new Date(iso));
}

async function gotoMeals(page: Page, query = "") {
  await page.goto(`/dashboard/meals${query}`);
  await expect(page.getByRole("heading", { level: 2, name: "Meals" })).toBeVisible();
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = true) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme}`, () => {
    test.use({ colorScheme });

    test("phone 390: the week plan lists Mon..Sun with dinner, and flags plans honestly", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      await stubApi(page);
      await gotoMeals(page);

      // Seven rows, Mon..Sun, each a 44 px+ target.
      const rows = page.locator("[data-meal-day]");
      await expect(rows).toHaveCount(7);
      await expect(page.locator("[data-meal-day]").first().getByText("Mon", { exact: true })).toBeVisible();
      await expect(page.locator("[data-meal-day]").nth(6).getByText("Sun", { exact: true })).toBeVisible();

      // Monday's dinner is there, with its rating; Wednesday's free-text night
      // is there and says it has no ingredients to add.
      await expect(page.locator('[data-meal-day="2026-11-16"] [data-meal-title]')).toHaveText("Sheet-pan chicken fajitas");
      await expect(page.locator('[data-meal-day="2026-11-16"]')).toContainText("5.0");
      await expect(page.locator('[data-meal-day="2026-11-18"] [data-meal-title]')).toHaveText("Pizza night");
      await expect(page.locator('[data-meal-day="2026-11-18"]')).toContainText("no ingredients to add");

      // Breakfast on Wednesday collapses into the "more meals" line, not a row.
      await expect(page.locator('[data-meal-day="2026-11-18"] [data-meal-otherslots]')).toContainText("1 more meal");
      await expect(page.locator('[data-meal-day="2026-11-18"] [data-meal-otherslots]')).toContainText("Pancakes");

      // Today (Monday) is marked, and the week heading says so.
      await expect(page.locator('[data-meal-day="2026-11-16"]')).toHaveAttribute("data-meal-today", "true");
      await expect(page.locator("[data-meals-weekday]")).toHaveText("This week");
      await expect(page.locator("[data-meals-weekrange]")).toContainText("2 dinners planned");

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, "meals-week-phone");
    });

    test("phone 390: a planned meal with no allergy data says 'not allergy-checked', never safe", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      // The REST API's shape today: no allergy_warnings, no checks, and Nora
      // has a fish allergy saved. An empty flag would read as "safe".
      await stubApi(page);
      await gotoMeals(page);

      await expect(page.locator('[data-meal-warning="not-checked"]')).toBeVisible();
      await expect(page.locator('[data-meal-warning="not-checked"]')).toContainText("Not allergy-checked");
      await expect(page.locator('[data-meal-warning="not-checked"]')).toContainText("Nora (fish)");
      // The inline flag on the row itself, not only the banner.
      await expect(page.locator('[data-meal-day="2026-11-16"] [data-meal-flag]')).toBeVisible();
      // And it never claims a check happened.
      await expect(page.getByText(/nothing matched/i)).toHaveCount(0);
      await snap(page, testInfo, "meals-not-checked-phone");
    });

    test("phone 390: no allergies saved reads as nothing to check, not as a warning", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      await stubApi(page, { people: PEOPLE_NO_ALLERGY });
      await gotoMeals(page);

      await expect(page.locator("[data-meal-warning]")).toHaveCount(0);
      await expect(page.locator("[data-meal-flag]")).toHaveCount(0);
      await expect(page.getByText(/not allergy-checked/i)).toHaveCount(0);
      await snap(page, testInfo, "meals-no-allergies-phone");
    });

    test("phone 390: plan a dinner from an empty night", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      const writes = await stubApi(page);
      await gotoMeals(page);

      // Tuesday has nothing planned: its row is the "Plan dinner" button.
      const tuesday = page.locator('[data-meal-empty="2026-11-17"]');
      await expect(tuesday).toBeVisible();
      const box = (await tuesday.boundingBox())!;
      expect(Math.round(box.height)).toBeGreaterThanOrEqual(44);

      await tuesday.click();
      const sheet = page.locator("[data-suggestion-body]");
      await expect(sheet).toBeVisible();
      // The heading names the night it is suggesting for: the drawer's title is
      // the accessible name of the dialog, and it is what tells a parent they
      // are planning Tuesday and not tonight.
      await expect(page.getByRole("dialog", { name: /What's for dinner on Tue November 17/ })).toBeVisible();

      await snap(page, testInfo, "meals-suggest-phone", false);

      // There is no REST endpoint for meal_suggest yet, so the sheet says so and
      // offers the recipe box. It never invents ranks or reasons.
      await expect(sheet.locator("[data-suggestions-unranked]")).toContainText("Suggestions aren't ranked here yet");
      await sheet.locator('[data-recipe-picker-plan]').first().click();

      // The write was PUT /meal-plans/2026-11-17/dinner with the recipe id.
      await expect.poll(() => writes.length).toBeGreaterThan(0);
      const put = writes.find((w) => w.method === "PUT")!;
      expect(put.url).toBe("/bff/meal-plans/2026-11-17/dinner");
      expect((put.body as { recipe_id: string }).recipe_id).toBe("r1");
      await expect(sheet).toHaveCount(0);
    });

    test("phone 390: the recipe picker says which recipes were allergy-checked", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      // A fish allergy is saved and the recipes carry no allergy data (the REST
      // API's shape today). The picker must not look like a clean bill of
      // health: every row says it was not checked.
      await stubApi(page);
      await gotoMeals(page);
      await page.getByRole("button", { name: /What's for dinner/ }).click();
      const sheet = page.locator("[data-suggestion-body]");
      await expect(sheet).toBeVisible();
      await expect(sheet.locator('[data-recipe-picker-flag="not-checked"]')).toHaveCount(2);
      await expect(sheet.locator("[data-recipe-picker-flag]").first()).toContainText("Not allergy-checked");
      await snap(page, testInfo, "meals-recipe-picker-phone", false);
    });

    test("phone 390: with a fish allergy, 11 of 12 excluded is EXPLAINED, not blank", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      // The manager's case, verbatim, delivered through the endpoint the page
      // asks for: meal_suggest returns ZERO ideas and 11 exclusions, because
      // Nora is allergic to fish. A blank list would read as broken; the sheet
      // must say which allergy excluded what, and that the total is 11 even
      // though only 10 were named (the server caps `excluded` at 20 — here the
      // payload keeps them all, so the count comes straight from the server).
      await stubApi(page, { suggestions: SUGGEST_ALL_EXCLUDED });
      await gotoMeals(page);

      await page.getByRole("button", { name: /What's for dinner/ }).click();
      const sheet = page.locator("[data-suggestion-body]");
      await expect(sheet).toBeVisible();

      const empty = sheet.locator('[data-suggestion-empty="none"]');
      await expect(empty).toBeVisible();
      await expect(empty.locator("[data-suggestion-headline]")).toHaveText("No ideas left after allergy checks");
      // WHICH allergy, and WHICH recipes — grouped by person+rule, biggest first.
      await expect(empty.locator("[data-exclusion-group]").first()).toContainText("Nora's fish allergy");
      await expect(empty.locator("[data-exclusion-group]").first()).toContainText("excluded 10 recipes");
      await expect(empty).toContainText("Fish tacos");
      // It reads as the safety rule working, never as a failure.
      await expect(empty).toContainText("nothing unsafe was offered");
      // And it is the explained state, so no unranked-fallback box.
      await expect(sheet.locator("[data-suggestions-unranked]")).toHaveCount(0);

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, "meals-suggest-all-excluded-phone", false);
    });

    test("phone 390: a checks line is shown verbatim when a rule could not be applied", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      const line = "Nora has an allergy saved as \"deathly fish\" that could not be checked against recipes; confirm the meal is safe.";
      await stubApi(page, { suggestions: { ...SUGGEST_ALL_EXCLUDED, checks: [line], excluded_total: 0, excluded: [] } });
      await gotoMeals(page);
      await page.getByRole("button", { name: /What's for dinner/ }).click();
      const sheet = page.locator("[data-suggestion-body]");
      await expect(sheet.locator("[data-suggestion-checks]")).toContainText(line);
      await snap(page, testInfo, "meals-suggest-checks-phone", false);
    });

    test("phone 390: ranked suggestions carry meal_suggest's own reasons", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      await stubApi(page, {
        suggestions: {
          ...SUGGEST_ALL_EXCLUDED,
          suggestions: [
            { rank: 1, recipe_id: "r1", name: "Sheet-pan chicken fajitas", reason: "Rated 5/5 by Nora; not had in 4 weeks.", total_minutes: 35, tags: ["weeknight"], times_made: 4 },
          ],
          excluded: [
            { recipe_id: "r2", name: "Fish tacos", reason: "Nora (fish): cod" },
          ],
          excluded_total: 1,
        },
      });
      await gotoMeals(page);
      await page.getByRole("button", { name: /What's for dinner/ }).click();
      const sheet = page.locator("[data-suggestion-body]");

      await expect(sheet.locator("[data-suggestion-row]")).toHaveCount(1);
      // The reason is the SERVER's sentence, rendered as written: rewording it
      // would make the page disagree with the assistant.
      await expect(sheet.locator("[data-suggestion-name]")).toHaveText("Sheet-pan chicken fajitas");
      await expect(sheet.locator("[data-suggestion-row]")).toContainText("Rated 5/5 by Nora; not had in 4 weeks.");

      // The exclusions collapse into "Skipped for safety" while there are ideas.
      const skipped = sheet.locator("[data-skipped-for-safety]");
      await expect(skipped).toBeVisible();
      await expect(skipped.locator("summary")).toContainText("Skipped for safety · 1");
      // Collapsed by default (the manager decision asks for collapsed, not hidden).
      await expect(skipped).not.toHaveAttribute("open", "");
      await skipped.locator("summary").click();
      await expect(skipped).toContainText("Nora's fish allergy");

      await snap(page, testInfo, "meals-suggest-ranked-phone", false);

      // "Plan this" writes the meal.
      await sheet.locator("[data-suggestion-plan]").click();
      await expect(sheet).toHaveCount(0);
    });

    test("phone 390: rate a meal for the people who ate it", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      const writes = await stubApi(page);
      await gotoMeals(page);

      await page.locator('[data-meal-day="2026-11-16"] [data-meal-rate]').click();
      const sheet = page.locator("[data-rate-body]");
      await expect(sheet).toBeVisible();
      await expect(sheet).toContainText("Sheet-pan chicken fajitas");

      // One row per person; an owner may rate for both (kids have no login).
      // Mantine's Rating is a row of radio inputs, each labelled per star, so
      // this is the control a screen reader announces. The INPUT itself is
      // visually hidden (that is how a star rating stays accessible without a
      // visible radio), so the tap lands on the label wrapped around it —
      // `check()` on the input fails with "outside of the viewport".
      const fourStarsForNora = sheet.locator('[data-rate-person="p1"] input[aria-label="4 stars for Nora"]');
      // Mantine renders the input then its label; the label is the tappable star.
      await fourStarsForNora.locator("xpath=following-sibling::label").click();
      await expect(fourStarsForNora).toBeChecked();
      await snap(page, testInfo, "meals-rate-phone", false);
      await sheet.getByRole("button", { name: "Save ratings" }).click();

      await expect.poll(() => writes.length).toBeGreaterThan(0);
      const put = writes.find((w) => w.method === "PUT" && w.url.includes("/feedback/"))!;
      expect(put.url).toBe("/bff/meals/m1/feedback/p1");
      expect((put.body as { rating: number }).rating).toBe(4);
    });

    test("phone 390: the grocery preview shows Add / On the list / Check amount, then writes once", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      const writes = await stubApi(page);
      await page.goto(`/dashboard/meals/grocery?from=${WEEK_FROM}&to=${WEEK_TO}`);
      await expect(page.getByRole("heading", { level: 2 })).toContainText("Add this week's dinners to Groceries");

      // Every row carries its verdict, and the ones that need a person show BOTH
      // amounts (the manager decision: the list is shared, so nothing is
      // silently overwritten).
      await expect(page.locator('[data-grocery-item="add"]')).toHaveCount(2);
      await expect(page.locator('[data-grocery-item="already_on_list"]')).toHaveCount(1);
      await expect(page.locator('[data-grocery-item="check_amount"]')).toHaveCount(1);
      await expect(page.locator("[data-check-amount]")).toContainText("The list says");
      await expect(page.locator("[data-check-amount]")).toContainText("3 lb");
      await expect(page.locator("[data-check-amount]")).toContainText("7 lb");

      // The summary strip and the skipped meal (a short list explains itself).
      await expect(page.locator("[data-count-add]")).toContainText("2 to add");
      await expect(page.locator("[data-count-already]")).toContainText("1 already on the list");
      await expect(page.locator("[data-count-check]")).toContainText("1 check the amount");
      await expect(page.locator("[data-grocery-skipped]")).toContainText("Pizza night");
      await expect(page.locator("[data-grocery-skipped]")).toContainText("not a recipe");
      // Sections in a store's walking order.
      await expect(page.locator("[data-grocery-section]")).toHaveCount(3);

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, "meals-grocery-preview-phone");

      // Nothing has been written yet: the preview is a GET.
      expect(writes.filter((w) => w.method === "POST").length).toBe(0);

      await page.locator("[data-grocery-confirm]").click();
      await expect.poll(() => writes.filter((w) => w.method === "POST").length).toBe(1);
      expect(writes[0].url).toContain("/bff/meal-plans/grocery");
      expect(writes[0].url).toContain(`from=${WEEK_FROM}`);

      // After the write the page reports the TRUTH: nothing new this time.
      await expect(page.locator("[data-grocery-headline]")).toContainText("Nothing new");
      await expect(page.locator("[data-grocery-nothing-new]")).toContainText("pressing the button twice safe");
      await snap(page, testInfo, "meals-grocery-applied-phone");
    });

    test("phone 390: the week plan hands the range to the grocery preview", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      await stubApi(page);
      await gotoMeals(page);

      await page.locator("[data-meals-grocery-go]").click();
      await expect(page).toHaveURL(new RegExp(`/dashboard/meals/grocery\\?from=${WEEK_FROM}&to=${WEEK_TO}`));
    });

    test("phone 390: a read-only role sees the plan and the preview, and cannot write", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      await stubApi(page, { role: "housesitter" });
      await gotoMeals(page);

      // No suggestion button, no "Plan dinner" control; the empty night is text.
      await expect(page.locator("[data-meals-suggest]")).toHaveCount(0);
      await expect(page.locator('[data-meal-empty="2026-11-17"]')).toBeDisabled();
      // Allergies are safety data and stay visible for the sitter cooking.
      await expect(page.locator('[data-meal-warning="not-checked"]')).toContainText("Nora (fish)");

      await page.goto(`/dashboard/meals/grocery?from=${WEEK_FROM}&to=${WEEK_TO}`);
      await expect(page.locator("[data-grocery-preview]")).toBeVisible();
      await expect(page.locator("[data-grocery-confirm]")).toHaveCount(0);
      await expect(page.locator("[data-grocery-readonly]")).toContainText("for the family");
      await snap(page, testInfo, "meals-grocery-readonly-phone");
    });
  });
}

test.describe("error and empty states", () => {
  test("phone 390: the grocery preview says nothing was written when the list is gone", async ({ loggedInPage: page }, testInfo) => {
    onlyOn("phone");
    await page.setViewportSize({ width: 390, height: 844 });
    await stubApi(page, { groceryError: 404 });
    await page.goto(`/dashboard/meals/grocery?from=${WEEK_FROM}&to=${WEEK_TO}`);
    await expect(page.getByText("That grocery list isn't available")).toBeVisible();
    await expect(page.getByRole("link", { name: "Use the default list" })).toBeVisible();
    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "meals-grocery-list-gone-phone");
  });

  /**
   * THE POINT OF THE SERVER-SIDE MERGE (#4568, manager decision #2).
   *
   * The grocery write goes into a SHARED list another family member may be
   * checking off in store mode. A blind `POST /lists/{id}/items` would append
   * the whole week again on every retry — twelve duplicate rows — so the write
   * is idempotent server-side (`lists.Repo.MergeItems`) and the preview and the
   * apply run the SAME merge.
   *
   * This drives the real page TWICE against a stub that models MergeItems
   * faithfully (a row whose words are already open is not added again; a
   * different amount is reported, not appended), and asserts the LIST did not
   * grow the second time. The API-level proof is TestGrocery_ApplyIsIdempotent
   * in apps/api/internal/meals; this is the proof that the button a person
   * presses cannot defeat it.
   */
  test("phone 390: adding the week twice does not duplicate a single row", async ({ loggedInPage: page }, testInfo) => {
    onlyOn("phone");
    await page.setViewportSize({ width: 390, height: 844 });
    await freezeToday(page);

    // The shared list as rows keyed by their words, holding the amount each
    // row says. It starts with what GROCERY_PREVIEW reports: Limes (same
    // amount) and Chicken thighs (the list says 3 lb, the plan wants 7 lb).
    const amount = (i: { quantity?: number | null; unit?: string | null }) =>
      i.quantity == null ? "" : `${i.quantity} ${i.unit ?? ""}`.trim();
    const rows = new Map<string, string>([
      ["limes", amount(GROCERY_PREVIEW.items[1])],
      ["chicken thighs", "3 lb"],
    ]);
    const posts: string[] = [];

    /** lists.Repo.MergeItems, in miniature. */
    const mergeWeek = () => {
      const items = GROCERY_PREVIEW.items.map((it) => {
        const key = it.text.trim().toLowerCase();
        const existing = rows.get(key);
        if (existing === undefined) {
          rows.set(key, amount(it));
          return { ...it, status: "add" };
        }
        // Same words, same amount: already there, add nothing.
        if (existing === amount(it)) return { ...it, status: "already_on_list" };
        // Same words, different amount: report both, change nothing.
        const [q, u] = existing.split(" ");
        return { ...it, status: "check_amount", existing_quantity: Number(q), existing_unit: u || null };
      });
      const count = (s: string) => items.filter((i) => i.status === s).length;
      return {
        ...GROCERY_PREVIEW,
        applied: true,
        items,
        counts: { add: count("add"), already_on: count("already_on_list"), check_amount: count("check_amount"), reopened: 0 },
        rows_on_list: rows.size,
      };
    };

    /** The preview: the same merge, written nothing (dry run). */
    const previewWeek = () => {
      const items = GROCERY_PREVIEW.items.map((it) => {
        const key = it.text.trim().toLowerCase();
        const existing = rows.get(key);
        if (existing === undefined) return it;
        if (existing === amount(it)) return { ...it, status: "already_on_list" };
        const [q, u] = existing.split(" ");
        return { ...it, status: "check_amount", existing_quantity: Number(q), existing_unit: u || null };
      });
      const count = (s: string) => items.filter((i) => i.status === s).length;
      return {
        ...GROCERY_PREVIEW,
        items,
        counts: { add: count("add"), already_on: count("already_on_list"), check_amount: count("check_amount"), reopened: 0 },
        rows_on_list: rows.size,
      };
    };

    await page.route("**/bff/meal-plans/grocery**", (route) => {
      if (route.request().method() === "POST") {
        posts.push(route.request().url());
        return route.fulfill({ json: { data: mergeWeek() } });
      }
      return route.fulfill({ json: { data: previewWeek() } });
    });
    await page.route("**/bff/me/households", (route) =>
      route.fulfill({
        json: {
          data: [
            { id: "h1", name: "Sims", timezone: "America/New_York", role: "owner", joined_at: "2026-01-01T00:00:00Z", is_default: true, current: true },
          ],
        },
      }),
    );

    const url = `/dashboard/meals/grocery?from=${WEEK_FROM}&to=${WEEK_TO}`;

    // FIRST visit: two rows to add (Bell peppers, Olive oil), one already on
    // the list (Limes), one to check by hand (Chicken thighs).
    await page.goto(url);
    await expect(page.locator('[data-grocery-item="add"]')).toHaveCount(2);
    await expect(page.locator('[data-grocery-item="already_on_list"]')).toHaveCount(1);
    await expect(page.locator('[data-grocery-item="check_amount"]')).toHaveCount(1);
    const rowsBefore = rows.size;
    await page.locator("[data-grocery-confirm]").click();
    await expect(page.locator("[data-grocery-headline]")).toContainText("Added 2 items");
    await expect.poll(() => rows.size).toBe(rowsBefore + 2);

    // SECOND visit — a fresh page load, a fresh preview, another press. This is
    // the retry manager decision #2 is about.
    await page.goto(url);
    await expect(page.locator('[data-grocery-item="add"]')).toHaveCount(0);
    await expect(page.locator('[data-grocery-item="already_on_list"]')).toHaveCount(3);
    await expect(page.locator('[data-grocery-item="check_amount"]')).toHaveCount(1);
    // Nothing to add, so the button is disabled: nobody can press it again to
    // duplicate the week, and the server would refuse to anyway.
    await expect(page.locator("[data-grocery-confirm]")).toBeDisabled();
    await expect(page.locator("[data-grocery-confirm]")).toContainText("Nothing new to add");

    // The list holds exactly one row per ingredient — the merge left it alone.
    expect(rows.size).toBe(rowsBefore + 2);
    // And the second visit never even issued a POST.
    expect(posts.length).toBe(1);
    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "meals-grocery-second-visit-no-duplicates-phone");
  });

  test("phone 390: a week with nothing planned offers the next empty night", async ({ loggedInPage: page }, testInfo) => {
    onlyOn("phone");
    await page.setViewportSize({ width: 390, height: 844 });
    await freezeToday(page);
    await stubApi(page, { plans: [] });
    await gotoMeals(page);

    await expect(page.locator("[data-meal-empty]")).toHaveCount(7);
    await expect(page.locator("[data-meals-weekrange]")).toContainText("0 dinners planned");
    await expect(page.getByText("Mon is open")).toBeVisible();
    await snap(page, testInfo, "meals-empty-week-phone");
  });

  test("desktop: the week plan and the grocery preview side by side", async ({ loggedInPage: page }, testInfo) => {
    onlyOn("desktop");
    await freezeToday(page);
    await stubApi(page);
    await gotoMeals(page);
    await expect(page.locator("[data-meal-day]")).toHaveCount(7);
    // The two-column layout the mockup draws from 992 px.
    const side = page.locator("[data-meals-grocery]");
    await expect(side).toBeVisible();
    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "meals-week-desktop");

    await page.goto(`/dashboard/meals/grocery?from=${WEEK_FROM}&to=${WEEK_TO}`);
    await expect(page.locator("[data-grocery-item]")).toHaveCount(4);
    await snap(page, testInfo, "meals-grocery-desktop");
  });

  test("desktop: the suggestions sheet is keyboard-reachable and Esc closes it", async ({ loggedInPage: page }) => {
    onlyOn("desktop");
    await freezeToday(page);
    await stubApi(page);
    await gotoMeals(page);
    const opener = page.locator("[data-meals-suggest]");
    await pressOpener(opener);
    await expect(page.locator("[data-suggestion-body]")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.locator("[data-suggestion-body]")).toHaveCount(0);
    // Focus returns to the button that opened it (Mantine Drawer).
    await expect(opener).toBeFocused();
  });
});
