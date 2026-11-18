// Run: npm test (node --test strips the TypeScript types natively).
//
// The UI mirror of the member-management rules (#4559) and the colour palette
// (#4821). The assertions below are the same rules as
// apps/api/internal/auth/roles.go (CanManageMembers / CanGrantRole /
// CanModifyMember, people.ActSetColor) and the palette in migration 071. If the
// API's policy moves, this file and lib/members/roles.ts move with it.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  MEMBER_ROLES,
  PALETTE,
  canGrantRole,
  canManageMembers,
  canModifyMember,
  canSeeMembers,
  canSeePeopleFull,
  canSetColor,
  canSetColorFor,
  capabilitiesOf,
  grantableRoles,
  hasMemberActions,
  isPaletteColor,
  memberActions,
  paletteVar,
  roleChangeSummary,
} from "./roles.ts";

const MANAGERS = ["owner", "family_manager"];
const FAMILY = ["owner", "family_manager", "family_member"];
const NON_MANAGERS = ["family_member", "viewer", "housesitter", "vendor"];

test("the six membership roles, in the enum's order", () => {
  assert.deepEqual([...MEMBER_ROLES], [
    "owner",
    "family_manager",
    "family_member",
    "viewer",
    "housesitter",
    "vendor",
  ]);
});

test("only owner and family_manager manage members", () => {
  for (const role of MANAGERS) assert.equal(canManageMembers(role), true, role);
  for (const role of NON_MANAGERS) assert.equal(canManageMembers(role), false, role);
  assert.equal(canManageMembers(null), false);
  assert.equal(canManageMembers("instance_admin"), false, "an unknown role fails closed");
});

test("CanGrantRole: an owner grants anything, a family_manager everything but owner", () => {
  assert.deepEqual(grantableRoles("owner"), [...MEMBER_ROLES]);
  assert.deepEqual(grantableRoles("family_manager"), [
    "family_manager",
    "family_member",
    "viewer",
    "housesitter",
    "vendor",
  ]);
  for (const target of MEMBER_ROLES) {
    assert.equal(canGrantRole("owner", target), true, `owner -> ${target}`);
    assert.equal(canGrantRole("family_manager", target), target !== "owner", `manager -> ${target}`);
    for (const role of NON_MANAGERS) assert.equal(canGrantRole(role, target), false, `${role} -> ${target}`);
  }
  assert.deepEqual(grantableRoles("family_member"), []);
  assert.deepEqual(grantableRoles(null), []);
  assert.equal(canGrantRole("owner", "superuser"), false, "an unknown target role is refused");
});

test("CanModifyMember: a family_manager never changes or removes an owner", () => {
  for (const target of MEMBER_ROLES) {
    assert.equal(canModifyMember("owner", target), true, `owner -> ${target}`);
    assert.equal(canModifyMember("family_manager", target), target !== "owner", `manager -> ${target}`);
    for (const role of NON_MANAGERS) assert.equal(canModifyMember(role, target), false, `${role} -> ${target}`);
  }
});

test("the members list is readable by everyone but a vendor", () => {
  for (const role of ["owner", "family_manager", "family_member", "viewer", "housesitter"]) {
    assert.equal(canSeeMembers(role), true, role);
  }
  assert.equal(canSeeMembers("vendor"), false, "a vendor sees no people at all");
  assert.equal(canSeeMembers(null), false);
});

test("full people details reach the family and a viewer, never a housesitter", () => {
  for (const role of [...FAMILY, "viewer"]) assert.equal(canSeePeopleFull(role), true, role);
  for (const role of ["housesitter", "vendor"]) assert.equal(canSeePeopleFull(role), false, role);
});

test("colour: family roles may set one, and only self-or-manager may pick it", () => {
  for (const role of FAMILY) assert.equal(canSetColor(role), true, role);
  for (const role of ["viewer", "housesitter", "vendor"]) {
    // The policy's canEdit refuses a people write for these roles at all, so
    // #4821 refuses their own colour too.
    assert.equal(canSetColor(role), false, role);
    assert.equal(canSetColorFor(role, { isSelf: true }), false, `${role} on self`);
  }

  for (const role of MANAGERS) {
    assert.equal(canSetColorFor(role, { isSelf: false }), true, `${role} on someone else`);
    assert.equal(canSetColorFor(role, { isSelf: true }), true, `${role} on self`);
  }
  assert.equal(canSetColorFor("family_member", { isSelf: true }), true);
  assert.equal(canSetColorFor("family_member", { isSelf: false }), false, "a member recolours only themselves");
});

test("memberActions: a non-manager gets no management controls, only their own colour", () => {
  for (const role of NON_MANAGERS) {
    const other = memberActions(role, { role: "family_member", isSelf: false }, 1);
    assert.deepEqual(other, { changeRole: false, remove: false, color: false }, `${role} on another member`);
    assert.equal(hasMemberActions(other), false, `${role} has no menu at all`);
    const self = memberActions(role, { role: "family_member", isSelf: true }, 1);
    assert.equal(self.changeRole, false, `${role} changes no roles`);
    assert.equal(self.remove, false, `${role} removes nobody`);
    assert.equal(self.color, role === "family_member", `${role} on own colour`);
  }
});

