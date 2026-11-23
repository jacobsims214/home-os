/**
 * Search results: how a list of index hits becomes the grouped, role-filtered,
 * context-carrying rows the Search page renders (#4561).
 *
 * Plain data and pure functions only (no JSX, no React), so `npm test`
 * (node --test) covers them — the grouping rule and the role gate are the two
 * things that must not drift, and neither can be tested inside a
 * `"use client"` page.
 *
 * WHY THIS FILE EXISTS
 *
 * 1. GROUPED, NOT INTERLEAVED. R1 (KB discoveries/war-game-r1-*.md) found
 *    search did not index calendar events or bills: "soccer", "date night" and
 *    "electric" returned nothing, which fed a false "no bills" answer to a
 *    parent. What search covers has since grown, and one ranked list mixing a
 *    bill with a note and a photo reads as noise — a parent looks for "the
 *    bill", not "result #4". Groups are in a fixed order (the areas a family
 *    looks for first), and within a group the API's ranking is kept.
 *
 * 2. A HIT MUST CARRY CONTEXT. The endpoint returns title + body only
 *    (apps/api/internal/search/typesense.go:118 — no date, amount or owner), so
 *    a bare-title list cannot be judged without opening every row. `factsFor`
 *    turns the row's own record (fetched by the page, per type) into the two
 *    or three facts that decide whether it is the right one: an amount and a
 *    due date for a bill, the category and who uploaded it for a document, the
 *    author and date for a note, whose property a thing belongs to.
 *
 * 3. THE ROLE POLICY IS THE UI'S PROBLEM TOO (#4696, #4689,
 *    decisions/2026-11-15-role-policy.md). The API already drops hits a role
 *    may not see, so a group the role cannot search is empty — but "empty
 *    because you may not search it" must not read as "empty because the family
 *    has none", and the page must not hint that anything is hidden ("3 results
 *    hidden" is itself the leak). So a group the role cannot search does not
 *    exist at all (`visibleGroups`), the "what search covers" copy is built
 *    from the same list, and no count is ever derived from anything but the
 *    rows actually rendered.
 *
 * 4. EMPTY AND NO-RESULTS ARE DIFFERENT STATES. An empty box is not a failure
 *    ("type to search"); a zero-result search is a real answer to a real
 *    question ("nothing matched X — try fewer words"). `searchState` is the one
 *    place that decides which, so the page cannot render a blank panel.
 */

import { canSeeEntity, canSeeFiles, canSeeSecrets } from "../resource-access.ts";
import { billDueFor } from "../today/due.ts";
import { formatDueLabel, formatMoney } from "../today/agenda.ts";

// ─── wire shape ───────────────────────────────────────────────

/**
 * One hit from `GET /api/v1/search?q=` (search.SearchResult).
 *
 * `file_id` is set on hits that are a stored file — an OCR chunk from the
 * `files` collection, or a file's metadata document. The files collection
 * stores the ATTACHED entity's type in `entity_type` (the worker writes
 * files.entity_type through), so `entity_type` alone cannot tell a photo of a
 * pet from the pet: `file_id` is the only reliable "this is a document" signal.
 * The endpoint collapses one file's several chunk hits into one.
 */
export interface SearchHit {
  entity_type: string;
  entity_id: string;
  title: string;
  body: string;
  score?: number;
  file_id?: string;
}

/** A role known to the household policy, or null/undefined while it loads. */
export type SearchRole = string | null | undefined;

// ─── how a result opens ──────────────────────────────────────

/**
 * - "document": the row is a stored file; open the file preview modal.
 * - "record":   a record with no page of its own yet; open it in the page's
 *               record modal (note, guide, upkeep task, credential, …).
 * - "link":     the record has a page; navigate to it.
 */
export type OpenMode = "document" | "record" | "link";

export interface GroupDef {
  id: string;
  /** Section heading. Plural, because it heads a list. */
  label: string;
  /** Singular, for "on a bill" / "on a pet". */
  kind: string;
  /**
   * The `entity_type` values this group collects. `document` is matched by
   * `file_id` instead (see `groupOf`), because the files collection carries
   * the attached entity's type.
   */
  entityTypes: readonly string[];
  open: OpenMode;
  /** The roles this group is drawn for at all. */
  canSee: (role: SearchRole) => boolean;
}

