// Run: npm test. The rule, made enforceable (#5316).
//
// A family's "today" is the HOUSEHOLD's today, not the device's. The way that
// rule gets broken is a page calling `dayjs()` / `new Date()` / `Date.now()` and
// deciding from it what day it is for this family. Three tickets in one day
// (#5305 meals, #5300 members, #5307 calendar) were the same mistake.
//
// So this test reads every source file in apps/ui and FAILS on a direct
// current-date read, unless the site is listed below WITH A REASON. Adding a
// reason is cheap; adding one you cannot justify is not, which is the point.
//
// Two kinds of site are legitimately allowed to look at the clock:
//
//   1. THE ONE HELPER — `householdToday(timezone)` in lib/today/agenda.ts, which
//      turns "now" into the household's civil date. Every page calls that
//      instead of reading the clock itself.
//   2. INSTANT arithmetic — a timestamp diff ("2h ago"), a JWT expiry, a
//      `checked_at` we are about to send. An instant is the same instant in
//      every time zone, so reading it never answers "what day is it here?", and
//      no browser can disagree with the household about it.
//
// Anything that decides a CIVIL DAY (today / this week / overdue / expired) is
// a defect if it reads the clock directly: route it through householdToday.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative, sep } from "node:path";

const ui = join(new URL(".", import.meta.url).pathname, "..", "..");
const SRC_EXT = new Set([".ts", ".tsx"]);

/** Every source file under apps/ui, minus the ones that cannot affect a page. */
function sources(dir, out = []) {
  for (const entry of readdirSync(dir)) {
    if (entry === "node_modules" || entry.startsWith(".")) continue;
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      // e2e specs run in the test runner against a stub server: their clock is
      // the harness's, never a family's. public/ is build output.
      if (entry === "e2e" || entry === "public") continue;
      sources(full, out);
      continue;
    }
    if (SRC_EXT.has(entry.slice(entry.lastIndexOf(".")))) out.push(full);
  }
  return out;
}

/**
 * A construct that reads the ambient clock, or renders a civil date in the
 * ambient zone. Each `re` matches CODE only (comments are stripped first).
 */
