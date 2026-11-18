/**
 * Who may write a property, its rooms and its things (#4578): the family roles
 * only, as roles.go grants ResProperties / ResAssets ActWrite.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { canWriteProperty } from "./property-access.ts";

test("family roles may add rooms and things", () => {
  for (const role of ["owner", "family_manager", "family_member"]) {
    assert.equal(canWriteProperty(role), true, role);
  }
});

test("a housesitter, a vendor and a viewer see the house read-only", () => {
  for (const role of ["housesitter", "vendor", "viewer", "something-new"]) {
    assert.equal(canWriteProperty(role), false, role);
  }
});

test("an unknown role is optimistic; the API is the gate", () => {
  assert.equal(canWriteProperty(null), true);
  assert.equal(canWriteProperty(""), true);
});
