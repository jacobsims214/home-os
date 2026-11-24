import type { Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { test, expect, expectNoHorizontalOverflow } from "./fixtures";

/**
 * The nav drawer's search box (#5303).
 *
 * WHAT THIS SPEC IS FOR. War game R2 (Amber, G4): "open nav drawer, tap Search,
 * type mortgage, wait 8 s: nothing happens." Until this ticket the box was a
 * submit-only form — `onSubmit` navigated, and nothing listened to the text — so
 * typing fired no request, drew no drop-down and gave no answer. The first test
 * below is the regression test for exactly that: it counts the requests the box
 * makes while a person types, so a build that goes back to listening only to
 * Enter fails it. (The ticket asked for this to fail on the old behaviour: on
 * 89b6c4e it does — zero requests.)
 *
 * The rest of the file is the other half of the rule: no silent states. Empty,
 * no-results and a failed read each say something, and the full page is still
 * one tap away for a phone with no Enter key.
 *
 * Everything is stubbed with page.route, so each payload is exact: the search
 * envelope is the real shape `GET /api/v1/search` returns
 * ({entity_type, entity_id, title, body, score}).
 */

const IPHONE = "iphone-390";

test.beforeEach(({}, testInfo) => {
  test.skip(testInfo.project.name !== IPHONE, `drawer-search.spec.ts runs on the iPhone project (${IPHONE}) only`);
});

const HOUSEHOLDS = {
  data: [
    { id: "h1", name: "Sims", timezone: "America/New_York", role: "owner", is_default: true, current: true },
  ],
};

/** The answer to "mortgage": a bill, with a note, so grouping is visible. */
const MORTGAGE = {
  data: {
    query: "mortgage",
    total: 2,
    results: [
      { entity_type: "bill", entity_id: "b9", title: "Rocket Mortgage", body: "mortgage", score: 0.93 },
      { entity_type: "note", entity_id: "n9", title: "Refinance offer", body: "The mortgage broker called about refinancing.", score: 0.71 },
    ],
  },
};

const EMPTY = { data: { query: "mortgage", total: 0, results: [] } };

const BILL = {
  data: {
    id: "b9",
    name: "Rocket Mortgage",
    amount: "2410.0000",
    category: "mortgage",
    frequency: "monthly",
    due_day: 1,
    due_date: null,
    paid_date: null,
    paid_for_due_date: null,
  },
};

const NOTE = {
  data: {
    id: "n9",
    title: "Refinance offer",
    body: "The mortgage broker called about refinancing.",
    author_name: "Amber Sims",
    entity_type: "property",
    created_at: "2026-11-02T12:00:00Z",
    updated_at: "2026-11-02T12:00:00Z",
  },
};

/** Stubs the search endpoint and the records its rows are completed from. */
async function stubLedger(page: Page, envelope: unknown) {
  await page.route("**/bff/me/households", (route) => route.fulfill({ json: HOUSEHOLDS }));
  await page.route("**/bff/search**", (route) => route.fulfill({ json: envelope }));
  await page.route("**/bff/bills/b9", (route) => route.fulfill({ json: BILL }));
  await page.route("**/bff/notes/n9", (route) => route.fulfill({ json: NOTE }));
}

/** Opens the drawer and returns it. */
async function openDrawer(page: Page) {
  await page.goto("/dashboard");
  await page.locator("header").getByRole("button", { name: "Open menu" }).click();
  const drawer = page.getByRole("dialog", { name: /Home OS menu/ });
  await expect(drawer).toBeVisible();
  return drawer;
}

test("typing in the drawer search asks the API and shows results — not a box that does nothing", async ({
  loggedInPage: page,
}) => {
  const queries: string[] = [];
  await stubLedger(page, MORTGAGE);
  // Re-stub search to count: a request per PAUSE, not per keystroke.
  await page.route("**/bff/search**", (route) => {
    queries.push(new URL(route.request().url()).searchParams.get("q") ?? "");
    return route.fulfill({ json: MORTGAGE });
  });

  const drawer = await openDrawer(page);
  const box = drawer.getByRole("searchbox", { name: "Search" });

  // THE OLD BUG, asserted first because it is the ticket: 8 characters typed by
  // a person, and the box answered with NOTHING — no request, no drop-down. On
  // 89b6c4e (submit-only) this poll times out with queries === [].
  await box.click();
  await box.pressSequentially("mortgage", { delay: 60 });
  await expect.poll(() => queries.length, { message: "typing must ask the API" }).toBeGreaterThan(0);

  // DEBOUNCED: one request for the pause after "mortgage", not one per letter.
  expect(queries, "one request per pause").toEqual(["mortgage"]);

  const panel = drawer.locator("[data-search-panel]");
  await expect(panel).toBeVisible();
  await expect(page.locator('[data-result-group="bill"] [data-result-title]')).toHaveText("Rocket Mortgage");

  // THE SAME ROWS AS THE PAGE: the bill's amount and due state come from its
  // own record, exactly as the Search page completes it (#4561's rule).
  const billGroup = page.locator('[data-result-group="bill"]');
  await expect(billGroup).toContainText("1 bill");
  await expect(billGroup.locator("[data-result-facts]")).toContainText("$2,410.00");
  await expect(page.locator('[data-result-group="note"]')).toContainText("1 note");

  // The drawer's nav is still reachable under the panel, and nothing overflows.
  await expect(drawer.getByRole("link", { name: "Today" })).toBeVisible();
  await expectNoHorizontalOverflow(page);

  await page.screenshot({ path: "e2e/.results/drawer-search-results.png" });
});

test("a phone can always submit: the Search button opens the full page", async ({ loggedInPage: page }) => {
  await stubLedger(page, MORTGAGE);
  const drawer = await openDrawer(page);
  const box = drawer.getByRole("searchbox", { name: "Search" });
  await box.click();
  await box.pressSequentially("mortgage", { delay: 30 });
  await expect(drawer.locator("[data-search-panel]")).toBeVisible();

  // No Enter key on a phone's keyboard: the button IS the submit.
  const submit = drawer.getByRole("button", { name: "Search everything" });
  await expect(submit).toBeVisible();
  const size = await submit.boundingBox();
  expect(Math.round(size!.height), "44 px tap target").toBeGreaterThanOrEqual(44);
  expect(Math.round(size!.width)).toBeGreaterThanOrEqual(44);

  await submit.click();
  await expect(page).toHaveURL(/\/dashboard\/search\?q=mortgage$/);
  // The drawer closes on navigation, so the page is not left under a scrim.
  await expect(page.getByRole("dialog", { name: /Home OS menu/ })).toHaveCount(0);
});

test("Enter in the box still submits the form (the keyboard path)", async ({ loggedInPage: page }) => {
  await stubLedger(page, MORTGAGE);
  const drawer = await openDrawer(page);
  const box = drawer.getByRole("searchbox", { name: "Search" });
  await box.click();
  await box.pressSequentially("mortgage", { delay: 30 });
  await box.press("Enter");
  await expect(page).toHaveURL(/\/dashboard\/search\?q=mortgage$/);
});

test("a query with no hits says so in the drawer — silence is never a state", async ({ loggedInPage: page }) => {
  await stubLedger(page, EMPTY);
  const drawer = await openDrawer(page);
  const box = drawer.getByRole("searchbox", { name: "Search" });
  await box.click();
  await box.pressSequentially("mortgage", { delay: 30 });

  const panel = drawer.locator("[data-search-panel]");
  await expect(panel).toBeVisible();
  // NOT a blank panel, and not a generic "no results": the query is named back
  // (so a typo is visible) and the cure is offered.
  await expect(panel.locator('[data-search-state="no-results"]')).toContainText('Nothing matched "mortgage"');
  await expect(panel.locator('[data-search-state="no-results"]')).toContainText("Try fewer words");
  // The panel is ANNOUNCED, not just drawn: the live status line carries the
  // same sentence a sighted person reads. (Not aria-describedby on the input —
  // Mantine overwrites that; see the note in SidebarSearch.tsx.)
  await expect(panel.locator("[data-search-status]")).toHaveText(/Nothing matched "mortgage"/);
});

test("a failed search says it failed and offers Try again — never reads as no matches", async ({
  loggedInPage: page,
}) => {
  await page.route("**/bff/me/households", (route) => route.fulfill({ json: HOUSEHOLDS }));
  let attempts = 0;
  await page.route("**/bff/search**", (route) => {
    attempts += 1;
    if (attempts === 1) return route.fulfill({ status: 500, body: "" });
    return route.fulfill({ json: MORTGAGE });
  });
  await page.route("**/bff/bills/b9", (route) => route.fulfill({ json: BILL }));
  await page.route("**/bff/notes/n9", (route) => route.fulfill({ json: NOTE }));

  const drawer = await openDrawer(page);
  const box = drawer.getByRole("searchbox", { name: "Search" });
  await box.click();
  await box.pressSequentially("mortgage", { delay: 30 });

  const panel = drawer.locator("[data-search-panel]");
  const failure = panel.locator('[data-search-state="error"]');
  await expect(failure).toBeVisible();
  await expect(failure).toContainText("Search didn’t answer");
  await expect(failure).not.toContainText("Nothing matched");

  // Try again really re-runs it, and the answer lands in the same panel.
  await panel.getByRole("button", { name: "Try again" }).click();
  await expect(panel.locator('[data-result-group="bill"]')).toContainText("Rocket Mortgage");
  expect(attempts, "the retry made a second request").toBe(2);
});

test("an untouched box invites a search and says what search covers", async ({ loggedInPage: page }) => {
  await stubLedger(page, MORTGAGE);
  const drawer = await openDrawer(page);
  const box = drawer.getByRole("searchbox", { name: "Search" });
  await box.click();

  const panel = drawer.locator("[data-search-panel]");
  await expect(panel).toBeVisible();
  const idle = panel.locator('[data-search-state="idle"]');
  // The areas are the invitation — never an empty white rectangle.
  await expect(idle).toContainText("documents");
  await expect(idle).toContainText("bills");
  await box.fill("");
  await expect(panel.locator('[data-search-state="idle"]')).toBeVisible();
});

test("clearing the box goes back to idle at once, and the nav was never blocked", async ({ loggedInPage: page }) => {
  await stubLedger(page, MORTGAGE);
  const drawer = await openDrawer(page);
  const box = drawer.getByRole("searchbox", { name: "Search" });
  await box.click();
  await box.pressSequentially("mortgage", { delay: 30 });
  await expect(drawer.locator('[data-result-group="bill"]')).toBeVisible();

  await box.fill("");
  await expect(drawer.locator('[data-search-panel] [data-search-state="idle"]')).toBeVisible();

  // The drawer is still a drawer: a nav row navigates and closes it.
  await drawer.getByRole("link", { name: "Calendar" }).click();
  await expect(page).toHaveURL(/\/dashboard\/calendar$/);
});

test("the panel has no critical or serious accessibility violations", async ({ loggedInPage: page }) => {
  await stubLedger(page, MORTGAGE);
  const drawer = await openDrawer(page);
  const box = drawer.getByRole("searchbox", { name: "Search" });
  await box.click();
  await box.pressSequentially("mortgage", { delay: 30 });
  await expect(page.locator('[data-result-group="bill"]')).toBeVisible();

  // Axe over the whole page while the drawer and its results are open — the
  // state the ticket is about. The rows are asserted here because it is the
  // easy place to get wrong: a row's accessible name must come from its own
  // visible text (title + facts), not from an aria-label that omits them
  // (WCAG 2.5.3 — the failure the sidebar's account button had, #4752).
  const { violations } = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  const bad = violations
    .filter((v) => v.impact === "critical" || v.impact === "serious")
    .map((v) => `${v.impact} ${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(" | ")}`);
  expect(bad, "critical/serious axe violations").toEqual([]);

  // The bill row is named by what it shows, so the amount is spoken too.
  const billRow = page.locator('[data-result-group="bill"] [data-result-row]');
  await expect(billRow).toHaveAccessibleName(/Rocket Mortgage[\s\S]*\$2,410\.00/);
});
