"use client";

/**
 * Lists (#4566): the household's shared checklists — grocery, packing, chores,
 * to-dos, shopping — at /dashboard/lists.
 *
 * Built to the approved mockup (artifact #88 v1, the "Lists index" state) inside
 * the approved shell (#75 v1), and the list-card pattern the rest of the Family
 * Hub wave uses (vehicles #4555, pets #4556, vendors #4557):
 *
 *  - Phone: a kind chip row, then kind sections of cards, each card one link
 *    with a 72 px tap target and a big open count.
 *  - >= 768 px: the same sections on a 3-across grid (the mockup's .kgs).
 *  - "New list" opens a sheet with the kind and a "Start from" picker (blank,
 *    a template, or a past list — the API copies its items unchecked).
 *  - Archived lists are one chip away, and read-only until unarchived.
 *
 * WHY NOT `EntityList`: this is grouped kind sections plus a template rail, not
 * a searchable table of one entity type, so the shared component cannot express
 * it (the same call vehicles, vendors and pets made). Worth saying out loud
 * because `EntityList` defaults `propertyFilter` ON and silently empties any
 * list whose entity has no `property_id` — the bug that shipped on Pets
 * (#4556). Lists are not property-scoped at all, so nothing here can trip it.
 *
 * THE COUNT ON A CARD IS `open_count` FROM THE API, and the check-off
 * optimistically decrements it (lib/lists/api.ts applyOpenCount), so tapping
 * something in an aisle and coming back shows the new count without a reload.
 */

