import type { Page } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, seedHouseholds } from "./fixtures";

/**
 * Settings split into sections (#4560). Phone-first: 390 px, the width this
 * ticket is about, run in the desktop-chrome project only (the viewport is
 * pinned; see the note at the top of this file's `test.use`). The layout is the
 * approved settings mockup's (artifact #103 v1).
 *
 * Three things the API's behaviour depends on:
 *  - the sections navigate, and a section a role cannot use is not offered;
 *  - a non-manager sees the household settings READ-ONLY: the same values, no
 *    form, and no PATCH is ever sent (PATCH /households/{id} is managers only,
 *    #4820);
 *  - an invalid time zone is refused BEFORE the request, so a bad zone can never
 *    reach the database and silently move every date the family sees.
 *
 * The stub is household-aware per session (POST /e2e/households), which is how
 * the role is set: the UI reads the role from GET /me/households.
 */

// Every test here pins 390 px (an iPhone 14's viewport) whatever project runs
// it, so the phone layout is what is asserted. `onlyOn`-style project skipping
// is not needed and the suite stays out of the other three phone projects.
test.use({ viewport: { width: 390, height: 844 } });

const SIMS = "0b9d4f1e-6a3c-4b8e-9f52-1c7d2a5e8b10";

/** The signed-in session's family, with the role under test. */
async function seedFamily(
  request: Parameters<typeof seedHouseholds>[0],
  sid: string,
  role: string,
  overrides: { name?: string; timezone?: string } = {},
) {
  await seedHouseholds(request, sid, [
    {
      id: SIMS,
      name: overrides.name ?? "Sims family",
      role,
      timezone: overrides.timezone ?? "America/Chicago",
    },
  ]);
}

const index = (page: Page) => page.getByTestId("settings-index");

/**
 * GET /api/v1/auth/me answers the user object bare (auth.handler.Me writes
 * userResponse directly; unlike the household routes there is no `data`
 * envelope). The other specs stub it the same way (today.spec.ts). The e2e stub
 * server wraps it, so it is routed here rather than teaching the app to read a
 * shape the API does not send.
 */
async function routeMe(page: Page) {
  await page.route("**/bff/auth/me", (route) =>
    route.fulfill({
      json: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "e2e@homeos.test",
        name: "E2E Tester",
        is_instance_admin: false,
      },
    }),
  );
}

// ── 1. Sections navigate ─────────────────────────────────────────────────────

test("an owner's phone: the index lists every section and each one opens", async ({
  loggedInPage: page,
  stubSid,
  request,
}) => {
  await seedFamily(request, stubSid!, "owner");
  await routeMe(page);
  await page.goto("/dashboard/settings");

  // The index: one row per section, no agent-tokens row (#4580 is not built).
  await expect(index(page)).toBeVisible();
  const rows = index(page).getByRole("link");
  await expect(rows).toHaveCount(5);
  for (const name of ["Household", "Members", "Credentials", "Phone calendar", "Account"]) {
    await expect(index(page).getByRole("link", { name: `${name} settings` })).toBeVisible();
  }
  await expect(index(page).getByRole("link", { name: /agent token/i })).toHaveCount(0);

  // Household: the manager form, pre-filled from GET /me/households.
  await index(page).getByRole("link", { name: "Household settings" }).click();
  await expect(page).toHaveURL(/\/dashboard\/settings\/household$/);
  await expect(page.getByRole("heading", { level: 2, name: "Household" })).toBeVisible();
  await expect(page.getByLabel("Family name")).toHaveValue("Sims family");
  await expect(page.getByRole("textbox", { name: "Time zone" })).toHaveValue("America/Chicago");

  // The picker is a dialog, not a dropdown laid over the form: at 390 px an
  // inline list covered Save, and a tap meant for Save chose a zone.
  await page.getByRole("button", { name: "Pick from the list" }).click();
  const picker = page.getByRole("dialog");
  await picker.getByLabel("Search").fill("chicago");
  await picker.getByRole("option", { name: /America\/Chicago/ }).click();
  await expect(page.getByRole("textbox", { name: "Time zone" })).toHaveValue("America/Chicago");
  // The form is usable again straight after: nothing is left over the button.
  await expect(page.getByRole("button", { name: "Save household" })).toBeVisible();

  // Back to the index, then into another section: the phone path is
  // index -> section -> back, so Back must not strand anyone.
  await page.getByRole("link", { name: "Back to Settings" }).click();
  await expect(page).toHaveURL(/\/dashboard\/settings$/);
  await index(page).getByRole("link", { name: "Account settings" }).click();
  await expect(page).toHaveURL(/\/dashboard\/settings\/account$/);
  await expect(page.getByRole("heading", { level: 2, name: "Account" })).toBeVisible();
  await expect(page.getByLabel("Email")).toHaveValue("e2e@homeos.test");

  await page.getByRole("link", { name: "Back to Settings" }).click();
  await index(page).getByRole("link", { name: "Phone calendar settings" }).click();
  await expect(page.getByRole("button", { name: "Generate app password" })).toBeVisible();

  await expectNoHorizontalOverflow(page);

  // The browser's own Back button works too: sub-routes are real pages.
  await page.goBack();
  await expect(page).toHaveURL(/\/dashboard\/settings$/);
});

