"use client";

/**
 * The record behind a search hit (#4561).
 *
 * `GET /api/v1/search` answers with title + body only — no date, amount or
 * owner (apps/api/internal/search/typesense.go:118). A list of bare titles is
 * what the ticket forbids, so each row is completed from ITS OWN record: the
 * detail route that already exists per type. Nothing is derived or guessed;
 * when a read fails the row shows its snippet and no facts.
 *
 * Why per type and not one batch: there is one search endpoint and one detail
 * route per entity; there is no `/search/expand`. The reads are small, they
 * happen only for the rows actually drawn (the role's groups, the collapsed
 * page of each), and TanStack Query caches them by (type, id), so typing more
 * letters in the same search costs nothing for rows already seen.
 *
 * Two types have no single-record route (upkeep tasks and credentials), so
 * they are read once as a list and indexed by id. A credential's ciphertext is
 * never part of this: only name and type are shown, which is what the index
 * holds too (the secret handler's own rule).
 */

import { useMemo } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { MY_HOUSEHOLDS_QUERY_KEY } from "@/components/shared/use-nav-role";
import { currentHousehold, type MyHousehold } from "@/lib/today/api";
import { householdToday } from "@/lib/today/agenda";
import { canSeeEntity } from "@/lib/resource-access";
import type { FactContext, Group, RecordFacts, SearchRole } from "./results";

/** Rows fetched at once, per group, before "Show all". */
export const COLLAPSED_ROWS = 5;

/** One record to read. `id` is the record's own id (`file_id` for documents). */
export interface Target {
  entityType: string;
  id: string;
}

/** Types with a single-record GET, and the path that answers it. */
const ITEM_PATHS: Readonly<Record<string, (id: string) => string>> = {
  bill: (id) => `/api/v1/bills/${id}`,
  loan: (id) => `/api/v1/loans/${id}`,
  vendor: (id) => `/api/v1/vendors/${id}`,
  asset: (id) => `/api/v1/assets/${id}`,
  vehicle: (id) => `/api/v1/vehicles/${id}`,
  pet: (id) => `/api/v1/pets/${id}`,
  property: (id) => `/api/v1/properties/${id}`,
  recipe: (id) => `/api/v1/recipes/${id}`,
  guide: (id) => `/api/v1/guides/${id}`,
  list: (id) => `/api/v1/lists/${id}`,
  note: (id) => `/api/v1/notes/${id}`,
  person: (id) => `/api/v1/people/${id}`,
  /** Documents: the metadata record, which carries category/date/uploader. */
  document: (id) => `/api/v1/files/${id}`,
};

/** Types read once as a list (no single-record route), indexed by id. */
const LIST_PATHS: Readonly<Record<string, string>> = {
  maintenance_task: "/api/v1/maintenance/tasks",
  maintenance_schedule: "/api/v1/maintenance/schedules",
  secret: "/api/v1/secrets",
};

/** Whether a row's facts can be completed at all (else: snippet only). */
export function canComplete(entityType: string): boolean {
  return entityType in ITEM_PATHS || entityType in LIST_PATHS;
}

/** The id the record is read by: a document is read by its file id. */
export function recordId(entityType: string, entityId: string, fileId?: string): string {
  return fileId || entityId;
}

/** The single-record path for a target, or null when it needs the list read. */
export function recordPath(entityType: string): ((id: string) => string) | null {
  return ITEM_PATHS[entityType] ?? null;
}

/** The list path for a target, or null when it has a single-record read. */
export function listPath(entityType: string): string | null {
  return LIST_PATHS[entityType] ?? null;
}

interface Rec {
  id?: string;
  [key: string]: unknown;
}

/**
 * What the page needs, one lookup per drawn row. Reads run in parallel and
 * fail independently: a row whose record couldn't be read keeps its title and
 * snippet and simply shows no facts.
 */