/** A page that renders one record, by entity type. */
const DETAIL_ROUTES: Readonly<Record<string, (id: string) => string>> = {
  asset: (id) => `/dashboard/assets/${id}`,
  vehicle: (id) => `/dashboard/vehicles/${id}`,
  pet: (id) => `/dashboard/pets/${id}`,
  property: (id) => `/dashboard/properties/${id}`,
  vendor: (id) => `/dashboard/vendors/${id}`,
  bill: (id) => `/dashboard/bills/${id}`,
  loan: (id) => `/dashboard/loans/${id}`,
  recipe: (id) => `/dashboard/recipes/${id}`,
  calendar_event: () => "/dashboard/calendar",
  calendar: () => "/dashboard/calendar",
  maintenance_task: () => "/dashboard/maintenance",
  maintenance_schedule: () => "/dashboard/maintenance",
  // #5503: the family reference (the KB). Its address is its path, so the id a
  // hit carries IS the route — a doc found here opens the doc, not a modal.
  kb_doc: (id) => `/dashboard/guides/reference/${id}`,
};

/** The page for a record, or null when the build has none. */
export function detailRoute(entityType: string, entityId: string): string | null {
  const build = DETAIL_ROUTES[entityType];
  return build ? build(entityId) : null;
}

/**
 * The grouping, in display order. The first eight are the areas the ticket and
 * the war games name (events, notes, documents, bills, people, lists, recipes,
 * contacts); upkeep, things, guides and credentials follow so that everything
 * the index can hold has a home and nothing falls into an unnamed bucket.
 */
export const GROUPS: readonly GroupDef[] = [
  {
    id: "event",
    label: "Events",
    kind: "event",
    entityTypes: ["calendar_event", "calendar"],
    open: "link",
    canSee: (role) => canSeeEntity(role, "calendar_event"),
  },
  {
    id: "note",
    label: "Notes",
    kind: "note",
    entityTypes: ["note"],
    open: "record",
    canSee: (role) => canSeeEntity(role, "note"),
  },
  {
    id: "document",
    label: "Documents",
    kind: "document",
    entityTypes: ["document", "file"],
    open: "document",
    canSee: (role) => canSeeFiles(role),
  },
  {
    id: "bill",
    label: "Bills",
    kind: "bill",
    entityTypes: ["bill"],
    open: "link",
    canSee: (role) => canSeeEntity(role, "bill"),
  },
  {
    id: "person",
    label: "People",
    kind: "person",
    entityTypes: ["person"],
    open: "record",
    canSee: (role) => canSeeEntity(role, "person"),
  },
  {
    id: "list",
    label: "Lists",
    kind: "list",
    entityTypes: ["list", "list_item"],
    open: "record",
    canSee: (role) => canSeeEntity(role, "list"),
  },
  {
    id: "recipe",
    label: "Recipes",
    kind: "recipe",
    entityTypes: ["recipe", "meal_plan", "meal_feedback"],
    open: "link",
    canSee: (role) => canSeeEntity(role, "recipe"),
  },
  {
    id: "contact",
    label: "Contacts",
    kind: "contact",
    entityTypes: ["vendor"],
    open: "link",
    canSee: (role) => canSeeEntity(role, "vendor"),
  },
  {
    id: "upkeep",
    label: "Upkeep",
    kind: "task",
    entityTypes: ["maintenance_task", "maintenance_schedule", "maintenance"],
    open: "link",
    canSee: (role) => canSeeEntity(role, "maintenance_task"),
  },
  {
    id: "thing",
    label: "Things",
    kind: "thing",
    entityTypes: ["asset", "vehicle", "pet", "property", "room", "loan"],
    open: "link",
    canSee: (role) =>
      canSeeEntity(role, "asset") ||
      canSeeEntity(role, "property") ||
      canSeeEntity(role, "vehicle") ||
      canSeeEntity(role, "pet") ||
      canSeeEntity(role, "loan"),
  },
  {
    id: "guide",
    label: "Guides",
    kind: "guide",
    entityTypes: ["guide"],
    open: "record",
    canSee: (role) => canSeeEntity(role, "guide"),
  },
  {
    // #5503: the family reference (kb_docs). A SEPARATE group from Guides
    // because it is a separate thing to a reader — a procedure you follow vs a
    // thing you look up — and because merging them would put a doc's path and
    // a guide's step count in one indistinguishable list.
    //
    // The KB is readable by EVERY role (ResKB read = rolesEveryone, and
    // migration 076 added kb_doc to role_can_see_entity for exactly that
    // reason: the sitter needs the router doc), so this group is drawn for
    // everyone the Guides section reaches. A vendor can read a doc by API but
    // has no nav to the surface; the API still answers their search.
    id: "kb_doc",
    label: "Reference",
    kind: "doc",
    entityTypes: ["kb_doc"],
    open: "link",
    canSee: (role) => canSeeEntity(role, "kb_doc"),
  },
  {
    id: "credential",
    label: "Credentials",
    kind: "credential",
    entityTypes: ["secret"],
    open: "record",
    // The heading appears only for a role that may read credentials at all, so
    // "there is a Credentials section" never tells a housesitter anything.
    canSee: (role) => canSeeSecrets(role),
  },
];

