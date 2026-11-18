// Run: npm test. Behaviour of the registration and the update flow (#4538),
// with stand-in ServiceWorker containers — no browser needed. Each case is a
// decision the app makes, so a regression shows up as a wrong offer (a reload
// nobody asked for, or a stale shell that never offers one).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  applyUpdate,
  hasWaitingUpdate,
  registerServiceWorker,
  subscribeConnectivity,
  SERVICE_WORKER_URL,
  SERVICE_WORKER_SCOPE,
} from "./service-worker.ts";
import { SKIP_WAITING_MESSAGE } from "./offline.js";

/** A waiting or installing worker that records what it was sent. */
function worker(state = "installed") {
  const sent = [];
  return { state, sent, postMessage: (message) => sent.push(message) };
}

/** A registration stand-in; `fire(type)` drives its updatefound listener. */
function registration({ waiting = null, installing = null, controller = true } = {}) {
  const listeners = {};
  const installingListeners = {};
  const reg = {
    waiting,
    installing,
    controller,
    addEventListener(type, fn) {
      (listeners[type] ||= []).push(fn);
    },
    fire(type) {
      for (const fn of listeners[type] || []) fn();
    },
    /** Simulates the installing worker reaching `installed`. */
    install(state, nextWaiting) {
      if (nextWaiting !== undefined) reg.waiting = nextWaiting;
      for (const fn of installingListeners.statechange || []) fn();
      void state;
    },
  };
  if (installing) {
    installing.addEventListener = (type, fn) => {
      (installingListeners[type] ||= []).push(fn);
    };
  }
  return reg;
}

/** A container stand-in that registers `reg` and records the calls. */
function container(reg = registration(), { controller = null } = {}) {
  const calls = [];
  const listeners = {};
  return {
    controller,
    calls,
    register: async (url, options) => {
      calls.push({ url, options });
      return reg;
    },
    addEventListener: (type, fn) => {
      (listeners[type] ||= []).push(fn);
    },
    fire(type) {
      for (const fn of listeners[type] || []) fn();
    },
    reg,
  };
}

// ── the update decision ────────────────────────────────────────────────────

test("a waiting worker under a controlled page is an update to offer", () => {
  assert.equal(hasWaitingUpdate({ waiting: worker() }, true), true);
});

test("a waiting worker with no controller is a first install, not an update", () => {
  // The first install reports `waiting` in some browsers; reloading there is a
  // pointless second round trip and would look like a glitch.
  assert.equal(hasWaitingUpdate({ waiting: worker() }, false), false);
  assert.equal(hasWaitingUpdate({ waiting: null }, true), false);
  assert.equal(hasWaitingUpdate(null, true), false);
});

test("applyUpdate posts SKIP_WAITING to the waiting worker, and reports it", () => {
  const waiting = worker();
  assert.equal(applyUpdate({ waiting }), true);
  assert.deepEqual(waiting.sent, [{ type: SKIP_WAITING_MESSAGE }]);
});

test("applyUpdate is a no-op with nothing waiting", () => {
  assert.equal(applyUpdate({ waiting: null }), false);
  assert.equal(applyUpdate(null), false);
});

// ── registration ───────────────────────────────────────────────────────────

test("registration uses /sw.js at the app's scope", async () => {
  const c = container();
  await registerServiceWorker({ container: c });
  assert.deepEqual(c.calls, [{ url: SERVICE_WORKER_URL, options: { scope: SERVICE_WORKER_SCOPE } }]);
});

test("no service workers in the browser: the app runs, registration is null", async () => {
  assert.equal(await registerServiceWorker({ container: null }), null);
});

test("a failed registration never throws (no offline shell, but the app works)", async () => {
  const c = {
    controller: null,
    register: async () => {
      throw new Error("SecurityError: insecure origin");
    },
    addEventListener: () => {},
  };
  assert.equal(await registerServiceWorker({ container: c }), null);
});

test("an update already waiting is reported at registration time", async () => {
  const waiting = worker();
  const reg = registration({ waiting });
  const c = container(reg, { controller: {} });
  const offered = [];
  await registerServiceWorker({ container: c, onUpdateReady: (r) => offered.push(r) });
  assert.equal(offered.length, 1, "the waiting update is offered without waiting for an event");
  assert.equal(offered[0], reg);
});

test("an update that finishes installing while the page is open is reported", async () => {
  const installing = worker("installing");
  const reg = registration({ installing });
  const c = container(reg, { controller: {} });
  const offered = [];
  await registerServiceWorker({ container: c, onUpdateReady: (r) => offered.push(r) });
  assert.equal(offered.length, 0, "nothing to offer yet");

  // updatefound fires, the new worker reaches installed and waits.
  reg.fire("updatefound");
  const newWaiting = worker();
  // `waiting` is only set on the registration once installed.
  reg.waiting = newWaiting;
  installing.state = "installed";
  reg.install("installed");

  assert.equal(offered.length, 1, "the new build is offered, not swapped in");
});

test("a first install (no controller) is never reported as an update", async () => {
  const installing = worker("installing");
  const reg = registration({ installing });
  const c = container(reg, { controller: null });
  const offered = [];
  await registerServiceWorker({ container: c, onUpdateReady: (r) => offered.push(r) });

  reg.fire("updatefound");
  reg.waiting = worker();
  installing.state = "installed";
  reg.install("installed");

  assert.deepEqual(offered, [], "the first install takes over on its own; nothing to reload");
});

test("controllerchange is forwarded once, so a reload cannot double-fire", async () => {
  const c = container();
  let changes = 0;
  await registerServiceWorker({ container: c, onControllerChange: () => (changes += 1) });
  c.fire("controllerchange");
  c.fire("controllerchange");
  assert.equal(changes, 1);
});

// ── connectivity ───────────────────────────────────────────────────────────

/** A Window stand-in for the connectivity subscription. */
function target(online) {
  const handlers = {};
  return {
    online,
    addEventListener: (type, fn) => {
      (handlers[type] ||= []).push(fn);
    },
    removeEventListener: (type, fn) => {
      handlers[type] = (handlers[type] || []).filter((f) => f !== fn);
    },
    fire(type) {
      for (const fn of handlers[type] || []) fn();
    },
    count: () => Object.values(handlers).reduce((n, list) => n + list.length, 0),
  };
}

test("subscribeConnectivity reports the current state, then every change", () => {
  const t = target(true);
  const seen = [];
  subscribeConnectivity((online) => seen.push(online), { target: t });
  assert.deepEqual(seen, [true], "the current value first, so the banner is right on first paint");
  t.fire("offline");
  t.fire("online");
  assert.deepEqual(seen, [true, false, true]);
});

test("unsubscribing removes both listeners", () => {
  const t = target(true);
  const unsubscribe = subscribeConnectivity(() => {}, { target: t });
  assert.equal(t.count(), 2);
  unsubscribe();
  assert.equal(t.count(), 0);
});

test("initial: false skips the first report", () => {
  const t = target(false);
  const seen = [];
  subscribeConnectivity((online) => seen.push(online), { target: t, initial: false });
  assert.deepEqual(seen, []);
});
