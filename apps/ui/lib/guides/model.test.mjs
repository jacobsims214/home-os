// Run: npm test (node --test strips the TypeScript types natively).
//
// The guides reading view (#4570) rests on one parser, so its rules are the
// rules a reader sees. These are the ones that would silently change what a
// guide SAYS — splitting steps wrong, swallowing a code block, counting a
// guide as empty — rather than CSS.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  formatGuideDate,
  guideStepCount,
  guideSubtitle,
  matchesSearch,
  parseGuideSteps,
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
