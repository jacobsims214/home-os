"use client";

/**
 * Phone bottom tab bar and More sheet (#4536), built to the approved shell
 * spec (KB design/app-shell.md, artifact #75 v1). Replaces the old,
 * never-mounted BottomNav.
 *
 * - Four tabs from nav-config (TAB_SECTIONS: Today, Calendar, Meals) plus More.
 *   The active tab comes from the route (activeSection), with
 *   aria-current="page". Every tab is a link: a section only exists in
 *   nav-config when its page exists (#5199), so there is no placeholder tab.
 * - More is a button that opens the More sheet: search, the Household sections
 *   as tiles, then Notifications, Switch family, Settings and the account row.
 *   While the sheet is open More is the active tab.
 * - Tabs and tiles are filtered by the user's household role
 *   (useNavSections, #4543): a housesitter has no Money or Documents tile (the
 *   documents hub #4569 is READERS-only, like Money), a vendor keeps Today and
 *   More only. The UI hides; the API enforces.
 * - DashboardShell mounts this in AppShell.Footer, below 48em only. The bar
 *   owns the bottom safe-area inset (env(safe-area-inset-bottom)).
 * - The "Switch family" row (#4574) is the family switcher's phone entry point:
 *   it closes this sheet and opens the picker the header chip owns, so a thumb
 *   can switch families without reaching the header. A single-family account
 *   gets the Members link instead.
 */

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { Avatar, Drawer, TextInput, UnstyledButton, VisuallyHidden } from "@mantine/core";
import { useReducedMotion } from "@mantine/hooks";
import { IconBell, IconChevronRight, IconLayoutGrid, IconSearch } from "@tabler/icons-react";
import { useNavSections } from "@/components/shared/use-nav-role";
import HouseholdSwitcher, {
  type HouseholdSwitcherControl,
} from "@/components/shared/HouseholdSwitcher";
import { useUnreadCount } from "@/components/notifications/use-notifications";
import { useAccountIdentity } from "@/components/shared/use-account";
import {
  NAV_ICON_STROKE,
  NAV_SECTIONS,
  TAB_SECTIONS,
  activeSection,
  type NavSection,
  type NavSectionId,
} from "@/components/shared/nav-config";
import classes from "./BottomNav.module.css";

/**
 * One-line hints under the More sheet's Household tiles, from artifact #75 v1
 * (#5199). They describe what is behind the tile; they never say "coming soon"
 * — a destination that isn't built isn't in nav-config, so it has no tile.
 * People & House uses the artifact's wording; Money keeps its static line
 * because the artifact's live count ("2 bills due this week") needs the
 * due-soon data the page tickets own.
 */
const TILE_HINTS: Partial<Record<NavSectionId, string>> = {
  // #4569: the artifact's own subtitle for the Documents tile (artifact #75 v1).
  documents: "Insurance, school forms, receipts",
  // #5696: "pets" left this hint because Pets became its own tile. Everything
  // else is the artifact's wording (artifact #75 v1); only the word that moved
  // was dropped, so the People tile stops promising a page it no longer owns.
  people: "Family, home, cars, vendors",
  // #5696: pets are their own tile now. The hint names the care a parent
  // actually comes for, because "Pets" alone reads like a view-only page.
  pets: "Vet, meds, feeding, appointments",
  // #5704: the House half of People & House, one tile each. Before this the
  // More sheet had four household tiles and none of them was a House page:
  // properties, assets, vehicles and vendors were reachable only by typing the
  // URL. Each hint names what a person comes to the page for (the artifact's
  // own wording where it has one, artifact #89 v1).
  properties: "Rooms, mortgages, upkeep",
  assets: "Everything in the house",
  vehicles: "Cars, service, mileage",
  vendors: "Plumber, cleaner, sitter",
  maintenance: "Upkeep and repairs",
  money: "Bills and loans",
  // #5408: the tile that was missing entirely — the More sheet was the only
  // phone surface that could have carried Guides and it had no tile to show.
  // #5503: the tile now leads to both halves of the section, so the hint names
  // both. It stays SHORT: a tile hint is a one-glance line at 390 px.
  guides: "How-tos, and things to look up",
};

interface BottomNavProps {
  /** Opens the notifications panel (the header bell's). */
  onOpenNotifications: () => void;
  /** Opens the account menu (the header avatar's). */
  onOpenAccount: () => void;
  /** The shell's family-switcher state (#4574). */
  switcher: HouseholdSwitcherControl;
}

export default function BottomNav({ onOpenNotifications, onOpenAccount, switcher }: BottomNavProps) {
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
        switcher={switcher}
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

function MoreSheet({
  opened,
  onClose,
  current,
  onOpenNotifications,
  onOpenAccount,
  switcher,
}: {
  opened: boolean;
  onClose: () => void;
  current: NavSectionId | undefined;
  onOpenNotifications: () => void;
  onOpenAccount: () => void;
  switcher: HouseholdSwitcherControl;
}) {
  const router = useRouter();
  const reduceMotion = useReducedMotion();
  const [query, setQuery] = useState("");
  // The identity the account row shows (#5198): GET /auth/me, shared with the
  // account menu and the drawer foot so all three name the same person.
  const { user, name, initials } = useAccountIdentity();

  // The notifications query is the header bell's (#4563), so the pill here and
  // the bell's badge can never disagree.
  const unread = useUnreadCount();
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

        {/* The family switcher's phone entry point (#4574): closes this sheet,
            then opens the picker the header chip owns. */}
        <HouseholdSwitcher
          variant="row"
          surfaceAt="none"
          control={switcher}
          onBeforeOpen={onClose}
        />

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

        {/* #5198: the account menu is a dialog (bottom sheet on phones, card
            popover above 48em), not a Mantine menu. */}
        <UnstyledButton className={classes.row} onClick={onOpenAccount} aria-haspopup="dialog">
          {/* Same construction as the header avatar (HeaderAccountButton) and the
              desktop SidebarAccount: the avatar is decoration, and a
              VisuallyHidden "Account menu for" names the row. Without them the
              initials leak into the accessible name — this row announced
              "ET E2E Tester Account & log out", so the three ways into the
              account menu disagreed (#5410). */}
          <Avatar
            size={32}
            radius="xl"
            color="cyan.9"
            variant="filled"
            src={user?.avatar_url ?? undefined}
            aria-hidden
          >
            {initials}
          </Avatar>
          <span className={classes.accountText}>
            <VisuallyHidden>Account menu for </VisuallyHidden>
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
  return (
    <Link href={section.href} className={classes.tile} aria-current={active ? "page" : undefined}>
      {inner}
    </Link>
  );
}
