/**
 * The trades vocabulary (#4557) — the UI's view of the ONE list the API and
 * MCP already share, not a second one.
 *
 * Source of truth: `packages/db/testdata/trade_normalization.json`, which
 * `apps/api/internal/vendors` (NormalizeTrade) and
 * `apps/mcp/internal/vendors` (normalizeTrade) both load and assert against
 * (KB apis/vendors.md). This module mirrors that map exactly, and
 * `trade-vocabulary.test.mjs` reads the fixture and fails if the two drift —
 * the same guard the Go tests use. So: change the fixture first, then the Go
 * maps, then this file.
 *
 * Why a mirror instead of importing the JSON: `packages/db/testdata` is
 * outside the Next.js app root, so the bundler cannot reach it. The map is
 * small and the test makes drift impossible to miss.
 *
 * What the UI uses this for:
 * - `normalizeTrade` — a chip's value must be the string the API stores
 *   ("Plumbing" is stored as "plumber"), so filters and form input normalise
 *   the same way the server will.
 * - `tradeLabel` — "pest_control" is stored snake_case; people read
 *   "Pest control".
 * - `TRADE_SUGGESTIONS` — the canonical stored forms, offered as a datalist
 *   type-ahead in the create/edit form. It is derived from the synonym map,
 *   so it is the same vocabulary, never a hand-written extras list.
 */

/** Mirrors `tradeSynonyms` in apps/api/internal/vendors/handler.go (043). */
export const TRADE_SYNONYMS: Record<string, string> = {
  heating: "hvac",
  "heating_&_cooling": "hvac",
  heating_and_cooling: "hvac",
  ac: "hvac",
  plumbing: "plumber",
  electrical: "electrician",
  electric: "electrician",
  landscaping: "landscaper",
  lawn_care: "landscaper",
  lawn: "landscaper",
  gardener: "landscaper",
  roofing: "roofer",
  exterminator: "pest_control",
  pest: "pest_control",
  cleaning: "cleaner",
  house_cleaning: "cleaner",
  housekeeper: "cleaner",
  pool_service: "pool",
  auto_repair: "mechanic",
  veterinarian: "vet",
  veterinary: "vet",
  animal_hospital: "vet",
  pediatrics: "pediatrician",
  physician: "doctor",
  dental: "dentist",
};

/** The limits the API enforces (both are 400s past them). */
export const MAX_TRADES = 20;
export const MAX_TRADE_LEN = 64;

/**
 * Turn user text into the stored trade form, exactly as the server does:
 * lower case, runs of space / hyphen / underscore become one underscore,
 * synonyms mapped, blank stays blank.
 */
export function normalizeTrade(input: string): string {
  // The Go version splits on space, '-' and '_' and rejoins with '_', which
  // also drops runs and trims both ends.
  const t = input
    .toLowerCase()
    .trim()
    .split(/[\s\-_]+/)
    .filter(Boolean)
    .join("_");
  if (!t) return "";
  return TRADE_SYNONYMS[t] ?? t;
}

/**
 * Normalise a comma-separated field ("Plumbing, Handyman") to stored trades.
 * De-duplicates in first-seen order, like `normalizeTrades` in the API.
 */
export function normalizeTradeList(input: string): string[] {
  const out: string[] = [];
  for (const part of input.split(",")) {
    const t = normalizeTrade(part);
    if (t && !out.includes(t)) out.push(t);
  }
  return out;
}

/** "pest_control" → "Pest control"; a family's own trade ("handyman") too. */
export function tradeLabel(trade: string): string {
  const words = trade.replace(/_/g, " ").trim();
  if (!words) return "";
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/** A comma-separated field value for a stored trade list ("plumber, vet"). */
export function tradesToField(trades: readonly string[] | undefined | null): string {
  return (trades ?? []).join(", ");
}

/**
 * The canonical stored trades, derived from the synonym map (its values), for
 * the form's type-ahead. Sorted, because a datalist reads better alphabetised.
 */
export const TRADE_SUGGESTIONS: readonly string[] = Array.from(
  new Set(Object.values(TRADE_SYNONYMS)),
).sort();
