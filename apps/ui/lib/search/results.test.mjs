// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
//
// Guards the two rules the Search page must never lose (#4561):
//
//  1. GROUPED + ROLE-GATED. A group the role cannot search does not exist, and
//     a hit of a hidden kind is not shown — "the UI must not show a section the
//     role cannot search, and must not reveal that filtered content exists"
//     (#4696, decisions/2026-11-15-role-policy.md). The housesitter and vendor
//     cases below are the ones the policy actually differs on.
//
//  2. CONTEXT PER ROW. A bill row carries its amount and its due state, a
//     document its uploader, a note its author — a bare title list is useless
//     (a person cannot judge "Electric" without the amount and whether it is
//     paid). These tests assert the facts that come back, and that a missing
//     record yields NO facts rather than a plausible-looking zero.
import { test } from "node:test";
import assert from "node:assert/strict";

import {
  GROUPS,
  areasLine,
  entityKindLabel,
  rowSnippet,
  billState,
  detailRoute,
  factsFor,
  formatShortDate,
  groupHits,
  groupOf,
  idleCopy,
  narrowGroups,
  noResultsCopy,
  searchHref,
  searchState,
  snippet,
  totalRows,
  visibleGroups,
} from "./results.ts";

const hit = (entity_type, entity_id, extra = {}) => ({
  entity_type,
  entity_id,
  title: `${entity_type} ${entity_id}`,
  body: "",
  ...extra,
});

// ── grouping ────────────────────────────────────────────────

test("hits are grouped by type in the fixed group order, not interleaved", () => {
  const groups = groupHits(
    [hit("bill", "b1"), hit("note", "n1"), hit("bill", "b2"), hit("attachment_missing", "x")],
    "owner",
  );
  assert.deepEqual(
    groups.map((g) => g.def.id),
    ["note", "bill"],
    "note and bill are in display order (notes before bills), each hit under its own heading",
  );
  assert.deepEqual(
    groups.find((g) => g.def.id === "bill").hits.map((h) => h.hit.entity_id),
    ["b1", "b2"],
    "ranking inside a group is the API's order",
  );
});

test("a hit with a file_id is a document whatever entity_type it carries", () => {
  // The files collection stores the ATTACHED entity's type: an OCR hit on a
  // pet's photo comes back as entity_type "pet".
  const ocr = hit("pet", "f1", { file_id: "f1", title: "IMG_0042.jpg" });
  assert.equal(groupOf(ocr).id, "document");
  const meta = hit("document", "f2", { file_id: "f2", title: "Roof invoice" });
  assert.equal(groupOf(meta).id, "document");
  assert.equal(groupOf(hit("pet", "p1")).id, "thing");
  assert.equal(groupOf(hit("vendor", "v1")).id, "contact");
  assert.equal(groupOf(hit("calendar_event", "e1")).id, "event");
  assert.equal(groupOf(hit("list", "l1")).id, "list");
  assert.equal(groupOf(hit("recipe", "r1")).id, "recipe");
  assert.equal(groupOf(hit("person", "pp1")).id, "person");
  assert.equal(groupOf(hit("nonsense", "z")), null, "an unknown type has no home and is dropped");
});

test("a file that matches twice (OCR chunk + metadata) is one row", () => {
  const groups = groupHits(
    [
      hit("document", "f1", { file_id: "f1", title: "Roof invoice" }),
      hit("property", "f1", { file_id: "f1", title: "IMG_9.jpg" }),
    ],
    "owner",
  );
  assert.equal(totalRows(groups), 1);
});

// ── the role gate ───────────────────────────────────────────

test("a group a role cannot search does not exist at all", () => {
  const owner = visibleGroups("owner").map((g) => g.id);
  for (const id of ["event", "note", "document", "bill", "person", "list", "recipe", "contact", "guide"]) {
    assert.ok(owner.includes(id), `owner sees ${id}`);
  }
  // Viewer: reads money and documents but no credentials.
  const viewer = visibleGroups("viewer").map((g) => g.id);
  assert.ok(viewer.includes("document"), "a viewer reads documents");
  assert.ok(viewer.includes("bill"), "a viewer reads bills");
  assert.ok(!viewer.includes("credential"), "a viewer never sees a Credentials heading");
  // Housesitter: calendar, lists, meals, people, pets, guides, contacts — no
  // documents, money, credentials, vehicles or the activity feed.
  const sitter = visibleGroups("housesitter").map((g) => g.id);
  assert.ok(sitter.includes("event") && sitter.includes("list") && sitter.includes("recipe"));
  assert.ok(
    !sitter.includes("document") && !sitter.includes("bill") && !sitter.includes("credential"),
    "no Documents, Bills or Credentials section for a housesitter",
  );
  // Vendor: properties/assets/maintenance only.
  const vendor = visibleGroups("vendor").map((g) => g.id);
  assert.deepEqual(vendor.sort(), ["thing", "upkeep"].sort());
});

