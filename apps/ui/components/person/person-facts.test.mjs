// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  allergyEntries,
  allergyNote,
  allergySummary,
  bareAllergen,
  bornValue,
  initials,
  personCreateBody,
  personEditBody,
  personSubtitle,
  preferenceCategory,
  preferenceGroups,
  relationLabel,
  schoolValue,
  splitAllergies,
} from "./person-facts.ts";

/**
 * THE ONE THE MANAGER NOTE ASKS FOR (#4565): an edit that leaves `allergies`
 * out of the PUT clears them on the server, so the body builder must always
 * carry the current list. Renaming a person must not touch their allergies.
 */
test("editing a name keeps the person's allergies in the PUT body", () => {
  const body = personEditBody(
    {
      display_name: "Nora Sims",
      relationship: "daughter",
      birthdate: "2012-03-04",
      school: "Springfield Elementary",
      grade: "8th",
      notes: "",
    },
    ["peanuts (carries an EpiPen)", "tree nuts"],
  );
  assert.deepEqual(body.allergies, ["peanuts (carries an EpiPen)", "tree nuts"]);
  assert.equal(body.display_name, "Nora Sims");
  assert.equal(body.grade, "8th");
});

test("the edit body never omits allergies, even when the person has none", () => {
  const body = personEditBody({ display_name: "Ola" }, []);
  assert.ok(Object.prototype.hasOwnProperty.call(body, "allergies"), "allergies is always a key");
  assert.deepEqual(body.allergies, []);
});

test("blank text fields clear to null; a timestamp birthdate sends its date part", () => {
  const body = personEditBody(
    { display_name: "  Ola  ", relationship: " ", birthdate: "2012-03-04T00:00:00Z", school: "", grade: "", notes: " " },
    ["kiwi"],
  );
  assert.equal(body.display_name, "Ola");
  assert.equal(body.relationship, null);
  assert.equal(body.birthdate, "2012-03-04");
  assert.equal(body.school, null);
  assert.equal(body.grade, null);
  assert.equal(body.notes, null);
  // An unreadable date is not sent as itself (the API would 400 on it).
  assert.equal(personEditBody({ display_name: "Ola", birthdate: "March 4" }, []).birthdate, null);
});

test("a create body has the same shape as an edit body", () => {
  const values = { display_name: "Theo", relationship: "son", birthdate: "", school: "", grade: "", notes: "" };
  assert.deepEqual(personCreateBody(values, ["strawberries"]), personEditBody(values, ["strawberries"]));
});

test("an allergy with a note splits into the bare allergen and the note", () => {
  assert.equal(bareAllergen("peanuts (carries an EpiPen)"), "peanuts");
  assert.equal(allergyNote("peanuts (carries an EpiPen)"), "carries an EpiPen");
  assert.equal(bareAllergen("tree nuts"), "tree nuts");
  assert.equal(allergyNote("tree nuts"), "");
});

test("the list line names every allergen and stays empty with none saved", () => {
  assert.equal(allergySummary({ allergies: ["peanuts (EpiPen)", "tree nuts"] }), "Allergic to peanuts, tree nuts");
  assert.equal(allergySummary({ allergies: [] }), "");
  assert.equal(allergySummary({}), "");
  assert.deepEqual(allergyEntries({ allergies: [" peanuts ", "", "  "] }), ["peanuts"]);
});

test("typed allergies are trimmed, deduped case-insensitively and keep their first spelling", () => {
  assert.deepEqual(splitAllergies("peanuts\nTree nuts, peanuts\n\n  kiwi  "), ["peanuts", "Tree nuts", "kiwi"]);
  assert.deepEqual(splitAllergies(null), []);
});

test("a person reads as relationship and age, or just what is known", () => {
  const nora = { id: "p1", display_name: "Nora Sims", relationship: "daughter", birthdate: "2012-03-04", allergies: [] };
  assert.equal(personSubtitle(nora, "2026-11-18"), "Daughter · 14");
  // Before the household's today is known the age is absent, never guessed.
  assert.equal(personSubtitle(nora, null), "Daughter");
  assert.equal(personSubtitle({ ...nora, relationship: null, birthdate: null }, "2026-11-18"), "");
  assert.equal(relationLabel("  "), "");
});

test("a birthday is read by its own parts and an age only with a today", () => {
  const nora = { id: "p1", display_name: "Nora", birthdate: "2012-03-04" };
  assert.equal(bornValue(nora, "2026-11-18"), "March 4, 2012 (14 years old)");
  assert.equal(bornValue(nora, null), "March 4, 2012");
  assert.equal(bornValue({ id: "p2", display_name: "Ola", birthdate: null }, "2026-11-18"), "\u2014");
});

test("school and grade share one line, and neither invents a value", () => {
  assert.equal(schoolValue({ school: "Springfield Elementary", grade: "4th" }), "Springfield Elementary · 4th");
  assert.equal(schoolValue({ school: "Springfield Elementary", grade: null }), "Springfield Elementary");
  assert.equal(schoolValue({ school: null, grade: null }), "");
});

test("initials are two letters, never a made-up name", () => {
  assert.equal(initials("Nora Sims"), "NS");
  assert.equal(initials("Nora"), "NO");
  assert.equal(initials("  "), "?");
  assert.equal(initials("Mary Jane Watson"), "MW");
});

test("preferences group by kind in a fixed order and drop empty kinds", () => {
  const groups = preferenceGroups([
    { id: "1", kind: "dietary", category: "general", value: "vegetarian" },
    { id: "2", kind: "like", category: "food", value: "pizza" },
    { id: "3", kind: "like", category: "general", value: "dinosaurs" },
  ]);
  assert.deepEqual(groups.map((g) => g.kind), ["like", "dietary"]);
  assert.deepEqual(groups[0].items.map((i) => i.value), ["pizza", "dinosaurs"]);
  assert.deepEqual(preferenceGroups([]), []);
  assert.deepEqual(preferenceGroups(null), []);
});

test("a preference category reads only when it is not the default", () => {
  assert.equal(preferenceCategory("food"), "food");
  assert.equal(preferenceCategory("GENERAL"), "");
  assert.equal(preferenceCategory(null), "");
});
