import type { Page, TestInfo } from "@playwright/test";
import { test, expect } from "./fixtures";

/**
 * Entity detail layout (#4531): components/layout/DetailPageLayout.tsx.
 * Phone: sticky title bar (back, title, actions menu) and collapsible
 * Details / Resources sections, no horizontal scroll. Desktop: two columns.
 * Viewports and colour schemes are pinned per test, so this runs once, in the
 * desktop-chrome project. Screenshots are attached for a visual check.
 */
const DETAIL = "/dashboard/bills/b1";
const BILL = {
  id: "b1",
  name: "Duke Energy electric — Main Residence (autopay)",
  amount: 142.37,
  due_day: 14,
  category: "Electric",
  account_number: "9100-2231-4478-0021-5590",
  payment_url: "https://www.duke-energy.com/my-account/sign-in/billing-and-payments",
  is_autopay: true,
  notes: "Budget billing since March. Call 800-777-9898 if the autopay fails twice.",
};

test.beforeEach(async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chrome", "viewports are pinned per test");
  await page.route("**/bff/bills/b1", (route) => route.fulfill({ json: { data: BILL } }));
});

async function gotoDetail(page: Page) {
  await page.goto(DETAIL);
  await expect(page.getByRole("heading", { level: 2, name: BILL.name })).toBeVisible();
}

const noHorizontalScroll = (page: Page) =>
  page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);

/** Rendered colour of an element, and the token it should equal, both as rgb(). */
async function fieldColours(page: Page, field: string) {
  return page.locator(`main [data-field="${field}"]`).first().evaluate((row) => {
    const probe = (v: string) => {
      const el = document.createElement("span");
      el.style.color = `var(${v})`;
      document.body.appendChild(el);
      const c = getComputedStyle(el).color;
      el.remove();
      return c;
    };
    const lum = (rgb: string) => {
      const [r, g, b] = rgb.match(/\d+/g)!.slice(0, 3).map((n) => {
        const c = Number(n) / 255;
        return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
      });
      return 0.2126 * r + 0.7152 * g + 0.0722 * b;
    };
    const dd = row.querySelector("dd")!;
    const dt = row.querySelector("dt")!;
    const value = getComputedStyle(dd).color;
    const surface = probe("--surface");
    const [hi, lo] = [lum(value), lum(surface)].sort((a, b) => b - a);
    return {
      value,
      label: getComputedStyle(dt).color,
      ink: probe("--ink"),
      ink3: probe("--ink-3"),
      contrast: (hi + 0.05) / (lo + 0.05),
    };
  });
}

