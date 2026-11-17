/**
 * Client-side purge of everything a session left in the browser (#4648).
 *
 * WHY THIS IS NOT THE SERVICE WORKER'S JOB. A logout has to remove what the
 * browser stored for the previous person even when the worker in control is an
 * *old* build (before #4648 the worker cached every `/bff/*` GET) or is not
 * running at all. Cache Storage and Web Storage are both reachable from the
 * page, so the page does the work — see the layers, in the order they run:
 *
 *   1. Prevention: public/sw.js never caches `/bff/*` or `/api/*`
 *      (lib/pwa/caching-policy.js), so nothing new leaks.
 *   2. Explicit logout: components/shared/Sidebar.tsx holds the native POST
 *      long enough to run teardownSession() first, then submits the form.
 *   3. The 303 from /api/auth/logout also carries
 *      `Clear-Site-Data: "cache", "storage"`, which makes the *browser* wipe
 *      the origin's caches, DOM storage, IndexedDB and service worker
 *      registrations without trusting any of our JS.
 *   4. Session expiry: every way a session can end (a /bff 401, the page
 *      guard, an already-expired cookie) lands on /login, and the login page
 *      sweeps once on mount before it hands off to Dex. So an expired session
 *      can never serve cached authenticated data, and this also clears caches
 *      left behind by the pre-#4648 build.
 *   5. The service worker deletes any cache that is not in its allowlist when
 *      it activates (worker/index.js), so a device that never signs out is
 *      cleaned up by the upgrade alone.
 *
 * WHAT SURVIVES ON PURPOSE: `home-os-color-scheme` (a per-device display
 * preference, not user data — the *server* never sees it) is left alone by
 * teardownSession(). Clear-Site-Data "storage" cannot be selective and does
 * clear it; that is the browser's call, not ours.
 *
 * No imports on purpose: this runs in the browser, but the rules are unit
 * tested under `node --test` with the storage APIs injected
 * (lib/session-teardown.test.mjs), and it must stay loadable there.
 */

/** zustand `persist` keys that hold something about the signed-in person. */
export const PERSISTED_SESSION_KEYS = [
  // stores/auth.ts — the user profile (id, email, name, avatar_url).
  "home-os-auth",
  // stores/recent.ts — recently viewed entity titles, i.e. household data.
  "home-os-recent",
  // stores/property.ts — the property the person was last looking at.
  "home-os-property",
] as const;

/** Per-device display preferences: deliberately kept across a logout. */
export const DEVICE_PREFERENCE_KEYS = ["home-os-color-scheme"] as const;

/** How long a single purge step may take before the caller stops waiting. */
const DEFAULT_TIMEOUT_MS = 2000;

/** The browser globals this needs, injected so `node --test` can stand in. */
export interface TeardownGlobals {
  caches?: CacheStorage;
  localStorage?: Storage;
  sessionStorage?: Storage;
  indexedDB?: IDBFactory;
  serviceWorker?: ServiceWorkerContainer;
}

export interface TeardownReport {
  /** Cache names deleted (includes legacy caches from earlier builds). */
  caches: string[];
  /** IndexedDB database names deleted. */
  databases: string[];
  /** localStorage keys removed. */
  storageKeys: string[];
  /** sessionStorage cleared (it is per-tab and wholly session-scoped). */
  clearedSessionStorage: boolean;
  /** Service worker registrations unregistered. */
  registrations: number;
  /** Steps that failed; a failure never stops the others. */
  errors: string[];
}

