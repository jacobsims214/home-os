import type { Page } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, testUser } from "./fixtures";

/**
 * The invite accept page (#4579): /invites/accept?token=…
 *
 * WHY THESE TESTS. The page's look is checked against the approved mockup
 * (artifact #103 v1); what a screenshot cannot show is whether a link can
 * actually be spent, and whether the bad cases stay calm. Each test below is one
 * of those:
 *
 *  1. A SIGNED-OUT PERSON WITH A LINK lands on the page, not on /login, and the
 *     form posts {token,name,password} to the API's PUBLIC route. Both halves
 *     matter: the middleware decides whether the token survives at all, and the
 *     BFF would answer 401 before the API saw it (that route is the one thing
 *     that must not go through /bff).
 *  2. A SIGNED-IN PERSON accepts through the BFF, which is the only place that
 *     can add the Bearer, and no password is asked for.
 *  3. EVERY BAD ANSWER IS A PAGE, NOT A CRASH: expired, used, revoked, wrong
 *     email, existing account, missing token. The wrong-email case offers sign
 *     out and comes back with the token still in the return path.
 *  4. THE TOKEN IS A SECRET: it is never in the page's text, and it goes back
 *     into a URL only as the login redirect's parameter.
 *  5. THE PHONE RULES the mockup sets: one h1, nothing scrolls sideways at
 *     390 px, 44 px actions and 16 px fields (below that iOS zooms the page).
 *
 * Stub mode only: the accept routes are fulfilled here so each API answer can be
 * played exactly. The session (cookie, middleware, BFF) is the real code.
 */

const TOKEN = "4f1c9a7e-2b8d-4e61-9a0f-7c3d5e2b1a64";
const ACCEPT_PATH = `/invites/accept?token=${TOKEN}`;

function onlyChromium(): void {
  test.skip(test.info().project.name !== "desktop-chrome", "one run: the behaviour does not depend on the viewport");
}

function onlyPhone(): void {
  test.skip(!["desktop-chrome", "iphone-390"].includes(test.info().project.name), "phone-width layout: 390 px projects");
}

test.beforeEach(() => {
  test.skip(!!process.env.E2E_BASE_URL, "fulfils the accept routes; stub mode only");
});

/**
 * The accept POSTs, intercepted, from either side of the session.
 *
 * `signedOut` answers the PUBLIC route (/api/v1/invites/accept, no session);
 * `signedIn` answers the BFF route (/bff/invites/accept, session attached).
 * Each records the request so a test can assert where the token went.
 */
async function stubAccept(
  page: Page,
  answer: { signedOut?: { status: number; json?: unknown }; signedIn?: { status: number; json?: unknown } },
) {
  const requests: Array<{ path: string; body: Record<string, unknown> }> = [];
  if (answer.signedOut) {
    await page.route("**/api/v1/invites/accept", async (route) => {
      requests.push({ path: new URL(route.request().url()).pathname, body: route.request().postDataJSON() ?? {} });
      const { status, json } = answer.signedOut!;
      await route.fulfill({ status, json: json ?? {} });
    });
  }
  if (answer.signedIn) {
    await page.route("**/bff/invites/accept", async (route) => {
      requests.push({ path: new URL(route.request().url()).pathname, body: route.request().postDataJSON() ?? {} });
      const { status, json } = answer.signedIn!;
      await route.fulfill({ status, json: json ?? {} });
    });
  }
  return requests;
}

/** The page's text, with any token removed, so "never rendered" can be asserted. */
async function visibleText(page: Page): Promise<string> {
  return page.locator("body").innerText();
}

// ── 1. A new person joins through the link ─────────────────────────────

