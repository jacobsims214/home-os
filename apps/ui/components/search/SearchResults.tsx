"use client";

/**
 * The pieces every search surface draws (#4561, shared for #5303).
 *
 * The Search page renders these; the sidebar/drawer box renders the same
 * `ResultRow`s in its dropdown. They were private to
 * `app/(dashboard)/dashboard/search/page.tsx` until #5303: the drawer box was
 * wired to nothing, and the ticket's rule is to reuse the working search rather
 * than write a second one — the drop-down therefore shows the SAME row, with the
 * same facts and the same tap target, and a row means the same thing whether it
 * was found from the page or from the drawer.
 *
 * Everything here is presentational. The grouping and the role gate come from
 * `lib/search/results.ts`; the request and the five states from
 * `lib/search/query.ts`; the record reads from `lib/search/records.ts`.
 *
 * A row is never a control that does nothing: a record with a page is a link,
 * a document opens the file preview it already has, and anything else opens its
 * own panel — the caller decides which of the last two it can host (the page
 * hosts both, the drawer navigates to the page that does).
 */

import { useMemo } from "react";
import Link from "next/link";
import { Button, Card, Group, Text, Title } from "@mantine/core";
import {
  IconArrowUpRight,
  IconBook,
  IconCalendarEvent,
  IconChevronRight,
  IconFileText,
  IconHome,
  IconKey,
  IconListCheck,
  IconNote,
  IconSearch,
  IconTool,
  IconToolsKitchen,
  IconUsers,
  IconWallet,
  type Icon as TablerIcon,
} from "@tabler/icons-react";
import { NAV_ICON_STROKE } from "@/components/shared/nav-config";
import type { FileRecord } from "@/lib/types/api";
import {
  detailRoute,
  factsFor,
  rowSnippet,
  type FactContext,
  type Group as SearchGroup,
  type GroupedHit,
  type RecordFacts,
  type SearchHit,
} from "@/lib/search/results";
import { COLLAPSED_ROWS, recordId } from "@/lib/search/records";

/** One icon set, one stroke: icons support the labels, they never replace them. */
export const GROUP_ICONS: Readonly<Record<string, TablerIcon>> = {
  event: IconCalendarEvent,
  note: IconNote,
  document: IconFileText,
  bill: IconWallet,
  person: IconUsers,
  list: IconListCheck,
  recipe: IconToolsKitchen,
  contact: IconUsers,
  upkeep: IconTool,
  thing: IconHome,
  guide: IconBook,
  credential: IconKey,
};

/** The icon for a group id, with a neutral fallback for a group this build doesn't know. */
export function groupIcon(id: string): TablerIcon {
  return GROUP_ICONS[id] ?? IconSearch;
}

/** "1 bill" / "3 bills" — from the drawn rows only. */
export function countLabel(group: SearchGroup): string {
  const n = group.hits.length;
  return `${n} ${n === 1 ? group.def.kind : group.def.label.toLowerCase()}`;
}

// ─── one row ─────────────────────────────────────────────────

interface RowProps {
  item: GroupedHit;
  record: RecordFacts | undefined;
  ctx: FactContext;
  onOpenRecord: (item: GroupedHit, record: RecordFacts | undefined) => void;
  onOpenDocument: (hit: SearchHit, record: RecordFacts | undefined) => void;
  /**
   * The narrow variant for a panel (~230 px): a smaller icon tile, tighter
   * padding and ONE line of facts that truncates, rather than three wrapped
   * ones. Same component, same data — only the room differs, because a second
   * row implementation for the drawer is how the two surfaces drift apart.
   */
  dense?: boolean;
}

/**
 * One result. The whole row is the target — one obvious tap area of at least
 * 48 px on a phone. A record with a page is a real link; a document opens the
 * file preview; anything else opens its record panel so a row never looks
 * tappable and then does nothing.
 *
 * `onOpenDocument` receives the hit and whatever record was read so far, so a
 * host that cannot draw the preview (the drawer) can pass a handler that
 * navigates to the document instead: the row's shape is shared, the action is
 * the host's.
 */
