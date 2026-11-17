"use client";

/**
 * Notifications on phones (#4563): the header bell and the panel it opens.
 *
 * - < 768 px: a bottom sheet (Drawer position="bottom") floating above the tab
 *   bar — full width, radius 16 at the top, a drag handle, safe-area padding —
 *   the same geometry as the More sheet (#4536), which KB design/app-shell.md
 *   requires for notifications on phones. Focus moves into it, Esc and a scrim
 *   tap close it, focus returns to the bell, and the tab bar stays above it so
 *   a thumb can leave for another section.
 * - >= 768 px: the 380 px popover the same spec asks for.
 *
 * The bell is a real dialog opener at every width: aria-haspopup="dialog",
 * aria-expanded, and aria-controls pointing at the panel (Mantine's Popover
 * owns those three when it is the surface, and they point at its dropdown).
 * #4752's rule still holds — nothing but the <button> carries them, so axe's
 * aria-allowed-attr stays clean.
 *
 * Freshness: real-time is out of scope, so opening the panel refetches it, and
 * the query also refetches on window focus (use-notifications.ts).
 */

import { forwardRef, useEffect, useRef } from "react";
import { ActionIcon, Drawer, Indicator, Popover, VisuallyHidden } from "@mantine/core";
import { useMediaQuery, useReducedMotion } from "@mantine/hooks";
import { IconBell } from "@tabler/icons-react";
import { useQueryClient } from "@tanstack/react-query";
import NotificationsPanel from "@/components/notifications/NotificationsPanel";
import {
  NOTIFICATIONS_QUERY_KEY,
  useUnreadCount,
} from "@/components/notifications/use-notifications";
import { NAV_ICON_STROKE } from "@/components/shared/nav-config";
import shell from "@/components/shared/DashboardShell.module.css";
import classes from "./Notifications.module.css";

/** Below Mantine `sm` (48em) — the line the shell itself switches at. */
const DESKTOP_QUERY = "(min-width: 48em)";

/** The id of the sheet's panel, which the bell's aria-controls names. */
export const NOTIFICATIONS_PANEL_ID = "notifications-panel";

export interface NotificationsSurfaceProps {
  opened: boolean;
  /** The shell owns the state (its More sheet opens the same panel). */
  onChange: (opened: boolean) => void;
  /** Test/viewport override for the relative times. */
  now?: number;
}

