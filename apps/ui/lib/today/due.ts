/**
 * When a bill is due — the browser's copy of the ONE bill due rule
 * (`packages/billdue`, KB architecture/bill-due-rule.md, tickets #4468/#4819).
 *
 * Why a copy at all: the rule lives in Go (`billdue.BillDueFor`: the API's
 * due-soon query, the MCP's `bill_find`/`budget_summary`, the calendar mirror
 * and the worker all call it), and the browser cannot call Go. This file
 * therefore re-expresses the same rule for the one screen that must show a
 * next-due date without a server round trip per bill: Today.
 *
 * THE RULE, unchanged: a monthly bill is due on `due_day` (1-31, clamped to
 * short months); quarterly/semiannual/annual anchor on `due_date` and step
 * 3/6/12 months on its day of the month (clamped); a `once` bill is due only
 * on its `due_date` and STAYS due after it passes until paid. The next due
 * date is the next one that still needs paying, and the status is `paid` when
 * the last payment covers the current due date. Coverage is
 * `paid_for_due_date` when stored, otherwise inferred from `paid_date` with a
 * 7-day grace (a payment 7 days late still pays the date it follows; 8 days
 * late pays the next one).
 *
 * `due.test.mjs` replays `apps/mcp/internal/bill/due_test.go`'s
 * TestBillDueFrequencies / TestPaidForDueDateFrequencies table by table, so a
 * change to either copy fails the other's test.
 *
 * READ BILL STATE FROM THE BILLS ENDPOINT, NEVER FROM A CALENDAR EVENT. This
 * was a real R1 failure (KB discoveries/war-game-r1-*.md): an agent told the
 * family "Electric $180 due, still unpaid" about a bill paid that morning,
 * because a stale calendar event looked like bill state. Every bill has a
 * mirror event on the Bills calendar (`packages/billcal`) whose text carries
 * the amount; that event says when a bill falls due and nothing about whether
 * it was paid. `isBillMirrorEvent` keeps those events out of the agenda for
 * the same reason.
 *
 * Civil dates only: every date here is YYYY-MM-DD, handled as a day number
 * (days since 1970-01-01, computed in UTC), so DST and timezones cannot move
 * a due date by a day. `today` must be the HOUSEHOLD's date (householdToday
 * in agenda.ts), not the browser's.
 */

/** bills.frequency; "" is read as monthly (rows that predate migration 044). */
export type BillFrequency = "monthly" | "quarterly" | "semiannual" | "annual" | "once" | "";

/** The bills columns the rule needs (GET /api/v1/bills). */
export interface BillSchedule {
  frequency?: string | null;
  /** bills.due_day: monthly bills only. */
  due_day?: number | null;
  /** bills.due_date (YYYY-MM-DD): bills that are not monthly. */
  due_date?: string | null;
}

/** Where a bill stands on a given day, as the Go rule reports it. */
export interface BillDue {
  /** The next due date that still needs paying (YYYY-MM-DD), null when none remains. */
  next: string | null;
  /** Whole days from today to `next`; only an unpaid past `once` bill is negative. */
  daysUntil: number;
  /** "paid" when the last payment covers the current due date, else "due". */
  status: "due" | "paid";
  /** The due date the last payment covers (YYYY-MM-DD), null when never paid. */
  paidFor: string | null;
}

// ── civil dates as day numbers ────────────────────────────────

const DAY_MS = 86_400_000;
const CIVIL_RE = /^(\d{4})-(\d{2})-(\d{2})$/;

/** A YYYY-MM-DD date as a day number, or null when it isn't one. */
export function parseCivil(value: string | null | undefined): number | null {
  if (!value) return null;
  const m = CIVIL_RE.exec(value.slice(0, 10));
  if (!m) return null;
  const y = Number(m[1]);
  const mo = Number(m[2]);
  const d = Number(m[3]);
  if (mo < 1 || mo > 12 || d < 1 || d > 31) return null;
  const n = Date.UTC(y, mo - 1, d) / DAY_MS;
  // Reject 2026-02-31 and friends: Date.UTC normalises them forward.
  return formatCivil(n) === `${m[1]}-${m[2]}-${m[3]}` ? n : null;
}

/** A day number as YYYY-MM-DD. */
export function formatCivil(day: number): string {
  const d = new Date(day * DAY_MS);
  const y = d.getUTCFullYear();
  const m = String(d.getUTCMonth() + 1).padStart(2, "0");
  const dayOfMonth = String(d.getUTCDate()).padStart(2, "0");
  return `${y}-${m}-${dayOfMonth}`;
}

/** The calendar parts of a day number. */
function parts(day: number): { y: number; m: number; d: number } {
  const t = new Date(day * DAY_MS);
  return { y: t.getUTCFullYear(), m: t.getUTCMonth() + 1, d: t.getUTCDate() };
}

function daysInMonth(y: number, m: number): number {
  return new Date(Date.UTC(y, m, 0)).getUTCDate();
}

/** Consecutive month numbering, the same arithmetic the Go rule uses. */
function monthIndex(day: number): number {
  const { y, m } = parts(day);
  return y * 12 + (m - 1);
}

/** `day` of the month at month index `idx`, clamped to that month's length. */
function inMonth(idx: number, day: number): number {
  const y = Math.floor(idx / 12);
  const m = (idx % 12) + 1;
  return Date.UTC(y, m - 1, Math.min(day, daysInMonth(y, m))) / DAY_MS;
}

function mod(a: number, n: number): number {
  return ((a % n) + n) % n;
}

