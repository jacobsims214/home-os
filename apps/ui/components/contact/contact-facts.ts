/**
 * Contact (vendor) display helpers (#4557).
 *
 * A contact is a vendor row: name, trades[], specialty, phone, email,
 * website, address, notes (KB apis/vendors.md). These keep the pages from
 * hand-rolling four different tel: schemes, and put the "who do I call"
 * question in one place.
 */

import { tradeLabel } from "./trade-vocabulary.ts";

/** The vendor row as GET /api/v1/vendors returns it (KB apis/vendors.md). */
export interface Contact {
  id: string;
  household_id: string;
  name: string;
  /**
   * Service provider, payee or both (#5529, migration 080). The contacts list
   * only returns rows that do work, so this is never `payee` here -- it is
   * `both` when the contact is also paid.
   */
  kind: "service_provider" | "payee" | "both";
  /** Always an array, never null, never omitted. Stored snake_case. */
  trades: string[];
  specialty?: string | null;
  phone?: string | null;
  email?: string | null;
  website?: string | null;
  /** One line, up to 500 chars (#4766). No longer kept in notes. */
  address?: string | null;
  notes?: string | null;
  /**
   * Whether a PORTAL LOGIN is on file for this payee (vendors.portal_secret_id,
   * 075 / #5506). ABSENT, not false, when the caller's role is outside the
   * credential policy — "no login" and "you may not know" must not look alike.
   */
  portal_login_stored?: boolean;
  /** The linked credential's id (the vault's handle, never a credential). */
  portal_secret_id?: string;
  created_at?: string;
  updated_at?: string;
}

/**
 * A `tel:` target: digits and a leading + only, so "555-0100" and
 * "(555) 010-0" both dial. Null when there is nothing to dial.
 *
 * Same rule the pet page uses for `tel:` (app/(dashboard)/dashboard/pets/[id]).
 */
export function telHref(phone: string | null | undefined): string | null {
  const digits = (phone ?? "").replace(/[^\d+]/g, "");
  // A bare "+" is not a number.
  return /\d/.test(digits) ? `tel:${digits}` : null;
}

/** A `sms:` target, with the same cleaning as telHref. */
export function smsHref(phone: string | null | undefined): string | null {
  const tel = telHref(phone);
  return tel ? `sms:${tel.slice(4)}` : null;
}

/** A `mailto:` target. Null when the value is not an address at all. */
export function mailtoHref(email: string | null | undefined): string | null {
  const e = (email ?? "").trim();
  // Deliberately loose: the API stores free text, and a wrong-looking address
  // should still be tappable rather than hidden. One @ and a dot is enough.
  return /^[^\s@]+@[^\s@]+$/.test(e) ? `mailto:${e}` : null;
}

/**
 * A maps link for a contact's address, tap-to-open on phones. Uses the
 * address as a search query, so "12 Main St, Springfield" opens the pin
 * rather than a route from nowhere.
 */
export function mapsHref(address: string | null | undefined): string | null {
  const a = (address ?? "").trim();
  if (!a) return null;
  return `https://www.google.com/maps/search/?api=1&query=${encodeURIComponent(a)}`;
}

/**
 * The contact's initial for the avatar tile: "Joe's Plumbing" → "J".
 *
 * Takes the first character that is not punctuation or space rather than a
 * Unicode letter class, because tsconfig targets es5 and the `u` flag needs
 * es6 — and the avatar is a letter or a digit in every real name anyway.
 */
export function contactInitial(name: string): string {
  const trimmed = name.trim();
  for (const ch of trimmed) {
    if (/[0-9A-Za-z]/.test(ch) || ch.charCodeAt(0) > 127) return ch.toUpperCase();
  }
  return "?";
}

/**
 * The trades line under a contact's name. Falls back to `specialty` (free
 * text, kept by the API) when no trades are tagged, so a contact with only a
 * specialty still says who they are. Empty string when neither is set — then
 * no subline renders, rather than a blank row.
 */
export function contactTradesLine(contact: Pick<Contact, "trades" | "specialty">): string {
  // Label them: the stored form is snake_case ("pest_control") and the audit
  // (#4509) called out exactly that kind of raw value on screen.
  const trades = (contact.trades ?? []).filter(Boolean).map(tradeLabel);
  if (trades.length > 0) return trades.join(" · ");
  return (contact.specialty ?? "").trim();
}

/**
 * The trades a set of contacts actually uses, with how many contacts each
 * has, ordered by count then alphabetically. The filter chips are built from
 * this rather than the synonym map: a chip that filters to nothing is noise,
 * and a family's own free-text trade ("handyman") gets a chip too.
 */
export function tradeFacets(contacts: readonly Contact[]): Array<{ trade: string; count: number }> {
  const counts = new Map<string, number>();
  for (const c of contacts) {
    for (const t of c.trades ?? []) {
      if (!t) continue;
      counts.set(t, (counts.get(t) ?? 0) + 1);
    }
  }
  // Array.from, not spread: tsconfig targets es5 and a Map iterator is not
  // downlevel-iterable without --downlevelIteration.
  const facets: Array<{ trade: string; count: number }> = [];
  counts.forEach((count, trade) => facets.push({ trade, count }));
  return facets.sort((a, b) => b.count - a.count || a.trade.localeCompare(b.trade));
}

/** True when the contact carries this trade (the chip's match rule). */
export function hasTrade(contact: Pick<Contact, "trades">, trade: string | null): boolean {
  if (!trade) return true;
  return (contact.trades ?? []).includes(trade);
}
