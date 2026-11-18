// Run: npm test (node --test strips the TypeScript types natively).
//
// The two joins the Members page depends on (#4559): members × people, and the
// 403/404/409 messages the member routes actually answer.
import { test } from "node:test";
import assert from "node:assert/strict";
import { ownerCount, pairMembers, writeError } from "./roster.ts";

const member = (over) => ({
  user_id: "u1",
  email: "a@x.test",
  name: "Amber Sims",
  role: "family_member",
  joined_at: "2026-01-02T00:00:00Z",
  vendor_id: null,
  vendor_name: null,
  color: "violet",
  ...over,
});

const person = (over) => ({
  id: "p1",
  display_name: "Amber Sims",
  relationship: "parent",
  allergies: ["peanuts"],
  user_id: "u1",
  color: "violet",
  ...over,
});

test("members pair with their person by login", () => {
  const [row] = pairMembers([member()], [person()], "u1");
  assert.equal(row.person_id, "p1", "the person id is what PATCH /people/{id} needs");
  assert.equal(row.relationship, "parent");
  assert.deepEqual(row.allergies, ["peanuts"]);
  assert.equal(row.color, "violet");
  assert.equal(row.is_self, true);
});

test("a housesitter's people rows have no user_id, so a unique name pairs them", () => {
  // view() nulls user_id for read_basic along with birthdate/school/grade/notes.
  const [row] = pairMembers([member()], [person({ user_id: null })], null);
  assert.equal(row.person_id, "p1");
  assert.equal(row.relationship, "parent", "a sitter may see the relationship");
  assert.deepEqual(row.allergies, ["peanuts"], "and the allergies — they are safety data");
  assert.equal(row.is_self, false, "without /auth/me no row claims to be you");
});

test("an ambiguous name pairs with nobody rather than with the wrong person", () => {
  const people = [
    person({ id: "p1", user_id: null }),
    person({ id: "p2", user_id: null, allergies: ["shellfish"] }),
  ];
  const [row] = pairMembers([member()], people, null);
  assert.equal(row.person_id, null);
  assert.equal(row.relationship, null);
  assert.deepEqual(row.allergies, [], "a wrong allergy is worse than a missing one");
  assert.equal(row.color, "violet", "the member's own colour still stands");
});

test("the member's colour wins, and a row with no person still renders", () => {
  const [row] = pairMembers([member({ color: "teal" })], [person({ color: "red" })], null);
  assert.equal(row.color, "teal", "people.color and members.color are the same column; prefer the API's member value");
  const [orphan] = pairMembers([member({ user_id: "nobody" })], [], null);
  assert.equal(orphan.person_id, null);
  assert.equal(orphan.color, "violet");
  assert.deepEqual(orphan.allergies, []);
});

test("ownerCount counts the owners, which is what gates last-owner actions", () => {
  assert.equal(ownerCount([]), 0);
  assert.equal(ownerCount([{ role: "owner" }]), 1);
  assert.equal(ownerCount([{ role: "owner" }, { role: "family_manager" }, { role: "viewer" }]), 1);
  assert.equal(ownerCount([{ role: "owner" }, { role: "owner" }]), 2);
});

test("writeError says something a person can act on for 403, 404, 409 and offline", () => {
  const forbidden = writeError({ status: 403, body: { error: { message: "only owners and family managers can manage members" } } }, "x");
  assert.equal(forbidden.title, "You can't do that");
  assert.match(forbidden.detail, /only owners and family managers/);

  const conflict = writeError({ status: 409, body: { error: { message: "the household must keep at least one owner" } } }, "fallback");
  assert.equal(conflict.title, "Not possible right now");
  assert.match(conflict.detail, /at least one owner/);

  const gone = writeError({ status: 404 }, "fallback");
  assert.match(gone.detail, /removed or changed/);

  const offline = writeError({ status: 0, offline: true }, "fallback");
  assert.equal(offline.title, "Not saved");
  assert.match(offline.detail, /Nothing was changed/);

  const other = writeError(new Error("boom"), "The role could not be changed.");
  assert.equal(other.detail, "The role could not be changed.");
});
