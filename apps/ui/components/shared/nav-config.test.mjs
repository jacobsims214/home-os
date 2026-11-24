// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  NAV_SECTIONS,
  TAB_SECTIONS,
  activeSection,
  canSeeSection,
  isSectionActive,
  pageTitle,
  sectionsForRole,
  sectionsInGroup,
} from "./nav-config.ts";

test("the built sections, in the approved order and groups", () => {
  // #4569 added Documents (Household group, between Meals and People & House,
  // which is where artifact #75 v1 draws it). #5408 added Guides, last in the
  // Household group, where artifact #75 v1 also draws it — /dashboard/guides
  // shipped in #4570 and was in no nav surface at all until this.
  // #5696 added Pets, right after People & House: it had been a `matches`
  // entry inside People, which highlights the section but reaches nothing.
  // #5704 added the House half of People & House as real sections: Properties,
  // Assets, Vehicles, Vendors and Maintenance were `matches` entries inside
  // People and nothing else, so none of them appeared on any nav surface.
  assert.deepEqual(
    NAV_SECTIONS.map((s) => s.id),
    [
      "today",
      "calendar",
      "meals",
      "lists",
      "documents",
      "people",
      "properties",
      "assets",
      "vehicles",
      "pets",
      "vendors",
      "maintenance",
      "money",
      "guides",
      "settings",
    ],
  );
  assert.deepEqual(sectionsInGroup("main").map((s) => s.label), ["Today", "Calendar", "Meals", "Lists"]);
  assert.deepEqual(sectionsInGroup("household").map((s) => s.label), [
    "Documents",
    "People & House",
    "Properties",
    "Assets",
    "Vehicles",
    "Pets",
    "Vendors",
    "Maintenance",
    "Money",
    "Guides",
  ]);
  assert.deepEqual(sectionsInGroup("you").map((s) => s.label), ["Settings"]);
  assert.deepEqual(TAB_SECTIONS.map((s) => s.id), ["today", "calendar", "meals"]);
});

// #5408: /dashboard/guides existed and worked, but no nav surface linked to it
// — the bottom tab bar, the More sheet and the drawer all draw from
// NAV_SECTIONS, and Guides had never been added back after #5199 took it out
// for having no page. This is the regression guard: Guides is a real Household
// section with a link, not a tab.
test("Guides is a Household section with a real route (#5408)", () => {
  const guides = NAV_SECTIONS.find((s) => s.id === "guides");
  assert.ok(guides, "Guides must be in nav-config or no nav surface can reach the page");
  assert.equal(guides.label, "Guides");
  assert.equal(guides.href, "/dashboard/guides");
  assert.equal(guides.group, "household", "the More sheet draws household sections as its tiles");
  assert.equal(guides.tab, undefined, "the four phone tabs are fixed by the approved shell");
  assert.equal(isSectionActive(guides, "/dashboard/guides"), true);
  assert.equal(activeSection("/dashboard/guides/9")?.id, "guides", "a single guide stays in the section");
  assert.equal(pageTitle("/dashboard/guides"), "Guides");
});

// #5696: /dashboard/pets existed and worked, and its Add dialog worked, but no
// nav surface linked to it — it was a `matches` entry inside People, and
// `matches` only decides which section a route highlights. This is the #5408
// guard repeated for the second feature it bit: a page in `matches` is not a
// page a person can reach.
test("Pets is a Household section with a real route, not just a People match (#5696)", () => {
  const pets = NAV_SECTIONS.find((s) => s.id === "pets");
  assert.ok(pets, "Pets must be in nav-config or no nav surface can reach the page");
  assert.equal(pets.label, "Pets");
  assert.equal(pets.href, "/dashboard/pets");
  assert.equal(pets.group, "household", "the More sheet draws household sections as its tiles");
  assert.equal(pets.tab, undefined, "the four phone tabs are fixed by the approved shell");
  assert.equal(isSectionActive(pets, "/dashboard/pets"), true);
  assert.equal(activeSection("/dashboard/pets/9")?.id, "pets", "a single pet stays in the section");
  assert.equal(pageTitle("/dashboard/pets"), "Pets");
  // The point of the test: no OTHER section claims the route, so the section
  // that reaches it is the section that highlights it.
  const owners = NAV_SECTIONS.filter((s) =>
    [s.href, ...(s.matches ?? [])].some((p) => p === "/dashboard/pets"),
  );
  assert.deepEqual(owners.map((s) => s.id), ["pets"], "only Pets may own /dashboard/pets");
});

