// Run: npm test (node --test strips the TypeScript types natively).
//
// HOME-OS-5503: the reference (KB) list is grouped by path and the path picker
// must show exactly what the server will store. These are the rules that, if
// wrong, either hide a doc from its group or save it somewhere nobody looks.
// The Go side is the source of truth (apps/api/internal/kb/repo.go CleanPath and
// migration 076's CHECK), so the path cases below are copied from its own
// examples rather than invented here.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  authorLabel,
  canWriteKb,
  cleanPath,
  folderSuggestions,
  formatKbDate,
  groupByTop,
  humanizeSegment,
  kbIsDirty,
  kbWriteBody,
  matchesFilter,
  parentPath,
  parseTags,
  pathFromUrlSegments,
  pathPreview,
  pathWithinTop,
  replaceWarning,
  rowSummary,
  sizeLabel,
  slugFromTitle,
  slugSegment,
  topSegment,
  topSegments,
} from "./model.ts";

test("cleanPath tidies what a person types, and agrees with the API's examples", () => {
  // The API's own doc comment: "'How To/Router Reset' becomes
  // 'how-to/router-reset'".
  assert.deepEqual(cleanPath("How To/Router Reset"), { path: "how-to/router-reset" });
  assert.deepEqual(cleanPath("/how-to/pets/vet/"), { path: "how-to/pets/vet" });
  assert.deepEqual(cleanPath("  how-to/pets/vet  "), { path: "how-to/pets/vet" });
  // Runs of whitespace collapse to ONE dash, as repo.go slugSegment does.
  assert.deepEqual(cleanPath("how  to/the   cabin"), { path: "how-to/the-cabin" });
  // A dot, underscore and digit are allowed inside a segment.
  assert.deepEqual(cleanPath("how-to/router.v2_reset"), { path: "how-to/router.v2_reset" });
});

test("cleanPath refuses the paths the database's CHECK refuses", () => {
  assert.ok("error" in cleanPath(""));
  assert.ok("error" in cleanPath("   "));
  assert.ok("error" in cleanPath("how-to//router"), "a doubled slash is an empty segment");
  assert.ok("error" in cleanPath("how-to/../etc"), "a parent segment is never a path");
  assert.ok("error" in cleanPath("how-to/."), "a lone dot is not a segment");
  assert.ok("error" in cleanPath("how to/reset!"), "a '!' is outside the CHECK's characters");
  assert.ok("error" in cleanPath("How/to/Résumé"), "non-ASCII is refused, not silently dropped");
});

test("slugFromTitle makes a usable segment, and '' when it cannot", () => {
  assert.equal(slugFromTitle("Reset the router"), "reset-the-router");
  assert.equal(slugFromTitle("  Vet after-hours (urgent)  "), "vet-after-hours-urgent");
  assert.equal(slugFromTitle("Wi-Fi!"), "wi-fi");
  assert.equal(slugFromTitle("!!!"), "", "no usable characters: the caller must ask for a path");
  assert.equal(slugFromTitle(""), "");
});

test("slugSegment lower-cases and dashes whitespace, keeping everything else", () => {
  assert.equal(slugSegment(" VET After Hours "), "vet-after-hours");
  assert.equal(slugSegment("a\tb\nc"), "a-b-c");
  assert.equal(slugSegment("Router_Reset.v2"), "router_reset.v2");
});

test("pathFromUrlSegments is the doc's address, or null when the URL is not one", () => {
  assert.equal(pathFromUrlSegments(["how-to", "pets", "vet"]), "how-to/pets/vet");
  assert.equal(pathFromUrlSegments(["How%20To"]), "how-to");
  assert.equal(pathFromUrlSegments([]), null);
  assert.equal(pathFromUrlSegments(null), null);
  assert.equal(pathFromUrlSegments(["how-to", "..", "etc"]), null);
  assert.equal(pathFromUrlSegments(["%E0%A4%A"]), null, "a broken escape is null, not a throw");
});

