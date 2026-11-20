/**
 * Documents hub: the pure logic behind /dashboard/documents (#4569).
 *
 * Built to the approved documents mockup (artifact for #4520, drawn inside the
 * shell #75 v1) on the API from #4478 (KB architecture/document-hub-api.md).
 * Everything here is plain data and pure functions, so `npm test` (node --test)
 * covers the wording, the date arithmetic and the OCR-state reading without a
 * browser or a server.
 *
 * Three rules this module exists to keep:
 *
 *  1. WHAT THE TEXT IS (#4941). A file's body can be the text that was filed
 *     (`text_origin: "raw"`) or text a worker READ from the blob
 *     (`text_origin: "extracted"`). A body that no longer hashes to its
 *     fingerprint (`text_verified: false`) was changed after filing — a $35
 *     bill filed as $5 with the text re-typed to match is exactly that case.
 *     The detail page must never present a derived or re-typed body as the
 *     original, so the wording lives in ONE place
 *     (components/files/file-display.ts, reused here); this module only
 *     decides which of its sentences the hub shows.
 *
 *  2. WHAT THE OCR STATE IS. The worker records retries in `files.ocr_error` as
 *     `[retry k/n next=<RFC3339>] <error>` and a final failure as
 *     `failed after <n> attempts: <error>` (#4419). A row being retried must
 *     read "retrying", not "failed", and the internal marker must never reach
 *     a person's eyes.
 *
 *  3. WHAT A DATE MEANS. `expires_on` / `document_date` are DATE columns the API
 *     selects with to_char as "YYYY-MM-DD", so they must be compared as
 *     calendar days and never through a Date parsed from them (a local-midnight
 *     Date in a western zone renders the day before in UTC, and vice versa).
 *     Everything here works on the string.
 */

import type { FileRecord } from "@/lib/types/api";

// ─── Categories (migration 048) ───────────────────────────────

/**
 * The API's category list, in the order the approved mockup shows them
 * (`file.Categories`): receipt, tax, insurance, medical, manual, warranty,
 * school, legal, vehicle, home, other.
 *
 * `other` is a CATEGORY, not a synonym for "unfiled" (#5299). The column is
 * NOT NULL DEFAULT 'other' (migration 048), the API rejects clearing it
 * ("category cannot be null; use \"other\"") and the pickers offer it by that
 * name — so a document whose card says "Other" has been filed as Other. Whether
 * a document is filed at all is ONE rule, `isFiled()` below; the count, the row
 * chip and the tints all ask it instead of comparing `category === "other"`
 * for themselves.
 */
export const CATEGORIES = [
  "receipt",
  "tax",
  "insurance",
  "medical",
  "manual",
  "warranty",
  "school",
  "legal",
  "vehicle",
  "home",
  "other",
] as const;

export type Category = (typeof CATEGORIES)[number];

/** Plural label, for a heading over a list ("Receipts"). */
export const CATEGORY_LABELS: Record<Category, string> = {
  receipt: "Receipts",
  tax: "Tax",
  insurance: "Insurance",
  medical: "Medical",
  manual: "Manuals",
  warranty: "Warranties",
  school: "School",
  legal: "Legal",
  vehicle: "Vehicles",
  home: "Home",
  other: "Other",
};

/** Singular label, for one document's chip ("Receipt"). */
export const CATEGORY_SINGULAR: Record<Category, string> = {
  receipt: "Receipt",
  tax: "Tax",
  insurance: "Insurance",
  medical: "Medical",
  manual: "Manual",
  warranty: "Warranty",
  school: "School",
  legal: "Legal",
  vehicle: "Vehicle",
  home: "Home",
  other: "Other",
};

export function isCategory(value: string | null | undefined): value is Category {
  return !!value && (CATEGORIES as readonly string[]).includes(value);
}

/**
 * A file's category, defaulting to `other` — the column's own default, and what
 * a row written before 048 (or by a client that sent nothing) really is.
 *
 * This is the ICON and BUCKET normaliser, not the filing rule: it can't tell
 * "filed as Other" from "sent no category", because both draw the same glyph.
 * Ask `isFiled()` whether a document is filed.
 */