test("signed out: the link opens the form and posts to the public API route", async ({ page }) => {
  onlyChromium();
  const requests = await stubAccept(page, {
    signedOut: { status: 201, json: { data: { role: "family_member", user_created: true, email: "kim@example.test" } } },
  });

  await page.goto(ACCEPT_PATH);

  // The middleware let them in: no bounce to /login, so the token is still here.
  expect(new URL(page.url()).pathname).toBe("/invites/accept");
  await expect(page.getByRole("heading", { name: "Create your account" })).toBeVisible();
  await expect(page.getByTestId("invite-name")).toBeVisible();

  await page.getByTestId("invite-name").fill("Kim Hart");
  await page.getByTestId("invite-password").fill("maple-otter-river");
  await page.getByTestId("invite-confirm").fill("maple-otter-river");
  await page.getByTestId("invite-create").click();

  await expect(page.getByRole("heading", { name: "You're in" })).toBeVisible();
  // Signed up but not signed in yet: the first sign-in uses the password just set.
  await expect(page.getByTestId("invite-signin")).toHaveAttribute("href", "/login");

  // Exactly one request, straight to the API's public route: the BFF would have
  // answered 401 before the API saw the token.
  expect(requests).toHaveLength(1);
  expect(requests[0].path).toBe("/api/v1/invites/accept");
  expect(requests[0].body).toEqual({ token: TOKEN, name: "Kim Hart", password: "maple-otter-river" });
});

test("signed out: the form refuses to spend the link on a typo", async ({ page }) => {
  onlyChromium();
  const requests = await stubAccept(page, { signedOut: { status: 201, json: { data: {} } } });

  await page.goto(ACCEPT_PATH);
  await page.getByTestId("invite-name").fill("Kim Hart");
  await page.getByTestId("invite-password").fill("maple-otter-river");
  await page.getByTestId("invite-confirm").fill("maple-otter-rivr");
  await page.getByTestId("invite-create").click();

  await expect(page.getByText("These don't match yet. Check the second one.")).toBeVisible();
  expect(requests, "an invite is single use: no request until the fields agree").toHaveLength(0);
});

// ── 2. An existing account accepts ─────────────────────────────────────

test("signed in: one press accepts through the BFF as the session's email", async ({ loggedInPage: page }) => {
  onlyChromium();
  const requests = await stubAccept(page, {
    signedIn: { status: 200, json: { data: { role: "viewer", user_created: false, email: testUser.email } } },
  });

  await page.goto(ACCEPT_PATH);

  await expect(page.getByRole("heading", { name: "Join this family?" })).toBeVisible();
  // The page names who is asking, so a wrong-account arrival is obvious before
  // anything is accepted.
  await expect(page.getByText(testUser.email, { exact: false })).toBeVisible();
  // No password: this person already has one.
  await expect(page.locator('input[type="password"]')).toHaveCount(0);

  await page.getByTestId("invite-accept").click();
  await expect(page.getByRole("heading", { name: "You're in" })).toBeVisible();
  // Already signed in: the way on is the app, not a second sign-in.
  await expect(page.getByTestId("invite-home")).toHaveAttribute("href", "/dashboard");

  expect(requests).toHaveLength(1);
  expect(requests[0].path).toBe("/bff/invites/accept");
  expect(requests[0].body).toEqual({ token: TOKEN });
});

// ── 3. The bad cases are pages, not crashes ────────────────────────────

// The one action each answer offers. Expired and revoked are finished (ask for
// a new link); "used" may have been someone else, so it points at sign-in.
const DEAD: Array<[string, number, unknown, string, string, string]> = [
  ["expired", 400, { error: { code: "BAD_REQUEST", message: "invitation has expired" } }, "error-expired", "This invite has expired", "invite-go-signin"],
  ["used", 409, { error: { code: "CONFLICT", message: "invitation already accepted" } }, "error-used", "This invite has already been used", "invite-signin-to-accept"],
  ["revoked", 404, { error: { code: "NOT_FOUND", message: "invalid or expired invitation" } }, "error-unknown", "This invite link doesn't work anymore", "invite-go-signin"],
];

