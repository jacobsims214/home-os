// Run: npm test. The pure derivations behind the calendar page (#4550).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  attributionLine,
  attributionMap,
  billReading,
  buildAgenda,
  canWriteIn,
  dueLabel,
  emptyReason,
  civilInstant,
  daysFromEvents,
  eventBody,
  instantInZone,
  missingDayTable,
  repeatsForever,
  recurrenceFromForm,
  zoneParts,
  formatTimeRange,
  formatWallClock,
  formatWhen,
  groupSeries,
  monthDay,
  monthGrid,
  occurrenceCount,
  rangeFor,
  readOnlyReason,
  reminderLabel,
  remindersLine,
  repeatSummary,
  ruleLabel,
  timesLabel,
} from "./model.ts";

// ── fixtures: the shapes GET /calendars/events answers ───────
//
// Every event here carries local_date / weekday / local_time, because the API
// does (apps/api/internal/calendar/localday.go). The tests never pass an
// instant without them: the point of the contract is that the page does not
// date anything itself.

/** A Tue/Thu piano lesson series: two occurrences in the range, one card. */
const PIANO = [
  {
    id: "piano", calendar_id: "family", event_type: "family", title: "Piano lesson",
    start: "2026-11-17T21:00:00Z", end: "2026-11-17T21:45:00Z", all_day: false,
    rrule: "FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261217T235959Z", recurring: true,
    recurrence_id: "2026-11-17T21:00:00Z",
    local_date: "2026-11-17", weekday: "Tuesday", local_time: "16:00",
    end_local_date: "2026-11-17", end_local_time: "16:45",
  },
  {
    id: "piano", calendar_id: "family", event_type: "family", title: "Piano lesson",
    start: "2026-11-19T21:00:00Z", end: "2026-11-19T21:45:00Z", all_day: false,
    rrule: "FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261217T235959Z", recurring: true,
    recurrence_id: "2026-11-19T21:00:00Z",
    local_date: "2026-11-19", weekday: "Thursday", local_time: "16:00",
    end_local_date: "2026-11-19", end_local_time: "16:45",
  },
];

const ALL_DAY_HOLIDAY = {
  id: "thanksgiving", calendar_id: "family", event_type: "family", title: "Thanksgiving in Asheville",
  start: "2026-11-26T00:00:00Z", end: "2026-11-30T00:00:00Z", all_day: true,
  local_date: "2026-11-26", weekday: "Thursday", end_local_date: "2026-11-29",
};

const BILL_MIRROR = {
  id: "bill-elec", calendar_id: "bills", event_type: "bill", entity_type: "bill", entity_id: "b-elec",
  title: "Duke Energy", start: "2026-11-18T00:00:00Z", end: "2026-11-19T00:00:00Z", all_day: true,
  rrule: "FREQ=MONTHLY;BYMONTHDAY=18", recurring: true, recurrence_id: "2026-11-18T00:00:00Z",
  local_date: "2026-11-18", weekday: "Wednesday", end_local_date: "2026-11-18",
};

const STALE_BILL_ENTRY = {
  id: "legacy", calendar_id: "bills", event_type: "event",
  title: "Electric bill due — $180", start: "2026-11-18T00:00:00Z", end: "2026-11-19T00:00:00Z",
  all_day: true, local_date: "2026-11-18", weekday: "Wednesday",
};

const DAYS = [
  { local_date: "2026-11-16", weekday: "Monday" },
  { local_date: "2026-11-17", weekday: "Tuesday" },
  { local_date: "2026-11-18", weekday: "Wednesday" },
  { local_date: "2026-11-19", weekday: "Thursday" },
  { local_date: "2026-11-20", weekday: "Friday" },
  { local_date: "2026-11-21", weekday: "Saturday" },
  { local_date: "2026-11-22", weekday: "Sunday" },
];

// ── one recurring event is ONE thing ─────────────────────────

test("a recurring series is one card with its occurrences, not N rows", () => {
  const series = groupSeries(PIANO);
  assert.equal(series.length, 1, "two occurrences of one series must group into one");
  assert.equal(series[0].id, "piano");
  assert.equal(series[0].repeating, true);
  assert.equal(occurrenceCount(series[0]), 2);
  assert.deepEqual(
    series[0].occurrences.map((o) => o.local_date),
    ["2026-11-17", "2026-11-19"],
    "occurrences are in date order and keep their own days",
  );
});

