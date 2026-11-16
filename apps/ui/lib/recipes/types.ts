/**
 * The recipe, meal and people shapes the Recipes pages read (#4567).
 *
 * These mirror the Go response structs (apps/api/internal/meals/handler.go
 * `recipeResponse`, `mealResponse`, `feedbackResponse`; people's
 * `personResponse`), NOT a hand-written ideal: every field name here is one
 * the API actually sends. See KB api/meals-recipes-meal-plans-feedback.md.
 *
 * `allergy_warnings` and `checks` are OPTIONAL because the REST API does not
 * return them today — the allergy engine lives in apps/mcp/internal/meals
 * (#4496/#4802–#4805/#4874) and the UI cannot call the MCP (it refuses
 * `home-os-ui` tokens by audience, #4692). Typing them as optional is what
 * lets lib/recipes/allergy.ts tell "checked and clear" apart from "nobody
 * checked", instead of assuming the best. When an API ticket adds them, the
 * pages pick them up with no other change.
 */
import type { Ingredient } from "./ingredients";
import type { AllergyWarning } from "./allergy";

/** GET /api/v1/recipes item and GET /api/v1/recipes/{id}. */
export interface Recipe {
  id: string;
  household_id: string;
  title: string;
  description?: string | null;
  servings?: number | null;
  prep_minutes?: number | null;
  cook_minutes?: number | null;
  ingredients: Ingredient[];
  /** ONE text column, one step per line (KB recipe-tools.md). */
  steps?: string | null;
  tags: string[];
  source_url?: string | null;
  created_at: string;
  updated_at: string;
  /** Present only when the server checked allergies. See this file's header. */
  allergy_warnings?: AllergyWarning[] | string[] | null;
  /** Things the server could not check (#4802/#4874). Render verbatim. */
  checks?: string[] | null;
}

/** POST /api/v1/recipes and PUT /api/v1/recipes/{id} body. */
export interface RecipeInput {
  title: string;
  description?: string | null;
  servings?: number | null;
  prep_minutes?: number | null;
  cook_minutes?: number | null;
  ingredients: Ingredient[];
  steps?: string | null;
  tags: string[];
  source_url?: string | null;
}

/** GET /api/v1/meals/{id}/feedback item. */
export interface MealFeedback {
  id: string;
  meal_plan_id: string;
  person_id?: string | null;
  rating?: number | null;
  comment?: string | null;
  created_at: string;
  updated_at: string;
}

/** GET /api/v1/meal-plans item: one planned meal in one slot. */
export interface Meal {
  id: string;
  household_id: string;
  date: string;
  slot: "breakfast" | "lunch" | "dinner" | "snack";
  recipe_id?: string | null;
  title: string;
  notes?: string | null;
  feedback: MealFeedback[];
  created_at: string;
  updated_at: string;
}

/** A household person, for the allergy context and the rating rows. */
export interface Person {
  id: string;
  household_id: string;
  user_id?: string | null;
  has_login?: boolean;
  display_name: string;
  relationship?: string | null;
  allergies: string[];
  notes?: string | null;
}

/** Response envelopes the handlers use. */
export interface DataEnvelope<T> {
  data: T;
}

export interface MealPlanResponse {
  data: Meal[];
  from: string;
  to: string;
}
