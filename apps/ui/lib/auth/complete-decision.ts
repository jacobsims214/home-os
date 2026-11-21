/**
 * What /auth/complete does when it loads — as a pure decision (#5304, #5407).
 *
 * WHY THIS IS A SEPARATE, PURE FUNCTION. Back-after-login has now regressed
 * TWICE, and both times the shape changed while the dead end stayed:
 *
 *   1. R2 (#5304): Back landed on Dex's spent-state 400 ("Bad Request"), so an
 *      app page was put between Dex and the destination. Back landed there — and
 *      that page forwarded to the destination UNCONDITIONALLY.
 *   2. R3 (#5407): for anyone whose session was gone (a throttled login, an
 *      expired session), the destination's guard answered 307 /login, whose page
 *      `location.replace()`s into /api/auth/login and on to Dex — where the
 *      login limiter serves a bare `local_rate_limited` body. Three independent
 *      R3 reports hit it. "Back lands in the app" still meant "and is thrown
 *      straight back into the chain that just failed".
 *
 * Both bugs lived in a decision that was only observable through a browser, so
 * both were "verified" by a person clicking Back on a build that had already
 * been deployed — and both came back. The decision is therefore extracted here,
 * where `node --test` pins it in milliseconds (complete-decision.test.mjs), and
 * AuthComplete.tsx keeps nothing but the paint.
 *
 * THE DECISION IS A FUNCTION OF THREE FACTS, and every one is load-bearing.
 * Drop any of them and one of the two dead ends comes back:
 *
 *   - `signedIn` (a session cookie exists): a signed-out visitor is NEVER handed
 *     off. That is the whole of #5407, and it is the R3 regression.
 *   - `marked` / `navigationType`: a Back or forward arrival is never
 *     auto-forwarded, or every Back would bounce forward again — a worse trap
 *     than the one being fixed.
 *   - the destination is only ever used when both of the above allow the
 *     hand-off; it is never a reason to navigate on its own.
 */

/** What the arrival at /auth/complete implies. */
export type AuthCompleteAction =
  /** No session: show the sign-in card. Never navigate — see #5407. */
  | "signed-out"
  /** A Back/forward arrival, or an entry already used: show the card. Never navigate. */
  | "revisited"
  /** A fresh, signed-in arrival: push the destination and hand off. */
  | "hand-off";

export interface AuthArrival {
  /** A session cookie (session or refresh) was present on the request. */
  signedIn: boolean;
  /** The history entry already carries the completion marker (a bfcache restore). */
  marked: boolean;
  /**
   * `performance.getEntriesByType("navigation")[0].type`:
   * "navigate" | "reload" | "back_forward" | "prerender" | "" (unsupported).
   */
  navigationType: string;
}

/**
 * The ORDER of the checks is the contract, not an implementation detail:
 * signed-out is answered FIRST, so no amount of freshness can make a
 * session-less visitor get forwarded. That ordering is what #5407 added, and
 * the case that would silently regress if someone reordered it is pinned by
 * name in the test.
 */
export function authCompleteAction({ signedIn, marked, navigationType }: AuthArrival): AuthCompleteAction {
  // #5407. A signed-out visitor has nothing to hand off TO: the destination's
  // guard would 307 them back into /api/auth/login and Dex, spending another
  // request against the rate limiter that just rejected them.
  if (!signedIn) return "signed-out";

  // #5304. Both revisit signals are needed: without bfcache the browser
  // re-fetches this page on Back and reports "back_forward"; with bfcache the
  // document is restored frozen (no new navigation entry at all) and the marker
  // written with replaceState is the only evidence.
  if (marked || navigationType === "back_forward" || navigationType === "prerender") return "revisited";

  return "hand-off";
}
