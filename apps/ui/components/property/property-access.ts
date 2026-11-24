/**
 * Who may change a property, its rooms and its things (#4578).
 *
 * Mirrors the API's role policy rather than inventing one: roles.go gives
 * ResProperties / ResAssets ActWrite to the family roles only (owner,
 * family_manager, family_member). A housesitter and a vendor read the house
 * and nothing more, so the page must not draw Add room / Add thing / Edit for
 * them — a control that answers 403 is the dead button the role policy warns
 * about (KB decisions/2026-11-15-role-policy.md).
 *
 * Pure data and pure functions only, so `node --test` covers it.
 */

const FAMILY = ["owner", "family_manager", "family_member"] as const;

/**
 * True when the role may add or edit here. An unknown/null role (still
 * loading, or the request failed) answers true: the common owner case must not
 * flicker, and the API refuses what the role cannot do.
 */
export function canWriteProperty(role: string | null | undefined): boolean {
  if (role == null || role === "") return true;
  return (FAMILY as readonly string[]).includes(role);
}
