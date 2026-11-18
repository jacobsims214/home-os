/**
 * The role rules and the colour palette behind the Members page (#4559).
 *
 * WHY THIS FILE EXISTS, in the policy's own terms
 * (KB decisions/2026-11-15-role-policy.md, architecture/api-invite-accept.md):
 *
 *  1. ONLY MANAGERS CHANGE THE FAMILY. `CanManageMembers` is owner +
 *     family_manager (apps/api/internal/auth/roles.go). A family_member sees
 *     the roster and manages nothing. The page must not draw a control that
 *     answers 403, so the same three rules the API applies are mirrored here:
 *     canManageMembers, canGrantRole and canModifyMember.
 *  2. A FAMILY MANAGER NEVER TOUCHES AN OWNER. CanModifyMember(actor, owner) is
 *     owner-only, and CanGrantRole(actor, "owner") is owner-only. So a manager's
 *     role picker has no Owner option and an owner's row has no menu for them.
 *  3. COLOUR IS SELF-OR-MANAGER (#4821, api/person-color.md). The policy grants
 *     people.ActSetColor to the family roles and the handler narrows it from
 *     memberships per request: a manager recolours anyone, a family_member only
 *     their own person record. A viewer, housesitter or vendor cannot recolour
 *     even themselves, because they may not edit a people record at all.
 *  4. THE VALUE IS ONE OF EIGHT NAMES, NEVER HEX. It reaches a style attribute,
 *     so a free-form value would be an injection surface and could not follow
 *     light/dark (#4754). `paletteVar` refuses anything outside the list and the
 *     picker renders only those eight.
 *  5. A ROLE CHANGE IS A REAL CHANGE (#4559 requirement 4): a demotion applies
 *     on the next request and housesitter/vendor change what data is visible.
 *     The confirmation therefore names what the person loses and what they gain,
 *     derived from one capability table rather than hand-written prose per pair.
 *
 * Plain data and pure functions only (no JSX, no Next imports), so `npm test`
 * (node --test) covers every rule in lib/members/roles.test.mjs. Keep the tables
 * in step with apps/api/internal/auth/roles.go — this module only decides what
 * to RENDER; the API still enforces.
 */

/** The six membership_role values, in the order the API's enum declares them. */
export const MEMBER_ROLES = [
  "owner",
  "family_manager",
  "family_member",
  "viewer",
  "housesitter",
  "vendor",
] as const;

export type MemberRole = (typeof MEMBER_ROLES)[number];

export function isMemberRole(role: string | null | undefined): role is MemberRole {
  return !!role && (MEMBER_ROLES as readonly string[]).includes(role);
}

/** owner + family_manager (roles.go CanManageMembers). */
const MANAGERS: readonly MemberRole[] = ["owner", "family_manager"];
/** The three roles that write household data (rolesFamily). */
const FAMILY: readonly MemberRole[] = ["owner", "family_manager", "family_member"];
/** Family roles plus viewer: people full read, money, activity (rolesReaders). */
const READERS: readonly MemberRole[] = [...FAMILY, "viewer"];
/** Readers plus housesitter (rolesSitter: the members list, people.read_basic). */
const SITTERS: readonly MemberRole[] = [...READERS, "housesitter"];

/**
 * The palette (#4821). These eight names are the whole vocabulary: migration
 * 071's CHECK, people.PaletteColours and the UI's Mantine variables are the
 * same list (api/person-color.md).
 */
export const PALETTE = [
  "red",
  "orange",
  "yellow",
  "green",
  "teal",
  "cyan",
  "blue",
  "violet",
] as const;

export type PaletteColor = (typeof PALETTE)[number];

/** What a person reads for a palette name. */
export const PALETTE_LABELS: Record<PaletteColor, string> = {
  red: "Red",
  orange: "Orange",
  yellow: "Yellow",
  green: "Green",
  teal: "Teal",
  cyan: "Cyan",
  blue: "Blue",
  violet: "Violet",
};

export function isPaletteColor(value: unknown): value is PaletteColor {
  return typeof value === "string" && (PALETTE as readonly string[]).includes(value);
}

