// Run: npm test. The provenance wording is the whole point of #4941's UI half:
// a re-typed or OCR'd body must never be presented as the original document.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  describeFileText,
  formatBytes,
  formatDate,
  isVerbatimText,
  textProvenance,
  textProvenanceLabel,
  uploaderLine,
} from "./file-display.ts";

test("formatBytes", () => {
  assert.equal(formatBytes(0), "0 B");
  assert.equal(formatBytes(512), "512 B");
  assert.equal(formatBytes(1536), "1.5 KB");
  assert.equal(formatBytes(3.4 * 1024 * 1024), "3.4 MB");
  assert.equal(formatBytes(-1), "0 B");
});

test("formatDate falls back to the raw value", () => {
  assert.equal(formatDate("2026-06-05T12:00:00Z").includes("2026"), true);
  assert.equal(formatDate("not a date"), "not a date");
  assert.equal(formatDate(null), "");
});

test("uploaderLine names the uploader, or is empty when unknown", () => {
  assert.equal(uploaderLine({ uploaded_by_name: "Amber Sims" }), "by Amber Sims");
  assert.equal(uploaderLine({ uploaded_by_name: "  " }), "");
  assert.equal(uploaderLine({ uploaded_by_name: null }), "");
  assert.equal(uploaderLine({}), "");
});

test("a raw body that still matches its fingerprint is the source", () => {
  const p = textProvenance({ text_origin: "raw", text_verified: true, extracted_text: "Well visit copay — $35.00" });
  assert.equal(p, "source");
  assert.equal(isVerbatimText(p), true);
  assert.equal(textProvenanceLabel(p), "Text as filed");
  assert.match(describeFileText(p), /exactly what was filed/);
});

test("a raw body that no longer matches is altered, never the original", () => {
  const p = textProvenance({ text_origin: "raw", text_verified: false, extracted_text: "Well visit copay — 5.00" });
  assert.equal(p, "altered");
  assert.equal(isVerbatimText(p), false);
  assert.equal(textProvenanceLabel(p), "Text changed after filing");
  assert.match(describeFileText(p), /no longer matches what was filed/);
  assert.doesNotMatch(describeFileText(p), /exactly what was filed/);
});

test("a raw body with no fingerprint cannot claim to be verbatim", () => {
  const p = textProvenance({ text_origin: "raw", extracted_text: "some text" });
  assert.equal(p, "altered");
  assert.equal(isVerbatimText(p), false);
});

test("extracted text is derived, and says so", () => {
  const p = textProvenance({ text_origin: "extracted", extracted_text: "OCR output" });
  assert.equal(p, "derived");
  assert.equal(isVerbatimText(p), false);
  assert.equal(textProvenanceLabel(p), "Text read from file");
  assert.match(describeFileText(p), /read from the file automatically/);
  assert.match(describeFileText(p), /not the original document/);
});

test("an old payload with no text_origin is treated as derived, not as the source", () => {
  // The column's default is 'extracted': a row that predates 072 claims nothing.
  assert.equal(textProvenance({ extracted_text: "text" }), "derived");
});

test("a file with no text has no marker and no claim", () => {
  assert.equal(textProvenance({ text_origin: "extracted" }), "none");
  assert.equal(textProvenance({ text_origin: "raw", text_verified: true }), "none");
  assert.equal(textProvenanceLabel("none"), null);
});
