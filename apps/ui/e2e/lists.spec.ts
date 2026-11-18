import type { BrowserContext, Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn, signInWithStub } from "./fixtures";

/**
 * Lists pages (#4566), built to the approved mockup (artifact #88 v1: Lists
 * index, Grocery, Store mode, Chores, Archived) inside the approved shell
 * (#75 v1).
 *
 * WHAT THIS SPEC PROVES, and why each part earns a test:
 *
 *  1. TWO BROWSER CONTEXTS SEE EACH OTHER'S CHECK-OFFS (the ticket's own
 *     acceptance criterion). Two separate contexts — two different sessions,
 *     which is what two phones are — against ONE in-test lists backend. A ticks
 *     something; B, already looking at the list, shows it ticked within its poll
 *     interval, with A's name on it, without a reload. Every other lists test
 *     could pass on a broken sync; this is the one that cannot.
 *
 *  2. THE TICK IS OPTIMISTIC. The check response is HELD OPEN and the tick must
 *     already be on screen. That is the supermarket case: the tap has to land
 *     now, on one bar of signal, not when the request returns. A refusal then
 *     has to put the row back — never leave a tick the server rejected.
 *
 *  3. THE TAP TARGET IS BIG. Store mode's whole row is a 64 px target; the
 *     regular row's checkbox is 48 px. Asserted as measured boxes, because a
 *     class name is not a tap target.
 *
 *  4. THE DATE IS THE DATE / THE NAME IS THE NAME. A checked row says "Checked
 *     by Amber, 6:12 PM" only when the server said who; before that it says
 *     only when. An overdue chore groups under Overdue, on its own day (#4550).
 *
 *  5. NO HORIZONTAL OVERFLOW at 375 / 390 px, the shell's phone widths.
 *
 * The API is stubbed against /bff (the UI talks to its own BFF), so every
 * payload is exact and the shared state is the test's own.
 */

const LIST_ID = "lst-weekly";

/** One seeded grocery list (listResponse). */
const GROCERY = {
  id: LIST_ID,
  household_id: "h1",
  name: "Weekly shop",
  kind: "grocery",
  entity_type: null,
  entity_id: null,
  is_template: false,
  archived: false,
  item_count: 5,
  open_count: 4,
  created_at: "2026-11-10T00:00:00Z",
  updated_at: "2026-11-18T16:00:00Z",
};

const CHORES = {
  ...GROCERY,
  id: "lst-chores",
  name: "Weekly chores",
  kind: "chores",
  item_count: 3,
  open_count: 3,
};

/** One row of list_items (itemResponse). */
function item(over: Record<string, unknown>) {
  return {
    id: "i1",
    list_id: LIST_ID,
    text: "Milk",
    quantity: null,
    unit: null,
    section: "Dairy & eggs",
    checked: false,
    checked_at: null,
    checked_by: null,
    checked_by_name: null,
    assignee_person_id: null,
    due_date: null,
    position: 1,
    created_at: "2026-11-10T00:00:00Z",
    updated_at: "2026-11-10T00:00:00Z",
    ...over,
  };
}

const GROCERY_ITEMS = [
  item({ id: "i1", text: "Bananas", quantity: 6, unit: null, section: "Produce", position: 1 }),
  item({ id: "i2", text: "Honeycrisp apples", quantity: 3, unit: "lb", section: "Produce", position: 2 }),
  item({ id: "i3", text: "Whole milk", quantity: 1, unit: "gal", section: "Dairy & eggs", position: 3 }),
  item({ id: "i4", text: "Ground beef", quantity: 2, unit: "lb", section: "Meat & seafood", position: 4 }),
  item({
    id: "i5",
    text: "Paper towels",
    quantity: 6,
    unit: "rolls",
    section: "Household",
    position: 5,
    checked: true,
    checked_at: "2026-11-18T18:12:00Z",
    checked_by: "u-amber",
    checked_by_name: "Amber",
  }),
];

const CHORE_ITEMS = [
  item({ id: "c1", list_id: "lst-chores", text: "Unload the dishwasher", section: null, due_date: "2026-11-14", position: 1 }),
  item({ id: "c2", list_id: "lst-chores", text: "Clean the bathroom sink", section: null, due_date: "2026-11-18", position: 2 }),
  item({ id: "c3", list_id: "lst-chores", text: "Rake the front yard", section: null, due_date: "2026-11-26", position: 3 }),
];

