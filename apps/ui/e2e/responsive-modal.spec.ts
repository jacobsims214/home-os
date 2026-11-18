import type { Locator, Page, TestInfo } from "@playwright/test";
import { test, expect, onlyOn, pressOpener } from "./fixtures";

/**
 * ResponsiveModal (#4532): components/ui/ResponsiveModal.tsx, exercised
 * through EntityList's "Add bill" create form (its first consumer).
 * - 390 px: a full-width bottom sheet with a 16 px top radius and a handle.
 * - 1440 px: a centred dialog.
 * - Both: Tab stays inside, Esc closes and focus returns to the opener, the
 *   back button closes it without leaving the page, tokens in both schemes.
 * - Keyboard-safe: window.visualViewport is replaced with a fake that
 *   shrinks like it does when the iOS keyboard opens (Playwright can't show
 *   a real on-screen keyboard); the sheet, the focused field and the actions
 *   must all fit in the visible part.
 * Viewports and colour schemes are pinned per test: desktop-chrome, and the
 * phone tests on iphone-390 too (WebKit, touch; #4757).
 */

const PROPERTIES = [{ id: "p1", name: "Main house" }];
const BILLS = [{ id: "b1", name: "Internet", amount: "70", due_day: 3, category: "Internet", property_id: "p1" }];
const KEYBOARD = 336; // iPhone portrait keyboard + accessory bar, roughly

async function openAddBill(page: Page): Promise<{ add: Locator; dialog: Locator; openedAt: number }> {
  await page.route("**/bff/properties", (route) => route.fulfill({ json: { data: PROPERTIES } }));
  await page.route("**/bff/bills", (route) => route.fulfill({ json: { data: BILLS } }));
  await page.goto("/dashboard/bills");
  const main = page.locator("main");
  await expect(main.getByRole("heading", { level: 2, name: "Bills" })).toBeVisible();
  await expect(main.getByText("Internet").filter({ visible: true }).first()).toBeVisible();
  const add = main.getByRole("button", { name: "Add bill" });
  const openedAt = await historyIndex(page);
  // From the keyboard: expectEscCloses checks focus goes back to the opener
  // (see pressOpener). expectBackCloses opens it with a click/tap.
  await pressOpener(add);
  const dialog = page.getByRole("dialog", { name: "Add bill" });
  await expect(dialog).toBeVisible();
  // Wait for the transition to finish (the sheet slides up 240 ms).
  await expect
    .poll(async () => dialog.evaluate((el) => getComputedStyle(el).transform), { timeout: 3000 })
    .toMatch(/^(none|matrix\(1, 0, 0, 1, 0, 0\))$/);
  return { add, dialog, openedAt };
}

async function snap(page: Page, testInfo: TestInfo, name: string) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

/** Tab and Shift+Tab never leave the dialog. */
async function expectFocusTrapped(page: Page, dialog: Locator) {
  for (const key of [...Array(12).fill("Tab"), ...Array(6).fill("Shift+Tab")]) {
    await page.keyboard.press(key);
    expect(await dialog.evaluate((el) => el.contains(document.activeElement)), `focus after ${key}`).toBe(true);
  }
}

async function expectTokens(dialog: Locator) {
  const [bg, raised] = await dialog.evaluate((el) => {
    const probe = document.createElement("div");
    probe.style.background = "var(--raised)";
    document.body.appendChild(probe);
    const want = getComputedStyle(probe).backgroundColor;
    probe.remove();
    return [getComputedStyle(el).backgroundColor, want];
  });
  expect(bg).toBe(raised);
}

/** Esc closes, focus goes back to the opener and no history entry is left. */
async function expectEscCloses(page: Page, add: Locator, dialog: Locator, openedAt: number) {
  await expect.poll(() => historyIndex(page)).toBe(openedAt + 1); // the dialog's entry
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(add).toBeFocused();
  await expect.poll(() => historyIndex(page)).toBe(openedAt); // popped again
}

