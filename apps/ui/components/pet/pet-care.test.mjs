// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  ageLabel,
  ageShort,
  ageYears,
  formatBirthday,
  formatBirthMonth,
  isCareWarning,
  noteSentences,
  parseDateOnly,
  petEditBody,
  petLine,
  petSubtitle,
  telHref,
  vetName,
  vetPhone,
} from "./pet-care.ts";

test("a date-only string is read from its own parts, never through a Date", () => {
  assert.deepEqual(parseDateOnly("2020-05-12"), { y: 2020, m: 5, d: 12 });
  // The API may return a timestamp; the date part is the date.
  assert.deepEqual(parseDateOnly("2020-05-12T00:00:00Z"), { y: 2020, m: 5, d: 12 });
  assert.equal(parseDateOnly(null), null);
  assert.equal(parseDateOnly(""), null);
  assert.equal(parseDateOnly("May 12, 2020"), null);
  assert.equal(parseDateOnly("2020-13-01"), null);
});

test("the birthday keeps its day in every zone (the #4550 bug class)", () => {
  // new Date("2020-05-12") formats as May 11 in New York; this must not.
  assert.equal(formatBirthday("2020-05-12"), "May 12, 2020");
  assert.equal(formatBirthday("2020-05-01"), "May 1, 2020");
  assert.equal(formatBirthday("2020-12-31T00:00:00Z"), "December 31, 2020");
  assert.equal(formatBirthday(null), "");
  assert.equal(formatBirthMonth("2020-05-12"), "May 2020");
});

test("the age is whole years, and only after the birthday", () => {
  assert.equal(ageYears("2020-05-12", "2026-11-18"), 6);
  assert.equal(ageYears("2020-05-12", "2026-05-11"), 5); // the day before
  assert.equal(ageYears("2020-05-12", "2026-05-12"), 6); // on the day
  // A birthday in the future is not a negative age; it is unknown.
  assert.equal(ageYears("2027-01-01", "2026-11-18"), null);
  assert.equal(ageYears(null, "2026-11-18"), null);
  assert.equal(ageLabel("2020-05-12", "2026-11-18"), "6 years");
  assert.equal(ageLabel("2025-05-12", "2026-11-18"), "1 year");
  assert.equal(ageShort("2020-05-12", "2026-11-18"), "6");
  assert.equal(ageShort(null, "2026-11-18"), "");
});

test("the pet's line is breed (or species), age, then the vet", () => {
  const biscuit = { breed: "Golden retriever", species: "Dog", date_of_birth: "2020-05-12", vet_name: "Springfield Animal Hospital" };
  assert.equal(petSubtitle(biscuit, "2026-11-18"), "Golden retriever · 6");
  assert.equal(petLine(biscuit, "2026-11-18"), "Golden retriever · 6 · Springfield Animal Hospital");
  // No breed: the species stands in. No vet: the line stops at the age.
  assert.equal(petSubtitle({ species: "Cat", date_of_birth: "2017-01-01" }, "2026-11-18"), "Cat · 9");
  assert.equal(petLine({ species: "Cat" }, "2026-11-18"), "Cat");
  // Before the household is known there is no age to show, and nothing guessed.
  assert.equal(petSubtitle(biscuit, null), "Golden retriever");
  // A pet with nothing filled in says nothing rather than a line of dots.
  assert.equal(petLine({}, "2026-11-18"), "");
});

test("the vet is the API's read-only name and phone", () => {
  assert.equal(vetName({ vet_name: " Paws & Claws " }), "Paws & Claws");
  assert.equal(vetName({ vet_name: null, vet_vendor_id: "v1" }), "");
  assert.equal(vetPhone({ vet_phone: "555-0100" }), "555-0100");
  assert.equal(vetPhone({}), "");
  // A phone is dialled as digits: spaces, brackets and dashes go, a + stays.
  assert.equal(telHref("(555) 010-0100"), "tel:5550100100");
  assert.equal(telHref("+1 555 0100"), "tel:+15550100");
  assert.equal(telHref("555.0100 ext 12"), "tel:555010012");
});

test("the PUT body sends the owned fields and the vet id only", () => {
  const body = petEditBody(
    { name: "  Biscuit ", species: "Dog", breed: "Golden retriever", date_of_birth: "2020-05-12T00:00:00Z", notes: " Salmon only " },
    "v1",
  );
  assert.deepEqual(body, {
    name: "Biscuit",
    species: "Dog",
    breed: "Golden retriever",
    date_of_birth: "2020-05-12",
    notes: "Salmon only",
    vet_vendor_id: "v1",
  });
  // The read-only columns can never leak back (#4700: PetWriteRequest has neither).
  assert.equal("vet_name" in body, false);
  assert.equal("vet_phone" in body, false);
  // A cleared field clears the column instead of being dropped.
  const cleared = petEditBody({ name: "Pepper", breed: "", date_of_birth: "" }, null);
  assert.equal(cleared.breed, null);
  assert.equal(cleared.date_of_birth, null);
  assert.equal(cleared.vet_vendor_id, null);
});

test("a care note is split into sentences, and warnings are marked in words", () => {
  assert.deepEqual(noteSentences("Allergic to chicken. Salmon kibble only, 1 cup at 7 AM. Apoquel with dinner!"), [
    "Allergic to chicken.",
    "Salmon kibble only, 1 cup at 7 AM.",
    "Apoquel with dinner!",
  ]);
  assert.deepEqual(noteSentences(null), []);
  assert.deepEqual(noteSentences("   "), []);
  assert.equal(isCareWarning("Allergic to chicken."), true);
  assert.equal(isCareWarning("Don't let her out."), true);
  assert.equal(isCareWarning("Never give him chocolate."), true);
  assert.equal(isCareWarning("Salmon kibble only, 1 cup at 7 AM."), false);
  assert.equal(isCareWarning("Apoquel with dinner."), false);
});
