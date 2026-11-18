// Run: npm test (node --test strips the TypeScript types natively).
import { test } from "node:test";
import assert from "node:assert/strict";
import { newVetBody, vetContactsKey } from "./vet-utils.ts";

test("a new vet contact is a vendor with trade vet", () => {
  assert.deepEqual(newVetBody({ name: "  Oak Vet Clinic ", phone: " 555-0199 " }), {
    ok: true,
    body: { name: "Oak Vet Clinic", phone: "555-0199", specialty: "Veterinarian", trades: ["vet"] },
  });
  assert.deepEqual(newVetBody({ name: "Oak", phone: "  " }).body, {
    name: "Oak",
    specialty: "Veterinarian",
    trades: ["vet"],
  });
});

test("a new vet contact needs a name", () => {
  const r = newVetBody({ name: "   ", phone: "555" });
  assert.equal(r.ok, false);
  assert.ok(r.errors.name);
});

test("the vet list query sits under the vendor key", () => {
  assert.equal(vetContactsKey[0], "vendor");
});
