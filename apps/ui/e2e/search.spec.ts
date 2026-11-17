import type { Page } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow } from "./fixtures";

/**
 * Search (#4561): one box, results grouped by type, every row with context —
 * on a phone.
 *
 * Runs on the iPhone project only (WebKit + isMobile + hasTouch, 390 px), which
 * is the viewport this page is built for: the parent who needs "electric" is
 * standing in a kitchen holding a phone. The two tests the ticket names are
 * here — grouped results render, and the no-results state — plus the two
 * differences that keep this page honest: an empty box is not a failed search,
 * and a role never sees a section it cannot search.
 *
 * The API is stubbed per test with page.route, so each payload is exact: the
 * search envelope is the real shape `GET /api/v1/search` returns
 * ({entity_type, entity_id, title, body, score}) and the per-record reads are
 * the real detail routes the page completes rows from.
 */

const IPHONE = "iphone-390";

test.beforeEach(async ({}, testInfo) => {
  test.skip(
    testInfo.project.name !== IPHONE,
    `search.spec.ts runs on the iPhone project (${IPHONE}) only`,
  );
});

const HOUSEHOLDS = {
  data: [
    { id: "h1", name: "Sims", timezone: "America/New_York", role: "owner", is_default: true, current: true },
  ],
};

/** The one endpoint under test: a grouped, semantic search answer. */
const SEARCH = {
  data: {
    query: "electric",
    total: 4,
    results: [
      { entity_type: "bill", entity_id: "b1", title: "Duke Energy", body: "utilities", score: 0.91 },
      // dayjs-style note hit: the body carries the matched words.
      { entity_type: "note", entity_id: "n1", title: "Kitchen lights flicker", body: "Asked Mike about the electric panel on the north wall.", score: 0.77 },
      { entity_type: "document", entity_id: "f1", title: "Duke Energy statement, October", body: "Category: utilities. Dated 2026-10-04", score: 0.74, file_id: "f1" },
      { entity_type: "vendor", entity_id: "v1", title: "Mike's Plumbing", body: "plumber", score: 0.61 },
    ],
  },
};

const BILL = {
  data: {
    id: "b1",
    name: "Duke Energy",
    amount: "182.4000",
    category: "utilities",
    frequency: "monthly",
    due_day: 12,
    due_date: null,
    paid_date: null,
    paid_for_due_date: null,
  },
};
const NOTE = {
  data: {
    id: "n1",
    title: "Kitchen lights flicker",
    body: "Asked Mike about the electric panel on the north wall.",
    author_name: "Amber Sims",
    entity_type: "property",
    created_at: "2026-11-02T12:00:00Z",
    updated_at: "2026-11-02T12:00:00Z",
  },
};
const FILE = {
  data: {
    id: "f1",
    household_id: "h1",
    blob_id: "bl1",
    name: "duke-oct.pdf",
    content_type: "application/pdf",
    size: 84213,
    ocr_status: "done",
    tags: ["utilities"],
    category: "utilities",
    document_date: "2026-10-04",
    uploaded_by_name: "Jacob Sims",
    created_at: "2026-10-04T12:00:00Z",
    updated_at: "2026-10-04T12:00:00Z",
  },
};
const VENDOR = {
  data: { id: "v1", name: "Mike's Plumbing", trades: ["plumber"], phone: "(919) 555-0142" },
};

async function stubSearch(page: Page, envelope: unknown) {
  await page.route("**/bff/search**", (route) => route.fulfill({ json: envelope }));
  await page.route("**/bff/me/households", (route) => route.fulfill({ json: HOUSEHOLDS }));
  await page.route("**/bff/bills/b1", (route) => route.fulfill({ json: BILL }));
  await page.route("**/bff/notes/n1", (route) => route.fulfill({ json: NOTE }));
  await page.route("**/bff/files/f1/content", (route) => route.fulfill({ status: 404, body: "" }));
  await page.route("**/bff/files/f1", (route) => route.fulfill({ json: FILE }));
  await page.route("**/bff/vendors/v1", (route) => route.fulfill({ json: VENDOR }));
}

