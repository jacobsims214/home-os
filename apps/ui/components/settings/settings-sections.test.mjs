// Run: npm test (node --test strips the TypeScript types natively).
//
// The Settings section table and its role gate (#4560). Every manager-only
// capability named here is one the API refuses for the other roles
// (apps/api/internal/auth/roles.go: PATCH /households/{id} is managers only,
// ResSecrets is family, GET /households/me/members is sitters).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  SETTINGS_SECTIONS,
  canManageHousehold,
  canSeeSettingsSection,
  findSettingsSection,
  firstSectionHref,
  roleLabel,
  settingsSectionsForRole,
} from "./settings-sections.ts";

const ids = (role) => settingsSectionsForRole(role).map((s) => s.id);

test("the five sections, in the order the settings mockup lists them", () => {
  assert.deepEqual(
    SETTINGS_SECTIONS.map((s) => s.id),
    ["household", "members", "credentials", "phone-calendar", "account"],
  );
  // No agent-tokens section: #4580 owns the page and nothing renders there yet.
  assert.equal(findSettingsSection("agent-tokens"), null);
});

test("every section is a real sub-route of the settings index", () => {
  for (const s of SETTINGS_SECTIONS) assert.match(s.path, /^\/dashboard\/settings\/[a-z-]+$/);
  assert.deepEqual(
    SETTINGS_SECTIONS.map((s) => findSettingsSection(s.id)?.id),
    SETTINGS_SECTIONS.map((s) => s.id),
  );
});

test("an owner sees every section", () => {
  assert.deepEqual(ids("owner"), ["household", "members", "credentials", "phone-calendar", "account"]);
});

test("a family_member sees everything but cannot manage the household", () => {
  assert.deepEqual(ids("family_member"), ["household", "members", "credentials", "phone-calendar", "account"]);
  assert.equal(canManageHousehold("family_member"), false);
});

test("a viewer gets no credentials vault (the API answers 403 there)", () => {
  assert.deepEqual(ids("viewer"), ["household", "members", "phone-calendar", "account"]);
});

test("a housesitter gets no credentials vault", () => {
  assert.deepEqual(ids("housesitter"), ["household", "members", "phone-calendar", "account"]);
});

test("a vendor gets neither members nor credentials: the login is about the house", () => {
  assert.deepEqual(ids("vendor"), ["household", "phone-calendar", "account"]);
});

test("managers are exactly owner and family_manager", () => {
  for (const role of ["owner", "family_manager"]) assert.equal(canManageHousehold(role), true, role);
  for (const role of ["family_member", "viewer", "housesitter", "vendor"]) {
    assert.equal(canManageHousehold(role), false, role);
  }
  // Unknown role is not a manager: the write is the destructive half.
  assert.equal(canManageHousehold(null), false);
  assert.equal(canManageHousehold("grandparent"), false);
});

test("a role that isn't known yet sees everything, and an unknown one fails closed", () => {
  assert.deepEqual(ids(null), ["household", "members", "credentials", "phone-calendar", "account"]);
  assert.deepEqual(ids(""), ["household", "members", "credentials", "phone-calendar", "account"]);
  assert.deepEqual(ids("grandparent"), ["household", "phone-calendar", "account"]);
});

test("canSeeSettingsSection reads the same table as settingsSectionsForRole", () => {
  for (const s of SETTINGS_SECTIONS) {
    for (const role of ["owner", "family_member", "viewer", "housesitter", "vendor"]) {
      assert.equal(canSeeSettingsSection(s, role), settingsSectionsForRole(role).includes(s), `${s.id}/${role}`);
    }
  }
});

test("a wide screen lands on the first section the role may open", () => {
  assert.equal(firstSectionHref("owner"), "/dashboard/settings/household");
  assert.equal(firstSectionHref("vendor"), "/dashboard/settings/household");
  assert.equal(firstSectionHref(null), "/dashboard/settings/household");
});

test("role labels are the words a family reads", () => {
  assert.equal(roleLabel("owner"), "Owner");
  assert.equal(roleLabel("family_manager"), "Family manager");
  assert.equal(roleLabel("housesitter"), "Housesitter");
  assert.equal(roleLabel(undefined), "Member");
});
