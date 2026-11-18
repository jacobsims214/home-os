/**
 * The calendar page's data shapes and the pure functions that turn a listing
 * into what it renders (#4550, built to artifact #87 v1 inside the shell of
 * #75 v1).
 *
 * WHY THIS FILE EXISTS AT ALL: round one of the R1 war game rendered a weekly
 * activity as 35 separate rows, so a parent scrolling the week saw the same
 * swim practice nine times and could not tell it was one thing. Everything
 * here is built around the fix:
 *
 *  1. THE SERIES IS THE UNIT. `GET /calendars/events?start&end` returns one
 *     row per OCCURRENCE (that is what makes the range honest: a moved or
 *     cancelled instance is a fact about that day), each carrying the series'
 *     id and a recurrence_id. `groupSeries` puts them back together, so a
 *     recurring event is ONE card that lists its occurrences.
 *  2. THE DAY AND TIME COME FROM THE DATA. Every event carries `local_date`,
 *     `weekday` and `local_time` computed by the API in the household's zone
 *     (apps/api/internal/calendar/localday.go). Nothing in this file derives a
 *     weekday from a Date, and nothing converts an instant to a wall clock: the
 *     R1 round-one calendar shifted an all-day event a day by doing exactly
 *     that, and a browser in another zone would do it again.
 *  3. AN ALL-DAY EVENT IS A DATE, never an instant: it renders on its
 *     `local_date` with "All day" and never as a clock time.
 *  4. A PAID BILL NEVER READS AS DUE. Bill mirrors sit on the Bills calendar;
 *     their amount and their state come from GET /bills (paid_date /
 *     paid_for_due_date through the shared bill rule in lib/today/due.ts), and
 *     a Bills-calendar entry the API did not link to a bill is labelled as
 *     exactly that instead of borrowing bill state.
 *
 * Plain data and pure functions (no JSX, no React), so `node --test` loads it.
 */
import { billDueFor, parseCivil, type BillDue } from "../today/due.ts";

// ── wire shapes (only the fields the page reads) ──────────────

/** GET /api/v1/calendars/events row, recurrence-expanded when a range is sent. */
export interface CalEvent {
  id: string;
  calendar_id: string;
  event_type: string;
  title: string;
  description?: string;
  /** RFC 3339 instant. Never rendered directly: use local_* below. */
  start: string;
  end: string;
  all_day: boolean;
  location?: string;
  color?: string;
  entity_type?: string;
  entity_id?: string;
  /** The series' rule, e.g. "FREQ=WEEKLY;BYDAY=TU,TH". */
  rrule?: string;
  recurring?: boolean;
  /** An expanded occurrence's original start (its own identity within the series). */
  recurrence_id?: string;
  reminders?: { minutes_before: number }[];
  /** The API's answer to "which day is this", in the household's zone. */
  local_date?: string;
  weekday?: string;
  /** HH:MM wall clock in the household's zone; "" for an all-day event. */
  local_time?: string;
  /** The last day the event occupies (inclusive); an all-day event's stored end is exclusive. */
  end_local_date?: string;
  end_local_time?: string;
}

/** One day of the requested range, as the listing answers it. */
export interface DayInfo {
  local_date: string;
  weekday: string;
}

/** GET /api/v1/calendars row. */
export interface CalendarRow {
  id: string;
  name: string;
  type: string;
  color: string;
  property_id?: string | null;
}

/** GET /api/v1/bills row (bill state, never a calendar event). */
export interface BillRow {
  id: string;
  name: string;
  amount?: string | null;
  frequency?: string | null;
  due_day?: number | null;
  due_date?: string | null;
  is_autopay?: boolean | null;
  paid_date?: string | null;
  paid_for_due_date?: string | null;
}

/** GET /api/v1/people row, the fields an event sheet shows. */
export interface PersonRow {
  id: string;
  display_name: string;
  color?: string | null;
  user_id?: string | null;
}

/** GET /api/v1/activity row (the attribution source, #4577's shape). */
export interface ActivityRow {
  id: number;
  action: string;
  entity_type: string;
  entity_id: string;
  description?: string;
  summary?: string;
  created_at: string;
  actor?: { user_id: string; person_id?: string | null; name?: string | null } | null;
  via_agent?: boolean;
  agent?: { token_id: string; name?: string | null } | null;
}

// ── what kind of thing an event is ───────────────────────────

/** A bill's mirror event (`packages/billcal`: event_type 'bill' AND entity_type 'bill'). */
export function isBillMirror(ev: Pick<CalEvent, "event_type" | "entity_type">): boolean {
  return ev.event_type === "bill" || ev.entity_type === "bill";
}

/** An upkeep task's mirror (event_type 'maintenance', entity_type 'maintenance_task'). */
export function isUpkeepMirror(ev: Pick<CalEvent, "event_type" | "entity_type">): boolean {
  return (
    ev.event_type === "maintenance" ||
    ev.entity_type === "maintenance_task" ||
    ev.entity_type === "maintenance"
  );
}

/** The calendar kinds a person may write in this page. Bills and upkeep come from their own modules. */
export const WRITABLE_CALENDAR_TYPES = ["family", "custom", "vehicles", "properties"] as const;

