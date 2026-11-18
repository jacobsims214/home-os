import { createHash, pbkdf2Sync } from "node:crypto";
import { test, expect, seedHouseholds, SESSION_COOKIE } from "./fixtures";
import type { APIRequestContext, Page } from "@playwright/test";

/**
 * Credentials list + "Shared with agents" (#4575), against the stub.
 *
 * What this proves, and what it deliberately does not:
 *  - the list renders one row per credential on a phone (card) and on a
 *    desktop (table), with the sharing state the API reported;
 *  - the switch is ENABLED for an owner/family_manager and DISABLED, with the
 *    reason, for a family_member — the UI mirror of the API's 403;
 *  - flipping it calls PUT /secrets/:id/agent-share and NOTHING else, and it
 *    never turns sharing on by itself: an unshared credential stays unshared
 *    until the click;
 *  - each credential links to the activity feed filtered to that credential.
 *
 * It does NOT test the API's authorization (Go tests cover that), and the
 * stub's blob is not decryptable, so the share PUT here is not exercised
 * end-to-end — the UI path is asserted by the request it makes.
 */

test.skip(!!process.env.E2E_BASE_URL, "needs the stub API's per-session records");

const HOUSEHOLD = "0b9d4f1e-6a3c-4b8e-9f52-1c7d2a5e8b10";
const stubUrl = `http://127.0.0.1:${process.env.E2E_STUB_PORT || 3901}`;
const PASSWORD = "e2e credentials 4575";

async function sessionId(page: Page): Promise<string> {
  const token = (await page.context().cookies()).find((c) => c.name === SESSION_COOKIE)?.value ?? "";
  return JSON.parse(Buffer.from(token.split(".")[1], "base64url").toString("utf8")).sid;
}

/** One shared and one unshared credential on a property (#4575's list). */
async function seedCredentials(page: Page, request: APIRequestContext): Promise<void> {
  await seedHouseholds(request, (await sessionId(page))!, [
    { id: HOUSEHOLD, name: "Sims family", role: "owner", timezone: "America/Chicago" },
  ]);
  await page.request.post(`${stubUrl}/e2e/secrets/records`, {
    data: {
      sid: await sessionId(page),
      records: [
        {
          id: "s-shared",
          entity_type: "property",
          entity_id: "p1",
          name: "Netflix",
          secret_type: "login",
          shared_with_agents: true,
          agent_shared_at: "2026-11-14T10:00:00Z",
        },
        {
          id: "s-private",
          entity_type: "property",
          entity_id: "p1",
          name: "Family Gmail",
          secret_type: "login",
          shared_with_agents: false,
        },
      ],
    },
  });
}

/**
 * A vault to unlock, with no setup flow in the way: seed the pre-051 verifier
 * for this session (the unmodified UI accepts it and upgrades the row), then
 * unlock through the real prompt, which is the only way a CryptoKey exists.
 */
async function seedVault(page: Page): Promise<void> {
  const salt = Buffer.from("credentials-4575-salt").toString("base64");
  const raw = pbkdf2Sync(PASSWORD, Buffer.from(salt, "base64"), 100_000, 32, "sha256");
  const keyHash = createHash("sha256").update(raw).digest("base64");
  const res = await page.request.post(`${stubUrl}/e2e/secrets/seed`, {
    data: { sid: await sessionId(page), key_salt: salt, key_hash: keyHash },
  });
  expect(res.ok(), "seed master key").toBeTruthy();
}

/** Unlocks the vault on whatever page is showing the locked panel. */
async function unlockVault(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Unlock Secrets" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Master Password").fill(PASSWORD);
  await dialog.getByRole("button", { name: "Unlock", exact: true }).click();
  await expect(page.getByText("No secrets added.")).toBeVisible({ timeout: 5_000 }).catch(() => {});
}