test("a search renders its results grouped by type, each row with the context to judge it", async ({
  loggedInPage: page,
}) => {
  await stubSearch(page, SEARCH);
  await page.goto("/dashboard/search?q=electric");

  const results = page.getByTestId("search-page").locator("[data-search-results]");
  await expect(results).toBeVisible();

  // GROUPED, NOT ONE LIST: each type has its own heading and its own card.
  const groups = page.locator("[data-result-group]");
  await expect(groups).toHaveCount(4);
  await expect(page.locator('[data-result-group="bill"] h3')).toHaveText("Bills");
  await expect(page.locator('[data-result-group="note"] h3')).toHaveText("Notes");
  await expect(page.locator('[data-result-group="note"]')).toContainText("1 note");
  await expect(page.locator('[data-result-group="document"] h3')).toHaveText("Documents");
  await expect(page.locator('[data-result-group="contact"] h3')).toHaveText("Contacts");

  // EVERY ROW CARRIES CONTEXT. The bill's row names the amount and its due
  // state — that is what makes "is this the electric bill I mean?" answerable
  // without opening it. The endpoint sends neither; the row's own record does.
  const billRow = page.locator('[data-result-group="bill"] [data-result-row]');
  await expect(billRow.locator("[data-result-title]")).toHaveText("Duke Energy");
  const billFacts = billRow.locator("[data-result-facts]");
  await expect(billFacts).toContainText("$182.40");
  await expect(billFacts).toContainText("Due");
  // A BILL'S STATE IS NEVER READ FROM A CALENDAR EVENT (R1's false "still
  // unpaid"): it is computed from the bill's own due rule, so with no payment
  // recorded it says "Due", never "Paid".
  await expect(billFacts).not.toContainText("Paid");

  // A document row knows its category, its date and who uploaded it.
  const docRow = page.locator('[data-result-group="document"] [data-result-row]');
  const docFacts = docRow.locator("[data-result-facts]");
  await expect(docFacts).toContainText("Utilities");
  await expect(docFacts).toContainText("Oct 4, 2026");
  await expect(docFacts).toContainText("Jacob Sims");

  // A note row names its author — two co-parents' notes are not interchangeable.
  const noteFacts = page.locator('[data-result-group="note"] [data-result-facts]');
  await expect(noteFacts).toContainText("Amber Sims");
  await expect(noteFacts).toContainText("On a property");

  // A snippet that only repeats the row's facts is not printed twice: the
  // bill's index body is its category, which the row already says.
  await expect(page.locator('[data-result-group="bill"] [data-result-snippet]')).toHaveCount(0);

  // Every group is drawn under the summary of what was searched.
  await expect(page.locator("[data-search-summary]")).toContainText('4 results for “electric”');
  await expect(page.locator("[data-search-summary]")).toContainText("in 4 areas");

  await expectNoHorizontalOverflow(page);
  await page.screenshot({ path: "e2e/.results/search-grouped.png", fullPage: true });

  // Dark mode is not an afterthought: the page is built from the token aliases
  // (--ink, --surface, --line, --accent-soft), so both schemes come out
  // readable and there is nothing to add per scheme.
  await page.emulateMedia({ colorScheme: "dark" });
  await expect
    .poll(() => page.evaluate(() => document.documentElement.getAttribute("data-mantine-color-scheme")))
    .toBe("dark");
  await page.screenshot({ path: "e2e/.results/search-grouped-dark.png", fullPage: true });
  await expectNoHorizontalOverflow(page);
});

test("narrowing to one area shows only that area", async ({ loggedInPage: page }) => {
  await stubSearch(page, SEARCH);
  await page.goto("/dashboard/search?q=electric");

  const filters = page.locator("[data-search-filters]");
  await expect(filters).toBeVisible();
  await expect(filters.locator('[data-search-filter="bill"]')).toBeVisible();

  // Mantine's chip keeps its checkbox input visually hidden and makes the
  // label the tap target, so tap the label the way a person does.
  await filters.getByText("Bills", { exact: true }).click();
  await expect(page.locator("[data-result-group]")).toHaveCount(1);
  await expect(page.locator('[data-result-group="bill"]')).toBeVisible();
  await expect(page.locator('[data-result-group="note"]')).toHaveCount(0);

  // "All" puts every area back.
  await filters.getByText("All", { exact: true }).click();
  await expect(page.locator("[data-result-group]")).toHaveCount(4);

  await expectNoHorizontalOverflow(page);
});

