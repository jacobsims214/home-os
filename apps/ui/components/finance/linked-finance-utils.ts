/**
 * Loans and bills linked to a property or vehicle (#4706).
 *
 * Since #4704 a property's mortgage/tax/insurance/HOA and a vehicle's
 * loan/registration/insurance live in loans and bills (property_id /
 * vehicle_id). The API still returns the old JSON fields, derived from those
 * rows, but the UI only shows them; it never sends them (#4707 drops the
 * columns). Edits go to the loan or bill itself.
 */

export type SubjectKind = "property" | "vehicle";

export interface Subject {
  kind: SubjectKind;
  id: string;
  /** Shown in the add forms ("For Lake Cabin") and used to suggest names. */
  name: string;
}

export interface LinkedLoan {
  id: string;
  name: string;
  property_id?: string | null;
  vehicle_id?: string | null;
  asset_id?: string | null;
  lender?: string | null;
  original_amount?: string | number | null;
  remaining_balance?: string | number | null;
  interest_rate?: string | number | null;
  term_months?: number | null;
  monthly_payment?: string | number | null;
  start_date?: string | null;
}

export interface LinkedBill {
  id: string;
  name: string;
  property_id?: string | null;
  vehicle_id?: string | null;
  asset_id?: string | null;
  amount?: string | number | null;
  category?: string | null;
  frequency?: string | null;
  due_day?: number | null;
  due_date?: string | null;
}

/**
 * The legacy finance fields the property/vehicle API derives from loans and
 * bills (KB apis/property-vehicle-finance.md). Never put these in a property
 * or vehicle request body.
 */
export const LEGACY_PROPERTY_FINANCE_FIELDS = [
  "mortgage_amount",
  "mortgage_rate",
  "mortgage_term_months",
  "mortgage_start_date",
  "mortgage_lender",
  "mortgage_account_number",
  "property_tax_annual",
  "property_tax_due_months",
  "insurance_annual",
  "insurance_provider",
  "hoa_fee_monthly",
] as const;

export const LEGACY_VEHICLE_FINANCE_FIELDS = [
  "lender",
  "loan_amount",
  "loan_term_months",
  "monthly_payment",
  "registration_cost",
  "registration_renewal_month",
  "insurance_cost",
  "insurance_provider",
] as const;

/** The fields the property edit form sends (PUT /api/v1/properties/:id). */
export const PROPERTY_EDIT_FIELDS = [
  "name",
  "address",
  "property_type",
  "notes",
  "purchase_price",
  "purchase_date",
  "current_value",
  "down_payment",
] as const;

/** The property PUT body: only the edit form's own fields, never a legacy finance field. */
export function propertyEditBody(values: Record<string, unknown>): Record<string, string> {
  const body: Record<string, string> = {};
  for (const k of PROPERTY_EDIT_FIELDS) {
    const v = values[k];
    body[k] = v == null ? "" : String(v);
  }
  return body;
}

/** A row about the subject itself. An asset's loan/bill also carries its property_id, but isn't the property's. */
export function isLinkedTo(row: { property_id?: string | null; vehicle_id?: string | null; asset_id?: string | null }, subject: Subject): boolean {
  if (row.asset_id) return false;
  return (subject.kind === "property" ? row.property_id : row.vehicle_id) === subject.id;
}

export const FREQUENCIES = [
  { value: "monthly", label: "Monthly" },
  { value: "quarterly", label: "Quarterly" },
  { value: "semiannual", label: "Twice a year" },
  { value: "annual", label: "Yearly" },
  { value: "once", label: "One time" },
];

const PERIODS_PER_YEAR: Record<string, number> = { weekly: 52, monthly: 12, quarterly: 4, semiannual: 2, annual: 1, once: 0 };

function toNumber(v: unknown): number {
  if (v == null || v === "") return 0;
  const n = Number(v);
  return Number.isFinite(n) ? n : 0;
}

/** What a bill costs per month. A one-time bill is not a monthly cost. Unknown frequency counts as monthly (the API default). */
export function monthlyBillAmount(bill: Pick<LinkedBill, "amount" | "frequency">): number {
  const periods = PERIODS_PER_YEAR[bill.frequency || "monthly"] ?? 12;
  return (toNumber(bill.amount) * periods) / 12;
}

export function frequencyLabel(frequency: string | null | undefined): string {
  return FREQUENCIES.find((f) => f.value === (frequency || "monthly"))?.label ?? String(frequency);
}

/** The property's mortgage: the oldest loan named like a mortgage, else its first loan (the API's rule). */
export function pickMortgage<T extends Pick<LinkedLoan, "name">>(loans: T[]): T | undefined {
  return loans.find((l) => /mortgage/i.test(l.name)) ?? loans[0];
}

