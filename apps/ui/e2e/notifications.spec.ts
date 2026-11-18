import type { Page, Route, TestInfo } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { test, expect, expectNoHorizontalOverflow, onlyOn, pressOpener } from "./fixtures";

/**
 * Notifications on phones (#4563), per KB design/app-shell.md ("Notifications":
 * a bottom sheet on phones, a popover from 768 px) and
 * architecture/notification-mark-read.md (#4947: mark-read names rows, there is
 * no mark-all).
 *
 * The rows are stubbed with page.route so every state can be exercised: the
 * stub server answers [] for /bff/notifications. Viewports are pinned; phone
 * widths run in desktop-chrome and on iphone-390 (WebKit, touch, safe areas).
 */

const HOME = "/dashboard";

/** A row as the REST API returns it (created_at is Go's http.TimeFormat). */
const row = (over: Record<string, unknown> = {}) => ({
  id: "n1",
  household_id: "h1",
  type: "item_changed",
  title: "Amber (via moltis) updated bill \"Internet\" (amount)",
  body: "You created this bill. Changed: amount.",
  entity_type: "bill",
  entity_id: "b1",
  created_at: new Date(Date.now() - 2 * 3600_000).toUTCString(),
  ...over,
});

const ROWS = [
  row({
    id: "n1",
    title: "Amber (via moltis) updated bill \"Internet\" (amount)",
    entity_type: "bill",
    entity_id: "b1",
    created_at: new Date(Date.now() - 2 * 3600_000).toUTCString(),
  }),
  row({
    id: "n2",
    type: "bill_due",
    title: "Duke Energy due soon",
    body: "Payment of $142.18 due on Wed Nov 19",
    entity_type: "bill",
    entity_id: "b2",
    created_at: new Date(Date.now() - 26 * 3600_000).toUTCString(),
  }),
  row({
    id: "n3",
    type: "calendar_alarm",
    title: "Uncle Kevin arrives",
    body: "Starts Thu Jan 2, 4:15 PM EST at RDU",
    entity_type: "calendar_event",
    entity_id: "e1",
    created_at: new Date(Date.now() - 3 * 60_000).toUTCString(),
  }),
];

/** Answers GET /bff/notifications with `rows`, and records PATCHes. */
async function stubNotifications(
  page: Page,
  rows: unknown[],
  opts: { failList?: boolean; failMark?: boolean } = {},
): Promise<() => string[]> {
  const writes: string[] = [];
  await page.route("**/bff/notifications**", (route: Route) => {
    if (route.request().method() === "PATCH") {
      writes.push(route.request().url());
      if (opts.failMark) return route.fulfill({ status: 404, json: { error: { code: "NOT_FOUND" } } });
      return route.fulfill({ status: 204, body: "" });
    }
    if (opts.failList) {
      return route.fulfill({ status: 500, json: { error: { code: "INTERNAL", message: "boom" } } });
    }
    return route.fulfill({ json: { data: rows } });
  });
  // The row's open button navigates; keep the detail page from 404-ing.
  await page.route("**/bff/bills/**", (route) =>
    route.fulfill({ json: { data: { id: "b1", name: "Internet", amount: "70" } } }),
  );
  return () => writes;
}

/** Saves a screenshot next to the test's other output (#4533's convention). */
async function snap(page: Page, testInfo: TestInfo, name: string) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

async function gotoHome(page: Page) {
  await page.goto(HOME);
  await expect(page.getByRole("heading", { name: /^(Good morning|Good afternoon|Good evening|Today)/, level: 2 })).toBeVisible();
}

const bell = (page: Page) => page.locator("header").getByRole("button", { name: /^Notifications/ });

/** Opens the panel and waits for it to settle (the sheet slides for 240 ms). */
async function openPanel(page: Page) {
  await pressOpener(bell(page));
  const panel = page.getByRole("dialog", { name: "Notifications" });
  await expect(panel).toBeVisible();
  await expect.poll(() => panel.evaluate((el) => getComputedStyle(el).opacity)).toBe("1");
  return panel;
}

async function expectNoSeriousAxeViolations(page: Page) {
  const { violations } = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  const bad = violations
    .filter((v) => v.impact === "critical" || v.impact === "serious")
    .map((v) => `${v.impact} ${v.id}: ${v.nodes.map((n) => `${n.target.join(" ")} ${n.html.slice(0, 160)}`).join(" | ")}`);
  expect(bad, "critical/serious axe violations").toEqual([]);
}

// ───────────────────────── phone ─────────────────────────

