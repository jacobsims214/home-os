import type { Page, TestInfo } from "@playwright/test";
import { test, expect, onlyOn } from "./fixtures";

/**
 * Theme tokens (#4529), per KB design/app-shell.md: the app follows the
 * system colour scheme until the user picks Light / Dark in the account menu,
 * and the shell paints from the semantic tokens in app/globals.css.
 * Viewports and colour schemes are pinned per test: desktop-chrome, and phone
 * widths on iphone-390 too (WebKit, touch; #4757). Each test attaches screenshots for a visual check.
 */
const HOME = "/dashboard";

async function gotoHome(page: Page) {
  await page.goto(HOME);
  await expect(page.getByRole("heading", { name: /^(Good morning|Good afternoon|Good evening|Today)/, level: 2 })).toBeVisible();
}

const scheme = (page: Page) => page.locator("html").getAttribute("data-mantine-color-scheme");
const cssVar = (page: Page, name: string) =>
  page.evaluate((n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim(), name);
const bg = (page: Page, selector: string) =>
  page.locator(selector).first().evaluate((el) => getComputedStyle(el).backgroundColor);

async function snap(page: Page, testInfo: TestInfo, name: string) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

for (const colorScheme of ["light", "dark"] as const) {
  const surface = colorScheme === "light" ? "rgb(255, 255, 255)" : "rgb(36, 36, 36)";
  const pageBg = colorScheme === "light" ? "rgb(248, 249, 250)" : "rgb(31, 31, 31)";

  test.describe(`system ${colorScheme}`, () => {
    test.use({ colorScheme });

    test("phone 390: follows the system scheme; shell paints from tokens", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await gotoHome(page);
      expect(await scheme(page)).toBe(colorScheme);
      expect(await bg(page, "header")).toBe(surface);
      expect(await bg(page, "main")).toBe(pageBg);
      expect(await bg(page, "footer")).toBe(surface);
      await snap(page, testInfo, `phone-390-${colorScheme}`);

      await page.locator("header").getByRole("button", { name: "Open menu" }).click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await page.waitForTimeout(400); // drawer transition
      await snap(page, testInfo, `phone-390-${colorScheme}-drawer`);
      await page.keyboard.press("Escape");

      await page.getByRole("button", { name: /^Account menu for/ }).click();
      await expect(page.getByRole("radiogroup", { name: "Theme" })).toBeVisible();
      await page.waitForTimeout(300); // menu fade-in
      await snap(page, testInfo, `phone-390-${colorScheme}-account-menu`);
    });

    test("desktop 1440: follows the system scheme; sidebar paints from tokens", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("desktop");
      await page.setViewportSize({ width: 1440, height: 900 });
      await gotoHome(page);
      expect(await scheme(page)).toBe(colorScheme);
      expect(await bg(page, "header")).toBe(surface);
      expect(await bg(page, "nav[aria-label='Main navigation']")).toBe(surface);
      expect(await cssVar(page, "--accent")).toBe(colorScheme === "light" ? "#0b7285" : "#66d9e8");
      await snap(page, testInfo, `desktop-1440-${colorScheme}`);
    });
  });
}

test.describe("manual override", () => {
  test.use({ colorScheme: "light" });

  test("picking Dark overrides a light system and survives a reload; System goes back", async ({ loggedInPage: page }) => {
    onlyOn("phone");
    await page.setViewportSize({ width: 390, height: 844 });
    await gotoHome(page);
    expect(await scheme(page)).toBe("light");

    const pick = async (label: string) => {
      await page.getByRole("button", { name: /^Account menu for/ }).click();
      await page.getByRole("radiogroup", { name: "Theme" }).getByText(label, { exact: true }).click();
      await page.keyboard.press("Escape");
    };

    await pick("Dark");
    await expect.poll(() => scheme(page)).toBe("dark");
    await page.reload();
    await expect(page.getByRole("heading", { name: /^(Good morning|Good afternoon|Good evening|Today)/, level: 2 })).toBeVisible();
    expect(await scheme(page)).toBe("dark");

    await pick("System");
    await expect.poll(() => scheme(page)).toBe("light");
  });

  test("an explicit Dark from the old toggle carries over", async ({ loggedInPage: page }) => {
    onlyOn("desktop");
    await page.addInitScript(() => {
      if (!sessionStorage.getItem("seeded")) {
        localStorage.removeItem("home-os-color-scheme");
        localStorage.setItem("mantine-color-scheme", '"dark"');
        sessionStorage.setItem("seeded", "1");
      }
    });
    await gotoHome(page);
    await expect.poll(() => scheme(page)).toBe("dark");
    expect(await page.evaluate(() => localStorage.getItem("home-os-color-scheme"))).toBe("dark");
  });
});

