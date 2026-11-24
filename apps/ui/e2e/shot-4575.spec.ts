import { createCipheriv, createHash, pbkdf2Sync, randomBytes } from "node:crypto";
import { test, seedHouseholds, SESSION_COOKIE } from "./fixtures";
import type { Page } from "@playwright/test";

/**
 * Screenshots for #4575's receipt (not an assertion spec): the credentials list
 * at phone and desktop widths, with a shared and an unshared credential, plus
 * the edit warning a family member sees on a shared one. Run with
 * SHOT_DIR set; Playwright's own projects supply the viewport.
 */
test.skip(!!process.env.E2E_BASE_URL, "needs the stub API");

const HOUSEHOLD = "0b9d4f1e-6a3c-4b8e-9f52-1c7d2a5e8b10";
const stubUrl = `http://127.0.0.1:${process.env.E2E_STUB_PORT || 3901}`;
const PASSWORD = "e2e credentials 4575";
const OUT = process.env.SHOT_DIR || "/tmp/shots";

async function sessionId(page: Page): Promise<string> {
  const token = (await page.context().cookies()).find((c) => c.name === SESSION_COOKIE)?.value ?? "";
  return JSON.parse(Buffer.from(token.split(".")[1], "base64url").toString("utf8")).sid;
}

/**
 * A real ciphertext for the stub, so the viewer and editor work: the same
 * PBKDF2-SHA256 32-byte key the browser derives, AES-256-GCM with a 12-byte
 * IV, base64 both. Without this the blob is empty, decryption fails, and the
 * Edit button never appears.
 */
function seal(password: string, saltB64: string, payload: Record<string, string>) {
  const key = pbkdf2Sync(password, Buffer.from(saltB64, "base64"), 100_000, 32, "sha256");
  const iv = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", key, iv);
  const body = Buffer.concat([cipher.update(JSON.stringify(payload), "utf8"), cipher.final(), cipher.getAuthTag()]);
  return { encrypted_data: body.toString("base64"), iv: iv.toString("base64") };
}

test("shots: credentials list", async ({ loggedInPage: page, isMobile, request }) => {
  const sid = (await sessionId(page))!;
  await seedHouseholds(request, sid, [
    { id: HOUSEHOLD, name: "Sims family", role: (process.env.SHOT_ROLE as "owner") || "owner", timezone: "America/Chicago" },
  ]);
  const salt = Buffer.from("credentials-4575-salt").toString("base64");
  const login = { username: "jacob@sims.family", password: "correct-horse-battery-staple", url: "netflix.com" };
  const wifi = { key_name: "GUEST_WIFI", key_value: "welcome-to-the-sims-house", url: "https://192.168.1.1" };
  await page.request.post(`${stubUrl}/e2e/secrets/records`, {
    data: {
      sid,
      records: [
        { id: "s-shared", entity_type: "property", entity_id: "p1", name: "Netflix", secret_type: "login", shared_with_agents: true, agent_shared_at: "2026-11-14T10:00:00Z", ...seal(PASSWORD, salt, login) },
        { id: "s-private", entity_type: "property", entity_id: "p1", name: "Family Gmail", secret_type: "login", shared_with_agents: false, ...seal(PASSWORD, salt, { ...login, username: "simsfamilyhub@gmail.com" }) },
        { id: "s-wifi", entity_type: "property", entity_id: "p1", name: "Guest Wi-Fi", secret_type: "api_key", shared_with_agents: true, agent_shared_at: "2026-11-02T10:00:00Z", ...seal(PASSWORD, salt, wifi) },
      ],
    },
  });
  const raw = pbkdf2Sync(PASSWORD, Buffer.from(salt, "base64"), 100_000, 32, "sha256");
  await page.request.post(`${stubUrl}/e2e/secrets/seed`, {
    data: { sid, key_salt: salt, key_hash: createHash("sha256").update(raw).digest("base64") },
  });

  await page.goto("/dashboard/properties/p1");
  if (isMobile) {
    await page.getByRole("main").getByRole("button", { name: /^Resources/ }).click();
  }
  await page.getByRole("tab", { name: "Secrets" }).click();
  await page.getByRole("button", { name: "Unlock Secrets" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Master Password").fill(PASSWORD);
  await dialog.getByRole("button", { name: "Unlock", exact: true }).click();
  await page.getByTestId("secrets-cards").waitFor();
  await page.waitForTimeout(400);

  await page.screenshot({ path: `${OUT}/list-${isMobile ? "phone" : "desktop"}.png`, fullPage: true });

  // The shared edit warning a family member sees (mockup #103's "Member edits
  // shared"): open the shared credential, Edit, and capture the form.
  await page.getByRole("button", { name: /Netflix/ }).first().click();
  await page.getByRole("button", { name: "Edit" }).click();
  await page.waitForTimeout(300);
  await page.screenshot({ path: `${OUT}/edit-${isMobile ? "phone" : "desktop"}.png` });
});