/** The groups this role is drawn for, in display order. */
export function visibleGroups(role: SearchRole): GroupDef[] {
  return GROUPS.filter((g) => g.canSee(role));
}

/**
 * Which group a hit belongs to, or null when the build does not know the type.
 *
 * A hit carrying `file_id` is a document whatever its `entity_type` says (the
 * files collection records the attached entity's type, not the file's).
 */
export function groupOf(hit: SearchHit): GroupDef | null {
  if (hit.file_id) {
    return GROUPS.find((g) => g.id === "document") ?? null;
  }
  const type = hit.entity_type || "";
  return GROUPS.find((g) => g.entityTypes.includes(type)) ?? null;
}

export interface GroupedHit {
  group: GroupDef;
  hit: SearchHit;
  /** Stable per-row key: one file is one row. */
  key: string;
}

export interface Group {
  def: GroupDef;
  hits: GroupedHit[];
}

/**
 * Group the hits this role may be shown, in group order, dropping empty
 * groups. Hits of a type the role may not see are dropped even though the API
 * should already have removed them: the role gate is cheap, and a policy bug
 * on the server must not become a heading on the client.
 *
 * A repeated (entity_id, file_id) within one group is kept once — the API
 * collapses files already, so this is only insurance against a duplicate row.
 */
export function groupHits(hits: readonly SearchHit[], role: SearchRole): Group[] {
  const visible = new Map(visibleGroups(role).map((g) => [g.id, g]));
  const byGroup = new Map<string, Group>();
  const seen = new Set<string>();

  for (const hit of hits) {
    const def = groupOf(hit);
    if (!def) continue;
    const drawn = visible.get(def.id);
    if (!drawn) continue;
    const key = `${def.id}:${hit.entity_id}:${hit.file_id ?? ""}`;
    if (seen.has(key)) continue;
    seen.add(key);
    const group = byGroup.get(def.id) ?? { def: drawn, hits: [] };
    group.hits.push({ group: drawn, hit, key });
    byGroup.set(def.id, group);
  }

  return GROUPS.map((g) => byGroup.get(g.id)).filter((g): g is Group => !!g && g.hits.length > 0);
}

/** How many rows are shown in total (what the header counts). */
export function totalRows(groups: readonly Group[]): number {
  return groups.reduce((n, g) => n + g.hits.length, 0);
}

// ─── the record behind a hit ─────────────────────────────────

/**
 * Each type's own record, as far as `factsFor` reads it. Every field is
 * optional: the page passes whatever `GET /<type>/<id>` answered, and a fact
 * is shown only when it exists. Nothing here is defaulted — a missing amount
 * prints no amount, rather than "$0.00".
 */
export interface RecordFacts {
  /* documents (files) */
  category?: string | null;
  document_date?: string | null;
  uploaded_by_name?: string | null;
  /* notes */
  author_name?: string | null;
  entity_type?: string | null;
  updated_at?: string | null;
  created_at?: string | null;
  /* bills */
  amount?: string | null;
  frequency?: string | null;
  due_day?: number | null;
  due_date?: string | null;
  paid_date?: string | null;
  paid_for_due_date?: string | null;
  /* things */
  property_id?: string | null;
  vehicle_id?: string | null;
  asset_id?: string | null;
  address?: string | null;
  property_type?: string | null;
  species?: string | null;
  breed?: string | null;
  manufacturer?: string | null;
  model?: string | null;
  color?: string | null;
  license_plate?: string | null;
  /* contacts */
  trades?: readonly string[] | null;
  specialty?: string | null;
  phone?: string | null;
  /* people */
  display_name?: string | null;
  relationship?: string | null;
  allergies?: readonly string[] | null;
  /* lists */
  item_count?: number | null;
  open_count?: number | null;
  /* upkeep */
  status?: string | null;
  /* guides */
  tags?: readonly string[] | null;
  body?: string | null;
  /* credentials */
  secret_type?: string | null;
  /* documents, for the preview modal */
  name?: string | null;
  content_type?: string | null;
  size?: number | null;
  text_origin?: string | null;
  text_verified?: boolean | null;
  /* recipes */
  prep_minutes?: number | null;
  cook_minutes?: number | null;
  servings?: number | null;
  title?: string | null;
}

