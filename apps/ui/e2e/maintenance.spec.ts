import type { Page, TestInfo } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * Maintenance / upkeep (#4558), built to the approved people & house mockup
 * (artifact #89 v1, the Upkeep screen).
 *
 * This spec exists for four reasons:
 *
 *  1. A TASK CAN BE COMPLETED ON A PHONE. The old board could only be changed
 *     by dragging a card, and at 390px just one of three columns was visible
 *     (audit #74). The phone list's 44px checkbox PATCHes status=done with
 *     completed_at, and the task then reads as done.
 *  2. THE PHONE LAYOUT IS A GROUPED LIST, the desktop layout is the kanban.
 *     Neither may overflow sideways at 390.
 *  3. THE KANBAN'S HEADERS PASS AXE. Its old bg-orange-500/blue-500/green-500
 *     headers with white text measured 2.80, 3.67 and 2.27:1 (manager note on
 *     #4754). They are now token surfaces with light Mantine variants, so a
 *     colour-contrast run over the page in both schemes finds nothing.
 *  4. A VIEWER SEES EVERY TASK AND CAN CHANGE NONE; a vendor may change a
 *     status but not a date (roles.go ResMaintenance).
 *
 * The API is stubbed per test with page.route, so each payload is exact. Phone
 * widths run on iphone-390 (WebKit + touch), per #4757.
 */

const HOUSEHOLDS = (role: string) => ({
  data: [
    { id: "h1", name: "Sims", timezone: "America/New_York", role, joined_at: "2026-01-01T00:00:00Z", is_default: true, current: true },
  ],
});

const SUBJECTS = {
  properties: [{ id: "p1", name: "Maple Street" }],
  assets: [{ id: "a1", name: "Furnace" }],
  vehicles: [{ id: "v1", year: 2019, make: "Honda", model: "Odyssey EX-L" }],
};

function task(over: Partial<Record<string, unknown>> & { id: string; name: string }) {
  const now = "2026-11-18T12:00:00Z";
  return {
    household_id: "h1",
    schedule_id: null,
    property_id: "p1",
    asset_id: null,
    vehicle_id: null,
    description: null,
    status: "pending",
    due_date: null,
    completed_at: null,
    cost: null,
    vendor_id: null,
    notes: null,
    created_at: now,
    updated_at: now,
    ...over,
  };
}

/**
 * One task in each of the phone list's groups. The clock the UI compares
 * against is the browser's "now", so the dates are built from it: exactly
 * today, five days old, five days out, forty days out, and none at all.
 */
function datedTasks(now: Date) {
  const day = (offset: number) => {
    const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() + offset, 12);
    return d.toISOString();
  };
  return [
    task({ id: "m-overdue", name: "Replace furnace filter", asset_id: "a1", due_date: day(-5) }),
    task({ id: "m-week", name: "Test smoke detectors", due_date: day(3), status: "in_progress" }),
    task({ id: "m-month", name: "Winterize cabin plumbing", property_id: "p1", due_date: day(20) }),
    task({ id: "m-later", name: "Oil change", vehicle_id: "v1", due_date: day(40) }),
    task({ id: "m-nodate", name: "Fix kitchen faucet drip", property_id: "p1" }),
    task({ id: "m-done", name: "Blow out sprinklers", status: "done", due_date: day(-9), completed_at: day(-9) }),
  ];
}

type Write = { method: string; url: string; body: Record<string, unknown> };

/** Answers every read the page makes, as `role`, and records the writes. */
async function stubMaintenance(
  page: Page,
  opts: { role?: string; tasks?: unknown[]; onPatch?: (body: Record<string, unknown>) => void } = {},
): Promise<{ writes: Write[] }> {
  const role = opts.role ?? "owner";
  const writes: Write[] = [];
  await page.route("**/bff/me/households*", (route) => route.fulfill({ json: HOUSEHOLDS(role) }));
  await page.route("**/bff/maintenance/tasks**", async (route) => {
    const req = route.request();
    if (req.method() !== "GET") {
      const body = (req.postDataJSON() ?? {}) as Record<string, unknown>;
      writes.push({ method: req.method(), url: req.url(), body });
      opts.onPatch?.(body);
      const id = new URL(req.url()).pathname.split("/").pop()!;
      const found = (opts.tasks ?? []).find((t) => (t as { id: string }).id === id) ?? {};
      return route.fulfill({ json: { data: { ...(found as object), ...body } } });
    }
    return route.fulfill({ json: { data: opts.tasks ?? [] } });
  });
  await page.route("**/bff/properties**", (route) => route.fulfill({ json: { data: SUBJECTS.properties } }));
  await page.route("**/bff/assets**", (route) => route.fulfill({ json: { data: SUBJECTS.assets } }));
  await page.route("**/bff/vehicles**", (route) => route.fulfill({ json: { data: SUBJECTS.vehicles } }));
  return { writes };
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = false) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

/** Critical/serious axe violations, with the offending nodes named. */
async function seriousAxe(page: Page, runOnly?: string[]) {
  const builder = new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]);
  const { violations } = await builder.analyze();
  return violations
    .filter((v) => (v.impact === "critical" || v.impact === "serious") && (!runOnly || runOnly.includes(v.id)))
    .map((v) => `${v.impact} ${v.id}: ${v.nodes.map((n) => `${n.target.join(" ")} ${n.html.slice(0, 160)}`).join(" | ")}`);
}

// ── phone: the grouped list and completing a task ─────────────

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`maintenance phone ${colorScheme}`, () => {
    test.use({ colorScheme });
    test.beforeEach(() => onlyOn("phone"));

    test("390: the list is grouped by due date, and a checkbox completes a task", async ({ loggedInPage: page }, testInfo) => {
      await page.setViewportSize({ width: 390, height: 844 });
      const now = new Date();
      const list = datedTasks(now);
      const { writes } = await stubMaintenance(page, { tasks: list, onPatch: (body) => {
        const found = list.find((t) => t.id === "m-overdue")!;
        if (body.status === "done") found.status = "done";
      } });

      await page.goto("/dashboard/maintenance");

      // The phone list is the layout, and the kanban is not drawn at all.
      const phoneList = page.getByTestId("upkeep-list");
      await expect(phoneList).toBeVisible();
      await expect(page.getByTestId("upkeep-kanban")).toBeHidden();

      // The groups, in the mockup's order, each with its count.
      await expect(page.getByRole("heading", { name: /^Overdue · 1$/ })).toBeVisible();
      await expect(page.getByRole("heading", { name: /^This week · 1$/ })).toBeVisible();
      await expect(page.getByRole("heading", { name: /^Next 30 days · 1$/ })).toBeVisible();
      await expect(page.getByRole("heading", { name: /^Later · 1$/ })).toBeVisible();
      await expect(page.getByRole("heading", { name: /^No due date · 1$/ })).toBeVisible();
      await expect(page.getByRole("heading", { name: /^Done · 1$/ })).toBeVisible();

      // The due line is a word, not colour alone.
      const overdueRow = page.locator('[data-task-row="m-overdue"]');
      await expect(overdueRow).toContainText("5 days overdue");
      // The subject line names the thing and the house, never a raw uuid.
      await expect(overdueRow).toContainText("Furnace");
      await expect(overdueRow).toContainText("Maple Street");
      await expect(page.locator('[data-task-row="m-later"]')).toContainText("2019 Honda Odyssey EX-L");

      // No money anywhere on the page: maintenance is not a money surface.
      await expect(page.getByTestId("maintenance-page")).not.toContainText(/\$\d/);

      // Complete it: the 44px checkbox PATCHes status=done with completed_at.
      const checkbox = page.getByRole("checkbox", { name: "Mark done: Replace furnace filter" });
      await expect(checkbox).toHaveAttribute("aria-checked", "false");
      const box = await checkbox.boundingBox();
      expect(box?.width ?? 0, "the checkbox is a 44px touch target").toBeGreaterThanOrEqual(43);
      expect(box?.height ?? 0, "the checkbox is a 44px touch target").toBeGreaterThanOrEqual(43);

      await checkbox.click();
      await expect.poll(() => writes.length, { message: "the completion was sent" }).toBeGreaterThan(0);
      const done = writes.find((w) => w.body.status === "done");
      expect(done, "PATCH status=done").toBeTruthy();
      expect(done!.method).toBe("PATCH");
      expect(done!.url).toContain("/maintenance/tasks/m-overdue");
      expect(typeof done!.body.completed_at).toBe("string");

      // It now reads as done in its own row: the status attribute the row
      // carries, the checked box, and a Done group that counted it in. (The
      // "Done <date>" line is not asserted: the stub does not persist
      // completed_at, so it is legitimately absent after the refetch.)
      await expect(page.getByRole("checkbox", { name: "Undo completion: Replace furnace filter" })).toHaveAttribute(
        "aria-checked",
        "true",
      );
      await expect(page.locator('[data-task-row="m-overdue"]')).toHaveAttribute("data-task-status", "done");
      await expect(page.getByRole("heading", { name: /^Done · 2$/ })).toBeVisible();

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `maintenance-390-${colorScheme}`, true);

      // axe: the colour-contrast failure #4754 found lived on this page.
      expect(await seriousAxe(page), "critical/serious axe violations").toEqual([]);
    });

    test("390: a viewer sees every task and can change none", async ({ loggedInPage: page }, testInfo) => {
      await page.setViewportSize({ width: 390, height: 844 });
      const { writes } = await stubMaintenance(page, { role: "viewer", tasks: datedTasks(new Date()) });

      await page.goto("/dashboard/maintenance");
      await expect(page.getByTestId("upkeep-list")).toBeVisible();
      await expect(page.getByTestId("view-only")).toContainText("View only");
      await expect(page.getByRole("checkbox", { name: "Mark done: Replace furnace filter" })).toBeDisabled();
      await expect(page.getByRole("button", { name: "More for Replace furnace filter" })).toHaveCount(0);
      expect(writes).toEqual([]);
      await snap(page, testInfo, "maintenance-viewer-390");
    });

    test("390: a housesitter has no maintenance at all", async ({ loggedInPage: page }, testInfo) => {
      await page.setViewportSize({ width: 390, height: 844 });
      await stubMaintenance(page, { role: "housesitter", tasks: datedTasks(new Date()) });

      await page.goto("/dashboard/maintenance");
      await expect(page.getByTestId("upkeep-no-access")).toContainText("isn&apos;t available to you".replace("&apos;", "'"));
      await expect(page.getByTestId("upkeep-list")).toHaveCount(0);
      expect(await seriousAxe(page), "critical/serious axe violations").toEqual([]);
      await snap(page, testInfo, "maintenance-noaccess-390");
    });
  });
}

