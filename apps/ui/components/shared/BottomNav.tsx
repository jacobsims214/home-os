"use client";

/**
 * Phone bottom tab bar and More sheet (#4536), built to the approved shell
 * spec (KB design/app-shell.md, artifact #75 v1). Replaces the old,
 * never-mounted BottomNav.
 *
 * - Five equal tabs: Today, Calendar, Lists, Meals (TAB_SECTIONS from
 *   nav-config) and More. The active tab comes from the route
 *   (activeSection), with aria-current="page".
 * - Sections without a page yet (`comingSoon`) stay in place but don't link
 *   to a 404: they're dimmed, and tapping one says it's coming soon.
 * - More is a button that opens the More sheet: search, the four Household
 *   sections as tiles, then Notifications, Switch family, Settings and the
 *   account row. While the sheet is open More is the active tab.
 * - Tabs and tiles are filtered by the user's household role
 *   (useNavSections, #4543): a housesitter has no Money or Documents tile,
 *   a vendor keeps Today and More only. The UI hides; the API enforces.
 * - DashboardShell mounts this in AppShell.Footer, below 48em only. The bar
 *   owns the bottom safe-area inset (env(safe-area-inset-bottom)).
 */

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { Avatar, Drawer, TextInput, UnstyledButton, VisuallyHidden } from "@mantine/core";
import { useReducedMotion } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import {
  IconBell,
  IconChevronRight,
  IconLayoutGrid,
  IconSearch,
  IconUsers,
} from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { useNavSections } from "@/components/shared/use-nav-role";
import { useUnreadCount } from "@/components/notifications/use-notifications";
import { useAuthStore } from "@/stores/auth";
import {
  NAV_ICON_STROKE,
  NAV_SECTIONS,
  TAB_SECTIONS,
  activeSection,
  type NavSection,
  type NavSectionId,
} from "@/components/shared/nav-config";
import classes from "./BottomNav.module.css";

/** One-line hints under the More sheet's Household tiles. */
const TILE_HINTS: Partial<Record<NavSectionId, string>> = {
  documents: "Coming soon",
  people: "Properties, members, vehicles",
  money: "Bills and loans",
  guides: "Coming soon",
};

interface BottomNavProps {
  /** Opens the notifications panel (the header bell's). */
  onOpenNotifications: () => void;
  /** Opens the account menu (the header avatar's). */
  onOpenAccount: () => void;
}

export default function BottomNav({ onOpenNotifications, onOpenAccount }: BottomNavProps) {
  const pathname = usePathname();
  const [moreOpened, setMoreOpened] = useState(false);
  const current = activeSection(pathname)?.id;
  const tabs = useNavSections(TAB_SECTIONS);

  // Navigating closes the sheet.
  useEffect(() => setMoreOpened(false), [pathname]);

  // The sheet closes first so its focus return doesn't fight the panel that
  // opens next; then the other panel opens on the next frame.
  const closeThen = (open: () => void) => () => {
    setMoreOpened(false);
    window.setTimeout(open, 0);
  };

  return (
    <>
      <div
        className={classes.bar}
        data-testid="tab-bar"
        style={{ gridTemplateColumns: `repeat(${tabs.length + 1}, minmax(0, 1fr))` }}
      >
        {tabs.map((s) => (
          <Tab key={s.id} section={s} active={!moreOpened && current === s.id} />
        ))}
        <button
          type="button"
          className={classes.tab}
          aria-haspopup="dialog"
          aria-expanded={moreOpened}
          data-active={moreOpened || undefined}
          onClick={() => setMoreOpened((o) => !o)}
        >
          <span className={classes.pill}>
            <IconLayoutGrid size={24} stroke={NAV_ICON_STROKE} aria-hidden />
          </span>
          More
        </button>
      </div>

      <MoreSheet
        opened={moreOpened}
        onClose={() => setMoreOpened(false)}
        current={current}
        onOpenNotifications={closeThen(onOpenNotifications)}
        onOpenAccount={closeThen(onOpenAccount)}
      />
    </>
  );
}

function Tab({ section, active }: { section: NavSection; active: boolean }) {
  const Icon = section.icon;
  const inner = (
    <>
      <span className={classes.pill}>
        <Icon size={24} stroke={NAV_ICON_STROKE} aria-hidden />
      </span>
      {section.label}
    </>
  );

  if (section.comingSoon) {
    return (
      <button
        type="button"
        className={classes.tab}
        aria-disabled="true"
        onClick={() =>
          notifications.show({
            id: `coming-soon-${section.id}`,
            message: `${section.label} is coming soon.`,
            autoClose: 2500,
          })
        }
      >
        {inner}
        <VisuallyHidden>(coming soon)</VisuallyHidden>
      </button>
    );
  }
  return (
    <Link
      href={section.href}
      className={classes.tab}
      aria-current={active ? "page" : undefined}
      data-active={active || undefined}
    >
      {inner}
    </Link>
  );
}

// ─── More sheet ───────────────────────────────────────────────

interface HouseholdResponse {
  data?: { name?: string } | null;
}

