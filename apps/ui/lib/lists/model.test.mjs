// Run: npm test (node --test). Guards the pure list helpers (#4566).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  amountLabel,
  checkedLabel,
  clockTime,
  countCaption,
  daysUntil,
  dueBucket,
  dueLabel,
  groupItems,
  guessSection,
  hasStoreMode,
  kindCounts,
  kindLabel,
  parseQuickAdd,
  partitionLists,
  splitChecked,
  updatedLabel,
} from "./model.ts";

// ── parseQuickAdd: the supermarket case ─────────────────────

test("2 lb ground beef → quantity 2, unit lb, text Ground beef", () => {
  assert.deepEqual(parseQuickAdd("2 lb ground beef"), {
    text: "Ground beef",
    quantity: 2,
    unit: "lb",
    section: "Meat & seafood",
  });
});

test("a bare count keeps the number and the name", () => {
  const p = parseQuickAdd("3 eggs");
  assert.equal(p.quantity, 3);
  // "eggs" is not a unit word, so it stays in the name.
  assert.equal(p.text, "Eggs");
  assert.equal(p.unit, null);
  assert.equal(p.section, "Dairy & eggs");
});

test("a word that is not a unit stays part of the name", () => {
  const p = parseQuickAdd("2 heirloom tomatoes");
  assert.equal(p.quantity, 2);
  assert.equal(p.unit, null);
  assert.equal(p.text, "Heirloom tomatoes");
});

test("number words are numbers, and '2 dozen' is not 2", () => {
  assert.equal(parseQuickAdd("two lemons").quantity, 2);
  assert.equal(parseQuickAdd("a loaf of bread").quantity, 1);
  assert.equal(parseQuickAdd("a loaf of bread").unit, "loaf");
  const dozen = parseQuickAdd("2 dozen eggs");
  assert.equal(dozen.quantity, 2, "2 dozen is 2 lots of 12, not 24: the API stores the pair");
  assert.equal(dozen.unit, "dozen");
  assert.equal(dozen.text, "Eggs");
});

test("a line with no amount is all name", () => {
  const p = parseQuickAdd("Paper towels");
  assert.equal(p.text, "Paper towels");
  assert.equal(p.quantity, null);
  assert.equal(p.unit, null);
});

test("a line that is only an amount keeps its words rather than becoming nameless", () => {
  const p = parseQuickAdd("2 lb");
  assert.equal(p.text, "2 lb");
  assert.equal(p.quantity, null, "there is nothing left to name, so no structured amount is claimed");
});

test("empty and whitespace input parse to nothing", () => {
  assert.deepEqual(parseQuickAdd("   "), { text: "", quantity: null, unit: null, section: null });
});

test("capitalisation keeps the rest of the word (BBQ sauce stays BBQ)", () => {
  assert.equal(parseQuickAdd("BBQ sauce").text, "BBQ sauce");
});

// ── the aisle hint ──────────────────────────────────────────

test("the last word wins: chicken broth is Pantry, chicken thighs are Meat", () => {
  assert.equal(guessSection("chicken broth"), "Pantry");
  assert.equal(guessSection("chicken thighs"), "Meat & seafood");
});

test("plurals match their singular aisle", () => {
  assert.equal(guessSection("Bananas"), "Produce");
  assert.equal(guessSection("Diapers"), "Household");
});

test("an unknown word guesses nothing rather than inventing an aisle", () => {
  assert.equal(guessSection("Saffron threads"), null);
});

// ── dates: the #4550 bug class ──────────────────────────────

test("daysUntil counts calendar days, not local-midnight offsets", () => {
  assert.equal(daysUntil("2026-11-26", "2026-11-18"), 8);
  assert.equal(daysUntil("2026-11-18", "2026-11-18"), 0);
  assert.equal(daysUntil("2026-11-14", "2026-11-18"), -4);
});

