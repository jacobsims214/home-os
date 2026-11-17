// Run: npm test. The pure derivations behind Today (#4549).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  attribution,
  billsAndUpkeepDue,
  buildAgenda,
  dinnerToday,
  feedParts,
  formatDueLabel,
  householdToday,
  isBillMirrorEvent,
  recurrenceLabel,
  recipeFacts,
  relativeToNow,
  upNextRow,
} from "./agenda.ts";

// ── the household's date ─────────────────────────────────────

test("today is the household's date, not the browser's", () => {
  // 2026-11-17T02:30Z is still Nov 16 in New York (UTC-5): a phone in another
  // zone must not move a family's day.
  const at = new Date("2026-11-17T02:30:00Z");
  assert.equal(householdToday("America/New_York", at), "2026-11-16");
  assert.equal(householdToday("Europe/London", at), "2026-11-17");
  assert.equal(householdToday("UTC", at), "2026-11-17");
  // An unknown zone falls back rather than throwing.
  assert.match(householdToday("Not/AZone", at), /^2026-11-1[67]$/);
});

// ── bill state never comes from a calendar event ─────────────

test("a bill's calendar mirror is not bill state and never reaches the agenda", () => {
  // The R1 failure: a stale Bills-calendar event looked like bill state, so an
  // agent said "Electric $180 due, still unpaid" about a bill paid that
  // morning. Today drops the mirror and reads /bills.
  assert.equal(isBillMirrorEvent({ event_type: "bill" }), true);
  assert.equal(isBillMirrorEvent({ event_type: "family", entity_type: "bill" }), true);
  assert.equal(isBillMirrorEvent({ event_type: "family", entity_type: "person" }), false);
  const events = [
    { id: "mirror", calendar_id: "c-bills", event_type: "bill", entity_type: "bill", title: "Electric", start: "2026-11-17T05:00:00Z", end: "2026-11-18T05:00:00Z", all_day: true, description: "Amount: $180.00" },
    { id: "e1", calendar_id: "c1", event_type: "family", title: "School drop-off", start: "2026-11-17T12:50:00Z", end: "2026-11-17T13:20:00Z", all_day: false },
  ];
  const rows = buildAgenda(events, "2026-11-17", "America/New_York", 12 * 60 + 42);
  assert.deepEqual(rows.map((r) => r.event.title), ["School drop-off"]);
});

test("a paid bill is not in the due list; an unpaid one is; a bad schedule is counted", () => {
  const bills = [
    // Paid this morning for today's date: NOT due.
    { id: "b1", name: "Electric", amount: "180.00", due_day: 17, frequency: "monthly", paid_date: "2026-11-17", paid_for_due_date: "2026-11-17" },
    // Unpaid, due in two days.
    { id: "b2", name: "Water", amount: "64.20", due_day: 19, frequency: "monthly", paid_date: null, paid_for_due_date: null },
    // No schedule: counted, not silently dropped.
    { id: "b3", name: "Mystery", amount: "10.00", due_day: null, frequency: "monthly" },
    // Outside the two-week window: due the 10th, and today is the 17th, so its
    // next date is 23 days out.
    { id: "b4", name: "Insurance", amount: "95.00", due_day: 10, frequency: "monthly" },
  ];
  const { rows, withoutDueDate } = billsAndUpkeepDue("2026-11-17", bills, []);
  assert.deepEqual(rows.map((r) => r.name), ["Water"]);
  assert.equal(rows[0].daysUntil, 2);
  assert.equal(withoutDueDate, 1);
});

test("overdue upkeep sorts first, and a done task is not due", () => {
  const tasks = [
    { id: "m1", name: "Replace HVAC filter", status: "pending", due_date: "2026-11-14T00:00:00Z" },
    { id: "m2", name: "Gutter cleaning", status: "pending", due_date: "2026-11-20T00:00:00Z" },
    { id: "m3", name: "Furnace service", status: "done", due_date: "2026-11-15T00:00:00Z" },
  ];
  const bills = [{ id: "b2", name: "Water", amount: "64.20", due_day: 19, frequency: "monthly" }];
  const { rows } = billsAndUpkeepDue("2026-11-17", bills, tasks);
  assert.deepEqual(rows.map((r) => r.name), ["Replace HVAC filter", "Water", "Gutter cleaning"]);
  assert.equal(rows[0].daysUntil, -3);
});

// ── the agenda ───────────────────────────────────────────────

