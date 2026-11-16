"use client";

/**
 * ResponsiveModal (#4532): the one dialog for create/edit forms.
 *
 * - >= 768 px: a centred Mantine Modal (tokens: --raised, --r-md, --shadow-lg).
 * - < 768 px: a full-height bottom sheet (radius 16 at the top, drag handle,
 *   safe-area insets) that slides up. The layout switch is CSS only
 *   (ResponsiveModal.module.css), so there is no SSR mismatch; JS only picks
 *   the transition, which runs after a click anyway.
 * - Keyboard-safe: while open, window.visualViewport's height and offset are
 *   written to --rm-vv-h / --rm-vv-top on <html>, and the phone sheet is sized
 *   to them. When the iOS keyboard opens the sheet shrinks to the space above
 *   it, the focused field is scrolled into view, and ResponsiveModalActions
 *   stays pinned at the bottom of the visible sheet.
 * - Accessible: role="dialog" + aria-modal labelled by the title, focus moves
 *   in on open (put `data-autofocus` on the first field) and returns to the
 *   opener on close, Tab is trapped inside, Esc and a scrim click close it,
 *   and so does the browser/Android back button (closeOnBack): opening pushes
 *   a same-URL history entry, and closing any other way pops it again.
 *   To navigate after a submit, call router.replace() WITHOUT closing first
 *   (see useCloseOnBack): closing pops the entry, and that pop would cancel
 *   a navigation started in the same tick.
 * - 44 px close button; on phones every Mantine input in the body is 44 px
 *   tall with 16 px text (no iOS zoom), and action buttons are 44 px.
 *
 * Usage:
 *   <ResponsiveModal opened={o} onClose={close} title="Add bill">
 *     <form onSubmit={submit}>
 *       …fields…
 *       <ResponsiveModalActions>
 *         <Button variant="default" onClick={close}>Cancel</Button>
 *         <Button type="submit">Add bill</Button>
 *       </ResponsiveModalActions>
 *     </form>
 *   </ResponsiveModal>
 */

import { useEffect, useRef, type FocusEvent, type ReactNode } from "react";
import { Modal, type ModalProps, type MantineTransition } from "@mantine/core";
import { useMediaQuery, useReducedMotion } from "@mantine/hooks";
import classes from "./ResponsiveModal.module.css";

/** Phones: below Mantine `sm` (48em), same as the app shell. */
export const PHONE_QUERY = "(max-width: 47.99em)";

const SHEET_TRANSITION: MantineTransition = {
  in: { transform: "translateY(0)" },
  out: { transform: "translateY(100%)" },
  common: { transformOrigin: "bottom" },
  transitionProperty: "transform",
};

const FIELD = "input, textarea, select, [contenteditable='true']";

export interface ResponsiveModalProps
  extends Omit<ModalProps, "opened" | "onClose" | "title" | "children" | "fullScreen" | "centered" | "classNames"> {
  opened: boolean;
  onClose: () => void;
  /** Dialog title; also its accessible name. */
  title: ReactNode;
  children: ReactNode;
  /** Close on the browser / Android back button. Default true. */
  closeOnBack?: boolean;
  /** Accessible label of the close button. Default "Close". */
  closeLabel?: string;
}

/**
 * While `active`, mirrors window.visualViewport onto CSS variables on <html>
 * so the phone sheet can size itself to the area above the on-screen keyboard.
 */
function useVisualViewportVars(active: boolean) {
  useEffect(() => {
    const vv = typeof window !== "undefined" ? window.visualViewport : null;
    if (!active || !vv) return;
    const root = document.documentElement;
    const update = () => {
      root.style.setProperty("--rm-vv-h", `${Math.round(vv.height)}px`);
      root.style.setProperty("--rm-vv-top", `${Math.round(vv.offsetTop)}px`);
    };
    update();
    vv.addEventListener("resize", update);
    vv.addEventListener("scroll", update);
    return () => {
      vv.removeEventListener("resize", update);
      vv.removeEventListener("scroll", update);
      root.style.removeProperty("--rm-vv-h");
      root.style.removeProperty("--rm-vv-top");
    };
  }, [active]);
}

