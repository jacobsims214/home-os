// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  LEGACY_PROPERTY_FINANCE_FIELDS,
  LEGACY_VEHICLE_FINANCE_FIELDS,
  PROPERTY_EDIT_FIELDS,
  isLinkedTo,
  monthlyBillAmount,
  newBillBody,
  newLoanBody,
  pickMortgage,
  propertyEditBody,
} from "./linked-finance-utils.ts";

const cabin = { kind: "property", id: "p1", name: "Lake Cabin" };
const truck = { kind: "vehicle", id: "v1", name: "2021 Ford F-150" };

test("the property edit body never carries a legacy finance field", () => {
  for (const f of LEGACY_PROPERTY_FINANCE_FIELDS) assert.ok(!PROPERTY_EDIT_FIELDS.includes(f), f);
  const shown = {
    name: "Lake Cabin",
    address: null,
    purchase_price: "250000.00",
    mortgage_amount: "200000.00",
    property_tax_annual: "1800.00",
    insurance_provider: "State Farm",
    hoa_fee_monthly: "50.00",
  };
  const body = propertyEditBody(shown);
  assert.deepEqual(Object.keys(body).sort(), [...PROPERTY_EDIT_FIELDS].sort());
  for (const f of LEGACY_PROPERTY_FINANCE_FIELDS) assert.ok(!(f in body), f);
  assert.equal(body.name, "Lake Cabin");
  assert.equal(body.address, "");
  assert.equal(body.purchase_price, "250000.00");
});

test("the vehicle legacy list covers what the API derives", () => {
  assert.deepEqual([...LEGACY_VEHICLE_FINANCE_FIELDS].sort(), [
    "insurance_cost", "insurance_provider", "lender", "loan_amount", "loan_term_months",
    "monthly_payment", "registration_cost", "registration_renewal_month",
  ]);
});

test("linked rows: same subject, not an asset's", () => {
  assert.equal(isLinkedTo({ property_id: "p1" }, cabin), true);
  assert.equal(isLinkedTo({ property_id: "p1", asset_id: "a1" }, cabin), false);
  assert.equal(isLinkedTo({ property_id: "p2" }, cabin), false);
  assert.equal(isLinkedTo({ vehicle_id: "v1" }, truck), true);
  assert.equal(isLinkedTo({ property_id: "v1" }, truck), false);
});

test("monthly bill amount follows the frequency", () => {
  assert.equal(monthlyBillAmount({ amount: "1800.00", frequency: "annual" }), 150);
  assert.equal(monthlyBillAmount({ amount: "210", frequency: "monthly" }), 210);
  assert.equal(monthlyBillAmount({ amount: "300", frequency: "quarterly" }), 100);
  assert.equal(monthlyBillAmount({ amount: "600", frequency: "semiannual" }), 100);
  assert.equal(monthlyBillAmount({ amount: "99", frequency: "once" }), 0);
  assert.equal(monthlyBillAmount({ amount: "40", frequency: null }), 40);
  assert.equal(monthlyBillAmount({ amount: null, frequency: "annual" }), 0);
});

test("the mortgage is the loan named like one, else the first", () => {
  const heloc = { id: "l1", name: "Cabin HELOC" };
  const mortgage = { id: "l2", name: "Lake Cabin Mortgage" };
  assert.equal(pickMortgage([heloc, mortgage]), mortgage);
  assert.equal(pickMortgage([heloc]), heloc);
  assert.equal(pickMortgage([]), undefined);
});

test("a new loan carries the subject's FK; balance defaults to the amount", () => {
  assert.deepEqual(newLoanBody(cabin, { name: " Lake Cabin mortgage ", lender: "Wells Fargo", original_amount: "$295,000", interest_rate: "4.25", term_months: "360" }), {
    ok: true,
    body: { name: "Lake Cabin mortgage", property_id: "p1", lender: "Wells Fargo", original_amount: "295000", remaining_balance: "295000", interest_rate: "4.25", term_months: 360 },
  });
  assert.deepEqual(newLoanBody(truck, { name: "Truck loan", monthly_payment: "450" }), {
    ok: true,
    body: { name: "Truck loan", vehicle_id: "v1", monthly_payment: "450" },
  });
  const bad = newLoanBody(cabin, { name: "", original_amount: "lots", interest_rate: "120", term_months: "0" });
  assert.equal(bad.ok, false);
  assert.deepEqual(Object.keys(bad.errors).sort(), ["interest_rate", "name", "original_amount", "term_months"]);
});

test("a new bill: monthly takes a due day, others a due date", () => {
  assert.deepEqual(newBillBody(cabin, { name: "Lake Cabin HOA fee", amount: "50", frequency: "monthly", due_day: "1", category: "HOA" }), {
    ok: true,
    body: { name: "Lake Cabin HOA fee", frequency: "monthly", property_id: "p1", amount: "50", due_day: 1, category: "HOA" },
  });
  assert.deepEqual(newBillBody(truck, { name: "Registration", amount: "120", frequency: "annual", due_day: "5", due_date: "2027-03-01" }), {
    ok: true,
    body: { name: "Registration", frequency: "annual", vehicle_id: "v1", amount: "120", due_date: "2027-03-01" },
  });
  const bad = newBillBody(cabin, { name: " ", amount: "x", frequency: "monthly", due_day: "40" });
  assert.equal(bad.ok, false);
  assert.deepEqual(Object.keys(bad.errors).sort(), ["amount", "due_day", "name"]);
});
