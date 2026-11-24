"use client";

/**
 * One reference doc (HOME-OS-5503): `/dashboard/guides/reference/<path>`.
 *
 * The address IS the doc: `GET /api/v1/kb/<path>` with the slashes in the URL,
 * which is how the API registers it (chi wildcard). So a doc is shareable,
 * bookmarkable and linkable from a note, and the URL says where it files —
 * "the sitter docs are under /sitters/" is readable in the address bar.
 *
 * The phone view, top to bottom:
 *
 * 1. A 44 px back button in a bar that sticks under the app header while
 *    scrolling, so a person three screens down in "what the sitter needs" gets
 *    back in one tap. The doc's title is drawn ONCE (in the body, under the
 *    back bar) rather than in the bar as well: two identical headings is a
 *    bug to anyone navigating by heading.
 * 2. The title, the PATH in mono, and one line of provenance (who last wrote
 *    it, when) — the three things that tell a reader whether this doc is the
 *    one they meant and whether it is current.
 * 3. The markdown (components/kb/DocBody.tsx), which is a continuous page at
 *    a 68ch measure with its own code blocks scrolling inside themselves.
 * 4. Edit, for the family roles only, which opens the form in place with the
 *    address locked: a doc's path is its identity, so "move it" is a new doc
 *    plus a delete, not a rename. The editor states the replace rule.
 *
 * Roles (#4689, ResKB): every household role READS the KB — the housesitter
 * needing the router doc is the whole point — so this page is drawn for all of
 * them, with no write control at all for a sitter, viewer or vendor. The API
 * refuses their writes regardless; the UI simply does not offer one.
 */

import { useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Skeleton } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  IconAlertTriangle,
  IconArrowLeft,
  IconBookmark,
  IconPencil,
  IconTag,
  IconTrash,
} from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { kbKeys } from "@/lib/query-keys";
import { DocBody } from "@/components/kb/DocBody";
import DocEditor from "@/components/kb/DocEditor";
import { useNavRole } from "@/components/shared/use-nav-role";
import ConfirmDialog from "@/components/ui/ConfirmDialog";
import {
  authorLabel,
  canWriteKb,
  formatKbDate,
  humanizeSegment,
  pathFromUrlSegments,
  topSegment,
  type KbDoc,
} from "@/lib/kb/model";

const STROKE = 1.75;

const iconButton =
  "inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-token-sm text-ink-2 " +
  "transition-colors hover:bg-surface-2 hover:text-ink";

function BackBar({ actions }: { actions?: React.ReactNode }) {
  return (
    <div className="sticky top-[var(--app-shell-header-offset,0px)] z-10 -mx-4 -mt-4 mb-4 flex min-h-14 items-center gap-1 border-b border-line bg-bg px-2 md:static md:mx-0 md:mt-0 md:mb-4 md:border-0 md:bg-transparent md:px-0">
      <Link
        href="/dashboard/guides/reference"
        aria-label="Back to the reference"
        className={`${iconButton} md:hidden`}
      >
        <IconArrowLeft size={22} stroke={STROKE} aria-hidden />
      </Link>
      <div className="min-w-0 flex-1">
        <Link
          href="/dashboard/guides/reference"
          className="hidden items-center gap-1 text-sm text-ink-3 hover:text-accent md:inline-flex"
        >
          <IconArrowLeft size={16} stroke={STROKE} aria-hidden />
          Back to the reference
        </Link>
      </div>
      {actions && <div className="flex shrink-0 items-center gap-1">{actions}</div>}
    </div>
  );
}

