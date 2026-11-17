// Run: npm test. Guards the installable-app setup (#4533): the manifest, the
// icon set scripts/generate-pwa-icons.mjs writes, and the iOS meta in
// app/layout.tsx. Colours are asserted against the tokens in globals.css, so
// the manifest cannot quietly drift to an invented hex.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync, existsSync } from "node:fs";
import { join } from "node:path";
import { inflateSync } from "node:zlib";

const here = new URL(".", import.meta.url).pathname;
const ui = join(here, "..");
const publicDir = join(ui, "public");
const manifest = JSON.parse(readFileSync(join(publicDir, "manifest.json"), "utf8"));
const layout = readFileSync(join(here, "layout.tsx"), "utf8");
const css = readFileSync(join(here, "globals.css"), "utf8");

// ── token lookup (same block parsing as theme-tokens.test.mjs) ──────────────
function tokenBlock(selector) {
  const start = css.indexOf(selector);
  assert.notEqual(start, -1, `globals.css has no ${selector} block`);
  const body = css.slice(css.indexOf("{", start) + 1, css.indexOf("}", start));
  return Object.fromEntries([...body.matchAll(/--([\w-]+):\s*([^;]+);/g)].map((m) => [m[1], m[2].trim()]));
}
const light = tokenBlock(':root[data-mantine-color-scheme="light"]');
/** --accent-fill as [r, g, b]: the colour behind the mark on every icon. */
const ACCENT_RGB = (() => {
  const hex = light["accent-fill"].replace("#", "");
  return [0, 2, 4].map((i) => parseInt(hex.slice(i, i + 2), 16));
})();
const dark = tokenBlock(':root[data-mantine-color-scheme="dark"]');

// ── PNG reading: enough to check dimensions and measure ink ─────────────────
/** Returns { width, height, pixels: Uint8Array RGBA } for an 8-bit PNG. */
function readPng(path) {
  const buf = readFileSync(path);
  assert.equal(buf.subarray(1, 4).toString("ascii"), "PNG", `${path} is not a PNG`);
  let pos = 8;
  let width = 0;
  let height = 0;
  let bitDepth = 0;
  let colorType = 0;
  const idat = [];
  while (pos < buf.length) {
    const len = buf.readUInt32BE(pos);
    const type = buf.subarray(pos + 4, pos + 8).toString("ascii");
    const data = buf.subarray(pos + 8, pos + 8 + len);
    if (type === "IHDR") {
      width = data.readUInt32BE(0);
      height = data.readUInt32BE(4);
      bitDepth = data[8];
      colorType = data[9];
      assert.equal(data[12], 0, `${path} is interlaced; the generator never writes interlaced PNGs`);
    } else if (type === "IDAT") {
      idat.push(data);
    } else if (type === "IEND") {
      break;
    }
    pos += 12 + len;
  }
  assert.equal(bitDepth, 8, `${path} is ${bitDepth}-bit, expected 8`);
  assert.ok(colorType === 6 || colorType === 2, `${path} is colour type ${colorType}, expected 6 (RGBA) or 2 (RGB)`);
  const channels = colorType === 6 ? 4 : 3;
  const raw = inflateSync(Buffer.concat(idat));
  const stride = width * channels;
  const out = new Uint8Array(width * height * 4);
  const prior = new Uint8Array(stride);
  const line = new Uint8Array(stride);
  for (let y = 0; y < height; y++) {
    const filter = raw[y * (stride + 1)];
    const src = raw.subarray(y * (stride + 1) + 1, (y + 1) * (stride + 1));
    for (let i = 0; i < stride; i++) {
      const a = i >= channels ? line[i - channels] : 0;
      const b = prior[i];
      const c = i >= channels ? prior[i - channels] : 0;
      const x = src[i];
      let v;
      switch (filter) {
        case 0: v = x; break;
        case 1: v = x + a; break;
        case 2: v = x + b; break;
        case 3: v = x + ((a + b) >> 1); break;
        case 4: {
          const p = a + b - c;
          const pa = Math.abs(p - a);
          const pb = Math.abs(p - b);
          const pc = Math.abs(p - c);
          v = x + (pa <= pb && pa <= pc ? a : pb <= pc ? b : c);
          break;
        }
        default: assert.fail(`${path}: unknown PNG filter ${filter}`);
      }
      line[i] = v & 0xff;
    }
    for (let x = 0; x < width; x++) {
      const o = (y * width + x) * 4;
      out[o] = line[x * channels];
      out[o + 1] = line[x * channels + 1];
      out[o + 2] = line[x * channels + 2];
      out[o + 3] = channels === 4 ? line[x * channels + 3] : 255;
    }
    prior.set(line);
  }
  return { width, height, pixels: out };
}

const sizeOf = (path) => {
  const { width, height } = readPng(path);
  return { width, height };
};

