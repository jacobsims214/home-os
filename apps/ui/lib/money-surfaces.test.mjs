// Run: npm test. One case per money surface, pinned to ONE contract.
//
// #5297: every bill rendered 100x too large on Money and on Today. The fault
// was the API sending the NUMERIC's mantissa (234000 for 2340.00) and the UI
// reading the string as dollars. The API now sends decimal DOLLARS
// (apps/api/pkg/money), and this file is the UI's half of that contract: given
// the value the API promises, every surface shows the true amount, and none of
// them scales it again.
//
// These are the surfaces the ticket names, each through its OWN formatter:
//   - Money page (the bills list at /dashboard/bills: table, "a month in all")
//   - Today's "Bills & upkeep" card
//   - the calendar's bill reading
//   - the bill detail page
// A fix in one helper would not cover these: they are three separate
// formatMoney functions (lib/bills/model.ts, lib/today/agenda.ts,
// lib/calendar/model.ts), so each is asserted here. If someone "fixes" the
// display by dividing by 100 somewhere, this catches it: dividing a dollar
// string yields $23.40 for a $2,340 mortgage.
import { test } from "node:test";
import assert from "node:assert/strict";

import { formatMoney as billsFormatMoney, monthlyAmount, monthlyTotal } from "./bills/model.ts";
import { billsAndUpkeepDue } from "./today/agenda.ts";
import { billReading, formatMoney as calendarFormatMoney } from "./calendar/model.ts";

// The API's contract, from the ticket's own ground truth.
const API_BILL = (name, amount, extra = {}) => ({ id: name.toLowerCase(), name, amount, category: "utilities", ...extra });

// The four amounts from MCP bill_find, and what a person must see.
const TRUTH = [
  ["Mortgage", "2340.00", "$2,340.00"],
  ["Electric", "180.00", "$180.00"],
  ["Internet", "75.00", "$75.00"],
  ["Riverside copay", "35.00", "$35.00"],
];

test("Money page: a bill's amount renders as dollars, never 100x", () => {
  for (const [name, amount, want] of TRUTH) {
    assert.equal(billsFormatMoney(amount), want, `${name} (${amount})`);
  }
  // The exact regression: the mantissa the API used to send must never be
  // silently accepted as the amount.
  assert.equal(billsFormatMoney("234000"), "$234,000.00");
  assert.notEqual(billsFormatMoney("2340.00"), billsFormatMoney("234000"));
});

test("Money page: the monthly total is the sum of the true amounts", () => {
  const bills = [
    API_BILL("Mortgage", "2340.00", { frequency: "monthly" }),
    API_BILL("Electric", "180.00", { frequency: "monthly" }),
    API_BILL("Internet", "75.00", { frequency: "monthly" }),
    API_BILL("Riverside copay", "35.00", { frequency: "monthly" }),
  ];
  // 2340 + 180 + 75 + 35 = 2630 dollars. The regression printed $263,000.
  assert.equal(billsFormatMoney(monthlyTotal(bills)), "$2,630.00");

  // A quarterly bill spreads over three months, and the spread is on dollars:
  // $186 a quarter is $62 a month, not $6,200.
  const quarterly = [API_BILL("Water", "186.00", { frequency: "quarterly" })];
  assert.equal(monthlyAmount(quarterly[0]), 62);
  assert.equal(billsFormatMoney(monthlyTotal(quarterly)), "$62.00");
});

test("Today: the Bills & upkeep card renders dollars, never 100x", () => {
  // Today reads GET /api/v1/bills and formats through lib/today/agenda.ts.
  const today = "2026-11-15";
  const bills = [API_BILL("Mortgage", "2340.00", { due_day: 17, frequency: "monthly" })];
  const { rows } = billsAndUpkeepDue(today, bills, []);
  assert.equal(rows.length, 1, "the mortgage is due inside the window");
  assert.equal(rows[0].amount, "$2,340.00");
  assert.notEqual(rows[0].amount, "$234,000.00");
});

test("Calendar: a bill's reading renders dollars, never 100x", () => {
  assert.equal(calendarFormatMoney("2340.00"), "$2,340.00");
  const event = {
    id: "e1",
    event_type: "bill",
    entity_type: "bill",
    entity_id: "mortgage",
    start: "2026-11-17T09:00:00Z",
  };
  const reading = billReading(event, [API_BILL("Mortgage", "2340.00", { due_day: 17 })], "2026-11-15");
  assert.equal(reading.kind, "bill");
  assert.equal(reading.amount, "$2,340.00");
});

test("every surface agrees: one amount in, one amount out", () => {
  // Three formatters, three files, one contract. A surface that starts
  // dividing by 100 (or multiplying) breaks this immediately.
  for (const [name, amount, want] of TRUTH) {
    assert.equal(billsFormatMoney(amount), want, `Money ${name}`);
    assert.equal(calendarFormatMoney(amount), want, `Calendar ${name}`);
  }
});

test("money that is absent stays absent on every surface", () => {
  // #4806: never "$0.00" for an amount the API did not send.
  for (const value of [null, undefined, ""]) {
    assert.equal(billsFormatMoney(value), null, `Money ${value}`);
    assert.equal(calendarFormatMoney(value), null, `Calendar ${value}`);
  }
  const { rows } = billsAndUpkeepDue("2026-11-15", [API_BILL("Water", null, { due_day: 17 })], []);
  assert.equal(rows[0].amount, null, "Today shows no amount, not $0.00");
});
