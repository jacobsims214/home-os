import { test, expect, seedHouseholds, SESSION_COOKIE } from "./fixtures";
import type { Page } from "@playwright/test";

/**
 * Activity feed filtered to one credential (#4575).
 *
 * A credential's page links to /dashboard/activity?entity_type=secret&
 * entity_id=<id>, which is where the agent's reads of that credential land
 * (activity_log action='read'). The API has filtered on those params all
 * along (apps/api/internal/activity/handler.go, parseFilter); these specs are
 * about the PAGE actually forwarding them, which is what makes the link mean
 * what it says instead of quietly showing the whole family feed.
 */

test.skip(!!process.env.E2E_BASE_URL, "needs the stub API");

const ACTIVITY = "/api/v1/activity";
const HOUSEHOLD = "0b9d4f1e-6a3c-4b8e-9f52-1c7d2a5e8b10";

/**
 * The feed is role-gated (#4689) and only fetches once the role is KNOWN, so
 * the session needs a family the way the real one always does. Without this
 * the page sits on its skeleton and never sends the request under test.
 */
async function seedFamily(page: Page, request: Parameters<typeof seedHouseholds>[0]) {
  const token = (await page.context().cookies()).find((c) => c.name === SESSION_COOKIE)?.value ?? "";
  const sid = JSON.parse(Buffer.from(token.split(".")[1], "base64url").toString("utf8")).sid;
  await seedHouseholds(request, sid, [
    { id: HOUSEHOLD, name: "Sims family", role: "owner", timezone: "America/Chicago" },
  ]);
}

test("the filtered view forwards entity_type/entity_id and says what it is showing", async ({
  loggedInPage: page,
  request,
}) => {
  await seedFamily(page, request);
  const seen: string[] = [];
  page.on("request", (r) => {
    const url = new URL(r.url());
    if (url.pathname === "/bff/activity") seen.push(url.search);
  });

  await page.goto("/dashboard/activity?entity_type=secret&entity_id=s-1234");

  // Forwarded, not swallowed: both params reach the BFF (and so the API).
  await expect
    .poll(() => seen.some((q) => q.includes("entity_type=secret") && q.includes("entity_id=s-1234")))
    .toBe(true);
  // And the page tells the person what they are looking at.
  await expect(
    page.getByText(/every time an agent was given this credential/i),
  ).toBeVisible();
});

test("the unfiltered feed sends no entity filter at all", async ({ loggedInPage: page, request }) => {
  await seedFamily(page, request);
  const seen: string[] = [];
  page.on("request", (r) => {
    const url = new URL(r.url());
    if (url.pathname === "/bff/activity") seen.push(url.search);
  });

  await page.goto("/dashboard/activity");
  await expect.poll(() => seen.length).toBeGreaterThan(0);
  // A filter leaking in here would silently hide most of the family's history.
  for (const q of seen) {
    expect(q).not.toContain("entity_type");
    expect(q).not.toContain("entity_id");
  }
  expect(ACTIVITY).toBe("/api/v1/activity");
});
