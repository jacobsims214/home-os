"use client";

/**
 * Store mode (#4566): the full-screen shopping view, built to the mockup's
 * "Store mode" state (artifact #88 v1).
 *
 * This is the phone-in-the-supermarket case the ticket is about, so every
 * decision here serves it:
 *
 * - FULL SCREEN, not a dialog over the page. A modal's focus trap and scrim
 *   are for a form you finish and dismiss; this is a screen you use for 40
 *   minutes while walking.
 * - EVERY ROW IS ONE 64 px TAP TARGET for its whole width (the checkbox is
 *   decoration inside it, not a separate control). A 16 px checkbox is a
 *   mis-tap, and a mis-tap in a store means a wrong purchase.
 * - THE LIST POLLS FASTER HERE (3 s, lib/lists/api.ts STORE_POLL_MS): two
 *   people shopping the same list is exactly what the ticket's "two phones
 *   stay in sync" criterion is about.
 * - A CHECKED ROW STAYS PUT for 700 ms (the mockup's "sinking" state) before
 *   it drops into "In the cart", so a mis-tap can be undone where the finger
 *   still is rather than by scrolling to find it.
 * - SCREEN STAYS ON via the Wake Lock API, requested on open. It is skipped
 *   silently where unsupported (Safari < 16.4, a non-secure origin) and
 *   re-acquired on visibilitychange, because the browser drops the lock when
 *   the tab is hidden.
 *
 * The rows are grouped by aisle (`groupItems`), in the store's walking order,
 * so the phone walks the shop the way the shop is laid out.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { IconCheck, IconEyeOff, IconSun, IconX } from "@tabler/icons-react";
import { groupItems, type ItemGroup } from "@/lib/lists/model";
import type { ListItem, ListRow } from "@/lib/lists/types";
import ItemRow from "./ItemRow";

const STROKE = 1.75;
/** How long a checked row holds its place, per the mockup's "sinking" state. */
export const SINK_MS = 700;

export interface StoreModeProps {
  list: ListRow;
  items: ListItem[];
  canCheck: boolean;
  today?: string;
  onToggle: (item: ListItem) => void;
  onClose: () => void;
}

/** Keeps the screen awake while shopping. Returns whether it is on. */
function useWakeLock(active: boolean): boolean {
  const [held, setHeld] = useState(false);
  const lockRef = useRef<{ release: () => Promise<void> } | null>(null);

  useEffect(() => {
    if (!active) return;
    let cancelled = false;
    const nav = typeof navigator === "undefined" ? undefined : (navigator as Navigator & { wakeLock?: { request: (t: "screen") => Promise<{ release: () => Promise<void>; addEventListener: (e: string, f: () => void) => void }> } }).wakeLock;
    if (!nav) return; // Safari < 16.4, non-secure origin: silently no lock.

    const acquire = async () => {
      try {
        const sentinel = await nav.request("screen");
        if (cancelled) {
          void sentinel.release();
          return;
        }
        lockRef.current = sentinel;
        setHeld(true);
        // The browser releases the lock when the tab is hidden; re-acquire.
        sentinel.addEventListener("release", () => setHeld(false));
      } catch {
        // A refused lock (battery saver, permissions) is not an error worth
        // showing: the screen simply dims as it normally would.
        setHeld(false);
      }
    };

    void acquire();
    const onVisible = () => {
      if (document.visibilityState === "visible" && !lockRef.current) void acquire();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      cancelled = true;
      document.removeEventListener("visibilitychange", onVisible);
      const lock = lockRef.current;
      lockRef.current = null;
      if (lock) void lock.release();
    };
  }, [active]);

  return held;
}

