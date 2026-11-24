/**
 * People helpers (#4565) — the readings the People list and a person's page
 * show, and the ONE place the PUT body is built.
 *
 * WHY THE EDIT BODY LIVES HERE
 * `PUT /api/v1/people/{id}` REPLACES every editable field: an optional field
 * left out of the body is CLEARED, and `allergies` is no different (KB
 * api/people-preferences.md; the manager note on #4565). A form that saves
 * just the name, or that sends `allergies` only when the person touched that
 * field, silently erases a child's allergy — the one fact on this record that
 * must never disappear. So there is exactly one function that builds the body,
 * it always carries the person's CURRENT allergies, and person-facts.test.mjs
 * proves a rename keeps them.
 *
 * ALLERGY NOTES. Since #4742 an allergy may be one free-text entry,
 * "peanuts (carries an EpiPen)". The bare allergen is the part before the
 * first " (" — that is what the summary line lists, and the parenthetical is
 * what a sitter needs to read.
 *
 * DATE-ONLY VALUES. `birthdate` is a DATE: `new Date("2012-03-04")` is UTC
 * midnight and renders as March 3 in New York (#4550's bug class). The
 * date-only helpers already exist (and are tested) in
 * components/pet/pet-care.ts; they are imported rather than copied, so there
 * is still only one implementation of "read a date-only string by its parts".
 *
 * Plain functions and types, no JSX and no Next imports, so `node --test`
 * loads this file directly.
 */
import { ageLabel, ageShort, formatBirthday, parseDateOnly } from "../pet/pet-care.ts";

/** A person as GET /api/v1/people returns it (apps/api/internal/people/handler.go). */
export interface PersonLike {
  id: string;
  display_name: string;
  relationship?: string | null;
  birthdate?: string | null;
  school?: string | null;
  grade?: string | null;
  allergies?: string[] | null;
  notes?: string | null;
  color?: string | null;
  user_id?: string | null;
  has_login?: boolean;
}

/** A person's preference row (GET /api/v1/people/{id} embeds these). */
export interface PreferenceLike {
  id: string;
  kind: string;
  category: string;
  value: string;
  notes?: string | null;
}

/** The four person_preference_kind values, in the order the pages show them. */
export const PREFERENCE_KINDS = ["like", "dislike", "dietary", "other"] as const;

export const PREFERENCE_LABELS: Record<string, string> = {
  like: "Likes",
  dislike: "Dislikes",
  dietary: "Dietary",
  other: "Other",
};

/** What a preference kind is called on a form (one value, not a list). */
export function preferenceKindLabel(kind: string): string {
  return (PREFERENCE_LABELS[kind] ?? "Other").replace(/s$/, "");
}

/** The bare allergen: "peanuts (carries an EpiPen)" → "peanuts". */
export function bareAllergen(entry: string): string {
  const text = (entry ?? "").trim();
  const cut = text.indexOf(" (");
  return (cut === -1 ? text : text.slice(0, cut)).trim();
}

/** The note inside an allergy entry, or "" when it has none. */
export function allergyNote(entry: string): string {
  const text = (entry ?? "").trim();
  const open = text.indexOf(" (");
  if (open === -1) return "";
  return text.slice(open + 2, text.endsWith(")") ? -1 : undefined).trim();
}

/** The person's allergies as display entries, blanks dropped. */
export function allergyEntries(person: Pick<PersonLike, "allergies">): string[] {
  return (person.allergies ?? []).map((a) => (a ?? "").trim()).filter(Boolean);
}

/**
 * "Allergic to peanuts, tree nuts" — the one line a list card shows. Empty
 * when the person has none, so a card never prints "no allergies".
 */
export function allergySummary(person: Pick<PersonLike, "allergies">): string {
  const names = allergyEntries(person).map(bareAllergen).filter(Boolean);
  return names.length > 0 ? `Allergic to ${names.join(", ")}` : "";
}

/** A person's initials, for the colour dot: "Nora Sims" → "NS". */
export function initials(name: string | null | undefined): string {
  const words = (name ?? "").trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return "?";
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return (words[0][0] + words[words.length - 1][0]).toUpperCase();
}

/** "Daughter" from "daughter": the first letter goes up, nothing else changes. */
export function relationLabel(relationship: string | null | undefined): string {
  const text = (relationship ?? "").trim();
  return text ? text[0].toUpperCase() + text.slice(1) : "";
}

/**
 * Whether this person is a SIGN-IN ACCOUNT or a record only (#5696).
 *
 * The household holds both, and they are different tables: a `person` is a
 * record (a kid with an allergy, a grandparent, a nanny); a `membership` is a
 * login with a role. `people.user_id` is the link — non-null means a
 * membership exists for them.
 *
 * ONE RULE, ONE HOME. `has_login` is the API's own answer
 * (apps/api/internal/people/handler.go:135, `p.UserID != nil`), and it is
 * deliberately among the fields a housesitter still gets (read_basic, same
 * file:347-357): a sitter must not mistake a child for an adult with a login.
 * Prefer it; fall back to `user_id` only for callers that hold a person row
 * without the flag. Never derive this from relationship or age — a 16-year-old
 * may have an account and a nanny may not.
 */
