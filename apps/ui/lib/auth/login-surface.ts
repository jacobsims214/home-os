/**
 * What the sign-in surface says, and what it offers, for each way a login can
 * fail (#5407).
 *
 * WHY THIS IS A MODULE AND NOT COPY INSIDE THE PAGES. The rate limit is served
 * by Envoy, not by us (deploy/helm/.../envoy/configmap.yaml throttles every
 * `/dex/auth` request and answers 429 with a local reply), so the wording has to
 * exist in two places: this app's pages, and the standalone HTML Envoy serves
 * from its `local_reply_config`. Keeping the codes, the retry window and the
 * sentence describing them in one tested place stops the two drifting, and lets
 * `node --test` assert the copy without a browser.
 *
 * The rule this encodes, from the ticket: ANY 429/rate-limit response must
 * render a real page with an explanation and retry guidance. The bare literal
 * `local_rate_limited` (Envoy's own local-reply body) is never acceptable as
 * something a person reads.
 *
 * No imports: loadable from a client component, a route handler and node --test.
 */

/**
 * How long a throttled sign-in has to wait, in seconds.
 *
 * Matches `envoy.dexLoginRateLimit`: a 10-requests/minute bucket refilled every
 * 60 s (values.yaml), and the `retry-after: "60"` the route sends. Deliberately
 * a constant, not a value read off the response: the browser never sees our
 * Envoy response headers when the block happens on a Dex hop, and a countdown
 * that is 60 s in one place and 30 in another is worse than none.
 */
export const RATE_LIMIT_RETRY_SECONDS = 60;

/** The `?error=` codes the login surface understands. */
export type LoginErrorCode =
  | "rate_limited"
  | "no_code"
  | "missing_verifier"
  | "state_mismatch"
  | "token_exchange_failed"
  | "no_id_token"
  | "token_expired"
  | "callback_error";

export type LoginErrorCopy = {
  /** Short heading of the warning block. */
  title: string;
  /** One sentence a family member can act on. */
  body: string;
  /** True when the only action is to wait (a throttle, not a failure). */
  throttled: boolean;
};

/**
 * The warning shown for a failed sign-in, or null when there is no error.
 *
 * An UNKNOWN code still gets a real answer, because the code is echoed from the
 * URL and anyone can type one: the generic wording is the fallback, never a
 * blank page and never the raw value alone.
 */
export function loginErrorCopy(code: string | null | undefined): LoginErrorCopy | null {
  if (!code) return null;
  if (code === "rate_limited") {
    return {
      title: "Too many sign-in attempts",
      body:
        `Sign-in is paused for about ${RATE_LIMIT_RETRY_SECONDS} seconds to protect your family's account ` +
        "from password guessing. Wait a moment, then try again.",
      throttled: true,
    };
  }
  return {
    title: "Sign-in didn't complete",
    // The code is included because it is the only diagnostic a person can
    // usefully quote; it is echoed through React, so it is escaped, and it is
    // never a token (the callback's codes are fixed literals).
    body: `Something went wrong while signing you in (${code}). Please try again.`,
    throttled: false,
  };
}

/**
 * Whether a visitor who arrives signed OUT at `/auth/complete` may be handed
 * straight to their destination (#5407).
 *
 * NO. That hand-off is the dead end the ticket reports. With no session, the
 * destination's guard answers 307 to /login, whose page `location.replace()`s
 * into /api/auth/login, which 307s to Dex -- so the person's Back press buys
 * them a second full round trip through the login chain, and if they have been
 * signing in repeatedly, the rate limiter answers that round trip with a bare
 * 429 body. The page must show the sign-in card instead, and that card's own
 * link carries `?redirect=` so retrying still returns them here.
 *
 * A signed-IN visitor is handed off as before (#5304).
 */
export function shouldHandOff(signedIn: boolean): boolean {
  return signedIn;
}

/**
 * The destination `/auth/complete` offers as a "sign in again" step.
 *
 * It is the page the person was trying to reach (already sanitised by
 * returnPath()), so the retry is one action rather than a trip to the dashboard.
 */
export function signInAgainHref(to: string): string {
  return `/login?redirect=${encodeURIComponent(to)}`;
}
