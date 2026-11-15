"use client";

/**
 * Responsive app shell (#4528), built to the approved shell spec
 * (KB design/app-shell.md, artifact #75 v1):
 *
 * - Phone (< 768 px): header with a menu button, the family chip above the
 *   page title, the bell and the avatar. The menu button opens the drawer.
 *   The bottom tab bar is #4536 (see TAB BAR below).
 * - Tablet (768-991 px): 80 px icon rail. Its menu button opens the drawer.
 * - Desktop (>= 992 px): 260 px sidebar, always open; no drawer.
 *
 * Root cause this fixes (audit #4509): the old AppShell had a navbar with no
 * `collapsed` state and no menu button, so below 48em Mantine rendered the
 * navbar open, full screen, over every page, with nothing to close it.
 * Now the navbar is collapsed on phones and the drawer is a real dialog
 * (focus trap, Esc, scrim, focus back to the opener).
 *
 * Hooks for the follow-up tickets:
 * - #4543 renders <Sidebar /> from nav-config; it is mounted here in both
 *   the desktop navbar and the drawer.
 * - #4536 mounts the tab bar where "TAB BAR" is marked.
 * - #4574 replaces the family chip's target with the family switcher.
 * - #4563 replaces the header bell's popover with the notifications sheet.
 */

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import {
  ActionIcon,
  AppShell,
  Avatar,
  Box,
  Drawer,
  Indicator,
  Menu,
  Popover,
  ScrollArea,
  Stack,
  Text,
  UnstyledButton,
  VisuallyHidden,
} from "@mantine/core";
import { useDisclosure, useMediaQuery, useReducedMotion } from "@mantine/hooks";
import { IconBell, IconChevronDown, IconLogout, IconMenu2, IconSettings } from "@tabler/icons-react";
import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import Sidebar from "@/components/shared/Sidebar";
import { apiFetch } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";
import {
  NAV_ICON_STROKE,
  NAV_SECTIONS,
  activeSection,
  pageTitle,
  sectionsInGroup,
  type NavSection,
} from "@/components/shared/nav-config";
import classes from "./DashboardShell.module.css";

dayjs.extend(relativeTime);

/** Mantine breakpoints the shell switches at (sm = 48em, md = 62em). */
const DESKTOP_QUERY = "(min-width: 62em)";

export default function DashboardShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const [drawerOpened, drawer] = useDisclosure(false);
  const isDesktop = useMediaQuery(DESKTOP_QUERY);
  const reduceMotion = useReducedMotion();

  // Navigating, or widening the window to desktop, closes the drawer.
  useEffect(() => {
    drawer.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pathname]);
  useEffect(() => {
    if (isDesktop) drawer.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isDesktop]);

  return (
    <AppShell
      layout="alt"
      className={classes.root}
      header={{ height: { base: "calc(3.5rem + env(safe-area-inset-top, 0px))", sm: 64 } }}
      navbar={{ width: { sm: 80, md: 260 }, breakpoint: "sm", collapsed: { mobile: true } }}
      // TAB BAR (#4536): add `footer={{ height: 56 }}` here and render
      // <AppShell.Footer hiddenFrom="sm"> below, with its own
      // padding-bottom: env(safe-area-inset-bottom).
      classNames={{ header: classes.header, navbar: classes.navbar, main: classes.main }}
    >
      <AppShell.Header>
        <ShellHeader
          pathname={pathname}
          drawerOpened={drawerOpened}
          onOpenDrawer={drawer.open}
        />
      </AppShell.Header>

      <AppShell.Navbar aria-label="Main navigation">
        <Box hiddenFrom="md" h="100%">
          <Rail pathname={pathname} drawerOpened={drawerOpened} onOpenDrawer={drawer.open} />
        </Box>
        <Box visibleFrom="md" className={classes.sidebar}>
          <Sidebar />
        </Box>
      </AppShell.Navbar>

      <AppShell.Main>
        <div className={classes.content}>{children}</div>
      </AppShell.Main>

      <Drawer
        opened={drawerOpened}
        onClose={drawer.close}
        position="left"
        size="min(320px, 86%)"
        hiddenFrom="md"
        title={<VisuallyHidden>Menu</VisuallyHidden>}
        closeButtonProps={{ "aria-label": "Close menu", size: "lg" }}
        scrollAreaComponent={ScrollArea.Autosize}
        transitionProps={{ duration: reduceMotion ? 0 : 240, timingFunction: "ease-out" }}
        overlayProps={{ backgroundOpacity: 0.45, blur: 0 }}
        classNames={{ content: classes.drawerContent, header: classes.drawerHeader, body: classes.drawerBody }}
        data-testid="nav-drawer"
      >
        {/* #4543 replaces Sidebar's content with the nav-config driven drawer. */}
        <Sidebar />
      </Drawer>
    </AppShell>
  );
}

