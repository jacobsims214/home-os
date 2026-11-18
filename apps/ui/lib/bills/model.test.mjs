// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
//
// The bills pages' rules, pinned:
//  1. A PAID bill is never due, and the reading comes from the API's
//     due-soon status, not from paid_date.
//  2. The weekday is the API's; nothing here derives one.
//  3. Money absent is not money zero.
//  4. Mark paid's promise (the next due date) is one period on, clamped.
//  5. The role policy: housesitter/vendor see no money; a viewer reads and
//     cannot write.
//  6. The form's schedule rule (migration 044) and the subject rule.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  addPeriods,
  canSeeMoney,
  canWriteMoney,
  dayMonth,
  dueCount,
  dueLabel,
  dueState,
  dueWhen,
  filterRows,
  formatMoney,
  frequencyChips,
  frequencyLabel,
  fullDate,
  mergeRows,
  monthlyAmount,
  monthlyTotal,
  nextAfterPaying,
  normaliseFrequency,
  periodMonths,
  roleKnown,
  scheduleBody,
  scheduleLabel,
  subjectBody,
  subjectKindOf,
  subjectLabel,
} from "./model.ts";

const bill = (over = {}) => ({ id: "b1", name: "Electric", amount: "164.00", frequency: "monthly", due_day: 22, ...over });
const dueItem = (over = {}) => ({
  source: "bill",
  id: "b1",
  name: "Electric",
  amount: "164.00",
  due_date: "2026-11-22",
  due_weekday: "Sun",
  days_until_due: 4,
  status: "due",
  autopay: false,
  ...over,
});

// ── 1. paid is never due ─────────────────────────────────────
test("a bill the API reports as paid is never due", () => {
  const rows = mergeRows([bill({ paid_date: "2026-11-18", paid_for_due_date: "2026-11-22" })], {
    items: [dueItem({ status: "paid", due_date: "2026-12-22", due_weekday: "Tue", days_until_due: 34 })],
  });
  assert.equal(dueState(rows[0]), "paid");
  assert.equal(dueCount(rows), 0);
});

test("a paid bill is not due even when its stored paid dates look stale", () => {
  // The R1 failure: paid_date is in the past, so a naive UI would say "due".
  // The API's status is the answer, and it says paid.
  const rows = mergeRows([bill({ paid_date: "2026-01-02", paid_for_due_date: "2026-01-22" })], {
    items: [dueItem({ status: "paid" })],
  });
  assert.equal(dueState(rows[0]), "paid");
});

test("an unpaid bill with a due reading is due", () => {
  const rows = mergeRows([bill()], { items: [dueItem({ status: "due" })] });
  assert.equal(dueState(rows[0]), "due");
  assert.equal(dueCount(rows), 1);
});

test("a bill with no API reading is unknown, never due", () => {
  const rows = mergeRows([bill()], { items: [] });
  assert.equal(rows[0].due, null);
  assert.equal(dueState(rows[0]), "unknown");
  assert.equal(dueCount(rows), 0);
});

test("a loan item never becomes a bill's reading", () => {
  const rows = mergeRows([bill()], { items: [dueItem({ source: "loan", id: "b1", status: "paid" })] });
  assert.equal(rows[0].due, null);
  assert.equal(dueState(rows[0]), "unknown");
});

// ── 2. the weekday and date are the API's ────────────────────
test("the due line is the API's weekday plus the local date", () => {
  assert.equal(dueLabel({ due_date: "2026-11-22", due_weekday: "Sun" }), "Sun, Nov 22");
  assert.equal(dueLabel({ due_date: "2026-12-01", due_weekday: "Tue" }), "Tue, Dec 1");
});

test("dayMonth and fullDate read a civil date without a timezone", () => {
  assert.equal(dayMonth("2026-12-15"), "Dec 15");
  assert.equal(fullDate("2026-12-15"), "Dec 15, 2026");
  assert.equal(dayMonth("2026-01-01"), "Jan 1");
  assert.equal(dayMonth(""), "");
  assert.equal(dayMonth("nope"), "");
});

test("the countdown comes from the API's days_until_due", () => {
  assert.equal(dueWhen(0), "Due today");
  assert.equal(dueWhen(1), "in 1 day");
  assert.equal(dueWhen(2), "in 2 days");
  assert.equal(dueWhen(-1), "1 day late");
  assert.equal(dueWhen(-4), "4 days late");
});

test("the stored schedule reads as a person would say it", () => {
  assert.equal(scheduleLabel({ frequency: "monthly", due_day: 22, due_date: null }), "Monthly, due the 22nd");
  assert.equal(scheduleLabel({ frequency: "monthly", due_day: 1, due_date: null }), "Monthly, due the 1st");
  assert.equal(scheduleLabel({ frequency: "monthly", due_day: 3, due_date: null }), "Monthly, due the 3rd");
  assert.equal(scheduleLabel({ frequency: "monthly", due_day: 11, due_date: null }), "Monthly, due the 11th");
  assert.equal(scheduleLabel({ frequency: "annual", due_day: null, due_date: "2026-12-15" }), "Yearly, Dec 15");
  assert.equal(scheduleLabel({ frequency: "monthly", due_day: null, due_date: null }), "Monthly, no due day");
});

