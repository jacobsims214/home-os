/**
 * Guides: the types the pages read and the one markdown parser they share
 * (#4570).
 *
 * A guide is a title, an optional summary, a markdown body and tags
 * (apps/api/internal/guides/handler.go guideResponse). The body is the whole
 * point of the page — "reset the router", "winterize the cabin" — so the
 * reader SPLITS it into steps a person can follow with one hand on a phone.
 *
 * The app has no markdown dependency (package.json), and a guide body is
 * written by a family, not a CMS: the subset below is exactly what the editor
 * and the MCP tools produce — ATX headings, paragraphs, bullet and numbered
 * lists, fenced code blocks, and inline bold/italic/code/links. Anything else
 * is shown as its own text, never dropped.
 *
 * Why blocks and not HTML: a code block, a serial number or a Wi-Fi password
 * must scroll INSIDE itself (GuideBlock renders as `overflow-x-auto
 * whitespace-pre`), never widen the page on a 390 px phone.
 *
 * Plain data and pure functions only (no JSX, no React), so `node --test` can
 * load this file directly (lib/guides/model.test.mjs).
 */

/** The guide response (apps/api/internal/guides/handler.go guideResponse). */
export interface Guide {
  id: string;
  household_id: string;
  title: string;
  summary: string | null;
  body: string;
  tags: string[];
  property_id: string | null;
  asset_id: string | null;
  vehicle_id: string | null;
  created_at: string;
  updated_at: string;
}

/**
 * A revision in the history list (revisionResponse). `body` is present only
 * for GET /guides/{id}/revisions/{revisionId} — the list omits it and carries
 * `body_length` instead, so history does not ship every version's markdown.
 */
export interface GuideRevision {
  id: string;
  guide_id: string;
  body?: string;
  body_length: number;
  edited_by: string | null;
  edited_by_name: string | null;
  change_note: string | null;
  created_at: string;
}

export type GuideBlock =
  | { kind: "p"; text: string }
  | { kind: "ul"; items: string[] }
  | { kind: "ol"; items: string[] }
  | { kind: "code"; text: string; lang: string | null };

/** One step of the reading view: an optional heading and what it says. */
export interface GuideStep {
  heading: string | null;
  blocks: GuideBlock[];
}

