import { test, expect } from "./fixtures";

/**
 * Middleware login bounce behind a proxy (#4621).
 *
 * Envoy forwards the public Host / X-Forwarded-Host, but in the standalone
 * server request.url carries the bind address (HOSTNAME:PORT, the pod name in
 * Kubernetes). The guard must build Location from the public host (PUBLIC_URL,
 * else X-Forwarded-Host / Host), never from request.url. Run with E2E_UI=standalone to exercise the build
 * where the bug showed up.
 *
 * Plain HTTP checks, so one project is enough.
 */
test.beforeEach(({}, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chrome", "HTTP-only check; runs once");
});

// Against an external stack the real proxy sets these; overriding Host there
// would misroute the request.
const proxied: Record<string, string> = process.env.E2E_BASE_URL
  ? {}
  : { Host: "home.example", "X-Forwarded-Host": "home.example", "X-Forwarded-Proto": "https" };

test("signed out behind a proxy: /login redirect uses the public host and keeps the return path", async ({ request }) => {
  const res = await request.get("/dashboard/bills?month=2026-11", { headers: proxied, maxRedirects: 0 });
  expect(res.status()).toBe(307);
  const location = res.headers()["location"];
  const want = "/login?redirect=%2Fdashboard%2Fbills%3Fmonth%3D2026-11";
  if (process.env.E2E_BASE_URL) expect(location).toBe(new URL(want, process.env.E2E_BASE_URL).toString());
  else expect(location).toBe(`https://home.example${want}`);
});

test("signed out: a // path cannot send the browser to another host", async ({ request, baseURL }) => {
  // Absolute URL: a bare "//evil.example/x" would resolve to another host client-side.
  const base = baseURL!.replace(/\/$/, "");
  const res = await request.get(`${base}//evil.example/x`, { headers: proxied, maxRedirects: 0 });
  // Next itself may first 308 the duplicate slash away; the middleware answers 307.
  expect([307, 308]).toContain(res.status());
  const publicBase = process.env.E2E_BASE_URL ? base : "https://home.example";
  const target = new URL(res.headers()["location"] ?? "", publicBase);
  expect(target.host).not.toBe("evil.example");
  const redirect = target.searchParams.get("redirect");
  if (redirect !== null) {
    expect(redirect).toMatch(/^\/(?![/\\])/);
    expect(new URL(redirect, publicBase).host).toBe(new URL(publicBase).host);
  }
});