import { useMemo, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { notifications } from "@mantine/notifications";
import { IconArchive, IconChevronRight, IconPlus } from "@tabler/icons-react";
import { ResponsiveModal, ResponsiveModalActions } from "@/components/ui";
import { useNavRole } from "@/components/shared/use-nav-role";
import {
  useAllLists,
  useCreateList,
  useUpdateList,
} from "@/lib/lists/api";
import {
  countCaption,
  kindCounts,
  kindLabel,
  KIND_LABELS,
  partitionLists,
  updatedLabel,
} from "@/lib/lists/model";
import { LIST_KINDS, type ListRow } from "@/lib/lists/types";
import { KindTile, kindIcon } from "@/components/lists/kind";

const STROKE = 1.75;
const MANAGE_ROLES = new Set(["owner", "family_manager", "family_member"]);

export default function ListsPage() {
  const router = useRouter();
  const params = useSearchParams();
  const role = useNavRole();
  const canManage = role == null ? true : MANAGE_ROLES.has(role);

  const listsQuery = useAllLists();
  const createList = useCreateList();
  const updateList = useUpdateList();

  // The archived view is a URL (?archived=1) so a phone's back button and a
  // shared link both work.
  const showArchived = params?.get("archived") === "1";
  const kindFilter = params?.get("kind") ?? "all";

  const [newOpen, setNewOpen] = useState(false);

  const all = useMemo(() => listsQuery.data?.data ?? [], [listsQuery.data]);
  const { active, archived, templates } = useMemo(() => partitionLists(all), [all]);
  const counts = useMemo(() => kindCounts(active), [active]);

  const shown = useMemo(() => {
    const source = showArchived ? archived : active;
    return kindFilter === "all" ? source : source.filter((list) => list.kind === kindFilter);
  }, [showArchived, archived, active, kindFilter]);

  const groups = useMemo(() => {
    const byKind = new Map<string, ListRow[]>();
    for (const list of shown) {
      const bucket = byKind.get(list.kind);
      if (bucket) bucket.push(list);
      else byKind.set(list.kind, [list]);
    }
    return LIST_KINDS.filter((kind) => byKind.has(kind)).map((kind) => ({ kind, lists: byKind.get(kind)! }));
  }, [shown]);

  const setQuery = (next: { archived?: boolean; kind?: string }) => {
    const search = new URLSearchParams();
    const arch = next.archived ?? showArchived;
    const kind = next.kind ?? kindFilter;
    if (arch) search.set("archived", "1");
    if (kind !== "all") search.set("kind", kind);
    const qs = search.toString();
    router.replace(`/dashboard/lists${qs ? `?${qs}` : ""}`, { scroll: false });
  };

  if (listsQuery.isLoading) return <IndexSkeleton />;
  if (listsQuery.error) {
    return (
      <div role="alert" className="rounded-token-md border border-line border-l-4 border-l-danger bg-surface p-4">
        <b className="block text-[15px] font-semibold text-ink">Couldn&rsquo;t load your lists</b>
        <p className="mt-0.5 text-sm text-ink-2">
          {listsQuery.error instanceof Error ? listsQuery.error.message : "Home OS didn’t answer."} Your lists are
          safe; anything you checked off will sync when it&rsquo;s back.
        </p>
        <button
          type="button"
          onClick={() => void listsQuery.refetch()}
          className="mt-3 inline-flex h-9 items-center rounded-token-sm border border-line px-3 text-sm font-semibold text-ink"
        >
          Try again
        </button>
      </div>
    );
  }

  const nothingAtAll = all.length === 0;

  return (
    <div className="min-w-0">
      {/* Chips: kind filters plus Archived. Horizontally scrollable on phones. */}
      <div className="mb-4 flex items-center gap-2">
        <div role="toolbar" aria-label="Filter lists by kind" className="-mx-4 flex min-w-0 flex-1 gap-1.5 overflow-x-auto px-4 pb-0.5 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden md:mx-0 md:flex-wrap md:px-0">
          <Chip pressed={!showArchived && kindFilter === "all"} onClick={() => setQuery({ archived: false, kind: "all" })}>
            All <span className="font-medium text-ink-3">{active.length}</span>
          </Chip>
          {counts.map(({ kind, count }) => (
            <Chip
              key={kind}
              pressed={!showArchived && kindFilter === kind}
              onClick={() => setQuery({ archived: false, kind })}
            >
              {KIND_LABELS[kind]} <span className="font-medium text-ink-3">{count}</span>
            </Chip>
          ))}
          {canManage && (
            <Chip pressed={showArchived} onClick={() => setQuery({ archived: true, kind: "all" })}>
              <IconArchive size={16} stroke={STROKE} aria-hidden />
              Archived <span className="font-medium text-ink-3">{archived.length}</span>
            </Chip>
          )}
        </div>
        {canManage && (
          <button
            type="button"
            onClick={() => setNewOpen(true)}
            className="hidden h-10 shrink-0 items-center gap-1.5 rounded-token-sm bg-accent-fill px-3 text-sm font-semibold text-white md:inline-flex"
          >
            <IconPlus size={18} stroke={2} aria-hidden />
            New list
          </button>
        )}
      </div>

      {nothingAtAll && <EmptyState onNew={() => setNewOpen(true)} canManage={canManage} />}

      {/* Kind sections. On phone each kind is a section; from md it's a grid. */}
      <div className="md:grid md:grid-cols-3 md:gap-3">
        {groups.map(({ kind, lists }) => (
          <section key={kind} className="mb-5 md:mb-0">
            <h3 className="mb-2 flex items-center gap-2 px-0.5 text-[13px] font-semibold text-ink-2">
              <span aria-hidden className="size-2 rounded-full bg-accent" />
              {KIND_LABELS[kind]} <span className="font-medium text-ink-3">{lists.length}</span>
            </h3>
            <ul className="overflow-hidden rounded-token-md border border-line bg-surface md:space-y-2 md:overflow-visible md:border-0 md:bg-transparent">
              {lists.map((list) => (
                <li key={list.id} className="md:rounded-token-md md:border md:border-line md:bg-surface">
                  <ListCard list={list} />
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>

      {showArchived && shown.length === 0 && (
        <p className="mt-2 text-sm text-ink-3">
          No archived lists. Archiving hides a list from Lists without losing anything on it.
        </p>
      )}

      {!showArchived && !nothingAtAll && templates.length > 0 && (
        <section className="mt-2">
          <h3 className="mb-2 px-0.5 text-[13px] font-semibold text-ink-2">
            Templates <span className="font-medium text-ink-3">{templates.length}</span>
          </h3>
          <div className="-mx-4 flex gap-2.5 overflow-x-auto px-4 pb-1 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden md:mx-0 md:flex-wrap md:px-0">
            {templates.map((template) => (
              <button
                key={template.id}
                type="button"
                onClick={() => setNewOpen(true)}
                className="flex w-[168px] shrink-0 flex-col gap-2 rounded-token-md border border-dashed border-line bg-surface p-3 text-left hover:border-accent md:w-[200px]"
              >
                <KindTile kind={template.kind} small />
                <b className="text-sm font-semibold leading-snug text-ink">{template.name}</b>
                <span className="text-xs text-ink-3">
                  {template.item_count} items. {KIND_LABELS[template.kind as keyof typeof KIND_LABELS] ?? "List"}
                </span>
                <span className="text-[13px] font-semibold text-accent">Use template</span>
              </button>
            ))}
          </div>
        </section>
      )}

      {!nothingAtAll && (
        <div className="mt-6 flex items-start gap-2.5 rounded-token-md bg-surface-2 p-3.5 text-[13px] text-ink-2">
          <span aria-hidden className="text-accent">✦</span>
          <span>
            You can also ask the assistant: <q className="font-medium text-ink">add milk and eggs to the grocery list</q>{" "}
            or <q className="font-medium text-ink">check off the turkey</q>.
          </span>
        </div>
      )}

      {/* Phone: the FAB. Desktop: the header button. One create flow, two entry points. */}
      {canManage && !nothingAtAll && (
        <button
          type="button"
          onClick={() => setNewOpen(true)}
          aria-label="New list"
          className="fixed bottom-[calc(72px+env(safe-area-inset-bottom))] right-4 z-30 grid size-14 place-items-center rounded-token-lg bg-accent-fill text-white shadow-token-lg md:hidden"
        >
          <IconPlus size={26} stroke={2} />
        </button>
      )}

      <NewListSheet
        opened={newOpen}
        templates={templates}
        pastLists={archived}
        saving={createList.isPending}
        onClose={() => setNewOpen(false)}
        onCreate={async (name, kind, templateId) => {
          try {
            const res = await createList.mutateAsync({ name, kind, templateId });
            setNewOpen(false);
            const id = res?.data?.id;
            if (id) router.push(`/dashboard/lists/${id}`);
            else notifications.show({ message: "List created", color: "teal" });
          } catch (err) {
            notifications.show({ message: err instanceof Error ? err.message : "That didn't save", color: "red" });
          }
        }}
      />
    </div>
  );
}

/** One list card: the whole card is a link with a 72 px target. */
function ListCard({ list }: { list: ListRow }) {
  const archived = list.archived;
  return (
    <Link
      href={archived ? `/dashboard/lists/${list.id}?archived=1` : `/dashboard/lists/${list.id}`}
      data-list-card={list.id}
      className="flex min-h-[72px] items-center gap-3 border-b border-line px-3.5 py-3 last:border-b-0 hover:bg-surface-2 md:min-h-0 md:rounded-token-md md:border-0 md:px-4 md:py-4"
    >
      <KindTile kind={list.kind} />
      <span className="min-w-0 flex-1">
        <span className="block text-[16px] font-semibold leading-snug text-ink [overflow-wrap:anywhere]">
          {list.name}
        </span>
        <span className="mt-0.5 block text-[13px] leading-tight text-ink-3">
          {archived ? "Archived" : list.open_count === 0 ? "All done" : updatedLabel(list.updated_at)}
        </span>
      </span>
      <span className="shrink-0 text-right leading-tight">
        <b
          className={[
            "block text-[22px] font-semibold tabular-nums tracking-[-0.01em]",
            list.open_count === 0 ? "font-medium text-ink-3" : "text-ink",
          ].join(" ")}
        >
          {list.open_count}
        </b>
        <span className="text-xs text-ink-3">{countCaption(list.kind)}</span>
      </span>
      <IconChevronRight size={18} stroke={STROKE} aria-hidden className="shrink-0 text-ink-3 md:hidden" />
    </Link>
  );
}

function Chip({ pressed, onClick, children }: { pressed: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      onClick={onClick}
      className={[
        "inline-flex h-9 shrink-0 items-center gap-1.5 rounded-full border px-3.5 text-sm font-medium",
        pressed ? "border-ink bg-ink text-surface" : "border-line bg-surface text-ink-2",
      ].join(" ")}
    >
      {children}
    </button>
  );
}

function EmptyState({ onNew, canManage }: { onNew: () => void; canManage: boolean }) {
  const starters = [
    { kind: "grocery", title: "Grocery list", hint: "Sorted by aisle, with a store mode" },
    { kind: "packing", title: "Packing list", hint: "Start from a template" },
    { kind: "chores", title: "Chores", hint: "Assign to people, with due dates" },
  ] as const;
  return (
    <div className="rounded-token-md border border-line bg-surface">
      <div className="flex items-start gap-3 p-5">
        <span aria-hidden className="grid size-12 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent">
          <IconPlus size={24} stroke={STROKE} />
        </span>
        <div>
          <b className="block text-[17px] font-semibold text-ink">Start your first list</b>
          <p className="mt-0.5 text-sm text-ink-2">
            Lists are shared with everyone in the family. Check things off together in the store, and pack for trips
            from a template.
          </p>
        </div>
      </div>
      {canManage && (
        <div className="grid gap-2 px-5 pb-5">
          {starters.map((starter) => {
            const Glyph = kindIcon(starter.kind);
            return (
              <button
                key={starter.kind}
                type="button"
                onClick={onNew}
                className="flex min-h-16 items-center gap-3 rounded-token-md border border-line bg-surface px-3.5 py-2.5 text-left hover:bg-surface-2"
              >
                <KindTile kind={starter.kind} />
                <span className="min-w-0 flex-1">
                  <b className="block text-[15px] font-semibold text-ink">{starter.title}</b>
                  <span className="text-[13px] text-ink-3">{starter.hint}</span>
                </span>
                <Glyph size={18} stroke={STROKE} aria-hidden className="text-ink-3" />
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

/** New list: a name, a kind, and what to start from (the mockup's sheet). */
function NewListSheet({
  opened,
  templates,
  pastLists,
  saving,
  onClose,
  onCreate,
}: {
  opened: boolean;
  templates: ListRow[];
  pastLists: ListRow[];
  saving: boolean;
  onClose: () => void;
  onCreate: (name: string, kind: string, templateId: string | null) => void;
}) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState<string>("grocery");
  const [source, setSource] = useState<string>("blank");

  const reuseOptions = [...templates, ...pastLists];

  const chosenKind = source === "blank" ? kind : (reuseOptions.find((l) => l.id === source)?.kind ?? kind);

  return (
    <ResponsiveModal opened={opened} onClose={onClose} title="New list">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          const trimmed = name.trim();
          if (!trimmed) return;
          onCreate(trimmed, chosenKind, source === "blank" ? null : source);
        }}
      >
        <div className="flex flex-col gap-4">
          <div>
            <label htmlFor="nl-name" className="text-[13px] font-semibold text-ink-2">
              Name
            </label>
            <input
              id="nl-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. Weekly shop"
              data-autofocus
              className="mt-1.5 h-11 w-full rounded-token-sm border border-line bg-surface px-3 text-[16px] text-ink placeholder:text-ink-3"
            />
          </div>

          <fieldset>
            <legend className="text-[13px] font-semibold text-ink-2">Kind</legend>
            <div className="mt-1.5 flex flex-wrap gap-1.5">
              {LIST_KINDS.map((k) => (
                <button
                  key={k}
                  type="button"
                  aria-pressed={chosenKind === k}
                  onClick={() => {
                    setKind(k);
                    setSource("blank");
                  }}
                  className={[
                    "inline-flex min-h-10 items-center gap-1.5 rounded-full border px-3 text-sm font-medium",
                    chosenKind === k
                      ? "border-accent bg-accent-soft text-ink"
                      : "border-line bg-surface text-ink-2",
                  ].join(" ")}
                >
                  {KIND_LABELS[k]}
                </button>
              ))}
            </div>
          </fieldset>

          {reuseOptions.length > 0 && (
            <div>
              <label htmlFor="nl-source" className="text-[13px] font-semibold text-ink-2">
                Start from
              </label>
              <select
                id="nl-source"
                value={source}
                onChange={(e) => setSource(e.target.value)}
                className="mt-1.5 h-11 w-full rounded-token-sm border border-line bg-surface px-3 text-[16px] text-ink"
              >
                <option value="blank">Blank list — add items as you go</option>
                {templates.length > 0 && (
                  <optgroup label="Templates">
                    {templates.map((t) => (
                      <option key={t.id} value={t.id}>
                        {t.name} ({t.item_count} items)
                      </option>
                    ))}
                  </optgroup>
                )}
                {pastLists.length > 0 && (
                  <optgroup label="Past lists">
                    {pastLists.map((t) => (
                      <option key={t.id} value={t.id}>
                        {t.name} ({t.item_count} items)
                      </option>
                    ))}
                  </optgroup>
                )}
              </select>
              <p className="mt-1.5 text-[12.5px] text-ink-3">
                Its items are copied unchecked, so nothing arrives already done.
              </p>
            </div>
          )}
        </div>

        <ResponsiveModalActions>
          <button type="button" onClick={onClose} className="min-h-11 rounded-token-sm px-4 text-sm font-semibold text-ink-2">
            Cancel
          </button>
          <button
            type="submit"
            disabled={!name.trim() || saving}
            className="min-h-11 rounded-token-sm bg-accent-fill px-4 text-sm font-semibold text-white disabled:opacity-50"
          >
            Create list
          </button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}

function IndexSkeleton() {
  return (
    <div role="status" aria-busy="true">
      <div className="mb-4 flex gap-1.5">
        {[72, 88, 74, 66].map((w, i) => (
          <span key={i} className="h-9 rounded-full bg-surface-2" style={{ width: w }} />
        ))}
      </div>
      <div className="mb-2 h-4 w-24 rounded bg-surface-2" />
      <div className="overflow-hidden rounded-token-md border border-line">
        {[0, 1, 2].map((i) => (
          <div key={i} className="flex min-h-[72px] items-center gap-3 border-b border-line px-3.5 last:border-b-0">
            <span className="size-10 shrink-0 rounded-token-md bg-surface-2" />
            <span className="flex-1">
              <span className="block h-4 w-1/2 rounded bg-surface-2" />
              <span className="mt-2 block h-3 w-1/3 rounded bg-surface-2" />
            </span>
            <span className="h-6 w-8 rounded bg-surface-2" />
          </div>
        ))}
      </div>
    </div>
  );
}
