import { defineConfig, devices } from "@playwright/test";

/**
 * Playwright E2E harness (#4510). Run with `npm run test:e2e`.
 *
 * Default mode (local, no docker): Playwright starts
 *   1. e2e/stub-server.mjs, a stand-in for Dex /token and the Go API, and
 *   2. the UI (`next dev`, or the standalone build with E2E_UI=standalone),
 * with API_INTERNAL_URL and DEX_TOKEN_URL pointed at the stub. Pages,
 * middleware and the BFF are the real code; only the backends are stubbed.
 *
 * External mode: set E2E_BASE_URL (for example https://localhost:8443 for
 * docker compose once it has Dex, #4619) plus E2E_USER and E2E_PASSWORD. No
 * servers are started, and the login fixture signs in through the Dex form.
 *
 * Env:
 *   E2E_BASE_URL   target an already-running stack (disables the stub)
 *   E2E_UI         "dev" (default) | "standalone": serves the production build;
 *                  run `npm run build` right before (next dev overwrites .next)
 *   E2E_UI_PORT    default 3900
 *   E2E_STUB_PORT  default 3901
 */

const external = process.env.E2E_BASE_URL;
const uiPort = Number(process.env.E2E_UI_PORT || 3900);
const stubPort = Number(process.env.E2E_STUB_PORT || 3901);
const stubUrl = `http://127.0.0.1:${stubPort}`;
const baseURL = external || `http://localhost:${uiPort}`;

const uiEnv = {
  API_INTERNAL_URL: stubUrl,
  DEX_TOKEN_URL: `${stubUrl}/dex/token`,
  PORT: String(uiPort),
  HOSTNAME: "localhost",
  NEXT_TELEMETRY_DISABLED: "1",
  // The React Query devtools button covers the phone tab bar's More tab.
  NEXT_PUBLIC_QUERY_DEVTOOLS: "0",
};

// The standalone server needs the static assets copied next to it
// (see KB architecture/bff-proxy.md, "Local testing").
const uiCommand =
  process.env.E2E_UI === "standalone"
    ? "cp -R .next/static .next/standalone/.next/ && cp -R public .next/standalone/ && node .next/standalone/server.js"
    : `next dev -p ${uiPort} -H localhost`;

// Phone sizes we care about. Viewports are pinned explicitly (the device
// registry's iPhone viewports subtract browser chrome); UA, touch, DPR and
// isMobile come from the descriptor.
const phones = [
  { name: "iphone-15", use: { ...devices["iPhone 15"] } },
  { name: "iphone-375", use: { ...devices["iPhone 12 Mini"], viewport: { width: 375, height: 812 } } },
  { name: "iphone-390", use: { ...devices["iPhone 14"], viewport: { width: 390, height: 844 } } },
  { name: "pixel-7", use: { ...devices["Pixel 7"] } },
];

export default defineConfig({
  testDir: "./e2e",
  outputDir: "./e2e/.results",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  // next dev compiles each route on first hit; keep the local default modest.
  workers: process.env.CI ? 2 : 3,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  reporter: process.env.CI ? [["list"], ["html", { open: "never", outputFolder: "e2e/.report" }]] : "list",
  use: {
    baseURL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    ignoreHTTPSErrors: !!external,
    // The production build registers next-pwa's service worker, which would
    // serve cached pages and bypass page.route(). Tests run without it.
    serviceWorkers: "block",
  },
  projects: [...phones, { name: "desktop-chrome", use: { ...devices["Desktop Chrome"] } }],
  webServer: external
    ? undefined
    : [
        {
          command: "node e2e/stub-server.mjs",
          url: `${stubUrl}/healthz`,
          env: { E2E_STUB_PORT: String(stubPort) },
          reuseExistingServer: !process.env.CI,
          timeout: 15_000,
        },
        {
          command: uiCommand,
          url: `${baseURL}/login`,
          env: uiEnv,
          reuseExistingServer: !process.env.CI,
          timeout: 180_000,
        },
      ],
});