test("two different events stay two cards", () => {
  const series = groupSeries([...PIANO, ALL_DAY_HOLIDAY]);
  assert.equal(series.length, 2);
  assert.deepEqual(series.map((s) => s.id), ["thanksgiving", "piano"], "all-day sorts first");
});

test("a one-off is not called repeating", () => {
  const [s] = groupSeries([ALL_DAY_HOLIDAY]);
  assert.equal(s.repeating, false);
  assert.equal(s.rrule, undefined);
});

test("the series card is on every day its occurrences fall on", () => {
  const days = buildAgenda(PIANO, DAYS, "2026-11-17");
  const withPiano = days.filter((d) => d.series.some((s) => s.id === "piano")).map((d) => d.localDate);
  assert.deepEqual(withPiano, ["2026-11-17", "2026-11-19"]);
  // …and it is the same series in both, never two series.
  assert.equal(days.flatMap((d) => d.series).length, 2);
  assert.equal(new Set(days.flatMap((d) => d.series).map((s) => s.id)).size, 1);
});

// ── the weekday and the day come from the data ───────────────

test("headings print the weekday the API sent, not one derived from a date", () => {
  const days = buildAgenda([], DAYS, "2026-11-19");
  assert.equal(days[0].weekday, "Monday");
  assert.equal(days[3].weekday, "Thursday");
  assert.equal(days[3].isToday, true);
  assert.equal(days[3].monthDay, "Nov 19");
  // An empty day is still a row: the gap is stated, not omitted.
  assert.equal(days.every((d) => d.series.length === 0), true);
});

test("an all-day event sits on its own local_date and says All day", () => {
  const [s] = groupSeries([ALL_DAY_HOLIDAY]);
  assert.equal(s.localDate, "2026-11-26", "the stored instant is midnight UTC; the date must not move");
  assert.equal(s.weekday, "Thursday");
  assert.equal(formatTimeRange(ALL_DAY_HOLIDAY), "All day");
  assert.equal(formatWhen(ALL_DAY_HOLIDAY), "Thu, Nov 26 – Nov 29 · All day");
  // With the listing's day table, the end day gets its weekday too.
  assert.equal(formatWhen(ALL_DAY_HOLIDAY, (d) => (d === "2026-11-29" ? "Sunday" : "")), "Thu, Nov 26 – Sun, Nov 29 · All day");
});

test("a timed event's time comes from local_time, never from the instant", () => {
  // 21:00Z is 16:00 in New York. Reading the instant in a browser in London
  // would print 9:00 PM; the API's local_time says 16:00, so we print 4:00 PM.
  assert.equal(formatWallClock("16:00"), "4:00 PM");
  assert.equal(formatTimeRange(PIANO[0]), "4:00 – 4:45 PM");
  assert.equal(formatWhen(PIANO[0]), "Tue, Nov 17 · 4:00 – 4:45 PM");
});

test("midnight and noon read correctly", () => {
  assert.equal(formatWallClock("00:00"), "12:00 AM");
  assert.equal(formatWallClock("12:00"), "12:00 PM");
  assert.equal(formatWallClock(""), "");
  assert.equal(formatWallClock(undefined), "");
});

test("a range label is read from the digits, not built as a Date", () => {
  assert.equal(monthDay("2026-11-09"), "Nov 9");
  assert.equal(monthDay("not-a-date"), "not-a-date");
});

// ── recurrence labels ───────────────────────────────────────

test("a rule reads as a sentence, with its end", () => {
  assert.equal(ruleLabel("FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261217T235959Z"), "Every Tue and Thu until Dec 17, 2026");
  assert.equal(ruleLabel("FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"), "Every weekday");
  assert.equal(ruleLabel("FREQ=WEEKLY;INTERVAL=2;BYDAY=TH"), "Every 2 weeks on Thu");
  assert.equal(ruleLabel("FREQ=DAILY"), "Every day");
  assert.equal(ruleLabel("FREQ=MONTHLY;BYMONTHDAY=18"), "Every month on day 18");
  assert.equal(ruleLabel("FREQ=MONTHLY;BYDAY=3TU"), "Every month on the 3rd Tuesday");
  assert.equal(ruleLabel("FREQ=WEEKLY;COUNT=10"), "Every week, 10 times");
  assert.equal(ruleLabel(""), "");
  assert.equal(ruleLabel(undefined), "");
  assert.equal(repeatSummary(PIANO[0]), "Every Tue and Thu until Dec 17, 2026");
});

