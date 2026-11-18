// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
//
// The UI's copy of the API's time zone rule (#4560, api/household-settings.md).
// The table below is the same one apps/api/internal/household/update_test.go
// uses for its 14 rejected zones: if the API's rule moves, this file moves with
// it. A value that passes here but fails there is a 400 the person cannot act
// on; a value that fails here but is valid there makes a real zone unpickable.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  MAX_TIMEZONE_LENGTH,
  TIMEZONE_ERROR,
  canonicalZoneName,
  timezoneError,
  timezoneOptions,
  validateTimezone,
} from "./timezone.ts";

const ACCEPTED = [
  "UTC",
  "GMT",
  "CET",
  "America/Chicago",
  "America/Argentina/Buenos_Aires",
  "America/Port-au-Prince",
  "Etc/GMT+5",
  "EST5EDT",
  "US/Eastern",
  "W-SU",
];

const REJECTED = [
  "Mars/Olympus_Mons",
  "not a zone",
  "../etc/passwd",
  "America",
  "america/chicago", // resolves in a browser, fails on a Linux container
  "America/chicago",
  "posixrules",
  "Local", // the server's own zone
  "Factory", // tzdata's fixed-UTC placeholder
  "Etc/Unknown",
  "#ff0000",
];

test("every accepted form the API accepts is accepted here too", () => {
  for (const tz of ACCEPTED) assert.equal(timezoneError(tz), null, tz);
});

test("junk, case variants and the two non-places are refused", () => {
  for (const tz of REJECTED) assert.equal(timezoneError(tz), TIMEZONE_ERROR, JSON.stringify(tz));
});

test("a blank field says so instead of naming the format", () => {
  assert.equal(timezoneError(""), "Enter a time zone");
  assert.equal(timezoneError(" \t "), "Enter a time zone");
});

test("the trimmed value is what goes in the PATCH body", () => {
  assert.deepEqual(validateTimezone("  America/Chicago  "), {
    ok: true,
    value: "America/Chicago",
    error: null,
  });
  assert.deepEqual(validateTimezone("america/chicago"), {
    ok: false,
    value: "",
    error: TIMEZONE_ERROR,
  });
});

test("a name longer than households.timezone allows is refused", () => {
  const long = "America/" + "A".repeat(MAX_TIMEZONE_LENGTH);
  assert.ok(long.length > MAX_TIMEZONE_LENGTH);
  assert.equal(timezoneError(long), TIMEZONE_ERROR);
});

test("canonicalZoneName follows the tz database's spelling, segment by segment", () => {
  assert.equal(canonicalZoneName("America/Chicago"), true);
  assert.equal(canonicalZoneName("Etc/GMT+5"), true);
  assert.equal(canonicalZoneName("EST5EDT"), true);
  assert.equal(canonicalZoneName("America/Argentina/Buenos_Aires"), true);
  assert.equal(canonicalZoneName("america/chicago"), false);
  assert.equal(canonicalZoneName("America//Chicago"), false);
  assert.equal(canonicalZoneName("America/"), false);
  assert.equal(canonicalZoneName("/America"), false);
  assert.equal(canonicalZoneName("America/Chicago "), false);
});

test("the picker offers UTC and only names the validator accepts", () => {
  const options = timezoneOptions();
  assert.ok(options.includes("UTC"), "UTC must be offered: it is the API's default");
  assert.ok(options.includes("America/Chicago"));
  assert.deepEqual(options, [...options].sort((a, b) => a.localeCompare(b)), "sorted");
  assert.equal(new Set(options).size, options.length, "no duplicates");
  // Every option must be a value the form will accept, or the picker would be
  // offering a 400.
  for (const tz of options) assert.equal(timezoneError(tz), null, tz);
});
