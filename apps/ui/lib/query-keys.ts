/**
 * TanStack Query key factories for all Home OS domains.
 *
 * Each domain exports a key factory object with:
 * - `all` — the base key for the domain (for invalidation)
 * - `lists()` / `detail()` / etc. — specific query keys
 *
 * Usage:
 *   useQuery({ queryKey: propertyKeys.list(householdId), queryFn: ... })
 *   queryClient.invalidateQueries({ queryKey: propertyKeys.all })
 */

export const authKeys = {
  all: ["auth"] as const,
  me: () => [...authKeys.all, "me"] as const,
};

export const propertyKeys = {
  all: ["properties"] as const,
  lists: () => [...propertyKeys.all, "list"] as const,
  list: (householdId: string) =>
    [...propertyKeys.lists(), householdId] as const,
  details: () => [...propertyKeys.all, "detail"] as const,
  detail: (id: string) => [...propertyKeys.details(), id] as const,
};

export const assetKeys = {
  all: ["assets"] as const,
  lists: () => [...assetKeys.all, "list"] as const,
  list: (householdId: string) => [...assetKeys.lists(), householdId] as const,
  details: () => [...assetKeys.all, "detail"] as const,
  detail: (id: string) => [...assetKeys.details(), id] as const,
  byProperty: (propertyId: string) =>
    [...assetKeys.all, "byProperty", propertyId] as const,
};

export const maintenanceKeys = {
  all: ["maintenance"] as const,
  lists: () => [...maintenanceKeys.all, "list"] as const,
  list: (householdId: string) =>
    [...maintenanceKeys.lists(), householdId] as const,
  details: () => [...maintenanceKeys.all, "detail"] as const,
  detail: (id: string) => [...maintenanceKeys.details(), id] as const,
  byProperty: (propertyId: string) =>
    [...maintenanceKeys.all, "byProperty", propertyId] as const,
};

export const vehicleKeys = {
  all: ["vehicles"] as const,
  lists: () => [...vehicleKeys.all, "list"] as const,
  list: (householdId: string) =>
    [...vehicleKeys.lists(), householdId] as const,
  details: () => [...vehicleKeys.all, "detail"] as const,
  detail: (id: string) => [...vehicleKeys.details(), id] as const,
};

export const petKeys = {
  all: ["pets"] as const,
  lists: () => [...petKeys.all, "list"] as const,
  list: (householdId: string) => [...petKeys.lists(), householdId] as const,
  details: () => [...petKeys.all, "detail"] as const,
  detail: (id: string) => [...petKeys.details(), id] as const,
};

export const vendorKeys = {
  all: ["vendors"] as const,
  lists: () => [...vendorKeys.all, "list"] as const,
  list: (householdId: string) =>
    [...vendorKeys.lists(), householdId] as const,
  details: () => [...vendorKeys.all, "detail"] as const,
  detail: (id: string) => [...vendorKeys.details(), id] as const,
};

export const billKeys = {
  all: ["bills"] as const,
  lists: () => [...billKeys.all, "list"] as const,
  list: (householdId: string) => [...billKeys.lists(), householdId] as const,
  byProperty: (propertyId: string) =>
    [...billKeys.all, "byProperty", propertyId] as const,
  details: () => [...billKeys.all, "detail"] as const,
  detail: (id: string) => [...billKeys.details(), id] as const,
};

export const searchKeys = {
  all: ["search"] as const,
  results: (q: string, type?: string, propertyId?: string) =>
    [...searchKeys.all, q, type ?? "", propertyId ?? ""] as const,
};

export const calendarKeys = {
	all: ["calendars"] as const,
	lists: () => [...calendarKeys.all, "list"] as const,
	events: () => [...calendarKeys.all, "events"] as const,
	eventsByProperty: (propertyId: string) =>
		[...calendarKeys.events(), "byProperty", propertyId] as const,
};

export const loanKeys = {
	all: ["loans"] as const,
	lists: () => [...loanKeys.all, "list"] as const,
	list: (householdId: string) => [...loanKeys.lists(), householdId] as const,
	details: () => [...loanKeys.all, "detail"] as const,
	detail: (id: string) => [...loanKeys.details(), id] as const,
};

export const fileKeys = {
  all: ["files"] as const,
  lists: () => [...fileKeys.all, "list"] as const,
  listByEntity: (entityType: string, entityId: string) =>
    [...fileKeys.lists(), entityType, entityId] as const,
  details: () => [...fileKeys.all, "detail"] as const,
  detail: (id: string) => [...fileKeys.details(), id] as const,
};

export const secretKeys = {
  all: ["secrets"] as const,
  lists: () => [...secretKeys.all, "list"] as const,
  listByEntity: (entityType: string, entityId: string) =>
    [...secretKeys.lists(), entityType, entityId] as const,
  details: () => [...secretKeys.all, "detail"] as const,
  detail: (id: string) => [...secretKeys.details(), id] as const,
};

/**
 * Recipes (#4567). The list key carries the SERVER-side filters (q, tag)
 * because the API filters, not the client: two different searches are two
 * different cached lists, and invalidating `recipeKeys.all` refreshes both.
 */
export const recipeKeys = {
  all: ["recipes"] as const,
  lists: () => [...recipeKeys.all, "list"] as const,
  list: (q = "", tag = "") => [...recipeKeys.lists(), q, tag] as const,
  details: () => [...recipeKeys.all, "detail"] as const,
  detail: (id: string) => [...recipeKeys.details(), id] as const,
};

/**
 * Meal plans and their ratings (#4567, and #4568's week view). The range is
 * part of the key so a week view and a "next planned" lookup don't fight over
 * one cache entry.
 */
export const mealKeys = {
  all: ["meals"] as const,
  ranges: () => [...mealKeys.all, "range"] as const,
  range: (from: string, to: string) => [...mealKeys.ranges(), from, to] as const,
  feedback: (mealId: string) => [...mealKeys.all, "feedback", mealId] as const,
};

/**
 * Lists (#4566). One key for the household's whole set of list rows (the index
 * and the template picker read the same `archived=all` response, so they share
 * a cache entry), one per list detail, and one per list's LIVE ITEM FEED.
 *
 * The feed key is deliberately not the detail key: the detail query is the
 * first paint and can be stale, while the feed is the polled truth that the
 * optimistic check-off writes into. Two keys means a poll can never clobber a
 * detail fetch that is still in flight.
 */
export const listKeys = {
  all: ["lists"] as const,
  allRows: () => [...listKeys.all, "rows"] as const,
  details: () => [...listKeys.all, "detail"] as const,
  detail: (id: string) => [...listKeys.details(), id] as const,
  // "feed", not "items": lib/today/api.ts owns ["lists","items",id] with a bare
  // ListItem[] value, and this entry is an ItemsFeed ({items, as_of}). Sharing
  // the key would hand Today a feed object where it expects an array.
  feed: (id: string) => [...listKeys.all, "feed", id] as const,
  people: () => [...listKeys.all, "people"] as const,
};

/** The household's people, including their allergies (#4567's safety context). */
export const personKeys = {
  all: ["people"] as const,
  lists: () => [...personKeys.all, "list"] as const,
  list: () => [...personKeys.lists()] as const,
  detail: (id: string) => [...personKeys.all, "detail", id] as const,
};
