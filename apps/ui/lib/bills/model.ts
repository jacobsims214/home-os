/**
 * Bills pages (#4551): the pure logic behind the list and the detail page.
 *
 * WHERE BILL STATE COMES FROM — and why it is two reads, not one.
 *
 *  - `GET /api/v1/bills` carries the record itself (name, amount, frequency,
 *    due_day / due_date, subject, notes) plus `paid_date` and
 *    `paid_for_due_date` exactly as stored. It does NOT compute a next due
 *    date: KB api/budget-categories-obligations.md, "No next-due computation
 *    in the API".
 *  - `GET /api/v1/budget/due-soon` (#4819) is the ONE place the REST API
 *    reports when a bill falls due and whether it is paid: each item carries
 *    `due_date`, `due_weekday` (the API's own weekday), `days_until_due` and
 *    `status` ("due" | "paid"), computed by the shared Go rule
 *    `billdue.BillDueFor` in the household's timezone.
 *
 * So these pages NEVER work out a weekday or a due date themselves. They join
 * the two reads by bill id. That is the R1 lesson (KB discoveries/war-game-r1.md):
 * an agent told the family "Electric $180 due, still unpaid" about a bill paid
 * that morning. A paid bill must never read as due, and the only trustworthy
 * answer is the API's `status`.
 *
 * A bill that due-soon does not report (no usable schedule, or further out
 * than the window) keeps its stored schedule ("Yearly, Dec 15") and says the
 * next date is not reported — it is never given an invented one.
 *
 * MONEY IS ABSENT, NOT ZERO (#4806/#4912): a role without money read gets no
 * amount field at all, so a missing amount renders "No amount" and never
 * "$0.00". Nothing here defaults a missing number to 0.
 *
 * Plain data and pure functions only (no JSX, no Date for calendar maths), so
 * `npm test` (node --test) covers it. Dates are civil strings handled as
 * calendar arithmetic, so no browser timezone can move a due date.
 */

// ── types (the API's JSON, as the pages read it) ──────────────

export type BillFrequency = "monthly" | "quarterly" | "semiannual" | "annual" | "once";

/**
 * The four states of the ONE attention rule (`billdue.AttentionFor`, #5498),
 * as the API sends them in `attention`.
 *
 * THE UI NEVER DERIVES THESE FROM `is_autopay`. A bill that says it pays itself
 * but has no account on record is NOT automatic — it is `needs_setup`, and the
 * family has to act. Re-deriving that here would be the second copy of the rule
 * this codebase keeps deleting (KB architecture/bill-attention-rule.md; worker
 * standing instructions §6, "one rule, one home"). If a component is about to
 * write `if (autopay)`, it is wrong: read `attention`.
 */
export type Attention = "automatic" | "needs_setup" | "due_soon" | "overdue";

/**
 * The payment account a bill draws on, as the API serializes it (075).
 * `last_four` is four digits and nothing else: `payment_accounts` has no column
 * for a full number, so there is nothing longer to leak. The key is absent
 * entirely for a role without money — the API omits it rather than blanking it,
 * so "no account" and "not allowed to see the account" are the same shape here
 * and the UI must never read the absence as "there is no account".
 */
export interface PaymentAccountRef {
  id: string;
  name: string;
  institution?: string | null;
  last_four?: string | null;
  account_type?: string | null;
}

/**
 * Who a bill is paid to (075). `vendor_id` is present only when the payee is a
 * vendors row; a free-text payee has a name and no contact details, because
 * there is nowhere to keep them. `phones` is a list in JSON although
 * `vendors.phone` is one column, so the field can grow without a rename.
 */
export interface Payee {
  vendor_id?: string | null;
  name: string;
  phones?: string[] | null;
  email?: string | null;
  website?: string | null;
}

/** A row of GET /api/v1/bills. Every field the pages use; all optional but id/name. */
export interface Bill {
  id: string;
  name: string;
  /** Decimal string ("182.40"). Absent/null when the role may not see money. */
  amount?: string | number | null;
  due_day?: number | null;
  due_date?: string | null;
  category?: string | null;
  frequency?: string | null;
  budget_category_id?: string | null;
  property_id?: string | null;
  vehicle_id?: string | null;
  asset_id?: string | null;
  vendor_id?: string | null;
  is_autopay?: boolean | null;
  /**
   * bills.is_autopay under the name the UI reads (#5498). This is the SWITCH
   * ("pay this one itself?"), not the answer ("does anything actually pay it?").
   * The answer is `attention`.
   */
  autopay_enabled?: boolean;
  /** The account an autopay bill draws on. Absent when hidden or unlinked. */
  payment_account?: PaymentAccountRef | null;
  /** Who is paid. Present for every role that may read the bill (vendors are not money). */
  payee?: Payee | null;
  /** The ONE rule's answer: automatic | needs_setup | due_soon | overdue. */
  attention?: Attention | string;
  account_number?: string | null;
  payment_url?: string | null;
  notes?: string | null;
  /** The last payment, as stored (written by the MCP bill_pay). */
  paid_date?: string | null;
  /** The due date that payment covers (migration 052). */
  paid_for_due_date?: string | null;
  created_at?: string;
  updated_at?: string;
}