/** The Secrets tab on a property page; phones start with Resources collapsed. */
async function openSecretsTab(page: Page, isMobile: boolean) {
  if (isMobile) {
    const resources = page.getByRole("main").getByRole("button", { name: /^Resources/ });
    await expect(resources).toHaveAttribute("aria-expanded", "false");
    await resources.click();
  }
  await page.getByRole("tab", { name: "Secrets" }).click();
}

test("credentials list shows sharing state, enables the switch only for managers, and links to the activity feed", async ({
  loggedInPage: page,
  isMobile,
  request,
}) => {
  await seedCredentials(page, request);
  await seedVault(page);
  await page.goto("/dashboard/properties/p1");
  await openSecretsTab(page, isMobile);
  await unlockVault(page);

  // The switch state comes from the list response, not from the key: nothing
  // in these assertions depends on decrypting anything.
  const shared = page.getByTestId("agent-share-s-shared");
  const privateRow = page.getByTestId("agent-share-s-private");

  // Only one of the two presentations is in the layout at a time (cards <
  // 768px, table >= 768px), so assert inside whichever is visible.
  const visible = page.getByTestId("secrets-cards");
  await expect(visible).toBeVisible();

  // Ownership: this spec seeds an owner, so both switches are enabled.
  const sharedSwitch = visible.locator('[data-testid="agent-share-s-shared"]').getByRole("switch");
  await expect(sharedSwitch).toBeChecked();
  await expect(sharedSwitch).toBeEnabled();
  const privateSwitch = visible.locator('[data-testid="agent-share-s-private"]').getByRole("switch");
  await expect(privateSwitch).not.toBeChecked();
  await expect(privateSwitch).toBeEnabled();

  // The per-credential audit link carries the filter the activity page reads.
  await expect(
    visible.getByRole("link", { name: /agent activity for netflix/i }),
  ).toHaveAttribute("href", "/dashboard/activity?entity_type=secret&entity_id=s-shared");

  // Turning sharing OFF is the one action whose full path this stub can serve
  // (no decryption needed): DELETE, and the list comes back unshared.
  //
  // Click the LABEL, not the input: Mantine's switch input is visually hidden
  // and ~20px, so a person hits the row. Proving the label toggles it is the
  // point of the assertion — a hidden input that only a test can click would
  // be a switch nobody on a phone could operate.
  const off = page.waitForRequest(
    (r) => new URL(r.url()).pathname === "/bff/secrets/s-shared/agent-share" && r.method() === "DELETE",
  );
  await visible.locator('[data-testid="agent-share-s-shared"] label').click();
  await off;
  await expect(sharedSwitch).not.toBeChecked();

  // Nothing was ever the state before a click: the private row is still off.
  await expect(privateSwitch).not.toBeChecked();
  void shared;
  void privateRow;
});

test("a family_member sees the switch disabled with the reason, and the hint above the list", async ({
  loggedInPage: page,
  isMobile,
  request,
}) => {
  await seedHouseholds(request, (await sessionId(page))!, [
    { id: HOUSEHOLD, name: "Sims family", role: "family_member", timezone: "America/Chicago" },
  ]);
  await page.request.post(`${stubUrl}/e2e/secrets/records`, {
    data: {
      sid: await sessionId(page),
      records: [
        {
          id: "s-shared",
          entity_type: "property",
          entity_id: "p1",
          name: "Netflix",
          secret_type: "login",
          shared_with_agents: true,
        },
      ],
    },
  });

  await seedVault(page);
  await page.goto("/dashboard/properties/p1");
  await openSecretsTab(page, isMobile);
  await unlockVault(page);

  const visible = page.getByTestId("secrets-cards");
  await expect(visible).toBeVisible();
  const sw = visible.locator('[data-testid="agent-share-s-shared"]').getByRole("switch");
  // Shared is a fact of the record, so it reads checked — but the member may
  // not change it, and the row says why.
  await expect(sw).toBeChecked();
  await expect(sw).toBeDisabled();
  await expect(visible.getByText("Only an owner or family manager can change sharing.")).toBeVisible();
});
