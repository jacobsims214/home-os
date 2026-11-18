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

// Agent tokens slots in after Phone calendar (#4580). Credentials keeps the
// place it already had rather than being moved for the mockup's ordering: a
// section's position is #4560's table, and moving one would be a change nobody
// asked this ticket for.
const ALL_IDS = ["household", "members", "credentials", "phone-calendar", "agent-tokens", "account"];

test("the six sections, in the order the settings page lists them", () => {
  assert.deepEqual(SETTINGS_SECTIONS.map((s) => s.id), ALL_IDS);
  // #4580: Agent tokens is a real section, reachable at its own route.
  assert.equal(findSettingsSection("agent-tokens")?.path, "/dashboard/settings/agent-tokens");
});

test("every section is a real sub-route of the settings index", () => {
  for (const s of SETTINGS_SECTIONS) assert.match(s.path, /^\/dashboard\/settings\/[a-z-]+$/);
  assert.deepEqual(
    SETTINGS_SECTIONS.map((s) => findSettingsSection(s.id)?.id),
    SETTINGS_SECTIONS.map((s) => s.id),
  );
});

test("an owner sees every section", () => {
  assert.deepEqual(ids("owner"), ALL_IDS);
});

test("a family_member sees everything but cannot manage the household", () => {
  assert.deepEqual(ids("family_member"), ALL_IDS);
  assert.equal(canManageHousehold("family_member"), false);
});

test("a viewer gets no credentials vault, but may hold a read-only agent token", () => {
  // The API refuses ResSecrets for a viewer but allows ResAgentTokens ActCreate
  // (read scope only), and the mockup lists the section for family + viewer.
  assert.deepEqual(ids("viewer"), ["household", "members", "phone-calendar", "agent-tokens", "account"]);
  assert.equal(ids("viewer").includes("credentials"), false);
});

test("a housesitter gets neither the vault nor agent tokens", () => {
  // ActCreate is rolesReaders, so a housesitter could only ever be shown a
  // form that answers 403; the section is not offered at all.
  assert.deepEqual(ids("housesitter"), ["household", "members", "phone-calendar", "account"]);
});

test("a vendor gets neither members, credentials nor tokens: the login is about the house", () => {
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
  assert.deepEqual(ids(null), ALL_IDS);
  assert.deepEqual(ids(""), ALL_IDS);
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
