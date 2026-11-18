"use client";

/**
 * Responsive app shell (#4528), built to the approved shell spec
 * (KB design/app-shell.md, artifact #75 v1):
 *
 * - Phone (< 768 px): header with a menu button, the family chip above the
 *   page title, the bell and the avatar. The menu button opens the drawer.
 *   The bottom tab bar and its More sheet (BottomNav, #4536) sit in the
 *   footer.
 * - Tablet (768-991 px): 80 px icon rail. Its menu button opens the drawer.
 * - Desktop (>= 992 px): 260 px sidebar, always open; no drawer.
 *
 * Root cause this fixes (audit #4509): the old AppShell had a navbar with no
 * `collapsed` state and no menu button, so below 48em Mantine rendered the
 * navbar open, full screen, over every page, with nothing to close it.
 * Now the navbar is collapsed on phones and the drawer is a real dialog
 * (focus trap, Esc, scrim, focus back to the opener).
 *
 * Navigation content comes from nav-config, filtered by the user's
 * household role (useNavSections, #4543): <Sidebar variant="sidebar"> in the
 * desktop navbar, <Sidebar variant="drawer"> in the drawer, and the rail
 * below.
 *
 * The family chip (#4574) above the title is the phone/tablet family switcher:
 * it names the family the whole app is scoped to and opens the picker (a bottom
 * sheet below 48em, a popover above). With a single family it is a plain label.
 * The open state lives here so the chip, the sidebar card and the More sheet row
 * can never have two pickers open at once.
 *
 * The header bell is #4563's notifications opener: the bottom sheet on phones,
 * the popover from 768 px up (components/notifications/NotificationsSurface).
 *
 * The header avatar opens the account menu (#5198, components/account/AccountMenu):
 * a bottom sheet below 48em, a 320 px card popover above, with the identity
 * header, Profile / Theme / Notification settings and Log out. Its open state
 * lives here too, because the More sheet's account row opens the same menu.
 */

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import {
  ActionIcon,
  AppShell,
  Box,
  Drawer,
  ScrollArea,
  VisuallyHidden,
} from "@mantine/core";
import { useDisclosure, useMediaQuery, useReducedMotion } from "@mantine/hooks";
import { IconChevronDown, IconMenu2 } from "@tabler/icons-react";
import dayjs from "dayjs";
import AccountMenu, { HeaderAccountButton } from "@/components/account/AccountMenu";
import { useAccountIdentity } from "@/components/shared/use-account";
import BottomNav from "@/components/shared/BottomNav";
import OfflineBanner, { ServiceWorkerBridge } from "@/components/shared/OfflineBanner";
import NotificationsSurface from "@/components/notifications/NotificationsSurface";
import Sidebar, { Brand } from "@/components/shared/Sidebar";
import HouseholdSwitcher, {
  useHouseholdSwitcherControl,
  type HouseholdSwitcherControl,
} from "@/components/shared/HouseholdSwitcher";
import { useNavSections } from "@/components/shared/use-nav-role";
import { apiFetch } from "@/lib/api";
import { NAV_ICON_STROKE, activeSection, pageTitle, type NavSection } from "@/components/shared/nav-config";
import classes from "./DashboardShell.module.css";
import bottomNavClasses from "./BottomNav.module.css";

/** Mantine breakpoints the shell switches at (sm = 48em, md = 62em). */
const DESKTOP_QUERY = "(min-width: 62em)";

export default function DashboardShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const [drawerOpened, drawer] = useDisclosure(false);
  const isDesktop = useMediaQuery(DESKTOP_QUERY);
  const reduceMotion = useReducedMotion();
  // The bell's panel and the account menu are opened from the header and from
  // the More sheet's rows, so their state lives here.
  const [bellOpened, setBellOpened] = useState(false);
  const [accountOpened, setAccountOpened] = useState(false);
  // One family-switcher state for the whole shell (#4574): the header chip, the
  // sidebar/drawer card and the More sheet row all open this one picker.
  const switcher = useHouseholdSwitcherControl();

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
      // The phone tab bar: 56 px + 1 px border + the bottom safe-area inset.
      // Height 0 from sm up, so main isn't padded for a bar that isn't there.
      footer={{ height: { base: "calc(3.5625rem + env(safe-area-inset-bottom, 0px))", sm: 0 } }}
      classNames={{ header: classes.header, navbar: classes.navbar, main: classes.main }}
    >
      <AppShell.Header>
        <ShellHeader
          pathname={pathname}
          drawerOpened={drawerOpened}
          onOpenDrawer={drawer.open}
          bellOpened={bellOpened}
          onBellChange={setBellOpened}
          accountOpened={accountOpened}
          onAccountChange={setAccountOpened}
          switcher={switcher}
        />
      </AppShell.Header>

      <AppShell.Navbar aria-label="Main navigation">
        <Box hiddenFrom="md" h="100%">
          <Rail pathname={pathname} drawerOpened={drawerOpened} onOpenDrawer={drawer.open} />
        </Box>
        <Box visibleFrom="md" className={classes.sidebar}>
          <Sidebar variant="sidebar" familySlot={<HouseholdSwitcher variant="card" surfaceAt="wide" control={switcher} />} />
        </Box>
      </AppShell.Navbar>

      <AppShell.Main>
        <div className={classes.content}>
          {/* Offline + "reload to update" (#4538), under the header in the page
              flow, as the shell spec's Offline state draws them. */}
          <OfflineBanner />
          {children}
        </div>
        {/* Registers the service worker (#4538). Mounted HERE, not in the root
            layout, on purpose: the auth pages must not register it. Logout
            unregisters the worker and deletes every cache (#4648), and a
            registration on /login would immediately precache again — leaving
            caches behind for the next person on a shared device. The offline
            shell belongs to the signed-in app. */}
        <ServiceWorkerBridge />
      </AppShell.Main>

      <AppShell.Footer hiddenFrom="sm" withBorder={false} className={bottomNavClasses.footer}>
        <nav aria-label="Main navigation">
          <BottomNav
            onOpenNotifications={() => setBellOpened(true)}
            onOpenAccount={() => setAccountOpened(true)}
            switcher={switcher}
          />
        </nav>
      </AppShell.Footer>

      <Drawer
        opened={drawerOpened}
        onClose={drawer.close}
        position="left"
        size="min(320px, 86%)"
        hiddenFrom="md"
        title={
          <>
            <Brand as="span" />
            <VisuallyHidden> menu</VisuallyHidden>
          </>
        }
        closeButtonProps={{ "aria-label": "Close menu", size: "lg" }}
        scrollAreaComponent={ScrollArea.Autosize}
        transitionProps={{ duration: reduceMotion ? 0 : 240, timingFunction: "ease-out" }}
        overlayProps={{ backgroundOpacity: 0.45, blur: 0 }}
        classNames={{ content: classes.drawerContent, header: classes.drawerHeader, body: classes.drawerBody }}
        data-testid="nav-drawer"
      >
        <Sidebar
          variant="drawer"
          familySlot={
            // The drawer closes first: two focus traps at once fight over focus
            // return, and the drawer's is the one being dismissed.
            <HouseholdSwitcher variant="card" surfaceAt="none" control={switcher} onBeforeOpen={drawer.close} />
          }
        />
      </Drawer>
    </AppShell>
  );
}

