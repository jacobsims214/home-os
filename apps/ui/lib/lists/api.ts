/**
 * TanStack Query hooks for the Lists pages (#4566).
 *
 * The endpoints are `apps/api/internal/lists/handler.go` (KB api/lists.md):
 *
 *   GET    /lists                  list rows with item_count / open_count
 *   GET    /lists?archived=all     every list, archived and templates included
 *   GET    /lists/{id}             one list (with its items)
 *   POST   /lists                  create (name, kind, template_id)
 *   PATCH  /lists/{id}             rename / re-subject / is_template
 *   POST   /lists/{id}/archive|unarchive
 *   GET    /lists/{id}/items       items; ?since= is an incremental feed
 *   POST   /lists/{id}/items       add items
 *   POST   /lists/{id}/items/check   {"item_ids": [...]}  batch, all or none
 *   POST   /lists/{id}/items/uncheck {"item_ids": [...]}
 *   DELETE /lists/{id}/items/{itemId}
 *
 * THREE THINGS THAT MATTER HERE, all of them the ticket's point:
 *
 * 1. THE CHECK-OFF IS OPTIMISTIC. A person standing in a supermarket aisle on
 *    one bar of signal must see the tick the instant they tap, not when the
 *    request lands. So `useCheckItems` writes the new row into the cache
 *    first (onMutate), and puts the previous rows back if the server refuses
 *    (onError) — never leaving a tick on screen that the server rejected.
 *    This is the same onMutate/onError/onSettled shape MaintenanceKanban
 *    (#4754) uses; there is no shared helper to reuse, so the shape is copied
 *    rather than a second abstraction invented for two callers.
 *
 * 2. TWO PHONES STAY IN SYNC by polling the incremental feed. `useItemsFeed`
 *    asks `?since=<as_of>` every few seconds, merges what comes back by id,
 *    and keeps the new `as_of`. The server's `as_of` overlaps backwards on
 *    purpose (feedOverlap, 5 s), so a write that commits just after a read is
 *    still delivered next time; merging by id makes the repeats harmless.
 *    Polling is faster (3 s) in store mode, where two people really are
 *    shopping the same list at once.
 *
 * 3. THE OPTIMISTIC ROW NEVER CLAIMS A NAME. `checked_by_name` is the
 *    server's (a DB trigger, migration 066) — the client does not know who
 *    the other phone is. An optimistic tick therefore sets only `checked` and
 *    `checked_at`; the row says "Checked just now" until the server answers
 *    with the name, and then it says "Checked by Jacob, 6:12 PM".
 */

import { useCallback, useRef } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";
import { listKeys } from "@/lib/query-keys";
import { applyCheck, applyOpenCount, mergeItems, openCountOf } from "./optimistic";
import type { ItemsFeed, ListItem, ListRow, ListWithItems, PersonRow } from "./types";

/**
 * Checks whose write is still in flight, by item id: {listId, checked}.
 *
 * THE POLL MUST NOT UNDO A TICK. The feed refetches every 3–5 s, and a
 * refetch that lands while a check is still on the wire returns the row as it
 * was — unchecked — which would flip the tick back off under the person's
 * finger. That is the exact flicker this ticket exists to prevent, and it is
 * invisible on a fast connection (the write wins the race) and constant on a
 * bad one. So every read re-applies the in-flight value after merging, and
 * only a settled mutation (success or rollback) clears it.
 *
 * A module-level Map, not React state: it is bookkeeping about requests, not
 * render data, and the feed's queryFn reads it without subscribing to it.
 */
const pendingChecks = new Map<string, boolean>();

/** Mark rows as pending and re-apply them onto a freshly read feed. */
function withPendingChecks(feed: ItemsFeed): ItemsFeed {
  if (pendingChecks.size === 0) return feed;
  return {
    ...feed,
    items: feed.items.map((item) => {
      const pending = pendingChecks.get(item.id);
      if (pending === undefined) return item;
      return pending
        ? { ...item, checked: true, checked_at: item.checked_at ?? new Date().toISOString() }
        : { ...item, checked: false, checked_at: null, checked_by: null, checked_by_name: null };
    }),
  };
}

/** How often an open list polls its feed. Store mode polls faster. */
export const POLL_MS = 5_000;
export const STORE_POLL_MS = 3_000;

