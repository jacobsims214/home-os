/**
 * Ingredient and step parsing for the Recipes pages (#4567).
 *
 * A recipe's ingredients are STRUCTURED — `{name, quantity, unit, section}`
 * (KB api/meals-recipes-meal-plans-feedback.md) — because the meal→grocery
 * merge (#4500) needs quantities and units to add up. A parent pasting a
 * recipe off a website must therefore get structured rows, not one blob.
 *
 * This mirrors, in TypeScript, the rules the MCP recipe tools apply when they
 * read a pasted string (apps/mcp/internal/meals/recipes.go: `parseLine`,
 * `parseAmount`, `normUnit`, `parseSteps`; #4476/#4500). It is duplicated on
 * purpose and deliberately narrow: the SERVER is still the authority (it
 * re-parses anything it is given, and POST /recipes takes structured rows), so
 * a disagreement here can only cost a nicer preview, never a wrong stored
 * value. The approved mockup (#106 v3) shows the paste box turning
 * "2 cups flour, sifted" into quantity 2, unit cup, item flour, note sifted.
 *
 * What is NOT duplicated: the allergen vocabulary. Matching a recipe against
 * a family's allergies stays on the server (`allergensIn`, `allergenGroups`,
 * `recipeAllergyCheck`), because KB mcp/recipe-tools.md is explicit that a
 * second "what counts as a match" list makes the warnings and meal_suggest
 * drift apart. See lib/recipes/allergy.ts.
 *
 * Pure functions and types only (no JSX, no imports), so `node --test` can
 * load this file directly.
 */

/** One structured ingredient row, in the shape the API stores. */
export interface Ingredient {
  /** The food itself ("flour"). Required. */
  name: string;
  /** A number when it can be one ("2", "1 1/2", "½"); otherwise text ("a pinch"). */
  quantity?: number | string | null;
  /** Normalised short unit ("tbsp", "cup", "g"); free text if unknown. */
  unit?: string | null;
  /** Store section ("Produce"), used by the grocery merge. */
  section?: string | null;
  /** Preparation note after a comma ("sifted"). Not stored by the API yet. */
  note?: string | null;
}

/**
 * Unit aliases, copied from `unitAliases` in apps/mcp/internal/meals/recipes.go.
 * Keyed lower-case; `T` and `t` are handled separately because they differ
 * only by case (T = tablespoon, t = teaspoon).
 */
export const UNIT_ALIASES: Readonly<Record<string, string>> = {
  tablespoon: "tbsp", tablespoons: "tbsp", tbsp: "tbsp", tbsps: "tbsp", tbs: "tbsp", tbl: "tbsp",
  teaspoon: "tsp", teaspoons: "tsp", tsp: "tsp", tsps: "tsp",
  cup: "cup", cups: "cup", c: "cup",
  ounce: "oz", ounces: "oz", oz: "oz",
  "fluid ounce": "fl oz", "fluid ounces": "fl oz", "fl oz": "fl oz",
  pound: "lb", pounds: "lb", lb: "lb", lbs: "lb",
  gram: "g", grams: "g", g: "g", gr: "g",
  kilogram: "kg", kilograms: "kg", kg: "kg", kgs: "kg",
  milliliter: "ml", milliliters: "ml", millilitre: "ml", millilitres: "ml", ml: "ml",
  liter: "l", liters: "l", litre: "l", litres: "l", l: "l",
  pint: "pint", pints: "pint", quart: "quart", quarts: "quart", gallon: "gallon", gallons: "gallon",
  clove: "clove", cloves: "clove", can: "can", cans: "can", jar: "jar", jars: "jar",
  package: "package", packages: "package", pkg: "package", packet: "packet", packets: "packet",
  slice: "slice", slices: "slice", stick: "stick", sticks: "stick", bunch: "bunch", bunches: "bunch",
  pinch: "pinch", pinches: "pinch", dash: "dash", dashes: "dash", head: "head", heads: "head",
  sprig: "sprig", sprigs: "sprig", piece: "piece", pieces: "piece", handful: "handful", handfuls: "handful",
};

/** Unit suggestions for the structured editor (the short forms above). */
export const UNIT_OPTIONS: readonly string[] = Array.from(new Set(Object.values(UNIT_ALIASES))).sort();

