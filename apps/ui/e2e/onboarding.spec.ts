import { test, expect, onlyOn, expectNoHorizontalOverflow } from "./fixtures";
import type { Page } from "@playwright/test";

/**
 * Onboarding wizard (#4572) — a brand-new family, start to Today, on a phone.
 *
 * The screen is built to the approved mockup (artifact #103 v1, its
 * "ONBOARDING" block): welcome -> family -> people -> property -> done, with a
 * progress bar and a bottom action bar.
 *
 * The contract this spec pins:
 *  1. THE DRAFT IS CLIENT-SIDE. Walking all the way to the last step writes
 *     NOTHING (the stub records every write in apiWrites); only the final
 *     action submits. That is what keeps an abandoned wizard from leaving a
 *     half-built family behind.
 *  2. THE OPTIONAL STEPS ARE SKIPPABLE. Skipping people and the home still
 *     creates the family and lands on Today.
 *  3. THE SUBMIT IS ORDERED AFTER THE FAMILY. The household is created first,
 *     and its id becomes the active family before any household-scoped write,
 *     so the people/property/invite go into the family that was just made.
 *  4. It fits a phone: no sideways scroll, and the primary action is >= 44 px.
 *
 * /welcome is a PROTECTED path (middleware.ts): the person is signed in and
 * simply has no family yet, so the guard must let them through on their
 * session cookie rather than bounce them to /login. The spec reaches it with a
 * signed-in stub session and nothing seeded — which is exactly the no-family
 * state the stub answers by default (an empty household list).
 */

const PHONE = { width: 390, height: 844 };

/**
 * Records the writes THIS page makes, in order.
 *
 * The stub is one server shared by every spec in the run (and specs run in
 * parallel), so its apiWrites log cannot say which write was whose. Every write
 * the UI makes goes through the BFF as a POST, so watching this page's own
 * requests is both exact and per-test — and it is the ordering the wizard
 * promises, recorded as it happened.
 */
function recordWrites(page: Page): Array<{ path: string; body: string }> {
  const writes: Array<{ path: string; body: string }> = [];
  page.on("request", (req) => {
    if (req.method() !== "POST") return;
    const path = new URL(req.url()).pathname;
    // The session teardown / logout posts are not the wizard's writes.
    if (!path.startsWith("/bff/")) return;
    writes.push({ path, body: req.postData() ?? "" });
  });
  return writes;
}

test.beforeEach(async ({ page }) => {
  await page.setViewportSize(PHONE);
});

