"use client";

/**
 * One list (#4566), at /dashboard/lists/[id]. Built to the approved mockup
 * (artifact #88 v1: the Grocery, Packing, Chores and Store-mode states) inside
 * the approved shell (#75 v1).
 *
 * WHAT THIS SCREEN IS FOR. Someone adds "2 lb ground beef" while standing in
 * their kitchen; someone else ticks it off in a supermarket aisle on one bar of
 * signal; a third person adds milk from their own phone while that happens. So:
 *
 *  - THE CHECK-OFF IS OPTIMISTIC (lib/lists/api.ts useCheckItems). The tick is
 *    painted from the tap, not from the response; if the server refuses, the
 *    row goes back exactly as it was and a toast says so.
 *  - THE LIST POLLS ITS OWN FEED (`?since=<as_of>`, 5 s here, 3 s in store
 *    mode), merging by id, so two phones converge without a reload.
 *  - QUICK ADD IS ONE LINE + ENTER, with the parse previewed first.
 *  - STORE MODE is a full-screen view where the whole row is a 64 px target.
 *  - ROLES (KB decisions/2026-11-15-role-policy.md, apps/api/internal/auth/
 *    roles.go ResLists): family manages and edits; a HOUSESITTER may read, add
 *    items and check items off but gets no rename/archive/template/edit/remove;
 *    a VIEWER reads only (checkboxes disabled, no quick add). The UI hides what
 *    the API refuses; the API is still the gate.
 *
 * WHAT IT DELIBERATELY DOES NOT DO: `EntityList` (the shared list component).
 * The index is grouped kind sections plus a template rail, and this page is one
 * list's grouped items — neither is EntityList's generic table. Worth recording
 * because EntityList defaults `propertyFilter` ON and silently empties any list
 * whose entity has no `property_id` (the bug shipped on Pets, #4556); lists are
 * not property-scoped at all, so that trap cannot reach this page by accident.
 */

import { useCallback, useMemo, useState } from "react";
import Link from "next/link";
import { notFound, useParams } from "next/navigation";
import { notifications } from "@mantine/notifications";
import {
  IconArchive,
  IconArrowLeft,
  IconCheck,
  IconDots,
  IconShoppingCart,
} from "@tabler/icons-react";
import { ResponsiveModal, ResponsiveModalActions } from "@/components/ui";
import { useNavRole } from "@/components/shared/use-nav-role";
import {
  useAddItem,
  useCheckBatcher,
  useItemsFeed,
  useList,
  usePeople,
  useRemoveItem,
  useUpdateList,
  POLL_MS,
  STORE_POLL_MS,
} from "@/lib/lists/api";
import { openCountOf } from "@/lib/lists/optimistic";
import {
  amountLabel,
  checkedLabel,
  dueLabel,
  groupItems,
  hasStoreMode,
  kindLabel,
  splitChecked,
} from "@/lib/lists/model";
import type { ListItem } from "@/lib/lists/types";
import { KindTile } from "@/components/lists/kind";
import ItemRow from "@/components/lists/ItemRow";
import QuickAdd from "@/components/lists/QuickAdd";
import StoreMode from "@/components/lists/StoreMode";
import { useHouseholdToday } from "@/lib/today/api";
import ItemEditor from "@/components/lists/ItemEditor";

const STROKE = 1.75;
/** Mirrors the API's ResLists policy: who may manage, edit, add, check. */
const MANAGE_ROLES = new Set(["owner", "family_manager", "family_member"]);
const ADD_ROLES = new Set(["owner", "family_manager", "family_member", "housesitter"]);
const CHECK_ROLES = ADD_ROLES;

