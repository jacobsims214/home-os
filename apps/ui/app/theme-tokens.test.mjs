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

// #4754: Mantine's colour variants (Badge, Alert, Button, Text c=) must also reach
// 4.5:1. The stock values come from Mantine's own resolver with our primaryShade.
// The overrides are the JSON objects in providers.tsx, applied the way
// colourFixes() applies them. Every palette colour is checked, not only the ones
// in use today, so a new <Badge color="lime"> can't slip below AA. "dark" is
// skipped because it's a neutral meant for light backgrounds, and the app doesn't use it.
test("Mantine colour variants reach 4.5:1 in both schemes", async () => {
  const { DEFAULT_THEME, mergeMantineTheme, defaultCssVariablesResolver } = await import("@mantine/core");
  const providers = readFileSync(join(here, "providers.tsx"), "utf8");
  const table = (name) => {
    const m = providers.match(new RegExp(`const ${name}: Record<string, string> = (\\{[\\s\\S]*?\\});`));
    assert.ok(m, `providers.tsx has no ${name} object`);
    return JSON.parse(m[1]);
  };
  const shade = providers.match(/primaryShade: \{ light: (\d), dark: (\d) \}/);
  assert.ok(shade, "providers.tsx primaryShade not found");
  const theme = mergeMantineTheme(DEFAULT_THEME, {
    primaryColor: "cyan",
    primaryShade: { light: Number(shade[1]), dark: Number(shade[2]) },
  });
  const stock = defaultCssVariablesResolver(theme);
  const LIGHT_INK = table("LIGHT_INK");
  const FILLED_BG = table("FILLED_BG");
  const DARK_INK = table("DARK_INK");

  const rgb = (h) => [0, 2, 4].map((i) => parseInt(h.replace("#", "").padEnd(6, "0").slice(i, i + 2), 16));
  const toHex = (c) => "#" + c.map((v) => Math.round(v).toString(16).padStart(2, "0")).join("");
  const mix = (a, w, b) => a.map((v, i) => v * w + b[i] * (1 - w));

  for (const [scheme, tokens, surfaces] of [
    ["light", light, ["bg", "surface", "surface-2"]],
    ["dark", dark, ["bg", "surface", "surface-2", "raised"]],
  ]) {
    const vars = { ...stock.variables, ...stock[scheme] };
    for (const [k, v] of Object.entries(tokens)) vars[`--${k}`] = v;
    const ink = scheme === "light" ? LIGHT_INK : DARK_INK;
    for (const [c, v] of Object.entries(FILLED_BG)) vars[`--mantine-color-${c}-filled`] = v;
    for (const [c, v] of Object.entries(ink)) {
      vars[`--mantine-color-${c}-light-color`] = v;
      vars[`--mantine-color-${c}-outline`] = v;
      if (scheme === "dark") vars[`--mantine-color-${c}-text`] = v;
    }
    // Resolves var(), #fff/#rrggbb and rgba() to [r, g, b, alpha].
    const resolve = (value) => {
      const v = value.trim();
      const ref = v.match(/^var\((--[\w-]+)\)$/);
      if (ref) {
        assert.ok(vars[ref[1]], `${scheme}: ${ref[1]} is not defined`);
        return resolve(vars[ref[1]]);
      }
      const a = v.match(/^rgba\((\d+),\s*(\d+),\s*(\d+),\s*([\d.]+)\)$/);
      if (a) return [Number(a[1]), Number(a[2]), Number(a[3]), Number(a[4])];
      assert.match(v, /^#([0-9a-f]{3}|[0-9a-f]{6})$/i, `${scheme}: can't measure ${v}`);
      const h = v.length === 4 ? "#" + [...v.slice(1)].map((x) => x + x).join("") : v;
      return [...rgb(h), 1];
    };
    const solid = (value) => {
      const c = resolve(value);
      assert.equal(c[3], 1, `${scheme}: ${value} should be opaque`);
      return c.slice(0, 3);
    };
    const check = (label, fg, bg) => {
      const ratio = contrast(toHex(fg), toHex(bg));
      assert.ok(ratio >= 4.5, `${scheme}: ${label} is ${ratio.toFixed(2)}:1`);
    };
    const white = [255, 255, 255];

    for (const color of Object.keys(DEFAULT_THEME.colors).filter((c) => c !== "dark")) {
      const v = (suffix) => `var(--mantine-color-${color}-${suffix})`;
      const filled = solid(v("filled"));
      check(`white on ${color} filled`, white, filled);
      // colourFixes() hover: color-mix(in srgb, filled 82%, #000)
      check(`white on ${color} filled-hover`, white, mix(filled, 0.82, [0, 0, 0]));
      for (const s of surfaces) {
        const surface = solid(`var(--${s})`);
        const tint = resolve(v("light"));
        check(`${color} light variant on --${s}`, solid(v("light-color")), mix(tint.slice(0, 3), tint[3], surface));
        check(`${color} outline on --${s}`, solid(v("outline")), surface);
        check(`${color} text (c="${color}") on --${s}`, solid(v("text")), surface);
      }
    }
  }
});

test("colourFixes in providers.tsx applies the tables the way this test assumes", () => {
  const providers = readFileSync(join(here, "providers.tsx"), "utf8");
  assert.match(providers, /color-mix\(in srgb, var\(--mantine-color-\$\{color\}-filled\) 82%, #000\)/);
  assert.match(providers, /light: \{ \.\.\.colourFixes\("light"\), \.\.\.sharedVariables \}/);
  assert.match(providers, /dark: \{ \.\.\.colourFixes\("dark"\), \.\.\.sharedVariables \}/);
});