/** True when the page may create/edit an event in this calendar. */
export function canWriteIn(cal: Pick<CalendarRow, "type"> | null | undefined): boolean {
  if (!cal) return false;
  return (WRITABLE_CALENDAR_TYPES as readonly string[]).includes(cal.type);
}

/**
 * Why an event is read-only here, in the words the sheet shows. Empty when it
 * is editable in this page.
 */
export function readOnlyReason(ev: CalEvent, cal: CalendarRow | undefined): string {
  if (isBillMirror(ev)) {
    return "This is a bill's due date. Change the amount or the date in Money, and it updates here.";
  }
  if (isUpkeepMirror(ev)) {
    return "This comes from a maintenance task in People & House. Reschedule it there or mark it done.";
  }
  if (cal && !canWriteIn(cal)) {
    return `The ${cal.name} calendar is filled by another part of Home OS, so its events are read-only here.`;
  }
  return "";
}

// ── one thing, with its occurrences ──────────────────────────

/**
 * A series: one card in the agenda, listing every occurrence the range holds.
 *
 * `id` is the series' own id for a repeating event and the event's id
 * otherwise, because occurrences carry the SERIES id (repo.go's expandEvent
 * copies `base` and only changes the times), so grouping by `id` is exactly
 * "one recurring event is one thing".
 */
export interface Series {
  id: string;
  title: string;
  /** Every occurrence in the range, earliest first. */
  occurrences: CalEvent[];
  /** True when the API says this repeats (rrule present or recurring flag). */
  repeating: boolean;
  rrule?: string;
  calendarId: string;
  /** The household-zone date of the FIRST occurrence, from the API. */
  localDate: string;
  weekday: string;
  /** The first occurrence's wall clock; "" when all-day. */
  localTime: string;
  allDay: boolean;
}

/** The occurrence's own key — unique inside a series. */
export function occurrenceKey(ev: CalEvent): string {
  return ev.recurrence_id ?? `${ev.id}@${ev.local_date ?? ev.start}`;
}

/** The series id an event belongs to. Occurrences share it; one-offs are their own. */
export function seriesId(ev: CalEvent): string {
  return ev.id;
}

/**
 * Groups occurrences into series, earliest first.
 *
 * Sorting is by the API's `local_date` and `local_time` (never by parsing the
 * instant), and an all-day event sorts before the day's timed events — the
 * mockup's all-day strip.
 */
export function groupSeries(events: readonly CalEvent[]): Series[] {
  const byId = new Map<string, Series>();
  for (const ev of events) {
    const localDate = ev.local_date ?? "";
    const localTime = ev.local_time ?? "";
    const key = seriesId(ev);
    const existing = byId.get(key);
    if (existing) {
      existing.occurrences.push(ev);
      continue;
    }
    byId.set(key, {
      id: key,
      title: ev.title,
      occurrences: [ev],
      repeating: !!ev.recurring || !!ev.rrule,
      rrule: ev.rrule,
      calendarId: ev.calendar_id,
      localDate,
      weekday: ev.weekday ?? "",
      localTime,
      allDay: !!ev.all_day,
    });
  }
  const out: Series[] = Array.from(byId.values());
  for (const s of out) {
    s.occurrences.sort(compareOccurrences);
    const first = s.occurrences[0];
    s.localDate = first.local_date ?? s.localDate;
    s.weekday = first.weekday ?? s.weekday;
    s.localTime = first.local_time ?? "";
    s.allDay = !!first.all_day;
    // A series whose first occurrence was changed to a one-off keeps its rule.
    s.repeating = s.occurrences.some((o) => !!o.recurring || !!o.rrule);
    s.title = first.title || s.title;
  }
  out.sort((a, b) => compareTimes(a.allDay, a.localTime, b.allDay, b.localTime) || a.title.localeCompare(b.title));
  return out;
}

/** All-day first, then by HH:MM (both strings the API produced). */
function compareTimes(aAllDay: boolean, aTime: string, bAllDay: boolean, bTime: string): number {
  if (aAllDay !== bAllDay) return aAllDay ? -1 : 1;
  return aTime.localeCompare(bTime);
}

function compareOccurrences(a: CalEvent, b: CalEvent): number {
  const day = (a.local_date ?? "").localeCompare(b.local_date ?? "");
  if (day !== 0) return day;
  return compareTimes(!!a.all_day, a.local_time ?? "", !!b.all_day, b.local_time ?? "");
}

// ── the days of a view ───────────────────────────────────────

export type CalendarView = "agenda" | "day" | "week" | "month";