// #5199: the shell shipped "Soon" pills and "Coming soon" tile copy for Lists,
// Documents and Guides — placeholder text advertising pages that do not exist
// (artifact #168 D7/D8). The approved shell design has no such badge, so a
// destination with no page is now simply absent from the nav. This test is the
// guard: no section may be planned-but-unbuilt, and the three unfinished
// destinations must not come back as inert entries.
test("only real destinations are in the nav — no placeholder/coming-soon sections (#5199)", () => {
  // Documents left this list in #4569, Lists in #4566 and Guides in #5408: all
  // three routes exist now, so all three sections are back with real hrefs
  // (checked against the filesystem below). No section may be planned-but-
  // unbuilt, and none may carry a "soon" badge.
  assert.equal(NAV_SECTIONS.find((s) => s.id === "guides")?.href, "/dashboard/guides", "the Guides section points at its real route");
  const documents = NAV_SECTIONS.find((s) => s.id === "documents");
  assert.equal(documents.href, "/dashboard/documents", "the Documents section points at its real route");
  const lists = NAV_SECTIONS.find((s) => s.id === "lists");
  assert.equal(lists.href, "/dashboard/lists", "the Lists section points at its real route");
  for (const s of NAV_SECTIONS) {
    assert.equal("comingSoon" in s, false, `${s.id} must not carry a coming-soon flag`);
    assert.equal(s.href, s.href.trim(), s.id);
  }
  // Every section's href must be a route that actually exists: checked against
  // the app directory below, so a new page needs no edit here and a deleted
  // page fails loudly instead of shipping a link to a 404.
  const routes = existingRoutes();
  for (const s of NAV_SECTIONS) {
    assert.ok(routes.has(s.href), `${s.id}: ${s.href} has no page.tsx under app/(dashboard)`);
  }
});

/** Routable paths with a page.tsx under app/(dashboard)/dashboard. */
function existingRoutes() {
  const root = fileURLToPath(new URL("../../app/(dashboard)/dashboard", import.meta.url));
  const out = new Set();
  const walk = (dir, prefix) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (entry.isDirectory()) walk(join(dir, entry.name), `${prefix}/${entry.name}`);
      else if (entry.name === "page.tsx") out.add(`/dashboard${prefix || ""}`);
    }
  };
  walk(root, "");
  return out;
}

test("activeSection: Today is the home page only, not every /dashboard route", () => {
  assert.equal(activeSection("/dashboard")?.id, "today");
  assert.equal(activeSection("/dashboard/")?.id, "today");
  assert.equal(activeSection("/dashboard/search"), null);
});

test("activeSection: today's pages map onto their sections", () => {
  assert.equal(activeSection("/dashboard/calendar")?.id, "calendar");
  // #5704: each House page owns its own routes now, so a deep link highlights
  // the section that links to it rather than People.
  assert.equal(activeSection("/dashboard/vehicles/123")?.id, "vehicles");
  assert.equal(activeSection("/dashboard/properties/9")?.id, "properties");
  assert.equal(activeSection("/dashboard/assets/a1")?.id, "assets");
  assert.equal(activeSection("/dashboard/vendors/v1")?.id, "vendors");
  assert.equal(activeSection("/dashboard/maintenance")?.id, "maintenance");
  assert.equal(activeSection("/dashboard/properties")?.id, "properties");
  assert.equal(activeSection("/dashboard/members")?.id, "people");
  assert.equal(activeSection("/dashboard/loans/9")?.id, "money");
  assert.equal(activeSection("/dashboard/bills")?.id, "money");
  assert.equal(activeSection("/dashboard/admin")?.id, "settings");
  // A prefix must end at a path segment.
  assert.equal(activeSection("/dashboard/billsx"), null);
});

test("pageTitle", () => {
  assert.equal(pageTitle("/dashboard"), "Today");
  assert.equal(pageTitle("/dashboard/calendar"), "Calendar");
  assert.equal(pageTitle("/dashboard/vehicles/abc"), "Vehicles");
  assert.equal(pageTitle("/dashboard/settings"), "Settings");
  assert.equal(pageTitle("/somewhere"), "Home OS");
});