export function categoryOf(file: Pick<FileRecord, "category">): Category {
  return isCategory(file.category) ? file.category : "other";
}

// ─── Filed or not: THE one rule (#5299) ──────────────────────

/**
 * The chip a document with no category at all shows on its card.
 */
export const UNFILED_CHIP_LABEL = "No category";

/**
 * Is this document filed? THE ONE RULE (#5299).
 *
 * Filed means it carries one of the API's categories — and `other` IS one of
 * them: migration 048 makes `files.category` `NOT NULL DEFAULT 'other'`, the
 * API rejects clearing it ("category cannot be null; use \"other\"") and both
 * pickers offer it by the name "Other". A document is unfiled only when the
 * payload carries no category at all (a row read before 048, or a client that
 * sent nothing, which the API stores as Other anyway).
 *
 * Before #5299 this question had two answers in two places: the hub counted
 * `category === "other"` as unfiled (its tile read "Not filed yet") while
 * `DocumentRow` printed "Other" from an independent label map — so one
 * document was both unfiled and filed. Everything that asks the question now
 * asks it HERE: the tile counts (`categoryCounts`), the list of unfiled ones
 * (`unfiledDocuments`), each card's chip (`documentCategoryLabel`) and the
 * glyph tints (`categoryTint`). "Not filed yet" is not a category name and is
 * never printed as one: the tile for `other` says Other, like the card does.
 */
export function isFiled(file: Pick<FileRecord, "category">): boolean {
  return isCategory(file.category);
}

/** The documents carrying no category at all, in list order. */
export function unfiledDocuments<T extends Pick<FileRecord, "category">>(files: readonly T[]): T[] {
  return files.filter((f) => !isFiled(f));
}

/**
 * The words a CARD uses for a document's category.
 *
 * Decided here, once, so it cannot contradict the count a tile shows (#5299):
 * a filed document shows its category's own name ("Other" included — that is
 * what its picker button says too), and an unfiled one says it has no category
 * instead of borrowing Other's name.
 */
export function documentCategoryLabel(file: Pick<FileRecord, "category">): string {
  return isFiled(file) ? CATEGORY_SINGULAR[categoryOf(file)] : UNFILED_CHIP_LABEL;
}

// ─── Titles and identity ──────────────────────────────────────

/** The name a document is shown by: the metadata title, or the file name. */
export function displayTitle(file: Pick<FileRecord, "title" | "name">): string {
  const title = (file.title ?? "").trim();
  return title || file.name || "Untitled document";
}

// ─── Dates as calendar days ──────────────────────────────────

/** A "YYYY-MM-DD" string that names a real day, or null. */
export function parseDay(day: string | null | undefined): { y: number; m: number; d: number } | null {
  if (!day) return null;
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day.trim());
  if (!m) return null;
  const y = Number(m[1]);
  const mo = Number(m[2]);
  const d = Number(m[3]);
  if (mo < 1 || mo > 12 || d < 1 || d > 31) return null;
  return { y, m: mo, d };
}

/**
 * Whole days from `from` to `to`, both "YYYY-MM-DD", or null. Both are read as
 * UTC midnight, so the difference is a whole number of days and no local zone
 * can move either date.
 */
export function daysBetween(from: string, to: string): number | null {
  const a = parseDay(from);
  const b = parseDay(to);
  if (!a || !b) return null;
  const ms = Date.UTC(b.y, b.m - 1, b.d) - Date.UTC(a.y, a.m - 1, a.d);
  return Math.round(ms / 86_400_000);
}

/**
 * "YYYY-MM-DD" for one instant, in a ZONE. Kept for callers that already have
 * the instant they mean (and for the tests that pin the local-day behaviour).
 *
 * NOT a source of "today" (#5316): a page's today is the FAMILY's, from
 * `householdToday(household?.timezone)` via `useHouseholdToday()`. The
 * `today` arguments below therefore have no default — passing the browser's
 * day must be a deliberate act, not something a missing argument does.
 */
