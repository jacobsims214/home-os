import type { Page, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow } from "./fixtures";

/**
 * The Members page (#4559) — the family roster and per-member settings.
 *
 * WHY THESE THREE TESTS, and not a screen tour: each one is a rule the ticket
 * exists to enforce, and each is the kind of failure that is invisible in a
 * diff.
 *
 *  1. THE ROSTER RENDERS what a person needs to run a family: name,
 *     relationship, role, the colour dot (people.color, #4821) and allergies
 *     (safety data). At 390 px, with no sideways scroll — the old table's row
 *     actions ran off the right edge (mobile audit #4509 / #11636).
 *  2. A NON-MANAGER SEES NO MANAGEMENT CONTROLS. Only owner and family_manager
 *     invite, change a role or remove a member (roles.go CanManageMembers). A
 *     family_member must not be shown a control that answers 403, and the page
 *     must say who can manage instead of looking broken.
 *  3. COLOUR EDITING IS SELF-OR-MANAGER (#4821). A family_member may recolour
 *     themselves and nobody else; a manager may recolour anyone; a housesitter
 *     gets no colour control at all (they may not edit a people record). The
 *     spec asserts the PATCH body too, so the eight-name palette is what
 *     reaches the API — never a hex.
 *
 * iPhone only, on purpose: this is a phone-first surface and iphone-390 is the
 * one WebKit project with isMobile/hasTouch. The desktop floor is covered by
 * `npm run build` plus the shared shell specs; running these five viewports
 * would triple the compile time for no extra assertion (`onlyIPhone`).
 */

const HOUSEHOLD = {
  id: "0b9d4f1e-6a3c-4b8e-9f52-1c7d2a5e8b10",
  name: "Sims family",
  timezone: "America/Chicago",
  role: "owner",
  joined_at: "2026-01-01T00:00:00Z",
  is_default: true,
  current: true,
};

const ME = { id: "u1", email: "jacob@sims.family", name: "Jacob Sims" };

/** GET /households/me/members — the API's own shape, colour included. */
const MEMBERS = [
  {
    user_id: "u1", email: "jacob@sims.family", name: "Jacob Sims", role: "owner",
    joined_at: "2026-03-02T00:00:00Z", vendor_id: null, vendor_name: null, color: "cyan",
  },
  {
    user_id: "u2", email: "amber@sims.family", name: "Amber Sims", role: "family_manager",
    joined_at: "2026-03-02T00:00:00Z", vendor_id: null, vendor_name: null, color: "violet",
  },
  {
    user_id: "u3", email: "carol.reyes@gmail.com", name: "Carol Reyes", role: "family_member",
    joined_at: "2026-04-18T00:00:00Z", vendor_id: null, vendor_name: null, color: "teal",
  },
  {
    user_id: "u4", email: "maria.lopez@gmail.com", name: "Maria Lopez", role: "housesitter",
    joined_at: "2026-10-28T00:00:00Z", vendor_id: null, vendor_name: null, color: "orange",
  },
];

/** GET /people — the person rows, joined on user_id. */
const PEOPLE = [
  { id: "p1", display_name: "Jacob Sims", relationship: "parent", allergies: [], user_id: "u1", color: "cyan" },
  { id: "p2", display_name: "Amber Sims", relationship: "parent", allergies: [], user_id: "u2", color: "violet" },
  { id: "p3", display_name: "Carol Reyes", relationship: "nanny", allergies: ["peanuts"], user_id: "u3", color: "teal" },
  { id: "p4", display_name: "Maria Lopez", relationship: "housesitter", allergies: [], user_id: "u4", color: "orange" },
];

const stubUrl = `http://127.0.0.1:${process.env.E2E_STUB_PORT || 3901}`;

/** Skips unless this is the iPhone project: the one phone-width WebKit run. */
function onlyIPhone(): void {
  test.skip(test.info().project.name !== "iphone-390", "iPhone-only spec (#4559): iphone-390 runs it");
}

interface Stubs {
  /** The caller's role in GET /me/households. */
  role: string;
  /**
   * The household's zone, for GET /me/households and GET /households/me. Omitted:
   * the seed's America/Chicago. `""` is a real family state (the API stores an
   * empty zone), which is the ONE case the card may call "not set" (#5300).
   */
  householdTimezone?: string;
  members?: unknown[];
  people?: unknown[];
  invites?: unknown[];
  /** Answers a write with this status instead of 200, to prove the message. */
  writeStatus?: number;
  writeMessage?: string;
  /** Fail the roster reads (GET /members and /people) with this status. */
  readStatus?: number;
  /** Never answer, so the loading skeleton is what is on screen. */
  hangRoster?: boolean;
}

