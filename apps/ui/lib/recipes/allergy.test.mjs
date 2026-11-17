// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  addConfirmCopy,
  describeFamilyAllergies,
  needsAddConfirm,
  normaliseChecks,
  normaliseWarnings,
  peopleWithAllergiesSaved,
  reportAllergies,
} from "./allergy.ts";

const PEOPLE = [
  { id: "p1", display_name: "Nora", allergies: ["peanuts"] },
  { id: "p2", display_name: "Theo", allergies: [] },
];
const NOBODY_ALLERGIC = [{ id: "p2", display_name: "Theo", allergies: [] }];

const UNCHECKED_LINE =
  'Nora has an allergy saved as "deathly peanuts" that could not be checked against recipes; confirm the meal is safe.';
const NO_INGREDIENTS = "no ingredients saved, can't check allergies";

test("warnings + checks present: warn and still say something couldn't be checked", () => {
  const r = reportAllergies(
    {
      allergy_warnings: [{ person: "Nora", person_id: "p1", allergy: "peanuts", ingredient: "peanut butter" }],
      checks: [UNCHECKED_LINE],
    },
    PEOPLE,
  );
  assert.equal(r.state, "warnings-and-unchecked");
  assert.equal(r.warnings.length, 1);
  assert.deepEqual(r.checks, [UNCHECKED_LINE]);
  assert.match(r.headline, /Allergy warning/);
  assert.match(r.detail, /couldn't be read/i);
  assert.equal(r.mustNotClaimSafe, true);
});

test("a warning alone names the person, the allergen and what matched", () => {
  const r = reportAllergies(
    { allergy_warnings: [{ person: "Nora", allergy: "peanuts", ingredient: "peanut sauce" }], checks: [] },
    PEOPLE,
  );
  assert.equal(r.state, "warnings");
  assert.equal(r.headline, "Allergy warning");
  // The specific match is on the warning itself, rendered as the list under it.
  assert.deepEqual(r.warnings, [{ person: "Nora", person_id: null, allergy: "peanuts", ingredient: "peanut sauce" }]);
  assert.equal(r.mustNotClaimSafe, true);
});

test("checks alone is 'unchecked' and never reads as safe (#4874 fail-closed)", () => {
  const r = reportAllergies({ allergy_warnings: [], checks: [UNCHECKED_LINE] }, PEOPLE);
  assert.equal(r.state, "unchecked");
  assert.equal(r.headline, "Some allergies couldn't be checked");
  assert.match(r.detail, /not verified safe/);
  assert.equal(r.mustNotClaimSafe, true);
});

test("a recipe with no ingredients is unchecked, not clear", () => {
  const r = reportAllergies({ allergy_warnings: [], checks: [NO_INGREDIENTS] }, PEOPLE);
  assert.equal(r.state, "unchecked");
  assert.deepEqual(r.checks, [NO_INGREDIENTS]);
  assert.equal(r.mustNotClaimSafe, true);
});

test("clean data with allergies saved is the only 'verified' state", () => {
  const r = reportAllergies({ allergy_warnings: [], checks: [] }, PEOPLE);
  assert.equal(r.state, "checked-clear");
  assert.match(r.headline, /nothing matched/);
  assert.match(r.detail, /Nora \(peanuts\)/);
  assert.equal(r.mustNotClaimSafe, false);
});

test("no allergy data at all while the family has allergies: NOT checked, never safe", () => {
  // This is the REST API's shape today: recipeResponse has no allergy fields.
  const r = reportAllergies({}, PEOPLE);
  assert.equal(r.state, "not-checked");
  assert.equal(r.headline, "Not allergy-checked");
  assert.match(r.detail, /Nora \(peanuts\)/);
  assert.match(r.detail, /has not been checked/);
  assert.equal(r.mustNotClaimSafe, true);
  assert.equal(needsAddConfirm(r), true);
});

test("no allergy data and nobody has an allergy: nothing to check", () => {
  const r = reportAllergies({}, NOBODY_ALLERGIC);
  assert.equal(r.state, "none-saved");
  assert.equal(r.mustNotClaimSafe, false);
});

test("people still loading (null) is not 'nobody has allergies'", () => {
  // A failed or pending people read must not license a "safe" claim.
  const r = reportAllergies({}, null);
  assert.equal(r.state, "none-saved");
  // ...but with warnings present it still warns.
  const w = reportAllergies({ allergy_warnings: ["Nora (peanuts): peanut butter"] }, null);
  assert.equal(w.state, "warnings");
  assert.equal(w.mustNotClaimSafe, true);
});

test("recipe_find's short-string warnings are parsed into the same shape", () => {
  const w = normaliseWarnings(["Nora (peanuts): peanut butter", "Sam (diet: vegetarian): chicken thighs", "mystery"]);
  assert.deepEqual(w[0], { person: "Nora", allergy: "peanuts", ingredient: "peanut butter" });
  assert.deepEqual(w[1], { person: "Sam", allergy: "diet: vegetarian", ingredient: "chicken thighs" });
  // Unrecognised text is kept whole, never dropped.
  assert.deepEqual(w[2], { person: "", allergy: "", ingredient: "mystery" });
});

test("normaliseChecks keeps order, trims and de-duplicates", () => {
  assert.deepEqual(normaliseChecks([" b ", "a", "b", ""]), ["b", "a"]);
  assert.deepEqual(normaliseChecks(null), []);
  assert.deepEqual(normaliseChecks(undefined), []);
});

test("peopleWithAllergiesSaved ignores blank entries and people with none", () => {
  const people = peopleWithAllergiesSaved([
    { id: "p1", display_name: "Nora", allergies: [" peanuts ", ""] },
    { id: "p2", display_name: "Theo", allergies: [] },
    { id: "p3", display_name: "Di", allergies: ["shellfish"] },
  ]);
  assert.deepEqual(people.map((p) => p.display_name), ["Nora", "Di"]);
  assert.deepEqual(people[0].allergies, ["peanuts"]);
  assert.equal(describeFamilyAllergies(people), "Nora (peanuts); Di (shellfish)");
});

test("needsAddConfirm: the hard confirm is only for an unsafe or unverified add", () => {
  assert.equal(needsAddConfirm(reportAllergies({ allergy_warnings: [], checks: [] }, PEOPLE)), false);
  assert.equal(needsAddConfirm(reportAllergies({ allergy_warnings: [], checks: [] }, NOBODY_ALLERGIC)), false);
  assert.equal(
    needsAddConfirm(reportAllergies({ allergy_warnings: ["Nora (peanuts): satay"], checks: [] }, PEOPLE)),
    true,
  );
  assert.equal(needsAddConfirm(reportAllergies({ allergy_warnings: [], checks: [NO_INGREDIENTS] }, PEOPLE)), true);
});

test("addConfirmCopy names who is allergic and what matched, and never claims safety", () => {
  const r = reportAllergies(
    { allergy_warnings: [{ person: "Nora", allergy: "peanuts", ingredient: "peanut sauce" }], checks: [UNCHECKED_LINE] },
    PEOPLE,
  );
  const copy = addConfirmCopy(r, "Pad Thai");
  assert.equal(copy.title, "Add \u201CPad Thai\u201D anyway?");
  assert.match(copy.body, /Nora is allergic to peanuts/);
  assert.match(copy.body, /peanut sauce/);
  assert.match(copy.body, /could not be checked/);
});

test("addConfirmCopy explains an unverified recipe with no warnings of its own", () => {
  const r = reportAllergies({}, PEOPLE);
  const copy = addConfirmCopy(r, "Mystery Casserole");
  assert.match(copy.body, /has not been checked against your family's allergies/);
});

test("addConfirmCopy is empty when there is nothing to confirm", () => {
  const copy = addConfirmCopy(reportAllergies({ allergy_warnings: [], checks: [] }, NOBODY_ALLERGIC), "Toast");
  assert.deepEqual(copy, { title: "", body: "" });
});