const CONSTRUCTS = [
  { id: "dayjs()", re: /\bdayjs\(\s*\)/, why: "dayjs() is the browser's clock" },
  { id: "new Date()", re: /\bnew Date\(\s*\)/, why: "new Date() is the browser's clock" },
  { id: "Date.now()", re: /\bDate\.now\(\s*\)/, why: "Date.now() is the browser's clock" },
  { id: "moment()", re: /\bmoment\(\s*\)(?![.])/, why: "moment() is the browser's clock" },
  // A locale DATE format with no explicit timeZone renders the parse zone's
  // civil date: for an ISO instant that is the BROWSER's day, which is exactly
  // the divergence. `timeZone` may be "UTC" or the household's, but it must be
  // given. A locale NUMBER format (currency, digits) carries no day, so only
  // the date-taking call is scanned.
  {
    id: "toLocaleDateString(no timeZone)",
    re: /\.toLocaleDateString\(/,
    why: "renders the browser's civil date",
  },
  // The same fingerprint on the timestamp class: Intl.format / formatToParts
  // with no timeZone renders the browser's zone.
];

/** True when this `toLocaleDateString` call is missing an explicit timeZone. */
function localeDateWithoutZone(code) {
  const at = code.indexOf(".toLocaleDateString(");
  if (at === -1) return false;
  // Take the call's own text: to the matching close paren.
  let depth = 0;
  let end = code.length;
  for (let i = at + ".toLocaleDateString".length; i < code.length; i++) {
    if (code[i] === "(") depth++;
    else if (code[i] === ")") {
      depth--;
      if (depth === 0) {
        end = i;
        break;
      }
    }
  }
  const call = code.slice(at, end + 1);
  return !/timeZone\s*:/.test(call);
}

/**
 * Sites that may read the clock, each with the reason it is not a defect.
 *
 * `constructs` narrows an allowance to those constructs in that file: the
 * helper may call `new Date()`, and nothing else in it may. A stale entry is a
 * test failure of its own (second test below), so the list cannot rot.
 */
const ALLOWED = [
  {
    path: "app/(dashboard)/dashboard/page.tsx",
    constructs: ["new Date()"],
    reason:
      "Today is the reference consumer: it ticks an instant and hands it to householdToday(timezone, now), which is the helper's contract (the same tick the shared hook makes). Consolidating this page is #4549's job, not this ticket's.",
  },
  {
    path: "lib/today/agenda.ts",
    constructs: ["new Date()"],
    reason:
      "THE helper. householdToday(timezone) is the one place a clock is read to decide the family's civil day (#4549); its `now` default is what every page ends up using.",
  },
];

/**
 * Files whose every construct is legitimate INSTANT arithmetic: a timestamp
 * difference, an expiry, or an instant being written to the API. None of them
 * answers "what day is it for this family?".
 */
const INSTANT_FILES = [
  ["app/bff/[...path]/route.ts", "Token expiry and cache TTL: `exp - Date.now()/1000` is an instant."],
  ["app/api/auth/callback/route.ts", "JWT `exp` comparison: instants."],
  ["stores/recent.ts", "A recents row is stamped with the instant it was opened."],
  ["lib/lists/api.ts", "`checked_at` on an optimistic tick is the instant of the tap."],
  ["lib/lists/optimistic.ts", "`checked_at` for the request we are about to send: the instant of the tap."],
  ["components/notifications/use-notifications.ts", "`read_at` is the instant the bell row was opened."],
  ["components/notifications/notification-model.ts", "relativeTime compares instants ('2h ago'); its date label passes timeZone: UTC."],
  ["lib/members/roster.ts", "inviteExpiry compares an invite's instant against now ('expires in 2 days')."],
  ["lib/calendar/model.ts", "relativeWhen/attributionMap compare an entry's instant against now ('12 min ago')."],
  ["lib/lists/model.ts", "clockTime/checkedLabel/updatedLabel compare a checked_at instant against now ('6:12 PM', '2 min ago')."],
  [
    "components/maintenance/MaintenanceKanban.tsx",
    "`completed_at` when a task is marked done: the instant of the click, sent to the API.",
  ],
  [
    "app/(dashboard)/dashboard/admin/page.tsx",
    "Zone NAME only, like HouseholdSection: an admin row names a family's zone; the day is never read.",
  ],
  [
    "components/settings/HouseholdSection.tsx",
    "Zone NAME only: Intl reads the zone's current name ('Central Time'). The date is discarded, and the zone is passed in explicitly.",
  ],
  [
    "components/settings/TimezoneField.tsx",
    "Zone NAME only, same as HouseholdSection: the day is never read.",
  ],
];

const norm = (p) => p.split(sep).join("/");

/** Every construct hit in the tree: {file, line, construct, text}. */
export function scan() {
  const hits = [];
  for (const file of sources(ui)) {
    const rel = norm(relative(ui, file));
    const raw = readFileSync(file, "utf8");
    // Comments are stripped so that prose explaining the rule (this project
    // documents its why in comments) is never read as a violation. Blanked
    // rather than removed, so line numbers still match the file.
    const code = raw
      .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, " "))
      .replace(/(^|[^:])\/\/.*$/gm, "$1");
    // A call may span lines (`toLocaleDateString("en-US", {\n timeZone`), so the
    // zone check runs on the whole call's text, located by its line.
    for (const c of CONSTRUCTS) {
      const re = new RegExp(c.re.source, "g");
      for (const m of code.matchAll(re)) {
        if (c.id === "toLocaleDateString(no timeZone)" && !localeDateWithoutZone(code.slice(m.index))) continue;
        const line = code.slice(0, m.index).split("\n").length;
        hits.push({ file: rel, line, construct: c.id, text: raw.split("\n")[line - 1].trim() });
      }
    }
  }
  return hits;
}

const allowed = (hit) =>
  ALLOWED.some((a) => a.path === hit.file && a.constructs.includes(hit.construct)) ||
  INSTANT_FILES.some(([p]) => p === hit.file);

test("no page decides the family's day from the browser's clock (#5316)", () => {
  const offenders = scan().filter((h) => !allowed(h));
  assert.deepEqual(
    offenders.map((h) => `${h.file}:${h.line}  ${h.construct}  -  ${h.text}`),
    [],
    `These read the current date/time directly. A "today" must come from
householdToday(household?.timezone) in lib/today/agenda.ts, computed after
mount on a 60 s tick and null until the household is known. If the site is
instant arithmetic (a diff, an expiry, a timestamp being written), add it to
INSTANT_FILES or ALLOWED in this file WITH A REASON.`,
  );
});

test("the allowlist has no entries that no longer occur", () => {
  const seen = new Set(scan().map((h) => `${h.file}|${h.construct}`));
  const dead = [
    ...ALLOWED.filter((a) => !a.constructs.some((c) => seen.has(`${a.path}|${c}`))).map(
      (a) => `${a.path} (${a.constructs.join(", ")})`,
    ),
    ...INSTANT_FILES.filter(([p]) => ![...seen].some((s) => s.startsWith(`${p}|`))).map(([p]) => p),
  ];
  assert.deepEqual(
    dead,
    [],
    "These allowlist entries match nothing any more: the site was fixed or moved, so drop the " +
      "entry in the same change. A stale allowance is how the next defect gets waved through.",
  );
});
