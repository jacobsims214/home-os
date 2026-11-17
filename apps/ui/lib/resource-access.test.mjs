// Run: npm test (node --test strips the TypeScript types natively).
//
// The UI mirror of the household role policy (#4564). These assertions are the
// same table as apps/api/internal/auth/roles.go and
// packages/db/testdata/household_role_policy.json; if the API's policy moves,
// this file and lib/resource-access.ts move with it.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  canAnnotateEntity,
  canReadActivity,
  canReadNotes,
  canSeeEntity,
  canSeeFiles,
  canSeeSecrets,
  canWriteFiles,
  canWriteNotes,
  resourceAccess,
} from "./resource-access.ts";

const FAMILY = ["owner", "family_manager", "family_member"];

test("files: family roles write, viewer reads, housesitter and vendor get nothing", () => {
  for (const role of FAMILY) {
    assert.equal(canSeeFiles(role), true, role);
    assert.equal(canWriteFiles(role), true, role);
  }
  assert.equal(canSeeFiles("viewer"), true);
  assert.equal(canWriteFiles("viewer"), false);
  for (const role of ["housesitter", "vendor"]) {
    assert.equal(canSeeFiles(role), false, role);
    assert.equal(canWriteFiles(role), false, role);
  }
});

test("secrets: family roles only", () => {
  for (const role of FAMILY) assert.equal(canSeeSecrets(role), true, role);
  for (const role of ["viewer", "housesitter", "vendor"]) {
    assert.equal(canSeeSecrets(role), false, role);
  }
});

test("notes: read is family + viewer + housesitter; write is family", () => {
  for (const role of FAMILY) {
    assert.equal(canReadNotes(role), true, role);
    assert.equal(canWriteNotes(role), true, role);
  }
  assert.equal(canReadNotes("viewer"), true);
  assert.equal(canWriteNotes("viewer"), false);
  assert.equal(canReadNotes("housesitter"), true);
  assert.equal(canWriteNotes("housesitter"), false);
  assert.equal(canReadNotes("vendor"), false);
});

test("canSeeEntity follows entityResources + read grants", () => {
  // A housesitter reads pets and properties, not bills or vehicles.
  assert.equal(canSeeEntity("housesitter", "pet"), true);
  assert.equal(canSeeEntity("housesitter", "property"), true);
  assert.equal(canSeeEntity("housesitter", "bill"), false);
  assert.equal(canSeeEntity("housesitter", "vehicle"), false);
  // A vendor reads properties, assets and maintenance, nothing else.
  assert.equal(canSeeEntity("vendor", "property"), true);
  assert.equal(canSeeEntity("vendor", "asset"), true);
  assert.equal(canSeeEntity("vendor", "maintenance_task"), true);
  assert.equal(canSeeEntity("vendor", "pet"), false);
  assert.equal(canSeeEntity("vendor", "bill"), false);
  // A viewer reads everything.
  for (const kind of ["bill", "vehicle", "pet", "person", "property"]) {
    assert.equal(canSeeEntity("viewer", kind), true, kind);
  }
  // An unknown kind fails closed for outsiders, open for the family.
  assert.equal(canSeeEntity("housesitter", "spaceship"), false);
  assert.equal(canSeeEntity("owner", "spaceship"), true);
});

test("canAnnotateEntity: family on anything they see, housesitter on pets only", () => {
  for (const role of FAMILY) {
    assert.equal(canAnnotateEntity(role, "bill"), true, role);
    assert.equal(canAnnotateEntity(role, "pet"), true, role);
  }
  assert.equal(canAnnotateEntity("housesitter", "pet"), true);
  assert.equal(canAnnotateEntity("housesitter", "property"), false);
  assert.equal(canAnnotateEntity("housesitter", "bill"), false);
  assert.equal(canAnnotateEntity("viewer", "bill"), false);
  assert.equal(canAnnotateEntity("vendor", "maintenance_task"), false);
});

test("resourceAccess: a housesitter on a bill gets no section at all", () => {
  const a = resourceAccess("housesitter", "bill");
  assert.deepEqual(a, { files: "none", notes: "none", secrets: "none", any: false });
});

test("resourceAccess: a housesitter on a pet gets notes (annotate), no files, no secrets", () => {
  const a = resourceAccess("housesitter", "pet");
  assert.equal(a.files, "none");
  // Annotate, not write: they may add a pet note but the API refuses their
  // rename and delete, so the UI must not draw those buttons (#4564).
  assert.equal(a.notes, "annotate");
  assert.equal(a.secrets, "none");
  assert.equal(a.any, true);
});

test("resourceAccess: a vendor gets nothing on any entity", () => {
  for (const kind of ["property", "asset", "maintenance_task", "bill", "pet"]) {
    const a = resourceAccess("vendor", kind);
    assert.equal(a.any, false, kind);
  }
});

test("resourceAccess: a viewer reads files and notes, never secrets", () => {
  const a = resourceAccess("viewer", "bill");
  assert.equal(a.files, "read");
  assert.equal(a.notes, "read");
  assert.equal(a.secrets, "none");
  assert.equal(a.any, true);
});

test("resourceAccess: a family member writes all three", () => {
  for (const role of FAMILY) {
    const a = resourceAccess(role, "property");
    assert.deepEqual(a, { files: "write", notes: "write", secrets: "write", any: true }, role);
  }
});

test("an unknown role fails closed; a null role (still loading) shows everything", () => {
  const unknown = resourceAccess("intruder", "bill");
  assert.equal(unknown.any, false);
  const loading = resourceAccess(null, "bill");
  assert.deepEqual(loading, { files: "write", notes: "write", secrets: "write", any: true });
});


// ── the activity feed's read grant (#4577) ───────────────────

test("canReadActivity: the family roles and a viewer, never a housesitter or vendor", () => {
  // ResActivity read is rolesReaders (apps/api/internal/auth/roles.go). A
  // housesitter and a vendor get 403 from GET /activity with a reason naming
  // the role, action and resource, so Today must not draw the panel for them
  // (KB decisions/2026-11-15-role-policy.md).
  for (const role of FAMILY) assert.equal(canReadActivity(role), true, role);
  assert.equal(canReadActivity("viewer"), true, "a viewer reads the feed read-only");
  assert.equal(canReadActivity("housesitter"), false, "no activity feed for a sitter");
  assert.equal(canReadActivity("vendor"), false, "no activity feed for a vendor");
});

test("canReadActivity: an unknown role fails closed, and an UNKNOWN role does not draw the panel", () => {
  // The other helpers above show everything for a null role and let the API
  // refuse. This one does not: GET /activity is a role-refused ROUTE (403), so
  // drawing it before the role is known would fire a request that is certain to
  // fail and show a housesitter a panel that is then taken away.
  assert.equal(canReadActivity("intruder"), false, "a role this build does not know");
  assert.equal(canReadActivity(""), false, "still loading");
  assert.equal(canReadActivity(null), false, "still loading");
  assert.equal(canReadActivity(undefined), false, "still loading");
});
