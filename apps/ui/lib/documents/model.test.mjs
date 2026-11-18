// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  CATEGORIES,
  CATEGORY_LABELS,
  CATEGORY_SINGULAR,
  EXPIRING_WINDOW_DAYS,
  UNFILED_CHIP_LABEL,
  categoryCounts,
  categoryOf,
  cleanTags,
  daysBetween,
  displayTitle,
  documentCategoryLabel,
  expiryOf,
  expiringSoon,
  fileKind,
  filterDocuments,
  formatDay,
  formatSize,
  isExpiring,
  isFiled,
  parseDay,
  populatedCategories,
  rowMeta,
  shortDay,
  tagCloud,
  textState,
  todayString,
  unfiledDocuments,
  uploaderName,
} from "./model.ts";

/** A file row with the fields the model reads. */
function file(over = {}) {
  return {
    id: "f1",
    household_id: "h1",
    blob_id: "b1",
    name: "IMG_4471.pdf",
    content_type: "application/pdf",
    size: 184320,
    ocr_status: "done",
    created_at: "2026-11-12T09:00:00Z",
    updated_at: "2026-11-12T09:00:00Z",
    category: "receipt",
    ...over,
  };
}

// ─── Dates ───────────────────────────────────────────────────

test("parseDay only accepts a real YYYY-MM-DD day", () => {
  assert.deepEqual(parseDay("2026-12-01"), { y: 2026, m: 12, d: 1 });
  for (const bad of [null, undefined, "", "2026-12", "12/01/2026", "2026-13-01", "2026-00-10", "2026-12-40", " 2026-12-01x"]) {
    assert.equal(parseDay(bad), null, String(bad));
  }
});

test("daysBetween counts whole calendar days and never shifts through a zone", () => {
  assert.equal(daysBetween("2026-11-17", "2026-12-01"), 14);
  assert.equal(daysBetween("2026-12-01", "2026-11-17"), -14);
  assert.equal(daysBetween("2026-11-17", "2026-11-17"), 0);
  // Across a DST change in most western zones: still 2 days, not 1.958.
  assert.equal(daysBetween("2026-03-07", "2026-03-09"), 2);
  assert.equal(daysBetween("2026-12-31", "2027-01-01"), 1);
  assert.equal(daysBetween("nope", "2026-01-01"), null);
});

test("formatDay and shortDay never go through a Date for a day string", () => {
  assert.equal(formatDay("2026-12-01"), "Dec 1, 2026");
  assert.equal(formatDay("2026-01-31"), "Jan 31, 2026");
  assert.equal(formatDay("2026-12-01T23:30:00Z"), "", "a day column is not a timestamp");
  assert.equal(formatDay(null), "");
  assert.equal(shortDay("2026-11-12"), "Nov 12");
  assert.equal(shortDay("2026-11-12T23:30:00-05:00"), "Nov 12");
  assert.equal(shortDay(null), "");
});

test("todayString is a local calendar day", () => {
  assert.equal(todayString(new Date(2026, 10, 18, 0, 5)), "2026-11-18");
  assert.equal(todayString(new Date(2026, 0, 3, 23, 55)), "2026-01-03");
});

// ─── Expiry ──────────────────────────────────────────────────

const TODAY = "2026-11-18";

test("expiryOf states the relative phrase, with the date it is relative to", () => {
  assert.equal(expiryOf({ expires_on: "2026-12-02" }, TODAY).label, "Expires in 14 days");
  assert.deepEqual(expiryOf({ expires_on: "2026-12-02" }, TODAY).state, "soon");
  assert.equal(expiryOf({ expires_on: "2026-11-19" }, TODAY).label, "Expires in 1 day");
  assert.equal(expiryOf({ expires_on: "2026-11-18" }, TODAY).label, "Expires today");
  assert.deepEqual(expiryOf({ expires_on: "2026-11-18" }, TODAY).state, "soon");
});

test("an expired document says so, in days, and is its own state", () => {
  const e = expiryOf({ expires_on: "2026-11-12" }, TODAY);
  assert.equal(e.state, "expired");
  assert.equal(e.days, -6);
  assert.equal(e.label, "Expired 6 days ago");
  assert.equal(expiryOf({ expires_on: "2026-11-17" }, TODAY).label, "Expired 1 day ago");
});