test("a hit of a kind the role may not see is dropped even if the API lets it through", () => {
  const hits = [hit("bill", "b1"), hit("secret", "s1"), hit("document", "f1", { file_id: "f1" })];
  const owner = groupHits(hits, "owner");
  assert.deepEqual(owner.map((g) => g.def.id), ["document", "bill", "credential"]);
  const sitter = groupHits(hits, "housesitter");
  assert.deepEqual(sitter, [], "a housesitter is shown none of them, and no hint that any exists");
});

test("an unknown/loading role shows everything and never flickers for the common owner", () => {
  // Same choice as nav-config.canSeeSection: null role = still loading.
  assert.deepEqual(
    visibleGroups(null).map((g) => g.id),
    GROUPS.map((g) => g.id),
  );
});

test("counts come only from what is rendered (never a hidden-results number)", () => {
  const groups = groupHits([hit("bill", "b1"), hit("secret", "s1")], "viewer");
  assert.equal(totalRows(groups), 1);
  const text = JSON.stringify(groups);
  assert.ok(!/hidden/i.test(text), "no 'N hidden' anywhere in what a viewer would see");
});

// ── context per row ────────────────────────────────────────

const billGroup = GROUPS.find((g) => g.id === "bill");
const docGroup = GROUPS.find((g) => g.id === "document");
const noteGroup = GROUPS.find((g) => g.id === "note");

test("a bill row carries its amount and its due state, never an event-derived guess", () => {
  const facts = factsFor(
    billGroup,
    { amount: "182.4000", frequency: "monthly", due_day: 12, paid_date: null, paid_for_due_date: null },
    { today: "2026-11-10" },
  );
  const byLabel = Object.fromEntries(facts.map((f) => [f.label || "due", f.value]));
  assert.equal(byLabel.Amount, "$182.40");
  assert.equal(byLabel.due, "Due Thu", "Nov 12 2026 is a Thursday, two days out");
});

test("a paid bill says Paid, not a due date it no longer has", () => {
  const state = billState(
    { frequency: "monthly", due_day: 12, paid_date: "2026-11-09", paid_for_due_date: "2026-11-12" },
    { today: "2026-11-10" },
  );
  assert.equal(state.paid, true);
  assert.equal(state.dueLabel, "Paid");
});

test("a document row says whose it is and when, and names the uploader", () => {
  const facts = factsFor(docGroup, {
    category: "insurance",
    document_date: "2024-03-01",
    uploaded_by_name: "Amber Sims",
  });
  assert.deepEqual(
    facts.map((f) => `${f.label}=${f.value}`),
    ["Category=Insurance", "Dated=Mar 1, 2024", "Uploaded by=Amber Sims"],
  );
});

test("a note row names its author and what it is attached to", () => {
  const facts = factsFor(noteGroup, {
    author_name: "Amber Sims",
    updated_at: "2026-11-01T12:00:00Z",
    entity_type: "property",
  });
  assert.equal(facts[0].value, "Amber Sims");
  assert.equal(facts[2].label, "On a");
  // The kind, not the group: a note on a property is "on a property", never
  // "on a thing" — where search files a record is not what the record is.
  assert.equal(facts[2].value, "property");
});

test("no record, no facts — never a placeholder that reads like data", () => {
  assert.deepEqual(factsFor(billGroup, null), []);
  assert.deepEqual(factsFor(billGroup, {}, { today: "2026-11-10" }), [], "an empty record claims nothing");
  assert.deepEqual(
    factsFor(docGroup, { uploaded_by_name: "Amber Sims" }).map((f) => f.label),
    ["Uploaded by"],
  );
});

test("a document's date is read as a civil date, so no zone shifts it a day", () => {
  // The year is always there: "Nov 1" cannot tell this year's insurance card
  // from last year's, which is the whole reason a document row shows a date.
  assert.equal(formatShortDate("2026-11-01"), "Nov 1, 2026");
  assert.equal(formatShortDate("2025-12-31"), "Dec 31, 2025");
  assert.equal(formatShortDate(""), "");
});

test("the snippet is short, on a word boundary, and empty when the body is", () => {
  const long = "the electric bill went up again this month because of the new heat pump and the space heaters in the kids rooms";
  const s = snippet(long, 40);
  assert.ok(s.length <= 41, `kept short: ${s}`);
  assert.ok(s.endsWith("…"));
  assert.ok(!/\s…$/.test(s), "no dangling space before the ellipsis");
  assert.equal(snippet("   "), "");
});

// ── open behaviour ─────────────────────────────────────────

