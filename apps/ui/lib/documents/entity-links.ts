/**
 * "What is this document attached to, and what is it linked to?" (#4569)
 *
 * Two different API facts, and the detail page keeps them apart:
 *
 *  1. ATTACHMENT. `files.entity_type` + `files.entity_id` is the one record a
 *     document belongs to (a bill, a vehicle, a pet, the household itself).
 *     The API's file module writes it at upload and re-homes it to the
 *     household if that record is deleted (rehome_entity_refs). It exists on
 *     every file row, so it is the reliable "belongs to".
 *
 *  2. LINKS. `GET /api/v1/links?entity_type=file&entity_id=<id>` returns the
 *     rows this document carries, and each of those (link_type url / file /
 *     secret) points somewhere else. The API has no reverse lookup, so links
 *     POINTING AT this file live under the other record and are not visible
 *     from here — the page says so rather than implying the list is complete.
 *
 * A link row carries only a `title` (the caller's label at the time it was
 * made), not a resolved name, so the page shows the label the family wrote.
 * It never fetches a stranger's record to build one: a link's title is exactly
 * what the person who made the link typed, which is the honest thing to show.
 *
 * Plain functions only, so `npm test` covers the labels and the URL rules.
 */

import type { FileRecord } from "@/lib/types/api";
import type { LinkRow } from "@/lib/documents/api";

/** The kinds `files.entity_type` can hold that this page can name and route to. */
export interface AttachmentTarget {
  /** The API's entity kind ("bill", "vehicle", "household", …). */
  kind: string;
  id: string;
  /** "Bill" / "Vehicle" — the noun the page prints. */
  noun: string;
  /** A page for the record, or null when this build has none. */
  href: string | null;
}

const NOUNS: Record<string, string> = {
  household: "Family",
  property: "Property",
  room: "Room",
  asset: "Thing",
  vehicle: "Vehicle",
  pet: "Pet",
  vendor: "Vendor",
  bill: "Bill",
  loan: "Loan",
  maintenance_task: "Upkeep task",
  maintenance_schedule: "Upkeep schedule",
  note: "Note",
  secret: "Credential",
  person: "Person",
  budget: "Budget",
  list: "List",
  recipe: "Recipe",
  meal_plan: "Meal plan",
  guide: "Guide",
  calendar: "Calendar",
  calendar_event: "Event",
  document_link: "Link",
};

/** The pages this build actually has for a kind (null: no page, so no link). */
const PAGES: Record<string, (id: string) => string> = {
  asset: (id) => `/dashboard/assets/${id}`,
  vehicle: (id) => `/dashboard/vehicles/${id}`,
  pet: (id) => `/dashboard/pets/${id}`,
  property: (id) => `/dashboard/properties/${id}`,
  vendor: (id) => `/dashboard/vendors/${id}`,
  bill: (id) => `/dashboard/bills/${id}`,
  loan: (id) => `/dashboard/loans/${id}`,
  guide: (id) => `/dashboard/guides/${id}`,
  recipe: (id) => `/dashboard/recipes/${id}`,
};

/** What a document is attached to, or null for a family-level document. */
export function attachmentOf(file: Pick<FileRecord, "entity_type" | "entity_id">): AttachmentTarget | null {
  const kind = (file.entity_type ?? "").trim();
  const id = (file.entity_id ?? "").trim();
  if (!kind || !id) return null;
  // A family-level file is attached to the household: it is not "attached to
  // something", so the page says "the whole family" in its Details instead of
  // drawing a link to a page that doesn't exist.
  if (kind === "household") return null;
  return { kind, id, noun: NOUNS[kind] ?? kind.replace(/_/g, " "), href: PAGES[kind]?.(id) ?? null };
}

/** The noun for a kind, for a label ("Bill" / "Vehicle"). */
export function entityNoun(kind: string | null | undefined): string {
  if (!kind) return "Record";
  return NOUNS[kind] ?? kind.replace(/_/g, " ");
}

/** The page for a linked record, or null when this build has none. */
export function entityHref(kind: string | null | undefined, id: string | null | undefined): string | null {
  if (!kind || !id) return null;
  return PAGES[kind]?.(id) ?? null;
}