export function useRecordFacts(targets: readonly Target[]): ReadonlyMap<string, RecordFacts> {
  const itemTargets = useMemo(() => {
    const seen = new Set<string>();
    return targets.filter((t) => {
      if (!ITEM_PATHS[t.entityType]) return false;
      const key = `${t.entityType}:${t.id}`;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
  }, [targets]);

  const listTypes = useMemo(() => {
    const set = new Set<string>();
    for (const t of targets) if (LIST_PATHS[t.entityType]) set.add(t.entityType);
    return Array.from(set);
  }, [targets]);

  const itemQueries = useQueries({
    queries: itemTargets.map((t) => ({
      queryKey: ["search", "record", t.entityType, t.id] as const,
      queryFn: () => apiFetch<{ data: Rec }>(ITEM_PATHS[t.entityType](t.id)),
      staleTime: 60_000,
      retry: false,
    })),
  });

  const listQueries = useQueries({
    queries: listTypes.map((type) => ({
      queryKey: ["search", "records", type] as const,
      queryFn: () => apiFetch<{ data: Rec[] }>(LIST_PATHS[type]),
      staleTime: 60_000,
      retry: false,
    })),
  });

  return useMemo(() => {
    const out = new Map<string, RecordFacts>();
    itemTargets.forEach((t, i) => {
      const data = itemQueries[i]?.data?.data;
      // `data` is the record itself (the API's envelope). A 404/failure has
      // none, and the row then claims no facts.
      if (data && typeof data === "object") out.set(`${t.entityType}:${t.id}`, data as RecordFacts);
    });
    listTypes.forEach((type, i) => {
      const rows = listQueries[i]?.data?.data;
      if (!Array.isArray(rows)) return;
      for (const row of rows) {
        if (row && typeof row === "object" && typeof row.id === "string") {
          out.set(`${type}:${row.id}`, row as RecordFacts);
        }
      }
    });
    return out;
  }, [itemTargets, itemQueries, listTypes, listQueries]);
}

/**
 * Every fact a list of drawn groups needs: the records behind the rows shown,
 * plus the two shared lookups those facts are computed against (today, in the
 * household's timezone, and the property names that make "For: main house"
 * readable).
 *
 * Split out of the Search page for #5303 so the drawer's drop-down completes its
 * rows exactly the way the page does — one implementation of "a row carries
 * context", reached by both surfaces, and the same TanStack cache keys, so
 * opening the page after searching in the drawer costs no extra reads.
 *
 * `expanded` may be omitted by a surface that never expands groups (the
 * drawer's compact panel): then only the collapsed page of each group is read.
 */
export function useGroupFacts(
  groups: readonly Group[],
  role: SearchRole,
  expanded: ReadonlySet<string> = new Set(),
): { records: ReadonlyMap<string, RecordFacts>; ctx: FactContext } {
  const targets = useMemo<Target[]>(() => {
    const out: Target[] = [];
    for (const g of groups) {
      const rows = expanded.has(g.def.id) ? g.hits : g.hits.slice(0, COLLAPSED_ROWS);
      for (const { hit } of rows) {
        out.push({ entityType: hit.entity_type, id: recordId(hit.entity_type, hit.entity_id, hit.file_id) });
      }
    }
    return out;
  }, [groups, expanded]);

  const records = useRecordFacts(targets);
  const today = useHouseholdToday();
  const wantsProperties = useMemo(() => groups.some((g) => ["bill", "thing", "upkeep"].includes(g.def.id)), [groups]);
  const propertyNames = usePropertyNames(wantsProperties && canSeeEntity(role, "property"));
  const ctx = useMemo<FactContext>(() => ({ today, propertyNames }), [today, propertyNames]);
  return { records, ctx };
}

/** The role's `today` in the household's timezone (bills are dated there). */
export function useHouseholdToday(): string | null {
  const q = useQuery({
    queryKey: MY_HOUSEHOLDS_QUERY_KEY,
    queryFn: () => apiFetch<{ data: MyHousehold[] }>("/api/v1/me/households"),
    staleTime: 5 * 60_000,
    retry: false,
  });
  const hh = currentHousehold(q.data?.data);
  return hh ? householdToday(hh.timezone) : null;
}

/**
 * property id → name, for the rows that say whose thing it is ("For: Main
 * house"). Read only when a group that can use it is on screen, and only for a
 * role the API would let read properties anyway; a failure is not an error
 * here (the facts that need it are simply absent).
 */
export function usePropertyNames(enabled: boolean): ReadonlyMap<string, string> {
  const q = useQuery({
    queryKey: ["search", "properties"],
    queryFn: () => apiFetch<{ data: { id: string; name: string }[] }>("/api/v1/properties"),
    enabled,
    staleTime: 5 * 60_000,
    retry: false,
  });
  return useMemo(() => {
    const map = new Map<string, string>();
    for (const p of q.data?.data ?? []) if (p?.id && p?.name) map.set(p.id, p.name);
    return map;
  }, [q.data]);
}
