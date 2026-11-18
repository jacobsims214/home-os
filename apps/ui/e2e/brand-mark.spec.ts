import type { Locator, Page } from "@playwright/test";
import { test, expect, onlyOn } from "./fixtures";

/**
 * The brand mark (#5200, findings D11). The approved shell mockup (artifact
 * #75 v1, KB design/app-shell.md) puts the house glyph on a 28 px accent tile
 * beside the "Home OS" wordmark; the build shipped a letter "H" instead.
 *
 * These tests hold the mark to the artifact rather than to a screenshot: the
 * three paths are the artifact's `#i-house` symbol, the tile is a 28 px square
 * at radius 8 on `--accent-fill`, and the glyph is 18 px of white stroke at
 * the mockup's 2/24 weight — in both colour schemes, since `--accent-fill` is
 * #0b7285 either way.
 *
 * Phone assertions run in the drawer (the phone's only nav surface that draws
 * the brand), desktop ones in the sidebar. tablet/desktop widths only in
 * desktop-chrome, phones in desktop-chrome and iphone-390 (#4757).
 */
const HOME = "/dashboard";

/** `#i-house` in artifact #75 v1, path for path. */
const ARTIFACT_PATHS = [
  "M5 12H3l9-9 9 9h-2",
  "M5 12v7a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-7",
  "M10 21v-5a2 2 0 0 1 4 0v5",
];
const ACCENT_FILL = "rgb(11, 114, 133)"; // #0b7285, identical in both schemes
const WHITE = "rgb(255, 255, 255)";

async function gotoHome(page: Page) {
  await page.goto(HOME);
  await expect(page.getByRole("heading", { name: /^(Good morning|Good afternoon|Good evening|Today)/, level: 2 })).toBeVisible();
}

/** Everything the artifact fixes about the mark, asserted on the rendered DOM. */
async function expectArtifactMark(page: Page, scope: Locator) {
  const mark = scope.locator("[data-home-mark]");
  await expect(mark).toBeVisible();

  // The tile: 28 x 28, radius 8, --accent-fill, as `.logo` in the mockup.
  const tile = await mark.evaluate((el) => {
    const cs = getComputedStyle(el);
    const r = el.getBoundingClientRect();
    return { width: r.width, height: r.height, radius: cs.borderRadius, background: cs.backgroundColor, text: (el.textContent ?? "").trim() };
  });
  expect(tile.width).toBe(28);
  expect(tile.height).toBe(28);
  expect(tile.radius).toBe("8px");
  expect(tile.background).toBe(ACCENT_FILL);
  // The bug this ticket fixes: the tile held the letter "H", not a glyph.
  expect(tile.text, "the brand tile must draw a glyph, not a letter").toBe("");

  // The glyph: `#i-house` on the 24 grid, 18 px, white, stroke 2, round joins.
  const glyph = await mark.locator("svg").evaluate((el) => {
    const cs = getComputedStyle(el);
    return {
      viewBox: el.getAttribute("viewBox"),
      width: el.getAttribute("width"),
      height: el.getAttribute("height"),
      paths: Array.from(el.querySelectorAll("path")).map((p) => p.getAttribute("d")),
      fill: el.getAttribute("fill"),
      stroke: cs.stroke,
      strokeWidth: el.getAttribute("stroke-width"),
      linecap: el.getAttribute("stroke-linecap"),
      linejoin: el.getAttribute("stroke-linejoin"),
    };
  });
  expect(glyph.viewBox).toBe("0 0 24 24");
  expect(glyph.paths).toEqual(ARTIFACT_PATHS);
  expect(glyph.width).toBe("18");
  expect(glyph.height).toBe("18");
  expect(glyph.fill).toBe("none");
  expect(glyph.strokeWidth).toBe("2");
  expect(glyph.linecap).toBe("round");
  expect(glyph.linejoin).toBe("round");
  // White glyph on #0b7285 is 5.59:1 — the accent-fill pair in the spec's
  // token table — and it does not change with the scheme.
  expect(glyph.stroke).toBe(WHITE);

  // The wordmark still names the product next to the mark.
  await expect(scope.getByText("Home OS", { exact: true })).toBeVisible();
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`brand mark, ${colorScheme}`, () => {
    test.use({ colorScheme });

    test("phone 390: the drawer draws the artifact's house mark, not a letter", async ({ loggedInPage: page }) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await gotoHome(page);

      await page.locator("header").getByRole("button", { name: "Open menu" }).click();
      const drawer = page.getByRole("dialog");
      await expect(drawer).toBeVisible();
      await expectArtifactMark(page, drawer);

      // And it is still the mark once the drawer is closed and reopened — no
      // transient fallback letter while the panel mounts.
      await page.keyboard.press("Escape");
      await expect(drawer).toBeHidden();
      await page.locator("header").getByRole("button", { name: "Open menu" }).click();
      await expectArtifactMark(page, drawer);
    });

    test("desktop 1440: the sidebar brand row draws the artifact's house mark", async ({ loggedInPage: page }) => {
      onlyOn("desktop");
      await page.setViewportSize({ width: 1440, height: 900 });
      await gotoHome(page);
      await expectArtifactMark(page, page.locator('[data-testid="nav-sidebar"]'));
    });
  });
}
