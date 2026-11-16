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
 * `familySlot` replaces the family card; the family switcher (#4574) plugs in
 * there.
 */

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { Avatar, Box, Menu, Text, TextInput, UnstyledButton, VisuallyHidden } from "@mantine/core";
import { IconChevronRight, IconLogout, IconSearch, IconSelector, IconSettings } from "@tabler/icons-react";
import ColorSchemeControl from "@/components/ui/ColorSchemeControl";
import { apiFetch } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";
import {
  NAV_GROUP_LABELS,
  NAV_ICON_STROKE,
  activeSection,
  type NavGroup,
  type NavSection,
} from "@/components/shared/nav-config";
import { useNavSections } from "@/components/shared/use-nav-role";
import classes from "./Sidebar.module.css";

export type SidebarVariant = "sidebar" | "drawer";

interface SidebarProps {
  variant?: SidebarVariant;
  /** Replaces the family card (the family switcher, #4574). */
  familySlot?: React.ReactNode;
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

      <div className={classes.family}>{familySlot ?? <FamilyCard />}</div>

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

/** The accent logo tile and wordmark (also the drawer's title). */
export function Brand({ as: Tag = "div" }: { as?: "div" | "span" }) {
  return (
    <Tag className={classes.brand}>
      <span className={classes.logo} aria-hidden>
        H
      </span>
      <span className={classes.brandName}>Home OS</span>
    </Tag>
  );
}

// ─── Family card ──────────────────────────────────────────────

interface HouseholdResponse {
  data?: { name?: string } | null;
}

/**
 * "Sims family · 5 members". Until the family switcher (#4574) exists, it
 * goes to the members page, where the family is managed today.
 */
function FamilyCard() {
  // Same query key as the header family chip and the More sheet.
  const household = useQuery({
    queryKey: ["household", "me"],
    queryFn: () => apiFetch<HouseholdResponse>("/api/v1/households/me"),
    staleTime: 5 * 60_000,
    retry: false,
  });
  // Vendors can't list members (403); the card then shows the name only.
  const members = useQuery({
    queryKey: ["household", "me", "members"],
    queryFn: () => apiFetch<{ data: unknown[] }>("/api/v1/households/me/members"),
    staleTime: 5 * 60_000,
    retry: false,
  });
  const name = household.data?.data?.name?.trim() || "Your family";
  const count = Array.isArray(members.data?.data) ? members.data!.data.length : 0;
  const meta = count > 0 ? `${count} ${count === 1 ? "member" : "members"}` : "Manage family";

  return (
    <UnstyledButton component={Link} href="/dashboard/members" className={classes.familyCard}>
      <span className={classes.familyMark} aria-hidden>
        {name[0]?.toUpperCase() ?? "F"}
      </span>
      <span className={classes.familyText}>
        <span className={classes.familyName}>{name}</span>
        <span className={classes.familyMeta}>{meta}</span>
      </span>
      <IconSelector size={18} stroke={NAV_ICON_STROKE} className={classes.familyIcon} aria-hidden />
    </UnstyledButton>
  );
}

// ─── Search ───────────────────────────────────────────────────

function SidebarSearch({ variant }: { variant: SidebarVariant }) {
  const router = useRouter();
  const [query, setQuery] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const drawer = variant === "drawer";

  // "/" focuses the sidebar search, unless the user is already typing.
  useEffect(() => {
    if (drawer) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey) return;
      const t = e.target as HTMLElement | null;
      if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
      const input = inputRef.current;
      // Only when the sidebar is on screen (it's display:none below 62em).
      if (!input || input.offsetParent === null) return;
      e.preventDefault();
      input.focus();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [drawer]);

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const q = query.trim();
    if (!q) return;
    router.push("/dashboard/search?q=" + encodeURIComponent(q));
  };

  return (
    <form role="search" onSubmit={submit} className={classes.search}>
      <TextInput
        ref={inputRef}
        type="search"
        value={query}
        onChange={(e) => setQuery(e.currentTarget.value)}
        placeholder="Search"
        aria-label="Search"
        leftSection={<IconSearch size={drawer ? 18 : 16} stroke={NAV_ICON_STROKE} aria-hidden />}
        rightSection={
          drawer ? undefined : (
            <kbd className={classes.kbd} aria-hidden>
              /
            </kbd>
          )
        }
        rightSectionPointerEvents="none"
        classNames={{ input: classes.searchInput }}
      />
    </form>
  );
}

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

  if (section.comingSoon) {
    // The page isn't built yet; show the section without linking to a 404.
    return (
      <span className={classes.row} aria-disabled="true" title={`${section.label}: coming soon`}>
        {inner}
        <span className={classes.soon}>
          Soon<VisuallyHidden>: coming soon</VisuallyHidden>
        </span>
      </span>
    );
  }
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

function useAccount() {
  const user = useAuthStore((s) => s.user);
  const name = user?.name || "Account";
  const initials =
    (user?.name || user?.email || "U")
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((p) => p[0]!.toUpperCase())
      .join("") || "U";
  return { user, name, initials };
}

/**
 * Native POST to /api/auth/logout (#4424): the route expires the session
 * cookies and 303s to /login, and the full page load drops the React Query
 * cache. Only the persisted profile is cleared here.
 */
function LogoutForm({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <form
      method="post"
      action="/api/auth/logout"
      className={className}
      onSubmit={() => useAuthStore.getState().setUser(null)}
    >
      {children}
    </form>
  );
}

/**
 * The account menu's rows: name and email, Settings, the Theme picker and
 * Log out. Shared by the header avatar menu (phone/tablet) and the sidebar
 * user button (desktop).
 */
export function AccountMenuItems() {
  const { user, name } = useAccount();
  return (
    <>
      <Box px="sm" py="xs">
        <Text size="sm" fw={600} truncate="end">
          {name}
        </Text>
        {user?.email && (
          <Text size="xs" c="var(--ink-3)" truncate="end">
            {user.email}
          </Text>
        )}
      </Box>
      <Menu.Divider />
      <Menu.Item component={Link} href="/dashboard/settings" leftSection={<IconSettings size={18} stroke={NAV_ICON_STROKE} />}>
        Settings
      </Menu.Item>
      <Menu.Divider />
      <Box px="sm" py="xs">
        <Text size="xs" fw={500} c="var(--ink-2)" mb={6}>
          Theme
        </Text>
        <ColorSchemeControl />
      </Box>
      <Menu.Divider />
      <LogoutForm>
        <Menu.Item
          component="button"
          type="submit"
          color="red.9"
          closeMenuOnClick={false}
          leftSection={<IconLogout size={18} stroke={NAV_ICON_STROKE} />}
          style={{ color: "var(--danger)" }}
        >
          Log out
        </Menu.Item>
      </LogoutForm>
    </>
  );
}

/** Desktop foot: the user button opens the account menu upward. */
function SidebarAccount() {
  const { user, name, initials } = useAccount();
  return (
    <Menu position="top-start" width={236} shadow="lg" radius="md" withinPortal offset={6}>
      <Menu.Target>
        {/* Named by its content, not aria-label, so the visible name and email are part of
            the accessible name (WCAG 2.5.3, axe label-content-name-mismatch, #4752). */}
        <UnstyledButton className={classes.userButton} aria-haspopup="menu">
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
      </Menu.Target>
      <Menu.Dropdown>
        <AccountMenuItems />
      </Menu.Dropdown>
    </Menu>
  );
}

/** Drawer foot: avatar, name, email and a Log out button. */
function DrawerAccount() {
  const { user, name, initials } = useAccount();
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