export function todayString(now: Date, timeZone?: string): string {
  if (timeZone) {
    const parts = new Intl.DateTimeFormat("en-CA", {
      timeZone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    }).formatToParts(now);
    const get = (t: string) => parts.find((p) => p.type === t)?.value ?? "";
    return `${get("year")}-${get("month")}-${get("day")}`;
  }
  const y = now.getFullYear();
  const m = String(now.getMonth() + 1).padStart(2, "0");
  const d = String(now.getDate()).padStart(2, "0");
  return `${y}-${m}-${d}`;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** "Dec 1, 2026" from "2026-12-01", without going through a Date. */
export function formatDay(day: string | null | undefined): string {
  const p = parseDay(day);
  if (!p) return "";
  return `${MONTHS[p.m - 1]} ${p.d}, ${p.y}`;
}

// ─── Expiry ──────────────────────────────────────────────────

/** Within this many days counts as "expiring soon" (the mockup's window). */
export const EXPIRING_WINDOW_DAYS = 45;

export type ExpiryState = "none" | "expired" | "soon" | "later";

/**
 * Where a document stands against its expiry date. `days` is negative once the
 * date has passed (0 is today).
 */
export interface Expiry {
  state: ExpiryState;
  days: number | null;
  /** "Expires in 14 days" / "Expired 6 days ago" / "" when there is nothing to say. */
  label: string;
}

export function expiryOf(file: Pick<FileRecord, "expires_on">, today: string): Expiry {
  const raw = file.expires_on ?? null;
  if (!raw) return { state: "none", days: null, label: "" };
  const days = daysBetween(today, raw);
  if (days === null) return { state: "none", days: null, label: "" };
  if (days < 0) {
    const n = Math.abs(days);
    return { state: "expired", days, label: n === 0 ? "Expired today" : `Expired ${n} day${n === 1 ? "" : "s"} ago` };
  }
  if (days === 0) return { state: "soon", days, label: "Expires today" };
  if (days <= EXPIRING_WINDOW_DAYS) {
    return { state: "soon", days, label: `Expires in ${days} day${days === 1 ? "" : "s"}` };
  }
  return { state: "later", days, label: `Expires ${formatDay(raw)}` };
}

/** Documents whose expiry is within the window, or already past. */
export function isExpiring(file: Pick<FileRecord, "expires_on">, today: string): boolean {
  const days = file.expires_on ? daysBetween(today, file.expires_on) : null;
  return days !== null && days <= EXPIRING_WINDOW_DAYS;
}

/**
 * The expiring list, soonest date first: an already-expired document is the
 * most urgent thing on the page, so the order is by date rather than by "days
 * left" with the past pushed to the end.
 */
export function expiringSoon<T extends Pick<FileRecord, "expires_on">>(
  files: readonly T[],
  today: string,
): T[] {
  return files
    .filter((f) => isExpiring(f, today))
    .sort((a, b) => (a.expires_on ?? "").localeCompare(b.expires_on ?? ""));
}

// ─── OCR state (#4419's markers) ─────────────────────────────

/** `[retry k/n next=<RFC3339>] <error>` — the worker's retry marker. */
const RETRY_MARKER = /^\[retry (\d+)\/(\d+) next=([^\]]+)\]\s*/;
/** `failed after <n> attempts: <error>` — the worker's final failure. */
const FAILED_MARKER = /^failed after (\d+) attempts?:\s*/;

export type TextStateKind = "pending" | "retrying" | "failed" | "done" | "none";

export interface TextState {
  kind: TextStateKind;
  /** One plain sentence for a person; never contains the raw marker. */
  note: string;
  /** Retries used / allowed, when the worker is retrying. */
  attempt?: number;
  max?: number;
  /** The error text with the marker stripped, when there is one. */
  error?: string;
}

/**
 * What a file's text extraction is doing right now, in a person's words.
 *
 * The worker records retries in `ocr_error` as a PREFIX on an otherwise
 * ordinary error message (#4419). Showing that raw string would print
 * `[retry 2/5 next=…]` at a family, and showing it as a plain failure would
 * call a document unreadable while it is still being retried. So:
 *   - `[retry k/n next=…]` → "retrying", marker stripped;
 *   - `failed after n attempts: …` → "failed", prefix stripped;
 *   - a `failed` row with a bare error → failed (the worker never prefixed it);
 *   - `skipped` → no text to read from this kind of file.
 */
