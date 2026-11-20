/**
 * The one list of app sections (#4528). Every navigation surface reads it:
 * the tablet rail and the header title (DashboardShell), the phone tab bar
 * (#4536) and the sidebar / drawer content (#4543).
 *
 * Order, grouping, labels and icons follow the approved shell spec
 * (KB design/app-shell.md, artifact #75 v1): Today, Calendar, Meals |
 * "Household": People & House, Money | Settings.
 *
 * EVERY SECTION HERE IS A REAL DESTINATION (#5199). The list used to carry
 * Lists, Documents and Guides with `comingSoon: true`, which the shell rendered
 * as a "Soon" pill / "Coming soon" tile: placeholder copy shipped to a family,
 * advertising features the build did not have (artifact #168, D7/D8; the
 * approved artifact has no such badge anywhere). There is deliberately no flag
 * for "planned but unbuilt": a section is added back when its route exists, and
 * a test walks the filesystem to prove every href resolves.
 *
 * Every one of those three has come back since, each when its page landed:
 * Lists in #4566, Documents in #4569, and Guides in #5408. Guides is the case
 * that made the rule hurt: /dashboard/guides shipped in #4570 and was reachable
 * only by typing the URL, because the shell draws itself from this list and the
 * list had no entry for it. A page that exists and a section that is defined
 * are one decision, so add them together (#5408).
 *
 * Documents came back in #4569, when /dashboard/documents landed: it sits in
 * the Household group between Meals and People & House, exactly as the approved
 * shell draws it (artifact #75 v1), and only the roles that may read files see
 * it — the family roles and a viewer. A housesitter and a vendor get 403 from
 * the files API, so the section is not drawn for them.
 *
 * Plain data and pure helpers only (no JSX), so it can be unit tested.
 */
import type { Icon } from "@tabler/icons-react";
import {
  IconBook,
  IconCalendar,
  IconChecklist,
  IconFiles,
  IconHome,
  IconSettings,
  IconSun,
  IconToolsKitchen,
  IconWallet,
} from "@tabler/icons-react";

export type NavGroup = "main" | "household" | "you";

/**
 * Household membership roles (membership_role enum). What each may do is
 * decided by the API's role policy (KB decisions/2026-11-15-role-policy.md,
 * apps/api/internal/auth/roles.go). The UI only hides sections a role can't
 * use; the API enforces.
 */
export type NavRole = "owner" | "family_manager" | "family_member" | "viewer" | "housesitter" | "vendor";

export const NAV_ROLES: readonly NavRole[] = [
  "owner",
  "family_manager",
  "family_member",
  "viewer",
  "housesitter",
  "vendor",
];

const FAMILY: readonly NavRole[] = ["owner", "family_manager", "family_member"];
/** Family roles plus viewer (read-only on everything). */
const READERS: readonly NavRole[] = [...FAMILY, "viewer"];
/** Readers plus housesitter (calendar and meals). */
const SITTERS: readonly NavRole[] = [...READERS, "housesitter"];

export type NavSectionId =
  | "today"
  | "calendar"
  | "meals"
  | "lists"
  | "documents"
  | "people"
  | "money"
  | "guides"
  | "settings";

export interface NavSection {
  id: NavSectionId;
  label: string;
  /** Rail label, where the full label is too wide for 68 px. */
  shortLabel?: string;
  href: string;
  icon: Icon;
  group: NavGroup;
  /** One of the four phone tabs (the fifth tab is "More"). */
  tab?: boolean;
  /**
   * Other route prefixes that belong to this section, so the section is
   * highlighted on today's pages until the Family Hub pages replace them.
   */
  matches?: string[];
  /**
   * Roles that see this section. Left out: every role (including the
   * vendor, who reaches properties and maintenance under People & House).
   */
  roles?: readonly NavRole[];
}

export const NAV_GROUP_LABELS: Record<NavGroup, string | null> = {
  main: null,
  household: "Household",
  you: "You",
};

/** Tabler icon stroke used across the shell. */
export const NAV_ICON_STROKE = 1.75;

