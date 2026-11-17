// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
//
// The browser's copy of the bill due rule must answer exactly what the Go
// rule answers. These two tables are COPIED from
// apps/mcp/internal/bill/due_test.go (TestBillDueFrequencies,
// TestPaidForDueDateFrequencies), which in turn mirror
// packages/billdue/due_test.go — the same tables the worker's copy keeps. A
// change to a rule in any copy that is not made in the others fails here.
import { test } from "node:test";
import assert from "node:assert/strict";
import { billDueFor, cadence, formatCivil, paidForDueDate, parseCivil } from "./due.ts";

const schedule = (frequency, dueDay, dueDate) => ({
  frequency,
  due_day: dueDay || null,
  due_date: dueDate || null,
});

// ── TestBillDueFrequencies ────────────────────────────────────
const DUE = [
  ["monthly through the same rule", "2026-11-15", "monthly", 20, "", "", "", "2026-11-20", "due", ""],
  ["quarterly: next quarter", "2026-11-15", "quarterly", 0, "2026-01-15", "", "", "2027-01-15", "due", ""],
  ["quarterly: due today", "2026-10-15", "quarterly", 0, "2026-01-15", "", "", "2026-10-15", "due", ""],
  ["quarterly: day after wraps a quarter", "2026-10-16", "quarterly", 0, "2026-01-15", "", "", "2027-01-15", "due", ""],
  ["quarterly: before the anchor is the anchor", "2025-12-01", "quarterly", 0, "2026-01-15", "", "", "2026-01-15", "due", ""],
  ["quarterly: Jan 31 clamps to Apr 30", "2026-04-02", "quarterly", 0, "2026-01-31", "", "", "2026-04-30", "due", ""],
  ["quarterly: after Apr 30 back to Jul 31", "2026-05-01", "quarterly", 0, "2026-01-31", "", "", "2026-07-31", "due", ""],
  ["semiannual: every 6 months", "2026-11-15", "semiannual", 0, "2026-03-10", "", "", "2027-03-10", "due", ""],
  ["annual: this year's date passed", "2026-11-15", "annual", 0, "2026-04-01", "", "", "2027-04-01", "due", ""],
  ["annual: leap day clamps to Feb 28", "2026-01-10", "annual", 0, "2024-02-29", "", "", "2026-02-28", "due", ""],
  ["annual: leap day is Feb 29 in a leap year", "2028-01-10", "annual", 0, "2024-02-29", "", "", "2028-02-29", "due", ""],
  ["once: upcoming", "2026-11-15", "once", 0, "2026-12-05", "", "", "2026-12-05", "due", ""],
  ["once: past and unpaid stays due", "2026-11-15", "once", 0, "2026-11-01", "", "", "2026-11-01", "due", ""],

  ["quarterly: paid early moves a quarter on", "2027-01-12", "quarterly", 0, "2026-01-15", "2027-01-10", "", "2027-04-15", "paid", "2027-01-15"],
  ["quarterly: paid last quarter only: still due", "2026-11-15", "quarterly", 0, "2026-01-15", "2026-10-14", "", "2027-01-15", "due", "2026-10-15"],
  ["quarterly: in grace covers the past due date", "2026-10-20", "quarterly", 0, "2026-01-15", "2026-10-20", "", "2027-01-15", "due", "2026-10-15"],
  ["annual: paid this year moves to next year", "2026-03-25", "annual", 0, "2026-04-01", "2026-03-20", "", "2027-04-01", "paid", "2026-04-01"],
  ["annual: stored paid_for wins", "2026-03-25", "annual", 0, "2026-04-01", "2026-03-20", "2025-04-01", "2026-04-01", "due", "2025-04-01"],
  ["once: paid has no next due date", "2026-11-15", "once", 0, "2026-12-05", "2026-11-10", "", "", "paid", "2026-12-05"],
  ["once: paid late still pays it", "2026-11-15", "once", 0, "2026-11-01", "2026-11-14", "", "", "paid", "2026-11-01"],

  ["no schedule: annual without due_date", "2026-11-15", "annual", 0, "", "", "", "", "", ""],
  ["no schedule: monthly without due_day", "2026-11-15", "monthly", 0, "", "", "", "", "", ""],
  ["no schedule: unknown frequency", "2026-11-15", "weekly", 0, "2026-11-20", "", "", "", "", ""],
];

