// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
//
// #5307: a fresh sign-in → /dashboard/calendar intermittently showed
// "Couldn't load the calendar" and a second visit worked. These tests pin the
// two halves of the fix: the reads wait for the household to be resolved, and a
// read that fails for a reason that IS a race retries once — while every real
// answer (role refusal, server fault, offline) is kept, never retried on a
// timer.
import { test } from "node:test";
import assert from "node:assert/strict";
import "./node-ts-resolve.mjs";

// After the resolver: static imports are resolved before its hooks register,
// and readiness.ts imports "@/lib/api" the way Next does.
const { ApiError } = await import("../api.ts");
const { calendarReadsReady, shouldRetrySessionRead } = await import("./session-readiness.ts");

// ── the precondition ────────────────────────────────────────

test("reads wait for the household to be resolved (the missing precondition)", () => {
  // The bug: on a fresh sign-in the page had `today` from the browser's clock
  // while /me/households was still in flight, so all five reads fired with no
  // household known. That must NOT enable them any more.
  assert.equal(calendarReadsReady({ resolved: false, failed: false }), false);
});

test("reads fire once the household is resolved", () => {
  assert.equal(calendarReadsReady({ resolved: true, failed: false }), true);
});

test("a /me/households failure does not trap the page on a skeleton", () => {
  // Waiting can't end here: the page loads and says what's wrong, the same
  // rule as a failed calendar read (never an empty week, never a spinner
  // forever).
  assert.equal(calendarReadsReady({ resolved: false, failed: true }), true);
  assert.equal(calendarReadsReady({ resolved: true, failed: true }), true);
});

// ── the retry condition ─────────────────────────────────────

const err = (status, { offline = false } = {}) =>
  new ApiError(status, `API ${status}`, undefined, offline);

test("retries the transient session race once: 401 and 503", () => {
  assert.equal(shouldRetrySessionRead(0, err(401)), true, "no session yet");
  assert.equal(shouldRetrySessionRead(0, err(503)), true, "AUTH_UNAVAILABLE");
  // Once only: a session that still isn't there is a signed-out person, not a
  // race.
  assert.equal(shouldRetrySessionRead(1, err(401)), false);
  assert.equal(shouldRetrySessionRead(1, err(503)), false);
});

test("keeps every answer that is not a race", () => {
  // 403 is the role/household policy, 404 and 500 are what they say, and a
  // 400 means the request itself is wrong. None of these change by asking
  // again.
  for (const status of [400, 403, 404, 409, 422, 429, 500, 502, 504]) {
    assert.equal(shouldRetrySessionRead(0, err(status)), false, String(status));
  }
});

test("never retries an offline device", () => {
  // #4538: the offline copy is the honest answer, and nothing is queued.
  assert.equal(shouldRetrySessionRead(0, err(0, { offline: true })), false);
  assert.equal(shouldRetrySessionRead(0, err(503, { offline: true })), false);
});

test("never retries something that is not an API error", () => {
  // A TypeError from a bug in a queryFn is not a session race; retrying it
  // just hides the bug for one round trip.
  assert.equal(shouldRetrySessionRead(0, new TypeError("x is not a function")), false);
  assert.equal(shouldRetrySessionRead(0, new Error("boom")), false);
  assert.equal(shouldRetrySessionRead(0, undefined), false);
  assert.equal(shouldRetrySessionRead(0, { status: 401 }), false, "a plain object is not an ApiError");
});
// ── the first-load timeline (#5307, the actual bug) ─────────

test("nothing is requested before the household is resolved — not even once", () => {
  // The old page enabled its reads on `!!today`, and `today` is set at mount
  // from the browser's clock. Replay a fresh sign-in: the page mounts, the
  // clock sets `today`, and /me/households is still in flight (the auth
  // callback has just cleared the household cookie, #4574).
  const pageMounts = { today: "2026-11-18", resolved: false, failed: false };
  const readsEarly = pageMounts.today ? 1 : 0; // the old expression
  const readsNow = calendarReadsReady(pageMounts) ? 1 : 0; // the fix

  assert.equal(readsEarly, 1, "the bug: a read went out with no household known");
  assert.equal(readsNow, 0, "the fix: no request until the household is known");

  // Then the list answers, and the read fires exactly once, scoped right.
  const resolved = { ...pageMounts, resolved: true };
  assert.equal(calendarReadsReady(resolved) ? 1 : 0, 1);
});