test("no expiry, or far away: nothing alarming", () => {
  assert.deepEqual(expiryOf({ expires_on: null }, TODAY), { state: "none", days: null, label: "" });
  const far = expiryOf({ expires_on: "2027-06-01" }, TODAY);
  assert.equal(far.state, "later");
  assert.equal(far.label, "Expires Jun 1, 2027");
});

test("the expiring window is inclusive and past dates count", () => {
  assert.equal(EXPIRING_WINDOW_DAYS, 45);
  assert.equal(isExpiring({ expires_on: "2026-12-02" }, TODAY), true);
  assert.equal(isExpiring({ expires_on: "2027-01-02" }, TODAY), true, "day 45 is inside the window");
  assert.equal(isExpiring({ expires_on: "2027-01-03" }, TODAY), false);
  assert.equal(isExpiring({ expires_on: "2020-01-01" }, TODAY), true, "expired is urgent");
  assert.equal(isExpiring({ expires_on: null }, TODAY), false);
});

test("expiringSoon orders by date, so the already-expired comes first", () => {
  const rows = [
    { id: "a", expires_on: "2026-12-05" },
    { id: "b", expires_on: "2026-11-10" },
    { id: "c", expires_on: "2027-06-01" },
    { id: "d", expires_on: "2026-11-20" },
    { id: "e", expires_on: null },
  ];
  assert.deepEqual(expiringSoon(rows, TODAY).map((r) => r.id), ["b", "d", "a"]);
});

// ─── Text provenance markers (the reason this module exists) ─

test("textState strips the worker's retry marker and says it is retrying (#4419)", () => {
  const s = textState({
    ocr_status: "failed",
    ocr_error: "[retry 2/5 next=2026-11-15T21:00:00Z] tika: connection refused",
    extracted_text: null,
  });
  assert.equal(s.kind, "retrying");
  assert.equal(s.attempt, 2);
  assert.equal(s.max, 5);
  assert.equal(s.error, "tika: connection refused");
  assert.ok(!s.note.includes("[retry"), "the raw marker never reaches a person");
  assert.match(s.note, /attempt 2 of 5/);
  assert.match(s.note, /trying again/i);
});

test("textState reports a final failure without the worker's prefix", () => {
  const s = textState({ ocr_status: "failed", ocr_error: "failed after 6 attempts: tika down", extracted_text: null });
  assert.equal(s.kind, "failed");
  assert.equal(s.error, "tika down");
  assert.ok(!s.note.includes("failed after"), "the prefix is not shown as-is");
  assert.match(s.note, /after 6 attempts/);
  assert.match(s.note, /downloadable/, "a reading failure never implies the file is lost");
});

test("textState: a bare failure error is shown, an unparseable retry time is dropped", () => {
  const bare = textState({ ocr_status: "failed", ocr_error: "boom", extracted_text: null });
  assert.equal(bare.kind, "failed");
  assert.equal(bare.error, "boom");
  const badTime = textState({ ocr_status: "failed", ocr_error: "[retry 1/3 next=soon] boom", extracted_text: null });
  assert.equal(badTime.kind, "retrying");
  assert.ok(!badTime.note.includes("after ."));
});

test("textState covers pending, done, skipped and done-but-empty", () => {
  assert.equal(textState({ ocr_status: "pending", extracted_text: null }).kind, "pending");
  assert.equal(textState({ ocr_status: "processing", extracted_text: null }).kind, "pending");
  assert.equal(textState({ ocr_status: "done", extracted_text: "hello" }).kind, "done");
  assert.match(textState({ ocr_status: "done", extracted_text: "" }).note, /found no text/);
  assert.equal(textState({ ocr_status: "skipped", extracted_text: null }).kind, "none");
});

// ─── Titles, kinds, facts ────────────────────────────────────

test("displayTitle prefers the metadata title and never invents one", () => {
  assert.equal(displayTitle({ title: "Odyssey policy renewal", name: "IMG_4471.pdf" }), "Odyssey policy renewal");
  assert.equal(displayTitle({ title: "   ", name: "IMG_4471.pdf" }), "IMG_4471.pdf");
  assert.equal(displayTitle({ title: null, name: "IMG_4471.pdf" }), "IMG_4471.pdf");
});

