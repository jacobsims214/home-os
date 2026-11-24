"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@mantine/core";
import { signInAgainHref } from "@/lib/auth/login-surface";

/**
 * Hands the browser from /auth/complete to the page the person asked for
 * (#5304), and is the page Back lands on afterwards (#5407, #5481).
 *
 * THE HISTORY CONTRACT, which is the whole point of this component:
 *
 *  - On a FRESH load WITH A SESSION (the callback's redirect, or a bookmark) it
 *    claims this document's history entry and then navigates to the destination
 *    IN THE SAME DOCUMENT, so this page stays directly behind the destination
 *    and Back from there lands here instead of on Dex's login form.
 *  - On a BACK/FORWARD arrival (a back/forward traversal, or the marker in this
 *    entry's history.state) it deliberately does NOT navigate. Auto-forwarding
 *    on Back would trap the person: every Back would bounce them forward again,
 *    which is a worse dead end than the one this ticket fixes. The card and the
 *    link stay, so Back is a normal step they can walk out of -- and the card
 *    re-reads the session, because the page is no longer re-requested on Back.
 *  - WITHOUT A SESSION it never navigates, even on a fresh load (#5407). The
 *    destination's guard answers 307 /login for a signed-out visitor, and that
 *    page `location.replace()`s into /api/auth/login and on to Dex -- so
 *    forwarding here does not "continue to the hub", it spends another request
 *    against the Dex login rate limiter and drops the person on whatever that
 *    answers (an unstyled 429 for anyone who has been retrying). The signed-out
 *    card says the session is not active and offers one click back into sign-in
 *    for THIS destination.
 *
 * WHY NO CROSS-DOCUMENT NAVIGATION (#5481 -- THIS IS THE THIRD ATTEMPT).
 * #5304 made this page and #5407 gated it on the session, and both handed off
 * with `location.assign(to)` -- a full document navigation. That is the bug, and
 * it is a race, which is why the two previous fixes tested green and still failed
 * live 3/6 (#5463).
 *
 * Chromium may classify a document navigation that a script starts, from a
 * document that was itself reached by a redirect chain and that has seen no user
 * gesture, as the CONTINUATION of that chain ("client redirect"). When it does,
 * the new document REPLACES this page's history entry instead of pushing onto
 * it: /auth/complete never survives as an entry, Dex's login form stays
 * immediately behind the destination, and Back goes back into the login chain --
 * either Dex re-serving its form, or (its state now spent) the raw
 * `Bad Request — "Requested resource does not exist."` body. When Chromium does
 * not, the entry survives and Back lands here, which is the 3/6 that passed.
 * The classification is a timing decision inside the browser: no amount of care
 * on our side makes `location.assign` deterministic.
 *
 * Measured on a faithful replica of the chain (one origin, the real
 * 307/302/303/303 topology): `location.assign` produced a 3-entry history with
 * no completion entry in 8/8 runs (Back -> Dex's form) -- deferred to `load`,
 * identical. Adding an explicit `history.pushState` before it still lost the
 * entry in some runs under load. An in-document navigation lost it in 0/8.
 *
 * So the hand-off is now a SAME-DOCUMENT navigation through the Next router.
 * There is no document navigation left to reclassify, so the completion entry
 * cannot be folded into the login chain: the history is always
 * [.. Dex form, /auth/complete, destination], with the entry created by a
 * plain pushState that the browser has no reason to merge.
 *
 * A `history.pushState` still runs FIRST, and it is not redundant: it is what
 * gives THIS entry a state marker, which is how a Back arrival recognises itself
 * (a same-document Back does not report navigation type "back_forward"). It is
 * also a cheap belt: if a future browser folds the entry anyway, replacing it
 * would still leave the completion URL -- an app page -- where the login chain
 * was.
 *
 * THIS PAGE MUST NEVER SEE THE AUTHORIZATION CODE. The callback is a redirect
 * hop: /auth/complete is reached by a 307 with only a sanitised `?to=` on it, so
 * no code, state or verifier is ever in this URL, in this document, or logged.
 */

const STATE_MARKER = "homeOsAuthCompleted";