// ── reads ────────────────────────────────────────────────────

/**
 * Every list in the household, archived and templates included, in one call.
 *
 * `archived=all` rather than three queries: the index needs the active lists,
 * the archived count for its chip AND the templates for "New list → Start
 * from", and three round trips on a bad signal is three chances to fail. The
 * API returns all of it from one indexed query.
 */
export function useAllLists() {
  return useQuery({
    queryKey: listKeys.allRows(),
    queryFn: () => apiFetch<{ data: ListRow[] }>("/api/v1/lists", { params: { archived: "all" } }),
    staleTime: 30_000,
    retry: false,
  });
}

/** One list, with its items (the first paint of a detail page). */
export function useList(id: string | null) {
  return useQuery({
    queryKey: listKeys.detail(id ?? ""),
    queryFn: () => apiFetch<{ data: ListWithItems }>(`/api/v1/lists/${id}`),
    enabled: !!id,
    // The feed owns freshness once the page is open; this is the first paint.
    staleTime: 10_000,
    retry: false,
  });
}

/** The household's people, for chore assignee names and colours. */
export function usePeople() {
  return useQuery({
    queryKey: listKeys.people(),
    queryFn: () => apiFetch<{ data: PersonRow[] }>("/api/v1/people"),
    staleTime: 5 * 60_000,
    retry: false,
  });
}

// ── the live feed ────────────────────────────────────────────

/**
 * The open list's items, kept fresh by polling `?since=<as_of>`.
 *
 * The cache value is an ItemsFeed (rows + as_of), not a bare array, because
 * the `as_of` is per-list server state that must survive between polls. See
 * optimistic.ts for the merge and the optimistic write, both pure and tested.
 *
 * `pollMs` is 0 to stop polling (an archived list, a role that may not check).
 */
export function useItemsFeed(listId: string | null, pollMs: number = POLL_MS) {
  return useQuery({
    queryKey: listKeys.feed(listId ?? ""),
    queryFn: async ({ client }): Promise<ItemsFeed> => {
      const cached = client.getQueryData<ItemsFeed>(listKeys.feed(listId!));
      const since = cached?.as_of;
      const res = await apiFetch<{ data: ListItem[]; as_of?: string }>(
        `/api/v1/lists/${listId}/items`,
        { params: since ? { since } : undefined },
      );
      const incoming = Array.isArray(res?.data) ? res.data : [];
      // No `since` means this was a full read, so the result IS the list —
      // which is also how a deleted row disappears (the since feed never
      // reports deletions; see the handler's Items comment).
      const merged = since ? mergeItems(cached?.items ?? [], incoming) : incoming;
      // A read landing mid-write must not undo the tick (see pendingChecks).
      return withPendingChecks({ items: merged, as_of: res?.as_of ?? cached?.as_of });
    },
    enabled: !!listId,
    // Poll while the page is open. React Query pauses interval polling when the
    // tab is hidden, which is what a phone in a pocket should do.
    refetchInterval: pollMs > 0 ? pollMs : false,
    // A refetch on focus would fight the interval; the interval is already the
    // shortest path back.
    refetchOnWindowFocus: false,
    staleTime: 0,
    retry: false,
  });
}

/** The previous rows, so onError can put them back exactly as they were. */
export interface CheckContext {
  itemsKey: readonly unknown[];
  previous: ItemsFeed | undefined;
}

// ── writes ───────────────────────────────────────────────────

interface CheckVars {
  listId: string;
  itemIds: string[];
  checked: boolean;
  /**
   * The rows as they were BEFORE any optimistic write. The batcher writes the
   * tick to the cache the instant it is tapped (so a 400 ms batching window is
   * never visible) and hands the pre-write snapshot here, so a refusal still
   * rolls back to the truth rather than to another optimistic guess.
   */
  previous?: ItemsFeed;
}

/**
 * Check or uncheck a batch of items, optimistically.
 *
 * The batch is sent as ONE request (`item_ids`), which is the API's shape and
 * also what makes a quick double-tap cheap: two taps inside 400 ms become one
 * round trip, and the server applies them all-or-none.
 */
