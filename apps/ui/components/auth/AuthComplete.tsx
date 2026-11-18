"use client";

import { useEffect, useRef, useState } from "react";
import { Button } from "@mantine/core";

/**
 * Hands the browser from /auth/complete to the page the person asked for
 * (#5304), and is the page Back lands on afterwards.
 *
 * THE HISTORY CONTRACT, which is the whole point of this component:
 *
 *  - On a FRESH load (the callback's redirect, or a bookmark) it writes a
 *    marker into this entry with history.replaceState and then PUSHES the
 *    destination with location.assign. Pushing (not replacing) is what puts
 *    this page -- an app page -- directly behind the destination, so Back from
 *    there lands here instead of on Dex's login form.
 *  - On a BACK/FORWARD arrival (performance navigation type "back_forward", or
 *    the marker already present) it deliberately does NOT navigate. Auto-
 *    forwarding on Back would trap the person: every Back would bounce them
 *    forward again, which is a worse dead end than the one this ticket fixes.
 *    The card and the link stay, so Back is a normal step they can walk out of.
 *
 * Both checks are needed. Without bfcache the browser re-fetches this page on
 * Back and reports "back_forward"; with bfcache the document is restored frozen
 * and the marker in history.state is what identifies it. The marker is written
 * with replaceState so it never adds an entry of its own.
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

export default function AuthComplete({ to }: { to: string }) {
  // Snapshot the arrival ONCE, at the first render, and decide from it. A
  // re-run of the effect (React StrictMode in dev, a re-render) must not be
  // read as a revisit -- and must not navigate twice.
  const arrival = useRef<{ marked: boolean; type: string } | null>(null);
  if (arrival.current === null) {
    arrival.current = { marked: markedCompleted(), type: navigationType() };
  }
  const handedOff = useRef(false);
  const [revisited, setRevisited] = useState(false);

  useEffect(() => {
    const { marked, type } = arrival.current!;
    const backOrForward = type === "back_forward" || type === "prerender";
    if (marked || backOrForward) {
      setRevisited(true);
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
  }, [to]);

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
