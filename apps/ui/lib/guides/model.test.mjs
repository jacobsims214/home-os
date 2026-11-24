// Run: npm test (node --test strips the TypeScript types natively).
//
// The guides reading view (#4570) rests on one parser, so its rules are the
// rules a reader sees. These are the ones that would silently change what a
// guide SAYS — splitting steps wrong, swallowing a code block, counting a
// guide as empty — rather than CSS.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  currentRevisionId,
  formatGuideDate,
  guideIsDirty,
  guideSaveBody,
  guideStepCount,
  guideSubtitle,
  isRestoreNote,
  matchesSearch,
  parseGuideSteps,
  restoreWarning,
  restoredRevisionId,
  revisionAuthor,
  revisionSizeLabel,
  revisionWhen,
  splitTags,
  stepCountLabel,
} from "./model.ts";

test("a heading starts a step, and the leading h1 title is dropped", () => {
  const steps = parseGuideSteps("# Reset the router\n\n## Unplug it\n\nWait 30 seconds.\n\n## Plug it back\n\nDone.");
  assert.equal(steps.length, 2, "the guide's own title is not a step");
  assert.equal(steps[0].heading, "Unplug it");
  assert.deepEqual(steps[0].blocks, [{ kind: "p", text: "Wait 30 seconds." }]);
  assert.equal(steps[1].heading, "Plug it back");
});

test("a bare numbered list is one step per item — that is what 'step by step' means", () => {
  const steps = parseGuideSteps("1. Shut the valve.\n2. Open the taps.\n3. Run the hose.");
  assert.equal(steps.length, 3);
  assert.equal(steps[0].heading, null);
  assert.deepEqual(steps[0].blocks, [{ kind: "p", text: "Shut the valve." }]);
  assert.equal(steps[2].blocks[0].text, "Run the hose.");
});

test("a body with no headings is ONE step, and an empty body is none", () => {
  assert.equal(guideStepCount("Pull the plug and wait."), 1);
  assert.equal(guideStepCount(""), 0);
  assert.equal(guideStepCount(null), 0);
  assert.equal(guideStepCount(undefined), 0);
  assert.deepEqual(parseGuideSteps("   \n\n  "), []);
});

test("a fenced code block is kept verbatim, indentation included, and never becomes a paragraph", () => {
  const steps = parseGuideSteps("## Serial\n\n```\n  SN 4f9a2c1e\n    indented\n```");
  assert.equal(steps.length, 1);
  const code = steps[0].blocks.find((b) => b.kind === "code");
  assert.ok(code, "the block is a code block");
  assert.equal(code.text, "  SN 4f9a2c1e\n    indented");
  assert.equal(code.lang, null);
});

test("an unclosed fence is still content: a half-written guide shows what was written", () => {
  const steps = parseGuideSteps("## Copy this\n\n```\nSN 1234\nstill going");
  const code = steps[0].blocks.find((b) => b.kind === "code");
  assert.ok(code);
  assert.match(code.text, /SN 1234\nstill going/);
});

test("a code block's language label is kept, and a ``` inside it closes it", () => {
  const steps = parseGuideSteps("```bash\nssh cabin.local\n```\n\nThen reboot.");
  assert.deepEqual(steps[0].blocks[0], { kind: "code", text: "ssh cabin.local", lang: "bash" });
  assert.deepEqual(steps[0].blocks[1], { kind: "p", text: "Then reboot." });
});

test("bullets and numbers are lists, and a line after one starts a new paragraph", () => {
  const steps = parseGuideSteps("## Lights\n\n- Internet: solid green\n- DSL: blinking is the line\n\nCall the ISP.");
  assert.deepEqual(steps[0].blocks[0], { kind: "ul", items: ["Internet: solid green", "DSL: blinking is the line"] });
  assert.deepEqual(steps[0].blocks[1], { kind: "p", text: "Call the ISP." });
});

test("stepCountLabel never says '1 steps', and an empty guide says so", () => {
  assert.equal(stepCountLabel(0), "No steps yet");
  assert.equal(stepCountLabel(1), "1 step");
  assert.equal(stepCountLabel(4), "4 steps");
});

test("search matches the title, the summary, the tags and the BODY, every word", () => {
  const guide = {
    title: "Reset the router",
    summary: null,
    body: "The wifi drops when the lights are red.",
    tags: ["network"],
  };
  assert.equal(matchesSearch(guide, ""), true, "an empty search shows everything");
  assert.equal(matchesSearch(guide, "   "), true);
  assert.equal(matchesSearch(guide, "wifi"), true, "a word only in the body still finds it");
  assert.equal(matchesSearch(guide, "network"), true, "a tag finds it");
  assert.equal(matchesSearch(guide, "router wifi"), true, "every word must match, in any field");
  assert.equal(matchesSearch(guide, "router cabin"), false, "one missing word excludes it");
  assert.equal(matchesSearch(guide, "ROUTER"), true, "case does not matter");
});

test("the subtitle falls back to the body's first real line, skipping markup", () => {
  assert.equal(guideSubtitle({ summary: "  When the wifi drops.  ", body: "x" }), "When the wifi drops.");
  assert.equal(guideSubtitle({ summary: "", body: "# Title\n\n## Step one\n\nPull the plug." }), "Title");
  assert.equal(guideSubtitle({ summary: null, body: "- Shut the valve.\n- Open the taps." }), "Shut the valve.");
  assert.equal(guideSubtitle({ summary: null, body: "```\ncode only\n```" }), null);
});

test("tags are lower-cased, trimmed, de-duplicated, and blanks dropped", () => {
  assert.deepEqual(splitTags("wifi, router , Reset, wifi,, "), ["wifi", "router", "reset"]);
  assert.deepEqual(splitTags(""), []);
});

