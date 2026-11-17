import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow } from "./fixtures";

/**
 * Family activity on Today (#4577) — the attribution panel.
 *
 * Why this spec exists, in the war game's own terms (discoveries/
 * war-game-r1.md): the feed is what answers "what has the other adult done?"
 * and "did anything get deleted?". In R1 an agent answered "Nothing was deleted
 * that I can see" about a deletion it had performed ITSELF, because nothing
 * could answer the question. So the assertions below are about the three things
 * a row has to carry — WHO (person), HOW (which agent), WHAT — plus what happens
 * when the record a row is about is gone, and the role gate that decides whether
 * the panel is drawn at all.
 *
 * iPhone only, on purpose: this is a phone-first surface, iphone-390 is the one
 * WebKit project with isMobile/hasTouch (Playwright's iOS engine), and running
 * the same assertions at five viewports buys nothing. `onlyIPhone` skips the
 * rest, including desktop-chrome.
 */

const TODAY = "2026-11-17";
const NOW_ISO = "2026-11-17T12:42:00Z";

const HOUSEHOLDS = {
  data: [
    { id: "h1", name: "Sims", timezone: "America/New_York", role: "owner", joined_at: "2026-01-01T00:00:00Z", is_default: true, current: true },
  ],
};

const PEOPLE = [
  { id: "p1", household_id: "h1", display_name: "Jacob Sims", relationship: "parent", allergies: [], color: "cyan", user_id: "u1" },
  { id: "p2", household_id: "h1", display_name: "Amber Sims", relationship: "parent", allergies: [], color: "violet", user_id: "u2" },
];

const ME = { id: "u1", email: "jacob@sims.family", name: "Jacob Sims" };

/**
 * The feed exactly as GET /activity answers it (#4505 + #4941): `description`
 * already names the actor and appends `via agent "<name>"` for an agent write;
 * `actor`/`via_agent`/`agent` are structured on the side.
 */
const ACTIVITY = [
  {
    id: 10, action: "create", entity_type: "bill", entity_id: "b1", parent_type: null, parent_id: null,
    changed_fields: [], summary: 'created bill "Copay"',
    description: 'Amber created bill "Copay" via agent "moltis-wg-amber"',
    created_at: "2026-11-17T12:30:00Z",
    actor: { user_id: "u2", person_id: "p2", name: "Amber Sims" },
    via_agent: true, agent: { token_id: "t1", name: "moltis-wg-amber" },
  },
  {
    id: 9, action: "update", entity_type: "pet", entity_id: "pet1", parent_type: null, parent_id: null,
    changed_fields: ["notes"], summary: 'updated pet "Biscuit" (notes)',
    description: 'Jacob updated pet "Biscuit" (notes)',
    created_at: "2026-11-17T11:15:00Z",
    actor: { user_id: "u1", person_id: "p1", name: "Jacob Sims" },
    via_agent: false, agent: null,
  },
];

async function freezeToday(page: Page, iso = NOW_ISO) {
  await page.clock.setFixedTime(new Date(iso));
}

/** Stubs every endpoint Today reads; `overrides` replaces any one of them. */
async function stubToday(
  page: Page,
  overrides: {
    activity?: unknown[];
    householdRole?: string;
    /** Record ids whose detail GET answers 404 ("deleted since"). */
    gone?: string[];
  } = {},
) {
  await page.route("**/bff/me/households", (route) =>
    route.fulfill({ json: { data: [{ ...HOUSEHOLDS.data[0], role: overrides.householdRole ?? "owner" }] } }),
  );
  await page.route("**/bff/auth/me", (route) => route.fulfill({ json: ME }));
  await page.route("**/bff/calendars/events**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/bills**", (route) => {
    const id = new URL(route.request().url()).pathname.split("/").pop() ?? "";
    if (overrides.gone?.includes(id)) return route.fulfill({ status: 404, json: { error: { code: "NOT_FOUND", message: "bill not found" } } });
    return route.fulfill({ json: { data: [] } });
  });
  await page.route("**/bff/pets**", (route) => {
    const id = new URL(route.request().url()).pathname.split("/").pop() ?? "";
    if (overrides.gone?.includes(id)) return route.fulfill({ status: 404, json: { error: { code: "NOT_FOUND", message: "pet not found" } } });
    return route.fulfill({ json: { data: { id: "pet1", display_name: "Biscuit" } } });
  });
  await page.route("**/bff/maintenance/tasks**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/meal-plans**", (route) =>
    route.fulfill({ json: { data: [], from: TODAY, to: TODAY } }),
  );
  await page.route("**/bff/recipes/**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/people**", (route) => route.fulfill({ json: { data: PEOPLE } }));
  await page.route("**/bff/lists**", (route) => {
    const path = new URL(route.request().url()).pathname;
    return route.fulfill({ json: { data: path.match(/\/lists\/[^/]+\/items$/) ? [] : [] } });
  });
  await page.route("**/bff/activity**", (route) =>
    route.fulfill({ json: { data: overrides.activity ?? ACTIVITY, next_cursor: null, poll_after: null } }),
  );
}