/** One day of the agenda: its heading (from the API's day table) and its series. */
export interface AgendaDay {
  localDate: string;
  weekday: string;
  /** Short label for the heading's right side, e.g. "Nov 17" — built from local_date. */
  monthDay: string;
  isToday: boolean;
  series: Series[];
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/**
 * "Nov 17" from a YYYY-MM-DD string. This reads the digits the API sent; it
 * never builds a Date, so no timezone can move it.
 */
export function monthDay(localDate: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(localDate ?? "");
  if (!m) return localDate ?? "";
  const month = MONTHS[Number(m[2]) - 1] ?? "";
  return `${month} ${Number(m[3])}`;
}

/** "Nov 17, 2026" — the month view's range label. */
export function monthYear(localDate: string): string {
  const m = /^(\d{4})-(\d{2})/.exec(localDate ?? "");
  if (!m) return "";
  return `${MONTHS[Number(m[2]) - 1] ?? ""} ${m[1]}`;
}

/**
 * The agenda's days: one entry per day of the window, in order, each with the
 * series that touch it. Days with nothing stay in the list (a quiet row that
 * says so) rather than disappearing: an unexplained gap is how a parent misses
 * that Wednesday is empty.
 *
 * A series appears on every day it OCCURS, so a Tue/Thu lesson is on Tuesday's
 * list and Thursday's list — but it is one card, in both places.
 */
export function buildAgenda(
  events: readonly CalEvent[],
  days: readonly DayInfo[],
  today: string,
): AgendaDay[] {
  const series = groupSeries(events);
  return days.map((day) => ({
    localDate: day.local_date,
    weekday: day.weekday,
    monthDay: monthDay(day.local_date),
    isToday: day.local_date === today,
    series: series.filter((s) => s.occurrences.some((o) => (o.local_date ?? "") === day.local_date)),
  }));
}

/**
 * The occurrences of `series` that fall on one day — what a day cell lists.
 * A series spanning several days is on each of them.
 */
export function occurrencesOn(series: Series, localDate: string): CalEvent[] {
  return series.occurrences.filter((o) => (o.local_date ?? "") === localDate);
}

/** A day holds something: any all-day event or any timed one. */
export function dayHasContent(day: AgendaDay): boolean {
  return day.series.length > 0;
}

/**
 * The days a view needs, taken from the API's own day table (which answers the
 * weekday for each). `days` may be shorter than asked for; callers use what
 * they get and the page says so rather than rendering a half-labeled grid.
 */
export function windowFor(
  anchor: string,
  view: CalendarView,
  days: readonly DayInfo[],
): DayInfo[] {
  if (view === "day") return days.filter((d) => d.local_date === anchor).slice(0, 1);
  if (view === "week" || view === "agenda") {
    const start = days.findIndex((d) => d.local_date === anchor);
    if (start < 0) return days.slice(0, 7);
    return days.slice(start, start + 7);
  }
  // Month: the API is asked for the whole month grid (6 weeks), so show it all.
  return days.slice();
}

// ── time and date labels, from the API's strings ─────────────

/** "4:00 PM" from the API's "16:00"; "" when there is no wall clock (all-day). */
export function formatWallClock(hhmm: string | null | undefined): string {
  const m = /^(\d{2}):(\d{2})$/.exec(hhmm ?? "");
  if (!m) return "";
  const h24 = Number(m[1]);
  const suffix = h24 < 12 ? "AM" : "PM";
  const h = h24 % 12 === 0 ? 12 : h24 % 12;
  return `${h}:${m[2]} ${suffix}`;
}

/** "4:00 – 4:45 PM" from local_time / end_local_time (never from the instants). */
export function formatTimeRange(ev: CalEvent): string {
  if (ev.all_day) return "All day";
  const start = formatWallClock(ev.local_time);
  const end = formatWallClock(ev.end_local_time);
  if (!start) return "";
  if (!end) return start;
  // Share the meridiem when both halves are in it: "4:00 – 4:45 PM".
  const startSuffix = start.slice(-2);
  const endSuffix = end.slice(-2);
  const shortStart = startSuffix === endSuffix ? start.slice(0, -3) : start;
  return `${shortStart} – ${end}`;
}

/** "Thu, Nov 17" — the sheet's when line, from local_date + the API's weekday. */
export function formatDayLabel(localDate: string, weekday: string): string {
  const wd = (weekday ?? "").slice(0, 3);
  const md = monthDay(localDate);
  if (!wd) return md; // no weekday in the payload: say the date, invent nothing
  return `${wd}, ${md}`;
}

/**
 * The when line for an event: one day, or the days it spans.
 * All-day events read "Thu, Nov 26 – Sun, Nov 29 · All day".
 */
export function formatWhen(
  ev: CalEvent,
  weekdayOf: (localDate: string) => string = () => "",
): string {
  const start = formatDayLabel(ev.local_date ?? "", ev.weekday ?? "");
  const endDate = ev.end_local_date ?? ev.local_date ?? "";
  const spans = !!endDate && endDate !== (ev.local_date ?? "");
  // The end day's weekday comes from the listing's own day table when the page
  // has it (weekdayOf), never from a Date.
  const when = spans ? `${start} – ${formatDayLabel(endDate, weekdayOf(endDate))}` : start;
  if (ev.all_day) return `${when} · All day`;
  const time = formatTimeRange(ev);
  return time ? `${when} · ${time}` : when;
}

/** "Every Tue and Thu until Dec 17" for the sheet, or "" for a one-off. */
export function repeatSummary(ev: Pick<CalEvent, "rrule">): string {
  return ruleLabel(ev.rrule);
}

/** How many occurrences of a series the range holds. */
export function occurrenceCount(series: Series): number {
  return series.occurrences.length;
}

/** "8 times" / "once" — the count a series card shows. */
export function timesLabel(n: number): string {
  if (n === 1) return "once";
  return `${n} times`;
}

// ── recurrence rules ─────────────────────────────────────────

const WD_SHORT: Record<string, string> = { MO: "Mon", TU: "Tue", WE: "Wed", TH: "Thu", FR: "Fri", SA: "Sat", SU: "Sun" };
const WD_LONG: Record<string, string> = {
  MO: "Monday", TU: "Tuesday", WE: "Wednesday", TH: "Thursday", FR: "Friday", SA: "Saturday", SU: "Sunday",
};

/**
 * A human reading of an RRULE. The same shapes `lib/today/agenda.ts` labels,
 * extended with UNTIL and COUNT so the sheet can say when a series ends.
 */
export function ruleLabel(rrule: string | null | undefined): string {
  if (!rrule) return "";
  const fields = new Map<string, string>();
  for (const part of rrule.replace(/^RRULE:/i, "").split(";")) {
    const [k, v] = part.split("=");
    if (k && v) fields.set(k.toUpperCase(), v.toUpperCase());
  }
  const freq = fields.get("FREQ");
  if (!freq) return "";
  const interval = Number(fields.get("INTERVAL") ?? "1") || 1;
  const byDay = (fields.get("BYDAY") ?? "").split(",").filter(Boolean);
  const until = fields.get("UNTIL");
  const count = Number(fields.get("COUNT") ?? "");
  const unit = { DAILY: "day", WEEKLY: "week", MONTHLY: "month", YEARLY: "year" }[freq] ?? "period";
  const every = interval > 1 ? `Every ${interval} ${unit}s` : "";

  let base = "";
  if (freq === "DAILY") base = interval > 1 ? every : "Every day";
  else if (freq === "WEEKLY") {
    if (byDay.length === 0) base = interval > 1 ? every : "Every week";
    else {
      const names = byDay.map((d) => WD_SHORT[d.slice(-2)] ?? d);
      const weekdays = ["Mon", "Tue", "Wed", "Thu", "Fri"];
      if (names.length === 5 && weekdays.every((d) => names.includes(d))) {
        base = interval > 1 ? `Every ${interval} weeks on weekdays` : "Every weekday";
      } else {
        const list = names.length > 1 ? `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}` : names[0];
        base = `${interval > 1 ? `Every ${interval} weeks on` : "Every"} ${list}`;
      }
    }
  } else if (freq === "MONTHLY") {
    const ordinal = byDay.find((d) => /^[+-]?\d/.test(d));
    const monthday = Number(fields.get("BYMONTHDAY") ?? "");
    if (ordinal) {
      const n = ordinal.slice(0, -2);
      const suffix = n === "1" ? "st" : n === "2" ? "nd" : n === "3" ? "rd" : "th";
      base = `${every || "Every month"} on the ${n}${suffix} ${WD_LONG[ordinal.slice(-2)] ?? ordinal}`;
    } else if (Number.isFinite(monthday) && monthday > 0) {
      base = `${every || "Every month"} on day ${monthday}`;
    } else base = every || "Every month";
  } else if (freq === "YEARLY") base = every || "Every year";
  else base = "Repeats";

  const end = until
    ? ` until ${formatUntil(until)}`
    : Number.isFinite(count) && count > 0
      ? `, ${count} times`
      : "";
  return `${base}${end}`;
}

/** "20261217" / "20261217T235959Z" → "Dec 17, 2026" (digits read, no Date). */
export function formatUntil(value: string): string {
  const m = /^(\d{4})(\d{2})(\d{2})/.exec(value);
  if (!m) return value;
  return `${MONTHS[Number(m[2]) - 1] ?? ""} ${Number(m[3])}, ${m[1]}`;
}

// ── bill state, never from a mirror ──────────────────────────

export type BillReading =
  | { kind: "bill"; state: "paid"; paidFor: string | null }
  | { kind: "bill"; state: "due"; dueDate: string | null; daysUntil: number; amount: string | null; autopay: boolean }
  | { kind: "unlinked" }
  | { kind: "not-bill" };

/**
 * What a Bills-calendar entry means on `today`.
 *
 * A mirror event (event_type 'bill' + entity_type 'bill') is read through the
 * bills module: its state is the bill's state. Two rules matter and both are
 * R1 failures:
 *
 *  - A PAID BILL IS NEVER "DUE". The mirror repeats on the due date forever;
 *    the payment is what says this month's is done.
 *  - AN ENTRY THAT IS NOT A LINKED MIRROR IS NOT BILL STATE. The API retires
 *    the old hand-typed "Electric bill due — $180" entries (migration 073) and
 *    the MCP reports the leftovers it could not match as bill-like entries;
 *    here, anything on the Bills calendar without a bill link is labelled
 *    "Bills calendar · not linked to a bill" and carries no due/paid/amount.
 */
export function billReading(
  ev: Pick<CalEvent, "event_type" | "entity_type" | "entity_id">,
  bills: readonly BillRow[],
  today: string,
  opts: { onBillsCalendar?: boolean } = {},
): BillReading {
  // Anything sitting on the Bills calendar is either a bill's own mirror (and
  // then read through the bill) or an entry nobody linked to a bill — the
  // legacy "Electric bill due — $180" shape migration 073 retires, and the
  // bill-like entries the MCP reports with a reason. Neither is bill state.
  if (!isBillMirror(ev)) return opts.onBillsCalendar ? { kind: "unlinked" } : { kind: "not-bill" };
  const bill = ev.entity_type === "bill" && ev.entity_id ? bills.find((b) => b.id === ev.entity_id) : undefined;
  if (!bill) return { kind: "unlinked" };
  const due: BillDue | null = billDueFor(today, bill);
  const amount = formatMoney(bill.amount);
  if (due && due.status === "paid") return { kind: "bill", state: "paid", paidFor: due.paidFor };
  return {
    kind: "bill",
    state: "due",
    dueDate: due?.next ?? null,
    daysUntil: due?.daysUntil ?? 0,
    amount,
    autopay: !!bill.is_autopay,
  };
}

/** "142.18" → "$142.18"; null/"" stays null. */
export function formatMoney(amount: string | null | undefined): string | null {
  if (amount === null || amount === undefined || amount === "") return null;
  const n = Number(amount);
  if (!Number.isFinite(n)) return null;
  return n.toLocaleString("en-US", { style: "currency", currency: "USD" });
}

/** "Due in 3 days", "Due today", "Overdue by 2 days" — from the bill rule's day count. */
export function dueLabel(daysUntil: number): string {
  if (daysUntil === 0) return "Due today";
  if (daysUntil === 1) return "Due tomorrow";
  if (daysUntil < 0) return `Overdue by ${Math.abs(daysUntil)} ${Math.abs(daysUntil) === 1 ? "day" : "days"}`;
  return `Due in ${daysUntil} days`;
}

/** "Paid Sep 18" — the date a payment covered, from the bill rule. */
export function paidLabel(paidFor: string | null): string {
  return paidFor ? `Paid for ${formatDayLabel(paidFor, "")}` : "Paid";
}

// ── who added what ───────────────────────────────────────────

export interface Attribution {
  /** "Amber", "You", or "" when nothing is known. */
  who: string;
  viaAgent: boolean;
  agentName: string | null;
  /** "2 h ago" — relative to the page's clock. */
  when: string;
}

/**
 * The newest calendar change per event id, from GET /activity.
 *
 * Requirement 5 of the rebuild: show who added what where the data allows,
 * because cross-family visibility is proven and "did you book that?" is a real
 * argument. Where it does not allow — the feed keeps a page of changes, and an
 * event nobody touched recently has no entry — this returns nothing and the
 * sheet simply does not claim an author. It never guesses.
 */
export function attributionMap(
  entries: readonly ActivityRow[],
  viewerUserId: string | null | undefined,
  now: Date = new Date(),
): Map<string, Attribution> {
  const byEvent = new Map<string, Attribution>();
  for (const entry of entries) {
    if (entry.entity_type !== "calendar_event" || !entry.entity_id) continue;
    if (entry.action === "delete") continue;
    const existing = byEvent.get(entry.entity_id);
    // The feed comes newest first; keep the first (newest) entry per event.
    if (existing && !isNewer(entry.created_at, existing)) continue;
    const name = entry.actor?.name?.trim() ?? "";
    const mine = !!viewerUserId && entry.actor?.user_id === viewerUserId;
    byEvent.set(entry.entity_id, {
      // First name only: the shell greets by first name and the sheet has one
      // line, so "Amber via moltis-wg-amber" reads; "Amber Sims" crowds it.
      who: mine ? "You" : name.split(/\s+/)[0] ?? "",
      viaAgent: !!entry.via_agent,
      agentName: entry.agent?.name?.trim() || null,
      when: relativeWhen(entry.created_at, now),
    });
  }
  return byEvent;
}

function isNewer(iso: string, existing: Attribution): boolean {
  // Existing entries were seen earlier in a newest-first list, so a later
  // entry is older: nothing to compare beyond presence.
  void iso;
  void existing;
  return false;
}

/** "just now", "12 min ago", "2 h ago", "3 days ago" — from an RFC 3339 instant. */
export function relativeWhen(iso: string, now: Date = new Date()): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return "";
  const mins = Math.round((now.getTime() - t.getTime()) / 60_000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins} min ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours} h ago`;
  const days = Math.round(hours / 24);
  return `${days} ${days === 1 ? "day" : "days"} ago`;
}

/** "Added by Amber · 2 h ago", "Added by Amber via moltis-wg-amber", or "". */
export function attributionLine(a: Attribution | undefined): string {
  if (!a || !a.who) return "";
  const agent = a.viaAgent ? ` via ${a.agentName ?? "an agent"}` : "";
  return `Added by ${a.who}${agent}${a.when ? ` · ${a.when}` : ""}`;
}

// ── reminders ────────────────────────────────────────────────

/** The reminder choices the API accepts (minutes_before, 0..40320, max 10). */
export const REMINDER_CHOICES: readonly { minutes: number; label: string }[] = [
  { minutes: 0, label: "At the start" },
  { minutes: 5, label: "5 minutes before" },
  { minutes: 10, label: "10 minutes before" },
  { minutes: 15, label: "15 minutes before" },
  { minutes: 30, label: "30 minutes before" },
  { minutes: 60, label: "1 hour before" },
  { minutes: 120, label: "2 hours before" },
  { minutes: 1440, label: "1 day before" },
  { minutes: 2880, label: "2 days before" },
  { minutes: 10080, label: "1 week before" },
];

/** The API's cap on reminders per event. */
export const MAX_REMINDERS = 10;

/** A reminder's label; an unlisted value still reads as what it is. */
export function reminderLabel(minutes: number): string {
  const known = REMINDER_CHOICES.find((c) => c.minutes === minutes);
  if (known) return known.label;
  if (minutes % 1440 === 0) return `${minutes / 1440} days before`;
  if (minutes % 60 === 0) return `${minutes / 60} hours before`;
  return `${minutes} minutes before`;
}

/** "15 minutes before, 1 hour before", or "No reminders". */
export function remindersLine(reminders: readonly { minutes_before: number }[] | undefined): string {
  if (!reminders || reminders.length === 0) return "No reminders";
  return reminders.map((r) => reminderLabel(r.minutes_before)).join(", ");
}

// ── the range the page asks for ──────────────────────────────

/**
 * The window a view needs, as YYYY-MM-DD dates. The API expands recurrences
 * only inside [start, end), so the page ALWAYS sends both (the manager's note
 * on #4550 and architecture/api-calendar-recurrence.md): without them a series
 * comes back once, as stored, and the agenda would show one lesson instead of
 * the term's.
 *
 * Month asks for the month plus a week either side — enough that the six-week
 * grid's first Sunday is always inside the answer — and `monthGrid` trims the
 * API's day table to it. The grid's columns therefore come from the API's
 * weekdays, not from arithmetic here.
 */
export function rangeFor(anchor: string, view: CalendarView): { start: string; end: string } {
  if (view === "day") return { start: anchor, end: plusDays(anchor, 1) };
  if (view === "week" || view === "agenda") return { start: anchor, end: plusDays(anchor, 7) };
  const first = monthStart(anchor);
  return { start: plusDays(first, -7), end: plusDays(first, 43) };
}

/**
 * The month view's six weeks (42 days), starting on the first Sunday the API's
 * day table names at or after the month's first day, so the grid's rows are
 * whole weeks. Fewer days come back only when the API's table is short.
 */
export function monthGrid(firstOfMonth: string, days: readonly DayInfo[]): DayInfo[] {
  const inMonth = days.findIndex((d) => d.local_date === firstOfMonth);
  if (inMonth < 0) return days.slice(0, 42);
  const back = weekdayNumber(days, firstOfMonth);
  const start = Math.max(0, inMonth - back);
  return days.slice(start, start + 42);
}

/**
 * The weekday's index (0 = Sunday) from the API's own day table: the label the
 * server sent, mapped to a column. Nothing here dates a day.
 */
export function weekdayNumber(days: readonly DayInfo[], localDate: string): number {
  const day = days.find((d) => d.local_date === localDate);
  if (!day) return 0;
  return WEEKDAY_NAMES.indexOf(day.weekday);
}

/** Sunday-first, the calendar's column order. */
export const WEEKDAY_NAMES: readonly string[] = [
  "Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday",
];

/** "Sun", "Mon", … for a header cell. */
export function weekdayShort(weekday: string): string {
  return (weekday ?? "").slice(0, 3);
}

/** `date` plus `n` days, as YYYY-MM-DD. */
export function plusDays(date: string, n: number): string {
  const day = parseCivil(date);
  if (day === null) return date;
  const d = new Date((day + n) * 86_400_000);
  return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, "0")}-${String(d.getUTCDate()).padStart(2, "0")}`;
}

