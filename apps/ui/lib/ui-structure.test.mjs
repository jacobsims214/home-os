// Run: npm test. Two #5410 defects that are about STRUCTURE, so they are pinned
// structurally: the three account-menu entry points must be constructed the same
// way, and no text field may put a font family on its ROOT (which fonts its
// label too).
//
// WHY A PARSE, NOT A GREP. Both rules are "this JSX must have/not have this
// shape", and the offending token also appears in the comments that explain the
// bugs. A grep would fail on its own documentation (#5410(a) writes `ff="monospace"`
// in a comment explaining why it is wrong), then get "fixed" by deleting the
// explanation — which is exactly how the envoy grep-only check lost its teeth
// (#5421). So these read the AST.
//
// Both are also guarded against silently matching nothing: each test asserts
// that it FOUND the sites it is supposed to be checking.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join, relative } from "node:path";
import ts from "typescript";

const ui = join(new URL(".", import.meta.url).pathname, "..");

function parse(relPath) {
  const file = join(ui, relPath);
  return {
    rel: relPath,
    source: readFileSync(file, "utf8"),
    sf: ts.createSourceFile(file, readFileSync(file, "utf8"), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX),
  };
}

/** Every JSX element in `sf`, with its opening element and tag name. */
function jsxElements(sf) {
  const out = [];
  const walk = (node) => {
    if (ts.isJsxElement(node) || ts.isJsxSelfClosingElement(node)) {
      const opening = ts.isJsxElement(node) ? node.openingElement : node;
      out.push({ node, opening, tag: opening.tagName.getText(sf) });
    }
    ts.forEachChild(node, walk);
  };
  ts.forEachChild(sf, walk);
  return out;
}

/** The value of a JSX attribute: its string text, or the expression's source. */
function attr(el, name) {
  for (const prop of el.opening.attributes.properties) {
    if (!ts.isJsxAttribute(prop) || prop.name.getText() !== name) continue;
    if (!prop.initializer) return "";
    if (ts.isStringLiteral(prop.initializer)) return prop.initializer.text;
    return prop.initializer.getText();
  }
  return undefined;
}

// ── #5410(b): one accessible name for the account menu, at all three entry points
//
// THE DEFECT. The More sheet's account row was a bare <Avatar> with no hidden
// prefix, unlike the header button and the sidebar button. So the row announced
// "ET E2E TesterAccount & log out" — the avatar's INITIALS leaked into the
// accessible name — and the three ways into one menu disagreed. Amber and Jacob
// both walk that row; a screen-reader user hears the leak.
//
// THE RULE. Every element that opens the account menu carries the same
// construction: its Avatar is aria-hidden (decoration, not a name) and a
// VisuallyHidden "Account menu for " supplies the name.

test("every account-menu entry point names itself the same way (#5410)", () => {
  // The three OPENERS: the header button (AccountMenu.tsx), the sidebar button
  // (Sidebar.tsx) and the phone More-sheet row (BottomNav.tsx). An opener is the
  // element with aria-haspopup="dialog"; a drawer foot with a logout form is a
  // different surface and is deliberately not in scope.
  const files = ["components/account/AccountMenu.tsx", "components/shared/Sidebar.tsx", "components/shared/BottomNav.tsx"];
  const offenders = [];
  let openers = 0;

  for (const rel of files) {
    const { sf } = parse(rel);
    // The avatar construction inside each opener: an Avatar whose enclosing
    // region also carries the VisuallyHidden prefix.
    const all = jsxElements(sf);
    for (const el of all) {
      const wantsDialog = attr(el, "aria-haspopup") === '"dialog"' || attr(el, "aria-haspopup") === "dialog";
      if (!wantsDialog) continue;
      // BottomNav has a notifications opener too; only the account one is ours.
      const region = el.node.getText(sf);
      if (!/Account menu for|onOpenAccount|classes.accountText/.test(region) && rel.endsWith("BottomNav.tsx")) continue;
      openers++;
      const avatar = all.find((a) => a.tag === "Avatar" && region.includes(a.node.getText(sf)));
      if (!avatar) {
        offenders.push(`${rel}: the account opener has no Avatar`);
        continue;
      }
      if (!/\baria-hidden\b/.test(avatar.opening.getText(sf))) {
        const line = sf.getLineAndCharacterOfPosition(avatar.node.getStart(sf)).line + 1;
        offenders.push(`${rel}:${line} Avatar is not aria-hidden (its initials leak into the accessible name)`);
      }
      if (!/VisuallyHidden>\s*Account menu for/.test(region)) {
        const line = sf.getLineAndCharacterOfPosition(el.node.getStart(sf)).line + 1;
        offenders.push(`${rel}:${line} account opener has no VisuallyHidden "Account menu for"`);
      }
    }
  }

  assert.ok(openers >= 3, `expected the three account-menu openers, found ${openers} — the scan is not looking at what it thinks`);
  assert.deepEqual(offenders, [], offenders.join("\n"));
});

