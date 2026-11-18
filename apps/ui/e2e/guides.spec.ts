import type { Page, Route, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * Guides (#4570): the list with search, and the step-by-step reading view.
 *
 * The ticket's acceptance criterion is "follow a guide step by step on a
 * phone", so this spec is written from the phone outwards:
 *
 * 1. the list renders the household's guides, a search narrows it without a
 *    round-trip, and a guide opens to its reading view;
 * 2. the reading view is STEPS — a heading per step, numbered — so someone in
 *    front of the boiler scrolls to the step they are on, not through prose;
 * 3. a step can be marked done, the header counts it, and the tick survives a
 *    reload (it is the reader's place in the job);
 * 4. a code block (a Wi-Fi password, a serial) scrolls INSIDE itself: the page
 *    itself never scrolls sideways at 390 px, long words and all;
 * 5. a guide with no steps says so, and a failed load says so with a retry
 *    rather than an empty page.
 *
 * The API is stubbed per test with page.route, so each guide body is exact and
 * each test stands alone. The pages, the markdown parser and the step
 * rendering are the real code.
 */

const IPHONE = "iphone-390";

/**
 * The reading view gets the full step treatment on the phone project, and a
 * desktop pass proves the measure does not go full-bleed on a wide monitor.
 * Both widths are `onlyOn`-guarded inside the tests that need them.
 */
const ROUTER_BODY = `## Turn it off and on

Pull the plug out of the wall socket and wait **30 seconds**. This clears the
fault the lights are showing.

## Check the lights

- The \`Internet\` light should be solid green.
- If \`DSL\` blinks, the line is the problem, not the router.

## Reconnect the Wi-Fi

The network is \`SimsHouse\` and the password is on a sticker under the router:

\`\`\`
password: correct-horse-battery-staple-2026
\`\`\`

If it still does not come back, call the ISP and read them the serial:

\`\`\`
SN 4f9a2c1e77b40d3a9f6e1c55a0b7d2e8f3c4a6b9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9
\`\`\`
`;

const NUMBERED_BODY = `1. Shut the main water valve under the kitchen sink.
2. Open the two taps at the lowest sink so the pipes drain.
3. Attach the hose to the boiler's drain valve and run it to the floor drain.
`;

const GUIDES = [
  {
    id: "g1",
    household_id: "h1",
    title: "Reset the router",
    summary: "When the Wi-Fi drops and the lights are red.",
    body: ROUTER_BODY,
    tags: ["wifi", "router"],
    property_id: null,
    asset_id: null,
    vehicle_id: null,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-11-02T00:00:00Z",
  },
  {
    id: "g2",
    household_id: "h1",
    title: "Drain the water heater",
    summary: null,
    body: NUMBERED_BODY,
    tags: ["plumbing"],
    property_id: null,
    asset_id: null,
    vehicle_id: null,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-20T00:00:00Z",
  },
  {
    id: "g3",
    household_id: "h1",
    title: "Open the cabin in winter",
    summary: "The water is off from November to April.",
    body: "",
    tags: [],
    property_id: null,
    asset_id: null,
    vehicle_id: null,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-05T00:00:00Z",
  },
];

type Writes = { method: string; url: string }[];

/** The stub's knobs, mutable so one test can flip a failure without re-routing. */
type Stub = { writes: Writes; fail: boolean };

/** Answers the BFF the way the real one does, from `rows`. */
async function stubGuides(page: Page, rows = GUIDES): Promise<Stub> {
  const writes: Writes = [];
  const stub: Stub = { writes, fail: false };
  await page.route("**/bff/me/households", (route: Route) =>
    route.fulfill({
      json: {
        data: [
          {
            id: "h1",
            name: "Sims",
            timezone: "UTC",
            role: "owner",
            joined_at: "2026-01-01T00:00:00Z",
            is_default: true,
            current: true,
          },
        ],
      },
    }),
  );

  // One handler for both the list and one guide: the real API's paths, and the
  // same envelope (`{ data }`) the pages read.
  await page.route(/\/bff\/guides(\?.*)?$|\/bff\/guides\/[^/?]+$/, async (route: Route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    if (req.method() !== "GET") {
      writes.push({ method: req.method(), url: path });
      return route.fulfill({ status: 405, json: { error: "read-only spec" } });
    }
    if (stub.fail) return route.fulfill({ status: 500, json: { error: "boom" } });
    const id = path.replace(/^.*\/guides\/?/, "");
    if (id) {
      const one = rows.find((g) => g.id === id);
      if (!one) return route.fulfill({ status: 404, json: { error: "not found" } });
      return route.fulfill({ json: { data: one } });
    }
    return route.fulfill({ json: { data: rows } });
  });
  return stub;
}

async function gotoGuides(page: Page) {
  await page.goto("/dashboard/guides");
  await expect(page.getByRole("heading", { level: 2, name: /Guides/ })).toBeVisible();
}

async function gotoGuide(page: Page, id = "g1") {
  await page.goto(`/dashboard/guides/${id}`);
  await expect(page.locator("main").getByRole("heading", { level: 2 }).first()).toBeVisible();
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = false) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme}`, () => {
    test.use({ colorScheme });

    test("phone 390: the list shows the guides with their step counts, and search narrows it", async ({
      loggedInPage: page,
    }, testInfo) => {
      onlyOn("phone");
      const stub = await stubGuides(page);
      await gotoGuides(page);

      // Every guide is a card, and each carries what tells a reader how big the
      // job is before they open it.
      await expect(page.locator("[data-guide-card]")).toHaveCount(3);
      await expect(page.locator("[data-guide-card='g1']")).toContainText("Reset the router");
      await expect(page.locator("[data-guide-card='g1']")).toContainText("3 steps");
      // A bare numbered list is one step per item: "follow it step by step".
      await expect(page.locator("[data-guide-card='g2']")).toContainText("3 steps");
      // A guide with no body says so instead of claiming a step count it lacks.
      await expect(page.locator("[data-guide-card='g3']")).toContainText("No steps yet");

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `${colorScheme}-guides-list`);

      // Search narrows on the client: matching the title, and the body too.
      const search = page.getByRole("searchbox", { name: "Search guides" });
      await search.fill("wifi");
      // "Reset the router" mentions wifi in its body; "cabin" does not.
      await expect(page.locator("[data-guide-card]")).toHaveCount(1);
      await expect(page.locator("[data-guide-card='g1']")).toBeVisible();
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `${colorScheme}-guides-search`);

      // Nothing matched says so, and clearing brings the list back.
      await search.fill("zzzz");
      await expect(page.getByText(/No guide matches/)).toBeVisible();
      await page.getByRole("button", { name: "Clear search" }).click();
      await expect(page.locator("[data-guide-card]")).toHaveCount(3);

      // The list is a read path: nothing here writes.
      expect(stub.writes, "the list and its search never write").toHaveLength(0);
    });

    test("phone 390: a guide reads step by step, ticks stay, and code scrolls inside itself", async ({
      loggedInPage: page,
    }, testInfo) => {
      onlyOn("phone");
      await stubGuides(page);
      await gotoGuide(page, "g1");

      // A heading per step: the reader scrolls to the step they are on.
      const steps = page.locator("[data-guide-step]");
      await expect(steps).toHaveCount(3);
      await expect(steps.nth(0)).toContainText("Turn it off and on");
      await expect(steps.nth(1)).toContainText("Check the lights");
      await expect(steps.nth(2)).toContainText("Reconnect the Wi-Fi");
      await expect(page.getByText("3 steps")).toBeVisible();
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `${colorScheme}-guide-read`);

      // Marking a step done counts it in the header, and the tick survives a
      // reload: it is the reader's place in the job, not a transient state.
      await steps.nth(0).getByRole("button", { name: "Mark done" }).click();
      await expect(steps.nth(0)).toHaveAttribute("data-done", "true");
      await expect(page.locator("[data-guide-progress]")).toHaveText("1 of 3 done");

      await page.reload();
      await expect(page.locator("[data-guide-step]").nth(0)).toHaveAttribute("data-done", "true");
      await expect(page.locator("[data-guide-progress]")).toHaveText("1 of 3 done");
      await snap(page, testInfo, `${colorScheme}-guide-progress`);

      // Finishing every step says so, once.
      await page.locator("[data-guide-step]").nth(1).getByRole("button", { name: "Mark done" }).click();
      await page.locator("[data-guide-step]").nth(2).getByRole("button", { name: "Mark done" }).click();
      await expect(page.locator("[data-guide-progress]")).toHaveText("3 of 3 done");
      await expect(page.locator("[data-guide-complete]")).toBeVisible();

      // The long serial and the Wi-Fi password are the two things that would
      // widen a 390 px page if they wrapped or bled out of their block. They
      // scroll inside their own <pre>, and the page does not move.
      const codeBlocks = page.locator("[data-guide-code]");
      await expect(codeBlocks).toHaveCount(2);
      const blocks = await codeBlocks.evaluateAll((els) =>
        els.map((el) => ({
          scrolls: el.scrollWidth > el.clientWidth,
          overflowX: getComputedStyle(el).overflowX,
        })),
      );
      expect(blocks.filter((b) => b.scrolls).length, "the long serial scrolls inside its own block").toBeGreaterThan(0);
      for (const b of blocks) expect(b.overflowX, "a code block clips its own overflow").toMatch(/auto|scroll/);
      await expectNoHorizontalOverflow(page);
    });

    test("phone 390: a guide with no steps says so, it does not claim zero of zero", async ({
      loggedInPage: page,
    }, testInfo) => {
      onlyOn("phone");
      await stubGuides(page);
      await gotoGuide(page, "g3");

      // "No steps yet" in the header AND a sentence in the body: two different
      // places, both honest, neither pretending the guide is empty of content.
      await expect(page.locator("[data-guide-progress]")).toHaveText("No steps yet");
      await expect(page.getByText(/This guide has no steps yet/)).toBeVisible();
      await expect(page.locator("[data-guide-step]")).toHaveCount(0);
      await expect(page.locator("[data-guide-complete]")).toHaveCount(0);
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `${colorScheme}-guide-empty`);
    });

    test("phone 390: a failed load offers a retry, and it is not an empty guide", async ({
      loggedInPage: page,
    }, testInfo) => {
      onlyOn("phone");
      // A 500 is NOT "this guide has no steps": the page must say it could not
      // open it, and give a retry that actually re-fetches.
      const stub = await stubGuides(page);
      stub.fail = true;
      await gotoGuide(page, "g1");

      const alert = page.locator("main").getByRole("alert");
      await expect(alert).toContainText(/Couldn't open this guide/);
      await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
      await expect(page.locator("[data-guide-step]")).toHaveCount(0);
      await snap(page, testInfo, `${colorScheme}-guide-error`);

      // The retry is real: the same page, now working, shows the steps.
      stub.fail = false;
      await page.getByRole("button", { name: "Try again" }).click();
      await expect(page.locator("[data-guide-step]")).toHaveCount(3);
      await expect(alert).toHaveCount(0);
      expect(stub.writes, "opening a guide never writes").toHaveLength(0);
    });

    test("desktop 1440: the reading view keeps a readable measure, not a 200-character line", async ({
      loggedInPage: page,
    }, testInfo) => {
      onlyOn("desktop");
      await stubGuides(page);
      await gotoGuide(page, "g1");

      const paragraphWidth = await page
        .locator("[data-guide-step] p")
        .first()
        .evaluate((el) => el.getBoundingClientRect().width);
      // 68ch at 16 px is ~700 px; the cap is what stops prose spanning a wide
      // monitor. Allow the cap plus the font's own slack.
      expect(paragraphWidth, "paragraph does not stretch across the page").toBeLessThan(820);
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `${colorScheme}-guide-desktop`, true);
    });
  });
}
