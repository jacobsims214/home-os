// Run: npm test (node --test). The component is JSX, so node can't import it;
// this reads it as text and holds it to the approved artifact instead.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";

const ui = new URL("../../", import.meta.url).pathname;
const read = (...p) => readFileSync(join(ui, ...p), "utf8");

const component = read("components/shared", "HomeMark.tsx");
const offlineShell = read("public", "offline.html");
const iconSource = read("scripts", "generate-pwa-icons.mjs");

/** The `#i-house` symbol in approved artifact #75 v1 (KB design/app-shell.md). */
const ARTIFACT_PATHS = [
  "M5 12H3l9-9 9 9h-2",
  "M5 12v7a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-7",
  "M10 21v-5a2 2 0 0 1 4 0v5",
];

const pathAttr = (source, d) => source.includes(`d="${d}"`);

test("HomeMark draws the approved artifact's house glyph, path for path", () => {
  for (const d of ARTIFACT_PATHS) {
    assert.ok(pathAttr(component, d), `HomeMark.tsx is missing the artifact path ${d}`);
  }
  // Exactly the artifact's three paths: a fourth would be an invented detail.
  const drawn = [...component.matchAll(/<path d="([^"]+)"/g)].map(([, d]) => d);
  assert.deepEqual(drawn, ARTIFACT_PATHS);
});

test("HomeMark keeps the mockup's tile and glyph geometry", () => {
  // artifact #75 v1: `.logo` 28x28 radius 8 on --accent-fill; `.logo svg.i`
  // 18x18 at stroke-width 2 on a 24 grid, round caps and joins, fill none.
  assert.match(component, /size = 28/, "the default tile is the mockup's 28 px");
  assert.match(component, /const GLYPH_RATIO = 18 \/ 28/, "the glyph is the mockup's 18 px on 28");
  assert.match(component, /strokeWidth=\{2\}/);
  assert.match(component, /viewBox="0 0 24 24"/);
  assert.match(component, /fill="none"/);
  assert.match(component, /strokeLinecap="round"/);
  assert.match(component, /strokeLinejoin="round"/);

  const css = read("components/shared", "HomeMark.module.css");
  assert.match(css, /border-radius: var\(--r-sm\)/, "radius 8 comes from the token, not a hex");
  assert.match(css, /background: var\(--accent-fill\)/, "--accent-fill is #0b7285 in both schemes");
  // No literal colour in any declaration (comments may name one): tokens only,
  // so the mark is byte-identical in light and dark.
  const declarations = css.replace(/\/\*[\s\S]*?\*\//g, "");
  assert.doesNotMatch(declarations, /#[0-9a-fA-F]{3,6}/, "a literal colour would need a dark-mode override");
});

test("the brand is a glyph, not a letter: no placeholder H survives", () => {
  const sidebar = read("components/shared", "Sidebar.tsx");
  // The old tile rendered a bare "H" between the logo span's tags.
  assert.doesNotMatch(sidebar, /<span className=\{classes\.logo\}[^>]*>\s*H\s*<\/span>/);
  assert.match(sidebar, /<HomeMark size=\{28\} \/>/, "Brand renders the shared mark");
  // The tile's own CSS now lives in HomeMark.module.css, not in a second copy.
  assert.doesNotMatch(read("components/shared", "Sidebar.module.css"), /\.logo\s*\{/);
});

test("every surface that draws the mark uses the component or the same paths", () => {
  // The static offline shell cannot import React: it repeats the glyph.
  for (const d of ARTIFACT_PATHS) {
    assert.ok(pathAttr(offlineShell, d), `public/offline.html is missing ${d}`);
  }
  // The icon set (#4533) already carries the same glyph; it is not ours to change.
  const homeGlyph = iconSource.match(/home: "([^"]+)"/)?.[1];
  assert.ok(homeGlyph, "generate-pwa-icons.mjs should still define the home glyph");
  assert.equal(homeGlyph, ARTIFACT_PATHS.join(""), "the PWA icon's home glyph is the artifact's");
});
