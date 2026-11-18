/**
 * The active household, as the browser stores it (#4574).
 *
 * A Home OS account can belong to several households (an adult with their own
 * parents' family, a caregiver across two families). The Go API chooses which
 * one a request is about from the `X-Household-ID` header — a household UUID
 * that must be one of the caller's memberships, else 403 (see
 * apps/api/internal/auth/resolve.go and KB decisions/2026-11-15-role-policy.md);
 * with no header it falls back to the oldest membership.
 *
 * The UI keeps the choice in the `home-os-household` cookie rather than in a
 * header set by client JS, for two reasons:
 *
 * 1. One place decides. The BFF (app/bff/[...path]/route.ts) strips any
 *    browser-supplied X-Household-ID and sets the header from this cookie, so a
 *    page — or a bug, or an injected script — cannot ask for another family's
 *    numbers by setting a header.
 * 2. It covers requests that are not apiFetch(). `<a href="/bff/files/x/content">`
 *    downloads (components/files/FileList.tsx) carry the cookie and nothing
 *    else, so they follow the switch instead of silently reading the default
 *    household.
 *
 * The cookie is deliberately script-readable (not httpOnly): its value is a
 * household id, not a credential, and reading it is what lets the client render
 * a switch without a round trip. It is worthless on its own — the API only
 * serves households the signed-in member belongs to, so a tampered id answers
 * 403 rather than another family's data.
 *
 * No Next and no DOM imports at module level, so `node --test` can load this
 * file directly (lib/household-cookie.test.mjs), like lib/auth/same-origin.ts.
 */

/** Cookie holding the household the UI is working in. */
export const HOUSEHOLD_COOKIE = "home-os-household";

/** A year: the choice should outlive a browser restart. */
export const HOUSEHOLD_COOKIE_MAX_AGE = 60 * 60 * 24 * 365;

/**
 * Any id the API's uuid.Parse accepts in X-Household-ID: 8-4-4-4-12 hex.
 * Version and variant nibbles are not checked, because the API does not check
 * them either; this only decides "is this worth sending upstream".
 */
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** True for a usable household id: a plain UUID string. */
export function isHouseholdId(value: unknown): value is string {
  return typeof value === "string" && UUID_RE.test(value.trim());
}

/**
 * The household id in a Cookie request header, or null when absent/malformed.
 * Takes the raw header (what the BFF sees) so callers never parse cookies
 * themselves; a half-written or hostile value is simply "not set".
 */
export function householdFromCookieHeader(cookieHeader: string | null | undefined): string | null {
  if (!cookieHeader) return null;
  for (const part of cookieHeader.split(";")) {
    const eq = part.indexOf("=");
    if (eq < 0) continue;
    if (part.slice(0, eq).trim() !== HOUSEHOLD_COOKIE) continue;
    let value = part.slice(eq + 1).trim();
    try {
      value = decodeURIComponent(value);
    } catch {
      return null; // malformed percent-encoding
    }
    return isHouseholdId(value) ? value.trim() : null;
  }
  return null;
}

/** Cookie options, shared by the browser write and the logout route's expiry. */
export type HouseholdCookieOptions = {
  /** Add `Secure`. Default false: the value is not a credential and dev/E2E run on http. */
  secure?: boolean;
  /** 0 expires the cookie. Default a year. */
  maxAgeSeconds?: number;
};

/** The `Set-Cookie`-shaped value for the household cookie; a null id expires it. */
export function serializeHouseholdCookie(
  id: string | null,
  opts: HouseholdCookieOptions = {},
): string {
  const maxAge = opts.maxAgeSeconds ?? HOUSEHOLD_COOKIE_MAX_AGE;
  const parts = [`${HOUSEHOLD_COOKIE}=${id ? encodeURIComponent(id) : ""}`, "Path=/", `Max-Age=${maxAge}`];
  if (maxAge === 0) parts.push("Expires=Thu, 01 Jan 1970 00:00:00 GMT");
  parts.push("SameSite=Lax");
  if (opts.secure) parts.push("Secure");
  return parts.join("; ");
}

// ---------------------------------------------------------------------------
// Browser side (guarded: this module is also loaded in node --test)
// ---------------------------------------------------------------------------

/** The active household id from document.cookie, or null. */
export function readHouseholdCookie(): string | null {
  if (typeof document === "undefined") return null;
  return householdFromCookieHeader(document.cookie);
}

/** Remember `id` as the active household, or forget it (null) when logging out. */
export function writeHouseholdCookie(id: string | null): void {
  if (typeof document === "undefined") return;
  document.cookie = serializeHouseholdCookie(id);
}
