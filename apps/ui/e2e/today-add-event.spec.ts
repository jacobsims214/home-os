import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * "Add event" on Today opens the event form IN PLACE (#5302).
 *
 * The failure this spec exists to pin down (war game R2, Amber gap G9): the chip
 * looked like a button that would open a form, and tapping it only changed the
 * page to /dashboard/calendar — 0 inputs on screen, no form, no explanation. A
 * control that promises a form and delivers a navigation is a lie about what it
 * does, so the test asserts THREE things, and the old behaviour fails all three:
 *
 *   1. the chip is a BUTTON that opens a dialog (`aria-haspopup`), not a link —
 *      so nothing navigates and the URL is unchanged after the tap;
 *   2. the real event form appears (the calendar's own EventEditor through
 *      ResponsiveModal: the wave's bottom sheet on a phone), with its fields
 *      filled from the day the person is looking at;
 *   3. saving it POSTs the event the form showed. The form is shared with the
 *      Calendar page, so this also proves the two cannot drift apart.
 *
 * The API is stubbed per test, and the clock is pinned to 07:42 in the
 * household's zone (America/New_York) so "today" is 2026-11-17.
 */
const TODAY = "2026-11-17";
const NOW_ISO = "2026-11-17T12:42:00Z";

const HOUSEHOLD = {
  id: "h1", name: "Sims", timezone: "America/New_York", role: "owner",
  joined_at: "2026-01-01T00:00:00Z", is_default: true, current: true,
};

const CALENDARS = [
  { id: "c-family", name: "Family", type: "family", color: "#0b7285", property_id: null },
  // Not writable from the form: Money fills it (the select must not offer it).
  { id: "c-bills", name: "Bills", type: "bills", color: "#5c940d", property_id: null },
];

const EVENTS = [
  {
    id: "e1", calendar_id: "c-family", event_type: "family", title: "School drop-off",
    start: "2026-11-17T12:50:00Z", end: "2026-11-17T13:20:00Z", all_day: false, color: "#0b7285",
  },
];

const BILLS = [
  {
    id: "b-water", name: "Water", amount: "64.20", due_day: 19, category: "utilities",
    frequency: "monthly", due_date: null, is_autopay: false, paid_date: null, paid_for_due_date: null,
  },
];

const PEOPLE = [
  { id: "p1", household_id: "h1", display_name: "Jacob Sims", relationship: "parent", allergies: [], color: "cyan", user_id: "u1" },
];

const LISTS = [
  { id: "l1", household_id: "h1", name: "Chores", kind: "chores", entity_type: "household", entity_id: "h1", is_template: false, archived: false, item_count: 3, open_count: 2 },
];

async function freezeToday(page: Page) {
  await page.clock.setFixedTime(new Date(NOW_ISO));
}

/** Stubs every endpoint Today reads, plus the calendars the form needs. */
async function stubToday(
  page: Page,
  overrides: { events?: unknown[]; role?: string; failCalendars?: boolean } = {},
) {
  await page.route("**/bff/me/households", (route) =>
    route.fulfill({ json: { data: [{ ...HOUSEHOLD, role: overrides.role ?? "owner" }] } }),
  );
  // A PREFIX route: the calendars LIST and the events it must not shadow.
  await page.route("**/bff/calendars**", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/calendars/events")) {
      return route.fulfill({ json: { data: overrides.events ?? EVENTS } });
    }
    if (path === "/bff/calendars") {
      if (overrides.failCalendars) {
        return route.fulfill({ status: 503, json: { error: { code: "UNAVAILABLE", message: "upstream is down" } } });
      }
      return route.fulfill({ json: { data: CALENDARS } });
    }
    return route.fulfill({ status: 404, json: { error: { code: "NOT_FOUND", message: path } } });
  });
  await page.route("**/bff/auth/me", (route) => route.fulfill({ json: { id: "u1", email: "jacob@sims.family", name: "Jacob Sims" } }));
  await page.route("**/bff/bills**", (route) => route.fulfill({ json: { data: BILLS } }));
  await page.route("**/bff/maintenance/tasks**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/meal-plans**", (route) => route.fulfill({ json: { data: [], from: TODAY, to: TODAY } }));
  await page.route("**/bff/people**", (route) => route.fulfill({ json: { data: PEOPLE } }));
  await page.route("**/bff/lists**", (route) => route.fulfill({ json: { data: LISTS } }));
  await page.route("**/bff/activity**", (route) => route.fulfill({ json: { data: [] } }));
}

async function gotoToday(page: Page) {
  await page.goto("/dashboard");
  await expect(page.getByTestId("today-agenda")).toBeVisible();
}

/**
 * The "Add event" chip, found the way a person finds it — role + name inside the
 * Quick actions group — and accepting EITHER a button (what it must be now) or a
 * link (what it was: the old, silent navigation). That is deliberate: on the old
 * code this locator still matches, the tap still happens, and the test fails on
 * the thing that was actually wrong — no form appeared and the page moved —
 * instead of failing early on a missing test id.
 */
