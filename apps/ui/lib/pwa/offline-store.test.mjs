// Run: npm test. The shared offline/update state (#4538): the bridge writes it,
// the shell's banners read it, and they are mounted in different places, so the
// store is the only thing keeping them in step.
import { test, beforeEach } from "node:test";
import assert from "node:assert/strict";
import {
  getOfflineState,
  resetOfflineState,
  setOfflineState,
  subscribeOfflineState,
} from "./offline-store.ts";

beforeEach(() => resetOfflineState());

test("starts online with no update, so nothing renders on a normal load", () => {
  assert.deepEqual(getOfflineState(), { online: true, update: null });
});

test("a change notifies subscribers", () => {
  let calls = 0;
  subscribeOfflineState(() => (calls += 1));
  setOfflineState({ online: false });
  assert.equal(calls, 1);
  assert.equal(getOfflineState().online, false);
});

test("setting the same value notifies nobody (no render loop)", () => {
  let calls = 0;
  subscribeOfflineState(() => (calls += 1));
  setOfflineState({ online: true });
  assert.equal(calls, 0, "online -> online is not a change");
});

test("a patch leaves the other field alone", () => {
  const waiting = { waiting: { postMessage: () => {} } };
  setOfflineState({ update: waiting });
  assert.equal(getOfflineState().online, true, "the update did not touch connectivity");
  setOfflineState({ online: false });
  assert.equal(getOfflineState().update, waiting, "going offline did not drop the waiting update");
});

test("unsubscribing stops the notifications", () => {
  let calls = 0;
  const unsubscribe = subscribeOfflineState(() => (calls += 1));
  unsubscribe();
  setOfflineState({ online: false });
  assert.equal(calls, 0);
});

test("several subscribers all hear about it (bridge and both banners)", () => {
  const seen = [];
  subscribeOfflineState(() => seen.push("a"));
  subscribeOfflineState(() => seen.push("b"));
  setOfflineState({ online: false });
  assert.deepEqual(seen, ["a", "b"]);
});