// #5409: the shell draws every page's h1 from pageTitle, so a route with no
// entry here shipped the wrong one — /dashboard/guides read "Home OS" (the
// app's name) and every settings sub-route read "Settings" (the parent's),
// which named the same page six times over in the document outline.
test("pageTitle names each page, not its section or the app", () => {
  assert.equal(pageTitle("/dashboard/guides"), "Guides");
  assert.equal(pageTitle("/dashboard/guides/abc-123"), "Guides");
  for (const [path, title] of [
    ["/dashboard/settings/household", "Household"],
    ["/dashboard/settings/members", "Members"],
    ["/dashboard/settings/credentials", "Credentials"],
    ["/dashboard/settings/phone-calendar", "Phone calendar"],
    ["/dashboard/settings/agent-tokens", "Agent tokens"],
    ["/dashboard/settings/account", "Account"],
  ]) {
    assert.equal(pageTitle(path), title, path);
  }
  // A segment that is not a section keeps the parent's title (the page says
  // "that section doesn't exist" and is still Settings).
  assert.equal(pageTitle("/dashboard/settings/not-a-section"), "Settings");
});

// The header's title and the section page's own heading must not disagree:
// both name the section with the same word (settings-sections.ts is the list
// the SettingsFrame and the index render from).
test("each settings sub-route's title is its section's label", async () => {
  const { SETTINGS_SECTIONS } = await import("../settings/settings-sections.ts");
  for (const section of SETTINGS_SECTIONS) {
    assert.equal(pageTitle(section.path), section.label, section.path);
  }
});

test("every section has an icon and a /dashboard href", () => {
  for (const s of NAV_SECTIONS) {
    assert.ok(s.icon, s.id);
    assert.match(s.href, /^\/dashboard(\/|$)/, s.id);
  }
});

// Mirrors the API role policy (KB decisions/2026-11-15-role-policy.md).
test("sectionsForRole follows the household role policy", () => {
  const ids = (role) => sectionsForRole(role).map((s) => s.id);
  const all = NAV_SECTIONS.map((s) => s.id);
  for (const role of ["owner", "family_manager", "family_member", "viewer"]) {
    assert.deepEqual(ids(role), all, role);
  }
  // No money, documents (files) or guides for a housesitter (#4689: files read
  // is the family roles plus viewer). A viewer DOES see Documents (#4569).
  // A housesitter also gets Lists (#4566) and Guides (#5408): ResLists and
  // ResGuides both read rolesSitter, so a sitter works the family's lists and
  // reads its runbooks.
  // #5696: a housesitter also gets Pets — ResPets read is rolesSitter, and a
  // sitter may add care notes on a pet (roles.go:217).
  // #5704 splits the House pages by their own read grants, and they do not
  // agree: Properties and Assets are rolesEveryone (so no `roles`), Pets and
  // Vendors are rolesSitter, Vehicles is rolesReaders (a sitter NOT included),
  // and Maintenance is rolesFabric (a vendor instead of a sitter). A
  // housesitter therefore gets Properties, Assets, Pets and Vendors but not
  // Vehicles or Maintenance — the two grants roles.go withholds (ResVehicles,
  // ResMaintenance).
  assert.deepEqual(ids("housesitter"), [
    "today",
    "calendar",
    "meals",
    "lists",
    "people",
    "properties",
    "assets",
    "pets",
    "vendors",
    "guides",
    "settings",
  ]);
  // A vendor reads properties/assets/maintenance (ResMaintenance put the vendor
  // in its read grant) and has no vehicles, pets or vendors grant.
  assert.deepEqual(ids("vendor"), ["today", "people", "properties", "assets", "maintenance", "settings"]);
});

test("sectionsForRole: unknown role yet shows everything; an unrecognised role fails closed", () => {
  assert.equal(sectionsForRole(null).length, NAV_SECTIONS.length);
  assert.equal(sectionsForRole(undefined).length, NAV_SECTIONS.length);
  // Fails closed: only sections with no `roles` at all survive an unknown
  // role. Properties and Assets are rolesEveryone (no `roles`); Maintenance is
  // rolesFabric, so it does not.
  assert.deepEqual(sectionsForRole("superuser").map((s) => s.id), [
    "today",
    "people",
    "properties",
    "assets",
    "settings",
  ]);
  const money = NAV_SECTIONS.find((s) => s.id === "money");
  assert.equal(canSeeSection(money, "viewer"), true);
  assert.equal(canSeeSection(money, "housesitter"), false);
});