test("topSegment / pathWithinTop / parentPath split an address the way the list groups it", () => {
  assert.equal(topSegment("how-to/pets/vet"), "how-to");
  assert.equal(topSegment("/how-to/pets/vet"), "how-to");
  assert.equal(topSegment("wifi"), "wifi");
  assert.equal(pathWithinTop("how-to/pets/vet"), "pets/vet");
  assert.equal(pathWithinTop("wifi"), "");
  assert.equal(parentPath("how-to/pets/vet"), "how-to/pets");
  assert.equal(parentPath("wifi"), "");
});

test("humanizeSegment turns a slug into a heading", () => {
  assert.equal(humanizeSegment("how-to"), "How to");
  assert.equal(humanizeSegment("wifi"), "Wi-Fi");
  assert.equal(humanizeSegment("after-hours"), "After hours");
  assert.equal(humanizeSegment("vet"), "Vet");
  assert.equal(humanizeSegment("hvac"), "HVAC");
});

/** A list row, with only the fields groupByTop reads made explicit. */
function row(path, over = {}) {
  return { path, title: path, tags: [], excerpt: "", updated_at: "2026-11-01T00:00:00Z", ...over };
}

test("groupByTop groups by the first segment, ordered by label, docs by path", () => {
  const groups = groupByTop([
    row("how-to/pets/vet"),
    row("money/cards"),
    row("how-to/network/router"),
    row("how-to/appliances/dishwasher"),
  ]);
  assert.deepEqual(
    groups.map((g) => g.label),
    ["How to", "Money"],
  );
  assert.deepEqual(
    groups[0].docs.map((d) => d.path),
    ["how-to/appliances/dishwasher", "how-to/network/router", "how-to/pets/vet"],
  );
  assert.equal(groups[0].top, "how-to");
  // An empty list is no groups, not one empty group: the page draws its own
  // empty state and must not have to filter a phantom heading.
  assert.deepEqual(groupByTop([]), []);
});

test("groupByTop keeps the API's ranking inside a group when the hits are ranked", () => {
  const groups = groupByTop([row("how-to/zebra"), row("how-to/apple")], true);
  assert.deepEqual(
    groups[0].docs.map((d) => d.path),
    ["how-to/zebra", "how-to/apple"],
    "a meaning search's best-first order is the result; re-sorting it loses the answer",
  );
});

test("folderSuggestions lists every folder already in use, once", () => {
  assert.deepEqual(
    folderSuggestions([row("how-to/pets/vet"), row("how-to/pets/food"), row("how-to/network/router"), row("money")]),
    ["how-to", "how-to/network", "how-to/pets"],
  );
  assert.deepEqual(folderSuggestions([]), []);
});

test("topSegments offers each top-level folder once, in order", () => {
  assert.deepEqual(topSegments([row("money/b"), row("how-to/a"), row("how-to/c")]), ["how-to", "money"]);
});

test("matchesFilter narrows a loaded list by path, title and tags — every word must hit", () => {
  const doc = row("how-to/pets/vet", { title: "After-hours vet", tags: ["urgent"] });
  assert.equal(matchesFilter(doc, ""), true);
  assert.equal(matchesFilter(doc, "vet"), true);
  assert.equal(matchesFilter(doc, "AFTER hours"), true);
  assert.equal(matchesFilter(doc, "urgent vet"), true);
  assert.equal(matchesFilter(doc, "urgent router"), false);
  assert.equal(matchesFilter(doc, "how-to"), true, "the path is searchable, it is the doc's address");
});

test("rowSummary strips the markdown an excerpt still carries", () => {
  assert.equal(rowSummary({ excerpt: "## After hours\n\nCall the clinic.\n" }), "After hours Call the clinic.");
  assert.equal(rowSummary({ excerpt: "- one\n- two" }), "one two");
  assert.equal(rowSummary({ excerpt: "```\npassword: hunter2\n```\nThe sticker." }), "The sticker.");
  assert.equal(rowSummary({ excerpt: "  \n\n " }), null);
});

test("formatKbDate is a UTC date, so it is the same day everywhere", () => {
  assert.equal(formatKbDate("2026-11-18T23:30:00Z"), "Nov 18, 2026");
  assert.equal(formatKbDate("2026-11-18T00:30:00Z"), "Nov 18, 2026");
  assert.equal(formatKbDate(null), null);
  assert.equal(formatKbDate("not a date"), null);
});

