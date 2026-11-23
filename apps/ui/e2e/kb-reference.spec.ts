import type { Page, Route } from "@playwright/test";
import { test, expect, expectNoHorizontalOverflow, onlyOn } from "./fixtures";

/**
 * The family reference — the KB (HOME-OS-5503).
 *
 * The ticket's acceptance criteria, written from the phone outwards:
 *
 * 1. A member can BROWSE the reference: it is reachable from the Guides
 *    section (the More sheet's tile leads there), grouped by the first part of
 *    the path, and every row opens its doc.
 * 2. A doc READS on a 390 px phone: comfortable measure, no horizontal page
 *    scroll, and a fenced block (a Wi-Fi password, a serial) scrolls inside
 *    itself rather than widening the page.
 * 3. SEARCH hits the KB: typing asks the API (`GET /kb?q=`), and the request
 *    the page actually made is asserted — a client-side filter that never
 *    called the server would pass a weaker test and ship the wrong feature.
 * 4. A housesitter can READ but is offered no write control anywhere on the
 *    surface — and the same page as a family member does offer them.
 * 5. WRITING warns before it replaces: `kb_write` is an upsert by path, so a
 *    save to an address that already holds a doc must say so, name the doc,
 *    and refuse to submit until the person confirms.
 *
 * The API is stubbed per test with page.route, so each doc body is exact and
 * each test stands alone. The pages, the path rules, the markdown parser and
 * the rendering are the real code.
 */

const ROUTER_BODY = `## The password

The network is \`SimsHouse\`. The password is on a sticker under the router.

## If it needs a reset

- Unplug it for **30 seconds**.
- Hold the pin in the back for 10 seconds.

## The serial, if you have to call the ISP

\`\`\`
SN 4f9a2c1e77b40d3a9f6e1c55a0b7d2e8f3c4a6b9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9
\`\`\`
`;

const DOCS = [
  {
    path: "how-to/network/router-password",
    title: "Router password and reset",
    body: ROUTER_BODY,
    tags: ["wifi"],
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-11-02T00:00:00Z",
    updated_by: "Jacob",
  },
  {
    path: "how-to/pets/vet-after-hours",
    title: "Vet after hours",
    body: "## Call first\n\nThe clinic's line goes to a vet on call after 6pm.",
    tags: ["pets"],
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-20T00:00:00Z",
    updated_by: "Jacob",
  },
  {
    path: "sitters/what-they-need",
    title: "What the sitter needs",
    body: "The alarm code, the vet, and the wifi.",
    tags: [],
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-22T00:00:00Z",
    updated_by: null,
  },
];

/** The list rows: the API's summary shape (no body, an excerpt instead). */
const SUMMARIES = DOCS.map((d) => ({
  path: d.path,
  title: d.title,
  tags: d.tags,
  excerpt: d.body.slice(0, 120),
  updated_at: d.updated_at,
  updated_by: d.updated_by,
}));

type Writes = { method: string; url: string; body: unknown }[];

interface Stub {
  writes: Writes;
  /** The `q` values the page actually sent to /kb. */
  queries: string[];
  role: string;
}

