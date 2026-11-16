/**
 * How the Recipes pages decide what to say about allergies (#4567).
 *
 * THE RULE: this file never decides whether a recipe is safe. It reads what
 * the server said and turns it into a state the UI can render honestly. All
 * matching lives in apps/mcp/internal/meals (`allergensIn`, `allergenGroups`,
 * `checkItems`, `recipeAllergyCheck`); KB mcp/recipe-tools.md forbids a second
 * "what counts as a match" list, because the warnings and meal_suggest drifted
 * apart the last time there were two (#4803).
 *
 * Why this needs a state machine at all: `checks` exists precisely because the
 * server may not have been able to check something. #4874 made an unreadable
 * allergy string fail CLOSED — it lands in `checks` instead of silently
 * matching nothing. If this UI rendered an empty `checks` and a missing field
 * the same way, it would undo that fix: a person would read "no warnings" as
 * "verified safe" when the truth is "nobody looked". So:
 *
 *   warnings      → someone's allergen matched this recipe's ingredients or name
 *   checks        → something could NOT be checked (an allergy string that
 *                   could not be read, or a recipe with no ingredients saved
 *                   while someone has an allergy: `noIngredientsLine`)
 *   not-checked   → the payload carries no allergy data at all (the REST API
 *                   does not return it today) while the family HAS allergies
 *                   saved. We say so rather than implying "safe".
 *   none-saved    → the family has no allergies saved, so there was nothing to
 *                   check. That is the only state where "no warnings" is the
 *                   whole truth.
 *   checked-clear → data present, no warnings, no checks: genuinely verified.
 *
 * Pure functions and types only (no JSX), so `node --test` loads it directly.
 */
import type { Ingredient } from "./ingredients";

/**
 * One allergy hit, as the MCP recipe tools return it (`AllergyWarning`).
 * `ingredient` is the ingredient OR the recipe name that matched (#4803).
 */
export interface AllergyWarning {
  person: string;
  person_id?: string | null;
  /** The allergen label as saved ("peanuts"), never the raw sentence. */
  allergy: string;
  /** What matched: "peanut butter", or the recipe's own name. */
  ingredient: string;
}

/** The family member whose allergies are in play (GET /api/v1/people). */
export interface PersonAllergy {
  id: string;
  display_name: string;
  allergies: string[];
}

/** What a recipe carries about allergies. Both fields are optional on purpose. */
export interface RecipeAllergyData {
  allergy_warnings?: AllergyWarning[] | string[] | null;
  checks?: string[] | null;
}

export type AllergyState =
  /** Someone's allergen matched: warn, and offer to swap — never block. */
  | "warnings"
  /** Something could not be checked. Shown even when there are warnings too. */
  | "unchecked"
  /** Both: a warning AND an unverifiable allergy. */
  | "warnings-and-unchecked"
  /** No allergy data came back and the family has allergies saved: say so. */
  | "not-checked"
  /** The family has no allergies saved, so nothing needed checking. */
  | "none-saved"
  /** Data came back clean: genuinely verified. */
  | "checked-clear";

export interface AllergyReport {
  state: AllergyState;
  /** Normalised warnings (the API may return short strings). */
  warnings: AllergyWarning[];
  /** Verbatim lines from the server's `checks` array. Never reworded. */
  checks: string[];
  /** True when the family has at least one allergy saved. */
  familyHasAllergies: boolean;
  /** People with allergies saved, for "Nora is allergic to peanuts". */
  peopleWithAllergies: PersonAllergy[];
  /**
   * The headline. Deliberately says what was NOT checked when that is the
   * case: "couldn't be checked" never reads as "safe".
   */
  headline: string;
  /** One line of detail under the headline; empty when nothing to add. */
  detail: string;
  /** True when the page must not claim the recipe is safe. */
  mustNotClaimSafe: boolean;
}