test("the CalDAV server shown is the API's server_url, never a localhost literal (#5289)", async ({
  loggedInPage: page,
}) => {
  await page.goto("/dashboard/settings/phone-calendar");

  // Before the app password exists there is no response to read, so the field
  // shows this deployment's own /dav/ — the page's origin, not a dev port.
  const serverField = page.locator('[data-settings-readonly="caldav-server"]');
  await expect(serverField).toHaveValue(/\/dav\/$/);

  await page.getByRole("button", { name: "Generate app password" }).click();

  // After it exists, the API's answer wins: the stub reports a public URL
  // (e2e/stub-server.mjs CALDAV_SERVER_URL), and it is what must be displayed.
  await expect(serverField).toHaveValue("https://home.example/dav/");
  await expect(serverField).not.toHaveValue(/localhost/);
  await expect(serverField).not.toHaveValue(/8081/);

  // The user name comes from the same response, so the pair on screen is the
  // pair the person types into the phone.
  await expect(page.locator('[data-settings-readonly="caldav-username"]')).toHaveValue(
    "e2e@homeos.test",
  );
});

test("an unknown section segment says so, and links back to the index", async ({ loggedInPage: page }) => {
  await page.goto("/dashboard/settings/not-a-section");
  await expect(page.getByTestId("settings-section-missing")).toBeVisible();
  await page.getByRole("link", { name: "Back to Settings" }).click();
  await expect(page).toHaveURL(/\/dashboard\/settings$/);
  await expect(index(page)).toBeVisible();
});

// ── 2. A non-manager sees the household read-only ────────────────────────────