test("frequency labels match the mockup", () => {
  assert.equal(frequencyLabel("monthly"), "Monthly");
  assert.equal(frequencyLabel("quarterly"), "Quarterly");
  assert.equal(frequencyLabel("semiannual"), "Every 6 months");
  assert.equal(frequencyLabel("annual"), "Yearly");
  assert.equal(frequencyLabel("once"), "Once");
  assert.equal(frequencyLabel(null), "Monthly"); // the API's default
  assert.equal(normaliseFrequency(undefined), "monthly");
  assert.equal(normaliseFrequency("weird"), "monthly");
});

// ── 3. money absent is not zero ──────────────────────────────
test("a missing amount is null, never $0.00", () => {
  assert.equal(formatMoney(null), null);
  assert.equal(formatMoney(undefined), null);
  assert.equal(formatMoney(""), null);
  assert.equal(formatMoney("not a number"), null);
  assert.equal(formatMoney("0"), "$0.00");
  assert.equal(formatMoney("1800"), "$1,800.00");
  assert.equal(formatMoney(164), "$164.00");
});

test("the monthly spread follows the budget rule", () => {
  assert.equal(monthlyAmount({ amount: "164.00", frequency: "monthly" }), 164);
  assert.equal(monthlyAmount({ amount: "186.00", frequency: "quarterly" }), 62);
  assert.equal(monthlyAmount({ amount: "642.00", frequency: "semiannual" }), 107);
  assert.equal(monthlyAmount({ amount: "1800.00", frequency: "annual" }), 150);
  assert.equal(monthlyAmount({ amount: "50.00", frequency: "once" }), 0);
  // No amount contributes nothing — and is not a zero bill.
  assert.equal(monthlyAmount({ amount: null, frequency: "monthly" }), 0);
});

test("the list total adds the spread, not the face values", () => {
  const total = monthlyTotal([
    { amount: "164.00", frequency: "monthly" },
    { amount: "1800.00", frequency: "annual" },
    { amount: null, frequency: "monthly" },
  ]);
  assert.equal(total, 314);
});

// ── 4. mark paid's promise ───────────────────────────────────
test("the next due date after paying is one period on", () => {
  assert.equal(nextAfterPaying({ due_date: "2026-11-22" }, "monthly"), "2026-12-22");
  assert.equal(nextAfterPaying({ due_date: "2026-11-10" }, "quarterly"), "2027-02-10");
  assert.equal(nextAfterPaying({ due_date: "2026-12-15" }, "annual"), "2027-12-15");
  assert.equal(nextAfterPaying({ due_date: "2026-11-28" }, "semiannual"), "2027-05-28");
  // A once bill has no next date.
  assert.equal(nextAfterPaying({ due_date: "2026-12-15" }, "once"), null);
  // No reading, no promise.
  assert.equal(nextAfterPaying(null, "monthly"), null);
});

test("the next date clamps to a short month, like the Go rule", () => {
  assert.equal(addPeriods("2026-01-31", 1), "2026-02-28");
  assert.equal(addPeriods("2026-03-31", 1), "2026-04-30");
  assert.equal(addPeriods("2026-12-15", 1), "2027-01-15");
  assert.equal(addPeriods("2026-11-30", 3), "2027-02-28");
  assert.equal(addPeriods("2026-12-15", 0), null);
});

test("period months follow the frequency", () => {
  assert.equal(periodMonths("monthly"), 1);
  assert.equal(periodMonths("quarterly"), 3);
  assert.equal(periodMonths("semiannual"), 6);
  assert.equal(periodMonths("annual"), 12);
  assert.equal(periodMonths("once"), 0);
  assert.equal(periodMonths(null), 1);
});

// ── 5. the role policy ───────────────────────────────────────
test("housesitter and vendor see no money at all", () => {
  for (const role of ["housesitter", "vendor"]) {
    assert.equal(canSeeMoney(role), false, role);
    assert.equal(canWriteMoney(role), false, role);
  }
});

test("owner, family_manager and family_member read and write money", () => {
  for (const role of ["owner", "family_manager", "family_member"]) {
    assert.equal(canSeeMoney(role), true, role);
    assert.equal(canWriteMoney(role), true, role);
  }
});

test("a viewer reads money and cannot write it", () => {
  assert.equal(canSeeMoney("viewer"), true);
  assert.equal(canWriteMoney("viewer"), false);
});