/**
 * The CSS variable for a palette name, or null for anything else.
 *
 * This is the ONLY place a stored colour becomes a style value. An unknown
 * string (a future API value, a tampered response) returns null, so the caller
 * draws the neutral fallback instead of interpolating it — a value that never
 * passes this function never reaches a style attribute.
 *
 * Shade 6 is a saturated, mid-lightness Mantine shade; app/providers.tsx
 * contrast-fixes every palette colour in both schemes (#4754), so the dot and
 * the picked swatch stay distinguishable in light and dark.
 */
export function paletteVar(value: string | null | undefined, shade = 6): string | null {
  if (!isPaletteColor(value)) return null;
  return `var(--mantine-color-${value}-${shade})`;
}

// ---------------------------------------------------------------------------
// Who may do what
// ---------------------------------------------------------------------------

/** Whether the role manages members at all (roles.go CanManageMembers). */
export function canManageMembers(role: string | null | undefined): boolean {
  return isMemberRole(role) && MANAGERS.includes(role);
}

/**
 * Whether `role` may give someone the role `target` (roles.go CanGrantRole).
 * An owner may grant anything; a family_manager anything except owner.
 */
export function canGrantRole(role: string | null | undefined, target: string | null | undefined): boolean {
  if (!isMemberRole(role) || !isMemberRole(target)) return false;
  if (!MANAGERS.includes(role)) return false;
  if (target === "owner") return role === "owner";
  return true;
}

/**
 * Whether `role` may change or remove a member whose current role is
 * `targetCurrent` (roles.go CanModifyMember). A family_manager never changes or
 * removes an owner.
 */
export function canModifyMember(
  role: string | null | undefined,
  targetCurrent: string | null | undefined,
): boolean {
  if (!isMemberRole(role) || !isMemberRole(targetCurrent)) return false;
  if (!MANAGERS.includes(role)) return false;
  if (targetCurrent === "owner") return role === "owner";
  return true;
}

/** The roles to offer in a role picker (or an invite), for this actor. */
export function grantableRoles(role: string | null | undefined): MemberRole[] {
  return MEMBER_ROLES.filter((r) => canGrantRole(role, r));
}

/**
 * Whether the role may read the members list at all (ResMembers read:
 * rolesSitter). A vendor gets 403, so the page renders an explanation and never
 * asks — the same rule the activity page follows.
 */
export function canSeeMembers(role: string | null | undefined): boolean {
  return isMemberRole(role) && SITTERS.includes(role);
}

/**
 * Whether the role may see a person's full record — birthdays, schools, grades,
 * notes (ResPeople read: rolesReaders). A housesitter gets read_basic: names,
 * relationship and allergies only (api/person-color.md, people/handler.go
 * `view`). The page renders only the fields listed here, so a sitter's roster
 * cannot carry a birthdate even if a response did.
 */
export function canSeePeopleFull(role: string | null | undefined): boolean {
  return isMemberRole(role) && READERS.includes(role);
}

/** Whether the role may set a colour at all (people.ActSetColor: rolesFamily). */
export function canSetColor(role: string | null | undefined): boolean {
  return isMemberRole(role) && FAMILY.includes(role);
}

/**
 * Whether this caller may open the colour picker for a given person: a manager
 * for anyone, a family_member for themselves only (#4821's canSetColor).
 */
export function canSetColorFor(
  callerRole: string | null | undefined,
  opts: { isSelf: boolean },
): boolean {
  if (!canSetColor(callerRole)) return false;
  return opts.isSelf || canManageMembers(callerRole);
}

/**
 * The row actions for one member, or none.
 *
 * `ownerCount` lets the page withhold a demote/remove the API would refuse with
 * 409 anyway: the household must keep at least one owner (ErrLastOwner). Your
 * own row never carries a role/remove action — the mockup shows none, and
 * demoting yourself is the one change a person cannot undo from this page (a
 * demoted manager has no menu left). The API would allow it; the UI declines to
 * make it a one-tap mistake. Colour IS offered on your own row: that is the
 * self-service half of #4821.
 */