/** What the page knows beyond the record itself. */
export interface FactContext {
  /** The household's today, YYYY-MM-DD (bills are dated in the family's zone). */
  today?: string | null;
  /** property id → name, so a thing can say whose it is. */
  propertyNames?: ReadonlyMap<string, string> | null;
}

/** One short fact on a row: `label: value` read together, e.g. "Due Wed". */
export interface Fact {
  label: string;
  value: string;
}

/** "utilities" → "Utilities"; "" / null → "". */
function humanize(value: string | null | undefined): string {
  const s = (value ?? "").trim();
  if (!s) return "";
  return s.charAt(0).toUpperCase() + s.slice(1).replace(/_/g, " ");
}

/**
 * A record date as "Nov 15, 2026" — the year always, like the note rows'
 * formatNoteDate (#4564) and for the same reason: a document's date is how a
 * person tells this year's insurance card from last year's, and a bare "Nov 15"
 * cannot do that. Civil dates (YYYY-MM-DD) are read as UTC so the browser's
 * zone cannot move a document back a day.
 */
export function formatShortDate(iso: string | null | undefined, timeZone?: string): string {
  if (!iso) return "";
  const civil = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso);
  const d = civil ? new Date(Date.UTC(+civil[1], +civil[2] - 1, +civil[3])) : new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  // `timeZone` is always explicit (#5316): a civil date is read as UTC so the
  // browser's zone cannot move it, and an instant is read in the household's
  // zone so two people in the same family see the same day.
  return d.toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
    timeZone: civil ? "UTC" : timeZone ?? "UTC",
  });
}

/** A bill's next due date and whether the last payment covers it. */
export interface BillState {
  due: string | null;
  paid: boolean;
  dueLabel: string;
}

/**
 * The bill's state, using the browser's copy of the one due rule
 * (`lib/today/due.ts` — the same rule the API, MCP, calendar mirror and worker
 * run). A bill's state is never inferred from a calendar event (R1's false
 * "electric still unpaid"), and a paid bill says so instead of showing a due
 * date it no longer has.
 */
export function billState(record: RecordFacts, ctx: FactContext): BillState | null {
  if (!ctx.today) return null;
  const due = billDueFor(ctx.today, {
    frequency: record.frequency ?? "",
    due_day: record.due_day ?? null,
    due_date: record.due_date ?? null,
    paid_date: record.paid_date ?? null,
    paid_for_due_date: record.paid_for_due_date ?? null,
  });
  if (!due) return null;
  if (due.status === "paid") return { due: due.next, paid: true, dueLabel: "Paid" };
  if (!due.next) return { due: null, paid: false, dueLabel: "" };
  return { due: due.next, paid: false, dueLabel: `Due ${formatDueLabel(due.next, ctx.today)}` };
}

/**
 * A readable singular for an entity kind, for "on a <kind>".
 *
 * Deliberately its own table rather than the group's `kind`: a note on a
 * property is "on a property", not "on a thing", because the group a kind sits
 * in says where search files it, not what the record is.
 */
const KIND_LABELS: Readonly<Record<string, string>> = {
  household: "household note",
  property: "property",
  room: "room",
  asset: "thing",
  vehicle: "vehicle",
  pet: "pet",
  vendor: "contact",
  bill: "bill",
  loan: "loan",
  maintenance: "upkeep task",
  maintenance_task: "upkeep task",
  maintenance_schedule: "upkeep schedule",
  calendar: "calendar",
  calendar_event: "event",
  note: "note",
  file: "document",
  document: "document",
  secret: "credential",
  person: "person",
  list: "list",
  list_item: "list",
  recipe: "recipe",
  meal_plan: "meal plan",
  guide: "guide",
};

/** "pet" → "pet"; a kind this build does not know → "" (the fact is omitted). */
export function entityKindLabel(kind: string | null | undefined): string {
  if (!kind) return "";
  return KIND_LABELS[kind] ?? "";
}

/**
 * The body line for a row: the hit's own text, unless the facts above already
 * say the same thing.
 *
 * The index body is what matched (a bill's category, a file's description), and
 * the row's facts are often built from exactly those fields — so a bill whose
 * only body is "utilities" would otherwise print "Category Utilities" and
 * "utilities" one under the other. When every meaningful word of the snippet is
 * already on the row, the snippet is dropped; a snippet that adds anything
 * stays.
 */
