// The sign-in surface's copy and rules (#5407).
// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  RATE_LIMIT_RETRY_SECONDS,
  loginErrorCopy,
  signInAgainHref,
} from "./login-surface.ts";

// --- The throttle has its own, actionable wording ---------------------------

test("a rate-limited sign-in explains itself and says how long to wait", () => {
  const copy = loginErrorCopy("rate_limited");
  assert.ok(copy, "rate_limited has copy");
  assert.equal(copy.throttled, true);
  // The bare Envoy literal is never what a person reads.
  assert.doesNotMatch(copy.title, /local_rate_limited/);
  assert.doesNotMatch(copy.body, /local_rate_limited/);
  // It NAMES the situation rather than saying "something went wrong".
  assert.match(copy.title, /too many/i);
  // ...and gives retry guidance, with the window, not just "try later".
  assert.match(copy.body, /wait/i);
  assert.match(copy.body, new RegExp(`${RATE_LIMIT_RETRY_SECONDS} seconds`));
});

test("the throttle's window matches the chart's bucket fill interval", () => {
  // envoy.dexLoginRateLimit: fill_interval 60s, retryAfterSeconds 60. If the
  // limiter is retuned, this constant and the chart must move together or the
  // page tells the person a wait that is wrong in one direction or the other.
  assert.equal(RATE_LIMIT_RETRY_SECONDS, 60);
});

test("the throttle is framed as a pause, not as a failure of the person's account", () => {
  const copy = loginErrorCopy("rate_limited");
  // Defence wording: it says why it exists, so it does not read as a bug.
  assert.match(copy.body, /protect/i);
  // It must never suggest the credentials were wrong: a slow typist at the
  // limit has not mistyped anything.
  assert.doesNotMatch(copy.body, /incorrect|invalid|wrong password/i);
});

// --- Every other code still gets a real answer ------------------------------

test("each callback error code gets copy, and none leaks a raw identifier as the whole message", () => {
  const codes = [
    "no_code",
    "missing_verifier",
    "state_mismatch",
    "token_exchange_failed",
    "no_id_token",
    "token_expired",
    "callback_error",
  ];
  for (const code of codes) {
    const copy = loginErrorCopy(code);
    assert.ok(copy, `${code} has copy`);
    assert.equal(copy.throttled, false, `${code} is not a throttle`);
    assert.match(copy.title, /sign-in/i, `${code} names what failed`);
    // The code is a diagnostic in parentheses, never the message itself.
    assert.match(copy.body, /try again/i, `${code} says what to do`);
    assert.notEqual(copy.body.trim(), code, `${code} is not rendered alone`);
  }
});

test("an unknown error code still renders a real explanation", () => {
  // `?error=` is attacker-typable and the callback may add a code before this
  // map does. A blank page or a raw identifier is the failure mode to avoid.
  const copy = loginErrorCopy("some_new_code");
  assert.ok(copy);
  assert.match(copy.title, /sign-in/i);
  assert.match(copy.body, /try again/i);
});

test("no error means no warning block", () => {
  assert.equal(loginErrorCopy(null), null);
  assert.equal(loginErrorCopy(undefined), null);
  assert.equal(loginErrorCopy(""), null);
});

// --- The retry keeps the destination ---------------------------------------

test("signing in again keeps the page the person was trying to reach", () => {
  assert.equal(signInAgainHref("/dashboard/calendar"), "/login?redirect=%2Fdashboard%2Fcalendar");
  // A destination with its own query survives intact (it is encoded once).
  assert.equal(
    signInAgainHref("/dashboard/bills?month=2026-11"),
    "/login?redirect=%2Fdashboard%2Fbills%3Fmonth%3D2026-11",
  );
  // The value reaching here is already sanitised by returnPath(); the point of
  // this check is that the helper does not hand-build a URL around it.
  assert.match(signInAgainHref("/dashboard"), /^\/login\?redirect=/);
});
