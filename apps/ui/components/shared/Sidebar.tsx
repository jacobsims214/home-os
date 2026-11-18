"use client";

/**
 * Sidebar and drawer content (#4543), rendered from nav-config so the
 * desktop sidebar, the phone/tablet drawer, the rail and the tab bar show
 * the same sections. Built to the approved shell spec (KB
 * design/app-shell.md, artifact #75 v1).
 *
 * - variant "sidebar" (desktop >= 992 px, 260 px column): brand, family card,
 *   search with a "/" shortcut, 40 px nav rows, the "Household" group, and a
 *   foot with Settings and the user button. The user button opens the
 *   account menu upward (theme picker, log out).
 * - variant "drawer" (phone and tablet, inside DashboardShell's Drawer, which
 *   draws the brand row and close button): family card, 16 px search, 48 px
 *   rows grouped "Household" and "You", and a foot with the avatar, name,
 *   email and a Log out button.
 *
 * Sections are filtered by the user's household role (useNavSections). The
 * UI only hides; the API enforces the role policy.
 *
 * Notifications live on the header bell at every breakpoint (spec), so the
 * old in-sidebar bell is gone. Log out is a native POST to /api/auth/logout
 * (#4424) in both variants.
 *
 * `familySlot` IS the family card: the shell passes the family switcher (#4574)
 * so the name, the role and the "5 members" line come from the same household
 * the whole app is scoped to. There is no fallback card on purpose — a second
 * implementation reading GET /households/me would be a second source of truth
 * about which family you are in.
 */

import Link from "next/link";
import { usePathname } from "next/navigation";
import { Avatar, UnstyledButton, VisuallyHidden } from "@mantine/core";
import { IconChevronRight, IconLogout } from "@tabler/icons-react";
import AccountMenu, { useAccountMenuState } from "@/components/account/AccountMenu";
import LogoutForm from "@/components/shared/LogoutForm";
import { useAccountIdentity } from "@/components/shared/use-account";
import {
  NAV_GROUP_LABELS,
  NAV_ICON_STROKE,
  activeSection,
  type NavGroup,
  type NavSection,
} from "@/components/shared/nav-config";
import { useNavSections } from "@/components/shared/use-nav-role";
import HomeMark from "@/components/shared/HomeMark";
import SidebarSearch from "@/components/search/SidebarSearch";
import classes from "./Sidebar.module.css";

export type SidebarVariant = "sidebar" | "drawer";

interface SidebarProps {
  variant?: SidebarVariant;
  /** The family card: the family switcher (#4574), supplied by the shell. */
  familySlot: React.ReactNode;
}

export default function Sidebar({ variant = "sidebar", familySlot }: SidebarProps) {
  const pathname = usePathname();
  const current = activeSection(pathname)?.id;
  const sections = useNavSections();
  const inGroup = (g: NavGroup) => sections.filter((s) => s.group === g);
  const drawer = variant === "drawer";

  const main = inGroup("main");
  const household = inGroup("household");
  const you = inGroup("you");

  return (
    <div className={classes.root} data-variant={variant} data-testid={`nav-${variant}`}>
      {!drawer && <Brand />}

      <div className={classes.family}>{familySlot}</div>

      <SidebarSearch variant={variant} />

      <div className={classes.nav}>
        <NavList sections={main} current={current} />
        {household.length > 0 && (
          <NavList label={NAV_GROUP_LABELS.household} sections={household} current={current} />
        )}
        {/* "You" is a group in the drawer; the sidebar pins it to its foot. */}
        {drawer && you.length > 0 && <NavList label={NAV_GROUP_LABELS.you} sections={you} current={current} />}
      </div>

      <div className={classes.foot}>
        {!drawer && you.length > 0 && <NavList sections={you} current={current} />}
        {drawer ? <DrawerAccount /> : <SidebarAccount />}
      </div>
    </div>
  );
}

// ─── Brand ────────────────────────────────────────────────────

/**
 * The accent logo tile and wordmark (also the drawer's title).
 *
 * The mark is `HomeMark` (#5200): the approved shell mockup's house glyph on
 * the 28 px accent tile, transcribed from artifact #75 v1. It used to be a
 * letter "H", which read as a placeholder for the product's only piece of
 * brand. The tile's geometry now lives in that component, so the mark is
 * identical wherever it is used; this row only supplies the wordmark.
 */