test("an ISO timestamp due date still lands on its own day", () => {
  assert.equal(daysUntil("2026-11-18T00:00:00Z", "2026-11-18"), 0);
});

test("dueLabel says the day, never the ISO string", () => {
  const today = "2026-11-18";
  assert.equal(dueLabel("2026-11-14", today), "Overdue, Sat");
  assert.equal(dueLabel("2026-11-17", today), "Overdue, yesterday");
  assert.equal(dueLabel("2026-11-18", today), "Today");
  assert.equal(dueLabel("2026-11-19", today), "Tomorrow");
  assert.equal(dueLabel("2026-11-21", today), "Sat");
  assert.equal(dueLabel("2026-11-26", today), "Thu, Nov 26");
});

test("dueBucket has an Overdue bucket, so a late chore is not filed under Later", () => {
  assert.equal(dueBucket({ due_date: "2026-11-14" }, "2026-11-18"), "Overdue");
  assert.equal(dueBucket({ due_date: "2026-11-18" }, "2026-11-18"), "Today");
  assert.equal(dueBucket({ due_date: "2026-11-21" }, "2026-11-18"), "This week");
  assert.equal(dueBucket({ due_date: "2026-12-01" }, "2026-11-18"), "Later");
  assert.equal(dueBucket({ due_date: null }, "2026-11-18"), "No date");
});

// ── grouping ───────────────────────────────────────────────

const item = (over) => ({
  id: over.id ?? Math.random().toString(36),
  list_id: "l1",
  text: over.text ?? "x",
  checked: over.checked ?? false,
  position: 0,
  created_at: "2026-11-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z",
  ...over,
});

test("a grocery list groups by aisle, in the store's walking order", () => {
  const rows = [
    item({ id: "1", text: "Paper towels", section: "Household" }),
    item({ id: "2", text: "Milk", section: "Dairy & eggs" }),
    item({ id: "3", text: "Bananas", section: "Produce" }),
  ];
  assert.deepEqual(
    groupItems(rows, "grocery", "2026-11-18").map((g) => g.label),
    ["Produce", "Dairy & eggs", "Household"],
  );
});

test("a chores list groups by due bucket, overdue first", () => {
  const rows = [
    item({ id: "1", text: "Later", due_date: "2026-12-01" }),
    item({ id: "2", text: "Late", due_date: "2026-11-14" }),
    item({ id: "3", text: "Today", due_date: "2026-11-18" }),
    item({ id: "4", text: "None", due_date: null }),
  ];
  assert.deepEqual(
    groupItems(rows, "chores", "2026-11-18").map((g) => g.label),
    ["Overdue", "Today", "Later", "No date"],
  );
});

test("an aisle-less list is one unlabelled section, not a heading that says Other", () => {
  const rows = [item({ id: "1", section: null }), item({ id: "2", section: null })];
  const groups = groupItems(rows, "grocery", "2026-11-18");
  assert.equal(groups.length, 1);
  assert.equal(groups[0].label, null);
});

test("a packing list with two vestigial aisles still shows both", () => {
  const rows = [item({ id: "1", section: "Everyone" }), item({ id: "2", section: "Jacob" })];
  assert.deepEqual(
    groupItems(rows, "packing", "2026-11-18").map((g) => g.label),
    ["Everyone", "Jacob"],
  );
});

// ── checked rows: who and when ─────────────────────────────

test("a checked row never invents a name", () => {
  const when = "2026-11-18T18:12:00Z";
  const named = checkedLabel({ ...item({}), checked_at: when, checked_by_name: "Jacob" }, new Date(when));
  assert.equal(named, "Checked by Jacob, just now");
  const anon = checkedLabel({ ...item({}), checked_at: when, checked_by_name: null }, new Date(when));
  assert.equal(anon, "Checked just now");
  const bare = checkedLabel({ ...item({}), checked_at: null, checked_by_name: null }, new Date(when));
  assert.equal(bare, "Checked");
});

