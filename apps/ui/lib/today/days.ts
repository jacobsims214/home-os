/**
 * Is this day past, today, or still ahead — the ONE rule for the whole UI
 * (#5305, War Game R2 G8).
 *
 * The Meals week view nagged about a dinner that had already gone: on
 * Wednesday Nov 18 it read "Mon is open — November 16 has no dinner planned.
 * Plan Mon's dinner". "Is this day still open?" was being answered as "which
 * row of the week is empty first", with the week's Monday as the reference
 * instead of the family's today (lib/recipes/week.ts).
 *
 * The reason it could happen is that the question had no home. It is asked all
 * over the app — a chore's due bucket, a document's expiry, a calendar row's
 * `past` flag, a maintenance task's "5 days overdue" — and each place writes
 * its own comparison (`days < 0`, `n === 0`, `localDate > today`) against its
 * own day-difference copy (there are four: lib/today/due.ts,
 * lib/documents/model.ts, lib/lists/model.ts,
 * components/maintenance/MaintenanceKanban.tsx). Four copies of the arithmetic
 * and no single answer to "is this day gone".
 *
 * This module is that answer, and it adds NO arithmetic of its own: the date →
 * day-number conversion is lib/today/due.ts's `parseCivil`, which already
 * guarantees a timezone or a DST change cannot move a civil date by a day. It
 * only names the comparison, so a caller cannot get the boundary wrong by
 * writing `<` where it meant `<=`.
 *
 * WHO USES IT TODAY (#5305): the Meals week view, through lib/recipes/week.ts
 * (`isToday` / `isPast` on each row, and `firstEmptyDay`). That is the surface
 * that had the bug.
 *
 * STILL TO MIGRATE, deliberately not done here: the four due-date sites above
 * keep their own difference arithmetic because each also needs the NUMBER of
 * days (a 45-day expiry window, a 7-day "this week" bucket, "5 days overdue"),
 * not just the position, so switching them is a behaviour-preserving refactor
 * of their own rather than a line in this one. They are named here so the next
 * agent adopts this rule instead of writing a fifth comparison — and so nobody
 * assumes the whole app is already on it.
 *
 * CIVIL DATES ONLY, and `today` MUST be the family's day. Both arguments are
 * YYYY-MM-DD, so the answer is about the household's calendar, never the
 * browser's or the server's. Pass `today` from `householdToday(timezone)`
 * (lib/today/agenda.ts), the one function that asks the household's zone what
 * day it is. A UTC date here would call tonight "past" at 23:30 in New York —
 * the other half of the same bug.
 *
 * An unparseable argument answers `null`, never a guess: "I don't know what day
 * that is" is not "past", and a nag suppressed on a bad date is worse than no
 * nag at all.
 *
 * Pure, and no imports beyond the sibling civil-date module, so `node --test`
 * loads it directly.
 */
import { parseCivil } from "./due.ts";

/** Where a civil date stands against the family's today. */
export type DayPosition = "past" | "today" | "future";

/**
 * A day number for a STRICT YYYY-MM-DD, or null.
 *
 * `parseCivil` alone is not enough here: it accepts a full timestamp by taking
 * its first ten characters, which for an ISO instant is the UTC day. Passing
 * `"2026-11-19T03:30:00Z"` would then quietly mean Nov 19 — the same
 * timezone mistake in a new place. A day-position question is only ever
 * meaningful about a calendar date, so anything with a time on it is refused.
 */
function strictDay(value: string | null | undefined): number | null {
  if (typeof value !== "string") return null;
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value.trim())) return null;
  return parseCivil(value.trim());
}

/**
 * Where `date` stands against `today` (both YYYY-MM-DD), or null when either is
 * not a real civil date.
 */
export function dayPosition(
  date: string | null | undefined,
  today: string | null | undefined,
): DayPosition | null {
  const d = strictDay(date);
  const t = strictDay(today);
  if (d === null || t === null) return null;
  if (d < t) return "past";
  if (d === t) return "today";
  return "future";
}

/**
 * The one predicate for "worth nagging about": the day is today or still to
 * come. False for a past day AND for a date that cannot be read — a nag is only
 * ever shown when the answer is certainly yes.
 *
 * This is the rule the Meals page broke (#5305): anything that asks a family to
 * do something on a day asks it through here.
 */
export function isTodayOrFutureDay(
  date: string | null | undefined,
  today: string | null | undefined,
): boolean {
  const position = dayPosition(date, today);
  return position === "today" || position === "future";
}
