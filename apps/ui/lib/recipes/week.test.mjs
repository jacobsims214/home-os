// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  addDays,
  addWeek,
  buildWeek,
  dateKey,
  dinnerRating,
  firstEmptyDay,
  firstUpcomingDay,
  mealAllergyPayload,
  mealTitle,
  parseDateKey,
  plannedDinners,
  weekHeading,
  weekLabel,
  weekStart,
} from "./week.ts";

const meal = (over) => ({
  id: over.id ?? "m1",
  household_id: "h1",
  date: over.date,
  slot: over.slot ?? "dinner",
  recipe_id: over.recipe_id ?? null,
  title: over.title ?? "Dinner",
  notes: null,
  feedback: over.feedback ?? [],
  created_at: "2026-11-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z",
});

test("dateKey never goes through UTC, so a date cannot shift a day", () => {
  // A local midnight in a negative-offset zone is the previous day in UTC;
  // using toISOString() here would be a classic off-by-one.
  assert.equal(dateKey(new Date(2026, 10, 16)), "2026-11-16");
  assert.equal(dateKey(new Date(2026, 0, 1)), "2026-01-01");
  assert.equal(dateKey(new Date(2026, 11, 31)), "2026-12-31");
});

test("parseDateKey is local and rejects nonsense", () => {
  const d = parseDateKey("2026-11-16");
  assert.ok(d);
  assert.equal(d.getFullYear(), 2026);
  assert.equal(d.getMonth(), 10);
  assert.equal(d.getDate(), 16);
  assert.equal(parseDateKey("16/11/2026"), null);
  assert.equal(parseDateKey("yesterday"), null);
});

test("addDays crosses month ends and a DST boundary", () => {
  assert.equal(addDays("2026-11-30", 1), "2026-12-01");
  assert.equal(addDays("2026-01-01", -1), "2025-12-31");
  // US DST ends 2026-11-01; adding a day across it must not lose or gain one.
  assert.equal(addDays("2026-10-31", 2), "2026-11-02");
  // A leap day.
  assert.equal(addDays("2028-02-28", 1), "2028-02-29");
});

test("weekStart is Monday, including from Sunday", () => {
  // 2026-11-16 is a Monday.
  assert.equal(weekStart("2026-11-16"), "2026-11-16");
  assert.equal(weekStart("2026-11-17"), "2026-11-16");
  assert.equal(weekStart("2026-11-22"), "2026-11-16", "Sunday belongs to the week that started Monday");
  assert.equal(weekStart("2026-11-23"), "2026-11-23");
  assert.equal(weekStart("2026-11-19"), "2026-11-16");
});

test("weekLabel and weekHeading", () => {
  assert.equal(weekLabel("2026-11-16"), "Nov 16 – 22");
  assert.equal(weekLabel("2026-11-30"), "Nov 30 – Dec 6");
  const today = "2026-11-18";
  assert.equal(weekHeading("2026-11-16", today), "This week");
  assert.equal(weekHeading("2026-11-23", today), "Next week");
  assert.equal(weekHeading("2026-11-09", today), "Last week");
  assert.equal(weekHeading("2026-12-07", today), "Dec 7 – 13");
});