/** Answers the BFF the way the real one does, from `docs`. */
async function stubKb(page: Page, opts: { role?: string; docs?: typeof DOCS } = {}): Promise<Stub> {
  const docs = opts.docs ?? DOCS;
  const summaries = docs.map((d) => ({
    path: d.path,
    title: d.title,
    tags: d.tags,
    excerpt: d.body.slice(0, 120),
    updated_at: d.updated_at,
    updated_by: d.updated_by,
  }));
  const stub: Stub = { writes: [], queries: [], role: opts.role ?? "owner" };

  await page.route("**/bff/me/households", (route: Route) =>
    route.fulfill({
      json: {
        data: [
          {
            id: "h1",
            name: "Sims",
            timezone: "UTC",
            role: stub.role,
            joined_at: "2026-01-01T00:00:00Z",
            is_default: true,
            current: true,
          },
        ],
      },
    }),
  );

  // One handler for the KB's whole surface: the list, the search (?q=) and one
  // doc by path. The real API registers the path as a chi wildcard, so the
  // slashes arrive unescaped — which is what the page builds.
  await page.route(/\/bff\/kb(\?.*)?$|\/bff\/kb\/.+$/, async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    if (req.method() !== "GET") {
      let body: unknown = null;
      try {
        body = req.postDataJSON();
      } catch {
        body = req.postData();
      }
      stub.writes.push({ method: req.method(), url: url.pathname, body });
      return route.fulfill({
        status: req.method() === "PUT" ? 200 : 204,
        json:
          req.method() === "PUT"
            ? { data: { doc: { ...docs[0], path: decodeURIComponent(url.pathname.replace(/^.*\/bff\/kb\//, "")) }, created: false, note: "Doc was REPLACED." } }
            : undefined,
      });
    }
    const q = url.searchParams.get("q");
    if (q !== null) {
      stub.queries.push(q);
      // A meaning search: only the router doc, whatever the words were.
      return route.fulfill({ json: { data: [summaries[0]], total: 1, query: q } });
    }
    const path = decodeURIComponent(url.pathname.replace(/^.*\/bff\/kb\/?/, ""));
    if (path) {
      const one = docs.find((d) => d.path === path);
      if (!one) return route.fulfill({ status: 404, json: { error: "not found" } });
      return route.fulfill({ json: { data: one } });
    }
    return route.fulfill({ json: { data: summaries, total: summaries.length } });
  });
  return stub;
}

async function gotoReference(page: Page) {
  await page.goto("/dashboard/guides/reference");
  await expect(page.getByRole("heading", { level: 2, name: "Reference" })).toBeVisible();
}

// ─── browse ──────────────────────────────────────────────────

test.describe("the reference list", () => {
  test("groups docs by the first part of the path, and every row opens its doc", async ({
    loggedInPage: page,
  }) => {
    onlyOn("phone");
    await stubKb(page);
    await gotoReference(page);

    // Two top-level folders in the fixture: "how-to" and "sitters".
    const howTo = page.locator('[data-kb-group="how-to"]');
    const sitters = page.locator('[data-kb-group="sitters"]');
    await expect(howTo.getByRole("heading", { level: 3 })).toHaveText(/How to\s*\(2\)/);
    await expect(sitters.getByRole("heading", { level: 3 })).toHaveText(/Sitters\s*\(1\)/);
    await expect(page.locator("[data-kb-doc-row]")).toHaveCount(3);

    // The address is on the row: two docs can share a title, and the path is
    // how the family tells them apart.
    await expect(howTo.getByText("network/router-password")).toBeVisible();

    await page.locator('[data-kb-doc-row="how-to/network/router-password"]').click();
    await expect(page).toHaveURL(/\/dashboard\/guides\/reference\/how-to\/network\/router-password$/);
    await expect(page.getByRole("heading", { level: 1, name: "Router password and reset" })).toBeVisible();
  });

  test("is reachable from the More sheet, on the Guides tile", async ({ loggedInPage: page }) => {
    onlyOn("phone");
    await stubKb(page);
    await page.goto("/dashboard");
    await page.getByTestId("tab-bar").getByRole("button", { name: "More" }).click();
    const sheet = page.getByRole("dialog", { name: "More" });
    await expect(sheet.getByRole("link", { name: /Guides/ })).toBeVisible();
    // The tile's hint now names both halves of the section (#5503).
    await expect(sheet.getByText(/look up/i)).toBeVisible();
  });

  test("narrows to one folder with a chip, without a round trip", async ({ loggedInPage: page }) => {
    onlyOn("phone");
    const stub = await stubKb(page);
    await gotoReference(page);
    await page.getByRole("group", { name: "Filter by folder" }).getByRole("button", { name: "Sitters" }).click();
    await expect(page.locator("[data-kb-doc-row]")).toHaveCount(1);
    await expect(page.locator('[data-kb-group="sitters"]')).toBeVisible();
    expect(stub.queries, "a folder chip filters the loaded list; it is not a search").toEqual([]);
  });

  test("the switch leads to the other half of the section, and back", async ({ loggedInPage: page }) => {
    onlyOn("phone");
    await stubKb(page);
    await gotoReference(page);
    await page.getByRole("navigation", { name: "Guides and reference" }).getByRole("link", { name: /Guides/ }).click();
    await expect(page).toHaveURL(/\/dashboard\/guides$/);
    await expect(page.getByRole("heading", { level: 2, name: /Guides/ })).toBeVisible();
    await page.getByRole("navigation", { name: "Guides and reference" }).getByRole("link", { name: /Reference/ }).click();
    await expect(page).toHaveURL(/\/dashboard\/guides\/reference$/);
  });
});

// ─── read ────────────────────────────────────────────────────

test.describe("reading a doc", () => {
  test("renders markdown at 390px: headings, lists, inline code, and NO horizontal page scroll", async ({
    loggedInPage: page,
  }) => {
    onlyOn("phone");
    await stubKb(page);
    await page.goto("/dashboard/guides/reference/how-to/network/router-password");

    await expect(page.locator("[data-kb-doc-body]")).toBeVisible();
    // Headings are h2s on the page, not raw markdown.
    await expect(page.getByRole("heading", { level: 2, name: "If it needs a reset" })).toBeVisible();
    await expect(page.getByRole("heading", { level: 2, name: "The password" })).toBeVisible();
    // A list became a list, and **bold** became bold. Scoped to the SECTION:
    // the page also carries a "On this page" jump list, and counting every `li`
    // would pass or fail on how many headings the doc happens to have.
    const reset = page.locator("section", { has: page.getByRole("heading", { name: "If it needs a reset" }) });
    await expect(reset.locator("ul li")).toHaveCount(2);
    await expect(reset.locator("strong", { hasText: "30 seconds" })).toBeVisible();
    // Inline code (the network name) is a <code>, not backticks in the prose.
    await expect(page.locator("[data-kb-doc-body] code", { hasText: "SimsHouse" })).toBeVisible();

    // The address and the provenance are on the page: they are what tell a
    // reader this is the doc they meant and whether it is current.
    await expect(page.locator("[data-kb-doc-path]")).toHaveText("how-to/network/router-password");
    await expect(page.locator("[data-kb-doc-provenance]")).toContainText("Jacob");

    // THE ACCEPTANCE CRITERION: a 200-character serial inside a fence must not
    // widen the page. The <pre> scrolls inside itself.
    await expectNoHorizontalOverflow(page);
    const pre = page.locator("[data-guide-code]").first();
    await expect(pre).toBeVisible();
    const scrolls = await pre.evaluate((el) => el.scrollWidth > el.clientWidth);
    expect(scrolls, "the fenced block is the element that scrolls, not the page").toBe(true);
  });

  test("a doc that does not exist says so, with the address, instead of an empty page", async ({
    loggedInPage: page,
  }) => {
    onlyOn("phone");
    await stubKb(page);
    await page.goto("/dashboard/guides/reference/how-to/nope");
    await expect(page.locator("[data-kb-doc-error]")).toBeVisible();
    await expect(page.locator("[data-kb-doc-error]")).toContainText("how-to/nope");
  });
});

// ─── search ──────────────────────────────────────────────────

test.describe("search", () => {
  test("typing asks the KB by meaning, and the row opens the doc", async ({ loggedInPage: page }) => {
    onlyOn("phone");
    const stub = await stubKb(page);
    await gotoReference(page);

    await page.locator("[data-kb-search]").fill("the dog is sick at night");
    // The request the page ACTUALLY made — a client-side filter would leave
    // this empty and still render a plausible list.
    await expect.poll(() => stub.queries.length, { timeout: 5_000 }).toBeGreaterThan(0);
    expect(stub.queries.at(-1)).toBe("the dog is sick at night");

    await expect(page.locator("[data-kb-search-mode]")).toContainText("by meaning");
    await expect(page.locator("[data-kb-doc-row]")).toHaveCount(1);

    await page.locator('[data-kb-doc-row="how-to/network/router-password"]').click();
    await expect(page.getByRole("heading", { level: 1, name: "Router password and reset" })).toBeVisible();
  });

  test("one character is not a round trip, and clearing the box restores the grouped list", async ({
    loggedInPage: page,
  }) => {
    onlyOn("phone");
    const stub = await stubKb(page);
    await gotoReference(page);
    await page.locator("[data-kb-search]").fill("r");
    // Long enough for any debounce to have fired and then some.
    await page.waitForTimeout(600);
    expect(stub.queries, "a single letter matches everything by meaning: not worth a request").toEqual([]);

    await page.locator("[data-kb-search]").fill("router");
    await expect.poll(() => stub.queries.length, { timeout: 5_000 }).toBeGreaterThan(0);
    await page.getByRole("button", { name: "Clear search" }).click();
    await expect(page.locator("[data-kb-doc-row]")).toHaveCount(3);
    await expect(page.locator("[data-kb-search-mode]")).toHaveCount(0);
  });
});

// ─── roles ───────────────────────────────────────────────────

test.describe("roles", () => {
  test("a housesitter reads the reference and is offered no write control", async ({ loggedInPage: page }) => {
    onlyOn("phone");
    await stubKb(page, { role: "housesitter" });
    await gotoReference(page);

    // Reading is the point: the sitter needs the router doc.
    await expect(page.locator("[data-kb-doc-row]")).toHaveCount(3);
    await expect(page.locator("[data-kb-new]")).toHaveCount(0);

    await page.locator('[data-kb-doc-row="how-to/network/router-password"]').click();
    await expect(page.getByRole("heading", { level: 1, name: "Router password and reset" })).toBeVisible();
    await expect(page.locator("[data-kb-edit]")).toHaveCount(0);
    await expect(page.locator("[data-kb-delete]")).toHaveCount(0);
    await expect(page.locator("[data-kb-doc-body]")).toBeVisible();
  });

  test("a family member sees New doc, Edit and Delete", async ({ loggedInPage: page }) => {
    onlyOn("phone");
    await stubKb(page, { role: "family_member" });
    await gotoReference(page);
    await expect(page.locator("[data-kb-new]")).toBeVisible();
    await page.locator('[data-kb-doc-row="how-to/pets/vet-after-hours"]').click();
    await expect(page.locator("[data-kb-edit]")).toBeVisible();
    await expect(page.locator("[data-kb-delete]")).toBeVisible();
  });
});

// ─── write ───────────────────────────────────────────────────

test.describe("writing", () => {
  test("a save to a path that already holds a doc warns, names it, and refuses until confirmed", async ({
    loggedInPage: page,
  }) => {
    onlyOn("phone");
    const stub = await stubKb(page);
    await gotoReference(page);
    await page.locator("[data-kb-new]").click();

    const editor = page.locator("[data-kb-editor]");
    await expect(editor).toBeVisible();

    // Type an address that is already taken, the way a person would.
    await editor.locator("[data-kb-folder]").fill("how-to/network");
    await editor.locator("[data-kb-slug]").fill("router-password");
    await editor.locator("[data-kb-title]").fill("My router notes");
    await editor.locator("[data-kb-body]").fill("## Mine\n\nThe password is hunter2.");

    // The address preview is the address that will be written.
    await expect(editor.locator("[data-kb-address]")).toHaveText("how-to/network/router-password");

    // The warning: names the doc that would be lost.
    const warning = editor.locator("[data-kb-replace-warning]");
    await expect(warning).toBeVisible();
    await expect(warning).toContainText("how-to/network/router-password");
    await expect(warning).toContainText("Router password and reset");
    await expect(warning).toContainText("cannot be brought back");

    // And the save is REFUSED until the box is ticked — not merely hinted at.
    await expect(editor.locator("[data-kb-save]")).toBeDisabled();
    await editor.locator("[data-kb-confirm-replace]").check();
    await expect(editor.locator("[data-kb-save]")).toBeEnabled();

    await editor.locator("[data-kb-save]").click();
    await expect.poll(() => stub.writes.length, { timeout: 5_000 }).toBe(1);
    const put = stub.writes[0];
    expect(put.method).toBe("PUT");
    // The path is the ADDRESS, in the URL, unescaped slashes and all.
    expect(put.url).toBe("/bff/kb/how-to/network/router-password");
    expect(put.body).toEqual({
      title: "My router notes",
      body: "## Mine\n\nThe password is hunter2.",
      tags: [],
    });
  });

  test("a NEW address says nothing about replacing and saves without a confirmation", async ({
    loggedInPage: page,
  }) => {
    onlyOn("phone");
    const stub = await stubKb(page);
    await gotoReference(page);
    await page.locator("[data-kb-new]").click();

    const editor = page.locator("[data-kb-editor]");
    await editor.locator("[data-kb-folder]").fill("money");
    await editor.locator("[data-kb-slug]").fill("tax-accountant");
    await editor.locator("[data-kb-title]").fill("Our accountant");
    await editor.locator("[data-kb-body]").fill("Call Priya before April.");

    await expect(editor.locator("[data-kb-address]")).toHaveText("money/tax-accountant");
    await expect(editor.locator("[data-kb-replace-warning]")).toHaveCount(0);
    await expect(editor.locator("[data-kb-confirm-replace]")).toHaveCount(0);
    await expect(editor.locator("[data-kb-save]")).toBeEnabled();

    await editor.locator("[data-kb-save]").click();
    await expect.poll(() => stub.writes.length, { timeout: 5_000 }).toBe(1);
    expect(stub.writes[0].url).toBe("/bff/kb/money/tax-accountant");
  });

  test("the title fills the address on a new doc, and a typed address is not overwritten", async ({
    loggedInPage: page,
  }) => {
    onlyOn("phone");
    await stubKb(page);
    await gotoReference(page);
    await page.locator("[data-kb-new]").click();
    const editor = page.locator("[data-kb-editor]");

    await editor.locator("[data-kb-folder]").fill("how-to");
    await editor.locator("[data-kb-title]").fill("Reset the water heater");
    await expect(editor.locator("[data-kb-address]")).toHaveText("how-to/reset-the-water-heater");

    // Once the person types their own name, the title stops driving it.
    await editor.locator("[data-kb-slug]").fill("water-heater");
    await editor.locator("[data-kb-title]").fill("Water heater, reset");
    await expect(editor.locator("[data-kb-address]")).toHaveText("how-to/water-heater");
  });

  test("editing a doc keeps its address, and says the old text is not recoverable", async ({
    loggedInPage: page,
  }) => {
    onlyOn("phone");
    const stub = await stubKb(page);
    await page.goto("/dashboard/guides/reference/how-to/pets/vet-after-hours");
    await page.locator("[data-kb-edit]").click();

    const editor = page.locator("[data-kb-editor]");
    await expect(editor).toBeVisible();
    // The address is not editable: a doc's path IS its identity.
    await expect(editor.locator("[data-kb-slug]")).toBeDisabled();
    await expect(editor.locator("[data-kb-folder]")).toBeDisabled();
    // Editing in place is a replace, not a conflict — said without a tick.
    await expect(editor.locator("[data-kb-replace-note]")).toContainText("keeps no older versions");
    await expect(editor.locator("[data-kb-replace-warning]")).toHaveCount(0);

    await editor.locator("[data-kb-body]").fill("## Call first\n\nThe night line is 555-0100.");
    await editor.locator("[data-kb-save]").click();
    await expect.poll(() => stub.writes.length, { timeout: 5_000 }).toBe(1);
    expect(stub.writes[0].url).toBe("/bff/kb/how-to/pets/vet-after-hours");
    expect(stub.writes[0].method).toBe("PUT");
  });
});