/** The first day of the month `anchor` is in. */
export function monthStart(anchor: string): string {
  return `${anchor.slice(0, 7)}-01`;
}

// ── the empty state ──────────────────────────────────────────

/**
 * Why the agenda is empty, in the page's words. A blank grid teaches nothing;
 * this is what the empty card says, and it changes with the filter, because
 * "nothing this week" and "you hid every calendar" are different truths.
 */
export function emptyReason(opts: {
  hiddenCount: number;
  calendarCount: number;
  hasAnyEvents: boolean;
  rangeLabel: string;
  canWrite: boolean;
}): { title: string; body: string; action: "new" | "show-all" | null } {
  if (opts.hiddenCount > 0) {
    return {
      title: `Nothing shown for ${opts.rangeLabel}`,
      body: `${opts.hiddenCount === 1 ? "One calendar is" : `${opts.hiddenCount} calendars are`} hidden. That may be all this week holds.`,
      action: "show-all",
    };
  }
  if (!opts.hasAnyEvents && opts.calendarCount === 0) {
    return {
      title: "No calendars yet",
      body: "Once the family has a calendar, its events appear here. Add one from your phone's calendar app, or ask your agent to create the first event.",
      action: null,
    };
  }
  return {
    title: `Nothing on the calendar for ${opts.rangeLabel}`,
    body: opts.canWrite
      ? "Add something — a lesson, an appointment, a day off — and everyone in the family sees it here. Repeating events show once with all their dates."
      : "Only the family can add events. Everything they add appears here for you to read.",
    action: opts.canWrite ? "new" : null,
  };
}

