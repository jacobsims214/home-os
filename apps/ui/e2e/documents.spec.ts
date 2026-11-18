import type { Page } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow } from "./fixtures";

/**
 * Documents hub (#4569): the list, the upload flow, one document, and the role
 * that may not see any of it.
 *
 * Runs on the iPhone project only (WebKit + isMobile + hasTouch, 390 px) —
 * `--project=iphone-390`. The hub is built phone-first because that is where a
 * receipt gets photographed: at a counter, on the way out of a shop. Running
 * the whole suite in every project for one page's spec costs CI minutes and
 * adds nothing; the three flows below are viewport-sensitive, and one real
 * iPhone engine is the honest test of them.
 *
 * What each test is FOR:
 *   1. the hub renders the household's documents, the expiring ones first, and
 *      a document opens to its detail page — the read path;
 *   2. the upload flow renders BOTH ways in (camera capture and a file
 *      picker), reports progress, files the document, and shows it saved — and
 *      a failure says nothing was saved instead of closing on a lie;
 *   3. `files.text_origin` is surfaced honestly: a re-typed `raw` body is
 *      marked as changed, an OCR body as read, and only a verified raw body is
 *      ever called the original (#4941);
 *   4. a housesitter (no files read, no Documents in the nav) sees no
 *      documents at all, and a viewer reads but cannot file.
 *
 * The API is stubbed per test with page.route, so each payload is exact and
 * each test stands alone. The pages, the components, the role decision and the
 * provenance wording are the real code.
 */

const IPHONE = "iphone-390";

test.beforeEach(async ({}, testInfo) => {
  test.skip(testInfo.project.name !== IPHONE, `documents.spec.ts runs on the iPhone project (${IPHONE}) only`);
});

/** Answers GET /bff/me/households as a member with `role`. */
async function asRole(page: Page, role: string) {
  await page.route("**/bff/me/households", (route) =>
    route.fulfill({
      json: { data: [{ id: "h1", name: "Sims", timezone: "UTC", role, joined_at: "2026-01-01T00:00:00Z", is_default: true, current: true }] },
    }),
  );
}

