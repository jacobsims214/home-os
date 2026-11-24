"use client";

/**
 * Search (#4561) — one box, results grouped by type, every row with enough
 * context to decide without opening it.
 *
 * WHY THIS PAGE LOOKS THE WAY IT DOES (all from the R1 war games, KB
 * discoveries/war-game-r1-*.md):
 *
 *  1. ONE BOX OVER EVERYTHING, GROUPED. R1 found search did not index calendar
 *     events or bills, so "soccer", "date night" and "electric" returned
 *     nothing, and an agent told a parent the family had no bills. The box is
 *     unchanged and the results are grouped by type — a parent looks for "the
 *     bill", not "result #4" — so a hit in a later group is still findable by
 *     its own heading rather than buried in one ranked list.
 *
 *  2. EVERY ROW CARRIES CONTEXT. The endpoint answers with title + body only
 *     (no date, amount or owner), so a bare-title list cannot be judged
 *     without opening every row. Each drawn row is completed from its own
 *     record (`lib/search/records.ts`): amount and due state on a bill,
 *     category/date/who uploaded it on a document, author and date on a note,
 *     whose property a thing belongs to. A record that could not be read shows
 *     its snippet and NO facts — never a plausible-looking zero.
 *
 *  3. THE ROLE POLICY IS RESPECTED, SILENTLY (#4696, #4689,
 *     decisions/2026-11-15-role-policy.md). Groups a role cannot search are not
 *     drawn, the "what search covers" copy is built from the same list, and
 *     every count comes from rows actually shown. There is no "N results
 *     hidden": reporting a hidden count is itself the leak.
 *
 *  4. EMPTY IS NOT NO-RESULTS. An untouched box says "type to search" and what
 *     search covers; a query with no hits names the query and suggests fewer
 *     words; a failed read is its own panel with Try again and never reads as
 *     "nothing matched". No state is a blank panel.
 *
 * WHAT SEARCH CANNOT SEE YET is rendered honestly rather than faked: the
 * Typesense index holds notes, documents, bills, contacts, properties/assets/
 * vehicles/pets, upkeep tasks, guides and credentials. Calendar events, people,
 * lists and recipes are covered only by the MCP's `search_all` (SQL), not by
 * this REST endpoint; their groups exist below and light up the moment the
 * indexer covers them. The page does not client-filter their list endpoints to
 * manufacture a match: that would be substring matching in some groups and
 * semantic search in others, presented as one search.
 *
 * Roles: a housesitter or vendor searches what they may read and sees only
 * those groups; a viewer gets no Credentials group; a vendor gets Things and
 * Upkeep only.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useSearchParams, useRouter } from "next/navigation";
import { Badge, Button, Chip, Group, Modal, Skeleton, Stack, Text, TextInput } from "@mantine/core";
import { IconAlertTriangle, IconSearch, IconX } from "@tabler/icons-react";
import { NAV_ICON_STROKE } from "@/components/shared/nav-config";
import FileViewer from "@/components/files/FileViewer";
import type { FileRecord } from "@/lib/types/api";
import { useGroupFacts } from "@/lib/search/records";
import { useSearchQuery } from "@/lib/search/query";
import { GROUP_ICONS, Panel, ResultGroup, viewerFile } from "@/components/search/SearchResults";
import {
  factsFor,
  idleCopy,
  narrowGroups,
  noResultsCopy,
  recordText,
  searchHref,
  snippet,
  visibleGroups,
  type FactContext,
  type GroupedHit,
  type RecordFacts,
  type SearchHit,
} from "@/lib/search/results";

// ─── the page ────────────────────────────────────────────────

export default function SearchPage() {
  const searchParams = useSearchParams();
  const router = useRouter();

  // The URL is the query (the sidebar box, the drawer box and the phone More
  // sheet all navigate here with ?q=), but the box stays local while typing so
  // each keystroke is not a navigation.
  const urlQuery = (searchParams.get("q") ?? "").trim();
  const [input, setInput] = useState(urlQuery);
  useEffect(() => setInput(urlQuery), [urlQuery]);

  // The request, the debounce, the role gate and the five states are
  // lib/search/query.ts — the same hook the sidebar/drawer box runs (#5303), so
  // there is one search implementation with two surfaces, not two searches.
  const search = useSearchQuery(input, { delayMs: 250, staleTime: 30_000 });
  const { role, submitted, count, state } = search;
  const allGroups = search.groups;

  /** Run a query now (from the "fewer words" button), skipping the debounce. */
  const run = useCallback(
    (q: string) => {
      setInput(q);
      search.runNow(q);
      router.replace(searchHref(q), { scroll: false });
    },
    [router, search],
  );

  // Narrowing to one area is a view of the answer the API already gave for this
  // role — it cannot ask about a section the role may not search, and it falls
  // back to every group rather than blanking a real answer (narrowGroups).
  const [narrow, setNarrow] = useState<string | null>(null);
  useEffect(() => setNarrow(null), [submitted]);
  const groups = useMemo(() => narrowGroups(allGroups, narrow), [allGroups, narrow]);

  // Which groups are showing all their rows. Reset whenever the query changes:
  // "show all" for one search must not silently expand the next one.
  const [expandedGroups, setExpandedGroups] = useState<ReadonlySet<string>>(new Set());
  useEffect(() => setExpandedGroups(new Set()), [submitted]);

  // Facts are read only for rows that will be drawn (the same hook the drawer
  // uses, so a row means the same thing on both surfaces).
  const { records, ctx } = useGroupFacts(groups, role, expandedGroups);

  // ── what a tap opens ──────────────────────────────────────
  const [viewer, setViewer] = useState<{ file: FileRecord } | null>(null);
  const [record, setRecord] = useState<{ item: GroupedHit; record: RecordFacts | undefined } | null>(null);

  const onOpenDocument = useCallback((hit: SearchHit, rec: RecordFacts | undefined) => {
    setViewer({ file: viewerFile(hit, rec) });
  }, []);
  const onOpenRecord = useCallback((item: GroupedHit, rec: RecordFacts | undefined) => {
    setRecord({ item, record: rec });
  }, []);

  const areas = useMemo(() => visibleGroups(role), [role]);
  const errorMessage = search.errorMessage;
  const idle = idleCopy(role);
  const none = noResultsCopy(submitted, role);

  return (
    <div
      className="mx-auto flex w-full max-w-[1120px] flex-col gap-4 px-4 pb-8 pt-4 md:px-6 md:pt-6"
      data-testid="search-page"
    >
      {/* ── The one box ───────────────────────────────────────── */}
      <form
        role="search"
        onSubmit={(e) => {
          e.preventDefault();
          run(input);
        }}
      >
        <TextInput
          value={input}
          onChange={(e) => {
            const value = e.currentTarget.value;
            setInput(value);
            // Clearing the box is a real action: show the idle panel at once
            // (useSearchQuery promotes "" immediately) and drop the ?q=.
            if (!value.trim()) router.replace("/dashboard/search", { scroll: false });
          }}
          placeholder="Search everything — a name, a word from a note, a bill"
          aria-label="Search everything"
          size="md"
          autoFocus
          leftSection={<IconSearch size={18} stroke={NAV_ICON_STROKE} aria-hidden />}
          rightSection={
            input ? (
              <button
                type="button"
                aria-label="Clear search"
                onClick={() => run("")}
                style={{ border: 0, background: "none", color: "var(--ink-3)", cursor: "pointer", width: 32, height: 32, display: "grid", placeItems: "center" }}
              >
                <IconX size={16} stroke={NAV_ICON_STROKE} />
              </button>
            ) : undefined
          }
          rightSectionPointerEvents={input ? "auto" : "none"}
          classNames={{ input: "max-md:[&_input]:min-h-12 max-md:text-base" }}
          data-search-input
        />
      </form>

      {/* The summary line. Counts come from drawn rows only. */}
      {state === "results" && (
        <Group gap={6} wrap="nowrap" data-search-summary>
          <Text size="sm" style={{ color: "var(--ink-2)" }}>
            <Text span fw={600} style={{ color: "var(--ink)" }}>
              {count}
            </Text>{" "}
            {count === 1 ? "result" : "results"} for “{submitted}”
          </Text>
          {groups.length > 1 && (
            <Text size="sm" style={{ color: "var(--ink-3)" }}>
              in {groups.length} areas
            </Text>
          )}
        </Group>
      )}

      {/* Narrowing chips, only when there is more than one area to narrow to.
          They are built from the groups this role's search actually answers, so
          no chip can name an area the role may not search. */}
      {state === "results" && allGroups.length > 1 && (
        <Group
          gap={6}
          wrap="nowrap"
          role="toolbar"
          aria-label="Narrow to one area"
          data-search-filters
          // One scrolling row on a phone (the Today quick-action pattern), so
          // the results start right below rather than two rows down; wrapping
          // again from 768 px, where there is room.
          className="-mx-4 overflow-x-auto px-4 pb-1 [scrollbar-width:none] md:mx-0 md:flex-wrap md:overflow-visible md:px-0 [&::-webkit-scrollbar]:hidden"
        >
          <Chip checked={narrow === null} onChange={() => setNarrow(null)} size="sm" variant="filled">
            All
          </Chip>
          {allGroups.map((g) => {
            const Icon = GROUP_ICONS[g.def.id] ?? IconSearch;
            return (
              // The data attribute sits on a wrapper: Mantine puts the input
              // itself off-screen, so a test (or a tap) targets the label.
              <span key={g.def.id} data-search-filter={g.def.id} style={{ flex: "none" }}>
                <Chip
                  checked={narrow === g.def.id}
                  onChange={() => setNarrow(narrow === g.def.id ? null : g.def.id)}
                  size="sm"
                  variant="filled"
                  icon={<Icon size={14} stroke={NAV_ICON_STROKE} aria-hidden />}
                >
                  {g.def.label}
                </Chip>
              </span>
            );
          })}
        </Group>
      )}

      {/* ── Idle: the box is empty ────────────────────────────── */}
      {state === "idle" && (
        <Panel icon={IconSearch} title={idle.title} body={idle.body} state="idle">
          {areas.length > 0 && (
            <Group gap={6} mt={10} wrap="wrap" aria-label="What search covers">
              {areas.map((g) => {
                const Icon = GROUP_ICONS[g.id] ?? IconSearch;
                return (
                  <Badge
                    key={g.id}
                    variant="light"
                    color="gray"
                    size="sm"
                    leftSection={<Icon size={12} stroke={NAV_ICON_STROKE} />}
                    style={{ textTransform: "none", fontWeight: 500 }}
                    data-search-area={g.id}
                  >
                    {g.label}
                  </Badge>
                );
              })}
            </Group>
          )}
        </Panel>
      )}

      {/* ── Loading ──────────────────────────────────────────── */}
      {state === "loading" && (
        <Stack gap="sm" role="status" aria-busy="true" aria-label="Searching" data-search-state="loading">
          {[0, 1].map((i) => (
            <div key={i}>
              <Skeleton height={16} width={110} radius="sm" mb={8} />
              <Skeleton height={64} radius="md" mb={6} />
              <Skeleton height={64} radius="md" />
            </div>
          ))}
        </Stack>
      )}

      {/* ── Failed to read: NOT "nothing matched" ─────────────── */}
      {state === "error" && (
        <Panel
          icon={IconAlertTriangle}
          tone="alert"
          title="Search didn’t answer"
          body={`${errorMessage} Nothing was changed, and your family’s records are untouched.`}
          state="error"
        >
          <Button variant="default" mt="sm" onClick={() => search.refetch()} className="max-md:min-h-11">
            Try again
          </Button>
        </Panel>
      )}

      {/* ── No results: a claim about the query ──────────────── */}
      {state === "empty" && (
        <Panel icon={IconSearch} title={none.title} body={none.body} state="no-results">
          <Button
            variant="default"
            mt="sm"
            onClick={() => {
              const words = submitted.split(/\s+/).filter(Boolean);
              if (words.length > 1) run(words.slice(0, -1).join(" "));
              else run("");
            }}
            className="max-md:min-h-11"
            data-search-fewer
          >
            {submitted.split(/\s+/).filter(Boolean).length > 1 ? "Try fewer words" : "Clear"}
          </Button>
        </Panel>
      )}

      {/* ── Results, grouped ─────────────────────────────────── */}
      {state === "results" && (
        <div style={{ display: "flex", flexDirection: "column", gap: 20 }} data-search-results>
          {groups.map((g) => (
            <ResultGroup
              key={g.def.id}
              group={g}
              records={records}
              ctx={ctx}
              expanded={expandedGroups.has(g.def.id)}
              onToggle={() =>
                setExpandedGroups((prev) => {
                  const next = new Set(prev);
                  if (next.has(g.def.id)) next.delete(g.def.id);
                  else next.add(g.def.id);
                  return next;
                })
              }
              onOpenRecord={onOpenRecord}
              onOpenDocument={onOpenDocument}
            />
          ))}
        </div>
      )}

      {/* A document opens its preview: the record itself, not a copy of it. */}
      <FileViewer file={viewer?.file ?? null} open={viewer !== null} onClose={() => setViewer(null)} />

      {/* A record with no page of its own (note, guide, upkeep task, credential,
          list, person) opens here, with the facts the row showed and the
          record's own text. */}
      <Modal
        opened={record !== null}
        onClose={() => setRecord(null)}
        title={record?.item.hit.title?.trim() || record?.item.group.kind || "Record"}
        size="lg"
        data-testid="search-record-modal"
      >
        {record && <RecordPanel item={record.item} record={record.record} ctx={ctx} />}
      </Modal>
    </div>
  );
}