const UNICODE_FRACTIONS: Readonly<Record<string, number>> = {
  "½": 0.5, "⅓": 1 / 3, "⅔": 2 / 3, "¼": 0.25, "¾": 0.75, "⅕": 0.2, "⅖": 0.4, "⅗": 0.6, "⅘": 0.8,
  "⅙": 1 / 6, "⅚": 5 / 6, "⅛": 0.125, "⅜": 0.375, "⅝": 0.625, "⅞": 0.875,
};

const round3 = (f: number): number => Math.round(f * 1000) / 1000;

/**
 * Reads "2", "1.5", "1/2", "1 1/2", "½" or "1½" as a number (rounded to 3
 * places). `ok` is false for anything else ("a pinch", "1-2"), which the
 * caller keeps as text — the same rule as `parseAmount` in the MCP package.
 */
export function parseAmount(input: string): { value: number; ok: boolean } {
  let s = input.trim();
  if (s === "") return { value: 0, ok: false };

  let total = 0;
  const last = s.slice(-1);
  const fraction = UNICODE_FRACTIONS[last];
  if (fraction !== undefined) {
    total = fraction;
    s = s.slice(0, -1).trim();
    if (s === "") return { value: round3(total), ok: true };
  }

  const parts = s.split(/\s+/).filter(Boolean);
  if (parts.length > 2 || (total !== 0 && parts.length !== 1)) return { value: 0, ok: false };
  for (let i = 0; i < parts.length; i += 1) {
    const part = parts[i];
    const slash = part.indexOf("/");
    if (slash !== -1) {
      if (i === 0 && parts.length === 2) return { value: 0, ok: false }; // "1/2 3"
      const n = Number(part.slice(0, slash));
      const d = Number(part.slice(slash + 1));
      if (!Number.isFinite(n) || !Number.isFinite(d) || d === 0 || n < 0) return { value: 0, ok: false };
      total += n / d;
      continue;
    }
    if (i === 1) return { value: 0, ok: false }; // "1 2"
    const n = Number(part);
    if (!Number.isFinite(n) || n < 0) return { value: 0, ok: false };
    total += n;
  }
  return { value: round3(total), ok: true };
}

/** Maps a unit to its short form; an unknown unit is kept, lower-cased (`normUnit`). */
export function normUnit(unit: string): string {
  const trimmed = unit.trim().replace(/\.$/, "");
  if (trimmed === "T") return "tbsp";
  const lower = trimmed.toLowerCase();
  const mapped = UNIT_ALIASES[lower];
  if (mapped !== undefined && lower !== "t") return mapped;
  if (lower === "t") return "tsp";
  return lower;
}

/** `amountPart` from the MCP package: a leading quantity, with an optional unit after it. */
const AMOUNT_PART = "(\\d+\\s+\\d+\\/\\d+|\\d+\\/\\d+|\\d*[½⅓⅔¼¾⅕⅖⅗⅘⅙⅚⅛⅜⅝⅞]|\\d+(?:\\.\\d+)?)";
const LINE_RE = new RegExp(`^${AMOUNT_PART}\\s*(.*)$`);
const STEP_NUMBER_RE = /^\s*(?:step\s*)?\d+\s*[.):-]\s*/i;
const BULLET_RE = /^[-*•·]\s*/;

/**
 * Turns one pasted ingredient line into a structured ingredient:
 * "2 cups flour, sifted" → {name: "flour", quantity: 2, unit: "cup", note: "sifted"}.
 * Without a leading amount the whole line is the name, exactly like `parseLine`.
 */
export function parseIngredientLine(rawLine: string): Ingredient {
  const line = rawLine.trim().replace(BULLET_RE, "").trim();
  const match = LINE_RE.exec(line);
  if (!match) return { name: line };

  const amount = parseAmount(match[1]);
  if (!amount.ok) return { name: line };

  const rest = match[2].trim();
  if (rest === "") return { name: line }; // a bare number: keep the text

  let unit: string | null = null;
  let words = rest.split(/\s+/).filter(Boolean);
  // Two-word units first ("fl oz", "fluid ounces"), like the MCP parser.
  for (let n = 2; n >= 1; n -= 1) {
    if (words.length <= n) continue;
    const candidate = words.slice(0, n).join(" ").replace(/\.$/, "");
    const known = candidate === "T" || UNIT_ALIASES[candidate.toLowerCase()] !== undefined;
    if (known) {
      unit = normUnit(candidate);
      words = words.slice(n);
      break;
    }
  }
  if (words.length > 1 && words[0].toLowerCase() === "of") words = words.slice(1);

  let item = words.join(" ");
  let note: string | null = null;
  const comma = item.indexOf(",");
  if (comma !== -1) {
    const name = item.slice(0, comma).trim();
    const after = item.slice(comma + 1).trim();
    if (name !== "") {
      item = name;
      if (after !== "") note = after;
    }
  }
  return { name: item, quantity: amount.value, unit, note };
}

