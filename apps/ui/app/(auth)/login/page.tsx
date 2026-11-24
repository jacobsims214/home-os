"use client";

import { useEffect, useState } from "react";
import { Button, Loader } from "@mantine/core";
import { DEFAULT_RETURN_PATH, returnPath } from "@/lib/auth/return-path";
import { RATE_LIMIT_RETRY_SECONDS, loginErrorCopy } from "@/lib/auth/login-surface";
import { hasSessionRemnants, teardownSession } from "@/lib/session-teardown";

/**
 * /login, rebuilt to the approved settings/onboarding mockup (artifact #103
 * v1, "standalone screens: auth" -> sign in) at phone width (#4562).
 *
 * THE AUTH PROTOCOL IS UNTOUCHED. This page never collects a password: it has
 * one job and it kept it -- mount, sweep what an expired session left behind
 * (#4648), then hand off to `/api/auth/login`, which sets the PKCE verifier
 * cookie and 307s to Dex with code_challenge + S256 (#694, #531, #4540). The
 * mockup draws an email/password form because a mockup cannot show a redirect;
 * a real form here would be a second, credential-handling login path beside
 * Dex, which is exactly what this ticket must not add. So the mockup's frame
 * (the (auth) layout's brand row and `acard`, an h1 + sub, a 48 px full-width
 * primary action) carries the two states the page actually has:
 *
 *  - signing in: a spinner and "Redirecting to login..." in a live region, so
 *    a screen reader says what is happening and a slow Dex is obviously
 *    working rather than broken;
 *  - a callback that failed (`?error=`): the mockup's warning block, the same
 *    message the page has always shown, and the 48 px "Try again". It does not
 *    bounce straight back to Dex -- that loops.
 *  - RATE LIMITED (`?error=rate_limited`, #5407): the same warning block with
 *    the throttle's own wording and retry guidance, and NO automatic bounce.
 *    The page that actually serves the throttle is Envoy's local reply (see
 *    the local_reply_config in the chart's envoy ConfigMap, which renders the
 *    same card) -- this state is what any in-app detection of a 429 hands the
 *    person, so the two surfaces cannot disagree about what happened.
 *
 * The `?redirect=` rule is unchanged and still applied here before the link is
 * built: anyone can craft a /login?redirect= link (#4617).
 */

// Server route that creates the PKCE verifier (httpOnly cookie) and redirects
// to Dex's authorize endpoint with code_challenge + S256.
const LOGIN_START = "/api/auth/login";

// /api/auth/login, carrying ?redirect= only once it passes the same rule as
// middleware.ts: anyone can craft a /login?redirect= link (#4617). The login
// route and the callback check it again before the browser is sent there.
function loginStartUrl(redirect: string | null): string {
  const path = returnPath(redirect);
  return path === DEFAULT_RETURN_PATH ? LOGIN_START : `${LOGIN_START}?redirect=${encodeURIComponent(path)}`;
}

// The mockup's auth type scale: 24 px heading, 15 px sub (`.acard h1`,
// `.acard .sub`).
const H1 = "m-0 text-[24px] font-semibold leading-tight tracking-[-0.015em] text-ink";
const SUB = "mb-5 mt-1.5 text-[15px] leading-normal text-ink-2";

export default function LoginPage() {
  const [error, setError] = useState<string | null>(null);
  const [startUrl, setStartUrl] = useState(LOGIN_START);
  // The artifact's sub-line names the hub ("at home.sims.family"). A signed-out
  // page cannot know it at build time -- /login is prerendered -- so it is read
  // off the address bar on mount. Empty until then, and the line falls back to
  // the host-less wording rather than rendering "at ." for a frame.
  const [host, setHost] = useState("");

  useEffect(() => {
    let cancelled = false;
    setHost(window.location.host);
    const params = new URLSearchParams(window.location.search);
    const start = loginStartUrl(params.get("redirect"));
    setStartUrl(start);
    const err = params.get("error");
    if (err) {
      // Don't bounce straight back to Dex after a failed callback: that loops.
      setError(err);
    }

    // Session-expiry sweep (#4648). Every way a session ends lands here: the
    // middleware's 307 with no cookie, the dashboard layout's redirect, a /bff
    // 401 (lib/api.ts) and the logout route's 303. If this browser is still
    // holding a signed-in person's leftovers -- cached /bff responses from a
    // worker built before #4648, or the persisted home-os-auth profile -- drop
    // them before signing anyone in, so an expired session can never serve
    // cached authenticated data. Nothing to do on a first visit, which is why
    // this asks first.
    const sweep = hasSessionRemnants().then((remnants) => (remnants ? teardownSession() : undefined));

    void sweep
      .catch(() => undefined)
      .then(() => {
        // A failed sweep must never block the sign-in.
        if (!cancelled && !err) window.location.replace(start);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  if (error) {
    const copy = loginErrorCopy(error)!;
    return (
      <div data-auth-page={copy.throttled ? "rate-limited" : "login"}>
        {/* The mockup's failed sign-in keeps the screen's heading and puts the
            warning under it (".acard h1" then the .err block), so the page
            never loses its name just because the callback failed. */}
        <h1 className={H1}>{copy.throttled ? copy.title : "Sign in"}</h1>
        <p className={SUB}>{host ? `To your family hub at ${host}` : "To your family hub."}</p>
        <div
          role="alert"
          className="mb-5 flex items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-3.5 text-warn-ink"
        >
          <svg
            width={20}
            height={20}
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth={1.75}
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden
            focusable="false"
            className="mt-0.5 shrink-0"
          >
            <path d="M12 9v4M12 17v.01" />
            <path d="M10.3 4.2 2.6 17.5A2 2 0 0 0 4.3 20.5h15.4a2 2 0 0 0 1.7-3L13.7 4.2a2 2 0 0 0-3.4 0z" />
          </svg>
          <div className="min-w-0">
            <p className="font-semibold">{copy.title}</p>
            <p className="mt-0.5 text-[13px] leading-snug">{copy.body}</p>
          </div>
        </div>
        <Button component="a" href={startUrl} size="md" fullWidth className="min-h-12 text-[15px]">
          {copy.throttled ? "Sign in again" : "Try again"}
        </Button>
        {/* The mockup's `.acard .alt`. There is no self-serve registration:
            an account arrives as an invite from a household admin. A throttled
            visitor gets the retry guidance here instead: it is the only thing
            that helps them, and the throttle lifts on its own. */}
        <p className="mb-0 mt-5 text-center text-sm text-ink-2">
          {copy.throttled
            ? `The pause lifts on its own after about ${RATE_LIMIT_RETRY_SECONDS} seconds.`
            : "New here? Ask someone in your family for an invite link."}
        </p>
      </div>
    );
  }

  return (
    <div data-auth-page="login" role="status" aria-live="polite">
      <h1 className={H1}>Sign in</h1>
      <p className={SUB}>{host ? `To your family hub at ${host}` : "To your family hub."}</p>
      <div className="flex items-center gap-3">
        <Loader size={20} aria-hidden />
        <p className="m-0 text-sm font-medium text-ink">Redirecting to login...</p>
      </div>
      <p className="mb-0 mt-2 text-[13px] leading-snug text-ink-3">
        You&apos;ll be redirected to the authentication page.
      </p>
    </div>
  );
}
