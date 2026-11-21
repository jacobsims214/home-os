"use client";

import { useEffect, useRef, useState } from "react";
import { Button } from "@mantine/core";
import { authCompleteAction } from "@/lib/auth/complete-decision";
import { signInAgainHref } from "@/lib/auth/login-surface";

/**
 * Hands the browser from /auth/complete to the page the person asked for
 * (#5304), and is the page Back lands on afterwards (#5407).
 *
 * THE HISTORY CONTRACT, which is the whole point of this component:
 *
 *  - On a FRESH load WITH A SESSION (the callback's redirect, or a bookmark) it
 *    writes a marker into this entry with history.replaceState and then PUSHES
 *    the destination with location.assign. Pushing (not replacing) is what puts
 *    this page -- an app page -- directly behind the destination, so Back from
 *    there lands here instead of on Dex's login form.
 *  - On a BACK/FORWARD arrival (performance navigation type "back_forward", or
 *    the marker already present) it deliberately does NOT navigate. Auto-
 *    forwarding on Back would trap the person: every Back would bounce them
 *    forward again, which is a worse dead end than the one this ticket fixes.
 *    The card and the link stay, so Back is a normal step they can walk out of.
 *  - WITHOUT A SESSION it never navigates, even on a fresh load (#5407). The
 *    destination's guard answers 307 /login for a signed-out visitor, and that
 *    page `location.replace()`s into /api/auth/login and on to Dex -- so
 *    forwarding here does not "continue to the hub", it spends another request
 *    against the Dex login rate limiter and drops the person on whatever that
 *    answers (an unstyled 429 for anyone who has been retrying). The signed-out
 *    card says the session is not active and offers one click back into sign-in
 *    for THIS destination. That is the difference between #5304 and #5407:
 *    #5304 changed which page Back lands on; this decides whether that page
 *    sends the person straight back into the chain that just failed.
 *
 * Both revisit checks are needed. Without bfcache the browser re-fetches this
 * page on Back and reports "back_forward"; with bfcache the document is restored
 * frozen and the marker in history.state is what identifies it. The marker is
 * written with replaceState so it never adds an entry of its own.
 *
 * THIS PAGE MUST NEVER SEE THE AUTHORIZATION CODE. The callback is a redirect
 * hop: /auth/complete is reached by a 307 with only a sanitised `?to=` on it, so
 * no code, state or verifier is ever in this URL, in this document, or logged.
 */

const STATE_MARKER = "homeOsAuthCompleted";

/** True when this entry was already used for a completed hand-off. */
function markedCompleted(): boolean {
  try {
    const state: unknown = window.history.state;
    return typeof state === "object" && state !== null && (state as Record<string, unknown>)[STATE_MARKER] === true;
  } catch {
    // A sandboxed/opaque context: treat as not marked, the navigation-type
    // check below still protects Back.
    return false;
  }
}

function writeMarker(): void {
  try {
    const state: Record<string, unknown> =
      typeof window.history.state === "object" && window.history.state !== null
        ? { ...(window.history.state as Record<string, unknown>) }
        : {};
    state[STATE_MARKER] = true;
    window.history.replaceState(state, "");
  } catch {
    // Not fatal: the navigation still happens, only the bfcache-revisit
    // detection loses its second signal.
  }
}

/** "navigate" | "reload" | "back_forward" | "prerender"; "" where unsupported. */
function navigationType(): string {
  try {
    const [entry] = performance.getEntriesByType("navigation") as PerformanceNavigationTiming[];
    return entry?.type ?? "";
  } catch {
    return "";
  }
}

// The auth type scale the (auth) frame and /login use (#4562).
const H1 = "m-0 text-[24px] font-semibold leading-tight tracking-[-0.015em] text-ink";
const SUB = "mb-5 mt-1.5 text-[15px] leading-normal text-ink-2";

type State = "handing-off" | "revisited" | "signed-out";

export default function AuthComplete({ to, signedIn }: { to: string; signedIn: boolean }) {
  // Snapshot the arrival ONCE, at the first render, and decide from it. A
  // re-run of the effect (React StrictMode in dev, a re-render) must not be
  // read as a revisit -- and must not navigate twice.
  const arrival = useRef<{ marked: boolean; type: string } | null>(null);
  if (arrival.current === null) {
    arrival.current = { marked: markedCompleted(), type: navigationType() };
  }
  const handedOff = useRef(false);
  // A signed-out visitor has nothing to hand off to: decide at render so the
  // card is right on the first paint, with no "Signing you in" flash before it.
  const [state, setState] = useState<State>(signedIn ? "handing-off" : "signed-out");

  useEffect(() => {
    // The decision itself is pure and lives in lib/auth/complete-decision.ts,
    // where complete-decision.test.mjs pins it (#5304, #5407). This effect is
    // the paint: it asks the decision what to do and does it. Nothing here
    // decides WHETHER to navigate — that is the part that regressed twice.
    const { marked, type } = arrival.current!;
    const action = authCompleteAction({ signedIn, marked, navigationType: type });
    if (action !== "hand-off") {
      setState(action);
      return;
    }
    if (handedOff.current) return;
    handedOff.current = true;
    writeMarker();
    // A push, never a replace: this entry has to survive as the screen Back
    // returns to. location.assign is used rather than the Next router because
    // it is a plain document navigation, so it cannot be turned into a
    // soft/replace navigation by the router's own history handling.
    window.location.assign(to);
  }, [to, signedIn]);

  if (state === "signed-out") {
    return (
      <div data-auth-page="auth-complete" data-auth-complete="signed-out">
        <h1 className={H1}>Sign in again</h1>
        <p className={SUB}>Your sign-in isn&apos;t active, so this page can&apos;t continue.</p>
        {/* A way in that keeps the destination, so retrying is one click and
            still ends on the page the person asked for. */}
        <Button
          component="a"
          href={signInAgainHref(to)}
          size="md"
          fullWidth
          className="min-h-12 text-[15px]"
          data-auth-action="sign-in-again"
        >
          Sign in
        </Button>
        <p className="mb-0 mt-5 text-center text-sm text-ink-2">
          If you were just signing in, the attempt may not have finished. Try again from here.
        </p>
      </div>
    );
  }

  const revisited = state === "revisited";
  return (
    <div data-auth-page="auth-complete" data-auth-complete={revisited ? "revisited" : "handing-off"}>
      <h1 className={H1}>{revisited ? "You're signed in" : "Signing you in"}</h1>
      <p className={SUB}>{revisited ? "Continue to your family hub." : "Taking you to your family hub."}</p>
      {/* The one primary action, and the way out if the automatic hand-off did
          not happen (no JS, a blocked navigation, or a Back arrival). */}
      <Button component="a" href={to} size="md" fullWidth className="min-h-12 text-[15px]">
        Continue
      </Button>
    </div>
  );
}
