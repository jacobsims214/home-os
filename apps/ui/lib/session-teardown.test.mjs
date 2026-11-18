// Run: npm test. What a logout clears (#4648): Cache Storage, the persisted
// zustand session stores, sessionStorage and IndexedDB. The storage APIs are
// stand-ins — Cache Storage and IndexedDB are not in Node — so what is asserted
// here is that every API is asked to drop everything, including the caches and
// databases this app never created itself (the pre-#4648 worker's "others" and
// "apis" caches hold authenticated /bff responses and must go too).
import { test } from "node:test";
import assert from "node:assert/strict";
import "./auth/node-ts-resolve.mjs";

const {
  PERSISTED_SESSION_KEYS,
  DEVICE_PREFERENCE_KEYS,
  teardownSession,
  hasSessionRemnants,
  deleteAppCaches,
  clearPersistedSession,
  clearSessionStorage,
  unregisterServiceWorkers,
} = await import("./session-teardown.ts");

// ── stand-ins ───────────────────────────────────────────────────────────────

/** A Cache Storage holding `entries`: { cacheName: [url, ...] }. */
function fakeCaches(entries = {}) {
  const store = new Map(Object.entries(entries).map(([name, urls]) => [name, new Set(urls)]));
  const calls = { keys: 0, open: [], delete: [] };
  return {
    store,
    calls,
    keys: async () => {
      calls.keys += 1;
      return [...store.keys()];
    },
    open: async (name) => {
      calls.open.push(name);
      if (!store.has(name)) store.set(name, new Set());
      const requests = store.get(name);
      return {
        keys: async () => [...requests].map((url) => ({ url })),
        match: async () => undefined,
        put: async () => undefined,
      };
    },
    delete: async (name) => {
      calls.delete.push(name);
      return store.delete(name);
    },
  };
}

/** A Storage (localStorage/sessionStorage) with a Map behind it. */
function fakeStorage(initial = {}) {
  const map = new Map(Object.entries(initial));
  const calls = { remove: [], clear: 0 };
  return {
    map,
    calls,
    get length() {
      return map.size;
    },
    key: (i) => [...map.keys()][i] ?? null,
    getItem: (k) => (map.has(k) ? map.get(k) : null),
    setItem: (k, v) => void map.set(k, String(v)),
    removeItem: (k) => {
      calls.remove.push(k);
      map.delete(k);
    },
    clear: () => {
      calls.clear += 1;
      map.clear();
    },
  };
}

/** An IDBFactory with `names` databases. */
function fakeIndexedDB(names = []) {
  const remaining = new Set(names);
  const calls = { databases: 0, deleted: [] };
  return {
    remaining,
    calls,
    databases: async () => {
      calls.databases += 1;
      return [...remaining].map((name) => ({ name }));
    },
    deleteDatabase: (name) => {
      calls.deleted.push(name);
      remaining.delete(name);
      const request = {};
      // Resolve before the caller attaches handlers: fine for a stub.
      queueMicrotask(() => request.onsuccess && request.onsuccess());
      return request;
    },
  };
}

/** A ServiceWorkerContainer with `n` registrations. */
function fakeServiceWorker(n) {
  const unregistered = [];
  return {
    unregistered,
    getRegistrations: async () =>
      Array.from({ length: n }, (_, i) => ({
        scope: `https://home.example/scope-${i}/`,
        unregister: async () => {
          unregistered.push(i);
          return true;
        },
      })),
  };
}

const SIGNED_IN_LOCAL = {
  // stores/auth.ts wrote this on the previous person's device.
  "home-os-auth": JSON.stringify({ state: { user: { id: "u1", email: "mum@home.example", name: "Mum" } }, version: 1 }),
  "home-os-recent": JSON.stringify({ state: { items: [{ title: "Dentist bill" }] }, version: 0 }),
  "home-os-property": JSON.stringify({ state: { activePropertyId: "p1" }, version: 0 }),
  // A display preference: kept.
  "home-os-color-scheme": "dark",
  // Unrelated keys must not be touched either.
  "some-other-app": "keep me",
};