// #4568: the week plan landed, so the Meals tab points at it (#4567 pointed it
// at the recipe box only because /dashboard/meals did not exist yet). The recipe
// box stays under Meals through `matches`, and keeps its own page title.
test("Meals links to the week plan and still owns the recipe box (#4568)", () => {
  const meals = NAV_SECTIONS.find((s) => s.id === "meals");
  assert.equal(meals.href, "/dashboard/meals", "the tab links to the week plan");
  assert.equal(activeSection("/dashboard/meals")?.id, "meals");
  assert.equal(activeSection("/dashboard/meals/grocery")?.id, "meals", "the grocery preview belongs to Meals");
  assert.equal(activeSection("/dashboard/recipes")?.id, "meals", "the recipe box is one tap away, still under Meals");
  assert.equal(activeSection("/dashboard/recipes/abc-123")?.id, "meals");
  assert.equal(pageTitle("/dashboard/meals"), "Meals");
  assert.equal(pageTitle("/dashboard/meals/grocery"), "Meals");
  assert.equal(pageTitle("/dashboard/recipes"), "Recipes");
});

// #4569: the documents hub. Documents is READERS-only (the API's files read
// grant), so a viewer keeps it and a housesitter / vendor never sees it; the
// document's own page keeps the section current and its title.
test("Documents: readers only, its pages belong to the section (#4569)", () => {
  const documents = NAV_SECTIONS.find((s) => s.id === "documents");
  assert.ok(documents);
  assert.equal(documents.label, "Documents");
  assert.equal(documents.group, "household");
  for (const role of ["owner", "family_manager", "family_member", "viewer"]) {
    assert.equal(canSeeSection(documents, role), true, `${role} may read files`);
  }
  for (const role of ["housesitter", "vendor"]) {
    assert.equal(canSeeSection(documents, role), false, `${role} may not read files`);
  }
  // A single document and the hub both keep Documents current in the shell.
  assert.equal(activeSection("/dashboard/documents")?.id, "documents");
  assert.equal(activeSection("/dashboard/documents/abc-123")?.id, "documents");
  assert.equal(pageTitle("/dashboard/documents"), "Documents");
  assert.equal(pageTitle("/dashboard/documents/abc-123"), "Documents");
  // A prefix must end at a path segment.
  assert.equal(activeSection("/dashboard/documentsx"), null);
});

// ─── Every page is reachable, or it says why not (#5704) ────────────────
//
// THE DEFECT THIS GUARDS, three times over: a route that exists, works, and is
// reachable on no nav surface — only by typing the URL.
//
//   #5408  /dashboard/guides       in NO nav config at all.
//   #5696  /dashboard/pets         in `people`'s `matches` and nowhere else.
//   #5704  /dashboard/properties, /assets, /vehicles, /vendors, /maintenance
//          and the Money OVERVIEW (/dashboard/money): all `matches` entries
//          inside other sections, none of them a link.
//
// The shared cause is that `matches` LOOKS like configuration. It only decides
// which section HIGHLIGHTS a route; it draws nothing. So the test above ("every
// section's href exists") could pass while half the pages were unreachable: it
// walks the nav, not the route tree.
//
// This walks the ROUTE TREE (app/(dashboard)/dashboard/**/page.tsx) and holds
// every page to one of exactly two answers:
//
//   1. it is a section's `href` (a real link on the tab bar, the More sheet,
//      the drawer and the desktop sidebar), or
//   2. it is listed in NOT_A_SECTION with a reason AND, where it is meant to be
//      reached another way, a source file that must still contain that link.
//
// Any other page fails. A stale exemption (a route with no page any more) fails
// too, so the list cannot silently accumulate.

/**
 * Routes under /dashboard that are NOT nav sections.
 *
 * Each entry is `[route, why, reachedFrom?]`:
 *   - `why` says what the route is and why a nav section would be wrong for it.
 *   - `reachedFrom` is the PROOF, when the route is reached some other way: a
 *     `{file, contains}` pair naming a source file that must still contain that
 *     href or literal. The test reads the file and fails if the link is gone, so
 *     "reached from Bills" cannot quietly become false while the route stays
 *     exempt from needing a section.
 *   - With no `reachedFrom`, `why` must say "URL-only" and who may type it.
 *
 * Keep this sorted by route. Adding an entry is the escape hatch; deleting a
 * `reachedFrom` is how the test gets loosened, and that shows up in review.
 */
