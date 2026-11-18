// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import { buildCreatePayload, cardParts, createFieldsFor, formatValue } from "./entity-list-utils.ts";

const BILL_COLUMNS = [
  { name: "name", label: "Name" },
  { name: "amount", label: "Amount", format: "currency" },
  { name: "due_day", label: "Due" },
];

test("create fields follow the API types for known entities", () => {
  const bill = createFieldsFor("bill", BILL_COLUMNS);
  assert.equal(bill.find((f) => f.name === "amount").type, "decimal");
  assert.equal(bill.find((f) => f.name === "due_day").type, "integer");
  assert.equal(bill.find((f) => f.name === "property_id").type, "property");
  assert.equal(createFieldsFor("vehicle", []).find((f) => f.name === "year").type, "integer");
});

test("unknown entity types fall back to the columns, first one required", () => {
  const fields = createFieldsFor("gadget", BILL_COLUMNS);
  assert.deepEqual(fields.map((f) => [f.name, f.type, !!f.required]), [
    ["name", "text", true],
    ["amount", "decimal", false],
    ["due_day", "text", false],
  ]);
});

test("payload trims, drops empty optionals, and types numbers the way the API wants", () => {
  const result = buildCreatePayload(createFieldsFor("bill", []), {
    name: "  Water  ",
    amount: "$1,204.50",
    due_day: "14",
    category: "",
  });
  assert.deepEqual(result, { ok: true, body: { name: "Water", amount: "1204.50", due_day: 14 } });
});

test("payload reports missing required fields and bad numbers per field", () => {
  const result = buildCreatePayload(createFieldsFor("bill", []), { name: " ", amount: "lots", due_day: "40" });
  assert.equal(result.ok, false);
  assert.deepEqual(Object.keys(result.errors).sort(), ["amount", "due_day", "name"]);
  assert.match(result.errors.due_day, /between 1 and 31/);

  const half = buildCreatePayload(createFieldsFor("vehicle", []), { year: "2019.5", make: "Honda", model: "Fit" });
  assert.equal(half.ok, false);
  assert.match(half.errors.year, /whole number/);
});

test("formatValue: currency, empty values, bad dates", () => {
  assert.equal(formatValue("182.4", "currency"), "$182");
  assert.equal(formatValue(null), "—");
  assert.equal(formatValue(""), "—");
  assert.equal(formatValue("abc", "currency"), "—");
  assert.equal(formatValue("not a date", "date"), "not a date");
  assert.equal(formatValue(12), "12");
});

test("cardParts: named entities drop the name column; unnamed ones are named by their columns", () => {
  assert.deepEqual(cardParts({ id: "b1", name: "Internet", amount: "70" }, BILL_COLUMNS), {
    heading: "Internet",
    details: BILL_COLUMNS.slice(1),
  });
  const vehicleCols = [
    { name: "year", label: "Year" },
    { name: "make", label: "Make" },
    { name: "model", label: "Model" },
  ];
  assert.deepEqual(cardParts({ id: "v1", year: 2019, make: "Honda", model: "Odyssey" }, vehicleCols), {
    heading: "2019 Honda Odyssey",
    details: [],
  });
  assert.equal(cardParts({ id: "x" }, vehicleCols).heading, "Untitled");
});
