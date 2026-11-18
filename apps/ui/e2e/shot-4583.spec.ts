import { test } from "./fixtures";

/**
 * Screenshots for #4583's receipt (not an assertion spec): the Phone-calendar
 * section at phone and desktop width, with the app password generated and the
 * iPhone, Android and Mac step blocks. Run with SHOT_DIR set; Playwright's own
 * projects supply the viewport.
 */
test.skip(!!process.env.E2E_BASE_URL, "needs the stub API");

const OUT = process.env.SHOT_DIR || "/tmp/shots";
/** Per-project names: both projects run in parallel and would overwrite one file. */
const TAG = process.env.SHOT_TAG || "phone";

test("shots: phone-calendar setup", async ({ loggedInPage: page }) => {
  await page.goto("/dashboard/settings/phone-calendar");
  await page.getByRole("button", { name: "Generate app password" }).click();
  await page.getByTestId("caldav-steps-iphone").waitFor();
  await page.screenshot({ path: `${OUT}/${TAG}-iphone.png`, fullPage: true });

  const picker = page.getByRole("radiogroup", { name: "Which device are you setting up?" });
  await picker.getByText("Android", { exact: true }).click();
  await page.getByTestId("caldav-steps-android").waitFor();
  await page.screenshot({ path: `${OUT}/${TAG}-android.png`, fullPage: true });

  await picker.getByText("Mac", { exact: true }).click();
  await page.getByTestId("caldav-steps-mac").waitFor();
  await page.screenshot({ path: `${OUT}/${TAG}-mac.png`, fullPage: true });
});
