/**
 * Today's day: the shapes it reads and the pure functions that turn them into
 * the rows the page renders (#4549, built to artifact #76 v1).
 *
 * Everything here is derived from an existing endpoint — nothing invents data:
 *
 *   GET /calendars/events?start&end   the agenda (recurrences expanded in the
 *                                     range, so a weekly drop-off is one row)
 *   GET /bills                        bill state: paid_date /
 *                                     paid_for_due_date, never a calendar event
 *   GET /maintenance/tasks            upkeep due in the next two weeks
 *   GET /meal-plans?from&to           tonight's dinner
 *   GET /recipes                      the dinner's recipe (time, ingredients)
 *   GET /people                       names, colours, allergies
 *   GET /lists                        open lists and their open-item counts
 *   GET /activity                     the attribution feed ("who added what")
 *   GET /me/households                the household's timezone and the role
 *
 * Pure functions and types only (no JSX, no React), so `node --test` loads this
 * file directly.
 */
import { addDays, billDueFor, daysBetween, parseCivil } from "./due.ts";

// ── wire shapes (only the fields the page reads) ──────────────

/** One occurrence from GET /calendars/events (recurrences already expanded). */
export interface AgendaEvent {
  id: string;
  calendar_id: string;
  event_type: string;
  title: string;
  description?: string;
  /** RFC 3339. */
  start: string;
  end: string;
  all_day: boolean;
  location?: string;
  color?: string;
  entity_type?: string;
  entity_id?: string;
  /** The series' rule when this occurrence repeats ("FREQ=WEEKLY;BYDAY=MO"). */
  rrule?: string;
  recurring?: boolean;
  /** Set on an expanded occurrence: the series' own start. */
  recurrence_id?: string;
}

/** GET /api/v1/bills row (billResponse). */
export interface BillRow {
  id: string;
  name: string;
  amount?: string | null;
  due_day?: number | null;
  category?: string | null;
  frequency?: string | null;
  due_date?: string | null;
  is_autopay?: boolean | null;
  paid_date?: string | null;
  paid_for_due_date?: string | null;
}

/** GET /api/v1/maintenance/tasks row (components/maintenance/types.ts). */
export interface MaintenanceRow {
  id: string;
  name: string;
  status: "pending" | "in_progress" | "done" | "skipped";
  due_date: string | null;
}

/** GET /api/v1/meal-plans row (lib/recipes/types.ts Meal). */
export interface MealRow {
  id: string;
  date: string;
  slot: string;
  recipe_id?: string | null;
  title: string;
  notes?: string | null;
}

/** GET /api/v1/lists row (listResponse). */
export interface ListRow {
  id: string;
  name: string;
  kind: string;
  archived: boolean;
  is_template: boolean;
  item_count: number;
  open_count: number;
}

/** GET /api/v1/people row (personResponse, the fields Today reads). */
export interface PersonRow {
  id: string;
  display_name: string;
  relationship?: string | null;
  allergies: string[];
  color?: string | null;
  user_id?: string | null;
}

/** GET /api/v1/activity row (entryResponse). */
export interface ActivityRow {
  id: number;
  /** create | update | delete (activity_log's CHECK constraint, migration 050). */
  action: string;
  entity_type: string;
  entity_id: string;
  parent_type?: string | null;
  parent_id?: string | null;
  /** Column names an update touched; [] for create and delete. */
  changed_fields?: string[];
  summary: string;
  description: string;
  created_at: string;
  actor?: { user_id: string; person_id?: string | null; name?: string | null } | null;
  via_agent: boolean;
  agent?: { token_id: string; name?: string | null } | null;
}

// ── household date ───────────────────────────────────────────

/**
 * Today's date in the HOUSEHOLD's timezone, as YYYY-MM-DD.
 *
 * The API computes "today" in the household's zone (bill due dates, meal plan
 * dates, `bill_find`); a phone in another zone must not show a different day
 * than the server's answer. `Intl` with the IANA zone from
 * GET /households/me does that without a dependency.
 */