test("tapping a result opens the record", async ({ loggedInPage: page }) => {
  await stubSearch(page, SEARCH);
  await page.goto("/dashboard/search?q=electric");

  // The bill has a page of its own.
  await expect(page.locator('[data-result-group="bill"] a[data-result-row]')).toHaveAttribute(
    "href",
    "/dashboard/bills/b1",
  );

  // The note has none, so the row opens the note itself — not a dead tap.
  await page.locator('[data-result-group="note"] [data-result-row]').click();
  const modal = page.locator(".mantine-Modal-content");
  await expect(modal).toBeVisible();
  await expect(modal).toContainText("Kitchen lights flicker");
  await expect(modal).toContainText("Asked Mike about the electric panel");
  await expect(modal).toContainText("Amber Sims");
});

test("a query with no hits says so, and names what it searched", async ({ loggedInPage: page }) => {
  await stubSearch(page, { data: { query: "xylophone lessons", total: 0, results: [] } });
  await page.goto("/dashboard/search?q=xylophone%20lessons");

  // NOT A BLANK PANEL, and not a generic "no results": the query is named back
  // (so a typo is visible) and the cure is suggested.
  const panel = page.locator('[data-search-state="no-results"]');
  await expect(panel).toBeVisible();
  await expect(panel).toContainText('Nothing matched "xylophone lessons"');
  await expect(panel).toContainText("Try fewer words");

  // What search covers is stated for THIS role, so the answer is a claim about
  // the household rather than about the box.
  await expect(panel).toContainText("documents");
  await expect(panel).toContainText("bills");
  await expect(page.locator("[data-search-results]")).toHaveCount(0);

  // "Try fewer words" actually re-runs the search with fewer words.
  let lastQuery = "";
  await page.route("**/bff/search**", (route) => {
    lastQuery = new URL(route.request().url()).searchParams.get("q") ?? "";
    route.fulfill({ json: { data: { query: lastQuery, total: 0, results: [] } } });
  });
  await page.locator("[data-search-fewer]").click();
  await expect.poll(() => lastQuery).toBe("xylophone");
  await expect(page.locator('[data-search-state="no-results"]')).toContainText('Nothing matched "xylophone"');

  await page.screenshot({ path: "e2e/.results/search-no-results.png", fullPage: true });
});

test("an untouched box says type to search — it is not a failed search", async ({ loggedInPage: page }) => {
  await stubSearch(page, { data: { query: "", total: 0, results: [] } });
  await page.goto("/dashboard/search");

  const idle = page.locator('[data-search-state="idle"]');
  await expect(idle).toBeVisible();
  await expect(idle).toContainText("Type to search");
  // The areas are the invitation: a person learns what is searchable before
  // typing, and never sees an empty white page.
  await expect(page.locator("[data-search-area]").first()).toBeVisible();
  await expect(page.locator('[data-search-state="no-results"]')).toHaveCount(0);
  await expect(page.locator("[data-search-results]")).toHaveCount(0);

  await page.screenshot({ path: "e2e/.results/search-idle.png", fullPage: true });
});

test("a housesitter is never shown a section they cannot search, nor told one is hidden", async ({
  loggedInPage: page,
}) => {
  // The API filters per hit (#4696), so a housesitter's answer cannot contain a
  // bill. This is the UI half: no Bills heading, no count that implies more
  // rows exist, and the copy only names the areas the role may search.
  await page.route("**/bff/me/households", (route) =>
    route.fulfill({
      json: { data: [{ id: "h1", name: "Sims", timezone: "America/New_York", role: "housesitter", is_default: true, current: true }] },
    }),
  );
  await page.route("**/bff/search**", (route) =>
    route.fulfill({
      json: {
        data: {
          query: "electric",
          total: 1,
          // Only what a housesitter may read can come back: a note on a property.
          results: [{ entity_type: "note", entity_id: "n1", title: "Kitchen lights flicker", body: "the electric panel", score: 0.8 }],
        },
      },
    }),
  );
  await page.route("**/bff/notes/n1", (route) => route.fulfill({ json: NOTE }));
  await page.goto("/dashboard/search?q=electric");

  await expect(page.locator('[data-result-group="note"]')).toBeVisible();
  for (const hidden of ["bill", "document", "credential"]) {
    await expect(page.locator(`[data-result-group="${hidden}"]`)).toHaveCount(0);
  }
  // Nothing in the page's own text suggests anything is being withheld.
  const body = await page.getByTestId("search-page").innerText();
  expect(body).not.toMatch(/hidden|withheld|\bmore results\b/i);
  await expect(page.locator("[data-search-summary]")).toContainText("1 result for");

  await page.screenshot({ path: "e2e/.results/search-housesitter.png", fullPage: true });
});
