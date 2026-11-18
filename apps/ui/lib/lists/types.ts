/**
 * The lists API's real response shapes (#4566).
 *
 * Copied from `apps/api/internal/lists/handler.go` (`listResponse`,
 * `itemResponse`) — not invented. Two fields deserve a note because the UI
 * must not assume them:
 *
 * - `checked_by` / `checked_by_name` are filled by a DB trigger from the
 *   request's actor (migration 066). They are null on a row that was checked
 *   before that migration, and on a row we have optimistically checked but the
 *   server has not answered for yet. So "Checked by Jacob, 6:12 PM" is only
 *   ever claimed from a field the server sent; otherwise the row says when,
 *   never who.
 * - `quantity` + `unit` are separate columns (not the mockup's single "6" or
 *   "2 lb" string), because the meal→grocery merge (#4500, #4568) has to add
 *   amounts up. The UI joins them for display and splits them on the way in.
 */

/** `lists_kind_check` (migration 046). The order is the mockup's kind order. */
export const LIST_KINDS = ["grocery", "packing", "chores", "todo", "shopping", "other"] as const;
export type ListKind = (typeof LIST_KINDS)[number];

export function isListKind(value: string): value is ListKind {
  return (LIST_KINDS as readonly string[]).includes(value);
}

/** One row of `GET /api/v1/lists` (listResponse, items omitted). */
export interface ListRow {
  id: string;
  household_id: string;
  name: string;
  kind: string;
  entity_type?: string | null;
  entity_id?: string | null;
  is_template: boolean;
  archived: boolean;
  item_count: number;
  open_count: number;
  created_at: string;
  updated_at: string;
}

/** One row of `GET /api/v1/lists/{id}/items` (itemResponse). */
export interface ListItem {
  id: string;
  list_id: string;
  text: string;
  quantity?: number | null;
  unit?: string | null;
  section?: string | null;
  checked: boolean;
  checked_at?: string | null;
  checked_by?: string | null;
  checked_by_name?: string | null;
  assignee_person_id?: string | null;
  due_date?: string | null;
  position: number;
  created_at: string;
  updated_at: string;
}

/** `GET /api/v1/lists/{id}` — the list with its items. */
export interface ListWithItems extends ListRow {
  items?: ListItem[];
}

/** The items feed: the merged rows plus the `as_of` to send back as `since`. */
export interface ItemsFeed {
  items: ListItem[];
  /**
   * The last `as_of` the server sent. `undefined` means "the next fetch is a
   * full one", which is also how the feed learns about deleted items (the
   * `since` feed does not report them — see the handler's Items comment).
   */
  as_of?: string;
}

/** The household's people, for chore assignees (GET /api/v1/people). */
export interface PersonRow {
  id: string;
  display_name: string;
  color?: string;
}