export const NAV_SECTIONS: readonly NavSection[] = [
  {
    id: "today",
    label: "Today",
    href: "/dashboard",
    icon: IconSun,
    group: "main",
    tab: true,
    // The full activity history ("see all" behind Today's Family activity
    // panel, #4577) is reached from Today and keeps the Today tab current.
    matches: ["/dashboard/activity"],
  },
  { id: "calendar", label: "Calendar", href: "/dashboard/calendar", icon: IconCalendar, group: "main", tab: true, roles: SITTERS },
  // Meals is the WEEK PLAN and the dinner suggestions (#4568), with the recipe
  // box one tap away on its own tab (/dashboard/recipes). #4567 built the recipe
  // pages and pointed this tab at them only because /dashboard/meals did not
  // exist yet; #4568 built it, so the tab points here and the recipe box stays
  // reachable from the Meals page's header button and its empty states.
  {
    id: "meals",
    label: "Meals",
    href: "/dashboard/meals",
    icon: IconToolsKitchen,
    group: "main",
    tab: true,
    roles: SITTERS,
    matches: ["/dashboard/recipes"],
  },
  {
    // #4566: Lists is a real destination now, so it is back in the nav — the
    // approved shell (artifact #75 v1) draws it right after Meals, and #5199
    // removed it only because there was no page to point at. ResLists in the
    // API's role policy is rolesSitter for reads: family, viewer, housesitter.
    // A vendor does not see the section at all.
    id: "lists",
    label: "Lists",
    href: "/dashboard/lists",
    icon: IconChecklist,
    group: "main",
    roles: SITTERS,
  },
  {
    // #4569: the household's paperwork (the documents hub). READERS is the
    // files read grant in the API's role policy, so a housesitter and a vendor
    // do not see the section at all — the API answers 403 for them.
    id: "documents",
    label: "Documents",
    href: "/dashboard/documents",
    icon: IconFiles,
    group: "household",
    roles: READERS,
  },
  {
    id: "people",
    label: "People & House",
    // The People page (#4565) is the way in: the family comes first, the house
    // follows from the same section (properties, vehicles, pets, vendors).
    href: "/dashboard/people",
    icon: IconHome,
    group: "household",
    matches: [
      "/dashboard/people",
      // Properties led this section before the People page existed; it is
      // still part of it now that the href is People.
      "/dashboard/properties",
      "/dashboard/members",
      "/dashboard/assets",
      "/dashboard/maintenance",
      "/dashboard/vehicles",
      "/dashboard/pets",
      "/dashboard/vendors",
    ],
  },
  {
    id: "money",
    label: "Money",
    // The Money overview (#4571) doesn't exist yet; bills is today's way in.
    href: "/dashboard/bills",
    icon: IconWallet,
    group: "household",
    matches: ["/dashboard/money", "/dashboard/loans"],
    roles: READERS,
  },
  {
    // #5408: Guides is the family's how-tos and runbooks (/dashboard/guides,
    // #4570). It sits last in the Household group, where the approved shell
    // draws it (artifact #75 v1), and it is the tile the More sheet was missing:
    // before this, no nav surface anywhere in the app linked to the page.
    //
    // The roles are the page's own read grant, not a guess: guides read is
    // ResGuides = rolesSitter in apps/api/internal/auth/roles.go, i.e. family +
    // viewer + housesitter, and writes are family only (#4689). A housesitter
    // reading "reset the router" is the whole reason guides exist, so the
    // section is SITTERS — the same grant as Calendar, Meals and Lists. A vendor
    // has no read grant and does not see the section.
    id: "guides",
    label: "Guides",
    href: "/dashboard/guides",
    icon: IconBook,
    group: "household",
    roles: SITTERS,
  },
  { id: "settings", label: "Settings", href: "/dashboard/settings", icon: IconSettings, group: "you", matches: ["/dashboard/admin"] },
];

