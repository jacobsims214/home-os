// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import { BOOLEAN_OPTIONS, booleanToOption, optionToBoolean } from "./boolean-field.ts";

test("stored booleans show as the matching Yes / No option", () => {
  assert.equal(booleanToOption(true), "true");
  assert.equal(booleanToOption(false), "false");
  const label = (v) => BOOLEAN_OPTIONS.find((o) => o.value === booleanToOption(v))?.label;
  assert.equal(label(true), "Yes");
  assert.equal(label(false), "No");
});

test("unset shows the placeholder", () => {
  assert.equal(booleanToOption(undefined), null);
  assert.equal(booleanToOption(null), null);
  assert.equal(booleanToOption(""), null);
});

test("string values from older forms are still recognised", () => {
  assert.equal(booleanToOption("true"), "true");
  assert.equal(booleanToOption("false"), "false");
});

test("a choice goes back as a boolean, never a string", () => {
  assert.equal(optionToBoolean("true"), true);
  assert.equal(optionToBoolean("false"), false);
  assert.equal(optionToBoolean(null), undefined);
});

test("true / false / unset round-trip unchanged", () => {
  for (const v of [true, false, undefined]) {
    assert.equal(optionToBoolean(booleanToOption(v)), v);
  }
});
