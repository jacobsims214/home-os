/**
 * Display names for a household membership role (#4574).
 *
 * The enum is the API's membership_role (packages/db; see
 * apps/api/internal/auth/roles.go and KB decisions/2026-11-15-role-policy.md).
 * Nav filtering uses the raw role (components/shared/nav-config.ts); this map is
 * only what a person reads, and it is shared so the switcher and the role that
 * navigation runs on cannot disagree about what "housesitter" is called.
 */

import type { NavRole } from "@/components/shared/nav-config";

export const HOUSEHOLD_ROLE_LABELS: Record<NavRole, string> = {
  owner: "Owner",
  family_manager: "Family manager",
  family_member: "Family member",
  viewer: "Viewer",
  housesitter: "Housesitter",
  vendor: "Vendor",
};

/**
 * A readable label for any role string. An unknown role (a newer API) is
 * title-cased rather than blank, so the row still says something true.
 */
export function householdRoleLabel(role: string | null | undefined): string {
  if (!role) return "Member";
  const known = HOUSEHOLD_ROLE_LABELS[role as NavRole];
  if (known) return known;
  const words = role.replace(/_/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/**
 * A word's indefinite article, chosen by the SOUND of its first syllable, not
 * its spelling: "an owner", "a family manager", "a user", "a one-off".
 *
 * Two closed lists cover every English spelling that breaks the "vowel letter ⇒
 * an" rule, because they are a handful and guessing at them per call site is how
 * "a owner" shipped (#5300):
 *  - silent H (the h is not pronounced, so the word starts on its vowel):
 *    hour, honest, honor/honour, heir;
 *  - a vowel LETTER said as a consonant (yoo-/w-/wun-): uni-, use-, usu-, uti-,
 *    ure-, uro-, ubiq-, eu-, ewe-, one-, once-.
 * Acronyms and numbers are outside this helper's job.
 */
export function indefiniteArticle(word: string): "a" | "an" {
  const w = word.trim().toLowerCase();
  if (w === "") return "a";
  if (/^(hour|honest|honor|honour|heir)/.test(w)) return "an";
  if (/^(uni|use|usu|uti|ure|uro|ubiq|eu|ewe|one\b|once\b)/.test(w)) return "a";
  return /^[aeiou]/.test(w) ? "an" : "a";
}

/**
 * A role with its indefinite article, for prose: "an owner", "a family
 * manager". The label is lowercased, because the phrase is used mid-sentence
 * ("you're an owner here"); `capitalised` gives the sentence-start form
 * ("An owner can change it").
 */
export function householdRoleWithArticle(
  role: string | null | undefined,
  { capitalised = false }: { capitalised?: boolean } = {},
): string {
  const label = householdRoleLabel(role).toLowerCase();
  const article = indefiniteArticle(label);
  if (capitalised) return `${article.charAt(0).toUpperCase()}${article.slice(1)} ${label}`;
  return `${article} ${label}`;
}

/**
 * The switching row's second line: the role this person holds here, and whether
 * this is the one they are in. Text, not colour alone, so the selected family is
 * readable without the accent tint. Kept short so it never wraps a 64 px row.
 */
export function householdRoleSummary(role: string | null | undefined, isActive: boolean): string {
  const label = householdRoleLabel(role);
  return isActive ? `You're here \u00b7 ${label}` : label;
}
