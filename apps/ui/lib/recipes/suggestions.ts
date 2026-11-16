/**
 * Why meal_suggest returned few or no ideas — and how to say it (#4567).
 *
 * A blank suggestion list reads as broken. An explained one reads as safe.
 * meal_suggest legitimately returns ZERO suggestions for a family with
 * allergies: allergens are a hard exclude, and a broad allergen over-matches
 * on purpose (the manager's note on this ticket: fish over-matches 11 of 12
 * sample recipes, and that is the correct behaviour — a missed allergen can be
 * anaphylaxis, a needless exclusion is a nuisance).
 *
 * So the empty state must explain WHICH allergy excluded WHAT, using the
 * `excluded` reasons the server already produces:
 *   "Nora (peanuts): peanut butter"     — an allergen
 *   "Sam (diet: vegetarian): chicken thighs" — a diet rule
 *   "no ingredients saved, can't check allergies" — fail-closed (#4803)
 * plus `checks` for anything that could not be applied at all (#4802/#4874).
 *
 * This module is pure so the meals page (#4568, which owns the suggestions
 * sheet) and this ticket's recipe pages share one implementation. Do not
 * write a second copy: KB mcp/meal-suggest-tool.md documents that the
 * suggester and the warnings drifting apart is the exact bug class that
 * #4803 had to fix.
 *
 * No JSX here; the rendering lives in components/recipes/SuggestionEmptyState.
 */

/** One excluded recipe, as meal_suggest returns it (`excludedRecipe`). */
export interface ExcludedRecipe {
  recipe_id?: string;
  name: string;
  /** "; "-joined reasons: "Nora (peanuts): peanut butter". */
  reason: string;
}

/** The part of meal_suggest's result this module reads. */
export interface SuggestSummary {
  suggestions?: unknown[];
  excluded?: ExcludedRecipe[] | null;
  excluded_total?: number | null;
  filtered_out?: number | null;
  checks?: string[] | null;
  eating?: string[] | null;
  /** True for a housesitter: likes, dislikes and diets were not applied. */
  limited?: boolean | null;
}

/** One reason a recipe was left out, attributed to a person and a cause. */
export interface ExclusionReason {
  /** The recipe's name. */
  recipe: string;
  /** Who the rule belongs to ("Nora"), or "" when the server didn't say. */
  person: string;
  /**
   * What kind of rule: "allergy" | "diet" | "unknown". A diet reason carries
   * the literal "diet: " prefix in the server's own text.
   */
  kind: "allergy" | "diet" | "unknown";
  /** The allergen or diet itself ("peanuts", "vegetarian"). */
  rule: string;
  /** What in the recipe matched ("peanut butter"), or the whole reason text. */
  matched: string;
}

const REASON_RE = /^([^()]+?)\s*\(([^)]*)\)\s*:\s*(.+)$/;

/**
 * Splits the server's `reason` string into structured parts. The documented
 * shape is "Person (allergy): ingredient", with diets written "Person (diet:
 * vegetarian): chicken". Anything unrecognised keeps its whole text in
 * `matched` so no reason is ever dropped or reworded.
 */
export function parseExclusionReason(recipe: string, reason: string): ExclusionReason[] {
  const text = String(reason ?? "").trim();
  if (text === "") return [{ recipe, person: "", kind: "unknown", rule: "", matched: "" }];
  return text.split(";").map((part) => {
    const piece = part.trim();
    const m = REASON_RE.exec(piece);
    if (!m) return { recipe, person: "", kind: "unknown" as const, rule: "", matched: piece };
    const person = m[1].trim();
    const label = m[2].trim();
    const matched = m[3].trim();
    const diet = /^diet\s*:\s*(.+)$/i.exec(label);
    if (diet) return { recipe, person, kind: "diet" as const, rule: diet[1].trim(), matched };
    return { recipe, person, kind: "allergy" as const, rule: label, matched };
  });
}

/** Every exclusion, flattened, one entry per recipe-and-rule. */
export function exclusionReasons(summary: SuggestSummary | null | undefined): ExclusionReason[] {
  const excluded = Array.isArray(summary?.excluded) ? summary!.excluded! : [];
  return excluded.flatMap((e) => parseExclusionReason(String(e?.name ?? "A recipe"), String(e?.reason ?? "")));
}

/**
 * "Nora (peanuts) ruled out 4 recipes" groups — what the empty state lists
 * first, because the person's rule is the explanation the parent needs. Sorted
 * by how much each rule excluded, then by name, so the biggest cause leads.
 */
export interface ExclusionGroup {
  person: string;
  kind: "allergy" | "diet" | "unknown";
  rule: string;
  /** The recipe names this rule excluded, in server order, de-duplicated. */
  recipes: string[];
}

