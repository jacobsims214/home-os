// Run: npm test (node --test strips the TypeScript types natively, like
// nav-config.test.mjs). Guards the display rules #4563 is about: the actor and
// the action must stay legible, and the mark-read shape must stay per-row.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  aboutLabel,
  absoluteTime,
  entityHref,
  groupNotifications,
  isSharedReadState,
  notificationKindLabel,
  notificationScope,
  parseNotificationTitle,
  relativeTime,
  scopeLabel,
  unreadCount,
} from "./notification-model.ts";

const row = (over = {}) => ({
  id: "n1",
  type: "item_changed",
  title: "Amber updated bill \"Internet\" (amount)",
  created_at: "Mon, 16 Nov 2026 09:00:00 +0000",
  ...over,
});

// ---------- actor + action ----------

test("item_changed: the actor and the action are separated, and nothing is dropped", () => {
  assert.deepEqual(parseNotificationTitle(row()), {
    actor: "Amber",
    via: null,
    verb: "updated",
    rest: "bill \"Internet\" (amount)",
  });

  // The agent that acted is part of who did it (migration 054 writes
  // "<actor> (via <agent>)"), so it must survive the parse.
  assert.deepEqual(
    parseNotificationTitle(
      row({ title: "Amber (via moltis) deleted event \"Date night\" on Sat Nov 28" }),
    ),
    { actor: "Amber", via: "moltis", verb: "deleted", rest: "event \"Date night\" on Sat Nov 28" },
  );

  // A name with a space in it, and a label with the word "deleted" in it.
  assert.deepEqual(
    parseNotificationTitle(row({ title: "Dana Kim updated list \"Deleted scenes\"" })),
    { actor: "Dana Kim", via: null, verb: "updated", rest: "list \"Deleted scenes\"" },
  );

  // An agent-only name still yields that name.
  assert.equal(
    parseNotificationTitle(row({ title: "moltis created list \"Costco\"" }))?.actor,
    "moltis",
  );
});

test("the rest of the title is never truncated", () => {
  const rest = `bill "${"x".repeat(300)}" (amount, notes, due_day, category, payment_url, +2 more)`;
  const parsed = parseNotificationTitle(row({ title: `Amber updated ${rest}` }));
  assert.equal(parsed.verb, "updated");
  assert.equal(parsed.rest, rest);
});

test("other types keep their title as the subject (no actor to split)", () => {
  // bill_due: "<bill name> due soon", family-wide, written with no actor.
  assert.equal(
    parseNotificationTitle({ type: "bill_due", title: "Duke Energy due soon" }),
    null,
  );
  // calendar_alarm: the event's own name.
  assert.equal(
    parseNotificationTitle({ type: "calendar_alarm", title: "Uncle Kevin arrives" }),
    null,
  );
  // A title that doesn't match the writer's shape falls back, never throws.
  assert.equal(parseNotificationTitle(row({ title: "Anything at all" })), null);
  assert.equal(parseNotificationTitle(row({ title: "" })), null);
});

// ---------- scope: whose unread state a row is ----------

test("family-wide types are the shared-read_at ones (bill_due, maintenance_due)", () => {
  // The writers in apps/worker/cmd/worker/main.go insert these with no user_id,
  // so every member sees them and read_at is one value for the family.
  assert.equal(notificationScope({ type: "bill_due" }), "family");
  assert.equal(notificationScope({ type: "maintenance_due" }), "family");
  assert.equal(isSharedReadState({ type: "bill_due" }), true);
  assert.equal(isSharedReadState({ type: "maintenance_due" }), true);
});

test("personal types are one member's own (calendar_alarm, item_changed)", () => {
  assert.equal(notificationScope({ type: "calendar_alarm" }), "personal");
  assert.equal(notificationScope({ type: "item_changed" }), "personal");
  assert.equal(isSharedReadState({ type: "calendar_alarm" }), false);
  assert.equal(isSharedReadState({ type: "item_changed" }), false);
});

test("an unknown type is not guessed either way", () => {
  // POST /api/v1/notifications can write any type, with or without a user_id,
  // so a type the UI doesn't know gets no scope claim.
  assert.equal(notificationScope({ type: "something_new" }), "unknown");
  assert.equal(scopeLabel("unknown"), null);
  assert.equal(isSharedReadState({ type: "something_new" }), false);
});

test("scope labels are words, not colours", () => {
  assert.equal(scopeLabel("family"), "Family-wide");
  assert.equal(scopeLabel("personal"), "Only you");
});

test("kind labels", () => {
  assert.equal(notificationKindLabel({ type: "bill_due" }), "Bill reminder");
  assert.equal(notificationKindLabel({ type: "maintenance_due" }), "Upkeep reminder");
  assert.equal(notificationKindLabel({ type: "calendar_alarm" }), "Calendar reminder");
  assert.equal(notificationKindLabel({ type: "item_changed" }), "Change notice");
  assert.equal(notificationKindLabel({ type: "whatever" }), "Notification");
});

// ---------- links ----------