// ── the editor's values, and what they become ────────────────

/**
 * The event form's values. A person types a DATE AND A TIME in the household's
 * zone; the API takes an instant plus a `timezone` for a series, so a weekly
 * 4:00 PM lesson stays at 4:00 PM across a DST change.
 */
export interface EventFormValues {
  title: string;
  calendarId: string;
  allDay: boolean;
  /** YYYY-MM-DD, in the household's zone. */
  startDate: string;
  /** HH:MM, 24-hour, household zone. */
  startTime: string;
  endDate: string;
  endTime: string;
  /** The RRULE value, or "" for a one-off. */
  repeat: string;
  /** minutes_before values. */
  reminders: number[];
  location: string;
  notes: string;
  eventType: string;
}

/**
 * The rule the form's repeat chips and sub-forms add up to.
 *
 * An empty string means "doesn't repeat". The sub-forms edit the RRULE in
 * place (BYDAY / BYMONTHDAY / UNTIL / COUNT), so this only has to keep the
 * pieces ordered and drop the ones the API would refuse.
 */
export function recurrenceFromForm(v: Pick<EventFormValues, "repeat">): string {
  const raw = (v.repeat ?? "").replace(/^RRULE:/i, "").trim();
  if (!raw) return "";
  const seen = new Set<string>();
  const parts: string[] = [];
  for (const part of raw.split(";")) {
    const [k, val] = part.split("=");
    if (!k || !val) continue;
    const key = k.toUpperCase();
    // One value per field: a hand-edited duplicate would be refused.
    if (seen.has(key)) continue;
    seen.add(key);
    parts.push(`${key}=${val.toUpperCase()}`);
  }
  if (!seen.has("FREQ")) return "";
  // COUNT and UNTIL together are invalid; UNTIL wins in the UI.
  if (seen.has("UNTIL") && seen.has("COUNT")) return parts.filter((p) => !p.startsWith("COUNT=")).join(";");
  return parts.join(";");
}

