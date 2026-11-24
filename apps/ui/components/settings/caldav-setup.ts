/**
 * The device-by-device setup steps the Phone-calendar section shows (#4583).
 *
 * The section used to carry five bullet lines that only covered an old iPhone
 * path ("Settings → Calendar → Accounts"), told the person to "turn Reminders
 * off" (a CalDAV account has no Reminders switch) and said nothing about a Mac
 * or an Android phone, while its own header promised "iPhone, iPad, Mac and any
 * CalDAV client". These are the steps, per device, written for someone holding
 * the phone they are setting up: the server address and user name they need are
 * rendered right above the steps, so the text says "the address above" rather
 * than repeating a hostname (which is what went wrong in #5289).
 *
 * iOS moved its account screens in 18: Settings → Apps → Calendar →
 * Calendar Accounts (iOS 17 and earlier: Settings → Calendar → Accounts), and
 * that moved path is the one people with a current phone see, so it is step 1
 * and the old path is named beside it.
 *
 * Android has no CalDAV client of its own: Google Calendar does not speak
 * CalDAV, so the phone needs DAVx5, which syncs a CalDAV account into the
 * Android calendar provider. Saying "use your Calendar app" there would send
 * someone to a screen with no CalDAV option at all.
 *
 * Plain data and pure functions (no JSX, no browser-only API at module load),
 * so `npm test` (node --test) covers the list in caldav-setup.test.mjs.
 */

export type CaldavDeviceId = "iphone" | "android" | "mac";

export interface CaldavDevice {
  id: CaldavDeviceId;
  /** The segmented control's label. Short: it shares 390 px with two others. */
  label: string;
  /** The app to open first, for the card's subtitle. */
  app: string;
  steps: string[];
  /** One line under the steps: the thing that goes wrong on this device. */
  note: string;
}

export const CALDAV_DEVICES: readonly CaldavDevice[] = [
  {
    id: "iphone",
    label: "iPhone",
    app: "Apple Calendar",
    steps: [
      "Open Settings → Apps → Calendar → Calendar Accounts (on iOS 17 or older: Settings → Calendar → Accounts).",
      "Tap Add Account → Other → Add CalDAV Account.",
      "Tap Server and paste the server address above, exactly as it is written, including https://. A missing https:// is the usual reason Add Account answers \u201cCannot Connect\u201d.",
      "Tap User Name and paste the email above, then paste the app password.",
      "Tap Next, leave the calendars you want switched on (switch off any you don't), then tap Save.",
    ],
    note: "If the account saves but no events appear, pull down on the calendar list to refresh. Generating a new app password makes the old one stop working everywhere, so generate one before adding a device, not after.",
  },
  {
    id: "android",
    label: "Android",
    app: "DAVx5 + your calendar app",
    steps: [
      "Android has no built-in CalDAV support, so install DAVx5 from the Play Store or F-Droid first.",
      "Open DAVx5 and tap + (Add account) → Login with URL and user name.",
      "Base URL: paste the server address above. User name and password: paste the email and app password from above.",
      "Tap Add account and let DAVx5 list the calendars, then let it create them in the Android calendar.",
      "Open your calendar app (Google Calendar, Samsung Calendar, …) and switch on the calendars you want to see.",
    ],
    note: "DAVx5 keeps syncing in the background; if a calendar stops updating, open DAVx5 and check the account says \u201cLogin successful\u201d. Changing the app password here means editing the account in DAVx5 too.",
  },
  {
    id: "mac",
    label: "Mac",
    app: "Apple Calendar",
    steps: [
      "Open the Calendar app, then Calendar → Settings (⌘,) → Accounts.",
      "Tap + → Other → Add CalDAV Account.",
      "Account type: Manual. Server: paste the server address above. User name: paste the email above. Password: paste the app password.",
      "Tap Sign In, choose which of the calendars to show, then tap Done.",
    ],
    note: "The Mac and the iPhone can use the same app password: an app password is per person, not per device, and generating a new one replaces it on every device at once.",
  },
] as const;

/** The device a fresh page shows first: the phone in most people's hand. */
export const DEFAULT_CALDAV_DEVICE: CaldavDeviceId = "iphone";

/** One device's steps; an unknown id falls back to the default, never blank. */
export function caldavDevice(id: string | null | undefined): CaldavDevice {
  return (
    CALDAV_DEVICES.find((d) => d.id === id) ??
    CALDAV_DEVICES.find((d) => d.id === DEFAULT_CALDAV_DEVICE)!
  );
}

/**
 * The line about which calendars end up on the phone. The set is the person's
 * role in the family, decided by the API (#4807: a housesitter or vendor loses
 * Bills, Maintenance and Vehicles), so the UI says the rule once and points at
 * the role rather than repeating a table that would drift.
 */
export const CALDAV_CALENDAR_NOTE =
  "Your phone shows the calendars you can see in Home OS, and your role in the family decides which those are — a housesitter or a vendor sees fewer than a family member. If a calendar is missing on the phone, check it is ticked in the account's settings first, then ask an owner about your role.";
