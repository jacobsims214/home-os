/**
 * The property tree (#4578): property → room → thing (the UI word for an
 * asset, per the People & House mockup, artifact #89).
 *
 * Pure data and pure functions only (no React, no "@/" imports) so
 * `node --test` can load it directly — see property-tree.test.mjs.
 *
 * Two rules the mockup and the domain model v2 both state:
 * - An asset with no room is shown under "Not in a room" rather than dropped,
 *   because a roof or a sump pump belongs to the house, not to a room.
 * - An asset with no property at all is "Family-owned things" on the House
 *   tab, and never appears on a property page.
 */

/** The subset of a room this module needs (matches RoomResponse). */
export interface TreeRoom {
  id: string;
  name: string;
  floor?: number | null;
  notes?: string | null;
}

/** The subset of an asset this module needs (matches Asset). */
export interface TreeAsset {
  id: string;
  name: string;
  room_id?: string | null;
  category?: string | null;
  manufacturer?: string | null;
  model?: string | null;
  warranty_expiry?: string | null;
}

/** The subset of a maintenance task this module needs (matches MaintenanceTask). */
export interface TreeTask {
  id: string;
  name: string;
  status: "pending" | "in_progress" | "done" | "skipped";
  due_date?: string | null;
  asset_id?: string | null;
}

export interface RoomWithThings {
  room: TreeRoom;
  things: TreeAsset[];
}

/** A task is open while it can still be done. Done and skipped are closed. */
export function isOpenTask(task: TreeTask): boolean {
  return task.status === "pending" || task.status === "in_progress";
}

/**
 * Open and past its due date. A closed task is never overdue.
 *
 * `today` is the FAMILY's civil date (#5316). This used to default to
 * `new Date()` and compare against `now.toISOString().slice(0, 10)` — the UTC
 * day of an instant — so it read a task due today as overdue for anyone east
 * of UTC, or a day late for anyone west of it. Two civil dates need no zone.
 */
export function isOverdue(task: TreeTask, today: string): boolean {
  if (!isOpenTask(task) || !task.due_date) return false;
  const due = civilKey(task.due_date);
  const now = civilKey(today);
  if (!due || !now) return false;
  // Two YYYY-MM-DD strings compare as dates as strings. No Date, no zone.
  return due < now;
}

/** The first ten characters of a civil date, or null when it is not one. */
function civilKey(value: string | null | undefined): string | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(value ?? "");
  return m ? `${m[1]}-${m[2]}-${m[3]}` : null;
}

/**
 * Rooms in the order they are given (the API returns them by name), each with
 * its things, plus the things that are not in any room. A thing whose room_id
 * points at a room of another property is treated as not-in-a-room here: the
 * property page never invents a room it does not have.
 */
export function groupByRoom(rooms: TreeRoom[], things: TreeAsset[]): {
  rooms: RoomWithThings[];
  notInARoom: TreeAsset[];
} {
  const known = new Set(rooms.map((r) => r.id));
  const byRoom = new Map<string, TreeAsset[]>();
  const notInARoom: TreeAsset[] = [];

  for (const thing of things) {
    const roomId = thing.room_id ?? null;
    if (!roomId || !known.has(roomId)) {
      notInARoom.push(thing);
      continue;
    }
    const list = byRoom.get(roomId);
    if (list) list.push(thing);
    else byRoom.set(roomId, [thing]);
  }

  return {
    rooms: rooms.map((room) => ({ room, things: byRoom.get(room.id) ?? [] })),
    notInARoom,
  };
}

/** How many of this room's things have an open, overdue task against them. */
export function roomOverdueCount(things: TreeAsset[], tasks: TreeTask[], today: string): number {
  if (things.length === 0 || tasks.length === 0) return 0;
  const ids = new Set(things.map((t) => t.id));
  return tasks.filter((t) => t.asset_id && ids.has(t.asset_id) && isOverdue(t, today)).length;
}

/** Every overdue task of the property, worst (earliest due) first. */
export function overdueTasks(tasks: TreeTask[], today: string): TreeTask[] {
  return tasks
    .filter((t) => isOverdue(t, today))
    .sort((a, b) => (a.due_date ?? "").localeCompare(b.due_date ?? ""));
}

/** "Carrier Infinity 59SC5 · 2019" — the line under a thing's name. */
export function thingSubtitle(thing: TreeAsset): string {
  return [thing.manufacturer, thing.model, thing.category].filter(Boolean).join(" · ");
}

/** "3 things", "1 thing" — the count line on a room card. */
export function thingsLabel(count: number): string {
  return `${count} ${count === 1 ? "thing" : "things"}`;
}

/**
 * The icon a room gets, by the words its name contains. Returns a key the
 * component maps to a Tabler icon, so this stays pure and testable.
 */
export function roomIconKey(name: string): string {
  const n = name.toLowerCase();
  if (/kitchen|pantry/.test(n)) return "cook";
  // "Bedroom", "Ellie's room", "Nursery": a room that belongs to someone.
  if (/bed|nursery|'s room|^room$|kids room/.test(n)) return "bed";
  if (/bath|shower|toilet|powder/.test(n)) return "drop";
  if (/garage|drive|parking|carport/.test(n)) return "car";
  if (/living|lounge|den|family room|sitting/.test(n)) return "sofa";
  if (/laundry|utility|boiler|furnace|mechanical/.test(n)) return "tool";
  if (/basement|attic|crawl|cellar|stairs/.test(n)) return "stairs";
  if (/yard|garden|garden|pool|deck|patio|lawn|outdoor|backyard/.test(n)) return "tree";
  if (/office|study|desk/.test(n)) return "bulb";
  if (/dining/.test(n)) return "cook";
  if (/storage|shed|closet/.test(n)) return "box";
  return "door";
}

/** The property_type keys the API stores, with the words the UI shows. */
export const PROPERTY_TYPE_LABELS: Record<string, string> = {
  single_family: "Single family",
  condo: "Condo",
  townhouse: "Townhouse",
  apartment: "Apartment",
  land: "Land",
  multi_family: "Multi-family",
  commercial: "Commercial",
};

/** "single_family" → "Single family"; an unknown value reads as itself. */
export function typeLabel(type: string | null | undefined): string | null {
  if (!type) return null;
  return PROPERTY_TYPE_LABELS[type] ?? type.replace(/_/g, " ");
}