/** Whether a rule would repeat forever, which the API accepts but a person should see. */
export function repeatsForever(rrule: string | null | undefined): boolean {
  if (!rrule) return false;
  return !/UNTIL=|COUNT=/.test(rrule);
}

/**
 * The instant an hour/minute on a civil date is, in a named zone, as RFC 3339.
 *
 * This is the ONE place the page builds an instant from what a person typed,
 * and it has to: a write needs a moment, and the person meant "4:00 PM here".
 * The conversion goes through Intl so the offset is the ZONE's on that date
 * (EST or EDT), not the browser's — a phone on holiday in London still books
 * the family's 4 PM.
 *
 * Returns "" when the parts cannot be parsed; the caller refuses the save.
 */
export function instantInZone(date: string, time: string, timezone: string | null | undefined): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date ?? "");
  const t = /^(\d{2}):(\d{2})$/.exec(time ?? "");
  if (!m || !t) return "";
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
  const [hh, mm] = [Number(t[1]), Number(t[2])];
  if (!timezone) {
    // No zone known: the API reads a naive local wall clock as the household's
    // zone for a series, and a browser's own zone is the best local answer.
    const local = new Date(y, mo - 1, d, hh, mm, 0, 0);
    return Number.isNaN(local.getTime()) ? "" : local.toISOString();
  }
  // Start from the same wall clock in UTC, then measure how the zone reads
  // that instant and correct by the difference. Two passes settle DST edges.
  let guess = Date.UTC(y, mo - 1, d, hh, mm, 0, 0);
  for (let i = 0; i < 2; i++) {
    const parts = zoneParts(guess, timezone);
    if (!parts) break;
    const asUtc = Date.UTC(parts.y, parts.mo - 1, parts.d, parts.h, parts.mi, 0, 0);
    const want = Date.UTC(y, mo - 1, d, hh, mm, 0, 0);
    guess += want - asUtc;
  }
  return new Date(guess).toISOString();
}