export function householdToday(timezone: string | null | undefined, now: Date = new Date()): string {
  if (timezone) {
    try {
      const parts = new Intl.DateTimeFormat("en-CA", {
        timeZone: timezone,
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
      }).formatToParts(now);
      const get = (t: string) => parts.find((p) => p.type === t)?.value ?? "";
      const y = get("year");
      const m = get("month");
      const d = get("day");
      if (y && m && d) return `${y}-${m}-${d}`;
    } catch {
      // An unknown zone falls through to the browser's date below.
    }
  }
  const y = now.getFullYear();
  const m = String(now.getMonth() + 1).padStart(2, "0");
  const d = String(now.getDate()).padStart(2, "0");
  return `${y}-${m}-${d}`;
}

// ── the agenda ───────────────────────────────────────────────

/**
 * A bill's mirror event on the household's Bills calendar
 * (`packages/billcal`: entity_type 'bill' and event_type 'bill').
 *
 * These never belong on Today's agenda: their text carries the amount, and
 * they say nothing about whether the bill was paid. Today shows bills from the
 * bills endpoint (see the header of due.ts for the R1 failure this avoids).
 */
export function isBillMirrorEvent(ev: Pick<AgendaEvent, "event_type" | "entity_type">): boolean {
  return ev.event_type === "bill" || ev.entity_type === "bill";
}

/** Upkeep events (a maintenance schedule's mirror) stay out of the family agenda; they show under upkeep. */
export function isMaintenanceEvent(ev: Pick<AgendaEvent, "event_type" | "entity_type">): boolean {
  return (
    ev.event_type === "maintenance" ||
    ev.entity_type === "maintenance_task" ||
    ev.entity_type === "maintenance"
  );
}

/**
 * The minutes-since-midnight of an instant in the household's zone, plus the
 * zone-local date it falls on. Null when the instant is unparseable.
 */
export function localTime(iso: string, timezone: string | null | undefined): { date: string; minutes: number } | null {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return null;
  if (timezone) {
    try {
      const parts = new Intl.DateTimeFormat("en-CA", {
        timeZone: timezone,
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      }).formatToParts(t);
      const get = (k: string) => parts.find((p) => p.type === k)?.value ?? "";
      const date = `${get("year")}-${get("month")}-${get("day")}`;
      // "24" appears for midnight in some ICU versions; 0 is the same instant.
      const hour = Number(get("hour")) % 24;
      const minute = Number(get("minute"));
      if (Number.isFinite(hour) && Number.isFinite(minute)) return { date, minutes: hour * 60 + minute };
    } catch {
      // fall through
    }
  }
  const y = t.getFullYear();
  const mo = String(t.getMonth() + 1).padStart(2, "0");
  const d = String(t.getDate()).padStart(2, "0");
  return { date: `${y}-${mo}-${d}`, minutes: t.getHours() * 60 + t.getMinutes() };
}

/** "7:50 AM" — the agenda's time column. */
export function formatTimeOfDay(minutes: number): string {
  const h24 = Math.floor(minutes / 60) % 24;
  const m = minutes % 60;
  const suffix = h24 < 12 ? "AM" : "PM";
  const h = h24 % 12 === 0 ? 12 : h24 % 12;
  return `${h}:${String(m).padStart(2, "0")} ${suffix}`;
}

/** A due date as the page writes it: "Wed", "Fri, Nov 20", "Dec 1". */
export function formatDueLabel(date: string, today: string): string {
  const n = parseCivil(date);
  const t = parseCivil(today);
  if (n === null || t === null) return date;
  const d = new Date(n * 86_400_000);
  const diff = n - t;
  const weekday = d.toLocaleDateString("en-US", { weekday: "short", timeZone: "UTC" });
  const month = d.toLocaleDateString("en-US", { month: "short", timeZone: "UTC" });
  if (diff === 0) return "Today";
  if (diff === 1) return "Tomorrow";
  if (diff > 1 && diff <= 6) return weekday;
  const sameYear = new Date(t * 86_400_000).getUTCFullYear() === d.getUTCFullYear();
  return sameYear ? `${weekday}, ${month} ${d.getUTCDate()}` : `${month} ${d.getUTCDate()}, ${d.getUTCFullYear()}`;
}