async function gotoToday(page: Page) {
  await page.goto("/dashboard");
  await expect(page.getByTestId("today-agenda")).toBeVisible();
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = true) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

/** Skips unless this is the iPhone project: the one phone-width WebKit run. */
function onlyIPhone(): void {
  test.skip(test.info().project.name !== "iphone-390", "iPhone-only spec (#4577): iphone-390 runs it");
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`Family activity on Today — ${colorScheme}`, () => {
    test.use({ colorScheme });

  // ── 1. attribution: WHO, HOW, WHAT ────────────────────────
  test("phone 390: every change names the person, the agent it came through, and what changed", async ({ loggedInPage: page }, testInfo) => {
    onlyIPhone();
    await freezeToday(page);
    await stubToday(page);
    await gotoToday(page);

    const feed = page.getByTestId("today-activity");
    await expect(feed).toBeVisible();
    await expect(feed).toContainText("Family activity");

    // The agent write: WHO (Amber), WHAT (created bill "Copay"), HOW
    // (via moltis-wg-amber). The agent is a chip of its own, so the row does not
    // say it twice; the API's own "via agent …" tail is not printed twice.
    const first = feed.getByTestId("activity-row").first();
    await expect(first).toContainText("Amber");
    await expect(first).toContainText('created bill "Copay"');
    const chip = first.getByTestId("activity-agent");
    await expect(chip).toContainText("via moltis-wg-amber");
    await expect(first).not.toContainText('via agent "moltis-wg-amber"');

    // A write with no agent says nothing about one: an agent is never invented.
    const second = feed.getByTestId("activity-row").nth(1);
    await expect(second).toContainText("You"); // the viewer's own write
    await expect(second).toContainText('updated pet "Biscuit"');
    await expect(second.getByTestId("activity-agent")).toHaveCount(0);

    // Tappable through to the record: the whole row is the link, so the tap
    // target is the row and not a 14 px phrase on it.
    const link = first.getByTestId("activity-link");
    await expect(link).toHaveAttribute("href", "/dashboard/bills/b1");
    await expect(first).toHaveAttribute("data-target", "link");

    // A "see all" door, because four rows is not the history.
    await expect(feed.getByTestId("activity-see-all")).toHaveAttribute("href", "/dashboard/activity");

    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "today-activity-attribution-iphone");
  });

  // ── 2. a dead reference says so ───────────────────────────
  test("phone 390: a deleted change and a record deleted since both say so — never a dead link", async ({ loggedInPage: page }, testInfo) => {
    onlyIPhone();
    await freezeToday(page);
    await stubToday(page, {
      // b-gone answers 404: the bill existed when this change was logged and has
      // been deleted since. The deletion row is a deletion of its own record.
      gone: ["b-gone"],
      activity: [
        {
          id: 30, action: "delete", entity_type: "bill", entity_id: "b-del", parent_type: null, parent_id: null,
          changed_fields: [], summary: 'deleted bill "Netflix"',
          description: 'Amber deleted bill "Netflix" via agent "moltis-wg-amber"',
          created_at: "2026-11-17T12:35:00Z",
          actor: { user_id: "u2", person_id: "p2", name: "Amber Sims" },
          via_agent: true, agent: { token_id: "t1", name: "moltis-wg-amber" },
        },
        {
          id: 29, action: "update", entity_type: "bill", entity_id: "b-gone", parent_type: null, parent_id: null,
          changed_fields: ["amount"], summary: 'updated bill "Copay" (amount)',
          description: 'Jacob updated bill "Copay" (amount)',
          created_at: "2026-11-17T11:55:00Z",
          actor: { user_id: "u1", person_id: "p1", name: "Jacob Sims" },
          via_agent: false, agent: null,
        },
      ],
    });
    await gotoToday(page);

    const feed = page.getByTestId("today-activity");

    // The change WAS the deletion: there is nothing to open, and the row says
    // why instead of offering a link that lands on an empty page.
    const deleted = feed.getByTestId("activity-row").first();
    await expect(deleted).toHaveAttribute("data-target", "deleted");
    await expect(deleted.getByTestId("activity-state-deleted")).toContainText("Deleted — nothing left to open.");
    await expect(deleted.getByTestId("activity-link")).toHaveCount(0);

    // The record was deleted AFTER the change: the probe's 404 is the answer.
    const gone = feed.getByTestId("activity-row").nth(1);
    await expect(gone).toHaveAttribute("data-target", "gone");
    await expect(gone.getByTestId("activity-state-gone")).toContainText("No longer there — deleted since.");
    await expect(gone.getByTestId("activity-link")).toHaveCount(0);

    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "today-activity-deleted-iphone");
  });

  // ── 3. the role gate ──────────────────────────────────────
  for (const role of ["housesitter", "vendor"]) {
    test(`phone 390: a ${role} does not get the activity section, and it is never requested`, async ({ loggedInPage: page }, testInfo) => {
      onlyIPhone();
      await freezeToday(page);
      await stubToday(page, { householdRole: role });

      // Count the calls: not drawing the panel is only half the policy — a
      // section whose request 403s is still a dead read, so the query must not
      // run either.
      let activityCalls = 0;
      page.on("request", (req) => {
        if (new URL(req.url()).pathname.startsWith("/bff/activity")) activityCalls += 1;
      });

      await gotoToday(page);

      // The page itself rendered (a sitter's Today still works) and the panel
      // is absent, heading and all — not an empty card, not an error card.
      await expect(page.getByTestId("today-agenda")).toBeVisible();
      await expect(page.getByTestId("today-activity")).toHaveCount(0);
      await expect(page.getByText("Family activity")).toHaveCount(0);
      await expect(page.getByTestId("activity-empty")).toHaveCount(0);
      expect(activityCalls, `GET /bff/activity calls for a ${role}`).toBe(0);

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `today-activity-${role}-iphone`);
    });
  }

  test("phone 390: a viewer DOES get the activity section (read-only on everything, feed included)", async ({ loggedInPage: page }, testInfo) => {
    onlyIPhone();
    await freezeToday(page);
    await stubToday(page, { householdRole: "viewer" });
    await gotoToday(page);

    // ResActivity read is rolesReaders: a viewer may read the feed, so the
    // section is drawn for them. The policy grants no write, and this panel
    // offers none — every row is a read.
    const feed = page.getByTestId("today-activity");
    await expect(feed).toBeVisible();
    await expect(feed.getByTestId("activity-row").first()).toContainText("Amber");
    await expect(feed.getByTestId("activity-agent").first()).toContainText("moltis-wg-amber");

    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "today-activity-viewer-iphone");
  });

  // ── 4. the window, and where "see all" goes ───────────────
  test("phone 390: Today shows a few changes and says how many it did not, and See all shows the full history", async ({ loggedInPage: page }, testInfo) => {
    onlyIPhone();
    await freezeToday(page);
    const many = Array.from({ length: 8 }, (_, i) => ({
      id: 100 + i, action: "create", entity_type: "list_item", entity_id: `i${i}`,
      parent_type: "list", parent_id: "l1", changed_fields: [], summary: `created list item "thing ${i}"`,
      description: `Amber created list item "thing ${i}"`,
      created_at: "2026-11-17T12:00:00Z",
      actor: { user_id: "u2", person_id: "p2", name: "Amber Sims" },
      via_agent: false, agent: null,
    }));
    await stubToday(page, { activity: many });
    await gotoToday(page);

    const feed = page.getByTestId("today-activity");
    // Four rows, not the whole feed: the R1 lesson is that bloated default
    // payloads slow a screen and give a reader more to misread.
    await expect(feed.getByTestId("activity-row")).toHaveCount(4);
    // The door to the rest is one tap away, and it makes no count claim: the
    // page fetched 8 of however many the family has, so a number here would be
    // a claim about rows this window never saw.
    await expect(feed.getByTestId("activity-more")).toContainText("See all activity");
    await expect(feed.getByTestId("activity-more")).not.toContainText(/\d+ more/);

    await feed.getByTestId("activity-see-all").click();
    await expect(page).toHaveURL(/\/dashboard\/activity$/);

    // The full history: all eight rows, newest first, same attribution.
    await expect(page.getByTestId("activity-row")).toHaveCount(8);
    await expect(page.getByTestId("activity-row").first()).toContainText('created list item "thing 0"');
    await expect(page.getByText("Family activity").first()).toBeVisible();

    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "today-activity-see-all-iphone");
  });
});
}