export function rowSnippet(body: string | null | undefined, facts: readonly Fact[], max = 160): string {
  const s = snippet(body, max);
  if (!s) return "";
  const hay = tokens(facts.map((f) => `${f.label} ${f.value}`).join(" ")).join(" ");
  const words = tokens(s);
  if (words.length === 0) return "";
  return words.every((w) => hay.includes(w)) ? "" : s;
}

/** Lower-case word tokens, for comparing a snippet with the facts. */
function tokens(text: string): string[] {
  return text
    .toLowerCase()
    .replace(/…$/, "")
    .split(/[^a-z0-9]+/)
    .filter((w) => w.length > 2);
}

/**
 * The facts a row shows under its title, by the group the hit landed in.
 *
 * The rule: two or three facts that decide "is this the one?". Everything here
 * comes from the record; when the record has not loaded (or the read failed)
 * the row simply shows its snippet, never a placeholder that looks like data.
 */
export function factsFor(group: GroupDef, record: RecordFacts | null | undefined, ctx: FactContext = {}): Fact[] {
  if (!record) return [];
  const facts: Fact[] = [];
  const push = (label: string, value: string | null | undefined) => {
    if (value) facts.push({ label, value });
  };
  const propertyName = (id: string | null | undefined) => (id && ctx.propertyNames?.get(id)) || "";

  switch (group.id) {
    case "document": {
      push("Category", humanize(record.category));
      push("Dated", formatShortDate(record.document_date));
      push("Uploaded by", record.uploaded_by_name ?? "");
      break;
    }
    case "note": {
      push("Author", record.author_name ?? "");
      push("Date", formatShortDate(record.updated_at ?? record.created_at));
      push("On a", entityKindLabel(record.entity_type));
      break;
    }
    case "bill": {
      push("Amount", formatMoney(record.amount) ?? "");
      const state = billState(record, ctx);
      push("", state?.dueLabel ?? "");
      push("For", propertyName(record.property_id));
      push("Category", humanize(record.category));
      break;
    }
    case "thing": {
      push("Category", humanize(record.category));
      push("Model", [record.manufacturer, record.model].filter(Boolean).join(" "));
      push("Species", record.species ?? "");
      push("Breed", record.breed ?? "");
      push("Address", record.address ?? "");
      push("Type", humanize(record.property_type));
      push("Colour", record.color ?? "");
      push("Plate", record.license_plate ?? "");
      push("At", propertyName(record.property_id));
      break;
    }
    case "contact": {
      const trades = (record.trades ?? []).filter(Boolean).map((t) => humanize(t));
      push("Trades", trades.join(", "));
      push("Specialty", record.specialty ?? "");
      push("Phone", record.phone ?? "");
      break;
    }
    case "person": {
      push("Relationship", humanize(record.relationship));
      const allergies = (record.allergies ?? []).filter(Boolean);
      // Allergy text is safety data: named, never summarised as "has allergies".
      push("Allergies", allergies.length ? allergies.join(", ") : "None saved");
      break;
    }
    case "list": {
      if (typeof record.open_count === "number" && typeof record.item_count === "number") {
        push("Items", `${record.open_count} open of ${record.item_count}`);
      }
      push("Updated", formatShortDate(record.updated_at));
      break;
    }
    case "upkeep": {
      push("Status", humanize(record.status));
      push("Due", formatShortDate(record.due_date));
      break;
    }
    case "guide": {
      push("Updated", formatShortDate(record.updated_at));
      push("Tags", (record.tags ?? []).filter(Boolean).join(", "));
      break;
    }
    case "credential": {
      push("Type", humanize(record.secret_type));
      break;
    }
    case "recipe": {
      const minutes = (record.prep_minutes ?? 0) + (record.cook_minutes ?? 0);
      push("Time", minutes > 0 ? `${minutes} min` : "");
      push("Serves", record.servings ? String(record.servings) : "");
      break;
    }
    default:
      break;
  }
  return facts;
}

// ─── snippets ────────────────────────────────────────────────

/**
 * A short piece of the hit's own body, on a word boundary. The index body is
 * where the match came from (a bill's category, a file's description, a note's
 * text), so it is the honest "why this matched" line.
 */
export function snippet(text: string | null | undefined, max = 160): string {
  const s = (text ?? "").replace(/\s+/g, " ").trim();
  if (!s) return "";
  if (s.length <= max) return s;
  const cut = s.slice(0, max);
  const at = cut.lastIndexOf(" ");
  return `${(at > max * 0.6 ? cut.slice(0, at) : cut).trimEnd()}…`;
}

