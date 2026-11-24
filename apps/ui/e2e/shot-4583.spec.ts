import { test } from "./fixtures";

/**
 * Screenshots for #4583's receipt (not an assertion spec): the Phone-calendar
 * section at phone and desktop width, with the app password generated and the
 * iPhone, Android and Mac step blocks. Run with SHOT_DIR set; Playwright's own
 * projects supply the viewport. SHOT_TAG keeps the two projects apart.
 *
 * On a phone the shot is the viewport, not the whole page: the tab bar is
 * fixed, so a full-page capture paints it across the middle of the image.
 */
const OUT = process.env.SHOT_DIR || "/tmp/shots";
const TAG = process.env.SHOT_TAG || "phone";

test("shots: phone-calendar setup", async ({ loggedInPage: page }) => {
  await page.goto("/dashboard/settings/phone-calendar");
  await page.getByRole("button", { name: "Generate app password" }).click();

  // The password is in the field before anything is captured: the button shows
  // a loader while the request is in flight, and a shot must show the settled
  // screen, not a spinner.
  const passwordField = page.locator('[data-settings-readonly="caldav-password"]');
  await passwordField.waitFor();
  await page.waitForFunction(
    (el) => (el as HTMLInputElement).value.length > 0,
    await passwordField.elementHandle(),
  );
  await page.waitForTimeout(400);

  const phone = !page.viewportSize() || page.viewportSize()!.width < 768;
  await page.screenshot({ path: `${OUT}/${TAG}-iphone.png`, fullPage: !phone });

  const picker = page.getByRole("radiogroup", { name: "Which device are you setting up?" });
  await picker.getByText("Android", { exact: true }).click();
  await page.getByTestId("caldav-steps-android").waitFor();
  if (phone) await page.getByTestId("caldav-steps-android").scrollIntoViewIfNeeded();
  await page.waitForTimeout(200);
  await page.screenshot({ path: `${OUT}/${TAG}-android.png`, fullPage: !phone });

  await picker.getByText("Mac", { exact: true }).click();
  await page.getByTestId("caldav-steps-mac").waitFor();
  if (phone) await page.getByTestId("caldav-steps-mac").scrollIntoViewIfNeeded();
  await page.waitForTimeout(200);
  await page.screenshot({ path: `${OUT}/${TAG}-mac.png`, fullPage: !phone });
});
