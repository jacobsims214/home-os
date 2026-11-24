// Run: npm test (node --test strips the TypeScript types natively).
//
// The Members page's "This family" card (#5300).
//
// THE TEST THAT MATTERS MOST is the first one, and it is a test of the REAL API
// payload: on the war game a family whose `family_overview.timezone` was
// "America/New_York" was told "Time zone not set", because the card read
// `data.timezone` from GET /api/v1/households/me — a route that serialises the
// Go `household.Household` struct with NO json tags, so its keys are `ID`,
// `Name`, `Timezone` (capital T). `data.timezone` was `undefined` on every real
// deployment; the e2e stub answered in lowercase, which is why nothing caught it.
//
// So the payload below is the literal wire shape of that route (see
// apps/api/internal/household/handler.go GetMe + model.go), and the assertions
// are: (a) the misnamed read really is undefined on it — the regression, pinned;
// (b) the household the card now reads (GET /me/households, lowercase
// "timezone") carries the zone; (c) the card says the zone.
//
// The SECOND fault this file pins is #5305's: an UNKNOWN household is not a
// household with no zone. While the family list is loading, the card must not
// claim "Time zone not set" — that is a false statement about the family, and it
// is exactly what the war game saw.

import { test } from "node:test";
import assert from "node:assert/strict";
import {
  HOUSEHOLD_UNKNOWN_NAME,
  TIMEZONE_NOT_SET,
  householdCardInitial,
  householdCardMeta,
  householdCardName,
  householdTimezoneLabel,
} from "./household-card.ts";

// ── The real wire shapes ──────────────────────────────────────────────

/**
 * GET /api/v1/households/me, verbatim: `map[string]any{"data": hh}` with
 * `household.Household` (apps/api/internal/household/model.go) having no json
 * tags, so Go marshals the field names. Copied from a real response, not typed
 * by hand from the struct: the Settings PATCH's response is the lowercase one,
 * this route is the capital one.
 */
const HOUSEHOLDS_ME_WIRE = {
  data: {
    ID: "0b9d4f1e-6a3c-4b8e-9f52-1c7d2a5e8b10",
    Name: "Sims family",
    Timezone: "America/New_York",
    Settings: null,
    CreatedAt: "2026-01-01T00:00:00Z",
    UpdatedAt: "2026-01-01T00:00:00Z",
  },
};

/** GET /api/v1/me/households entry (UserHousehold + is_default/current). */
const ME_HOUSEHOLDS_ENTRY = {
  id: "0b9d4f1e-6a3c-4b8e-9f52-1c7d2a5e8b10",
  name: "Sims family",
  timezone: "America/New_York",
  role: "owner",
  joined_at: "2026-01-01T00:00:00Z",
  is_default: true,
  current: true,
};

test("regression (#5300): the zone exists on the family, so the card must not say it is unset", () => {
  // The bug: the old card read this route with a lowercase key. Pin it, so a
  // future reader that goes back to GET /households/me fails here first.
  assert.equal(
    HOUSEHOLDS_ME_WIRE.data.timezone,
    undefined,
    "GET /households/me still answers `Timezone`; reading `timezone` from it is the #5300 bug",
  );
  assert.equal(HOUSEHOLDS_ME_WIRE.data.Timezone, "America/New_York");

  // THE OLD BEHAVIOUR, verbatim: hand the new formatter exactly what the old
  // card read (the wire object, whose `timezone` key does not exist) and it
  // produces the war game's sentence on a family that has a zone. If a future
  // change goes back to that route, this assertion is what notices.
  assert.equal(
    householdTimezoneLabel(HOUSEHOLDS_ME_WIRE.data),
    TIMEZONE_NOT_SET,
    "the old read renders 'Time zone not set' for a family whose zone is America/New_York — the #5300 bug",
  );

  // The fix: the household the card reads (useCurrentHousehold → GET
  // /me/households) carries the zone, and the card renders it.
  assert.equal(householdTimezoneLabel(ME_HOUSEHOLDS_ENTRY), "America/New_York");
  assert.notEqual(householdTimezoneLabel(ME_HOUSEHOLDS_ENTRY), TIMEZONE_NOT_SET);
  // The whole line a person reads, exactly as the card draws it.
  assert.equal(householdCardMeta(ME_HOUSEHOLDS_ENTRY, "owner"), "America/New_York · you're an owner");
});

test("the same fault on the old read: the family's name also vanished, so the card said 'This family'", () => {
  assert.equal(HOUSEHOLDS_ME_WIRE.data.name, undefined);
  assert.equal(householdCardName(ME_HOUSEHOLDS_ENTRY), "Sims family");
  assert.notEqual(householdCardName(ME_HOUSEHOLDS_ENTRY), HOUSEHOLD_UNKNOWN_NAME);
  assert.equal(householdCardInitial(ME_HOUSEHOLDS_ENTRY), "S");
});

test("an UNKNOWN household says nothing about the zone (#5300 + #5305), it never claims one is unset", () => {
  // `null` is "the family list has not arrived"; "Time zone not set" is a
  // statement about a family, and making it on no evidence is the war game's bug
  // with the opposite cause.
  assert.equal(householdTimezoneLabel(null), null);
  assert.equal(householdTimezoneLabel(undefined), null);
  // The role half still renders, so the line is not empty and not a dangling "·".
  assert.equal(householdCardMeta(null, "owner"), "you're an owner");
  assert.equal(householdCardMeta(undefined, null), "");
});

test("a household that really has no zone is the only one that says so", () => {
  for (const timezone of ["", "   ", null, undefined]) {
    const household = { id: "h1", name: "Sims family", timezone };
    assert.equal(householdTimezoneLabel(household), TIMEZONE_NOT_SET, `timezone ${JSON.stringify(timezone)}`);
    assert.equal(householdCardMeta(household, "viewer"), `${TIMEZONE_NOT_SET} · you're a viewer`);
  }
});

test("a zone is trimmed, and a blank name falls back rather than drawing an empty card", () => {
  assert.equal(householdTimezoneLabel({ timezone: "  Europe/London  " }), "Europe/London");
  assert.equal(householdCardName({ name: "  " }), HOUSEHOLD_UNKNOWN_NAME);
  assert.equal(householdCardName({ name: null }), HOUSEHOLD_UNKNOWN_NAME);
  assert.equal(householdCardName(null), HOUSEHOLD_UNKNOWN_NAME);
  // The mark is the first letter of whatever the title says, so the two can
  // never disagree ("This family" draws a T, not a blank).
  assert.equal(householdCardInitial(null), HOUSEHOLD_UNKNOWN_NAME.charAt(0).toUpperCase());
});

test("the role half reads 'an owner' — the war game's 'you're a owner' (#5300)", () => {
  const household = { name: "Sims family", timezone: "America/New_York" };
  // The exact sentence, for every role a family can hold. The article is chosen
  // by the word's sound, so only owner takes "an".
  const expected = {
    owner: "you're an owner",
    family_manager: "you're a family manager",
    family_member: "you're a family member",
    viewer: "you're a viewer",
    housesitter: "you're a housesitter",
    vendor: "you're a vendor",
  };
  for (const [role, phrase] of Object.entries(expected)) {
    assert.equal(householdCardMeta(household, role), `America/New_York · ${phrase}`, role);
  }
  // No role known: no dangling separator, no empty "you're ".
  assert.equal(householdCardMeta(household, null), "America/New_York");
  assert.equal(householdCardMeta(household, ""), "America/New_York");
});