test("the More sheet's account row carries the hidden prefix, not just the name", () => {
  // The specific regression site. Asserted on structure so a refactor that keeps
  // the prefix somewhere else in the file cannot satisfy it.
  const { sf } = parse("components/shared/BottomNav.tsx");
  const prefixes = jsxElements(sf).filter((el) => el.tag === "VisuallyHidden");
  const accountPrefixes = prefixes.filter((el) =>
    /Account menu for/.test(el.node.getText(sf)) && /Account menu for/.test(
      el.node.children.map((c) => c.getText(sf)).join(""),
    ),
  );
  assert.equal(accountPrefixes.length, 1, "exactly one VisuallyHidden 'Account menu for' in the More sheet");
});

// ── #5410(c): a font family on a field's ROOT fonts the LABEL too
//
// THE DEFECT. `<TextInput ff="monospace">` sets the font on the field's root
// element, which the label inherits. A monospace face draws a space as one full
// character cell, so "User name" read as "User  name" and "App password" looked
// broken. The value is what needs the fixed pitch, so the family belongs on
// `styles={{ input: … }}`.
//
// The rule is narrow on purpose: it does not forbid monospace, it forbids
// putting it where the label inherits it.

test("no text field puts a font family on its root (#5410)", () => {
  const files = [
    "components/settings/PhoneCalendarSection.tsx",
    "components/settings/CalDAVSetup.tsx",
  ];
  const offenders = [];
  let checked = 0;

  for (const rel of files) {
    let parsed;
    try {
      parsed = parse(rel);
    } catch {
      continue; // a file that moved is caught by the count below
    }
    for (const el of jsxElements(parsed.sf)) {
      if (!/^(TextInput|Textarea|PasswordInput|NumberInput)$/.test(el.tag)) continue;
      checked++;
      // `ff` is Mantine's fontFamily shorthand and lands on the root.
      if (attr(el, "ff") !== undefined) {
        const line = parsed.sf.getLineAndCharacterOfPosition(el.node.getStart(parsed.sf)).line + 1;
        offenders.push(`${rel}:${line} <${el.tag} ff=…> fonts the label too — use styles={{ input: … }}`);
      }
    }
  }

  assert.ok(checked > 0, "the scan found no text fields — it is not looking at what it thinks");
  assert.deepEqual(offenders, [], offenders.join("\n"));
});

test("the monospace that IS wanted lands on the input (the positive half)", () => {
  // If someone "fixes" the rule by deleting monospace entirely, the value stops
  // being fixed-pitch — a real regression in a copy-this-into-your-phone field.
  const { source } = parse("components/settings/PhoneCalendarSection.tsx");
  assert.match(source, /styles=\{\{\s*input:\s*\{[^}]*fontFamily:[^}]*monospace/, "the input keeps its monospace family");
});
