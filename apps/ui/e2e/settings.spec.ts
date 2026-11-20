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

  // The index: one row per section, Agent tokens included since #4580.
  await expect(index(page)).toBeVisible();
  const rows = index(page).getByRole("link");
  await expect(rows).toHaveCount(6);
  for (const name of ["Household", "Members", "Credentials", "Phone calendar", "Agent tokens", "Account"]) {
    await expect(index(page).getByRole("link", { name: `${name} settings` })).toBeVisible();
  }

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
  // Everything except Credentials, plus Agent tokens (a viewer may hold a
  // read-only one: the API grants ResAgentTokens ActCreate to rolesReaders).
  await expect(rows).toHaveCount(5);
  await expect(index(page).getByRole("link", { name: "Credentials settings" })).toHaveCount(0);
  await expect(index(page).getByRole("link", { name: "Agent tokens settings" })).toBeVisible();

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

// ── 5. Agent tokens (#4580) ──────────────────────────────────────────────────
//
// The one thing this surface must never do is show a token it was not just
// given: POST is the only response with the plaintext (the API sets no-store),
// every read carries token_prefix only. So the assertions are about which
// characters are on screen, not about a happy path:
//
//  1. the full token appears exactly once, in the dialog straight after
//     creation, and is gone from the DOM once that dialog closes;
//  2. a list row shows the prefix, never the whole value;
//  3. revoking asks first, and only the confirmed tap sends the DELETE.

/** The API's Create shape: metadata plus the one-time plaintext. */
const NEW_TOKEN =
  "hos_Pq7Lm2Xc9vB4nR8tK1wZ6yH3jF5dG0sA2eU7iO4pQ9r";

const SEED_TOKENS = [
  {
    id: "t1",
    name: "Moltis on the home server",
    token_prefix: "hos_k3Fq9aZx",
    scopes: ["read", "write"],
    last_used_at: "2026-11-18T17:56:00Z",
    expires_at: null,
    revoked_at: null,
    created_at: "2026-11-01T10:00:00Z",
  },
  {
    id: "t2",
    name: "Old laptop",
    token_prefix: "hos_2Lr0bTnv",
    scopes: ["read"],
    last_used_at: null,
    expires_at: null,
    revoked_at: "2026-11-02T12:00:00Z",
    created_at: "2026-10-01T10:00:00Z",
  },
];

/** Serves the token endpoints; the create echoes NEW_TOKEN as the API does. */
async function routeTokens(page: Page, rows = SEED_TOKENS) {
  const calls: string[] = [];
  await page.route("**/bff/agent-tokens", async (route) => {
    if (route.request().method() === "POST") {
      calls.push(`POST ${await route.request().postData()}`);
      return route.fulfill({
        status: 201,
        json: {
          data: {
            id: "t3",
            name: "Moltis on my phone",
            token_prefix: NEW_TOKEN.slice(0, 12),
            scopes: ["read"],
            last_used_at: null,
            expires_at: null,
            revoked_at: null,
            created_at: "2026-11-18T18:00:00Z",
            token: NEW_TOKEN,
          },
        },
      });
    }
    return route.fulfill({ json: { data: rows } });
  });
  await page.route("**/bff/agent-tokens/*", (route) => {
    calls.push(`${route.request().method()} ${route.request().url()}`);
    return route.fulfill({ status: 204, body: "" });
  });
  return calls;
}

test("a token is shown once, then only its prefix is on screen", async ({
  loggedInPage: page,
  stubSid,
  request,
}) => {
  await seedFamily(request, stubSid!, "owner");
  const calls = await routeTokens(page);
  await page.goto("/dashboard/settings/agent-tokens");

  // The list draws prefixes, and never the value of a token it was not given.
  await expect(page.getByText("Moltis on the home server")).toBeVisible();
  await expect(page.getByText("hos_k3Fq9aZx…")).toBeVisible();
  await expect(page.getByText(NEW_TOKEN)).toHaveCount(0);
  await expect(page.getByText("Read & write")).toBeVisible();
  // The revoked token is kept, under its own group, not in the active list.
  await expect(page.getByText("Revoked", { exact: false }).first()).toBeVisible();

  // Create: name, then the read-only choice is the default.
  await page.getByRole("button", { name: "Create token" }).first().click();
  const form = page.getByRole("dialog");
  await form.getByLabel("Name").fill("Moltis on my phone");
  await form.getByRole("radio", { name: /Read only/ }).click();
  await form.getByRole("button", { name: "Create token" }).click();

  // The one chance to copy it: the full value, with the warning.
  const shown = page.getByRole("dialog", { name: "Copy your token" });
  await expect(shown).toBeVisible();
  await expect(shown.getByText(NEW_TOKEN)).toBeVisible();
  await expect(shown.getByText(/only time you'll see it/i)).toBeVisible();
  expect(calls.some((c) => c.startsWith("POST"))).toBe(true);

  // Closing it drops the value: no copy is left anywhere in the page — and it
  // was never written to localStorage.
  await shown.getByRole("button", { name: /I've copied it/ }).click();
  await expect(shown).toBeHidden();
  await expect(page.getByText(NEW_TOKEN)).toHaveCount(0);
  expect(await page.evaluate(() => JSON.stringify(localStorage))).not.toContain(NEW_TOKEN);

  await expectNoHorizontalOverflow(page);
});

test("a viewer's write choice is disabled and explained, not silently refused", async ({
  loggedInPage: page,
  stubSid,
  request,
}) => {
  await seedFamily(request, stubSid!, "viewer");
  await routeTokens(page, []);
  await page.goto("/dashboard/settings/agent-tokens");

  await expect(page.getByText("No agent tokens yet")).toBeVisible();
  await page.getByRole("button", { name: "Create token" }).first().click();
  const form = page.getByRole("dialog");

  // The API refuses `write` for a viewer (ActWriteScope is rolesFamily), so the
  // card says so instead of letting the person submit into a 403.
  const writeChoice = form.getByRole("radio", { name: /Read & write/ });
  await expect(writeChoice).toHaveAttribute("aria-disabled", "true");
  await expect(form.getByText(/can only create read-only tokens/i)).toBeVisible();
});

test("revoking asks first, and only the confirmation sends the DELETE", async ({
  loggedInPage: page,
  stubSid,
  request,
}) => {
  await seedFamily(request, stubSid!, "owner");
  const calls = await routeTokens(page);
  await page.goto("/dashboard/settings/agent-tokens");

  await page.getByRole("button", { name: "Revoke Moltis on the home server" }).click();
  const confirm = page.getByRole("dialog");
  await expect(confirm.getByRole("heading", { name: /Revoke Moltis on the home server\?/ })).toBeVisible();

  // Backing out sends nothing: a revoke cannot be undone.
  await confirm.getByRole("button", { name: "Keep token" }).click();
  await expect(confirm).toBeHidden();
  expect(calls.filter((c) => c.startsWith("DELETE"))).toEqual([]);

  await page.getByRole("button", { name: "Revoke Moltis on the home server" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Revoke token" }).click();
  await expect.poll(() => calls.filter((c) => c.startsWith("DELETE")).length).toBe(1);
  expect(calls.find((c) => c.startsWith("DELETE"))).toContain("/agent-tokens/t1");
});