export function isAccount(person: Pick<PersonLike, "has_login" | "user_id">): boolean {
  if (typeof person.has_login === "boolean") return person.has_login;
  return !!person.user_id;
}

/**
 * The badge that says which one this person is. Two words, the same two
 * everywhere: "Signs in" (an account, with a role in Members) and "Record"
 * (no login — the child, the grandparent, the nanny).
 */
export function accountLabel(person: Pick<PersonLike, "has_login" | "user_id">): string {
  return isAccount(person) ? "Signs in" : "No login";
}

/**
 * "Daughter · 8": what a list card carries under the name. The relationship
 * leads (it is how a family reads its own household), then the age when the
 * birthday is known. `today` is null until the household is known, so an age
 * is absent for that render rather than computed in a zone that may be wrong.
 */
export function personSubtitle(person: PersonLike, today: string | null): string {
  const parts: string[] = [];
  const relation = relationLabel(person.relationship);
  if (relation) parts.push(relation);
  // The short age, the way a pet's list line reads ("Daughter · 14"): a list
  // line is tight, and the person's page spells it out.
  const age = personAge(person, today);
  if (age) parts.push(age);
  return parts.join(" · ");
}

/**
 * The short age a list prints ("14"), or "" when the birthday is unknown or
 * `today` is not known yet. One implementation, used by the card's subtitle,
 * the table's Age cell and the page's head line.
 */
export function personAge(person: Pick<PersonLike, "birthdate">, today: string | null): string {
  return today ? ageShort(person.birthdate, today) : "";
}

/** "March 4, 2012 (14 years old)", or an em dash when unknown. */
export function bornValue(person: PersonLike, today: string | null): string {
  const birthday = formatBirthday(person.birthdate);
  if (!birthday) return "\u2014";
  const age = today ? ageLabel(person.birthdate, today) : "";
  return age ? `${birthday} (${age} old)` : birthday;
}

/** A school and grade on one line: "Springfield Elementary · 4th". */
export function schoolValue(person: Pick<PersonLike, "school" | "grade">): string {
  const parts = [(person.school ?? "").trim(), (person.grade ?? "").trim()].filter(Boolean);
  return parts.length > 0 ? parts.join(" · ") : "";
}

/**
 * A typed list of allergies → the array the API stores. One per line (or a
 * comma), trimmed, blanks dropped, case-insensitive duplicates dropped, and
 * the first spelling kept. This mirrors the API's own normalisation in
 * personRequest.input(), so what the form shows after a save is what it sent.
 */
export function splitAllergies(text: string | null | undefined): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of (text ?? "").split(/[\n,]/)) {
    const value = raw.trim();
    if (!value) continue;
    const key = value.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(value);
  }
  return out;
}

/**
 * The person PUT body: every editable field, and ALWAYS the allergies.
 *
 * `currentAllergies` is the record's allergies as they stand (from the detail
 * read, or the form's own edited list). Passing them through untouched is the
 * whole point: a PUT that omits `allergies` clears them, so "edit the grade"
 * would otherwise delete a life-threatening fact. An empty string clears a
 * text field (the API stores NULL), which is how a grade or a school is
 * removed.
 */
export function personEditBody(
  values: Record<string, string>,
  currentAllergies: readonly string[],
): Record<string, unknown> {
  // birthdate is *string "YYYY-MM-DD" in the API; the detail read may hand back
  // a timestamp, so only the date part is sent, and only when it is a date.
  const born = (values.birthdate ?? "").trim().slice(0, 10);
  const validBorn = parseDateOnly(born) ? born : null;
  return {
    display_name: (values.display_name ?? "").trim(),
    relationship: blankToNull(values.relationship),
    birthdate: validBorn,
    school: blankToNull(values.school),
    grade: blankToNull(values.grade),
    // Never omitted, never filtered away: the record's allergies go back as-is.
    allergies: currentAllergies.map((a) => (a ?? "").trim()).filter(Boolean),
    notes: blankToNull(values.notes),
  };
}

/** The create body: the same shape; a brand-new person may have no allergies. */
export function personCreateBody(values: Record<string, string>, allergies: readonly string[]): Record<string, unknown> {
  return personEditBody(values, allergies);
}

/** Preferences grouped by kind, in PREFERENCE_KINDS order; empty kinds dropped. */
export function preferenceGroups(
  preferences: readonly PreferenceLike[] | null | undefined,
): { kind: string; label: string; items: PreferenceLike[] }[] {
  const all = preferences ?? [];
  return PREFERENCE_KINDS.map((kind) => ({
    kind,
    label: PREFERENCE_LABELS[kind],
    items: all.filter((p) => p.kind === kind),
  })).filter((group) => group.items.length > 0);
}

/** A preference's category, unless it is the API's default "general". */
export function preferenceCategory(category: string | null | undefined): string {
  const text = (category ?? "").trim();
  return text && text.toLowerCase() !== "general" ? text : "";
}

function blankToNull(value: string | undefined): string | null {
  return (value ?? "").trim() || null;
}