export function ResultRow({ item, record, ctx, onOpenRecord, onOpenDocument, dense = false }: RowProps) {
  const { group, hit } = item;
  const Icon = groupIcon(group.id);
  const facts = useMemo(() => factsFor(group, record, ctx), [group, record, ctx]);
  const body = dense ? "" : rowSnippet(hit.body, facts, facts.length > 0 ? 110 : 160);
  // In a ~230 px panel there is room for the two facts that decide "is this the
  // one" (a bill's amount and due state; a note's author and date). Three of
  // them side by side are three ellipses, which tells a person nothing — so the
  // third is dropped rather than clipped, and the row's tap opens the record
  // with all of it.
  const shownFacts = dense ? facts.slice(0, 2) : facts;
  const title = hit.title?.trim() || "(untitled)";
  const route = group.open === "link" ? detailRoute(hit.entity_type, hit.entity_id) : null;

  const inner = (
    <>
      <span
        aria-hidden
        style={{
          display: "grid",
          placeItems: "center",
          width: dense ? 26 : 36,
          height: dense ? 26 : 36,
          flex: "none",
          borderRadius: dense ? "var(--r-sm)" : "var(--r-md)",
          background: "var(--surface-2)",
          color: "var(--ink-2)",
        }}
      >
        <Icon size={dense ? 15 : 18} stroke={NAV_ICON_STROKE} />
      </span>
      <span style={{ flex: 1, minWidth: 0, display: "flex", flexDirection: "column", gap: 1 }}>
        <Text
          fw={600}
          size={dense ? "xs" : "sm"}
          style={{
            color: "var(--ink)",
            lineHeight: 1.3,
            // A dense row is one line per part: an ellipsis keeps the list
            // scannable, and the full title is the row's accessible name.
            ...(dense
              ? { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }
              : { overflowWrap: "anywhere" as const }),
          }}
          data-result-title
        >
          {title}
        </Text>

        {/* The facts that decide "is this the one": amount, date, whose it is. */}
        {shownFacts.length > 0 && (
          <span
            style={{
              display: "flex",
              flexWrap: "wrap",
              columnGap: 10,
              rowGap: 2,
              minWidth: 0,
            }}
            data-result-facts
          >
            {shownFacts.map((f) => (
              <Text key={`${f.label}:${f.value}`} size="xs" style={{ color: "var(--ink-2)" }}>
                {f.label ? (
                  <>
                    <span style={{ color: "var(--ink-3)" }}>{f.label} </span>
                    {f.value}
                  </>
                ) : (
                  f.value
                )}
              </Text>
            ))}
          </span>
        )}

        {body && (
          <Text size="xs" style={{ color: "var(--ink-3)", overflowWrap: "anywhere" }} data-result-snippet>
            {body}
          </Text>
        )}
      </span>
      {group.open === "link" ? (
        <IconArrowUpRight
          size={16}
          stroke={NAV_ICON_STROKE}
          style={{ color: "var(--ink-3)", flex: "none" }}
          aria-hidden
        />
      ) : (
        <IconChevronRight
          size={18}
          stroke={NAV_ICON_STROKE}
          style={{ color: "var(--ink-3)", flex: "none" }}
          aria-hidden
        />
      )}
    </>
  );

  const style: React.CSSProperties = {
    display: "flex",
    alignItems: "center",
    gap: dense ? 8 : 12,
    minHeight: dense ? 40 : 48,
    padding: dense ? "7px 8px" : "10px 12px",
    borderTop: "1px solid var(--line)",
    color: "inherit",
    textDecoration: "none",
    background: "var(--surface)",
    width: "100%",
    textAlign: "left",
    cursor: "pointer",
  };

  if (route) {
    return (
      <Link
        href={route}
        style={style}
        data-result-row
        data-result-type={hit.entity_type}
        data-result-id={hit.entity_id}
      >
        {inner}
      </Link>
    );
  }
  return (
    <button
      type="button"
      style={style}
      data-result-row
      data-result-type={hit.entity_type}
      data-result-id={hit.entity_id}
      // NO aria-label: the row's own text IS its accessible name (title, then
      // the facts and snippet a person reads to judge it). An `aria-label="Open
      // <title>"` does not contain the visible facts text, which is an axe
      // label-content-name-mismatch / WCAG 2.5.3 failure — the same mistake the
      // sidebar's account button had (#4752) — and it also hid the amount and
      // date from a screen reader. The button role already says it is a
      // control, so nothing is lost by dropping the word "Open".
      onClick={() =>
        group.open === "document" ? onOpenDocument(hit, record) : onOpenRecord(item, record)
      }
    >
      {inner}
    </button>
  );
}

/** The record a row's facts come from, keyed the way `useRecordFacts` keys it. */
export function rowRecordKey(hit: SearchHit): string {
  return `${hit.entity_type}:${recordId(hit.entity_type, hit.entity_id, hit.file_id)}`;
}

/**
 * A file record for the preview modal. When the record has loaded this IS the
 * API's file record; before it has (or if that read failed) only the hit is
 * known, so the unused required fields are blank and the viewer falls back to
 * offering a download rather than claiming to know the file's type.
 */
export function viewerFile(hit: SearchHit, record: RecordFacts | undefined): FileRecord {
  const known = (record ?? {}) as Partial<FileRecord>;
  return {
    id: hit.file_id || hit.entity_id,
    household_id: known.household_id ?? "",
    blob_id: known.blob_id ?? "",
    name: known.name ?? hit.title ?? "Document",
    content_type: known.content_type ?? "",
    size: typeof known.size === "number" ? known.size : 0,
    ocr_status: known.ocr_status ?? "done",
    tags: known.tags ?? [],
    created_at: known.created_at ?? "",
    updated_at: known.updated_at ?? "",
    text_origin: known.text_origin,
    text_verified: known.text_verified,
    extracted_text: known.extracted_text,
  };
}