function addEventChip(page: Page) {
  const group = page.getByRole("group", { name: "Quick actions" });
  return group
    .getByRole("button", { name: "Add event", exact: true })
    .or(group.getByRole("link", { name: "Add event", exact: true }));
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = false) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

/** Wait for the sheet's slide-up to finish (240 ms) before measuring it. */
async function settle(dialog: import("@playwright/test").Locator) {
  await expect
    .poll(async () => dialog.evaluate((el) => getComputedStyle(el).transform), { timeout: 3000 })
    .toMatch(/^(none|matrix\(1, 0, 0, 1, 0, 0\))$/);
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(colorScheme, () => {
    test.use({ colorScheme });

    // ── the finding ────────────────────────────────────────────
    test("phone 390: Add event on Today opens the event form in place — no page change", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      await stubToday(page);
      await gotoToday(page);

      const add = addEventChip(page);
      await expect(add).toBeVisible();
      const url = page.url();
      await snap(page, testInfo, `today-quick-actions-${colorScheme}`);

      await add.click();

      // THE FINDING, stated first: a form appears and the page does not move.
      // On the old code this tap went to /dashboard/calendar, so this is the
      // assertion that fails with "dialog not visible".
      const dialog = page.getByRole("dialog", { name: "New event" });
      await expect(dialog.getByTestId("event-editor")).toBeVisible();
      expect(page.url(), "the tap must not navigate").toBe(url);
      await expect(page).toHaveURL(/\/dashboard$/);
      await expect(page.getByTestId("calendar-view")).toHaveCount(0);

      // The fields G9 measured as absent (0 inputs after the tap).
      await expect(dialog.getByTestId("editor-title")).toBeVisible();
      await expect(dialog.getByRole("textbox", { name: "What's happening?" })).toBeVisible();
      await expect(dialog.getByTestId("editor-start-date")).toBeVisible();
      await expect(dialog.getByTestId("editor-save")).toBeVisible();

      // It also SAYS what it does: a button that opens a dialog, not a link
      // that quietly changes the page (the R2 G9 failure).
      await expect(add).toHaveAttribute("aria-haspopup", "dialog");
      await expect(page.getByRole("group", { name: "Quick actions" }).getByRole("link", { name: "Add event" })).toHaveCount(0);

      // The wave's phone idiom: a full-width bottom sheet, anchored to the
      // bottom, with a drag handle — not a floating card and not a new page.
      await settle(dialog);
      const box = (await dialog.boundingBox())!;
      expect(box.x).toBeCloseTo(0, 0);
      expect(box.width).toBeCloseTo(390, 0);
      expect(box.y + box.height).toBeCloseTo(844, 0);
      await expect(dialog.locator("[data-responsive-modal-handle]")).toBeVisible();

      // It opens ready to type, on the day the person is looking at, in the
      // one calendar they may write in — the BILLS calendar is not offered.
      await expect(dialog.getByTestId("editor-title")).toBeFocused();
      await expect(dialog.getByTestId("editor-start-date")).toHaveValue(TODAY);
      await expect(dialog.getByTestId("editor-calendar")).toHaveValue("Family");

      await snap(page, testInfo, `today-add-event-sheet-${colorScheme}`);
    });

    // ── and it is the Calendar's own form, wired to the same write ──
    test("phone 390: the form Today opens saves the event the person typed", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await freezeToday(page);
      await stubToday(page);

      const writes: { method: string; url: string; body: Record<string, unknown> }[] = [];
      await page.route("**/bff/calendars/c-family/events", async (route) => {
        if (route.request().method() === "POST") {
          const body = route.request().postDataJSON() as Record<string, unknown>;
          writes.push({ method: "POST", url: route.request().url(), body });
          return route.fulfill({ status: 201, json: { data: { id: "new-1", ...body } } });
        }
        return route.fulfill({ json: { data: EVENTS } });
      });

      await gotoToday(page);
      const url = page.url();
      await addEventChip(page).click();
      const dialog = page.getByRole("dialog", { name: "New event" });
      await expect(dialog.getByTestId("event-editor")).toBeVisible();
      expect(page.url(), "the tap must not navigate").toBe(url);

      await dialog.getByTestId("editor-title").fill("Dentist");
      await dialog.getByTestId("editor-start-time").fill("15:30");
      await dialog.getByTestId("editor-end-time").fill("16:15");
      await snap(page, testInfo, `today-add-event-filled-${colorScheme}`);
      await dialog.getByTestId("editor-save").click();

      await expect.poll(() => writes.length).toBe(1);
      const body = writes[0].body;
      expect(writes[0].url).toContain("/bff/calendars/c-family/events");
      expect(body.title).toBe("Dentist");
      expect(body.all_day).toBe(false);
      expect(body.rrule).toBe("");
      expect(body.event_type).toBe("family");
      // 3:30 PM in New York on Nov 17 is 20:30Z (EST, UTC-5): the form's own
      // rule, so Today and the Calendar write the same instant.
      expect(body.start).toBe("2026-11-17T20:30:00.000Z");
      expect(body.end).toBe("2026-11-17T21:15:00.000Z");

      // It closes and says the family sees it — no silent success.
      await expect(dialog).toBeHidden();
      await expect(page.getByText("Event added. Everyone in the family sees it.")).toBeVisible();
      expect(page.url()).toContain("/dashboard");
    });
  });
}

