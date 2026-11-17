import type { Page, TestInfo } from "@playwright/test";
import { test, expect, onlyOn } from "./fixtures";

/**
 * Files and notes on an entity detail page (#4564).
 *
 * The three things the ticket says must work on a phone, each as its own test:
 *   1. adding a note (and reading a full one),
 *   2. the near-duplicate prompt appearing when the API returns candidates
 *      (#4945's rule, on the API since #4564) — and the note still being saved,
 *   3. a role without access not seeing the section at all.
 * Plus the provenance marker: a re-typed body must not read as the original
 * document (#4941).
 *
 * The API is stubbed with page.route, so these run without the Go stack. The
 * pages, the components and the role decision are the real code.
 */

const BILL = {
  id: "b1",
  name: "Duke Energy electric",
  amount: 142.37,
  due_day: 14,
  category: "Electric",
  notes: null,
};
const DETAIL = "/dashboard/bills/b1";

/** Answers GET /bff/me/households as a member with `role`. */
async function asRole(page: Page, role: string) {
  await page.route("**/bff/me/households", (route) =>
    route.fulfill({
      json: {
        data: [
          { id: "h1", name: "Sims", timezone: "UTC", role, joined_at: "2026-01-01T00:00:00Z", is_default: true, current: true },
        ],
      },
    }),
  );
}

const NOTE = {
  id: "n1",
  household_id: "h1",
  entity_type: "bill",
  entity_id: "b1",
  title: "Budget billing",
  body: "Budget billing since March. Call 800-777-9898 if the autopay fails twice.",
  author_id: "u1",
  author_name: "Amber Sims",
  created_at: "2026-11-16T12:00:00Z",
  updated_at: "2026-11-16T12:00:00Z",
};

const FILE = {
  id: "f1",
  household_id: "h1",
  blob_id: "bl1",
  name: "duke-energy-statement.pdf",
  content_type: "application/pdf",
  size: 184320,
  entity_type: "bill",
  entity_id: "b1",
  ocr_status: "done",
  created_at: "2026-11-15T09:00:00Z",
  updated_at: "2026-11-15T09:00:00Z",
  text_origin: "extracted",
  uploaded_by_name: "Jacob Sims",
};

/** Stubs the bill, its files and its notes. `notes` may be a function. */
async function stubEntity(
  page: Page,
  opts: {
    files?: unknown[];
    notes?: unknown[];
    onCreateNote?: (body: Record<string, unknown>) => { status?: number; json: unknown };
  } = {},
) {
  await page.route("**/bff/bills/b1", (route) => route.fulfill({ json: { data: BILL } }));
  await page.route("**/bff/files?**", (route) => route.fulfill({ json: { data: opts.files ?? [] } }));
  await page.route("**/bff/notes?**", (route) => route.fulfill({ json: { data: opts.notes ?? [] } }));
  await page.route("**/bff/notes", async (route) => {
    if (route.request().method() !== "POST") return route.fulfill({ json: { data: [] } });
    const body = route.request().postDataJSON() as Record<string, unknown>;
    const out = opts.onCreateNote?.(body) ?? { json: { data: { ...NOTE, ...body }, possible_duplicates: [] } };
    return route.fulfill({ status: out.status ?? 201, json: out.json });
  });
}

/**
 * Opens the Resources section and selects a tab. Resources starts closed on a
 * phone (the toggle is the heading) and the Files tab is active by default, so
 * both steps are needed before a notes assertion.
 */
async function openResources(page: Page, tab: "Files" | "Notes" = "Files") {
  await page.goto(DETAIL);
  await expect(page.getByRole("heading", { level: 2, name: BILL.name })).toBeVisible();
  const toggle = page.locator("main").getByRole("button", { name: /^Resources/ });
  await expect(toggle).toBeVisible();
  if ((await toggle.getAttribute("aria-expanded")) === "false") await toggle.click();
  const tabButton = page.getByRole("tab", { name: tab });
  if (await tabButton.count()) await tabButton.click();
}

async function snap(page: Page, testInfo: TestInfo, name: string) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage: true });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

const noHorizontalScroll = (page: Page) =>
  page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);

// ─── 1. Adding a note, and reading a full one ─────────────────

