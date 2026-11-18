import type { Page, Route, TestInfo } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * Contacts (vendors) pages (#4557), mobile-first per the People & House
 * mockup (artifact #89, People & House > Contacts), on the list and
 * detail-page patterns (KB architecture/entity-list-pattern.md,
 * architecture/detail-page-layout.md).
 *
 * The regressions this spec holds down, all from the mobile audit (#4509,
 * artifact #74) and the manager notes on the ticket:
 * 1. "Add" went to /dashboard/vendors/new, a route that does not exist.
 * 2. The detail page rendered an EMPTY card at every width — no phone, no
 *    trades, no notes.
 * 3. Nothing on the page was tappable: no tel: or mailto: anywhere, so a
 *    phone could not call the plumber.
 * 4. EntityList turned the property filter on by default, which just emptied
 *    the list (a contact has no property_id).
 * 5. Addresses (#4766) are a real column now and had no UI.
 *
 * Phone (390): chips over one-column cards with Call/Text, 44 px targets, no
 * horizontal scroll; the detail is a head, the actions, the Maps address and
 * the fields. Desktop (1440): the card grid goes multi-column and the detail
 * keeps the same content in the frame's two-column layout.
 */

type Body = Record<string, unknown>;

const CONTACTS = [
  {
    id: "v1", household_id: "h1", name: "Joe's Plumbing",
    trades: ["plumber", "handyman"], specialty: "Plumbing",
    phone: "(555) 010-0142", email: "joe@joesplumbing.example",
    website: "joesplumbing.example", address: "12 Main St, Springfield",
    notes: "Ask for Joe. Shut-off is under the kitchen sink.",
    created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "v2", household_id: "h1", name: "Springfield Animal Hospital",
    trades: ["vet"], specialty: "Veterinarian",
    phone: "555-0100", email: null, website: null,
    address: "400 Vet Way, Springfield", notes: null,
    created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "v3", household_id: "h1", name: "Ace Pest Control",
    // A contact with no phone and no email: the card must not render an
    // empty action row, and the page must still be usable.
    trades: ["pest_control"], specialty: null, phone: null, email: null,
    website: null, address: null, notes: null,
    created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "v4", household_id: "h1", name: "Hannah Lee",
    // A family's own free-text trade, not in the synonym map: it still gets
    // a chip, because the chips come from the data.
    trades: ["babysitter"], specialty: null, phone: "555-0199", email: "hannah@example.test",
    website: null, address: null, notes: null,
    created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
  },
];

const PETS = [
  { id: "p1", household_id: "h1", name: "Biscuit", species: "dog", vet_vendor_id: "v2" },
  // Another pet with no vet: it must not appear on any contact.
  { id: "p2", household_id: "h1", name: "Pepper", species: "cat", vet_vendor_id: null },
];

const BILLS = [
  { id: "b1", household_id: "h1", name: "Quarterly pest spray", amount: "120.00", frequency: "quarterly", vendor_id: "v3" },
  { id: "b2", household_id: "h1", name: "Electricity", amount: "88.00", frequency: "monthly", vendor_id: null },
];

async function stubContacts(
  page: Page,
  opts: { fail?: boolean; rows?: unknown[] } = {},
): Promise<{ puts: Body[]; posts: Body[] }> {
  const writes = { puts: [] as Body[], posts: [] as Body[] };

  await page.route(/\/bff\/vendors(\?.*)?$/, async (route: Route) => {
    const req = route.request();
    if (req.method() === "POST") {
      const body = req.postDataJSON() as Body;
      writes.posts.push(body);
      return route.fulfill({ status: 201, json: { data: { id: "v9", trades: [], ...body } } });
    }
    if (opts.fail) return route.fulfill({ status: 500, json: { error: "boom" } });
    return route.fulfill({ json: { data: opts.rows ?? CONTACTS } });
  });

  // Each contact's detail: the same row, plus PUT capture.
  for (const c of CONTACTS) {
    await page.route(`**/bff/vendors/${c.id}`, async (route: Route) => {
      if (route.request().method() === "PUT") {
        const body = route.request().postDataJSON() as Body;
        writes.puts.push(body);
        return route.fulfill({ json: { data: { ...c, ...body } } });
      }
      return route.fulfill({ json: { data: c } });
    });
  }

  await page.route(/\/bff\/pets(\?.*)?$/, (route) => route.fulfill({ json: { data: PETS } }));
  await page.route(/\/bff\/bills(\?.*)?$/, (route) => route.fulfill({ json: { data: BILLS } }));
  return writes;
}

async function gotoContacts(page: Page) {
  await page.goto("/dashboard/vendors");
  await expect(page.locator("main").getByRole("heading", { level: 2, name: /Contacts/ })).toBeVisible();
}

async function snap(page: Page, testInfo: TestInfo, name: string, fullPage = false) {
  const path = testInfo.outputPath(`${name}.png`);
  await page.screenshot({ path, fullPage });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme}`, () => {
    test.use({ colorScheme });

    test("phone 390: trade chips filter the list, cards carry Call and Text", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubContacts(page);
      await gotoContacts(page);
      const main = page.locator("main");

      // 4. The property filter has no business here: a contact has no property.
      await expect(main.getByText("Filter by property")).toHaveCount(0);

      const cards = main.locator("[data-contact-card]");
      await expect(cards).toHaveCount(CONTACTS.length);
      await expect(cards.first()).toContainText("Joe's Plumbing");
      // Labelled, not raw storage: "pest_control" must never reach the screen (#4509).
      await expect(cards.first()).toContainText("Plumber · Handyman");
      await expect(main.locator('[data-contact-card="v3"]')).toContainText("Pest control");
      await expect(main.locator('[data-contact-card="v3"]')).not.toContainText("pest_control");

      // The chips come from the data: every distinct trade, plus All.
      const chips = main.locator("[data-trade-chip]");
      await expect(chips).toHaveCount(1 + 5); // All + plumber, handyman, vet, pest_control, babysitter
      await expect(chips.first()).toContainText("All");
      await expect(chips.first()).toContainText("4");
      // Stored snake_case is shown as words.
      await expect(main.getByRole("button", { name: /Pest control/ })).toBeVisible();
      await expect(main.getByRole("button", { name: /^Pest_control/ })).toHaveCount(0);
      // A family's own trade gets a chip too (never a hand-written list).
      await expect(main.getByRole("button", { name: /Babysitter/ })).toBeVisible();

      // 3. Tap-to-call, with the digits the dialler needs.
      const call = cards.first().getByRole("link", { name: "Call Joe's Plumbing" });
      await expect(call).toHaveAttribute("href", "tel:5550100142");
      await expect(cards.first().getByRole("link", { name: "Text Joe's Plumbing" })).toHaveAttribute(
        "href",
        "sms:5550100142",
      );
      await expect(cards.first().getByRole("link", { name: "Email Joe's Plumbing" })).toHaveAttribute(
        "href",
        "mailto:joe@joesplumbing.example",
      );

      // A contact with no phone has no empty action row.
      const ace = main.locator('[data-contact-card="v3"]');
      await expect(ace.locator("[data-contact-action]")).toHaveCount(0);

      // 44 px targets, and nothing spills sideways.
      for (let i = 0; i < CONTACTS.length; i++) {
        const box = (await cards.nth(i).boundingBox())!;
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width).toBeLessThanOrEqual(390);
      }
      for (const el of [call, main.getByRole("button", { name: "Add contact" }), chips.first()]) {
        expect((await el.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);
      }
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `contacts-390-${colorScheme}`, true);

      // Filtering really filters, by one chip at a time.
      await main.getByRole("button", { name: /^Vet/ }).click();
      await expect(cards).toHaveCount(1);
      await expect(cards.first()).toContainText("Springfield Animal Hospital");
      await expect(main.getByRole("button", { name: /^Vet/ })).toHaveAttribute("aria-pressed", "true");
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `contacts-390-${colorScheme}-vet`);

      // All comes back.
      await chips.first().click();
      await expect(cards).toHaveCount(CONTACTS.length);
    });

    test("phone 390: Add opens a sheet and POSTs, never a /new route", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      const writes = await stubContacts(page);
      await gotoContacts(page);
      const main = page.locator("main");

      // 1. The audit's bug: Add navigated to a /new route that doesn't exist.
      await main.getByRole("button", { name: "Add contact" }).click();
      const dialog = page.getByRole("dialog");
      await expect(dialog).toBeVisible();
      expect(page.url()).not.toContain("/new");

      await dialog.getByRole("textbox", { name: "Name" }).fill("Bright Spark Electric");
      // Typed the way a person would: the server's own vocabulary saves it as
      // electrician, and the form normalises it before sending.
      // The trades field has a datalist type-ahead, so ARIA gives it the
      // combobox role (Mantine renders it that way); Name and Phone stay textboxes.
      await dialog.getByRole("combobox", { name: "Trades" }).fill("Electrical");
      await dialog.getByRole("textbox", { name: "Phone" }).fill("555-0161");
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `contacts-390-${colorScheme}-add`);
      await dialog.getByRole("button", { name: "Add contact" }).click();

      await expect.poll(() => writes.posts.length).toBe(1);
      expect(writes.posts[0]).toEqual({
        name: "Bright Spark Electric",
        trades: ["electrician"],
        phone: "555-0161",
      });
    });

    test("phone 390: detail shows trades, call actions and the address in Maps", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      await stubContacts(page);
      await page.goto("/dashboard/vendors/v1");
      const main = page.locator("main");

      // 2. The empty card the audit found: the real fields are here now.
      await expect(main.getByRole("heading", { level: 2, name: "Joe's Plumbing" })).toBeVisible();
      const detail = main.locator("[data-contact-detail]");
      await expect(detail).toContainText("Plumbing");
      await expect(detail).toContainText("Shut-off is under the kitchen sink");

      // Trades as chips in the head, words not snake_case.
      const trades = main.locator("[data-contact-trades]");
      await expect(trades).toContainText("Plumber");
      await expect(trades).toContainText("Handyman");

      // The actions a phone needs, real anchors.
      const actions = main.locator("[data-contact-actions]");
      await expect(actions.getByRole("link", { name: "Call" })).toHaveAttribute("href", "tel:5550100142");
      await expect(actions.getByRole("link", { name: "Text" })).toHaveAttribute("href", "sms:5550100142");
      await expect(actions.getByRole("link", { name: "Email" })).toHaveAttribute(
        "href",
        "mailto:joe@joesplumbing.example",
      );
      for (let i = 0; i < 3; i++) {
        expect((await actions.getByRole("link").nth(i).boundingBox())!.height).toBeGreaterThanOrEqual(43.5);
      }

      // 5. The address (#4766), tap-to-open in Maps.
      const address = main.locator("[data-contact-address]");
      await expect(address).toContainText("12 Main St, Springfield");
      await expect(address).toHaveAttribute("href", /google\.com\/maps\/search\/\?api=1&query=12%20Main%20St/);
      await expect(address).toHaveAttribute("target", "_blank");
      expect((await address.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);

      // The field rows.
      await expect(main.locator('[data-field="trades"]')).toContainText("Plumber, Handyman");
      await expect(main.locator('[data-field="phone"]')).toContainText("(555) 010-0142");
      await expect(main.locator('[data-field="website"]')).toContainText("joesplumbing.example");
      await expect(main.locator('[data-field="website"] a')).toHaveAttribute("href", "https://joesplumbing.example");

      // The bills paid to this contact; another vendor's bill is not here.
      const bills = main.locator("[data-contact-bills]");
      await expect(bills).toHaveCount(0); // v1 is a plumber, the bill is Ace's

      // The phone back target is 44 px and goes to the list.
      const back = main.getByRole("link", { name: "Back to Vendors" });
      expect((await back.boundingBox())!.height).toBeGreaterThanOrEqual(43.5);
      await expect(back).toHaveAttribute("href", "/dashboard/vendors");

      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `contact-detail-390-${colorScheme}`, true);
    });

    test("phone 390: the vet's page lists the pets, and saving clears what was emptied", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("phone");
      await page.setViewportSize({ width: 390, height: 844 });
      const writes = await stubContacts(page);
      await page.goto("/dashboard/vendors/v2");
      const main = page.locator("main");

      // The vet is referenced by pets.vet_vendor_id (#4703), so the pets come here.
      const pets = main.locator("[data-contact-pets]");
      await expect(pets).toContainText("Biscuit");
      await expect(pets).not.toContainText("Pepper");
      await expect(pets.getByRole("link", { name: /Biscuit/ })).toHaveAttribute("href", "/dashboard/pets/p1");

      // A bill links to its vendor: Ace Pest Control's page shows it.
      await page.goto("/dashboard/vendors/v3");
      const aceBills = main.locator("[data-contact-bills]");
      await expect(aceBills).toContainText("Quarterly pest spray");
      await expect(aceBills).not.toContainText("Electricity");
      // And a contact with no phone still shows no action row.
      await expect(main.locator("[data-contact-actions]")).toHaveCount(0);
      await snap(page, testInfo, `contact-detail-390-${colorScheme}-pest`);

      // Editing: an emptied address must CLEAR (the API keeps an omitted key).
      await page.goto("/dashboard/vendors/v2");
      // On a phone the frame keeps Edit in the title bar's actions menu
      // (DetailPageLayout #4531), so open that first.
      await main.getByRole("button", { name: "Actions for Springfield Animal Hospital" }).click();
      await page.getByRole("menuitem", { name: "Edit" }).click();
      await main.getByRole("textbox", { name: "Address" }).fill("");
      await main.getByRole("button", { name: "Save" }).click();

      await expect.poll(() => writes.puts.length).toBe(1);
      const body = writes.puts[0];
      // Empty address is sent explicitly as "", not dropped.
      expect(body).toHaveProperty("address", "");
      // Trades are always sent, so the box is the truth.
      expect(body.trades).toEqual(["vet"]);
      expect(body.name).toBe("Springfield Animal Hospital");
    });

    test("desktop 1440: the grid goes multi-column and the detail keeps every fact", async ({ loggedInPage: page }, testInfo) => {
      onlyOn("desktop");
      await page.setViewportSize({ width: 1440, height: 900 });
      await stubContacts(page);
      await gotoContacts(page);
      const main = page.locator("main");

      // Two cards share a row at desktop width.
      const cards = main.locator("[data-contact-card]");
      await expect(cards).toHaveCount(CONTACTS.length);
      const xs = await cards.evaluateAll((els) => els.map((el) => Math.round(el.getBoundingClientRect().x)));
      expect(new Set(xs).size).toBeGreaterThan(1);
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `contacts-1440-${colorScheme}`);

      // The card link is named "Joe's Plumbing plumber · handyman" (it wraps
      // the whole row), so open it by its href rather than a name regex.
      await main.locator('[data-contact-card="v1"] a[href="/dashboard/vendors/v1"]').click();
      await expect(main.getByRole("heading", { level: 2, name: "Joe's Plumbing" })).toBeVisible();
      await expect(main.locator('[data-field="address"]')).toContainText("12 Main St, Springfield");
      await expect(main.locator("[data-contact-address]")).toBeVisible();
      await expectNoHorizontalOverflow(page);
      await snap(page, testInfo, `contact-detail-1440-${colorScheme}`);
    });
  });
}

test("error state offers Try again and never reads as an empty list", async ({ loggedInPage: page }) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  await stubContacts(page, { fail: true });
  await page.goto("/dashboard/vendors");
  const main = page.locator("main");
  await expect(main.getByRole("alert")).toContainText("Couldn't load contacts");
  await expect(main.getByText("No contacts yet")).toHaveCount(0);
  await expect(main.getByRole("button", { name: "Try again" })).toBeVisible();
  await expectNoHorizontalOverflow(page);
});

test("empty state offers Add instead of a dead end", async ({ loggedInPage: page }) => {
  onlyOn("phone");
  await page.setViewportSize({ width: 390, height: 844 });
  await stubContacts(page, { rows: [] });
  await gotoContacts(page);
  const main = page.locator("main");
  await expect(main.getByText("No contacts yet")).toBeVisible();
  // No chips when there is nothing to filter.
  await expect(main.locator("[data-trade-chip]")).toHaveCount(0);
  // Header and empty state both offer Add.
  await expect(main.getByRole("button", { name: "Add contact" })).toHaveCount(2);
  await main.getByRole("button", { name: "Add contact" }).last().click();
  await expect(page.getByRole("dialog")).toBeVisible();
  expect(page.url()).not.toContain("/new");
});