// ── the empty agenda's Add event is the same control ─────────────────
test.describe("an empty day", () => {
  test.use({ colorScheme: "light" });

  test("phone 390: the empty agenda's Add event opens the form too", async ({ loggedInPage: page }, testInfo) => {
    onlyOn("phone");
    await page.setViewportSize({ width: 390, height: 844 });
    await freezeToday(page);
    await stubToday(page, { events: [] });
    await gotoToday(page);

    const empty = page.getByTestId("today-agenda");
    await expect(empty).toContainText("Nothing scheduled today");
    const add = page.getByTestId("today-add-event-empty");
    await expect(add).toBeVisible();
    const url = page.url();
    await add.click();

    const dialog = page.getByRole("dialog", { name: "New event" });
    await expect(dialog.getByTestId("event-editor")).toBeVisible();
    expect(page.url()).toBe(url);
    await expect(dialog.getByTestId("editor-start-date")).toHaveValue(TODAY);

    // Closing returns to the page untouched (the sheet closes; nothing saved).
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(dialog).toBeHidden();
    await expect(page.getByTestId("today-agenda")).toContainText("Nothing scheduled today");
    await snap(page, testInfo, "today-add-event-empty-day");
  });
});

// ── the honest failure modes ────────────────────────────────────────
test.describe("when the form cannot work", () => {
  test.use({ colorScheme: "light" });

  test("phone 390: a failed calendars read says so inside the form, never an empty select", async ({ loggedInPage: page }) => {
    onlyOn("phone");
    await page.setViewportSize({ width: 390, height: 844 });
    await freezeToday(page);
    await stubToday(page, { failCalendars: true });
    await gotoToday(page);

    await addEventChip(page).click();
    const dialog = page.getByRole("dialog", { name: "New event" });
    await expect(dialog.getByTestId("event-editor")).toBeVisible();
    // "No calendars" would be a claim the page cannot make; it names the failure.
    await expect(dialog.getByTestId("editor-error")).toContainText("couldn't be loaded");
    await expect(dialog.getByTestId("editor-calendar")).toBeDisabled();
  });

  test("phone 390: a house-sitter is not offered a form the API would refuse", async ({ loggedInPage: page }) => {
    onlyOn("phone");
    await page.setViewportSize({ width: 390, height: 844 });
    await freezeToday(page);
    await stubToday(page, { role: "housesitter" });
    await gotoToday(page);

    // ResCalendar write is owner/family_manager/family_member: the day is
    // readable, adding to it is not. The chip is ABSENT — the same idiom as the
    // Calendar page, which hides its New event control from a sitter — rather
    // than a link to another page (the old behaviour) or a form that 403s.
    await expect(addEventChip(page)).toHaveCount(0);
    await expect(page.getByTestId("event-editor")).toHaveCount(0);
    // The rest of the quick actions are still there: the day is not a dead end.
    await expect(page.getByRole("group", { name: "Quick actions" }).getByRole("link", { name: "Plan dinner" })).toBeVisible();
  });

  test("phone 390: a viewer may read Today but is not offered the event form either", async ({ loggedInPage: page }) => {
    onlyOn("phone");
    await page.setViewportSize({ width: 390, height: 844 });
    await freezeToday(page);
    await stubToday(page, { role: "viewer" });
    await gotoToday(page);
    await expect(addEventChip(page)).toHaveCount(0);
  });
});

// ── desktop: the same control, the centred dialog ───────────────────
test.describe("desktop", () => {
  test.use({ colorScheme: "light" });

  test("desktop 1440: Add event opens the centred dialog, not a page change", async ({ loggedInPage: page }, testInfo) => {
    onlyOn("desktop");
    await page.setViewportSize({ width: 1440, height: 900 });
    await freezeToday(page);
    await stubToday(page);
    await gotoToday(page);

    const url = page.url();
    await addEventChip(page).click();
    const dialog = page.getByRole("dialog", { name: "New event" });
    await expect(dialog.getByTestId("event-editor")).toBeVisible();
    expect(page.url(), "the tap must not navigate").toBe(url);

    // >= 768 px: a centred dialog, not the full-height sheet.
    await settle(dialog);
    const box = (await dialog.boundingBox())!;
    expect(box.width).toBeLessThan(760);
    expect(box.x).toBeGreaterThan(200);
    expect(box.y).toBeGreaterThan(0);
    expect(box.y + box.height).toBeLessThan(900);
    await expect(dialog.locator("[data-responsive-modal-handle]")).toBeHidden();

    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "today-add-event-desktop");
  });
});
