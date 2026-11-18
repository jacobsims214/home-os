/**
 * The Home OS brand mark: the accent tile with the house glyph (#5200, D11).
 *
 * THE GLYPH IS TRANSCRIBED, NOT REDRAWN. The approved shell mockup
 * (artifact #75 v1, KB design/app-shell.md) draws the brand row as
 *
 *   <div class="brand"><span class="logo"><svg class="i"><use href="#i-house"/></svg></span>Home OS</div>
 *
 * with `.logo` a 28x28 tile, border-radius 8px, background `--accent-fill`, a
 * white glyph, `display:grid; place-items:center`; `.logo svg.i` is 18x18 at
 * `stroke-width:2`, over the shared `.i` rule (fill none, stroke
 * currentColor, round caps and joins). The
 * `#i-house` symbol in that artifact is three paths on a 24x24 grid:
 *
 *   M5 12H3l9-9 9 9h-2
 *   M5 12v7a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-7
 *   M10 21v-5a2 2 0 0 1 4 0v5
 *
 * which are the Tabler `home` outline at the mockup's 2/24 stroke — the same
 * geometry public/icons/icon.svg and scripts/generate-pwa-icons.mjs already
 * use (#4533). Only the in-app brand component was still drawing a letter
 * "H"; the glyph here is that symbol, path for path.
 *
 * Sizes derive from the mockup's 28 px tile and 18 px glyph: pass `size` for
 * the tile and the glyph keeps the 18/28 ratio, so a 28 px mark is exactly
 * what the artifact shows. `strokeWidth` is on the 24 grid, so it thickens
 * with the glyph (as the artifact's fixed `stroke-width: 2` does).
 *
 * Colour comes from tokens only (app/globals.css): `--accent-fill` is cyan.9,
 * the same value in both schemes, and the glyph is white, so the mark is
 * identical in light and dark — 5.59:1 white-on-accent, the contrast the token
 * table in design/app-shell.md records for accent fills. (No literal colours
 * here or in the CSS: app/theme-tokens.test.mjs bans them in components.)
 *
 * Decorative by default (aria-hidden): in every current placement the wordmark
 * "Home OS" sits beside it and already names the product. Pass `label` when
 * the mark stands alone and must be announced.
 */
import classes from "./HomeMark.module.css";

export interface HomeMarkProps {
  /** Edge of the accent tile in CSS px. The shell's brand row uses the mockup's 28. */
  size?: number;
  /** Accessible name. Omit where a visible "Home OS" wordmark already names it. */
  label?: string;
  className?: string;
}

/** The artifact's 18 px glyph on its 28 px tile. */
const GLYPH_RATIO = 18 / 28;

export default function HomeMark({ size = 28, label, className }: HomeMarkProps) {
  const glyph = Math.round(size * GLYPH_RATIO);
  return (
    <span
      className={className ? `${classes.tile} ${className}` : classes.tile}
      style={{ width: size, height: size }}
      data-home-mark=""
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    >
      <svg
        className={classes.glyph}
        width={glyph}
        height={glyph}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth={2}
        strokeLinecap="round"
        strokeLinejoin="round"
        focusable="false"
      >
        <path d="M5 12H3l9-9 9 9h-2" />
        <path d="M5 12v7a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-7" />
        <path d="M10 21v-5a2 2 0 0 1 4 0v5" />
      </svg>
    </span>
  );
}