// ─── Header ───────────────────────────────────────────────────

function ShellHeader({
  pathname,
  drawerOpened,
  onOpenDrawer,
}: {
  pathname: string;
  drawerOpened: boolean;
  onOpenDrawer: () => void;
}) {
  // Rendered after mount so server and client agree (the server's timezone
  // may differ from the browser's).
  const [today, setToday] = useState<string | null>(null);
  useEffect(() => setToday(dayjs().format("dddd, MMMM D")), []);

  return (
    <div className={classes.headerRow}>
      <ActionIcon
        hiddenFrom="sm"
        variant="subtle"
        color="gray"
        className={classes.iconButton}
        onClick={onOpenDrawer}
        aria-label="Open menu"
        aria-haspopup="dialog"
        aria-expanded={drawerOpened}
      >
        <IconMenu2 size={24} stroke={NAV_ICON_STROKE} />
      </ActionIcon>

      <div className={classes.titleBlock}>
        <Box hiddenFrom="md">
          <FamilyChip />
        </Box>
        <div className={classes.titleLine}>
          <h1 className={classes.title}>{pageTitle(pathname)}</h1>
          {today && <span className={classes.date}>{today}</span>}
        </div>
      </div>

      <HeaderBell />
      <Box hiddenFrom="md">
        <UserMenu />
      </Box>
    </div>
  );
}

interface HouseholdResponse {
  data?: { name?: string } | null;
}

/** "Sims family ⌄" above the title on phone and tablet. */
function FamilyChip() {
  const household = useQuery({
    queryKey: ["household", "me"],
    queryFn: () => apiFetch<HouseholdResponse>("/api/v1/households/me"),
    staleTime: 5 * 60_000,
    retry: false,
  });
  const name = household.data?.data?.name?.trim() || "Your family";

  // Until the family switcher (#4574) exists, the chip goes to the members
  // page, where the family is managed today.
  return (
    <UnstyledButton
      component={Link}
      href="/dashboard/members"
      className={classes.familyChip}
      aria-label={`Family: ${name}. Manage family`}
    >
      <span>{name}</span>
      <IconChevronDown size={14} stroke={NAV_ICON_STROKE} aria-hidden />
    </UnstyledButton>
  );
}

interface ShellNotification {
  id: string;
  title: string;
  body: string;
  is_read: boolean;
  created_at: string;
}

/**
 * Header bell with the unread count, at every breakpoint. Shares the
 * ["notifications"] query (and so its cache) with NotificationBell.
 * #4563 turns the popover into a bottom sheet on phones.
 */
function HeaderBell() {
  const [opened, setOpened] = useState(false);
  const notificationsQ = useQuery({
    queryKey: ["notifications"],
    queryFn: () => apiFetch<{ data: ShellNotification[] }>("/api/v1/notifications"),
  });
  const items = Array.isArray(notificationsQ.data?.data) ? notificationsQ.data!.data : [];
  const unread = items.filter((n) => !n.is_read).length;
  const label = unread > 0 ? `Notifications, ${unread} unread` : "Notifications";

  return (
    <Popover
      opened={opened}
      onChange={setOpened}
      position="bottom-end"
      width={380}
      shadow="lg"
      radius="md"
      trapFocus
      returnFocus
    >
      <Popover.Target>
        <Indicator
          disabled={unread === 0}
          label={unread > 99 ? "99+" : unread}
          size={18}
          offset={11}
          color="red.8"
          withBorder
          styles={{ indicator: { fontSize: 11, fontWeight: 600 } }}
        >
          <ActionIcon
            variant="subtle"
            color="gray"
            className={classes.iconButton}
            onClick={() => setOpened((o) => !o)}
            aria-label={label}
            aria-haspopup="dialog"
            aria-expanded={opened}
          >
            <IconBell size={24} stroke={NAV_ICON_STROKE} />
          </ActionIcon>
        </Indicator>
      </Popover.Target>
      <Popover.Dropdown
        role="dialog"
        aria-label="Notifications"
        style={{ maxWidth: "calc(100vw - 16px)", maxHeight: "70vh", overflowY: "auto" }}
      >
        <Text fw={600} size="md" mb="xs">
          Notifications
        </Text>
        {items.length === 0 ? (
          <Text size="sm" py="md" ta="center" c="var(--shell-ink-3)">
            You&apos;re all caught up.
          </Text>
        ) : (
          <Stack gap="sm">
            {items.map((n) => (
              <div key={n.id}>
                <Text size="sm" fw={n.is_read ? 500 : 600} lineClamp={2}>
                  {!n.is_read && <VisuallyHidden>Unread: </VisuallyHidden>}
                  {n.title}
                </Text>
                <Text size="xs" c="var(--shell-ink-3)">
                  {dayjs(n.created_at).fromNow()}
                </Text>
              </div>
            ))}
          </Stack>
        )}
      </Popover.Dropdown>
    </Popover>
  );
}

