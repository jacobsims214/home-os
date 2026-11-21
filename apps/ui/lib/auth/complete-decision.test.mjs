// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
//
// Back-after-login (#5304 -> #5407) — THE ONE THAT REGRESSED TWICE.
//
// Both rounds "fixed" it and both times it came back in a new shape, because
// the decision lived only inside a client component and could only be observed
// by a person clicking Back on a deployed build. These cases pin the decision
// itself, so a regression is a red unit test in ~2 ms instead of a third
// war-game round.
//
// The two live defects, each written as a case that FAILS on the code that
// shipped them:
//
//   A. #5407 (R3): a signed-out visitor was forwarded to the destination, whose
//      guard bounced them into /api/auth/login and Dex, where the limiter served
//      a bare `local_rate_limited` page. On the pre-#5407 code this case returns
//      "hand-off" — a navigation — instead of "signed-out".
//   B. #5304 (R2): a Back arrival on a session whose entry was already used was
//      forwarded again, so Back could never be walked out of. On the pre-#5304
//      shape this case returns "hand-off" instead of "revisited".
import { test } from "node:test";
import assert from "node:assert/strict";
import { authCompleteAction } from "./complete-decision.ts";

/** A fresh, signed-in arrival: the callback's own redirect. */
const fresh = { signedIn: true, marked: false, navigationType: "navigate" };

// ── THE R3 REGRESSION (#5407): a signed-out visitor is never forwarded ───────
//
// THIS IS THE CASE THE TREADMILL MISSED. R2 shipped "Back lands on an app page"
// and forwarded unconditionally; R3 found that a session that did not survive
// still ended at the Dex rate limiter. The assertion is not "the page renders" —
// that passed while the bug shipped — it is "this page decides NOT to navigate".

test("signed out, fresh arrival: never hand off, however the page was reached (#5407)", () => {
  // Every way a session-less visitor can arrive at /auth/complete. A forward is
  // wrong in all of them: the destination's guard answers 307 /login and the
  // login page replaces itself into the Dex chain.
  for (const navigationType of ["navigate", "reload", "back_forward", "prerender", ""]) {
    assert.equal(
      authCompleteAction({ signedIn: false, marked: false, navigationType }),
      "signed-out",
      `navigationType=${JSON.stringify(navigationType)}`,
    );
  }
});

test("signed out beats every other reason to navigate (the check order is the fix)", () => {
  // A session-less visitor whose entry ALSO looks fresh must still not be
  // forwarded. If someone reorders the checks so freshness is answered first,
  // this case goes red — which is exactly how #5407 would come back.
  assert.equal(authCompleteAction({ signedIn: false, marked: false, navigationType: "navigate" }), "signed-out");
  // ...and one whose entry is marked is told it is signed out, not "revisited":
  // the card that renders has to be the sign-in one, or the person is told
  // "You're signed in" with no session.
  assert.equal(authCompleteAction({ signedIn: false, marked: true, navigationType: "back_forward" }), "signed-out");
});

// ── THE R2 REGRESSION (#5304): Back must be walkable ────────────────────────

test("a Back/forward arrival is never auto-forwarded (#5304)", () => {
  // Without this, every Back bounces the person forward again: a trap that is
  // worse than the dead end the page was built to remove.
  assert.equal(authCompleteAction({ ...fresh, navigationType: "back_forward" }), "revisited");
});

test("an entry that already handed off is not handed off twice (the bfcache path)", () => {
  // With bfcache the document is restored FROZEN: no new navigation entry, so
  // the navigation type still says "navigate". The replaceState marker is the
  // only evidence that this entry was already used — drop this check and the
  // Back trap comes back on exactly the browsers that use bfcache.
  assert.equal(authCompleteAction({ ...fresh, marked: true }), "revisited");
  assert.equal(authCompleteAction({ ...fresh, marked: true, navigationType: "reload" }), "revisited");
});

test("a prerender is treated as a revisit, never as a hand-off", () => {
  // A prerendered load is not the person asking for this page; navigating there
  // would spend a Dex request for a page nobody is looking at.
  assert.equal(authCompleteAction({ ...fresh, navigationType: "prerender" }), "revisited");
});

// ── THE HAPPY PATH, so the guard is not just "never navigate" ───────────────

test("a fresh signed-in arrival is the one case that hands off", () => {
  // If this ever stops being "hand-off", login is broken outright — the other
  // half of the contract, and the reason a naive "never navigate" fix cannot
  // pass this suite.
  assert.equal(authCompleteAction(fresh), "hand-off");
  // A reload of this page is a fresh navigation of an unmarked entry (the
  // marker is written at hand-off time, so a reload before it is still fresh).
  assert.equal(authCompleteAction({ ...fresh, navigationType: "reload" }), "hand-off");
  // An engine that reports no navigation type at all (the "" case) is treated
  // as fresh: unknown must not mean "trapped on a card with no way forward",
  // because the card always carries the Continue link.
  assert.equal(authCompleteAction({ ...fresh, navigationType: "" }), "hand-off");
});

test("the action is total: every combination answers exactly one of three things", () => {
  const actions = new Set();
  for (const signedIn of [true, false]) {
    for (const marked of [true, false]) {
      for (const navigationType of ["navigate", "reload", "back_forward", "prerender", ""]) {
        const action = authCompleteAction({ signedIn, marked, navigationType });
        assert.ok(
          ["signed-out", "revisited", "hand-off"].includes(action),
          `unexpected action ${action} for ${JSON.stringify({ signedIn, marked, navigationType })}`,
        );
        actions.add(action);
      }
    }
  }
  // All three are reachable: a decision function where one branch is dead would
  // let a whole class of arrival go unhandled.
  assert.deepEqual([...actions].sort(), ["hand-off", "revisited", "signed-out"]);
});

// ── THE LINK, so this file cannot become decoration ────────────────────────
//
// A pure decision nothing calls is a very convincing green tick. The component
// could inline its own copy of the rule — which is exactly how the defect
// shipped twice: the logic was in the component, so no test could reach it and
// each fix was verified by a person clicking Back on a deployed build. These
// two assertions keep the tested function and the shipped component the same
// function.

import { readFileSync } from "node:fs";

test("AuthComplete uses the tested decision, and does not re-implement it", () => {
  const source = readFileSync(new URL("../../components/auth/AuthComplete.tsx", import.meta.url), "utf8");
  // It must import the decision from the module this file tests.
  assert.match(source, /import\s*\{\s*authCompleteAction\s*\}\s*from\s*"@\/lib\/auth\/complete-decision"/);
  // ...and call it.
  assert.match(source, /authCompleteAction\(\{/);
  // The re-implementation must be gone: a branch on the navigation type or the
  // marker inside the component is the shape that regressed twice. Anchored on
  // the string literals the old inline rule compared against, which have no
  // other reason to appear in a component that delegates.
  assert.doesNotMatch(source, /type\s*===\s*"back_forward"/, "the component re-implements the revisit rule");
  assert.doesNotMatch(source, /type\s*===\s*"prerender"/, "the component re-implements the revisit rule");
});