test("occurrence counts read like a person says them", () => {
  assert.equal(timesLabel(1), "once");
  assert.equal(timesLabel(8), "8 times");
});

// ── bill state never comes from a mirror ────────────────────

const BILLS = [
  {
    id: "b-elec", name: "Duke Energy", amount: "142.18", frequency: "monthly", due_day: 18,
    due_date: null, is_autopay: false, paid_date: "2026-11-15", paid_for_due_date: "2026-11-18",
  },
];

test("a PAID bill's mirror does not read as due", () => {
  const reading = billReading(BILL_MIRROR, BILLS, "2026-11-17");
  assert.equal(reading.kind, "bill");
  assert.equal(reading.state, "paid");
  assert.equal(reading.paidFor, "2026-11-18");
});

test("an unpaid bill's mirror reads as due, with the amount", () => {
  const unpaid = [{ ...BILLS[0], paid_date: null, paid_for_due_date: null }];
  const reading = billReading(BILL_MIRROR, unpaid, "2026-11-15");
  assert.equal(reading.kind, "bill");
  assert.equal(reading.state, "due");
  assert.equal(reading.amount, "$142.18");
  assert.equal(reading.daysUntil, 3);
  assert.equal(dueLabel(reading.daysUntil), "Due in 3 days");
});

test("an unlinked entry on the Bills calendar is not bill state", () => {
  const reading = billReading(STALE_BILL_ENTRY, BILLS, "2026-11-17", { onBillsCalendar: true });
  assert.equal(reading.kind, "unlinked", "no bill link is no bill state, whatever the title says");
});

test("a family event is not a bill at all", () => {
  assert.equal(billReading(PIANO[0], BILLS, "2026-11-17").kind, "not-bill");
});

test("bill-like-but-unmatched entries are never presented as a bill", () => {
  const entry = { ...BILL_MIRROR, entity_id: undefined };
  const reading = billReading(entry, BILLS, "2026-11-17");
  assert.equal(reading.kind, "unlinked");
});

// ── who added what ──────────────────────────────────────────

test("attribution names the person and the agent, and says You for the viewer", () => {
  const feed = [
    {
      id: 1, action: "create", entity_type: "calendar_event", entity_id: "piano",
      created_at: "2026-11-17T13:00:00Z",
      actor: { user_id: "u2", person_id: "p2", name: "Amber Sims" }, via_agent: true,
      agent: { token_id: "t1", name: "moltis-wg-amber" },
    },
    {
      id: 2, action: "update", entity_type: "calendar_event", entity_id: "thanksgiving",
      created_at: "2026-11-17T12:00:00Z",
      actor: { user_id: "u1", person_id: "p1", name: "Jacob Sims" }, via_agent: false, agent: null,
    },
  ];
  const map = attributionMap(feed, "u1", new Date("2026-11-17T14:00:00Z"));
  assert.equal(map.get("piano").who, "Amber");
  assert.equal(map.get("piano").viaAgent, true);
  assert.equal(attributionLine(map.get("piano")), "Added by Amber via moltis-wg-amber · 1 h ago");
  assert.equal(map.get("thanksgiving").who, "You");
  assert.equal(attributionLine(map.get("thanksgiving")), "Added by You · 2 h ago");
});

test("an event nobody touched has no attribution, and none is invented", () => {
  const map = attributionMap([], "u1");
  assert.equal(map.get("piano"), undefined);
  assert.equal(attributionLine(undefined), "");
});

test("attribution takes the newest entry for an event", () => {
  const feed = [
    { id: 9, action: "update", entity_type: "calendar_event", entity_id: "piano", created_at: "2026-11-17T13:00:00Z", actor: { user_id: "u2", name: "Amber Sims" } },
    { id: 8, action: "create", entity_type: "calendar_event", entity_id: "piano", created_at: "2026-11-01T13:00:00Z", actor: { user_id: "u1", name: "Jacob Sims" } },
  ];
  const map = attributionMap(feed, "u3");
  assert.equal(map.get("piano").who, "Amber");
});

test("a deleted event contributes nothing", () => {
  const feed = [{ id: 7, action: "delete", entity_type: "calendar_event", entity_id: "piano", created_at: "2026-11-17T13:00:00Z", actor: { user_id: "u2", name: "Amber Sims" } }];
  assert.equal(attributionMap(feed, "u1").get("piano"), undefined);
});