test("memberActions: a manager manages others, never themselves, and never the last owner", () => {
  const managerOnMember = memberActions("family_manager", { role: "family_member", isSelf: false }, 2);
  assert.deepEqual(managerOnMember, { changeRole: true, remove: true, color: true });

  const managerOnOwner = memberActions("family_manager", { role: "owner", isSelf: false }, 2);
  assert.equal(managerOnOwner.changeRole, false, "a manager cannot change an owner");
  assert.equal(managerOnOwner.remove, false, "a manager cannot remove an owner");
  assert.equal(managerOnOwner.color, true, "colour is not a management act: a manager may still recolour an owner");

  const ownerOnOwner = memberActions("owner", { role: "owner", isSelf: false }, 2);
  assert.equal(ownerOnOwner.changeRole, true);

  const lastOwner = memberActions("owner", { role: "owner", isSelf: false }, 1);
  assert.equal(lastOwner.changeRole, false, "the last owner cannot be demoted (API answers 409)");
  assert.equal(lastOwner.remove, false);

  const ownRow = memberActions("owner", { role: "owner", isSelf: true }, 2);
  assert.deepEqual(ownRow, { changeRole: false, remove: false, color: true });
});

test("paletteVar: only the eight names reach a style attribute", () => {
  for (const name of PALETTE) {
    assert.equal(isPaletteColor(name), true, name);
    assert.equal(paletteVar(name), `var(--mantine-color-${name}-6)`, name);
  }
  for (const bad of ["Red", " red", "#ff0000", "rgb(0,0,0)", "", null, undefined, "expression(1)", "red;background:url(x)"]) {
    assert.equal(isPaletteColor(bad), false, String(bad));
    assert.equal(paletteVar(bad), null, `${String(bad)} must not become a style value`);
  }
  // Eight, and no more: a ninth name would need migration 071 to agree.
  assert.equal(PALETTE.length, 8);
});

test("roleChangeSummary names what a demotion actually takes away", () => {
  const demote = roleChangeSummary("family_manager", "family_member");
  assert.equal(demote.narrower, true);
  assert.deepEqual(demote.lost, ["Invite people and change roles"]);
  assert.deepEqual(demote.gained, []);

  // An owner demoted to manager keeps the family's work but loses the two
  // owner-only powers: granting owner and touching another owner.
  const ownerToManager = roleChangeSummary("owner", "family_manager");
  assert.deepEqual(ownerToManager.lost, ["Make someone an owner, or change an owner"]);
  assert.equal(ownerToManager.narrower, true);

  // housesitter/vendor change WHAT DATA IS VISIBLE, not just what can be edited.
  const toSitter = roleChangeSummary("family_member", "housesitter");
  assert.ok(toSitter.lost.includes("See money: bills, loans and the budget"), "money goes");
  assert.ok(toSitter.lost.includes("Open and edit credentials"), "credentials go");
  assert.ok(toSitter.lost.includes("See and edit everything in the family"), "write goes");
  // A family_member already had read_basic, so moving to housesitter gains
  // nothing: it is a pure narrowing, which is what makes it the example the
  // confirmation must spell out.
  assert.deepEqual(toSitter.gained, []);
  // The meals/lists/house capabilities survive a move to housesitter: that is
  // exactly the data a sitter keeps, and the reason the confirmation lists
  // what is lost rather than saying "less access".
  assert.ok(!toSitter.lost.includes("See the calendar, lists and meal plan"));
  assert.ok(!toSitter.lost.includes("Add and check off list items"));

  const toVendor = roleChangeSummary("family_member", "vendor");
  assert.ok(toVendor.lost.length > 0);
  assert.ok(
    toVendor.lost.includes("See the family activity feed") ||
      toVendor.lost.includes("See money: bills, loans and the budget"),
    "a vendor loses the family's data",
  );

  const promote = roleChangeSummary("viewer", "family_manager");
  assert.equal(promote.narrower, false);
  assert.deepEqual(promote.lost, []);
  assert.ok(promote.gained.includes("Invite people and change roles"));

  assert.deepEqual(roleChangeSummary("viewer", "viewer"), { narrower: false, gained: [], lost: [] });
});

test("the capability table grows monotonically along the role ladder", () => {
  // owner ⊇ family_manager ⊇ family_member ⊇ viewer (nothing is taken back by a
  // promotion), and a housesitter is not a subset of a vendor or vice versa:
  // they are different jobs, which is why the confirmation lists both sides.
  const count = (role) => capabilitiesOf(role).length;
  assert.ok(count("owner") > count("family_manager"));
  assert.ok(count("family_manager") > count("family_member"));
  assert.ok(count("family_member") > count("viewer"));
  assert.ok(count("viewer") > count("vendor"));
  assert.ok(count("viewer") > count("housesitter"));
  for (const role of MEMBER_ROLES) assert.ok(count(role) > 0, `${role} can do something`);
});
