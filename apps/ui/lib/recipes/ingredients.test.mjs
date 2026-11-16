// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  cleanIngredients,
  formatIngredient,
  formatQuantity,
  groupBySection,
  ingredientsToText,
  normUnit,
  parseAmount,
  parseIngredientLine,
  parseIngredientLines,
  parseSteps,
} from "./ingredients.ts";

test("parseAmount reads every form the MCP parser accepts", () => {
  assert.deepEqual(parseAmount("2"), { value: 2, ok: true });
  assert.deepEqual(parseAmount("1.5"), { value: 1.5, ok: true });
  assert.deepEqual(parseAmount("1/2"), { value: 0.5, ok: true });
  assert.deepEqual(parseAmount("1 1/2"), { value: 1.5, ok: true });
  assert.deepEqual(parseAmount("½"), { value: 0.5, ok: true });
  assert.deepEqual(parseAmount("1½"), { value: 1.5, ok: true });
  assert.deepEqual(parseAmount("  2  "), { value: 2, ok: true });
});

test("parseAmount refuses text, ranges and nonsense (the quantity stays a string)", () => {
  for (const bad of ["a pinch", "1-2", "", "lots", "1/0", "1 2", "-3"]) {
    assert.equal(parseAmount(bad).ok, false, bad);
  }
});

test("normUnit shortens known units and keeps unknown ones lower-cased", () => {
  assert.equal(normUnit("tablespoons"), "tbsp");
  assert.equal(normUnit("T"), "tbsp");
  assert.equal(normUnit("t"), "tsp");
  assert.equal(normUnit("Cups"), "cup");
  assert.equal(normUnit("lbs"), "lb");
  assert.equal(normUnit("cloves"), "clove");
  assert.equal(normUnit("handful"), "handful");
  assert.equal(normUnit("Smidgen"), "smidgen");
  assert.equal(normUnit("fl oz."), "fl oz");
});

test("parseIngredientLine splits the mockup's example: 2 cups flour, sifted", () => {
  assert.deepEqual(parseIngredientLine("2 cups flour, sifted"), {
    name: "flour",
    quantity: 2,
    unit: "cup",
    note: "sifted",
  });
});

test("parseIngredientLine handles fractions, two-word units, bullets and bare names", () => {
  assert.deepEqual(parseIngredientLine("1½ lb chicken thighs, sliced"), {
    name: "chicken thighs",
    quantity: 1.5,
    unit: "lb",
    note: "sliced",
  });
  assert.deepEqual(parseIngredientLine("2 fl oz vanilla"), { name: "vanilla", quantity: 2, unit: "fl oz", note: null });
  assert.deepEqual(parseIngredientLine("- 3 cloves garlic"), { name: "garlic", quantity: 3, unit: "clove", note: null });
  // No amount: the whole line is the name, exactly like parseLine.
  assert.deepEqual(parseIngredientLine("Salt and pepper to taste"), { name: "Salt and pepper to taste" });
  // A line with no amount has no quantity/unit/note keys at all.
  assert.equal("quantity" in parseIngredientLine("Salt and pepper to taste"), false);
  // "of" is dropped when a unit was found.
  assert.deepEqual(parseIngredientLine("2 cups of rice"), { name: "rice", quantity: 2, unit: "cup", note: null });
  // A bare number with no item keeps the text rather than becoming nameless.
  assert.deepEqual(parseIngredientLine("2"), { name: "2" });
});

test("parseIngredientLines drops blank lines and keeps order", () => {
  const rows = parseIngredientLines("1½ lb Italian sausage\n\n3 bell peppers, sliced\n  \n1 red onion, sliced\n");
  assert.deepEqual(rows.map((r) => r.name), ["Italian sausage", "bell peppers", "red onion"]);
  assert.equal(rows[0].quantity, 1.5);
  assert.equal(rows[1].note, "sliced");
});

test("parseSteps strips leading numbers and blank lines", () => {
  assert.deepEqual(
    parseSteps("1. Heat the oven to 425°F.\n2) Toss everything.\nStep 3: Roast 30 min.\n\n"),
    ["Heat the oven to 425°F.", "Toss everything.", "Roast 30 min."],
  );
});

test("formatQuantity renders fractions the way a recipe does", () => {
  assert.equal(formatQuantity(2), "2");
  assert.equal(formatQuantity(1.5), "1½");
  assert.equal(formatQuantity(0.5), "½");
  assert.equal(formatQuantity(0.25), "¼");
  assert.equal(formatQuantity(1.25), "1¼");
  assert.equal(formatQuantity(2.5), "2½");
  assert.equal(formatQuantity("a pinch"), "a pinch");
  assert.equal(formatQuantity(null), "");
  assert.equal(formatQuantity(undefined), "");
});

test("formatIngredient rebuilds a readable line and round-trips", () => {
  assert.equal(formatIngredient({ name: "flour", quantity: 2, unit: "cup", note: "sifted" }), "2 cup flour (sifted)");
  assert.equal(formatIngredient({ name: "salt" }), "salt");
  const rows = parseIngredientLines("2 cups flour, sifted\n3 cloves garlic");
  assert.equal(ingredientsToText(rows), "2 cup flour (sifted)\n3 clove garlic");
});

test("cleanIngredients drops half-typed rows and trims the rest", () => {
  const cleaned = cleanIngredients([
    { name: "  flour  ", quantity: 2, unit: " cup " },
    { name: "   ", quantity: 3 },
    { name: "salt", quantity: "a pinch" },
  ]);
  assert.deepEqual(cleaned, [
    { name: "flour", quantity: 2, unit: "cup" },
    { name: "salt", quantity: "a pinch" },
  ]);
});

test("groupBySection keeps first-seen order and puts Other last", () => {
  const groups = groupBySection([
    { name: "garlic", section: "Produce" },
    { name: "salt" },
    { name: "limes", section: "Produce" },
    { name: "chicken", section: "Meat" },
  ]);
  assert.deepEqual(groups.map((g) => g.section), ["Produce", "Meat", "Other"]);
  assert.deepEqual(groups[0].items.map((i) => i.name), ["garlic", "limes"]);
  assert.deepEqual(groups[2].items.map((i) => i.name), ["salt"]);
});
