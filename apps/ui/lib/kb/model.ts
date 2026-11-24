/**
 * The family reference (the KB): types, path rules and list grouping
 * (HOME-OS-5503).
 *
 * WHAT THIS IS, IN ONE SENTENCE. A KB doc is household knowledge with a
 * stable ADDRESS: `how-to/networking/router-reset`. `kb_write` on the MCP is an
 * upsert BY PATH, so the path is the doc's identity — there is no id to open,
 * no revision to restore, and a write replaces the whole doc.
 *
 * WHY IT LIVES BESIDE GUIDES. Guides (#4570) are curated, step-by-step
 * procedures with revision history and an optional property/asset/vehicle
 * subject; the reference is freeform knowledge an agent or a person wrote for
 * later lookup. They overlap as *documents* (title + markdown + tags, both
 * indexed into household_search) and differ as *records*, so the UI is shared
 * and the tables are not: both live under /dashboard/guides, and both render
 * their markdown with the ONE parser in lib/guides/model.ts. Building a second
 * markdown renderer here would be the one-rule-two-homes defect this project
 * keeps paying for.
 *
 * PATHS ARE VALIDATED HERE THE WAY THE API VALIDATES THEM. `CleanPath` in
 * apps/api/internal/kb/repo.go lower-cases, slugifies whitespace runs and
 * rejects anything else; migration 076's CHECK enforces the same shape. This
 * module repeats those rules so the path the picker SHOWS is the path the
 * server STORES — a preview that disagrees with the write is a lie, and the
 * person only finds out after saving.
 *
 * Plain data and pure functions only (no JSX, no React), so `node --test` can
 * load this file directly (lib/kb/model.test.mjs).
 */

/** Family roles may write the KB (apps/api/internal/auth/roles.go ResKB.write). */
const KB_WRITERS: readonly string[] = ["owner", "family_manager", "family_member"];

/** A doc's full body (GET /api/v1/kb/<path> → docResponse). */
export interface KbDoc {
  path: string;
  title: string;
  body: string;
  tags: string[];
  created_at: string;
  updated_at: string;
  created_by?: string | null;
  updated_by?: string | null;
  created_by_id?: string | null;
  updated_by_id?: string | null;
}

/**
 * One list or search row (GET /api/v1/kb → summaryResponse). `score` is
 * present on search hits only; higher is a better match.
 */
export interface KbSummary {
  path: string;
  title: string;
  tags: string[];
  excerpt: string;
  updated_at: string;
  updated_by?: string | null;
  score?: number;
}

/** The PUT body for a doc: the API replaces whatever is at the path. */
export interface KbWriteValues {
  title: string;
  body: string;
  tags: string;
}

// ─── paths ───────────────────────────────────────────────────

/** Mirrors `pathSegment` in apps/api/internal/kb/repo.go. */
const PATH_SEGMENT = /^[a-z0-9][a-z0-9._-]*$/;

/**
 * One segment, slugified the way the API slugs it: trimmed, lower-cased, and
 * each run of whitespace turned into one '-'. Anything else is left alone so
 * `cleanPath` can reject it rather than silently mangling it.
 */
export function slugSegment(input: string): string {
  let out = "";
  let lastDash = false;
  for (const ch of String(input ?? "").trim().toLowerCase()) {
    if (/\s/.test(ch)) {
      if (!lastDash) {
        out += "-";
        lastDash = true;
      }
      continue;
    }
    lastDash = false;
    out += ch;
  }
  return out;
}

/**
 * A whole path, cleaned and validated, or an error in words a person can act
 * on. This is `CleanPath` (apps/api/internal/kb/repo.go) restated: trim
 * slashes, split on '/', slug each segment, reject empty/`.`/`..` and anything
 * outside the migration's CHECK character set.
 */