/**
 * Stubs every endpoint the Members page reads. `/people` is answered with the
 * shape the ACTOR would get: a housesitter's rows have user_id null and carry
 * only relationship/allergies (people/handler.go `view`, read_basic), which is
 * exactly the contract the page must not exceed.
 */
async function stubMembers(page: Page, stubs: Stubs) {
  const timezone = stubs.householdTimezone ?? HOUSEHOLD.timezone;
  await page.route("**/bff/me/households", (route) =>
    route.fulfill({ json: { data: [{ ...HOUSEHOLD, timezone, role: stubs.role }] } }),
  );
  await page.route("**/bff/auth/me", (route) => route.fulfill({ json: ME }));
  // GET /households/me, in the API's REAL wire shape (#5300): the handler
  // serialises the Go Household struct, which carries no json tags, so the keys
  // are `ID`/`Name`/`Timezone` — capitalised. This stub used to answer the tidy
  // lowercase object the page wished it got, which is how "Time zone not set" on
  // a family with a zone survived every test. Stub the API as it is, not as the
  // page would like it: the page no longer reads this route at all (it reads
  // GET /me/households, the one household source), and a spec that passes here
  // cannot be passing on a payload the product never sends.
  await page.route("**/bff/households/me", (route) =>
    route.fulfill({
      json: {
        data: {
          ID: HOUSEHOLD.id,
          Name: HOUSEHOLD.name,
          Timezone: timezone,
          Settings: null,
          CreatedAt: "2026-01-01T00:00:00Z",
          UpdatedAt: "2026-01-01T00:00:00Z",
        },
      },
    }),
  );
  await page.route("**/bff/households/me/members", (route) => {
    if (stubs.hangRoster) return; // never answers: the page stays in loading
    if (stubs.readStatus) {
      return route.fulfill({
        status: stubs.readStatus,
        json: { error: { code: "ERROR", message: "upstream failed" } },
      });
    }
    return route.fulfill({ json: { data: stubs.members ?? MEMBERS } });
  });
  await page.route("**/bff/people", (route) => {
    if (stubs.readStatus) {
      return route.fulfill({
        status: stubs.readStatus,
        json: { error: { code: "ERROR", message: "upstream failed" } },
      });
    }
    const people = stubs.people ?? PEOPLE;
    const redacted = stubs.role === "housesitter";
    return route.fulfill({
      json: {
        data: people.map((p) => {
          const row = p as Record<string, unknown>;
          if (!redacted) return row;
          // read_basic: birthdate/school/grade/notes/user_id are nulled.
          return {
            id: row.id,
            display_name: row.display_name,
            relationship: row.relationship,
            allergies: row.allergies,
            user_id: null,
            birthdate: null,
            school: null,
            grade: null,
            notes: null,
            color: row.color,
          };
        }),
      },
    });
  });
  await page.route("**/bff/invites", (route) => {
    if (route.request().method() === "GET") return route.fulfill({ json: { data: stubs.invites ?? [] } });
    return route.fulfill({
      status: stubs.writeStatus ?? 201,
      json: stubs.writeStatus
        ? { error: { code: "FORBIDDEN", message: stubs.writeMessage ?? "refused" } }
        : { data: { accept_url: "https://home.sims.family/invites/accept?token=t" } },
    });
  });
  await page.route("**/bff/vendors**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/people/*", (route) => {
    if (route.request().method() !== "PATCH") return route.fulfill({ json: { data: {} } });
    if (stubs.writeStatus) {
      return route.fulfill({
        status: stubs.writeStatus,
        json: { error: { code: "FORBIDDEN", message: stubs.writeMessage ?? "refused" } },
      });
    }
    const body = route.request().postDataJSON() as Record<string, unknown>;
    return route.fulfill({ json: { data: { id: "p3", ...body } } });
  });
  await page.route("**/bff/households/me/members/*", (route) => {
    if (route.request().method() === "GET") return route.fulfill({ json: { data: MEMBERS } });
    return route.fulfill({
      status: stubs.writeStatus ?? 200,
      json: stubs.writeStatus
        ? { error: { code: "FORBIDDEN", message: stubs.writeMessage ?? "refused" } }
        : { data: MEMBERS },
    });
  });
}

