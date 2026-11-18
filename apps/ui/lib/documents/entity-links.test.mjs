// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import { attachmentOf, documentLinks, entityHref, entityNoun, metadataPatch, safeUrl, tagsForApi } from "./entity-links.ts";

test("attachmentOf names the record a document belongs to, and routes only to pages that exist", () => {
  assert.deepEqual(attachmentOf({ entity_type: "bill", entity_id: "b1" }), {
    kind: "bill",
    id: "b1",
    noun: "Bill",
    href: "/dashboard/bills/b1",
  });
  assert.equal(attachmentOf({ entity_type: "vehicle", entity_id: "v1" }).href, "/dashboard/vehicles/v1");
  // A kind with no page of its own still names the record, without a dead link.
  const task = attachmentOf({ entity_type: "maintenance_task", entity_id: "t1" });
  assert.equal(task.noun, "Upkeep task");
  assert.equal(task.href, null);
  // A family-level document is not "attached to" anything: the details say
  // "the whole family" rather than linking to a household page that isn't one.
  assert.equal(attachmentOf({ entity_type: "household", entity_id: "h1" }), null);
  assert.equal(attachmentOf({ entity_type: "", entity_id: "" }), null);
  assert.equal(attachmentOf({}), null);
});

test("entityNoun and entityHref are the naming rules for a kind", () => {
  assert.equal(entityNoun("bill"), "Bill");
  assert.equal(entityNoun("meal_plan"), "Meal plan");
  assert.equal(entityNoun("something_new"), "something new", "an unknown kind is readable, not blank");
  assert.equal(entityNoun(null), "Record");
  assert.equal(entityHref("bill", "b1"), "/dashboard/bills/b1");
  assert.equal(entityHref("secret", "s1"), null, "a credential is not deep-linked from a document");
  assert.equal(entityHref("bill", null), null);
});

test("safeUrl only lets http(s) through, so a stored javascript: URL cannot run", () => {
  assert.equal(safeUrl("https://www.progressive.com/policy"), "https://www.progressive.com/policy");
  assert.equal(safeUrl("http://example.com/x"), "http://example.com/x");
  for (const bad of ["javascript:alert(1)", "data:text/html,<script>x</script>", "ftp://x/y", "not a url", "", null, undefined]) {
    assert.equal(safeUrl(bad), null, String(bad));
  }
});

test("documentLinks labels each link kind and keeps an unopenable url visible", () => {
  const links = documentLinks([
    { id: "l1", entity_type: "file", entity_id: "f1", link_type: "url", link_id: "u1", title: "Progressive policy", url: "https://progressive.com", created_at: "" },
    { id: "l2", entity_type: "file", entity_id: "f1", link_type: "file", link_id: "f2", title: "The renewal letter", created_at: "" },
    { id: "l3", entity_type: "file", entity_id: "f1", link_type: "secret", link_id: "s1", title: "Bank login", created_at: "" },
    { id: "l4", entity_type: "file", entity_id: "f1", link_type: "url", link_id: "u2", title: "Broken", url: "javascript:alert(1)", created_at: "" },
  ]);
  assert.deepEqual(links.map((l) => l.kind), ["url", "file", "secret", "url"]);
  assert.equal(links[0].href, "https://progressive.com/");
  assert.equal(links[0].external, true);
  assert.equal(links[1].href, "/dashboard/documents/f2", "a file link opens the other document");
  assert.equal(links[2].href, null, "a credential is named, not deep-linked");
  assert.match(links[2].detail, /Settings/);
  assert.equal(links[3].href, null);
  assert.match(links[3].detail, /can't be opened/);
  assert.deepEqual(documentLinks(undefined), []);
});

test("tagsForApi trims, drops blanks and dedupes — the same rule the API stores", () => {
  assert.deepEqual(tagsForApi(["  auto ", "auto", "", "utility"]), ["auto", "utility"]);
  assert.deepEqual(tagsForApi(null), []);
});

test("metadataPatch sends only what changed: absent keeps, null clears", () => {
  const original = {
    title: "Odyssey policy",
    category: "insurance",
    document_date: "2026-10-30",
    expires_on: "2026-12-01",
    description: "Scanned at the counter",
    person_id: "p1",
    tags: ["auto"],
  };
  // Nothing changed → an empty body (the API 400s on an empty patch, so this
  // must be empty rather than a no-op full write).
  assert.deepEqual(
    metadataPatch(original, {
      title: "Odyssey policy",
      category: "insurance",
      documentDate: "2026-10-30",
      expiresOn: "2026-12-01",
      description: "Scanned at the counter",
      personId: "p1",
      tags: ["auto"],
    }),
    {},
  );

  // Clearing the title and description sends null, not "" (the API's jsonopt
  // reads null as clear and a string as a value).
  const cleared = metadataPatch(original, {
    title: "  ",
    category: "insurance",
    documentDate: "2026-10-30",
    expiresOn: "2026-12-01",
    description: "",
    personId: "p1",
    tags: ["auto"],
  });
  assert.deepEqual(cleared, { title: null, description: null });

  // Only the changed fields are in the body.
  const changed = metadataPatch(original, {
    title: "Odyssey policy renewal",
    category: "vehicle",
    documentDate: "2026-10-31",
    expiresOn: "",
    description: "Scanned at the counter",
    personId: null,
    tags: ["auto", "odyssey"],
  });
  assert.deepEqual(changed, {
    title: "Odyssey policy renewal",
    category: "vehicle",
    document_date: "2026-10-31",
    expires_on: null,
    person_id: null,
    tags: ["auto", "odyssey"],
  });
});
