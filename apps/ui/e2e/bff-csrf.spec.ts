import { randomUUID } from "node:crypto";
import { test, expect } from "./fixtures";

/**
 * CSRF guard on the BFF (#4646). The session is cookie-only, so a write that
 * did not come from the app's own origin must be refused before it reaches
 * the API, while the app's own writes keep working.
 *
 * The cross-site cases use page.request, which shares the signed-in browser
 * context's cookies, so they carry a real session: exactly what a sibling
 * host's form or fetch would send. The stub records every write the API
 * receives (apiWrites), tagged with a per-test probe, so "creates nothing" is
 * checked at the API, not just by the status code.
 */

const external = !!process.env.E2E_BASE_URL;

test("cross-site writes to /bff get 403 and create nothing", async ({ loggedInPage: page, baseURL, stubStats }) => {
  // Session is live: a same-origin read works.
  await page.goto("/dashboard");
  const probe = randomUUID();
  const url = `${baseURL}/bff/properties?probe=${probe}`;
  const body = JSON.stringify({ name: `csrf-probe-${probe}` });

  const attempts: Record<string, string>[] = [
    // The reviewer's reproduction: foreign Origin, cross-site, text/plain (no CORS preflight).
    { origin: "https://evil.example", "sec-fetch-site": "cross-site", "content-type": "text/plain" },
    // A sibling host under the same parent domain: SameSite=Lax lets the cookie through.
    { origin: "https://other.home.example", "sec-fetch-site": "same-site", "content-type": "text/plain" },
    // Neither header (not a browser): refused by default.
    { "content-type": "application/json" },
  ];
  for (const headers of attempts) {
    for (const method of ["POST", "PUT", "PATCH", "DELETE"] as const) {
      const res = await page.request.fetch(url, { method, headers, data: body, maxRedirects: 0 });
      expect(res.status(), `${method} ${JSON.stringify(headers)}`).toBe(403);
      expect((await res.json()).error.code).toBe("FORBIDDEN");
    }
  }

  if (!external) {
    const writes = (await stubStats()).apiWrites.filter((w) => w.includes(probe));
    expect(writes, "the API saw none of the cross-site writes").toEqual([]);
  }
});

test("the app's own write still goes through the BFF", async ({ loggedInPage: page, stubStats }) => {
  test.skip(external, "would create a real property; the stub records it instead");
  const name = `E2E house ${randomUUID().slice(0, 8)}`;

  await page.goto("/dashboard/properties");
  await page.getByRole("button", { name: "Add Property" }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Property Name").fill(name);

  const created = page.waitForResponse(
    (r) => new URL(r.url()).pathname === "/bff/properties" && r.request().method() === "POST",
  );
  await dialog.getByRole("button", { name: "Add Property" }).click();
  const res = await created;

  // The browser sent its own Origin / Sec-Fetch-Site and the BFF accepted them.
  expect(res.status()).toBe(201);
  const sent = await res.request().allHeaders();
  expect(sent.origin).toBe(new URL(page.url()).origin);
  await expect(dialog).toBeHidden();
  expect((await stubStats()).apiWrites).toContain(`POST /api/v1/properties ${name}`);
});