export default function ListDetailPage() {
  const params = useParams<{ id: string }>();
  const listId = typeof params?.id === "string" ? params.id : Array.isArray(params?.id) ? params.id[0] : "";
  const role = useNavRole();

  const canManage = role == null ? true : MANAGE_ROLES.has(role);
  const canAdd = role == null ? true : ADD_ROLES.has(role);
  const canCheck = role == null ? true : CHECK_ROLES.has(role);

  const listQuery = useList(listId || null);
  const list = listQuery.data?.data;
  const archived = !!list?.archived;
  const storeKind = hasStoreMode(list?.kind ?? "");
  const [storeOpen, setStoreOpen] = useState(false);

  // The feed polls; an archived list is read-only and nothing on another phone
  // can change it, so it does not poll.
  const pollMs = archived || !listId ? 0 : storeOpen ? STORE_POLL_MS : POLL_MS;
  const feed = useItemsFeed(listId || null, pollMs);
  const people = usePeople();
  const toggle = useCheckBatcher();
  const addItem = useAddItem();
  const removeItem = useRemoveItem();
  const updateList = useUpdateList();

  const [editorItem, setEditorItem] = useState<ListItem | null>(null);
  const [editorOpen, setEditorOpen] = useState(false);
  const [actionsOpen, setActionsOpen] = useState(false);
  const [renaming, setRenaming] = useState(false);

  // The FAMILY's day (#5316): "bought today" is the household's today, not a
  // UTC browser's. todayKey() takes it as an argument (see lib/lists/model).
  const day = useHouseholdToday();
  // The feed is the live truth; the detail query is the first paint. Prefer the
  // feed the moment it has anything, so an optimistic tick is never overwritten
  // by a slower detail response.
  const items = useMemo<ListItem[]>(() => {
    const fed = feed.data?.items;
    if (fed && fed.length > 0) return fed;
    return list?.items ?? [];
  }, [feed.data, list?.items]);

  const nameOf = useMemo(() => {
    const map = new Map<string, string>();
    for (const person of people.data?.data ?? []) map.set(person.id, person.display_name);
    return map;
  }, [people.data]);

  const { open: openItems, checked: checkedItems } = useMemo(() => splitChecked(items), [items]);
  const groups = useMemo(() => groupItems(openItems, list?.kind ?? "", day), [openItems, list?.kind, day]);
  const openCount = openCountOf(items);
  const pct = items.length ? Math.round((checkedItems.length / items.length) * 100) : 0;

  const onToggle = useCallback(
    (item: ListItem) => {
      if (!listId || !canCheck || archived) return;
      toggle(listId, item.id, !item.checked);
    },
    [canCheck, archived, listId, toggle],
  );

  /** Archive with no confirm, and an Undo in the toast (artifact #88 v1). */
  const archive = async (name: string) => {
    try {
      await updateList.mutateAsync({ listId, patch: { archived: true } });
      notifications.show({
        message: `Archived ${name}`,
        color: "teal",
        autoClose: 6000,
      });
    } catch (err) {
      notifications.show({ message: err instanceof Error ? err.message : "That didn't save", color: "red" });
    }
  };

  const onQuickAdd = async (parsed: ReturnType<typeof import("@/lib/lists/model").parseQuickAdd>) => {
    if (!listId || !parsed.text) return false;
    try {
      await addItem.mutateAsync({
        listId,
        text: parsed.text,
        quantity: parsed.quantity,
        unit: parsed.unit,
        section: parsed.section,
      });
      return true;
    } catch (err) {
      notifications.show({
        message: err instanceof Error ? err.message : "That didn't save",
        color: "red",
      });
      return false;
    }
  };

  if (listQuery.isLoading) return <ListSkeleton />;
  if (listQuery.error) {
    return (
      <div role="alert" className="rounded-token-md border border-line border-l-4 border-l-danger bg-surface p-4">
        <b className="block text-[15px] font-semibold text-ink">Couldn&rsquo;t load this list</b>
        <p className="mt-0.5 text-sm text-ink-2">
          {listQuery.error instanceof Error ? listQuery.error.message : "Home OS didn’t answer."} Your list is safe;
          anything checked off will sync when it&rsquo;s back.
        </p>
        <button
          type="button"
          onClick={() => void listQuery.refetch()}
          className="mt-3 inline-flex h-9 items-center rounded-token-sm border border-line px-3 text-sm font-semibold text-ink"
        >
          Try again
        </button>
      </div>
    );
  }
  if (!list) notFound();

  const checkedHeading = storeKind ? "In the cart" : "Checked off";

  return (
    <div className="min-w-0 md:max-w-[760px]">
      {/* STORE MODE COVERS THIS, IT DOES NOT SIT ON TOP OF A LIVE PAGE. While
          it is open the page content is inert and hidden from assistive tech:
          otherwise every item is announced (and found by a query) twice, and
          two tap targets compete for the same tap. The content stays mounted,
          so scrolling back to where the person was costs nothing. StoreMode
          and the editor are OUTSIDE this wrapper — they are what stays live. */}
      {/*
        `inert` is a DOM attribute React 18 has no types for, so it is spread
        through a typed assertion rather than written as JSX (which fails the
        build on `DetailedHTMLProps`). An empty string is the attribute's own
        "present" value; a boolean `true` would just warn.
      */}
      <div {...(storeOpen ? ({ inert: "" } as Record<string, string>) : {})} aria-hidden={storeOpen || undefined}>
      {/* Title bar: back on phones, the list's own header everywhere. */}
      <div className="mb-3 flex items-center gap-1 md:hidden">
        <Link
          href={archived ? "/dashboard/lists?archived=1" : "/dashboard/lists"}
          aria-label="All lists"
          className="-ml-1.5 inline-flex min-h-11 items-center gap-0.5 pr-2 text-sm font-semibold text-accent"
        >
          <IconArrowLeft size={22} stroke={STROKE} aria-hidden />
          All lists
        </Link>
      </div>

      <header className="flex items-start gap-3">
        <KindTile kind={list.kind} />
        <div className="min-w-0 flex-1">
          <h2 className="text-[22px] font-semibold leading-tight tracking-[-0.01em] text-ink [overflow-wrap:anywhere]">
            {list.name}
          </h2>
          <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-ink-3">
            <span>{kindLabel(list.kind)}</span>
            {list.entity_id && (
              <>
                <span aria-hidden>·</span>
                <span className="rounded-full px-2 py-0.5 ring-1 ring-inset ring-line">Linked</span>
              </>
            )}
          </p>
          <p className="mt-1.5 text-[13px] text-ink-3" data-open-count>
            <b className="text-sm font-semibold text-ink">
              {openCount} {list.kind === "packing" ? "to pack" : "left"}
            </b>{" "}
            of {items.length}
          </p>
          <div
            role="progressbar"
            aria-label="Checked off"
            aria-valuenow={pct}
            aria-valuemin={0}
            aria-valuemax={100}
            className="mt-2.5 h-1.5 overflow-hidden rounded-full bg-surface-2"
          >
            <i className="block h-full rounded-full bg-accent-fill" style={{ width: `${pct}%` }} />
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          {storeKind && !archived && openCount > 0 && canCheck && (
            <button
              type="button"
              onClick={() => setStoreOpen(true)}
              className="inline-flex h-10 items-center gap-1.5 rounded-token-sm bg-accent-fill px-3 text-sm font-semibold text-white"
            >
              <IconShoppingCart size={18} stroke={STROKE} aria-hidden />
              Shop
            </button>
          )}
          {canManage && !archived && (
            <button
              type="button"
              onClick={() => setActionsOpen(true)}
              aria-label="List actions"
              aria-haspopup="dialog"
              className="grid size-11 place-items-center rounded-token-md text-ink-2 hover:bg-surface-2"
            >
              <IconDots size={20} stroke={STROKE} />
            </button>
          )}
        </div>
      </header>

      {archived && (
        <div role="status" className="mt-3 flex items-center gap-3 rounded-token-md border border-line bg-surface-2 px-3.5 py-3 text-sm text-ink-2">
          <IconArchive size={20} stroke={STROKE} aria-hidden className="shrink-0" />
          <span className="min-w-0 flex-1">
            <b className="font-semibold text-ink">Archived.</b> Items can&rsquo;t be changed until you unarchive it.
          </span>
          {canManage && (
            <button
              type="button"
              onClick={() => updateList.mutate({ listId, patch: { archived: false } })}
              className="shrink-0 rounded-token-sm border border-line bg-surface px-3 py-2 text-[13px] font-semibold"
            >
              Unarchive
            </button>
          )}
        </div>
      )}

      {role === "housesitter" && !archived && (
        <div className="mt-3 flex items-start gap-2.5 rounded-token-md border border-line bg-surface px-3.5 py-3 text-[13px] text-ink-2">
          <span aria-hidden className="text-accent">i</span>
          <span>
            <b className="font-semibold text-ink">You can add items and check them off.</b> Ask the family if
            something needs renaming or removing.
          </span>
        </div>
      )}

      {canAdd && !archived && (
        <div className="mt-4">
          <QuickAdd
            placeholder={
              list.kind === "grocery"
                ? "Add an item, e.g. 2 lb ground beef"
                : list.kind === "chores"
                  ? "Add a chore, e.g. Clean the garage"
                  : list.kind === "packing"
                    ? "Add something to pack"
                    : "Add an item"
            }
            onAdd={onQuickAdd}
          />
        </div>
      )}

      {openItems.length === 0 && (
        <div className="mt-4 flex items-start gap-3 rounded-token-md border border-line bg-surface p-4">
          <span aria-hidden className="grid size-9 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent">
            <IconCheck size={20} stroke={2.5} />
          </span>
          <div>
            <b className="block text-[15px] font-semibold text-ink">Everything is checked off</b>
            <p className="mt-0.5 text-[13px] text-ink-3">
              New items you add show up here. Checked items stay below until you clear them.
            </p>
          </div>
        </div>
      )}

      {groups.map((group) => (
        <section key={group.key} className="mt-4" data-section={group.key}>
          {group.label && (
            <h3 className="flex items-baseline gap-2 px-0.5 pb-1.5 text-[13px] font-semibold text-ink-2">
              {group.label} <span className="font-medium text-ink-3">{group.items.length}</span>
            </h3>
          )}
          <ul className="overflow-hidden rounded-token-md border border-line bg-surface">
            {group.items.map((item) => (
              <ItemRow
                key={item.id}
                item={item}
                kind={list.kind}
                today={day}
                assigneeName={item.assignee_person_id ? nameOf.get(item.assignee_person_id) : null}
                canCheck={canCheck && !archived}
                canEdit={canManage && !archived}
                onToggle={onToggle}
                onEdit={(target) => {
                  setEditorItem(target);
                  setEditorOpen(true);
                }}
              />
            ))}
          </ul>
        </section>
      ))}

      {checkedItems.length > 0 && (
        <details className="mt-4" open={checkedItems.length < 4}>
          <summary className="flex min-h-11 cursor-pointer list-none items-center gap-2 px-0.5 text-[13px] font-semibold text-ink-2 [&::-webkit-details-marker]:hidden">
            {checkedHeading} · {checkedItems.length}
            {canManage && !archived && (
              <button
                type="button"
                onClick={async (e) => {
                  e.preventDefault();
                  await Promise.all(
                    checkedItems.map((item) => removeItem.mutateAsync({ listId, itemId: item.id }).catch(() => null)),
                  );
                  notifications.show({ message: `Cleared ${checkedItems.length} checked items`, color: "teal" });
                }}
                className="ml-auto text-[13px] font-semibold text-accent"
              >
                Clear checked
              </button>
            )}
          </summary>
          <ul className="overflow-hidden rounded-token-md border border-line bg-surface">
            {checkedItems.map((item) => (
              <ItemRow
                key={item.id}
                item={item}
                kind={list.kind}
                today={day}
                assigneeName={item.assignee_person_id ? nameOf.get(item.assignee_person_id) : null}
                canCheck={canCheck && !archived}
                canEdit={canManage && !archived}
                onToggle={onToggle}
                onEdit={(target) => {
                  setEditorItem(target);
                  setEditorOpen(true);
                }}
              />
            ))}
          </ul>
        </details>
      )}

      </div>

      {storeOpen && storeKind && (
        <StoreMode
          list={list}
          items={items}
          canCheck={canCheck}
          today={day}
          onToggle={onToggle}
          onClose={() => setStoreOpen(false)}
        />
      )}

      <ItemEditor
        opened={editorOpen}
        item={editorItem}
        kind={list.kind}
        listId={listId}
        people={people.data?.data ?? []}
        onClose={() => {
          setEditorOpen(false);
          setEditorItem(null);
        }}
      />

      <ResponsiveModal
        opened={actionsOpen}
        onClose={() => setActionsOpen(false)}
        title={list.name}
      >
        <ul className="-mx-2">
          <li>
            <button
              type="button"
              onClick={() => {
                setActionsOpen(false);
                setRenaming(true);
              }}
              className="flex min-h-[52px] w-full items-center gap-3.5 rounded-token-sm px-5 text-left text-[16px] font-medium text-ink hover:bg-surface-2"
            >
              Rename
            </button>
          </li>
          <li>
            <button
              type="button"
              onClick={() => {
                setActionsOpen(false);
                void archive(list.name);
              }}
              className="flex min-h-[52px] w-full items-center gap-3.5 rounded-token-sm px-5 text-left text-[16px] font-medium text-ink hover:bg-surface-2"
            >
              <IconArchive size={20} stroke={STROKE} aria-hidden />
              <span className="flex-1">
                Archive list
                <small className="block text-[12.5px] font-normal text-ink-3">
                  Hides it from Lists. You can unarchive it any time.
                </small>
              </span>
            </button>
          </li>
        </ul>
      </ResponsiveModal>

      <RenameModal
        opened={renaming}
        name={list.name}
        saving={updateList.isPending}
        onClose={() => setRenaming(false)}
        onSave={async (name) => {
          await updateList.mutateAsync({ listId, patch: { name } });
          notifications.show({ message: "List renamed", color: "teal" });
          setRenaming(false);
        }}
      />

    </div>
  );
}