test("buildWeek returns seven rows Mon..Sun and marks today", () => {
  const days = buildWeek("2026-11-16", [], "2026-11-18");
  assert.equal(days.length, 7);
  assert.deepEqual(days.map((d) => d.dow), ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"]);
  assert.deepEqual(days.map((d) => d.dayNum), ["16", "17", "18", "19", "20", "21", "22"]);
  assert.deepEqual(days.map((d) => d.isToday), [false, false, true, false, false, false, false]);
  assert.equal(days[2].long, "November 18");
});

test("buildWeek puts dinner on its row and collapses other slots into a count", () => {
  const days = buildWeek(
    "2026-11-16",
    [
      meal({ id: "d1", date: "2026-11-16", slot: "dinner", title: "Fajitas" }),
      meal({ id: "b1", date: "2026-11-16", slot: "breakfast", title: "Pancakes" }),
      meal({ id: "l1", date: "2026-11-16", slot: "lunch", title: "Leftovers" }),
      meal({ id: "d2", date: "2026-11-19", slot: "dinner", title: "Curry" }),
    ],
    "2026-11-16",
  );
  assert.equal(days[0].dinner.title, "Fajitas");
  assert.deepEqual(days[0].otherSlots.map((m) => m.slot), ["breakfast", "lunch"]);
  assert.equal(days[3].dinner.title, "Curry");
  assert.equal(days[1].dinner, null);
  assert.equal(plannedDinners(days), 2);
});

test("a dinner outside the week's dates never lands on a row", () => {
  const days = buildWeek("2026-11-16", [meal({ date: "2026-11-23", title: "Next Monday" })], "2026-11-16");
  assert.equal(days.filter((d) => d.dinner !== null).length, 0);
});

test("firstEmptyDay returns the empty slot a suggestion would fill", () => {
  const full = buildWeek("2026-11-16", Array.from({ length: 7 }, (_, i) => meal({ id: `m${i}`, date: addDays("2026-11-16", i) })), "2026-11-16");
  assert.equal(firstEmptyDay(full, "2026-11-16"), null);
  const partial = buildWeek("2026-11-16", [meal({ date: "2026-11-16" })], "2026-11-16");
  assert.equal(firstEmptyDay(partial, "2026-11-16").date, "2026-11-17");
});

// ── #5305: a past day is never "open" ───────────────────────

test("firstEmptyDay never offers a day that has already gone (R2 G8)", () => {
  // The reported week and day: Mon Nov 16 - Sun Nov 22, opened on Wed Nov 18.
  // Monday and Tuesday are empty. The OLD firstEmptyDay returned Monday the
  // 16th, so the page said "Mon is open — November 16 has no dinner planned.
  // Plan Mon's dinner" two days after that dinner would have been eaten.
  const days = buildWeek("2026-11-16", [], "2026-11-18");
  const next = firstEmptyDay(days, "2026-11-18");
  assert.ok(next, "some day of this week is still open");
  assert.equal(next.date, "2026-11-18", "it is today, not Monday the 16th");
  assert.equal(next.dow, "Wed");
  assert.equal(firstEmptyDay(days, "2026-11-16").date, "2026-11-16", "on Monday itself, Monday IS open");
  assert.equal(firstEmptyDay(days, "2026-11-17").date, "2026-11-17");
});

test("firstEmptyDay skips a planned today and lands on tomorrow", () => {
  // Wednesday is planned; Thursday is the first open day a family can cook.
  const days = buildWeek("2026-11-16", [meal({ date: "2026-11-18", title: "Pizza night" })], "2026-11-18");
  assert.equal(firstEmptyDay(days, "2026-11-18").date, "2026-11-19");
});

test("a week entirely in the past has no next empty day", () => {
  // Browsing back to last week must not invite anyone to plan a dinner for it.
  const lastWeek = buildWeek("2026-11-09", [], "2026-11-18");
  assert.equal(firstEmptyDay(lastWeek, "2026-11-18"), null);
  assert.equal(firstUpcomingDay(lastWeek, "2026-11-18"), null);
  // Its empty rows are marked past, so the page can render them as settled.
  assert.equal(lastWeek.every((d) => d.isPast), true);
});

test("buildWeek marks each row past / today / future against the family's day", () => {
  const days = buildWeek("2026-11-16", [], "2026-11-18");
  assert.deepEqual(
    days.map((d) => [d.date, d.isToday, d.isPast]),
    [
      ["2026-11-16", false, true],
      ["2026-11-17", false, true],
      ["2026-11-18", true, false],
      ["2026-11-19", false, false],
      ["2026-11-20", false, false],
      ["2026-11-21", false, false],
      ["2026-11-22", false, false],
    ],
  );
});

test("buildWeek with no known today calls nothing past (never a guess)", () => {
  // The page passes "" for the one paint before the household's day is known.
  // Nothing may be called gone on that paint: suppressing a nag is only safe
  // when the answer is certain.
  const days = buildWeek("2026-11-16", [], "");
  assert.equal(days.some((d) => d.isPast), false);
  assert.equal(days.some((d) => d.isToday), false);
});

test("firstUpcomingDay gives the dinner sheet a day that hasn't gone", () => {
  // Every remaining day is planned, so there is no empty slot, but the
  // "What's for dinner?" sheet still needs a day — a past one would be wrong.
  const planned = Array.from({ length: 7 }, (_, i) => meal({ id: `m${i}`, date: addDays("2026-11-16", i) }));
  const days = buildWeek("2026-11-16", planned, "2026-11-18");
  assert.equal(firstEmptyDay(days, "2026-11-18"), null);
  assert.equal(firstUpcomingDay(days, "2026-11-18").date, "2026-11-18");
});

test("addWeek moves the whole week, across a month boundary", () => {
  assert.equal(addWeek("2026-11-16", 1), "2026-11-23");
  assert.equal(addWeek("2026-12-28", 1), "2027-01-04");
  assert.equal(addWeek("2026-11-16", -1), "2026-11-09");
});

test("mealTitle trims, and never invents a title", () => {
  assert.equal(mealTitle(meal({ date: "2026-11-16", title: "  Curry  " })), "Curry");
  assert.equal(mealTitle(null), "");
});

test("dinnerRating averages only real ratings", () => {
  assert.equal(dinnerRating(null), null);
  assert.equal(dinnerRating(meal({ date: "2026-11-16", feedback: [] })), null);
  const r = dinnerRating(meal({
    date: "2026-11-16",
    feedback: [
      { id: "f1", meal_plan_id: "m1", rating: 5 },
      { id: "f2", meal_plan_id: "m1", rating: 4 },
      { id: "f3", meal_plan_id: "m1", rating: null },
    ],
  }));
  assert.ok(r);
  assert.equal(r.count, 2);
  assert.equal(r.average, 4.5);
});

test("mealAllergyPayload: a linked recipe's payload is used, absence stays absent", () => {
  const recipes = new Map([["r1", { allergy_warnings: [], checks: [] }]]);
  const linked = meal({ date: "2026-11-16", recipe_id: "r1" });
  assert.deepEqual(mealAllergyPayload(linked, recipes), { allergy_warnings: [], checks: [] });

  // A recipe the page has not loaded: absent, which reportAllergies reads as
  // "the server said nothing" — never as "nothing found".
  assert.equal(mealAllergyPayload(meal({ date: "2026-11-16", recipe_id: "r2" }), recipes), null);
  // A free-text meal: nothing to read either.
  assert.equal(mealAllergyPayload(meal({ date: "2026-11-16", title: "Takeout" }), recipes), null);
  assert.equal(mealAllergyPayload(null, recipes), null);
});