export default function ReferenceDocPage() {
  const params = useParams<{ path?: string | string[] }>();
  const router = useRouter();
  const queryClient = useQueryClient();
  const role = useNavRole();
  const canWrite = canWriteKb(role);
  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const raw = params?.path;
  const segments = Array.isArray(raw) ? raw : raw ? [raw] : [];
  const path = pathFromUrlSegments(segments);

  const { data, isLoading, isError, refetch, isRefetching } = useQuery({
    queryKey: kbKeys.detail(path ?? ""),
    queryFn: () => apiFetch<{ data: KbDoc }>(`/api/v1/kb/${path ?? ""}`),
    enabled: Boolean(path),
    retry: false,
  });

  const remove = useMutation({
    mutationFn: () => apiFetch(`/api/v1/kb/${path ?? ""}`, { method: "DELETE" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: kbKeys.all });
      notifications.show({ message: `Deleted ${path}.`, color: "teal" });
      router.push("/dashboard/guides/reference");
    },
    onError: (err: Error) => {
      notifications.show({ message: err.message || "Couldn't delete the doc.", color: "red" });
      setConfirmDelete(false);
    },
  });

  if (!path) {
    return (
      <div className="mx-auto w-full max-w-3xl">
        <BackBar />
        <div role="alert" className="rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink">
          <p className="text-sm">
            That address is not a doc path. A doc lives at something like
            <code className="mx-1 font-mono">how-to/pets/vet</code>.
          </p>
        </div>
      </div>
    );
  }

  if (isLoading) {
    return (
      <div className="mx-auto w-full max-w-3xl" role="status" aria-busy="true" aria-label="Loading the doc">
        <BackBar />
        <Skeleton height={22} width="65%" radius="sm" />
        <Skeleton height={12} width="40%" radius="sm" className="mt-2" />
        <div className="mt-5 space-y-2">
          {[1, 2, 3, 4].map((i) => (
            <Skeleton key={i} height={12} width={i % 2 ? "92%" : "78%"} radius="sm" />
          ))}
        </div>
      </div>
    );
  }

  if (isError || !data?.data) {
    return (
      <div className="mx-auto w-full max-w-3xl">
        <BackBar />
        <div
          role="alert"
          data-kb-doc-error
          className="flex flex-col items-start gap-3 rounded-token-md border border-warn-line bg-warn-bg p-4 text-warn-ink sm:flex-row sm:items-center"
        >
          <IconAlertTriangle size={20} stroke={STROKE} className="shrink-0" aria-hidden />
          <p className="flex-1 text-sm [overflow-wrap:anywhere]">
            There is no doc at <code className="font-mono">{path}</code>. It may have been deleted, or
            the address may be mistyped.
          </p>
          <Button
            variant="default"
            onClick={() => refetch()}
            loading={isRefetching}
            className="max-md:min-h-11"
          >
            Try again
          </Button>
        </div>
      </div>
    );
  }

  const doc = data.data;
  const when = formatKbDate(doc.updated_at);
  const who = authorLabel(doc);
  const tags = doc.tags ?? [];
  const top = topSegment(doc.path);

  return (
    <div className="mx-auto w-full max-w-3xl min-w-0" data-kb-doc={doc.path}>
      <BackBar
        actions={
          canWrite && !editing ? (
            <>
              <button
                type="button"
                onClick={() => setEditing(true)}
                aria-label="Edit this doc"
                className={iconButton}
                data-kb-edit
              >
                <IconPencil size={20} stroke={STROKE} aria-hidden />
              </button>
              <button
                type="button"
                onClick={() => setConfirmDelete(true)}
                aria-label="Delete this doc"
                className={iconButton}
                data-kb-delete
              >
                <IconTrash size={20} stroke={STROKE} aria-hidden />
              </button>
            </>
          ) : undefined
        }
      />

      {canWrite && editing && (
        <div className="mb-4">
          <DocEditor
            doc={doc}
            lockedPath
            onCancel={() => setEditing(false)}
            onSaved={(saved) => {
              queryClient.setQueryData(kbKeys.detail(saved.path), { data: saved });
              setEditing(false);
            }}
          />
        </div>
      )}

      <header className="mb-4 min-w-0">
        <div className="mb-3 flex items-start gap-3.5">
          <span
            aria-hidden
            className="mt-0.5 grid size-11 shrink-0 place-items-center rounded-token-md bg-accent-soft text-accent"
          >
            <IconBookmark size={22} stroke={STROKE} />
          </span>
          <div className="min-w-0 flex-1">
            <h1 className="text-xl font-semibold leading-tight text-ink [overflow-wrap:anywhere] md:text-2xl">
              {doc.title}
            </h1>
            {/* The address. Mono and full, because it is how this doc is
                addressed by a person, an agent and a link. */}
            <p
              data-kb-doc-path
              className="mt-1 font-mono text-[12px] leading-snug text-ink-3 [overflow-wrap:anywhere]"
            >
              {doc.path}
            </p>
            {/* Said once, in words, because "how-to/network/router-password" is
                an address and not everyone reads slashes as a filing system. */}
            {top && (
              <p className="mt-0.5 text-xs text-ink-3">Filed under {humanizeSegment(top)}</p>
            )}
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          <span className="inline-flex items-center rounded-full bg-surface-2 px-2.5 py-1 text-xs font-medium text-ink-2">
            Reference
          </span>
          {tags.map((tag) => (
            <span
              key={tag}
              className="inline-flex items-center gap-1 rounded-full bg-surface-2 px-2.5 py-1 text-xs font-medium text-ink-3"
            >
              <IconTag size={12} stroke={STROKE} aria-hidden />
              {tag}
            </span>
          ))}
        </div>
        {(who || when) && (
          <p className="mt-2 text-xs text-ink-3" data-kb-doc-provenance>
            {who ? `Last written by ${who}` : "Written by an unnamed account"}
            {when ? ` · ${when}` : ""}
          </p>
        )}
      </header>

      <DocBody body={doc.body} />

      <ConfirmDialog
        open={confirmDelete}
        onClose={() => setConfirmDelete(false)}
        onConfirm={() => remove.mutate()}
        loading={remove.isPending}
        title="Delete this doc?"
        message={`${doc.title} (${doc.path}) will be removed from the reference. The address becomes free for someone else to use.`}
      />
    </div>
  );
}
