/**
 * The one list of app sections (#4528). Every navigation surface reads it:
 * the tablet rail and the header title (DashboardShell), the phone tab bar
 * (#4536) and the sidebar / drawer content (#4543).
 *
 * Order, grouping, labels and icons follow the approved shell spec
 * (KB design/app-shell.md, artifact #75 v1): Today, Calendar, Lists, Meals |
 * "Household": Documents, People & House, Money, Guides | Settings.
 *
 * Plain data and pure helpers only (no JSX), so it can be unit tested.
 */
import type { Icon } from "@tabler/icons-react";
import {
  IconBook,
  IconCalendar,
  IconFileText,
  IconHome,
  IconListCheck,
  IconSettings,
  IconSun,
  IconToolsKitchen,
  IconWallet,
} from "@tabler/icons-react";

export type NavGroup = "main" | "household" | "you";

export type NavSectionId =
  | "today"
  | "calendar"
  | "lists"
  | "meals"
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
   * The page doesn't exist yet (its Stage 3 ticket builds it). Navigation
   * surfaces show the item but don't link to a 404.
   */
  comingSoon?: boolean;
}

export const NAV_GROUP_LABELS: Record<NavGroup, string | null> = {
  main: null,
  household: "Household",
  you: "You",
};

/** Tabler icon stroke used across the shell. */
export const NAV_ICON_STROKE = 1.75;

export const NAV_SECTIONS: readonly NavSection[] = [
  { id: "today", label: "Today", href: "/dashboard", icon: IconSun, group: "main", tab: true },
  { id: "calendar", label: "Calendar", href: "/dashboard/calendar", icon: IconCalendar, group: "main", tab: true },
  { id: "lists", label: "Lists", href: "/dashboard/lists", icon: IconListCheck, group: "main", tab: true, comingSoon: true },
  { id: "meals", label: "Meals", href: "/dashboard/meals", icon: IconToolsKitchen, group: "main", tab: true, comingSoon: true },
  { id: "documents", label: "Documents", href: "/dashboard/documents", icon: IconFileText, group: "household", comingSoon: true },
  {
    id: "people",
    label: "People & House",
    // The People page (#4565) doesn't exist yet; properties is today's way in.
    href: "/dashboard/properties",
    icon: IconHome,
    group: "household",
    matches: [
      "/dashboard/people",
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
  },
  { id: "guides", label: "Guides", href: "/dashboard/guides", icon: IconBook, group: "household", comingSoon: true },
  { id: "settings", label: "Settings", href: "/dashboard/settings", icon: IconSettings, group: "you", matches: ["/dashboard/admin"] },
];

/** Page titles for routes that aren't a section's own page. */
const ROUTE_TITLES: ReadonlyArray<[prefix: string, title: string]> = [
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
  ["/dashboard/search", "Search"],
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
