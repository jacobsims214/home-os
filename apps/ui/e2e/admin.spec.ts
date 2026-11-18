import type { Page, Route } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow } from "./fixtures";

/**
 * Instance admin: families and users (#4573).
 *
 * The page is instance-admin only, and that flag is `is_instance_admin` on
 * GET /api/v1/auth/me — not the household role. So every test pins /me, the way
 * settings.spec.ts does: the stub wraps /auth/me in `{data}` but the API sends
 * the user object bare (auth.handler.Me writes userResponse directly).
 *
 * The /admin routes are stubbed per test rather than in e2e/stub-server.mjs:
 * they are this page's whole surface, and a test wants to read (and assert on)
 * the exact body the page posts. What is NOT stubbed is anything in front of
 * them — the middleware, the BFF, the React Query cache — so the assertions
 * still exercise the real UI code.
 *
 * The layout is the approved mockup's (artifact #103 v1): counts, then Users /
 * Families tabs, Add user in a ResponsiveModal, Deactivate behind a
 * confirmation. The phone width is pinned the way settings.spec.ts pins it.
 */

const SELF = {
  id: "00000000-0000-4000-8000-000000000001",
  email: "e2e@homeos.test",
  name: "E2E Tester",
};

const SIMS = { id: "0b9d4f1e-6a3c-4b8e-9f52-1c7d2a5e8b10", name: "Sims family" };
const HART = { id: "5e2b7c9d-1a44-4f60-8c31-9d0e2b6a4f77", name: "The Harts" };

type StubUser = {
  id: string;
  email: string;
  name: string;
  active: boolean;
  is_instance_admin: boolean;
  created_at: string;
  updated_at: string;
  memberships: { household_id: string; household_name: string; role: string }[];
};

function user(overrides: Partial<StubUser> & Pick<StubUser, "id" | "email" | "name">): StubUser {
  return {
    active: true,
    is_instance_admin: false,
    created_at: "2026-03-02T12:00:00Z",
    updated_at: "2026-03-02T12:00:00Z",
    memberships: [],
    ...overrides,
  };
}

const SELF_USER = user({
  ...SELF,
  is_instance_admin: true,
  memberships: [{ household_id: SIMS.id, household_name: SIMS.name, role: "owner" }],
});

const AMBER = user({
  id: "00000000-0000-4000-8000-0000000000b2",
  email: "amber@sims.family",
  name: "Amber Sims",
  memberships: [{ household_id: SIMS.id, household_name: SIMS.name, role: "family_manager" }],
});

const RICK = user({
  id: "00000000-0000-4000-8000-0000000000c3",
  email: "rick@bensonlawn.com",
  name: "Rick Benson",
  active: false,
  memberships: [{ household_id: SIMS.id, household_name: SIMS.name, role: "vendor" }],
});

const FAMILIES = [
  {
    id: SIMS.id,
    name: SIMS.name,
    timezone: "America/Chicago",
    member_count: 2,
    created_at: "2026-03-02T12:00:00Z",
  },
];

/** What the page posts to POST /admin/users, captured for assertions. */
type CreateBody = {
  name: string;
  email: string;
  password: string;
  role?: string;
  household_id?: string;
  household_name?: string;
  timezone?: string;
};

type AdminApi = {
  created: CreateBody[];
  activated: string[];
  users: StubUser[];
};

/**
 * Pins /me for the session and answers the five /admin routes. Returns the
 * recorder the test asserts on; `admin` false is a plain member of the family
 * (the page must never ask /admin anything).
 */
