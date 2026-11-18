/**
 * Property tree grouping and overdue math (#4578). Covers the two rules the
 * property page must never break: a thing with no room is shown, not dropped,
 * and a thing whose room belongs to another property does not appear to be in
 * this one's room.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  groupByRoom,
  isOverdue,
  isOpenTask,
  overdueTasks,
  roomIconKey,
  roomOverdueCount,
  thingSubtitle,
  thingsLabel,
  typeLabel,
} from "./property-tree.ts";

const ROOMS = [
  { id: "r1", name: "Kitchen", floor: 1 },
  { id: "r2", name: "Utility room", floor: 0 },
  { id: "r3", name: "Backyard", floor: null },
];
const THINGS = [
  { id: "a1", name: "Furnace", room_id: "r2", manufacturer: "Carrier", model: "Infinity 59SC5" },
  { id: "a2", name: "Roof", room_id: null },
  { id: "a3", name: "Kettle", room_id: "r1", category: "Appliance" },
  // A room of a different property: not this page's room.
  { id: "a4", name: "Dock lift", room_id: "elsewhere" },
];

test("things group under their room, and not-in-a-room is kept", () => {
  const { rooms, notInARoom } = groupByRoom(ROOMS, THINGS);
  assert.deepEqual(rooms.map((r) => [r.room.id, r.things.map((t) => t.id)]), [
    ["r1", ["a3"]],
    ["r2", ["a1"]],
    ["r3", []],
  ]);
  assert.deepEqual(notInARoom.map((t) => t.id), ["a2", "a4"]);
});

test("an empty property yields no rooms and no loose things", () => {
  const { rooms, notInARoom } = groupByRoom([], []);
  assert.equal(rooms.length, 0);
  assert.equal(notInARoom.length, 0);
});

test("overdue only counts open tasks past their due date", () => {
  const now = new Date("2026-11-18T12:00:00Z");
  assert.equal(isOverdue({ id: "t1", name: "Filter", status: "pending", due_date: "2026-11-13" }, now), true);
  assert.equal(isOverdue({ id: "t2", name: "Filter", status: "done", due_date: "2026-11-13" }, now), false);
  assert.equal(isOverdue({ id: "t3", name: "Filter", status: "pending", due_date: "2026-11-18" }, now), false);
  assert.equal(isOverdue({ id: "t4", name: "Filter", status: "pending", due_date: null }, now), false);
  assert.equal(isOpenTask({ id: "t5", name: "x", status: "in_progress" }), true);
  assert.equal(isOpenTask({ id: "t6", name: "x", status: "skipped" }), false);
});

test("a room's overdue count is its own things only, soonest first across the property", () => {
  const now = new Date("2026-11-18T12:00:00Z");
  const tasks = [
    { id: "t1", name: "Replace filter", status: "pending", due_date: "2026-11-13", asset_id: "a1" },
    { id: "t2", name: "Clean gutters", status: "pending", due_date: "2026-11-06", asset_id: "a1" },
    { id: "t3", name: "Kettle descale", status: "pending", due_date: "2026-11-10", asset_id: "a3" },
    { id: "t4", name: "Whole house", status: "pending", due_date: "2026-11-01", asset_id: null },
    { id: "t5", name: "Done one", status: "done", due_date: "2026-10-01", asset_id: "a1" },
  ];
  assert.equal(roomOverdueCount([{ id: "a1", name: "Furnace" }], tasks, now), 2);
  assert.equal(roomOverdueCount([{ id: "a3", name: "Kettle" }], tasks, now), 1);
  assert.equal(roomOverdueCount([], tasks, now), 0);
  assert.deepEqual(overdueTasks(tasks, now).map((t) => t.id), ["t4", "t2", "t3", "t1"]);
});

test("labels read like the mockup", () => {
  assert.equal(thingsLabel(1), "1 thing");
  assert.equal(thingsLabel(5), "5 things");
  assert.equal(thingSubtitle({ id: "a", name: "Furnace", manufacturer: "Carrier", model: "59SC5" }), "Carrier · 59SC5");
  assert.equal(thingSubtitle({ id: "a", name: "Roof" }), "");
  assert.equal(roomIconKey("Utility room"), "tool");
  assert.equal(roomIconKey("Ellie's room"), "bed");
  assert.equal(roomIconKey("Backyard"), "tree");
  assert.equal(roomIconKey("Whatever"), "door");
});

test("property types read as words, and an unknown type is not hidden", () => {
  assert.equal(typeLabel("single_family"), "Single family");
  assert.equal(typeLabel("multi_family"), "Multi-family");
  assert.equal(typeLabel("barn"), "barn");
  assert.equal(typeLabel(null), null);
});
