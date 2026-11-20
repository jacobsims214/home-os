"use client";

/**
 * Guide editor (#5309): the authoring half of the guides feature.
 *
 * One component serves both jobs, because they are the same job:
 *
 * 1. **Edit** the title, summary, steps (the markdown body) and tags of an
 *    existing guide, with an optional change note that is stored on the
 *    revision the save writes.
 * 2. **History** - the revisions of this guide, newest first, who and when,
 *    with the body of any one of them previewed and restored.
 *
 * THE TRAP THIS FILE IS BUILT AROUND (#4565): `PUT /api/v1/guides/{id}` reads
 * like a patch and is NOT one on the wire. The handler loads the row, applies
 * whichever fields the request carried, and then `saveLocked` writes EVERY
 * column back. A body that omits `summary` is not "leave the summary alone" in
 * any sense a person would expect, and a body that omits a subject column
 * (property_id/asset_id/vehicle_id) writes back whatever the row held when it
 * was locked. So the form never builds a partial body: `guideSaveBody`
 * (lib/guides/model.ts) echoes every field of the guide the form was opened
 * on, including the subject columns the form cannot even edit. There is a unit
 * test on that function, because a missing key here is silent data loss.
 *
 * A RESTORE IS RECOVERABLE, and that is a UI promise, not just an API one:
 * `POST .../revisions/{rev}/restore` puts the OLD body back by writing it as a
 * NEW revision ("Restored revision <id>"). Nothing is deleted, so the version
 * that was replaced is still in the list and still restorable. The history
 * says so in words before anyone taps, and after a restore the list has one
 * MORE row, not one different one.
 *
 * Mobile first, matching the wave's patterns: 44 px targets at phone widths
 * (`max-md:min-h-11`), 16 px inputs so iOS does not zoom on focus, full-width
 * stacked actions on a phone and inline ones from sm up, and nothing that can
 * push a 390 px page sideways (long ids and titles wrap).
 */

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Skeleton, Textarea, TextInput } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import {
  IconAlertTriangle,
  IconCheck,
  IconClock,
  IconHistory,
  IconDeviceFloppy,
  IconRestore,
} from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { guideKeys } from "@/lib/query-keys";
import {
  formatGuideDate,
  guideIsDirty,
  guideSaveBody,
  restoreWarning,
  restoredRevisionId,
  isRestoreNote,
  revisionAuthor,
  revisionSizeLabel,
  revisionWhen,
  tagsToInput,
  type Guide,
  type GuideRevision,
} from "@/lib/guides/model";

const STROKE = 1.75;

interface FormValues {
  title: string;
  summary: string;
  body: string;
  tags: string;
  change_note: string;
}

function valuesOf(guide: Guide): FormValues {
  return {
    title: guide.title,
    summary: guide.summary ?? "",
    body: guide.body,
    tags: tagsToInput(guide.tags),
    change_note: "",
  };
}

/** A tab-like pair; the phone shows them as two equal 44 px buttons. */
function TabButton({
  active,
  onClick,
  icon: Icon,
  children,
  tab,
}: {
  active: boolean;
  onClick: () => void;
  icon: typeof IconHistory;
  children: React.ReactNode;
  tab: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      role="tab"
      aria-selected={active}
      data-guide-tab={tab}
      className={`inline-flex min-h-11 flex-1 items-center justify-center gap-1.5 rounded-token-sm px-3 text-sm font-semibold transition-colors sm:flex-none ${
        active ? "bg-surface text-ink shadow-token-sm" : "text-ink-2 hover:text-ink"
      }`}
    >
      <Icon size={16} stroke={STROKE} aria-hidden />
      {children}
    </button>
  );
}

function ErrorNote({ children }: { children: React.ReactNode }) {
  return (
    <p role="alert" className="flex items-start gap-1.5 text-sm text-danger">
      <IconAlertTriangle size={16} stroke={STROKE} className="mt-0.5 shrink-0" aria-hidden />
      <span className="min-w-0 [overflow-wrap:anywhere]">{children}</span>
    </p>
  );
}

