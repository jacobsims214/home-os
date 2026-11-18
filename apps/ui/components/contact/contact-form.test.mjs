// Run: npm test. The create/update bodies and display helpers for contacts
// (#4557). The PUT rules here are the ones a wrong body silently breaks:
// trades [] and address "" must CLEAR (the API keeps an omitted key), so a
// contact whose address is deleted in the form must really lose it.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  MAX_ADDRESS_LEN,
  buildContactUpdate,
  contactToFormValues,
  createContactBody,
  websiteHref,
} from "./contact-form.ts";
import {
  contactInitial,
  contactTradesLine,
  mailtoHref,
  mapsHref,
  smsHref,
  telHref,
  tradeFacets,
} from "./contact-facts.ts";

const EMPTY = contactToFormValues({ name: "Joe's Plumbing" });

test("create: a name is enough, and trades are normalised client-side", () => {
  const ok = createContactBody({ name: "  Joe's Plumbing ", trades: "Plumbing, handyman", phone: "555-0100" });
  assert.equal(ok.ok, true);
  assert.deepEqual(ok.body, { name: "Joe's Plumbing", trades: ["plumber", "handyman"], phone: "555-0100" });
});

test("create: no name is a field error, never a request", () => {
  const bad = createContactBody({ name: "   " });
  assert.equal(bad.ok, false);
  assert.match(bad.error, /name/i);
});

test("create: empty optional fields are left out, not sent blank", () => {
  const ok = createContactBody({ name: "Vet", trades: "", phone: "" });
  assert.equal(ok.ok, true);
  assert.deepEqual(ok.body, { name: "Vet" });
});

test("create: too many trades is caught before the 400", () => {
  const trades = Array.from({ length: 21 }, (_, i) => `t${i}`).join(", ");
  const bad = createContactBody({ name: "x", trades });
  assert.equal(bad.ok, false);
  assert.match(bad.error, /20/);
});

test("update: trades [] and address \"\" clear, every scalar is present", () => {
  const res = buildContactUpdate({ ...EMPTY, trades: "", address: "" });
  assert.equal(res.ok, true);
  // Clearing is explicit: an omitted key would KEEP the stored value.
  assert.deepEqual(res.body.trades, []);
  assert.equal(res.body.address, "");
  // A full replace: the keys are all there so an emptied box nulls the column.
  for (const k of ["name", "specialty", "phone", "email", "website", "notes"]) {
    assert.ok(k in res.body, `${k} must be present`);
  }
  assert.equal(res.body.specialty, null);
  assert.equal(res.body.notes, null);
});

test("update: address over 500 chars is a field error", () => {
  const res = buildContactUpdate({ ...EMPTY, address: "a".repeat(MAX_ADDRESS_LEN + 1) });
  assert.equal(res.ok, false);
  assert.match(res.error, /500/);
});

test("update: a 500-char address is allowed, and is trimmed", () => {
  const ok = buildContactUpdate({ ...EMPTY, address: `  ${"a".repeat(MAX_ADDRESS_LEN)}  ` });
  assert.equal(ok.ok, true);
  assert.equal(ok.body.address.length, MAX_ADDRESS_LEN);
});

test("update: no name is a field error", () => {
  const bad = buildContactUpdate({ ...EMPTY, name: " " });
  assert.equal(bad.ok, false);
});

test("form values round-trip a contact, including its absent fields", () => {
  const v = contactToFormValues({
    name: "Ace Pest Control", trades: ["pest_control"], phone: null, address: "12 Main St",
  });
  assert.equal(v.trades, "pest_control");
  assert.equal(v.address, "12 Main St");
  assert.equal(v.phone, "");
  const back = buildContactUpdate(v);
  assert.equal(back.ok, true);
  assert.deepEqual(back.body.trades, ["pest_control"]);
  assert.equal(back.body.address, "12 Main St");
});

test("tel/sms/mailto/maps links: real targets or nothing", () => {
  assert.equal(telHref("(555) 010-0"), "tel:5550100");
  assert.equal(telHref("+1 555 0100"), "tel:+15550100");
  assert.equal(telHref(""), null);
  assert.equal(telHref(null), null);
  assert.equal(smsHref("555-0100"), "sms:5550100");
  assert.equal(mailtoHref("office@brightspark.example"), "mailto:office@brightspark.example");
  assert.equal(mailtoHref("call the office"), null);
  assert.equal(mapsHref("12 Main St, Springfield"), "https://www.google.com/maps/search/?api=1&query=12%20Main%20St%2C%20Springfield");
  assert.equal(mapsHref("  "), null);
});

test("website: a bare host becomes a URL, junk is refused", () => {
  assert.equal(websiteHref("brightspark.example"), "https://brightspark.example");
  assert.equal(websiteHref("https://brightspark.example/x"), "https://brightspark.example/x");
  assert.equal(websiteHref("call me"), null);
  assert.equal(websiteHref(""), null);
});

test("avatar initial never renders an empty tile", () => {
  assert.equal(contactInitial("Joe's Plumbing"), "J");
  assert.equal(contactInitial("  ace"), "A");
  assert.equal(contactInitial(""), "?");
  assert.equal(contactInitial("!!!"), "?");
});

test("the trades line falls back to specialty, then to nothing", () => {
  assert.equal(contactTradesLine({ trades: ["plumber", "handyman"], specialty: null }), "plumber · handyman");
  assert.equal(contactTradesLine({ trades: [], specialty: "Plumbing" }), "Plumbing");
  assert.equal(contactTradesLine({ trades: [], specialty: null }), "");
});

test("trade facets count the family's own trades, ordered by use", () => {
  const facets = tradeFacets([
    { id: "1", household_id: "h", name: "a", trades: ["plumber", "handyman"] },
    { id: "2", household_id: "h", name: "b", trades: ["plumber"] },
    { id: "3", household_id: "h", name: "c", trades: [] },
  ]);
  assert.deepEqual(facets, [{ trade: "plumber", count: 2 }, { trade: "handyman", count: 1 }]);
});
