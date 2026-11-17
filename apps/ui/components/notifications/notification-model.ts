/**
 * Pure helpers for the notifications surface (#4563). No React and no browser
 * APIs, so `node --test` loads this file directly (notification-model.test.mjs).
 *
 * Why the UI parses the title at all: the REST API returns a notification as
 * `{id, title, body, entity_type, entity_id, read_at, created_at}` — there is
 * no `actor` field. The actor is baked into the title by the database writer
 * that creates the row (migrations 054/061/070, `notify_entity_creator`):
 *
 *   "<actor> updated bill \"Internet\" (amount)"
 *   "<actor> (via moltis) deleted event \"Date night\" on Sat Nov 28"
 *
 * The war-game record (#4947, KB architecture/notification-mark-read.md) is
 * explicit that this trail — "Amber paid the internet", and who did it — is
 * what stops a double payment or an argument, so it has to stay legible. The
 * parser pulls the actor out to render it as the row's subject instead of
 * leaving a wall of text; the action text itself is never truncated.
 *
 * A real `actor` field belongs to the API (see the KB doc written with this
 * ticket); until then this stays a display-only parse that degrades to the raw
 * title when the shape doesn't match.
 */

/** One row of GET /api/v1/notifications (apps/api/internal/notification/handler.go). */
export interface NotificationItem {
  id: string;
  household_id?: string;
  /** bill_due, maintenance_due, calendar_alarm, item_changed, ... */
  type: string;
  title: string;
  /** Missing when the row's body is NULL (calendar alarms can be). */
  body?: string;
  /** entity_kind domain value (migration 070), or absent for family-level rows. */
  entity_type?: string;
  entity_id?: string;
  /** RFC 1123 Z (Go's http.TimeFormat), e.g. "Mon, 16 Nov 2026 09:00:00 +0000". */
  created_at: string;
  /** Set once read. The list endpoint only returns unread rows, so this is
   *  normally absent; it is set locally when the user marks one read. */
  read_at?: string;
}

export interface NotificationsResponse {
  data: NotificationItem[];
}

/** Who the row is visible to (derived from its type; see notificationScope). */
export type NotificationScope = "family" | "personal" | "unknown";

/** The actor/action split of an `item_changed` title. */
export interface NotificationAction {
  /** Family display name of whoever made the change (never empty in a match). */
  actor: string;
  /** The agent token that acted for them, e.g. "moltis". */
  via: string | null;
  /** created | updated | deleted. */
  verb: string;
  /** What changed, e.g. `bill "Internet" (amount)`. */
  rest: string;
}

/**
 * Types whose rows carry no user_id, so every member of the family sees them
 * and one shared read_at covers them (apps/worker/cmd/worker/main.go:
 * billDueSQL and maintenanceDueSQL insert without user_id).
 */
const FAMILY_WIDE_TYPES = new Set(["bill_due", "maintenance_due"]);

/**
 * Types that are one member's own row: calendar_alarm is written once per
 * member (apps/worker/internal/alarms/alarms.go insertSQL) and item_changed
 * goes to the record's creator only (migration 054).
 */
const PERSONAL_TYPES = new Set(["calendar_alarm", "item_changed"]);

/**
 * Who else can see a row. The API doesn't return scope or user_id, so it is
 * derived from the type — the same rule the writers use. A type this doesn't
 * know is "unknown" rather than a guess: POST /api/v1/notifications can create
 * any type, with or without a user_id.
 */
export function notificationScope(n: Pick<NotificationItem, "type">): NotificationScope {
  if (FAMILY_WIDE_TYPES.has(n.type)) return "family";
  if (PERSONAL_TYPES.has(n.type)) return "personal";
  return "unknown";
}

/** Human words for a row's scope, or null when it isn't known. */
export function scopeLabel(scope: NotificationScope): string | null {
  if (scope === "family") return "Family-wide";
  if (scope === "personal") return "Only you";
  return null;
}

/** True when reading this row read it for everyone (family-wide rows). */
export function isSharedReadState(n: Pick<NotificationItem, "type">): boolean {
  return notificationScope(n) === "family";
}

/**
 * `^(actor)( (via agent))? (created|updated|deleted) (what)$`, non-greedy on
 * the actor so the first verb wins. " (via ...)" only appears when an agent
 * token made the change (migration 054: `v_actor || ' (via ' || v_agent || ')'`).
 *
 * Positional groups, not named ones: tsconfig targets es5, where named
 * capturing groups are a compile error (TS1503).
 */
const TITLE_ACTION = /^(.+?)(?: \(via ([^)]+)\))? (created|updated|deleted) (.+)$/;

/**
 * Splits an `item_changed` title into actor / via / verb / what. Returns null
 * for every other type and for a title the writer didn't shape that way
 * (bill_due's "<bill> due soon", an alarm's event name), so callers fall back
 * to showing the title as the subject.
 */
export function parseNotificationTitle(
  n: Pick<NotificationItem, "type" | "title">,
): NotificationAction | null {
  if (n.type !== "item_changed") return null;
  const m = TITLE_ACTION.exec(n.title ?? "");
  if (!m) return null;
  const [, actor, via, verb, rest] = m;
  if (!actor || !verb || !rest) return null;
  return { actor, via: via ?? null, verb, rest };
}