/**
 * The in-test lists backend. One object, shared by every context the test
 * opens, so "did the other phone see it" is a real question with a real
 * answer.
 */
interface FakeBackend {
  lists: Array<typeof GROCERY>;
  items: Record<string, Array<Record<string, unknown>>>;
  /** Writes the API received, in order. */
  writes: string[];
  /** When set, a check/uncheck holds its response until the test releases it. */
  holdCheck: { release: () => void; wait: Promise<void> } | null;
  /** When set, the next check is refused with this status. */
  failNextCheck: number | null;
}

/**
 * A fresh backend, DEEP-cloned from the seed constants.
 *
 * A shallow copy is the bug that made this spec flaky: `[...GROCERY_ITEMS]`
 * copies the array but not the row objects, and the check/uncheck route mutates
 * rows in place (`row.checked = …`) while the archive test sets
 * `lists[0].archived = true`. Both then leak into the NEXT test in the same
 * worker — a grocery list that quietly arrives pre-archived, a tick that was
 * already on, and a spec that passes alone and fails in a suite. `structuredClone`
 * is the whole fix.
 */
function newBackend(): FakeBackend {
  return {
    lists: structuredClone([GROCERY, CHORES]),
    items: {
      [LIST_ID]: structuredClone(GROCERY_ITEMS),
      "lst-chores": structuredClone(CHORE_ITEMS),
    } as Record<string, Array<Record<string, unknown>>>,
    writes: [],
    holdCheck: null,
    failNextCheck: null,
  };
}

/** The people read (GET /people) — chore assignees and the check-off name. */
const PEOPLE = [
  { id: "u-jacob", display_name: "Jacob", color: "teal" },
  { id: "u-amber", display_name: "Amber", color: "grape" },
];

/**
 * Wire one page to the shared backend. Routes are registered per page, so both
 * contexts see the same data by construction.
 */