export function Brand({ as: Tag = "div" }: { as?: "div" | "span" }) {
  return (
    <Tag className={classes.brand}>
      <HomeMark size={28} />
      <span className={classes.brandName}>Home OS</span>
    </Tag>
  );
}

// ─── Search ───────────────────────────────────────────────────

// The box lives in components/search/SidebarSearch.tsx (#5303): it was here as
// a submit-only form, which on a phone was a box that did nothing while you
// typed. It is a component of its own because it now runs the real search
// (debounce, drop-down, empty/error states) through the same hook and the same
// rows as the Search page, and because that is more than a nav file should own.

// ─── Nav rows ─────────────────────────────────────────────────

function NavList({
  label,
  sections,
  current,
}: {
  label?: string | null;
  sections: NavSection[];
  current: string | undefined;
}) {
  return (
    <div className={classes.group}>
      {label && (
        <div className={classes.groupLabel} aria-hidden>
          {label}
        </div>
      )}
      <ul className={classes.list} aria-label={label ?? undefined}>
        {sections.map((s) => (
          <li key={s.id}>
            <NavRow section={s} active={current === s.id} />
          </li>
        ))}
      </ul>
    </div>
  );
}

function NavRow({ section, active }: { section: NavSection; active: boolean }) {
  const Icon = section.icon;
  const inner = (
    <>
      <Icon size={20} stroke={NAV_ICON_STROKE} className={classes.rowIcon} aria-hidden />
      <span className={classes.rowLabel}>{section.label}</span>
    </>
  );

  // Every section in nav-config is a real page (#5199): a destination with no
  // page is not in the list at all, so a row is always a link.
  return (
    <Link
      href={section.href}
      className={classes.row}
      aria-current={active ? "page" : undefined}
      data-active={active || undefined}
    >
      {inner}
    </Link>
  );
}

// ─── Account ──────────────────────────────────────────────────

/**
 * Desktop foot: the user button opens the account menu upward (#5198).
 *
 * The menu itself is components/account/AccountMenu — the same identity header,
 * Profile / Theme / Notification settings rows and ghost-danger Log out that the
 * phone's bottom sheet shows — placed as a popover here because the artifact
 * (#75 v1) draws it opening upward from the sidebar's user button at 1440 px.
 *
 * Named by its content, not aria-label, so the visible name and email are part of
 * the accessible name (WCAG 2.5.3, axe label-content-name-mismatch, #4752).
 */
function SidebarAccount() {
  const { user, name, initials } = useAccountIdentity();
  const menu = useAccountMenuState();
  return (
    <AccountMenu
      placement="sidebar"
      opened={menu.opened}
      onChange={(next) => (next ? menu.open() : menu.close())}
      trigger={
        <UnstyledButton
          className={classes.userButton}
          onClick={menu.open}
          aria-haspopup="dialog"
          aria-expanded={menu.opened}
        >
          <Avatar size={32} radius="xl" color="cyan.9" variant="filled" src={user?.avatar_url ?? undefined} aria-hidden>
            {initials}
          </Avatar>
          <span className={classes.userText}>
            <VisuallyHidden>Account menu for </VisuallyHidden>
            <span className={classes.userName}>{name}</span>
            {user?.email && <span className={classes.userEmail}>{user.email}</span>}
          </span>
          <IconChevronRight size={16} stroke={NAV_ICON_STROKE} className={classes.userChevron} aria-hidden />
        </UnstyledButton>
      }
    />
  );
}

/** Drawer foot: avatar, name, email and a Log out button. */
function DrawerAccount() {
  const { user, name, initials } = useAccountIdentity();
  return (
    <div className={classes.drawerAccount}>
      <Avatar size={36} radius="xl" color="cyan.9" variant="filled" src={user?.avatar_url ?? undefined}>
        {initials}
      </Avatar>
      <span className={classes.userText}>
        <span className={classes.userName}>{name}</span>
        {user?.email && <span className={classes.userEmail}>{user.email}</span>}
      </span>
      <LogoutForm>
        <button type="submit" className={classes.logout}>
          <IconLogout size={18} stroke={NAV_ICON_STROKE} aria-hidden />
          Log out
        </button>
      </LogoutForm>
    </div>
  );
}