export function cleanPath(input: string): { path: string } | { error: string } {
  const raw = String(input ?? "").trim().replace(/^\/+|\/+$/g, "");
  if (raw === "") return { error: "Give the doc a path, e.g. how-to/networking/router-reset." };
  if (raw.length > 512) return { error: "A path is at most 512 characters." };
  const segments: string[] = [];
  for (const rawSegment of raw.split("/")) {
    const segment = slugSegment(rawSegment);
    if (segment === "" || segment === "." || segment === "..") {
      return { error: `“${raw}” has an empty or invalid part. Use words separated by “/”.` };
    }
    if (!PATH_SEGMENT.test(segment)) {
      return {
        error:
          "A path uses lower-case letters, digits, “-”, “_” or “.”, separated by “/”. Type words and we will tidy them.",
      };
    }
    segments.push(segment);
  }
  const path = segments.join("/");
  if (path.length > 512) return { error: "A path is at most 512 characters." };
  return { path };
}

/** The doc path a route's catch-all segments address, or null when unusable. */
export function pathFromUrlSegments(segments: readonly string[] | null | undefined): string | null {
  if (!segments || segments.length === 0) return null;
  let joined: string;
  try {
    joined = segments.map((s) => decodeURIComponent(s)).join("/");
  } catch {
    return null;
  }
  const cleaned = cleanPath(joined);
  return "path" in cleaned ? cleaned.path : null;
}

/**
 * A slug from a title: "Reset the router" → "reset-the-router". Empty when
 * nothing usable survives, so the caller can offer a path field instead of
 * saving a doc called "doc".
 */
export function slugFromTitle(title: string): string {
  return slugSegment(String(title ?? ""))
    .replace(/[^a-z0-9._-]+/g, "-")
    .replace(/-+/g, "-")
    .replace(/^[._-]+|[._-]+$/g, "");
}

/** The top-level segment: what the list groups by ("how-to"). */
export function topSegment(path: string): string {
  const cleaned = String(path ?? "").replace(/^\/+/, "");
  const i = cleaned.indexOf("/");
  return i === -1 ? cleaned : cleaned.slice(0, i);
}

/** The path minus its top segment, for the second line of a grouped row. */
export function pathWithinTop(path: string): string {
  const cleaned = String(path ?? "").replace(/^\/+/, "");
  return cleaned.slice(topSegment(cleaned).length).replace(/^\/+/, "");
}

/** The folder of a doc (`how-to/networking` for `how-to/networking/router`). */
export function parentPath(path: string): string {
  const cleaned = String(path ?? "").replace(/^\/+|\/+$/g, "");
  const i = cleaned.lastIndexOf("/");
  return i === -1 ? "" : cleaned.slice(0, i);
}

// ─── the list ────────────────────────────────────────────────

export interface KbGroup {
  /** The top-level segment this group collects ("how-to"). */
  top: string;
  /** Its heading ("How to"). */
  label: string;
  docs: KbSummary[];
}

/** Small words that read wrong when only the first letter is capitalised. */
const SPECIAL_WORDS: Readonly<Record<string, string>> = {
  kb: "KB",
  diy: "DIY",
  hvac: "HVAC",
  id: "ID",
  tv: "TV",
  url: "URL",
  vet: "Vet",
  wifi: "Wi-Fi",
};

/**
 * A path segment as a heading: "how-to" → "How to", "wifi" → "Wi-Fi".
 * Hyphens, underscores and dots separate words, because a segment is a slug and
 * "after-hours" is two words to a reader.
 */
export function humanizeSegment(segment: string): string {
  const words = String(segment ?? "")
    .split(/[-_.]+/)
    .filter(Boolean);
  if (words.length === 0) return String(segment ?? "");
  return words
    .map((word, i) => {
      const special = SPECIAL_WORDS[word.toLowerCase()];
      if (special) return special;
      return i === 0 ? word.charAt(0).toUpperCase() + word.slice(1) : word;
    })
    .join(" ");
}