// ── manifest ────────────────────────────────────────────────────────────────
test("manifest has the fields an installable app needs", () => {
  assert.equal(manifest.name, "Home OS");
  assert.ok(manifest.short_name && manifest.short_name.length <= 12, "short_name must be short enough to fit a launcher");
  assert.equal(manifest.display, "standalone");
  assert.equal(manifest.scope, "/");
  assert.ok(manifest.start_url.startsWith("/"), "start_url must be in scope");
  assert.equal(manifest.id, "/");
  assert.equal(manifest.prefer_related_applications, false);
  assert.equal(manifest.lang, "en");
  assert.ok(Array.isArray(manifest.icons) && manifest.icons.length > 0);
});

test("manifest start_url is a page that exists", () => {
  assert.ok(routeExists(manifest.start_url), `no page for start_url ${manifest.start_url}`);
});

test("manifest colours are the theme tokens, not invented hex", () => {
  // background_color is --bg: the splash and the page share the light page
  // colour, so there is no jump between them.
  assert.equal(manifest.background_color, light["bg"], "background_color must be --bg (light)");
  // theme_color is --surface: the status bar / task switcher sits over the
  // header, which is --surface in the shell.
  assert.equal(manifest.theme_color, light["surface"], "theme_color must be --surface (light)");
  // And the same values reach the live app: viewport themeColor covers dark.
  assert.match(layout, new RegExp(`color: "${light["surface"]}"`.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  assert.match(layout, new RegExp(`color: "${dark["surface"]}"`.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
});

// ── icons ───────────────────────────────────────────────────────────────────
test("every manifest icon exists at the size it declares", () => {
  for (const icon of manifest.icons) {
    const path = join(publicDir, icon.src.replace(/^\//, ""));
    assert.ok(existsSync(path), `${icon.src} is missing`);
    const [w, h] = icon.sizes.split("x").map(Number);
    const actual = sizeOf(path);
    assert.deepEqual(actual, { width: w, height: h }, `${icon.src} is ${actual.width}x${actual.height}, declared ${icon.sizes}`);
  }
});

test("the icon set covers any, maskable and monochrome at the sizes launchers ask for", () => {
  const of = (purpose) =>
    manifest.icons.filter((i) => (i.purpose ?? "any").split(" ").includes(purpose)).map((i) => i.sizes);
  assert.ok(of("any").includes("192x192"), "no 192 any icon");
  assert.ok(of("any").includes("512x512"), "no 512 any icon");
  assert.ok(of("maskable").includes("192x192"), "no 192 maskable icon");
  assert.ok(of("maskable").includes("512x512"), "no 512 maskable icon");
  assert.ok(of("monochrome").length > 0, "no monochrome icon for Android themed icons");
  for (const icon of manifest.icons) assert.equal(icon.type, "image/png", `${icon.src} needs a type`);
});

test("maskable icons keep their ink inside the 80% safe circle", () => {
  // The OS may crop a maskable icon to a circle/squircle/teardrop, so every
  // opaque non-background pixel must sit inside the safe circle of radius
  // 40% of the width. Measured from the pixels, not from the intent.
  for (const icon of manifest.icons.filter((i) => (i.purpose ?? "").includes("maskable"))) {
    const { width, height, pixels } = readPng(join(publicDir, icon.src.replace(/^\//, "")));
    assert.equal(width, height, `${icon.src} is not square`);
    const r = width * 0.4;
    const cx = width / 2;
    const cy = height / 2;
    /*
     * Ink is anything that is not the accent background: the white mark and
     * every anti-aliased pixel between the two. Comparing against the
     * background colour rather than a brightness cut-off means a faint edge
     * pixel counts, so a glyph that only just overflows the safe circle still
     * fails instead of hiding under the threshold.
     */
    const BG = ACCENT_RGB;
    const isInk = (i) => {
      if (pixels[i + 3] < 8) return false; // transparent: outside the tile
      const d =
        Math.abs(pixels[i] - BG[0]) + Math.abs(pixels[i + 1] - BG[1]) + Math.abs(pixels[i + 2] - BG[2]);
      return d > 18;
    };
    let inkPixels = 0;
    let worst = 0;
    for (let y = 0; y < height; y++) {
      for (let x = 0; x < width; x++) {
        const i = (y * width + x) * 4;
        if (!isInk(i)) continue;
        inkPixels++;
        worst = Math.max(worst, Math.hypot(x + 0.5 - cx, y + 0.5 - cy));
      }
    }
    assert.ok(inkPixels > width, `${icon.src} looks empty (${inkPixels} ink pixels)`);
    assert.ok(worst <= r, `${icon.src}: ink reaches ${worst.toFixed(1)}px from centre, safe radius is ${r}px`);
  }
});

test("the maskable icon is full bleed (the OS supplies the shape)", () => {
  for (const icon of manifest.icons.filter((i) => (i.purpose ?? "").includes("maskable"))) {
    const { width, height, pixels } = readPng(join(publicDir, icon.src.replace(/^\//, "")));
    for (const [x, y] of [[0, 0], [width - 1, 0], [0, height - 1], [width - 1, height - 1]]) {
      assert.equal(pixels[(y * width + x) * 4 + 3], 255, `${icon.src} corner (${x},${y}) is transparent; maskable must be full bleed`);
    }
  }
});

test("the apple-touch-icon is the 180 px opaque one iOS wants", () => {
  const path = join(publicDir, "icons", "apple-touch-icon.png");
  assert.ok(existsSync(path), "public/icons/apple-touch-icon.png is missing");
  assert.deepEqual(sizeOf(path), { width: 180, height: 180 });
  // iOS rounds the corners itself and fills any transparency with black, so
  // the file must be opaque to the edges.
  const { width, height, pixels } = readPng(path);
  for (const [x, y] of [[0, 0], [width - 1, 0], [0, height - 1], [width - 1, height - 1]]) {
    assert.equal(pixels[(y * width + x) * 4 + 3], 255, `apple-touch-icon corner (${x},${y}) is transparent`);
  }
  assert.match(layout, /apple:\s*\[\{\s*url:\s*"\/icons\/apple-touch-icon\.png"/, "layout.tsx does not declare the apple-touch-icon");
});

test("the favicon exists and carries 16, 32 and 48 px images", () => {
  const buf = readFileSync(join(publicDir, "favicon.ico"));
  assert.equal(buf.readUInt16LE(0), 0, "ICO reserved field");
  assert.equal(buf.readUInt16LE(2), 1, "ICO type must be 1");
  const count = buf.readUInt16LE(4);
  const sizes = [];
  for (let i = 0; i < count; i++) {
    const e = 6 + i * 16;
    const w = buf.readUInt8(e) || 256;
    const h = buf.readUInt8(e + 1) || 256;
    assert.equal(w, h, "ICO entry is not square");
    // Each entry holds a PNG: its IHDR must agree with the directory entry.
    const offset = buf.readUInt32LE(e + 12);
    assert.equal(buf.subarray(offset + 1, offset + 4).toString("ascii"), "PNG", "ICO payload is not a PNG");
    assert.equal(buf.readUInt32BE(offset + 16), w, "ICO entry width disagrees with its PNG");
    sizes.push(w);
  }
  assert.deepEqual(sizes, [16, 32, 48]);
});

test("the manifest shortcuts deep-link to routes that exist, with real icons", () => {
  assert.ok(Array.isArray(manifest.shortcuts) && manifest.shortcuts.length > 0);
  for (const shortcut of manifest.shortcuts) {
    assert.ok(shortcut.name && shortcut.url, "a shortcut needs a name and a url");
    assert.ok(shortcut.url.startsWith("/"), `${shortcut.name}: shortcut urls must be in scope`);
    assert.ok(routeExists(shortcut.url), `${shortcut.name} points at ${shortcut.url}, which is not a page`);
    assert.ok(shortcut.icons?.length, `${shortcut.name} has no icon`);
    for (const icon of shortcut.icons) {
      const path = join(publicDir, icon.src.replace(/^\//, ""));
      assert.ok(existsSync(path), `${icon.src} is missing`);
      const [w, h] = icon.sizes.split("x").map(Number);
      assert.deepEqual(sizeOf(path), { width: w, height: h }, `${icon.src} is not ${icon.sizes}`);
    }
  }
});

test("the vector source of the icon set is in public/icons", () => {
  const svg = readFileSync(join(publicDir, "icons", "icon.svg"), "utf8");
  assert.match(svg, /^<svg[^>]+viewBox="0 0 512 512"/);
  // The brand tile is --accent-fill, the same in both schemes.
  assert.ok(svg.includes(light["accent-fill"]), "icon.svg does not use --accent-fill");
});

// ── iOS ─────────────────────────────────────────────────────────────────────
test("iOS standalone meta is wired", () => {
  // Next renders apple-mobile-web-app-capable=yes from appleWebApp.capable.
  assert.match(layout, /capable:\s*true/);
  assert.match(layout, /statusBarStyle:\s*"default"/);
  assert.match(layout, /title:\s*"Home OS"/);
  assert.match(layout, /manifest:\s*"\/manifest\.json"/);
});

test("the viewport respects the safe area without killing pinch zoom", () => {
  assert.match(layout, /viewportFit:\s*"cover"/, "viewport-fit=cover is what makes env(safe-area-inset-*) non-zero");
  assert.match(layout, /width:\s*"device-width"/);
  assert.match(layout, /initialScale:\s*1/);
  assert.doesNotMatch(layout, /maximumScale/, "maximum-scale disables pinch zoom (WCAG 1.4.4)");
  assert.doesNotMatch(layout, /userScalable:\s*false/, "user-scalable=no disables pinch zoom (WCAG 1.4.4)");
  // The shell is what consumes the insets, so both halves must be present.
  const shell = readFileSync(join(ui, "components/shared/DashboardShell.tsx"), "utf8");
  assert.match(shell, /env\(safe-area-inset-top/);
  assert.match(shell, /env\(safe-area-inset-bottom/);
  // Double-tap zoom is stopped in CSS instead of in the viewport.
  assert.match(css, /touch-action:\s*manipulation/);
});

/** The launch-screen list from layout.tsx: [{file, width, height, dpr}]. */
function splashScreens() {
  const block = layout.slice(layout.indexOf("APPLE_SPLASH_SCREENS"), layout.indexOf("] as const"));
  return [...block.matchAll(/\{ file: "([^"]+)", width: (\d+), height: (\d+), dpr: (\d+) \}/g)].map(
    ([, file, width, height, dpr]) => ({ file, width: +width, height: +height, dpr: +dpr }),
  );
}

test("every declared iOS launch screen exists at its device size", () => {
  const screens = splashScreens();
  assert.ok(screens.length >= 4, `found ${screens.length} launch screens; iOS needs one per current device size`);
  for (const shot of screens) {
    const path = join(publicDir, "icons", "splash", shot.file);
    assert.ok(existsSync(path), `icons/splash/${shot.file} is missing (run npm run icons:pwa)`);
    assert.deepEqual(
      sizeOf(path),
      { width: shot.width * shot.dpr, height: shot.height * shot.dpr },
      `icons/splash/${shot.file} does not match its ${shot.width}x${shot.height} @${shot.dpr}x media query`,
    );
    assert.match(shot.file, new RegExp(`-${shot.width * shot.dpr}x${shot.height * shot.dpr}\\.png$`), `${shot.file} is named for a different size`);
  }
  // The media query must be built from the same three numbers, or iOS gets a
  // query for a device we have no image for and shows its own launch screen.
  assert.match(
    layout,
    /media: `\(device-width: \$\{shot\.width\}px\) and \(device-height: \$\{shot\.height\}px\) and \(-webkit-device-pixel-ratio: \$\{shot\.dpr\}\) and \(orientation: portrait\)`/,
    "layout.tsx's startupImage media query does not match the APPLE_SPLASH_SCREENS entries",
  );
  assert.match(layout, /startupImage: APPLE_SPLASH_SCREENS\.map/, "layout.tsx does not render the launch screens");
});

test("every file in public/icons belongs to the manifest or the layout", () => {
  // Catches an icon someone added by hand that nothing references, which is
  // how the old placeholder squares survived.
  const referenced = new Set([
    ...manifest.icons.map((i) => i.src),
    ...manifest.shortcuts.flatMap((s) => s.icons.map((i) => i.src)),
  ]);
  const layoutRefs = new Set([...layout.matchAll(/\/icons\/[A-Za-z0-9./_-]+\.(?:png|svg)/g)].map((m) => m[0]));
  // The launch screens are referenced through a template literal, so they come
  // from the declared list rather than from a literal URL.
  for (const shot of splashScreens()) layoutRefs.add(`/icons/splash/${shot.file}`);
  for (const file of readdirSync(join(publicDir, "icons"), { withFileTypes: true })) {
    if (file.isDirectory()) continue;
    const url = `/icons/${file.name}`;
    assert.ok(referenced.has(url) || layoutRefs.has(url), `${url} is not referenced by the manifest or layout.tsx`);
  }
  for (const file of readdirSync(join(publicDir, "icons", "splash"))) {
    const url = `/icons/splash/${file}`;
    assert.ok(layoutRefs.has(url), `${url} is not one of the declared launch screens`);
  }
  assert.ok(statSync(join(publicDir, "favicon.ico")).size > 0);
});

// ── helpers ─────────────────────────────────────────────────────────────────
/**
 * True when a Next page serves this URL. Route groups ("(dashboard)") are
 * dropped, because they do not appear in the URL.
 */
function routeExists(url) {
  const target = url.split(/[?#]/)[0].replace(/\/$/, "") || "/";
  const app = join(ui, "app");
  const walk = (dir, prefix) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name);
      if (entry.isDirectory()) {
        // Route groups and private folders (leading _) are not URL segments;
        // dynamic segments are, but we never link to one statically.
        const segment = entry.name.startsWith("(") ? "" : `/${entry.name}`;
        if (walk(full, prefix + segment)) return true;
      } else if (entry.name === "page.tsx") {
        if ((prefix || "/") === target) return true;
      }
    }
    return false;
  };
  return walk(app, "");
}