const HEADING = /^(#{1,6})\s+(.*)$/;
const FENCE = /^```(.*)$/;
const BULLET = /^\s*[-*+]\s+(.*)$/;
const NUMBERED = /^\s*\d{1,3}[.)]\s+(.*)$/;

/**
 * Splits a guide body into steps.
 *
 * - A heading starts a step (`## Reset the router`).
 * - A body with no headings is ONE step, unless it is a pure numbered list:
 *   then each item is its own step, which is what "step by step" means for the
 *   guides people actually write ("1. Pull the plug. 2. Wait 30 s.").
 * - A leading `# Title` is dropped: the page already shows the title, and two
 *   identical headings read like a bug.
 * - Fenced code blocks are their own block, kept verbatim (indentation
 *   included), so the reader can scroll them sideways and copy them.
 */
export function parseGuideSteps(body: string | null | undefined): GuideStep[] {
  const lines = String(body ?? "").replace(/\r\n?/g, "\n").split("\n");
  const steps: GuideStep[] = [];
  let current: GuideStep | null = null;
  let paragraph: string[] = [];
  let list: { kind: "ul" | "ol"; items: string[] } | null = null;
  let fence: { lang: string | null; text: string[] } | null = null;

  const ensure = (): GuideStep => {
    if (!current) {
      current = { heading: null, blocks: [] };
      steps.push(current);
    }
    return current;
  };
  const flushParagraph = () => {
    if (paragraph.length === 0) return;
    ensure().blocks.push({ kind: "p", text: paragraph.join("\n").trim() });
    paragraph = [];
  };
  const flushList = () => {
    if (!list) return;
    const items = list.items;
    ensure().blocks.push(list.kind === "ul" ? { kind: "ul", items } : { kind: "ol", items });
    list = null;
  };
  const flushAll = () => {
    flushParagraph();
    flushList();
  };

  for (const line of lines) {
    if (fence) {
      if (FENCE.test(line)) {
        ensure().blocks.push({ kind: "code", text: fence.text.join("\n"), lang: fence.lang });
        fence = null;
      } else {
        fence.text.push(line);
      }
      continue;
    }

    const openFence = FENCE.exec(line);
    if (openFence) {
      flushAll();
      const lang = openFence[1].trim();
      fence = { lang: lang || null, text: [] };
      continue;
    }

    const heading = HEADING.exec(line);
    if (heading) {
      flushAll();
      const text = heading[2].trim();
      // A level-1 heading with nothing before it is the guide's own title.
      if (heading[1].length === 1 && steps.length === 0 && !current) continue;
      current = { heading: text, blocks: [] };
      steps.push(current);
      continue;
    }

    if (line.trim() === "") {
      flushAll();
      continue;
    }

    const bullet = BULLET.exec(line);
    if (bullet) {
      flushParagraph();
      if (list?.kind !== "ul") {
        flushList();
        list = { kind: "ul", items: [] };
      }
      list.items.push(bullet[1].trim());
      continue;
    }

    const numbered = NUMBERED.exec(line);
    if (numbered) {
      flushParagraph();
      if (list?.kind !== "ol") {
        flushList();
        list = { kind: "ol", items: [] };
      }
      list.items.push(numbered[1].trim());
      continue;
    }

    // A plain line continues the paragraph it follows.
    flushList();
    paragraph.push(line);
  }

  if (fence) {
    // An unclosed fence is still content: show it, never swallow it.
    ensure().blocks.push({ kind: "code", text: fence.text.join("\n"), lang: fence.lang });
  }
  flushAll();

  // A body that is nothing but a numbered list reads as one step per item.
  const only = steps[0];
  const onlyBlock = only && only.heading === null && only.blocks.length === 1 ? only.blocks[0] : null;
  if (steps.length === 1 && onlyBlock && onlyBlock.kind === "ol") {
    return onlyBlock.items.map((item) => ({ heading: null, blocks: [{ kind: "p" as const, text: item }] }));
  }

  return steps.filter((s) => s.heading !== null || s.blocks.length > 0);
}

/** How many steps the reader will see; the list shows "4 steps". */
export function guideStepCount(body: string | null | undefined): number {
  return parseGuideSteps(body).length;
}

/** "4 steps" / "1 step" — never "4 step"; and "No steps yet" when empty. */
export function stepCountLabel(count: number): string {
  if (count === 0) return "No steps yet";
  return `${count} step${count === 1 ? "" : "s"}`;
}

/** Everything a person might type to find this guide, lower-cased. */
function haystack(guide: Pick<Guide, "title" | "summary" | "body" | "tags">): string {
  return [guide.title, guide.summary ?? "", guide.body, (guide.tags ?? []).join(" ")]
    .join(" ")
    .toLowerCase();
}

/**
 * Whether a guide matches a search. Every whitespace-separated word must
 * appear somewhere, so "router wifi" finds "Reset the router" whose body
 * mentions wifi, and "reset" alone still does.
 */
export function matchesSearch(
  guide: Pick<Guide, "title" | "summary" | "body" | "tags">,
  query: string,
): boolean {
  const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const hay = haystack(guide);
  return words.every((w) => hay.includes(w));
}

/** A one-line preview: the summary, or the body's first real line when none. */
export function guideSubtitle(guide: Pick<Guide, "summary" | "body">): string | null {
  const summary = (guide.summary ?? "").trim();
  if (summary) return summary.replace(/\s+/g, " ");
  const first = String(guide.body ?? "")
    .replace(/```[\s\S]*?```/g, " ")
    .split("\n")
    .map((l) => l.replace(/^#{1,6}\s+|^\s*[-*+]\s+|^\s*\d{1,3}[.)]\s+/, "").trim())
    .find((l) => l !== "");
  return first ?? null;
}

/**
 * "Nov 18, 2026" — a save date, never a time a reader has to convert.
 *
 * `timeZone: "UTC"` because this renders a stored CIVIL date, not a moment in
 * the reader's day: without it the same revision would read as a different day
 * for a browser west of UTC (the defect class #5316 guards). Same rule as
 * notification-model.ts's date label.
 */
export function formatGuideDate(iso: string | null | undefined): string | null {
  if (!iso) return null;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return null;
  return d.toLocaleDateString("en-US", { year: "numeric", month: "short", day: "numeric", timeZone: "UTC" });
}

/**
 * The PUT body for an edit: the page sends every editable field so clearing
 * one actually clears it (`summary: ""` clears the summary). `change_note` is
 * why the person changed it and is stored on the revision this save writes
 * (068); blank means none.
 */
export function guideEditBody(values: {
  title: string;
  summary: string;
  body: string;
  tags: string;
  change_note: string;
}): Record<string, unknown> {
  return {
    title: values.title.trim(),
    summary: values.summary.trim(),
    body: values.body,
    tags: splitTags(values.tags),
    change_note: values.change_note.trim(),
  };
}

/** "wifi, router , Reset" → ["wifi", "router", "reset"] (the API lower-cases). */
export function splitTags(input: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of input.split(",")) {
    const tag = raw.trim().toLowerCase();
    if (!tag || seen.has(tag)) continue;
    seen.add(tag);
    out.push(tag);
  }
  return out;
}

/** The tag chip row of one guide, as "a, b" for an input field. */
export function tagsToInput(tags: readonly string[] | null | undefined): string {
  return (tags ?? []).join(", ");
}

/**
 * The PUT body for an edit of an EXISTING guide (#5309).
 *
 * The trap this function exists for: `PUT /api/v1/guides/{id}` is not a patch
 * on the wire even though it is one on the server. The handler loads the row
 * under `FOR UPDATE`, applies whichever fields the request carried, and
 * `saveLocked` writes EVERY column back (repo.go). So a field the form forgot
 * to send is not "left alone" in the sense a reader would expect — it is
 * written back from a row that may already be older than what someone else
 * just saved, and a subject column omitted from the body is silently KEPT
 * rather than cleared. The #4565 trap in a different table.
 *
 * The rule, and the reason this is a pure function with a test: send every
 * editable field, every time, from the guide the form was opened on — the
 * subject columns included, echoed as-is, even though the form cannot change
 * them. A save then describes the whole record, so two people editing at once
 * cannot interleave two half-records.
 */
export function guideSaveBody(
  guide: Pick<Guide, "property_id" | "asset_id" | "vehicle_id">,
  values: { title: string; summary: string; body: string; tags: string; change_note: string },
): Record<string, unknown> {
  return {
    ...guideEditBody(values),
    // Echoed, never edited: the form has no subject picker. "" clears on the
    // API's side (subject.merge), so a null in the record must send "" — not
    // be omitted, which would leave whatever the server has.
    property_id: guide.property_id ?? "",
    asset_id: guide.asset_id ?? "",
    vehicle_id: guide.vehicle_id ?? "",
  };
}

/**
 * A restore's note is written by the API: "Restored revision <revisionId>"
 * (repo.go `Restore`). Recognising it lets history read as a restore rather
 * than an unexplained save.
 */
export function isRestoreNote(note: string | null | undefined): boolean {
  return typeof note === "string" && /^Restored revision\s+[0-9a-fA-F-]{36}$/.test(note.trim());
}

/** The revision a restore put back, from its note, or null. */
export function restoredRevisionId(note: string | null | undefined): string | null {
  if (!isRestoreNote(note)) return null;
  return String(note).trim().split(/\s+/).pop() ?? null;
}

/**
 * Which revision the guide's body currently equals: the newest one whose body
 * matches. This is how history says "this is what the guide says now" without
 * trusting `updated_at`, and it holds after a restore (which writes the old
 * body as a NEW revision, so the newest match is the restore).
 */
export function currentRevisionId(
  revisions: readonly Pick<GuideRevision, "id" | "body">[],
  body: string | null | undefined,
): string | null {
  const text = String(body ?? "");
  for (const rev of revisions) {
    if (typeof rev.body === "string" && rev.body === text) return rev.id;
  }
  return null;
}

/** A revision's size is a fact, not a promise: "412 characters". */
export function revisionSizeLabel(bodyLength: number): string {
  if (!Number.isFinite(bodyLength) || bodyLength <= 0) return "empty";
  return `${bodyLength} character${bodyLength === 1 ? "" : "s"}`;
}

/**
 * Who made a revision. The API joins `edited_by_name`; a person with no users
 * row (local dev) is NULL on both. "Someone" and "Not recorded" are honest;
 * inventing a name is not.
 */
export function revisionAuthor(rev: Pick<GuideRevision, "edited_by" | "edited_by_name">): string {
  const name = (rev.edited_by_name ?? "").trim();
  if (name) return name;
  return rev.edited_by ? "Someone" : "Not recorded";
}

/**
 * "2 hours ago" / "Nov 2, 2026". Recent saves are the ones a person is
 * checking on ("did my edit land?"), so they read as an interval; anything
 * older reads as a date, because "36 days ago" is arithmetic nobody wants.
 * `now` is injectable so the test is not a clock race.
 */
export function revisionWhen(iso: string | null | undefined, now: number = Date.now()): string | null {
  if (!iso) return null;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return null;
  const ms = now - d.getTime();
  if (ms < 0) return formatGuideDate(iso);
  const minutes = Math.floor(ms / 60_000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} hour${hours === 1 ? "" : "s"} ago`;
  return formatGuideDate(iso);
}

/**
 * Whether a save would actually change anything. A save that writes an
 * identical body still adds a revision and moves `updated_at`, so "Save" on an
 * untouched form should be refused rather than fill the history with copies
 * of the same guide.
 */
export function guideIsDirty(
  guide: Pick<Guide, "title" | "summary" | "body" | "tags">,
  values: { title: string; summary: string; body: string; tags: string },
): boolean {
  return (
    values.title.trim() !== guide.title.trim() ||
    values.summary.trim() !== (guide.summary ?? "").trim() ||
    values.body !== guide.body ||
    splitTags(values.tags).join(",") !== (guide.tags ?? []).map((t) => t.toLowerCase()).join(",")
  );
}

/** What a restore will do, in the person's words, before they do it. */
export function restoreWarning(when: string | null): string {
  return (
    `Restoring this revision puts its steps back as the guide's current version. ` +
    `Nothing is deleted: the version it replaces stays in the history` +
    (when ? `, so you can restore that one too.` : `.`)
  );
}
