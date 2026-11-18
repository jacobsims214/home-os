/**
 * TanStack Query hooks for the Recipes pages (#4567).
 *
 * Everything goes through `apiFetch` (lib/api.ts), which routes to the BFF and
 * sends a 401 to /login. The endpoints are the meals API from #4475
 * (KB api/meals-recipes-meal-plans-feedback.md):
 *
 *   GET    /recipes?q=&tag=      list, case-insensitive title substring
 *   POST   /recipes              create (structured ingredients)
 *   GET    /recipes/{id}         one recipe, by uuid OR exact name
 *   PUT    /recipes/{id}         REPLACE: omitted optional fields are cleared
 *   DELETE /recipes/{id}         keeps planned meals' titles, keeps feedback
 *   GET    /meal-plans?from&to   planned meals with their feedback
 *   GET    /meals/{id}/feedback  one meal's ratings
 *   PUT    /meals/{id}/feedback/{personId}   upsert one person's rating
 *   GET    /people               the household, INCLUDING allergies
 *
 * Two things worth knowing before changing these:
 * - PUT replaces, so the edit form always sends every field (see
 *   `toRecipeInput`), never a partial body. A partial PUT would silently wipe
 *   the description or the steps.
 * - `q` and `tag` are server-side filters, not client-side, so the list stays
 *   correct past the first page and matches recipe_find's own semantics.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { mealKeys, personKeys, recipeKeys } from "@/lib/query-keys";
import type { SuggestSummary } from "./suggestions";
import type {
  DataEnvelope,
  GroceryResult,
  Meal,
  MealFeedback,
  MealPlanResponse,
  Person,
  Recipe,
  RecipeInput,
} from "./types";

/** The household's people, with allergies. Every role may read allergies (safety data). */
export function usePeople() {
  return useQuery({
    queryKey: personKeys.list(),
    queryFn: () => apiFetch<DataEnvelope<Person[]>>("/api/v1/people"),
    staleTime: 60_000,
    retry: false,
  });
}

/** The recipe list. `q` and `tag` are passed to the server, not filtered here. */
export function useRecipes(filters: { q?: string; tag?: string } = {}) {
  const q = (filters.q ?? "").trim();
  const tag = (filters.tag ?? "").trim();
  return useQuery({
    queryKey: recipeKeys.list(q, tag),
    queryFn: () => apiFetch<DataEnvelope<Recipe[]>>("/api/v1/recipes", { params: { q: q || undefined, tag: tag || undefined } }),
    // Keep the previous list on screen while a new search runs, so the page
    // doesn't flash empty between keystrokes.
    placeholderData: (prev) => prev,
  });
}

/** One recipe by id (the API also accepts an exact name). */
export function useRecipe(id: string | undefined) {
  return useQuery({
    queryKey: recipeKeys.detail(id ?? ""),
    queryFn: () => apiFetch<DataEnvelope<Recipe>>(`/api/v1/recipes/${encodeURIComponent(id!)}`),
    enabled: !!id,
    retry: false,
  });
}

/** Create a recipe. Invalidates the list so the new row appears. */
export function useCreateRecipe() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: RecipeInput) => apiFetch<DataEnvelope<Recipe>>("/api/v1/recipes", { method: "POST", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: recipeKeys.all });
    },
  });
}

/** Replace a recipe (PUT replaces the whole record — send every field). */
export function useUpdateRecipe(id: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: RecipeInput) =>
      apiFetch<DataEnvelope<Recipe>>(`/api/v1/recipes/${encodeURIComponent(id)}`, { method: "PUT", body }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: recipeKeys.all });
      queryClient.invalidateQueries({ queryKey: recipeKeys.detail(id) });
    },
  });
}

/** Delete a recipe. Planned meals keep their title and feedback is kept. */
export function useDeleteRecipe() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/api/v1/recipes/${encodeURIComponent(id)}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: recipeKeys.all });
    },
  });
}