/** The wall clock a named zone shows for an instant. */
export function zoneParts(epochMs: number, timezone: string): { y: number; mo: number; d: number; h: number; mi: number } | null {
  try {
    const fmt = new Intl.DateTimeFormat("en-CA", {
      timeZone: timezone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    });
    const parts = fmt.formatToParts(new Date(epochMs));
    const get = (k: string) => parts.find((p) => p.type === k)?.value ?? "";
    const y = Number(get("year"));
    const mo = Number(get("month"));
    const d = Number(get("day"));
    const h = Number(get("hour")) % 24;
    const mi = Number(get("minute"));
    if (![y, mo, d, h, mi].every((n) => Number.isFinite(n))) return null;
    return { y, mo, d, h, mi };
  } catch {
    return null;
  }
}

/** Midnight UTC of a civil date — the form an all-day event is stored in. */
export function civilInstant(date: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date ?? "");
  if (!m) return "";
  return new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]))).toISOString();
}

/** The date one day after a civil date (an all-day event's exclusive end). */
export function dayAfter(date: string): string {
  return plusDays(date, 1);
}

/** The request body a form's values become. */
export function eventBody(
  v: EventFormValues,
  timezone: string | null | undefined,
): {
  title: string;
  description: string;
  location: string;
  event_type: string;
  start: string;
  end: string;
  all_day: boolean;
  rrule: string;
  timezone?: string;
  reminders: { minutes_before: number }[];
} {
  const allDay = v.allDay;
  const start = allDay ? civilInstant(v.startDate) : instantInZone(v.startDate, v.startTime || "09:00", timezone);
  // An all-day event's stored end is the exclusive next midnight; a timed one
  // ends on its end date.
  const end = allDay
    ? civilInstant(dayAfter(v.endDate && v.endDate >= v.startDate ? v.endDate : v.startDate))
    : instantInZone(v.endDate || v.startDate, v.endTime || v.startTime || "10:00", timezone);
  const rule = recurrenceFromForm(v);
  const body: {
    title: string;
    description: string;
    location: string;
    event_type: string;
    start: string;
    end: string;
    all_day: boolean;
    rrule: string;
    timezone?: string;
    reminders: { minutes_before: number }[];
  } = {
    title: v.title.trim(),
    description: v.notes.trim(),
    location: v.location.trim(),
    event_type: v.eventType || "family",
    start,
    end,
    all_day: allDay,
    rrule: rule,
    reminders: v.reminders.slice(0, MAX_REMINDERS).map((minutes) => ({ minutes_before: minutes })),
  };
  // A timed series repeats in a zone (the API's own default is the household's,
  // but sending it says what we mean); an all-day one never carries a zone.
  if (!allDay && rule && timezone) body.timezone = timezone;
  return body;
}

