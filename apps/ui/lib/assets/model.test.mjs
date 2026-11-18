// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
//
// The assets pages' rules, pinned:
//  1. Money absent is not money zero (#4912): a redacted current_value must
//     render as absent, never as "$0".
//  2. A date-only string never slips a month: monthYear formats in UTC.
//  3. The card's second line is make + model, or the category, or nothing.
//  4. The detail summary omits every fact the API did not send, so a role
//     without money read gets no strip at all.
import { test } from "node:test";
import assert from "node:assert/strict";
import { factsFor, makeAndModel, monthYear, summaryFacts, usd } from "./model.ts";

const asset = (over = {}) => ({
  id: "a1",
  household_id: "h1",
  name: "Furnace",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  ...over,
});

test("usd: formats a money string, and null is null (never $0)", () => {
  assert.equal(usd("4200.00"), "$4,200");
  assert.equal(usd("4200.50", 2), "$4,200.50");
  assert.equal(usd(null), null);
  assert.equal(usd(undefined), null);
  assert.equal(usd(""), null);
  assert.equal(usd("not a number"), null);
  // A real zero is still a number the API sent, so it shows.
  assert.equal(usd("0"), "$0");
});

test("monthYear: an ISO date keeps its own month in every zone", () => {
  // Parsed at UTC midnight and formatted in UTC: 2029-10-01 stays October even
  // where the local zone is behind UTC.
  assert.equal(monthYear("2029-10-01"), "Oct 2029");
  assert.equal(monthYear("2029-10-01T00:00:00Z"), "Oct 2029");
  assert.equal(monthYear("2027-01-31T23:00:00Z"), "Jan 2027");
  assert.equal(monthYear("2027-01-01"), "Jan 2027");
  // Not a date: handed back as-is rather than shown as "Invalid Date".
  assert.equal(monthYear("soon"), "soon");
});

test("makeAndModel: make and model, else the category, else nothing", () => {
  assert.equal(makeAndModel({ manufacturer: "Carrier", model: "Infinity 59SC5" }), "Carrier \u00b7 Infinity 59SC5");
  assert.equal(makeAndModel({ manufacturer: "Carrier" }), "Carrier");
  assert.equal(makeAndModel({ model: "", category: "HVAC" }), "HVAC");
  assert.equal(makeAndModel({}), null);
  assert.equal(makeAndModel({ manufacturer: "", model: "", category: null }), null);
});

test("factsFor: a card shows only what the API sent", () => {
  const full = factsFor(asset({ current_value: "4200.00", warranty_expiry: "2029-10-01" }), "Maple Street");
  assert.deepEqual(full, [
    { label: "Place", value: "Maple Street" },
    { label: "Value", value: "$4,200" },
    { label: "Warranty", value: "Until Oct 2029" },
  ]);

  // A family-owned thing (no property) and no money: no facts at all, so the
  // card is just its name line rather than a strip of dashes.
  assert.deepEqual(factsFor(asset(), null), []);

  // A housesitter: the API sent no current_value, so there is no Value fact.
  // Crucially there is no { label: "Value", value: "$0" }.
  const redacted = factsFor(asset({ current_value: null, purchase_price: null, purchase_date: null }), "Maple Street");
  assert.deepEqual(redacted, [{ label: "Place", value: "Maple Street" }]);
});

test("summaryFacts: order and omission, so a redacted asset has no strip", () => {
  const stats = summaryFacts(
    asset({
      current_value: "4200.00",
      purchase_price: "5100.00",
      purchase_date: "2019-10-02",
      warranty_expiry: "2029-10-01",
      category: "HVAC",
    }),
  );
  assert.deepEqual(
    stats.map((s) => s.label),
    ["Value", "Paid", "Warranty", "Bought", "Category"],
  );
  assert.equal(stats.find((s) => s.label === "Value").value, "$4,200");

  // #4912: a role without money read gets purchase_* omitted and current_value
  // null. No money fact survives, so the detail page renders no summary strip.
  assert.deepEqual(summaryFacts(asset({ current_value: null, category: "HVAC" })), [
    { label: "Category", value: "HVAC" },
  ]);
  assert.deepEqual(summaryFacts(asset()), []);
});