async function gotoMembers(page: Page) {
  await page.goto("/dashboard/members");
  await expect(page.getByTestId("members-page")).toBeVisible();
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = true) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

const rowFor = (page: Page, name: string) =>
  page.getByTestId("member-row").filter({ hasText: name });

/**
 * The OPEN menu for one member. Mantine keeps a dismissed menu mounted (hidden),
 * so a bare getByTestId would match the last one too; scoping to the menu's
 * accessible name is what makes "the row I just opened" unambiguous.
 */
const menuFor = (page: Page, name: string) =>
  page.getByRole("menu", { name: `Options for ${name}` });

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`Members — ${colorScheme}`, () => {
    test.use({ colorScheme });

    // ── 1. the roster ─────────────────────────────────────────
    test("phone 390: the roster carries name, relationship, role, colour and allergies", async ({ loggedInPage: page }, testInfo) => {
      onlyIPhone();
      await stubMembers(page, { role: "owner" });
      await gotoMembers(page);

      const roster = page.getByTestId("members-roster");
      await expect(roster).toBeVisible();

      const carol = rowFor(page, "Carol Reyes");
      await expect(carol).toContainText("Carol Reyes");
      await expect(carol).toContainText("nanny");
      await expect(carol.getByTestId("member-role")).toHaveText("Family member");
      await expect(carol.getByTestId("member-allergy")).toContainText("peanuts");

      // The colour dot is the member's people.color, drawn from a palette
      // variable — never a free value.
      const dot = carol.locator("[data-color]").first();
      await expect(dot).toHaveAttribute("data-color", "teal");
      expect(await dot.evaluate((el) => getComputedStyle(el).backgroundColor)).not.toBe("rgba(0, 0, 0, 0)");

      // Every member the API returned is on the page, and the counts agree.
      await expect(page.getByTestId("member-row")).toHaveCount(MEMBERS.length);
      await expect(page.getByRole("heading", { name: "Sims family" })).toBeVisible();
      await expect(page.getByText("America/Chicago")).toBeVisible();

      // Row actions reach the edge: no sideways scroll at 390 px (#11636).
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, "members-roster-iphone");
    });

    // ── 1b. the household card tells the truth about the family (#5300) ──
    test("phone 390: the family card shows the real zone and 'an owner', never 'not set'", async ({ loggedInPage: page }, testInfo) => {
      onlyIPhone();
      test.skip(colorScheme === "dark", "the card's copy runs once (light)");
      await stubMembers(page, { role: "owner" });
      await gotoMembers(page);

      // The war game's line, verbatim. The zone comes from GET /me/households
      // (the household source Today, Calendar, People and Bills read); this
      // family has one, so the card must say it and must not claim otherwise.
      const meta = page.getByTestId("household-meta");
      await expect(meta).toContainText("America/Chicago");
      await expect(meta).not.toContainText("not set");
      // "you're a owner" was the second half of the same line.
      await expect(meta).toContainText("you're an owner");
      await expect(meta).not.toContainText("a owner");

      // The name is the family's, not the placeholder the misnamed read drew.
      await expect(page.getByTestId("household-name")).toHaveText("Sims family");

      await snap(page, testInfo, "members-household-card-iphone");
    });

    test("phone 390: a family with NO zone is the only case that says 'Time zone not set'", async ({ loggedInPage: page }, testInfo) => {
      onlyIPhone();
      test.skip(colorScheme === "dark", "the empty-zone copy runs once (light)");
      // The household record itself carries an empty zone: a real family state
      // (the API stores ""), not a loading placeholder. Both reads are stubbed
      // so the two cannot disagree.
      await stubMembers(page, { role: "owner", householdTimezone: "" });
      await gotoMembers(page);

      const meta = page.getByTestId("household-meta");
      await expect(meta).toContainText("Time zone not set");
      await expect(meta).toContainText("you're an owner");
    });

    // ── 2. a non-manager sees no management controls ───────────
    test("phone 390: a family member sees the roster but no invite, role or remove control", async ({ loggedInPage: page }, testInfo) => {
      onlyIPhone();
      test.skip(colorScheme === "dark", "role gating runs once (light)");
      await stubMembers(page, { role: "family_member" });
      await gotoMembers(page);

      // The roster itself is theirs to read.
      await expect(page.getByTestId("members-roster")).toBeVisible();
      await expect(page.getByTestId("member-row")).toHaveCount(MEMBERS.length);

      // No invite button, and the page says who can manage instead of looking
      // broken.
      await expect(page.getByTestId("invite-open")).toHaveCount(0);
      await expect(page.getByTestId("members-manager-hint")).toBeVisible();
      await expect(page.getByTestId("members-manager-hint")).toContainText("owner or family manager");

      // Their own row offers no role or remove control; another member's row
      // offers nothing at all.
      await expect(page.getByTestId("member-change-role")).toHaveCount(0);
      await expect(page.getByTestId("member-remove")).toHaveCount(0);
      // Opening every menu that IS there must not reveal management: the only
      // per-row menu a member should have is their own colour.
      await expect(page.getByTestId("member-menu")).toHaveCount(1); // own row only
      await rowFor(page, "Jacob Sims").getByTestId("member-menu").click();
      const own = menuFor(page, "Jacob Sims");
      await expect(own.getByTestId("member-change-color")).toBeVisible();
      await expect(own.getByTestId("member-change-role")).toHaveCount(0);
      await expect(own.getByTestId("member-remove")).toHaveCount(0);

      await page.keyboard.press("Escape");
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, "members-non-manager-iphone");
    });

    // ── 3. colour is self-or-manager ──────────────────────────
    test("phone 390: colour is self-or-manager, and only the eight palette names are sent", async ({ loggedInPage: page }, testInfo) => {
      onlyIPhone();
      test.skip(colorScheme === "dark", "the colour rule runs once (light)");
      await stubMembers(page, { role: "family_member" });
      await gotoMembers(page);

      // A family_member: their own row offers colour, another member's does not.
      await expect(rowFor(page, "Jacob Sims").getByTestId("member-menu")).toHaveCount(1); // self
      await expect(rowFor(page, "Carol Reyes").getByTestId("member-menu")).toHaveCount(0); // someone else
      await expect(rowFor(page, "Amber Sims").getByTestId("member-menu")).toHaveCount(0);

      // The palette is exactly eight names, each a radio — never a free input.
      await rowFor(page, "Jacob Sims").getByTestId("member-menu").click();
      await menuFor(page, "Jacob Sims").getByTestId("member-change-color").click();
      const swatches = page.getByTestId("color-swatches").getByRole("radio");
      await expect(swatches).toHaveCount(8);
      for (const name of ["red", "orange", "yellow", "green", "teal", "cyan", "blue", "violet"]) {
        await expect(page.getByTestId(`color-swatch-${name}`)).toBeVisible();
      }
      await expect(page.locator('input[type="color"]')).toHaveCount(0);

      // Picking one PATCHes /people/{id} with a palette NAME.
      const patch = page.waitForRequest(
        (req) => req.method() === "PATCH" && /\/bff\/people\/p1$/.test(new URL(req.url()).pathname),
      );
      await page.getByTestId("color-swatch-green").click();
      await page.getByTestId("color-save").click();
      const body = (await (await patch).postDataJSON()) as Record<string, unknown>;
      expect(body).toEqual({ color: "green" });
      expect(String(body.color)).not.toMatch(/^#|rgb|hsl/);

      await expect(page.getByTestId("color-swatches")).toHaveCount(0);
      await snap(page, testInfo, "members-color-self-iphone");
    });

    test("phone 390: a manager recolours anyone; a housesitter gets no colour control", async ({ loggedInPage: page }, testInfo) => {
      onlyIPhone();
      // One scheme is enough for the management half: the colours themselves
      // are asserted in both schemes by the roster test above.
      test.skip(colorScheme === "dark", "management roles run once (light)");
      // A manager on someone else's row.
      await stubMembers(page, { role: "owner" });
      await gotoMembers(page);
      await expect(page.getByTestId("member-menu")).toHaveCount(MEMBERS.length);
      await rowFor(page, "Carol Reyes").getByTestId("member-menu").click();
      await expect(menuFor(page, "Carol Reyes").getByTestId("member-change-role")).toBeVisible();
      await menuFor(page, "Carol Reyes").getByTestId("member-change-color").click();
      await expect(page.getByTestId("color-swatches").getByRole("radio")).toHaveCount(8);
      // The consequence of a role change is stated before it happens.
      await page.keyboard.press("Escape");
      await rowFor(page, "Carol Reyes").getByTestId("member-menu").click();
      await menuFor(page, "Carol Reyes").getByTestId("member-change-role").click();
      await page.getByTestId("role-choice-viewer").click();
      await expect(page.getByTestId("role-consequence")).toBeVisible();
      await expect(page.getByTestId("role-consequence")).toContainText("takes access away");
      await snap(page, testInfo, "members-role-confirm-iphone");

      // A housesitter: the roster is theirs, the colour is not.
      await page.unrouteAll();
      await stubMembers(page, { role: "housesitter" });
      await gotoMembers(page);
      await expect(page.getByTestId("members-roster")).toBeVisible();
      await expect(page.getByTestId("member-menu")).toHaveCount(0);
      await expect(rowFor(page, "Carol Reyes")).toContainText("peanuts");
      await expect(page.getByTestId("invite-open")).toHaveCount(0);
      // read_basic says so, and nothing the sitter may not see is drawn: no
      // birthdate, school, grade or note anywhere on the page.
      await expect(page.getByTestId("members-basic-people")).toBeVisible();
      await expect(page.getByTestId("members-basic-people")).toContainText("Birth dates, schools and");
      for (const field of ["Birthday", "School", "Grade", "Notes", "Preferences"]) {
        await expect(page.getByText(field, { exact: true })).toHaveCount(0);
      }
      await snap(page, testInfo, "members-housesitter-iphone");
    });

    test("phone 390: a family manager can manage members but never an owner", async ({ loggedInPage: page }, testInfo) => {
      onlyIPhone();
      test.skip(colorScheme === "dark", "the manager rule runs once (light)");
      await stubMembers(page, { role: "family_manager" });
      await gotoMembers(page);

      // CanManageMembers: the invite control and the other members' menus exist,
      // and the invite picker has no Owner card (CanGrantRole).
      await expect(page.getByTestId("invite-open")).toBeVisible();
      await page.getByTestId("invite-open").click();
      await expect(page.getByTestId("invite-choices").getByRole("radio")).toHaveCount(5);
      await expect(page.getByTestId("invite-role-owner")).toHaveCount(0);
      await page.keyboard.press("Escape");

      // The caller is Jacob (GET /auth/me), who is the household's only OWNER,
      // and the page is being looked at as a family_manager — the role Jacob
      // would have after a demotion. So his own row is the "an owner" case AND
      // the "yourself" case: no role action (CanModifyMember refuses a manager
      // an owner; the last owner also cannot be demoted, 409), no remove, and a
      // colour action, because recolouring is not a management act (#4821).
      await rowFor(page, "Jacob Sims").getByTestId("member-menu").click();
      const ownRow = menuFor(page, "Jacob Sims");
      await expect(ownRow.getByTestId("member-change-color")).toBeVisible();
      await expect(ownRow.getByTestId("member-change-role")).toHaveCount(0);
      await expect(ownRow.getByTestId("member-remove")).toHaveCount(0);
      await page.keyboard.press("Escape");

      // Anyone who is neither an owner nor the caller: all three actions.
      await rowFor(page, "Amber Sims").getByTestId("member-menu").click();
      const otherRow = menuFor(page, "Amber Sims");
      await expect(otherRow.getByTestId("member-change-role")).toBeVisible();
      await expect(otherRow.getByTestId("member-change-color")).toBeVisible();
      await expect(otherRow.getByTestId("member-remove")).toBeVisible();
      await page.keyboard.press("Escape");

      // A manager's role picker has no Owner card (CanGrantRole).
      await rowFor(page, "Carol Reyes").getByTestId("member-menu").click();
      await menuFor(page, "Carol Reyes").getByTestId("member-change-role").click();
      await expect(page.getByTestId("role-choices").getByRole("radio")).toHaveCount(5);
      await expect(page.getByTestId("role-choice-owner")).toHaveCount(0);
      await page.getByTestId("role-choice-family_manager").click();
      await snap(page, testInfo, "members-manager-role-picker-iphone");
      await page.keyboard.press("Escape");
    });
  });
}

