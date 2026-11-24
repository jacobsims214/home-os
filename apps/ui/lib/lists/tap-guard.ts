/**
 * The tap guard for a check-off row (#5406): "the row moved under the finger".
 *
 * THE BUG THIS EXISTS TO STOP. Checking an item removes it from the visible
 * list at once — that is deliberate, the optimistic paint (#4566) — so the rows
 * below it move UP into the space the finger just left. A second tap ~120 ms
 * later therefore lands on a DIFFERENT item at the same screen coordinates and
 * checks that one off too. In a supermarket that silently marks an unbought
 * item as bought, and nobody buys it.
 *
 * WHY A TAP GUARD AND NOT THE BUTTON-SUBMIT GUARD. A Save button keeps its
 * identity and its place, so its answer is "be inert while the write is
 * pending" (`saving` → Mantine `Button`'s `disabled`; `LogoutForm`'s
 * `submitting` ref latch). A check-off row cannot be made inert that way: after
 * the tap the row is GONE from where it was, and the control that would carry
 * the disabled state is the WRONG row — the one that moved into its place. The
 * latch that does fit is the time window
 * `components/maintenance/TaskCard.tsx` already uses for its status cycle
 * ("ignore a second tap on this control for a moment", `pending` + 500 ms),
 * extended with the one thing it needs to be correct here: it remembers WHICH
 * item was tapped and WHERE.
 *
 * WHAT IT CANNOT SWALLOW (each case is pinned in tap-guard.test.mjs):
 *  - a tap on the SAME item: that is the store-mode UNDO (the row holds its
 *    place for SINK_MS on purpose, see StoreMode.tsx). `previous.itemId ===
 *    next.itemId` returns false before anything else is looked at;
 *  - a tap on a DIFFERENT item anywhere but the same spot: rows are 56–64 px
 *    against a 24 px slop, so a neighbour's centre is never inside it;
 *  - the same spot on a DIFFERENT LIST: the list id is part of the match;
 *  - a tap after the 500 ms window: the person has read the list and moved on;
 *  - keyboard activation (Enter/Space): `detail === 0`, so `tapPointOf` returns
 *    null and nothing is ever guarded.
 */

/** How long after a tap a second tap at the same spot is treated as one. */
export const TAP_GUARD_MS = 500;

/**
 * How far a finger may move and still be "the same spot". A real double tap
 * wanders a few px; a list row is 56–64 px tall, so its neighbour's centre is
 * never inside this radius.
 */
export const TAP_SLOP_PX = 24;

/** One real tap on a row: which list, which item, when, and where. */
export interface TapPoint {
  listId: string;
  itemId: string;
  /** The event's monotonic timeStamp, in ms: only ever used for a difference. */
  at: number;
  /**
   * The pointer's clientX/clientY, or null when the engine did not report a
   * usable one (some touch engines send 0,0; a keyboard click sends nothing).
   */
  x: number | null;
  y: number | null;
  /**
   * The tapped row's viewport top at tap time: the fallback evidence of "same
   * spot" when the pointer position is unavailable. The bug's signature is
   * precisely that the NEXT row now occupies the rect the last one occupied.
   */
  rowTop: number;
}

/** The click fields the guard reads (a real MouseEvent satisfies this). */
export interface TapEvent {
  detail?: number;
  clientX?: number;
  clientY?: number;
  /** The DOM's own monotonic event time (ms since the time origin). */
  timeStamp?: number;
  currentTarget?: Element | null;
}

/**
 * Build a TapPoint from the click that activated a row, or null when the
 * activation was not a tap (keyboard, assistive tech, a script) and so cannot
 * be a double tap on a moved row.
 */
