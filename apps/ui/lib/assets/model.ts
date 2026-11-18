/**
 * Pure helpers for the assets ("things") pages (#4554): money and date
 * formatting, the card's second line, and the facts a card or a detail page
 * shows. Kept free of React and of "@/" imports so `node --test` can load it
 * directly (see model.test.mjs), the same split as lib/bills/model.ts.
 *
 * The money rule (#4912): GET /assets is rolesEveryone, but the API leaves
 * purchase_price / current_value / purchase_date out (or null) for a role
 * without money read. Every helper here therefore returns null for a missing
 * value and never falls back to 0 — a redacted value must read as absent, not
 * as "worth nothing".
 */

export interface Asset {
  id: string;
  household_id: string;
  property_id?: string | null;
  room_id?: string | null;
  name: string;
  category?: string | null;
  manufacturer?: string | null;
  model?: string | null;
  serial_number?: string | null;
  /** Money and purchase date: absent or null for a role without money read (#4912). */
  purchase_date?: string | null;
  purchase_price?: string | null;
  current_value?: string | null;
  warranty_expiry?: string | null;
  notes?: string | null;
  created_at: string;
  updated_at: string;
}

export interface Fact {
  label: string;
  value: string;
}

/** "$1,204", or null when there is no number to show (never "$0" for a redacted field). */
export function usd(v: unknown, digits = 0): string | null {
  if (v == null || v === "") return null;
  const n = Number(v);
  if (!Number.isFinite(n)) return null;
  return n.toLocaleString("en-US", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  });
}

/** "Carrier · Infinity 59SC5", or the category alone when there is no make/model. */
export function makeAndModel(a: Pick<Asset, "manufacturer" | "model" | "category">): string | null {
  const mm = [a.manufacturer, a.model].filter((x) => x != null && x !== "").join(" · ");
  return mm || a.category || null;
}

/**
 * "Oct 2029" from an ISO date. Parsed at UTC midnight and formatted in UTC, so
 * a date-only string cannot slip to the previous month in a western zone.
 */
export function monthYear(iso: string): string {
  const d = new Date(`${iso.slice(0, 10)}T00:00:00Z`);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString("en-US", { month: "short", year: "numeric", timeZone: "UTC" });
}

/** The facts a list card shows, from what the API actually returned. */
export function factsFor(a: Asset, propertyName: string | null): Fact[] {
  const facts: Fact[] = [];
  if (propertyName) facts.push({ label: "Place", value: propertyName });
  const value = usd(a.current_value);
  if (value) facts.push({ label: "Value", value });
  if (a.warranty_expiry) facts.push({ label: "Warranty", value: `Until ${monthYear(a.warranty_expiry)}` });
  return facts;
}

/**
 * The detail page's summary strip: the numbers worth seeing before scrolling,
 * in the order the mockup puts them. Empty when the API sent no money at all
 * (a housesitter or vendor), so the strip is simply absent rather than a row of
 * dashes about money they may not see.
 */
export function summaryFacts(a: Asset): Fact[] {
  const facts: Fact[] = [];
  const value = usd(a.current_value);
  if (value) facts.push({ label: "Value", value });
  const paid = usd(a.purchase_price);
  if (paid) facts.push({ label: "Paid", value: paid });
  if (a.warranty_expiry) facts.push({ label: "Warranty", value: monthYear(a.warranty_expiry) });
  if (a.purchase_date) facts.push({ label: "Bought", value: monthYear(a.purchase_date) });
  if (a.category) facts.push({ label: "Category", value: a.category });
  return facts;
}