test("entityHref links only to pages that exist", () => {
  assert.equal(entityHref("bill", "b1"), "/dashboard/bills/b1");
  assert.equal(entityHref("calendar_event", "e1"), "/dashboard/calendar");
  assert.equal(entityHref("maintenance_task", "m1"), "/dashboard/maintenance");
  assert.equal(entityHref("property", "p1"), "/dashboard/properties/p1");

  // No page yet: no link rather than the old silent fallback to /dashboard.
  for (const kind of ["list", "note", "document_link", "secret", "budget", "guide"]) {
    assert.equal(entityHref(kind, "x1"), null, `${kind} should not link yet`);
  }
  // A row with no entity at all.
  assert.equal(entityHref(undefined, undefined), null);
  // A kind that needs an id but has none.
  assert.equal(entityHref("bill", undefined), null);
});

test("aboutLabel names the thing being opened", () => {
  assert.equal(aboutLabel("bill"), "bill");
  assert.equal(aboutLabel("calendar_event"), "event");
  assert.equal(aboutLabel("maintenance_task"), "upkeep task");
  assert.equal(aboutLabel("secret"), "credential");
  assert.equal(aboutLabel("unknown_kind"), null);
  assert.equal(aboutLabel(undefined), null);
});

// ---------- time ----------

test("relativeTime: minutes, hours, days, then a date", () => {
  const now = Date.parse("2026-11-17T18:00:00Z");
  const at = (iso) => relativeTime(iso, now);

  assert.equal(at("2026-11-17T17:59:50Z"), "just now");
  assert.equal(at("2026-11-17T17:30:00Z"), "30 minutes ago");
  assert.equal(at("2026-11-17T15:00:00Z"), "3 hours ago");
  assert.equal(at("2026-11-16T18:00:00Z"), "yesterday");
  assert.equal(at("2026-11-14T18:00:00Z"), "3 days ago");
  // A week or more: the date instead, so "23 days ago" never appears.
  assert.match(at("2026-11-01T18:00:00Z"), /^Nov 1$/);
  // Another year keeps its year.
  assert.match(at("2025-12-30T18:00:00Z"), /^Dec 30, 2025$/);
});

test("relativeTime reads Go's http.TimeFormat (what the API sends)", () => {
  const now = Date.parse("2026-11-16T10:00:00Z");
  assert.equal(relativeTime("Mon, 16 Nov 2026 09:00:00 +0000", now), "1 hour ago");
  assert.equal(relativeTime("Mon, 16 Nov 2026 09:00:00 GMT", now), "1 hour ago");
  // An unparseable value is empty, not "Invalid Date".
  assert.equal(relativeTime("not a date", now), "");
  assert.equal(relativeTime("", now), "");
});

test("absoluteTime is the exact local time for the row's title attribute", () => {
  // Built from a fixed instant, so the assertion holds in any TZ.
  const s = absoluteTime("Mon, 16 Nov 2026 09:00:00 +0000");
  assert.ok(s.length > 0);
  assert.equal(absoluteTime("nope"), "");
});

// ---------- grouping and counts ----------

test("unread rows are New, locally-read rows move to Earlier", () => {
  const items = [
    row({ id: "a" }),
    row({ id: "b", read_at: "2026-11-17T18:00:00Z" }),
    row({ id: "c", type: "bill_due", title: "Duke Energy due soon" }),
  ];
  assert.deepEqual(
    groupNotifications(items).map((g) => [g.key, g.items.map((n) => n.id)]),
    [
      ["new", ["a", "c"]],
      ["earlier", ["b"]],
    ],
  );
  assert.equal(unreadCount(items), 2);

  // Only one group exists at a time on a fresh list, so no empty "Earlier".
  assert.deepEqual(groupNotifications([row({ id: "a" })]).map((g) => g.key), ["new"]);
  assert.deepEqual(groupNotifications([]), []);
});

// ---------- the mark-read shape ----------

test("mark-read is per row: there is no bulk endpoint to call", async () => {
  // #4947 removed the MCP `all` flag after an agent marked the household's
  // whole unread state read. The REST API never had one, and the UI must not
  // invent one: PATCH /api/v1/notifications/{id}/read names a single id.
  const { readFileSync } = await import("node:fs");
  const here = new URL(".", import.meta.url).pathname;
  const api = readFileSync(here + "use-notifications.ts", "utf8");

  assert.match(api, /\/api\/v1\/notifications\/\$\{encodeURIComponent\(id\)\}\/read/);
  assert.match(api, /method: "PATCH"/);
  // No mark-all request of any spelling.
  for (const banned of ["read-all", "mark-all", "markAll", "all: true", "\"all\"", "read/all"]) {
    assert.ok(!api.includes(banned), `use-notifications.ts must not mention ${banned}`);
  }
  // The panel has no mark-all control either: no button or link labelled
  // "Mark all …", only the per-row one (whose label is "Mark read").
  const panel = readFileSync(here + "NotificationsPanel.tsx", "utf8");
  assert.ok(!/>\s*Mark all/i.test(panel), "the panel must not render a mark-all control");
  assert.match(panel, /Mark read/);
});