/**
 * Planned meals in a range, for "next planned" on the detail page. The API
 * needs both `from` and `to` as YYYY-MM-DD; without them it answers the next
 * 7 days, which is not what a "when did we last make this" view needs.
 */
export function useMealPlans(range: { from: string; to: string }, enabled = true) {
  return useQuery({
    queryKey: mealKeys.range(range.from, range.to),
    queryFn: () => apiFetch<MealPlanResponse>("/api/v1/meal-plans", { params: { from: range.from, to: range.to } }),
    enabled,
  });
}

/**
 * One meal's ratings. `feedback` is already embedded in the meal-plan rows, so
 * the detail page reads it from there and only this hook refetches it.
 */
export function useMealFeedback(mealId: string | undefined) {
  return useQuery({
    queryKey: mealKeys.feedback(mealId ?? ""),
    queryFn: () => apiFetch<DataEnvelope<MealFeedback[]>>(`/api/v1/meals/${encodeURIComponent(mealId!)}/feedback`),
    enabled: !!mealId,
    retry: false,
  });
}

/**
 * Rates one person's meal (one row per person per meal; PUT upserts). Owners
 * and family managers may rate for anyone — kids have no login, so a parent
 * rates for them; a family_member may only rate as their own person. The API
 * enforces that (meals.Policy), the UI only offers what the role can do.
 */
export function useRateMeal(mealId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ personId, rating, comment }: { personId: string; rating: number; comment?: string }) =>
      apiFetch<DataEnvelope<MealFeedback>>(`/api/v1/meals/${encodeURIComponent(mealId)}/feedback/${encodeURIComponent(personId)}`, {
        method: "PUT",
        body: { rating, comment: comment?.trim() || undefined },
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: mealKeys.all });
    },
  });
}

/** The newest planned meal for a recipe, or null. Used for "Next planned". */
export function nextPlannedFor(meals: readonly Meal[] | undefined, recipeId: string | undefined): Meal | null {
  if (!recipeId || !Array.isArray(meals)) return null;
  const hits = meals.filter((m) => m.recipe_id === recipeId).sort((a, b) => a.date.localeCompare(b.date));
  return hits[0] ?? null;
}

/** Average of a meal's ratings, or null when nobody rated it. */
export function averageRating(feedback: readonly MealFeedback[] | undefined): { average: number; count: number } | null {
  const ratings = (feedback ?? []).map((f) => f.rating).filter((r): r is number => typeof r === "number" && r > 0);
  if (ratings.length === 0) return null;
  return { average: ratings.reduce((a, b) => a + b, 0) / ratings.length, count: ratings.length };
}

/**
 * "Plan this recipe on a night" — PUT /meal-plans/{date}/{slot} upserts the
 * slot and keeps feedback already on it (KB api/meals-recipes-meal-plans-feedback.md).
 * The week view's suggestion sheet and its empty slots both use this.
 */
export function usePlanMeal() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ date, slot, recipeId, title, notes }: {
      date: string;
      slot: Meal["slot"];
      recipeId?: string;
      title?: string;
      notes?: string;
    }) =>
      apiFetch<DataEnvelope<Meal>>(`/api/v1/meal-plans/${encodeURIComponent(date)}/${encodeURIComponent(slot)}`, {
        method: "PUT",
        body: { recipe_id: recipeId, title, notes: notes?.trim() || undefined },
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: mealKeys.all });
    },
  });
}

/**
 * Clear a night: DELETE /meal-plans/{date}/{slot} is idempotent (204 even when
 * the slot is already empty), so a second tap is harmless.
 */