// ─── Header ───────────────────────────────────────────────────

function ShellHeader({
  pathname,
  drawerOpened,
  onOpenDrawer,
  bellOpened,
  onBellChange,
  accountOpened,
  onAccountChange,
  switcher,
}: {
  pathname: string;
  drawerOpened: boolean;
  onOpenDrawer: () => void;
  bellOpened: boolean;
  onBellChange: (opened: boolean) => void;
  accountOpened: boolean;
  onAccountChange: (opened: boolean) => void;
  /** The shell's single family-switcher state (#4574). */
  switcher: HouseholdSwitcherControl;
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
          <HouseholdSwitcher variant="chip" surfaceAt="compact" control={switcher} />
        </Box>
        <div className={classes.titleLine}>
          <h1 className={classes.title}>{pageTitle(pathname)}</h1>
          {today && <span className={classes.date}>{today}</span>}
        </div>
      </div>

      <HeaderBell opened={bellOpened} onChange={onBellChange} />
      <Box hiddenFrom="md">
        <UserMenu opened={accountOpened} onChange={onAccountChange} />
      </Box>
    </div>
  );
}

/**
 * Header bell with the unread count, at every breakpoint. It is one of the two
 * notifications openers: on phones it opens the bottom sheet, from 768 px the
 * popover (#4563, NotificationsSurface). Both surfaces render the same panel,
 * so the bell, the More sheet's row and the panel share one ["notifications"]
 * query and never disagree about the count.
 */
function HeaderBell({ opened, onChange: setOpened }: { opened: boolean; onChange: (opened: boolean) => void }) {
  return (
    <NotificationsSurface opened={opened} onChange={setOpened} />
  );
}

/**
 * Avatar menu (phone and tablet; desktop has the user block in the sidebar).
 *
 * #5198 rebuilt it to the approved shell spec (artifact #75 v1): below 48em it is
 * a bottom sheet, from 48em the 320 px card popover, both with the identity
 * header, Profile / Theme / Notification settings rows and a full-width
 * ghost-danger Log out. The old Mantine Menu (a small anchored dropdown with a
 * static "Account" label) was the biggest build-vs-design gap in the shell
 * audit (artifact #168, rows D3/D4). AccountMenu owns the surfaces; this only
 * supplies the avatar trigger and the open state the More sheet shares.
 */
function UserMenu({ opened, onChange }: { opened: boolean; onChange: (opened: boolean) => void }) {
  const { user, name, initials } = useAccountIdentity();

  return (
    <AccountMenu
      placement="header"
      opened={opened}
      onChange={onChange}
      trigger={
        <HeaderAccountButton
          name={name}
          initials={initials}
          avatarUrl={user?.avatar_url}
          expanded={opened}
          onClick={() => onChange(!opened)}
        />
      }
    />
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
  const sections = useNavSections();
  const main = sections.filter((s) => s.group === "main");
  const household = sections.filter((s) => s.group === "household");
  const settings = sections.find((s) => s.id === "settings");

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
      {main.map((s) => (
        <RailItem key={s.id} section={s} active={current === s.id} />
      ))}
      {household.length > 0 && <span className={classes.railRule} aria-hidden />}
      {household.map((s) => (
        <RailItem key={s.id} section={s} active={current === s.id} />
      ))}
      {settings && <RailItem section={settings} active={current === "settings"} className={classes.railBottom} />}
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

  // Every section in nav-config is a real page (#5199); nothing is drawn as a
  // dimmed non-link any more.
  return (
    <Link href={section.href} className={cls} aria-current={active ? "page" : undefined}>
      {inner}
    </Link>
  );
}
