/**
 * Generates the installed-app icon set from one vector source (#4533).
 *
 *   npm run icons:pwa          (needs the Playwright chromium that
 *                               `npm run test:e2e:install` downloads)
 *
 * One mark, drawn here in SVG, is written to public/icons/ in every size the
 * manifest, iOS and the browsers ask for. Change a glyph below and re-run;
 * never hand-edit the PNGs. app/pwa.test.mjs asserts the output matches the
 * manifest and that the maskable safe zone holds.
 *
 * THE MARK. The approved shell mockup (KB design/app-shell.md, artifact #75 v1)
 * shows the product's logo as a rounded accent tile with a white stroke house
 * glyph (`.logo` in the mockup: 28x28, radius 8, background var(--accent-fill),
 * glyph stroke-width 2). There is no brand asset file in the repo — the built
 * Brand component draws a plain letter "H" (components/shared/Sidebar.tsx) —
 * so this is the mockup's tile, redrawn as vector: --accent-fill (#0b7285, the
 * same in both schemes) behind the Tabler `home` outline the shell uses, at the
 * mockup's 2/24 stroke. The shortcut glyphs are the mockup's own nav symbols
 * (#i-today, #i-cal, #i-meals, #i-cart), so the icons and the app agree.
 *
 * WHY THE VARIANTS DIFFER
 * - "any" (manifest, purpose omitted = any): rounded tile, transparent outside,
 *   because the launcher shows the icon as drawn.
 * - maskable: full bleed, no rounding (the OS applies its own shape) and the
 *   glyph kept inside the central 80% safe circle, so a circle, squircle or
 *   teardrop mask never clips it.
 * - monochrome: the glyph alone on transparent, for Android 13 themed icons.
 * - apple-touch-icon: full bleed and opaque; iOS applies its own squircle.
 * - shortcuts: 96 px, rounded like the app icon, so they read as one family.
 * - favicon.ico: 16/32/48, the app mark (PNG-in-ICO, which every browser since
 *   Vista reads).
 * - apple-touch-startup-image: the light --bg page colour with the mark
 *   centred, matching the manifest's background_color.
 */
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "@playwright/test";

// Colour tokens (KB architecture/theme-tokens.md, apps/ui/app/globals.css).
const ACCENT_FILL = "#0b7285"; // --accent-fill, identical in both schemes
const BG_LIGHT = "#f8f9fa"; // --bg, light
const WHITE = "#ffffff";

const iconsDir = new URL("../public/icons/", import.meta.url).pathname;
const publicDir = new URL("../public/", import.meta.url).pathname;

// Tabler glyphs, 24x24, as the mockup draws them (artifact #75 v1 symbols).
const GLYPHS = {
  // #i-house, the brand tile in the sidebar and the drawer.
  home: "M5 12H3l9-9 9 9h-2M5 12v7a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-7M10 21v-5a2 2 0 0 1 4 0v5",
  // #i-today: Today is the sun.
  sun: "M3 12h1m8-9v1m8 8h1m-9 8v1M5.6 5.6l.7.7m12.1-.7-.7.7m0 11.4.7.7m-12.1-.7-.7.7",
  // #i-cal
  calendar: "M16 3v4M8 3v4M4 11h16M8 15h2v2H8z",
  // #i-meals
  meals: "M5 3v6a2 2 0 0 0 4 0V3M7 11v10M17 21V3c-2.2 1.2-3 3.8-3 7.5V13h3",
  // #i-cart: the grocery list.
  cart: "M17 17H6V3H4M6 5l14 1-1 7H6",
};
/** Glyphs drawn with a shape as well as a path (the sun's disc, the cart's wheels). */
const SHAPES = {
  sun: '<circle cx="12" cy="12" r="4"/>',
  calendar: '<rect x="4" y="5" width="16" height="16" rx="2"/>',
  cart: '<circle cx="6" cy="19" r="2"/><circle cx="17" cy="19" r="2"/>',
};
// Mockup `.logo` uses stroke-width 2 in a 24-unit box.
const STROKE = 2;
const BOX = 24;

/**
 * An icon: `background` behind `glyph` (white), the glyph's 24-unit box scaled
 * to `glyphRatio` of the shorter side and centred.
 */
