// Run: npm test (node --test strips the TypeScript types natively).
//
// The Agent tokens section's rules (#4580), mirroring the API
// (apps/api/internal/auth/roles.go ResAgentTokens, apps/api/internal/agenttoken).
// The point of these tests is the two facts a diff cannot show: a list only ever
// draws the PREFIX, and a viewer can only create a read-only token.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  canCreateAgentTokens,
  canWriteAgentTokens,
  expiryFromDays,
  expiryLabel,
  expiresSoon,
  lastUsedLabel,
  maskedToken,
  revokedLabel,
  scopeLabel,
  scopesFor,
  splitTokens,
} from "./agent-tokens.ts";

const NOW = Date.parse("2026-11-18T18:00:00Z");
const at = (days) => new Date(NOW + days * 86_400_000).toISOString();

test("a list shows the prefix and an ellipsis, never a whole token", () => {
  assert.equal(maskedToken("hos_k3Fq9aZx"), "hos_k3Fq9aZx…");
  // The real token is 47 characters; nothing produced here is ever that long.
  assert.ok(maskedToken("hos_k3Fq9aZx").length < 20);
  // A row with no prefix still reads as a token rather than an empty cell.
  assert.equal(maskedToken(""), "hos_…");
  assert.equal(maskedToken(undefined), "hos_…");
});

test("scopes read as the words on the token", () => {
  assert.equal(scopeLabel(["read"]), "Read only");
  assert.equal(scopeLabel(["read", "write"]), "Read & write");
  assert.equal(scopeLabel([]), "Read only");
  assert.equal(scopeLabel(null), "Read only");
  // A write scope anywhere wins: it can change data.
  assert.equal(scopeLabel(["write"]), "Read & write");
});

test("the create body always carries read, and write only when asked", () => {
  assert.deepEqual(scopesFor(false), ["read"]);
  assert.deepEqual(scopesFor(true), ["read", "write"]);
});

test("who may create a token: owner, family_manager, family_member (API ActCreate)", () => {
  for (const role of ["owner", "family_manager", "family_member"]) {
    assert.equal(canCreateAgentTokens(role), true, role);
  }
  // rolesReaders is the API's list; housesitter and vendor are not in it.
  for (const role of ["viewer", "housesitter", "vendor"]) {
    assert.equal(canCreateAgentTokens(role), false, role);
  }
  // Unknown role or not loaded yet fails closed for the destructive half.
  assert.equal(canCreateAgentTokens(null), false);
  assert.equal(canCreateAgentTokens("grandparent"), false);
});

test("who may ask for write: the same three family roles (API ActWriteScope)", () => {
  for (const role of ["owner", "family_manager", "family_member"]) {
    assert.equal(canWriteAgentTokens(role), true, role);
  }
  // A viewer's token is read-only; the API refuses the write scope.
  for (const role of ["viewer", "housesitter", "vendor"]) {
    assert.equal(canWriteAgentTokens(role), false, role);
  }
  assert.equal(canWriteAgentTokens(null), false);
});

test("last used reads in human time", () => {
  assert.equal(lastUsedLabel(null, NOW), "Not used yet");
  assert.equal(lastUsedLabel(undefined, NOW), "Not used yet");
  assert.equal(lastUsedLabel("not-a-date", NOW), "Not used yet");
  assert.equal(lastUsedLabel(new Date(NOW - 20_000).toISOString(), NOW), "Used just now");
  assert.equal(lastUsedLabel(new Date(NOW - 4 * 60_000).toISOString(), NOW), "Used 4 min ago");
  assert.equal(lastUsedLabel(new Date(NOW - 3_600_000).toISOString(), NOW), "Used 1 hour ago");
  assert.equal(lastUsedLabel(new Date(NOW - 5 * 3_600_000).toISOString(), NOW), "Used 5 hours ago");
  assert.equal(lastUsedLabel(new Date(NOW - 86_400_000).toISOString(), NOW), "Used 1 day ago");
  assert.equal(lastUsedLabel(new Date(NOW - 3 * 86_400_000).toISOString(), NOW), "Used 3 days ago");
  // Older than a week falls back to the date itself, which is unambiguous.
  assert.match(lastUsedLabel("2026-11-02T12:00:00Z", NOW), /^Used Nov 2$/);
});

test("expiry says never, a date, or that it went", () => {
  assert.equal(expiryLabel(null, NOW), "Never expires");
  // This year keeps it short; next year names the year (a 90-day token).
  assert.equal(expiryLabel(at(6), NOW), "Expires Nov 24");
  assert.equal(expiryLabel(at(90), NOW), "Expires Feb 16, 2027");
  assert.equal(expiryLabel(at(-3), NOW), "Expired Nov 15");
  // A 30-day window is what the list flags, and a past date never flags.
  assert.equal(expiresSoon(at(10), NOW), true);
  assert.equal(expiresSoon(at(90), NOW), false);
  assert.equal(expiresSoon(at(-1), NOW), false);
  assert.equal(expiresSoon(null, NOW), false);
});

test("revoked reads as the date it stopped", () => {
  assert.match(revokedLabel("2026-11-02T12:00:00Z", NOW), /^Revoked Nov 2$/);
  assert.equal(revokedLabel(null, NOW), "Revoked");
});

test("the expiry choice becomes an RFC3339 instant in the future", () => {
  assert.equal(expiryFromDays(null, new Date(NOW)), undefined);
  const at90 = expiryFromDays(90, new Date(NOW));
  assert.ok(at90);
  assert.equal(Date.parse(at90) - NOW, 90 * 86_400_000);
});

test("active and revoked split, order kept", () => {
  const rows = [
    { id: "a", revoked_at: null },
    { id: "b", revoked_at: "2026-11-02T12:00:00Z" },
    { id: "c", revoked_at: null },
  ];
  const { active, revoked } = splitTokens(rows);
  assert.deepEqual(active.map((t) => t.id), ["a", "c"]);
  assert.deepEqual(revoked.map((t) => t.id), ["b"]);
  assert.deepEqual(splitTokens([]), { active: [], revoked: [] });
});
