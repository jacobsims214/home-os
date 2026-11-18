/**
 * Contact form bodies (#4557) for POST and PUT /api/v1/vendors.
 *
 * The API's rules these encode (KB apis/vendors.md):
 * - `name` is required on both.
 * - `trades` is normalised and de-duplicated server-side; blank entries, a
 *   trade over 64 chars and more than 20 distinct trades are 400s. This
 *   checks them first so the user gets a field error instead of a 400.
 * - On PUT, **omitted trades and address are kept**, `[]` / `null` / `""`
 *   clears them. `buildContactUpdate` therefore always sends `trades` (the
 *   form shows them, so what the box says is what is stored) and sends
 *   `address` as `""` when cleared, which the API turns into NULL.
 * - `specialty`, `phone`, `email`, `website`, `notes` are a full replace:
 *   omitted means null, so the PUT body carries every one of them.
 * - `address` is one line, at most 500 characters.
 */

import { MAX_TRADE_LEN, MAX_TRADES, normalizeTradeList } from "./trade-vocabulary.ts";

export const MAX_ADDRESS_LEN = 500;

export type ContactFormValues = {
  name: string;
  trades: string;
  specialty: string;
  phone: string;
  email: string;
  website: string;
  address: string;
  notes: string;
};

export type ContactBodyResult =
  | { ok: true; body: Record<string, unknown> }
  | { ok: false; error: string };

/** The form's trade box as stored trades, with the API's limits checked. */
function tradesFor(values: Pick<ContactFormValues, "trades">): ContactBodyResult | string[] {
  const list = normalizeTradeList(values.trades);
  for (const t of list) {
    if (t.length > MAX_TRADE_LEN) {
      return { ok: false, error: `Each trade must be at most ${MAX_TRADE_LEN} characters.` };
    }
  }
  if (list.length > MAX_TRADES) {
    return { ok: false, error: `Use at most ${MAX_TRADES} trades.` };
  }
  return list;
}

/** Optional text as a trimmed string, or null when blank (the API clears it). */
const orNull = (v: string | undefined): string | null => {
  const t = (v ?? "").trim();
  return t === "" ? null : t;
};

/**
 * POST /api/v1/vendors. Only what the list's quick-add asks for must be
 * filled in: a name. Trades and phone are optional.
 */
export function createContactBody(input: {
  name: string;
  trades?: string;
  phone?: string;
}): { ok: true; body: Record<string, unknown> } | { ok: false; error: string } {
  const name = input.name.trim();
  if (!name) return { ok: false, error: "Enter a name for the contact." };

  const trades = tradesFor({ trades: input.trades ?? "" });
  if (!Array.isArray(trades)) return trades;

  const body: Record<string, unknown> = { name };
  if (trades.length > 0) body.trades = trades;
  const phone = orNull(input.phone);
  if (phone) body.phone = phone;
  return { ok: true, body };
}

/**
 * PUT /api/v1/vendors/{id}. A full replace of the scalar fields, with
 * `trades` and `address` always present so clearing them is deliberate.
 */
export function buildContactUpdate(values: ContactFormValues): ContactBodyResult {
  const name = values.name.trim();
  if (!name) return { ok: false, error: "Enter a name for the contact." };

  const trades = tradesFor(values);
  if (!Array.isArray(trades)) return trades;

  const address = (values.address ?? "").trim();
  if (address.length > MAX_ADDRESS_LEN) {
    return { ok: false, error: `The address must be at most ${MAX_ADDRESS_LEN} characters.` };
  }

  return {
    ok: true,
    // trades [] clears; address "" clears (the API stores NULL).
    body: {
      name,
      trades,
      specialty: orNull(values.specialty),
      phone: orNull(values.phone),
      email: orNull(values.email),
      website: orNull(values.website),
      address,
      notes: orNull(values.notes),
    },
  };
}

/** The contact's values as form strings, for the edit form. */
export function contactToFormValues(contact: {
  name?: string | null;
  trades?: string[] | null;
  specialty?: string | null;
  phone?: string | null;
  email?: string | null;
  website?: string | null;
  address?: string | null;
  notes?: string | null;
}): ContactFormValues {
  return {
    name: contact.name ?? "",
    trades: (contact.trades ?? []).join(", "),
    specialty: contact.specialty ?? "",
    phone: contact.phone ?? "",
    email: contact.email ?? "",
    website: contact.website ?? "",
    address: contact.address ?? "",
    notes: contact.notes ?? "",
  };
}

/**
 * A website field as an href. The API stores free text, so accept a bare
 * host ("brightspark.example") as well as a full URL; refuse anything else.
 */
export function websiteHref(website: string | null | undefined): string | null {
  const w = (website ?? "").trim();
  if (!w) return null;
  if (/^https?:\/\//i.test(w)) return w;
  if (/^[a-z0-9.-]+\.[a-z]{2,}(\/.*)?$/i.test(w)) return `https://${w}`;
  return null;
}