/** One revision row: who, when, why, how big - and what you can do with it. */
function RevisionRow({
  revision,
  isCurrent,
  guideTitle,
  onPreview,
  onRestore,
  restoring,
}: {
  revision: GuideRevision;
  isCurrent: boolean;
  guideTitle: string;
  onPreview: () => void;
  onRestore: () => void;
  restoring: boolean;
}) {
  const when = revisionWhen(revision.created_at);
  const date = formatGuideDate(revision.created_at);
  const restored = restoredRevisionId(revision.change_note);

  return (
    <li
      data-guide-revision={revision.id}
      data-guide-revision-current={isCurrent ? "true" : "false"}
      className="min-w-0 rounded-token-md border border-line bg-surface p-3 shadow-token-sm"
    >
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <span className="text-sm font-semibold text-ink">{revisionAuthor(revision)}</span>
        {when && (
          <span className="text-xs text-ink-3" title={date ?? undefined} data-guide-revision-when>
            {when}
          </span>
        )}
        <span className="text-xs text-ink-3">· {revisionSizeLabel(revision.body_length)}</span>
        {isCurrent && (
          <span className="inline-flex items-center gap-1 rounded-full bg-accent-soft px-2 py-0.5 text-xs font-medium text-accent">
            <IconCheck size={12} stroke={2.25} aria-hidden />
            Current
          </span>
        )}
      </div>

      {revision.change_note && (
        <p className="mt-1 min-w-0 text-sm text-ink-2 [overflow-wrap:anywhere]" data-guide-revision-note>
          {isRestoreNote(revision.change_note) ? "Restored an earlier version" : revision.change_note}
          {restored && (
            <span className="ml-1 text-xs text-ink-3">
              ({formatGuideDate(revision.created_at) ?? "earlier"})
            </span>
          )}
        </p>
      )}

      <div className="mt-2 flex flex-col gap-2 sm:flex-row">
        <Button
          variant="default"
          size="sm"
          onClick={onPreview}
          leftSection={<IconClock size={16} stroke={STROKE} aria-hidden />}
          className="max-md:min-h-11"
          data-guide-revision-preview={revision.id}
        >
          View this version
        </Button>
        {!isCurrent && (
          <Button
            variant="light"
            size="sm"
            onClick={onRestore}
            loading={restoring}
            leftSection={<IconRestore size={16} stroke={STROKE} aria-hidden />}
            className="max-md:min-h-11"
            data-guide-revision-restore={revision.id}
            aria-label={`Restore the version from ${date ?? "earlier"} of ${guideTitle}`}
          >
            Restore
          </Button>
        )}
      </div>
    </li>
  );
}