function RenameModal({
  opened,
  name,
  saving,
  onClose,
  onSave,
}: {
  opened: boolean;
  name: string;
  saving: boolean;
  onClose: () => void;
  onSave: (name: string) => Promise<void>;
}) {
  const [value, setValue] = useState(name);
  // Reopening starts from the list's current name, not the last draft.
  if (!opened && value !== name) setValue(name);
  return (
    <ResponsiveModal opened={opened} onClose={onClose} title="Rename list">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (value.trim()) void onSave(value.trim());
        }}
      >
        <label htmlFor="list-name" className="text-[13px] font-semibold text-ink-2">
          Name
        </label>
        <input
          id="list-name"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          data-autofocus
          className="mt-1.5 h-11 w-full rounded-token-sm border border-line bg-surface px-3 text-[16px] text-ink"
        />
        <ResponsiveModalActions>
          <button type="button" onClick={onClose} className="min-h-11 rounded-token-sm px-4 text-sm font-semibold text-ink-2">
            Cancel
          </button>
          <button
            type="submit"
            disabled={!value.trim() || saving}
            className="min-h-11 rounded-token-sm bg-accent-fill px-4 text-sm font-semibold text-white disabled:opacity-50"
          >
            Save
          </button>
        </ResponsiveModalActions>
      </form>
    </ResponsiveModal>
  );
}

function ListSkeleton() {
  return (
    <div aria-busy="true" className="min-w-0">
      <div className="flex items-start gap-3">
        <span className="size-10 shrink-0 rounded-token-md bg-surface-2" />
        <div className="flex-1">
          <span className="block h-5 w-1/2 rounded bg-surface-2" />
          <span className="mt-2 block h-3.5 w-1/4 rounded bg-surface-2" />
        </div>
      </div>
      <div className="mt-5 h-12 rounded-token-md bg-surface-2" />
      <div className="mt-4 overflow-hidden rounded-token-md border border-line">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="flex items-center gap-3 border-b border-line px-3 py-3 last:border-b-0">
            <span className="size-6 shrink-0 rounded-full bg-surface-2" />
            <span className="block h-4 bg-surface-2" style={{ width: `${[55, 70, 45, 62][i]}%` }} />
          </div>
        ))}
      </div>
    </div>
  );
}