export function useCheckItems() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ listId, itemIds, checked }: CheckVars) =>
      apiFetch<{ data: ListItem[] }>(`/api/v1/lists/${listId}/items/${checked ? "check" : "uncheck"}`, {
        method: "POST",
        body: { item_ids: itemIds },
      }),
    onMutate: async ({ listId, itemIds, checked, previous: snapshot }) => {
      const itemsKey = listKeys.feed(listId);
      await queryClient.cancelQueries({ queryKey: itemsKey });
      const previous = snapshot ?? queryClient.getQueryData<ItemsFeed>(itemsKey);
      for (const id of itemIds) pendingChecks.set(id, checked);
      const next = applyCheck(previous, itemIds, checked);
      if (next) {
        queryClient.setQueryData<ItemsFeed>(itemsKey, next);
        // The index's open counts move with the tap, not a poll later.
        queryClient.setQueryData<ListRow[]>(listKeys.allRows(), (rows) =>
          rows ? applyOpenCount(rows, listId, openCountOf(next.items)) : rows,
        );
      }
      return { itemsKey, previous } satisfies CheckContext;
    },
    onError: (_err, { listId, itemIds }, context) => {
      for (const id of itemIds) pendingChecks.delete(id);
      // The server refused. Put the row back the way it was rather than leaving
      // an optimistic lie on screen (the MaintenanceKanban rule).
      if (context?.previous) queryClient.setQueryData(context.itemsKey, context.previous);
      void queryClient.invalidateQueries({ queryKey: listKeys.allRows() });
      void queryClient.invalidateQueries({ queryKey: listKeys.feed(listId) });
    },
    onSuccess: (res, { listId, itemIds }) => {
      for (const id of itemIds) pendingChecks.delete(id);
      // Adopt the server's rows for the ones just written: that is how the
      // real checked_by_name / checked_at replace our optimistic guess.
      const rows = Array.isArray(res?.data) ? res.data : [];
      if (!rows.length) return;
      queryClient.setQueryData<ItemsFeed>(listKeys.feed(listId), (feed) => {
        if (!feed) return feed;
        const byId = new Map(rows.map((row) => [row.id, row]));
        return { ...feed, items: feed.items.map((item) => byId.get(item.id) ?? item) };
      });
    },
    // Not an invalidate: a poll landing mid-flight would overwrite the
    // optimistic row with the old one and the tick would flicker back off.
  });
}

/** Add one item (the quick-add box). Not optimistic: the row needs its id. */
export function useAddItem() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      listId,
      text,
      quantity,
      unit,
      section,
      assigneePersonId,
      dueDate,
    }: {
      listId: string;
      text: string;
      quantity?: number | null;
      unit?: string | null;
      section?: string | null;
      assigneePersonId?: string | null;
      dueDate?: string | null;
    }) =>
      apiFetch<{ data: ListItem[] }>(`/api/v1/lists/${listId}/items`, {
        method: "POST",
        body: {
          items: [
            {
              text,
              quantity: quantity ?? null,
              unit: unit ?? null,
              section: section ?? null,
              assignee_person_id: assigneePersonId ?? null,
              due_date: dueDate ?? null,
            },
          ],
        },
      }),
    onSuccess: (res, { listId }) => {
      // Append what the server stored (it may have coerced a section), so the
      // new row is on screen before the next poll.
      const rows = Array.isArray(res?.data) ? res.data : [];
      if (rows.length) {
        queryClient.setQueryData<ItemsFeed>(listKeys.feed(listId), (feed) =>
          feed ? { items: mergeItems(feed.items, rows), as_of: feed.as_of } : feed,
        );
      }
      void queryClient.invalidateQueries({ queryKey: listKeys.allRows() });
    },
  });
}

/** Remove an item (the "Remove" in an item's editor). */
export function useRemoveItem() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ listId, itemId }: { listId: string; itemId: string }) =>
      apiFetch<void>(`/api/v1/lists/${listId}/items/${itemId}`, { method: "DELETE" }),
    onMutate: async ({ listId, itemId }) => {
      const itemsKey = listKeys.feed(listId);
      await queryClient.cancelQueries({ queryKey: itemsKey });
      const previous = queryClient.getQueryData<ItemsFeed>(itemsKey);
      if (previous) {
        queryClient.setQueryData<ItemsFeed>(itemsKey, {
          ...previous,
          items: previous.items.filter((item) => item.id !== itemId),
        });
      }
      return { itemsKey, previous } satisfies CheckContext;
    },
    onError: (_err, _vars, context) => {
      if (context?.previous) queryClient.setQueryData(context.itemsKey, context.previous);
    },
    onSettled: (_res, _err, { listId }) => {
      void queryClient.invalidateQueries({ queryKey: listKeys.feed(listId) });
      void queryClient.invalidateQueries({ queryKey: listKeys.allRows() });
    },
  });
}

