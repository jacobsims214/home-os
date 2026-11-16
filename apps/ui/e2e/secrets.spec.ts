import { createHash, pbkdf2Sync } from "node:crypto";
import type { Page, Request } from "@playwright/test";
import { test, expect, SESSION_COOKIE } from "./fixtures";

/**
 * Secrets master key: setup -> lock -> unlock against the stub API, through
 * the real BFF (#4445). The UI sends `verifier` (never `key_hash`, the key or
 * the password), probes "set up?" with GET /secrets/key, and a wrong master
 * password stays on the page instead of being treated as a lost session.
 *
 * The stub keeps one vault per sign-in session (sid claim), so these tests
 * run in parallel without sharing a master key.
 */

test.skip(!!process.env.E2E_BASE_URL, "needs the stub API's per-session vaults");

const stubUrl = `http://127.0.0.1:${process.env.E2E_STUB_PORT || 3901}`;
const PASSWORD = "e2e master password 4445";

type VaultState = { set_up: boolean; legacy: boolean; upgraded: boolean; requests: { endpoint: string; fields: string[] }[] };

/** The stub session id from the signed-in context's id token. */
async function sessionId(page: Page): Promise<string> {
  const token = (await page.context().cookies()).find((c) => c.name === SESSION_COOKIE)?.value ?? "";
  return JSON.parse(Buffer.from(token.split(".")[1], "base64url").toString("utf8")).sid;
}

async function vault(page: Page): Promise<VaultState> {
  const res = await page.request.get(`${stubUrl}/e2e/secrets?sid=${await sessionId(page)}`);
  return (await res.json()) as VaultState;
}

const isSecrets = (path: string, method: string) => (r: Request) =>
  new URL(r.url()).pathname === `/bff/secrets/${path}` && r.method() === method;

/** Nothing secret ended up in web storage. */
async function expectNothingStored(page: Page, ...secrets: string[]) {
  const stored = await page.evaluate(() => {
    const dump = (s: Storage) => Object.keys(s).map((k) => `${k}=${s.getItem(k)}`).join("\n");
    return `${dump(localStorage)}\n${dump(sessionStorage)}`;
  });
  for (const s of secrets) expect(stored).not.toContain(s);
}

