/**
 * Property money math (#4553). Regression for the mobile audit #4509 bug:
 * the properties list summed a monthly total of ~$86,606 where the real
 * figure was ~$1,412, because the mortgage rate (a percent) was divided by
 * 12 instead of 100*12.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  formatUsd,
  hasFinancialData,
  monthlyCarryingCost,
  monthlyMortgagePayment,
  toNumber,
  totalMonthlyCarryingCost,
} from "./property-finance.ts";

const close = (actual, expected, tolerance = 0.01) =>
  assert.ok(
    Math.abs(actual - expected) <= tolerance,
    `expected ${actual} to be within ${tolerance} of ${expected}`,
  );

test("mortgage_rate is a percent: 4.25% on 200k/360 is ~$983.88, not ~$98,388", () => {
  const payment = monthlyMortgagePayment(200000, 4.25, 360);
  close(payment, 983.88);
  // The old bug divided by 12 only, which is ~100x too big.
  assert.ok(payment < 2000, `payment ${payment} should be under $2,000/mo`);
});

test("a zero rate is straight-line principal over the term", () => {
  close(monthlyMortgagePayment(120000, 0, 240), 500);
});

test("missing or unusable mortgage inputs yield no payment", () => {
  assert.equal(monthlyMortgagePayment(null, 4.25, 360), null);
  assert.equal(monthlyMortgagePayment(200000, 4.25, null), null);
  assert.equal(monthlyMortgagePayment(0, 4.25, 360), null);
  assert.equal(monthlyMortgagePayment(200000, 4.25, 0), null);
});

test("carrying cost adds tax/12, insurance/12 and HOA to the payment", () => {
  const cost = monthlyCarryingCost({
    mortgage_amount: "200000.00",
    mortgage_rate: "4.250",
    mortgage_term_months: "360",
    property_tax_annual: "1800.00",
    insurance_annual: "1200.00",
    hoa_fee_monthly: "50",
  });
  // 983.88 + 150 + 100 + 50
  close(cost, 1283.88);
});

test("carrying cost is null when nothing can be computed (not $0)", () => {
  assert.equal(monthlyCarryingCost({}), null);
  assert.equal(monthlyCarryingCost({ mortgage_amount: "200000" }), null);
});

test("the audit scenario: two homes total ~$1,412/mo, never ~$86,606", () => {
  const properties = [
    {
      mortgage_amount: "200000.00",
      mortgage_rate: "4.250",
      mortgage_term_months: "360",
      property_tax_annual: "1800.00",
      insurance_annual: "1200.00",
      hoa_fee_monthly: "50",
    },
    // A paid-off cabin: tax and insurance only.
    { property_tax_annual: "1200.00", insurance_annual: "336.00" },
  ];
  const total = totalMonthlyCarryingCost(properties);
  close(total, 1283.88 + 100 + 28);
  assert.ok(total < 2000, `total ${total} must be a plausible monthly figure`);
  assert.equal(formatUsd(total), "$1,412");
});

test("properties with no cost contribute nothing to the total", () => {
  const total = totalMonthlyCarryingCost([
    { hoa_fee_monthly: "100" },
    {},
    { current_value: "500000" },
  ]);
  close(total, 100);
});

test("toNumber rejects blanks and non-numbers", () => {
  assert.equal(toNumber(""), null);
  assert.equal(toNumber(null), null);
  assert.equal(toNumber(undefined), null);
  assert.equal(toNumber("abc"), null);
  assert.equal(toNumber("4.250"), 4.25);
});

test("hasFinancialData is false for a bare property", () => {
  assert.equal(hasFinancialData({}), false);
  assert.equal(hasFinancialData({ current_value: "500000" }), true);
});

test("formatUsd renders whole dollars and passes null through", () => {
  assert.equal(formatUsd(1412), "$1,412");
  assert.equal(formatUsd(null), null);
  assert.equal(formatUsd(Number.NaN), null);
});