const NOT_A_SECTION = [
  // ── Detail pages: one record, reached from its list ──────────────────
  ["/dashboard/assets/[id]", "one asset; a detail page", { file: "app/(dashboard)/dashboard/assets/page.tsx", contains: "/dashboard/assets/${asset.id}" }],
  ["/dashboard/bills/[id]", "one bill; a detail page", { file: "app/(dashboard)/dashboard/bills/page.tsx", contains: "/dashboard/bills/${bill.id}" }],
  ["/dashboard/documents/[id]", "one document; a detail page", { file: "components/documents/DocumentRow.tsx", contains: "/dashboard/documents/${file.id}" }],
  ["/dashboard/guides/[id]", "one guide; a detail page", { file: "app/(dashboard)/dashboard/guides/page.tsx", contains: "/dashboard/guides/" }],
  ["/dashboard/lists/[id]", "one list; a detail page", { file: "app/(dashboard)/dashboard/lists/page.tsx", contains: "/dashboard/lists/" }],
  ["/dashboard/loans/[id]", "one loan; a detail page", { file: "app/(dashboard)/dashboard/loans/page.tsx", contains: "/dashboard/loans/${loan.id}" }],
  ["/dashboard/people/[id]", "one person; a detail page", { file: "app/(dashboard)/dashboard/people/page.tsx", contains: "/dashboard/people/" }],
  ["/dashboard/pets/[id]", "one pet; a detail page", { file: "app/(dashboard)/dashboard/pets/page.tsx", contains: "/dashboard/pets/" }],
  ["/dashboard/properties/[id]", "one property; a detail page", { file: "app/(dashboard)/dashboard/properties/page.tsx", contains: "/dashboard/properties/${property.id}" }],
  ["/dashboard/vehicles/[id]", "one vehicle; a detail page", { file: "app/(dashboard)/dashboard/vehicles/page.tsx", contains: "/dashboard/vehicles/${vehicle.id}" }],
  ["/dashboard/vendors/[id]", "one contact; a detail page", { file: "app/(dashboard)/dashboard/vendors/page.tsx", contains: "/dashboard/vendors/${contact.id}" }],

  // ── A section's own sub-view, under the section that owns it ─────────
  ["/dashboard/guides/reference", "the family KB index: the second half of Guides, one shared section by #5503's decision", { file: "components/kb/LibrarySwitch.tsx", contains: "/dashboard/guides/reference" }],
  ["/dashboard/guides/reference/[...path]", "one KB doc; reached from the reference index", { file: "components/kb/ReferenceList.tsx", contains: "/dashboard/guides/reference/${doc.path}" }],
  ["/dashboard/meals/grocery", "the grocery preview; a sub-view of Meals", { file: "app/(dashboard)/dashboard/page.tsx", contains: "/dashboard/meals/grocery" }],
  ["/dashboard/recipes", "the recipe box: a sub-view of Meals, owned by it through `matches`", { file: "app/(dashboard)/dashboard/meals/page.tsx", contains: "/dashboard/recipes" }],
  ["/dashboard/recipes/[id]", "one recipe; a sub-view of Meals, not a section", { file: "app/(dashboard)/dashboard/recipes/page.tsx", contains: "/dashboard/recipes/" }],

  // ── Settings' own section pages: Settings is the index that lists them ─
  ["/dashboard/settings/[section]", "one Settings section; the Settings index lists every section the role may open", { file: "app/(dashboard)/dashboard/settings/page.tsx", contains: "section.path" }],

  // ── Reached from a page, but not a section of their own ─────────────
  // A section is a place the tab bar, the More sheet and the drawer all draw.
  // These keep their parent's section highlighted through `matches`, which is
  // what `matches` is for — but none is exempt on a promise: each names the
  // link that reaches it.
  ["/dashboard/activity", "the full activity history: Today's feed's see-all and a person's page", { file: "components/today/ActivityFeed.tsx", contains: "/dashboard/activity" }],
  ["/dashboard/bills", "the Money section's page: Money's href is the overview, so Bills is one tap on from it", { file: "app/(dashboard)/dashboard/money/page.tsx", contains: "/dashboard/bills" }],
  ["/dashboard/loans", "one tap on from the Money overview, and from a property's mortgage", { file: "app/(dashboard)/dashboard/money/page.tsx", contains: "/dashboard/loans" }],
  ["/dashboard/members", "the roster's account side: reached from Today, the family switcher and Settings > Members", { file: "components/shared/HouseholdSwitcher.tsx", contains: "/dashboard/members" }],
  ["/dashboard/search", "a query result page: the search box on every breakpoint", { file: "components/search/SidebarSearch.tsx", contains: "/dashboard/search" }],

  // ── Deliberately URL-only ───────────────────────────────────────────
  // The only route in this file that is URL-only BY DESIGN. Instance admin is
  // not a household feature: it is the operator's console for every family on
  // the box, gated on users.is_instance_admin (a different axis from the
  // household role). A nav entry would offer it to every parent in every
  // family, and /dashboard/admin refuses everyone else — so a link from a
  // household surface would only ever open a refusal card. An instance admin
  // is expected to know the URL. (A link belongs on Settings > Account, gated
  // on the flag; that is a follow-up, noted in #5704's completion comment.)
  ["/dashboard/admin", "URL-only by design: the instance-admin console, gated on is_instance_admin, not a household page. Only an instance admin may open it"],
];