export default function NotificationsSurface({
  opened,
  onChange,
  now,
}: NotificationsSurfaceProps) {
  const onClose = () => onChange(false);
  // Resolved after mount: the panel only ever opens from a click, so there is
  // no flash of the wrong surface.
  const isWide = useMediaQuery(DESKTOP_QUERY);
  const reduceMotion = useReducedMotion();
  const queryClient = useQueryClient();

  // Opening the panel refreshes the list. Together with refetch-on-focus this is
  // the whole freshness story (#4563: no real-time).
  useEffect(() => {
    if (opened) void queryClient.invalidateQueries({ queryKey: NOTIFICATIONS_QUERY_KEY });
  }, [opened, queryClient]);

  // Mantine's Drawer marks its content section role="dialog" but gives it no
  // id, and aria-controls has to name the element it controls, so the open
  // sheet's dialog gets the id the bell's aria-controls references. The dialog
  // mounts a frame or two after `opened` flips (Mantine's transition), hence the
  // bounded retry; it stops as soon as the id is set.
  const sheetRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!opened) return;
    let frame = 0;
    let tries = 0;
    const label = () => {
      const dialog = sheetRef.current?.querySelector<HTMLElement>('[role="dialog"]');
      if (dialog) {
        dialog.id = NOTIFICATIONS_PANEL_ID;
        return;
      }
      if (++tries < 60) frame = window.requestAnimationFrame(label);
    };
    label();
    return () => window.cancelAnimationFrame(frame);
  }, [opened]);

  const bell = <NotificationsBell opened={opened} onToggle={() => onChange(!opened)} />;

  if (isWide) {
    return (
      <Popover
        opened={opened}
        onChange={(next) => {
          if (!next) onClose();
        }}
        position="bottom-end"
        offset={8}
        width={380}
        withinPortal
        trapFocus
        returnFocus
        closeOnEscape
        closeOnClickOutside
        transitionProps={{ duration: reduceMotion ? 0 : 150, timingFunction: "ease-out" }}
        classNames={{ dropdown: classes.popover }}
      >
        <Popover.Target>{bell}</Popover.Target>
        <Popover.Dropdown role="dialog" aria-label="Notifications" data-testid="notifications-popover">
          <div className={classes.popoverBody}>
            {/* The popover has no Drawer header, so the panel renders its h2. */}
            <NotificationsPanel now={now} onNavigate={onClose} heading />
          </div>
        </Popover.Dropdown>
      </Popover>
    );
  }

  return (
    <>
      {bell}
      <Drawer
        ref={sheetRef}
        opened={opened}
        onClose={onClose}
        position="bottom"
        size="auto"
        // Above the header (100) and below the tab bar (102 in
        // BottomNav.module.css), like the More sheet.
        zIndex={101}
        title="Notifications"
        closeButtonProps={{ "aria-label": "Close notifications", size: "lg" }}
        transitionProps={{ duration: reduceMotion ? 0 : 240, timingFunction: "ease-out" }}
        overlayProps={{ backgroundOpacity: 0.45, blur: 0 }}
        classNames={{
          inner: classes.sheetInner,
          content: classes.sheet,
          header: classes.sheetHeader,
          title: classes.sheetTitle,
          body: classes.sheetBody,
        }}
        data-testid="notifications-sheet"
      >
        <span className={classes.handle} aria-hidden />
        {/* The Drawer header is the dialog's h2, so the panel doesn't repeat it. */}
        <div className={classes.sheetPanel}>
          <NotificationsPanel now={now} onNavigate={onClose} heading={false} />
        </div>
      </Drawer>
    </>
  );
}

/**
 * Mantine's cloned Popover.Target props (className, id, aria-controls, ...)
 * are spread onto this component, so every one of them has to pass through.
 */
export interface NotificationsBellProps
  extends Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, "onClick"> {
  opened: boolean;
  onToggle: () => void;
}

/**
 * The header bell and its unread badge.
 *
 * The <button> owns the popup ARIA (#4752: only a button may carry
 * aria-haspopup/aria-expanded, and axe's aria-allowed-attr stays clean), and
 * the Indicator stays outside it.
 *
 * The default aria-controls names the phone sheet's panel. As a
 * Popover.Target, Mantine clones its own aria-haspopup / aria-expanded /
 * aria-controls onto the component it is given, so this has to be a
 * forwardRef that spreads those onto the ActionIcon — otherwise the popover's
 * anchor and its ARIA both point at the outer <div> (or nowhere).
 */
export const NotificationsBell = forwardRef<HTMLButtonElement, NotificationsBellProps>(
  function NotificationsBell(
    { opened, onToggle, "aria-controls": controls, ...rest }: NotificationsBellProps,
    ref: React.ForwardedRef<HTMLButtonElement>,
  ) {
    const unread = useUnreadCount();
    const label = unread > 0 ? `Notifications, ${unread} unread` : "Notifications";

    return (
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
          // Mantine clones its props onto this component and passes className=""
          // when the target has none, so the spread goes first and the shell's
          // 44 px icon button is applied after it — otherwise the button shrinks
          // and the badge intercepts taps at its centre.
          {...rest}
          ref={ref}
          variant="subtle"
          color="gray"
          className={[shell.iconButton, rest.className].filter(Boolean).join(" ")}
          onClick={onToggle}
          aria-label={label}
          aria-haspopup="dialog"
          aria-expanded={opened}
          // Mantine's Popover points this at its own dropdown (the dialog
          // itself, from 768 px up); with no popover it names the sheet's panel.
          aria-controls={controls ?? NOTIFICATIONS_PANEL_ID}
        >
          <IconBell size={24} stroke={NAV_ICON_STROKE} aria-hidden />
        </ActionIcon>
      </Indicator>
    );
  },
);