// ── reminders ───────────────────────────────────────────────

test("reminders read as the list the API stores", () => {
  assert.equal(reminderLabel(15), "15 minutes before");
  assert.equal(reminderLabel(1440), "1 day before");
  assert.equal(reminderLabel(90), "90 minutes before");
  assert.equal(remindersLine([{ minutes_before: 15 }, { minutes_before: 60 }]), "15 minutes before, 1 hour before");
  assert.equal(remindersLine([]), "No reminders");
  assert.equal(remindersLine(undefined), "No reminders");
});

// ── read-only reasons ───────────────────────────────────────

test("a bill or upkeep mirror says where it comes from", () => {
  const bills = { id: "bills", name: "Bills", type: "bills", color: "#5c940d" };
  assert.match(readOnlyReason(BILL_MIRROR, bills), /bill's due date.*Money/);
  assert.match(readOnlyReason({ ...PIANO[0], event_type: "maintenance", entity_type: "maintenance_task" }, bills), /maintenance task/);
});

test("a family event in a writable calendar has no read-only reason", () => {
  const family = { id: "family", name: "Family", type: "family", color: "#0b7285" };
  assert.equal(readOnlyReason(PIANO[0], family), "");
  assert.equal(canWriteIn(family), true);
  assert.equal(canWriteIn({ id: "bills", name: "Bills", type: "bills", color: "" }), false);
});

// ── the range and the grid ──────────────────────────────────

test("the page always asks for both bounds: recurrence expands only in a range", () => {
  for (const view of ["agenda", "day", "week", "month"]) {
    const r = rangeFor("2026-11-17", view);
    assert.match(r.start, /^\d{4}-\d{2}-\d{2}$/);
    assert.match(r.end, /^\d{4}-\d{2}-\d{2}$/);
    assert.ok(r.end > r.start, `${view} range must not be empty`);
  }
  assert.deepEqual(rangeFor("2026-11-17", "week"), { start: "2026-11-17", end: "2026-11-24" });
  assert.deepEqual(rangeFor("2026-11-17", "day"), { start: "2026-11-17", end: "2026-11-18" });
});

test("the month grid starts on a Sunday and is six weeks, per the API's day table", () => {
  // November 2026 starts on a Sunday — that is in the data, not computed here.
  const days = [];
  let d = new Date(Date.UTC(2026, 10, 1));
  for (let i = 0; i < 50; i++) {
    const iso = d.toISOString().slice(0, 10);
    days.push({ local_date: iso, weekday: d.toLocaleDateString("en-US", { weekday: "long", timeZone: "UTC" }) });
    d = new Date(d.getTime() + 86_400_000);
  }
  const grid = monthGrid("2026-11-01", days);
  assert.equal(grid.length, 42);
  assert.equal(grid[0].local_date, "2026-11-01");
  assert.equal(grid[0].weekday, "Sunday");
  assert.equal(grid[41].weekday, "Saturday");
});

// ── the empty state explains itself ─────────────────────────

test("an empty agenda says why it is empty, and what fills it", () => {
  const fresh = emptyReason({ hiddenCount: 0, calendarCount: 1, hasAnyEvents: false, rangeLabel: "this week", canWrite: true });
  assert.match(fresh.title, /Nothing on the calendar/);
  assert.match(fresh.body, /Repeating events show once/);
  assert.equal(fresh.action, "new");

  const hidden = emptyReason({ hiddenCount: 2, calendarCount: 4, hasAnyEvents: false, rangeLabel: "this week", canWrite: false });
  assert.match(hidden.body, /2 calendars are hidden/);
  assert.equal(hidden.action, "show-all");

  const noCals = emptyReason({ hiddenCount: 0, calendarCount: 0, hasAnyEvents: false, rangeLabel: "this week", canWrite: false });
  assert.match(noCals.title, /No calendars yet/);
  assert.equal(noCals.action, null);

  const reader = emptyReason({ hiddenCount: 0, calendarCount: 2, hasAnyEvents: false, rangeLabel: "this week", canWrite: false });
  assert.match(reader.body, /Only the family can add events/);
  assert.equal(reader.action, null);
});

// ── the editor: what a person typed becomes what the API takes ──

test("a weekly lesson keeps its wall clock across a DST change", () => {
  // 4:00 PM in New York is 21:00Z before the change and 20:00Z after it.
  const before = instantInZone("2026-10-27", "16:00", "America/New_York");
  const after = instantInZone("2026-11-17", "16:00", "America/New_York");
  assert.equal(before, "2026-10-27T20:00:00.000Z", "EDT: 16:00 is UTC-4");
  assert.equal(after, "2026-11-17T21:00:00.000Z", "EST: 16:00 is UTC-5");
  // Reading either instant back in the zone gives 16:00 again.
  for (const iso of [before, after]) {
    const p = zoneParts(new Date(iso).getTime(), "America/New_York");
    assert.equal(p.h, 16);
    assert.equal(p.mi, 0);
  }
});

test("an all-day event is sent as a civil date at midnight UTC", () => {
  assert.equal(civilInstant("2026-11-26"), "2026-11-26T00:00:00.000Z");
  assert.equal(eventBody(ALLLD, null).start, "2026-11-26T00:00:00.000Z");
  // The stored end is exclusive: the last day it occupies plus one.
  assert.equal(eventBody(ALLLD, null).end, "2026-11-30T00:00:00.000Z");
  assert.equal(eventBody(ALLLD, null).all_day, true);
  assert.equal(eventBody(ALLLD, "America/New_York").timezone, undefined, "all-day events never carry a zone");
});

test("a repeat rule is kept ordered and de-duplicated, with one end", () => {
  assert.equal(recurrenceFromForm({ repeat: "" }), "");
  assert.equal(recurrenceFromForm({ repeat: "freq=weekly;byday=tu,th" }), "FREQ=WEEKLY;BYDAY=TU,TH");
  assert.equal(
    recurrenceFromForm({ repeat: "FREQ=WEEKLY;UNTIL=20261217;COUNT=10" }),
    "FREQ=WEEKLY;UNTIL=20261217",
    "UNTIL and COUNT together are invalid; the UI keeps UNTIL",
  );
  assert.equal(recurrenceFromForm({ repeat: "BYDAY=TU,TH" }), "", "a rule with no FREQ repeats nothing");
});

const ALLLD = {
  title: "Thanksgiving in Asheville", calendarId: "family", allDay: true,
  startDate: "2026-11-26", startTime: "09:00", endDate: "2026-11-29", endTime: "10:00",
  repeat: "", reminders: [], location: "", notes: "", eventType: "family",
};

test("the editor's body carries a timed series' zone, reminders capped at 10", () => {
  const body = eventBody(
    {
      title: " Piano lesson ", calendarId: "family", allDay: false,
      startDate: "2026-11-17", startTime: "16:00", endDate: "2026-11-17", endTime: "16:45",
      repeat: "FREQ=WEEKLY;BYDAY=TU,TH", reminders: Array.from({ length: 12 }, () => 15),
      location: " Ms. Alvarez's studio ", notes: "", eventType: "family",
    },
    "America/New_York",
  );
  assert.equal(body.title, "Piano lesson");
  assert.equal(body.start, "2026-11-17T21:00:00.000Z");
  assert.equal(body.end, "2026-11-17T21:45:00.000Z");
  assert.equal(body.timezone, "America/New_York");
  assert.equal(body.reminders.length, 10, "the API refuses more than 10");
  assert.equal(body.location, "Ms. Alvarez's studio");
});

test("a repeat that never ends is recognisable, so the editor can say so", () => {
  assert.equal(repeatsForever("FREQ=WEEKLY"), true);
  assert.equal(repeatsForever("FREQ=WEEKLY;COUNT=4"), false);
  assert.equal(repeatsForever(""), false);
});

// ── degrades without the API's day table ────────────────────

test("the agenda still groups by day when the API sends no day table", () => {
  const days = daysFromEvents(PIANO);
  assert.deepEqual(days, [
    { local_date: "2026-11-17", weekday: "Tuesday" },
    { local_date: "2026-11-19", weekday: "Thursday" },
  ]);
  const agenda = buildAgenda(PIANO, days, "2026-11-17");
  assert.equal(agenda.length, 2);
  assert.equal(agenda[0].isToday, true);
  assert.equal(agenda[1].weekday, "Thursday", "the weekday is the event's own, from the API");
});

test("a missing day table is reported, so the grid can say why it can't draw", () => {
  assert.equal(missingDayTable(undefined), true);
  assert.equal(missingDayTable([]), true);
  assert.equal(missingDayTable([{ local_date: "2026-11-17", weekday: "Tuesday" }]), false);
});