/** A day `n` days from today, as the API's "YYYY-MM-DD" (no Date parsing). */
function dayFromToday(n: number): string {
  const d = new Date();
  d.setDate(d.getDate() + n);
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

const POLICY = {
  id: "f1",
  household_id: "h1",
  blob_id: "bl1",
  name: "IMG_4471.pdf",
  title: "Odyssey auto policy renewal",
  content_type: "application/pdf",
  size: 184320,
  entity_type: "household",
  entity_id: "h1",
  category: "insurance",
  document_date: "2026-10-30",
  expires_on: dayFromToday(14),
  description: "Renewal came in the mail; scanned at the kitchen counter.",
  person_id: null,
  ocr_status: "done",
  extracted_text: "Dear Jacob Sims,\n\nThis notice confirms your auto insurance policy will renew.",
  text_origin: "extracted",
  tags: ["progressive"],
  created_at: "2026-11-15T09:00:00Z",
  updated_at: "2026-11-15T09:00:00Z",
  uploaded_by_name: "Jacob Sims",
};

const RECEIPT = {
  id: "f2",
  household_id: "h1",
  blob_id: "bl2",
  name: "costco-receipt.jpg",
  title: "Costco - water heater parts",
  content_type: "image/jpeg",
  size: 903168,
  entity_type: "household",
  entity_id: "h1",
  category: "receipt",
  document_date: "2026-11-12",
  expires_on: null,
  person_id: null,
  ocr_status: "failed",
  // The worker's retry marker (#4419): the row is being retried, not dead.
  ocr_error: "[retry 2/5 next=2030-01-01T00:00:00Z] tika: connection refused",
  text_origin: "extracted",
  tags: [],
  created_at: "2026-11-12T18:00:00Z",
  updated_at: "2026-11-12T18:00:00Z",
  uploaded_by_name: "Amber Sims",
};

const PEOPLE = { data: [{ id: "p1", display_name: "Ellie Sims", relationship: "child", color: "brick" }] };

/**
 * Stubs the household reads the hub needs.
 *
 * Three routes, because they are three different endpoints and a catch-all
 * would hide a page reading the wrong one:
 *   - GET /files           the list (`?category=` narrows it, like the API);
 *   - GET /files/{id}      one document's metadata;
 *   - GET /files/{id}/content   the bytes (the preview fetches these).
 */
async function stubHub(
  page: Page,
  files: unknown[] = [POLICY, RECEIPT],
  opts: { links?: unknown[]; detail?: Record<string, unknown> } = {},
) {
  await asRole(page, "family_member");
  await page.route("**/bff/people", (route) => route.fulfill({ json: PEOPLE }));
  await page.route("**/bff/links?**", (route) => route.fulfill({ json: { data: { other: opts.links ?? [] } } }));
  // Content comes before the metadata route: both are under /files/, and the
  // last-registered route wins in Playwright.
  await page.route("**/bff/files/*/content", (route) =>
    route.fulfill({ status: 200, headers: { "content-type": "text/plain" }, body: "content" }),
  );
  await page.route(/\/bff\/files\/[0-9a-zA-Z-]+$/, (route) => {
    const id = new URL(route.request().url()).pathname.split("/").pop()!;
    const found = (opts.detail ? [opts.detail] : files).find((f) => (f as { id?: string }).id === id);
    return found ? route.fulfill({ json: { data: found } }) : route.fulfill({ status: 404, json: { error: { code: "NOT_FOUND", message: "file not found" } } });
  });
  await page.route(/\/bff\/files(\?.*)?$/, (route) => {
    const url = new URL(route.request().url());
    const category = url.searchParams.get("category");
    const data = category ? files.filter((f) => (f as { category?: string }).category === category) : files;
    return route.fulfill({ json: { data } });
  });
}

// ─── 1. The list and one document ────────────────────────────

test.describe("documents hub", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("the hub lists documents, says what expires, and opens one", async ({ loggedInPage: page }) => {
    await stubHub(page);
    await page.goto("/dashboard/documents");

    // The hub itself, not an error or an empty state.
    await expect(page.locator("[data-documents-page]")).toBeVisible();
    await expect(page.getByRole("heading", { name: /All documents/ })).toBeVisible();

    // Both documents, in the full list, by the title the API calls them by.
    const all = page.locator("[data-documents-all]");
    await expect(all.locator("[data-document-row]")).toHaveCount(2);
    await expect(all.locator('[data-document-row="f1"] [data-document-title]')).toHaveText("Odyssey auto policy renewal");
    await expect(all.locator('[data-document-row="f2"] [data-document-title]')).toHaveText("Costco - water heater parts");

    // Expiring soon comes from the real date, and it is first on the page (a
    // document with an expiry appears here AND in the list, by design).
    const expiring = page.locator("[data-documents-expiring]");
    await expect(expiring).toBeVisible();
    await expect(expiring.locator("[data-document-row]")).toHaveCount(1);
    await expect(expiring.getByText(/Expires in 14 days/)).toBeVisible();

    // Categories carry real counts, and "other" (empty here) has no tile.
    await expect(page.locator('[data-category-tile="insurance"]')).toBeVisible();
    await expect(page.locator('[data-category-tile="receipt"]')).toBeVisible();
    await expect(page.locator('[data-category-tile="other"]')).toHaveCount(0);

    // The search box is a door into semantic search, not a second search.
    await page.getByRole("searchbox", { name: "Search documents" }).fill("car insurance");
    await page.getByRole("button", { name: "Search everything" }).click();
    await expect(page).toHaveURL(/\/dashboard\/search\?q=car%20insurance$/);

    // Back to the hub: a document opens on its own page.
    await page.goto("/dashboard/documents");
    // The list draws a document with an expiry twice (Expiring soon and All
    // documents); the full list's row is the one to open.
    await page.locator('[data-documents-all] [data-document-row="f1"]').click();
    await expect(page).toHaveURL(/\/dashboard\/documents\/f1$/);
    await expect(page.locator("[data-document-detail]")).toBeVisible();
    await expect(page.locator("[data-document-title]")).toHaveText("Odyssey auto policy renewal");
    await expect(page.getByRole("heading", { name: "Details" })).toBeVisible();
    await expectNoHorizontalOverflow(page);
  });

  test("a category view is the API's own filter and offers a way back", async ({ loggedInPage: page }) => {
    // stubHub's list route narrows on ?category= exactly as the API does.
    await stubHub(page, [POLICY, RECEIPT]);
    await page.goto("/dashboard/documents?category=insurance");
    await expect(page.locator('[data-documents-view="category"]')).toBeVisible();
    await expect(page.getByRole("heading", { level: 2, name: "Insurance" })).toBeVisible();
    await expect(page.locator("[data-document-row]")).toHaveCount(1);
    await expect(page.locator('[data-document-row="f1"]')).toBeVisible();
    // A way back to the whole hub.
    await expect(page.locator("nav").getByRole("link", { name: "Documents", exact: true })).toHaveAttribute("href", "/dashboard/documents");
  });

  test("a family with no documents is told what will appear here", async ({ loggedInPage: page }) => {
    await stubHub(page, []);
    await page.goto("/dashboard/documents");
    const empty = page.locator("[data-documents-empty]");
    await expect(empty).toBeVisible();
    await expect(empty.getByText("No documents yet")).toBeVisible();
    await expect(empty.getByText(/warns you before anything expires/i)).toBeVisible();
    await expect(empty.getByText(/find it by meaning/i)).toBeVisible();
    // No fake rows and no fake counts.
    await expect(page.locator("[data-document-row]")).toHaveCount(0);
    await expect(page.locator("[data-documents-expiring]")).toHaveCount(0);
  });

  test("a failed load is its own state with a retry, never 'no documents yet'", async ({ loggedInPage: page }) => {
    await asRole(page, "family_member");
    await page.route("**/bff/people", (route) => route.fulfill({ json: PEOPLE }));
    await page.route(/\/bff\/files(\?.*)?$/, (route) =>
      route.fulfill({ status: 500, json: { error: { code: "INTERNAL", message: "boom" } } }),
    );
    await page.goto("/dashboard/documents");
    await expect(page.locator("[data-documents-error]")).toBeVisible();
    await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
    await expect(page.locator("[data-documents-empty]")).toHaveCount(0);
  });
});

