/**
 * Pet care helpers (#4556) — the readings the Pets pages show, in one place so
 * the list and the pet's page cannot disagree.
 *
 * WHY THE BIRTHDAY IS PARSED BY HAND
 *
 * `pets.date_of_birth` is a DATE, not a moment (KB apis/pets.md).
 * `new Date("2020-05-12")` parses it as UTC midnight, so in New York it
 * formats as May 11: a birthday one day early, every time. #4550 paid for the
 * same bug class on bills ("the weekday and the date are the API's"), and
 * entity-list-utils' `formatValue(…, "date")` is exactly that `new Date()`
 * call. So a date-only string is split into its parts and formatted from
 * those: no browser zone can move it, and there is one implementation of it.
 *
 * The age is whole calendar years, counted the way a person counts (has the
 * birthday happened this year or not), against `today` supplied by the caller
 * (`householdToday()`, lib/today/agenda.ts) so the family's zone decides the
 * day and the server render cannot disagree with the client.
 *
 * No JSX and no "@/" imports, so `node --test` loads this file directly
 * (pet-care.test.mjs).
 */

/**
 * What these helpers read off a pet. Structural, so the page's own `Pet`
 * interface (and a raw JSON record from a test) both fit.
 */
export interface PetLike {
  species?: string | null;
  breed?: string | null;
  date_of_birth?: string | null;
  vet_name?: string | null;
  vet_phone?: string | null;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const MONTHS_LONG = [
  "January",
  "February",
  "March",
  "April",
  "May",
  "June",
  "July",
  "August",
  "September",
  "October",
  "November",
  "December",
];

/** A YYYY-MM-DD string split without going through Date's timezone handling. */
export function parseDateOnly(value: string | null | undefined): { y: number; m: number; d: number } | null {
  if (!value) return null;
  const match = /^(\d{4})-(\d{2})-(\d{2})/.exec(value);
  if (!match) return null;
  const y = Number(match[1]);
  const m = Number(match[2]);
  const d = Number(match[3]);
  if (m < 1 || m > 12 || d < 1 || d > 31) return null;
  return { y, m, d };
}

/** "May 12, 2020" from a date-only string, or "" when it is not one. */
export function formatBirthday(value: string | null | undefined): string {
  const p = parseDateOnly(value);
  return p ? `${MONTHS_LONG[p.m - 1]} ${p.d}, ${p.y}` : "";
}

/** "May 2020": the short form. */
export function formatBirthMonth(value: string | null | undefined): string {
  const p = parseDateOnly(value);
  return p ? `${MONTHS[p.m - 1]} ${p.y}` : "";
}

/**
 * Whole years old on `today` ("2026-11-18"), or null when the birthday is
 * unreadable or in the future (a pet cannot be minus two).
 */
export function ageYears(dateOfBirth: string | null | undefined, today: string): number | null {
  const born = parseDateOnly(dateOfBirth);
  const now = parseDateOnly(today);
  if (!born || !now) return null;
  let age = now.y - born.y;
  if (now.m < born.m || (now.m === born.m && now.d < born.d)) age -= 1;
  return age >= 0 && age < 200 ? age : null;
}

/** "6 years", "1 year", or "" when the age is unknown. */
export function ageLabel(dateOfBirth: string | null | undefined, today: string): string {
  const age = ageYears(dateOfBirth, today);
  if (age === null) return "";
  return age === 1 ? "1 year" : `${age} years`;
}

/**
 * The short age the mockup prints under a pet's name ("Cat · 9"): the number,
 * without the word, because a list line is tight. A one-year-old still says
 * "1", and the detail page spells it out.
 */
export function ageShort(dateOfBirth: string | null | undefined, today: string): string {
  const age = ageYears(dateOfBirth, today);
  return age === null ? "" : String(age);
}

/**
 * "Golden retriever · 6": what the list and the detail head show under the
 * name. Breed first (falling back to species, so a mutt still says Dog), then
 * the age when it is known. `today` is null until the household is known, so
 * the age is absent for that render rather than computed in a zone that may
 * be the wrong one.
 */
export function petSubtitle(pet: PetLike, today: string | null): string {
  const species = str(pet.species);
  const breed = str(pet.breed);
  const parts: string[] = [];
  if (breed) parts.push(breed);
  else if (species) parts.push(species);
  const age = today ? ageShort(str(pet.date_of_birth), today) : "";
  if (age) parts.push(age);
  return parts.join(" · ");
}

/**
 * "Golden retriever · 6 · Springfield Animal Hospital": the whole line a row
 * carries, in the mockup's order. When there is no vet it stops after the age;
 * it never prints "No vet" (the pet's page says that, in the Vet section).
 */
export function petLine(pet: PetLike, today: string | null): string {
  return [petSubtitle(pet, today), vetName(pet)].filter(Boolean).join(" · ");
}

/**
 * The vet's name, from the API's read-only `vet_name` (#4700): the contact the
 * pet points at, resolved server-side. Never the id, and never the legacy
 * columns written from a form.
 */
export function vetName(pet: PetLike): string {
  return str(pet.vet_name) ?? "";
}

/** The vet's phone as the API returned it, or "". */
export function vetPhone(pet: PetLike): string {
  return str(pet.vet_phone) ?? "";
}

/** A phone as a `tel:` href: digits and a leading + only. */
export function telHref(phone: string): string {
  return `tel:${phone.replace(/[^\d+]/g, "")}`;
}

/**
 * The pet PUT body (#4700's petUpdateRequest): the fields this page owns, and
 * the vet as `vet_vendor_id` alone. `vet_name` / `vet_phone` are the contact's
 * and are never sent back. An empty text field clears the column (null), which
 * is how a birthday or a breed is removed.
 */
export function petEditBody(values: Record<string, string>, vetVendorId: string | null): Record<string, unknown> {
  const dob = (values.date_of_birth ?? "").trim().slice(0, 10);
  return {
    name: (values.name ?? "").trim(),
    species: blankToNull(values.species),
    breed: blankToNull(values.breed),
    // *time.Time in the API: a date-only string parses, "" does not.
    date_of_birth: dob || null,
    notes: (values.notes ?? "").trim() || null,
    vet_vendor_id: vetVendorId || null,
  };
}

/**
 * A care note split into sentences, so the warning ones can carry emphasis.
 * Every sentence is still shown: nothing is hidden by this.
 */
export function noteSentences(note: string | null | undefined): string[] {
  const text = (note ?? "").trim();
  if (!text) return [];
  return text
    .split(/(?<=[.!?])\s+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

const WARNING =
  /\b(allergic|allergy|allergies|don'?t|do not|never|danger|poison|toxic|avoid|keep away|no treats|no human food)\b/i;

/**
 * Whether a care sentence is a warning worth emphasis. The words are always
 * there beside the colour (never colour alone: KB
 * design/accessibility-and-responsive.md), so this only decides emphasis.
 */
export function isCareWarning(sentence: string): boolean {
  return WARNING.test(sentence);
}

function blankToNull(v: string | undefined): string | null {
  return (v ?? "").trim() || null;
}

function str(v: unknown): string | null {
  return typeof v === "string" && v.trim() ? v.trim() : null;
}
