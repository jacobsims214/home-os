// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  NAV_SECTIONS,
  TAB_SECTIONS,
  activeSection,
  canSeeSection,
  pageTitle,
  sectionsForRole,
  sectionsInGroup,
} from "./nav-config.ts";

test("all nine sections, in the approved order and groups", () => {
  assert.deepEqual(
    NAV_SECTIONS.map((s) => s.id),
    ["today", "calendar", "lists", "meals", "documents", "people", "money", "guides", "settings"],
  );
  assert.deepEqual(sectionsInGroup("main").map((s) => s.label), ["Today", "Calendar", "Lists", "Meals"]);
  assert.deepEqual(sectionsInGroup("household").map((s) => s.label), ["Documents", "People & House", "Money", "Guides"]);
  assert.deepEqual(sectionsInGroup("you").map((s) => s.label), ["Settings"]);
  assert.deepEqual(TAB_SECTIONS.map((s) => s.id), ["today", "calendar", "lists", "meals"]);
});

test("activeSection: Today is the home page only, not every /dashboard route", () => {
  assert.equal(activeSection("/dashboard")?.id, "today");
  assert.equal(activeSection("/dashboard/")?.id, "today");
  assert.equal(activeSection("/dashboard/search"), null);
});

test("activeSection: today's pages map onto their sections", () => {
  assert.equal(activeSection("/dashboard/calendar")?.id, "calendar");
  assert.equal(activeSection("/dashboard/vehicles/123")?.id, "people");
  assert.equal(activeSection("/dashboard/properties")?.id, "people");
  assert.equal(activeSection("/dashboard/members")?.id, "people");
  assert.equal(activeSection("/dashboard/loans/9")?.id, "money");
  assert.equal(activeSection("/dashboard/bills")?.id, "money");
  assert.equal(activeSection("/dashboard/admin")?.id, "settings");
  // A prefix must end at a path segment.
  assert.equal(activeSection("/dashboard/billsx"), null);
});

test("pageTitle", () => {
  assert.equal(pageTitle("/dashboard"), "Today");
  assert.equal(pageTitle("/dashboard/calendar"), "Calendar");
  assert.equal(pageTitle("/dashboard/vehicles/abc"), "Vehicles");
  assert.equal(pageTitle("/dashboard/settings"), "Settings");
  assert.equal(pageTitle("/somewhere"), "Home OS");
});

test("every section has an icon and a /dashboard href", () => {
  for (const s of NAV_SECTIONS) {
    assert.ok(s.icon, s.id);
    assert.match(s.href, /^\/dashboard(\/|$)/, s.id);
  }
});

// Mirrors the API role policy (KB decisions/2026-11-15-role-policy.md).
test("sectionsForRole follows the household role policy", () => {
  const ids = (role) => sectionsForRole(role).map((s) => s.id);
  const all = NAV_SECTIONS.map((s) => s.id);
  for (const role of ["owner", "family_manager", "family_member", "viewer"]) {
    assert.deepEqual(ids(role), all, role);
  }
  // No money, documents (files) for a housesitter.
  assert.deepEqual(ids("housesitter"), ["today", "calendar", "lists", "meals", "people", "guides", "settings"]);
  // A vendor reads properties/assets/maintenance only (People & House).
  assert.deepEqual(ids("vendor"), ["today", "people", "settings"]);
});

test("sectionsForRole: unknown role yet shows everything; an unrecognised role fails closed", () => {
  assert.equal(sectionsForRole(null).length, NAV_SECTIONS.length);
  assert.equal(sectionsForRole(undefined).length, NAV_SECTIONS.length);
  assert.deepEqual(sectionsForRole("superuser").map((s) => s.id), ["today", "people", "settings"]);
  const money = NAV_SECTIONS.find((s) => s.id === "money");
  assert.equal(canSeeSection(money, "viewer"), true);
  assert.equal(canSeeSection(money, "housesitter"), false);
});