/** The body of the record modal: the same facts as the row, plus its text. */
function RecordPanel({
  item,
  record,
  ctx,
}: {
  item: GroupedHit;
  record: RecordFacts | undefined;
  ctx: FactContext;
}) {
  const facts = factsFor(item.group, record, ctx);
  const text = recordText(item.group, record);
  return (
    <Stack gap="sm">
      {facts.length > 0 && (
        <Group gap={12} wrap="wrap" data-record-facts>
          {facts.map((f) => (
            <Text key={`${f.label}:${f.value}`} size="sm" style={{ color: "var(--ink-2)" }}>
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
        </Group>
      )}
      {text ? (
        <Text size="sm" style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere", color: "var(--ink)" }} data-record-body>
          {text}
        </Text>
      ) : (
        // Not loaded, or the record has no body of its own. Say which, rather
        // than showing an empty panel that reads as "this record is blank".
        <Text size="sm" style={{ color: "var(--ink-3)" }} data-record-body>
          {record
            ? snippet(item.hit.body, 400) || "This record has no text of its own."
            : "This record didn’t load. Close and tap the row again."}
        </Text>
      )}
      {item.hit.entity_type === "credential" && (
        <Button
          component={Link}
          href="/dashboard/settings"
          variant="default"
          className="max-md:min-h-11"
          onClick={() => undefined}
        >
          Open credentials in Settings
        </Button>
      )}
    </Stack>
  );
}