// ─── states ──────────────────────────────────────────────────

export type SearchState = "idle" | "loading" | "error" | "empty" | "results";

export interface StateInput {
  /** The query as submitted (trimmed). */
  query: string;
  loading: boolean;
  error: boolean;
  /** Rows the role may be shown. */
  count: number;
}

/**
 * Which panel the page draws. `idle` and `empty` are deliberately different:
 * nothing typed is not a failed search, and a failed search is not an empty
 * family. A page that conflated them is how "no bills" got said about a family
 * with bills (R1) — "nothing matched" must be a claim about a query, made only
 * after the query ran.
 */
export function searchState({ query, loading, error, count }: StateInput): SearchState {
  if (!query.trim()) return "idle";
  if (error) return "error";
  if (loading) return "loading";
  if (count === 0) return "empty";
  return "results";
}

/** The areas a role's search covers, in display order, as one line. */
export function areasLine(role: SearchRole): string {
  const labels = visibleGroups(role).map((g) => g.label.toLowerCase());
  if (labels.length === 0) return "";
  if (labels.length === 1) return labels[0];
  return `${labels.slice(0, -1).join(", ")} and ${labels[labels.length - 1]}`;
}

/**
 * The copy under an empty result set. It names the query back (so a typo is
 * visible), suggests the cure, and says what search covers FOR THIS ROLE —
 * which is how a housesitter learns search does not include bills without
 * being told something exists and is being withheld.
 */
export function noResultsCopy(query: string, role: SearchRole): { title: string; body: string } {
  const areas = areasLine(role);
  return {
    title: `Nothing matched "${query.trim()}"`,
    body: areas
      ? `Try fewer words, or a different spelling. Search looks through your ${areas}.`
      : "Try fewer words, or a different spelling.",
  };
}

/** The copy in an untouched search box. Never a blank panel. */
export function idleCopy(role: SearchRole): { title: string; body: string } {
  const areas = areasLine(role);
  return {
    title: "Type to search",
    body: areas
      ? `Find anything in your household: ${areas}. Start with a word you remember — "electric", "soccer", "passport".`
      : "Start with a word you remember.",
  };
}

// ─── what a record panel shows ──────────────────────────────

/**
 * The body text a row without a page shows in its record panel: the record's
 * own words, or nothing. A credential's stored value is deliberately NOT here —
 * search never holds it (the secret handler indexes name and type only), so the
 * panel says where the value actually lives instead of implying it is empty.
 */
export function recordText(group: GroupDef, record: RecordFacts | null | undefined): string {
  if (!record) return "";
  if (group.id === "credential") {
    return "The password itself is never part of search — only a credential's name and type are. Open it in Settings → Credentials to read or copy it.";
  }
  return (record.body ?? "").trim();
}

// ─── the one URL a query lives at ───────────────────────────

/**
 * Where a query lives (`#5303`). The Search page renders `?q=`, the sidebar and
 * drawer boxes navigate to it, and the phone's More sheet does too — one rule,
 * so a query typed anywhere can be linked, reloaded and shared, and a second
 * surface cannot invent its own way to spell a search.
 *
 * An empty query drops the parameter rather than leaving `?q=`: the page reads
 * a missing `q` and an empty one the same way, but a bare `?q=` in the address
 * bar looks like a search that has not happened yet.
 */
export function searchHref(q: string | null | undefined): string {
  const trimmed = (q ?? "").trim();
  return trimmed ? `/dashboard/search?q=${encodeURIComponent(trimmed)}` : "/dashboard/search";
}

// ─── narrowing to one area ──────────────────────────────────

/**
 * The groups to draw when a person has narrowed to one area.
 *
 * The narrowing is CLIENT-side, over rows the API already filtered for the
 * role, so a chip can never become a way to ask about a section the role may
 * not search — the chips are built from `visibleGroups` and nothing else.
 *
 * `active` is ignored when the current results have no row in that area: a
 * filter left over from the previous query must not turn a query that DID
 * match into "nothing matched". Narrowing is a view of an answer, never a way
 * to make an answer disappear.
 */
export function narrowGroups(groups: readonly Group[], active: string | null | undefined): Group[] {
  if (!active) return [...groups];
  const narrowed = groups.filter((g) => g.def.id === active);
  return narrowed.length > 0 ? narrowed : [...groups];
}