test("clockTime is a clock today, a weekday this week, a date after", () => {
  const now = new Date(2026, 10, 18, 19, 0, 0); // Wed Nov 18, 7 PM local
  assert.equal(clockTime(new Date(2026, 10, 18, 18, 12).toISOString(), now), "6:12 PM");
  assert.equal(clockTime(new Date(2026, 10, 16, 9, 0).toISOString(), now), "Mon");
  assert.equal(clockTime(new Date(2026, 10, 2, 9, 0).toISOString(), now), "Nov 2");
});

test("splitChecked keeps open rows in server order and sorts checked newest first", () => {
  const { open, checked } = splitChecked([
    item({ id: "a", checked: true, checked_at: "2026-11-18T10:00:00Z" }),
    item({ id: "b" }),
    item({ id: "c", checked: true, checked_at: "2026-11-18T12:00:00Z" }),
    item({ id: "d" }),
  ]);
  assert.deepEqual(open.map((i) => i.id), ["b", "d"]);
  assert.deepEqual(checked.map((i) => i.id), ["c", "a"]);
});

// ── display odds and ends ──────────────────────────────────

test("amountLabel joins quantity and unit, and drops float noise", () => {
  assert.equal(amountLabel({ quantity: 2, unit: "lb" }), "2 lb");
  assert.equal(amountLabel({ quantity: 6, unit: null }), "6");
  assert.equal(amountLabel({ quantity: null, unit: "lb" }), "");
  assert.equal(amountLabel({ quantity: 0.1 + 0.2, unit: "kg" }), "0.3 kg");
});

test("kindCaption and kindLabel come from the kind, not from a per-page table", () => {
  assert.equal(countCaption("packing"), "to pack");
  assert.equal(countCaption("chores"), "to do");
  assert.equal(countCaption("grocery"), "left");
  assert.equal(kindLabel("todo"), "To-do");
  assert.equal(kindLabel("nonsense"), "Other");
});

test("partitionLists separates templates from archived from active", () => {
  const row = (id, over) => ({
    id,
    household_id: "h",
    name: id,
    kind: "grocery",
    is_template: false,
    archived: false,
    item_count: 0,
    open_count: 0,
    created_at: "",
    updated_at: "",
    ...over,
  });
  const out = partitionLists([
    row("a"),
    row("b", { archived: true }),
    row("c", { is_template: true }),
    row("d", { archived: true, is_template: true }),
  ]);
  assert.deepEqual(out.active.map((l) => l.id), ["a"]);
  assert.deepEqual(out.archived.map((l) => l.id), ["b"]);
  assert.deepEqual(out.templates.map((l) => l.id), ["c", "d"]);
});

test("kindCounts keeps the mockup's kind order and omits empty kinds", () => {
  const row = (kind) => ({ id: kind, household_id: "h", name: kind, kind, is_template: false, archived: false, item_count: 0, open_count: 0, created_at: "", updated_at: "" });
  assert.deepEqual(kindCounts([row("chores"), row("grocery"), row("grocery")]), [
    { kind: "grocery", count: 2 },
    { kind: "chores", count: 1 },
  ]);
});

test("updatedLabel says how long ago, in words", () => {
  const now = new Date("2026-11-18T18:00:00Z");
  assert.equal(updatedLabel("2026-11-18T17:58:00Z", now), "Updated 2 min ago");
  assert.equal(updatedLabel("2026-11-18T18:00:00Z", now), "Updated just now");
  assert.equal(updatedLabel("2026-11-18T15:00:00Z", now), "Updated 3 hr ago");
  assert.equal(updatedLabel("2026-11-17T18:00:00Z", now), "Updated yesterday");
});

test("only grocery and shopping get a store mode", () => {
  assert.equal(hasStoreMode("grocery"), true);
  assert.equal(hasStoreMode("shopping"), true);
  assert.equal(hasStoreMode("chores"), false);
  assert.equal(hasStoreMode("packing"), false);
});