export interface MemberActions {
  changeRole: boolean;
  remove: boolean;
  color: boolean;
}

export function memberActions(
  callerRole: string | null | undefined,
  target: { role: string; isSelf: boolean },
  ownerCount: number,
): MemberActions {
  const manager = canManageMembers(callerRole);
  const modifiable = canModifyMember(callerRole, target.role);
  const lastOwner = target.role === "owner" && ownerCount <= 1;
  const manage = manager && modifiable && !target.isSelf && !lastOwner;
  return {
    changeRole: manage,
    remove: manage,
    color: canSetColorFor(callerRole, { isSelf: target.isSelf }),
  };
}

/** True when the row has any action at all, so the page can omit the menu. */
export function hasMemberActions(actions: MemberActions): boolean {
  return actions.changeRole || actions.remove || actions.color;
}

// ---------------------------------------------------------------------------
// What each role can do (the confirmation and the legend)
// ---------------------------------------------------------------------------

interface Capability {
  key: string;
  label: string;
  roles: readonly MemberRole[];
}

/**
 * One capability table, used by BOTH the role legend ("What each role can do")
 * and the role-change confirmation ("what they lose / gain"). Hand-written prose
 * per role pair would drift from the policy; this cannot.
 */
const CAPABILITIES: readonly Capability[] = [
  { key: "manage", label: "Invite people and change roles", roles: MANAGERS },
  { key: "owners", label: "Make someone an owner, or change an owner", roles: ["owner"] },
  { key: "write", label: "See and edit everything in the family", roles: FAMILY },
  { key: "credentials", label: "Open and edit credentials", roles: FAMILY },
  { key: "money", label: "See money: bills, loans and the budget", roles: READERS },
  { key: "people_full", label: "See everyone's full details, birthdays and notes", roles: READERS },
  { key: "people_basic", label: "See people's names, relationship and allergies", roles: SITTERS },
  { key: "day", label: "See the calendar, lists and meal plan", roles: SITTERS },
  { key: "lists", label: "Add and check off list items", roles: [...FAMILY, "housesitter"] },
  { key: "house", label: "See the house, rooms and maintenance jobs", roles: MEMBER_ROLES },
  { key: "jobs", label: "Update maintenance jobs", roles: [...FAMILY, "vendor"] },
  { key: "activity", label: "See the family activity feed", roles: READERS },
];

/** Every capability the role has, in table order. */
export function capabilitiesOf(role: MemberRole): string[] {
  return CAPABILITIES.filter((c) => c.roles.includes(role)).map((c) => c.label);
}

export interface RoleChangeSummary {
  /** True when the person ends up with strictly fewer capabilities. */
  narrower: boolean;
  /** Capabilities the new role gives them that the current one does not. */
  gained: string[];
  /** Capabilities the current role gives them that the new one does not. */
  lost: string[];
}

/** What actually changes when a member moves from one role to another. */
export function roleChangeSummary(from: MemberRole, to: MemberRole): RoleChangeSummary {
  const before = capabilitiesOf(from);
  const after = capabilitiesOf(to);
  // IndexOf over short arrays instead of a Set: the app targets es5 in
  // tsconfig, where spreading a Set is a compile error.
  const gained = after.filter((c) => before.indexOf(c) < 0);
  const lost = before.filter((c) => after.indexOf(c) < 0);
  return { narrower: after.length < before.length && lost.length > 0, gained, lost };
}

/** One sentence per role, for the legend under the roster. */
export const ROLE_SUMMARIES: Record<MemberRole, string> = {
  owner: "Everything, including making other people owners.",
  family_manager: "Everything, plus inviting people and changing roles, but not owners.",
  family_member: "Sees and edits everything, credentials too. Cannot manage people.",
  viewer: "Sees the family but not credentials, and changes nothing.",
  housesitter: "Pets, house, calendar, lists, guides and the meal plan. Names and allergies only.",
  vendor: "The house and its maintenance jobs. Nothing about the family itself.",
};