// ── desktop: the kanban, its headers, and the drag affordances ─

test.describe("maintenance desktop", () => {
  test.beforeEach(() => onlyOn("desktop"));

  test("1440: the kanban draws three token-coloured columns and passes axe", async ({ loggedInPage: page }, testInfo) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    const { writes } = await stubMaintenance(page, { tasks: datedTasks(new Date()) });
    await page.goto("/dashboard/maintenance");

    const kanban = page.getByTestId("upkeep-kanban");
    await expect(kanban).toBeVisible();
    await expect(page.getByTestId("upkeep-list")).toBeHidden();

    await expect(page.getByRole("heading", { name: "To do", exact: true })).toBeVisible();
    await expect(page.getByRole("heading", { name: "In progress", exact: true })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Done", exact: true })).toBeVisible();

    // Every card carries a button path to the same change drag can make, so
    // drag is never the only way (the audit's complaint).
    const card = page.locator('[data-kanban-card="m-week"]');
    await expect(card).toBeVisible();
    await card.getByRole("button", { name: "Done" }).click();
    await expect.poll(() => writes.length).toBeGreaterThan(0);
    expect(writes[0].body.status).toBe("done");

    // No white-on-saturated header is left: the old contrast failure is a
    // colour-contrast violation, and this runs the whole WCAG A/AA set.
    expect(await seriousAxe(page), "critical/serious axe violations").toEqual([]);

    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "maintenance-1440");
  });

  test("390: a vendor may change a status but gets no Snooze (due_date is family-only)", async ({ loggedInPage: page }) => {
    // A phone width, in this project: the row menu is the phone list's, and a
    // vendor's status edit is what the checkbox does there.
    await page.setViewportSize({ width: 390, height: 844 });
    const { writes } = await stubMaintenance(page, { role: "vendor", tasks: datedTasks(new Date()) });
    await page.goto("/dashboard/maintenance");

    await expect(page.getByTestId("upkeep-list")).toBeVisible();
    // A vendor may update status (roles.go), so the checkbox works...
    await expect(page.getByRole("checkbox", { name: "Mark done: Replace furnace filter" })).toBeEnabled();
    // ...but Snooze writes due_date, which ActWrite grants to the family only.
    await page.getByRole("button", { name: "More for Replace furnace filter" }).click();
    await expect(page.getByRole("menuitem", { name: "Start" })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "Snooze a week" })).toHaveCount(0);
    await page.keyboard.press("Escape");
    expect(writes).toEqual([]);
  });
});