/** True when THIS entry already completed a hand-off (so Back must not navigate again). */
function markedCompleted(): boolean {
  try {
    const state: unknown = window.history.state;
    return typeof state === "object" && state !== null && (state as Record<string, unknown>)[STATE_MARKER] === true;
  } catch {
    // A sandboxed/opaque context: treat as not marked; the popstate check below
    // still protects Back.
    return false;
  }
}

/**
 * Writes the marker into THIS entry, without adding one of its own: it keeps
 * Next's own history state (the router stores its tree there and drops it if we
 * overwrite the object) and adds only the marker.
 */
function writeMarker(): void {
  try {
    const state: Record<string, unknown> =
      typeof window.history.state === "object" && window.history.state !== null
        ? { ...(window.history.state as Record<string, unknown>) }
        : {};
    state[STATE_MARKER] = true;
    window.history.replaceState(state, "");
  } catch {
    // Not fatal: the navigation still happens, only the marker is lost, and the
    // popstate check still covers the Back arrival.
  }
}

/**
 * "navigate" | "reload" | "back_forward" | "prerender"; "" where unsupported.
 *
 * Still worth reading: a browser that restores this page from the back/forward
 * cache delivers it with the arrival's own navigation type, and a page reloaded
 * on Back reports "back_forward". Both are revisits.
 */
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
  const router = useRouter();
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

  // A Back/Forward traversal is the same document in this design, so the
  // navigation type never changes -- the popstate is the only signal. Without
  // this, going Back here and then Forward again could re-run the hand-off.
  //
  // THE REFRESH IS WHAT KEEPS THE CARD HONEST (#5481). This page's `signedIn`
  // is read from cookies ON THE SERVER, and in this design Back no longer
  // re-requests the page -- it restores the entry we already have, so the prop
  // is whatever it was when the login finished. Left alone, a person whose
  // session has ended since would come Back to a card reading "You're signed in
  // / Continue to your family hub", and the Continue would drop them into the
  // login chain. router.refresh() re-runs the server component for the CURRENT
  // route and patches the tree in place -- same document, same history entry,
  // no navigation -- so the card is re-decided from the cookies that exist now.
  // Guarded by a ref: the effect re-runs when the refreshed prop arrives, and
  // one refresh is enough.
  // `refresh` is read through a ref inside the listener so the effect can run
  // once: router.refresh() re-renders this component, and a dependency on the
  // router object would re-subscribe for nothing.
  const routerRef = useRef(router);
  routerRef.current = router;
  const refreshed = useRef(false);
  const refreshSession = () => {
    if (refreshed.current) return;
    refreshed.current = true;
    routerRef.current.refresh();
  };

  useEffect(() => {
    const onPopState = () => {
      const { type } = arrival.current!;
      // A history traversal means this entry is a revisit, whatever the URL.
      if (type !== "reload") setState((current) => (current === "signed-out" ? current : "revisited"));
      // ...and the session is re-read: Back does not re-request this page, so
      // the server-computed `signedIn` is whatever it was at login time.
      refreshSession();
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  useEffect(() => {
    if (!signedIn) {
      setState("signed-out");
      return;
    }
    const { marked, type } = arrival.current!;
    const backOrForward = type === "back_forward" || type === "prerender";
    if (marked || backOrForward) {
      setState("revisited");
      // Restored from the back/forward cache rather than traversed (no popstate
      // fired for THIS document): re-read the session the same way.
      refreshSession();
      return;
    }
    if (handedOff.current) return;
    handedOff.current = true;
    // 1. Mark THIS entry, which is also how a later Back arrival recognises
    //    itself: a same-document Back reports navigation type "navigate", so the
    //    marker and the popstate listener are the signals that remain.
    writeMarker();
    // 2. Navigate to the destination WITHOUT a document load. This is the point
    //    of the whole file: a router push creates its history entry with
    //    pushState inside this document, so there is no navigation for Chromium
    //    to reclassify as the continuation of the login redirect chain -- which
    //    is exactly how `location.assign` lost this page's entry (#5481).
    router.push(to);
  }, [router, to, signedIn]);

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
