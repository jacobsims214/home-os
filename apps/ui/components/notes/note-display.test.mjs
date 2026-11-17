// Run: npm test. Authorship and the duplicate prompt are the two things the
// war game said a notes UI must not get wrong (#4564, #4945).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  COLLAPSED_ROWS,
  authorInitial,
  formatNoteDate,
  isCollapsible,
  noteAuthor,
  titleHint,
  visibleNotes,
} from "./note-display.ts";
import { duplicateQuestion, normalizeWriteResult } from "../../lib/note-write.ts";

const note = (id, extra = {}) => ({
  id,
  entity_type: "pet",
  entity_id: "p1",
  title: null,
  body: "body",
  author_name: "Amber Sims",
  created_at: "2026-11-16T12:00:00Z",
  ...extra,
});

test("noteAuthor names the author, and says Former member rather than nothing", () => {
  assert.equal(noteAuthor({ author_name: "Amber Sims" }), "Amber Sims");
  assert.equal(noteAuthor({ author_name: "  " }), "Former member");
  assert.equal(noteAuthor({ author_name: null }), "Former member");
  assert.equal(noteAuthor({}), "Former member");
});

test("authorInitial", () => {
  assert.equal(authorInitial({ author_name: "amber" }), "A");
  assert.equal(authorInitial({ author_name: null }), "?");
});

test("formatNoteDate", () => {
  assert.match(formatNoteDate("2026-11-16T12:00:00Z"), /2026/);
  assert.equal(formatNoteDate("nope"), "nope");
  assert.equal(formatNoteDate(null), "");
});

test("a long list collapses at COLLAPSED_ROWS and expands", () => {
  const many = Array.from({ length: COLLAPSED_ROWS + 3 }, (_, i) => note(`n${i}`));
  assert.equal(visibleNotes(many, false).length, COLLAPSED_ROWS);
  assert.equal(visibleNotes(many, true).length, many.length);
  assert.equal(isCollapsible(many), true);
  assert.equal(isCollapsible(many.slice(0, COLLAPSED_ROWS)), false);
});

test("the title hint explains why a title is worth filling in", () => {
  assert.match(titleHint(), /similar note/);
});

test("normalizeWriteResult turns a missing possible_duplicates into an empty list", () => {
  const saved = note("n1");
  assert.deepEqual(normalizeWriteResult({ data: saved }).possible_duplicates, []);
  const withCandidates = normalizeWriteResult({
    data: saved,
    possible_duplicates: [
      { id: "x", title: "Ava's school", updated_at: "", updated_local_date: "2026-11-16", excerpt: "", title_similarity: 0.8 },
    ],
  });
  assert.equal(withCandidates.possible_duplicates.length, 1);
});

test("the duplicate question names the candidate and offers both choices", () => {
  const q = duplicateQuestion({
    id: "x",
    title: "Ava's school — teacher & pickup rules",
    updated_at: "",
    updated_local_date: "2026-11-16",
    excerpt: "",
    title_similarity: 0.8,
  });
  assert.match(q, /Ava's school — teacher & pickup rules/);
  assert.match(q, /2026-11-16/);
  assert.match(q, /Update that one, or keep both\?/);
});