const EVENTS = [
  { id: "e1", calendar_id: "c1", event_type: "family", title: "School drop-off", start: "2026-11-17T12:50:00Z", end: "2026-11-17T13:20:00Z", all_day: false, rrule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR", recurrence_id: "2026-11-17T12:50:00Z" },
  { id: "e2", calendar_id: "c1", event_type: "family", title: "Morning run", start: "2026-11-17T11:30:00Z", end: "2026-11-17T12:15:00Z", all_day: false, rrule: "FREQ=WEEKLY;BYDAY=MO,WE,FR" },
  { id: "e3", calendar_id: "c1", event_type: "family", title: "Trash day", start: "2026-11-17T05:00:00Z", end: "2026-11-18T05:00:00Z", all_day: true },
  { id: "e4", calendar_id: "c1", event_type: "family", title: "Tomorrow's thing", start: "2026-11-18T14:00:00Z", end: "2026-11-18T15:00:00Z", all_day: false },
];

test("the agenda is one merged list in time order, all-day first, past kept", () => {
  // Now is 07:42 in New York: the 6:30 run is past, drop-off has not started.
  const rows = buildAgenda(EVENTS, "2026-11-17", "America/New_York", 7 * 60 + 42);
  assert.deepEqual(rows.map((r) => r.event.title), ["Trash day", "Morning run", "School drop-off"]);
  assert.equal(rows[0].allDay, true);
  assert.equal(rows[1].past, true, "the 6:30 run is over");
  assert.equal(rows[1].time, "6:30 AM");
  assert.equal(rows[2].past, false);
  assert.equal(rows[2].time, "7:50 AM");
  // Up next is the first timed event that has not started.
  assert.equal(upNextRow(rows)?.event.title, "School drop-off");
});

test("an event in progress is happening now, not past", () => {
  // 08:00 New York: drop-off (7:50-8:20) is underway.
  const rows = buildAgenda(EVENTS, "2026-11-17", "America/New_York", 8 * 60);
  const drop = rows.find((r) => r.event.title === "School drop-off");
  assert.equal(drop.past, false);
  assert.equal(drop.happeningNow, true);
});

test("recurrence labels come from the rule, never from a guess", () => {
  assert.equal(recurrenceLabel("FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"), "Every weekday");
  assert.equal(recurrenceLabel("FREQ=WEEKLY;BYDAY=MO,WE,FR"), "Mon, Wed, Fri");
  assert.equal(recurrenceLabel("FREQ=WEEKLY;BYDAY=MO"), "Mon");
  assert.equal(recurrenceLabel("FREQ=MONTHLY;BYDAY=3MO"), "3rd Mon");
  assert.equal(recurrenceLabel("FREQ=DAILY"), "Every day");
  assert.equal(recurrenceLabel(""), "");
  assert.equal(recurrenceLabel(null), "");
});

test("relative times and due labels read like a parent says them", () => {
  assert.equal(relativeToNow(7 * 60 + 50, 7 * 60 + 42), "in 8 min");
  assert.equal(relativeToNow(10 * 60, 7 * 60), "in 3 h");
  assert.equal(relativeToNow(7 * 60 + 42, 7 * 60 + 42), "now");
  assert.equal(relativeToNow(6 * 60, 7 * 60 + 42), "");
  assert.equal(formatDueLabel("2026-11-17", "2026-11-17"), "Today");
  assert.equal(formatDueLabel("2026-11-18", "2026-11-17"), "Tomorrow");
  assert.equal(formatDueLabel("2026-11-19", "2026-11-17"), "Thu");
  // Inside a week the weekday alone is enough; past it, the date.
  assert.equal(formatDueLabel("2026-11-20", "2026-11-17"), "Fri");
  assert.equal(formatDueLabel("2026-11-24", "2026-11-17"), "Tue, Nov 24");
  assert.equal(formatDueLabel("2026-12-01", "2026-11-17"), "Tue, Dec 1");
});

// ── dinner ───────────────────────────────────────────────────

test("dinner is tonight's planned dinner, not tomorrow's", () => {
  const meals = [
    { id: "m1", date: "2026-11-17", slot: "lunch", title: "Sandwiches" },
    { id: "m2", date: "2026-11-17", slot: "dinner", title: "Sheet-pan chicken fajitas" },
    { id: "m3", date: "2026-11-18", slot: "dinner", title: "Pizza night" },
  ];
  assert.equal(dinnerToday(meals, "2026-11-17")?.title, "Sheet-pan chicken fajitas");
  assert.equal(dinnerToday(meals, "2026-11-19"), null);
  assert.equal(recipeFacts({ prep_minutes: 10, cook_minutes: 25, servings: 5 }), "35 min · serves 5");
  assert.equal(recipeFacts({ servings: 4 }), "serves 4");
  assert.equal(recipeFacts(null), "");
});

// ── attribution ──────────────────────────────────────────────

const AMBER = { user_id: "u2", person_id: "p2", name: "Amber Sims" };
const JACOB = { user_id: "u1", person_id: "p1", name: "Jacob Sims" };

test("an agent's own name is on the row: Amber via moltis-wg-amber", () => {
  const entry = {
    description: 'Amber created list item "oat milk" via agent "moltis-wg-amber"',
    summary: "created list item",
    actor: AMBER,
    via_agent: true,
    agent: { token_id: "t1", name: "moltis-wg-amber" },
  };
  const parts = feedParts(entry, "u1");
  assert.equal(parts.who, "Amber Sims");
  assert.equal(parts.text, 'created list item "oat milk"', "the agent tail becomes the chip, not a second phrase");
  assert.equal(parts.viaAgent, true);
  assert.equal(parts.agentName, "moltis-wg-amber");
});

test("the viewer's own write reads as You, and the name is not repeated", () => {
  const parts = feedParts(
    { description: 'Jacob created calendar event "Nora\'s check-up"', summary: "created", actor: JACOB, via_agent: false, agent: null },
    "u1",
  );
  assert.equal(parts.who, "You");
  assert.equal(parts.text, 'created calendar event "Nora\'s check-up"');
  assert.equal(parts.viaAgent, false);
});

test("an unnamed actor is never invented, and an unnamed agent says so", () => {
  const parts = feedParts({ description: "created list item \"bins\" via agent", summary: "created", actor: null, via_agent: true, agent: null }, "u1");
  assert.equal(parts.who, "Someone");
  assert.equal(parts.viaAgent, true);
  assert.equal(parts.agentName, "an agent");
  const when = attribution({ actor: null, via_agent: false, agent: null, created_at: "2026-11-17T12:00:00Z" }, "u1", new Date("2026-11-17T12:12:00Z"));
  assert.equal(when.when, "12 min ago");
  assert.equal(when.who, "Someone");
});