/** "in 8 min", "in 3 h", "now", or "" for something that has started. */
export function relativeToNow(startMinutes: number, nowMinutes: number): string {
  const diff = startMinutes - nowMinutes;
  if (diff < 0) return "";
  if (diff === 0) return "now";
  if (diff < 60) return `in ${diff} min`;
  const h = Math.floor(diff / 60);
  const m = diff % 60;
  return m === 0 ? `in ${h} h` : `in ${h} h ${m} min`;
}

/** One row of the day's agenda. */
export interface AgendaRow {
  event: AgendaEvent;
  /** The occurrence's own key: a series' occurrences share the series id. */
  key: string;
  /** "HH:MM" in the household's zone; "" for an all-day row. */
  time: string;
  /** Minutes since midnight, for ordering the "now" line; null for all-day. */
  minutes: number | null;
  allDay: boolean;
  /** Already over as of `nowMinutes`. */
  past: boolean;
  /** In progress: started, not ended. */
  happeningNow: boolean;
  /** A human label for the row's rule, or "". */
  recurrence: string;
}

/** A human reading of an RRULE, or "" when it repeats in no simple way. */
export function recurrenceLabel(rrule: string | null | undefined): string {
  if (!rrule) return "";
  const rule = rrule.replace(/^RRULE:/i, "");
  const fields = new Map<string, string>();
  for (const part of rule.split(";")) {
    const [k, v] = part.split("=");
    if (k && v) fields.set(k.toUpperCase(), v.toUpperCase());
  }
  const freq = fields.get("FREQ");
  if (!freq) return "";
  const byDay = (fields.get("BYDAY") ?? "").split(",").filter(Boolean);
  const weekdays: Record<string, string> = { MO: "Mon", TU: "Tue", WE: "Wed", TH: "Thu", FR: "Fri", SA: "Sat", SU: "Sun" };
  const dayName = (d: string) => weekdays[d.slice(-2)] ?? d;
  switch (freq) {
    case "DAILY":
      return "Every day";
    case "WEEKLY": {
      if (byDay.length === 0) return "Every week";
      const names = byDay.map(dayName);
      if (names.length === 5 && ["Mon", "Tue", "Wed", "Thu", "Fri"].every((d) => names.includes(d))) {
        return "Every weekday";
      }
      return names.join(", ");
    }
    case "MONTHLY": {
      const pos = fields.get("BYDAY") ?? byDay[0] ?? "";
      const n = Number(fields.get("BYMONTHDAY") ?? "");
      if (fields.get("BYDAY") && /^[+-]?\d/.test(pos)) {
        const ord = pos.slice(0, -2);
        const suffix = ord === "1" ? "st" : ord === "2" ? "nd" : ord === "3" ? "rd" : "th";
        return `${ord}${suffix} ${dayName(pos)}`;
      }
      if (Number.isFinite(n) && n > 0) return `Monthly on the ${n}${n === 1 ? "st" : n === 2 ? "nd" : n === 3 ? "rd" : "th"}`;
      return "Monthly";
    }
    case "YEARLY":
      return "Every year";
    default:
      return "";
  }
}

/**
 * The day's agenda: today's (and, for all-day events, today's only) rows in
 * time order, past rows kept and dimmed rather than hidden.
 *
 * `nowMinutes` is the household-local time of "now"; pass null to leave every
 * row "upcoming" (the server-rendered first paint).
 */
export function buildAgenda(
  events: readonly AgendaEvent[],
  today: string,
  timezone: string | null | undefined,
  nowMinutes: number | null,
): AgendaRow[] {
  const rows: AgendaRow[] = [];
  for (const ev of events) {
    if (isBillMirrorEvent(ev)) continue;
    const start = localTime(ev.start, timezone);
    if (!start) continue;
    // An all-day event's start is midnight in the household's zone, so its own
    // date is the day it belongs to.
    if (start.date !== today) continue;
    const end = localTime(ev.end, timezone);
    const minutes = ev.all_day ? null : start.minutes;
    const startsPast = nowMinutes !== null && minutes !== null && minutes < nowMinutes;
    const endsAfterNow = (() => {
      if (nowMinutes === null) return false;
      if (!end) return false;
      if (end.date !== today) return true; // runs past midnight
      return end.minutes > nowMinutes;
    })();
    rows.push({
      event: ev,
      key: ev.recurrence_id ?? ev.id,
      time: ev.all_day ? "" : formatTimeOfDay(start.minutes),
      minutes,
      allDay: ev.all_day,
      past: startsPast && !endsAfterNow,
      happeningNow: startsPast && endsAfterNow,
      recurrence: recurrenceLabel(ev.rrule),
    });
  }
  // All-day rows first (the mockup's strip), then timed rows in time order.
  rows.sort((a, b) => {
    if (a.allDay !== b.allDay) return a.allDay ? -1 : 1;
    return (a.minutes ?? 0) - (b.minutes ?? 0);
  });
  return rows;
}

