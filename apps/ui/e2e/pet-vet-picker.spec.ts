import type { Page, Route, TestInfo } from "@playwright/test";
import { test, expect } from "./fixtures";

/**
 * Pet vet picker (#4703): the pet's vet is a family contact tagged "vet"
 * (pets.vet_vendor_id), chosen in a ResponsiveModal sheet, with an inline
 * "add a new vet contact". The pet PUT carries vet_vendor_id and never
 * vet_name / vet_phone (#4705 drops those columns). Phone width 390, pinned,
 * so this runs once, in the desktop-chrome project.
 */

const VETS = [
  { id: "v1", household_id: "h1", name: "Springfield Animal Hospital", phone: "555-0100", trades: ["vet"] },
  { id: "v2", household_id: "h1", name: "Oak Street Veterinary", phone: "555-0142", trades: ["vet", "groomer"] },
];

type Body = Record<string, unknown>;

async function stubPet(page: Page) {
  let pet: Body = {
    id: "p1",
    name: "Max",
    species: "Dog",
    breed: "Beagle",
    vet_vendor_id: "v1",
    vet_name: VETS[0].name,
    vet_phone: VETS[0].phone,
  };
  const vets = [...VETS];
  const writes: { petPuts: Body[]; vendorPosts: Body[]; vendorQueries: string[] } = {
    petPuts: [],
    vendorPosts: [],
    vendorQueries: [],
  };

  await page.route("**/bff/pets/p1", async (route: Route) => {
    if (route.request().method() === "PUT") {
      const body = route.request().postDataJSON() as Body;
      writes.petPuts.push(body);
      const vet = vets.find((v) => v.id === body.vet_vendor_id);
      // What #4700's API returns: the vet's name/phone come from the contact.
      const { vet_name: _n, vet_phone: _p, ...rest } = body;
      pet = { id: "p1", ...rest, ...(vet ? { vet_name: vet.name, vet_phone: vet.phone } : { vet_vendor_id: undefined }) };
      return route.fulfill({ json: { data: pet } });
    }
    return route.fulfill({ json: { data: pet } });
  });
  await page.route(/\/bff\/vendors(\?.*)?$/, async (route: Route) => {
    const req = route.request();
    if (req.method() === "POST") {
      const body = req.postDataJSON() as Body;
      writes.vendorPosts.push(body);
      const created = { id: "v9", household_id: "h1", ...body } as (typeof VETS)[number];
      vets.unshift(created);
      return route.fulfill({ status: 201, json: { data: created } });
    }
    writes.vendorQueries.push(new URL(req.url()).search);
    return route.fulfill({ json: { data: vets } });
  });
  return writes;
}

async function startEditing(page: Page) {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/dashboard/pets/p1");
  const main = page.locator("main");
  await expect(main.getByRole("heading", { level: 2, name: "Max" })).toBeVisible();
  // Read-only view shows the contact's name, from the API's read-only vet_name.
  await expect(main.locator('[data-field="vet_vendor_id"] dd')).toContainText(VETS[0].name);
  await main.getByRole("button", { name: "Actions for Max" }).click();
  await page.getByRole("menuitem", { name: "Edit" }).click();
  const picker = main.locator("[data-vet-picker]");
  await expect(picker).toContainText(VETS[0].name);
  return { main, picker };
}

async function openSheet(page: Page, picker: ReturnType<Page["locator"]>) {
  await picker.click();
  const dialog = page.getByRole("dialog", { name: "Choose a vet" });
  await expect(dialog).toBeVisible();
  // Bottom sheet on a phone: full width, anchored to the bottom.
  const box = (await dialog.boundingBox())!;
  expect(Math.round(box.width)).toBe(390);
  expect(Math.round(box.y + box.height)).toBe(844);
  return dialog;
}

async function snap(page: Page, testInfo: TestInfo, name: string) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

const noHorizontalScroll = (page: Page) =>
  page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);

function expectOnlyVendorId(body: Body, id: string) {
  expect(body.vet_vendor_id).toBe(id);
  expect(body).not.toHaveProperty("vet_name");
  expect(body).not.toHaveProperty("vet_phone");
}

test.beforeEach(async ({ page: _page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-chrome", "viewport is pinned per test");
});

test("390: pick an existing vet contact; the save sends only vet_vendor_id", async ({ loggedInPage: page }, testInfo) => {
  const writes = await stubPet(page);
  const { main, picker } = await startEditing(page);

  const dialog = await openSheet(page, picker);
  expect(writes.vendorQueries).toContain("?trade=vet");
  const current = dialog.getByRole("radio", { name: new RegExp(VETS[0].name) });
  await expect(current).toBeChecked();
  const oak = dialog.locator('[data-vet-option="v2"]');
  expect((await oak.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);
  expect(await noHorizontalScroll(page)).toBe(true);
  await snap(page, testInfo, "vet-sheet-390");

  await dialog.getByRole("radio", { name: new RegExp(VETS[1].name) }).check();
  await dialog.getByRole("button", { name: "Use this vet" }).click();
  await expect(dialog).toBeHidden();
  await expect(picker).toContainText(VETS[1].name);
  await expect(picker).toContainText(VETS[1].phone);
  // Closing the sheet didn't leave the page.
  await expect(page).toHaveURL(/\/dashboard\/pets\/p1$/);

  await main.getByRole("button", { name: "Save" }).click();
  await expect(main.locator('[data-field="vet_vendor_id"] dd')).toContainText(VETS[1].name);
  expect(writes.petPuts).toHaveLength(1);
  expectOnlyVendorId(writes.petPuts[0], "v2");
  expect(writes.vendorPosts).toHaveLength(0);
});

test("390: add a new vet contact inline; it is created tagged vet and chosen", async ({ loggedInPage: page }, testInfo) => {
  const writes = await stubPet(page);
  const { main, picker } = await startEditing(page);

  const dialog = await openSheet(page, picker);
  await dialog.getByRole("button", { name: "Add a new vet contact" }).click();
  const name = dialog.getByRole("textbox", { name: "Vet or clinic name" });
  await expect(name).toBeFocused();

  // A name is required.
  await dialog.getByRole("button", { name: "Add vet" }).click();
  await expect(dialog.getByText("Enter the vet's name")).toBeVisible();
  expect(writes.vendorPosts).toHaveLength(0);

  await name.fill("Riverside Pet Clinic");
  await dialog.getByRole("textbox", { name: "Phone" }).fill("555-0177");
  expect(await noHorizontalScroll(page)).toBe(true);
  await snap(page, testInfo, "vet-sheet-390-add");
  await dialog.getByRole("button", { name: "Add vet" }).click();
  await expect(dialog).toBeHidden();

  expect(writes.vendorPosts).toEqual([
    { name: "Riverside Pet Clinic", phone: "555-0177", specialty: "Veterinarian", trades: ["vet"] },
  ]);
  await expect(picker).toContainText("Riverside Pet Clinic");

  await main.getByRole("button", { name: "Save" }).click();
  await expect(main.locator('[data-field="vet_vendor_id"] dd')).toContainText("Riverside Pet Clinic");
  expect(writes.petPuts).toHaveLength(1);
  expectOnlyVendorId(writes.petPuts[0], "v9");
  await snap(page, testInfo, "pet-390-after-save");
});
