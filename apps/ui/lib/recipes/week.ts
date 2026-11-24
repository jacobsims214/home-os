/**
 * The Meals week view's pure logic (#4568).
 *
 * Three jobs, all testable without React:
 *
 * 1. Turn a date into the seven rows of a week, in the household's calendar
 *    (never the browser's clock: a family in Auckland and a phone in Chicago
 *    must agree about which dinners belong to "this week").
 * 2. Group a range's meals into those rows: dinners are the rows the page
 *    shows, every other slot collapses into a count chip per day (the approved
 *    mockup, artifact #106 v3, draws exactly that).
 * 3. Say whether a planned meal's ingredients could be checked against the
 *    family's allergies. This is NOT an allergy matcher — the matching engine
 *    lives on the server (apps/mcp/internal/meals) and KB mcp/recipe-tools.md
 *    forbids a second list. It only answers "did the server tell us anything
 *    about this recipe", which is what lib/recipes/allergy.ts needs.
 *
 * No JSX and no imports beyond the sibling modules, so `node --test` loads it.
 */
// A VALUE import needs the extension for `node --test` (see the file header):
// "is this day still open" is answered by ONE shared rule, never re-compared here.
import { dayPosition, isTodayOrFutureDay } from "../today/days.ts";
import type { RecipeAllergyData } from "./allergy";
import type { Meal, Recipe } from "./types";

