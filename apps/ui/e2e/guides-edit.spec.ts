import type { Page, Route } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * Guides editing and revision history (#5309).
 *
 * The ticket's criteria, in the order they are checked here:
 *
 * 1. editing a guide on a phone is usable, and SAVES WITHOUT WIPING unrelated
 *    fields - the #4565 trap: `PUT /guides/{id}` writes every column back, so
 *    the form must send the whole record. The stub here records the body and
 *    the test asserts every key is present, including the subject columns the
 *    form cannot edit;
 * 2. prior revisions are listed with who and when;
 * 3. restoring one works AND is itself recoverable: the revision the restore
 *    replaced is still in the list afterwards, and restoring it puts things
 *    back. That is the assertion that would fail if a restore ever became a
 *    destructive rollback.
 *
 * The API is stubbed statefully (a PUT really replaces the row and appends a
 * revision, a restore really writes a new one) so the pages, the editor and the
 * history are the real code and the assertions are about behaviour, not calls.
 */

const IPHONE = "iphone-390";

interface Row {
  id: string;
  household_id: string;
  title: string;
  summary: string | null;
  body: string;
  tags: string[];
  property_id: string | null;
  asset_id: string | null;
  vehicle_id: string | null;
  created_at: string;
  updated_at: string;
}

interface Rev {
  id: string;
  guide_id: string;
  body: string;
  edited_by: string | null;
  edited_by_name: string | null;
  change_note: string | null;
  created_at: string;
}

const GUIDE: Row = {
  id: "g1",
  household_id: "h1",
  title: "Reset the router",
  summary: "When the Wi-Fi drops and the lights are red.",
  body: "## Turn it off and on\n\nPull the plug and wait 30 seconds.\n\n## Check the lights\n\n- Internet: solid green",
  tags: ["wifi", "router"],
  // A subject the form does NOT show. If a save drops it, the API writes back
  // whatever the row held - so this is the field the trap actually loses.
  property_id: "prop-1",
  asset_id: null,
  vehicle_id: null,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-11-02T00:00:00Z",
};

/** Three versions, newest first, with the ids the tests act on. */
function seedRevisions(): Rev[] {
  return [
    {
      id: "11111111-1111-4111-8111-111111111111",
      guide_id: "g1",
      body: GUIDE.body,
      edited_by: "u1",
      edited_by_name: "Jacob",
      change_note: "tidied step 2",
      created_at: "2026-11-02T00:00:00Z",
    },
    {
      id: "22222222-2222-4222-8222-222222222222",
      guide_id: "g1",
      body: "## Turn it off and on\n\nPull the plug.",
      edited_by: "u1",
      edited_by_name: "Jacob",
      change_note: null,
      created_at: "2026-10-20T00:00:00Z",
    },
    {
      id: "33333333-3333-4333-8333-333333333333",
      guide_id: "g1",
      body: "## First draft\n\nUnplug it.",
      edited_by: null,
      edited_by_name: null,
      change_note: null,
      created_at: "2026-10-01T00:00:00Z",
    },
  ];
}

interface Stub {
  /** Every non-GET request, with its parsed body: the record of what was sent. */
  writes: { method: string; url: string; body: unknown }[];
  revisions: Rev[];
  row: Row;
}

/**
 * A stateful stub of the guides API, at the paths the UI actually calls:
 * GET/PUT one guide, GET its revisions, GET one revision, POST a restore.
 * `?*` is allowed everywhere because apiFetch may add nothing but the tests
 * must not be brittle about it.
 */
async function stubGuideApi(page: Page): Promise<Stub> {
  const stub: Stub = { writes: [], revisions: seedRevisions(), row: { ...GUIDE } };

  await page.route("**/bff/me/households", (route: Route) =>
    route.fulfill({
      json: {
        data: [
          {
            id: "h1",
            name: "Sims",
            timezone: "UTC",
            role: "owner",
            joined_at: "2026-01-01T00:00:00Z",
            is_default: true,
            current: true,
          },
        ],
      },
    }),
  );

  await page.route("**/bff/guides/**", async (route: Route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    const method = req.method();

    if (method === "PUT") {
      const body = JSON.parse(req.postData() ?? "{}");
      stub.writes.push({ method, url: path, body });

      // The real handler applies the fields the body carried and writes every
      // column back. Modelled faithfully: an ABSENT key keeps the old value,
      // which is exactly why the form must send them all.
      const next: Row = { ...stub.row };
      if (body.title !== undefined) next.title = String(body.title).trim();
      if (body.summary !== undefined) next.summary = String(body.summary).trim() || null;
      if (body.body !== undefined) next.body = String(body.body);
      if (body.tags !== undefined) next.tags = (body.tags as string[]).map((t) => t.toLowerCase());
      if (body.property_id !== undefined) next.property_id = body.property_id === "" ? null : String(body.property_id);
      if (body.asset_id !== undefined) next.asset_id = body.asset_id === "" ? null : String(body.asset_id);
      if (body.vehicle_id !== undefined) next.vehicle_id = body.vehicle_id === "" ? null : String(body.vehicle_id);
      next.updated_at = "2026-11-18T12:00:00Z";
      stub.row = next;

      const rev: Rev = {
        id: "44444444-4444-4444-8444-444444444444",
        guide_id: "g1",
        body: next.body,
        edited_by: "u1",
        edited_by_name: "Jacob",
        change_note: (body.change_note as string | undefined)?.trim() || null,
        created_at: "2026-11-18T12:00:00Z",
      };
      stub.revisions = [rev, ...stub.revisions];
      return route.fulfill({ json: { data: next } });
    }

    const restore = /\/revisions\/([^/]+)\/restore$/.exec(path);
    if (restore && method === "POST") {
      stub.writes.push({ method, url: path, body: null });
      const old = stub.revisions.find((r) => r.id === restore[1]);
      if (!old) return route.fulfill({ status: 404, json: { error: "not found" } });
      // The restore writes the OLD body as a NEW revision. Nothing is removed,
      // which is the property the test below is about.
      stub.row = { ...stub.row, body: old.body, updated_at: "2026-11-18T13:00:00Z" };
      stub.revisions = [
        {
          id: "55555555-5555-4555-8555-555555555555",
          guide_id: "g1",
          body: old.body,
          edited_by: "u1",
          edited_by_name: "Jacob",
          change_note: `Restored revision ${old.id}`,
          created_at: "2026-11-18T13:00:00Z",
        },
        ...stub.revisions,
      ];
      return route.fulfill({ json: { data: stub.row } });
    }

    const one = /\/revisions\/([^/]+)$/.exec(path);
    if (one) {
      const rev = stub.revisions.find((r) => r.id === one[1]);
      if (!rev) return route.fulfill({ status: 404, json: { error: "not found" } });
      return route.fulfill({ json: { data: rev } });
    }

    if (/\/revisions$/.test(path)) {
      // The list carries body_length and no body, like the real handler.
      return route.fulfill({
        json: {
          data: stub.revisions.map((r) => ({
            id: r.id,
            guide_id: r.guide_id,
            body_length: r.body.length,
            edited_by: r.edited_by,
            edited_by_name: r.edited_by_name,
            change_note: r.change_note,
            created_at: r.created_at,
          })),
        },
      });
    }

    return route.fulfill({ json: { data: stub.row } });
  });

  return stub;
}

async function gotoGuideEdit(page: Page) {
  await page.goto("/dashboard/guides/g1");
  await expect(page.locator("[data-guide-detail]")).toBeVisible();
  await page.locator("[data-guide-edit-button]").click();
  await expect(page.locator("[data-guide-editor]")).toBeVisible();
}

async function gotoHistory(page: Page) {
  await page.goto("/dashboard/guides/g1");
  await expect(page.locator("[data-guide-detail]")).toBeVisible();
  await page.locator("[data-guide-history-button]").click();
  await expect(page.locator("[data-guide-revisions]")).toBeVisible();
}

test.describe("guides: edit and revision history (#5309)", () => {
  test("phone 390: editing a guide saves the FULL record and keeps the subject", async ({
    loggedInPage: page,
  }) => {
    onlyOn("phone");
    const stub = await stubGuideApi(page);
    await gotoGuideEdit(page);

    // The form arrives filled with what the guide says: an edit is a change,
    // not a re-type.
    // Found by ROLE and label - what a person (and a screen reader) uses, not
    // a test id. The role matters: the reading view below also has an element
    // labelled "Steps" (the list of them), so a bare label match is ambiguous.
    const steps = page.getByRole("textbox", { name: "Steps" });
    await expect(page.getByRole("textbox", { name: "Name" })).toHaveValue("Reset the router");
    await expect(steps).toHaveValue(/Pull the plug/);
    await expect(page.getByRole("textbox", { name: "Summary" })).toHaveValue(GUIDE.summary!);
    await expectNoHorizontalOverflow(page);

    // An untouched form cannot be saved: a save would add a copy to history.
    await expect(page.locator("[data-guide-save]")).toBeDisabled();

    await page.getByRole("textbox", { name: "Name" }).fill("Reset the router (2026)");
    await steps.fill("## Turn it off and on\n\nPull the plug and wait 60 seconds.");
    await page.getByRole("textbox", { name: "What changed?" }).fill("wait 60s, not 30");
    await expectNoHorizontalOverflow(page);

    await page.locator("[data-guide-save]").click();

    await expect
      .poll(() => stub.writes.filter((w) => w.method === "PUT").length, { message: "one save" })
      .toBe(1);

    const put = stub.writes.find((w) => w.method === "PUT")!;
    const body = put.body as Record<string, unknown>;

    // THE TRAP (#4565). The whole record goes, so nothing is written back from
    // a stale row and the subject the form cannot show survives.
    for (const key of ["title", "summary", "body", "tags", "change_note", "property_id", "asset_id", "vehicle_id"]) {
      expect(key in body, `the save body must carry ${key}`).toBe(true);
    }
    expect(body.title).toBe("Reset the router (2026)");
    expect(body.change_note).toBe("wait 60s, not 30");
    expect(body.property_id, "the subject is echoed, never dropped").toBe("prop-1");
    expect(body.asset_id).toBe("");

    // Unrelated fields were not wiped: the summary the form never touched is
    // still there on the saved row.
    await expect(page.locator("[data-guide-detail]")).toContainText("When the Wi-Fi drops and the lights are red.");
    expect(stub.row.summary).toBe(GUIDE.summary);
    expect(stub.row.property_id).toBe("prop-1");
    await expectNoHorizontalOverflow(page);
  });

  test("phone 390: history lists who and when, and a restore keeps the version it replaced", async ({
    loggedInPage: page,
  }) => {
    onlyOn("phone");
    const stub = await stubGuideApi(page);
    await gotoHistory(page);

    // Every version, newest first, with who made it and when.
    const rows = page.locator("[data-guide-revision]");
    await expect(rows).toHaveCount(3);
    await expect(rows.nth(0)).toContainText("Jacob");
    await expect(rows.nth(0)).toContainText("tidied step 2");
    // A version with no user row says so rather than inventing a name.
    await expect(rows.nth(2)).toContainText("Not recorded");
    // The newest is what the guide says now, and the only one you cannot
    // "restore" (it is already the current version).
    await expect(rows.nth(0)).toHaveAttribute("data-guide-revision-current", "true");
    await expect(page.locator("[data-guide-revision-restore]")).toHaveCount(2);
    await expectNoHorizontalOverflow(page);

    // A version's body is fetched only when asked for.
    await page.locator("[data-guide-revision-preview]").nth(1).click();
    await expect(page.locator("[data-guide-revision-body]")).toContainText("Pull the plug.");
    await page.getByRole("button", { name: "Close" }).click();

    // Restoring: the confirmation says the replaced version survives BEFORE
    // the tap, so the promise is on screen and not just in this test.
    await page.locator("[data-guide-revision-restore]").nth(0).click();
    const confirm = page.locator("[data-guide-restore-confirm]");
    await expect(confirm).toBeVisible();
    await expect(confirm).toContainText("stays in the history");
    await expectNoHorizontalOverflow(page);

    await page.locator("[data-guide-restore-confirm-button]").click();
    await expect
      .poll(() => stub.writes.filter((w) => w.method === "POST").length, { message: "one restore" })
      .toBe(1);
    expect(stub.writes.find((w) => w.method === "POST")!.url).toContain("/revisions/");

    // A RESTORE IS RECOVERABLE: one MORE row, not one different one, and the
    // version that was current a moment ago is still there to restore.
    await expect(rows).toHaveCount(4);
    await expect(page.locator(`[data-guide-revision="11111111-1111-4111-8111-111111111111"]`)).toHaveCount(1);
    await expect(rows.nth(0)).toContainText("Restored an earlier version");
    await expect(rows.nth(0)).toHaveAttribute("data-guide-revision-current", "true");

    // The guide now says what the restored version said.
    expect(stub.row.body).toContain("Pull the plug.");
    await expectNoHorizontalOverflow(page);
  });

  test("desktop: the editor and history fit a wide screen without overflowing", async ({
    loggedInPage: page,
  }, testInfo) => {
    onlyOn("desktop");
    await stubGuideApi(page);
    await gotoHistory(page);
    await expect(page.locator("[data-guide-revision]")).toHaveCount(3);
    await expectNoHorizontalOverflow(page);
    const path = testInfo.outputPath("desktop-guides-history.png");
    await page.screenshot({ path, fullPage: true });
    await testInfo.attach("desktop-guides-history", { path, contentType: "image/png" });
  });
});

/** The project the phone tests target, exported for the runner's report. */
export const EDIT_PHONE_PROJECT = IPHONE;