export function textState(file: Pick<FileRecord, "ocr_status" | "ocr_error" | "extracted_text">): TextState {
  const raw = (file.ocr_error ?? "").trim();
  const hasText = typeof file.extracted_text === "string" && file.extracted_text.length > 0;

  switch (file.ocr_status) {
    case "done":
      return hasText
        ? { kind: "done", note: "Text is ready." }
        : { kind: "done", note: "Reading finished but found no text in this one." };
    case "skipped":
      return { kind: "none", note: "There is no text to read from this kind of file." };
    case "pending":
    case "processing":
      return { kind: "pending", note: "Reading it now — this usually takes a few seconds." };
    case "failed": {
      const retry = RETRY_MARKER.exec(raw);
      if (retry) {
        const attempt = Number(retry[1]);
        const max = Number(retry[2]);
        const error = raw.slice(retry[0].length).trim();
        // An unparseable moment is dropped rather than printed as
        // "next try after ." — the retry itself is the fact that matters.
        const next = retry[3] ? prettyMoment(retry[3].trim()) : "";
        const when = next ? ` Next try after ${next}.` : "";
        return {
          kind: "retrying",
          attempt,
          max,
          error: error || undefined,
          note: `Couldn't read it on attempt ${attempt} of ${max} — Home OS is trying again.${when}`,
        };
      }
      const failed = FAILED_MARKER.exec(raw);
      const error = failed ? raw.slice(failed[0].length).trim() : raw;
      const attempts = failed ? Number(failed[1]) : null;
      return {
        kind: "failed",
        error: error || undefined,
        note: attempts
          ? `Couldn't read the text after ${attempts} attempts. The file itself is fine and still downloadable.`
          : "Couldn't read the text from this file. The file itself is fine and still downloadable.",
      };
    }
    default:
      return { kind: "none", note: "" };
  }
}

/** An RFC3339 moment as "Mon, Jan 2, 3:04 PM" in the browser's zone, or "". */
export function prettyMoment(iso: string): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return "";
  return t.toLocaleString("en-US", {
    weekday: "short",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

// ─── File facts for a row ────────────────────────────────────

/** "PDF" / "Photo" / "Spreadsheet" — a file's kind as a person says it. */
export function fileKind(contentType: string | null | undefined, name: string): string {
  const ct = (contentType ?? "").toLowerCase();
  if (ct === "application/pdf") return "PDF";
  if (ct.startsWith("image/")) return ct === "image/jpeg" ? "Photo" : "Image";
  if (ct.startsWith("text/")) return "Text file";
  if (ct.includes("spreadsheet") || ct.includes("csv") || ct.includes("excel") || ct.includes("sheet")) {
    return "Spreadsheet";
  }
  if (ct.includes("word") || ct.includes("document")) return "Document";
  const ext = /\.([a-z0-9]+)$/i.exec(name)?.[1]?.toUpperCase();
  return ext || "File";
}

/** The one-line facts under a row's title: "PDF · 1.2 MB · Nov 12 · by Amber". */
export function rowMeta(
  file: Pick<FileRecord, "content_type" | "name" | "size" | "document_date" | "created_at" | "uploaded_by_name">,
): string {
  return [
    fileKind(file.content_type, file.name),
    formatSize(file.size),
    shortDay(file.document_date ?? file.created_at),
    uploaderName(file),
  ]
    .filter(Boolean)
    .join(" · ");
}

/** "1.2 MB" — bytes as a person reads them. */
export function formatSize(bytes: number | null | undefined): string {
  if (!bytes || bytes < 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const i = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)));
  const value = bytes / Math.pow(1024, i);
  return `${i === 0 ? value : value.toFixed(1)} ${units[i]}`;
}

/** "Nov 12" for a created_at timestamp or a document date; "" when unset. */
export function shortDay(value: string | null | undefined): string {
  if (!value) return "";
  const day = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (day) return `${MONTHS[Number(day[2]) - 1]} ${Number(day[3])}`;
  const t = new Date(value);
  if (Number.isNaN(t.getTime())) return "";
  return `${MONTHS[t.getMonth()]} ${t.getDate()}`;
}

