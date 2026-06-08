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
  details: () => [...billKeys.all, "detail"] as const,
  detail: (id: string) => [...billKeys.details(), id] as const,
};

export const searchKeys = {
  all: ["search"] as const,
  results: (q: string, type?: string, propertyId?: string) =>
    [...searchKeys.all, q, type ?? "", propertyId ?? ""] as const,
};