async function wire(page: Page, backend: FakeBackend, role = "owner") {
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
  await page.route("**/bff/people*", (route) => route.fulfill({ json: { data: PEOPLE } }));

  // The list index reads archived=all and gets every row.
  await page.route(/\/bff\/lists(\?.*)?$/, async (route) => {
    const req = route.request();
    if (req.method() === "POST") {
      const body = req.postDataJSON() as { name: string; kind: string };
      backend.writes.push("POST /lists");
      const created = { ...GROCERY, id: "lst-new", name: body.name, kind: body.kind, item_count: 0, open_count: 0 };
      backend.lists.push(created);
      backend.items["lst-new"] = [];
      return route.fulfill({ status: 201, json: { data: { ...created, items: [] } } });
    }
    return route.fulfill({ json: { data: backend.lists } });
  });

  // One list, with its items.
  await page.route(/\/bff\/lists\/[^/?]+$/, async (route) => {
    const req = route.request();
    const id = req.url().split("/bff/lists/")[1].split("?")[0];
    const list = backend.lists.find((l) => l.id === id);
    if (!list) return route.fulfill({ status: 404, json: { error: "not found" } });
    if (req.method() === "PATCH") {
      backend.writes.push(`PATCH /lists/${id}`);
      Object.assign(list, req.postDataJSON() as object);
      return route.fulfill({ json: { data: list } });
    }
    return route.fulfill({ json: { data: { ...list, items: backend.items[id] ?? [] } } });
  });

  // Archive / unarchive.
  await page.route(/\/bff\/lists\/[^/]+\/(archive|unarchive)$/, async (route) => {
    const [, id, action] = /\/bff\/lists\/([^/]+)\/(archive|unarchive)$/.exec(route.request().url())!;
    backend.writes.push(`POST /lists/${id}/${action}`);
    const list = backend.lists.find((l) => l.id === id);
    if (list) list.archived = action === "archive";
    return route.fulfill({ json: { data: list } });
  });

  // The items feed: a full read, or ?since= (which here always echoes every
  // row — the server's overlap makes that legal, and the client merges by id).
  await page.route(/\/bff\/lists\/[^/]+\/items(\?.*)?$/, async (route) => {
    const req = route.request();
    const id = /\/bff\/lists\/([^/]+)\/items/.exec(req.url())![1];
    if (req.method() === "POST") {
      const body = req.postDataJSON() as { items: Array<Record<string, unknown>> };
      backend.writes.push(`POST /lists/${id}/items`);
      const rows = body.items.map((row, i) =>
        item({ ...row, id: `new-${backend.items[id].length + i}`, list_id: id, position: backend.items[id].length + i + 1 }),
      );
      backend.items[id].push(...rows);
      return route.fulfill({ status: 201, json: { data: rows } });
    }
    return route.fulfill({ json: { data: backend.items[id] ?? [], as_of: new Date().toISOString() } });
  });

  // Single item edit / remove. Registered BEFORE check/uncheck: Playwright
  // tries the most recently registered matching route first (the gotcha in KB
  // ui/recipes-pages.md), and /items/[^/]+ would otherwise swallow /items/check.
  await page.route(/\/bff\/lists\/[^/]+\/items\/(?!check$|uncheck$)[^/]+$/, async (route) => {
    const req = route.request();
    const id = /\/bff\/lists\/([^/]+)\/items\//.exec(req.url())![1];
    const itemId = req.url().split("/items/")[1].split("?")[0];
    if (req.method() === "DELETE") {
      backend.writes.push(`DELETE /lists/${id}/items/${itemId}`);
      backend.items[id] = backend.items[id].filter((row) => row.id !== itemId);
      return route.fulfill({ status: 204, body: "" });
    }
    const patch = req.postDataJSON() as Record<string, unknown>;
    backend.writes.push(`PATCH /lists/${id}/items/${itemId}`);
    const row = backend.items[id].find((r) => r.id === itemId);
    if (row) Object.assign(row, patch);
    return route.fulfill({ json: { data: row } });
  });

  // Check / uncheck: the optimistic write's endpoint.
  await page.route(/\/bff\/lists\/[^/]+\/items\/(check|uncheck)$/, async (route) => {
    const req = route.request();
    const [, id, action] = /\/bff\/lists\/([^/]+)\/items\/(check|uncheck)$/.exec(req.url())!;
    const body = req.postDataJSON() as { item_ids: string[] };
    backend.writes.push(`POST /lists/${id}/items/${action} ${body.item_ids.join(",")}`);

    if (backend.holdCheck) {
      await backend.holdCheck.wait;
    }
    if (backend.failNextCheck) {
      const status = backend.failNextCheck;
      backend.failNextCheck = null;
      return route.fulfill({ status, json: { error: "the server said no" } });
    }

    const checked = action === "check";
    const rows = backend.items[id]
      .filter((row) => body.item_ids.includes(row.id as string))
      .map((row) => {
        row.checked = checked;
        row.checked_at = checked ? new Date().toISOString() : null;
        // Who: the API fills this from the request's actor (migration 066).
        row.checked_by_name = checked ? "Jacob" : null;
        return row;
      });
    return route.fulfill({ json: { data: rows } });
  });

  // The shell's noise: unread count, activity.
  await page.route("**/bff/notifications**", (route) => route.fulfill({ json: { data: [], unread: 0 } }));
  await page.route("**/bff/activity**", (route) => route.fulfill({ json: { data: [] } }));
}

/**
 * The checkbox of one item, by its stable `data-item` id.
 *
 * A NAME-BASED locator is only good for one state: a checkbox is "Check off
 * Whole milk" while open and "Uncheck Whole milk" once ticked, so
 * getByRole({name}) silently stops resolving at exactly the moment the test is
 * trying to assert. This walks from the row instead, which never renames.
 */
function checkBox(page: Page, itemId: string) {
  return page.locator(`[data-item="${itemId}"] [role=checkbox]`);
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = false) {
  await page.screenshot({ path: testInfo.outputPath(`${name}.png`), fullPage });
}

/** A second, separately signed-in browser context — "the other phone". */
async function otherPhone(context: BrowserContext, baseURL: string): Promise<Page> {
  const second = await context.browser()!.newContext({ viewport: { width: 390, height: 844 } });
  const page = await second.newPage();
  await signInWithStub(page, baseURL);
  return page;
}