test("an unknown role is not money: the page waits, it does not guess", () => {
  // No role yet (the list is in flight) is "unknown": not money, and the page
  // must wait rather than flash an owner's controls.
  for (const role of [null, undefined, ""]) {
    assert.equal(canSeeMoney(role), false);
    assert.equal(canWriteMoney(role), false);
    assert.equal(roleKnown(role), false);
  }
  // A role string this build does not know is KNOWN (we asked and got an
  // answer) but not money: fails closed, like the API's unknown kinds.
  assert.equal(roleKnown("something_else"), true);
  assert.equal(canSeeMoney("something_else"), false);
  assert.equal(canWriteMoney("something_else"), false);
  assert.equal(roleKnown("viewer"), true);
});

// ── 6. the form's rules ─────────────────────────────────────
test("a monthly bill sends due_day and no due_date", () => {
  assert.deepEqual(scheduleBody("monthly", "22", "2026-12-15"), { frequency: "monthly", due_day: 22 });
  assert.deepEqual(scheduleBody("monthly", "", "2026-12-15"), { frequency: "monthly" });
});

test("a non-monthly bill sends due_date and no due_day", () => {
  assert.deepEqual(scheduleBody("annual", "22", "2026-12-15"), { frequency: "annual", due_date: "2026-12-15" });
  assert.deepEqual(scheduleBody("annual", "22", ""), { frequency: "annual" });
});

test("switching annual to monthly drops the date; monthly to annual drops the day", () => {
  assert.deepEqual(scheduleBody("monthly", "5", "2026-12-15"), { frequency: "monthly", due_day: 5 });
  assert.deepEqual(scheduleBody("annual", "5", "2026-12-15"), { frequency: "annual", due_date: "2026-12-15" });
});

test("a bill is for one subject, and picking a vehicle clears the home", () => {
  assert.deepEqual(subjectBody("vehicle", "v1"), { vehicle_id: "v1" });
  assert.deepEqual(subjectBody("property", "p1"), { property_id: "p1" });
  assert.deepEqual(subjectBody("asset", "a1"), { asset_id: "a1" });
  // Nothing clears every column (the API reads "" as "no subject").
  assert.deepEqual(subjectBody("none", "p1"), { property_id: "", asset_id: "", vehicle_id: "" });
});

test("a stored bill's subject kind is the typed FK", () => {
  assert.equal(subjectKindOf({ vehicle_id: "v1", property_id: null, asset_id: null }), "vehicle");
  assert.equal(subjectKindOf({ property_id: "p1", asset_id: "a1", vehicle_id: null }), "asset");
  assert.equal(subjectKindOf({ property_id: "p1", asset_id: null, vehicle_id: null }), "property");
  assert.equal(subjectKindOf({ property_id: null, asset_id: null, vehicle_id: null }), "none");
});

test("a bill's For line names the thing, and an asset beats its property", () => {
  const names = { properties: { p1: "Maple Street" }, assets: { a1: "Water heater" }, vehicles: { v1: "F-150" } };
  assert.deepEqual(subjectLabel({ property_id: "p1" }, names), { kind: "property", name: "Maple Street" });
  assert.deepEqual(subjectLabel({ property_id: "p1", asset_id: "a1" }, names), { kind: "asset", name: "Water heater" });
  assert.deepEqual(subjectLabel({ vehicle_id: "v1" }, names), { kind: "vehicle", name: "F-150" });
  assert.equal(subjectLabel({}, names), null);
});

// ── list: order, chips, filter ──────────────────────────────
test("rows sort by next due date, then name, and unreported last", () => {
  const rows = mergeRows(
    [bill({ id: "c", name: "Water" }), bill({ id: "a", name: "Electric" }), bill({ id: "b", name: "Cabin tax" })],
    {
      items: [
        dueItem({ id: "a", days_until_due: 4 }),
        dueItem({ id: "c", days_until_due: 4 }),
        dueItem({ id: "b", days_until_due: 30 }),
      ],
    },
  );
  assert.deepEqual(rows.map((r) => r.bill.id), ["a", "c", "b"]);
});

test("a bill with no reading sorts after every dated one", () => {
  const rows = mergeRows([bill({ id: "x", name: "Aaa" }), bill({ id: "y", name: "Zzz" })], {
    items: [dueItem({ id: "y", days_until_due: 90 })],
  });
  assert.deepEqual(rows.map((r) => r.bill.id), ["y", "x"]);
});

test("the frequency chips count what is behind them and skip empty ones", () => {
  const chips = frequencyChips([
    { id: "1", name: "A", frequency: "monthly" },
    { id: "2", name: "B", frequency: "monthly" },
    { id: "3", name: "C", frequency: "annual" },
  ]);
  assert.deepEqual(chips.map((c) => [c.value, c.count]), [
    ["all", 3],
    ["monthly", 2],
    ["annual", 1],
  ]);
});

test("a frequency filter keeps only its own bills", () => {
  const rows = mergeRows(
    [
      { id: "1", name: "A", frequency: "monthly" },
      { id: "2", name: "B", frequency: "annual" },
    ],
    null,
  );
  assert.deepEqual(filterRows(rows, "annual").map((r) => r.bill.id), ["2"]);
  assert.equal(filterRows(rows, "all").length, 2);
  assert.equal(filterRows(rows, "quarterly").length, 0);
});