function icon({ size, width = size, height = size, glyph, glyphRatio, rounded = false, background, ink = WHITE }) {
  const span = glyphRatio * Math.min(width, height);
  const k = span / BOX;
  const tx = (width - BOX * k) / 2;
  const ty = (height - BOX * k) / 2;
  const radius = rounded ? Math.min(width, height) * 0.225 : 0;
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}">
  ${background ? `<rect width="${width}" height="${height}" rx="${radius}" ry="${radius}" fill="${background}"/>` : ""}
  <g transform="translate(${tx} ${ty}) scale(${k})" fill="none" stroke="${ink}"
     stroke-width="${STROKE}" stroke-linecap="round" stroke-linejoin="round">
    ${SHAPES[glyph] ?? ""}<path d="${GLYPHS[glyph]}"/>
  </g>
</svg>
`;
}

/**
 * Full-bleed icon whose glyph must survive any mask. The maskable safe zone is
 * a circle of radius 40% of the icon width at its centre (the "safe zone" in
 * the manifest spec), and any shape outside it — a circle, squircle or
 * teardrop — may be cut away, so the glyph's whole ink has to sit inside.
 *
 * 0.74 is measured, not guessed: at that box size the rendered ink reaches
 * 0.358 of the width from the centre, about 90% of the 0.4 radius, which
 * leaves room for the anti-aliased stroke end while still filling the tile.
 * The house outline's corners are empty, which is why a bounding-box estimate
 * would be far too conservative. app/pwa.test.mjs re-measures the pixels of
 * every written maskable icon, so changing this number is checked, not trusted.
 */
const maskable = (size) => icon({ size, glyph: "home", glyphRatio: 0.74, background: ACCENT_FILL });

const ICONS = [
  // manifest "any": the launcher shows the tile as drawn.
  { file: "icon-192.png", size: 192, svg: () => icon({ size: 192, glyph: "home", glyphRatio: 0.62, rounded: true, background: ACCENT_FILL }) },
  { file: "icon-512.png", size: 512, svg: () => icon({ size: 512, glyph: "home", glyphRatio: 0.62, rounded: true, background: ACCENT_FILL }) },
  // maskable: full bleed, glyph inside the 80% safe circle.
  { file: "icon-maskable-192.png", size: 192, svg: () => maskable(192) },
  { file: "icon-maskable-512.png", size: 512, svg: () => maskable(512) },
  // monochrome: the glyph alone, for Android 13 themed icons.
  { file: "icon-monochrome-512.png", size: 512, svg: () => icon({ size: 512, glyph: "home", glyphRatio: 0.62, ink: WHITE }) },
  // iOS home screen: opaque, full bleed (iOS masks it itself).
  { file: "apple-touch-icon.png", size: 180, svg: () => icon({ size: 180, glyph: "home", glyphRatio: 0.6, background: ACCENT_FILL }) },
  // manifest shortcuts (96 px is what Chrome and Android ask for).
  { file: "shortcut-today.png", size: 96, svg: () => icon({ size: 96, glyph: "sun", glyphRatio: 0.52, rounded: true, background: ACCENT_FILL }) },
  { file: "shortcut-calendar.png", size: 96, svg: () => icon({ size: 96, glyph: "calendar", glyphRatio: 0.52, rounded: true, background: ACCENT_FILL }) },
  { file: "shortcut-meals.png", size: 96, svg: () => icon({ size: 96, glyph: "meals", glyphRatio: 0.52, rounded: true, background: ACCENT_FILL }) },
  { file: "shortcut-groceries.png", size: 96, svg: () => icon({ size: 96, glyph: "cart", glyphRatio: 0.52, rounded: true, background: ACCENT_FILL }) },
];

// apple-touch-startup-image, portrait. iOS only uses an entry whose media query
// matches the device exactly, so each file has its own query in layout.tsx.
export const SPLASHES = [
  { file: "splash/apple-splash-1290x2796.png", width: 1290, height: 2796, css: [430, 932, 3] }, // 16/15 Pro Max
  { file: "splash/apple-splash-1206x2622.png", width: 1206, height: 2622, css: [402, 874, 3] }, // 16 Pro
  { file: "splash/apple-splash-1179x2556.png", width: 1179, height: 2556, css: [393, 852, 3] }, // 15 / 15 Pro / 14 Pro
  { file: "splash/apple-splash-1170x2532.png", width: 1170, height: 2532, css: [390, 844, 3] }, // 14 / 13 / 12
  { file: "splash/apple-splash-1125x2436.png", width: 1125, height: 2436, css: [375, 812, 3] }, // 13 mini / X / 11 Pro
  { file: "splash/apple-splash-750x1334.png", width: 750, height: 1334, css: [375, 667, 2] }, // SE 2/3, 8
];

/**
 * iOS launch screen: the light --bg page colour with the app mark centred, so
 * the splash hands off to the app's first paint without a colour jump.
 */
function splash({ width, height }) {
  const d = Math.round(Math.min(width, height) * 0.19); // the accent tile
  const x = Math.round((width - d) / 2);
  const y = Math.round((height - d) / 2);
  const radius = Math.round(d * 0.225);
  const k = (d * 0.64) / BOX; // mockup .logo: an 18 px glyph in a 28 px tile
  const tx = x + (d - BOX * k) / 2;
  const ty = y + (d - BOX * k) / 2;
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}">
  <rect width="${width}" height="${height}" fill="${BG_LIGHT}"/>
  <rect x="${x}" y="${y}" width="${d}" height="${d}" rx="${radius}" ry="${radius}" fill="${ACCENT_FILL}"/>
  <g transform="translate(${tx} ${ty}) scale(${k})" fill="none" stroke="${WHITE}"
     stroke-width="${STROKE}" stroke-linecap="round" stroke-linejoin="round">
    <path d="${GLYPHS.home}"/>
  </g>
</svg>
`;
}