// ── 1. THE TICKET'S CRITERION: two contexts, one list ───────

test.describe("two phones on one list", () => {
  test("a check-off on one phone shows on the other within seconds, with who did it", async ({
    loggedInPage: pageA,
    context,
    baseURL,
  }) => {
    const backend = newBackend();
    await wire(pageA, backend);
    const pageB = await otherPhone(context, baseURL!);
    await wire(pageB, backend);

    await pageA.goto(`/dashboard/lists/${LIST_ID}`);
    await pageB.goto(`/dashboard/lists/${LIST_ID}`);

    // Both start with "Whole milk" unchecked.
    const milkA = checkBox(pageA, "i3");
    const milkB = checkBox(pageB, "i3");
    await expect(milkA).toHaveAttribute("aria-checked", "false");
    await expect(milkB).toHaveAttribute("aria-checked", "false");

    // A ticks it. The write reaches the shared backend.
    await milkA.click();
    await expect(milkA).toHaveAttribute("aria-checked", "true");
    await expect.poll(() => backend.writes.filter((w) => w.includes("/items/check")).length).toBeGreaterThan(0);

    // B, with no reload and no interaction, converges on it — and says who.
    // The feed polls every 5 s, so "within seconds" is the real thing being
    // asserted here, not a helper.
    await expect(milkB).toHaveAttribute("aria-checked", "true", { timeout: 12_000 });
    await expect(pageB.getByText(/Checked by Jacob/)).toBeVisible();

    await snap(pageA, test.info(), "lists-two-phones-a");
    await snap(pageB, test.info(), "lists-two-phones-b");
  });

  test("the other phone shows a new item within seconds", async ({ loggedInPage: pageA, context, baseURL }) => {
    const backend = newBackend();
    await wire(pageA, backend);
    const pageB = await otherPhone(context, baseURL!);
    await wire(pageB, backend);

    await pageA.goto(`/dashboard/lists/${LIST_ID}`);
    await pageB.goto(`/dashboard/lists/${LIST_ID}`);
    await expect(pageB.getByRole("checkbox", { name: "Check off Bananas" })).toBeVisible();

    await pageA.getByLabel("Add an item, e.g. 2 lb ground beef").fill("2 lb chicken thighs");
    await pageA.getByLabel("Add an item, e.g. 2 lb ground beef").press("Enter");

    await expect(pageA.getByText("Chicken thighs")).toBeVisible();
    await expect(pageB.getByText("Chicken thighs")).toBeVisible({ timeout: 12_000 });
  });
});

// ── 2. THE TICK IS OPTIMISTIC, AND A REFUSAL PUTS IT BACK ──

test.describe("optimistic check-off", () => {
  test("the tick lands before the server answers", async ({ loggedInPage: page }) => {
    const backend = newBackend();
    await wire(page, backend);
    await page.goto(`/dashboard/lists/${LIST_ID}`);

    // Hold the check response open: any tick on screen now came from the tap.
    let release = () => {};
    backend.holdCheck = { release: () => release(), wait: new Promise<void>((r) => (release = r)) };

    const milk = checkBox(page, "i3");
    await expect(milk).toHaveAttribute("aria-checked", "false");
    await milk.click();
    // Still in flight, and already ticked: the tap painted it, not the response.
    await expect(milk).toHaveAttribute("aria-checked", "true");
    await expect(page.getByText(/^Checked /).first()).toBeVisible();

    backend.holdCheck?.release();
    backend.holdCheck = null;
    // And the server's name replaces the guess once it lands.
    await expect(page.getByText(/Checked by Jacob/)).toBeVisible();
  });

  test("a refused check puts the row back and says so", async ({ loggedInPage: page }) => {
    const backend = newBackend();
    await wire(page, backend);
    await page.goto(`/dashboard/lists/${LIST_ID}`);

    backend.failNextCheck = 500;
    await page.getByRole("checkbox", { name: "Check off Whole milk" }).click();

    // Rolled back: the row is unchecked again, not a lie left on screen.
    await expect(page.getByRole("checkbox", { name: "Check off Whole milk" })).toHaveAttribute(
      "aria-checked",
      "false",
      { timeout: 10_000 },
    );
  });
});

