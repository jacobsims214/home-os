// Run: npm test (node --test; Node >= 23.6 strips the TypeScript types natively).
//
// The invite accept page's decision table (#4579). What matters here is not
// layout: it is that every answer the API can give maps to copy a person can
// act on, and that the token stays a secret while it happens. The API's
// statuses and messages are the ones in invite/handler.go writeAcceptError.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  MISSING_TOKEN,
  TOKEN_PARAM,
  acceptedCopy,
  describeAcceptError,
  inviteLoginHref,
  readAcceptSuccess,
  readInviteToken,
  redactToken,
  tokenInUrl,
} from "./accept.ts";

const err = (message) => ({ error: { code: "X", message } });
const TOKEN = "4f1c9a7e-2b8d-4e61-9a0f-7c3d5e2b1a64";

test("the token is read from the query once, trimmed, and never invented", () => {
  assert.equal(readInviteToken(`?${TOKEN_PARAM}=${TOKEN}`), TOKEN);
  assert.equal(readInviteToken(`?${TOKEN_PARAM}=%20${TOKEN}%20`), TOKEN, "whitespace from a copy is trimmed");
  assert.equal(readInviteToken(""), "");
  assert.equal(readInviteToken("?token="), "");
  assert.equal(readInviteToken(null), "");
  assert.equal(readInviteToken(undefined), "");
  // Another parameter named like it must not be picked up.
  assert.equal(readInviteToken("?redirect=/x&token=t"), "t");
  // Percent-encoding survives: a token is opaque, so it is decoded exactly once.
  assert.equal(readInviteToken("?token=a%2Bb"), "a+b");
});

test("the login link carries the invite back to this page and no fragment", () => {
  const href = inviteLoginHref(TOKEN);
  assert.match(href, /^\/login\?redirect=/);
  const redirect = new URL(href, "https://home.example").searchParams.get("redirect");
  assert.equal(redirect, `/invites/accept?token=${TOKEN}`, "the token comes back after the Dex round trip");
  assert.equal(href.includes("#"), false, "never a fragment: it would not reach the server");
});

test("a token in a message is redacted; everything else is left alone", () => {
  assert.equal(redactToken(`failed: /invites/accept?token=${TOKEN}`, TOKEN), "failed: /invites/accept?token=[invite token]");
  assert.equal(redactToken("no token in here", TOKEN), "no token in here");
  assert.equal(redactToken(`two ${TOKEN} and ${TOKEN}`, TOKEN), "two [invite token] and [invite token]");
  assert.equal(redactToken("keep it", ""), "keep it");
});

test("an expired invite is a dead link with no action but to ask again", () => {
  const copy = describeAcceptError(400, err("invitation has expired"));
  assert.equal(copy.kind, "expired");
  assert.equal(copy.dead, true);
  assert.equal(copy.action, "none");
  assert.match(copy.title, /expired/i);
  assert.match(copy.detail, /new link/i);
});

test("a used invite says so, and offers sign in", () => {
  const copy = describeAcceptError(409, err("invitation already accepted"));
  assert.equal(copy.kind, "used");
  assert.equal(copy.dead, true);
  assert.equal(copy.action, "signin");
  assert.match(copy.title, /already been used/i);
});

test("an unknown or revoked token is one dead-link answer, indistinguishably", () => {
  const copy = describeAcceptError(404, err("invalid or expired invitation"));
  assert.equal(copy.kind, "unknown");
  assert.equal(copy.dead, true);
  assert.equal(copy.action, "none");
  // The API does not reveal which it was, so neither does the copy.
  assert.doesNotMatch(copy.title, /revoked|cancelled/i);
  assert.match(copy.detail, /cancelled/i, "the detail may list the possibilities");
});

test("406 is not the answer here: an unrecognised status still says something true", () => {
  const copy = describeAcceptError(500, err("internal server error"));
  assert.equal(copy.kind, "other");
  assert.equal(copy.dead, false);
  assert.equal(copy.action, "signup");
  assert.match(copy.detail, /nothing was changed/i);
});