/** Routable paths with a page.tsx under app/(dashboard)/dashboard. */
function routeTree() {
  const root = fileURLToPath(new URL("../../app/(dashboard)/dashboard", import.meta.url));
  const out = [];
  const walk = (dir, prefix) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (entry.isDirectory()) walk(join(dir, entry.name), `${prefix}/${entry.name}`);
      else if (entry.name === "page.tsx") out.push(`/dashboard${prefix}`);
    }
  };
  walk(root, "");
  return out.sort();
}

test("every dashboard page is a nav section's href or a stated exception (#5704)", () => {
  const routes = routeTree();
  // Guard the walker itself: an empty tree would make the assertion below
  // vacuous, which is how a "nothing is unreachable" test lies.
  assert.ok(routes.length > 25, `route tree looks wrong: ${routes.length} pages found`);
  assert.ok(routes.includes("/dashboard"), "the walker must see the dashboard root");
  assert.ok(routes.includes("/dashboard/properties"), "the walker must see a nested page");

  const hrefs = new Set(NAV_SECTIONS.map((s) => s.href));
  const exceptions = new Set(NOT_A_SECTION.map(([route]) => route));

  const unreachable = routes.filter((r) => !hrefs.has(r) && !exceptions.has(r));
  assert.deepEqual(
    unreachable,
    [],
    "these pages have no nav section and no stated reason — a person can only reach them by typing the URL. " +
      "Either give the page a section in nav-config.ts, or list it in NOT_A_SECTION with where it IS reached from:\n" +
      unreachable.join("\n"),
  );
});

// The escape hatch has to be as strict as the rule, or it becomes a place to
// hide a page. Every exception either names a source file that still contains
// the link that reaches it, or says outright that it is URL-only and why.
test("every NOT_A_SECTION entry proves its own reachability (#5704)", () => {
  const uiRoot = fileURLToPath(new URL("../../", import.meta.url));
  for (const [route, why, reachedFrom] of NOT_A_SECTION) {
    assert.ok(typeof why === "string" && why.trim().length > 20, `${route}: an exception needs a real reason`);
    if (!reachedFrom) {
      assert.match(
        why,
        /URL-only/,
        `${route}: with no reachedFrom proof the reason must say "URL-only" and who may type it`,
      );
      continue;
    }
    const source = readFileSync(join(uiRoot, reachedFrom.file), "utf8");
    assert.ok(
      source.includes(reachedFrom.contains),
      `${route}: ${reachedFrom.file} no longer contains ${JSON.stringify(reachedFrom.contains)}, so nothing ` +
        "reaches this page any more. Fix the link, or give the page a nav section.",
    );
  }
});