for (const [name, today, frequency, dueDay, dueDate, paid, paidFor, wantNext, wantStatus, wantPaid] of DUE) {
  test(`due rule: ${name}`, () => {
    const got = billDueFor(today, {
      ...schedule(frequency, dueDay, dueDate),
      paid_date: paid || null,
      paid_for_due_date: paidFor || null,
    });
    if (!wantStatus) {
      assert.equal(got, null, `expected no schedule, got ${JSON.stringify(got)}`);
      return;
    }
    assert.ok(got, "expected a schedule");
    assert.equal(got.next ?? "", wantNext, "next due date");
    assert.equal(got.status, wantStatus, "status");
    assert.equal(got.paidFor ?? "", wantPaid, "covered due date");
    if (got.next) {
      const todayN = parseCivil(today);
      assert.equal(got.daysUntil, parseCivil(got.next) - todayN, "days until");
    }
  });
}

// ── TestPaidForDueDateFrequencies ─────────────────────────────
const PAYS = [
  ["2026-10-14", "quarterly", "2026-01-15", "2026-10-15"], // early
  ["2026-10-22", "quarterly", "2026-01-15", "2026-10-15"], // 7 days late: still October's
  ["2026-10-23", "quarterly", "2026-01-15", "2027-01-15"], // 8 days late: next quarter
  ["2025-12-01", "quarterly", "2026-01-15", "2026-01-15"], // before the anchor: the first due date
  ["2026-05-03", "quarterly", "2026-01-31", "2026-04-30"], // 3 days after the clamped Apr 30
  ["2026-02-27", "annual", "2024-02-29", "2026-02-28"], // leap-day anchor clamps
  ["2027-01-02", "semiannual", "2026-07-01", "2027-01-01"], // 1 day late
  ["2026-11-30", "once", "2026-11-01", "2026-11-01"], // a once bill: any payment pays it
  ["2026-10-01", "once", "2026-11-01", "2026-11-01"],
];

for (const [paid, frequency, dueDate, want] of PAYS) {
  test(`payment covers: ${frequency} from ${dueDate}, paid ${paid}`, () => {
    const c = cadence(schedule(frequency, 0, dueDate));
    assert.ok(c, "cadence");
    assert.equal(formatCivil(paidForDueDate(parseCivil(paid), c)), want);
  });
}

// ── the Today screen's own cases ──────────────────────────────

test("a bill paid this morning is not due: status paid, next date moves on", () => {
  // The R1 failure this guards (discoveries/war-game-r1-*): "Electric $180
  // due, still unpaid" about a bill paid that morning. The bill row says so;
  // the Bills-calendar mirror event does not.
  const got = billDueFor("2026-11-17", {
    frequency: "monthly",
    due_day: 17,
    paid_date: "2026-11-17",
    paid_for_due_date: "2026-11-17",
  });
  assert.equal(got.status, "paid");
  assert.equal(got.next, "2026-12-17");
  assert.equal(got.paidFor, "2026-11-17");
});

test("overdue monthly bill is today's or yesterday's date, negative days only for once", () => {
  // A monthly bill due on the 5th, today the 17th: the next unpaid date is
  // NEXT month's 5th — the monthly rule has no overdue state.
  const got = billDueFor("2026-11-17", { frequency: "monthly", due_day: 5 });
  assert.equal(got.next, "2026-12-05");
  assert.equal(got.daysUntil, 18);
  // A past once bill is honestly overdue.
  const once = billDueFor("2026-11-17", { frequency: "once", due_date: "2026-11-10" });
  assert.equal(once.next, "2026-11-10");
  assert.equal(once.daysUntil, -7);
  assert.equal(once.status, "due");
});

test("parseCivil rejects impossible dates instead of normalising them", () => {
  assert.equal(parseCivil("2026-02-31"), null);
  assert.equal(parseCivil("2026-13-01"), null);
  assert.equal(parseCivil("not a date"), null);
  assert.equal(parseCivil(""), null);
  assert.equal(formatCivil(parseCivil("2026-02-28")), "2026-02-28");
});
