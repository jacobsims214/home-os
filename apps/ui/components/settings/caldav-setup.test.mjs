// Run: npm test (node --test; Node >= 23.6 strips TypeScript types natively).
//
// The Phone-calendar section's setup steps (#4583). What was wrong before was
// not the URL (#5289 fixed that) but the instructions around it: five lines
// that only covered an iOS path Apple moved in iOS 18, told the person to
// "turn Reminders off" on an account that has no Reminders switch, and said
// nothing about a Mac or an Android phone while the section's own header
// promised them.
//
// What is asserted here is the part that keeps the page honest:
//   - every device the header promises has steps;
//   - no step tells the person to do something the OS does not offer;
//   - the steps never carry a hostname or a port: the address is rendered right
//     above them from the API's server_url (#5289), and a copied one here is
//     exactly how the localhost bug came back before.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import {
  CALDAV_CALENDAR_NOTE,
  CALDAV_DEVICES,
  DEFAULT_CALDAV_DEVICE,
  caldavDevice,
} from "./caldav-setup.ts";

const here = new URL(".", import.meta.url).pathname;

test("iPhone, Android and Mac: the three devices the section's header promises", () => {
  assert.deepEqual(
    CALDAV_DEVICES.map((d) => d.id),
    ["iphone", "android", "mac"],
  );
  for (const d of CALDAV_DEVICES) {
    assert.ok(d.steps.length >= 4, `${d.id} has ${d.steps.length} steps`);
    assert.ok(d.note.length > 20, `${d.id} has no note`);
    // A label shares 390 px with two others, so it is a device name only.
    assert.match(d.label, /^[A-Za-z]+$/);
  }
});

test("a fresh page opens on the phone, and an unknown id still gets steps", () => {
  assert.equal(DEFAULT_CALDAV_DEVICE, "iphone");
  assert.equal(caldavDevice(undefined).id, "iphone");
  assert.equal(caldavDevice(null).id, "iphone");
  assert.equal(caldavDevice("").id, "iphone");
  // A stale URL or a hand-edited value falls back instead of rendering blank.
  assert.equal(caldavDevice("ipad").id, "iphone");
  assert.equal(caldavDevice("android").id, "android");
});

test("the iPhone path is the one iOS 18 actually has, and the older one is named", () => {
  const [first] = caldavDevice("iphone").steps;
  // iOS 18 moved accounts under Apps. A person on a current phone who reads the
  // iOS 17 path taps into a screen that no longer matches.
  assert.match(first, /Settings → Apps → Calendar → Calendar Accounts/);
  assert.match(first, /iOS 17/);
  const all = caldavDevice("iphone").steps.join(" ");
  assert.match(all, /Add CalDAV Account/);
  // The old text told people to switch Reminders off; a CalDAV account has no
  // Reminders switch, so the instruction could never be followed.
  assert.doesNotMatch(all, /Reminders/i);
  // The scheme matters: "Cannot Connect" is what a missing https:// produces.
  assert.match(all, /https:\/\//);
});

test("Android is DAVx5, because Android has no CalDAV client of its own", () => {
  const android = caldavDevice("android");
  const all = android.steps.join(" ");
  // Saying "use your Calendar app" here sends someone to a screen with no
  // CalDAV option at all: Google Calendar does not speak CalDAV.
  assert.match(all, /DAVx5/);
  assert.match(all, /Play Store/);
  assert.match(android.app, /DAVx5/);
  assert.match(all, /Base URL/);
});

test("the Mac steps are the Calendar app's own account flow", () => {
  const all = caldavDevice("mac").steps.join(" ");
  assert.match(all, /Calendar → Settings/);
  assert.match(all, /Add CalDAV Account/);
  assert.match(all, /Manual/);
});

test("the app password is per person, not per device, and is said so", () => {
  // Someone with a phone and a laptop otherwise generates two passwords and
  // breaks the first device without knowing why.
  assert.match(caldavDevice("mac").note, /per person, not per device/);
  assert.match(caldavDevice("iphone").note, /everywhere/);
});

test("the role note explains a missing calendar before it blames the setup", () => {
  // #4807: the API hides Bills, Maintenance and Vehicles from a housesitter or
  // vendor. A page that just said "the family calendars" would have that person
  // hunting a sync failure that is a policy decision.
  assert.match(CALDAV_CALENDAR_NOTE, /role/);
  assert.match(CALDAV_CALENDAR_NOTE, /housesitter/);
  assert.match(CALDAV_CALENDAR_NOTE, /owner/);
});

test("no step names a host or a port: the address comes from the API (#5289)", () => {
  // The address is rendered above the steps from the API's server_url. A host
  // or a port typed in here is how the localhost:8081 bug shipped, and it would
  // go stale the moment a deployment moved.
  for (const d of CALDAV_DEVICES) {
    const text = [d.app, d.note, ...d.steps].join(" ");
    assert.doesNotMatch(text, /localhost/i, `${d.id} names localhost`);
    assert.doesNotMatch(text, /:\d{2,5}\b/, `${d.id} names a port`);
    // A scheme alone ("including https://") is instruction, not an address:
    // a host is a scheme followed by something other than punctuation.
    assert.doesNotMatch(text, /https?:\/\/[^.\s]/i, `${d.id} names a host`);
  }
});

test("the section renders the steps and the role note, and keeps the server from the API", () => {
  const component = readFileSync(join(here, "PhoneCalendarSection.tsx"), "utf8");
  // The steps are data, not JSX prose: this is what makes them testable, and
  // it is why the list above is the single copy of them.
  assert.match(component, /CALDAV_DEVICES/);
  assert.match(component, /caldavDevice\(/);
  assert.match(component, /CALDAV_CALENDAR_NOTE/);
  // #5289's wiring stays exactly as it was: the value is read, not hardcoded.
  assert.match(component, /caldavServerUrl\(/);
  assert.match(component, /server_url/);
  assert.doesNotMatch(component, /localhost/);
  assert.doesNotMatch(component, /8081/);
});