/** Page titles for routes that aren't a section's own page. */
const ROUTE_TITLES: ReadonlyArray<[prefix: string, title: string]> = [
  // The People page (#4565); a person's own page keeps the same title.
  ["/dashboard/people", "People"],
  ["/dashboard/properties", "Properties"],
  ["/dashboard/members", "Members"],
  ["/dashboard/assets", "Assets"],
  ["/dashboard/maintenance", "Maintenance"],
  ["/dashboard/vehicles", "Vehicles"],
  ["/dashboard/pets", "Pets"],
  ["/dashboard/vendors", "Vendors"],
  ["/dashboard/bills", "Bills"],
  ["/dashboard/loans", "Loans"],
  ["/dashboard/admin", "Admin"],
  ["/dashboard/meals", "Meals"],
  ["/dashboard/recipes", "Recipes"],
  // A single list keeps "Lists", like a recipe keeps "Recipes"; the section's
  // own label covers it, so no entry is needed here.
  // The Documents hub's own page is the section (its title comes from there);
  // a single document keeps "Documents", like a recipe keeps "Recipes".
  ["/dashboard/documents", "Documents"],
  ["/dashboard/search", "Search"],
  // "See all" behind Today's Family activity panel (#4577). It is not a nav
  // section of its own — the header still reads "Family activity", and the tab
  // bar keeps Today current (activeSection falls back to Today for it).
  ["/dashboard/activity", "Family activity"],
  // Guides (#4570) is a real page with no nav section of its own (#5199), so
  // without this entry the header h1 fell through to "Home OS" — the app's
  // name instead of the page's, which is what #5409 found. It sits below
  // Documents here because a guide keeps the list's title, like a recipe.
  ["/dashboard/guides", "Guides"],
  // Settings' sub-routes (#4560). Each section page draws its body under this
  // header, so without these the h1 read "Settings" on all six and the
  // document outline named the same page everywhere (#5409). The labels are
  // the ones in SETTINGS_SECTIONS (components/settings/settings-sections.ts),
  // which is what the section page's own h2 shows — keep the two in step.
  ["/dashboard/settings/household", "Household"],
  ["/dashboard/settings/members", "Members"],
  ["/dashboard/settings/credentials", "Credentials"],
  ["/dashboard/settings/phone-calendar", "Phone calendar"],
  ["/dashboard/settings/agent-tokens", "Agent tokens"],
  ["/dashboard/settings/account", "Account"],
];

function underPrefix(pathname: string, prefix: string): boolean {
  return pathname === prefix || pathname.startsWith(prefix + "/");
}

/** The section a route belongs to, or null (e.g. search). */
export function activeSection(pathname: string | null | undefined): NavSection | null {
  const path = (pathname || "/").replace(/\/+$/, "") || "/";
  let best: NavSection | null = null;
  let bestLen = -1;
  for (const s of NAV_SECTIONS) {
    for (const prefix of [s.href, ...(s.matches ?? [])]) {
      // "/dashboard" is Today's own page only, not a prefix of everything.
      const hit = prefix === "/dashboard" ? path === prefix : underPrefix(path, prefix);
      if (hit && prefix.length > bestLen) {
        best = s;
        bestLen = prefix.length;
      }
    }
  }
  return best;
}

export function isSectionActive(section: NavSection, pathname: string | null | undefined): boolean {
  return activeSection(pathname)?.id === section.id;
}

/** The header title (h1) for a route. */
export function pageTitle(pathname: string | null | undefined): string {
  const path = (pathname || "/").replace(/\/+$/, "") || "/";
  for (const [prefix, title] of ROUTE_TITLES) {
    if (underPrefix(path, prefix)) return title;
  }
  return activeSection(path)?.label ?? "Home OS";
}

export function sectionsInGroup(group: NavGroup): NavSection[] {
  return NAV_SECTIONS.filter((s) => s.group === group);
}

/** The four phone tabs, in order (#4536 adds "More" as the fifth). */
export const TAB_SECTIONS: readonly NavSection[] = NAV_SECTIONS.filter((s) => s.tab);

/**
 * Whether a role sees a section in the navigation.
 * - `null`/`undefined`: the role isn't known yet (still loading, or the
 *   request failed). Everything shows, so the common owner case doesn't
 *   flicker; the API refuses what the role can't use.
 * - A role this build doesn't know: only the sections every role sees
 *   (fails closed, like the API's unknown entity kinds).
 */
export function canSeeSection(section: NavSection, role: string | null | undefined): boolean {
  if (!section.roles) return true;
  if (role == null || role === "") return true;
  if (!(NAV_ROLES as readonly string[]).includes(role)) return false;
  return section.roles.includes(role as NavRole);
}

/** NAV_SECTIONS (or the given list) narrowed to what a role sees. */
export function sectionsForRole(
  role: string | null | undefined,
  sections: readonly NavSection[] = NAV_SECTIONS,
): NavSection[] {
  return sections.filter((s) => canSeeSection(s, role));
}