test.describe("maintenance desktop dark", () => {
  test.use({ colorScheme: "dark" });
  test.beforeEach(() => onlyOn("desktop"));

  test("1440: the kanban passes axe in the dark scheme too", async ({ loggedInPage: page }, testInfo) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await stubMaintenance(page, { tasks: datedTasks(new Date()) });
    await page.goto("/dashboard/maintenance");
    await expect(page.getByTestId("upkeep-kanban")).toBeVisible();

    expect(await seriousAxe(page), "critical/serious axe violations in dark").toEqual([]);
    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "maintenance-1440-dark");
  });
});

test.describe("maintenance: a finished task never reads as overdue", () => {
  test.beforeEach(() => onlyOn("phone"));

  test("390: a done task says when it was finished, not how late it is", async ({ loggedInPage: page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 844 });
    const now = new Date();
    await stubMaintenance(page, {
      tasks: [
        // Due nine days ago but finished nine days ago: done, not overdue.
        task({ id: "m-done", name: "Blow out sprinklers", status: "done", due_date: datedTasks(now)[5].due_date, completed_at: "2026-11-09T17:00:00Z" }),
        // Deliberately not done, and past its date: skipped, not overdue either.
        task({ id: "m-skipped", name: "Rake the leaves", status: "skipped", due_date: datedTasks(now)[5].due_date }),
      ],
    });
    await page.goto("/dashboard/maintenance");

    const doneRow = page.locator('[data-task-row="m-done"]');
    await expect(doneRow).toContainText(/Done/);
    await expect(doneRow).not.toContainText("overdue");

    // Skipped gets its own group; nothing claims to be overdue.
    await expect(page.getByRole("heading", { name: /^Skipped · 1$/ })).toBeVisible();
    await expect(page.getByRole("heading", { name: /^Overdue/ })).toHaveCount(0);
    await expect(page.locator('[data-task-row="m-skipped"]')).not.toContainText("overdue");
    // One done, one skipped, nothing open: the page says so without claiming
    // the skipped one was completed.
    await expect(page.getByTestId("maintenance-page")).toContainText("Nothing left to do");
    await snap(page, testInfo, "maintenance-done-not-overdue-390");
  });
});