/** What a row is, in words: "Bill reminder", "Calendar reminder", ... */
export function notificationKindLabel(n: Pick<NotificationItem, "type">): string {
  switch (n.type) {
    case "bill_due":
      return "Bill reminder";
    case "maintenance_due":
      return "Upkeep reminder";
    case "calendar_alarm":
      return "Calendar reminder";
    case "item_changed":
      return "Change notice";
    default:
      return "Notification";
  }
}

/** The noun for an "Open <noun>" action label. */
const ABOUT_LABELS: Record<string, string> = {
  bill: "bill",
  loan: "loan",
  asset: "asset",
  vehicle: "vehicle",
  pet: "pet",
  vendor: "vendor",
  property: "property",
  person: "person",
  recipe: "recipe",
  meal_plan: "meal plan",
  maintenance_task: "upkeep task",
  maintenance_schedule: "upkeep schedule",
  calendar: "calendar",
  calendar_event: "event",
  list: "list",
  note: "note",
  secret: "credential",
  file: "file",
  document_link: "document",
  budget: "budget",
  guide: "guide",
  room: "room",
  household: "family",
};

export function aboutLabel(entityType: string | undefined): string | null {
  if (!entityType) return null;
  return ABOUT_LABELS[entityType] ?? null;
}

/**
 * Where a row opens, or null when the app has no page for it yet. Only routes
 * that exist today are listed — the old map fell back to /dashboard for
 * anything unknown, which reads as "this notification goes somewhere" when it
 * does not. entity_type is the entity_kind domain (migration 070).
 */
export function entityHref(
  entityType: string | undefined,
  entityId: string | undefined,
): string | null {
  if (!entityType) return null;
  switch (entityType) {
    case "bill":
      return entityId ? `/dashboard/bills/${entityId}` : null;
    case "loan":
      return entityId ? `/dashboard/loans/${entityId}` : null;
    case "asset":
      return entityId ? `/dashboard/assets/${entityId}` : null;
    case "vehicle":
      return entityId ? `/dashboard/vehicles/${entityId}` : null;
    case "pet":
      return entityId ? `/dashboard/pets/${entityId}` : null;
    case "vendor":
      return entityId ? `/dashboard/vendors/${entityId}` : null;
    case "property":
      return entityId ? `/dashboard/properties/${entityId}` : null;
    case "recipe":
      return entityId ? `/dashboard/recipes/${entityId}` : null;
    case "maintenance_task":
    case "maintenance_schedule":
      return "/dashboard/maintenance";
    case "calendar":
    case "calendar_event":
      return "/dashboard/calendar";
    case "person":
      return "/dashboard/members";
    case "meal_plan":
      return "/dashboard/meals";
    case "household":
      return "/dashboard";
    // No page yet (its ticket is pending): lists, notes, documents,
    // credentials, budgets, guides, rooms. The row still shows its full text,
    // so nothing is lost by not linking.
    default:
      return null;
  }
}

const MINUTE = 60;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/**
 * "2 hours ago", "yesterday", "Nov 12". `now` is injectable so the boundary
 * cases are testable. An unparseable timestamp returns "" (the row still shows
 * its absolute time in the `title` attribute).
 */
export function relativeTime(iso: string, now: number = Date.now()): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "";
  const deltaSeconds = (t - now) / 1000;
  const abs = Math.abs(deltaSeconds);
  if (abs < 45) return "just now";

  const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });
  if (abs < HOUR) return rtf.format(Math.round(deltaSeconds / MINUTE), "minute");
  if (abs < DAY) return rtf.format(Math.round(deltaSeconds / HOUR), "hour");
  if (abs < 7 * DAY) return rtf.format(Math.round(deltaSeconds / DAY), "day");
  return absoluteDate(iso, now);
}

/** "Mon, Nov 16, 9:00 AM" — the exact time, for the row's title attribute. */
export function absoluteTime(iso: string): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "";
  return new Date(t).toLocaleString("en-US", {
    weekday: "short",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

/** "Nov 12", with the year once it isn't this one. */
function absoluteDate(iso: string, now: number = Date.now()): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "";
  const date = new Date(t);
  const sameYear = date.getFullYear() === new Date(now).getFullYear();
  return date.toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    ...(sameYear ? {} : { year: "numeric" }),
  });
}

export interface NotificationGroup {
  key: "new" | "earlier";
  label: string;
  items: NotificationItem[];
}

/**
 * "New" (unread) then "Earlier" (read in this session — the list endpoint only
 * returns unread rows, so a row read server-side never arrives). Order inside a
 * group is the API's, newest first.
 */
export function groupNotifications(items: NotificationItem[]): NotificationGroup[] {
  const fresh = items.filter((n) => !n.read_at);
  const earlier = items.filter((n) => !!n.read_at);
  const groups: NotificationGroup[] = [];
  if (fresh.length > 0) groups.push({ key: "new", label: "New", items: fresh });
  if (earlier.length > 0) groups.push({ key: "earlier", label: "Earlier", items: earlier });
  return groups;
}

/** How many rows are still unread — the badge count and the sheet's summary. */
export function unreadCount(items: NotificationItem[]): number {
  return items.filter((n) => !n.read_at).length;
}