// ── the cadence ───────────────────────────────────────────────

interface Cadence {
  /** Months between due dates: 1, 3, 6, 12, or 0 for a once bill. */
  step: number;
  /** Day of the month a due date falls on. */
  day: number;
  /** The anchor's month index (irrelevant for monthly bills). */
  anchorMonth: number;
  /** The first possible due date. */
  anchor: number;
}

/** Bills due every `step` months. "" is monthly. */
function frequencyMonths(frequency: string): number | null {
  switch (frequency) {
    case "monthly":
    case "":
      return 1;
    case "quarterly":
      return 3;
    case "semiannual":
      return 6;
    case "annual":
      return 12;
    case "once":
      return 0;
    default:
      return null;
  }
}

/**
 * Resolves a bill's schedule to a cadence. Null when the schedule cannot
 * produce a due date: an unknown frequency, a monthly bill without a
 * `due_day` in 1-31, or another bill without a `due_date`.
 */
export function cadence(s: BillSchedule): Cadence | null {
  const step = frequencyMonths(s.frequency ?? "");
  if (step === null) return null;
  if (step === 1) {
    const day = s.due_day;
    if (typeof day !== "number" || day < 1 || day > 31) return null;
    // A monthly bill has no anchor; the same fixed January 2000 anchor the Go
    // rule uses keeps the arithmetic identical.
    return { step: 1, day, anchorMonth: 2000 * 12, anchor: Date.UTC(2000, 0, day) / DAY_MS };
  }
  const anchor = parseCivil(s.due_date);
  if (anchor === null) return null;
  return { step, day: parts(anchor).d, anchorMonth: monthIndex(anchor), anchor };
}

/** The first due date on or after `day`; null when there is none. */
function onOrAfter(c: Cadence, day: number): number | null {
  if (c.step === 0 || day < c.anchor) return c.anchor >= day ? c.anchor : null;
  let idx = monthIndex(day);
  idx += mod(c.anchorMonth - idx, c.step);
  let due = inMonth(idx, c.day);
  if (due < day) due = inMonth(idx + c.step, c.day);
  return due;
}

/** The last due date on or before `day`; null when there is none. */
function onOrBefore(c: Cadence, day: number): number | null {
  if (c.step === 0 || day < c.anchor) return c.anchor <= day ? c.anchor : null;
  let idx = monthIndex(day);
  idx -= mod(idx - c.anchorMonth, c.step);
  let due = inMonth(idx, c.day);
  if (due > day) due = inMonth(idx - c.step, c.day);
  return due;
}

/** The due date one step after `due`; null for a once bill. */
function after(c: Cadence, due: number): number | null {
  if (c.step === 0) return null;
  return inMonth(monthIndex(due) + c.step, c.day);
}

/** The due date a payment made on `paid` covers (the 7-day grace rule). */
export function paidForDueDate(paid: number, c: Cadence): number {
  const last = onOrBefore(c, paid);
  if (last !== null && paid - last <= 7) return last;
  const next = onOrAfter(c, paid);
  if (next !== null) return next;
  return c.anchor;
}

/** The due date the last payment covers; null when the bill was never paid. */
function coveredDueDate(c: Cadence, paidDate: number | null, paidFor: number | null): number | null {
  if (paidFor !== null) return paidFor;
  if (paidDate === null) return null;
  return paidForDueDate(paidDate, c);
}

/**
 * Where a bill stands on `today` (a day number), or null when the schedule is
 * unusable. The exact arithmetic of the Go `billDue`.
 */
function billDue(
  today: number,
  s: BillSchedule,
  paidDate: number | null,
  paidFor: number | null,
): { next: number | null; status: "due" | "paid"; paidFor: number | null } | null {
  const c = cadence(s);
  if (!c) return null;
  let next: number | null = onOrAfter(c, today);
  // A once bill past its date is still due until it is paid.
  if (next === null) next = c.anchor;
  let status: "due" | "paid" = "due";
  const covered = coveredDueDate(c, paidDate, paidFor);
  if (covered === null) return { next, status, paidFor: null };
  if (next === null || covered < next) return { next, status, paidFor: covered };
  status = "paid";
  while (next !== null && covered >= next) next = after(c, next);
  return { next, status, paidFor: covered };
}

/**
 * When a bill is due as of `today` (the HOUSEHOLD's date, YYYY-MM-DD), from a
 * GET /api/v1/bills row. Null when the bill has no usable schedule — count
 * those (`withoutDueDate`) rather than silently dropping them.
 */
export function billDueFor(
  today: string,
  bill: BillSchedule & { paid_date?: string | null; paid_for_due_date?: string | null },
): BillDue | null {
  const t = parseCivil(today);
  if (t === null) return null;
  const info = billDue(t, bill, parseCivil(bill.paid_date), parseCivil(bill.paid_for_due_date));
  if (!info) return null;
  return {
    next: info.next === null ? null : formatCivil(info.next),
    daysUntil: info.next === null ? 0 : info.next - t,
    status: info.status,
    paidFor: info.paidFor === null ? null : formatCivil(info.paidFor),
  };
}

/** Whole days from `from` to `to` (both YYYY-MM-DD); null if either is bad. */
export function daysBetween(from: string, to: string): number | null {
  const a = parseCivil(from);
  const b = parseCivil(to);
  if (a === null || b === null) return null;
  return b - a;
}

/** `date` plus `days`, as YYYY-MM-DD. */
export function addDays(date: string, days: number): string {
  const n = parseCivil(date);
  if (n === null) return date;
  return formatCivil(n + days);
}
