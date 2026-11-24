// Run: npm test (node --test strips the TypeScript types natively).
//
// #4557. The UI must not invent a second trade vocabulary. The API
// (apps/api/internal/vendors) and the MCP (apps/mcp/internal/vendors) both
// load packages/db/testdata/trade_normalization.json and assert their synonym
// maps equal it; these tests do the same for components/contact/trade-vocabulary.ts,
// so a trade added on the server without the UI (or the reverse) fails here
// instead of showing up as a filter chip that matches nothing.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import {
  MAX_TRADE_LEN,
  MAX_TRADES,
  TRADE_SUGGESTIONS,
  normalizeTrade,
  normalizeTradeList,
  tradeLabel,
  tradesToField,
} from "./trade-vocabulary.ts";

const fixturePath = fileURLToPath(
  new URL("../../../../packages/db/testdata/trade_normalization.json", import.meta.url),
);
const fixture = JSON.parse(readFileSync(fixturePath, "utf8"));

test("the synonym map equals the shared fixture exactly", async () => {
  const { TRADE_SYNONYMS } = await import("./trade-vocabulary.ts");
  assert.deepEqual(TRADE_SYNONYMS, fixture.synonyms, "change the fixture first, then the Go maps, then trade-vocabulary.ts");
});

test("the limits match the fixture", () => {
  assert.equal(MAX_TRADES, fixture.max_trades);
  assert.equal(MAX_TRADE_LEN, fixture.max_trade_len);
});

test("every fixture case normalises the same way the API does", () => {
  for (const c of fixture.cases) {
    assert.equal(normalizeTrade(c.in), c.want, JSON.stringify(c.in));
  }
});

test("list normalisation: dedupe in first-seen order, blank entries dropped", () => {
  assert.deepEqual(normalizeTradeList(""), []);
  assert.deepEqual(normalizeTradeList("Plumbing, vet, plumber, Veterinarian"), ["plumber", "vet"]);
  // Blank parts are simply not trades; the page validates separately.
  assert.deepEqual(normalizeTradeList("plumber, , handyman"), ["plumber", "handyman"]);
});

test("labels read as words, never snake_case", () => {
  assert.equal(tradeLabel("pest_control"), "Pest control");
  assert.equal(tradeLabel("vet"), "Vet");
  assert.equal(tradeLabel("handyman"), "Handyman");
  assert.equal(tradeLabel(""), "");
});

test("a trade list round-trips through the form field", () => {
  assert.equal(tradesToField(["plumber", "handyman"]), "plumber, handyman");
  assert.equal(tradesToField([]), "");
  assert.equal(tradesToField(null), "");
});

test("the type-ahead offers only stored (canonical) forms", () => {
  const canonical = new Set(Object.values(fixture.synonyms));
  for (const s of TRADE_SUGGESTIONS) assert.ok(canonical.has(s), `${s} is not a canonical trade`);
  // And it is the whole vocabulary, not a subset someone typed by hand.
  assert.equal(TRADE_SUGGESTIONS.length, canonical.size);
});