// ── States and desktop, which the role tests above do not cover ───────

test.describe("Members — states", () => {
  test.describe.configure({ mode: "serial" });

  test("phone 390: loading explains itself with skeleton rows, not a blank page", async ({ loggedInPage: page }, testInfo) => {
    onlyIPhone();
    await stubMembers(page, { role: "owner", hangRoster: true });
    await page.goto("/dashboard/members");
    await expect(page.getByTestId("members-page")).toBeVisible();
    await expect(page.getByTestId("members-loading")).toBeVisible();
    // The heading and the household are already there: a slow roster does not
    // empty the page.
    await expect(page.getByRole("heading", { name: "Sims family" })).toBeVisible();
    await snap(page, testInfo, "members-loading-iphone");
  });

  test("phone 390: a failed read is an error with Try again, never an empty roster", async ({ loggedInPage: page }, testInfo) => {
    onlyIPhone();
    await stubMembers(page, { role: "owner", readStatus: 500 });
    await page.goto("/dashboard/members");
    await expect(page.getByTestId("members-page")).toBeVisible();
    const err = page.getByTestId("members-error");
    await expect(err).toBeVisible();
    await expect(err).toContainText("Nothing was changed");
    await expect(err.getByRole("button", { name: /try again/i })).toBeVisible();
    await expect(page.getByTestId("members-roster")).toHaveCount(0);
    await expect(page.getByTestId("members-empty")).toHaveCount(0);
    await expectNoHorizontalOverflow(page);
    await snap(page, testInfo, "members-error-iphone");
  });

  test("phone 390: an empty roster says what a roster is for", async ({ loggedInPage: page }, testInfo) => {
    onlyIPhone();
    await stubMembers(page, { role: "owner", members: [], people: [] });
    await page.goto("/dashboard/members");
    const empty = page.getByTestId("members-empty");
    await expect(empty).toBeVisible();
    await expect(empty).toContainText("Nobody");
    await expect(page.getByTestId("members-roster")).toHaveCount(0);
    await snap(page, testInfo, "members-empty-iphone");
  });

  test("phone 390: a vendor gets an explanation instead of a request the API would refuse", async ({ loggedInPage: page }, testInfo) => {
    onlyIPhone();
    let membersAsked = 0;
    await stubMembers(page, { role: "vendor" });
    await page.route("**/bff/households/me/members", (route) => {
      membersAsked += 1;
      return route.fulfill({ status: 403, json: { error: { code: "FORBIDDEN", message: "no" } } });
    });
    await page.goto("/dashboard/members");
    await expect(page.getByText("isn't part of your access")).toBeVisible();
    expect(membersAsked, "the page never asks for a roster it may not read").toBe(0);
    await snap(page, testInfo, "members-vendor-iphone");
  });
});