/**
 * The list as groups by top-level segment. Groups are ordered by label so the
 * list does not reshuffle when one doc changes; within a group, by path, which
 * puts a folder's docs together because the path IS the filing order.
 *
 * A SEARCH result is passed through `groupByTop` too, but `groupByTop` keeps
 * the order it was given inside a group only when `ranked` is true — a meaning
 * search that gets re-sorted alphabetically throws away the one thing it
 * computed (the ranking).
 */
export function groupByTop(docs: readonly KbSummary[], ranked = false): KbGroup[] {
  const byTop = new Map<string, KbSummary[]>();
  for (const doc of docs) {
    const top = topSegment(doc.path);
    const bucket = byTop.get(top);
    if (bucket) bucket.push(doc);
    else byTop.set(top, [doc]);
  }
  const groups = Array.from(byTop, ([top, items]) => ({
    top,
    label: humanizeSegment(top),
    docs: ranked ? items : [...items].sort((a, b) => a.path.localeCompare(b.path)),
  }));
  return ranked ? groups : groups.sort((a, b) => a.label.localeCompare(b.label) || a.top.localeCompare(b.top));
}

/**
 * The folders already in use, so a family that filed "how-to/networking" once
 * does not have to type it again — and does not create "howto/networking" by
 * accident.
 */
export function folderSuggestions(docs: readonly Pick<KbSummary, "path">[]): string[] {
  const seen = new Set<string>();
  for (const doc of docs) {
    const parts = String(doc.path ?? "").split("/").filter(Boolean);
    for (let i = 1; i < parts.length; i++) seen.add(parts.slice(0, i).join("/"));
  }
  return Array.from(seen).sort((a, b) => a.localeCompare(b));
}

/** The distinct top-level segments, which the filter chips offer. */
export function topSegments(docs: readonly Pick<KbSummary, "path">[]): string[] {
  return Array.from(new Set(docs.map((d) => topSegment(d.path)))).sort((a, b) => a.localeCompare(b));
}

/**
 * Whether a doc matches the CLIENT-SIDE filter. The KB's real search is the
 * API's semantic one (`?q=`); this narrows an ALREADY-LOADED list by path,
 * title and tags only, so it is not a second search — it is the offline / not
 * yet asked state, and the page says which one it is using.
 */
export function matchesFilter(doc: Pick<KbSummary, "path" | "title" | "tags">, query: string): boolean {
  const words = String(query ?? "").trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const hay = [doc.path, doc.title, (doc.tags ?? []).join(" ")].join(" ").toLowerCase();
  return words.every((w) => hay.includes(w));
}

// ─── reading a row ───────────────────────────────────────────

/**
 * A one-line preview of the doc. The API's excerpt is the opening of the body
 * (repo.go Excerpt, 240 chars), so it still carries markdown: fenced blocks,
 * heading markers and list bullets are stripped here, because "```" is not
 * something a reader wants in a list row.
 */
export function rowSummary(summary: Pick<KbSummary, "excerpt">): string | null {
  const text = String(summary.excerpt ?? "")
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/^\s*#{1,6}\s+/gm, "")
    .replace(/^\s*[-*+]\s+/gm, "")
    .replace(/^\s*\d{1,3}[.)]\s+/gm, "")
    .replace(/\s+/g, " ")
    .trim();
  return text === "" ? null : text;
}

/**
 * "Nov 18, 2026". `timeZone: "UTC"` for the same reason as the guides date
 * label: this renders a stored moment's date, and a browser west of UTC must
 * not show a different day than a browser east of it (#5316's defect class).
 */
export function formatKbDate(iso: string | null | undefined): string | null {
  if (!iso) return null;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return null;
  return d.toLocaleDateString("en-US", { year: "numeric", month: "short", day: "numeric", timeZone: "UTC" });
}

/**
 * Who last wrote the doc, in words, or null. The API returns a display name or
 * nothing; an unnamed author stays unnamed rather than being guessed at (a doc
 * an agent wrote has no person's name on it, and inventing "an agent" would put
 * a claim in the UI that the API never made).
 */
export function authorLabel(doc: { updated_by?: string | null }): string | null {
  const name = (doc.updated_by ?? "").trim();
  return name === "" ? null : name;
}