/** One item of GET /api/v1/budget/due-soon (detail=items). */
export interface DueItem {
  source: "bill" | "loan" | string;
  id: string;
  name: string;
  amount: string | null;
  /** YYYY-MM-DD in the family's timezone. */
  due_date: string;
  /** The API's weekday, e.g. "Tue". Never derived here. */
  due_weekday: string;
  days_until_due: number;
  status: "due" | "paid" | string;
  autopay: boolean;
  subject?: { type: string; id: string; name: string } | null;
}

/** The due-soon payload, as much of it as the pages use. */
export interface DueSoon {
  from?: string;
  to?: string;
  days?: number;
  total?: string;
  bill_count?: number;
  loan_count?: number;
  items?: DueItem[];
  coverage?: { items_listed?: number; items_not_listed?: number; without_due_date?: number };
}

/** One bill with its due reading joined on (null when the API did not report one). */
export interface BillRow {
  bill: Bill;
  due: DueItem | null;
}

// ── money and labels ─────────────────────────────────────────

/** "$1,800.00" from a decimal string; null stays null (money absent, never 0). */
export function formatMoney(value: string | number | null | undefined): string | null {
  if (value === null || value === undefined || value === "") return null;
  const n = Number(value);
  if (!Number.isFinite(n)) return null;
  return n.toLocaleString("en-US", { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

/** The frequency labels the mockup uses (artifact #102). */
export const FREQUENCY_LABELS: Record<string, string> = {
  monthly: "Monthly",
  quarterly: "Quarterly",
  semiannual: "Every 6 months",
  annual: "Yearly",
  once: "Once",
};

/** The frequencies in the order the form offers them. */
export const FREQUENCY_OPTIONS: { value: BillFrequency; label: string }[] = [
  { value: "monthly", label: "Monthly" },
  { value: "quarterly", label: "Quarterly" },
  { value: "semiannual", label: "Every 6 months" },
  { value: "annual", label: "Yearly" },
  { value: "once", label: "Once" },
];

/** A bills.frequency as a person reads it; "" and unknown read as Monthly (the API default). */
export function frequencyLabel(frequency: string | null | undefined): string {
  return FREQUENCY_LABELS[normaliseFrequency(frequency)] ?? "Monthly";
}

/** The frequency as one of the five stored values (the API's default is monthly). */
export function normaliseFrequency(frequency: string | null | undefined): BillFrequency {
  switch (frequency) {
    case "quarterly":
    case "semiannual":
    case "annual":
    case "once":
      return frequency;
    default:
      return "monthly";
  }
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** "2026-12-15" → "Dec 15". Month and day only: no weekday, no timezone. */
export function dayMonth(civil: string | null | undefined): string {
  if (!civil) return "";
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(civil);
  if (!m) return "";
  const month = Number(m[2]);
  if (month < 1 || month > 12) return "";
  return `${MONTHS[month - 1]} ${Number(m[3])}`;
}

/** "2026-12-15" → "Dec 15, 2026". */
export function fullDate(civil: string | null | undefined): string {
  if (!civil) return "";
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(civil);
  if (!m) return "";
  return `${dayMonth(civil)}, ${m[1]}`;
}

/**
 * "Tue, Dec 15" — the API's weekday plus the local date. The weekday is the
 * API's (`due_weekday`); this only adds the month and day.
 */
export function dueLabel(due: Pick<DueItem, "due_date" | "due_weekday"> | null | undefined): string {
  if (!due || !due.due_date) return "";
  const dm = dayMonth(due.due_date);
  return due.due_weekday ? `${due.due_weekday}, ${dm}` : dm;
}

/** "Due today", "in 2 days", "3 days late" — from the API's days_until_due. */
export function dueWhen(daysUntilDue: number): string {
  if (daysUntilDue === 0) return "Due today";
  if (daysUntilDue === 1) return "in 1 day";
  if (daysUntilDue === -1) return "1 day late";
  if (daysUntilDue < 0) return `${-daysUntilDue} days late`;
  return `in ${daysUntilDue} days`;
}

/** The bill's own schedule, as stored: "Monthly, due the 22nd", "Yearly, Dec 15". */
export function scheduleLabel(bill: Pick<Bill, "frequency" | "due_day" | "due_date">): string {
  const f = normaliseFrequency(bill.frequency);
  if (f === "monthly") {
    return bill.due_day ? `Monthly, due the ${ordinal(bill.due_day)}` : "Monthly, no due day";
  }
  if (!bill.due_date) return `${frequencyLabel(f)}, no due date`;
  return `${frequencyLabel(f)}, ${dayMonth(bill.due_date)}`;
}

function ordinal(n: number): string {
  const mod100 = n % 100;
  if (mod100 >= 11 && mod100 <= 13) return `${n}th`;
  switch (n % 10) {
    case 1:
      return `${n}st`;
    case 2:
      return `${n}nd`;
    case 3:
      return `${n}rd`;
    default:
      return `${n}th`;
  }
}

/** What a bill costs per month: x1, /3, /6, /12; a once bill is not a monthly cost (0). */
export function monthlyAmount(bill: Pick<Bill, "amount" | "frequency">): number {
  if (bill.amount === null || bill.amount === undefined || bill.amount === "") return 0;
  const n = Number(bill.amount);
  if (!Number.isFinite(n)) return 0;
  switch (normaliseFrequency(bill.frequency)) {
    case "quarterly":
      return n / 3;
    case "semiannual":
      return n / 6;
    case "annual":
      return n / 12;
    case "once":
      return 0;
    default:
      return n;
  }
}

/** The total the list header states: the monthly spread of the bills shown. */
export function monthlyTotal(bills: readonly Pick<Bill, "amount" | "frequency">[]): number {
  return bills.reduce((sum, b) => sum + monthlyAmount(b), 0);
}

// ── the attention field: what the family has to do ───────────
//
// The API answers "what do I have to do about this bill?" in ONE field,
// `attention` (billdue.AttentionFor, #5498). These helpers present that answer;
// they never recompute it. There is deliberately no `if (bill.is_autopay)`
// anywhere in the UI: a bill whose autopay is switched on but which has no
// account on record is NOT automatic — it is `needs_setup`, and the family has
// to fix it. That trap is exactly why the field exists.

/** The four states, in the order the API ranks them (most urgent first). */
export const ATTENTION_ORDER: readonly Attention[] = ["overdue", "needs_setup", "due_soon", "automatic"];

/** What each state is called on screen. Short: it sits in a badge at 390px. */
export const ATTENTION_LABELS: Record<Attention, string> = {
  automatic: "Automatic",
  needs_setup: "Needs setup",
  due_soon: "Due soon",
  overdue: "Overdue",
};

/** The one-line explanation under a badge, in the family's words. */
export const ATTENTION_HELP: Record<Attention, string> = {
  automatic: "Paid automatically — nothing to do.",
  needs_setup: "Nothing pays this yet — set up autopay or pay it yourself.",
  due_soon: "You pay this one — it's coming up.",
  overdue: "You pay this one — it's past due.",
};

/**
 * The bill's attention state, or null when the API did not send one.
 *
 * Null is NOT "automatic": an older payload, or a hidden field, must not read
 * as "nothing to do". The caller renders the unknown state as unknown.
 */
export function attentionOf(bill: Pick<Bill, "attention">): Attention | null {
  const a = bill.attention;
  return a === "automatic" || a === "needs_setup" || a === "due_soon" || a === "overdue" ? a : null;
}

/**
 * Whether the family has to do something about this bill. True for
 * `needs_setup`, `due_soon` and `overdue`; false for `automatic`, and false for
 * an unknown state (the UI must not promise "nothing to do" it cannot back up).
 *
 * This is a grouping of the API's four answers, not a second derivation: the
 * only question asked here is "did the API say automatic?", never "is autopay
 * on?".
 */
export function attentionNeedsAction(attention: Attention | null | undefined): boolean {
  return attention === "needs_setup" || attention === "due_soon" || attention === "overdue";
}

/** The badge's colour role. `auto` reads as calm, `action` as "you, now". */
export type AttentionTone = "auto" | "action" | "warn" | "danger" | "unknown";

export function attentionTone(attention: Attention | null | undefined): AttentionTone {
  switch (attention) {
    case "automatic":
      return "auto";
    case "needs_setup":
      return "action";
    case "due_soon":
      return "warn";
    case "overdue":
      return "danger";
    default:
      return "unknown";
  }
}

/** The label for a badge; "Unknown" when the API sent no state. */
export function attentionLabel(attention: Attention | null | undefined): string {
  return attention ? ATTENTION_LABELS[attention] : "Unknown";
}

/** The one-line explanation for a state; a neutral sentence when unknown. */
export function attentionHelp(attention: Attention | null | undefined): string {
  return attention ? ATTENTION_HELP[attention] : "This bill's state is not known yet.";
}

// ── the payment account ──────────────────────────────────────

/**
 * The account a bill draws on, as a family reads it: "Joint checking ····1234".
 *
 * NEVER a full number. `last_four` is the only account digits the API has
 * (payment_accounts has no column for more), and this renders at most four.
 * Null when the bill has no account on record — or when the role may not see
 * one, which the API expresses by omitting the key. The caller decides which of
 * those two it is; this function only formats.
 */
export function accountLine(account: PaymentAccountRef | null | undefined): string | null {
  if (!account) return null;
  const name = (account.name ?? "").trim();
  // Slice to the last four even though the API promises exactly four: this
  // function is the one place a full number could reach the screen, and the
  // promise is not worth trusting with a bank account. A longer string can
  // only ever lose digits here, never gain them.
  const last4 = (account.last_four ?? "").trim().slice(-4);
  if (name && last4) return `${name} ····${last4}`;
  if (name) return name;
  if (last4) return `····${last4}`;
  return null;
}

/** The account with its institution: "Riverside Credit Union · Joint checking ····1234". */
export function accountSummary(account: PaymentAccountRef | null | undefined): string | null {
  if (!account) return null;
  const line = accountLine(account);
  const institution = (account.institution ?? "").trim();
  if (institution && line) return `${institution} · ${line}`;
  return line;
}

/** What kind of account it is, in words: "Checking", "Savings", "Credit card", "Other". */
export function accountTypeLabel(type: string | null | undefined): string | null {
  switch ((type ?? "").trim()) {
    case "checking":
      return "Checking";
    case "savings":
      return "Savings";
    case "credit_card":
      return "Credit card";
    case "other":
      return "Other";
    default:
      return null;
  }
}

// ── the split a household actually wants ─────────────────────

export interface AttentionSplit {
  /** The monthly spread of every bill in the set. */
  monthly: number;
  /** The part of it that pays itself (the API's `automatic`). */
  automatic: number;
  /** The part the family has to pay (needs_setup, due_soon, overdue, unknown). */
  manual: number;
  /** How many bills are in each part. */
  automaticCount: number;
  manualCount: number;
}

/**
 * The monthly total split by what the family has to do, keyed on the API's
 * `attention` — never on `is_autopay`.
 *
 * "The number a household actually wants" is "$2,595/month, of which $2,340 is
 * on autopay". The arithmetic is `monthlyAmount`'s, the grouping is
 * `attention`'s; this function only adds them up, so the spread rule stays in
 * one place.
 */
export function attentionSplit(bills: readonly Pick<Bill, "amount" | "frequency" | "attention">[]): AttentionSplit {
  const split: AttentionSplit = { monthly: 0, automatic: 0, manual: 0, automaticCount: 0, manualCount: 0 };
  for (const bill of bills) {
    const perMonth = monthlyAmount(bill);
    split.monthly += perMonth;
    if (attentionOf(bill) === "automatic") {
      split.automatic += perMonth;
      split.automaticCount += 1;
    } else {
      split.manual += perMonth;
      split.manualCount += 1;
    }
  }
  return split;
}

/**
 * The sentence the header states: "$2,595/month, of which $2,340 is on autopay."
 * Null when there is nothing to total (no bills, or no amounts on any of them),
 * so a page never prints "$0/month".
 */
export function splitSentence(split: AttentionSplit): string | null {
  if (split.monthly <= 0) return null;
  const total = formatMoney(split.monthly);
  if (!total) return null;
  if (split.automatic <= 0) return `${total}/month, none of it on autopay`;
  if (split.manual <= 0) return `${total}/month, all of it on autopay`;
  const auto = formatMoney(split.automatic);
  return `${total}/month, of which ${auto} is on autopay`;
}

/**
 * How many bills the family has to act on, in the words the list header uses.
 * "Nothing to do" is only said when the API said `automatic` for every bill.
 */
export function actionCountLabel(split: AttentionSplit): string {
  if (split.manualCount === 0) return "Nothing to do";
  if (split.manualCount === 1) return "1 needs you";
  return `${split.manualCount} need you`;
}

export interface SubjectNames {
  properties?: Record<string, string>;
  assets?: Record<string, string>;
  vehicles?: Record<string, string>;
}

/** What the bill is for, as a line: the subject's name, or null for a family-level bill. */
export function subjectLabel(
  bill: Pick<Bill, "property_id" | "vehicle_id" | "asset_id">,
  names: SubjectNames,
): { kind: "property" | "asset" | "vehicle"; name: string } | null {
  if (bill.vehicle_id) return { kind: "vehicle", name: names.vehicles?.[bill.vehicle_id] ?? "Vehicle" };
  // An asset's bill also carries the property_id (the 038 trigger), and it is
  // the ASSET's bill: show the thing, not the house (#4706's rule).
  if (bill.asset_id) return { kind: "asset", name: names.assets?.[bill.asset_id] ?? "Item" };
  if (bill.property_id) return { kind: "property", name: names.properties?.[bill.property_id] ?? "Property" };
  return null;
}

// ── the join: bills + the API's due readings ─────────────────

/**
 * Joins the bills to the API's due readings by id. The order is by next due
 * date (earliest first), then by name; a bill with no reported due date sorts
 * last, because there is no date to rank it by.
 */
export function mergeRows(bills: readonly Bill[], dueSoon: DueSoon | null | undefined): BillRow[] {
  const byID = new Map<string, DueItem>();
  for (const item of dueSoon?.items ?? []) {
    if (item.source === "bill") byID.set(item.id, item);
  }
  const rows = bills.map((bill) => ({ bill, due: byID.get(bill.id) ?? null }));
  return rows.sort((a, b) => {
    const ad = a.due ? a.due.days_until_due : Number.POSITIVE_INFINITY;
    const bd = b.due ? b.due.days_until_due : Number.POSITIVE_INFINITY;
    if (ad !== bd) return ad - bd;
    return a.bill.name.localeCompare(b.bill.name);
  });
}

/**
 * A bill is DUE when the API says its current due date is not covered. A bill
 * the API reports as paid is never due, whatever its stored dates look like.
 * A bill with no reading is "unknown", never "due": nothing is claimed that
 * the API did not say.
 */
export type DueState = "due" | "paid" | "unknown";

export function dueState(row: BillRow): DueState {
  if (!row.due) return "unknown";
  return row.due.status === "paid" ? "paid" : "due";
}

/** How many bills in the list are due now (not paid, not unknown). */
export function dueCount(rows: readonly BillRow[]): number {
  return rows.filter((r) => dueState(r) === "due").length;
}

/**
 * The due date a payment made today would leave the bill on — what the
 * "mark paid" sheet promises. The current due date is covered by the payment,
 * so the next one is one period after it (the shared rule's `after()`): the
 * same day of the month, one month / quarter / half-year / year on, clamped to
 * a short month. A once bill has no next date (null): paying it is the end.
 *
 * It is arithmetic on a civil date, not a weekday: the weekday of the result
 * comes from the API once the payment is recorded.
 */
export function nextAfterPaying(
  due: Pick<DueItem, "due_date"> | null | undefined,
  frequency: string | null | undefined,
): string | null {
  if (!due || !due.due_date) return null;
  return addPeriods(due.due_date, periodMonths(frequency));
}

/** Months between due dates for a frequency; 0 for a once bill. */
export function periodMonths(frequency: string | null | undefined): number {
  switch (normaliseFrequency(frequency)) {
    case "quarterly":
      return 3;
    case "semiannual":
      return 6;
    case "annual":
      return 12;
    case "once":
      return 0;
    default:
      return 1;
  }
}

/** "2026-12-15" plus `months`, clamped to the target month's last day; null for 0 months. */
export function addPeriods(civil: string, months: number): string | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(civil);
  if (!m) return null;
  if (months === 0) return null;
  const year = Number(m[1]);
  const month = Number(m[2]);
  const day = Number(m[3]);
  const total = year * 12 + (month - 1) + months;
  const y = Math.floor(total / 12);
  const mo = (total % 12) + 1;
  const last = new Date(Date.UTC(y, mo, 0)).getUTCDate();
  return `${y}-${String(mo).padStart(2, "0")}-${String(Math.min(day, last)).padStart(2, "0")}`;
}

// ── role policy (mirrors apps/api/internal/auth/roles.go) ────

/** Roles whose bills/loans/budget READ grant is on: owner, family_manager, family_member, viewer. */
const MONEY_READERS = new Set(["owner", "family_manager", "family_member", "viewer"]);
/** Roles whose bills WRITE grant is on: the family roles. A viewer reads money and cannot write it. */
const MONEY_WRITERS = new Set(["owner", "family_manager", "family_member"]);

/**
 * Whether a role sees money at all (auth.CanSeeMoney, #4806). A housesitter
 * and a vendor get no amount, no bill and no Money nav entry; an unknown role
 * (null while /me/households is in flight) is treated as "wait", not as yes.
 */
export function canSeeMoney(role: string | null | undefined): boolean {
  return !!role && MONEY_READERS.has(role);
}

/**
 * Whether a role may write money (add, edit, delete a bill). A viewer may read
 * and not write, so the page renders with every write affordance disabled
 * rather than hidden (KB decisions/2026-11-15-role-policy.md).
 */
export function canWriteMoney(role: string | null | undefined): boolean {
  return !!role && MONEY_WRITERS.has(role);
}

/** True once the role is known, so a page can wait instead of guessing. */
export function roleKnown(role: string | null | undefined): boolean {
  return !!role;
}

// ── list filtering ───────────────────────────────────────────

export type FrequencyFilter = "all" | BillFrequency;

/** The frequency chips, with the count of bills behind each (the mockup's chips). */
export function frequencyChips(bills: readonly Bill[]): { value: FrequencyFilter; label: string; count: number }[] {
  const counts = new Map<string, number>();
  for (const b of bills) {
    const f = normaliseFrequency(b.frequency);
    counts.set(f, (counts.get(f) ?? 0) + 1);
  }
  const chips: { value: FrequencyFilter; label: string; count: number }[] = [
    { value: "all", label: "All", count: bills.length },
  ];
  for (const f of ["monthly", "quarterly", "semiannual", "annual", "once"] as BillFrequency[]) {
    const count = counts.get(f) ?? 0;
    // A chip with nothing behind it is not offered: it would filter to an empty list.
    if (count > 0) chips.push({ value: f, label: FREQUENCY_LABELS[f], count });
  }
  return chips;
}

/** The rows a frequency chip keeps. */
export function filterRows(rows: readonly BillRow[], filter: FrequencyFilter): BillRow[] {
  if (filter === "all") return [...rows];
  return rows.filter((r) => normaliseFrequency(r.bill.frequency) === filter);
}

// ── the form's schedule rule (migration 044) ─────────────────

/**
 * The schedule a bill form should send: a monthly bill sends due_day and no
 * due_date; any other frequency sends due_date and no due_day. Migration 044's
 * CHECK, and the API answers 400 for the conflicting pair — so the form never
 * sends both, and switching frequency drops the field that no longer applies.
 */
export function scheduleBody(
  frequency: string | null | undefined,
  dueDay: string | number | null | undefined,
  dueDate: string | null | undefined,
): { frequency: BillFrequency; due_day?: number; due_date?: string } {
  const f = normaliseFrequency(frequency);
  if (f === "monthly") {
    const day = Number(dueDay);
    return Number.isInteger(day) && day >= 1 && day <= 31 ? { frequency: f, due_day: day } : { frequency: f };
  }
  const date = (dueDate ?? "").slice(0, 10);
  return /^\d{4}-\d{2}-\d{2}$/.test(date) ? { frequency: f, due_date: date } : { frequency: f };
}

export type SubjectKindChoice = "none" | "property" | "asset" | "vehicle";

/**
 * The subject a bill form sends: nothing, one property/asset, or one vehicle —
 * never both (the DB's bills_subject_check). Clearing sends empty strings,
 * which the API reads as "no subject".
 */
export function subjectBody(kind: SubjectKindChoice, id: string | null | undefined): Record<string, string> {
  if (kind === "none" || !id) return { property_id: "", asset_id: "", vehicle_id: "" };
  if (kind === "vehicle") return { vehicle_id: id };
  if (kind === "asset") return { asset_id: id };
  return { property_id: id };
}

/** The subject kind a stored bill already has. */
export function subjectKindOf(bill: Pick<Bill, "property_id" | "vehicle_id" | "asset_id">): SubjectKindChoice {
  if (bill.vehicle_id) return "vehicle";
  if (bill.asset_id) return "asset";
  if (bill.property_id) return "property";
  return "none";
}