test("authorLabel never invents an author", () => {
  assert.equal(authorLabel({ updated_by: "Jacob" }), "Jacob");
  assert.equal(authorLabel({ updated_by: "  " }), null);
  assert.equal(authorLabel({ updated_by: null }), null);
  assert.equal(authorLabel({}), null);
});

test("canWriteKb is the API's ResKB write grant: family roles only", () => {
  for (const role of ["owner", "family_manager", "family_member"]) {
    assert.equal(canWriteKb(role), true, role);
  }
  for (const role of ["viewer", "housesitter", "vendor", "superuser"]) {
    assert.equal(canWriteKb(role), false, `${role} reads the KB and must not see write controls`);
  }
  // Still loading: show, and let the API refuse (the guides pages' rule).
  assert.equal(canWriteKb(null), true);
  assert.equal(canWriteKb(undefined), true);
});

test("kbWriteBody sends every field, trimmed, with tags parsed", () => {
  assert.deepEqual(kbWriteBody({ title: "  Router  ", body: "## Reset\n", tags: "Wifi, router , wifi" }), {
    title: "Router",
    body: "## Reset\n",
    tags: ["wifi", "router"],
  });
  assert.deepEqual(kbWriteBody({ title: "x", body: "", tags: "" }), { title: "x", body: "", tags: [] });
  // An empty title is sent as "" and the API refuses it: the form validates
  // first, and a client that "fixes" it would be inventing a title.
  assert.equal(kbWriteBody({ title: "   ", body: "b", tags: [] }).title, "");
});

test("parseTags lower-cases, trims and dedupes, keeping the first order", () => {
  assert.deepEqual(parseTags("Wifi, router , WIFI ,, "), ["wifi", "router"]);
  assert.deepEqual(parseTags(""), []);
  assert.deepEqual(parseTags(["A", "b", "a"]), ["a", "b"]);
});

test("kbIsDirty: an untouched form is not dirty, a changed field is", () => {
  const doc = { title: "Router", body: "Reset it.", tags: ["wifi"] };
  assert.equal(kbIsDirty(doc, { title: "Router", body: "Reset it.", tags: "wifi" }), false);
  assert.equal(kbIsDirty(doc, { title: " Router ", body: "Reset it.", tags: "wifi" }), false, "whitespace is not a change");
  assert.equal(kbIsDirty(doc, { title: "Router", body: "Reset it.", tags: "wifi, router" }), true);
  assert.equal(kbIsDirty(doc, { title: "Router 2", body: "Reset it.", tags: "wifi" }), true);
  assert.equal(kbIsDirty(doc, { title: "Router", body: "", tags: "wifi" }), true, "clearing the body is a change");
  assert.equal(kbIsDirty(null, { title: "", body: "", tags: "" }), true, "a new doc is always a change");
});

test("replaceWarning names the path and says the old doc is gone", () => {
  const text = replaceWarning("how-to/network/router", "Reset the router");
  assert.match(text, /REPLACES the doc already at how-to\/network\/router/);
  assert.match(text, /Reset the router/);
  assert.match(text, /cannot be brought back/);
  // An untitled existing doc must still read as a warning, not as a blank.
  assert.match(replaceWarning("money/tax", null), /money\/tax/);
  assert.match(replaceWarning("money/tax", "  "), /REPLACES/);
});

test("pathPreview joins a folder and a slug the way the save will store them", () => {
  assert.deepEqual(pathPreview("how-to/pets", "Vet After Hours"), { path: "how-to/pets/vet-after-hours" });
  assert.deepEqual(pathPreview("", "money/tax"), { path: "money/tax" });
  assert.deepEqual(pathPreview("/how-to/", "/vet/"), { path: "how-to/vet" });
  assert.ok("error" in pathPreview("how-to", ""));
});

test("sizeLabel counts characters, and says so in the singular when it is one", () => {
  assert.equal(sizeLabel("abc"), "3 characters");
  assert.equal(sizeLabel("a"), "1 character");
  assert.equal(sizeLabel(""), "0 characters");
  assert.equal(sizeLabel("x".repeat(1204)), "1,204 characters");
});
