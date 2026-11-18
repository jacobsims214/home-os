// Run: npm test (node --test strips the TypeScript types natively).
//
// The indefinite article and the role's prose form (#5300).
//
// "you're a owner" shipped on the Members page and in Settings → Household
// because each place wrote the phrase by hand with a literal "a". This file pins
// the one implementation: the article is chosen by the SOUND of the first
// syllable, so a word spelled with a vowel but said with a consonant ("a user",
// "a one-off") does not get "an".

import { test } from "node:test";
import assert from "node:assert/strict";
import {
  HOUSEHOLD_ROLE_LABELS,
  householdRoleWithArticle,
  indefiniteArticle,
} from "./household-roles.ts";

test("every role label takes the right article — owner is the only 'an'", () => {
  const expected = {
    owner: "an owner",
    family_manager: "a family manager",
    family_member: "a family member",
    viewer: "a viewer",
    housesitter: "a housesitter",
    vendor: "a vendor",
  };
  for (const [role, phrase] of Object.entries(expected)) {
    assert.equal(householdRoleWithArticle(role), phrase, role);
    // Mid-sentence form: the label is lowercased, never the article.
    assert.match(householdRoleWithArticle(role), /^(a|an) /, role);
  }
  // The regression the war game saw, asserted by name so it cannot come back.
  assert.notEqual(householdRoleWithArticle("owner"), "a owner");
  assert.equal(`you're ${householdRoleWithArticle("owner")}`, "you're an owner");
});

test("the article follows the word's sound, not its spelling", () => {
  // Vowel letter AND vowel sound.
  assert.equal(indefiniteArticle("owner"), "an");
  assert.equal(indefiniteArticle("agent"), "an");
  assert.equal(indefiniteArticle("hour"), "an"); // silent h
  assert.equal(indefiniteArticle("honest"), "an");
  // Consonant letter.
  assert.equal(indefiniteArticle("family"), "a");
  assert.equal(indefiniteArticle("viewer"), "a");
  // Vowel LETTER, consonant SOUND — the cases a naive /[aeiou]/ rule gets wrong.
  assert.equal(indefiniteArticle("user"), "a");
  assert.equal(indefiniteArticle("unique"), "a");
  assert.equal(indefiniteArticle("European"), "a");
  assert.equal(indefiniteArticle("one-off"), "a");
  assert.equal(indefiniteArticle("utility"), "a");
  // Nothing to go on: a, never "an ".
  assert.equal(indefiniteArticle(""), "a");
  assert.equal(indefiniteArticle("   "), "a");
  // Case and whitespace are not the caller's problem.
  assert.equal(indefiniteArticle("  Owner  "), "an");
  assert.equal(indefiniteArticle("OWNER"), "an");
});

test("an unknown or absent role still reads as a sentence", () => {
  // A role this build doesn't know: the API's own words, title-cased, with an
  // article that fits.
  assert.equal(householdRoleWithArticle("estate_manager"), "an estate manager");
  assert.equal(householdRoleWithArticle("apprentice"), "an apprentice");
  assert.equal(householdRoleWithArticle("jobs_coordinator"), "a jobs coordinator");
  // No role at all falls back to householdRoleLabel's "Member".
  assert.equal(householdRoleWithArticle(null), "a member");
  assert.equal(householdRoleWithArticle(undefined), "a member");
  // Sentence-start form capitalises the article and the label, so a sentence
  // beginning with the phrase is not "an owner …" mid-capitalisation.
  assert.equal(householdRoleWithArticle("owner", { capitalised: true }), "An owner");
  assert.equal(householdRoleWithArticle("viewer", { capitalised: true }), "A viewer");
});

test("the role labels this helper reads are the shared ones", () => {
  // The article is derived from householdRoleLabel, so this map is the authority
  // for both. A new role in the map needs no change here — the test asserts the
  // invariant, not a copy of the list.
  for (const [role, label] of Object.entries(HOUSEHOLD_ROLE_LABELS)) {
    assert.equal(
      householdRoleWithArticle(role),
      `${indefiniteArticle(label)} ${label.toLowerCase()}`,
      role,
    );
  }
  assert.equal(householdRoleWithArticle("owner"), "an owner");
});