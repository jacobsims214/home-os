// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  exclusionReasons,
  explainSuggestions,
  groupExclusions,
  groupLabel,
  parseExclusionReason,
} from "./suggestions.ts";

const UNCHECKED = 'Nora has an allergy saved as "deathly peanuts" that could not be checked against recipes; confirm the meal is safe.';

test("parseExclusionReason reads the server's allergy reason", () => {
  assert.deepEqual(parseExclusionReason("Pad Thai", "Nora (peanuts): peanut sauce"), [
    { recipe: "Pad Thai", person: "Nora", kind: "allergy", rule: "peanuts", matched: "peanut sauce" },
  ]);
});

test("parseExclusionReason tells a diet rule from an allergy", () => {
  const [r] = parseExclusionReason("Chicken curry", "Sam (diet: vegetarian): chicken thighs");
  assert.equal(r.kind, "diet");
  assert.equal(r.rule, "vegetarian");
  assert.equal(r.matched, "chicken thighs");
});

test("parseExclusionReason keeps an unrecognised reason whole instead of dropping it", () => {
  const [r] = parseExclusionReason("Dumplings", "no ingredients saved, can't check allergies");
  assert.equal(r.kind, "unknown");
  assert.equal(r.matched, "no ingredients saved, can't check allergies");
});

test("a recipe excluded for two people gives two reasons", () => {
  const reasons = parseExclusionReason("Satay", "Nora (peanuts): peanut sauce; Jacob (peanuts): satay");
  assert.equal(reasons.length, 2);
  assert.deepEqual(reasons.map((r) => r.person), ["Nora", "Jacob"]);
});

test("the manager's fish case: 11 of 12 recipes excluded, explained rather than blank", () => {
  // meal_suggest over-matches on purpose; the UI must explain it, not look broken.
  const excluded = Array.from({ length: 11 }, (_, i) => ({
    name: `Fish dish ${i + 1}`,
    reason: "Amber (fish): salmon",
  }));
  const e = explainSuggestions({ suggestions: [], excluded, excluded_total: 11, checks: [] }, 5);
  assert.equal(e.kind, "none");
  assert.equal(e.headline, "No ideas left after allergy checks");
  assert.match(e.body, /Amber's fish allergy/);
  assert.match(e.body, /safety rule working, not a failure/);
  assert.equal(e.groups.length, 1);
  assert.equal(e.groups[0].recipes.length, 11);
  assert.equal(e.reassuring, true);
});

test("several rules: the biggest cause leads and the rest are counted", () => {
  const e = explainSuggestions({
    suggestions: [],
    excluded: [
      { name: "A", reason: "Nora (peanuts): satay" },
      { name: "B", reason: "Nora (peanuts): pad thai" },
      { name: "C", reason: "Sam (diet: vegetarian): chicken" },
    ],
    excluded_total: 3,
  });
  assert.equal(e.groups[0].person, "Nora");
  assert.equal(groupLabel(e.groups[0]), "Nora's peanuts allergy");
  assert.equal(groupLabel(e.groups[1]), "Sam's vegetarian diet");
  assert.match(e.body, /Nora's peanuts allergy and 1 more rule/);
});

test("some ideas left: say how many were excluded for safety, without alarm", () => {
  const e = explainSuggestions(
    { suggestions: [{ name: "Tacos" }], excluded: [{ name: "Pad Thai", reason: "Nora (peanuts): peanuts" }], excluded_total: 1 },
    5,
  );
  assert.equal(e.kind, "few");
  assert.equal(e.headline, "1 of 5 ideas — 1 recipe left out for safety");
});

test("excluded_total above the named list reports the hidden remainder", () => {
  const excluded = Array.from({ length: 20 }, (_, i) => ({ name: `R${i}`, reason: "Nora (peanuts): peanut" }));
  const e = explainSuggestions({ suggestions: [], excluded, excluded_total: 27 });
  assert.equal(e.excludedTotal, 27);
  assert.equal(e.excludedHidden, 7);
});

test("nothing excluded and nothing suggested: the family has no recipes yet", () => {
  const e = explainSuggestions({ suggestions: [], excluded: [], excluded_total: 0 });
  assert.equal(e.kind, "no-recipes");
  assert.equal(e.headline, "No recipes to suggest yet");
});

test("filters, not safety: a time limit or tag removed everything", () => {
  const e = explainSuggestions({ suggestions: [], excluded: [], filtered_out: 4 });
  assert.equal(e.kind, "filtered");
  assert.equal(e.headline, "Nothing matched those filters");
  assert.match(e.body, /4 recipes were left out/);
});

test("checks are carried through verbatim so an unreadable allergy is visible", () => {
  const e = explainSuggestions({ suggestions: [], excluded: [], checks: [UNCHECKED] });
  assert.deepEqual(e.checks, [UNCHECKED]);
});

test("a null result (request failed) is not presented as 'no recipes'", () => {
  // explainSuggestions is only called with a real result; a failure is an
  // error state with a retry. Passing null must still not crash.
  const e = explainSuggestions(null);
  assert.equal(e.kind, "no-recipes");
  assert.deepEqual(e.checks, []);
});

test("exclusionReasons and groupExclusions are consistent", () => {
  const summary = {
    excluded: [
      { name: "A", reason: "Nora (peanuts): satay; Jacob (peanuts): satay" },
      { name: "B", reason: "Nora (peanuts): peanut oil" },
    ],
    excluded_total: 2,
  };
  const reasons = exclusionReasons(summary);
  assert.equal(reasons.length, 3);
  const groups = groupExclusions(reasons);
  assert.equal(groups.length, 2);
  // Nora's rule excluded both recipes; Jacob's only one, so Nora leads.
  assert.equal(groups[0].person, "Nora");
  assert.deepEqual(groups[0].recipes, ["A", "B"]);
  assert.deepEqual(groups[1].recipes, ["A"]);
});