export function tapPointOf(listId: string, itemId: string, event: TapEvent): TapPoint | null {
  // `detail` is the click count: 0 means the control was activated by the
  // keyboard (or a script), not by a pointer. There is no finger to double-tap
  // and no row to have moved under it.
  if (!event.detail) return null;
  const x = event.clientX ?? null;
  const y = event.clientY ?? null;
  // A pointer whose coords are both 0 is the engines' "not reported" value
  // (the top-left corner of the page is never a list row): treat it as unknown
  // and fall back to the row's geometry.
  const usable = x !== null && y !== null && !(x === 0 && y === 0);
  const row = event.currentTarget ?? null;
  const rowTop = row && typeof row.getBoundingClientRect === "function" ? row.getBoundingClientRect().top : 0;
  // `event.timeStamp`, NOT Date.now(): this only ever measures the gap between
  // two taps, and the DOM's event time is monotonic (a wall clock can jump
  // backwards) and reads no ambient clock at all — the #5316 rule in
  // lib/today/one-clock.test.mjs allows instant arithmetic, and this needs not
  // even that.
  const at = event.timeStamp ?? 0;
  return { listId, itemId, at, x: usable ? x : null, y: usable ? y : null, rowTop };
}

/**
 * True when `next` is the second half of a double tap on a row that MOVED: the
 * same spot, moments later, a different item under the finger — and that item
 * now occupies the RECT the previous row left, which is what "the list moved"
 * looks like and "a person re-aimed" does not.
 *
 * The rect test is what keeps the guard narrow. Tapping row i1 checks it and
 * removes it; the row that follows starts at exactly i1's old top, so the two
 * rows share a `rowTop`. A later, deliberate tap on any OTHER row is a whole
 * row height away (56–64 px against a 24 px slop) and never matches, even if
 * it somehow landed on the same pixel.
 *
 * Pure, so the rule is pinned by `node --test` (tap-guard.test.mjs) instead of
 * only being observable through a browser.
 */
export function isMovedRowDoubleTap(
  previous: TapPoint | null,
  next: TapPoint,
  windowMs: number = TAP_GUARD_MS,
  slopPx: number = TAP_SLOP_PX,
): boolean {
  // Nothing before it, another list, or a deliberate second tap on the row
  // itself (the store-mode undo). None of those is this bug.
  if (!previous || previous.listId !== next.listId || previous.itemId === next.itemId) return false;

  const dt = next.at - previous.at;
  // A clock that went backwards is not evidence of anything.
  if (dt < 0 || dt > windowMs) return false;

  // THE SIGNATURE: the row tapped now sits in the rect the tapped row vacated.
  if (Math.abs(next.rowTop - previous.rowTop) > slopPx) return false;

  // And, when both taps reported a pointer position, the finger did not travel
  // either: a deliberate reach for a neighbour is a whole row of movement.
  if (previous.x !== null && previous.y !== null && next.x !== null && next.y !== null) {
    return Math.abs(next.x - previous.x) <= slopPx && Math.abs(next.y - previous.y) <= slopPx;
  }
  return true;
}

/**
 * The last tap, held at module scope the way `pendingChecks` is held in
 * `lib/lists/api.ts`: bookkeeping about the finger, not render data.
 *
 * It MUST be shared rather than per row: the second tap is delivered to a
 * DIFFERENT row component than the first (the tapped row unmounted as the row
 * below it moved up), so a per-row ref would always start empty and never see
 * the double tap at all. A `useRef` in the page would work only for that page's
 * list; a module-level value also covers the page's store-mode copy of the rows.
 */
let lastTap: TapPoint | null = null;

/**
 * Record a tap and say whether the check-off it activated must be ignored.
 *
 * Returns false for anything that is not a pointer tap (a keyboard activation
 * has no TapPoint), so Enter/Space is never swallowed.
 */
export function beginCheckTap(point: TapPoint | null): boolean {
  if (!point) return false;
  const ignore = isMovedRowDoubleTap(lastTap, point);
  lastTap = point;
  return ignore;
}

/** Test seam: forget the last tap (a fresh list, or between cases). */
export function resetCheckTap(): void {
  lastTap = null;
}
