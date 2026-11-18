// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
//
// The Phone-calendar page's Server field (#5289). It rendered the module
// constant "http://localhost:8081" — a docker-compose port — on the deployed
// UI, so a real household typed an address their phone cannot reach. The value
// now comes from the API's `server_url` (POST /api/v1/auth/caldav-password,
// built from the API's PUBLIC_URL), with this page's own origin + /dav/ while
// there is no response yet.
//
// The rule under test is the anti-regression one: nothing this module can
// return is ever localhost on a deployment, and the component holds no
// address at all.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { CALDAV_PATH, caldavServerUrl } from "./caldav-server.ts";

const DEPLOYED = "https://homeos.simstech.cloud";
const here = new URL(".", import.meta.url).pathname;

test("the API's server_url is what the page shows", () => {
  assert.equal(caldavServerUrl(`${DEPLOYED}/dav/`, DEPLOYED), `${DEPLOYED}/dav/`);
  // Verbatim, not re-derived: an operator who points the API somewhere else
  // (a different public hostname, a path prefix) gets that value on the page.
  assert.equal(caldavServerUrl("https://cal.example.net/dav/", DEPLOYED), "https://cal.example.net/dav/");
  assert.equal(caldavServerUrl(`${DEPLOYED}/dav/?t=1`, DEPLOYED), `${DEPLOYED}/dav/?t=1`);
});

test("whitespace from the API is not a server address", () => {
  assert.equal(caldavServerUrl("   ", DEPLOYED), `${DEPLOYED}/dav/`);
  assert.equal(caldavServerUrl("", DEPLOYED), `${DEPLOYED}/dav/`);
});

test("before the API answers, the page shows its own origin + /dav/ — never a port", () => {
  // This is the pre-fix failure: on the deployed origin the field said
  // http://localhost:8081.
  assert.equal(caldavServerUrl(undefined, DEPLOYED), `${DEPLOYED}${CALDAV_PATH}`);
  assert.equal(caldavServerUrl(null, DEPLOYED), `${DEPLOYED}${CALDAV_PATH}`);
  // An older API that predates server_url (or a stripped field) lands here.
  assert.equal(caldavServerUrl(undefined, "http://localhost:3000"), `http://localhost:3000${CALDAV_PATH}`);
  // A LAN address with a port of its own: the origin keeps ITS port, it never
  // becomes 8081.
  assert.equal(caldavServerUrl(undefined, "http://192.168.1.20:8443"), "http://192.168.1.20:8443/dav/");
});

test("no API value and no origin (server render) shows nothing, not localhost", () => {
  // The old fallback was the localhost literal; it reached the built HTML
  // through the server render of this client component. An empty value with a
  // placeholder is honest; a wrong address is not.
  assert.equal(caldavServerUrl(undefined, undefined), "");
  assert.equal(caldavServerUrl(null, null), "");
  assert.equal(caldavServerUrl("  ", ""), "");
  // An origin a browser can parse but no /dav/ hangs off.
  assert.equal(caldavServerUrl(undefined, "file:///Users/me/index.html"), "");
  assert.equal(caldavServerUrl(undefined, "not a url"), "");
});

test("nothing this module can return is ever a localhost:8081 address", () => {
  const answers = [
    caldavServerUrl(undefined, undefined),
    caldavServerUrl(null, DEPLOYED),
    caldavServerUrl(`${DEPLOYED}/dav/`, DEPLOYED),
    caldavServerUrl(undefined, "http://localhost:3000"),
  ];
  for (const a of answers) assert.doesNotMatch(a, /:8081/, `returned ${a}`);
});

test("the component holds no address literal at all", () => {
  const component = readFileSync(join(here, "PhoneCalendarSection.tsx"), "utf8");
  // The only way this bug comes back is someone re-adding a host or port here.
  assert.doesNotMatch(component, /localhost/, "PhoneCalendarSection.tsx names localhost");
  assert.doesNotMatch(component, /8081/, "PhoneCalendarSection.tsx hardcodes a port");
  // And it must still read the value from the API response, not a constant.
  assert.match(component, /caldavServerUrl\(/);
  assert.match(component, /server_url/);
});

test("the fallback literal is gone: no module constant for the server", () => {
  const src = readFileSync(join(here, "caldav-server.ts"), "utf8");
  assert.doesNotMatch(src, /localhost/, "caldav-server.ts still names localhost");
  assert.doesNotMatch(src, /8081/, "caldav-server.ts still names the dev port");
  // The path is the one thing that IS a constant, because the API uses it too.
  assert.equal(CALDAV_PATH, "/dav/");
});
