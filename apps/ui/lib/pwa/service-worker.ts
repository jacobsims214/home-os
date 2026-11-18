/**
 * Service worker registration and the update flow (#4538).
 *
 * REGISTRATION. next-pwa's `register: true` injects its registration into the
 * pages-router `main.js` entry, and this is an App Router app: `main-app-*.js`
 * loads and `main.js` never does, so the worker was never registered at all
 * (found while testing #4648 — the KB note "the worker never registered"). This
 * module is the real registration, called once from ServiceWorkerBridge.
 *
 * THE UPDATE FLOW, and why the worker waits. `skipWaiting: false` means a new
 * build's worker installs and then sits in `waiting` while the OLD one keeps
 * serving. That is the right default: swapping the worker under someone
 * mid-form would replace the assets a running page is loading. What must not
 * happen is the other extreme — a stale shell served forever. So:
 *
 *   - as soon as a new worker is waiting, the app offers "Reload to update";
 *   - the person's tap posts SKIP_WAITING to the waiting worker;
 *   - the new worker activates, and on `controllerchange` the page reloads ONCE.
 *
 * Nothing here reloads on its own: there is no `location.reload()` on a timer,
 * and no reload when the connection comes back (next-pwa's `reloadOnOnline` is
 * gone with its register.js, on purpose — going offline and back during a
 * checkout or a half-written note is not a reason to lose the screen).
 *
 * WHILE OFFLINE. This module also reports the browser's online/offline state,
 * so the shell can say so (OfflineBanner) and a write can fail with a clear
 * message instead of a generic network error. It never queues a write: see
 * lib/pwa/offline.js for why a silent retry queue is the data-loss trap this
 * ticket rules out.
 *
 * Plain data + DOM APIs only, no JSX and no React, so `node --test` can exercise
 * the decision functions (lib/pwa/service-worker.test.mjs) with stand-ins.
 */

import { SKIP_WAITING_MESSAGE } from "./offline.js";

/** The worker next-pwa writes to public/sw.js, at the app's scope. */
export const SERVICE_WORKER_URL = "/sw.js";
export const SERVICE_WORKER_SCOPE = "/";

/** The event the app fires when a new build is ready to take over. */
export const UPDATE_READY_EVENT = "home-os:update-ready";

/**
 * The subset of ServiceWorkerRegistration this module uses. Declared so tests
 * can pass a stand-in and the code never needs `as any`.
 */
type WaitingWorker = { postMessage: (message: unknown) => void; state?: string } | null;

export interface RegistrationLike {
  waiting: WaitingWorker;
  installing?: WaitingWorker;
  addEventListener?: (type: string, listener: () => void) => void;
}

/**
 * True when the registration is holding a NEW worker that is not the one
 * running the page. `waiting` is set exactly when an update finished installing
 * while another worker already controls the page, which is the "offer a reload"
 * condition. It is also set on the very first install of a follow-up load in
 * some browsers, so `controller` is checked where the caller has it: a page
 * that no worker controls yet is a first install, not an update.
 */
export function hasWaitingUpdate(registration: RegistrationLike | null | undefined, hasController: boolean): boolean {
  if (!registration || !registration.waiting) return false;
  // No controller yet: this page was never under a worker, so the "waiting"
  // one is the first install. Nothing to reload — it takes over on its own.
  return hasController;
}

/**
 * Asks the waiting worker to take over. Called ONLY from the person's tap on
 * "Reload to update": this is the whole point of `skipWaiting: false`.
 */
export function applyUpdate(registration: RegistrationLike | null | undefined): boolean {
  const waiting = registration && registration.waiting;
  if (!waiting) return false;
  waiting.postMessage({ type: SKIP_WAITING_MESSAGE });
  return true;
}

/**
 * Registers the worker and wires the update flow.
 *
 * Returns the registration (or null when the browser has no service workers, or
 * registration failed — a failed registration must never break the app: the
 * pages work online without a worker, they just have no offline shell).
 *
 * `onUpdateReady` is called once per newly-waiting worker. `onControllerChange`
 * is called when a worker takes control of this page, which is the signal to
 * reload — the caller decides, so the page can skip the reload if the update was
 * the FIRST install.
 */
export async function registerServiceWorker(options: {
  onUpdateReady?: (registration: RegistrationLike) => void;
  onControllerChange?: () => void;
  /** For tests: the container, defaulting to navigator.serviceWorker. */
  container?: ServiceWorkerContainer | null;
} = {}): Promise<RegistrationLike | null> {
  const container =
    options.container !== undefined
      ? options.container
      : typeof navigator !== "undefined"
        ? navigator.serviceWorker
        : null;
  // No worker support (or an insecure context, where the API is absent): the app
  // runs normally, just without an offline shell.
  if (!container || typeof container.register !== "function") return null;

  try {
    const registration = (await container.register(SERVICE_WORKER_URL, {
      scope: SERVICE_WORKER_SCOPE,
    })) as unknown as ServiceWorkerRegistration;

    const hasController = () => !!container.controller;

    // A worker that finished installing before this listener existed.
    if (hasWaitingUpdate(registration, hasController())) {
      options.onUpdateReady?.(registration);
    }

    // A worker that finishes installing while the page is open.
    if (typeof registration.addEventListener === "function") {
      registration.addEventListener("updatefound", () => {
        const installing = (registration as ServiceWorkerRegistration).installing;
        if (!installing || typeof installing.addEventListener !== "function") return;
        installing.addEventListener("statechange", () => {
          if (installing.state === "installed" && hasWaitingUpdate(registration, hasController())) {
            options.onUpdateReady?.(registration);
          }
        });
      });
    }

    if (options.onControllerChange && typeof container.addEventListener === "function") {
      // Fires when activate() takes control (skipWaiting + clientsClaim, or a
      // first install). The caller reloads once, and only when it was an update.
      let fired = false;
      container.addEventListener("controllerchange", () => {
        if (fired) return;
        fired = true;
        options.onControllerChange?.();
      });
    }

    return registration as unknown as RegistrationLike;
  } catch {
    // A registration failure is not an app failure: no offline shell, but every
    // page still works. (Common cause: an insecure origin, where registration is
    // refused.)
    return null;
  }
}

/**
 * The browser's connectivity, as a subscription. Returns an unsubscribe
 * function. `navigator.onLine` is optimistic (it reports true on many captive
 * portals), so it is used for the BANNER and for failing a write fast — the
 * request itself is still what decides; see lib/api.ts.
 */
export function subscribeConnectivity(
  onChange: (online: boolean) => void,
  options: { target?: Pick<Window, "addEventListener" | "removeEventListener">; initial?: boolean } = {},
): () => void {
  const target = options.target ?? (typeof window !== "undefined" ? window : undefined);
  if (!target) return () => {};

  const online = () => onChange(true);
  const offline = () => onChange(false);

  if (options.initial !== false) {
    // The current value first, so the banner is right on first paint.
    onChange(typeof navigator === "undefined" ? true : navigator.onLine !== false);
  }

  target.addEventListener("online", online);
  target.addEventListener("offline", offline);
  return () => {
    target.removeEventListener("online", online);
    target.removeEventListener("offline", offline);
  };
}

/** True when the browser says there is no connection. */
export function isOffline(): boolean {
  if (typeof navigator === "undefined") return false;
  // `onLine === false` is the only value that means "definitely offline".
  return navigator.onLine === false;
}