test("teardownSession deletes every cache, including next-pwa's pre-#4648 caches", async () => {
  const caches = fakeCaches({
    // The pre-#4648 worker's caches: these are where /bff responses leaked.
    others: ["https://home.example/bff/members", "https://home.example/dashboard"],
    apis: ["https://home.example/bff/bills?month=2026-11"],
    "start-url": ["https://home.example/"],
    "home-os-static": ["https://home.example/_next/static/chunks/a.js"],
  });
  const report = await teardownSession({
    globals: { caches, localStorage: fakeStorage(SIGNED_IN_LOCAL), sessionStorage: fakeStorage() },
  });
  // Every cache is gone, not just the ones this build created.
  assert.deepEqual(report.caches.sort(), ["apis", "home-os-static", "others", "start-url"]);
  assert.equal(caches.store.size, 0);
  assert.deepEqual(caches.calls.delete.sort(), ["apis", "home-os-static", "others", "start-url"]);
});

test("teardownSession clears the persisted user and the other session stores", async () => {
  const localStorage = fakeStorage(SIGNED_IN_LOCAL);
  const sessionStorage = fakeStorage({ "next-route-cache": "1" });
  const report = await teardownSession({ globals: { caches: fakeCaches(), localStorage, sessionStorage } });

  for (const key of PERSISTED_SESSION_KEYS) {
    assert.equal(localStorage.map.has(key), false, `${key} is gone from localStorage`);
    assert.ok(report.storageKeys.includes(key), `${key} reported`);
  }
  // NOT home-os-auth's contents left as `{"user":null}`: the key is removed.
  assert.equal(localStorage.getItem("home-os-auth"), null);
  // A per-device display preference and unrelated keys survive.
  assert.deepEqual([...localStorage.map.keys()], ["home-os-color-scheme", "some-other-app"]);
  // sessionStorage is wholly session-scoped, so it is emptied.
  assert.equal(sessionStorage.map.size, 0);
  assert.equal(report.clearedSessionStorage, true);
});

test("teardownSession deletes IndexedDB databases too", async () => {
  const indexedDB = fakeIndexedDB(["home-os-files", "some-sdk-cache"]);
  const report = await teardownSession({ globals: { caches: fakeCaches(), indexedDB } });
  assert.deepEqual(report.databases.sort(), ["home-os-files", "some-sdk-cache"]);
  assert.equal(indexedDB.remaining.size, 0);
});

test("teardownSession leaves the worker registered by default and unregisters on request", async () => {
  const serviceWorker = fakeServiceWorker(2);
  const kept = await teardownSession({ globals: { caches: fakeCaches(), serviceWorker } });
  assert.equal(kept.registrations, 0);
  assert.deepEqual(serviceWorker.unregistered, []);

  const removed = await teardownSession({ globals: { caches: fakeCaches(), serviceWorker }, unregister: true });
  assert.equal(removed.registrations, 2);
  assert.deepEqual(serviceWorker.unregistered, [0, 1]);
});

test("teardownSession never throws and reports a failing step, then does the rest", async () => {
  const caches = fakeCaches({ others: ["https://home.example/bff/members"] });
  caches.keys = async () => {
    throw new Error("SecurityError: storage blocked");
  };
  const localStorage = fakeStorage(SIGNED_IN_LOCAL);
  const report = await teardownSession({ globals: { caches, localStorage, sessionStorage: fakeStorage() } });
  assert.equal(report.errors.length, 1);
  assert.match(report.errors[0], /cache delete.*SecurityError/);
  // The storage purge still happened.
  assert.equal(localStorage.map.has("home-os-auth"), false);
});

