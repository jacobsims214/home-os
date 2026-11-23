// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import { readdirSync } from "node:fs";
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
  assert.deepEqual(
    NAV_SECTIONS.map((s) => s.id),
    ["today", "calendar", "meals", "lists", "documents", "people", "pets", "money", "guides", "settings"],
  );
  assert.deepEqual(sectionsInGroup("main").map((s) => s.label), ["Today", "Calendar", "Meals", "Lists"]);
  assert.deepEqual(sectionsInGroup("household").map((s) => s.label), [
    "Documents",
    "People & House",
    "Pets",
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
  assert.equal(activeSection("/dashboard/vehicles/123")?.id, "people");
  assert.equal(activeSection("/dashboard/properties")?.id, "people");
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
  assert.deepEqual(ids("housesitter"), [
    "today",
    "calendar",
    "meals",
    "lists",
    "people",
    "pets",
    "guides",
    "settings",
  ]);
  // A vendor reads properties/assets/maintenance only (People & House) and has
  // no pets grant, so no Pets section.
  assert.deepEqual(ids("vendor"), ["today", "people", "settings"]);
});

test("sectionsForRole: unknown role yet shows everything; an unrecognised role fails closed", () => {
  assert.equal(sectionsForRole(null).length, NAV_SECTIONS.length);
  assert.equal(sectionsForRole(undefined).length, NAV_SECTIONS.length);
  assert.deepEqual(sectionsForRole("superuser").map((s) => s.id), ["today", "people", "settings"]);
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
