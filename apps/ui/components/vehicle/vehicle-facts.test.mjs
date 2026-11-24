import assert from "node:assert/strict";
import test from "node:test";

import { registrationLabel, vehicleSubtitle, vehicleTitle } from "./vehicle-facts.ts";

test("vehicleTitle joins whatever of year/make/model is set", () => {
  assert.equal(vehicleTitle({ year: 2019, make: "Honda", model: "Odyssey" }), "2019 Honda Odyssey");
  assert.equal(vehicleTitle({ make: "Ford", model: "F-150" }), "Ford F-150");
  assert.equal(vehicleTitle({ year: 2021, make: "Ford" }), "2021 Ford");
  // The API stores year as *int; a string from a form still reads the same.
  assert.equal(vehicleTitle({ year: "2019", make: "Honda", model: "Odyssey" }), "2019 Honda Odyssey");
  // Blank strings are not a name.
  assert.equal(vehicleTitle({ year: null, make: "  ", model: "" }), "Untitled vehicle");
});

test("vehicleTitle falls back to the plate, never to an empty card", () => {
  assert.equal(vehicleTitle({ license_plate: "8KJD214" }), "8KJD214");
  assert.equal(vehicleTitle({}), "Untitled vehicle");
});

test("vehicleSubtitle is plate - color, and empty when neither is set", () => {
  assert.equal(vehicleSubtitle({ license_plate: "8KJD214", color: "Blue" }), "8KJD214 \u00b7 Blue");
  assert.equal(vehicleSubtitle({ license_plate: "8KJD214" }), "8KJD214");
  assert.equal(vehicleSubtitle({ color: "Blue" }), "Blue");
  assert.equal(vehicleSubtitle({}), "");
});

test("registrationLabel names the month, and nothing else", () => {
  assert.equal(registrationLabel(3), "Renews in March");
  assert.equal(registrationLabel("12"), "Renews in December");
  assert.equal(registrationLabel(1), "Renews in January");
  // Unset or nonsense months produce no tag rather than a wrong date.
  assert.equal(registrationLabel(null), null);
  assert.equal(registrationLabel(undefined), null);
  assert.equal(registrationLabel(0), null);
  assert.equal(registrationLabel(13), null);
  assert.equal(registrationLabel(3.5), null);
  assert.equal(registrationLabel("March"), null);
});
