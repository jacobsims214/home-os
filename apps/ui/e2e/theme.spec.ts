import type { Page, TestInfo } from "@playwright/test";
import { test, expect } from "./fixtures";

/**
 * Theme tokens (#4529), per KB design/app-shell.md: the app follows the
 * system colour scheme until the user picks Light / Dark in the account menu,
 * and the shell paints from the semantic tokens in app/globals.css.
 * Viewports and colour schemes are pinned per test, so this runs once, in the
 * desktop-chrome project. Each test attaches screenshots for a visual check.
 */
const HOME = "/dashboard";

test.beforeEach(({}, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chrome", "viewports are pinned per test");
});

async function gotoHome(page: Page) {
  await page.goto(HOME);
  await expect(page.getByRole("heading", { name: "Dashboard", level: 2 })).toBeVisible();
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
    await expect(page.getByRole("heading", { name: "Dashboard", level: 2 })).toBeVisible();
    expect(await scheme(page)).toBe("dark");

    await pick("System");
    await expect.poll(() => scheme(page)).toBe("light");
  });

  test("an explicit Dark from the old toggle carries over", async ({ loggedInPage: page }) => {
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
