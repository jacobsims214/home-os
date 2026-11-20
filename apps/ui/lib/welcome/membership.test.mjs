// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import { welcomeState } from "./membership.ts";

test("welcomeState: only an answered, empty list may show the wizard", () => {
  assert.equal(welcomeState({ isLoading: false, isError: false, families: 0 }), "newcomer");
  assert.equal(welcomeState({ isLoading: false, isError: false, families: 1 }), "member");
  assert.equal(welcomeState({ isLoading: false, isError: false, families: 3 }), "member");
});

test("welcomeState: not knowing is never read as having no family (#5409)", () => {
  // Still fetching: the page must not have said anything yet.
  assert.equal(welcomeState({ isLoading: true, isError: false, families: 0 }), "checking");
  assert.equal(welcomeState({ isLoading: true, isError: false, families: null }), "checking");
  // The read failed: unknown, not "start a family hub".
  assert.equal(welcomeState({ isLoading: false, isError: true, families: null }), "unknown");
  assert.equal(welcomeState({ isLoading: false, isError: true, families: 0 }), "unknown");
  // A payload that is not a list is unknown too.
  assert.equal(welcomeState({ isLoading: false, isError: false, families: null }), "unknown");
});