/**
 * A recipe's warnings arrive either as objects (recipe_get) or as short
 * strings (recipe_find items: "Nora (peanuts): peanut butter"). Both are
 * rendered; a string is kept whole because re-parsing it would be guessing.
 */
export function normaliseWarnings(raw: RecipeAllergyData["allergy_warnings"]): AllergyWarning[] {
  if (!Array.isArray(raw)) return [];
  const out: AllergyWarning[] = [];
  for (const item of raw) {
    if (typeof item === "string") {
      const text = item.trim();
      if (text === "") continue;
      // "Nora (peanuts): peanut butter" — parse only the documented shape;
      // anything else keeps the whole line as the "what matched" text.
      const m = /^([^()]+?)\s*\(([^)]*)\)\s*:\s*(.+)$/.exec(text);
      if (m) out.push({ person: m[1].trim(), allergy: m[2].trim(), ingredient: m[3].trim() });
      else out.push({ person: "", allergy: "", ingredient: text });
      continue;
    }
    if (item && typeof item === "object") {
      const w = item as AllergyWarning;
      if (typeof w.person !== "string" && typeof w.allergy !== "string" && typeof w.ingredient !== "string") continue;
      out.push({
        person: String(w.person ?? ""),
        person_id: w.person_id ?? null,
        allergy: String(w.allergy ?? ""),
        ingredient: String(w.ingredient ?? ""),
      });
    }
  }
  return out;
}

/** The server's `checks` lines, trimmed and de-duplicated, order kept. */
export function normaliseChecks(raw: RecipeAllergyData["checks"]): string[] {
  if (!Array.isArray(raw)) return [];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const line of raw) {
    if (typeof line !== "string") continue;
    const text = line.trim();
    if (text === "" || seen.has(text)) continue;
    seen.add(text);
    out.push(text);
  }
  return out;
}

/** People with at least one non-blank allergy saved. */
export function peopleWithAllergiesSaved(people: readonly PersonAllergy[] | null | undefined): PersonAllergy[] {
  if (!Array.isArray(people)) return [];
  return people
    .map((p) => ({
      id: String(p.id ?? ""),
      display_name: String(p.display_name ?? "").trim(),
      allergies: (Array.isArray(p.allergies) ? (p.allergies as unknown[]) : [])
        .map((a: unknown) => String(a).trim())
        .filter((a: string) => a !== ""),
    }))
    .filter((p) => p.allergies.length > 0);
}

/**
 * "Nora (peanuts), Jacob (peanuts and tree nuts)" — the family's saved
 * allergies, for the banner and for the housesitter view. Empty when none.
 */
export function describeFamilyAllergies(people: readonly PersonAllergy[]): string {
  return people
    .filter((p) => p.allergies.length > 0)
    .map((p) => `${p.display_name || "Someone"} (${p.allergies.join(", ")})`)
    .join("; ");
}

/**
 * Works out what the page may say. `people` is the household's people list
 * (null while it is still loading, or if the read failed — both are treated
 * as "we don't know yet", never as "nobody has allergies").
 */