/**
 * Back closes the dialog. On open a same-URL history entry is pushed (Next's
 * patched pushState copies its router state onto it), so the browser or
 * Android back button pops that entry instead of leaving the page; the
 * popstate closes the dialog. Closing any other way (Esc, X, scrim, Cancel)
 * while still on the same URL pops the entry again with history.back().
 *
 * Gotcha: Next's own popstate listener runs before ours (window listeners
 * fire in registration order, capture or not), so the pop can't be hidden
 * from the router; it dispatches a RESTORE of the same URL, which is harmless
 * unless a navigation is pending, because a RESTORE cancels it. So to leave
 * the page after a submit, don't close first: call router.replace(url) and
 * let the dialog unmount. Replace swaps out the dialog's entry, so Back from
 * the new page returns to the list, and nothing is popped (the URL changed).
 */
function useCloseOnBack(active: boolean, onClose: () => void) {
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (!active || typeof window === "undefined") return;
    const href = window.location.href;
    let pushed = false;
    let popped = false;

    const onPop = () => {
      popped = true;
      window.removeEventListener("popstate", onPop);
      onCloseRef.current();
    };

    // Deferred so React StrictMode's mount/unmount/mount in dev pushes once.
    const timer = window.setTimeout(() => {
      window.history.pushState({ responsiveModal: true }, "");
      pushed = true;
      window.addEventListener("popstate", onPop);
    }, 0);

    return () => {
      window.clearTimeout(timer);
      window.removeEventListener("popstate", onPop);
      // Closed without Back and still on the page: drop the entry we pushed.
      if (pushed && !popped && window.location.href === href) window.history.back();
    };
  }, [active]);
}

export default function ResponsiveModal({
  opened,
  onClose,
  title,
  children,
  closeOnBack = true,
  closeLabel = "Close",
  size = "md",
  transitionProps,
  closeButtonProps,
  ...rest
}: ResponsiveModalProps) {
  const isPhone = useMediaQuery(PHONE_QUERY);
  const reduceMotion = useReducedMotion();

  useVisualViewportVars(opened);
  useCloseOnBack(opened && closeOnBack, onClose);

  // Phones: once the keyboard has resized the sheet, bring the field into view.
  const onFocus = (e: FocusEvent<HTMLDivElement>) => {
    const el = e.target as HTMLElement;
    if (!window.matchMedia(PHONE_QUERY).matches || !el.matches?.(FIELD)) return;
    window.setTimeout(() => {
      if (document.activeElement === el) el.scrollIntoView({ block: "nearest" });
    }, 300);
  };

  return (
    <Modal
      opened={opened}
      onClose={onClose}
      title={title}
      size={size}
      centered
      trapFocus
      returnFocus
      closeOnEscape
      closeOnClickOutside
      transitionProps={{
        transition: isPhone ? SHEET_TRANSITION : "pop",
        duration: reduceMotion ? 0 : isPhone ? 240 : 180,
        timingFunction: "ease-out",
        ...transitionProps,
      }}
      closeButtonProps={{ "aria-label": closeLabel, size: "xl", iconSize: 20, ...closeButtonProps }}
      classNames={{
        overlay: classes.overlay,
        inner: classes.inner,
        content: classes.content,
        header: classes.header,
        title: classes.title,
        close: classes.close,
        body: classes.body,
      }}
      {...rest}
    >
      <span className={classes.handle} aria-hidden data-responsive-modal-handle />
      <div className={classes.wrap} onFocus={onFocus} data-responsive-modal-body>
        {children}
      </div>
    </Modal>
  );
}

/** A sticky row of action buttons at the bottom of a ResponsiveModal. */
export function ResponsiveModalActions({ children }: { children: ReactNode }) {
  return (
    <div className={classes.actions} data-responsive-modal-actions>
      {children}
    </div>
  );
}