export function groupExclusions(reasons: readonly ExclusionReason[]): ExclusionGroup[] {
  const groups = new Map<string, ExclusionGroup>();
  for (const r of reasons) {
    const key = `${r.person}\u0000${r.kind}\u0000${r.rule}`;
    const found = groups.get(key);
    if (found) {
      if (!found.recipes.includes(r.recipe)) found.recipes.push(r.recipe);
    } else {
      groups.set(key, { person: r.person, kind: r.kind, rule: r.rule, recipes: [r.recipe] });
    }
  }
  return Array.from(groups.values()).sort(
    (a, b) => b.recipes.length - a.recipes.length || a.person.localeCompare(b.person) || a.rule.localeCompare(b.rule),
  );
}

/** The headline for a group: "Nora's peanut allergy", "Sam's vegetarian diet". */
export function groupLabel(group: ExclusionGroup): string {
  const who = group.person || "A family rule";
  if (group.kind === "allergy") return group.rule ? `${who}'s ${group.rule} allergy` : `${who}'s allergy`;
  if (group.kind === "diet") return group.rule ? `${who}'s ${group.rule} diet` : `${who}'s diet`;
  return `${who}'s rule`;
}

export type EmptyKind =
  /** No suggestions at all. */
  | "none"
  /** Some suggestions, but fewer than asked for because recipes were excluded. */
  | "few"
  /** No suggestions and no exclusions either: the family has no recipes yet. */
  | "no-recipes"
  /** No suggestions because filters (time, tag) removed everything. */
  | "filtered";

export interface EmptyExplanation {
  kind: EmptyKind;
  /** Short headline: why the list looks like this. */
  headline: string;
  /** One or two sentences a parent can act on. */
  body: string;
  /** The rules that did the excluding, biggest first. Empty when none. */
  groups: ExclusionGroup[];
  /** Total recipes excluded (the server's count, which may exceed the list). */
  excludedTotal: number;
  /** How many were excluded but not named (`excluded` is capped at 20). */
  excludedHidden: number;
  /** Verbatim `checks` lines: rules that could not be applied at all. */
  checks: string[];
  /** True when the answer must not read as "nothing is safe, ever". */
  reassuring: boolean;
}

/**
 * Explains a meal_suggest result. `requested` is the count the caller asked
 * for (default 5 on the server), so "few" can be told from "none".
 */
export function explainSuggestions(
  summary: SuggestSummary | null | undefined,
  requested = 5,
): EmptyExplanation {
  const suggestions = Array.isArray(summary?.suggestions) ? summary!.suggestions! : [];
  const reasons = exclusionReasons(summary);
  const groups = groupExclusions(reasons);
  const excludedTotal = Number(summary?.excluded_total ?? reasons.length) || 0;
  const namedRecipes = new Set(reasons.map((r) => r.recipe)).size;
  const excludedHidden = Math.max(0, excludedTotal - namedRecipes);
  const checks = (Array.isArray(summary?.checks) ? summary!.checks! : [])
    .map((c) => String(c).trim())
    .filter((c) => c !== "");
  const filteredOut = Number(summary?.filtered_out ?? 0) || 0;

  const base = { groups, excludedTotal, excludedHidden, checks };

  if (suggestions.length > 0) {
    // "few" is only interesting when it is fewer than were asked for.
    const short = suggestions.length < requested;
    return {
      ...base,
      kind: "few",
      headline:
        excludedTotal > 0 && short
          ? `${suggestions.length} of ${requested} ideas — ${excludedTotal} recipe${excludedTotal === 1 ? "" : "s"} left out for safety`
          : excludedTotal > 0
            ? `${excludedTotal} recipe${excludedTotal === 1 ? "" : "s"} left out for safety`
            : "",
      body: "",
      reassuring: true,
    };
  }

  // Nothing suggested. Why?
  if (excludedTotal > 0) {
    const top = groups[0];
    const lead = top ? groupLabel(top) : "An allergy or diet rule";
    return {
      ...base,
      kind: "none",
      headline: "No ideas left after allergy checks",
      body:
        `Every recipe this could pick was excluded by a rule for someone eating: ${lead}` +
        (groups.length > 1 ? ` and ${groups.length - 1} more rule${groups.length - 2 === 0 ? "" : "s"}` : "") +
        ". That is the safety rule working, not a failure — nothing unsafe was offered. Add a recipe that is safe for everyone, or plan this night by hand.",
      reassuring: true,
    };
  }

  if (filteredOut > 0) {
    return {
      ...base,
      kind: "filtered",
      headline: "Nothing matched those filters",
      body: `${filteredOut} recipe${filteredOut === 1 ? "" : "s"} were left out by the time limit or the tag. Clear the filter to see more ideas.`,
      reassuring: true,
    };
  }

  return {
    ...base,
    kind: "no-recipes",
    headline: "No recipes to suggest yet",
    body: "Save a few of the dinners your family actually cooks and suggestions start from those. Add one by hand, or paste one in from a link.",
    reassuring: true,
  };
}