test("teardownSession is bounded: a wedged cache API cannot hang a logout", async () => {
  const caches = fakeCaches();
  // Never resolves — the browser equivalent of a stuck Cache Storage.
  caches.keys = () => new Promise(() => {});
  const started = Date.now();
  const report = await teardownSession({
    globals: { caches, localStorage: fakeStorage(SIGNED_IN_LOCAL) },
    timeoutMs: 50,
  });
  assert.ok(Date.now() - started < 2000, "returned without waiting for the wedged call");
  assert.match(report.errors.join(" "), /cache delete: timed out/);
  assert.equal(report.storageKeys.includes("home-os-auth"), true);
});

test("a browser without Cache Storage or IndexedDB still logs out", async () => {
  // Older Safari private mode: accessing the APIs throws.
  const report = await teardownSession({ globals: {} });
  assert.deepEqual(report.caches, []);
  assert.deepEqual(report.databases, []);
  assert.deepEqual(report.errors, []);
});

// ── the pieces teardownSession is built from ───────────────────────────────

test("deleteAppCaches deletes every cache name it finds", async () => {
  const caches = fakeCaches({ others: [], apis: [], "home-os-static": [] });
  assert.deepEqual((await deleteAppCaches(caches)).sort(), ["apis", "home-os-static", "others"]);
  assert.equal(caches.store.size, 0);
});

test("clearPersistedSession reports only keys that were actually there", () => {
  const storage = fakeStorage({ "home-os-auth": "x" });
  assert.deepEqual(clearPersistedSession(storage), ["home-os-auth"]);
  assert.deepEqual(clearPersistedSession(storage), []);
});

test("clearSessionStorage tolerates a throwing storage", () => {
  const storage = fakeStorage();
  storage.clear = () => {
    throw new Error("blocked");
  };
  assert.equal(clearSessionStorage(storage), false);
});

test("unregisterServiceWorkers counts only real unregistrations", async () => {
  const container = {
    getRegistrations: async () => [{ unregister: async () => true }, { unregister: async () => false }],
  };
  assert.equal(await unregisterServiceWorkers(container), 1);
});

// ── the session-expiry sweep ───────────────────────────────────────────────

test("hasSessionRemnants: false on a fresh device", async () => {
  assert.equal(
    await hasSessionRemnants({
      globals: { caches: fakeCaches(), localStorage: fakeStorage({ "home-os-color-scheme": "dark" }), sessionStorage: fakeStorage(), indexedDB: fakeIndexedDB([]) },
    }),
    false,
  );
});

test("hasSessionRemnants: true when a cached /bff response is still there", async () => {
  // Exactly the pre-#4648 state: the worker's "others" cache holds /bff data.
  assert.equal(
    await hasSessionRemnants({
      globals: { caches: fakeCaches({ others: ["https://home.example/bff/members"] }) },
    }),
    true,
  );
  // A static-only cache is not a remnant.
  assert.equal(
    await hasSessionRemnants({
      globals: { caches: fakeCaches({ "home-os-static": ["https://home.example/_next/static/chunks/a.js"] }) },
    }),
    false,
  );
});

test("hasSessionRemnants: true for a persisted profile, sessionStorage or IndexedDB", async () => {
  assert.equal(await hasSessionRemnants({ globals: { localStorage: fakeStorage({ "home-os-auth": "{}" }) } }), true);
  assert.equal(await hasSessionRemnants({ globals: { sessionStorage: fakeStorage({ k: "v" }) } }), true);
  assert.equal(await hasSessionRemnants({ globals: { indexedDB: fakeIndexedDB(["home-os-files"]) } }), true);
});

test("hasSessionRemnants tolerates blocked storage", async () => {
  assert.equal(await hasSessionRemnants({ globals: {} }), false);
});

test("DEVICE_PREFERENCE_KEYS is not cleared by the purge", async () => {
  const localStorage = fakeStorage({ "home-os-color-scheme": "light" });
  await teardownSession({ globals: { caches: fakeCaches(), localStorage } });
  for (const key of DEVICE_PREFERENCE_KEYS) assert.equal(localStorage.getItem(key), "light");
});
