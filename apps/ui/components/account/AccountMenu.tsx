"use client";

/**
 * The account menu (#5198), rebuilt to the approved shell spec
 * (KB design/app-shell.md, "Family switcher, user menu, notifications") and
 * artifact #75 v1. It replaces the small anchored dropdown that item D3/D4 of
 * the build-vs-design review (artifact #168) called the shell's biggest gap.
 *
 * WHAT IT IS, PER WIDTH
 *
 * - Phone (< 768 px): a BOTTOM SHEET — full width, radius 16 at the top, a drag
 *   handle, padding-bottom 16 px + safe-area — carrying the identity header
 *   (44 px avatar, name, email, close), then Profile, Theme and Notification
 *   settings rows, then a full-width ghost-danger Log out. In the artifact the
 *   sheet sits at the bottom of the screen over the tab bar (it is modal); the
 *   More sheet is the one surface that floats above the bar.
 * - 768 px and up: the same content in a 320 px CARD POPOVER — 1 px border,
 *   radius 12, max-height 70vh, no transition — anchored under the header
 *   avatar (tablet) or opening upward from the sidebar's user button (desktop),
 *   which is where the artifact draws it at 820 and 1440.
 *
 * The rows are rows, not menu items: Profile and Notification settings are real
 * links, Theme is a segmented control, and only Log out is a button (inside the
 * native logout form). That is why the surfaces are a dialog and a popover
 * rather than a Mantine Menu — the old dropdown was a `role="menu"` and a menu
 * of links is not what this is.
 *
 * DESTINATIONS. Profile → the Account settings section (sign-in email and
 * password, the closest existing page to "your profile"). Notification settings
 * → the Settings index: Home OS has no notification preferences yet (the API
 * stores notifications, not per-person preferences), so the row goes to the
 * place such a section would live rather than to a page that does not exist.
 * Both close the menu first, so focus never lands behind the overlay.
 *
 * Log out stays the native POST form (#4424) in its own module because this
 * component and Sidebar both render it.
 */

import { forwardRef, useState } from "react";
import Link from "next/link";
import {
  ActionIcon,
  Avatar,
  Drawer,
  Popover,
  UnstyledButton,
  VisuallyHidden,
} from "@mantine/core";
import { useMediaQuery, useReducedMotion } from "@mantine/hooks";
import {
  IconBell,
  IconChevronRight,
  IconLogout,
  IconMoon,
  IconUser,
  IconX,
} from "@tabler/icons-react";
import ColorSchemeControl from "@/components/ui/ColorSchemeControl";
import LogoutForm from "@/components/shared/LogoutForm";
import { NAV_ICON_STROKE } from "@/components/shared/nav-config";
import { useAccountIdentity } from "@/components/shared/use-account";
import classes from "./AccountMenu.module.css";

/** Below Mantine `sm` (48em) the menu is a sheet — the line the shell switches at. */
const PHONE_QUERY = "(max-width: 47.99em)";

/** Where the menu is opened from, which decides where the popover sits. */
export type AccountMenuPlacement = "header" | "sidebar";

/** The Settings destinations the rows lead to. */
const PROFILE_HREF = "/dashboard/settings/account";
const NOTIFICATION_SETTINGS_HREF = "/dashboard/settings";

export interface AccountMenuProps {
  /**
   * `header` — the avatar in the header (phone and tablet); positioned under it.
   * `sidebar` — the user button in the desktop sidebar foot; opens upward.
   */
  placement: AccountMenuPlacement;
  opened: boolean;
  onChange: (opened: boolean) => void;
  /** The button that opens the menu. It carries its own shape, name and ARIA. */
  trigger: React.ReactElement;
}

export default function AccountMenu({ placement, opened, onChange, trigger }: AccountMenuProps) {
  // Resolved after mount. The menu only ever opens from a click, so the first
  // render (server, and the first client paint) can never show the wrong surface.
  const isPhone = useMediaQuery(PHONE_QUERY);
  const reduceMotion = useReducedMotion();
  const onClose = () => onChange(false);

  const identity = <Identity />;

  if (isPhone) {
    return (
      <>
        {trigger}
        <Drawer
          opened={opened}
          onClose={onClose}
          position="bottom"
          size="auto"
          // Above the tab bar (z 102, BottomNav.module.css): the artifact's
          // account sheet is modal over the whole screen, unlike the More sheet.
          zIndex={103}
          title={identity}
          closeButtonProps={{ "aria-label": "Close account menu", size: "lg" }}
          transitionProps={{ duration: reduceMotion ? 0 : 240, timingFunction: "ease-out" }}
          overlayProps={{ backgroundOpacity: 0.45, blur: 0 }}
          classNames={{
            content: classes.sheet,
            header: classes.sheetHeader,
            title: classes.sheetTitle,
            body: classes.sheetBody,
          }}
          data-testid="account-menu-sheet"
        >
          <span className={classes.handle} aria-hidden />
          <MenuBody onClose={onClose} />
        </Drawer>
      </>
    );
  }

  return (
    <Popover
      opened={opened}
      onChange={(next) => {
        if (!next) onClose();
      }}
      position={placement === "sidebar" ? "top-start" : "bottom-end"}
      offset={8}
      width={320}
      withinPortal
      trapFocus
      returnFocus
      closeOnEscape
      closeOnClickOutside
      transitionProps={{ duration: 0, timingFunction: "ease-out" }}
      classNames={{ dropdown: classes.popover }}
    >
      <Popover.Target>{trigger}</Popover.Target>
      <Popover.Dropdown role="dialog" aria-label="Account menu" data-testid="account-menu-popover">
        <div className={classes.panel}>
          <Identity onClose={onClose} closeable />
          <MenuBody onClose={onClose} />
        </div>
      </Popover.Dropdown>
    </Popover>
  );
}