/*
 * Dark-mode bridge (#4753): the legacy-colour remap at the bottom of
 * app/globals.css must not outrank hover:/disabled: variants, and must cover
 * the /NN opacity classes. Checked on a bill's Files tab (drop zone + one file).
 */
const BILL_URL = "/dashboard/bills/b1";
const FILE = {
  id: "f1",
  household_id: "h1",
  blob_id: "bl1",
  name: "statement.pdf",
  content_type: "application/pdf",
  size: 48213,
  entity_type: "bill",
  entity_id: "b1",
  ocr_status: "done",
  tags: null,
  created_at: "2026-11-01T12:00:00Z",
  updated_at: "2026-11-01T12:00:00Z",
};

async function gotoBillFiles(page: Page) {
  await page.route("**/bff/bills/b1", (route) =>
    route.fulfill({ json: { data: { id: "b1", name: "Electric", amount: 42, due_day: 14, category: "Electric", is_autopay: false } } }),
  );
  await page.route((url) => url.pathname === "/bff/files", (route) => route.fulfill({ json: { data: [FILE] } }));
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(BILL_URL);
  await expect(page.getByRole("heading", { level: 2, name: "Electric" })).toBeVisible();
  await expect(page.getByRole("button", { name: `Delete ${FILE.name}` })).toBeVisible();
}

/** A token resolved to rgb(), for comparing with computed colours. */
const tokenRgb = (page: Page, name: string) =>
  page.evaluate((n) => {
    const el = document.createElement("span");
    el.style.color = `var(${n})`;
    document.body.appendChild(el);
    const c = getComputedStyle(el).color;
    el.remove();
    return c;
  }, name);

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`legacy-colour bridge, system ${colorScheme}`, () => {
    test.use({ colorScheme });

    test("delete button: hover turns it the danger colour; disabled shows no red hover", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("desktop");
      await gotoBillFiles(page);
      const del = page.getByRole("button", { name: `Delete ${FILE.name}` });
      const style = () => del.evaluate((el) => ({ color: getComputedStyle(el).color, bg: getComputedStyle(el).backgroundColor }));
      // #4564 rebuilt the file row on the semantic tokens, so the delete
      // button no longer goes through the legacy bridge: its hover is
      // --danger on --surface-2 in BOTH schemes, which is what the tokens
      // promise. (The bridge itself is still covered by SecretsSection, which
      // keeps the legacy classes.)
      const danger = await tokenRgb(page, "--danger");
      const hoverBg = await tokenRgb(page, "--surface-2");

      await del.hover();
      await expect.poll(style).toEqual({ color: danger, bg: hoverBg });
      await snap(page, testInfo, `bridge-delete-hover-${colorScheme}`);

      await page.mouse.move(0, 0);
      await del.evaluate((el) => ((el as HTMLButtonElement).disabled = true));
      await del.hover({ force: true });
      const rest = await tokenRgb(page, "--ink-3");
      await expect.poll(style).toEqual({ color: rest, bg: "rgba(0, 0, 0, 0)" });
    });
  });
}

test.describe("legacy-colour bridge, drop zone", () => {
  test.use({ colorScheme: "dark" });

  test("dark 1440: the Files drop zone passes axe colour-contrast", async ({ loggedInPage: page }, testInfo) => {
    onlyOn("desktop");
    await gotoBillFiles(page);
    // Named by its visible text since #4588 (the old aria-label said
    // "click to upload", words the zone does not show).
    const zone = page.getByRole("button", { name: /^Drop files here, or browse/ });
    await expect(zone).toBeVisible();
    await page.mouse.move(0, 0);
    await page.waitForTimeout(200); // transition-colors
    expect(await zone.evaluate((el) => getComputedStyle(el).backgroundColor)).toBe(await tokenRgb(page, "--surface-2"));

    await page.addScriptTag({ path: require.resolve("axe-core/axe.min.js") });
    const violations = await zone.evaluate(async (el) => {
      const axe = (window as unknown as { axe: { run: (ctx: Element, opts: object) => Promise<{ violations: { id: string; nodes: { target: string[]; failureSummary: string }[] }[] }> } }).axe;
      const res = await axe.run(el, { runOnly: { type: "rule", values: ["color-contrast"] } });
      return res.violations.flatMap((v) => v.nodes.map((n) => `${n.target.join(" ")}: ${n.failureSummary}`));
    });
    expect(violations).toEqual([]);
    await zone.screenshot({ path: testInfo.outputPath("bridge-dropzone-dark.png") });
    await testInfo.attach("bridge-dropzone-dark", { path: testInfo.outputPath("bridge-dropzone-dark.png"), contentType: "image/png" });
  });
});