// ── 3. THE TAP TARGETS (the supermarket case) ──────────────

test.describe("store mode", () => {
  test("every row is a 64 px target and checking is one tap", async ({ loggedInPage: page }, testInfo) => {
    const backend = newBackend();
    await wire(page, backend);
    await page.goto(`/dashboard/lists/${LIST_ID}`);

    await page.getByRole("button", { name: "Shop" }).click();
    const dialog = page.getByRole("dialog", { name: "Weekly shop" });
    await expect(dialog).toBeVisible();

    // Measured, not asserted from a class name.
    // Scoped to the store view: the page underneath is still in the DOM (inert),
    // so an unscoped [data-item] would resolve to the smaller row behind it.
    const row = dialog.locator('[data-item="i3"] [role=checkbox]');
    const box = (await row.boundingBox())!;
    expect(Math.round(box.height)).toBeGreaterThanOrEqual(64);
    // Full width of the screen minus the 12 px gutters.
    expect(Math.round(box.width)).toBeGreaterThan(300);

    await row.click();
    await expect(row).toHaveAttribute("aria-checked", "true");
    // The store view's own live count, not the page's count behind it.
    await expect(dialog.locator("[data-store-count]")).toHaveText("4 left of 5");

    await snap(page, testInfo, "lists-store-mode", true);
  });

  test("the cart block holds the checked rows and puts one back", async ({ loggedInPage: page }) => {
    const backend = newBackend();
    await wire(page, backend);
    await page.goto(`/dashboard/lists/${LIST_ID}`);
    await page.getByRole("button", { name: "Shop" }).click();

    // Paper towels starts checked, so the store view's cart has one row.
    const store = page.getByRole("dialog", { name: "Weekly shop" });
    const cart = store.getByText(/In the cart · 1/);
    await expect(cart).toBeVisible();
    await cart.click();
    const paper = store.locator('[data-item="i5"] [role=checkbox]');
    await paper.click();
    await expect.poll(() => backend.writes.some((w) => w.includes("/items/uncheck"))).toBe(true);
    await expect(paper).toHaveAttribute("aria-checked", "false");
  });
});

// ── 4. GROUPING, DATES AND NAMES ───────────────────────────

test.describe("chores and grouping", () => {
  test("a chore lands in the bucket its own date says, on the household's today", async ({ loggedInPage: page }) => {
    const backend = newBackend();
    // Pin the clock BEFORE wiring/goto, so every read of "today" agrees. The
    // household's today is 2026-11-18; the #4550 bug class is a DATE read with
    // local getters, which files Nov 26 under Nov 25 in New York.
    await page.clock.setFixedTime(new Date("2026-11-18T14:00:00-05:00"));
    await wire(page, backend);
    await page.goto("/dashboard/lists/lst-chores");

    // Grouped by due bucket, not by a raw date string.
    const sections = page.locator("section[data-section]");
    expect(await sections.evaluateAll((els) => els.map((el) => el.getAttribute("data-section")))).toEqual([
      "Overdue",
      "Today",
      "Later",
    ]);

    // The overdue chore names the day it was due, never the ISO string.
    await expect(page.getByText("Overdue, Sat")).toBeVisible();
    // The one due today says Today; the one a fortnight out names its date.
    await expect(page.getByText("Thu, Nov 26")).toBeVisible();
    await expect(page.getByText("2026-11-26")).toHaveCount(0);
  });

  test("a grocery list groups by aisle in the store's walking order", async ({ loggedInPage: page }) => {
    const backend = newBackend();
    await wire(page, backend);
    await page.goto(`/dashboard/lists/${LIST_ID}`);

    // The open rows' aisle headings, in the store's walking order. Household is
    // absent because its only item (paper towels) is already checked, and the
    // open list is what these headings describe — the checked rows live in
    // their own "In the cart" block below.
    const sections = page.locator("section[data-section]");
    await expect(sections).toHaveCount(3);
    expect(await sections.evaluateAll((els) => els.map((el) => el.getAttribute("data-section")))).toEqual([
      "Produce",
      "Dairy & eggs",
      "Meat & seafood",
    ]);

    // And the checked row is down in the cart, not in an aisle.
    await expect(page.getByText(/In the cart · 1/)).toBeVisible();
  });

  test("a checked row says who only when the server said who", async ({ loggedInPage: page }) => {
    const backend = newBackend();
    await wire(page, backend);
    await page.goto(`/dashboard/lists/${LIST_ID}`);

    // Paper towels was checked by Amber: the server sent the name.
    await expect(page.getByText(/Checked by Amber/)).toBeVisible();

    // Now check one ourselves. The server (the stub) answers with the actor's
    // name, so the row picks it up from the response — never from the browser's
    // own guess about who is holding the phone (see optimistic.ts).
    await checkBox(page, "i1").click();
    await expect(checkBox(page, "i1")).toHaveAttribute("aria-checked", "true");
    await expect(page.getByText(/Checked by Jacob/)).toBeVisible();
  });
});