test.describe("notes on a phone", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test.beforeEach(() => onlyOn("phone"));

  test("add a note: the form saves it and the list shows the author and date", async ({ loggedInPage: page }, testInfo) => {
    await asRole(page, "family_member");
    const posts: Record<string, unknown>[] = [];
    await stubEntity(page, {
      notes: [NOTE],
      onCreateNote: (body) => {
        posts.push(body);
        return { json: { data: { ...NOTE, ...body, id: "n2" }, possible_duplicates: [] } };
      },
    });
    await openResources(page, "Notes");

    // The existing note names its author: the war game's point.
    const list = page.getByTestId("note-list");
    await expect(list.getByText("Amber Sims")).toBeVisible();
    await expect(list.getByText("Nov 16, 2026")).toBeVisible();

    await page.getByRole("button", { name: "Add note" }).click();
    const form = page.getByTestId("note-form");
    await expect(form).toBeVisible();
    await form.getByPlaceholder(/Ava's school/).fill("Water heater");
    await form.getByPlaceholder(/What should the family know/).fill("Flushed in March and October.");
    // Phone: the inputs are 16 px so iOS does not zoom on focus.
    expect(await form.getByPlaceholder(/What should the family know/).evaluate((el) => getComputedStyle(el).fontSize)).toBe("16px");
    await form.getByRole("button", { name: "Save note" }).click();

    await expect.poll(() => posts.length).toBe(1);
    expect(posts[0]).toMatchObject({
      entity_type: "bill",
      entity_id: "b1",
      title: "Water heater",
      body: "Flushed in March and October.",
    });
    // No candidates: no prompt.
    await expect(page.getByTestId("note-duplicate-prompt")).toHaveCount(0);
    expect(await noHorizontalScroll(page)).toBe(true);
    await snap(page, testInfo, "notes-390-added");
  });

  test("a long note opens whole, with no truncation", async ({ loggedInPage: page }, testInfo) => {
    await asRole(page, "family_member");
    const long = Array.from({ length: 12 }, (_, i) => `Line ${i + 1}: the side door code changes on the first of the month.`).join("\n");
    await stubEntity(page, { notes: [{ ...NOTE, body: long }] });
    await openResources(page, "Notes");

    await page.getByRole("button", { name: "Open", exact: true }).click();
    const view = page.getByTestId("note-view");
    await expect(view).toBeVisible();
    const body = view.getByTestId("note-view-body");
    await expect(body).toContainText("Line 12");
    // The whole body is there, not a clamped preview.
    expect((await body.textContent())?.length).toBeGreaterThan(600);
    expect(await noHorizontalScroll(page)).toBe(true);
    await snap(page, testInfo, "notes-390-full-view");
  });
});

// ─── 2. The near-duplicate prompt ─────────────────────────────

