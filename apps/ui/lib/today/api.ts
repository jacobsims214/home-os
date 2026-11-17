"use client";

/**
 * The data layer Today reads (#4549). Every hook here calls an endpoint that
 * exists — nothing is invented; a section whose data has no endpoint is a
 * labelled gap on the page, never fake rows (see the page's own header).
 *
 * One query per source, each with its own cache key, so one failing source
 * shows one inline error card and leaves the rest of the page up (the mockup's
 * rule, and the cure for audit #74's root cause: a failed read must never
 * render as empty data).
 */
import { useQueries, useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { mealKeys, personKeys } from "@/lib/query-keys";
import type { AgendaEvent, ActivityRow, BillRow, ListRow, MaintenanceRow, MealRow, PersonRow } from "./agenda";
import { agendaRange } from "./agenda";
import type { AllergyWarning } from "@/lib/recipes/allergy";

export interface MyHousehold {
  id: string;
  name: string;
  timezone: string;
  role: string;
  is_default?: boolean;
  current?: boolean;
}

/** GET /api/v1/me/households — the household Today belongs to (timezone + role). */
export function useTodayHousehold() {
  return useQuery({
    queryKey: ["me", "households"],
    queryFn: () => apiFetch<{ data: MyHousehold[] }>("/api/v1/me/households"),
    staleTime: 5 * 60_000,
    retry: false,
  });
}

/** The entry the API resolves for this session: `current`, else the default one. */
export function currentHousehold(list: readonly MyHousehold[] | undefined): MyHousehold | null {
  if (!list || list.length === 0) return null;
  return list.find((h) => h.current) ?? list.find((h) => h.is_default) ?? list[0];
}

/**
 * GET /api/v1/calendars/events for today and tomorrow. Recurrences come back
 * expanded (`start`/`end` given), so a weekly drop-off is one row per day and
 * the row carries the series' RRULE for its label.
 *
 * Bill mirror events are in the response: the API's role policy filters them
 * only for roles that may not see bills, and Today needs to leave them out for
 * everyone (`isBillMirrorEvent`) because they are not bill STATE.
 */
export function useAgenda(today: string | null) {
  const range = today ? agendaRange(today) : null;
  return useQuery({
    queryKey: ["calendars", "events", "today", range?.start ?? "", range?.end ?? ""],
    queryFn: () =>
      apiFetch<{ data: AgendaEvent[] }>("/api/v1/calendars/events", {
        // The API expands a recurring series inside [start, end); a day's worth
        // of zone slack on each side keeps a late-evening event in range.
        params: { start: `${range!.start}T00:00:00Z`, end: `${range!.end}T00:00:00Z` },
      }),
    enabled: !!range,
    staleTime: 60_000,
    retry: false,
  });
}

/** GET /api/v1/bills — bill state (paid dates included). Never a calendar event. */
export function useBills(enabled = true) {
  return useQuery({
    queryKey: ["bills", "today"],
    queryFn: () => apiFetch<{ data: BillRow[] }>("/api/v1/bills"),
    enabled,
    staleTime: 60_000,
    retry: false,
  });
}

/** GET /api/v1/maintenance/tasks — upkeep. */
export function useMaintenanceTasks(enabled = true) {
  return useQuery({
    queryKey: ["maintenance", "tasks", "today"],
    queryFn: () => apiFetch<{ data: MaintenanceRow[] }>("/api/v1/maintenance/tasks"),
    enabled,
    staleTime: 60_000,
    retry: false,
  });
}

/** GET /api/v1/meal-plans?from&to — today's plan (dinner, and anything else planned). */
export function useTodayMeals(today: string | null) {
  return useQuery({
    queryKey: mealKeys.range(today ?? "", today ?? ""),
    queryFn: () => apiFetch<{ data: MealRow[] }>("/api/v1/meal-plans", { params: { from: today!, to: today! } }),
    enabled: !!today,
    staleTime: 60_000,
    retry: false,
  });
}

export interface RecipeRow {
  id: string;
  title: string;
  servings?: number | null;
  prep_minutes?: number | null;
  cook_minutes?: number | null;
  ingredients?: { name: string }[];
  /** Present only when the server checked allergies (see lib/recipes/types.ts). */
  allergy_warnings?: AllergyWarning[] | string[] | null;
  /** Things the server could not check; render verbatim. */
  checks?: string[] | null;
}

/** GET /api/v1/recipes/{id} — the dinner's recipe facts and its allergy payload. */
export function useRecipe(id: string | null | undefined) {
  return useQuery({
    queryKey: ["recipes", "detail", id ?? ""],
    queryFn: () => apiFetch<{ data: RecipeRow }>(`/api/v1/recipes/${id}`),
    enabled: !!id,
    staleTime: 60_000,
    retry: false,
  });
}

/** GET /api/v1/people — names, colours and allergies (allergy text is safety data). */
export function usePeople(enabled = true) {
  return useQuery({
    queryKey: personKeys.list(),
    queryFn: () => apiFetch<{ data: PersonRow[] }>("/api/v1/people"),
    enabled,
    staleTime: 60_000,
    retry: false,
  });
}

/** GET /api/v1/lists — open lists and their open-item counts. */
export function useLists(enabled = true) {
  return useQuery({
    queryKey: ["lists", "today"],
    queryFn: () => apiFetch<{ data: ListRow[] }>("/api/v1/lists"),
    enabled,
    staleTime: 60_000,
    retry: false,
  });
}

/** GET /api/v1/activity — the family change feed ("who added what"). */
export function useActivity(enabled = true) {
  return useQuery({
    queryKey: ["activity", "today"],
    queryFn: () => apiFetch<{ data: ActivityRow[] }>("/api/v1/activity", { params: { limit: 20 } }),
    enabled,
    staleTime: 30_000,
    retry: false,
  });
}

export interface ListItemRow {
  id: string;
  list_id: string;
  text: string;
  checked: boolean;
  checked_at?: string | null;
  checked_by_name?: string | null;
  assignee_person_id?: string | null;
  due_date?: string | null;
}

/** GET /api/v1/lists/{id}/items — one list's items (the chores/needs-you source). */
export function useListItems(listId: string | null) {
  return useQuery({
    queryKey: ["lists", "items", listId ?? ""],
    queryFn: () => apiFetch<{ data: ListItemRow[] }>(`/api/v1/lists/${listId}/items`),
    enabled: !!listId,
    staleTime: 30_000,
    retry: false,
  });
}

/**
 * The items of several lists at once, for "Needs you": that panel is the
 * VIEWER's own items across every list, and no endpoint returns "my items"
 * (there is no assignee filter on /lists), so it reads each list that has open
 * items. `ids` is capped by the caller — a family has a handful of lists, not
 * hundreds, and the panel only needs the ones with something open.
 */
export function useListItemsFor(ids: readonly string[]) {
  return useQueries({
    queries: ids.map((id) => ({
      queryKey: ["lists", "items", id],
      queryFn: () => apiFetch<{ data: ListItemRow[] }>(`/api/v1/lists/${id}/items`),
      staleTime: 30_000,
      retry: false,
    })),
    combine: (results) => ({
      items: results.flatMap((r) => r.data?.data ?? []),
      isLoading: results.some((r) => r.isLoading),
      error: results.find((r) => r.error)?.error ?? null,
      refetch: () => results.forEach((r) => r.refetch()),
    }),
  });
}

/** GET /api/v1/auth/me — the viewer's own id, for "You" in the feed. */
export function useMe() {
  return useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => apiFetch<{ id: string; name?: string; email?: string }>("/api/v1/auth/me"),
    staleTime: 10 * 60_000,
    retry: false,
  });
}