function browserGlobals(): TeardownGlobals {
  if (typeof window === "undefined") return {};
  const g = globalThis as unknown as TeardownGlobals;
  const out: TeardownGlobals = {};
  // Each is probed separately: a browser without IndexedDB must still get its
  // caches purged.
  try {
    if (typeof g.caches !== "undefined" && typeof g.caches.keys === "function") out.caches = g.caches;
  } catch {
    // Accessing a blocked storage API can throw; treat it as absent.
  }
  try {
    if (typeof g.localStorage !== "undefined" && g.localStorage) out.localStorage = g.localStorage;
  } catch {
    // Safari in private mode throws on access.
  }
  try {
    if (typeof g.sessionStorage !== "undefined" && g.sessionStorage) out.sessionStorage = g.sessionStorage;
  } catch {
    // As above.
  }
  try {
    if (typeof g.indexedDB !== "undefined" && g.indexedDB) out.indexedDB = g.indexedDB;
  } catch {
    // As above.
  }
  try {
    if (typeof navigator !== "undefined" && navigator.serviceWorker) out.serviceWorker = navigator.serviceWorker;
  } catch {
    // As above.
  }
  return out;
}

/** Bounds one step so a wedged API can never hold up a logout. */
async function bounded<T>(work: Promise<T>, label: string, timeoutMs: number, errors: string[]): Promise<T | undefined> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      work,
      new Promise<undefined>((resolve) => {
        timer = setTimeout(() => {
          errors.push(`${label}: timed out after ${timeoutMs}ms`);
          resolve(undefined);
        }, timeoutMs);
      }),
    ]);
  } catch (err) {
    errors.push(`${label}: ${err instanceof Error ? err.message : String(err)}`);
    return undefined;
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

/**
 * Deletes every Cache Storage cache in this origin and returns their names.
 *
 * Every cache, not just the ones we created: before #4648 the worker wrote
 * `/bff/*` responses into caches named "others" and "apis" (next-pwa's
 * defaults), and those are exactly the entries that must not survive a logout
 * on a shared device. Cache Storage is per-origin and this origin serves only
 * the app, so "all of them" is the right scope — see the KB doc.
 */
export async function deleteAppCaches(caches: CacheStorage | undefined): Promise<string[]> {
  if (!caches) return [];
  const names = await caches.keys();
  const deleted: string[] = [];
  await Promise.all(
    names.map(async (name) => {
      // A single failing delete must not spare the others.
      if (await caches.delete(name)) deleted.push(name);
    }),
  );
  return deleted;
}

/**
 * Deletes every IndexedDB database in this origin. The app stores nothing
 * there today (no `indexedDB` call outside this file); this guarantees a
 * future one cannot quietly outlive a logout.
 */
export async function deleteAppDatabases(factory: IDBFactory | undefined): Promise<string[]> {
  if (!factory || typeof factory.databases !== "function") return [];
  const databases = await factory.databases();
  const deleted: string[] = [];
  const names = databases.map((db) => db.name).filter((name): name is string => !!name);
  await Promise.all(
    names.map(
      (name) =>
        new Promise<void>((resolve) => {
          const request = factory.deleteDatabase(name);
          // deleteDatabase blocks on open connections; resolve either way so
          // one stuck database cannot stall the purge.
          request.onsuccess = () => resolve();
          request.onerror = () => resolve();
          request.onblocked = () => resolve();
        }),
    ).map((p, i) =>
      p.then(() => {
        deleted.push(names[i]!);
      }),
    ),
  );
  return deleted;
}

/** Removes the persisted zustand session stores; returns the keys removed. */
export function clearPersistedSession(storage: Storage | undefined): string[] {
  if (!storage) return [];
  const removed: string[] = [];
  for (const key of PERSISTED_SESSION_KEYS) {
    try {
      if (storage.getItem(key) !== null) {
        storage.removeItem(key);
        removed.push(key);
      }
    } catch {
      // A throwing removeItem is reported by the caller not seeing the key.
    }
  }
  return removed;
}

/** Empties sessionStorage: everything in it belongs to the session. */
export function clearSessionStorage(storage: Storage | undefined): boolean {
  if (!storage) return false;
  try {
    storage.clear();
    return true;
  } catch {
    return false;
  }
}

/** Unregisters this origin's service workers; returns how many. */
export async function unregisterServiceWorkers(container: ServiceWorkerContainer | undefined): Promise<number> {
  if (!container || typeof container.getRegistrations !== "function") return 0;
  const registrations = await container.getRegistrations();
  const results = await Promise.all(
    registrations.map(async (registration) => (await registration.unregister()) === true),
  );
  return results.filter(Boolean).length;
}

/**
 * Purges everything a session left in the browser. Never throws: each step is
 * bounded and its failure is reported in `errors`, because a logout must
 * finish even when the browser is uncooperative.
 *
 * `unregister` is off by default: deleting the caches is what removes the data,
 * and the worker must stay registered for the login page (a re-register on
 * every visit is churn). It is on for the explicit logout, where the next page
 * load registers again anyway.
 */
export async function teardownSession(
  options: { globals?: TeardownGlobals; timeoutMs?: number; unregister?: boolean } = {},
): Promise<TeardownReport> {
  const report: TeardownReport = {
    caches: [],
    databases: [],
    storageKeys: [],
    clearedSessionStorage: false,
    registrations: 0,
    errors: [],
  };
  const globals = options.globals ?? browserGlobals();
  const timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;

  // Synchronous steps first: once they return, the data is gone even if the
  // caller navigates immediately.
  report.storageKeys = clearPersistedSession(globals.localStorage);
  report.clearedSessionStorage = clearSessionStorage(globals.sessionStorage);

  const [caches, databases, registrations] = await Promise.all([
    bounded(deleteAppCaches(globals.caches), "cache delete", timeoutMs, report.errors),
    bounded(deleteAppDatabases(globals.indexedDB), "indexeddb delete", timeoutMs, report.errors),
    options.unregister
      ? bounded(unregisterServiceWorkers(globals.serviceWorker), "service worker unregister", timeoutMs, report.errors)
      : Promise.resolve(0),
  ]);
  report.caches = caches ?? [];
  report.databases = databases ?? [];
  report.registrations = registrations ?? 0;
  return report;
}

/** True when a cache entry is an authenticated API response (/bff or /api). */
function isApiRequestUrl(href: string): boolean {
  try {
    const path = new URL(href, "https://home.invalid").pathname;
    return path === "/bff" || path.startsWith("/bff/") || path === "/api" || path.startsWith("/api/");
  } catch {
    return false;
  }
}

/**
 * Whether this browser is holding anything a previous session left behind:
 * cached `/bff`/`/api` responses, a persisted zustand session store, an
 * IndexedDB database, or anything in sessionStorage.
 *
 * The login page uses this to sweep only when there is something to sweep, so
 * a first visit on a new device does not pay for it. `/login` is where every
 * session end lands — the middleware's 307 with no cookie, the dashboard
 * layout's redirect, a `/bff` 401 (lib/api.ts), and the logout route's 303 —
 * which makes it the one place that catches an *expired* session as well as an
 * explicit logout.
 */
export async function hasSessionRemnants(
  options: { globals?: TeardownGlobals; timeoutMs?: number } = {},
): Promise<boolean> {
  const globals = options.globals ?? browserGlobals();
  const timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  const errors: string[] = [];

  if (globals.localStorage) {
    for (const key of PERSISTED_SESSION_KEYS) {
      try {
        if (globals.localStorage.getItem(key) !== null) return true;
      } catch {
        // Blocked storage: nothing readable, nothing to leak.
      }
    }
  }
  if (globals.sessionStorage) {
    try {
      if (globals.sessionStorage.length > 0) return true;
    } catch {
      // As above.
    }
  }

  const cachedApi = bounded(
    (async () => {
      if (!globals.caches) return false;
      const names = await globals.caches.keys();
      for (const name of names) {
        const cache = await globals.caches.open(name);
        const requests = await cache.keys();
        if (requests.some((request) => isApiRequestUrl(request.url))) return true;
      }
      return false;
    })(),
    "cache inspect",
    timeoutMs,
    errors,
  );
  const hasDatabases = bounded(
    (async () => {
      if (!globals.indexedDB || typeof globals.indexedDB.databases !== "function") return false;
      const databases = await globals.indexedDB.databases();
      return databases.length > 0;
    })(),
    "indexeddb inspect",
    timeoutMs,
    errors,
  );
  const [api, dbs] = await Promise.all([cachedApi, hasDatabases]);
  return api === true || dbs === true;
}