/**
 * Parses the paste box: one ingredient per line. Blank lines are dropped, so
 * a trailing newline or a blank line between the ingredients and the steps
 * doesn't produce an empty row.
 */
export function parseIngredientLines(text: string): Ingredient[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "")
    .map(parseIngredientLine);
}

/**
 * Parses a recipe's steps: one per line, with leading numbers ("1.", "2)",
 * "Step 3:") stripped — the order is the number (`parseSteps`). The API stores
 * steps as ONE text column with a step per line (KB recipe-tools.md).
 */
export function parseSteps(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.replace(STEP_NUMBER_RE, "").trim())
    .filter((line) => line !== "");
}

/** Renders an ingredient's quantity for display: 2, 1.5, "a pinch". */
export function formatQuantity(quantity: Ingredient["quantity"]): string {
  if (quantity === null || quantity === undefined || quantity === "") return "";
  if (typeof quantity === "string") return quantity;
  if (!Number.isFinite(quantity)) return "";
  // Common fractions read better than 0.5; everything else keeps up to 2 dp.
  const whole = Math.trunc(quantity);
  const frac = round3(quantity - whole);
  const NAMED: Readonly<Record<string, string>> = {
    "0.25": "¼", "0.333": "⅓", "0.5": "½", "0.667": "⅔", "0.75": "¾", "0.125": "⅛",
  };
  const glyph = NAMED[String(frac)];
  if (glyph) return whole === 0 ? glyph : `${whole}${glyph}`;
  return String(Math.round(quantity * 100) / 100);
}

/** One ingredient as a single readable line: "2 cups flour (sifted)". */
export function formatIngredient(ing: Ingredient): string {
  const q = formatQuantity(ing.quantity);
  const parts = [q, ing.unit ?? "", ing.name].filter((p) => p !== "");
  const base = parts.join(" ");
  return ing.note ? `${base} (${ing.note})` : base;
}

/**
 * Turns the structured rows back into paste-box text, so "edit" shows the
 * same shape the parent typed and a round trip is lossless.
 */
export function ingredientsToText(ingredients: readonly Ingredient[]): string {
  return ingredients.map(formatIngredient).join("\n");
}

/**
 * Drops the rows a parent left blank in the structured editor. A row with a
 * quantity but no name is a half-typed row, not an ingredient, and the API
 * rejects it ("each ingredient needs a name"), so it is dropped here.
 */
export function cleanIngredients(rows: readonly Ingredient[]): Ingredient[] {
  return rows
    .filter((row) => row.name.trim() !== "")
    .map((row) => {
      const out: Ingredient = { name: row.name.trim() };
      if (typeof row.quantity === "number" && Number.isFinite(row.quantity)) out.quantity = row.quantity;
      else if (typeof row.quantity === "string" && row.quantity.trim() !== "") out.quantity = row.quantity.trim();
      if (row.unit && row.unit.trim() !== "") out.unit = row.unit.trim();
      if (row.section && row.section.trim() !== "") out.section = row.section.trim();
      return out;
    });
}

/**
 * Groups ingredients by their store section, in first-seen order, so the
 * detail page reads in supermarket order. Ingredients with no section come
 * last under "Other", which is what a recipe typed without sections looks
 * like.
 */
export function groupBySection(ingredients: readonly Ingredient[]): Array<{ section: string; items: Ingredient[] }> {
  const groups = new Map<string, Ingredient[]>();
  for (const ing of ingredients) {
    const key = (ing.section ?? "").trim() || "Other";
    const list = groups.get(key);
    if (list) list.push(ing);
    else groups.set(key, [ing]);
  }
  const entries = Array.from(groups.entries());
  const named = entries.filter(([k]) => k !== "Other");
  const other = entries.filter(([k]) => k === "Other");
  return [...named, ...other].map(([section, items]) => ({ section, items }));
}