/** Session history position (Chromium's Navigation API). */
function historyIndex(page: Page): Promise<number> {
  return page.evaluate(() => (window as unknown as { navigation: { currentEntry: { index: number } } }).navigation.currentEntry.index);
}

/** The back button closes the dialog and stays on the page. */
async function expectBackCloses(page: Page, add: Locator) {
  const url = page.url();
  const before = await historyIndex(page);
  await add.click();
  const dialog = page.getByRole("dialog", { name: "Add bill" });
  await expect(dialog).toBeVisible();
  // Opening pushed a same-URL entry for back to pop.
  await expect.poll(() => historyIndex(page)).toBe(before + 1);
  await page.evaluate(() => history.back());
  await expect(dialog).toBeHidden();
  expect(page.url()).toBe(url);
  expect(await historyIndex(page)).toBe(before);
  await expect(page.locator("main").getByRole("heading", { level: 2, name: "Bills" })).toBeVisible();
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(colorScheme, () => {
    test.use({ colorScheme });

    test("phone 390: bottom sheet, focus trap, Esc and back close", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      const { add, dialog, openedAt } = await openAddBill(page);

      const box = (await dialog.boundingBox())!;
      expect(box.x).toBeCloseTo(0, 0);
      expect(box.width).toBeCloseTo(390, 0);
      expect(box.y + box.height).toBeCloseTo(844, 0); // anchored to the bottom
      expect(box.y).toBeGreaterThan(0); // a sheet, with a gap at the top
      expect(box.y).toBeLessThan(40); // full height
      expect(await dialog.evaluate((el) => getComputedStyle(el).borderTopLeftRadius)).toBe("16px");
      expect(await dialog.evaluate((el) => getComputedStyle(el).borderBottomLeftRadius)).toBe("0px");
      await expect(dialog.locator("[data-responsive-modal-handle]")).toBeVisible();
      await expectTokens(dialog);

      // 44 px targets: close, fields, actions.
      const targets = [
        dialog.getByRole("button", { name: "Close" }),
        dialog.getByRole("textbox", { name: "Name" }),
        dialog.getByRole("button", { name: "Cancel" }),
        dialog.getByRole("button", { name: "Add bill" }),
      ];
      for (const t of targets) {
        const b = (await t.boundingBox())!;
        expect(b.height).toBeGreaterThanOrEqual(43.5);
      }
      await expect(dialog.getByRole("textbox", { name: "Name" })).toBeFocused();
      await snap(page, testInfo, `modal-390-${colorScheme}`);

      await expectFocusTrapped(page, dialog);
      await expectEscCloses(page, add, dialog, openedAt);
      await expectBackCloses(page, add);
    });

    test("phone 390: keyboard open keeps the field and actions visible", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      // A controllable visualViewport, installed before the page's code runs.
      await page.addInitScript(() => {
        const fake = Object.assign(new EventTarget(), {
          width: window.innerWidth,
          height: window.innerHeight,
          offsetTop: 0,
          offsetLeft: 0,
          pageTop: 0,
          pageLeft: 0,
          scale: 1,
        });
        Object.defineProperty(window, "visualViewport", { configurable: true, get: () => fake });
        (window as unknown as { __vv: typeof fake }).__vv = fake;
      });
      const { dialog } = await openAddBill(page);

      // The keyboard opens: the visual viewport loses KEYBOARD px at the bottom.
      const visible = 844 - KEYBOARD;
      await page.evaluate((h) => {
        const vv = (window as unknown as { __vv: { height: number } & EventTarget }).__vv;
        vv.height = h;
        vv.dispatchEvent(new Event("resize"));
      }, visible);
      const field = dialog.getByRole("textbox", { name: "Due day of month" });
      await field.focus();

      await expect.poll(async () => (await dialog.boundingBox())!.y + (await dialog.boundingBox())!.height).toBeLessThanOrEqual(visible + 0.5);
      const sheet = (await dialog.boundingBox())!;
      await expect
        .poll(async () => {
          const b = (await field.boundingBox())!;
          return b.y >= sheet.y && b.y + b.height <= visible;
        })
        .toBe(true);
      for (const name of ["Cancel", "Add bill"]) {
        const b = (await dialog.getByRole("button", { name }).boundingBox())!;
        expect(b.y, `${name} top`).toBeGreaterThanOrEqual(sheet.y);
        expect(b.y + b.height, `${name} bottom`).toBeLessThanOrEqual(visible + 0.5);
      }
      // The focused field isn't hidden behind the sticky actions bar.
      const actions = (await dialog.locator("[data-responsive-modal-actions]").boundingBox())!;
      const f = (await field.boundingBox())!;
      expect(f.y + f.height).toBeLessThanOrEqual(actions.y + 0.5);
      await snap(page, testInfo, `modal-390-${colorScheme}-keyboard`);

      // Keyboard closes: the sheet grows back to full height.
      await page.evaluate(() => {
        const vv = (window as unknown as { __vv: { height: number } & EventTarget }).__vv;
        vv.height = 844;
        vv.dispatchEvent(new Event("resize"));
      });
      await expect.poll(async () => Math.round((await dialog.boundingBox())!.y + (await dialog.boundingBox())!.height)).toBe(844);
    });

    test("desktop 1440: centred dialog, focus trap, Esc and back close", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("desktop");
      await page.setViewportSize({ width: 1440, height: 900 });
      const { add, dialog, openedAt } = await openAddBill(page);

      const box = (await dialog.boundingBox())!;
      expect(box.width).toBeLessThan(700);
      expect(Math.abs(box.x + box.width / 2 - 720)).toBeLessThan(2);
      expect(Math.abs(box.y + box.height / 2 - 450)).toBeLessThan(2);
      expect(await dialog.evaluate((el) => getComputedStyle(el).borderTopLeftRadius)).toBe("12px");
      await expect(dialog.locator("[data-responsive-modal-handle]")).toBeHidden(); // sheets only
      await expectTokens(dialog);
      expect((await dialog.getByRole("button", { name: "Close" }).boundingBox())!.height).toBeGreaterThanOrEqual(43.5);
      await expect(dialog.getByRole("textbox", { name: "Name" })).toBeFocused();
      await snap(page, testInfo, `modal-1440-${colorScheme}`);

      await expectFocusTrapped(page, dialog);
      await expectEscCloses(page, add, dialog, openedAt);
      await expectBackCloses(page, add);
    });
  });
}

test("submit navigates to the new record, and Back returns to the list", async ({ loggedInPage: page }) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  const { dialog, openedAt } = await openAddBill(page);
  // Registered after openAddBill's stubs, so it's tried first.
  await page.route("**/bff/bills", (route) =>
    route.request().method() === "POST"
      ? route.fulfill({ status: 201, json: { data: { id: "b9", name: "Water" } } })
      : route.fallback(),
  );
  await page.route("**/bff/bills/b9", (route) => route.fulfill({ json: { data: { id: "b9", name: "Water", amount: "45", due_day: 9 } } }));
  await dialog.getByRole("textbox", { name: "Name" }).fill("Water");
  await dialog.getByRole("textbox", { name: "Amount" }).fill("45");
  await dialog.getByRole("textbox", { name: "Due day of month" }).fill("9");
  await dialog.getByRole("button", { name: "Add bill" }).click();
  await page.waitForURL("**/dashboard/bills/b9");
  // router.replace swapped out the dialog's entry: no extra entry is left.
  expect(await historyIndex(page)).toBe(openedAt + 1);
  await page.goBack();
  await page.waitForURL(/\/dashboard\/bills$/);
  await expect(page.locator("main").getByRole("heading", { level: 2, name: "Bills" })).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
});