export default function GuideEditor({
  guide,
  canWrite,
  initialTab = "edit",
  onSaved,
  onClose,
}: {
  guide: Guide;
  canWrite: boolean;
  initialTab?: "edit" | "history";
  onSaved?: (guide: Guide) => void;
  onClose?: () => void;
}) {
  const queryClient = useQueryClient();
  const [tab, setTab] = useState<"edit" | "history">(initialTab);
  const [values, setValues] = useState<FormValues>(() => valuesOf(guide));
  const [formError, setFormError] = useState<string | null>(null);
  const [previewId, setPreviewId] = useState<string | null>(null);
  const [confirmRestore, setConfirmRestore] = useState<GuideRevision | null>(null);

  // A refetched guide re-seeds the form ONLY when the person has not typed
  // anything: re-seeding under them would throw away what they were writing.
  const dirty = guideIsDirty(guide, values);
  useEffect(() => {
    if (!dirty) setValues(valuesOf(guide));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [guide.id, guide.updated_at]);

  const revisions = useQuery({
    queryKey: guideKeys.revisions(guide.id),
    queryFn: () => apiFetch<{ data: GuideRevision[] }>(`/api/v1/guides/${guide.id}/revisions`),
    enabled: tab === "history" && canWrite,
  });

  const preview = useQuery({
    queryKey: guideKeys.revision(guide.id, previewId ?? ""),
    queryFn: () =>
      apiFetch<{ data: GuideRevision }>(`/api/v1/guides/${guide.id}/revisions/${previewId}`),
    enabled: previewId !== null,
  });

  const rows = useMemo(() => revisions.data?.data ?? [], [revisions.data]);

  /**
   * The save. Full body, always - see the file comment. `guideSaveBody` is
   * pure and unit-tested precisely because dropping a key here is silent.
   */
  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      apiFetch<{ data: Guide }>(`/api/v1/guides/${guide.id}`, { method: "PUT", body }),
    onSuccess: async (res) => {
      const saved = res.data;
      setValues(valuesOf(saved));
      setFormError(null);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: guideKeys.detail(guide.id) }),
        queryClient.invalidateQueries({ queryKey: guideKeys.lists() }),
        queryClient.invalidateQueries({ queryKey: guideKeys.revisions(guide.id) }),
      ]);
      notifications.show({ message: "Guide saved", color: "teal" });
      onSaved?.(saved);
    },
    onError: (err: unknown) => {
      setFormError(err instanceof Error ? err.message : "Couldn't save the guide.");
    },
  });

  const restore = useMutation({
    mutationFn: (revisionId: string) =>
      apiFetch<{ data: Guide }>(`/api/v1/guides/${guide.id}/revisions/${revisionId}/restore`, {
        method: "POST",
      }),
    onSuccess: async (res) => {
      setConfirmRestore(null);
      setPreviewId(null);
      // The restored body is the guide's body now, so the form shows it.
      setValues(valuesOf(res.data));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: guideKeys.detail(guide.id) }),
        queryClient.invalidateQueries({ queryKey: guideKeys.lists() }),
        queryClient.invalidateQueries({ queryKey: guideKeys.revisions(guide.id) }),
      ]);
      notifications.show({ message: "Version restored", color: "teal" });
      onSaved?.(res.data);
    },
    onError: (err: unknown) => {
      setFormError(err instanceof Error ? err.message : "Couldn't restore that version.");
    },
  });

  function submit(e: React.FormEvent) {
    e.preventDefault();
    if (values.title.trim() === "") {
      setFormError("A guide needs a name.");
      return;
    }
    setFormError(null);
    // Every editable field AND the subject columns the form cannot change.
    save.mutate(guideSaveBody(guide, values));
  }

  const canSave = !save.isPending && (dirty || values.change_note.trim() !== "");

  return (
    <section
      data-guide-editor={guide.id}
      aria-label="Edit guide"
      className="min-w-0 rounded-token-md border border-line bg-surface-2 p-3 md:p-4"
    >
      <div className="mb-3 flex gap-1 rounded-token-sm bg-surface-2 p-1" role="tablist" aria-label="Guide editor">
        <TabButton active={tab === "edit"} onClick={() => setTab("edit")} icon={IconDeviceFloppy} tab="edit">
          Edit
        </TabButton>
        <TabButton active={tab === "history"} onClick={() => setTab("history")} icon={IconHistory} tab="history">
          History
        </TabButton>
      </div>

      {tab === "edit" && (
        <form onSubmit={submit} noValidate className="min-w-0">
          <div className="flex flex-col gap-3">
            <TextInput
              label="Name"
              required
              value={values.title}
              onChange={(e) => {
                // Read the value NOW: React nulls `currentTarget` before the
                // functional updater runs, so reading it inside setValues throws
                // (#5309: it crashed on the second keystroke).
                const value = e.currentTarget.value;
                setValues((v) => ({ ...v, title: value }));
              }}
              error={values.title.trim() === "" ? "Required" : undefined}
              classNames={{ input: "max-md:min-h-11 max-md:text-base" }}
              data-guide-edit="title"
            />
            <Textarea
              label="Summary"
              description="Optional. One line under the name in the list."
              autosize
              minRows={2}
              value={values.summary}
              onChange={(e) => {
                // Read the value NOW: React nulls `currentTarget` before the
                // functional updater runs, so reading it inside setValues throws
                // (#5309: it crashed on the second keystroke).
                const value = e.currentTarget.value;
                setValues((v) => ({ ...v, summary: value }));
              }}
              classNames={{ input: "max-md:text-base" }}
              data-guide-edit="summary"
            />
            <Textarea
              label="Steps"
              description="Markdown. A “## heading” starts a step; a plain list is one step per item."
              autosize
              minRows={8}
              maxRows={28}
              value={values.body}
              onChange={(e) => {
                // Read the value NOW: React nulls `currentTarget` before the
                // functional updater runs, so reading it inside setValues throws
                // (#5309: it crashed on the second keystroke).
                const value = e.currentTarget.value;
                setValues((v) => ({ ...v, body: value }));
              }}
              classNames={{ input: "max-md:text-base font-mono text-[13px]" }}
              data-guide-edit="steps"
            />
            <TextInput
              label="Tags"
              description="Comma separated. “wifi, router”."
              value={values.tags}
              onChange={(e) => {
                // Read the value NOW: React nulls `currentTarget` before the
                // functional updater runs, so reading it inside setValues throws
                // (#5309: it crashed on the second keystroke).
                const value = e.currentTarget.value;
                setValues((v) => ({ ...v, tags: value }));
              }}
              classNames={{ input: "max-md:min-h-11 max-md:text-base" }}
              data-guide-edit="tags"
            />
            <TextInput
              label="What changed?"
              description="Optional. Saved with this version so history says why."
              maxLength={500}
              value={values.change_note}
              onChange={(e) => {
                // Read the value NOW: React nulls `currentTarget` before the
                // functional updater runs, so reading it inside setValues throws
                // (#5309: it crashed on the second keystroke).
                const value = e.currentTarget.value;
                setValues((v) => ({ ...v, change_note: value }));
              }}
              classNames={{ input: "max-md:min-h-11 max-md:text-base" }}
              data-guide-edit="change-note"
            />
            {formError && <ErrorNote>{formError}</ErrorNote>}
          </div>

          {/* Stacked full-width on a phone (the thumb-reachable pattern this
              wave uses), inline from sm up. */}
          <div className="mt-4 flex flex-col gap-2 sm:flex-row sm:justify-end">
            {onClose && (
              <Button variant="default" type="button" onClick={onClose} className="max-md:min-h-11">
                Cancel
              </Button>
            )}
            <Button
              type="submit"
              loading={save.isPending}
              disabled={!canSave}
              className="max-md:min-h-11"
              data-guide-save
            >
              Save guide
            </Button>
          </div>
        </form>
      )}

      {tab === "history" && (
        <div className="min-w-0">
          {revisions.isLoading && (
            <div role="status" aria-busy="true" aria-label="Loading history" className="space-y-2">
              {[1, 2].map((i) => (
                <div key={i} className="rounded-token-md border border-line bg-surface p-3">
                  <Skeleton height={12} width="45%" radius="sm" />
                  <Skeleton height={12} width="70%" radius="sm" mt={8} />
                </div>
              ))}
            </div>
          )}

          {revisions.isError && (
            <div role="alert" className="rounded-token-md border border-warn-line bg-warn-bg p-3 text-warn-ink">
              <p className="text-sm">Couldn&apos;t load the history. Check your connection and try again.</p>
              <Button
                variant="default"
                size="sm"
                onClick={() => revisions.refetch()}
                loading={revisions.isRefetching}
                className="mt-2 max-md:min-h-11"
              >
                Try again
              </Button>
            </div>
          )}

          {!revisions.isLoading && !revisions.isError && rows.length === 0 && (
            <p className="rounded-token-md border border-dashed border-line px-3 py-6 text-center text-sm text-ink-3">
              No earlier versions yet. Every save from now on is kept here.
            </p>
          )}

          {rows.length > 0 && (
            <ol aria-label="Versions" className="m-0 list-none space-y-2 p-0" data-guide-revisions>
              {rows.map((rev) => (
                <RevisionRow
                  key={rev.id}
                  revision={rev}
                  isCurrent={previewId === null && rev.id === rows[0]?.id}
                  guideTitle={guide.title}
                  onPreview={() => setPreviewId(rev.id)}
                  onRestore={() => {
                    setFormError(null);
                    setConfirmRestore(rev);
                  }}
                  restoring={restore.isPending && confirmRestore?.id === rev.id}
                />
              ))}
            </ol>
          )}

          {/* The confirmation is inline, not a modal: on a phone a modal over the
              list you are restoring from hides the thing you chose. The sentence
              says the old version SURVIVES - that is the promise "a restore is
              itself recoverable" makes, and it belongs on screen before the tap. */}
          {confirmRestore && (
            <div
              data-guide-restore-confirm
              role="group"
              aria-label="Confirm restore"
              className="mt-3 rounded-token-md border border-line bg-surface p-3"
            >
              <p className="text-sm text-ink">{restoreWarning(revisionWhen(confirmRestore.created_at))}</p>
              <div className="mt-3 flex flex-col gap-2 sm:flex-row">
                <Button
                  variant="default"
                  onClick={() => setConfirmRestore(null)}
                  disabled={restore.isPending}
                  className="max-md:min-h-11"
                >
                  Keep the current version
                </Button>
                <Button
                  onClick={() => restore.mutate(confirmRestore.id)}
                  loading={restore.isPending}
                  className="max-md:min-h-11"
                  data-guide-restore-confirm-button
                >
                  Restore this version
                </Button>
              </div>
              {restore.isError && (
                <div className="mt-2">
                  <ErrorNote>
                    {restore.error instanceof Error ? restore.error.message : "Couldn't restore that version."}
                  </ErrorNote>
                </div>
              )}
            </div>
          )}

          {/* A version's body, fetched only when someone asks for it: the list
              deliberately carries body_length instead of every version. */}
          {previewId && (
            <div className="mt-3 min-w-0 rounded-token-md border border-line bg-surface p-3" data-guide-revision-body>
              <div className="mb-2 flex items-start justify-between gap-2">
                <h4 className="min-w-0 text-sm font-semibold text-ink">
                  This version
                  {preview.data?.data && (
                    <span className="ml-1 font-normal text-ink-3">
                      ({revisionAuthor(preview.data.data)},{" "}
                      {revisionWhen(preview.data.data.created_at) ?? "unknown date"})
                    </span>
                  )}
                </h4>
                <Button variant="subtle" size="compact-sm" onClick={() => setPreviewId(null)}>
                  Close
                </Button>
              </div>
              {preview.isLoading && <Skeleton height={60} radius="sm" />}
              {preview.isError && <ErrorNote>Couldn&apos;t load that version.</ErrorNote>}
              {preview.data?.data && (
                <pre className="m-0 max-h-80 overflow-auto whitespace-pre-wrap break-words rounded-token-sm bg-surface-2 p-3 font-mono text-[13px] text-ink [overflow-wrap:anywhere]">
                  {preview.data.data.body ?? ""}
                </pre>
              )}
            </div>
          )}
        </div>
      )}
    </section>
  );
}