/** Avatar menu (phone and tablet; desktop has the user block in the sidebar). */
function UserMenu() {
  const user = useAuthStore((s) => s.user);
  const name = user?.name || "Account";
  const initials =
    (user?.name || user?.email || "U")
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((p) => p[0]!.toUpperCase())
      .join("") || "U";

  return (
    <Menu position="bottom-end" width={280} shadow="lg" radius="md" withinPortal>
      <Menu.Target>
        <UnstyledButton
          className={classes.avatarButton}
          aria-label={`Account menu for ${name}`}
          aria-haspopup="menu"
        >
          <Avatar size={32} radius="xl" color="cyan.9" variant="filled" src={user?.avatar_url ?? undefined}>
            {initials}
          </Avatar>
        </UnstyledButton>
      </Menu.Target>
      <Menu.Dropdown>
        <Box px="sm" py="xs">
          <Text size="sm" fw={600} truncate="end">
            {name}
          </Text>
          {user?.email && (
            <Text size="xs" c="var(--shell-ink-3)" truncate="end">
              {user.email}
            </Text>
          )}
        </Box>
        <Menu.Divider />
        <Menu.Item component={Link} href="/dashboard/settings" leftSection={<IconSettings size={18} stroke={NAV_ICON_STROKE} />}>
          Settings
        </Menu.Item>
        <Menu.Divider />
        {/* Native POST to /api/auth/logout (#4424): the route expires the
            session cookies and 303s to /login; the full page load drops the
            React Query cache. Only the persisted profile is cleared here. */}
        <form method="post" action="/api/auth/logout" onSubmit={() => useAuthStore.getState().setUser(null)}>
          <Menu.Item
            component="button"
            type="submit"
            color="red.9"
            closeMenuOnClick={false}
            leftSection={<IconLogout size={18} stroke={NAV_ICON_STROKE} />}
          >
            Log out
          </Menu.Item>
        </form>
      </Menu.Dropdown>
    </Menu>
  );
}

// ─── Tablet rail ──────────────────────────────────────────────

function Rail({
  pathname,
  drawerOpened,
  onOpenDrawer,
}: {
  pathname: string;
  drawerOpened: boolean;
  onOpenDrawer: () => void;
}) {
  const current = activeSection(pathname)?.id;
  const settings = NAV_SECTIONS.find((s) => s.id === "settings")!;

  return (
    // AppShell.Navbar is the <nav aria-label="Main navigation"> around this.
    <div className={classes.rail}>
      <ActionIcon
        variant="subtle"
        color="gray"
        className={`${classes.iconButton} ${classes.railMenu}`}
        onClick={onOpenDrawer}
        aria-label="Open menu"
        aria-haspopup="dialog"
        aria-expanded={drawerOpened}
      >
        <IconMenu2 size={24} stroke={NAV_ICON_STROKE} />
      </ActionIcon>
      {sectionsInGroup("main").map((s) => (
        <RailItem key={s.id} section={s} active={current === s.id} />
      ))}
      <span className={classes.railRule} aria-hidden />
      {sectionsInGroup("household").map((s) => (
        <RailItem key={s.id} section={s} active={current === s.id} />
      ))}
      <RailItem section={settings} active={current === "settings"} className={classes.railBottom} />
    </div>
  );
}

function RailItem({
  section,
  active,
  className,
}: {
  section: NavSection;
  active: boolean;
  className?: string;
}) {
  const Icon = section.icon;
  const inner = (
    <>
      <span className={classes.railPill}>
        <Icon size={22} stroke={NAV_ICON_STROKE} aria-hidden />
      </span>
      {section.label}
    </>
  );
  const cls = className ? `${classes.railItem} ${className}` : classes.railItem;

  if (section.comingSoon) {
    // The page isn't built yet; show the section without linking to a 404.
    return (
      <span className={cls} aria-disabled="true" title={`${section.label}: coming soon`}>
        {inner}
        <VisuallyHidden>(coming soon)</VisuallyHidden>
      </span>
    );
  }
  return (
    <Link href={section.href} className={cls} aria-current={active ? "page" : undefined}>
      {inner}
    </Link>
  );
}