async function routeAdmin(page: Page, options: { admin: boolean }): Promise<AdminApi> {
  const api: AdminApi = { created: [], activated: [], users: [SELF_USER, AMBER, RICK] };

  await page.route("**/bff/auth/me", (route: Route) =>
    route.fulfill({
      json: { ...SELF, is_instance_admin: options.admin },
    }),
  );

  await page.route("**/bff/admin/**", async (route: Route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const method = request.method();

    if (path === "/bff/admin/families" && method === "GET") {
      return route.fulfill({ json: { data: FAMILIES } });
    }
    if (path === "/bff/admin/users" && method === "GET") {
      return route.fulfill({ json: { data: api.users } });
    }
    if (path === "/bff/admin/users" && method === "POST") {
      const body = JSON.parse(request.postData() ?? "{}") as CreateBody;
      api.created.push(body);
      const created: StubUser = user({
        id: "00000000-0000-4000-8000-0000000000d4",
        email: body.email,
        name: body.name,
        memberships: [
          {
            household_id: body.household_id ?? HART.id,
            household_name: body.household_id === HART.id ? HART.name : SIMS.name,
            role: body.role ?? "owner",
          },
        ],
      });
      api.users = [...api.users, created];
      return route.fulfill({
        status: 201,
        json: {
          data: {
            user_id: created.id,
            email: created.email,
            name: created.name,
            household_id: body.household_id ?? HART.id,
            household_name: body.household_id === HART.id ? HART.name : SIMS.name,
            role: body.role ?? "owner",
            household_created: body.household_id == null,
          },
        },
      });
    }
    const deactivate = path.match(/^\/bff\/admin\/users\/([^/]+)\/(deactivate|reactivate)$/);
    if (deactivate && method === "POST") {
      const [, id, action] = deactivate;
      api.activated.push(`${action}:${id}`);
      const target = api.users.find((u) => u.id === id);
      if (target) target.active = action === "reactivate";
      return route.fulfill({ json: { data: target ?? null } });
    }
    return route.fulfill({ status: 404, json: { error: { code: "NOT_FOUND", message: path } } });
  });

  return api;
}

