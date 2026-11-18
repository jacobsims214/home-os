/**
 * The optimistic check-off's pure half (#4566).
 *
 * Kept in its own file, with no React and no `@/` imports, so `node --test`
 * can load it directly and pin the behaviour that is invisible in a screenshot
 * (lib/lists/check.test.mjs). `api.ts` imports these and wires them into
 * TanStack Query.
 *
 * The rule these functions encode: the tick appears immediately, and the ONLY
 * thing an optimistic write may claim is `checked` + `checked_at`. A name
 * (`checked_by_name`) comes from the server — a DB trigger reads the request's
 * actor (migration 066) — because the client has no idea which of the family's
 * phones is tapping. An invented "Checked by Jacob" on the other parent's
 * phone would be a lie, and on a shared list a lie about who did a chore is a
 * domestic incident.
 */

import type { ListItem, ListRow } from "./types";

export interface ItemsFeedState {
  items: ListItem[];
  as_of?: string;
}

/**
 * Merge an incremental feed into the rows we hold.
 *
 * By id: a row the feed repeated (the server overlaps `since` by 5 s on
 * purpose, so a write committed just after a read still arrives) replaces
 * itself, a new row is added, and a row the feed did not mention is left alone.
 * Sorted by position so the screen order is the server's order.
 */
export function mergeItems(current: readonly ListItem[], incoming: readonly ListItem[]): ListItem[] {
  const byId = new Map(current.map((item) => [item.id, item]));
  for (const item of incoming) byId.set(item.id, item);
  return Array.from(byId.values()).sort((a, b) => a.position - b.position || a.created_at.localeCompare(b.created_at));
}

/**
 * Write a check/uncheck into the feed before the server answers.
 *
 * A check sets `checked_at` and explicitly NULLS `checked_by_name`, so the row
 * reads "Checked just now" until the server says who. An uncheck clears all
 * three, so no stale "Checked by Jacob" survives on a row that is back on the
 * list.
 */
export function applyCheck<T extends ItemsFeedState>(
  feed: T | undefined,
  itemIds: readonly string[],
  checked: boolean,
): T | undefined {
  if (!feed) return feed;
  const ids = new Set(itemIds);
  const now = new Date().toISOString();
  return {
    ...feed,
    items: feed.items.map((item) =>
      ids.has(item.id)
        ? checked
          ? { ...item, checked: true, checked_at: now, checked_by_name: null }
          : { ...item, checked: false, checked_at: null, checked_by: null, checked_by_name: null }
        : item,
    ),
  };
}

/** The list rows' open counts, adjusted so the index does not lag a tap. */
export function applyOpenCount(rows: readonly ListRow[], listId: string, openCount: number): ListRow[] {
  return rows.map((row) => (row.id === listId ? { ...row, open_count: openCount } : row));
}

/** How many rows are still open (what the header's "13 left" reads). */
export function openCountOf(items: readonly ListItem[]): number {
  return items.reduce((n, item) => (item.checked ? n : n + 1), 0);
}