test.describe("near-duplicate prompt", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test.beforeEach(() => onlyOn("phone"));

  test("the API's candidates are shown as a question, and the note is still saved", async ({ loggedInPage: page }, testInfo) => {
    await asRole(page, "family_member");
    const existing = {
      ...NOTE,
      id: "n-old",
      title: "Ava's school — teacher & pickup rules",
      body: "Mrs. Patel. 2:50 car line. Green tag, front office.",
    };
    await stubEntity(page, {
      notes: [existing],
      onCreateNote: (body) => ({
        json: {
          data: { ...NOTE, ...body, id: "n-new" },
          possible_duplicates: [
            {
              id: "n-old",
              title: "Ava's school — teacher & pickup rules",
              updated_at: "2026-11-16T12:00:00Z",
              updated_local_date: "2026-11-16",
              excerpt: "Mrs. Patel. 2:50 car line. Green tag, front office.",
              title_similarity: 0.8,
            },
          ],
        },
      }),
    });
    await openResources(page, "Notes");

    await page.getByRole("button", { name: "Add note" }).click();
    const form = page.getByTestId("note-form");
    await form.getByPlaceholder(/Ava's school/).fill("Ava's school: teacher & pickup");
    await form.getByPlaceholder(/What should the family know/).fill("Ms. Delgado. 3:15 side door. Nut allergy, EpiPen in the office.");
    await form.getByRole("button", { name: "Save note" }).click();

    // The prompt appears, names the twin, and offers both choices.
    const prompt = page.getByTestId("note-duplicate-prompt");
    await expect(prompt).toBeVisible();
    await expect(prompt).toContainText("This looks like an existing note");
    await expect(prompt.getByText("Ava's school — teacher & pickup rules")).toBeVisible();
    await expect(prompt.getByText(/80% similar/)).toBeVisible();
    await expect(prompt.getByText(/Mrs. Patel/)).toBeVisible();
    await expect(prompt.getByRole("button", { name: "Keep both" })).toBeVisible();
    await expect(prompt.getByRole("button", { name: "Read that note" })).toBeVisible();
    // It says plainly that nothing was merged or deleted.
    await expect(prompt).toContainText("Nothing was merged or deleted");
    // The form closed: the note was saved, not rejected.
    await expect(page.getByTestId("note-form")).toHaveCount(0);
    expect(await noHorizontalScroll(page)).toBe(true);
    await snap(page, testInfo, "notes-390-duplicate-prompt");

    // "Read that note" opens the existing one so the two can be compared.
    await prompt.getByRole("button", { name: "Read that note" }).click();
    await expect(page.getByTestId("note-view")).toBeVisible();
    await expect(page.getByTestId("note-view-body")).toContainText("Mrs. Patel");
  });

  test("a distinct title shows no prompt", async ({ loggedInPage: page }) => {
    await asRole(page, "family_member");
    await stubEntity(page, {
      notes: [NOTE],
      onCreateNote: (body) => ({ json: { data: { ...NOTE, ...body, id: "n2" }, possible_duplicates: [] } }),
    });
    await openResources(page, "Notes");
    await page.getByRole("button", { name: "Add note" }).click();
    const form = page.getByTestId("note-form");
    await form.getByPlaceholder(/Ava's school/).fill("Water heater");
    await form.getByPlaceholder(/What should the family know/).fill("Flushed in March.");
    await form.getByRole("button", { name: "Save note" }).click();
    await expect(page.getByTestId("note-form")).toHaveCount(0);
    await expect(page.getByTestId("note-duplicate-prompt")).toHaveCount(0);
  });
});

// ─── 3. A role without access sees no section ─────────────────

test.describe("role policy", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test.beforeEach(() => onlyOn("phone"));

  test("a housesitter on a bill sees no Resources section at all", async ({ loggedInPage: page }, testInfo) => {
    await asRole(page, "housesitter");
    await stubEntity(page, { files: [FILE], notes: [NOTE] });
    await page.goto(DETAIL);
    await expect(page.getByRole("heading", { level: 2, name: BILL.name })).toBeVisible();

    // No Resources toggle, no tab bar, no Files/Notes/Secrets heading.
    await expect(page.locator("main").getByRole("button", { name: /^Resources/ })).toHaveCount(0);
    await expect(page.getByTestId("entity-resources")).toHaveCount(0);
    await expect(page.getByRole("tablist", { name: "Resource types" })).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "Files" })).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "Notes" })).toHaveCount(0);
    // Details is still there: the page itself is readable.
    await expect(page.locator("main").getByRole("button", { name: /^Details/ })).toBeVisible();
    await snap(page, testInfo, "notes-390-housesitter-no-section");
  });

  test("a vendor sees no Resources section either", async ({ loggedInPage: page }) => {
    await asRole(page, "vendor");
    await stubEntity(page, { files: [FILE], notes: [NOTE] });
    await page.goto(DETAIL);
    await expect(page.getByRole("heading", { level: 2, name: BILL.name })).toBeVisible();
    await expect(page.getByTestId("entity-resources")).toHaveCount(0);
    await expect(page.locator("main").getByRole("button", { name: /^Resources/ })).toHaveCount(0);
  });

  test("a viewer reads files and notes but gets no upload, delete or Secrets tab", async ({ loggedInPage: page }, testInfo) => {
    await asRole(page, "viewer");
    await stubEntity(page, { files: [FILE], notes: [NOTE] });
    await openResources(page);

    // Files tab: the list is there, the upload controls are not.
    await expect(page.getByTestId("file-row")).toHaveCount(1);
    await expect(page.getByRole("button", { name: "Upload file" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Take photo" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: /^Delete / })).toHaveCount(0);
    // A viewer still reads a note whole (Open is a read action, not a write).
    await page.getByRole("tab", { name: "Notes" }).click();
    await expect(page.getByRole("button", { name: "Open", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Add note" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Delete" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: /^Edit note/ })).toHaveCount(0);
    await page.getByRole("tab", { name: "Files" }).click();
    // No Secrets tab for a viewer.
    await expect(page.getByRole("tab", { name: "Secrets" })).toHaveCount(0);
    await expect(page.getByRole("tab", { name: "Files" })).toBeVisible();
    await expect(page.getByRole("tab", { name: "Notes" })).toBeVisible();
    await snap(page, testInfo, "notes-390-viewer-read-only");
  });
});

// ─── 4. Provenance: a re-typed body is not the original ───────

test.describe("file text provenance", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test.beforeEach(() => onlyOn("phone"));

  test("a re-typed body is marked as changed, never as the original", async ({ loggedInPage: page }, testInfo) => {
    await asRole(page, "family_member");
    await stubEntity(page, {
      files: [
        { ...FILE, id: "f-raw", name: "copay-original.txt", text_origin: "raw", text_verified: true, extracted_text: "Well visit copay — $35.00" },
        { ...FILE, id: "f-altered", name: "copay-retyped.txt", text_origin: "raw", text_verified: false, extracted_text: "Well visit copay — 5.00" },
        { ...FILE, id: "f-ocr", name: "statement.pdf", text_origin: "extracted", extracted_text: "OCR text" },
      ],
    });
    await openResources(page);

    const rows = page.getByTestId("file-row");
    await expect(rows).toHaveCount(3);

    // The source says so; the re-typed one says it changed; the OCR one says
    // it was read from the file. None of them claims to be the original when
    // it is not.
    const markers = page.getByTestId("file-text-marker");
    await expect(markers).toHaveCount(3);
    await expect(page.locator('[data-provenance="source"]')).toContainText("Text as filed");
    await expect(page.locator('[data-provenance="altered"]')).toContainText("Text changed after filing");
    await expect(page.locator('[data-provenance="derived"]')).toContainText("Text read from file");

    // The uploader and date are on the row.
    await expect(rows.first().getByText("by Jacob Sims")).toBeVisible();
    await expect(rows.first().getByText("Nov 15, 2026")).toBeVisible();
    expect(await noHorizontalScroll(page)).toBe(true);
    await snap(page, testInfo, "notes-390-provenance");

    // Opening the altered file repeats the sentence in full.
    await rows.nth(1).getByRole("button", { name: "copay-retyped.txt", exact: true }).click();
    const notice = page.getByTestId("file-text-notice");
    await expect(notice).toBeVisible();
    await expect(notice).toHaveAttribute("data-provenance", "altered");
    await expect(notice).toContainText("no longer matches what was filed");
  });
});