export function reportAllergies(
  data: RecipeAllergyData | null | undefined,
  people: readonly PersonAllergy[] | null | undefined,
): AllergyReport {
  const withAllergies = peopleWithAllergiesSaved(people);
  const familyHasAllergies = withAllergies.length > 0;
  const warnings = normaliseWarnings(data?.allergy_warnings);
  const checks = normaliseChecks(data?.checks);
  // The payload carries allergy data only if the field is PRESENT. `null` and
  // `[]` are both "present" (the server looked and found nothing); an absent
  // key means the server said nothing about allergies at all.
  const dataPresent = !!data && (data.allergy_warnings !== undefined || data.checks !== undefined);

  let state: AllergyState;
  if (warnings.length > 0 && checks.length > 0) state = "warnings-and-unchecked";
  else if (warnings.length > 0) state = "warnings";
  else if (checks.length > 0) state = "unchecked";
  else if (!dataPresent && familyHasAllergies) state = "not-checked";
  else if (!familyHasAllergies) state = "none-saved";
  else state = "checked-clear";

  const who = describeFamilyAllergies(withAllergies);
  let headline = "";
  let detail = "";

  switch (state) {
    case "warnings":
      // The headline is the ALARM; the list under it carries who/what/which
      // ingredient. Repeating the match in all three places read as noise and
      // made the banner three lines longer than it needed to be.
      headline = warnings.length === 1 ? "Allergy warning" : `Allergy warning: ${warnings.length} matches`;
      detail = familyHasAllergies
        ? "The family's saved allergies are applied automatically. Swap it, or plan it only if everyone eating is safe."
        : "";
      break;
    case "unchecked":
      headline = "Some allergies couldn't be checked";
      detail =
        "Home OS couldn't read everything saved about allergies, so this recipe is not verified safe. Check the ingredients yourself before cooking.";
      break;
    case "warnings-and-unchecked":
      headline = "Allergy warning — and something couldn't be checked";
      detail =
        "There is a match below, and at least one saved allergy couldn't be read — so there may be more. Check the ingredients yourself.";
      break;
    case "not-checked":
      headline = "Not allergy-checked";
      detail = who
        ? `${who}: this recipe came back without allergy information, so it has not been checked against those allergies. Check the ingredients yourself.`
        : "This recipe came back without allergy information, so it has not been checked.";
      break;
    case "none-saved":
      headline = "No allergies saved for this family";
      detail = "Add an allergy to a person on People & House and every recipe is checked against it automatically.";
      break;
    case "checked-clear":
      headline = "Checked against the family's allergies: nothing matched";
      detail = who ? `Checked for ${who}.` : "";
      break;
  }

  return {
    state,
    warnings,
    checks,
    familyHasAllergies,
    peopleWithAllergies: withAllergies,
    headline,
    detail,
    // Anything but a clean check means the page must not say "safe".
    mustNotClaimSafe: state !== "checked-clear" && state !== "none-saved",
  };
}

/**
 * Whether adding this recipe to the plan needs the hard confirm dialog. The
 * manager's decision on #4519 (#13039/#13046): a warn banner and an inline
 * flag on a meal that is ALREADY planned — never a modal mid-cook — but ONE
 * hard confirm at the moment an unsafe recipe is added, because that is the
 * only point where a block protects anyone.
 *
 * "unsafe" includes "couldn't be checked": a recipe we cannot verify is not
 * one to add silently (the same fail-closed rule the server applies to a
 * recipe with no ingredients while anyone has an allergy, #4803).
 */
export function needsAddConfirm(report: AllergyReport): boolean {
  return report.mustNotClaimSafe;
}

/** The confirm dialog's title and body for a recipe being added to the plan. */
export function addConfirmCopy(report: AllergyReport, title: string): { title: string; body: string } {
  if (report.state === "none-saved") return { title: "", body: "" };
  const lines: string[] = [];
  for (const w of report.warnings) {
    lines.push(`${w.person || "Someone"} is allergic to ${w.allergy || "an ingredient"} — this recipe has ${w.ingredient}.`);
  }
  for (const c of report.checks) lines.push(c);
  if (lines.length === 0 && report.state === "not-checked") {
    lines.push(`This recipe came back without allergy information, so it has not been checked against your family's allergies.`);
  }
  return {
    title: `Add “${title}” anyway?`,
    body: lines.join(" "),
  };
}

/**
 * How many ingredient rows a recipe has, for the "no ingredients saved"
 * warning. The server fails closed on an ingredient-less recipe when anyone
 * has an allergy (`noIngredientsLine`), so the editor warns while typing.
 */
export function hasIngredients(ingredients: readonly Ingredient[] | null | undefined): boolean {
  return Array.isArray(ingredients) && ingredients.some((i) => (i?.name ?? "").trim() !== "");
}
