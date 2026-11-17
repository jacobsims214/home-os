// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
// The active-household cookie (#4574): what the BFF accepts from it, and what a
// stored value has to look like before it is forwarded as X-Household-ID.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  HOUSEHOLD_COOKIE,
  HOUSEHOLD_COOKIE_MAX_AGE,
  householdFromCookieHeader,
  isHouseholdId,
  serializeHouseholdCookie,
} from "./household-cookie.ts";

const A = "0b9d4f1e-6a3c-4b8e-9f52-1c7d2a5e8b10";
const B = "f3a7c2d1-8e4b-4c9f-a1d6-5b2e7c9f0a34";

test("isHouseholdId accepts API-shaped UUIDs and nothing else", () => {
  assert.equal(isHouseholdId(A), true);
  assert.equal(isHouseholdId(A.toUpperCase()), true, "hex case does not matter to the API");
  assert.equal(isHouseholdId(` ${A} `), true, "surrounding space is trimmed before it is sent");
  for (const bad of ["", "family-2", "h1", `${A}-x`, "0b9d4f1e6a3c4b8e9f521c7d2a5e8b10", null, undefined, 42, {}]) {
    assert.equal(isHouseholdId(bad), false, `${String(bad)} is not a household id`);
  }
});

test("householdFromCookieHeader finds the cookie among the session cookies", () => {
  assert.equal(
    householdFromCookieHeader(`home-os-token=abc; ${HOUSEHOLD_COOKIE}=${A}; home-os-refresh=r1`),
    A,
  );
  assert.equal(householdFromCookieHeader(`${HOUSEHOLD_COOKIE}=${encodeURIComponent(A)}`), A);
});

test("householdFromCookieHeader is null when the cookie is absent or unusable", () => {
  assert.equal(householdFromCookieHeader(""), null);
  assert.equal(householdFromCookieHeader(null), null);
  assert.equal(householdFromCookieHeader(undefined), null);
  assert.equal(householdFromCookieHeader("home-os-token=abc; home-os-refresh=r1"), null);
  // Malformed values are "not set", never forwarded: the API answers 400 for a
  // non-UUID header and the page would look broken over a corrupted cookie.
  assert.equal(householdFromCookieHeader(`${HOUSEHOLD_COOKIE}=family-2`), null);
  assert.equal(householdFromCookieHeader(`${HOUSEHOLD_COOKIE}=`), null);
  assert.equal(householdFromCookieHeader(`${HOUSEHOLD_COOKIE}=%E0%A4%A`), null, "bad percent-encoding");
  // A lookalike name must not be mistaken for the cookie.
  assert.equal(householdFromCookieHeader(`x${HOUSEHOLD_COOKIE}=${A}`), null);
});

test("the first value wins, so a duplicated cookie cannot smuggle a second id", () => {
  assert.equal(householdFromCookieHeader(`${HOUSEHOLD_COOKIE}=${A}; ${HOUSEHOLD_COOKIE}=${B}`), A);
});

test("serializeHouseholdCookie round-trips and expires on logout", () => {
  const stored = serializeHouseholdCookie(A);
  assert.match(stored, new RegExp(`^${HOUSEHOLD_COOKIE}=${A}; Path=/; Max-Age=${HOUSEHOLD_COOKIE_MAX_AGE}; SameSite=Lax$`));
  assert.equal(householdFromCookieHeader(stored), A);
  assert.doesNotMatch(stored, /HttpOnly/, "the UI has to read it");
  assert.doesNotMatch(stored, /Secure/, "not a credential, and dev/E2E run on http");

  const cleared = serializeHouseholdCookie(null, { maxAgeSeconds: 0 });
  assert.match(cleared, /Max-Age=0/);
  assert.match(cleared, /Expires=Thu, 01 Jan 1970/);
  assert.equal(householdFromCookieHeader(cleared), null);

  assert.match(serializeHouseholdCookie(A, { secure: true }), /; Secure$/);
});
