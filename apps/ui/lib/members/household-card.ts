/**
 * The "This family" card's text (#5300), as pure functions so `npm test` covers
 * it without React.
 *
 * WHY THIS EXISTS — the two faults it removes
 *
 * The card read `hh?.data?.timezone` from `GET /api/v1/households/me`, which
 * serialises the Go `household.Household` struct with no json tags
 * (`apps/api/internal/household/handler.go`, `map[string]any{"data": hh}`). The
 * wire payload is therefore `{"data":{"ID","Name","Timezone","Settings",…}}` —
 * capital T. `data.timezone` was `undefined` on every real deployment, so a
 * family with America/New_York was told "Time zone not set" and its name was
 * replaced by "This family". The e2e stub answered that route in lowercase
 * (`e2e/members.spec.ts`), so no test ever saw it.
 *
 * The household's timezone has ONE home in the UI: `GET /api/v1/me/households`
 * through `useCurrentHousehold()` (components/shared/use-nav-role.ts), whose
 * `UserHousehold` carries a real `json:"timezone"` tag and which Today,
 * Calendar, Pets, People, Bills and search already read. The page reads the card
 * from that same object — not from a second endpoint, and not from a per-user
 * profile field that would be a second source of truth about the family.
 *
 * AND THE SECOND HALF, from #5305: an UNKNOWN household is not a household with
 * no zone. While the list is still loading the card must not claim "Time zone
 * not set" (that is a statement about the family, made on no evidence); it says
 * nothing about the zone until it knows one. Only a household that really has an
 * empty `timezone` gets the "not set" sentence.
 */

import { householdRoleWithArticle } from "../household-roles.ts";

/** The fields of a household the card reads (GET /me/households, UserHousehold). */
export interface HouseholdSummary {
  id?: string;
  name?: string | null;
  timezone?: string | null;
}

/** What the card's name line says when the household is not known yet. */
export const HOUSEHOLD_UNKNOWN_NAME = "This family";

/** The sentence for a household whose zone really is empty (never for "unknown"). */
export const TIMEZONE_NOT_SET = "Time zone not set";

/**
 * The card's title: the family's name, or a neutral placeholder while the
 * household is unknown. A blank name counts as unknown — the API refuses to
 * store one (`PATCH /households/{id}` 400s on `""`), so it means "not read yet".
 */
export function householdCardName(household: HouseholdSummary | null | undefined): string {
  const name = household?.name?.trim();
  return name ? name : HOUSEHOLD_UNKNOWN_NAME;
}

/** The mark's letter: the first letter of the name the card shows, so the mark
 * and the title can never disagree. */
export function householdCardInitial(household: HouseholdSummary | null | undefined): string {
  return householdCardName(household).charAt(0).toUpperCase();
}

/**
 * The zone as it should be displayed. `null` means "we don't know yet" and the
 * card renders nothing for it; a known-but-empty zone is the only case that
 * says "Time zone not set".
 */
export function householdTimezoneLabel(household: HouseholdSummary | null | undefined): string | null {
  if (household == null) return null;
  const zone = household.timezone?.trim();
  return zone ? zone : TIMEZONE_NOT_SET;
}

/**
 * The card's meta line: the zone, then the viewer's role in it —
 * "America/New_York · you're an owner".
 *
 * The role half is written through `householdRoleWithArticle` so the article
 * matches the word ("an owner", "a family manager"); the bug #5300 fixes was a
 * hand-written "a " in front of a label that starts with a vowel.
 *
 * Either half can be absent, and an absent half leaves no dangling "·".
 */
export function householdCardMeta(
  household: HouseholdSummary | null | undefined,
  role: string | null | undefined,
): string {
  const parts: string[] = [];
  const zone = householdTimezoneLabel(household);
  if (zone) parts.push(zone);
  if (role) parts.push(`you're ${householdRoleWithArticle(role)}`);
  return parts.join(" · ");
}