// Run: npm test (node --test). Pins the optimistic check-off (#4566).
//
// The point of these tests: the tick must be on screen before the server
// answers, a refusal must put the row back exactly, and the optimistic row must
// never claim a name it does not have.
import { test } from "node:test";
import assert from "node:assert/strict";
import { applyCheck, applyOpenCount, mergeItems, openCountOf } from "./optimistic.ts";

const row = (over) => ({
  id: over.id ?? "i1",
  list_id: "l1",
  text: over.text ?? "Milk",
  quantity: null,
  unit: null,
  section: null,
  checked: false,
  checked_at: null,
  checked_by: null,
  checked_by_name: null,
  assignee_person_id: null,
  due_date: null,
  position: over.position ?? 0,
  created_at: over.created_at ?? "2026-11-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z",
  ...over,
});

test("checking a row ticks it immediately and claims no name", () => {
  const feed = { items: [row({ id: "a" })], as_of: "2026-11-18T18:00:00Z" };
  const next = applyCheck(feed, ["a"], true);
  assert.equal(next.items[0].checked, true);
  assert.ok(next.items[0].checked_at, "checked_at is set so the row can say when");
  assert.equal(next.items[0].checked_by_name, null, "a client never invents the name");
  assert.equal(next.as_of, feed.as_of, "an optimistic write does not move the server's as_of");
});

test("unchecking clears the whos and whens, so nothing stale is left behind", () => {
  const feed = {
    items: [row({ id: "a", checked: true, checked_at: "2026-11-18T18:00:00Z", checked_by: "u1", checked_by_name: "Jacob" })],
    as_of: "2026-11-18T18:00:00Z",
  };
  const next = applyCheck(feed, ["a"], false);
  assert.equal(next.items[0].checked, false);
  assert.equal(next.items[0].checked_at, null);
  assert.equal(next.items[0].checked_by, null);
  assert.equal(next.items[0].checked_by_name, null);
});

test("a batch ticks every id it names and nothing else", () => {
  const feed = { items: [row({ id: "a" }), row({ id: "b" }), row({ id: "c" })] };
  const next = applyCheck(feed, ["a", "c"], true);
  assert.deepEqual(next.items.map((i) => i.checked), [true, false, true]);
});

test("applyCheck on an unfetched feed is a no-op, not a crash", () => {
  assert.equal(applyCheck(undefined, ["a"], true), undefined);
});

test("the index's open count follows the tap", () => {
  const rows = [
    { id: "l1", open_count: 3 },
    { id: "l2", open_count: 5 },
  ];
  assert.deepEqual(applyOpenCount(rows, "l1", 2).map((r) => r.open_count), [2, 5]);
  assert.deepEqual(applyOpenCount(rows, "nope", 0).map((r) => r.open_count), [3, 5]);
});

test("openCountOf counts the open rows", () => {
  assert.equal(openCountOf([row({ id: "a" }), row({ id: "b", checked: true })]), 1);
  assert.equal(openCountOf([]), 0);
});

// ── the two-phone merge ────────────────────────────────────

test("an incremental feed replaces rows by id and appends new ones", () => {
  const current = [row({ id: "a", position: 1 }), row({ id: "b", position: 2 })];
  const incoming = [
    row({ id: "b", position: 2, checked: true, checked_by_name: "Amber" }),
    row({ id: "c", position: 3 }),
  ];
  const merged = mergeItems(current, incoming);
  assert.deepEqual(merged.map((i) => i.id), ["a", "b", "c"]);
  assert.equal(merged[1].checked, true);
  assert.equal(merged[1].checked_by_name, "Amber", "the other phone's name arrives with the feed");
});

test("a repeated row from the server's 5 s overlap is harmless", () => {
  const current = [row({ id: "a", checked: true })];
  const merged = mergeItems(current, [row({ id: "a", checked: true })]);
  assert.equal(merged.length, 1);
});

test("the merge sorts by position, so the screen order is the server's", () => {
  const current = [row({ id: "b", position: 20 }), row({ id: "a", position: 10 })];
  assert.deepEqual(mergeItems(current, []).map((i) => i.id), ["a", "b"]);
});