test("an existing account is a sign-in, not a dead link", () => {
  const copy = describeAcceptError(409, err("an account with this email already exists; sign in to accept the invitation"));
  assert.equal(copy.kind, "account-exists");
  assert.equal(copy.action, "signin");
  assert.equal(copy.dead, false, "the link is still good: signing in completes it");
});

test("already a member offers a way into the app, not a retry", () => {
  const copy = describeAcceptError(409, err("already a member of this household"));
  assert.equal(copy.kind, "already-member");
  assert.equal(copy.action, "home");
});

test("a different email says sign out, and says the link survives it", () => {
  const copy = describeAcceptError(403, err("this invitation was sent to a different email address"));
  assert.equal(copy.kind, "email-mismatch");
  assert.equal(copy.action, "signout");
  assert.equal(copy.dead, false);
  assert.doesNotMatch(copy.detail, /expired/i, "do not tell them the link is dead when it is not");
});

test("a deactivated account explains who can fix it", () => {
  const copy = describeAcceptError(403, err("this account has been deactivated"));
  assert.equal(copy.kind, "deactivated");
  assert.equal(copy.action, "none");
  assert.match(copy.detail, /admin/i);
});

test("401, 429 and 502/503 are transient, never a dead link", () => {
  for (const [status, kind] of [[401, "not-authenticated"], [429, "rate-limited"], [502, "unavailable"], [503, "unavailable"]]) {
    const copy = describeAcceptError(status, err("whatever"));
    assert.equal(copy.kind, kind, String(status));
    assert.equal(copy.dead, false, `${status} leaves the invite usable`);
  }
  assert.equal(describeAcceptError(401, err("x")).action, "signin");
  assert.equal(describeAcceptError(429, err("x")).action, "signup");
  assert.equal(describeAcceptError(503, err("x")).action, "signup");
});

test("the match does not depend on the message's capitalisation", () => {
  assert.equal(describeAcceptError(409, err("INVITATION ALREADY ACCEPTED")).kind, "used");
  assert.equal(describeAcceptError(403, err("This Invitation Was Sent To A Different Email Address")).kind, "email-mismatch");
});

test("a body with no message does not crash the page and still maps by status", () => {
  assert.equal(describeAcceptError(404, null).kind, "unknown");
  assert.equal(describeAcceptError(409, {}).kind, "other", "a 409 with no message cannot be pinned down");
  assert.equal(describeAcceptError(400, "not an object").kind, "other");
});

test("a missing token is its own card, and it is not the inviter's fault", () => {
  assert.equal(MISSING_TOKEN.dead, true);
  assert.equal(MISSING_TOKEN.action, "none");
  assert.match(MISSING_TOKEN.detail, /copied/i);
  assert.doesNotMatch(MISSING_TOKEN.detail, /expired/i);
});

test("success reads the API's own shape, both modes", () => {
  const signedUp = readAcceptSuccess({
    data: { role: "housesitter", user_created: true, email: "maria@example.test", household_id: "h" },
  });
  assert.deepEqual(signedUp, { role: "housesitter", userCreated: true, email: "maria@example.test" });
  assert.match(acceptedCopy(signedUp).detail, /maria@example\.test/);
  assert.match(acceptedCopy(signedUp).detail, /sign in/i, "a new account needs a first sign-in");

  const joined = readAcceptSuccess({ data: { role: "viewer", user_created: false, email: "tom@example.test" } });
  assert.equal(joined.userCreated, false);
  assert.doesNotMatch(acceptedCopy(joined).detail, /sign in/i, "an existing session does not sign in again");
});

test("a success body missing fields still renders", () => {
  assert.deepEqual(readAcceptSuccess(null), { role: "", userCreated: false, email: "" });
  assert.deepEqual(readAcceptSuccess({}), { role: "", userCreated: false, email: "" });
  assert.match(acceptedCopy(readAcceptSuccess({})).detail, /joined the family/i);
});

test("tokenInUrl is the only place a token becomes a URL, and it encodes it", () => {
  assert.equal(tokenInUrl(TOKEN), `/invites/accept?token=${TOKEN}`);
  assert.equal(tokenInUrl("a b&c"), "/invites/accept?token=a%20b%26c");
});