export default function StoreMode({ list, items, canCheck, today, onToggle, onClose }: StoreModeProps) {
  // `today` is the household's day, passed down from the page (#5316). Falling
  // back to the browser's clock here is what this consolidation removed: with
  // no day yet, "bought today" simply does not claim one.
  const day = today ?? "";
  const wakeHeld = useWakeLock(true);
  const [hideCart, setHideCart] = useState(false);
  /** Rows checked here in the last 700 ms, still drawn in place. */
  const [sinking, setSinking] = useState<Set<string>>(new Set());
  const timers = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map());

  // A new screen should start at the top, and Esc closes it like any dialog.
  useEffect(() => {
    window.scrollTo(0, 0);
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  useEffect(() => {
    const map = timers.current;
    return () => {
      for (const timer of Array.from(map.values())) clearTimeout(timer);
      map.clear();
    };
  }, []);

  const toggle = (item: ListItem) => {
    onToggle(item);
    // Hold the row in place for SINK_MS when it is being checked, so the
    // finger that just tapped can tap again to undo it.
    if (!item.checked) {
      setSinking((prev) => new Set(prev).add(item.id));
      const existing = timers.current.get(item.id);
      if (existing) clearTimeout(existing);
      const timer = setTimeout(() => {
        timers.current.delete(item.id);
        setSinking((prev) => {
          const next = new Set(prev);
          next.delete(item.id);
          return next;
        });
      }, SINK_MS);
      timers.current.set(item.id, timer);
    }
  };

  const open = useMemo(() => items.filter((item) => !item.checked || sinking.has(item.id)), [items, sinking]);
  const done = useMemo(() => items.filter((item) => item.checked && !sinking.has(item.id)), [items, sinking]);
  const groups: ItemGroup[] = useMemo(() => groupItems(open, list.kind, day), [open, list.kind, day]);
  const pct = items.length ? Math.round((done.length / items.length) * 100) : 0;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="store-title"
      data-store-mode
      // zIndex 400, the same as cook mode (components/recipes/CookMode.tsx):
      // this is a screen you stay on, not a dialog you dismiss, so it must sit
      // above the shell header (100), the phone tab bar (102) and Mantine's
      // modals (200). At z-50 the tap bar painted over the store header and
      // swallowed its own "Done" — the screenshot caught it.
      style={{ zIndex: 400 }}
      className="fixed inset-0 flex flex-col bg-bg"
    >
      <div className="shrink-0 border-b border-line bg-surface pt-[env(safe-area-inset-top)]">
        <div className="flex h-14 items-center gap-1 px-1.5">
          <button
            type="button"
            onClick={onClose}
            className="inline-flex h-11 items-center rounded-token-sm px-3 text-[16px] font-bold text-accent"
          >
            Done
          </button>
          <div className="min-w-0 flex-1 text-center">
            <h2 id="store-title" className="truncate text-[17px] font-semibold leading-tight text-ink">
              {list.name}
            </h2>
            <p className="text-[13px] text-ink-3" data-store-count>
              {open.length ? `${open.length} left of ${items.length}` : `All ${items.length} in the cart`}
            </p>
          </div>
          <button
            type="button"
            onClick={() => setHideCart((h) => !h)}
            aria-pressed={hideCart}
            aria-label="Hide items in the cart"
            className="grid size-11 shrink-0 place-items-center rounded-token-md text-ink-2 hover:bg-surface-2"
          >
            <IconEyeOff size={20} stroke={STROKE} />
          </button>
        </div>
        <div
          role="progressbar"
          aria-label="Checked off"
          aria-valuenow={pct}
          aria-valuemin={0}
          aria-valuemax={100}
          className="h-1 bg-surface-2"
        >
          <i className="block h-full bg-accent-fill transition-[width] duration-300 ease-out" style={{ width: `${pct}%` }} />
        </div>
      </div>

      <div className="flex shrink-0 items-center gap-2 border-b border-line bg-surface px-4 py-2 text-[13px] text-ink-2">
        <span aria-hidden className="size-2 shrink-0 rounded-full bg-accent" />
        <span>Updates every 3 seconds, so everyone shopping sees the same list.</span>
        <span className="ml-auto inline-flex items-center gap-1 text-xs text-ink-3">
          <IconSun size={14} stroke={STROKE} aria-hidden />
          {wakeHeld ? "Screen on" : "Screen may dim"}
        </span>
      </div>

      <div className="flex-1 overflow-auto pb-4">
        {groups.map((group) => (
          <section key={group.key}>
            {group.label && (
              <h3 className="sticky top-0 z-[2] flex justify-between bg-bg px-4 pb-1.5 pt-3.5 text-[12px] font-bold uppercase tracking-[0.07em] text-ink-2">
                {group.label}
                <span className="font-semibold text-ink-3">{group.items.length}</span>
              </h3>
            )}
            <ul className="mx-3 overflow-hidden rounded-token-md border border-line">
              {group.items.map((item) => (
                <ItemRow
                  key={item.id}
                  item={item}
                  kind={list.kind}
                  today={day}
                  canCheck={canCheck}
                  canEdit={false}
                  store
                  onToggle={toggle}
                />
              ))}
            </ul>
          </section>
        ))}

        {open.length === 0 && (
          <div className="mx-3 mt-6 rounded-token-md border border-line bg-surface p-7 text-center">
            <span aria-hidden className="mx-auto mb-3 grid size-14 place-items-center rounded-token-lg bg-accent-soft text-accent">
              <IconCheck size={28} stroke={2.5} />
            </span>
            <b className="block text-lg font-semibold text-ink">Everything is in the cart</b>
            <p className="mt-1 text-sm text-ink-2">Clear the checked items when you&rsquo;re home, or keep them for next time.</p>
            <button
              type="button"
              onClick={onClose}
              className="mt-4 inline-flex h-11 items-center rounded-token-sm bg-accent-fill px-4 text-sm font-semibold text-white"
            >
              Finish shopping
            </button>
          </div>
        )}

        {done.length > 0 && !hideCart && (
          <details className="mx-3 mt-4" open={open.length === 0}>
            <summary className="flex min-h-[52px] cursor-pointer list-none items-center gap-2 px-1 text-sm font-semibold text-ink-2 [&::-webkit-details-marker]:hidden">
              <IconCheck size={18} stroke={STROKE} aria-hidden />
              In the cart · {done.length}
              <span className="ml-auto text-[13px] font-medium text-ink-3">Tap one to put it back</span>
            </summary>
            <ul className="overflow-hidden rounded-token-md border border-line">
              {done.map((item) => (
                <ItemRow
                  key={item.id}
                  item={item}
                  kind={list.kind}
                  today={day}
                  canCheck={canCheck}
                  canEdit={false}
                  store
                  onToggle={toggle}
                />
              ))}
            </ul>
          </details>
        )}
      </div>

      <div className="flex shrink-0 items-center gap-2 border-t border-line bg-surface px-3 py-2.5 pb-[calc(10px+env(safe-area-inset-bottom))]">
        <button
          type="button"
          onClick={onClose}
          className="inline-flex h-[52px] flex-1 items-center justify-center gap-1.5 rounded-token-md bg-accent-fill text-base font-semibold text-white"
        >
          <IconX size={20} stroke={STROKE} aria-hidden />
          Done shopping
        </button>
      </div>
    </div>
  );
}