for (const [name, status, body, state, heading, action] of DEAD) {
  test(`signed out: a ${name} invite says so and offers a way on`, async ({ page }) => {
    onlyChromium();
    const requests = await stubAccept(page, { signedOut: { status, json: body } });

    await page.goto(ACCEPT_PATH);
    await page.getByTestId("invite-name").fill("Kim Hart");
    await page.getByTestId("invite-password").fill("maple-otter-river");
    await page.getByTestId("invite-confirm").fill("maple-otter-river");
    await page.getByTestId("invite-create").click();

    const card = page.locator(`[data-state="${state}"]`);
    await expect(card).toBeVisible();
    await expect(page.getByRole("heading", { name: heading })).toBeVisible();
    // A dead link offers no form and no retry that could only fail again: one
    // action, and it is the one that can help.
    await expect(page.locator('input[type="password"]')).toHaveCount(0);
    await expect(page.getByTestId(action)).toBeVisible();
    if (action === "invite-go-signin") await expect(page.getByTestId(action)).toHaveAttribute("href", "/login");
    // One request: a dead link is not retried behind the person's back.
    expect(requests).toHaveLength(1);
  });
}

test("an address that already has an account is offered sign-in, not a dead end", async ({ page }) => {
  onlyChromium();
  await stubAccept(page, {
    signedOut: {
      status: 409,
      json: { error: { code: "CONFLICT", message: "an account with this email already exists; sign in to accept the invitation" } },
    },
  });

  await page.goto(ACCEPT_PATH);
  await page.getByTestId("invite-name").fill("Kim Hart");
  await page.getByTestId("invite-password").fill("maple-otter-river");
  await page.getByTestId("invite-confirm").fill("maple-otter-river");
  await page.getByTestId("invite-create").click();

  await expect(page.getByRole("heading", { name: "You already have an account" })).toBeVisible();
  // The link survives: signing in returns to this invite, token and all.
  const href = await page.getByTestId("invite-signin-to-accept").getAttribute("href");
  const redirect = new URL(href!, "https://home.example").searchParams.get("redirect");
  expect(redirect).toBe(ACCEPT_PATH);
});

test("signed in as the wrong email: sign out comes back to the same invite", async ({ loggedInPage: page }) => {
  onlyChromium();
  await stubAccept(page, {
    signedIn: { status: 403, json: { error: { code: "FORBIDDEN", message: "this invitation was sent to a different email address" } } },
  });
  // The logout POST is real (it clears the cookies); the login start is answered
  // so the browser never leaves for Dex, and the return path it was given can be
  // read.
  let loginStart: URL | null = null;
  await page.route("**/api/auth/login**", async (route) => {
    loginStart = new URL(route.request().url());
    await route.fulfill({ status: 200, contentType: "text/plain", body: "login start" });
  });

  await page.goto(ACCEPT_PATH);
  await page.getByTestId("invite-accept").click();

  await expect(page.getByRole("heading", { name: "This invite is for a different address" })).toBeVisible();
  expect(await visibleText(page)).not.toMatch(/expired/i);

  await page.getByTestId("invite-sign-out").click();
  // /login is the next page and hands straight off to /api/auth/login, carrying
  // the return path through. Either hop proves where the invite came back to.
  await expect.poll(() => loginStart && loginStart.pathname).toBe("/api/auth/login");
  // The token rode along in the return path, so signing in as the right person
  // lands back on this invite rather than on Today.
  expect(loginStart!.searchParams.get("redirect")).toBe(ACCEPT_PATH);
  // And the session really ended: the cookie is gone.
  const cookies = await page.context().cookies();
  expect(cookies.some((c) => c.name === "home-os-token" && c.value)).toBe(false);
});

test("a link with no token is its own card, not an empty form", async ({ page }) => {
  onlyChromium();
  await page.goto("/invites/accept");
  await expect(page.getByRole("heading", { name: "This invite link is incomplete" })).toBeVisible();
  await expect(page.locator('input[type="password"]')).toHaveCount(0);
  await expect(page.getByTestId("invite-go-signin")).toHaveAttribute("href", "/login");
});