// ─── 2. Upload ───────────────────────────────────────────────

test.describe("upload from a phone", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("camera and file picker are both real inputs, progress shows, and the document is filed", async ({
    loggedInPage: page,
  }) => {
    await stubHub(page, [POLICY]);
    const posts: Array<{ body: Buffer; contentType: string }> = [];
    await page.route("**/bff/files/upload", async (route) => {
      const req = route.request();
      posts.push({ body: req.postDataBuffer() ?? Buffer.alloc(0), contentType: req.headers()["content-type"] ?? "" });
      return route.fulfill({ status: 201, json: { data: { ...RECEIPT, id: "f-new", title: "Counter receipt", ocr_status: "pending", ocr_error: undefined } } });
    });
    // The reading step polls GET /files/{id}; answer with a terminal status.
    await page.route(/\/bff\/files\/f-new$/, (route) =>
      route.fulfill({ json: { data: { ...RECEIPT, id: "f-new", title: "Counter receipt", ocr_status: "done", ocr_error: undefined, extracted_text: "COSTCO 64.19" } } }),
    );

    await page.goto("/dashboard/documents");
    await page.getByRole("button", { name: /Scan or upload a document/ }).click();

    const sheet = page.getByTestId("upload-sheet");
    await expect(sheet).toBeVisible();

    // BOTH ways in exist, and the camera one really captures: `capture` is what
    // makes a phone open the camera instead of the photo library.
    const camera = page.getByTestId("upload-camera-input");
    const picker = page.getByTestId("upload-file-input");
    await expect(camera).toHaveAttribute("capture", "environment");
    await expect(camera).toHaveAttribute("accept", "image/*");
    await expect(picker).not.toHaveAttribute("capture", /.+/);
    await expect(page.getByRole("button", { name: "Take a photo" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Choose a file" })).toBeVisible();

    // Pick a file through the picker the way a person does.
    await picker.setInputFiles({ name: "counter-receipt.jpg", mimeType: "image/jpeg", buffer: Buffer.from("\xff\xd8\xff\xe0fakejpeg") });

    // The details step, pre-filled with a starting title from the file name.
    await expect(page.getByTestId("upload-details")).toBeVisible();
    const title = page.getByTestId("upload-title");
    await expect(title).toHaveValue("Counter receipt");
    await title.fill("Counter receipt");
    await page.locator('[data-testid="upload-details"] [data-category="receipt"]').click();
    await page.getByTestId("upload-submit").click();

    // It really posted a multipart form carrying the file and the metadata.
    await expect.poll(() => posts.length).toBe(1);
    expect(posts[0].contentType).toContain("multipart/form-data");
    const posted = posts[0].body.toString("latin1");
    expect(posted).toContain('name="file"');
    expect(posted).toContain("counter-receipt.jpg");
    expect(posted).toContain('name="category"');
    expect(posted).toContain("receipt");
    expect(posted).toContain('name="title"');

    // Filed, in as many words — and only after the record really came back.
    await expect(page.getByTestId("upload-saved")).toBeVisible();
    await expect(page.getByText(/Filed under Receipts/)).toBeVisible();
    await expectNoHorizontalOverflow(page);
  });

  test("a refused upload says nothing was saved and offers a retry", async ({ loggedInPage: page }) => {
    await stubHub(page, [POLICY]);
    let attempts = 0;
    await page.route("**/bff/files/upload", (route) => {
      attempts += 1;
      return route.fulfill({
        status: attempts === 1 ? 413 : 201,
        json:
          attempts === 1
            ? { error: { code: "BAD_REQUEST", message: "upload too large or invalid multipart form (max 25MB)" } }
            : { data: { ...RECEIPT, id: "f-new", ocr_status: "pending" } },
      });
    });
    await page.route(/\/bff\/files\/f-new$/, (route) => route.fulfill({ json: { data: { ...RECEIPT, id: "f-new", ocr_status: "done", extracted_text: "x" } } }));

    await page.goto("/dashboard/documents");
    await page.getByRole("button", { name: /Scan or upload a document/ }).click();
    await page.getByTestId("upload-file-input").setInputFiles({ name: "huge.pdf", mimeType: "application/pdf", buffer: Buffer.from("%PDF-1.4 big") });
    await page.getByTestId("upload-submit").click();

    // The failure is its own step: named, explained, and honest that the file
    // did NOT reach the server — never a closed sheet that looks like success.
    const failure = page.getByTestId("upload-failure");
    await expect(failure).toBeVisible();
    await expect(failure).toContainText(/25 MB/i);
    await expect(failure).toContainText(/Nothing was saved/i);
    await expect(page.getByTestId("upload-saved")).toHaveCount(0);
    // The sheet stays open, and the retry goes back to the details step with
    // the file still in hand — nothing to re-type, nothing re-sent silently.
    await page.getByTestId("upload-retry").click();
    await expect(page.getByTestId("upload-details")).toBeVisible();
    await expect(page.getByTestId("upload-submit")).toBeVisible();
    await page.getByTestId("upload-submit").click();
    // The second attempt succeeded, so the flow ends where it should.
    await expect(page.getByTestId("upload-saved")).toBeVisible();
  });

  test("a viewer reads documents but is never offered the add or edit controls", async ({ loggedInPage: page }) => {
    await stubHub(page);
    await asRole(page, "viewer");
    await page.goto("/dashboard/documents");

    // A viewer CAN read the hub (#4689: files read is family + viewer).
    await expect(page.locator("[data-documents-all] [data-document-row]")).toHaveCount(2);
    await expect(page.locator("[data-documents-viewer-note]")).toBeVisible();
    // But nothing to file or change it with.
    await expect(page.getByRole("button", { name: /Scan or upload a document/ })).toHaveCount(0);
    await expect(page.getByTestId("upload-sheet")).toHaveCount(0);

    await page.waitForLoadState("networkidle");
    await page.goto("/dashboard/documents/f1");
    await expect(page.locator("[data-document-detail]")).toBeVisible();
    await expect(page.getByTestId("document-text")).toBeVisible();
    await expect(page.locator("[data-document-edit]")).toHaveCount(0);
    await expect(page.locator("[data-document-delete]")).toHaveCount(0);
    await expect(page.locator("[data-document-download]")).toBeVisible();
  });
});

// ─── 3. text_origin (#4941) ──────────────────────────────────

test.describe("what a document's text is", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("a re-typed body is never presented as the original", async ({ loggedInPage: page }) => {
    // The war game's file: the text was rPATCHed after filing, so the stored
    // body no longer matches the fingerprint taken at upload.
    const retyped = {
      ...RECEIPT,
      id: "f-typed",
      title: "Costco receipt (filed from the email)",
      ocr_status: "done",
      ocr_error: undefined,
      extracted_text: "COSTCO\nWater heater  $35.00\nTotal $5.00",
      text_origin: "raw",
      text_verified: false,
    };
    await stubHub(page, [retyped]);
    await page.goto("/dashboard/documents/f-typed");

    const notice = page.getByTestId("document-text-provenance");
    await expect(notice).toHaveAttribute("data-provenance", "altered");
    // It says, in words, that this is NOT the file.
    await expect(notice).toContainText(/no longer matches what was filed/i);
    await expect(notice).toContainText(/Treat the file .* as the original/i);
    // And it never claims the opposite.
    await expect(notice).not.toContainText(/exactly what was filed/i);
    await expect(page.getByTestId("document-text")).toContainText("$5.00");
  });

  test("OCR text says it was read, and only a verified raw body is called the original", async ({ loggedInPage: page }) => {
    await stubHub(page, [POLICY]);
    await page.goto("/dashboard/documents/f1");
    const ocr = page.getByTestId("document-text-provenance");
    await expect(ocr).toHaveAttribute("data-provenance", "derived");
    await expect(ocr).toContainText(/read from the file automatically/i);
    await expect(ocr).toContainText(/may differ/i);

    // The same document with a verified raw body: now it IS the original, and
    // it says so.
    await page.unroute(/\/bff\/files\/[0-9a-zA-Z-]+$/);
    await page.route(/\/bff\/files\/[0-9a-zA-Z-]+$/, (route) =>
      route.fulfill({ json: { data: { ...POLICY, text_origin: "raw", text_verified: true } } }),
    );
    await page.reload();
    const source = page.getByTestId("document-text-provenance");
    await expect(source).toHaveAttribute("data-provenance", "source");
    await expect(source).toContainText(/exactly what was filed/i);
  });

  test("a retrying file says it is retrying, without the worker's marker", async ({ loggedInPage: page }) => {
    await stubHub(page, [RECEIPT]);
    await page.goto("/dashboard/documents/f2");
    const state = page.getByTestId("document-text-state");
    await expect(state).toBeVisible();
    await expect(state).toContainText(/trying again/i);
    await expect(state).not.toContainText("[retry");
    // The file is still there and downloadable.
    await expect(page.locator("[data-document-download]")).toBeVisible();
  });
});

// ─── 4. Roles ────────────────────────────────────────────────

test.describe("roles", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("a housesitter sees no documents and no Documents entry in the nav", async ({ loggedInPage: page }) => {
    await asRole(page, "housesitter");
    let fileRequests = 0;
    await page.route(/\/bff\/files/, (route) => {
      fileRequests += 1;
      return route.fulfill({ json: { data: [POLICY, RECEIPT] } });
    });
    await page.route("**/bff/people", (route) => route.fulfill({ json: PEOPLE }));

    await page.goto("/dashboard/documents");

    // The page refuses to draw household paperwork for this role...
    await expect(page.locator("[data-documents-blocked]")).toBeVisible();
    await expect(page.getByText(/Documents is for the family/)).toBeVisible();
    // ...and does not even ask the API for the list (a refused request per
    // visit is what #4564 forbids).
    expect(fileRequests).toBe(0);
    await expect(page.locator("[data-document-row]")).toHaveCount(0);

    // Documents is not in her navigation at all (#4689 + nav-config).
    const bar = page.getByTestId("tab-bar");
    await bar.getByRole("button", { name: "More" }).click();
    const sheet = page.getByRole("dialog", { name: "More" });
    await expect(sheet).toBeVisible();
    await expect(sheet.getByText("Documents", { exact: true })).toHaveCount(0);
    await expect(sheet.getByText("People & House", { exact: true })).toBeVisible();
    await page.keyboard.press("Escape");
    await expectNoHorizontalOverflow(page);
  });

  test("a vendor sees no documents either", async ({ loggedInPage: page }) => {
    await asRole(page, "vendor");
    await page.route(/\/bff\/files/, (route) => route.fulfill({ json: { data: [POLICY] } }));
    await page.goto("/dashboard/documents");
    await expect(page.locator("[data-documents-blocked]")).toBeVisible();
    await expect(page.locator("[data-document-row]")).toHaveCount(0);
  });

  test("a family member DOES see the Documents section in the nav", async ({ loggedInPage: page }) => {
    await stubHub(page);
    await page.goto("/dashboard/documents");
    const bar = page.getByTestId("tab-bar");
    await bar.getByRole("button", { name: "More" }).click();
    const sheet = page.getByRole("dialog", { name: "More" });
    await expect(sheet).toBeVisible();
    const tile = sheet.getByRole("link", { name: /Documents/ });
    await expect(tile).toBeVisible();
    // Its subtitle describes the destination; it never says "coming soon".
    await expect(sheet.getByText("Insurance, school forms, receipts")).toBeVisible();
    await expect(sheet.getByText(/coming soon/i)).toHaveCount(0);
  });
});
