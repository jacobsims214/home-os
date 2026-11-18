/**
 * The Settings page's sections (#4560), in the order they are listed.
 *
 * Settings used to be one long page. It is now a section index plus one
 * sub-route per section, which is the app's own idiom for "a list, then one
 * record": every detail page here is its own route (`/dashboard/members`,
 * `/dashboard/bills/[id]`), so Back works, a section can be linked to, and the
 * phone gets a real screen per section instead of a 700 px scroll. The layout
 * follows the approved settings mockup (artifact #103 v1): phone = section
 * index -> section page; >= 768 px = a sticky section list beside the pane.
 *
 * The table below is also the role gate. It mirrors the API's household role
 * policy (apps/api/internal/auth/roles.go, middleware/roles.go, KB
 * decisions/2026-11-15-role-policy.md): a section a role cannot use is not
 * rendered at all, because an entry that opens a 403 teaches someone to tap a
 * dead control. The API enforces; this only decides what is drawn.
 *
 * Agent tokens (#4580) and the phone-calendar rebuild (#4583) are separate
 * tickets. Agent tokens has no UI yet, so it is deliberately absent here rather
 * than shipped as a link to nowhere (#5199's rule); the phone-calendar section
 * carries the app-password block that already works, so #4583 can replace its
 * body without moving the route.
 *
 * Plain data and pure helpers only (no JSX), so `npm test` covers the role
 * table in settings-sections.test.mjs.
 */

import type { Icon } from "@tabler/icons-react";
import { IconDeviceMobile, IconHome, IconKey, IconUser, IconUsers } from "@tabler/icons-react";

/** Household roles, as the API names them (membership_role enum). */
export type SettingsRole = "owner" | "family_manager" | "family_member" | "viewer" | "housesitter" | "vendor";

export const SETTINGS_ROLES: readonly SettingsRole[] = [
  "owner",
  "family_manager",
  "family_member",
  "viewer",
  "housesitter",
  "vendor",
];

/** Roles that may change household settings (API: ResHousehold ActWrite). */
const MANAGERS: readonly SettingsRole[] = ["owner", "family_manager"];
/** Roles that may use the credentials vault (API: ResSecrets). */
const FAMILY: readonly SettingsRole[] = ["owner", "family_manager", "family_member"];
/**
 * Roles that may read the member list (API: GET /households/me/members is
 * granted to `sitters`: family + viewer + housesitter, not the vendor, whose
 * login is about the house and its jobs).
 */
const SITTERS: readonly SettingsRole[] = [...FAMILY, "viewer", "housesitter"];

export type SettingsSectionId = "household" | "members" | "credentials" | "phone-calendar" | "account";

export interface SettingsSection {
  id: SettingsSectionId;
  /** The section page's h2 and the list row's title. */
  label: string;
  /** One line under the title in the list. */
  hint: string;
  /** Route, relative to the section index. */
  path: string;
  icon: Icon;
  /** Roles that see the section. Left out: every role. */
  roles?: readonly SettingsRole[];
}

/** The route of the section index. Sub-routes keep the shell's Settings active. */
export const SETTINGS_BASE = "/dashboard/settings";

export const SETTINGS_SECTIONS: readonly SettingsSection[] = [
  {
    id: "household",
    label: "Household",
    hint: "Name, time zone and how Home OS looks",
    path: `${SETTINGS_BASE}/household`,
    icon: IconHome,
    // Every role reads it; only owners and family managers can change it, and
    // the page itself renders read-only for everybody else.
  },
  {
    id: "members",
    label: "Members",
    hint: "Who can sign in to this family",
    path: `${SETTINGS_BASE}/members`,
    icon: IconUsers,
    roles: SITTERS,
  },
  {
    id: "credentials",
    label: "Credentials",
    hint: "Family logins and codes, locked with the master password",
    path: `${SETTINGS_BASE}/credentials`,
    icon: IconKey,
    roles: FAMILY,
  },
  {
    id: "phone-calendar",
    label: "Phone calendar",
    hint: "Add the family calendars to your phone's Calendar app",
    path: `${SETTINGS_BASE}/phone-calendar`,
    icon: IconDeviceMobile,
  },
  {
    id: "account",
    label: "Account",
    hint: "Sign-in email and password",
    path: `${SETTINGS_BASE}/account`,
    icon: IconUser,
  },
];

/**
 * Whether a role may open a section.
 * - `null`/`undefined`: the role isn't known yet (still loading, or the
 *   request failed). Everything shows, so the common owner case doesn't
 *   flicker; the API refuses what the role can't use. Same rule as
 *   nav-config's canSeeSection.
 * - A role this build doesn't know: only the sections every role sees (fails
 *   closed).
 */
export function canSeeSettingsSection(
  section: SettingsSection,
  role: string | null | undefined,
): boolean {
  if (!section.roles) return true;
  if (role == null || role === "") return true;
  if (!(SETTINGS_ROLES as readonly string[]).includes(role)) return false;
  return section.roles.includes(role as SettingsRole);
}

/** SETTINGS_SECTIONS (or the given list) narrowed to what a role sees. */
export function settingsSectionsForRole(
  role: string | null | undefined,
  sections: readonly SettingsSection[] = SETTINGS_SECTIONS,
): SettingsSection[] {
  return sections.filter((s) => canSeeSettingsSection(s, role));
}

/** The section a route names, or null when the segment is not one. */
export function findSettingsSection(sectionId: string | null | undefined): SettingsSection | null {
  if (!sectionId) return null;
  return SETTINGS_SECTIONS.find((s) => s.id === sectionId) ?? null;
}

/**
 * Where the index sends a wide screen: the first section this role may open.
 * Null when the role may open none (impossible today, since Household and
 * Account have no role gate, but the caller must not build "/undefined").
 */
export function firstSectionHref(role: string | null | undefined): string | null {
  const sections = settingsSectionsForRole(role);
  return sections[0]?.path ?? null;
}

/** Whether a role may change household name and time zone (API: managers only). */
export function canManageHousehold(role: string | null | undefined): boolean {
  if (role == null || role === "") return false;
  return MANAGERS.includes(role as SettingsRole);
}

/** "family_manager" -> "Family manager"; null/unknown -> "Member". */
export function roleLabel(role: string | null | undefined): string {
  switch (role) {
    case "owner":
      return "Owner";
    case "family_manager":
      return "Family manager";
    case "family_member":
      return "Family member";
    case "viewer":
      return "Viewer";
    case "housesitter":
      return "Housesitter";
    case "vendor":
      return "Vendor";
    default:
      return "Member";
  }
}
