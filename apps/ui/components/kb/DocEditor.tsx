"use client";

/**
 * DocEditor (HOME-OS-5503): writing a reference doc, with a PATH PICKER.
 *
 * THE ONE THING THIS FILE EXISTS FOR. `PUT /api/v1/kb/<path>` is an UPSERT: it
 * does not create-only, it does not merge, and `kb_docs` has no revision table
 * (migration 076) — so a save to a path that already holds a doc overwrites
 * that doc's title, body and tags, and the old text is gone. An agent that
 * `kb_write`s a path a person already used can therefore destroy their doc
 * without either of them seeing it happen.
 *
 * So the UI makes the replace unmistakable rather than merely possible:
 *
 * 1. The path is not free text with a Save button next to it. It is a folder +
 *    name pair, both slugged as you type, with the resulting address shown in
 *    mono underneath — the address is what will be written.
 * 2. As soon as the address resolves, it is CHECKED against the library
 *    (`GET /kb?prefix=`) and, when a doc already lives there, the fact is said
 *    in words at the top of the form, in a warning panel, naming the doc's own
 *    title and when it last changed. Not a hint under a field.
 * 3. Save is REFUSED until the person ticks "I know this replaces it". The
 *    checkbox is required on a replace and absent on a create, so the two
 *    cases never look alike. (A tick box rather than a second modal: a modal
 *    that says the same sentence is one more tap and teaches nothing.)
 * 4. The API's own answer says which it did ("… was REPLACED"), and that is
 *    what the toast repeats — the server's word, not ours.
 *
 * The folder picker offers the folders already in use, so a family does not
 * invent `howto/` beside its own `how-to/`, and a new folder is a deliberate
 * choice (type it) rather than the default.
 *
 * Roles: a housesitter never reaches this component (the API refuses writes
 * from them); the page draws no button for them either. That is belt and
 * braces on purpose — see ReferenceList.
 */

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Autocomplete, Button, Checkbox, Text, Textarea, TextInput } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { IconAlertTriangle, IconDeviceFloppy, IconFolder } from "@tabler/icons-react";
import { apiFetch } from "@/lib/api";
import { kbKeys } from "@/lib/query-keys";
import {
  STARTER_FOLDERS,
  folderSuggestions,
  kbIsDirty,
  kbWriteBody,
  parentPath,
  parseTags,
  pathPreview,
  replaceWarning,
  sizeLabel,
  slugFromTitle,
  tagsToInput,
  type KbDoc,
  type KbSummary,
} from "@/lib/kb/model";

const STROKE = 1.75;

export interface DocFormValues {
  folder: string;
  slug: string;
  title: string;
  body: string;
  tags: string;
}

/** The form as it opens: an existing doc's address split back into its parts. */
export function valuesOf(doc: KbDoc | null): DocFormValues {
  if (!doc) return { folder: "", slug: "", title: "", body: "", tags: "" };
  const folder = parentPath(doc.path);
  const slug = doc.path.slice(folder.length === 0 ? 0 : folder.length + 1);
  return { folder, slug, title: doc.title, body: doc.body, tags: tagsToInput(doc.tags) };
}

/**
 * When the address resolves and a doc already lives there, the doc that would
 * be lost. Null means "this save creates something", which is the safe case.
 */
export function clashingDoc(
  candidates: readonly KbSummary[] | null | undefined,
  path: string | null,
): KbSummary | null {
  if (!path || !candidates) return null;
  return candidates.find((d) => d.path === path) ?? null;
}

