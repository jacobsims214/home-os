// Run: npm test (node --test strips the TypeScript types natively).
//
// The ONE past/today/future rule (#5305). These tests exist because the Meals
// page answered "is this day still open?" with the week's Monday instead of the
// family's today, and nagged a household to plan a dinner for a day two days
// gone (War Game R2, Amber G8). A regression here is that bug, on any surface.
import { test } from "node:test";
import assert from "node:assert/strict";
import { dayPosition, isTodayOrFutureDay } from "./days.ts";
import { householdToday } from "./agenda.ts";

// ── the three positions ─────────────────────────────────────

test("dayPosition: past, today and future, exactly on the boundary", () => {
  assert.equal(dayPosition("2026-11-16", "2026-11-18"), "past");
  assert.equal(dayPosition("2026-11-17", "2026-11-18"), "past"); // yesterday
  assert.equal(dayPosition("2026-11-18", "2026-11-18"), "today"); // the boundary
  assert.equal(dayPosition("2026-11-19", "2026-11-18"), "future"); // tomorrow
  assert.equal(dayPosition("2026-11-22", "2026-11-18"), "future");
});

test("dayPosition works across months, years and DST changeovers", () => {
  assert.equal(dayPosition("2026-10-31", "2026-11-01"), "past");
  assert.equal(dayPosition("2026-12-31", "2027-01-01"), "past");
  assert.equal(dayPosition("2027-01-01", "2026-12-31"), "future");
  // 2026-03-08 is the US spring-forward day: 23 hours long, still one day.
  assert.equal(dayPosition("2026-03-08", "2026-03-09"), "past");
  assert.equal(dayPosition("2026-11-01", "2026-11-02"), "past"); // 25-hour day
});

test("isTodayOrFutureDay is true on today and after, false before", () => {
  assert.equal(isTodayOrFutureDay("2026-11-16", "2026-11-18"), false);
  assert.equal(isTodayOrFutureDay("2026-11-17", "2026-11-18"), false);
  assert.equal(isTodayOrFutureDay("2026-11-18", "2026-11-18"), true);
  assert.equal(isTodayOrFutureDay("2026-11-19", "2026-11-18"), true);
});

// ── never a guess on a bad date ─────────────────────────────

test("an unreadable date is never past and never nagged about", () => {
  assert.equal(dayPosition("", "2026-11-18"), null);
  assert.equal(dayPosition(null, "2026-11-18"), null);
  assert.equal(dayPosition("18/11/2026", "2026-11-18"), null);
  assert.equal(dayPosition("2026-02-31", "2026-11-18"), null); // not a real day
  assert.equal(isTodayOrFutureDay("", "2026-11-18"), false);
  assert.equal(isTodayOrFutureDay("2026-11-18", null), false);
  // A date with a time on it is not a civil date: refuse rather than guess a day.
  assert.equal(dayPosition("2026-11-18T23:30:00Z", "2026-11-18"), null);
});

// ── the bug: local time and UTC disagree ────────────────────

test("late evening in a negative-offset zone: the household's day is what counts", () => {
  // 2026-11-19T03:30Z — 22:30 on Wednesday Nov 18 in New York (UTC-5), but
  // already Thursday Nov 19 in UTC. This is the class of bug #5305 sits in: a
  // date taken from UTC (or from `toISOString()`) is a day ahead all evening in
  // the Americas, so TONIGHT's dinner would read as "past" and the page would
  // skip the very day a family still has to cook for.
  const at = new Date("2026-11-19T03:30:00Z");
  const familyToday = householdToday("America/New_York", at);
  const utcToday = householdToday("UTC", at);
  assert.equal(familyToday, "2026-11-18");
  assert.equal(utcToday, "2026-11-19");
  assert.notEqual(familyToday, utcToday, "the two must actually disagree for this test to mean anything");

  // Wednesday the 18th is still open with the family's day — and would be
  // called "gone" by the UTC date, which is exactly the old behaviour.
  assert.equal(isTodayOrFutureDay("2026-11-18", familyToday), true);
  assert.equal(dayPosition("2026-11-18", familyToday), "today");
  assert.equal(isTodayOrFutureDay("2026-11-18", utcToday), false);
  assert.equal(dayPosition("2026-11-18", utcToday), "past");

  // Monday the 16th is gone either way: no timezone makes it plannable.
  assert.equal(isTodayOrFutureDay("2026-11-16", familyToday), false);
});

test("early morning in a positive-offset zone: a UTC date would nag about yesterday", () => {
  // 2026-11-18T22:30Z is already 08:30 Thursday Nov 19 in Auckland (UTC+13).
  const at = new Date("2026-11-18T22:30:00Z");
  const familyToday = householdToday("Pacific/Auckland", at);
  assert.equal(familyToday, "2026-11-19");
  // Nov 18 is yesterday for that family, so it is not an invitation — a UTC
  // date would still be showing it as "today".
  assert.equal(isTodayOrFutureDay("2026-11-18", familyToday), false);
  assert.equal(isTodayOrFutureDay("2026-11-19", familyToday), true);
});
