// Run: npm test (node --test). Pins the check-off tap guard (#5406).
//
// THE BUG: checking an item removes the row instantly (the deliberate
// optimistic paint, #4566), so the row below slides UP under the finger. A
// second tap ~120 ms later lands on a different item and checks that off too —
// in a supermarket, an unbought item silently marked as bought.
//
// These cases are the guard's whole contract, and they are written so the FIRST
// one fails on the old behaviour (no guard at all: `isMovedRowDoubleTap` did not
// exist) and the rest fail if the guard is ever widened carelessly into
// swallowing real taps.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  TAP_GUARD_MS,
  TAP_SLOP_PX,
  beginCheckTap,
  isMovedRowDoubleTap,
  resetCheckTap,
  tapPointOf,
} from "./tap-guard.ts";

/** One tap on a row at a screen point, `at` ms on the test's clock. */
function tap(over = {}) {
  return {
    listId: "lst-weekly",
    itemId: "i1",
    at: 1_000,
    x: 100,
    y: 300,
    rowTop: 280,
    ...over,
  };
}

/** The click a browser sends for a pointer tap on a row whose top is `rowTop`. */
function clickEvent(over = {}) {
  return {
    detail: 1,
    // The DOM's event time; the guard only ever subtracts two of them.
    timeStamp: 1_000,
    clientX: 100,
    clientY: 300,
    currentTarget: { getBoundingClientRect: () => ({ top: 280, left: 0, width: 390, height: 48 }) },
    ...over,
  };
}

// ── THE BUG ITSELF ─────────────────────────────────────────

test("a second tap on the spot the checked row vacated is the same tap, not a new one", () => {
  // Apples tapped; the row below (Bell peppers) moved into its rect; the thumb
  // did not move. This is the exact reported double-tap (120 ms apart).
  const apples = tap({ itemId: "apples", at: 1_000 });
  const peppers = tap({ itemId: "peppers", at: 1_120, rowTop: 280 });
  assert.equal(isMovedRowDoubleTap(apples, peppers), true);
});

test("the guard is the difference between one write and two", () => {
  resetCheckTap();
  const first = beginCheckTap(tap({ itemId: "apples", at: 1_000 }));
  const second = beginCheckTap(tap({ itemId: "peppers", at: 1_120 }));
  assert.equal(first, false, "the first tap is a real tap: it paints the tick");
  assert.equal(second, true, "the second tap is swallowed: it would hit a different item");
});

test("the same spot but a different list is not the same tap", () => {
  const onWeekly = tap({ listId: "lst-weekly", itemId: "apples" });
  const onChores = tap({ listId: "lst-chores", itemId: "dishes", at: 1_050 });
  assert.equal(isMovedRowDoubleTap(onWeekly, onChores), false);
});

test("a pointer that reports no usable position falls back to the row geometry", () => {
  // Some engines send 0,0 for a touch click. The bug's signature is that the
  // next row now sits in the rect the tapped one occupied, so that decides it.
  const before = tap({ itemId: "apples", x: null, y: null, rowTop: 280 });
  const movedUp = tap({ itemId: "peppers", at: 1_120, x: null, y: null, rowTop: 280 });
  const elsewhere = tap({ itemId: "peppers", at: 1_120, x: null, y: null, rowTop: 400 });
  assert.equal(isMovedRowDoubleTap(before, movedUp), true);
  assert.equal(isMovedRowDoubleTap(before, elsewhere), false);
});

// ── WHAT THE GUARD MUST NEVER SWALLOW ──────────────────────

test("checking the SAME item twice is not swallowed (the store-mode undo)", () => {
  // A checked row holds its place for SINK_MS precisely so the finger that
  // tapped it can tap again to undo. That is a legitimate second tap.
  const check = tap({ itemId: "apples", at: 1_000 });
  const undo = tap({ itemId: "apples", at: 1_120 });
  assert.equal(isMovedRowDoubleTap(check, undo), false);
});

test("a tap on a different item 500 ms later is a new tap", () => {
  const first = tap({ itemId: "apples", at: 1_000 });
  const later = tap({ itemId: "peppers", at: 1_000 + TAP_GUARD_MS + 1 });
  assert.equal(isMovedRowDoubleTap(first, later), false);
});

test("a tap on a different item elsewhere on the screen is a new tap", () => {
  const first = tap({ itemId: "apples", at: 1_000, y: 300 });
  // The next row's centre is 56–64 px away — well outside the slop.
  const next = tap({ itemId: "peppers", at: 1_120, y: 300 + 60, rowTop: 280 + 60 });
  assert.equal(isMovedRowDoubleTap(first, next), false);
});

test("a finger that drifted past the slop is not a double tap", () => {
  const first = tap({ itemId: "apples", at: 1_000, y: 300 });
  const drifted = tap({ itemId: "peppers", at: 1_120, y: 300 + TAP_SLOP_PX + 1 });
  assert.equal(isMovedRowDoubleTap(first, drifted), false);
});

test("a finger that strayed sideways past the slop is not a double tap", () => {
  const first = tap({ itemId: "apples", at: 1_000, x: 100 });
  const strayed = tap({ itemId: "peppers", at: 1_120, x: 100 + TAP_SLOP_PX + 1 });
  assert.equal(isMovedRowDoubleTap(first, strayed), false);
});

test("there is nothing to double-tap before the first tap", () => {
  assert.equal(isMovedRowDoubleTap(null, tap({ itemId: "peppers" })), false);
});

test("a clock that went backwards guards nothing", () => {
  const first = tap({ itemId: "apples", at: 2_000 });
  const backwards = tap({ itemId: "peppers", at: 1_000 });
  assert.equal(isMovedRowDoubleTap(first, backwards), false);
});

// ── WHAT IS NOT A TAP AT ALL ───────────────────────────────

test("keyboard activation is never guarded", () => {
  // Enter/Space on a focused checkbox sends a click with detail 0: there is no
  // finger, so there is no double tap to protect against.
  resetCheckTap();
  const viaKeyboard = tapPointOf("lst-weekly", "apples", clickEvent());
  assert.ok(viaKeyboard, "a pointer tap produces a TapPoint");
  assert.equal(tapPointOf("lst-weekly", "apples", clickEvent({ detail: 0 })), null);
  // And a null point is never swallowed, even right after another row's tap.
  beginCheckTap(tapPointOf("lst-weekly", "apples", clickEvent({ detail: 0 })));
  assert.equal(beginCheckTap(tapPointOf("lst-weekly", "peppers", clickEvent({ detail: 0 }))), false);
});

test("a tap point carries the row's geometry for the no-pointer fallback", () => {
  const point = tapPointOf("lst-weekly", "apples", clickEvent({ timeStamp: 1_234 }));
  assert.deepEqual(point, {
    listId: "lst-weekly",
    itemId: "apples",
    at: 1_234,
    x: 100,
    y: 300,
    rowTop: 280,
  });
});
