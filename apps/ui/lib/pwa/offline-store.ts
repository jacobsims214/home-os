/**
 * The offline/update state, shared by the two halves of #4538's UI.
 *
 * WHY A STORE AND NOT COMPONENT STATE. Registration has to happen once, for the
 * whole app, from the root layout (the login page needs a worker too, or the
 * first visit has no offline shell). The BANNERS, though, belong inside the
 * dashboard shell's content — the approved spec draws the offline state as a
 * warn banner UNDER THE HEADER (KB design/app-shell.md, artifact #75 v1), not as
 * a floating bar over the page. Those are two different mount points, so the
 * state between them lives here rather than in a parent.
 *
 * `useSyncExternalStore` is the React 18 way to read module state: it is
 * tear-free, works during SSR (getServerSnapshot), and needs no provider.
 *
 * Nothing here touches the network or the DOM: the bridge writes, the banners
 * read, and `node --test` can drive the whole thing (lib/pwa/offline-store.test.mjs).
 */

import { useSyncExternalStore } from "react";
import type { RegistrationLike } from "./service-worker";

export interface OfflineState {
  /** False only when the browser says the connection is gone. */
  online: boolean;
  /** A new build's worker, waiting for the person to accept it. */
  update: RegistrationLike | null;
}

/**
 * Online until told otherwise. The server has no navigator, and a banner that
 * flashes on every load would be worse than one that appears a tick late.
 */
const INITIAL: OfflineState = { online: true, update: null };

let state: OfflineState = INITIAL;
const listeners = new Set<() => void>();

function emit(): void {
  // forEach, not for..of: tsconfig targets es5, where Set iteration needs
  // --downlevelIteration.
  listeners.forEach((listener) => listener());
}

/** Replace the state; a no-op when nothing changed, so React does not re-render. */
export function setOfflineState(patch: Partial<OfflineState>): void {
  const next = { ...state, ...patch };
  if (next.online === state.online && next.update === state.update) return;
  state = next;
  emit();
}

export function getOfflineState(): OfflineState {
  return state;
}

export function subscribeOfflineState(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** Test seam: back to the initial state, with no listeners. */
export function resetOfflineState(): void {
  state = INITIAL;
  listeners.clear();
}

/** The current state, as a hook. */
export function useOfflineState(): OfflineState {
  return useSyncExternalStore(subscribeOfflineState, getOfflineState, () => INITIAL);
}
