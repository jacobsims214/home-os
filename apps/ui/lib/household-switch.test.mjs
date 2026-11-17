// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
//
// A household switch (#4574) is security-relevant: every step below is what
// stops the previous family's rows from staying on screen, so the ORDER is the
// behaviour under test. The real useHouseholdSwitch passes the real side
// effects; here they are recorded.
import { test } from "node:test";
import assert from "node:assert/strict";
import "./auth/node-ts-resolve.mjs";

// Loaded after the resolve hooks so the module's extensionless relative import
// of ./household-cookie resolves the way Next resolves it.
const {
  HOUSEHOLD_LIST_QUERY_KEY,
  applyHouseholdSwitch,
  householdSwitchTarget,
  isHouseholdListQueryKey,
} = await import("./household-switch.ts");

const A = "0b9d4f1e-6a3c-4b8e-9f52-1c7d2a5e8b10";

/** A stand-in for QueryClient that records the calls a switch makes. */
function fakeClient(log) {
  return {
    cancelQueries: async (filters) => {
      log.push(["cancel", filters]);
    },
    resetQueries: async (filters) => {
      log.push(["reset", filters]);
    },
    invalidateQueries: async (filters) => {
      log.push(["invalidate", filters]);
    },
  };
}

test("a switch remembers the household before it touches any cached data", async () => {
  const log = [];
  await applyHouseholdSwitch(
    {
      remember: (id) => log.push(["remember", id]),
      forgetHouseholdState: () => log.push(["forget"]),
      client: fakeClient(log),
    },
    A,
  );
  assert.deepEqual(log[0], ["remember", A], "the cookie write comes first: everything after is scoped to it");
  const names = log.map((e) => e[0]);
  assert.deepEqual(names, ["remember", "forget", "cancel", "reset", "invalidate"]);
  // Client state goes before the refetch, so a page can't rebuild from it.
  assert.ok(names.indexOf("forget") < names.indexOf("reset"));
});

test("in-flight requests are cancelled, not just invalidated", async () => {
  const log = [];
  await applyHouseholdSwitch(
    { remember: () => {}, forgetHouseholdState: () => {}, client: fakeClient(log) },
    A,
  );
  const cancelled = log.find((e) => e[0] === "cancel");
  assert.ok(cancelled, "cancelQueries ran: a response already on the wire belongs to the old household");
  // It is awaited BEFORE the reset, so no old response can land after the
  // cache was cleared.
  assert.ok(log.findIndex((e) => e[0] === "cancel") < log.findIndex((e) => e[0] === "reset"));
});

test("cached data is RESET (dropped) rather than invalidated (kept while refetching)", async () => {
  const log = [];
  await applyHouseholdSwitch(
    { remember: () => {}, forgetHouseholdState: () => {}, client: fakeClient(log) },
    A,
  );
  const reset = log.find((e) => e[0] === "reset");
  assert.ok(reset, "resetQueries ran: the old rows must leave the screen, not sit there while refetching");
  // invalidate touches ONE key only: the family list, whose `current` flag moved.
  const invalidations = log.filter((e) => e[0] === "invalidate");
  assert.equal(invalidations.length, 1);
  assert.deepEqual(invalidations[0][1], { queryKey: HOUSEHOLD_LIST_QUERY_KEY });
});

test("the family list survives a switch; every other query is reset", async () => {
  const log = [];
  await applyHouseholdSwitch(
    { remember: () => {}, forgetHouseholdState: () => {}, client: fakeClient(log) },
    A,
  );
  const resetPredicate = log.find((e) => e[0] === "reset")[1].predicate;

  // Kept: the list of the user's families with their roles (the nav reads it,
  // and the new role must apply in the same render).
  assert.equal(resetPredicate({ queryKey: ["me", "households"] }), false);
  // Dropped: everything scoped to the household we are leaving.
  for (const queryKey of [
    ["bills"],
    ["properties"],
    ["household", "me"],
    ["household", "me", "members"],
    ["notifications"],
    ["meals"],
    ["loans"],
    ["vendors"],
  ]) {
    assert.equal(resetPredicate({ queryKey }), true, `${queryKey.join("/")} is household-scoped`);
  }
});

test("isHouseholdListQueryKey matches the list and its subkeys only", () => {
  assert.equal(isHouseholdListQueryKey(["me", "households"]), true);
  assert.equal(isHouseholdListQueryKey(["me", "households", "x"]), true);
  assert.equal(isHouseholdListQueryKey(["me"]), false);
  assert.equal(isHouseholdListQueryKey(["households"]), false);
  assert.equal(isHouseholdListQueryKey([]), false);
});

test("a malformed id is refused before anything is written", async () => {
  const log = [];
  await assert.rejects(
    applyHouseholdSwitch(
      { remember: (id) => log.push(["remember", id]), forgetHouseholdState: () => log.push(["forget"]), client: fakeClient(log) },
      "family-2",
    ),
    /not a household id/,
  );
  assert.deepEqual(log, [], "nothing ran: a non-UUID would make every later request a 400");
});

test("the id is trimmed before it is remembered", async () => {
  const log = [];
  await applyHouseholdSwitch(
    { remember: (id) => log.push(["remember", id]), forgetHouseholdState: () => {}, client: fakeClient(log) },
    ` ${A} `,
  );
  assert.deepEqual(log[0], ["remember", A]);
});

test("switching off a record page goes to that section's list", () => {
  // Record pages name one household's row by id; after a switch that id is
  // another family's, so the page would 404 on a row that exists next door.
  assert.equal(householdSwitchTarget("/dashboard/bills/0b9d4f1e-6a3c-4b8e-9f52-1c7d2a5e8b10"), "/dashboard/bills");
  assert.equal(householdSwitchTarget("/dashboard/properties/p1"), "/dashboard/properties");
  assert.equal(householdSwitchTarget("/dashboard/recipes/abc"), "/dashboard/recipes");
  // Section pages and nested pages that are not records stay put.
  assert.equal(householdSwitchTarget("/dashboard"), null);
  assert.equal(householdSwitchTarget("/dashboard/bills"), null);
  assert.equal(householdSwitchTarget("/dashboard/meals/grocery"), null);
  assert.equal(householdSwitchTarget("/dashboard/search"), null);
  assert.equal(householdSwitchTarget("/dashboard/settings"), null);
  assert.equal(householdSwitchTarget(null), null);
});