export interface BillPreset {
  label: string;
  /** Suffix for the suggested name: "<subject name> <suffix>". */
  suffix: string;
  category: string;
  frequency: string;
}

/** Quick picks in the Add bill form. Names and categories match what the API's derivation looks for. */
export const BILL_PRESETS: Record<SubjectKind, BillPreset[]> = {
  property: [
    { label: "Property tax", suffix: "property tax", category: "Property Tax", frequency: "annual" },
    { label: "Insurance", suffix: "insurance", category: "Insurance", frequency: "annual" },
    { label: "HOA", suffix: "HOA fee", category: "HOA", frequency: "monthly" },
  ],
  vehicle: [
    { label: "Registration", suffix: "registration", category: "Vehicle", frequency: "annual" },
    { label: "Insurance", suffix: "insurance", category: "Insurance", frequency: "annual" },
  ],
};

export const BILL_CATEGORIES: Record<SubjectKind, string[]> = {
  property: ["Property Tax", "Insurance", "HOA", "Electric", "Water", "Gas", "Internet", "Trash", "Other"],
  vehicle: ["Vehicle", "Insurance", "Other"],
};

type Result = { ok: true; body: Record<string, unknown> } | { ok: false; errors: Record<string, string> };

const subjectFk = (subject: Subject) => (subject.kind === "property" ? { property_id: subject.id } : { vehicle_id: subject.id });

function money(raw: string, label: string, errors: Record<string, string>, key: string): string | undefined {
  const cleaned = raw.replace(/[$,\s]/g, "");
  if (cleaned === "") return undefined;
  if (!/^\d+(\.\d+)?$/.test(cleaned)) {
    errors[key] = `${label} must be a number`;
    return undefined;
  }
  return cleaned;
}

function wholeNumber(raw: string, label: string, min: number, max: number, errors: Record<string, string>, key: string): number | undefined {
  const t = raw.trim();
  if (t === "") return undefined;
  const n = Number(t);
  if (!Number.isInteger(n) || n < min || n > max) {
    errors[key] = `${label} must be a whole number from ${min} to ${max}`;
    return undefined;
  }
  return n;
}

/** POST /api/v1/loans body for a loan about the subject. Remaining balance defaults to the amount. */
export function newLoanBody(subject: Subject, v: Record<string, string>): Result {
  const errors: Record<string, string> = {};
  const name = (v.name ?? "").trim();
  if (!name) errors.name = "Enter a name";
  const original = money(v.original_amount ?? "", "Amount", errors, "original_amount");
  const remaining = money(v.remaining_balance ?? "", "Balance", errors, "remaining_balance");
  const monthly = money(v.monthly_payment ?? "", "Monthly payment", errors, "monthly_payment");
  const rate = money(v.interest_rate ?? "", "Rate", errors, "interest_rate");
  if (rate !== undefined && Number(rate) >= 100) errors.interest_rate = "Rate must be under 100";
  const term = wholeNumber(v.term_months ?? "", "Term", 1, 1200, errors, "term_months");
  if (Object.keys(errors).length) return { ok: false, errors };

  const body: Record<string, unknown> = { name, ...subjectFk(subject) };
  const lender = (v.lender ?? "").trim();
  if (lender) body.lender = lender;
  if (original !== undefined) body.original_amount = original;
  if (remaining !== undefined || original !== undefined) body.remaining_balance = remaining ?? original;
  if (rate !== undefined) body.interest_rate = rate;
  if (term !== undefined) body.term_months = term;
  if (monthly !== undefined) body.monthly_payment = monthly;
  if (v.start_date) body.start_date = v.start_date;
  return { ok: true, body };
}

/** POST /api/v1/bills body for a bill about the subject. Monthly bills take a due day; the rest a due date. */
export function newBillBody(subject: Subject, v: Record<string, string>): Result {
  const errors: Record<string, string> = {};
  const name = (v.name ?? "").trim();
  if (!name) errors.name = "Enter a name";
  const amount = money(v.amount ?? "", "Amount", errors, "amount");
  const frequency = v.frequency || "monthly";
  if (!FREQUENCIES.some((f) => f.value === frequency)) errors.frequency = "Choose how often";
  const dueDay = frequency === "monthly" ? wholeNumber(v.due_day ?? "", "Due day", 1, 31, errors, "due_day") : undefined;
  if (Object.keys(errors).length) return { ok: false, errors };

  const body: Record<string, unknown> = { name, frequency, ...subjectFk(subject) };
  if (amount !== undefined) body.amount = amount;
  if (dueDay !== undefined) body.due_day = dueDay;
  if (frequency !== "monthly" && v.due_date) body.due_date = v.due_date;
  const category = (v.category ?? "").trim();
  if (category) body.category = category;
  return { ok: true, body };
}