test("categoryOf defaults to other, the column's own default", () => {
  assert.equal(categoryOf({ category: "insurance" }), "insurance");
  assert.equal(categoryOf({ category: "" }), "other");
  assert.equal(categoryOf({}), "other");
  assert.equal(categoryOf({ category: "not-a-category" }), "other");
  assert.deepEqual([...CATEGORIES].slice(0, 3), ["receipt", "tax", "insurance"]);
  assert.equal(CATEGORIES.at(-1), "other", "other is last, like the mockup's picker");
});

test("fileKind and formatSize say what a person recognises", () => {
  assert.equal(fileKind("application/pdf", "x.pdf"), "PDF");
  assert.equal(fileKind("image/jpeg", "x.jpg"), "Photo");
  assert.equal(fileKind("image/png", "x.png"), "Image");
  assert.equal(fileKind("text/csv", "x.csv"), "Text file");
  assert.equal(fileKind("application/vnd.ms-excel", "x.xls"), "Spreadsheet");
  assert.equal(fileKind("", "scan.HEIC"), "HEIC");
  assert.equal(fileKind("", "noext"), "File");
  assert.equal(formatSize(0), "0 B");
  assert.equal(formatSize(512), "512 B");
  assert.equal(formatSize(184320), "180.0 KB");
  assert.equal(formatSize(3.4 * 1024 * 1024), "3.4 MB");
});

test("rowMeta names the file, its size, its day and who filed it", () => {
  const meta = rowMeta(file({ uploaded_by_name: "Amber Sims", document_date: "2026-11-10" }));
  assert.equal(meta, "PDF · 180.0 KB · Nov 10 · by Amber Sims");
  // No uploader: no empty "by".
  assert.equal(uploaderName({ uploaded_by_name: null }), "");
  assert.equal(uploaderName({ uploaded_by_name: "  " }), "");
  assert.ok(!rowMeta(file()).includes("by "));
});

// ─── Counts ──────────────────────────────────────────────────

test("categoryCounts counts, flags expiring and remembers the newest", () => {
  const files = [
    file({ id: "a", category: "receipt", created_at: "2026-11-10T00:00:00Z" }),
    file({ id: "b", category: "receipt", created_at: "2026-11-12T00:00:00Z", title: "Costco" }),
    file({ id: "c", category: "insurance", created_at: "2026-11-01T00:00:00Z", expires_on: "2026-11-20" }),
    file({ id: "d", category: "", created_at: "2026-10-01T00:00:00Z" }),
  ];
  const counts = categoryCounts(files, TODAY);
  assert.equal(counts.get("receipt").count, 2);
  assert.equal(counts.get("receipt").latest.title, "Costco", "newest first, so the newest is latest");
  assert.equal(counts.get("insurance").expiring, 1);
  assert.equal(
    counts.get("other").count,
    0,
    "a row with NO category is not an Other document — Other is a category, and nothing was filed as it (#5299)",
  );
  assert.equal(counts.get("tax").count, 0);
});

test("populatedCategories is the display order, only what has documents", () => {
  const files = [file({ category: "home" }), file({ category: "receipt" }), file({ category: "receipt" })];
  const cats = populatedCategories(files, TODAY);
  assert.deepEqual(cats.map((c) => c.category), ["receipt", "home"]);
  assert.deepEqual(cats.map((c) => c.count), [2, 1]);
  assert.deepEqual(populatedCategories([], TODAY), [], "an empty family gets no tiles");
});

// ─── Filed or not: the ONE rule (#5299) ─────────────────────
//
// The bug: the hub's "Not filed yet" count treated category "other" as
// uncategorised, while the card printed "Other" from a separate label map. One
// document was counted as unfiled AND displayed with a category. These tests
// pin the rule in one place and pin the counter and the card to it, so no future
// change can give them different answers.

test("a document filed as Other is FILED — Other is a category, not a lack of one", () => {
  // The ticket's own case: a card showing "Other".
  assert.equal(isFiled(file({ category: "other" })), true);
  assert.equal(documentCategoryLabel(file({ category: "other" })), "Other");
  // Every real category is filed, by the same rule.
  for (const category of CATEGORIES) {
    assert.equal(isFiled(file({ category })), true, `${category} is a category, so its document is filed`);
  }
  // Only a payload with no category at all is unfiled, and the old bug counted
  // that row as "other".
  for (const category of [undefined, null, "", "not-a-category"]) {
    assert.equal(isFiled(file({ category })), false, JSON.stringify(category));
    assert.equal(
      documentCategoryLabel(file({ category })),
      UNFILED_CHIP_LABEL,
      "an unfiled card must not borrow Other's name — that was the contradiction",
    );
  }
  assert.equal(UNFILED_CHIP_LABEL, "No category");
});