test("settings: set up, lock, wrong password stays put, unlock", async ({ loggedInPage: page }) => {
  const probes: string[] = [];
  page.on("request", (r) => {
    if (isSecrets("verify", "POST")(r) || isSecrets("key", "GET")(r)) probes.push(`${r.method()} ${new URL(r.url()).pathname}`);
  });
  await page.goto("/dashboard/settings");
  await expect(page.getByText("Not Set Up", { exact: true })).toBeVisible();
  // The setup probe is GET /secrets/key, never a verifier-less POST /verify.
  expect(probes).toContain("GET /bff/secrets/key");
  expect(probes).not.toContain("POST /bff/secrets/verify");

  // Setup
  await page.getByRole("button", { name: "Set Up Master Password" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Master Password").fill(PASSWORD);
  await dialog.getByLabel("Confirm Password").fill(PASSWORD);
  const setupReq = page.waitForRequest(isSecrets("setup", "POST"));
  await dialog.getByRole("button", { name: "Set Up", exact: true }).click();
  const setupBody = (await setupReq).postDataJSON();
  expect(Object.keys(setupBody).sort()).toEqual(["key_salt", "key_version", "verifier"]);
  expect(Buffer.from(setupBody.verifier, "base64")).toHaveLength(32);
  expect(JSON.stringify(setupBody)).not.toContain(PASSWORD);
  await expect(page.getByText("Unlocked", { exact: true })).toBeVisible();

  // Lock
  await page.getByRole("button", { name: "Lock Secrets" }).click();
  await expect(page.getByText("Locked", { exact: true })).toBeVisible();

  // Wrong password: an error in the modal, and still signed in on this page.
  await page.getByRole("button", { name: "Unlock Secrets" }).click();
  await dialog.getByLabel("Master Password").fill("not the password");
  await dialog.getByRole("button", { name: "Unlock", exact: true }).click();
  await expect(dialog.getByText("Incorrect master password.")).toBeVisible();
  expect(new URL(page.url()).pathname).toBe("/dashboard/settings");

  // Right password
  await dialog.getByLabel("Master Password").fill(PASSWORD);
  const verifyReq = page.waitForRequest(isSecrets("verify", "POST"));
  await dialog.getByRole("button", { name: "Unlock", exact: true }).click();
  const verifyBody = (await verifyReq).postDataJSON();
  expect(verifyBody).toEqual({ verifier: setupBody.verifier, key_version: 1 });
  await expect(page.getByText("Unlocked", { exact: true })).toBeVisible();

  const state = await vault(page);
  expect(state.requests.map((r) => r.fields.join(","))).not.toContain(expect.stringContaining("key_hash"));
  await expectNothingStored(page, PASSWORD, setupBody.verifier);
});

test("a household set up before migration 051 unlocks with the same password and is upgraded", async ({ loggedInPage: page }) => {
  // Pre-051 the API stored key_hash = base64(SHA-256(PBKDF2-SHA256(password,
  // salt, 100k))). Seed exactly that, computed outside the UI.
  const salt = Buffer.from("legacy-salt-4445").toString("base64");
  const raw = pbkdf2Sync(PASSWORD, Buffer.from(salt, "base64"), 100_000, 32, "sha256");
  const keyHash = createHash("sha256").update(raw).digest("base64");
  await page.goto("/dashboard/settings");
  const seed = await page.request.post(`${stubUrl}/e2e/secrets/seed`, {
    data: { sid: await sessionId(page), key_salt: salt, key_hash: keyHash },
  });
  expect(seed.ok()).toBeTruthy();
  await page.reload();
  await expect(page.getByText("Locked", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Unlock Secrets" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Master Password").fill(PASSWORD);
  await dialog.getByRole("button", { name: "Unlock", exact: true }).click();
  await expect(page.getByText("Unlocked", { exact: true })).toBeVisible();

  expect(await vault(page)).toMatchObject({ set_up: true, legacy: false, upgraded: true });
});

// On phones the resource tabs sit in the collapsed "Resources" section of the
// stacked detail layout (#4531); expand it first.
async function openSecretsTab(page: Page, isMobile: boolean) {
  if (isMobile) {
    const resources = page.getByRole("main").getByRole("button", { name: /^Resources/ });
    await expect(resources).toHaveAttribute("aria-expanded", "false");
    await resources.click();
  }
  await page.getByRole("tab", { name: "Secrets" }).click();
}

test("entity Secrets tab: probe offers setup, then unlock after a reload", async ({ loggedInPage: page, isMobile }) => {
  await page.goto("/dashboard/properties/p1");
  await openSecretsTab(page, isMobile);

  const probe = page.waitForRequest(isSecrets("key", "GET"));
  await page.getByRole("button", { name: "Unlock Secrets" }).click();
  await probe;
  const dialog = page.getByRole("dialog", { name: "Set up your master password" });
  await dialog.getByLabel("Master password").fill(PASSWORD);
  await dialog.getByLabel("Confirm password").fill(PASSWORD);
  await dialog.getByRole("button", { name: "Set Up", exact: true }).click();
  await expect(page.getByText("No secrets added.")).toBeVisible();

  // The key lives only in memory: a reload locks the vault again.
  await page.reload();
  await openSecretsTab(page, isMobile);
  await page.getByRole("button", { name: "Unlock Secrets" }).click();
  const unlock = page.getByRole("dialog", { name: "Enter your master password" });
  await unlock.getByLabel("Master password").fill(PASSWORD);
  await unlock.getByRole("button", { name: "Unlock", exact: true }).click();
  await expect(page.getByText("No secrets added.")).toBeVisible();
  await expectNothingStored(page, PASSWORD);
});