/** ICO container holding PNG payloads (read by every browser since Vista). */
function ico(images) {
  const header = Buffer.alloc(6);
  header.writeUInt16LE(0, 0); // reserved
  header.writeUInt16LE(1, 2); // 1 = icon
  header.writeUInt16LE(images.length, 4);
  let offset = 6 + images.length * 16;
  const entries = [];
  for (const { size, png } of images) {
    const e = Buffer.alloc(16);
    e.writeUInt8(size >= 256 ? 0 : size, 0); // width (0 means 256)
    e.writeUInt8(size >= 256 ? 0 : size, 1); // height
    e.writeUInt8(0, 2); // palette colours
    e.writeUInt8(0, 3); // reserved
    e.writeUInt16LE(1, 4); // colour planes
    e.writeUInt16LE(32, 6); // bits per pixel
    e.writeUInt32LE(png.length, 8);
    e.writeUInt32LE(offset, 12);
    offset += png.length;
    entries.push(e);
  }
  return Buffer.concat([header, ...entries, ...images.map((i) => i.png)]);
}

async function main() {
  mkdirSync(iconsDir, { recursive: true });
  mkdirSync(join(iconsDir, "splash"), { recursive: true });

  // The vector source, so the mark stays editable and is a usable favicon.svg.
  writeFileSync(
    join(iconsDir, "icon.svg"),
    icon({ size: 512, glyph: "home", glyphRatio: 0.62, rounded: true, background: ACCENT_FILL }),
  );

  const browser = await chromium.launch();
  const page = await browser.newPage({ deviceScaleFactor: 1 });

  /** Renders `markup` at exactly width x height and returns the PNG bytes. */
  async function render(markup, width, height) {
    await page.setViewportSize({ width, height });
    await page.setContent(
      `<html><body style="margin:0;padding:0;background:transparent">${markup}</body></html>`,
      { waitUntil: "load" },
    );
    const box = await page.locator("svg").first().boundingBox();
    if (!box || Math.round(box.width) !== width || Math.round(box.height) !== height) {
      throw new Error(`render: expected ${width}x${height}, element is ${JSON.stringify(box)}`);
    }
    return page.screenshot({ clip: { x: 0, y: 0, width, height }, omitBackground: true, type: "png" });
  }

  for (const entry of ICONS) {
    const png = await render(entry.svg(), entry.size, entry.size);
    writeFileSync(join(iconsDir, entry.file), png);
    console.log(`wrote icons/${entry.file} (${entry.size}x${entry.size}, ${png.length} bytes)`);
  }

  const faviconPngs = [];
  for (const size of [16, 32, 48]) {
    const png = await render(
      icon({ size, glyph: "home", glyphRatio: 0.64, rounded: true, background: ACCENT_FILL }),
      size,
      size,
    );
    faviconPngs.push({ size, png });
  }
  writeFileSync(join(publicDir, "favicon.ico"), ico(faviconPngs));
  console.log("wrote favicon.ico (16/32/48)");

  for (const shot of SPLASHES) {
    const png = await render(splash(shot), shot.width, shot.height);
    writeFileSync(join(iconsDir, shot.file), png);
    console.log(`wrote icons/${shot.file} (${shot.width}x${shot.height}, ${png.length} bytes)`);
  }

  await browser.close();

  console.log("\nstartupImage media queries for app/layout.tsx:");
  for (const shot of SPLASHES) {
    const [w, h, dpr] = shot.css;
    console.log(
      `  ${shot.file}: (device-width: ${w}px) and (device-height: ${h}px) and (-webkit-device-pixel-ratio: ${dpr}) and (orientation: portrait)`,
    );
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});