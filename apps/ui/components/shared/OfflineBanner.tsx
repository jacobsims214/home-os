"use client";

/**
 * "You're offline", and "Reload to update" (#4538).
 *
 * The app-side half of the offline shell. The worker half (worker/index.js)
 * serves public/offline.html when a NAVIGATION fails; these are what a page that
 * is already open shows when the connection drops mid-session — and what offers
 * the next build's reload.
 *
 * Deliberately NOT the spec's draft copy ("Showing what was saved at 7:42 AM.
 * Changes you make will sync when you're back online.") — there is no data cache
 * and no write queue (see lib/pwa/offline.js), so that sentence would be a lie,
 * and a dangerous one: it would tell a co-parent their note is safe when it isn't.
 *
 * TWO EXPORTS, ONE MOUNT POINT — DashboardShell:
 *
 *   - ServiceWorkerBridge: registers the worker once and publishes the state.
 *     Renders nothing; it exists for its effect.
 *   - OfflineBanner (default): the visible rows, at the top of the shell's
 *     content. The approved spec draws the offline state as a warn banner UNDER
 *     THE HEADER (KB design/app-shell.md, artifact #75 v1), in the page flow, so
 *     it sits inside the content rather than floating over it.
 *
 * WHY BOTH ARE IN THE SHELL, NOT THE ROOT LAYOUT. The auth pages must not
 * register the worker. Logout unregisters it and deletes every cache (#4648),
 * and a registration on /login would precache again straight after — leaving
 * caches behind for the next person on a shared family device, which is exactly
 * the leak #4648 closed. The offline shell belongs to the signed-in app.
 *
 * Both rows are role="status", so a screen reader announces the change without
 * stealing focus, and neither is a dialog: they never block the page.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { IconAlertTriangle, IconRefresh } from "@tabler/icons-react";
import { NAV_ICON_STROKE } from "@/components/shared/nav-config";
import {
  applyUpdate,
  registerServiceWorker,
  subscribeConnectivity,
} from "@/lib/pwa/service-worker";
import { setOfflineState, useOfflineState } from "@/lib/pwa/offline-store";
import classes from "./OfflineBanner.module.css";

/**
 * Registers the service worker once, for the signed-in app, and publishes a
 * waiting update and the connectivity state. Renders nothing: it exists for its
 * effect (mounted by DashboardShell).
 */
export function ServiceWorkerBridge() {
  useEffect(() => {
    let cancelled = false;
    // Only a reload the person asked for reloads the page. The FIRST install
    // also fires controllerchange, and reloading there is a pointless second
    // round trip.
    let applyRequested = false;

    void registerServiceWorker({
      onUpdateReady: (ready) => {
        if (!cancelled) setOfflineState({ update: ready });
      },
      onControllerChange: () => {
        if (cancelled || !applyRequested) return;
        window.location.reload();
      },
    });

    const unsubscribe = subscribeConnectivity((online) => {
      if (!cancelled) setOfflineState({ online });
    });

    // The Reload tap marks the reload as intentional before it posts the message.
    const markApply = () => {
      applyRequested = true;
    };
    applyHooks.add(markApply);

    return () => {
      cancelled = true;
      unsubscribe();
      applyHooks.delete(markApply);
    };
  }, []);

  return null;
}

/**
 * How UpdateBar tells the bridge that a reload is intentional. A module-level set
 * rather than a prop: the bridge and the bar are separate siblings under
 * DashboardShell, and the state between them lives in lib/pwa/offline-store.ts.
 */
const applyHooks = new Set<() => void>();

/** The offline banner. Renders nothing while online. */
function OfflineNotice() {
  const { online } = useOfflineState();
  if (online) return null;

  return (
    <div className={classes.banner} role="status" data-testid="offline-banner">
      <IconAlertTriangle
        size={18}
        stroke={NAV_ICON_STROKE}
        className={classes.bannerIcon}
        aria-hidden
      />
      <span>
        You&rsquo;re offline. Nothing new can be loaded or saved until you reconnect
        &mdash; anything you try to save now will not go through.
      </span>
    </div>
  );
}

/** The "reload to update" bar. Renders nothing until a new build is waiting. */
function UpdateBar() {
  const { update } = useOfflineState();
  const [applying, setApplying] = useState(false);

  const reload = useCallback(() => {
    if (!update || applying) return;
    setApplying(true);
    // forEach, not for..of: tsconfig targets es5 (see lib/pwa/offline-store.ts).
    applyHooks.forEach((hook) => hook());
    if (!applyUpdate(update)) {
      // No waiting worker any more (it was superseded): a plain reload still
      // gets the newest build.
      window.location.reload();
      return;
    }
    // The new worker takes over and controllerchange reloads. This timeout is
    // the fallback for a browser that does not fire it.
    window.setTimeout(() => window.location.reload(), 3000);
  }, [update, applying]);

  if (!update) return null;

  return (
    <div className={classes.update} role="status" data-testid="update-bar">
      <IconRefresh size={18} stroke={NAV_ICON_STROKE} aria-hidden />
      <span className={classes.updateText}>A new version of Home OS is ready.</span>
      <button type="button" className={classes.updateButton} onClick={reload} disabled={applying}>
        {applying ? "Reloading…" : "Reload to update"}
      </button>
    </div>
  );
}

/**
 * Both rows, in the order the shell shows them: the update offer first (it is
 * actionable), then the offline state. Mounted by DashboardShell inside its
 * content, so they sit under the header in the page flow, as the spec draws them.
 */
export default function OfflineBanner() {
  return (
    <>
      <UpdateBar />
      <OfflineNotice />
    </>
  );
}