test("a `matches`-only route fails, even though it highlights correctly (#5696, #5704)", () => {
  const routes = routeTree();
  const hrefs = new Set(NAV_SECTIONS.map((s) => s.href));
  const exceptions = new Set(NOT_A_SECTION.map(([route]) => route));
  // Every route some section claims through `matches` or `href`, so this can
  // tell "the section highlights here" from "the section links here".
  const claims = new Map();
  for (const s of NAV_SECTIONS) {
    for (const prefix of [s.href, ...(s.matches ?? [])]) {
      if (!claims.has(prefix)) claims.set(prefix, s.id);
    }
  }

  const offenders = [];
  for (const route of routes) {
    if (hrefs.has(route) || exceptions.has(route)) continue;
    if (!claims.has(route)) continue;
    // Somebody put it in a `matches` array and stopped there. That is the bug.
    offenders.push(`${route} — in ${claims.get(route)}'s matches, but no section links to it`);
  }
  assert.deepEqual(offenders, [], offenders.join("\n"));
});

// The seven routes #5696 and #5704 fixed, named individually so a future edit
// that moves one back into a parent's `matches` is caught by name, not only by
// the generic sweep above.
test("each route that used to be URL-only is a section href (#5696, #5704)", () => {
  const hrefs = new Map(NAV_SECTIONS.map((s) => [s.href, s.id]));
  const fixed = {
    "/dashboard/pets": "pets", // #5696
    "/dashboard/properties": "properties", // #5704
    "/dashboard/assets": "assets",
    "/dashboard/vehicles": "vehicles",
    "/dashboard/vendors": "vendors",
    "/dashboard/maintenance": "maintenance",
    "/dashboard/money": "money",
  };
  for (const [route, id] of Object.entries(fixed)) {
    assert.equal(hrefs.get(route), id, `${route} must be the ${id} section's own href`);
    // Exactly one section may own a route. Another section listing it is the
    // #5696/#5704 shape: it highlights and reaches nothing.
    const otherOwners = NAV_SECTIONS.filter(
      (s) => s.id !== id && [...(s.matches ?? []), s.href].includes(route),
    );
    assert.deepEqual(
      otherOwners.map((s) => s.id),
      [],
      `${route} is ${id}'s href, so no other section may also claim it`,
    );
  }
});

test("NOT_A_SECTION has no stale entries, no duplicates and no section", () => {
  const routes = new Set(routeTree());
  const hrefs = new Set(NAV_SECTIONS.map((s) => s.href));
  const seen = new Set();
  for (const [route] of NOT_A_SECTION) {
    assert.equal(seen.has(route), false, `${route} is listed twice`);
    seen.add(route);
    assert.ok(routes.has(route), `${route} is listed as not-a-section but no page.tsx exists — delete the entry`);
    assert.equal(hrefs.has(route), false, `${route} is a section now, so it must leave NOT_A_SECTION`);
  }
});

test("removing a section makes this suite report the page unreachable (revert-check)", () => {
  // The regression stated as a property: take a page reachable only because a
  // section links it, drop that section in memory, and the sweep above must
  // report the page. (The on-disk revert-check — removing Properties from
  // nav-config.ts and watching this file go red — is in the ticket's note.)
  const routes = routeTree();
  const exceptions = new Set(NOT_A_SECTION.map(([route]) => route));

  for (const id of ["properties", "assets", "vehicles", "vendors", "maintenance", "pets"]) {
    const section = NAV_SECTIONS.find((s) => s.id === id);
    assert.ok(section, `${id} must be a section`);
    const withoutIt = NAV_SECTIONS.filter((s) => s.id !== id);
    const hrefs = new Set(withoutIt.map((s) => s.href));
    const unreachable = routes.filter((r) => !hrefs.has(r) && !exceptions.has(r));
    assert.ok(
      unreachable.includes(section.href),
      `with the ${id} section removed, ${section.href} must be reported unreachable`,
    );
  }
});

test("each fixed section is a Household tile, so it is two taps from the phone (#5704)", () => {
  // "Two taps" is a property of nav-config, not of a rendered tap target: the
  // More sheet draws one tile per Household section, so tap 1 is More and tap 2
  // is the tile. A House page left in a parent's `matches` has no tile at all —
  // reachable in zero taps, which is the bug.
  const householdTiles = sectionsInGroup("household").map((s) => s.id);
  for (const id of ["properties", "assets", "vehicles", "vendors", "maintenance", "pets", "money"]) {
    assert.ok(householdTiles.includes(id), `${id} must be a Household More-sheet tile (two taps from Today)`);
  }
  // The tab bar is the four fixed tabs; no House page is one, and none may
  // become one (the approved shell fixes them, artifact #75 v1).
  assert.deepEqual(TAB_SECTIONS.map((s) => s.id), ["today", "calendar", "meals"]);
});