function MoreSheet({
  opened,
  onClose,
  current,
  onOpenNotifications,
  onOpenAccount,
}: {
  opened: boolean;
  onClose: () => void;
  current: NavSectionId | undefined;
  onOpenNotifications: () => void;
  onOpenAccount: () => void;
}) {
  const router = useRouter();
  const reduceMotion = useReducedMotion();
  const [query, setQuery] = useState("");
  const user = useAuthStore((s) => s.user);

  // The notifications query is the header bell's (#4563), so the pill here and
  // the bell's badge can never disagree.
  const unread = useUnreadCount();
  const household = useQuery({
    queryKey: ["household", "me"],
    queryFn: () => apiFetch<HouseholdResponse>("/api/v1/households/me"),
    staleTime: 5 * 60_000,
    retry: false,
  });
  const familyName = household.data?.data?.name?.trim() || "Your family";
  const name = user?.name || "Account";
  const initials =
    (user?.name || user?.email || "U")
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((p) => p[0]!.toUpperCase())
      .join("") || "U";
  const sections = useNavSections();
  const tiles = sections.filter((s) => s.group === "household");
  const settings = sections.find((s) => s.id === "settings") ?? NAV_SECTIONS.find((s) => s.id === "settings")!;
  const SettingsIcon = settings.icon;

  const search = (e: React.FormEvent) => {
    e.preventDefault();
    const q = query.trim();
    if (!q) return;
    onClose();
    router.push("/dashboard/search?q=" + encodeURIComponent(q));
  };

  return (
    <Drawer
      opened={opened}
      onClose={onClose}
      position="bottom"
      size="auto"
      title="More"
      // Below the tab bar (z 102 in BottomNav.module.css), above the header.
      zIndex={101}
      closeButtonProps={{ "aria-label": "Close", size: "lg" }}
      transitionProps={{ duration: reduceMotion ? 0 : 240, timingFunction: "ease-out" }}
      overlayProps={{ backgroundOpacity: 0.45, blur: 0 }}
      classNames={{
        inner: classes.sheetInner,
        content: classes.sheet,
        header: classes.sheetHeader,
        title: classes.sheetTitle,
        body: classes.sheetBody,
      }}
      data-testid="more-sheet"
    >
      <span className={classes.handle} aria-hidden />

      <form role="search" onSubmit={search} className={classes.search}>
        <TextInput
          type="search"
          value={query}
          onChange={(e) => setQuery(e.currentTarget.value)}
          placeholder="Search events, lists, documents…"
          aria-label="Search"
          leftSection={<IconSearch size={18} stroke={NAV_ICON_STROKE} aria-hidden />}
          classNames={{ input: classes.searchInput }}
        />
      </form>

      {tiles.length > 0 && (
        <div className={classes.tiles}>
          {tiles.map((s) => (
            <Tile key={s.id} section={s} active={current === s.id} />
          ))}
        </div>
      )}

      <div className={classes.rows}>
        <UnstyledButton className={classes.row} onClick={onOpenNotifications} aria-haspopup="dialog">
          <IconBell size={22} stroke={NAV_ICON_STROKE} className={classes.rowIcon} aria-hidden />
          <span className={classes.rowLabel}>Notifications</span>
          {unread > 0 && (
            <span className={classes.count}>
              {unread > 99 ? "99+" : unread}
              <VisuallyHidden> unread</VisuallyHidden>
            </span>
          )}
          <IconChevronRight size={18} stroke={NAV_ICON_STROKE} className={classes.chevron} aria-hidden />
        </UnstyledButton>

        {/* Until the family switcher (#4574) exists, this goes to the
            members page, like the header family chip. */}
        <Link href="/dashboard/members" className={classes.row} onClick={onClose}>
          <IconUsers size={22} stroke={NAV_ICON_STROKE} className={classes.rowIcon} aria-hidden />
          <span className={classes.rowLabel}>Switch family</span>
          <span className={classes.rowMeta}>{familyName}</span>
          <IconChevronRight size={18} stroke={NAV_ICON_STROKE} className={classes.chevron} aria-hidden />
        </Link>

        <Link
          href={settings.href}
          className={classes.row}
          aria-current={current === "settings" ? "page" : undefined}
          onClick={onClose}
        >
          <SettingsIcon size={22} stroke={NAV_ICON_STROKE} className={classes.rowIcon} aria-hidden />
          <span className={classes.rowLabel}>{settings.label}</span>
          <IconChevronRight size={18} stroke={NAV_ICON_STROKE} className={classes.chevron} aria-hidden />
        </Link>

        <UnstyledButton className={classes.row} onClick={onOpenAccount} aria-haspopup="menu">
          <Avatar size={32} radius="xl" color="cyan.9" variant="filled" src={user?.avatar_url ?? undefined}>
            {initials}
          </Avatar>
          <span className={classes.accountText}>
            <span className={classes.rowLabel}>{name}</span>
            <span className={classes.rowSub}>Account &amp; log out</span>
          </span>
          <IconChevronRight size={18} stroke={NAV_ICON_STROKE} className={classes.chevron} aria-hidden />
        </UnstyledButton>
      </div>
    </Drawer>
  );
}

function Tile({ section, active }: { section: NavSection; active: boolean }) {
  const Icon = section.icon;
  const inner = (
    <>
      <Icon size={24} stroke={NAV_ICON_STROKE} className={classes.tileIcon} aria-hidden />
      <span className={classes.tileTitle}>{section.label}</span>
      <span className={classes.tileHint}>{TILE_HINTS[section.id]}</span>
    </>
  );
  if (section.comingSoon) {
    return (
      <div className={classes.tile} aria-disabled="true">
        {inner}
      </div>
    );
  }
  return (
    <Link href={section.href} className={classes.tile} aria-current={active ? "page" : undefined}>
      {inner}
    </Link>
  );
}