test("a date is formatted for a person, and an unparseable one is not invented", () => {
  assert.equal(formatGuideDate("2026-11-01T12:00:00Z"), "Nov 1, 2026");
  assert.equal(formatGuideDate(null), null);
  assert.equal(formatGuideDate(""), null);
  assert.equal(formatGuideDate("not a date"), null);
});

// --- #5309: editing and revision history ---

test("a save body carries EVERY field, subject columns included (#4565 trap)", () => {
  const guide = { property_id: "p1", asset_id: null, vehicle_id: "v1" };
  const body = guideSaveBody(guide, {
    title: "  Reset the router  ",
    summary: "",
    body: "## Unplug it\n\nWait.",
    tags: "wifi, Router",
    change_note: "  fixed step 2  ",
  });

  // The editable fields, normalised the way guideEditBody does.
  assert.equal(body.title, "Reset the router");
  assert.equal(body.summary, "", "an emptied summary is sent as '', which clears it");
  assert.equal(body.body, "## Unplug it\n\nWait.");
  assert.deepEqual(body.tags, ["wifi", "router"]);
  assert.equal(body.change_note, "fixed step 2");

  // The trap: the form cannot change these, but omitting one makes the API
  // write back whatever the row held when it locked. A null must be sent as
  // "" (subject.Apply's clear), never left out.
  assert.equal(body.property_id, "p1", "the subject columns are echoed, not dropped");
  assert.equal(body.asset_id, "");
  assert.equal(body.vehicle_id, "v1");
  for (const key of ["title", "summary", "body", "tags", "change_note", "property_id", "asset_id", "vehicle_id"]) {
    assert.ok(key in body, `${key} must be present in a save body`);
  }
});

test("a restore's API note is recognised, and its revision id recovered", () => {
  const rev = "8f14e45f-ceea-467a-9a3e-1b0f2f0b8b1a";
  assert.equal(isRestoreNote(`Restored revision ${rev}`), true);
  assert.equal(restoredRevisionId(`Restored revision ${rev}`), rev);
  assert.equal(isRestoreNote("fixed the wifi step"), false, "a person's own note is not a restore");
  assert.equal(isRestoreNote(null), false);
  assert.equal(restoredRevisionId("  Restored revision " + rev + "  "), rev, "surrounding space is fine");
});

test("the current revision is the NEWEST whose body matches - which is the restore after one", () => {
  const revisions = [
    { id: "r3", body: "old body" },   // the restore: old text written as a new revision
    { id: "r2", body: "new body" },   // the version it replaced, still present
    { id: "r1", body: "old body" },
  ];
  assert.equal(currentRevisionId(revisions, "old body"), "r3");
  assert.equal(currentRevisionId(revisions, "new body"), "r2");
  assert.equal(currentRevisionId(revisions, "gone"), null);
  assert.equal(currentRevisionId(revisions, null), null);
  assert.equal(currentRevisionId([{ id: "r1" }], "x"), null, "the list carries no body: no answer, not a wrong one");
});

test("who and when of a revision are said honestly, and never invented", () => {
  assert.equal(revisionAuthor({ edited_by: "u1", edited_by_name: "Jacob" }), "Jacob");
  assert.equal(revisionAuthor({ edited_by: "u1", edited_by_name: "  " }), "Someone");
  assert.equal(revisionAuthor({ edited_by: null, edited_by_name: null }), "Not recorded");

  const now = Date.parse("2026-11-18T12:00:00Z");
  assert.equal(revisionWhen("2026-11-18T11:59:40Z", now), "just now");
  assert.equal(revisionWhen("2026-11-18T11:30:00Z", now), "30 min ago");
  assert.equal(revisionWhen("2026-11-18T09:00:00Z", now), "3 hours ago");
  assert.equal(revisionWhen("2026-11-18T10:00:00Z", now), "2 hours ago");
  assert.equal(revisionWhen("2026-10-02T12:00:00Z", now), "Oct 2, 2026", "old versions read as a date");
  assert.equal(revisionWhen(null, now), null);
  assert.equal(revisionWhen("nonsense", now), null);
});

test("saving an untouched form is refused, because it would add a copy to the history", () => {
  const guide = { title: "Reset the router", summary: "When wifi drops.", body: "## Step", tags: ["wifi"] };
  const same = { title: "Reset the router", summary: "When wifi drops.", body: "## Step", tags: "wifi" };
  assert.equal(guideIsDirty(guide, same), false, "nothing changed");
  assert.equal(guideIsDirty(guide, { ...same, tags: " WiFi , wifi " }), false, "tag order/space is not a change");
  assert.equal(guideIsDirty(guide, { ...same, title: " Reset the router " }), false, "trim is not a change");
  assert.equal(guideIsDirty(guide, { ...same, body: "## Step 2" }), true);
  assert.equal(guideIsDirty(guide, { ...same, summary: "" }), true, "clearing a field is a change");
  assert.equal(guideIsDirty({ ...guide, summary: null }, { ...same, summary: "" }), false);
});

test("the restore warning promises that the replaced version survives", () => {
  const withDate = restoreWarning("3 hours ago");
  assert.match(withDate, /stays in the history/);
  assert.match(withDate, /restore that one too/);
  assert.match(restoreWarning(null), /stays in the history/);
  assert.doesNotMatch(restoreWarning(null), /undefined|null/, "no placeholder leaks into the sentence");
});

test("a size label is a fact: characters, or 'empty'", () => {
  assert.equal(revisionSizeLabel(412), "412 characters");
  assert.equal(revisionSizeLabel(1), "1 character");
  assert.equal(revisionSizeLabel(0), "empty");
});