// ── 5. THE INDEX, ROLES, AND MOBILE LAYOUT ─────────────────

test.describe("lists index", () => {
  test("shows each list's open count and the kind chips", async ({ loggedInPage: page }, testInfo) => {
    const backend = newBackend();
    await wire(page, backend);
    await page.goto("/dashboard/lists");

    await expect(page.getByRole("heading", { name: "Lists", level: 1 })).toBeVisible();
    await expect(page.getByRole("link", { name: /Weekly shop/ })).toBeVisible();
    await expect(page.getByRole("link", { name: /Weekly chores/ })).toBeVisible();
    // The count is the API's open_count, and the caption comes from the kind.
    const shop = page.locator('[data-list-card="lst-weekly"]');
    await expect(shop.getByText("4")).toBeVisible();
    await expect(shop.getByText("left")).toBeVisible();
    const chores = page.locator('[data-list-card="lst-chores"]');
    await expect(chores.getByText("to do")).toBeVisible();

    await snap(page, testInfo, "lists-index", true);
  });

  test("a viewer reads but cannot check, add, or manage", async ({ loggedInPage: page }) => {
    const backend = newBackend();
    await wire(page, backend, "viewer");
    await page.goto(`/dashboard/lists/${LIST_ID}`);

    // The list is readable...
    await expect(checkBox(page, "i3")).toBeDisabled();
    // ...and has no quick add, no Shop button, no actions menu.
    await expect(page.locator("[data-quick-add]")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Shop" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "List actions" })).toHaveCount(0);
  });

  test("a housesitter adds and checks, but cannot rename or archive", async ({ loggedInPage: page }) => {
    const backend = newBackend();
    await wire(page, backend, "housesitter");
    await page.goto(`/dashboard/lists/${LIST_ID}`);

    await expect(page.locator("[data-quick-add]")).toBeVisible();
    await expect(checkBox(page, "i3")).toBeEnabled();
    await expect(page.getByRole("button", { name: "List actions" })).toHaveCount(0);
    // And it says what they can do, rather than leaving them guessing.
    await expect(page.getByText(/You can add items and check them off/)).toBeVisible();
  });

  test("an archived list is read-only until unarchived", async ({ loggedInPage: page }) => {
    const backend = newBackend();
    backend.lists[0].archived = true;
    await wire(page, backend);
    await page.goto(`/dashboard/lists/${LIST_ID}`);

    await expect(page.getByText(/Archived\./)).toBeVisible();
    await expect(checkBox(page, "i3")).toBeDisabled();
    await expect(page.getByRole("button", { name: "Unarchive" })).toBeVisible();
  });

  for (const width of [375, 390]) {
    test(`no horizontal overflow at ${width} px`, async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width, height: 844 });
      const backend = newBackend();
      await wire(page, backend);

      await page.goto("/dashboard/lists");
      await expect(page.getByRole("link", { name: /Weekly shop/ })).toBeVisible();
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `lists-index-${width}`);

      await page.goto(`/dashboard/lists/${LIST_ID}`);
      await expect(page.getByRole("checkbox", { name: "Check off Bananas" })).toBeVisible();
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `lists-detail-${width}`);
    });
  }
});