test("a family member sees the household settings read-only and cannot save", async ({
  loggedInPage: page,
  stubSid,
  request,
}) => {
  await seedFamily(request, stubSid!, "family_member");

  const writes: string[] = [];
  page.on("request", (r) => {
    if (r.method() !== "GET" && r.url().includes("/households")) writes.push(`${r.method()} ${r.url()}`);
  });

  await page.goto("/dashboard/settings/household");

  // The values are there, as text — not as a form.
  await expect(page.locator('[data-settings-readonly="household-name"]')).toHaveText("Sims family");
  await expect(page.locator('[data-settings-readonly="household-timezone"]')).toContainText("America/Chicago");
  await expect(page.getByLabel("Family name")).toHaveCount(0);
  await expect(page.getByRole("textbox", { name: "Time zone" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Save household" })).toHaveCount(0);
  // And the page says who can change them.
  await expect(page.getByText(/read-only/i)).toBeVisible();

  // Sections this role cannot use are not in the index at all.
  await page.goto("/dashboard/settings");
  await expect(index(page).getByRole("link", { name: "Credentials settings" })).toBeVisible();
  await expect(index(page).getByRole("link", { name: "Members settings" })).toBeVisible();

  expect(writes).toEqual([]);
});

test("a viewer is offered no credentials section, and its URL renders no vault", async ({
  loggedInPage: page,
  stubSid,
  request,
}) => {
  await seedFamily(request, stubSid!, "viewer");

  await page.goto("/dashboard/settings");
  const rows = index(page).getByRole("link");
  await expect(rows).toHaveCount(4);
  await expect(index(page).getByRole("link", { name: "Credentials settings" })).toHaveCount(0);

  // Deep-linking the gated section is refused in the UI too: no probe of the
  // vault happens, because the section is not rendered.
  const probes: string[] = [];
  page.on("request", (r) => {
    if (r.url().includes("/secrets/key")) probes.push(r.url());
  });
  await page.goto("/dashboard/settings/credentials");
  await expect(page.getByTestId("settings-section-forbidden")).toBeVisible();
  await expect(page.getByText("Secrets Manager")).toHaveCount(0);
  expect(probes).toEqual([]);
});

// ── 3. An invalid time zone is refused ───────────────────────────────────────

test("an invalid time zone is refused in the form and never sent to the API", async ({
  loggedInPage: page,
  stubSid,
  request,
}) => {
  await seedFamily(request, stubSid!, "owner");

  const patches: string[] = [];
  page.on("request", (r) => {
    if (r.method() === "PATCH") patches.push(r.url());
  });

  await page.goto("/dashboard/settings/household");
  const tz = page.getByRole("textbox", { name: "Time zone" });
  await expect(tz).toHaveValue("America/Chicago");

  // An invalid value can also be injected by typing, which is the path the
  // field must validate: the picker only offers names the API accepts.

  // 1. Junk.
  await tz.fill("Mars/Olympus_Mons");
  await page.getByRole("button", { name: "Save household" }).click();
  await expect(page.getByText(/Use a time zone name from the list/)).toBeVisible();
  // 2. A real zone written the way a case-insensitive browser resolves it: the
  //    one that would silently become UTC on a Linux server.
  await tz.fill("america/chicago");
  await page.getByRole("button", { name: "Save household" }).click();
  await expect(page.getByText(/Use a time zone name from the list/)).toBeVisible();
  // 3. Blank.
  await tz.fill("");
  await page.getByRole("button", { name: "Save household" }).click();
  await expect(page.getByText("Enter a time zone")).toBeVisible();

  // Nothing was sent: the stored zone is untouched.
  expect(patches).toEqual([]);

  // A valid zone does save, and only that call goes out.
  await tz.fill("America/New_York");
  const patch = page.waitForRequest((r) => r.method() === "PATCH" && r.url().includes(`/households/${SIMS}`));
  await page.getByRole("button", { name: "Save household" }).click();
  const body = (await patch).postDataJSON();
  expect(body).toEqual({ timezone: "America/New_York" });
  await expect(page.getByRole("status").filter({ hasText: "Saved" })).toBeVisible();
});

test("a name that is only spaces is refused before the request", async ({
  loggedInPage: page,
  stubSid,
  request,
}) => {
  await seedFamily(request, stubSid!, "owner");
  const patches: string[] = [];
  page.on("request", (r) => {
    if (r.method() === "PATCH") patches.push(r.url());
  });

  await page.goto("/dashboard/settings/household");
  await page.getByLabel("Family name").fill("   ");
  await page.getByRole("button", { name: "Save household" }).click();
  await expect(page.getByText("Enter a name for your household")).toBeVisible();
  expect(patches).toEqual([]);
});

// ── 4. Account: the password change only claims success on a 200 ─────────────

test("a wrong current password keeps the person on the page and claims nothing", async ({
  loggedInPage: page,
}) => {
  await page.route("**/bff/auth/change-password", (route) =>
    route.fulfill({
      status: 401,
      json: { error: { code: "INVALID_CREDENTIALS", message: "current password is incorrect" } },
    }),
  );

  await page.goto("/dashboard/settings/account");
  await page.getByLabel("Current password").fill("wrong password");
  await page.getByLabel("New password", { exact: true }).fill("maple-otter-river");
  await page.getByLabel("Type it again").fill("maple-otter-river");
  await page.getByRole("button", { name: "Change password" }).click();

  await expect(page.getByText(/current password is not right/i)).toBeVisible();
  await expect(page.getByText("Password changed")).toHaveCount(0);
  // The API's 401 for a wrong current password is NOT a dead session: the
  // shared client must not have sent the person to /login.
  expect(new URL(page.url()).pathname).toBe("/dashboard/settings/account");
});

test("a password change is only reported once the API answers 200", async ({ loggedInPage: page }) => {
  let seen: unknown = null;
  await page.route("**/bff/auth/change-password", async (route) => {
    seen = route.request().postDataJSON();
    await route.fulfill({ status: 200, json: { message: "password changed" } });
  });

  await page.goto("/dashboard/settings/account");
  await page.getByLabel("Current password").fill("the old one");
  await page.getByLabel("New password", { exact: true }).fill("maple-otter-river");
  await page.getByLabel("Type it again").fill("maple-otter-river");
  await page.getByRole("button", { name: "Change password" }).click();

  await expect(page.getByText("Password changed")).toBeVisible();
  expect(seen).toEqual({ current_password: "the old one", new_password: "maple-otter-river" });
});

test("mismatched new passwords never reach the API", async ({ loggedInPage: page }) => {
  const posts: string[] = [];
  page.on("request", (r) => {
    if (r.url().includes("/change-password")) posts.push(r.url());
  });
  await page.goto("/dashboard/settings/account");
  await page.getByLabel("Current password").fill("the old one");
  await page.getByLabel("New password", { exact: true }).fill("maple-otter-river");
  await page.getByLabel("Type it again").fill("maple-otter-rivr");
  await page.getByRole("button", { name: "Change password" }).click();
  await expect(page.getByText(/don't match/)).toBeVisible();
  expect(posts).toEqual([]);
});