// ── 4. The token is a secret ───────────────────────────────────────────

test("the token never reaches the page's text, a fragment or a shortened label", async ({ page }) => {
  onlyChromium();
  await stubAccept(page, { signedOut: { status: 404, json: { error: { code: "NOT_FOUND", message: "invalid or expired invitation" } } } });

  await page.goto(ACCEPT_PATH);
  await page.getByTestId("invite-name").fill("Kim Hart");
  await page.getByTestId("invite-password").fill("maple-otter-river");
  await page.getByTestId("invite-confirm").fill("maple-otter-river");
  await page.getByTestId("invite-create").click();
  await expect(page.getByTestId("invite-go-signin")).toBeVisible();

  const text = await visibleText(page);
  expect(text).not.toContain(TOKEN);
  // Not even a prefix of it: a shortened token is still a hint.
  expect(text).not.toContain(TOKEN.slice(0, 8));
  // And it never moves into the fragment, where the server would never see it.
  expect(new URL(page.url()).hash).toBe("");
  // The one place it does go back into a URL is the login redirect, as a param.
  const href = await page.getByTestId("invite-go-signin").getAttribute("href");
  expect(href).toBe("/login");
  // No element a person can see carries it either (a label, a href, a title).
  // Next's own inline <script> ferry does: that is how the token reaches the
  // page at all, and it is not rendered.
  const leaks = await page.evaluate((token) => {
    const hits: string[] = [];
    for (const el of Array.from(document.querySelectorAll<HTMLElement>("body *"))) {
      if (el.tagName === "SCRIPT" || el.tagName === "STYLE") continue;
      for (const attr of Array.from(el.attributes)) {
        if (attr.name === "data-testid" || attr.name === "class") continue;
        if (attr.value.includes(token)) hits.push(`${el.tagName}[${attr.name}]`);
      }
      if (el.children.length === 0 && (el.textContent ?? "").includes(token)) hits.push(`${el.tagName} text`);
    }
    return hits;
  }, TOKEN);
  expect(leaks).toEqual([]);
});

test("the 'already have an account' link carries the invite back after login", async ({ page }) => {
  onlyChromium();
  await page.goto(ACCEPT_PATH);
  await expect(page.getByTestId("invite-have-account")).toBeVisible();
  const href = await page.getByTestId("invite-have-account").getAttribute("href");
  const redirect = new URL(href!, "https://home.example").searchParams.get("redirect");
  expect(redirect).toBe(ACCEPT_PATH);
});

// ── 5. The members page hands the link over safely ─────────────────────