// ─── 5. The same section on another entity type ───────────────

test.describe("the section is reusable across entity types", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test.beforeEach(() => onlyOn("phone"));

  test("a pet detail page gets the same Notes section, and a housesitter may add a pet note", async ({ loggedInPage: page }, testInfo) => {
    // The role policy's one narrow note grant: a housesitter may annotate a
    // pet (roles.go CanAnnotateEntity), and gets no files there.
    await asRole(page, "housesitter");
    const PET = { id: "pet1", name: "Biscuit", species: "Dog", breed: "Beagle" };
    const posts: Record<string, unknown>[] = [];
    await page.route("**/bff/pets/pet1", (route) => route.fulfill({ json: { data: PET } }));
    await page.route("**/bff/notes?**", (route) =>
      route.fulfill({
        json: {
          data: [
            {
              id: "pn1",
              entity_type: "pet",
              entity_id: "pet1",
              title: "Feeding",
              body: "Half a cup, twice a day. No table food — his stomach cannot take it.",
              author_name: "Jacob Sims",
              created_at: "2026-11-14T12:00:00Z",
            },
          ],
        },
      }),
    );
    await page.route("**/bff/notes", (route) => {
      if (route.request().method() !== "POST") return route.fulfill({ json: { data: [] } });
      const body = route.request().postDataJSON() as Record<string, unknown>;
      posts.push(body);
      return route.fulfill({ status: 201, json: { data: { id: "pn2", ...body }, possible_duplicates: [] } });
    });

    await page.goto("/dashboard/pets/pet1");
    await expect(page.getByRole("heading", { level: 2, name: "Biscuit" })).toBeVisible();
    const toggle = page.locator("main").getByRole("button", { name: /^Resources/ });
    await expect(toggle).toBeVisible();
    if ((await toggle.getAttribute("aria-expanded")) === "false") await toggle.click();

    // No Files tab (a housesitter sees no documents), no Secrets tab; Notes only.
    await expect(page.getByRole("tab", { name: "Files" })).toHaveCount(0);
    await expect(page.getByRole("tab", { name: "Secrets" })).toHaveCount(0);
    await expect(page.getByRole("tab", { name: "Notes" })).toHaveCount(0); // one tab: no bar
    await expect(page.getByText("Jacob Sims")).toBeVisible();

    // The narrow grant, exactly: add yes, edit/delete no. The API's Update and
    // Delete need ResNotes write (family), so a sitter's buttons would 403.
    // "Open" stays: reading a note whole is not a write.
    await expect(page.getByRole("button", { name: "Delete" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: /^Edit note/ })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Open", exact: true })).toBeVisible();

    await page.getByRole("button", { name: "Add note" }).click();
    const form = page.getByTestId("note-form");
    await form.getByPlaceholder(/Ava's school/).fill("Vet visit");
    await form.getByPlaceholder(/What should the family know/).fill("Rabies shot due in March.");
    await form.getByRole("button", { name: "Save note" }).click();

    await expect.poll(() => posts.length).toBe(1);
    expect(posts[0]).toMatchObject({ entity_type: "pet", entity_id: "pet1", body: "Rabies shot due in March." });
    await snap(page, testInfo, "notes-390-pet-housesitter");
  });
});