// ── the view's date label (the toolbar's title) ──────────────

/**
 * The toolbar's range title, from the day table the API sent.
 *   agenda/day  "Tue, Nov 17"
 *   week        "Nov 16 – 22, 2026"
 *   month       "November 2026"
 * The weekday and the digits both come from the payload, so the title can never
 * disagree with the grid under it.
 */
export function rangeTitle(days: readonly DayInfo[], view: CalendarView): string {
  const first = days[0];
  const last = days[days.length - 1];
  if (!first) return "";
  const LONG = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
  const fullMonth = (d: string) => LONG[Number((d ?? "").slice(5, 7)) - 1] ?? monthYear(d).split(" ")[0] ?? "";
  const year = (d: string) => (d ?? "").slice(0, 4);
  if (view === "day" || view === "agenda") {
    return days.length > 1 ? `${weekdayShort(first.weekday)}, ${monthDay(first.local_date)}` : `${weekdayShort(first.weekday)}, ${monthDay(first.local_date)}`;
  }
  if (view === "week") {
    if (!last) return monthDay(first.local_date);
    const sameMonth = first.local_date.slice(0, 7) === last.local_date.slice(0, 7);
    return `${monthDay(first.local_date)} – ${sameMonth ? Number(last.local_date.slice(8, 10)) : monthDay(last.local_date)}, ${year(last.local_date)}`;
  }
  // Month: the anchor day is the middle of the requested window (the page asks
  // for a month plus a week either side), so its month is the one on screen.
  const anchor = days[Math.floor(days.length / 2)] ?? first;
  return `${fullMonth(anchor.local_date)} ${year(anchor.local_date)}`;
}

/**
 * The week's title from its first and last day, both from the API's table:
 * "Nov 16 – 22, 2026", or "Nov 30 – Dec 6, 2026" when the week straddles a
 * month. Empty when the table did not arrive — the toolbar says "Calendar"
 * rather than a title nobody can trust.
 */
export function weekRangeLabel(days: readonly DayInfo[]): string {
  if (days.length === 0) return "";
  const first = days[0];
  const last = days[days.length - 1];
  const year = (last.local_date ?? "").slice(0, 4);
  const sameMonth = first.local_date.slice(0, 7) === last.local_date.slice(0, 7);
  if (sameMonth) return `${monthDay(first.local_date)} – ${Number(last.local_date.slice(8, 10))}, ${year}`;
  return `${monthDay(first.local_date)} – ${monthDay(last.local_date)}, ${year}`;
}

// ── when the API's day table is missing ─────────────────────

/**
 * Days built from the events themselves, for an API that did not send the
 * range's day table.
 *
 * Each event carries its OWN local_date and weekday (localday.go), so the
 * agenda can still group by day and print the right weekday without the table
 * and without the page ever dating anything. What it cannot do is name a day
 * that has no events (there is no data about it) or draw a grid's columns, so
 * the page says that in words instead of guessing.
 */
export function daysFromEvents(events: readonly CalEvent[]): DayInfo[] {
  const seen = new Map<string, DayInfo>();
  for (const ev of events) {
    const date = ev.local_date ?? "";
    const weekday = ev.weekday ?? "";
    if (!date || !weekday || seen.has(date)) continue;
    seen.set(date, { local_date: date, weekday });
  }
  return Array.from(seen.values()).sort((a, b) => a.local_date.localeCompare(b.local_date));
}

/** True when the payload carried no day table, so grid headers and quiet days are unavailable. */
export function missingDayTable(daysFromApi: readonly DayInfo[] | undefined): boolean {
  return !daysFromApi || daysFromApi.length === 0;
}