test("a new family is created from the wizard and the person lands on Today", async ({
  loggedInPage: page,
}) => {
  onlyOn("phone");
  const writes = recordWrites(page);
  await page.goto("/welcome");

  // ── Welcome ────────────────────────────────────────────────────
  await expect(page.getByRole("heading", { level: 1, name: /Welcome to Home OS/i })).toBeVisible();
  await expect(page.getByRole("button", { name: /Start a family hub/i }).first()).toBeVisible();
  await page.getByRole("button", { name: /Start a family hub/i }).first().click();

  // ── 1. Family ──────────────────────────────────────────────────
  await expect(page.getByRole("heading", { level: 1, name: /Name your family hub/i })).toBeVisible();
  await page.getByLabel("Family name").fill("The Testers");
  // The zone list can be long; the device's own zone is preselected, so the
  // spec only asserts that a real zone is chosen rather than picking one.
  const zone = page.getByLabel("Time zone");
  await expect(zone).toBeVisible();
  expect((await zone.inputValue()).trim().length).toBeGreaterThan(0);
  await page.getByRole("button", { name: "Continue" }).click();

  // ── 2. People (add one, then continue) ─────────────────────────
  await expect(page.getByRole("heading", { level: 1, name: /Who's in the family/i })).toBeVisible();
  await page.getByLabel("Name", { exact: true }).fill("Nora Testers");
  await page.getByLabel("Relationship").fill("Daughter");
  await page.getByRole("button", { name: "Add person" }).click();
  await expect(page.getByRole("list", { name: /People added so far/i })).toContainText("Nora Testers");
  await page.getByRole("button", { name: "Continue" }).click();

  // ── 3. Property ────────────────────────────────────────────────
  await expect(page.getByRole("heading", { level: 1, name: /Add your home/i })).toBeVisible();
  await page.getByLabel("Name", { exact: true }).fill("Maple Street");
  await page.getByLabel(/Address/).fill("1824 Maple St, Springfield");
  await page.getByRole("radio", { name: "House" }).click();
  await page.getByRole("button", { name: "Continue" }).click();

  // ── 4. Review: still nothing written ───────────────────────────
  await expect(page.getByRole("heading", { level: 1, name: /Ready to start The Testers/i })).toBeVisible();
  await expect(page.getByText("Maple Street")).toBeVisible();
  // The draft holds all three, and not one write has gone out yet.
  expect(writes).toEqual([]);

  // ── The one submit ─────────────────────────────────────────────
  await page.getByRole("button", { name: "Create family hub" }).click();
  await page.waitForURL(/\/dashboard(\/|\?|$)/, { timeout: 30_000 });

  // The family is created first, with what the wizard collected...
  expect(writes[0].path).toBe("/bff/households");
  expect(writes[0].body).toContain("The Testers");
  // ...then the person and the home, and nothing else.
  const person = writes.find((w) => w.path === "/bff/people");
  expect(person?.body).toContain("Nora Testers");
  const home = writes.find((w) => w.path === "/bff/properties");
  expect(home?.body).toContain("Maple Street");
  expect(writes).toHaveLength(3);
});

test("the optional steps can be skipped and the family is still created", async ({
  loggedInPage: page,
}) => {
  onlyOn("phone");
  const writes = recordWrites(page);
  await page.goto("/welcome");
  await page.getByRole("button", { name: /Start a family hub/i }).first().click();

  await page.getByLabel("Family name").fill("Skippers");
  await page.getByRole("button", { name: "Continue" }).click();

  // People: skip. Property: skip. No partner invite either.
  await expect(page.getByRole("heading", { level: 1, name: /Who's in the family/i })).toBeVisible();
  await page.getByRole("button", { name: "Skip" }).click();
  await expect(page.getByRole("heading", { level: 1, name: /Add your home/i })).toBeVisible();
  await page.getByRole("button", { name: "Skip" }).click();

  await expect(page.getByRole("heading", { level: 1, name: /Ready to start Skippers/i })).toBeVisible();
  // The review lists only what the family will really have: the name and the
  // calendars, but no person and no home, because neither was filled in.
  const review = page.getByRole("list").filter({ hasText: "3 calendars" });
  await expect(review).toBeVisible();
  await expect(review).not.toContainText("person");
  await expect(review.locator("li")).toHaveCount(2);

  await page.getByRole("button", { name: "Create family hub" }).click();
  await page.waitForURL(/\/dashboard(\/|\?|$)/, { timeout: 30_000 });

  // Exactly the family: no people, no home, no invite.
  expect(writes).toHaveLength(1);
  expect(writes[0].path).toBe("/bff/households");
  expect(writes[0].body).toContain("Skippers");
});

test("a family with no name is refused at the end, and nothing is written", async ({
  loggedInPage: page,
}) => {
  onlyOn("phone");
  const writes = recordWrites(page);
  await page.goto("/welcome");
  await page.getByRole("button", { name: /Start a family hub/i }).first().click();

  // Walk to the last step leaving the name blank; skip both optional steps.
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("button", { name: "Skip" }).click();
  await page.getByRole("button", { name: "Skip" }).click();
  await page.getByRole("button", { name: "Create family hub" }).click();

  // It sends the person back to the step that has the missing answer, and the
  // reason is a live, readable message rather than a silent no-op.
  await expect(page.getByRole("heading", { level: 1, name: /Name your family hub/i })).toBeVisible();
  // The wizard's own live region, not the router's #__next-route-announcer__
  // (which is also role=alert, so an unqualified getByRole("alert") is ambiguous).
  await expect(page.locator("#ob-err")).toContainText(/Give the family a name/i);
  // A refused submit is not a submit: no family was created.
  expect(writes).toEqual([]);
});

test("every step fits a phone and its actions are finger-sized", async ({ loggedInPage: page }) => {
  onlyOn("phone");
  await page.goto("/welcome");

  const steps: Array<() => Promise<void>> = [
    async () => {},
    async () => {
      await page.getByRole("button", { name: /Start a family hub/i }).first().click();
      await expect(page.getByLabel("Family name")).toBeVisible();
    },
    async () => {
      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.getByRole("heading", { level: 1, name: /Who's in the family/i })).toBeVisible();
    },
    async () => {
      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.getByRole("heading", { level: 1, name: /Add your home/i })).toBeVisible();
    },
    async () => {
      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.getByRole("heading", { level: 1, name: /Ready to start/i })).toBeVisible();
    },
  ];

  for (const advance of steps) {
    await advance();
    await expectNoHorizontalOverflow(page);
    // The primary action is the one control a person must hit; 44 px is the floor.
    const primary = page.getByRole("button", { name: /Continue|Create family hub|Start a family hub/i }).last();
    const box = await primary.boundingBox();
    expect(box, "the primary action is on screen").not.toBeNull();
    expect(box!.height).toBeGreaterThanOrEqual(44);
  }
});