/** "YYYY-MM-DD" for a date, without going through UTC (which shifts a day). */
export function dateKey(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

/** Parses "YYYY-MM-DD" as a LOCAL date, so no timezone moves it. */
export function parseDateKey(key: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(key.trim());
  if (!m) return null;
  const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
  return Number.isNaN(d.getTime()) ? null : d;
}

/** Adds days to a "YYYY-MM-DD" key, across month and DST boundaries. */
export function addDays(key: string, days: number): string {
  const d = parseDateKey(key);
  if (!d) return key;
  d.setDate(d.getDate() + days);
  return dateKey(d);
}

/**
 * The Monday..Sunday week containing `key`. The family's week starts on Monday
 * (the mockup shows Mon-Sun, and the grocery range "this week" means the same
 * in meal_build_grocery_list).
 */
export function weekStart(key: string): string {
  const d = parseDateKey(key);
  if (!d) return key;
  const dow = d.getDay(); // 0 = Sunday
  const back = dow === 0 ? 6 : dow - 1;
  return addDays(key, -back);
}

export interface WeekDay {
  /** "YYYY-MM-DD". */
  date: string;
  /** "Mon". */
  dow: string;
  /** "16". */
  dayNum: string;
  /** "November 16" for the accessible label. */
  long: string;
  isToday: boolean;
  /**
   * The day is already gone (before the family's today). A past row is
   * history, not an invitation: the page never asks anyone to plan dinner
   * for it (#5305). Derived from the shared dayPosition rule, not a compare.
   */
  isPast: boolean;
  /** The dinner planned for this day, if any. */
  dinner: Meal | null;
  /** Meals in the other slots, for the "2 more" chip. */
  otherSlots: Meal[];
}

const DOW = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const MONTHS = [
  "January", "February", "March", "April", "May", "June",
  "July", "August", "September", "October", "November", "December",
];

/** The seven rows of the week starting at `start` ("YYYY-MM-DD", a Monday). */
export function buildWeek(start: string, meals: readonly Meal[], today: string): WeekDay[] {
  const byDate = new Map<string, Meal[]>();
  for (const m of meals) {
    const list = byDate.get(m.date);
    if (list) list.push(m);
    else byDate.set(m.date, [m]);
  }
  const out: WeekDay[] = [];
  for (let i = 0; i < 7; i++) {
    const date = addDays(start, i);
    const d = parseDateKey(date);
    const dayMeals = byDate.get(date) ?? [];
    // Dinner is the family's planned slot (the mockup's rows); breakfast,
    // lunch and snack collapse into a count.
    const dinner = dayMeals.find((m) => m.slot === "dinner") ?? null;
    const otherSlots = dayMeals
      .filter((m) => m.slot !== "dinner")
      .sort((a, b) => SLOT_ORDER.indexOf(a.slot) - SLOT_ORDER.indexOf(b.slot));
    out.push({
      date,
      dow: d ? DOW[d.getDay()] : "",
      dayNum: d ? String(d.getDate()) : "",
      long: d ? `${MONTHS[d.getMonth()]} ${d.getDate()}` : date,
      // Both flags come from the ONE day-position rule (lib/today/days.ts), so
      // "today" and "gone" can never disagree with each other or with another
      // page. A plain `date === today` here would be a second comparison to keep
      // in step, and #5305 is what happens when they drift.
      isToday: dayPosition(date, today) === "today",
      isPast: dayPosition(date, today) === "past",
      dinner,
      otherSlots,
    });
  }
  return out;
}

const SLOT_ORDER: readonly Meal["slot"][] = ["breakfast", "lunch", "dinner", "snack"];

/** "Nov 16 – 22" or "Nov 30 – Dec 6" for the week's heading. */
export function weekLabel(start: string): string {
  const a = parseDateKey(start);
  const b = parseDateKey(addDays(start, 6));
  if (!a || !b) return start;
  const short = (d: Date) => MONTHS[d.getMonth()].slice(0, 3);
  if (a.getMonth() === b.getMonth()) return `${short(a)} ${a.getDate()} – ${b.getDate()}`;
  return `${short(a)} ${a.getDate()} – ${short(b)} ${b.getDate()}`;
}

/** "This week", "Next week", or the plain range for any other week. */
export function weekHeading(start: string, today: string): string {
  const thisWeek = weekStart(today);
  if (start === thisWeek) return "This week";
  if (start === addWeek(thisWeek, 1)) return "Next week";
  if (start === addWeek(thisWeek, -1)) return "Last week";
  return weekLabel(start);
}

/** The start of the week `offset` weeks from `start`. */
export function addWeek(start: string, offset: number): string {
  return addDays(start, offset * 7);
}

/** How many dinners this week have a meal planned. */
export function plannedDinners(days: readonly WeekDay[]): number {
  return days.filter((d) => d.dinner !== null).length;
}

/**
 * The first dinner still worth planning: the earliest empty slot that is TODAY
 * or later, or null when every remaining day is planned (or the week is over).
 *
 * This is where #5305 was: the function returned the first empty row of the
 * whole Mon-Sun week, so on Wednesday it answered "Monday the 16th" — two days
 * gone — and the page asked the family to plan last Monday's dinner. "Still
 * open" is a question about the family's today, so it is answered by the shared
 * `isTodayOrFutureDay` rule (lib/today/days.ts) against `today`, which the
 * caller must have taken from `householdToday(timezone)`.
 *
 * A week entirely in the past therefore has no next empty day, which is the
 * truth: nothing in it can still be planned.
 */
export function firstEmptyDay(days: readonly WeekDay[], today: string): WeekDay | null {
  return days.find((d) => d.dinner === null && isTodayOrFutureDay(d.date, today)) ?? null;
}

/**
 * The first row that is today or later, for a control that needs *some* day of
 * the week when no dinner is open (the "What's for dinner?" sheet). Null only
 * when the whole week is past. Kept here so the page never reaches for `days[0]`
 * and lands on a gone Monday again.
 */
export function firstUpcomingDay(days: readonly WeekDay[], today: string): WeekDay | null {
  return days.find((d) => isTodayOrFutureDay(d.date, today)) ?? null;
}

/**
 * Whether the page can say anything about this meal's allergens.
 *
 * The REST API does not return `allergy_warnings` or `checks` yet (#4891 is the
 * follow-up), and the UI cannot call the MCP (it refuses `home-os-ui` tokens by
 * audience, #4692). So a planned meal's report comes out `not-checked` when the
 * family has an allergy saved — and that is the honest answer: nobody checked.
 * This helper exists so the page asks the question in ONE place instead of
 * scattering `meal.allergy_warnings === undefined` around the components.
 *
 * It reads the meal's recipe payload when the API starts sending one; until
 * then `undefined` means "the server said nothing", never "nothing found".
 */
export function mealAllergyPayload(
  meal: Meal | null,
  recipesById: ReadonlyMap<string, Pick<Recipe, "allergy_warnings" | "checks">>,
): RecipeAllergyData | null {
  if (!meal) return null;
  if (meal.recipe_id) {
    const recipe = recipesById.get(meal.recipe_id);
    if (recipe) return recipe;
  }
  // A free-text meal whose title matched a recipe: the API resolves that only
  // inside the grocery merge, so there is no payload to read. Absent means
  // "nobody looked", which reportAllergies turns into `not-checked`.
  return null;
}

/**
 * A meal's title for display. A free-text meal linked to a recipe keeps the
 * title the family typed (the API stores it), which is what the mockup shows.
 */
export function mealTitle(meal: Meal | null): string {
  return meal?.title?.trim() ?? "";
}

/**
 * "Linked to a recipe" when the meal carries a recipe_id, "" otherwise. The
 * grocery merge also counts a free-text title that exactly equals a recipe name
 * (mealIsRecipe), but only the server can decide that, so the page does not
 * claim it.
 */
export function mealRecipeLink(meal: Meal | null): string {
  return meal?.recipe_id ? "Linked to a recipe" : "";
}

/** The average of a dinner's ratings, for the stars on a row. */
export function dinnerRating(meal: Meal | null): { average: number; count: number } | null {
  const ratings = (meal?.feedback ?? []).map((f) => f.rating).filter((r): r is number => typeof r === "number" && r > 0);
  if (ratings.length === 0) return null;
  return { average: ratings.reduce((a, b) => a + b, 0) / ratings.length, count: ratings.length };
}