for (const width of [390, 375]) {
  test.describe(`notifications sheet ${width}px`, () => {
    test.use({ viewport: { width, height: 844 } });
    test.beforeEach(() => onlyOn("phone"));

    test("the bell opens a sheet above the tab bar; rows show the actor, the action and the time in full", async ({
      loggedInPage: page,
    }, testInfo) => {
      await stubNotifications(page, ROWS);
      await gotoHome(page);
      await expect(bell(page)).toHaveAccessibleName("Notifications, 3 unread");
      await page.locator("header .mantine-Indicator-indicator").first().waitFor();

      const panel = await openPanel(page);

      // A sheet, not a popover: full width, bottom sheet geometry, and it sits
      // above the tab bar, which stays visible (KB design/app-shell.md).
      const sheetBox = (await panel.boundingBox())!;
      const barBox = (await page.getByTestId("tab-bar").boundingBox())!;
      expect(Math.round(sheetBox.x)).toBe(0);
      expect(Math.round(sheetBox.width)).toBe(width);
      expect(sheetBox.y + sheetBox.height).toBeLessThanOrEqual(barBox.y + 1);
      await expect(page.getByTestId("tab-bar")).toBeVisible();
      // Radius 16 at the top, square at the bottom: a bottom sheet, not a card.
      const radius = await panel.evaluate((el) => getComputedStyle(el).borderRadius);
      expect(radius).toMatch(/^16px 16px 0px 0px$|^16px 16px 0 0$/);

      // The actor is its own line, and the agent that acted is named next to it.
      await expect(panel.getByText("Amber via moltis", { exact: false })).toBeVisible();
      // The action and what changed are shown in full — never "Something changed",
      // and not clipped to an ellipsis.
      const what = panel.getByText(/updated bill "Internet" \(amount\)/);
      await expect(what).toBeVisible();
      const clipped = await what.evaluate((el) => {
        const cs = getComputedStyle(el);
        return cs.textOverflow === "ellipsis" && el.scrollWidth > el.clientWidth + 1;
      });
      expect(clipped, "the action text is truncated").toBe(false);
      // The body (what the writer recorded) is shown too.
      await expect(panel.getByText("You created this bill. Changed: amount.")).toBeVisible();

      // A relative time, with the exact time on the element.
      const time = panel.locator("time").first();
      await expect(time).toContainText(/hour|minute|day/i);
      expect(await time.getAttribute("title")).toBeTruthy();

      // Scope in words: the change notice is personal, the bill reminder is not.
      await expect(panel.getByText("Only you").first()).toBeVisible();
      await expect(panel.getByText("Family-wide").first()).toBeVisible();
      // A family-wide row says so where it is marked read.
      await expect(
        panel.getByText(/marking it read marks it read for all of them/i),
      ).toBeVisible();

      // 44 px hit targets on the phone.
      for (const b of await panel.getByRole("button", { name: /Mark read/ }).all()) {
        const box = (await b.boundingBox())!;
        expect(box.height).toBeGreaterThanOrEqual(44);
      }

      await expectNoHorizontalOverflow(page);
      await expectNoSeriousAxeViolations(page);
      await snap(page, testInfo, `notifications-sheet-${width}`);

      // Esc closes it and focus goes back to the bell.
      await page.keyboard.press("Escape");
      await expect(page.getByRole("dialog", { name: "Notifications" })).toBeHidden();
      await expect(bell(page)).toBeFocused();
      await expect(bell(page)).toHaveAttribute("aria-expanded", "false");
    });

    test("mark-read is one row at a time: PATCH /notifications/{id}/read, no bulk call", async ({
      loggedInPage: page,
    }, testInfo) => {
      const writes = await stubNotifications(page, ROWS);
      await gotoHome(page);
      const panel = await openPanel(page);

      const rowItem = panel.locator("li", { hasText: 'updated bill "Internet"' });
      await rowItem.getByRole("button", { name: /^Mark read/ }).click();

      await expect.poll(() => writes()).toEqual([expect.stringMatching(/\/bff\/notifications\/n1\/read$/)]);
      // No request names a bulk path or carries a body asking for many rows.
      expect(writes().some((u) => /read-all|mark-all|all/i.test(u))).toBe(false);

      // The row moves to "Earlier" and the count drops by exactly one.
      await expect(panel.getByRole("heading", { name: "Earlier" })).toBeVisible();
      await expect(panel.getByText("2 unread")).toBeVisible();
      await expect(bell(page)).toHaveAccessibleName("Notifications, 2 unread");

      // The other rows are untouched: no mark-read button was pressed for them.
      expect(writes()).toHaveLength(1);
      await expect(
        panel.locator("li", { hasText: "Duke Energy due soon" }).getByRole("button", { name: /^Mark read/ }),
      ).toBeVisible();
      await snap(page, testInfo, `notifications-sheet-${width}-one-read`);
    });

    test("an empty state says what will appear, not a blank panel", async ({ loggedInPage: page }, testInfo) => {
      await stubNotifications(page, []);
      await gotoHome(page);
      const panel = await openPanel(page);

      const empty = panel.getByTestId("notifications-empty");
      await expect(empty).toBeVisible();
      await expect(empty.getByText("Nothing needs you right now")).toBeVisible();
      await expect(empty.getByText(/This is where the family's notices land/)).toBeVisible();
      for (const what of [/a bill is due soon/i, /home upkeep comes due/i, /calendar reminder fires/i]) {
        await expect(empty.getByText(what)).toBeVisible();
      }
      // No badge and no count while there is nothing unread.
      await expect(panel.getByText("All caught up")).toBeVisible();
      await expect(page.locator("header .mantine-Indicator-indicator")).toHaveCount(0);
      await snap(page, testInfo, `notifications-sheet-${width}-empty`);
    });

    test("a failed load is an error with Try again, never a blank list", async ({ loggedInPage: page }) => {
      await stubNotifications(page, [], { failList: true });
      await gotoHome(page);
      const panel = await openPanel(page);

      const err = panel.getByTestId("notifications-error");
      await expect(err).toBeVisible();
      await expect(err.getByText("Notifications didn't load")).toBeVisible();
      await expect(err.getByRole("button", { name: "Try again" })).toBeVisible();
      await expect(panel.getByTestId("notifications-empty")).toHaveCount(0);
      await expectNoSeriousAxeViolations(page);
    });

    test("a failed mark-read stays on that row and offers nothing destructive", async ({
      loggedInPage: page,
    }) => {
      await stubNotifications(page, ROWS, { failMark: true });
      await gotoHome(page);
      const panel = await openPanel(page);
      const rowItem = panel.locator("li", { hasText: "Duke Energy due soon" });
      await rowItem.getByRole("button", { name: /^Mark read/ }).click();
      await expect(rowItem.getByText(/Couldn't mark that read/)).toBeVisible();
      // Still unread: the row keeps its button, and the count is unchanged.
      await expect(bell(page)).toHaveAccessibleName("Notifications, 3 unread");
    });

    test("the More sheet's Notifications row opens the same panel and shows the same count", async ({
      loggedInPage: page,
    }) => {
      await stubNotifications(page, ROWS);
      await gotoHome(page);
      const nav = page.getByRole("navigation", { name: "Main navigation" }).last();
      await nav.getByRole("button", { name: "More" }).click();
      const more = page.getByRole("dialog", { name: "More" });
      await expect(more).toBeVisible();
      const row = more.getByRole("button", { name: /^Notifications/ });
      await expect(row).toContainText("3");
      await row.click();
      const panel = page.getByRole("dialog", { name: "Notifications" });
      await expect(panel).toBeVisible();
      await expect(panel.getByText("3 unread")).toBeVisible();
    });
  });
}

// ───────────────────────── desktop ─────────────────────────

test.describe("notifications popover 1440px", () => {
  test.use({ viewport: { width: 1440, height: 900 } });
  test.beforeEach(() => onlyOn("desktop"));

  test("from 768 px it is a 380 px popover, not a sheet", async ({ loggedInPage: page }) => {
    await stubNotifications(page, ROWS);
    await gotoHome(page);
    await expect(bell(page)).toHaveAttribute("aria-haspopup", "dialog");
    await expect(bell(page)).toHaveAttribute("aria-expanded", "false");

    const panel = await openPanel(page);
    await expect(bell(page)).toHaveAttribute("aria-expanded", "true");
    await expect(page.getByTestId("notifications-popover")).toBeVisible();
    await expect(page.getByTestId("notifications-sheet")).toHaveCount(0);
    expect(Math.round((await panel.boundingBox())!.width)).toBe(380);
    await expect(panel.getByText("Amber via moltis")).toBeVisible();
    await expectNoSeriousAxeViolations(page);

    // Only the button owns the popup ARIA (#4752).
    const header = page.locator("header");
    await expect(header.locator("[aria-haspopup]:not(button)")).toHaveCount(0);
    await expect(header.locator("[aria-expanded]:not(button)")).toHaveCount(0);
  });
});