// ─── one group ───────────────────────────────────────────────

/**
 * One group heading + its card of rows.
 *
 * `idPrefix` keeps the heading ids unique when two surfaces are mounted at
 * once (the drawer over a search page): an `aria-labelledby` that points at the
 * first match would name the drawer's list after the page's heading.
 */
export function ResultGroup({
  group,
  records,
  ctx,
  expanded,
  onToggle,
  onOpenRecord,
  onOpenDocument,
  idPrefix = "search",
  dense = false,
}: {
  group: SearchGroup;
  records: ReadonlyMap<string, RecordFacts>;
  ctx: FactContext;
  expanded: boolean;
  onToggle?: () => void;
  onOpenRecord: RowProps["onOpenRecord"];
  onOpenDocument: RowProps["onOpenDocument"];
  idPrefix?: string;
  dense?: boolean;
}) {
  const hidden = onToggle ? Math.max(0, group.hits.length - COLLAPSED_ROWS) : 0;
  const shown = expanded ? group.hits : group.hits.slice(0, COLLAPSED_ROWS);
  const headingId = `${idPrefix}-group-${group.def.id}`;
  return (
    <section aria-labelledby={headingId} data-result-group={group.def.id}>
      <Group gap={8} mb={dense ? 4 : 6} wrap="nowrap">
        <Title order={3} id={headingId} style={{ fontSize: dense ? 13 : 15, fontWeight: 600, color: "var(--ink)" }}>
          {group.def.label}
        </Title>
        <Text size="xs" style={{ color: "var(--ink-3)" }}>
          {countLabel(group)}
        </Text>
      </Group>
      <Card padding={0} radius="token-md" withBorder style={{ overflow: "hidden", background: "var(--surface)" }}>
        {shown.map((item) => (
          <ResultRow
            key={item.key}
            item={item}
            record={records.get(rowRecordKey(item.hit))}
            ctx={ctx}
            onOpenRecord={onOpenRecord}
            onOpenDocument={onOpenDocument}
            dense={dense}
          />
        ))}
        {hidden > 0 && onToggle && (
          <Button
            variant="subtle"
            fullWidth
            onClick={onToggle}
            style={{ borderTop: "1px solid var(--line)", borderRadius: 0, minHeight: 44, color: "var(--accent)" }}
            aria-expanded={expanded}
            data-result-more
          >
            {expanded ? "Show fewer" : `Show all ${group.hits.length}`}
          </Button>
        )}
      </Card>
    </section>
  );
}

// ─── a calm panel ────────────────────────────────────────────

/**
 * The idle / loading / error / no-results panel: a 40 px icon tile, a title, a
 * body, and whatever action the state offers. `state` is the machine's own word
 * (`data-search-state`), so a test or a screen reader hears the same thing the
 * page decided. The alert tone is for a failed read only — never for "nothing
 * matched", which is a real answer, not an error.
 */
export function Panel({
  icon: Icon,
  title,
  body,
  children,
  tone = "calm",
  state,
  size = "md",
}: {
  icon: TablerIcon;
  title: string;
  body: string;
  children?: React.ReactNode;
  tone?: "calm" | "alert";
  state: string;
  /** "sm" is the drawer's compact variant; "md" the page's. */
  size?: "sm" | "md";
}) {
  const alert = tone === "alert";
  const small = size === "sm";
  const tile = small ? 32 : 40;
  return (
    <div
      role={alert ? "alert" : undefined}
      data-search-state={state}
      style={{
        display: "flex",
        gap: small ? 10 : 14,
        alignItems: "flex-start",
        padding: small ? "12px 12px" : "18px 16px",
        borderRadius: "var(--r-md)",
        border: "1px solid var(--line)",
        borderLeft: alert ? "4px solid var(--danger)" : "1px solid var(--line)",
        background: "var(--surface)",
      }}
    >
      <span
        aria-hidden
        style={{
          display: "grid",
          placeItems: "center",
          width: tile,
          height: tile,
          flex: "none",
          borderRadius: "var(--r-md)",
          background: alert ? "transparent" : "var(--accent-soft)",
          color: alert ? "var(--danger)" : "var(--accent)",
        }}
      >
        <Icon size={small ? 18 : 22} stroke={NAV_ICON_STROKE} />
      </span>
      <div style={{ minWidth: 0 }}>
        <Text fw={600} size="sm" style={{ color: "var(--ink)" }} data-panel-title>
          {title}
        </Text>
        <Text
          size={small ? "xs" : "sm"}
          mt={2}
          style={{ color: "var(--ink-2)", overflowWrap: "anywhere" }}
          data-panel-body
        >
          {body}
        </Text>
        {children}
      </div>
    </div>
  );
}