export default function DocEditor({
  doc,
  path,
  lockedPath = false,
  onSaved,
  onCancel,
}: {
  /** The doc being edited, or null for a new one. */
  doc: KbDoc | null;
  /** A new doc's path when the caller already knows it (a "write this" link). */
  path?: string | null;
  /** True when editing an existing doc: its address cannot move under it. */
  lockedPath?: boolean;
  onSaved?: (saved: KbDoc, created: boolean) => void;
  onCancel?: () => void;
}) {
  const queryClient = useQueryClient();
  const [values, setValues] = useState<DocFormValues>(() => {
    const base = valuesOf(doc);
    if (!path) return base;
    const folder = parentPath(path);
    return { ...base, folder, slug: path.slice(folder.length === 0 ? 0 : folder.length + 1) };
  });
  const [titleTouched, setTitleTouched] = useState(Boolean(doc));
  const [confirmedReplace, setConfirmedReplace] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const resolved = useMemo(() => pathPreview(values.folder, values.slug), [values.folder, values.slug]);
  const targetPath = "path" in resolved ? resolved.path : null;

  // The folder vocabulary: what the family already uses, then the starters it
  // does not. Fetched here rather than passed in, so the picker still works
  // when the editor is opened from a deep link with no list behind it.
  const library = useQuery({
    queryKey: kbKeys.list(),
    queryFn: () => apiFetch<{ data: KbSummary[] }>("/api/v1/kb"),
    staleTime: 30_000,
  });

  const folderOptions = useMemo(() => {
    const used = folderSuggestions(library.data?.data ?? []);
    return Array.from(new Set([...used, ...STARTER_FOLDERS]));
  }, [library.data]);

  // The doc at the address, straight from the server — the list above is a
  // convenience and may be a moment stale, and the question "will this
  // overwrite something?" has to be answered by the source of truth.
  const existing = useQuery({
    queryKey: kbKeys.detail(targetPath ?? ""),
    queryFn: () => apiFetch<{ data: KbDoc }>(`/api/v1/kb/${targetPath ?? ""}`),
    enabled: Boolean(targetPath) && targetPath !== doc?.path,
    retry: false,
  });
  const clash = targetPath && targetPath !== doc?.path && existing.data?.data ? existing.data.data : null;
  // While the read is in flight the answer is unknown, and an unknown answer
  // must not draw "Creating a new doc" — that is the sentence that would let
  // someone overwrite a doc without a warning.
  const checking = Boolean(targetPath) && targetPath !== doc?.path && existing.isFetching;

  // Editing a doc at its OWN address is not a clash with itself: the row the
  // library holds for this doc must not make "Save changes" look like a
  // destructive write. Only a DIFFERENT doc at the address is one.
  const editingSelf = Boolean(doc) && targetPath !== null && targetPath === doc?.path;
  const fromList = editingSelf ? null : clashingDoc(library.data?.data, targetPath);
  const conflictTitle = clash?.title ?? fromList?.title ?? null;
  const willReplace = !editingSelf && Boolean(clash || fromList);

  // A stale tick must never authorize a different address: the confirmation is
  // about ONE path, so changing the path clears it.
  useEffect(() => setConfirmedReplace(false), [targetPath]);

  const dirty = doc ? kbIsDirty(doc, { title: values.title, body: values.body, tags: values.tags }) : true;

  const save = useMutation({
    mutationFn: async () => {
      if (!targetPath) throw new Error("Give the doc an address first.");
      const body = kbWriteBody({ title: values.title, body: values.body, tags: values.tags });
      if (body.title === "") throw new Error("A doc needs a title — what is it?");
      if (willReplace && !confirmedReplace) {
        throw new Error("Tick the box to confirm this replaces the doc already there.");
      }
      const res = await apiFetch<{ data: { doc: KbDoc; created: boolean; note: string } }>(
        `/api/v1/kb/${targetPath}`,
        { method: "PUT", body },
      );
      return res.data;
    },
    onSuccess: (data) => {
      // The API's own sentence: "Doc at <path> was REPLACED" or "Doc created
      // at <path>." Repeating it verbatim means the UI's claim about what
      // happened is the server's, not a second opinion.
      notifications.show({
        message: data.note || (data.created ? "Doc created." : "Doc replaced."),
        color: data.created ? "teal" : "yellow",
      });
      queryClient.invalidateQueries({ queryKey: kbKeys.all });
      queryClient.invalidateQueries({ queryKey: kbKeys.detail(targetPath ?? "") });
      setConfirmedReplace(false);
      onSaved?.(data.doc, data.created);
    },
    onError: (err: Error) => setFormError(err.message || "Couldn't save. Try again."),
  });

  // A title typed on a new doc fills the address, until the person edits the
  // address themselves — then the address is theirs.
  const onTitle = (title: string) => {
    setValues((v) => ({
      ...v,
      title,
      slug: !titleTouched && !doc ? slugFromTitle(title) : v.slug,
    }));
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    if (!targetPath) {
      setFormError("path" in resolved ? "" : resolved.error);
      return;
    }
    if (values.title.trim() === "") {
      setFormError("A doc needs a title — what is it?");
      return;
    }
    if (willReplace && !confirmedReplace) {
      setFormError("Tick the box to confirm this replaces the doc already there.");
      return;
    }
    save.mutate();
  };

  const address = targetPath ?? (values.folder || values.slug ? `${values.folder}/${values.slug}` : "");

  return (
    <form onSubmit={submit} noValidate className="min-w-0" data-kb-editor>
      {/* Editing a doc at its own address is not a CONFLICT, but it is still a
          replace: kb_docs has no revision table, so the version this save
          overwrites cannot be recovered. Said once, calmly, without a tick —
          the person is looking at the text they are about to change. */}
      {editingSelf && (
        <div
          data-kb-replace-note
          className="mb-3 rounded-token-md border border-line bg-surface-2 px-3.5 py-2.5"
        >
          <p className="text-sm text-ink-2">
            Saving replaces this doc&apos;s current text. The reference keeps no older versions, so
            the words you are looking at now will not be recoverable.
          </p>
        </div>
      )}

      {/* THE WARNING, above the fields, saying the doc's own name. Someone who
          scrolled here from a link sees it before they see the form. */}
      {willReplace && !checking && (
        <div
          role="alert"
          data-kb-replace-warning
          className="mb-3 flex items-start gap-2 rounded-token-md border border-warn-line bg-warn-bg p-3 text-warn-ink"
        >
          <IconAlertTriangle size={20} stroke={STROKE} className="mt-0.5 shrink-0" aria-hidden />
          <div className="min-w-0 flex-1">
            <p className="text-sm font-semibold">This address is already taken</p>
            <p className="mt-0.5 text-sm [overflow-wrap:anywhere]">
              {replaceWarning(targetPath ?? "", conflictTitle)}
            </p>
          </div>
        </div>
      )}

      <div className="flex flex-col gap-3">
        {/* The address. Folder is an autocomplete over the folders in use (so
            "how-to" is reused rather than reinvented); the name is a slug. */}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <Autocomplete
            label="Folder"
            description="Where it files. Pick one already in use."
            placeholder="how-to/pets"
            leftSection={<IconFolder size={16} stroke={STROKE} aria-hidden />}
            data={folderOptions}
            value={values.folder}
            onChange={(folder) => setValues((v) => ({ ...v, folder }))}
            disabled={lockedPath}
            classNames={{ input: "max-md:min-h-11 max-md:text-base font-mono" }}
            data-kb-folder
          />
          <TextInput
            label="Name"
            required
            description="Becomes the last part of the address."
            placeholder="router-password"
            value={values.slug}
            onChange={(e) => {
              setTitleTouched(true);
              const slug = e.currentTarget.value;
              setValues((v) => ({ ...v, slug }));
            }}
            disabled={lockedPath}
            classNames={{ input: "max-md:min-h-11 max-md:text-base font-mono" }}
            data-kb-slug
          />
        </div>

        {/* The address, verbatim, as the save will write it. A preview that
            disagreed with the write is the bug this line exists to prevent. */}
        <div className="rounded-token-sm bg-surface-2 px-3 py-2">
          <p className="text-xs font-medium uppercase tracking-wide text-ink-3">Will be saved at</p>
          <p
            data-kb-address
            data-kb-address-valid={targetPath ? "true" : "false"}
            className={`mt-0.5 font-mono text-sm [overflow-wrap:anywhere] ${
              targetPath ? "text-ink" : "text-ink-3"
            }`}
          >
            {address || "—"}
          </p>
          {"error" in resolved && values.slug.trim() !== "" && (
            <p role="alert" className="mt-1 text-xs text-danger">
              {resolved.error}
            </p>
          )}
        </div>

        <TextInput
          label="Title"
          required
          placeholder="Router password and reset"
          value={values.title}
          onChange={(e) => onTitle(e.currentTarget.value)}
          classNames={{ input: "max-md:min-h-11 max-md:text-base" }}
          data-kb-title
        />

        <Textarea
          label="What to know"
          description="Markdown: headings, lists, links, ```code```."
          placeholder={"## The password\n\nIt's on a sticker under the router.\n\n## If it needs a reset\n\nHold the pin for 10 seconds."}
          minRows={10}
          autosize
          maxRows={24}
          value={values.body}
          // The value is read OUTSIDE the updater on purpose. React nulls
          // `event.currentTarget` once the handler returns, and an updater
          // closure may run later (or during a render), where reading it
          // throws "null is not an object" and takes the sheet down with it.
          onChange={(e) => {
            const body = e.currentTarget.value;
            setValues((v) => ({ ...v, body }));
          }}
          classNames={{ input: "max-md:text-base font-mono text-[15px]" }}
          data-kb-body
        />

        <div className="flex flex-wrap items-center justify-between gap-2">
          <TextInput
            label="Tags"
            description="Comma separated."
            placeholder="wifi, router"
            value={values.tags}
            onChange={(e) => {
              const tags = e.currentTarget.value;
              setValues((v) => ({ ...v, tags }));
            }}
            classNames={{ root: "min-w-0 flex-1", input: "max-md:min-h-11 max-md:text-base" }}
            data-kb-tags
          />
          <Text size="xs" className="shrink-0 text-ink-3">
            {sizeLabel(values.body)}
          </Text>
        </div>

        {parseTags(values.tags).length > 0 && (
          <div className="flex flex-wrap gap-1.5" aria-label="Tags that will be saved">
            {parseTags(values.tags).map((tag) => (
              <span
                key={tag}
                className="inline-flex items-center rounded-full bg-surface-2 px-2.5 py-1 text-xs font-medium text-ink-2"
              >
                {tag}
              </span>
            ))}
          </div>
        )}

        {/* The acknowledgement. Only drawn when there IS something to replace,
            so the two saves look different and this never becomes a box people
            tick without reading. */}
        {willReplace && (
          <Checkbox
            checked={confirmedReplace}
            onChange={(e) => setConfirmedReplace(e.currentTarget.checked)}
            label={
              <span className="text-sm">
                I know this replaces <strong className="font-semibold">{targetPath}</strong>
                {conflictTitle ? ` (“${conflictTitle}”)` : ""} and the old text is gone.
              </span>
            }
            classNames={{ label: "max-md:text-base", body: "items-start", inner: "mt-0.5" }}
            data-kb-confirm-replace
          />
        )}

        {checking && (
          <p className="text-xs text-ink-3" role="status">
            Checking whether anything is already at this address…
          </p>
        )}

        {formError && (
          <p role="alert" className="text-sm text-danger [overflow-wrap:anywhere]" data-kb-form-error>
            {formError}
          </p>
        )}

        {/* Stacked on a phone (44 px each), inline from sm up. */}
        <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          {onCancel && (
            <Button variant="default" type="button" onClick={onCancel} className="max-md:min-h-11">
              Cancel
            </Button>
          )}
          <Button
            type="submit"
            loading={save.isPending}
            disabled={!dirty || Boolean(willReplace && !confirmedReplace)}
            leftSection={<IconDeviceFloppy size={16} stroke={STROKE} aria-hidden />}
            className="max-md:min-h-11"
            data-kb-save
          >
            {doc ? "Save changes" : "Save doc"}
          </Button>
        </div>
      </div>
    </form>
  );
}