// ─── writing ─────────────────────────────────────────────────

/** Family roles write the KB; a housesitter reads it (ResKB, roles.go). */
export function canWriteKb(role: string | null | undefined): boolean {
  if (role == null || role === "") return true; // still loading: the API refuses
  return KB_WRITERS.includes(role);
}

/** The PUT body. Every field is sent: the API replaces the doc wholesale. */
export function kbWriteBody(values: {
  title: string;
  body: string;
  tags: string | readonly string[];
}): { title: string; body: string; tags: string[] } {
  return {
    title: String(values.title ?? "").trim(),
    body: String(values.body ?? ""),
    tags: parseTags(values.tags),
  };
}

/** "wifi, Router , router" → ["wifi", "router"] (the API lower-cases and dedupes). */
export function parseTags(input: string | readonly string[] | null | undefined): string[] {
  const parts = typeof input === "string" ? input.split(",") : (input ?? []);
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of parts) {
    const tag = String(raw ?? "").trim().toLowerCase();
    if (!tag || seen.has(tag)) continue;
    seen.add(tag);
    out.push(tag);
  }
  return out;
}

/** Tags as the input field shows them. */
export function tagsToInput(tags: readonly string[] | null | undefined): string {
  return (tags ?? []).join(", ");
}

/**
 * What saving to an EXISTING path does, in the person's own words, before they
 * do it. `kb_write` is an upsert by path: there is no revision table, so the
 * old body is not recoverable. The UI says that out loud and makes the person
 * confirm — a save that silently clobbers a doc someone else wrote is the one
 * outcome this feature must not allow.
 */
export function replaceWarning(path: string, title: string | null): string {
  const name = (title ?? "").trim();
  return (
    `Saving here REPLACES the doc already at ${path}` +
    (name ? ` (“${name}”)` : "") +
    `. Its title, body and tags are overwritten and cannot be brought back: the reference keeps no older versions.`
  );
}

/** Whether a save would change anything; a no-op save still replaces the doc. */
export function kbIsDirty(doc: Pick<KbDoc, "title" | "body" | "tags"> | null, values: {
  title: string;
  body: string;
  tags: string | readonly string[];
}): boolean {
  if (!doc) return true;
  return (
    values.title.trim() !== doc.title.trim() ||
    values.body !== doc.body ||
    parseTags(values.tags).join(",") !== (doc.tags ?? []).map((t) => t.toLowerCase()).join(",")
  );
}

/** "1,204 characters" — the size the person is about to store. */
export function sizeLabel(body: string): string {
  const n = String(body ?? "").length;
  return `${n.toLocaleString("en-US")} character${n === 1 ? "" : "s"}`;
}

/**
 * Where a doc's path goes next, from what they typed: the cleaned path when it
 * is valid, else the raw text so the caller can still show the error. Used by
 * the picker's live preview.
 */
export function pathPreview(folder: string, slug: string): { path: string } | { error: string } {
  // Both halves lose their own slashes first: a person who types "/vet/" in
  // the slug field means "vet", and joining first would produce the doubled
  // slash cleanPath is right to refuse.
  const trim = (s: string) => String(s ?? "").trim().replace(/^\/+|\/+$/g, "");
  const base = trim(folder);
  const leaf = trim(slug);
  if (leaf === "") return { error: "Give the doc a name, or type the whole path." };
  return cleanPath(base === "" ? leaf : `${base}/${leaf}`);
}

/**
 * The well-known top-level folders, offered before the family has filed
 * anything. They are the ones the migration's own examples use
 * ("how-to/pets/vet-after-hours") plus the subjects a family looks things up
 * for; the picker ALSO offers every folder already in use, so this list is a
 * starting point, never the whole vocabulary.
 */
export const STARTER_FOLDERS: readonly string[] = [
  "how-to",
  "how-to/network",
  "how-to/appliances",
  "how-to/pets",
  "people",
  "pets",
  "home",
  "money",
  "sitters",
];