test("a result opens its record: a page when one exists, the modal otherwise", () => {
  assert.equal(detailRoute("bill", "b1"), "/dashboard/bills/b1");
  assert.equal(detailRoute("pet", "p1"), "/dashboard/pets/p1");
  assert.equal(detailRoute("note", "n1"), null, "notes have no page: the row opens the note itself");
  assert.equal(detailRoute("guide", "g1"), null);
});

// ── states ─────────────────────────────────────────────────

test("empty box and no-results are different states", () => {
  assert.equal(searchState({ query: "", loading: false, error: false, count: 0 }), "idle");
  assert.equal(searchState({ query: "   ", loading: false, error: false, count: 0 }), "idle");
  assert.equal(searchState({ query: "electric", loading: false, error: false, count: 0 }), "empty");
  assert.equal(searchState({ query: "electric", loading: false, error: false, count: 3 }), "results");
  assert.equal(searchState({ query: "electric", loading: true, error: false, count: 0 }), "loading");
  // A failed read is neither "nothing matched" nor an empty family.
  assert.equal(searchState({ query: "electric", loading: false, error: true, count: 0 }), "error");
});

test("the no-results copy names the query and what this role can search", () => {
  const owner = noResultsCopy("  electric bill ", "owner");
  assert.ok(owner.title.includes('"electric bill"'), owner.title);
  assert.ok(/fewer words/.test(owner.body));
  assert.ok(owner.body.includes("documents") && owner.body.includes("bills"));

  const sitter = noResultsCopy("electric", "housesitter");
  assert.ok(!/bills|documents|credentials/.test(sitter.body), "a housesitter is never told those areas exist");
  assert.ok(/lists|recipes|events/.test(sitter.body));
});

test("the idle copy tells a person what to type; it is never blank", () => {
  const idle = idleCopy("owner");
  assert.equal(idle.title, "Type to search");
  assert.ok(idle.body.length > 30);
  assert.ok(areasLine("owner").includes("documents"));
  assert.ok(areasLine("vendor").includes("upkeep"));
});

test("a note's parent is named by what it is, not by the group it is filed under", () => {
  assert.equal(entityKindLabel("property"), "property");
  assert.equal(entityKindLabel("pet"), "pet");
  assert.equal(entityKindLabel("maintenance_task"), "upkeep task");
  assert.equal(entityKindLabel(""), "");
  assert.equal(entityKindLabel("something_new"), "", "an unknown kind claims nothing");
});

test("a snippet that only repeats the facts is dropped; one that adds stays", () => {
  const billFacts = factsFor(billGroup, { amount: "182.40", category: "utilities" }, { today: "2026-11-10" });
  // The index body of a bill is its category, which the row already says.
  assert.equal(rowSnippet("utilities", billFacts), "");
  // A note's body adds what its facts (author, date) do not say.
  const noteFacts = factsFor(noteGroup, { author_name: "Amber Sims", entity_type: "property" });
  assert.match(rowSnippet("the electric panel is behind the fridge", noteFacts), /electric panel/);
  assert.equal(rowSnippet("", billFacts), "");
});

// ── narrowing ──────────────────────────────────────────────

test("narrowing to one area shows only that area, and never hides a real answer", () => {
  const groups = groupHits([hit("bill", "b1"), hit("note", "n1")], "owner");
  assert.deepEqual(
    narrowGroups(groups, "bill").map((g) => g.def.id),
    ["bill"],
  );
  // No filter: every group, in display order (notes before bills).
  assert.deepEqual(
    narrowGroups(groups, null).map((g) => g.def.id),
    ["note", "bill"],
  );
  // A filter left over from a previous query must not blank a new answer.
  assert.deepEqual(
    narrowGroups(groups, "document").map((g) => g.def.id),
    ["note", "bill"],
    "no rows in the filtered area: fall back to every group rather than showing nothing",
  );
});

// ── one URL for a query (#5303) ─────────────────────────────

test("a query has one URL, spelled one way, wherever it was typed", () => {
  // The drawer box, the desktop sidebar box and the Search page all link to the
  // same place, so a query typed in the drawer can be reloaded and shared.
  assert.equal(searchHref("electric"), "/dashboard/search?q=electric");
  // Trimmed, so a stray space from a phone keyboard is not part of the query.
  assert.equal(searchHref("  electric bill  "), "/dashboard/search?q=electric%20bill");
  // Encoded, so a query with & or # is still one query (not a second param).
  assert.equal(searchHref("salt & pepper"), "/dashboard/search?q=salt%20%26%20pepper");
  // An empty query is not a search: the parameter is dropped, not left empty.
  assert.equal(searchHref(""), "/dashboard/search");
  assert.equal(searchHref("   "), "/dashboard/search");
  assert.equal(searchHref(null), "/dashboard/search");
  assert.equal(searchHref(undefined), "/dashboard/search");
});
