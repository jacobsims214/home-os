// Run: npm test. Guards the semantic colour tokens (#4529, KB design/app-shell.md).
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

const here = new URL(".", import.meta.url).pathname;
const ui = join(here, "..");
const css = readFileSync(join(here, "globals.css"), "utf8");

const TOKENS = [
  "bg", "surface", "surface-2", "raised", "line",
  "ink", "ink-2", "ink-3",
  "accent", "accent-fill", "accent-soft",
  "badge", "danger", "warn-bg", "warn-ink", "warn-line",
  "scrim", "shadow-sm", "shadow-lg",
];

function block(selector) {
  const start = css.indexOf(selector);
  assert.notEqual(start, -1, `globals.css has no ${selector} block`);
  const body = css.slice(css.indexOf("{", start) + 1, css.indexOf("}", start));
  return Object.fromEntries([...body.matchAll(/--([\w-]+):\s*([^;]+);/g)].map((m) => [m[1], m[2].trim()]));
}

const light = block(':root[data-mantine-color-scheme="light"]');
const dark = block(':root[data-mantine-color-scheme="dark"]');

test("every token is defined for light and dark", () => {
  for (const t of TOKENS) {
    assert.ok(light[t], `--${t} missing in light`);
    assert.ok(dark[t], `--${t} missing in dark`);
  }
});

function luminance(hex) {
  const n = hex.replace("#", "");
  const [r, g, b] = [0, 2, 4].map((i) => {
    const c = parseInt(n.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}
function contrast(a, b) {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

test("text tokens reach 4.5:1 on the surfaces they sit on", () => {
  for (const [name, s] of [["light", light], ["dark", dark]]) {
    for (const fg of ["ink", "ink-2", "ink-3", "accent", "danger"]) {
      for (const bg of ["bg", "surface", "raised", "surface-2"]) {
        const ratio = contrast(s[fg], s[bg]);
        assert.ok(ratio >= 4.5, `${name}: --${fg} on --${bg} is ${ratio.toFixed(2)}:1`);
      }
    }
    assert.ok(contrast("#ffffff", s["accent-fill"]) >= 4.5, `${name}: white on --accent-fill`);
    assert.ok(contrast(s["warn-ink"], s["warn-bg"]) >= 4.5, `${name}: --warn-ink on --warn-bg`);
  }
  assert.ok(contrast(light.accent, light["accent-soft"]) >= 4.5, "light: --accent on --accent-soft");
});

test("follows the system scheme by default, with a stored override", () => {
  const providers = readFileSync(join(here, "providers.tsx"), "utf8");
  const layout = readFileSync(join(here, "layout.tsx"), "utf8");
  assert.match(providers, /defaultColorScheme="auto"/);
  assert.doesNotMatch(providers, /forceColorScheme/);
  assert.match(layout, /<ColorSchemeScript defaultColorScheme="auto"/);
});

test("shared components use tokens, not hard-coded light colours", () => {
  const banned = [
    /#[0-9a-fA-F]{3,8}\b(?![\w-])/, // hex colours (ticket refs like #4529 are digits-only and skipped below)
    /\b(?:text|bg|border|ring|divide)-(?:gray|slate|zinc|neutral|white|black|cyan|indigo)(?:-\d+)?\b/,
    /light-dark\(var\(--mantine-color-white\)/,
  ];
  for (const dir of ["components/ui", "components/shared"]) {
    for (const file of readdirSync(join(ui, dir))) {
      if (!/\.(tsx|ts|css)$/.test(file)) continue;
      const lines = readFileSync(join(ui, dir, file), "utf8").split("\n");
      lines.forEach((line, i) => {
        const code = line.replace(/#\d+\b/g, ""); // ticket numbers in comments
        for (const re of banned) {
          assert.doesNotMatch(code, re, `${dir}/${file}:${i + 1}: ${line.trim()}`);
        }
      });
    }
  }
});