/**
 * The row that should expand in place: the first timed event that has not
 * started yet. Null when everything today has started (or nothing is left).
 */
export function upNextRow(rows: readonly AgendaRow[]): AgendaRow | null {
  return rows.find((r) => !r.allDay && !r.past && !r.happeningNow) ?? null;
}

// ── bills due / upkeep due ───────────────────────────────────

/** One row of "Bills & upkeep": a bill or a maintenance task, with its due date. */
export interface DueRow {
  kind: "bill" | "upkeep";
  id: string;
  name: string;
  /** The bill's amount, already formatted; null for upkeep. */
  amount: string | null;
  /** "Electric · autopay on" — the row's second line. */
  detail: string;
  dueDate: string;
  /** Days from today: negative when overdue. */
  daysUntil: number;
  autopay: boolean;
  /** The bill is already paid for the due date the row would otherwise show. */
  paid: boolean;
}

/** Bills and upkeep falling due from today through today + `windowDays`. */
export function billsAndUpkeepDue(
  today: string,
  bills: readonly BillRow[],
  tasks: readonly MaintenanceRow[],
  windowDays = 14,
): { rows: DueRow[]; withoutDueDate: number } {
  const rows: DueRow[] = [];
  let withoutDueDate = 0;
  for (const b of bills) {
    const due = billDueFor(today, b);
    if (!due || !due.next) {
      // A bill whose schedule cannot produce a date, or a paid once bill with
      // nothing left: count the former, skip the latter.
      if (!due) withoutDueDate += 1;
      continue;
    }
    if (due.daysUntil > windowDays) continue;
    // A PAID bill does not appear as due. It is not due: the payment covers
    // the date this row would show. (R1: "Electric $180 due, still unpaid"
    // about a bill paid that morning.)
    if (due.status === "paid") continue;
    rows.push({
      kind: "bill",
      id: b.id,
      name: b.name,
      amount: formatMoney(b.amount),
      detail: [titleCase(b.category ?? ""), b.is_autopay ? "autopay on" : "autopay off"].filter(Boolean).join(" · "),
      dueDate: due.next,
      daysUntil: due.daysUntil,
      autopay: !!b.is_autopay,
      paid: false,
    });
  }
  for (const t of tasks) {
    if (t.status === "done" || t.status === "skipped") continue;
    if (!t.due_date) {
      withoutDueDate += 1;
      continue;
    }
    const d = daysBetween(today, t.due_date);
    if (d === null || d > windowDays) continue;
    rows.push({
      kind: "upkeep",
      id: t.id,
      name: t.name,
      amount: null,
      detail: t.status === "in_progress" ? "In progress" : "Upkeep",
      dueDate: t.due_date.slice(0, 10),
      daysUntil: d,
      autopay: false,
      paid: false,
    });
  }
  // Overdue first, then by date; a bill and a task on the same day keep the
  // bill first (its money is the reason a parent opens this).
  rows.sort((a, b) => {
    const ad = a.daysUntil;
    const bd = b.daysUntil;
    const aLate = ad < 0;
    const bLate = bd < 0;
    if (aLate !== bLate) return aLate ? -1 : 1;
    if (ad !== bd) return ad - bd;
    if (a.kind !== b.kind) return a.kind === "bill" ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
  return { rows, withoutDueDate };
}

/** "142.18" or "142.1800" → "$142.18". Null stays null. */
export function formatMoney(amount: string | null | undefined): string | null {
  if (amount === null || amount === undefined || amount === "") return null;
  const n = Number(amount);
  if (!Number.isFinite(n)) return null;
  return n.toLocaleString("en-US", { style: "currency", currency: "USD" });
}

function titleCase(s: string): string {
  if (!s) return "";
  return s.charAt(0).toUpperCase() + s.slice(1);
}

// ── dinner ───────────────────────────────────────────────────

/** Tonight's dinner, and the recipe facts the card shows when there is one. */
export interface DinnerToday {
  meal: MealRow;
  recipeId: string | null;
  /** "35 min · serves 5", or "" when the recipe is not loaded/none. */
  facts: string;
  /** "Amber is cooking" is not stored anywhere; the page says what IS known. */
  linkedToRecipe: boolean;
}

/** The dinner planned for `today`, if any. */
export function dinnerToday(meals: readonly MealRow[], today: string): MealRow | null {
  return meals.find((m) => m.date.slice(0, 10) === today && m.slot === "dinner") ?? null;
}

/** "35 min · serves 5" from a recipe's prep/cook minutes and servings. */
export function recipeFacts(recipe: { prep_minutes?: number | null; cook_minutes?: number | null; servings?: number | null } | null | undefined): string {
  if (!recipe) return "";
  const parts: string[] = [];
  const minutes = (recipe.prep_minutes ?? 0) + (recipe.cook_minutes ?? 0);
  if (minutes > 0) parts.push(`${minutes} min`);
  if (recipe.servings && recipe.servings > 0) parts.push(`serves ${recipe.servings}`);
  return parts.join(" · ");
}

// ── tomorrow ─────────────────────────────────────────────────

/** Tomorrow's rows, for the "Tomorrow" peek. */
export function buildTomorrow(
  events: readonly AgendaEvent[],
  tomorrow: string,
  timezone: string | null | undefined,
): AgendaRow[] {
  return buildAgenda(events, tomorrow, timezone, null);
}

/** "Tue, Nov 17" for a YYYY-MM-DD. */
export function formatDayHeading(date: string): string {
  const n = parseCivil(date);
  if (n === null) return date;
  return new Date(n * 86_400_000).toLocaleDateString("en-US", {
    weekday: "short",
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  });
}

/** The range Today asks the calendar for: today 00:00 to the day after tomorrow 00:00 (household zone). */
export function agendaRange(today: string): { start: string; end: string } {
  return { start: today, end: addDays(today, 2) };
}

// ── attribution ──────────────────────────────────────────────

export interface Attribution {
  /** Who made the change: "Amber", "You", or "Someone" when the API named nobody. */
  who: string;
  /** True when it arrived through an agent token (moltis, Claude, ...). */
  viaAgent: boolean;
  /** The agent's name when the entry carries one, else null. */
  agentName: string | null;
  /** "12 min ago", "Yesterday, 8:14 PM" — when it happened, in the household's zone. */
  when: string;
}

/**
 * Who a feed entry came from, in the shape the row prints: "Amber via
 * moltis-wg-amber". Attribution is the point of this panel — it is what stops
 * "did you book that?" — so the agent's name is shown whenever the API knows
 * it and never invented when it doesn't.
 */
export function attribution(
  entry: Pick<ActivityRow, "actor" | "via_agent" | "agent" | "created_at">,
  viewerUserId: string | null | undefined,
  now: Date = new Date(),
): Attribution {
  const name = entry.actor?.name?.trim() || "";
  const isViewer = !!viewerUserId && entry.actor?.user_id === viewerUserId;
  return {
    who: isViewer ? "You" : name || "Someone",
    viaAgent: !!entry.via_agent,
    agentName: entry.agent?.name?.trim() || null,
    when: relativeWhen(entry.created_at, now),
  };
}

/** "12 min ago", "2 h ago", "Yesterday, 8:14 PM", "Saturday". */
export function relativeWhen(iso: string, now: Date = new Date()): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return "";
  const diffMs = now.getTime() - t.getTime();
  const mins = Math.round(diffMs / 60_000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins} min ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours} h ago`;
  const days = Math.round(hours / 24);
  if (days === 1) return "Yesterday";
  if (days < 7) return `${days} days ago`;
  return t.toLocaleDateString("en-US", { weekday: "long", timeZone: "UTC" });
}

/**
 * The feed's row, split so the page can style it: who, what, and — separately —
 * the agent's name.
 *
 * The API's `description` already reads `Amber created list item "oat milk" via
 * agent "moltis-wg-amber"`, so the agent is in the string; rendering it again as
 * a chip would say it twice. This pulls the ` via agent "<name>"` tail off (it
 * becomes the chip) and rewrites the leading actor to "You" for the viewer's own
 * writes. Every other word is the server's.
 */
export function feedParts(
  entry: Pick<ActivityRow, "description" | "summary" | "actor" | "via_agent" | "agent">,
  viewerUserId: string | null | undefined,
): { who: string; text: string; viaAgent: boolean; agentName: string | null } {
  const agentName = entry.agent?.name?.trim() || null;
  const who = actorLabel(entry, viewerUserId);
  let text = (entry.description ?? entry.summary ?? "").trim();
  // Drop the API's own "via agent ..." tail: it is rendered as the chip.
  text = text.replace(/\s+via agent(?:\s+"[^"]*")?\s*$/i, "").trim();
  // Take the actor's name off the front: the row prints who in bold before the
  // text. The API writes the person's full name ("Jacob Sims"), but the shell
  // greets by first name, so a "Jacob created ..." line is stripped by its first
  // word too — otherwise the row reads "You Jacob created ...".
  const name = entry.actor?.name?.trim() || "";
  const firstName = name.split(/\s+/)[0] ?? "";
  for (const prefix of [name, firstName, who === "You" ? "You" : ""]) {
    if (prefix && text.startsWith(`${prefix} `)) {
      text = text.slice(prefix.length).trim();
      break;
    }
  }
  return { who, text, viaAgent: !!entry.via_agent, agentName: agentName ?? (entry.via_agent ? "an agent" : null) };
}

/** "You", the actor's name, or "Someone" when the API named nobody. */
export function actorLabel(
  entry: Pick<ActivityRow, "actor">,
  viewerUserId: string | null | undefined,
): string {
  const name = entry.actor?.name?.trim() || "";
  if (viewerUserId && entry.actor?.user_id === viewerUserId) return "You";
  return name || "Someone";
}

// ── opening the record a change is about ─────────────────────

/**
 * Where a change's record lives, for the kinds that have a page. The plural
 * rule is EntityDetail's (`/api/v1/${entityType}s/{id}`), which is also how a
 * notification row links (#4563) — one idea of which pages exist, not two that
 * drift.
 *
 * A kind with no page maps to nothing rather than to a fallback. A row that
 * opens "somewhere" when it cannot open the record is exactly the dead control
 * this repo's conventions forbid, so lists, notes, documents, credentials and
 * guides stay text-only until they have a page.
 */
const RECORD_ROUTES: Readonly<Record<string, string>> = {
  bill: "/dashboard/bills",
  loan: "/dashboard/loans",
  asset: "/dashboard/assets",
  vehicle: "/dashboard/vehicles",
  pet: "/dashboard/pets",
  vendor: "/dashboard/vendors",
  property: "/dashboard/properties",
  recipe: "/dashboard/recipes",
  // Pages that exist but are not per-record: the row opens the section that
  // holds the record, which is where a person would look for it.
  maintenance_task: "/dashboard/maintenance",
  maintenance_schedule: "/dashboard/maintenance",
  calendar_event: "/dashboard/calendar",
  calendar: "/dashboard/calendar",
  person: "/dashboard/members",
  meal_plan: "/dashboard/meals",
  meal_feedback: "/dashboard/meals",
};

/** The kinds whose page reads ONE record, so its existence can be probed. */
const PROBEABLE_KINDS = new Set([
  "bill",
  "loan",
  "asset",
  "vehicle",
  "pet",
  "vendor",
  "property",
  "recipe",
]);

/** The href for a change's record, or null when the app has no page for it. */
export function activityHref(
  entry: Pick<ActivityRow, "entity_type" | "entity_id">,
): string | null {
  const base = RECORD_ROUTES[entry.entity_type];
  if (!base) return null;
  if (!PROBEABLE_KINDS.has(entry.entity_type)) return base;
  return entry.entity_id ? `${base}/${entry.entity_id}` : null;
}

/** The GET that answers "does this record still exist", or null. */
export function activityProbePath(
  entry: Pick<ActivityRow, "entity_type" | "entity_id">,
): string | null {
  if (!PROBEABLE_KINDS.has(entry.entity_type) || !entry.entity_id) return null;
  return `/api/v1/${entry.entity_type}s/${entry.entity_id}`;
}

/**
 * What a row can do about the record it is about.
 *
 * - `deleted`: the change WAS the deletion, so there is nothing to open, and
 *   the row says so instead of offering a link that cannot work.
 * - `gone`: the record answered 404 when checked — it was deleted after the
 *   change. This is the question R1 could not answer ("nothing was deleted that
 *   I can see" about a deletion the agent had made itself), so the row states
 *   it rather than opening a page that silently shows nothing.
 * - `link`: open it.
 * - `none`: no page exists for this kind of record yet; the row is text only.
 *
 * `exists` is tri-state on purpose: `undefined`/`null` means nobody checked, or
 * the check failed for a reason other than 404. A failed check is never
 * reported as a deletion — that would be the same false certainty, inverted.
 */
export type ActivityTargetKind = "deleted" | "gone" | "link" | "none";

export interface ActivityTarget {
  kind: ActivityTargetKind;
  href: string | null;
  /** The line the row prints under the change; null for a plain link. */
  note: string | null;
}

export function activityTarget(
  entry: Pick<ActivityRow, "action" | "entity_type" | "entity_id">,
  exists?: boolean | null,
): ActivityTarget {
  if (entry.action === "delete") {
    return { kind: "deleted", href: null, note: "Deleted — nothing left to open." };
  }
  const href = activityHref(entry);
  if (!href) return { kind: "none", href: null, note: null };
  if (exists === false) {
    return { kind: "gone", href: null, note: "No longer there — deleted since." };
  }
  return { kind: "link", href, note: null };
}

/**
 * The avatar a change's row draws: the actor's initials and, when the family
 * records a colour for them, that member's colour — the same rule the People
 * panel and the mockup's `.av.amber` use, so one person looks the same wherever
 * they appear.
 *
 * The label is initials and never the word "You": "You" is what the row SAYS,
 * not what a two-letter circle can hold (it rendered as "YOU"). A viewer whose
 * actor record carries no name gets "Y" rather than a stub, and an entirely
 * unnamed actor ("Someone") gets "?" — never a made-up name.
 */
export function activityAvatar(
  entry: Pick<ActivityRow, "actor">,
  viewerUserId: string | null | undefined,
  people: readonly PersonRow[] = [],
): { label: string; color: string | null } {
  const name = entry.actor?.name?.trim() ?? "";
  const isViewer = !!viewerUserId && entry.actor?.user_id === viewerUserId;
  let label = "";
  if (name) {
    label = name
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((part) => part[0] ?? "")
      .join("");
  } else {
    label = isViewer ? "Y" : "?";
  }
  // The actor's colour comes from their people record: person_id is the direct
  // link, user_id the one people rows carry for anyone with a login.
  const person = people.find(
    (p) =>
      (entry.actor?.person_id && p.id === entry.actor.person_id) ||
      (!!entry.actor?.user_id && p.user_id === entry.actor.user_id),
  );
  return { label: label.toUpperCase(), color: person?.color?.trim() || null };
}

/**
 * The changes Today shows: the newest few, and how many of THOSE it did not show.
 *
 * Today must not render the whole feed — the R1 war game found bloated default
 * payloads slow screens down and give an agent more to misread — so the panel is
 * a window onto the feed, with "see all" for the rest.
 *
 * `hidden` counts only what the page actually holds: the feed hook asks for a
 * handful of rows, so a family with a hundred changes has far more than
 * `hidden` says. It is therefore enough to decide THAT there is more — it is
 * never printed as a count, because "4 more changes" would be a false claim
 * about a feed that holds ninety-six. Say "see all" instead.
 */
export function activityWindow(
  entries: readonly ActivityRow[],
  limit = 4,
): { rows: ActivityRow[]; hidden: number } {
  const cap = Math.max(0, limit);
  return { rows: entries.slice(0, cap), hidden: Math.max(0, entries.length - cap) };
}
