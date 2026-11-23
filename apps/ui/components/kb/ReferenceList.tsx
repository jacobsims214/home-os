"use client";

/**
 * ReferenceList (HOME-OS-5503): the family reference — every KB doc, on a
 * phone.
 *
 * WHY IT IS A TAB INSIDE GUIDES AND NOT A NAV TILE OF ITS OWN. Guides (#4570)
 * and the KB are both title + markdown + tags, both in household_search, both
 * readable by a housesitter. Two tiles for two markdown stores is the "one rule
 * in two homes" defect: a person with a router fix has to guess which of two
 * nearly identical pages it belongs on. One section, one switch, and the
 * difference stated in one sentence on each side.
 *
 * THE DIFFERENCE, AS THE UI SAYS IT. A guide is a procedure you follow (the
 * step cards, the tick); the reference is a thing you look up (the router's
 * password, the vet's after-hours line, which night the bins go out). That is
 * why this list is grouped by PATH: the path is the doc's address, and the
 * address is the filing system. A flat list of titles would be a pile.
 *
 * MOBILE FIRST (390 px, the width the wave is built for):
 *
 * - Grouped rows, one per doc: the title, then the path's remainder ("pets /
 *   vet-after-hours" under the "How to" heading) and the excerpt. The title
 *   wraps rather than pushing the row sideways ([overflow-wrap:anywhere]); the
 *   row is a 44 px+ link with a chevron.
 * - A 44 px, 16 px-font search box (no iOS zoom). TYPING IS THE API'S JOB: the
 *   box debounces into `GET /kb?q=` (semantic + keyword), because the whole
 *   point of the feature is "what do we do if the dog is sick at night"
 *   finding how-to/pets/vet-after-hours without sharing a word with its path.
 *   Clearing the box returns to the grouped list. The already-loaded list is
 *   NOT filtered as a second, quieter search: that would give substring
 *   matching and semantic matching the same heading.
 * - Chips filter by top-level folder, from the list already loaded — that is a
 *   filter, not a search, and it is labelled as one.
 * - "New doc" is a 44 px button drawn only for the roles that may write
 *   (ResKB write = family roles). A housesitter reads everything here and sees
 *   no write control at all — and the API refuses writes from them anyway.
 *
 * NOT a bare `<ul>` of everything: a family's reference will hold dozens of
 * docs across a handful of folders ("how-to", "money", "sitters"), and an
 * ungrouped list stops being browsable around twenty.
 */

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { Button, Skeleton, TextInput } from "@mantine/core";
import {
  IconAlertTriangle,
  IconBookmark,
  IconChevronRight,
  IconFolder,
  IconPlus,
  IconSearch,
  IconX,
} from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { kbKeys } from "@/lib/query-keys";
import { useNavRole } from "@/components/shared/use-nav-role";
import {
  authorLabel,
  canWriteKb,
  formatKbDate,
  groupByTop,
  humanizeSegment,
  pathWithinTop,
  rowSummary,
  topSegment,
  type KbSummary,
} from "@/lib/kb/model";

const STROKE = 1.75;

/** How long typing settles before the KB is asked. */
export const SEARCH_DEBOUNCE_MS = 300;

/** The API answers more than the page draws; the whole point is the top hits. */
export const SEARCH_LIMIT = 20;

/**
 * The search as the API should see it: trimmed, and long enough to be worth a
 * round trip. One character matches almost every doc by meaning, which is a
 * useless answer and a request per keystroke.
 */
export function effectiveQuery(typed: string): string {
  const q = String(typed ?? "").trim();
  return q.length < 2 ? "" : q.slice(0, 500);
}

/** The request path for the list or the search, from one box's value. */
export function kbQueryPath(q: string, prefix?: string): string {
  if (q) return `/api/v1/kb?q=${encodeURIComponent(q)}`;
  if (prefix) return `/api/v1/kb?prefix=${encodeURIComponent(prefix)}`;
  return "/api/v1/kb";
}