/** Label and value boxes of one field row. */
async function fieldBoxes(page: Page, field: string) {
  const row = page.locator(`main [data-field="${field}"]`).first();
  return {
    label: (await row.locator("dt").boundingBox())!,
    value: (await row.locator("dd").boundingBox())!,
  };
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = false) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme}`, () => {
    test.use({ colorScheme });

    test("phone 390: stacked, collapsible, actions in a menu, no horizontal scroll", async ({ loggedInPage: page }, testInfo) => {
      await page.setViewportSize({ width: 390, height: 844 });
      await gotoDetail(page);

      const main = page.locator("main");
      await expect(main.getByRole("link", { name: "Back to Bills" })).toBeVisible();
      // Edit/Delete live in the actions menu, not as a button row.
      await expect(main.getByRole("button", { name: "Edit", exact: true })).toBeHidden();
      const actions = main.getByRole("button", { name: `Actions for ${BILL.name}` });
      const box = (await actions.boundingBox())!;
      expect(box.width).toBeGreaterThanOrEqual(44);
      expect(box.height).toBeGreaterThanOrEqual(44);

      // Details open, Resources closed; both toggle.
      const details = main.getByRole("button", { name: /^Details/ });
      const resources = main.getByRole("button", { name: /^Resources/ });
      await expect(details).toHaveAttribute("aria-expanded", "true");
      await expect(resources).toHaveAttribute("aria-expanded", "false");
      await expect(main.getByText("Account Number")).toBeVisible();
      await expect(main.getByRole("tablist", { name: "Resource types" })).toBeHidden();
      expect(await noHorizontalScroll(page)).toBe(true);

      // #4537: values are --ink (readable in both schemes), labels --ink-3.
      const colours = await fieldColours(page, "name");
      expect(colours.value).toBe(colours.ink);
      expect(colours.label).toBe(colours.ink3);
      expect(colours.contrast).toBeGreaterThanOrEqual(7);
      // #4537: labels sit above their values with a gap, never run together.
      for (const field of ["name", "account_number", "payment_url"]) {
        const { label, value } = await fieldBoxes(page, field);
        expect(value.y).toBeGreaterThanOrEqual(label.y + label.height);
        expect(Math.abs(value.x - label.x)).toBeLessThan(1);
        expect(value.x + value.width).toBeLessThanOrEqual(390);
      }
      await expect(main.locator('[data-field="name"] dd')).toHaveText(BILL.name);
      await snap(page, testInfo, `detail-390-${colorScheme}`, true);

      await resources.click();
      await expect(resources).toHaveAttribute("aria-expanded", "true");
      await expect(main.getByRole("tablist", { name: "Resource types" })).toBeVisible();
      await details.click();
      await expect(main.getByText("Account Number")).toBeHidden();
      expect(await noHorizontalScroll(page)).toBe(true);
      await snap(page, testInfo, `detail-390-${colorScheme}-collapsed`);

      // Edit from the menu reopens Details and swaps the menu for Cancel / Save.
      await actions.click();
      const del = page.getByRole("menuitem", { name: "Delete" });
      await expect(del).toBeVisible();
      // Destructive item paints in --danger.
      const danger = colorScheme === "light" ? "rgb(201, 42, 42)" : "rgb(255, 135, 135)";
      expect(await del.evaluate((el) => getComputedStyle(el).color)).toBe(danger);
      await page.waitForTimeout(300); // menu fade-in
      await snap(page, testInfo, `detail-390-${colorScheme}-menu`);
      await page.getByRole("menuitem", { name: "Edit" }).click();
      await expect(details).toHaveAttribute("aria-expanded", "true");
      await expect(main.getByRole("button", { name: "Save" })).toBeVisible();
      await expect(main.getByRole("button", { name: "Cancel" })).toBeVisible();
      expect(await noHorizontalScroll(page)).toBe(true);
      // #4537: touch-sized inputs with 16 px text on phones.
      const input = main.getByRole("textbox", { name: "Account Number" });
      expect((await input.boundingBox())!.height).toBeGreaterThanOrEqual(44);
      expect(await input.evaluate((el) => getComputedStyle(el).fontSize)).toBe("16px");

      // The title bar sticks under the shell header while scrolling.
      await page.mouse.wheel(0, 600);
      await page.waitForTimeout(200);
      const headerBottom = (await page.locator("header").boundingBox())!;
      const saveBox = (await main.getByRole("button", { name: "Save" }).boundingBox())!;
      expect(saveBox.y).toBeGreaterThanOrEqual(headerBottom.y + headerBottom.height - 1);
      expect(saveBox.y).toBeLessThan(headerBottom.y + headerBottom.height + 60);
      await snap(page, testInfo, `detail-390-${colorScheme}-editing`);
    });

    test("desktop 1440: two columns with buttons, nothing collapses", async ({ loggedInPage: page }, testInfo) => {
      await page.setViewportSize({ width: 1440, height: 900 });
      await gotoDetail(page);

      const main = page.locator("main");
      await expect(main.getByRole("button", { name: "Edit", exact: true })).toBeVisible();
      await expect(main.getByRole("button", { name: "Delete", exact: true })).toBeVisible();
      await expect(main.getByRole("button", { name: /^Actions for/ })).toBeHidden();
      await expect(main.getByRole("button", { name: /^Details/ })).toBeHidden();

      const details = (await main.getByRole("region", { name: "Details" }).boundingBox())!;
      const resources = (await main.getByRole("region", { name: "Resources" }).boundingBox())!;
      expect(resources.x).toBeGreaterThan(details.x + details.width - 1);
      expect(Math.abs(resources.y - details.y)).toBeLessThan(2);
      await expect(main.getByRole("tablist", { name: "Resource types" })).toBeVisible();
      expect(await noHorizontalScroll(page)).toBe(true);

      // #4537: label/value grid on desktop, values readable.
      const colours = await fieldColours(page, "name");
      expect(colours.value).toBe(colours.ink);
      expect(colours.contrast).toBeGreaterThanOrEqual(7);
      const { label, value } = await fieldBoxes(page, "name");
      expect(value.x).toBeGreaterThanOrEqual(label.x + label.width + 16);
      expect(Math.abs(value.y - label.y)).toBeLessThan(2);
      await snap(page, testInfo, `detail-1440-${colorScheme}`);
    });
  });
}
