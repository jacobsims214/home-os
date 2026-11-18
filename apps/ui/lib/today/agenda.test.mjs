// Run: npm test. The pure derivations behind Today (#4549).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  activityAvatar,
  activityHref,
  activityProbePath,
  activityTarget,
  activityWindow,
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


// ── the window onto the feed (#4577) ─────────────────────────

const feedRow = (i, over = {}) => ({
  id: i,
  action: "create",
  entity_type: "list_item",
  entity_id: `i${i}`,
  summary: `created list item "thing ${i}"`,
  description: `Amber created list item "thing ${i}"`,
  created_at: "2026-11-17T12:00:00Z",
  actor: AMBER,
  via_agent: false,
  agent: null,
  ...over,
});

test("Today shows a few changes and says how many it did not show", () => {
  const many = Array.from({ length: 8 }, (_, i) => feedRow(i + 1));
  const w = activityWindow(many, 4);
  assert.equal(w.rows.length, 4, "four rows, not the whole feed");
  assert.equal(w.rows[0].id, 1, "newest first, as the API sends them");
  assert.equal(w.hidden, 4);

  const exactly = activityWindow(many.slice(0, 4), 4);
  assert.equal(exactly.hidden, 0, "nothing hidden means no 'more' row");

  const fewer = activityWindow(many.slice(0, 2), 4);
  assert.equal(fewer.rows.length, 2);
  assert.equal(fewer.hidden, 0, "a short feed never reports a negative count");

  assert.deepEqual(activityWindow([], 4), { rows: [], hidden: 0 });
  // A zero/negative limit cannot produce a negative "hidden".
  assert.deepEqual(activityWindow(many, 0), { rows: [], hidden: 8 });
});

// ── where a change opens (#4577) ─────────────────────────────

test("a change opens the record's own page where one exists", () => {
  assert.equal(activityHref({ entity_type: "bill", entity_id: "b1" }), "/dashboard/bills/b1");
  assert.equal(activityHref({ entity_type: "pet", entity_id: "pet1" }), "/dashboard/pets/pet1");
  assert.equal(activityHref({ entity_type: "recipe", entity_id: "r1" }), "/dashboard/recipes/r1");
});

test("a kind whose page is not per-record opens that section, not a fake id", () => {
  assert.equal(activityHref({ entity_type: "calendar_event", entity_id: "e1" }), "/dashboard/calendar");
  assert.equal(activityHref({ entity_type: "maintenance_task", entity_id: "m1" }), "/dashboard/maintenance");
  assert.equal(activityHref({ entity_type: "person", entity_id: "p1" }), "/dashboard/members");
});

test("a kind with no page yet returns no link rather than a dead one", () => {
  for (const kind of ["list", "list_item", "note", "secret", "file", "guide", "budget", "unknown_kind"]) {
    assert.equal(activityHref({ entity_type: kind, entity_id: "x1" }), null, `${kind} must not link`);
  }
  assert.equal(activityHref({ entity_type: "bill", entity_id: "" }), null, "no id, no per-record link");
});

test("only a per-record page is probed, and only with an id", () => {
  assert.equal(activityProbePath({ entity_type: "bill", entity_id: "b1" }), "/api/v1/bills/b1");
  assert.equal(activityProbePath({ entity_type: "assET".toLowerCase(), entity_id: "a1" }), "/api/v1/assets/a1");
  // A section page cannot answer "does this record exist", so it is not probed.
  assert.equal(activityProbePath({ entity_type: "calendar_event", entity_id: "e1" }), null);
  assert.equal(activityProbePath({ entity_type: "list_item", entity_id: "i1" }), null);
  assert.equal(activityProbePath({ entity_type: "bill", entity_id: "" }), null);
});

test("a deletion says so: there is nothing left to open", () => {
  const t = activityTarget({ action: "delete", entity_type: "bill", entity_id: "b1" });
  assert.equal(t.kind, "deleted");
  assert.equal(t.href, null);
  assert.match(t.note, /Deleted/);
});

test("a record that answers 404 is reported gone, not linked to an empty page", () => {
  const t = activityTarget({ action: "update", entity_type: "bill", entity_id: "b1" }, false);
  assert.equal(t.kind, "gone");
  assert.equal(t.href, null);
  assert.match(t.note, /deleted since/);
});

test("an unchecked or failed probe is NEVER called deleted", () => {
  // undefined: nobody asked. null: the check failed for a reason other than 404.
  for (const exists of [undefined, null, true]) {
    const t = activityTarget({ action: "update", entity_type: "bill", entity_id: "b1" }, exists);
    assert.equal(t.kind, "link", `exists=${String(exists)} must still link`);
    assert.equal(t.href, "/dashboard/bills/b1");
    assert.equal(t.note, null);
  }
});

test("a kind with no page is text only, and a deletion of it still says deleted", () => {
  const none = activityTarget({ action: "create", entity_type: "list_item", entity_id: "i1" }, true);
  assert.equal(none.kind, "none");
  assert.equal(none.href, null);
  assert.equal(none.note, null, "no page is not the same claim as deletion");
  const del = activityTarget({ action: "delete", entity_type: "list_item", entity_id: "i1" });
  assert.equal(del.kind, "deleted");
});

// ── the actor's avatar (#4577) ───────────────────────────────

const NORA = { id: "p3", household_id: "h1", display_name: "Nora Sims", allergies: [], color: "red" };
const AMBER_PERSON = { id: "p2", household_id: "h1", display_name: "Amber Sims", allergies: [], color: "violet", user_id: "u2" };

test("an avatar is initials, never the word You — two letters is all a circle holds", () => {
  const mine = activityAvatar({ actor: JACOB }, "u1");
  assert.equal(mine.label, "JS", "the viewer's own write shows their initials, not 'You'");
  assert.equal(mine.color, null, "no people record passed: no colour invented");

  const amber = activityAvatar({ actor: AMBER }, "u1");
  assert.equal(amber.label, "AS");
});

test("the avatar takes the member's own colour from their people record", () => {
  // By person_id.
  assert.equal(activityAvatar({ actor: AMBER }, "u1", [AMBER_PERSON]).color, "violet");
  // By user_id, for a people row that carries the login.
  assert.equal(activityAvatar({ actor: { user_id: "u1", person_id: null, name: "Jacob Sims" } }, "u1", [{ ...NORA, id: "p1", user_id: "u1", color: "cyan" }]).color, "cyan");
  // Somebody not in the family list: no colour, and the fallback style.
  assert.equal(activityAvatar({ actor: { user_id: "u9", name: "Dana Kim" } }, "u1", [AMBER_PERSON]).color, null);
});

test("an actor the API did not name gets no invented initials", () => {
  assert.equal(activityAvatar({ actor: null }, "u1").label, "?", "an unnamed actor is never given a name");
  // A viewer write whose actor row carries no name still reads as theirs.
  assert.equal(activityAvatar({ actor: { user_id: "u1", name: null } }, "u1").label, "Y");
});

test("a single-word name yields one letter, and non-ASCII is kept upper-cased", () => {
  assert.equal(activityAvatar({ actor: { user_id: "u2", name: "Amber" } }, "u1").label, "A");
  assert.equal(activityAvatar({ actor: { user_id: "u2", name: "élise moreau" } }, "u1").label, "ÉM");
});