function DocRow({ doc, showTop }: { doc: KbSummary; showTop: boolean }) {
  const summary = rowSummary(doc);
  const where = showTop ? doc.path : pathWithinTop(doc.path);
  const when = formatKbDate(doc.updated_at);
  const who = authorLabel(doc);

  return (
    <Link
      href={`/dashboard/guides/reference/${doc.path}`}
      data-kb-doc-row={doc.path}
      className="flex min-h-11 items-start gap-3 rounded-token-md border border-line bg-surface p-3.5 shadow-token-sm transition-colors hover:bg-surface-2 active:bg-surface-2"
    >
      <span
        aria-hidden
        className="mt-0.5 grid size-9 shrink-0 place-items-center rounded-token-sm bg-surface-2 text-ink-3"
      >
        <IconBookmark size={18} stroke={STROKE} />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-[15px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">
          {doc.title}
        </span>
        {/* The address, not a date: this is how the family files things, and
            two docs with the same title ("Vet") are told apart by it. */}
        <span className="mt-0.5 block font-mono text-[12px] leading-snug text-ink-3 [overflow-wrap:anywhere]">
          {where}
        </span>
        {summary && (
          <span className="mt-1 line-clamp-2 block text-sm text-ink-2 [overflow-wrap:anywhere]">{summary}</span>
        )}
        {(when || who) && (
          <span className="mt-1.5 block text-xs text-ink-3">
            {who ? `${who} · ` : ""}
            {when ? `updated ${when}` : ""}
          </span>
        )}
      </span>
      <IconChevronRight size={20} stroke={STROKE} className="mt-1.5 shrink-0 text-ink-3" aria-hidden />
    </Link>
  );
}

export default function ReferenceList({ onNewDoc }: { onNewDoc: () => void }) {
  const role = useNavRole();
  const canWrite = canWriteKb(role);
  const [typed, setTyped] = useState("");
  const [query, setQuery] = useState("");
  const [folder, setFolder] = useState<string | null>(null);

  // Typing settles before it becomes a request: a round trip per keystroke is
  // what makes a search box feel broken on a phone.
  useEffect(() => {
    const next = effectiveQuery(typed);
    if (next === query) return;
    const t = window.setTimeout(() => setQuery(next), SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(t);
  }, [typed, query]);

  const listing = useQuery({
    queryKey: query ? kbKeys.search(query) : kbKeys.list(),
    queryFn: ({ signal }) =>
      apiFetch<{ data: KbSummary[]; total: number }>(kbQueryPath(query), { signal }),
    // A search keeps the previous result on screen while the next one is in
    // flight, so the list does not blink to a skeleton on every letter.
    placeholderData: (prev) => prev,
  });

  const docs = useMemo(() => listing.data?.data ?? [], [listing.data]);
  const folders = useMemo(
    () => Array.from(new Set(docs.map((d) => topSegment(d.path)))).sort((a, b) => a.localeCompare(b)),
    [docs],
  );
  const shown = useMemo(
    () => (folder ? docs.filter((d) => topSegment(d.path) === folder) : docs).slice(0, SEARCH_LIMIT),
    [docs, folder],
  );
  // A folder chip that the new result set no longer has would filter the page
  // to nothing with no way back, so it is dropped as soon as it is gone.
  useEffect(() => {
    if (folder && !folders.includes(folder)) setFolder(null);
  }, [folder, folders]);

  const groups = useMemo(() => groupByTop(shown, Boolean(query)), [shown, query]);
  const searching = Boolean(query);

  return (
    <div className="min-w-0" data-kb-reference>
      {/* The distinction, in one sentence, at the top of the page it applies
          to. A person choosing between the two tabs should not have to read a
          ticket or a comment to know which one to use. */}
      <div className="mb-3 rounded-token-md border border-line bg-surface-2 px-3.5 py-3">
        <p className="text-sm leading-relaxed text-ink-2">
          Things you <strong className="font-semibold text-ink">look up</strong>, by address: the
          router&apos;s password, the vet&apos;s after-hours line, what the sitter needs. A{" "}
          <em>guide</em> is a procedure you follow step by step.
        </p>
      </div>

      <div className="mb-3 flex items-end gap-2">
        <TextInput
          type="search"
          value={typed}
          onChange={(e) => setTyped(e.currentTarget.value)}
          placeholder="Search the reference"
          aria-label="Search the family reference"
          leftSection={<IconSearch size={16} stroke={STROKE} aria-hidden />}
          rightSection={
            typed ? (
              <button
                type="button"
                onClick={() => setTyped("")}
                aria-label="Clear search"
                className="grid size-8 place-items-center rounded-full text-ink-3 hover:bg-surface-2 hover:text-ink"
              >
                <IconX size={16} stroke={STROKE} aria-hidden />
              </button>
            ) : undefined
          }
          classNames={{ root: "min-w-0 flex-1", input: "max-md:min-h-11 max-md:text-base" }}
          data-kb-search
        />
        {canWrite && (
          <Button
            onClick={onNewDoc}
            leftSection={<IconPlus size={16} stroke={STROKE} aria-hidden />}
            aria-haspopup="dialog"
            className="shrink-0 max-md:min-h-11"
            data-kb-new
          >
            New doc
          </Button>
        )}
      </div>

      {folders.length > 1 && !searching && (
        <div className="-mx-4 mb-3 overflow-x-auto px-4" role="group" aria-label="Filter by folder">
          <div className="flex w-max items-center gap-2">
            <FolderChip active={folder === null} onClick={() => setFolder(null)}>
              All {docs.length}
            </FolderChip>
            {folders.map((f) => (
              <FolderChip key={f} active={folder === f} onClick={() => setFolder(f === folder ? null : f)}>
                {humanizeSegment(f)}
              </FolderChip>
            ))}
          </div>
        </div>
      )}

      {listing.isLoading && (
        <div role="status" aria-busy="true" aria-label="Loading the reference" className="space-y-2">
          {[1, 2, 3, 4].map((i) => (
            <div key={i} className="space-y-2 rounded-token-md border border-line bg-surface p-4 shadow-token-sm">
              <Skeleton height={14} width="55%" radius="sm" />
              <Skeleton height={12} width="80%" radius="sm" />
            </div>
          ))}
        </div>
      )}

      {listing.isError && (
        <div
          role="alert"
          data-kb-error
          className="flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
        >
          <IconAlertTriangle size={20} stroke={STROKE} className="shrink-0" aria-hidden />
          <p className="flex-1 text-sm">
            Couldn&apos;t load the reference. Check your connection and try again.
          </p>
          <Button
            variant="default"
            onClick={() => listing.refetch()}
            loading={listing.isRefetching}
            className="max-md:min-h-11"
          >
            Try again
          </Button>
        </div>
      )}

      {!listing.isLoading && !listing.isError && shown.length === 0 && searching && (
        <div className="rounded-token-md border border-dashed border-line px-4 py-10 text-center">
          <p className="text-sm text-ink-3">Nothing in the reference matches “{query}”.</p>
          <Button variant="subtle" onClick={() => setTyped("")} className="mt-2 max-md:min-h-11">
            Clear search
          </Button>
        </div>
      )}

      {!listing.isLoading && !listing.isError && shown.length === 0 && !searching && (
        <div className="flex flex-col items-center justify-center gap-3 rounded-token-md border-2 border-dashed border-line px-4 py-12 text-center">
          <p className="text-sm text-ink-3">
            Nothing in the reference yet. Write down the router&apos;s password, the plumber&apos;s number,
            which night the bins go out — the things you end up texting someone to ask.
          </p>
          {canWrite && (
            <Button
              variant="light"
              onClick={onNewDoc}
              leftSection={<IconPlus size={16} stroke={STROKE} aria-hidden />}
              className="max-md:min-h-11"
            >
              Write the first doc
            </Button>
          )}
        </div>
      )}

      {shown.length > 0 && (
        <div className="space-y-4">
          {groups.map((group) => (
            <section key={group.top} aria-label={group.label} data-kb-group={group.top}>
              <h3 className="mb-1.5 flex items-center gap-1.5 px-0.5 text-xs font-semibold uppercase tracking-wide text-ink-3">
                <IconFolder size={14} stroke={STROKE} aria-hidden />
                {group.label}
                <span className="font-normal normal-case tracking-normal">({group.docs.length})</span>
              </h3>
              <ul aria-label={`${group.label} documents`} className="m-0 list-none space-y-2 p-0">
                {group.docs.map((doc) => (
                  <li key={doc.path} className="min-w-0">
                    {/* A search hit is a whole address, so the row shows the
                        whole path rather than a remainder under a heading that
                        would imply a filing order the ranking does not have. */}
                    <DocRow doc={doc} showTop={searching || Boolean(folder)} />
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      )}

      {/* Say what the box is doing, because the two states are different
          searches and a reader deserves to know which answered. */}
      {searching && shown.length > 0 && (
        <p className="mt-3 text-xs text-ink-3" data-kb-search-mode>
          Best matches for “{query}”, by meaning and by word, best first.
        </p>
      )}
    </div>
  );
}

function FolderChip({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={`inline-flex min-h-9 shrink-0 items-center rounded-full border px-3 text-sm font-medium transition-colors ${
        active
          ? "border-transparent bg-accent-fill text-white"
          : "border-line bg-surface text-ink-2 hover:bg-surface-2"
      }`}
    >
      {children}
    </button>
  );
}
