import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * Today — the home screen (#4549), built to the approved mockup (artifact #76
 * v1) inside the approved shell (#75 v1).
 *
 * Three things this spec exists to prove, and one of them is a regression the
 * R1 war game found in the real household:
 *
 *  1. a POPULATED day renders: the agenda merged and in time order, bills due,
 *     tonight's dinner, people, the attribution feed;
 *  2. a NEW FAMILY gets an explained empty state on every panel, never a blank
 *     one;
 *  3. A PAID BILL DOES NOT APPEAR AS DUE. In R1 an agent told the family
 *     "Electric $180 due, still unpaid" about a bill paid that morning, because
 *     a stale Bills-calendar event looked like bill state. Bill state comes
 *     from /bills here (paid_date / paid_for_due_date) and the calendar mirror
 *     events are dropped, so the paid bill is absent and an unrelated unpaid
 *     one still shows — both facts, in one assertion each.
 *
 * The API is stubbed per test with page.route, so each payload is exact. The
 * clock is pinned so "today" is stable: the page reads today in the HOUSEHOLD's
 * timezone (GET /me/households → America/New_York), so the pinned instant must
 * fall on 2026-11-17 there.
 */
const TODAY = "2026-11-17";
/** 07:42 in America/New_York (EST, UTC-5): 8 minutes before school drop-off. */
const NOW_ISO = "2026-11-17T12:42:00Z";

const HOUSEHOLDS = {
  data: [
    { id: "h1", name: "Sims", timezone: "America/New_York", role: "owner", joined_at: "2026-01-01T00:00:00Z", is_default: true, current: true },
  ],
};

/** A typical busy morning: a past run, a drop-off in 8 minutes, an appointment. */
const EVENTS = [
  {
    id: "e1", calendar_id: "c1", event_type: "family", title: "School drop-off",
    start: "2026-11-17T12:50:00Z", end: "2026-11-17T13:20:00Z", all_day: false,
    location: "Lincoln Elementary, 1200 Oberlin Rd", color: "#0b7285",
    rrule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR", recurring: true, recurrence_id: "2026-11-17T12:50:00Z",
  },
  {
    id: "e2", calendar_id: "c1", event_type: "family", title: "Nora's 18-month check-up",
    start: "2026-11-17T14:30:00Z", end: "2026-11-17T15:15:00Z", all_day: false,
    location: "Riverside Pediatrics", color: "#a61e4d",
  },
  {
    id: "e3", calendar_id: "c1", event_type: "family", title: "Morning run",
    start: "2026-11-17T11:30:00Z", end: "2026-11-17T12:15:00Z", all_day: false,
    color: "#9c36b5", rrule: "FREQ=WEEKLY;BYDAY=MO,WE,FR", recurring: true, recurrence_id: "2026-11-17T11:30:00Z",
  },
  {
    id: "e4", calendar_id: "c1", event_type: "family", title: "Trash & recycling pickup",
    start: "2026-11-17T05:00:00Z", end: "2026-11-18T05:00:00Z", all_day: true, color: "#868e96",
  },
  // Tomorrow's event, to prove the peek.
  {
    id: "e5", calendar_id: "c1", event_type: "family", title: "Ellie's piano recital",
    start: "2026-11-18T23:00:00Z", end: "2026-11-19T00:30:00Z", all_day: false, color: "#b8460b",
  },
];

/**
 * A BILLS-CALENDAR MIRROR EVENT for the electric bill, still in the window: the
 * exact shape R1 mistook for bill state. Today must never render it.
 */
const BILL_MIRROR_EVENT = {
  id: "e-bill", calendar_id: "c-bills", event_type: "bill", entity_type: "bill", entity_id: "b-electric",
  title: "Electric", description: "Amount: $180.00", start: "2026-11-17T05:00:00Z",
  end: "2026-11-18T05:00:00Z", all_day: true, color: "#e03131",
};

/**
 * Bills. Electric was PAID THIS MORNING for today's date; water is unpaid and
 * due in two days; the mortgage is due Dec 1 (outside the two-week window).
 */
const BILLS = [
  {
    id: "b-electric", name: "Electric", amount: "180.00", due_day: 17, category: "utilities",
    frequency: "monthly", due_date: null, is_autopay: false,
    paid_date: TODAY, paid_for_due_date: TODAY,
  },
  {
    id: "b-water", name: "Water", amount: "64.20", due_day: 19, category: "utilities",
    frequency: "monthly", due_date: null, is_autopay: false, paid_date: null, paid_for_due_date: null,
  },
  {
    id: "b-mortgage", name: "Mortgage", amount: "2410.00", due_day: 1, category: "housing",
    frequency: "monthly", due_date: null, is_autopay: true, paid_date: null, paid_for_due_date: null,
  },
];

const MAINTENANCE = [
  { id: "m1", name: "Replace HVAC filter", status: "pending", due_date: "2026-11-14T00:00:00Z" },
];

const PLANS = {
  data: [{ id: "mp1", household_id: "h1", date: TODAY, slot: "dinner", recipe_id: "r1", title: "Sheet-pan chicken fajitas", notes: null, feedback: [] }],
  from: TODAY,
  to: TODAY,
};

const RECIPE = {
  id: "r1", household_id: "h1", title: "Sheet-pan chicken fajitas", servings: 5,
  prep_minutes: 10, cook_minutes: 25, ingredients: [{ name: "chicken thighs" }, { name: "limes" }], tags: [],
};

const PEOPLE = [
  { id: "p1", household_id: "h1", display_name: "Jacob Sims", relationship: "parent", allergies: [], color: "cyan", user_id: "u1" },
  { id: "p2", household_id: "h1", display_name: "Amber Sims", relationship: "parent", allergies: [], color: "violet", user_id: "u2" },
  { id: "p3", household_id: "h1", display_name: "Nora Sims", relationship: "child", allergies: ["peanuts", "fish"], color: "red" },
];

const LISTS = [
  { id: "l1", household_id: "h1", name: "Chores", kind: "chores", entity_type: "household", entity_id: "h1", is_template: false, archived: false, item_count: 6, open_count: 3 },
  { id: "l2", household_id: "h1", name: "Groceries", kind: "todo", entity_type: "household", entity_id: "h1", is_template: false, archived: false, item_count: 9, open_count: 7 },
];

const CHORE_ITEMS = [
  { id: "i1", list_id: "l1", text: "Take the bins to the curb", checked: true, checked_at: "2026-11-17T11:58:00Z", checked_by_name: "Theo Sims", assignee_person_id: "p4", due_date: TODAY },
  // Jacob (p1, the viewer) owes two of these; Nora (p3) owes one that must NOT
  // appear under "Needs you", and one of Jacob's is already ticked today.
  { id: "i2", list_id: "l1", text: "Feed Biscuit", checked: false, assignee_person_id: "p3", due_date: TODAY },
  { id: "i3", list_id: "l1", text: "Sign Theo's field trip form", checked: false, assignee_person_id: "p1", due_date: TODAY },
  { id: "i4", list_id: "l1", text: "Empty the dishwasher", checked: false, assignee_person_id: "p1", due_date: "2026-11-14" },
  { id: "i5", list_id: "l1", text: "Call the plumber", checked: true, checked_at: "2026-11-17T11:20:00Z", checked_by_name: "Jacob Sims", assignee_person_id: "p1", due_date: TODAY },
];

/**
 * The activity feed, in the shape GET /activity answers: `description` already
 * names the actor and, for an agent write, appends `via agent "<name>"`.
 */
const ACTIVITY = [
  {
    id: 4, action: "create", entity_type: "list_item", entity_id: "i9", summary: 'created list item "oat milk"',
    description: 'Amber created list item "oat milk" via agent "moltis-wg-amber"',
    created_at: "2026-11-17T13:30:00Z",
    actor: { user_id: "u2", person_id: "p2", name: "Amber Sims" }, via_agent: true,
    agent: { token_id: "t1", name: "moltis-wg-amber" },
  },
  {
    id: 3, action: "create", entity_type: "calendar_event", entity_id: "e2", summary: 'created calendar event "Nora\'s 18-month check-up"',
    description: 'Jacob created calendar event "Nora\'s 18-month check-up"',
    created_at: "2026-11-17T12:10:00Z",
    actor: { user_id: "u1", person_id: "p1", name: "Jacob Sims" }, via_agent: false, agent: null,
  },
];

const ME_TODAY = { id: "u1", email: "jacob@sims.family", name: "Jacob Sims" };

async function freezeToday(page: Page, iso = NOW_ISO) {
  await page.clock.setFixedTime(new Date(iso));
}

/** Stubs every endpoint Today reads. `overrides` replaces any single one. */
async function stubToday(
  page: Page,
  overrides: {
    events?: unknown[];
    bills?: unknown[];
    maintenance?: unknown[];
    plans?: typeof PLANS;
    people?: unknown[];
    lists?: unknown[];
    items?: unknown[];
    activity?: unknown[];
    householdRole?: string;
    fail?: Array<"agenda" | "bills" | "meals" | "people" | "lists" | "activity">;
  } = {},
) {
  const fail = new Set(overrides.fail ?? []);
  const failOnce = async (route: import("@playwright/test").Route) =>
    route.fulfill({ status: 503, json: { error: { code: "UNAVAILABLE", message: "upstream is down" } } });

  await page.route("**/bff/me/households", (route) =>
    route.fulfill({ json: { data: [{ ...HOUSEHOLDS.data[0], role: overrides.householdRole ?? "owner" }] } }),
  );
  await page.route("**/bff/auth/me", (route) => route.fulfill({ json: ME_TODAY }));
  await page.route("**/bff/calendars/events**", (route) =>
    fail.has("agenda") ? failOnce(route) : route.fulfill({ json: { data: overrides.events ?? EVENTS } }),
  );
  await page.route("**/bff/bills**", (route) =>
    fail.has("bills") ? failOnce(route) : route.fulfill({ json: { data: overrides.bills ?? BILLS } }),
  );
  await page.route("**/bff/maintenance/tasks**", (route) =>
    route.fulfill({ json: { data: overrides.maintenance ?? MAINTENANCE } }),
  );
  await page.route("**/bff/meal-plans**", (route) =>
    fail.has("meals") ? failOnce(route) : route.fulfill({ json: overrides.plans ?? PLANS }),
  );
  await page.route("**/bff/recipes/**", (route) => route.fulfill({ json: { data: RECIPE } }));
  await page.route("**/bff/people**", (route) =>
    fail.has("people") ? failOnce(route) : route.fulfill({ json: { data: overrides.people ?? PEOPLE } }),
  );
  // One handler for the list endpoints, with explicit precedence: "**/bff/lists**"
  // is a PREFIX, so a separate route for /lists/{id}/items registered before it
  // would be silently shadowed (the KB gotcha #4567 hit). Checked here instead.
  await page.route("**/bff/lists**", (route) => {
    const path = new URL(route.request().url()).pathname;
    const itemsMatch = path.match(/\/lists\/([^/]+)\/items$/);
    if (itemsMatch) {
      // The real API answers per list; only Chores (l1) has items in this
      // fixture, and Groceries answers empty — returning the same rows for
      // every list would duplicate them in a panel that spans lists.
      const data = itemsMatch[1] === "l1" ? overrides.items ?? CHORE_ITEMS : [];
      return route.fulfill({ json: { data } });
    }
    if (fail.has("lists")) return failOnce(route);
    return route.fulfill({ json: { data: overrides.lists ?? LISTS } });
  });
  await page.route("**/bff/activity**", (route) =>
    fail.has("activity") ? failOnce(route) : route.fulfill({ json: { data: overrides.activity ?? ACTIVITY } }),
  );
}

async function gotoToday(page: Page) {
  await page.goto("/dashboard");
  await expect(page.getByTestId("today-agenda")).toBeVisible();
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = true) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme}`, () => {
    test.use({ colorScheme });

    // ── 1. populated ───────────────────────────────────────────
    test("phone 390: a populated day renders — agenda, bills, dinner, people, attribution", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      await stubToday(page);
      await gotoToday(page);

      // The greeting names the viewer and summarises the day, computed from
      // today's date — 3 timed events today, one bill due, fajitas tonight.
      await expect(page.getByRole("heading", { level: 2, name: "Good morning, Jacob" })).toBeVisible();
      const summary = page.getByTestId("today-summary");
      await expect(summary).toContainText("events today");
      await expect(summary).toContainText("due in the next 2 weeks");
      await expect(summary).toContainText("fajitas tonight");

      // The agenda: merged, in TIME order (the 6:30 run before the 7:50
      // drop-off, even though the API returned them in another order), with the
      // all-day strip on top and the past row dimmed rather than hidden.
      const rows = page.locator("[data-testid='agenda-row']");
      await expect(rows.first()).toContainText("Morning run");
      await expect(page.locator("[data-testid='today-allday']")).toContainText("Trash & recycling pickup");
      const texts = await rows.allInnerTexts();
      const order = texts.map((t) => (t.includes("Morning run") ? "run" : t.includes("School drop-off") ? "drop" : t.includes("check-up") ? "check" : "?"));
      expect(order, `agenda order was ${order.join(",")}`).toEqual(["run", "drop", "check"]);
      // The recurring label comes from the RRULE, not from a guess.
      await expect(rows.filter({ hasText: "School drop-off" })).toContainText("Every weekday");
      // Past rows stay visible, marked.
      await expect(rows.filter({ hasText: "Morning run" })).toHaveAttribute("data-past", "true");

      // Up next expands in place: the first event that has not started, with
      // how far away it is and directions from its location.
      await expect(rows.filter({ hasText: "School drop-off" })).toContainText("in 8 min");
      await expect(page.getByRole("link", { name: "Directions" })).toBeVisible();

      // Bills & upkeep: the unpaid bill and the late upkeep are there, with
      // dates computed from TODAY (Nov 17), not from anything stored.
      const due = page.getByTestId("today-due");
      await expect(due).toContainText("Water");
      await expect(due).toContainText("$64.20");
      await expect(due).toContainText("Due Thu");
      await expect(due).toContainText("Replace HVAC filter");
      await expect(due).toContainText("3 days late");

      // Tonight's dinner, with the recipe's own facts.
      const dinner = page.getByTestId("today-dinner");
      await expect(dinner).toContainText("Sheet-pan chicken fajitas");
      await expect(dinner).toContainText("35 min");
      await expect(dinner).toContainText("serves 5");

      // People, including the allergy that matters (safety data, always shown).
      const people = page.getByTestId("today-people");
      await expect(people).toContainText("Nora Sims");
      await expect(people).toContainText("Allergic to peanuts, fish");

      // Attribution: the agent's name is on the row, so nobody has to ask
      // "did you book that?".
      const feed = page.getByTestId("today-activity");
      await expect(feed.getByTestId("activity-row").first()).toContainText("Amber");
      await expect(feed.getByTestId("activity-agent").first()).toContainText("moltis-wg-amber");

      // Needs you: only what is assigned to the VIEWER, with what they ticked
      // off today kept (struck through) until midnight, and no checkbox — Today
      // is read-only, and a box that does nothing is a false affordance.
      const mine = page.getByTestId("today-needs-you");
      await expect(mine).toContainText("Sign Theo's field trip form");
      await expect(mine).toContainText("Empty the dishwasher");
      await expect(mine).toContainText("Overdue");
      await expect(mine.getByTestId("needs-you-done")).toContainText("Call the plumber");
      await expect(mine).not.toContainText("Feed Biscuit");
      await expect(mine).not.toContainText("Take the bins to the curb");
      await expect(mine.getByRole("checkbox")).toHaveCount(0);

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `today-populated-phone-${colorScheme}`);
    });

    // ── 2. empty ───────────────────────────────────────────────
    test("phone 390: a new family gets explained empty states, never blank panels", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      await stubToday(page, { events: [], bills: [], maintenance: [], plans: { data: [], from: TODAY, to: TODAY }, people: [], lists: [], items: [], activity: [] });
      await gotoToday(page);

      // Every panel explains what will appear and offers the way to start.
      await expect(page.getByTestId("today-agenda")).toContainText("Nothing scheduled today");
      await expect(page.getByTestId("today-due")).toContainText("Nothing due in the next 2 weeks");
      await expect(page.getByTestId("today-dinner")).toContainText("No dinner planned yet");
      await expect(page.getByTestId("today-people")).toContainText("No one added yet");
      await expect(page.getByTestId("today-needs-you")).toContainText("No lists yet");
      await expect(page.getByTestId("today-activity")).toContainText("No changes yet");

      // The quick actions are present, so the page is actionable, not a dead end.
      await expect(page.getByRole("group", { name: "Quick actions" }).getByRole("link", { name: "Plan dinner" })).toBeVisible();
      // The summary says the truth about an empty day.
      await expect(page.getByTestId("today-summary")).toContainText("Nothing on the calendar today");

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `today-empty-phone-${colorScheme}`);
    });

    // ── 3. the R1 regression ───────────────────────────────────
    test("phone 390: a bill paid this morning is NOT shown as due, and its stale calendar event never is", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      // The R1 household: Electric is paid for today (paid_date = today,
      // paid_for_due_date = today) AND its Bills-calendar mirror event is still
      // sitting in the range, saying "Amount: $180.00".
      await stubToday(page, { events: [...EVENTS, BILL_MIRROR_EVENT] });
      await gotoToday(page);

      const due = page.getByTestId("today-due");
      // The unpaid bill is due, so the panel is not simply empty.
      await expect(due).toContainText("Water");
      // The paid bill is nowhere on the page: not in "Bills & upkeep", and not
      // in the agenda through its old mirror event.
      await expect(due).not.toContainText("Electric");
      await expect(due).not.toContainText("$180");
      await expect(page.getByTestId("today-agenda")).not.toContainText("Electric");
      await expect(page.getByTestId("today-agenda")).not.toContainText("$180");
      // The mirror event is the ONLY event that said anything about the bill:
      // if the page had read it, the row would be here.
      await expect(page.locator("[data-testid='agenda-row']", { hasText: "Electric" })).toHaveCount(0);

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `today-paid-bill-phone-${colorScheme}`);
    });
  });
}

// ── desktop ──────────────────────────────────────────────────
test.describe("desktop", () => {
  test.use({ colorScheme: "light" });

  test("desktop 1440: two columns, agenda left and bills/dinner/people right", async ({ loggedInPage: page }, testInfo) => {
    onlyOn("desktop");
    await page.setViewportSize({ width: 1440, height: 900 });
    await freezeToday(page);
    await stubToday(page);
    await gotoToday(page);

    // The two columns really are side by side: the agenda's left edge is left
    // of the "Bills & upkeep" card's, and both sit on the same row.
    const agenda = await page.getByTestId("today-agenda").boundingBox();
    const due = await page.getByTestId("today-due").boundingBox();
    expect(agenda, "agenda box").toBeTruthy();
    expect(due, "due box").toBeTruthy();
    expect(due!.x, "Bills & upkeep is the right column").toBeGreaterThan(agenda!.x + agenda!.width - 1);

    await expect(page.getByTestId("today-dinner")).toContainText("Sheet-pan chicken fajitas");
    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "today-desktop-1440");
  });
});

// ── honest degradation ───────────────────────────────────────
test.describe("a source that fails", () => {
  test.use({ colorScheme: "light" });

  test("phone 390: a failed calendar read says so and never reads as an empty day", async ({ loggedInPage: page }, testInfo) => {
    onlyOn("phone");
    await page.setViewportSize({ width: 390, height: 844 });
    await freezeToday(page);
    await stubToday(page, { fail: ["agenda"] });
    await gotoToday(page);

    const agenda = page.getByTestId("today-agenda");
    await expect(agenda.getByTestId("section-error")).toBeVisible();
    await expect(agenda).toContainText("The calendar didn't load");
    await expect(agenda).toContainText("Everything else below is up to date");
    // The critical distinction (audit #74): a failure is not an empty day.
    await expect(agenda).not.toContainText("Nothing scheduled today");
    // Try again really retries.
    await expect(agenda.getByRole("button", { name: "Try again" })).toBeVisible();

    // The other panels still render their real data.
    await expect(page.getByTestId("today-dinner")).toContainText("Sheet-pan chicken fajitas");
    await expect(page.getByTestId("today-due")).toContainText("Water");

    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "today-agenda-error-phone");
  });

  test("phone 390: an unchecked dinner says 'Not allergy-checked', never a tick", async ({ loggedInPage: page }, testInfo) => {
    onlyOn("phone");
    await page.setViewportSize({ width: 390, height: 844 });
    await freezeToday(page);
    await stubToday(page);
    await gotoToday(page);

    // The REST API returns no allergy_warnings/checks today (#4891) and Nora
    // has allergies saved, so the only honest answer is "not checked".
    const line = page.getByTestId("dinner-allergy");
    await expect(line).toHaveAttribute("data-state", "not-checked");
    await expect(line).toContainText("Not allergy-checked");
  });
});
