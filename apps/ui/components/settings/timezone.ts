/**
 * IANA time zone validation and the picker's option list (#4560).
 *
 * Why this exists at all: a wrong time zone does not fail loudly. Every
 * consumer loads the stored name and falls back to UTC when it cannot, so junk
 * in households.timezone silently shifts every date the family sees (reminders,
 * bill due dates, when "today" rolls over). The API therefore refuses anything
 * that is not a real IANA zone (apps/api/internal/household/handler.go
 * validateTimezone, #4820, KB api/household-settings.md).
 *
 * The UI runs THE SAME four checks before it sends, so a person gets the reason
 * inline instead of a 400 after a round trip, and the picker can only offer
 * names the API will accept. The API stays the authority: a 400 is still shown
 * verbatim, and nothing here is a security check.
 *
 *   1. not blank after trimming, and within households.timezone VARCHAR(100);
 *   2. not "Local" (the SERVER's zone, which follows the container) and not
 *      "Factory" (tzdata's fixed-UTC test placeholder);
 *   3. written the way the tz database writes it - every "/"-separated segment
 *      starts with an uppercase ASCII letter or a digit and holds only
 *      [A-Za-z0-9_+-]. This is the non-obvious one: browsers accept
 *      "america/chicago" (their ICU resolves it case-insensitively) but a
 *      Linux container's case-sensitive tzdata does not, so accepting it here
 *      would ship a zone that every consumer then reads as UTC;
 *   4. the runtime actually resolves it (Intl.DateTimeFormat with timeZone
 *      throws RangeError for junk like "Mars/Olympus_Mons" or "../etc/passwd").
 *
 * Plain data and pure functions (no JSX, no browser-only API at module load), so
 * `npm test` (node --test) covers the table in timezone.test.mjs.
 */

/** households.timezone is VARCHAR(100). */
export const MAX_TIMEZONE_LENGTH = 100;

/** The two names that resolve but are not family places. */
const NOT_A_PLACE = ["Local", "Factory"];

/** The one message every refusal uses, so the field and the API agree. */
export const TIMEZONE_ERROR = "Use a time zone name from the list, e.g. America/Chicago";

/**
 * Whether a name is written the way the IANA tz database writes a zone:
 * "/"-separated segments that each start with an uppercase ASCII letter or a
 * digit (America/Chicago, Etc/GMT+5, EST5EDT, US/Eastern) and contain only
 * zone-name characters. Mirrors canonicalZoneName in the API.
 */
export function canonicalZoneName(name: string): boolean {
  for (const segment of name.split("/")) {
    if (segment === "") return false;
    const first = segment[0];
    if (!((first >= "A" && first <= "Z") || (first >= "0" && first <= "9"))) return false;
    for (const ch of segment) {
      const ok =
        (ch >= "a" && ch <= "z") ||
        (ch >= "A" && ch <= "Z") ||
        (ch >= "0" && ch <= "9") ||
        ch === "_" ||
        ch === "+" ||
        ch === "-";
      if (!ok) return false;
    }
  }
  return true;
}

/** Whether the runtime's zone database resolves this name. */
function resolvable(name: string): boolean {
  try {
    // Constructing a formatter is the cheapest real check; RangeError is the
    // documented answer for an unknown timeZone.
    new Intl.DateTimeFormat("en-US", { timeZone: name });
    return true;
  } catch {
    return false;
  }
}

/** The error for a time zone the API would refuse, or null when it is fine. */
export function timezoneError(raw: string): string | null {
  const tz = raw.trim();
  if (tz === "") return "Enter a time zone";
  if (tz.length > MAX_TIMEZONE_LENGTH) return TIMEZONE_ERROR;
  if (NOT_A_PLACE.includes(tz)) return TIMEZONE_ERROR;
  if (!canonicalZoneName(tz)) return TIMEZONE_ERROR;
  if (!resolvable(tz)) return TIMEZONE_ERROR;
  return null;
}

export interface TimezoneValidation {
  ok: boolean;
  /** The trimmed zone name; empty when `ok` is false. */
  value: string;
  /** The reason, when `ok` is false. */
  error: string | null;
}

/** timezoneError with the trimmed value kept for the PATCH body. */
export function validateTimezone(raw: string): TimezoneValidation {
  const error = timezoneError(raw);
  return error ? { ok: false, value: "", error } : { ok: true, value: raw.trim(), error: null };
}

/**
 * The picker's options: every zone this runtime knows, plus "UTC", which
 * Intl.supportedValuesOf leaves out (it lists only zone names with a location,
 * and UTC is what the API defaults to). Sorted, so the list reads the same
 * everywhere.
 *
 * Called from an effect, never at module load or during render: the server's
 * Node build and the phone's browser have different tzdata versions, and an
 * option list that differs between render passes is a hydration mismatch.
 */
export function timezoneOptions(): string[] {
  const intl = Intl as typeof Intl & { supportedValuesOf?: (key: "timeZone") => string[] };
  let zones: string[] = [];
  try {
    zones = intl.supportedValuesOf?.("timeZone") ?? [];
  } catch {
    // A very old engine: the field still accepts a typed name, and validation
    // above still decides. The list is a convenience, not the rule.
    zones = [];
  }
  const names = new Set<string>(["UTC", ...zones]);
  // A curated handful of names this build documents and people look for, all
  // resolvable and canonical, that supportedValuesOf omits (it lists the
  // canonical zone names, not the compatibility aliases).
  for (const alias of ["Etc/UTC", "GMT", "CET", "US/Eastern", "US/Central", "US/Mountain", "US/Pacific"]) {
    if (resolvable(alias)) names.add(alias);
  }
  // Array.from, not spread: tsconfig targets es5, where a Set is not iterable
  // without downlevelIteration.
  return Array.from(names).sort((a, b) => a.localeCompare(b));
}