test("the 'Not filed yet' count and the card's category can never disagree (#5299)", () => {
  // The reproduction, one document per shape a real household has: an insurance
  // policy, a document filed as Other, and one with no category at all.
  const other = file({ id: "o", category: "other", created_at: "2026-11-14T00:00:00Z" });
  const insurance = file({ id: "i", category: "insurance", created_at: "2026-11-13T00:00:00Z" });
  const bare = file({ id: "b", category: "", created_at: "2026-11-12T00:00:00Z" });
  const files = [other, insurance, bare];

  const unfiled = unfiledDocuments(files);
  const counts = categoryCounts(files, TODAY);

  // THE TICKET'S SYMPTOM, asserted first: no document may be counted as "not
  // filed yet" while its own card shows a category (Amber saw "Not filed yet
  // 1" over a card reading "Other").
  for (const f of files) {
    const countedAsUnfiled = unfiled.includes(f);
    const showsCategory = documentCategoryLabel(f) !== UNFILED_CHIP_LABEL;
    assert.equal(
      countedAsUnfiled,
      !showsCategory,
      `${f.id}: counted as not-filed=${countedAsUnfiled} but its card shows "${documentCategoryLabel(f)}" — the count and the card must agree`,
    );
  }

  // And the counted set is exactly the documents with no category at all.
  assert.deepEqual(unfiled.map((f) => f.id), ["b"], "only the document with no category is not filed yet");

  // The Other tile is what a household sees when a document was filed as Other,
  // and it holds exactly that document — not the uncategorised one.
  assert.equal(counts.get("other").count, 1, "the document filed as Other has an Other tile");
  assert.deepEqual(
    populatedCategories(files, TODAY).map((c) => c.category),
    ["insurance", "other"],
    "the uncategorised document contributes no tile; Other does",
  );
  // The pre-#5299 shape: `other: 2` (the real Other PLUS the uncategorised one),
  // which is how the card showing "Other" got counted as unfiled.
  const wouldHaveBeenCountedAsOther = files.filter((f) => categoryOf(f) === "other");
  assert.equal(wouldHaveBeenCountedAsOther.length, 2, "the old rule would have merged these two — the bug");
});

test("the Other tile is named Other, so a tile and a card never use two names for it", () => {
  // The tile heading and the card chip are the same word for the same value.
  assert.equal(CATEGORY_LABELS.other, CATEGORY_SINGULAR.other);
  assert.notEqual(CATEGORY_LABELS.other, UNFILED_CHIP_LABEL, "the tile for Other is not the unfiled group's name");
});

// ─── Filtering and tags ─────────────────────────────────────

test("filterDocuments narrows by category, person and words — and is not a search", () => {
  const files = [
    file({ id: "a", category: "receipt", title: "Costco water heater", person_id: "p1", tags: ["utility"] }),
    file({ id: "b", category: "insurance", title: "Odyssey policy", person_id: "p2" }),
  ];
  assert.deepEqual(filterDocuments(files, { category: "receipt" }).map((f) => f.id), ["a"]);
  assert.deepEqual(filterDocuments(files, { personId: "p2" }).map((f) => f.id), ["b"]);
  assert.deepEqual(filterDocuments(files, { q: "water" }).map((f) => f.id), ["a"]);
  assert.deepEqual(filterDocuments(files, { q: "utility" }).map((f) => f.id), ["a"], "a tag matches");
  assert.deepEqual(filterDocuments(files, { q: "car" }).map((f) => f.id), [], "no synonym guessing: that is search's job");
  assert.deepEqual(filterDocuments(files, {}).map((f) => f.id), ["a", "b"]);
});

test("cleanTags trims, drops blanks and keeps order without duplicates", () => {
  assert.deepEqual(cleanTags([" auto ", "auto", "", "  ", "utility"]), ["auto", "utility"]);
  assert.deepEqual(cleanTags(null), []);
});

test("tagCloud is most-used first, then alphabetical, capped", () => {
  const files = [file({ tags: ["b", "a"] }), file({ tags: ["a", "c"] }), file({ tags: ["a"] })];
  assert.deepEqual(tagCloud(files), ["a", "b", "c"]);
  assert.deepEqual(tagCloud(files, 2), ["a", "b"]);
});
