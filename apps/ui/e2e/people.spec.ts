import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * People pages (#4565), built to the approved People & House mockup (artifact
 * #89: the Family section and a person's screen) inside the approved shell
 * (#75 v1), on the card + DetailPageLayout patterns the properties (#4553),
 * assets (#4554), vehicles (#4555) and pets (#4556) pages use.
 *
 * What this spec proves, and why each part is worth a test:
 *  1. THE ACCEPTANCE CRITERION: a child with an allergy is added on a PHONE.
 *     At 390 px the list is cards and "Add person" opens the bottom sheet (no
 *     /new route: the #4509 audit bug), the allergy goes in and is POSTed, and
 *     the new person's page opens with the allergy on it.
 *  2. AN EDIT KEEPS THE ALLERGIES. PUT /people/{id} replaces every field, so a
 *     body that leaves `allergies` out CLEARS them (the manager note on this
 *     ticket). Renaming a person must send their allergies back. This is the
 *     e2e half of person-facts.test.mjs, and the half that proves the page
 *     actually sends what the helper built.
 *  3. DELETE ASKS, WITH THE COUNTS. DELETE answers 409 CONFIRM_REQUIRED plus
 *     what points at the person (#4743). The dialog must show those counts,
 *     say the notes are LOST, and only then repeat with ?confirm=true. A 409
 *     LINKED_MEMBER is a different sentence: remove the member instead.
 *  4. MOBILE-FIRST: cards at 390 px, a table at 1440 px, nothing clips, and
 *     every card is one 44 px+ link.
 *
 * The API is stubbed per test with page.route against /bff (the UI talks to
 * its own BFF), so each payload is exact. The clock is pinned so an age cannot
 * drift: the household's today is 2026-11-18 and Nora (born 2012-03-04) is 14.
 */

/** The mockup's Nora: a daughter with an allergy that carries a note. */
const NORA = {
  id: "p1",
  household_id: "h1",
  user_id: null,
  has_login: false,
  display_name: "Nora Sims",
  relationship: "daughter",
  birthdate: "2012-03-04T00:00:00Z",
  school: "Springfield Elementary",
  grade: "8th",
  allergies: ["peanuts (carries an EpiPen)", "tree nuts"],
  notes: "Piano on Tuesdays.",
  color: "violet",
  created_at: "2020-06-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

/** Sam: an adult with no allergies and no login of their own. */
const SAM = {
  id: "p2",
  household_id: "h1",
  user_id: null,
  has_login: false,
  display_name: "Sam Sims",
  relationship: "grandma",
  birthdate: null,
  school: null,
  grade: null,
  allergies: [],
  notes: null,
  color: "teal",
  created_at: "2020-06-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

/** The preferences GET /people/{id} embeds (person_preferences, migration 041). */
const PREFERENCES = [
  { id: "pr1", person_id: "p1", kind: "like", category: "food", value: "pizza", notes: null },
  { id: "pr2", person_id: "p1", kind: "dislike", category: "food", value: "olives", notes: null },
  { id: "pr3", person_id: "p1", kind: "dietary", category: "general", value: "vegetarian", notes: "Since March." },
];

type Captured = {
  posts: Record<string, unknown>[];
  puts: Record<string, unknown>[];
  deletes: string[];
};

/**
 * Answers the reads these pages make and records the writes. `person` is what
 * GET /people/{id} returns; `conflict` makes DELETE answer 409 first, the way
 * the API does for a person that records point at.
 */
async function stubPeopleApi(
  page: Page,
  opts: {
    people?: unknown[];
    person?: Record<string, unknown>;
    role?: string;
    conflict?: { code: string; references?: Record<string, number> };
  } = {},
): Promise<Captured> {
  const writes: Captured = { posts: [], puts: [], deletes: [] };
  const role = opts.role ?? "owner";
  const people = opts.people ?? [NORA, SAM];
  const person = opts.person ?? { ...NORA, preferences: PREFERENCES };
  // What a POST created, so the detail read after it serves the new person
  // (the real API would). Without this the new page would show Nora.
  let created: Record<string, unknown> | null = null;

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

  await page.route(/\/bff\/people(\?.*)?$/, async (route) => {
    const req = route.request();
    if (req.method() === "POST") {
      const body = req.postDataJSON() as Record<string, unknown>;
      writes.posts.push(body);
      created = { ...NORA, id: "p9", preferences: [], ...body };
      return route.fulfill({ status: 201, json: { data: created } });
    }
    return route.fulfill({ json: { data: people } });
  });

  await page.route(/\/bff\/people\/[^/?]+(\?.*)?$/, async (route) => {
    const req = route.request();
    if (req.method() === "PUT") {
      const body = req.postDataJSON() as Record<string, unknown>;
      writes.puts.push(body);
      return route.fulfill({ json: { data: { ...person, ...body } } });
    }
    if (req.method() === "DELETE") {
      writes.deletes.push(req.url());
      const wantsConfirm = /confirm=true/i.test(req.url());
      if (opts.conflict && !wantsConfirm) {
        return route.fulfill({
          status: 409,
          json: {
            error: { code: opts.conflict.code, message: "still has things attached" },
            ...(opts.conflict.references ? { references: opts.conflict.references } : {}),
          },
        });
      }
      return route.fulfill({ status: 204, body: "" });
    }
    return route.fulfill({ json: { data: created ?? person } });
  });

  // The frame's Resources section (role-gated, #4564) makes its own reads.
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

    /**
     * THE ACCEPTANCE CRITERION: add a child with an allergy on a phone.
     * 390 px, the bottom sheet, the allergy typed in, POSTed, and the new
     * person's page shows it.
     */
    test("phone 390: add a child with an allergy, and it lands on their page", async ({
      loggedInPage: page,
    }, testInfo) => {
      onlyOn("phone");
      await page.clock.setFixedTime(new Date("2026-11-18T15:00:00Z"));
      await page.setViewportSize({ width: 390, height: 844 });
      const writes = await stubPeopleApi(page);
      await page.goto("/dashboard/people");
      const main = page.locator("main");
      await expect(main.getByRole("heading", { level: 2, name: /People/ })).toBeVisible();

      // Cards, one link each, 44 px+, inside the viewport.
      const cards = main.locator("[data-person-card]");
      await expect(cards).toHaveCount(2);
      await expect(main.locator("[data-person-table]")).toBeHidden();
      for (let i = 0; i < 2; i++) {
        const box = (await cards.nth(i).boundingBox())!;
        expect(box.height).toBeGreaterThanOrEqual(43.5);
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width).toBeLessThanOrEqual(390);
      }

      // Nora's card: the name, "Daughter · 14", and her allergy in words.
      const nora = cards.first();
      await expect(nora).toContainText("Nora Sims");
      await expect(nora).toContainText("Daughter \u00b7 14");
      await expect(nora).toContainText("Allergic to peanuts, tree nuts");
      await expect(nora).toHaveAttribute("href", "/dashboard/people/p1");
      // Sam's card invents no age and no allergy line.
      const sam = cards.nth(1);
      await expect(sam).toContainText("Sam Sims");
      await expect(sam).toContainText("Grandma");
      await expect(sam).not.toContainText("Allergic to");

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `people-390-${colorScheme}`, true);

      // Add: the sheet, never a /new route (the #4509 audit bug).
      const add = main.getByRole("button", { name: "Add person" });
      expect((await add.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);
      await add.click();
      const dialog = page.getByRole("dialog", { name: "Add person" });
      await expect(dialog).toBeVisible();
      expect(page.url()).not.toContain("/new");

      await dialog.getByRole("textbox", { name: "Name" }).fill("Theo Sims");
      await dialog.getByRole("textbox", { name: "Relationship" }).fill("son");
      await dialog.getByRole("textbox", { name: "Allergies" }).fill("strawberries\nkiwi");
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `people-390-${colorScheme}-add`);

      await dialog.getByRole("button", { name: "Add person" }).click();
      await expect.poll(() => writes.posts.length).toBe(1);
      const body = writes.posts[0];
      expect(body.display_name).toBe("Theo Sims");
      expect(body.relationship).toBe("son");
      expect(body.allergies).toEqual(["strawberries", "kiwi"]);

      // It navigates to the new person's page, which the person reads.
      await expect(page).toHaveURL(/\/dashboard\/people\/p9$/);
      const allergies = page.locator("main [data-person-allergies]");
      await expect(allergies).toBeVisible();
      await expect(allergies).toContainText("strawberries");
      await expect(allergies).toContainText("kiwi");
    });

    /**
     * AN EDIT KEEPS THE ALLERGIES (the manager note): the PUT must carry them.
     */
    test("phone 390: renaming a person keeps their allergies in the PUT", async ({
      loggedInPage: page,
    }, testInfo) => {
      onlyOn("phone");
      await page.clock.setFixedTime(new Date("2026-11-18T15:00:00Z"));
      await page.setViewportSize({ width: 390, height: 844 });
      const writes = await stubPeopleApi(page);
      await page.goto("/dashboard/people/p1");
      const main = page.locator("main");
      await expect(main.getByRole("heading", { level: 2, name: "Nora Sims" })).toBeVisible();

      // The head, the allergy with its note, and the preferences by kind.
      await expect(main.locator("[data-person-head]")).toContainText("Daughter \u00b7 14");
      // The allergen leads, its note follows once (never printed twice).
      const allergyList = main.locator("[data-person-allergies]");
      await expect(allergyList).toContainText("peanuts");
      await expect(allergyList).toContainText("\u2014 carries an EpiPen");
      await expect(allergyList).toContainText("tree nuts");
      await expect(allergyList).not.toContainText("(carries an EpiPen)");
      const prefs = main.locator("[data-person-preferences]");
      await expect(prefs).toContainText("Likes");
      await expect(prefs).toContainText("pizza");
      await expect(prefs).toContainText("Dietary");
      await expect(prefs).toContainText("Since March.");
      // The birthday is the date the API sent, never a day earlier (#4550).
      await expect(main.locator('[data-field="birthdate"] dd')).toContainText("March 4, 2012");
      await expect(main.locator('[data-field="school"] dd')).toContainText("Springfield Elementary \u00b7 8th");
      // Readable text, not the raw stored value.
      await expect(main.locator('[data-field="relationship"] dd')).toHaveText("Daughter");
      // The two links out to what they are part of.
      await expect(main.getByRole("link", { name: "Their documents" })).toHaveAttribute(
        "href",
        "/dashboard/documents?person=p1",
      );
      await expect(main.getByRole("link", { name: "Their activity" })).toHaveAttribute(
        "href",
        "/dashboard/activity?member=p1",
      );
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `person-390-${colorScheme}`, true);

      // Edit and change ONLY the name.
      await main.getByRole("button", { name: "Actions for Nora Sims" }).click();
      await page.getByRole("menuitem", { name: "Edit" }).click();
      const name = main.getByRole("textbox", { name: "Name" });
      await expect(name).toHaveValue("Nora Sims");
      // The allergy field opens with the stored allergies, one per line.
      await expect(main.getByRole("textbox", { name: "Allergies" })).toHaveValue(
        "peanuts (carries an EpiPen)\ntree nuts",
      );
      await snap(page, testInfo, `person-390-${colorScheme}-edit`);
      await name.fill("Nora Sims-Reed");
      await main.getByRole("button", { name: "Save" }).click();

      await expect.poll(() => writes.puts.length).toBe(1);
      const body = writes.puts[0];
      expect(body.display_name).toBe("Nora Sims-Reed");
      // THE POINT: a rename did not drop the allergies.
      expect(body.allergies).toEqual(["peanuts (carries an EpiPen)", "tree nuts"]);
      // A date-only birthday, still the same day.
      expect(body.birthdate).toBe("2012-03-04");
      expect(body.school).toBe("Springfield Elementary");
    });

    /**
     * DELETE ASKS WITH THE COUNTS (#4743), and the notes are called out as lost.
     */
    test("phone 390: delete asks with the reference counts before it repeats with confirm", async ({
      loggedInPage: page,
    }, testInfo) => {
      onlyOn("phone");
      await page.clock.setFixedTime(new Date("2026-11-18T15:00:00Z"));
      await page.setViewportSize({ width: 390, height: 844 });
      const writes = await stubPeopleApi(page, {
        conflict: { code: "CONFIRM_REQUIRED", references: { notes: 2, documents: 1, list_items: 1, meal_ratings: 0, events: 1 } },
      });
      await page.goto("/dashboard/people/p1");
      const main = page.locator("main");
      await expect(main.getByRole("heading", { level: 2, name: "Nora Sims" })).toBeVisible();

      await main.getByRole("button", { name: "Actions for Nora Sims" }).click();
      await page.getByRole("menuitem", { name: "Delete" }).click();

      // The first call asked the API, which answered with the counts.
      await expect.poll(() => writes.deletes.length).toBe(1);
      expect(writes.deletes[0]).not.toContain("confirm=true");

      const dialog = page.getByRole("dialog", { name: "Delete person" });
      await expect(dialog).toBeVisible();
      await expect(dialog).toContainText("2 notes about them");
      await expect(dialog).toContainText("1 document");
      await expect(dialog).toContainText("1 list item");
      await expect(dialog).toContainText("1 event");
      // The one that is LOST, said plainly.
      await expect(dialog).toContainText("notes are deleted with them");
      await snap(page, testInfo, `person-390-${colorScheme}-delete`);

      await dialog.getByRole("button", { name: "Delete" }).click();
      await expect.poll(() => writes.deletes.length).toBe(2);
      expect(writes.deletes[1]).toContain("confirm=true");
      await expect(page).toHaveURL(/\/dashboard\/people$/);
    });

    /**
     * A MEMBER IS NOT DELETABLE, and the dialog says what to do instead.
     */
    test("phone 390: a linked member is told to remove the member, not the person", async ({
      loggedInPage: page,
    }) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubPeopleApi(page, { conflict: { code: "LINKED_MEMBER" } });
      await page.goto("/dashboard/people/p1");
      const main = page.locator("main");
      await expect(main.getByRole("heading", { level: 2, name: "Nora Sims" })).toBeVisible();

      await main.getByRole("button", { name: "Actions for Nora Sims" }).click();
      await page.getByRole("menuitem", { name: "Delete" }).click();
      const dialog = page.getByRole("dialog", { name: "Can't delete a member" });
      await expect(dialog).toBeVisible();
      await expect(dialog).toContainText("Remove the member in Members instead");
      // No second, destructive button: this is not a confirm.
      await expect(dialog.getByRole("button", { name: "Delete" })).toHaveCount(0);
    });

    /**
     * A role that may not manage people gets no Add and no Delete: the API
     * would refuse both, so neither button is drawn.
     */
    test("phone 390: a family member reads the list but gets no Add or Delete", async ({
      loggedInPage: page,
    }, testInfo) => {
      onlyOn("phone");
      await page.clock.setFixedTime(new Date("2026-11-18T15:00:00Z"));
      await page.setViewportSize({ width: 390, height: 844 });
      await stubPeopleApi(page, { role: "family_member" });
      await page.goto("/dashboard/people");
      const main = page.locator("main");
      await expect(main.getByRole("heading", { level: 2, name: /People/ })).toBeVisible();
      await expect(main.locator("[data-person-card]")).toHaveCount(2);
      await expect(main.getByRole("button", { name: "Add person" })).toHaveCount(0);
      await snap(page, testInfo, `people-390-${colorScheme}-member`);

      await page.goto("/dashboard/people/p1");
      await expect(main.getByRole("heading", { level: 2, name: "Nora Sims" })).toBeVisible();
      await main.getByRole("button", { name: "Actions for Nora Sims" }).click();
      await expect(page.getByRole("menuitem", { name: "Edit" })).toBeVisible();
      await expect(page.getByRole("menuitem", { name: "Delete" })).toHaveCount(0);
    });

    test("1440: the list is a table and the person's page is two columns", async ({
      loggedInPage: page,
    }, testInfo) => {
      onlyOn("desktop");
      await page.clock.setFixedTime(new Date("2026-11-18T15:00:00Z"));
      await page.setViewportSize({ width: 1440, height: 900 });
      await stubPeopleApi(page);
      await page.goto("/dashboard/people");
      const main = page.locator("main");
      await expect(main.getByRole("heading", { level: 2, name: /People/ })).toBeVisible();

      const table = main.locator("[data-person-table]");
      await expect(table).toBeVisible();
      await expect(main.locator("[data-person-card]").first()).toBeHidden();
      await expect(table).toContainText("peanuts, tree nuts");
      // The first cell is a real link, so keyboard and middle-click work.
      await expect(table.getByRole("link", { name: "Nora Sims" })).toHaveAttribute("href", "/dashboard/people/p1");
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `people-1440-${colorScheme}`, true);

      await page.goto("/dashboard/people/p1");
      await expect(main.getByRole("heading", { level: 2, name: "Nora Sims" })).toBeVisible();
      // Two columns from 1024 px: the phone-only affordances are gone.
      await expect(main.getByRole("button", { name: /^Resources/ })).toBeHidden();
      await expect(main.getByRole("link", { name: "Back to People" })).toBeVisible();
      await expect(main.getByRole("button", { name: "Edit", exact: true })).toBeVisible();
      await expect(main.locator("[data-person-allergies]")).toBeVisible();
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `person-1440-${colorScheme}`, true);
    });
  });
}