/** "by Amber Sims" / "" — never a fabricated name. */
export function uploaderName(file: Pick<FileRecord, "uploaded_by_name">): string {
  const name = file.uploaded_by_name?.trim();
  return name ? `by ${name}` : "";
}

// ─── Counts for the category tiles ───────────────────────────

export interface CategoryCount {
  category: Category;
  count: number;
  expiring: number;
  /** The newest document's title and day, for "latest: Costco, Nov 12". */
  latest: { title: string; when: string } | null;
}

/**
 * Per-category counts over a file list.
 *
 * Only FILED documents are counted here (`isFiled`, #5299): a document with no
 * category at all is not an "Other" document, it is what `unfiledDocuments()`
 * returns, and the hub notes how many of those there are. Before #5299 they
 * were folded into `other`, which is how a card showing "Other" ended up
 * counted as unfiled.
 *
 * `other` is in the map like every other category, so the hub draws its tile
 * only when documents were really filed as Other.
 */
export function categoryCounts(files: readonly FileRecord[], today: string): Map<Category, CategoryCount> {
  const out = new Map<Category, CategoryCount>();
  for (const c of CATEGORIES) out.set(c, { category: c, count: 0, expiring: 0, latest: null });
  // Newest first, so the first row seen per category is its latest.
  const sorted = [...files].sort((a, b) => (b.created_at ?? "").localeCompare(a.created_at ?? ""));
  for (const f of sorted) {
    if (!isFiled(f)) continue; // no category: `unfiledDocuments()` counts it, never "Other"
    const entry = out.get(categoryOf(f))!;
    entry.count += 1;
    if (isExpiring(f, today)) entry.expiring += 1;
    if (!entry.latest) entry.latest = { title: displayTitle(f), when: shortDay(f.document_date ?? f.created_at) };
  }
  return out;
}

/** The categories worth a tile: everything with documents, in display order. */
export function populatedCategories(files: readonly FileRecord[], today: string): CategoryCount[] {
  const counts = categoryCounts(files, today);
  return CATEGORIES.map((c) => counts.get(c)!).filter((c) => c.count > 0);
}

// ─── Filtering the list ─────────────────────────────────────

export interface DocumentFilters {
  category?: Category | null;
  personId?: string | null;
  /** Free text over title, file name, description and tags. */
  q?: string;
}

/**
 * Client-side narrowing of ONE already-fetched list.
 *
 * The API filters by `category` and `person_id` server-side (GET /files), and
 * the hub uses those params when it changes the category. This helper serves
 * the free-text box over a list already in hand; it never stands in for the
 * semantic search, which is the search page's job (#4561).
 */
export function filterDocuments(files: readonly FileRecord[], filters: DocumentFilters): FileRecord[] {
  const q = (filters.q ?? "").trim().toLowerCase();
  return files.filter((f) => {
    // Same rule as everywhere else: a document with no category is not a copy
    // of the "other" category, so `?category=other` does not pick it up (#5299).
    if (filters.category && (!isFiled(f) || categoryOf(f) !== filters.category)) return false;
    if (filters.personId && (f.person_id ?? "") !== filters.personId) return false;
    if (!q) return true;
    const hay = [displayTitle(f), f.name, f.description ?? "", ...(f.tags ?? [])].join(" ").toLowerCase();
    return q.split(/\s+/).every((word) => hay.includes(word));
  });
}

// ─── Tags ────────────────────────────────────────────────────

/** A tag list as the API stores it: trimmed, non-empty, unique, order kept. */
export function cleanTags(tags: readonly string[] | null | undefined): string[] {
  const out: string[] = [];
  for (const t of tags ?? []) {
    const v = t.trim();
    if (v && !out.includes(v)) out.push(v);
  }
  return out;
}

/** Every tag in use, most-used first — the chips a list page offers. */
export function tagCloud(files: readonly FileRecord[], limit = 12): string[] {
  const counts = new Map<string, number>();
  for (const f of files) for (const t of cleanTags(f.tags)) counts.set(t, (counts.get(t) ?? 0) + 1);
  return Array.from(counts.entries())
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .slice(0, limit)
    .map(([t]) => t);
}
