import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * Calendar (#4550), built to the approved mockup (artifact #87 v1) inside the
 * shell of #75 v1.
 *
 * Four things this spec exists to prove, three of them R1 war-game regressions:
 *
 *  1. A RECURRING SERIES IS ONE EVENT WITH OCCURRENCES, not N rows. Round one
 *     of the R1 war game rendered the same weekly activity as 9 rows, then 35.
 *     Here the API returns two occurrences (Tue and Thu) sharing one series id,
 *     and the page must draw ONE card with two date chips — asserted by counting
 *     cards, not by looking for text.
 *  2. AN ALL-DAY EVENT LANDS ON THE RIGHT DATE. Round one shifted one by a day.
 *     The page renders `local_date` (from the API) and never converts an
 *     instant, so a Thanksgiving stored at 00:00Z on the 26th sits on the 26th
 *     even though a New York browser reads that instant as the 25th at 19:00.
 *  3. THE EMPTY STATE EXPLAINS ITSELF — never a blank grid.
 *  4. A PAID BILL'S MIRROR DOES NOT READ AS DUE, and an entry on the Bills
 *     calendar that is not linked to a bill is labelled as exactly that.
 *
 * The API is stubbed per test with page.route, so every payload is exact. The
 * clock is pinned so "today" is stable: the page reads today in the HOUSEHOLD's
 * zone (GET /me/households → America/New_York), so the pinned instant must fall
 * on 2026-11-17 there.
 */
const TODAY = "2026-11-17";
/** 07:42 in America/New_York (EST, UTC-5). */
const NOW_ISO = "2026-11-17T12:42:00Z";

const HOUSEHOLD = { id: "h1", name: "Sims", timezone: "America/New_York", role: "owner", joined_at: "2026-01-01T00:00:00Z", is_default: true, current: true };

const CALENDARS = [
  { id: "c-family", name: "Family", type: "family", color: "#0b7285", property_id: null },
  { id: "c-bills", name: "Bills", type: "bills", color: "#5c940d", property_id: null },
];

/** The API's day table for the week the page asks for. */
function weekDays(start = "2026-11-17"): { local_date: string; weekday: string }[] {
  const names = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
  const out: { local_date: string; weekday: string }[] = [];
  const [y, m, d] = start.split("-").map(Number);
  for (let i = 0; i < 7; i++) {
    const day = new Date(Date.UTC(y, m - 1, d + i));
    out.push({ local_date: day.toISOString().slice(0, 10), weekday: names[day.getUTCDay()] });
  }
  return out;
}

/**
 * A Tue/Thu piano lesson: TWO occurrences of ONE series. Both carry the series
 * id ("piano"), their own recurrence_id, and the API's local_date/weekday/
 * local_time — the fields the page renders.
 */
const PIANO = [
  {
    id: "piano", calendar_id: "c-family", event_type: "family", title: "Piano lesson",
    start: "2026-11-17T21:00:00Z", end: "2026-11-17T21:45:00Z", all_day: false,
    location: "Ms. Alvarez's studio", color: "#0b7285",
    rrule: "FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261217T235959Z", recurring: true,
    recurrence_id: "2026-11-17T21:00:00Z",
    local_date: "2026-11-17", weekday: "Tuesday", local_time: "16:00",
    end_local_date: "2026-11-17", end_local_time: "16:45",
    reminders: [{ minutes_before: 15 }],
  },
  {
    id: "piano", calendar_id: "c-family", event_type: "family", title: "Piano lesson",
    start: "2026-11-19T21:00:00Z", end: "2026-11-19T21:45:00Z", all_day: false,
    location: "Ms. Alvarez's studio", color: "#0b7285",
    rrule: "FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261217T235959Z", recurring: true,
    recurrence_id: "2026-11-19T21:00:00Z",
    local_date: "2026-11-19", weekday: "Thursday", local_time: "16:00",
    end_local_date: "2026-11-19", end_local_time: "16:45",
    reminders: [{ minutes_before: 15 }],
  },
];

/**
 * Thanksgiving, 26–29 Nov. Stored as an all-day event: midnight UTC on the 26th,
 * exclusive end midnight UTC on the 30th. In New York that first instant is Wed
 * Nov 25 at 19:00, so anything that converts it lands the event a day early —
 * which is the round-one bug this asserts against.
 */
const THANKSGIVING = {
  id: "thanksgiving", calendar_id: "c-family", event_type: "family", title: "Thanksgiving in Asheville",
  start: "2026-11-26T00:00:00Z", end: "2026-11-30T00:00:00Z", all_day: true,
  local_date: "2026-11-26", weekday: "Thursday", end_local_date: "2026-11-29",
};

/** A bill's mirror on the Bills calendar: the Duke Energy bill, PAID for Nov 18. */
const BILL_MIRROR = {
  id: "bill-elec", calendar_id: "c-bills", event_type: "bill", entity_type: "bill", entity_id: "b-elec",
  title: "Duke Energy", start: "2026-11-18T00:00:00Z", end: "2026-11-19T00:00:00Z", all_day: true,
  rrule: "FREQ=MONTHLY;BYMONTHDAY=18", recurring: true, recurrence_id: "2026-11-18T00:00:00Z",
  local_date: "2026-11-18", weekday: "Wednesday", end_local_date: "2026-11-18",
};

/** The legacy hand-typed entry migration 073 retires if it can match a bill. */
const UNLINKED_BILL_ENTRY = {
  id: "legacy-elec", calendar_id: "c-bills", event_type: "event",
  title: "Electric bill due — $180", start: "2026-11-20T00:00:00Z", end: "2026-11-21T00:00:00Z", all_day: true,
  local_date: "2026-11-20", weekday: "Friday", end_local_date: "2026-11-20",
};

const BILLS = [
  {
    id: "b-elec", name: "Duke Energy", amount: "142.18", frequency: "monthly", due_day: 18,
    due_date: null, is_autopay: false, paid_date: "2026-11-15", paid_for_due_date: "2026-11-18",
  },
];

const PEOPLE = [{ id: "p1", household_id: "h1", display_name: "Jacob Sims", relationship: "parent", allergies: [], color: "cyan", user_id: "u1" }];

const ACTIVITY = [
  {
    id: 3, action: "create", entity_type: "calendar_event", entity_id: "piano",
    summary: 'created calendar event "Piano lesson"',
    description: 'Amber created calendar event "Piano lesson" via agent "moltis-wg-amber"',
    created_at: "2026-11-17T12:10:00Z",
    actor: { user_id: "u2", person_id: "p2", name: "Amber Sims" }, via_agent: true,
    agent: { token_id: "t1", name: "moltis-wg-amber" },
  },
];

async function freezeToday(page: Page) {
  await page.clock.setFixedTime(new Date(NOW_ISO));
}

/** Stubs every endpoint the calendar page reads. */
async function stubCalendar(
  page: Page,
  overrides: {
    events?: unknown[];
    days?: { local_date: string; weekday: string }[];
    calendars?: unknown[];
    bills?: unknown[];
    activity?: unknown[];
    failEvents?: boolean;
    role?: string;
  } = {},
) {
  await page.route("**/bff/me/households", (route) =>
    route.fulfill({ json: { data: [{ ...HOUSEHOLD, role: overrides.role ?? "owner" }] } }),
  );
  await page.route("**/bff/auth/me", (route) => route.fulfill({ json: { id: "u1", email: "jacob@sims.family", name: "Jacob Sims" } }));
  await page.route("**/bff/calendars/events**", (route) => {
    if (overrides.failEvents) {
      return route.fulfill({ status: 503, json: { error: { code: "UNAVAILABLE", message: "upstream is down" } } });
    }
    return route.fulfill({
      json: {
        data: overrides.events ?? [],
        // The API answers one entry per day of the range it was asked for.
        days: overrides.days ?? weekDays(),
      },
    });
  });
  await page.route("**/bff/calendars", (route) => route.fulfill({ json: { data: overrides.calendars ?? CALENDARS } }));
  await page.route("**/bff/bills**", (route) => route.fulfill({ json: { data: overrides.bills ?? BILLS } }));
  await page.route("**/bff/people**", (route) => route.fulfill({ json: { data: PEOPLE } }));
  await page.route("**/bff/activity**", (route) => route.fulfill({ json: { data: overrides.activity ?? ACTIVITY } }));
}

async function gotoCalendar(page: Page) {
  await page.goto("/dashboard/calendar");
  await expect(page.getByTestId("calendar-view")).toBeVisible();
}

async function snap(page: Page, testInfo: TestInfo, name: string) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage: true });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme}`, () => {
    test.use({ colorScheme });

    // ── 1. one recurring event is one card ────────────────────
    test("phone 390: a recurring series renders as ONE event with its occurrences", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      await stubCalendar(page, { events: PIANO });
      await gotoCalendar(page);

      const cards = page.locator("[data-testid='series-card']");
      // Two occurrences, ONE card. This is the round-one regression: it used to
      // be one row per occurrence.
      await expect(cards).toHaveCount(1);
      await expect(cards.first()).toContainText("Piano lesson");
      await expect(cards.first()).toHaveAttribute("data-repeating", "true");

      // The other day it occurs on carries a continuation row that points back
      // at the card — not a second card that reads as a second activity.
      const continuations = page.locator("[data-testid='series-continuation']");
      await expect(continuations).toHaveCount(1);
      await expect(continuations.first()).toHaveAttribute("data-day", "2026-11-19");
      await expect(continuations.first()).toContainText("see dates");

      // The card carries its dates as chips: Tue 17 and Thu 19, from the API's
      // own weekday and local_date.
      const chips = page.locator("[data-testid='occurrence-chip']");
      await expect(chips).toHaveCount(2);
      await expect(chips.nth(0)).toHaveText("Tue 17");
      await expect(chips.nth(1)).toHaveText("Thu 19");

      // The rule, from the RRULE — a sentence, not a raw string.
      await expect(page.getByTestId("series-rule").first()).toHaveText("Every Tue and Thu until Dec 17, 2026");
      // The count says how many dates this view holds.
      await expect(page.getByTestId("series-count").first()).toContainText("2 dates");

      // It is still under both of its days: the agenda groups by day, so a
      // parent reading Thursday sees the lesson there.
      const thursday = page.locator("[data-testid='agenda-day'][data-date='2026-11-19']");
      await expect(thursday).toContainText("Piano lesson");

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, "calendar-agenda-recurring");

      // The sheet says plainly that editing changes every date.
      await page.getByTestId("series-open").first().click();
      const sheet = page.getByTestId("event-sheet");
      await expect(sheet).toBeVisible();
      await expect(page.getByTestId("sheet-title")).toContainText("Piano lesson");
      await expect(page.getByTestId("sheet-when")).toContainText("Tue, Nov 17 · 4:00 – 4:45 PM");
      await expect(page.getByTestId("sheet-repeat")).toContainText("Every Tue and Thu until Dec 17, 2026");
      await expect(page.getByTestId("sheet-series-note")).toContainText("Changing it changes every date");
      // Attribution, from the activity feed.
      await expect(page.getByTestId("sheet-attribution")).toHaveText("Added by Amber via moltis-wg-amber · 32 min ago");
      await snap(page, testInfo, "calendar-event-sheet");

      // The editor refuses to promise a single-occurrence edit it cannot keep.
      await page.getByTestId("sheet-edit").click();
      await expect(page.getByTestId("editor-scope-note")).toContainText("every date");
      await expect(page.getByTestId("editor-save")).toHaveText("Save all dates");
      await expect(page.getByTestId("editor-repeat")).toBeVisible();
      await expect(page.getByTestId("editor-allday")).toBeVisible();
      await snap(page, testInfo, "calendar-editor");
    });

    // ── 2. an all-day event lands on the right date ───────────
    test("phone 390: an all-day event lands on its own date, not a day earlier", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      // The day table covers the week Thanksgiving falls in, so the page can
      // show it: pin the anchor by walking there with the toolbar.
      await stubCalendar(page, {
        events: [THANKSGIVING],
        days: [
          ...weekDays("2026-11-23"),
        ],
      });
      await page.goto("/dashboard/calendar");
      await expect(page.getByTestId("calendar-view")).toBeVisible();
      // Move to next week (the pinned today is Nov 17; Nov 23 is the next week).
      await page.getByTestId("calendar-next").click();
      await expect(page.getByTestId("calendar-range")).toContainText("Nov 23");

      // The card is on THURSDAY the 26th — the date the event carries. Its
      // stored instant (00:00Z) is Nov 25 at 19:00 in New York, so a page that
      // converted the instant would put it on Wednesday.
      const thursday = page.locator("[data-testid='agenda-day'][data-date='2026-11-26']");
      await expect(thursday).toContainText("Thanksgiving in Asheville");
      await expect(thursday).toContainText("All day");
      const wednesday = page.locator("[data-testid='agenda-day'][data-date='2026-11-25']");
      await expect(wednesday).not.toContainText("Thanksgiving in Asheville");
      // The heading's weekday is the API's, not one worked out from a Date.
      await expect(thursday.locator("h3")).toContainText("Thursday");
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, "calendar-allday");
    });

    // ── 3. the empty state ────────────────────────────────────
    test("phone 390: an empty week explains why it is empty", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      await stubCalendar(page, { events: [] });
      await gotoCalendar(page);

      const empty = page.getByTestId("calendar-empty");
      await expect(empty).toBeVisible();
      await expect(empty).toContainText("Nothing on the calendar");
      // It says what fills it, and offers the one action that does.
      await expect(empty).toContainText("Repeating events show once with all their dates");
      await expect(empty.getByRole("button", { name: "Add an event" })).toBeVisible();
      // A quiet day is stated in place, never by vanishing — but with nothing at
      // all, the whole week is one explained card, so no day cards are drawn.
      await expect(page.locator("[data-testid='series-card']")).toHaveCount(0);
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, "calendar-empty");

      // A failed read is NOT an empty week: it is an error card with Try again.
      await page.unroute("**/bff/calendars/events**");
      await stubCalendar(page, { events: [], failEvents: true });
      await page.reload();
      const error = page.getByTestId("calendar-error");
      await expect(error).toBeVisible();
      await expect(error).toContainText("nothing is shown rather than an empty week");
      await expect(page.getByTestId("calendar-empty")).toHaveCount(0);
      await snap(page, testInfo, "calendar-error");
    });

    // ── 4. a paid bill's mirror does not read as due ──────────
    test("phone 390: a paid bill reads as paid, and an unlinked Bills entry is not a due date", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      await stubCalendar(page, { events: [PIANO[0], BILL_MIRROR, UNLINKED_BILL_ENTRY] });
      await gotoCalendar(page);

      // The paid bill is labelled paid — never "due".
      const paidCard = page.locator("[data-testid='series-card'][data-series-id='bill-elec']");
      await expect(paidCard).toContainText("Duke Energy");
      await expect(page.getByTestId("bill-paid").first()).toContainText("Paid for Nov 18");
      await expect(page.getByTestId("bill-due")).toHaveCount(0);

      // The legacy entry says what it is and what it is not.
      const unlinked = page.locator("[data-testid='series-card'][data-series-id='legacy-elec']");
      await expect(unlinked).toContainText("Electric bill due — $180");
      await expect(unlinked).toContainText("not linked to a bill");
      await expect(unlinked).not.toContainText("Paid");
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, "calendar-bills");

      // Opening it repeats the warning and offers no editor.
      await unlinked.getByTestId("series-open").click();
      await expect(page.getByTestId("sheet-bill-unlinked")).toContainText("not a due date");
      await expect(page.getByTestId("sheet-edit")).toHaveCount(0);
      await snap(page, testInfo, "calendar-bill-unlinked-sheet");
    });

    // ── 5. create a recurring event with a reminder ───────────
    test("phone 390: create a recurring event with a reminder", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      await stubCalendar(page, { events: [] });

      const writes: { method: string; body: Record<string, unknown> }[] = [];
      await page.route("**/bff/calendars/c-family/events", async (route) => {
        if (route.request().method() === "POST") {
          const body = route.request().postDataJSON() as Record<string, unknown>;
          writes.push({ method: "POST", body });
          return route.fulfill({ status: 201, json: { data: { id: "new-event", ...body } } });
        }
        return route.fulfill({ json: { data: [] } });
      });

      await gotoCalendar(page);
      // With an empty week the empty card carries the only Add action (the FAB
      // steps aside so two Add buttons never stack); from a week with events the
      // FAB and the toolbar's New event are the ways in.
      await page.getByTestId("calendar-empty").getByRole("button", { name: "Add an event" }).click();
      const editor = page.getByTestId("event-editor");
      await expect(editor).toBeVisible();

      await page.getByTestId("editor-title").fill("Swim practice");
      await page.getByTestId("editor-start-date").fill("2026-11-18");
      await page.getByTestId("editor-start-time").fill("16:30");
      await page.getByTestId("editor-end-time").fill("17:30");

      // Weekly on Mon and Wed, ending on a date: the rule the API accepts.
      await page.getByTestId("editor-repeat").click();
      await page.getByRole("listbox").getByRole("option", { name: "Weekly" }).click();
      await page.getByTestId("editor-day-MO").click();
      await page.getByTestId("editor-day-WE").click();
      await expect(page.getByTestId("editor-repeat")).toHaveValue("Every Mon and Wed");

      // Two reminders: the first changed to 30 minutes before, a second added
      // and set to a day before.
      await page.getByTestId("editor-reminder-0").click();
      await page.getByRole("listbox").getByRole("option", { name: "30 minutes before", exact: true }).click();
      await expect(page.getByTestId("editor-reminder-0")).toHaveValue("30 minutes before");
      await page.getByTestId("editor-add-reminder").click();
      await expect(page.getByTestId("editor-reminder-1")).toBeVisible();
      await page.getByTestId("editor-reminder-1").click();
      await page.getByRole("listbox").getByRole("option", { name: "1 day before", exact: true }).click();
      await expect(page.getByTestId("editor-reminder-1")).toHaveValue("1 day before");

      await snap(page, testInfo, "calendar-create-form");
      await page.getByTestId("editor-save").click();

      await expect.poll(() => writes.length).toBe(1);
      const body = writes[0].body;
      assertShape(body);
      expect(body.title).toBe("Swim practice");
      expect(body.rrule).toBe("FREQ=WEEKLY;BYDAY=MO,WE");
      expect(body.all_day).toBe(false);
      expect(body.timezone).toBe("America/New_York");
      // 4:30 PM in New York on Nov 18 is 21:30Z (EST).
      expect(body.start).toBe("2026-11-18T21:30:00.000Z");
      expect(body.end).toBe("2026-11-18T22:30:00.000Z");
      expect(body.reminders).toEqual([{ minutes_before: 30 }, { minutes_before: 1440 }]);

      // It closes on success and says the family sees it.
      await expect(page.getByTestId("event-editor")).toHaveCount(0);
    });
  });
}

/** A tiny shape check, so a missing field fails with a readable message. */
function assertShape(body: Record<string, unknown>) {
  for (const key of ["title", "start", "end", "all_day", "rrule", "reminders"]) {
    expect(Object.prototype.hasOwnProperty.call(body, key), `body has ${key}`).toBe(true);
  }
}

// ── desktop 1440: the secondary target ──────────────────────

test.describe("desktop", () => {
  test("1440: the week grid places blocks by the API's wall clock", async ({ loggedInPage: page }, testInfo) => {
    onlyOn("desktop");
    await page.setViewportSize({ width: 1440, height: 900 });
    await freezeToday(page);
    await stubCalendar(page, { events: PIANO });
    await gotoCalendar(page);
    // Mantine's SegmentedControl hides its radio inputs; the label is the control.
    await page.getByTestId("calendar-view").getByText("Week", { exact: true }).click();

    const grid = page.getByTestId("week-grid");
    await expect(grid).toBeVisible();
    // One series, two blocks (Tue and Thu), never four.
    const blocks = page.locator("[data-testid='grid-block']");
    await expect(blocks).toHaveCount(2);
    await expect(blocks.first()).toContainText("4:00 PM");
    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "calendar-week-desktop");
  });
});
