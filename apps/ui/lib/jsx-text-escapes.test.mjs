// Run: npm test. A backslash escape in JSX TEXT is six literal characters (#5410).
//
// THE DEFECT THE WAR GAME FOUND. The Meals header rendered `Nov 16 – 22 \u00b7 2
// dinners planned` — the words "u00b7" instead of "·" — because this sat in JSX:
//
//     {weekLabel(start)} \u00b7 {planned} dinners planned
//
// JSX text is NOT a JavaScript string literal. `·` and `\u00b7` are the same
// character inside `{"…"}`, but between two expressions the backslash form is
// text, so it shipped to the page verbatim. Nobody noticed for two rounds
// because no test ever read that line's rendered text.
//
// WHY A PARSER AND NOT A GREP. `grep '\\u00b7'` would pass on the fix and fail on
// the correct uses — every legal `{"\u2014"}` string, every comment explaining
// the bug, every test asserting the escape does not appear. A check that has to
// be "fixed" by deleting the explanation is the grep-only check this project has
// already shipped once (the envoy 429 page, #5421). So this parses each .tsx
// with the TypeScript compiler and looks ONLY at syntax kind JsxText: the one
// place where a backslash is literal. Strings, template literals, JSX attribute
// strings and comments are all legal and untouched.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import ts from "typescript";

const ui = join(new URL(".", import.meta.url).pathname, "..");

/** Every .tsx source file, minus builds, deps and test fixtures. */
function tsxFiles(dir, out = []) {
  for (const entry of readdirSync(dir)) {
    if (entry === "node_modules" || entry.startsWith(".")) continue;
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      if (entry === "public" || entry === "e2e" || entry === ".next") continue;
      tsxFiles(full, out);
      continue;
    }
    if (entry.endsWith(".tsx")) out.push(full);
  }
  return out;
}

/** JSX text nodes carrying a backslash escape, as {file, line, text}. */
function escapeInJsxText(files) {
  const found = [];
  for (const file of files) {
    const source = ts.createSourceFile(
      file,
      readFileSync(file, "utf8"),
      ts.ScriptTarget.Latest,
      /* setParentNodes */ true,
      ts.ScriptKind.TSX,
    );
    const walk = (node) => {
      if (node.kind === ts.SyntaxKind.JsxText && /\\./.test(node.text)) {
        const { line } = source.getLineAndCharacterOfPosition(node.getStart(source));
        found.push({ file: relative(ui, file), line: line + 1, text: node.text.trim() });
      }
      ts.forEachChild(node, walk);
    };
    ts.forEachChild(source, walk);
  }
  return found;
}

test("no JSX text node carries a backslash escape (the Meals \\u00b7 defect, #5410)", () => {
  const bad = escapeInJsxText(tsxFiles(ui));
  assert.deepEqual(
    bad,
    [],
    "a backslash escape in JSX TEXT is served literally (use a real character, or wrap it in {\"...\"}):\n" +
      bad.map((b) => `  ${b.file}:${b.line}  ${JSON.stringify(b.text)}`).join("\n"),
  );
});

test("the detector still finds a planted escape (it cannot silently stop working)", () => {
  // A guard that quietly matches nothing is the failure mode this project has
  // shipped three times. So the detector is exercised on in-memory sources: the
  // exact #5410 shape must hit, and every LEGAL use of a backslash must not.
  // If TypeScript's JSX handling ever changes, this fails before the check
  // above starts passing for the wrong reason.
  const hits = (body) => {
    const source = ts.createSourceFile("probe.tsx", body, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
    const found = [];
    const walk = (node) => {
      if (node.kind === ts.SyntaxKind.JsxText && /\\./.test(node.text)) found.push(node.text.trim());
      ts.forEachChild(node, walk);
    };
    ts.forEachChild(source, walk);
    return found;
  };

  // The defect, verbatim in shape: an escape between two expressions.
  assert.deepEqual(hits("const a = <p>{x} \\u00b7 {y}</p>;"), ["\\u00b7"], "the shape that shipped");
  // Legal: a real character in JSX text.
  assert.deepEqual(hits("const a = <p>{x} \u00b7 {y}</p>;".replace("\\u00b7", "\u00b7")), [], "a real middle dot");
  // Legal: an escape inside a JS expression container (a real TS string).
  assert.deepEqual(hits('const a = <p>{x}{"\\u00b7"}{y}</p>;'), [], "an escape in a string expression");
  // Legal: an escape in an attribute string.
  assert.deepEqual(hits('const a = <p title="\\u00b7" />;'), [], "an escape in an attribute");
  // Legal: an escape in a comment.
  assert.deepEqual(hits("// \\u00b7 in JSX text is literal\nconst a = <p>\u00b7</p>;"), [], "a comment");
});