test("members: a fresh invite offers Copy link, Share where available, and says who could use it", async ({ loggedInPage: page }) => {
  onlyChromium();
  const HOUSEHOLD = {
    id: "0b9d4f1e-6a3c-4b8e-9f52-1c7d2a5e8b10",
    name: "Sims family",
    timezone: "America/Chicago",
    role: "owner",
    joined_at: "2026-01-01T00:00:00Z",
    is_default: true,
    current: true,
  };
  await page.route("**/bff/me/households", (route) => route.fulfill({ json: { data: [HOUSEHOLD] } }));
  await page.route("**/bff/auth/me", (route) => route.fulfill({ json: { data: { id: "u1", email: "jacob@sims.family" } } }));
  await page.route("**/bff/households/me", (route) =>
    route.fulfill({
      json: {
        data: {
          ID: HOUSEHOLD.id,
          Name: HOUSEHOLD.name,
          Timezone: HOUSEHOLD.timezone,
          Settings: null,
          CreatedAt: "2026-01-01T00:00:00Z",
          UpdatedAt: "2026-01-01T00:00:00Z",
        },
      },
    }),
  );
  await page.route("**/bff/households/me/members", (route) =>
    route.fulfill({
      json: {
        data: [
          { user_id: "u1", email: "jacob@sims.family", name: "Jacob Sims", role: "owner", joined_at: "2026-03-02T00:00:00Z", vendor_id: null, vendor_name: null, color: "cyan" },
        ],
      },
    }),
  );
  await page.route("**/bff/people", (route) =>
    route.fulfill({
      json: { data: [{ id: "p1", display_name: "Jacob Sims", relationship: "parent", allergies: [], user_id: "u1", color: "cyan" }] },
    }),
  );
  await page.route("**/bff/vendors**", (route) => route.fulfill({ json: { data: [] } }));
  await page.route("**/bff/invites", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ json: { data: [] } })
      : route.fulfill({ status: 201, json: { data: { accept_url: `https://home.example${ACCEPT_PATH}` } } }),
  );
  // A phone has the OS share sheet; this browser has to be told it does.
  await page.addInitScript(() => {
    // @ts-expect-error test shim
    navigator.share = async () => undefined;
  });

  await page.goto("/dashboard/members");
  await page.getByTestId("invite-open").click();
  await page.getByLabel("Their email").fill("kim.hart@example.test");
  await page.getByTestId("invite-create").click();

  // Copy link is always there; Share joins it when the browser can share.
  await expect(page.getByTestId("invite-copy")).toBeVisible();
  await expect(page.getByTestId("invite-share")).toBeVisible();
  // The warning names the role the link grants and who it is for, and it never
  // prints the URL: the token leaves only through the clipboard or the sheet.
  const note = page.getByTestId("invite-secret-note");
  await expect(note).toContainText("Anyone who has this link can join as");
  await expect(note).toContainText("Family member");
  await expect(note).toContainText("kim.hart@example.test");
  expect(await note.innerText()).not.toContain(TOKEN);
  expect(await page.locator("[data-testid=invite-copy]").innerText()).not.toContain(TOKEN);
});

// ── 6. The phone rules ─────────────────────────────────────────────────

test("phone 390: the signup form fits, with one h1 and 16 px fields", async ({ page }) => {
  onlyPhone();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(ACCEPT_PATH);

  await expect(page.getByRole("heading", { name: "Create your account", level: 1 })).toBeVisible();
  await expect(page.locator("h1")).toHaveCount(1);
  await expectNoHorizontalOverflow(page);

  for (const id of ["invite-name", "invite-password", "invite-confirm"]) {
    // Mantine puts data-testid on the <input> itself.
    const input = page.getByTestId(id);
    const box = await input.boundingBox();
    expect(box!.height, `${id} is a 48 px field`).toBeGreaterThanOrEqual(44);
    const fontSize = await input.evaluate((el) => parseFloat(getComputedStyle(el).fontSize));
    expect(fontSize, `${id} does not trigger iOS zoom`).toBeGreaterThanOrEqual(16);
  }
  const submit = page.getByTestId("invite-create");
  expect((await submit.boundingBox())!.height).toBeGreaterThanOrEqual(44);
});

test("phone 390: a dead link fits and names the one thing to do", async ({ page }) => {
  onlyPhone();
  await page.setViewportSize({ width: 390, height: 844 });
  await stubAccept(page, { signedOut: { status: 400, json: { error: { code: "BAD_REQUEST", message: "invitation has expired" } } } });
  await page.goto(ACCEPT_PATH);
  await page.getByTestId("invite-name").fill("Kim Hart");
  await page.getByTestId("invite-password").fill("maple-otter-river");
  await page.getByTestId("invite-confirm").fill("maple-otter-river");
  await page.getByTestId("invite-create").click();

  await expect(page.getByRole("heading", { name: "This invite has expired", level: 1 })).toBeVisible();
  await expect(page.getByText("Ask the person who invited you for a new link.")).toBeVisible();
  await expectNoHorizontalOverflow(page);
  expect((await page.getByTestId("invite-go-signin").boundingBox())!.height).toBeGreaterThanOrEqual(44);
});