test.describe("Members — desktop 1440", () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test("desktop: the same roster, the same rules, and the palette in one row", async ({ loggedInPage: page }, testInfo) => {
    test.skip(test.info().project.name !== "desktop-chrome", "1440 px is desktop-chrome");
    await stubMembers(page, { role: "owner" });
    await gotoMembers(page);

    await expect(page.getByTestId("member-row")).toHaveCount(MEMBERS.length);
    await expect(page.getByTestId("invite-open")).toBeVisible();
    await expect(page.getByTestId("roles-legend")).toBeVisible();
    await expect(page.getByTestId("members-basic-people")).toHaveCount(0);
    await expectNoHorizontalOverflow(page);

    // Eight swatches on one row at desktop width (4 on a phone).
    await rowFor(page, "Carol Reyes").getByTestId("member-menu").click();
    await menuFor(page, "Carol Reyes").getByTestId("member-change-color").click();
    const tops = await page.getByTestId("color-swatches").getByRole("radio").evaluateAll((els) =>
      els.map((el) => Math.round(el.getBoundingClientRect().top)),
    );
    expect(tops.length, "eight swatches").toBe(8);
    expect(new Set(tops).size, "one row of swatches at 1440 px").toBe(1);
    await snap(page, testInfo, "members-color-desktop");
    await page.keyboard.press("Escape");

    // The role dialog at desktop width: a centred dialog, the consequence
    // visible without scrolling past six cards.
    await rowFor(page, "Carol Reyes").getByTestId("member-menu").click();
    await menuFor(page, "Carol Reyes").getByTestId("member-change-role").click();
    await page.getByTestId("role-choice-viewer").click();
    await expect(page.getByTestId("role-consequence")).toBeInViewport();
    await expect(page.getByTestId("role-consequence")).toContainText("takes access away");
    await snap(page, testInfo, "members-role-desktop");
    await page.keyboard.press("Escape");

    await snap(page, testInfo, "members-roster-desktop");
  });
});