/** Create a list, optionally from a template (the API copies its items unchecked). */
export function useCreateList() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      name,
      kind,
      templateId,
    }: {
      name: string;
      kind: string;
      templateId?: string | null;
    }) =>
      apiFetch<{ data: ListWithItems }>("/api/v1/lists", {
        method: "POST",
        body: { name, kind, template_id: templateId ?? null },
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: listKeys.allRows() });
    },
  });
}

/** Rename, archive, unarchive or mark-as-template. One PATCH or one POST. */
export function useUpdateList() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      listId,
      patch,
    }: {
      listId: string;
      patch: { name?: string; is_template?: boolean; archived?: boolean };
    }) => {
      if (patch.archived !== undefined) {
        return apiFetch<{ data: ListRow }>(
          `/api/v1/lists/${listId}/${patch.archived ? "archive" : "unarchive"}`,
          { method: "POST" },
        );
      }
      return apiFetch<{ data: ListRow }>(`/api/v1/lists/${listId}`, { method: "PATCH", body: patch });
    },
    onSuccess: (_res, { listId }) => {
      void queryClient.invalidateQueries({ queryKey: listKeys.allRows() });
      void queryClient.invalidateQueries({ queryKey: listKeys.detail(listId) });
    },
  });
}

/**
 * A 400 ms window that folds rapid taps on the same list into one write.
 *
 * Tapping three things in a row in an aisle should be one round trip on a bad
 * signal, not three. The tick is written to the cache IMMEDIATELY, before the
 * window opens, so batching is never visible to the person tapping; the
 * pre-write snapshot travels with the mutation so a refusal rolls back to the
 * server's truth.
 */
export function useCheckBatcher() {
  const client = useQueryClient();
  const check = useCheckItems();
  const pending = useRef<{
    listId: string;
    ids: Set<string>;
    checked: boolean;
    previous: ItemsFeed | undefined;
    timer: ReturnType<typeof setTimeout>;
  } | null>(null);

  return useCallback(
    (listId: string, itemId: string, checked: boolean) => {
      const key = listKeys.feed(listId);

      // Mark the row pending NOW, not when the batch flushes 400 ms later: a
      // poll can land inside that window, and without this the read would
      // revert the tick under the person's finger. This is the flicker the
      // ticket is about, and it only shows up on a slow connection.
      pendingChecks.set(itemId, checked);

      const current = pending.current;
      if (current && current.listId === listId && current.checked === checked) {
        // Same batch: the tick is already on screen, just remember the id.
        current.ids.add(itemId);
        const fed = applyCheck(client.getQueryData<ItemsFeed>(key), [itemId], checked);
        if (fed) client.setQueryData<ItemsFeed>(key, fed);
        return;
      }

      // A different direction (or list) starts a new batch: flush the old one
      // first, with the snapshot it captured.
      if (current) {
        clearTimeout(current.timer);
        pending.current = null;
        check.mutate({
          listId: current.listId,
          itemIds: Array.from(current.ids),
          checked: current.checked,
          previous: current.previous,
        });
      }

      const before = client.getQueryData<ItemsFeed>(key);
      const after = applyCheck(before, [itemId], checked);
      if (after) {
        client.setQueryData<ItemsFeed>(key, after);
        // The index's counts and the header's "13 left" move with the tap.
        client.setQueryData<ListRow[]>(listKeys.allRows(), (rows) =>
          rows ? applyOpenCount(rows, listId, openCountOf(after.items)) : rows,
        );
      }

      const ids = new Set([itemId]);
      const timer = setTimeout(() => {
        pending.current = null;
        check.mutate({ listId, itemIds: Array.from(ids), checked, previous: before });
      }, 400);
      pending.current = { listId, ids, checked, previous: before, timer };
    },
    [check, client],
  );
}