export function useClearMeal() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ date, slot }: { date: string; slot: Meal["slot"] }) =>
      apiFetch<void>(`/api/v1/meal-plans/${encodeURIComponent(date)}/${encodeURIComponent(slot)}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: mealKeys.all });
    },
  });
}

/**
 * Dinner suggestions for one date and slot — meal_suggest, over REST.
 *
 * THERE IS NO SUCH ENDPOINT YET. meal_suggest ships as an MCP tool (#4496) and
 * the UI cannot call the MCP (it refuses `home-os-ui` tokens by audience,
 * #4692), so GET /meal-plans/suggestions answers 404 today. This hook asks
 * anyway, and `retry: false` plus a 404 → null mapping means the Meals page
 * degrades to its recipe box with an explicit note instead of an error box.
 *
 * Why ask at all: the day the API gains the endpoint (the follow-up to #4891,
 * which adds the same fields to /recipes), the sheet's ranked list and its
 * SuggestionEmptyState light up with NO page change — and the e2e can prove
 * that path now by stubbing the endpoint with meal_suggest's real payload
 * shape. A component that has never been driven by a payload is not tested.
 */
export interface SuggestionQuery {
  date: string;
  slot: Meal["slot"];
}

export function useMealSuggestions(query: SuggestionQuery | null) {
  return useQuery({
    queryKey: [...mealKeys.all, "suggest", query?.date ?? "", query?.slot ?? ""],
    queryFn: async () => {
      try {
        return await apiFetch<DataEnvelope<SuggestSummary>>("/api/v1/meal-plans/suggestions", {
          params: { date: query!.date, slot: query!.slot },
          redirectOn401: false,
        });
      } catch (err) {
        // 404 = this Home OS build has no suggestions endpoint. That is stated
        // on the page, not shown as a failure, and it must not send anyone to
        // /login: it is not an auth problem.
        if ((err as { status?: number })?.status === 404) return null;
        throw err;
      }
    },
    enabled: !!query,
    retry: false,
    staleTime: 60_000,
  });
}

/** The grocery range the week view asks for: the week's dinners. */
export interface GroceryRange {
  from: string;
  to: string;
  slots?: string[];
  listId?: string;
}

function groceryParams(range: GroceryRange): Record<string, string> {
  const params: Record<string, string> = { from: range.from, to: range.to };
  if (range.slots && range.slots.length > 0) params.slots = range.slots.join(",");
  if (range.listId) params.list_id = range.listId;
  return params;
}

/**
 * The grocery merge PREVIEW: what "Add dinners to Groceries" would do. Writes
 * nothing (the endpoint runs the merge in a rolled-back transaction), so it is
 * safe to load the moment the preview page opens — including while someone
 * else is checking items off the same list in store mode.
 */
export function useGroceryPreview(range: GroceryRange, enabled = true) {
  return useQuery({
    queryKey: [...mealKeys.all, "grocery", "preview", range.from, range.to, (range.slots ?? []).join(","), range.listId ?? ""],
    queryFn: () => apiFetch<DataEnvelope<GroceryResult>>("/api/v1/meal-plans/grocery", { params: groceryParams(range) }),
    enabled: enabled && !!range.from && !!range.to,
    // A preview is a snapshot of a shared list: refetching it under the user
    // while they read it would change the numbers under their thumb.
    staleTime: 30_000,
    refetchOnWindowFocus: false,
  });
}

/**
 * The grocery merge APPLY. Idempotent server-side (an item whose words are
 * already open on the list is not added again), so a double tap cannot
 * duplicate the week — and the response says what really happened per row.
 */
export function useApplyGrocery() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (range: GroceryRange) =>
      apiFetch<DataEnvelope<GroceryResult>>("/api/v1/meal-plans/grocery", {
        method: "POST",
        params: groceryParams(range),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [...mealKeys.all, "grocery"] });
    },
  });
}

/**
 * Existing items on a list, for the recipe detail page's "on the plan" idea
 * applied to grocery. Kept here rather than a new lists client: the Meals page
 * is the only reader today.
 */
export interface GroceryListItem {
  id: string;
  text: string;
  quantity?: number | null;
  unit?: string | null;
  checked: boolean;
}

export function useListItems(listId: string | undefined) {
  return useQuery({
    queryKey: [...mealKeys.all, "list-items", listId ?? ""],
    queryFn: () => apiFetch<DataEnvelope<GroceryListItem[]>>(`/api/v1/lists/${encodeURIComponent(listId!)}/items`),
    enabled: !!listId,
    retry: false,
  });
}