// ─── Link rows ───────────────────────────────────────────────

export type LinkKind = "url" | "file" | "secret" | "other";

export interface DocumentLink {
  id: string;
  kind: LinkKind;
  /** What the family called it when they made the link. */
  title: string;
  /** Where the row goes: a document for a file link, the site for a url. */
  href: string | null;
  /** The visible second line: "Web link" / "Another document" / "Credential". */
  detail: string;
  /** True when the target is external (open in a new tab, rel noreferrer). */
  external: boolean;
}

/**
 * Only http(s) URLs are ever rendered as a link. A `javascript:` or `data:`
 * URL stored in a link row would otherwise become a script the page runs; the
 * API validates nothing about `url`, so the renderer is the gate.
 */
export function safeUrl(url: string | null | undefined): string | null {
  if (!url) return null;
  const trimmed = url.trim();
  if (!/^https?:\/\//i.test(trimmed)) return null;
  try {
    const parsed = new URL(trimmed);
    return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.toString() : null;
  } catch {
    return null;
  }
}

/** The rows a document's "Linked to" section draws. */
export function documentLinks(rows: readonly LinkRow[] | undefined): DocumentLink[] {
  const out: DocumentLink[] = [];
  for (const row of rows ?? []) {
    if (row.link_type === "url") {
      const href = safeUrl(row.url);
      out.push({
        id: row.id,
        kind: "url",
        title: row.title,
        href,
        detail: href ? "Web link" : "Web link (address is not http, so it can't be opened)",
        external: !!href,
      });
    } else if (row.link_type === "file") {
      out.push({
        id: row.id,
        kind: "file",
        title: row.title,
        href: row.link_id ? `/dashboard/documents/${row.link_id}` : null,
        detail: "Another document",
        external: false,
      });
    } else if (row.link_type === "secret") {
      // A credential's target is a secret, whose page needs the master
      // password; the row names it and points at Settings' credentials rather
      // than deep-linking a locked record.
      out.push({
        id: row.id,
        kind: "secret",
        title: row.title,
        href: null,
        detail: "Credential — open it from Settings",
        external: false,
      });
    } else {
      out.push({ id: row.id, kind: "other", title: row.title, href: null, detail: entityNoun(row.link_type), external: false });
    }
  }
  return out;
}

// ─── Tag editing ─────────────────────────────────────────────

/** Parse a TagsInput value list into the API's tag array (trimmed, unique). */
export function tagsForApi(tags: readonly string[] | null | undefined): string[] {
  const out: string[] = [];
  for (const t of tags ?? []) {
    const v = t.trim();
    if (v && !out.includes(v)) out.push(v);
  }
  return out;
}

/**
 * A PATCH body that only carries what actually changed, because the API's
 * PATCH distinguishes absent ("keep") from null ("clear") — sending every
 * field would blank the ones the form left empty.
 */
export function metadataPatch(
  original: Pick<FileRecord, "title" | "category" | "document_date" | "expires_on" | "description" | "person_id" | "tags">,
  next: {
    title: string;
    category: string;
    documentDate: string;
    expiresOn: string;
    description: string;
    personId: string | null;
    tags: string[];
  },
): Record<string, unknown> {
  const patch: Record<string, unknown> = {};
  const norm = (v: string | null | undefined) => (v ?? "").trim();
  if (norm(next.title) !== norm(original.title)) patch.title = norm(next.title) || null;
  if (next.category !== (original.category ?? "other")) patch.category = next.category;
  if (next.documentDate !== (original.document_date ?? "")) patch.document_date = next.documentDate || null;
  if (next.expiresOn !== (original.expires_on ?? "")) patch.expires_on = next.expiresOn || null;
  if (norm(next.description) !== norm(original.description)) patch.description = norm(next.description) || null;
  if ((next.personId ?? "") !== (original.person_id ?? "")) patch.person_id = next.personId || null;
  const before = tagsForApi(original.tags);
  const after = tagsForApi(next.tags);
  if (before.join("\u0000") !== after.join("\u0000")) patch.tags = after;
  return patch;
}