test.describe("instance admin at 390 px", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("an admin sees every family and user, and creates a user into a family", async ({
    loggedInPage: page,
  }) => {
    const api = await routeAdmin(page, { admin: true });
    const writes: string[] = [];
    page.on("request", (r) => {
      if (r.method() !== "GET" && r.url().includes("/bff/admin/")) writes.push(r.url());
    });

    await page.goto("/dashboard/admin");
    await expect(page.getByTestId("admin-page")).toBeVisible();

    // Counts, then the users list: everyone, with the signed-in admin marked.
    await expect(page.getByTestId("admin-stats")).toContainText("Families");
    await expect(page.getByRole("tab", { name: /Users/ })).toBeVisible();
    // Each row is scoped to its card, so the hidden desktop table cannot match.
    const amberCard = page.getByTestId(`admin-user-card-${AMBER.id}`);
    await expect(amberCard).toContainText("Amber Sims");
    await expect(amberCard).toContainText("amber@sims.family");
    await expect(amberCard).toContainText("Sims family · Family manager");
    // A deactivated user is labelled, and offers Reactivate rather than Deactivate.
    const rickCard = page.getByTestId(`admin-user-card-${RICK.id}`);
    await expect(rickCard).toContainText("Rick Benson");
    await expect(rickCard).toContainText("Deactivated");
    await expect(rickCard.getByRole("button", { name: "Reactivate" })).toBeVisible();
    await expect(rickCard.getByRole("button", { name: "Deactivate" })).toHaveCount(0);

    // Deactivate is behind a confirmation that says what it does.
    await page
      .getByTestId(`admin-user-card-${AMBER.id}`)
      .getByRole("button", { name: "Deactivate" })
      .click();
    const confirm = page.getByRole("dialog");
    await expect(confirm).toContainText("signed out everywhere");
    await confirm.getByRole("button", { name: "Deactivate" }).click();
    await expect.poll(() => api.activated).toEqual([`deactivate:${AMBER.id}`]);

    // Add user into an existing family: the body carries household_id and role,
    // and a password long enough for the API (8–72 bytes).
    await page.getByRole("button", { name: "Add user" }).click();
    const modal = page.getByRole("dialog");
    await modal.getByLabel("Name").fill("Priya Patel");
    await modal.getByLabel("Email").fill("priya@example.com");
    const password = await modal.getByLabel("Temporary password").inputValue();
    expect(password.length).toBeGreaterThanOrEqual(8);
    await modal.getByRole("button", { name: "Add user" }).click();

    await expect.poll(() => api.created.length).toBe(1);
    expect(api.created[0]).toMatchObject({
      name: "Priya Patel",
      email: "priya@example.com",
      role: "family_member",
      household_id: SIMS.id,
    });
    expect(api.created[0].password).toBe(password);
    expect(api.created[0].household_name).toBeUndefined();
    await expect(modal).toHaveCount(0);
    await expect(
      page.getByTestId("admin-user-card-00000000-0000-4000-8000-0000000000d4"),
    ).toContainText("Priya Patel");

    await expectNoHorizontalOverflow(page);
    expect(writes.some((w) => w.includes("/admin/users"))).toBe(true);
  });

  test("creating a user into a NEW family sends household_name with role owner", async ({
    loggedInPage: page,
  }) => {
    const api = await routeAdmin(page, { admin: true });
    await page.goto("/dashboard/admin");

    await page.getByRole("button", { name: "Add user" }).click();
    const modal = page.getByRole("dialog");
    await modal.getByLabel("Name").fill("Tom Hart");
    await modal.getByLabel("Email").fill("tom.hart@icloud.com");
    await modal.getByRole("radio", { name: "New family" }).click();
    await modal.getByLabel("New family name").fill("The Harts");
    await modal.getByRole("button", { name: "Add user" }).click();

    await expect.poll(() => api.created.length).toBe(1);
    expect(api.created[0]).toMatchObject({
      name: "Tom Hart",
      email: "tom.hart@icloud.com",
      role: "owner",
      household_name: "The Harts",
    });
    expect(api.created[0].household_id).toBeUndefined();
    // No time zone entered: the field is not sent, so the API defaults to UTC.
    expect(api.created[0].timezone).toBeUndefined();
  });

  test("the Families tab lists each family with its members and time zone", async ({
    loggedInPage: page,
  }) => {
    await routeAdmin(page, { admin: true });
    await page.goto("/dashboard/admin");

    await page.getByRole("tab", { name: /Families/ }).click();
    await expect(page.getByTestId(`admin-family-${SIMS.id}`)).toContainText("Sims family");
    await expect(page.getByTestId(`admin-family-${SIMS.id}`)).toContainText("America/Chicago");
    await expect(page.getByTestId(`admin-family-${SIMS.id}`)).toContainText("2 members");
  });

  test("a non-admin gets a refusal and the page never asks the admin API", async ({
    loggedInPage: page,
  }) => {
    const api = await routeAdmin(page, { admin: false });
    const adminRequests: string[] = [];
    page.on("request", (r) => {
      if (r.url().includes("/bff/admin/")) adminRequests.push(`${r.method()} ${r.url()}`);
    });

    await page.goto("/dashboard/admin");
    await expect(page.getByTestId("admin-forbidden")).toBeVisible();
    await expect(page.getByTestId("admin-page")).toHaveCount(0);
    await expect(page.getByText("Add user")).toHaveCount(0);

    // Not one /admin request: no probe, no empty list, nothing to 403.
    expect(adminRequests).toEqual([]);
    expect(api.created).toEqual([]);
  });
});

test.describe("instance admin on a desktop", () => {
  test.use({ viewport: { width: 1280, height: 900 } });

  test("users are a table with families and status, and deactivate/reactivate work", async ({
    loggedInPage: page,
  }) => {
    const api = await routeAdmin(page, { admin: true });
    await page.goto("/dashboard/admin");

    const table = page.locator("table");
    await expect(table).toBeVisible();
    await expect(table).toContainText("amber@sims.family");
    await expect(table).toContainText("Sims family");
    await expect(table).toContainText("Family manager");

    // Reactivating the deactivated user is immediate — no confirmation needed.
    await page
      .getByTestId(`admin-user-row-${RICK.id}`)
      .getByRole("button", { name: "Reactivate" })
      .click();
    await expect.poll(() => api.activated).toEqual([`reactivate:${RICK.id}`]);

    // Your own row offers no Deactivate: the API refuses self-deactivation.
    const selfRow = page.getByTestId(`admin-user-row-${SELF.id}`);
    await expect(selfRow).toBeVisible();
    await expect(selfRow.getByRole("button", { name: "Deactivate" })).toHaveCount(0);

    await expectNoHorizontalOverflow(page);
  });
});