// ─── The identity header ──────────────────────────────────────

/**
 * Avatar, name and email, plus a close button when this is the popover.
 *
 * In the phone sheet this is the Drawer's own title, so the Drawer's header
 * supplies the close button and `closeable` stays off — two X buttons in one
 * header would be one too many.
 *
 * The visually hidden prefix gives the sheet an accessible name that says what
 * it is ("Account menu for Jacob Sims …") rather than just reading the name out.
 */
function Identity({ onClose, closeable }: { onClose?: () => void; closeable?: boolean }) {
  const { user, name, initials } = useAccountIdentity();
  return (
    <div className={classes.identity}>
      <Avatar
        size={44}
        radius="xl"
        color="cyan.9"
        variant="filled"
        src={user?.avatar_url ?? undefined}
        aria-hidden
      >
        {initials}
      </Avatar>
      <div className={classes.identityText}>
        <VisuallyHidden>Account menu for </VisuallyHidden>
        <span className={classes.identityName}>{name}</span>
        {user?.email && <span className={classes.identityEmail}>{user.email}</span>}
      </div>
      {closeable && (
        <ActionIcon
          variant="subtle"
          color="gray"
          size={40}
          onClick={onClose}
          aria-label="Close account menu"
          className={classes.close}
        >
          <IconX size={20} stroke={NAV_ICON_STROKE} aria-hidden />
        </ActionIcon>
      )}
    </div>
  );
}

// ─── Rows + logout (shared by the sheet and the popover) ──────

function MenuBody({ onClose }: { onClose: () => void }) {
  return (
    <div className={classes.body} data-account-menu-body>
      <ul className={classes.rows}>
        <li>
          <Link href={PROFILE_HREF} className={classes.row} onClick={onClose}>
            <IconUser size={22} stroke={NAV_ICON_STROKE} className={classes.rowIcon} aria-hidden />
            <span className={classes.rowLabel}>Profile</span>
            <IconChevronRight size={18} stroke={NAV_ICON_STROKE} className={classes.chevron} aria-hidden />
          </Link>
        </li>
        <li>
          <div className={classes.row} data-static>
            <IconMoon size={22} stroke={NAV_ICON_STROKE} className={classes.rowIcon} aria-hidden />
            <span className={classes.rowLabel}>Theme</span>
            {/* The Light / Dark / System picker (#4529), which keeps its own
                aria-label ("Theme") and persists through the colour-scheme
                manager in providers.tsx. */}
            <ColorSchemeControl fullWidth={false} />
          </div>
        </li>
        <li>
          <Link href={NOTIFICATION_SETTINGS_HREF} className={classes.row} onClick={onClose}>
            <IconBell size={22} stroke={NAV_ICON_STROKE} className={classes.rowIcon} aria-hidden />
            <span className={classes.rowLabel}>Notification settings</span>
            <IconChevronRight size={18} stroke={NAV_ICON_STROKE} className={classes.chevron} aria-hidden />
          </Link>
        </li>
      </ul>

      <LogoutForm className={classes.logoutForm}>
        <button type="submit" className={classes.logout}>
          <IconLogout size={18} stroke={NAV_ICON_STROKE} aria-hidden />
          Log out
        </button>
      </LogoutForm>
    </div>
  );
}

// ─── Triggers ────────────────────────────────────────────────

/**
 * The header avatar (phone and tablet). A forwardRef that spreads Mantine's
 * cloned props: as a Popover.Target, Mantine clones aria-haspopup /
 * aria-expanded / aria-controls and its ref onto this component, so the popover
 * anchors to the button itself and the button owns the popup ARIA (#4752).
 *
 * Its accessible name is a VisuallyHidden "Account menu for <name>", not an
 * aria-label: the avatar shows the initials, and a label that does not contain
 * the visible text fails axe's label-content-name-mismatch (WCAG 2.5.3). The
 * avatar is aria-hidden, exactly as the desktop user button already does it.
 */
export const HeaderAccountButton = forwardRef<
  HTMLButtonElement,
  { name: string; initials: string; avatarUrl?: string | null; expanded?: boolean; onClick: () => void } & Omit<
    React.ButtonHTMLAttributes<HTMLButtonElement>,
    "children" | "onClick"
  >
>(function HeaderAccountButton({ name, initials, avatarUrl, expanded, className, ...rest }, ref) {
  return (
    <UnstyledButton
      {...rest}
      ref={ref}
      className={[className, classes.headerButton].filter(Boolean).join(" ")}
      aria-haspopup="dialog"
      aria-expanded={expanded}
    >
      <VisuallyHidden>Account menu for {name}</VisuallyHidden>
      <Avatar size={32} radius="xl" color="cyan.9" variant="filled" src={avatarUrl ?? undefined} aria-hidden>
        {initials}
      </Avatar>
    </UnstyledButton>
  );
});

/** Convenience for hosts that own the open state (the desktop sidebar). */
export function useAccountMenuState(): { opened: boolean; open: () => void; close: () => void } {
  const [opened, setOpened] = useState(false);
  return {
    opened,
    open: () => setOpened(true),
    close: () => setOpened(false),
  };
}
