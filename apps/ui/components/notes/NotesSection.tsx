"use client";

/**
 * NotesSection — notes on any entity, mobile-first (#4564).
 *
 * The pattern, not a page: `<NotesSection entityType entityId access />` under
 * a detail page's Resources section, for a property, vehicle, pet, person,
 * maintenance task, bill or anything else the API can attach a note to. The
 * page supplies nothing but its identity; this component owns fetching, the
 * add form, the full-note view, editing and the near-duplicate prompt.
 *
 * Three things it must get right, and why:
 *
 * 1. **Authorship.** A note row always says who wrote it and when. The war game
 *    showed why: two co-parents' notes about the same school pickup gave
 *    different teachers, times and doors, and a reader could not tell whose
 *    word was newer. An unknown author reads "Former member", never blank.
 *
 * 2. **Near-duplicates (#4945, on the API since #4564).** The API compares the
 *    title with the household's existing notes and returns
 *    `possible_duplicates`. The note is ALWAYS saved. So this component does
 *    not retry, block or discard: it shows the candidates and asks "update
 *    that one, or keep both?" — with a one-tap "open that note" so the person
 *    can read them side by side before deciding. Swallowing the response would
 *    put back exactly the silence #4945 removed.
 *
 * 3. **Phone first.** 390 px: rows are full width, every control is at least
 *    44 px tall, the body wraps (no horizontal scroll), and a long list is cut
 *    at DUPLICATE-free 8 rows behind "Show all" so a page with 40 notes stays
 *    navigable.
 *
 * Role policy (#4689): the section renders only when the role may use it —
 * the caller passes `access` from `resourceAccess()` (lib/resource-access.ts),
 * "read" for a viewer or a housesitter on a pet, "none" to render nothing.
 * That is a UI courtesy; the API enforces.
 */

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createNote,
  deleteNote,
  listNotes,
  updateNote,
  type DuplicateCandidate,
  type Note,
} from "@/lib/notes-api";
import type { Access } from "@/lib/resource-access";
import {
  authorInitial,
  formatNoteDate,
  isCollapsible,
  noteAuthor,
  titleHint,
  visibleNotes,
} from "@/components/notes/note-display";

// ─── Constants ────────────────────────────────────────────────

/** Phone: every control is at least this tall (44 px touch target). */
const TOUCH = "max-md:min-h-11";

const notesQueryKey = (entityType: string, entityId: string) =>
  ["notes", entityType, entityId] as const;

// ─── Icons (inline, decorative) ───────────────────────────────

function PlusIcon({ className = "h-4 w-4" }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth={1.75} stroke="currentColor" aria-hidden>
      <path strokeLinecap="round" strokeLinejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
    </svg>
  );
}

function AlertIcon({ className = "h-4 w-4" }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth={1.75} stroke="currentColor" aria-hidden>
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z"
      />
    </svg>
  );
}

// ─── Props ────────────────────────────────────────────────────

interface NotesSectionProps {
  entityType: string;
  entityId: string;
  /**
   * What the signed-in role may do here (lib/resource-access.ts):
   * - "write":    add, edit and delete.
   * - "annotate": add only. A housesitter may add a pet note but the API
   *               refuses their rename and delete (both need ResNotes
   *               ActWrite), so neither button is drawn for them (#4564).
   * - "read":     a read-only list.
   * - "none":     renders nothing at all.
   * Defaults to "write" so a page that has not been wired to the role policy
   * yet does not silently lose its notes; every detail page in this build
   * passes the real value.
   */
  access?: Access;
  /** Hide the section's own heading when a parent already renders one. */
  hideHeading?: boolean;
}

// ─── Component ────────────────────────────────────────────────

export default function NotesSection({
  entityType,
  entityId,
  access = "write",
  hideHeading = false,
}: NotesSectionProps) {
  const queryClient = useQueryClient();
  const qKey = notesQueryKey(entityType, entityId);

  const [showAll, setShowAll] = useState(false);
  // null: the form is closed. "new": the add form. A note id: that note is
  // being edited. One piece of state, so the two forms can never both be open.
  const [editing, setEditing] = useState<string | null>(null);
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [openNoteId, setOpenNoteId] = useState<string | null>(null);
  /** The duplicate prompt for the write that just landed, if any. */
  const [prompt, setPrompt] = useState<{
    saved: Note;
    candidates: DuplicateCandidate[];
  } | null>(null);

  // Adding is allowed for both write levels; changing or removing a note only
  // for "write". `canAdd` gates the form, `canWrite` gates edit and delete.
  const canAdd = access === "write" || access === "annotate";
  const canWrite = access === "write";

  const notesQ = useQuery({
    queryKey: qKey,
    queryFn: () => listNotes(entityType, entityId),
    enabled: access !== "none" && !!entityType && !!entityId,
  });

  const notes = useMemo(() => notesQ.data ?? [], [notesQ.data]);
  const shown = visibleNotes(notes, showAll);

  const saveMutation = useMutation({
    mutationFn: async () => {
      if (editing && editing !== "new") {
        return updateNote(editing, { title: title.trim() || null, body });
      }
      return createNote({ entityType, entityId, title, body });
    },
    onSuccess: (result) => {
      void queryClient.invalidateQueries({ queryKey: qKey });
      resetForm();
      const candidates = result.possible_duplicates ?? [];
      // The note is saved either way. Only a real candidate opens a prompt;
      // nothing is merged, renamed or deleted.
      setPrompt(candidates.length > 0 ? { saved: result.data, candidates } : null);
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteNote(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: qKey });
      setOpenNoteId(null);
    },
  });

  if (access === "none") return null;

  function resetForm() {
    setEditing(null);
    setTitle("");
    setBody("");
  }

  function startNew() {
    setEditing("new");
    setTitle("");
    setBody("");
  }

  function startEdit(note: Note) {
    setEditing(note.id);
    setTitle(note.title ?? "");
    setBody(note.body);
    setOpenNoteId(null);
  }

  const canSubmit = body.trim().length > 0 && !saveMutation.isPending;
  const formOpen = editing !== null;
  const editingNote = editing && editing !== "new" ? notes.find((n) => n.id === editing) : undefined;
  const openNote = openNoteId ? notes.find((n) => n.id === openNoteId) : undefined;

  return (
    <section className="mt-6" aria-labelledby={hideHeading ? undefined : "notes-heading"} data-section="notes">
      {/* The toolbar is always here: a parent that hides the heading (the
          Resources tab bar already says "Notes") must not also lose the add
          affordance. Only the h3 is hidden. */}
      <div className="mb-3 flex items-center justify-between gap-2">
        {hideHeading ? (
          <span className="text-sm text-ink-3">
            {notes.length > 0 ? `${notes.length} ${notes.length === 1 ? "note" : "notes"}` : ""}
          </span>
        ) : (
          <h3 id="notes-heading" className="text-base font-semibold text-ink">
            Notes
          </h3>
        )}
        {canAdd && !formOpen && (
          <button
            type="button"
            onClick={startNew}
            className={`inline-flex items-center gap-1.5 rounded-token-sm border border-line bg-surface px-3 py-1.5 text-sm font-medium text-ink-2 transition-colors hover:bg-surface-2 hover:text-ink ${TOUCH}`}
          >
            <PlusIcon />
            Add note
          </button>
        )}
      </div>

      {/* Near-duplicate prompt (#4945): the note is already saved. This asks a
          question, it does not report a failure. */}
      {prompt && (
        <div
          role="status"
          data-testid="note-duplicate-prompt"
          className="mb-3 rounded-token-md border border-warn-line bg-warn-bg p-3 text-sm text-warn-ink"
        >
          <div className="flex items-start gap-2">
            <AlertIcon className="mt-0.5 h-4 w-4 shrink-0" />
            <div className="min-w-0 flex-1">
              <p className="font-semibold">
                {prompt.candidates.length === 1
                  ? "This looks like an existing note."
                  : "This looks like existing notes."}
              </p>
              <p className="mt-0.5">
                &ldquo;{prompt.saved.title || "Untitled note"}&rdquo; was saved. Update the existing
                one, or keep both?
              </p>
              <ul className="mt-2 space-y-2">
                {prompt.candidates.map((c) => (
                  <li key={c.id} className="rounded-token-sm border border-warn-line bg-surface p-2.5">
                    <p className="text-sm font-medium text-ink" data-candidate-title>
                      {c.title}
                    </p>
                    <p className="mt-0.5 text-xs text-ink-3">
                      updated {c.updated_local_date} · {Math.round(c.title_similarity * 100)}% similar
                    </p>
                    {c.excerpt && (
                      <p className="mt-1 line-clamp-3 text-xs text-ink-2">{c.excerpt}</p>
                    )}
                    <div className="mt-2 flex flex-wrap gap-2">
                      <button
                        type="button"
                        onClick={() => {
                          setPrompt(null);
                          // Show that note next to the new one, so the person
                          // can actually compare them before deciding.
                          setShowAll(true);
                          setOpenNoteId(c.id);
                        }}
                        className={`inline-flex items-center rounded-token-sm border border-line bg-surface px-3 py-1.5 text-xs font-semibold text-ink hover:bg-surface-2 ${TOUCH}`}
                      >
                        Read that note
                      </button>
                      <button
                        type="button"
                        onClick={() => {
                          // The person keeps both: close the prompt and go edit
                          // the saved note if they want to. Nothing is deleted
                          // on their behalf.
                          setPrompt(null);
                          const saved = prompt.saved;
                          setEditing(saved.id);
                          setTitle(saved.title ?? "");
                          setBody(saved.body);
                        }}
                        className={`inline-flex items-center rounded-token-sm border border-line bg-surface px-3 py-1.5 text-xs font-semibold text-ink hover:bg-surface-2 ${TOUCH}`}
                      >
                        Edit the new note
                      </button>
                      <button
                        type="button"
                        onClick={() => setPrompt(null)}
                        className={`inline-flex items-center rounded-token-sm px-3 py-1.5 text-xs font-semibold text-ink-2 hover:bg-surface-2 ${TOUCH}`}
                      >
                        Keep both
                      </button>
                    </div>
                  </li>
                ))}
              </ul>
              <p className="mt-2 text-xs">
                Nothing was merged or deleted — both notes are saved. Two notes with similar titles
                are sometimes right (a new teacher, a new term).
              </p>
            </div>
            <button
              type="button"
              onClick={() => setPrompt(null)}
              aria-label="Dismiss"
              className="inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-token-sm text-warn-ink hover:bg-surface-2"
            >
              <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.75} stroke="currentColor" aria-hidden>
                <path strokeLinecap="round" strokeLinejoin="round" d="M6 18 18 6M6 6l12 12" />
              </svg>
            </button>
          </div>
        </div>
      )}

      {/* Add / edit form */}
      {formOpen && (
        <form
          data-testid="note-form"
          onSubmit={(e) => {
            e.preventDefault();
            if (canSubmit) saveMutation.mutate();
          }}
          className="mb-3 space-y-2 rounded-token-md border border-accent bg-accent-soft p-3"
        >
          <label className="block">
            <span className="mb-1 block text-xs font-medium text-ink-2">Title (optional)</span>
            <input
              type="text"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="e.g. Ava's school — teacher & pickup"
              className={`block w-full rounded-token-sm border border-line bg-surface px-3 py-2 text-base text-ink placeholder:text-ink-3 focus:border-accent focus:outline-none md:text-sm ${TOUCH}`}
            />
            <span className="mt-1 block text-xs text-ink-3">{titleHint()}</span>
          </label>
          <label className="block">
            <span className="mb-1 block text-xs font-medium text-ink-2">Note</span>
            <textarea
              value={body}
              onChange={(e) => setBody(e.target.value)}
              placeholder="What should the family know?"
              rows={5}
              required
              className="block w-full rounded-token-sm border border-line bg-surface px-3 py-2 text-base text-ink placeholder:text-ink-3 focus:border-accent focus:outline-none md:text-sm"
            />
          </label>
          {saveMutation.isError && (
            <p role="alert" className="text-sm text-danger">
              {saveMutation.error instanceof Error ? saveMutation.error.message : "Could not save the note."}
            </p>
          )}
          <div className="flex flex-wrap justify-end gap-2">
            <button
              type="button"
              onClick={resetForm}
              disabled={saveMutation.isPending}
              className={`inline-flex items-center rounded-token-sm border border-line bg-surface px-3 py-1.5 text-sm font-medium text-ink-2 hover:bg-surface-2 ${TOUCH}`}
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={!canSubmit}
              className={`inline-flex items-center rounded-token-sm bg-accent-fill px-4 py-1.5 text-sm font-semibold text-white hover:opacity-90 disabled:opacity-50 ${TOUCH}`}
            >
              {saveMutation.isPending ? "Saving…" : editingNote ? "Save changes" : "Save note"}
            </button>
          </div>
        </form>
      )}

      {/* Loading */}
      {notesQ.isLoading && (
        <div className="space-y-2">
          {[1, 2].map((i) => (
            <div key={i} className="h-20 animate-pulse rounded-token-md bg-surface-2" />
          ))}
        </div>
      )}

      {/* Error */}
      {notesQ.isError && (
        <div className="rounded-token-md border border-danger bg-surface px-4 py-3 text-sm">
          <p className="font-medium text-danger">Could not load notes.</p>
          <p className="mt-0.5 text-ink-2">
            {notesQ.error instanceof Error ? notesQ.error.message : "Unknown error"}
          </p>
          <button
            type="button"
            onClick={() => notesQ.refetch()}
            className={`mt-2 inline-flex items-center rounded-token-sm border border-line bg-surface px-3 py-1.5 text-xs font-semibold text-ink hover:bg-surface-2 ${TOUCH}`}
          >
            Try again
          </button>
        </div>
      )}

      {/* Empty */}
      {!notesQ.isLoading && !notesQ.isError && notes.length === 0 && !formOpen && (
        <p className="text-sm text-ink-3">
          {canAdd
            ? "No notes yet. A note is how the rest of the family finds out what you know."
            : "No notes yet."}
        </p>
      )}

      {/* List */}
      <ul className="space-y-2" data-testid="note-list">
        {shown.map((note) => (
          <li key={note.id}>
            <div className="rounded-token-md border border-line bg-surface px-3 py-3">
              <div className="flex items-center gap-2">
                <span
                  className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-accent-soft text-xs font-semibold text-accent"
                  aria-hidden
                >
                  {authorInitial(note)}
                </span>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium text-ink" data-note-author>
                    {noteAuthor(note)}
                  </p>
                  <p className="text-xs text-ink-3">{formatNoteDate(note.created_at)}</p>
                </div>
                {canWrite && (
                  <button
                    type="button"
                    onClick={() => startEdit(note)}
                    className="inline-flex h-11 w-11 items-center justify-center rounded-token-sm text-ink-3 hover:bg-surface-2 hover:text-ink"
                    aria-label={`Edit note ${note.title || formatNoteDate(note.created_at)}`}
                  >
                    <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.75} stroke="currentColor" aria-hidden>
                      <path strokeLinecap="round" strokeLinejoin="round" d="m16.862 4.487 1.687-1.688a1.875 1.875 0 1 1 2.652 2.652L6.832 19.82a4.5 4.5 0 0 1-1.897 1.13l-2.685.8.8-2.685a4.5 4.5 0 0 1 1.13-1.897L16.863 4.487Z" />
                    </svg>
                  </button>
                )}
              </div>
              {note.title && (
                <p className="mt-1 text-sm font-semibold text-ink" data-note-title>
                  {note.title}
                </p>
              )}
              {/* Full body, never truncated: this is the note. */}
              <p className="mt-1 whitespace-pre-wrap break-words text-sm text-ink-2" data-note-body>
                {note.body}
              </p>
              {/* Open is a read action: every role that can see the note can
                  read it whole. Delete is a write, so only "write" gets it. */}
              <div className="mt-1 flex gap-2">
                <button
                  type="button"
                  onClick={() => setOpenNoteId(note.id)}
                  className={`text-xs font-semibold text-accent hover:underline ${TOUCH} inline-flex items-center`}
                >
                  Open
                </button>
                {canWrite && (
                  <button
                    type="button"
                    onClick={() => {
                      if (deleteMutation.isPending) return;
                      deleteMutation.mutate(note.id);
                    }}
                    disabled={deleteMutation.isPending}
                    className={`text-xs font-semibold text-danger hover:underline disabled:opacity-50 ${TOUCH} inline-flex items-center`}
                  >
                    Delete
                  </button>
                )}
              </div>
            </div>
          </li>
        ))}
      </ul>

      {/* Show all / show fewer */}
      {isCollapsible(notes) && (
        <button
          type="button"
          onClick={() => setShowAll((v) => !v)}
          className={`mt-2 inline-flex w-full items-center justify-center rounded-token-sm border border-line bg-surface px-3 py-2 text-sm font-medium text-ink-2 hover:bg-surface-2 ${TOUCH}`}
        >
          {showAll ? "Show fewer" : `Show all ${notes.length} notes`}
        </button>
      )}

      {/* Full note view (a sheet on a phone, a dialog on a desktop) */}
      {openNote && (
        <NoteView
          note={openNote}
          onClose={() => setOpenNoteId(null)}
          onEdit={canWrite ? () => startEdit(openNote) : undefined}
        />
      )}
    </section>
  );
}

// ─── Full note view ───────────────────────────────────────────

/**
 * One note, whole: title, body, author and date, with no truncation and no
 * line clamp. On a phone it is a bottom sheet (a fixed panel with a grab
 * handle, max 86% of the viewport, scrollable); from md up it is a centred
 * dialog. The point is that a long note is readable on a phone at all.
 */
function NoteView({
  note,
  onClose,
  onEdit,
}: {
  note: Note;
  onClose: () => void;
  onEdit?: () => void;
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center md:items-center" role="dialog" aria-modal="true" aria-labelledby="note-view-title" data-testid="note-view">
      <button type="button" aria-label="Close" onClick={onClose} className="absolute inset-0 bg-scrim" />
      <div className="relative flex max-h-[86vh] w-full flex-col rounded-t-[var(--r-lg)] border border-line bg-raised shadow-token-lg md:max-w-lg md:rounded-token-lg">
        <div className="mx-auto mt-2 h-1 w-9 rounded-full bg-line md:hidden" aria-hidden />
        <div className="flex items-start gap-2 border-b border-line px-4 py-3">
          <div className="min-w-0 flex-1">
            <h4 id="note-view-title" className="break-words text-base font-semibold text-ink">
              {note.title || "Note"}
            </h4>
            <p className="mt-0.5 text-xs text-ink-3">
              {noteAuthor(note)} · {formatNoteDate(note.created_at)}
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close note"
            className="inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-token-sm text-ink-3 hover:bg-surface-2 hover:text-ink"
          >
            <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={1.75} stroke="currentColor" aria-hidden>
              <path strokeLinecap="round" strokeLinejoin="round" d="M6 18 18 6M6 6l12 12" />
            </svg>
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
          <p className="whitespace-pre-wrap break-words text-base leading-relaxed text-ink md:text-sm" data-testid="note-view-body">
            {note.body}
          </p>
        </div>
        <div className="flex justify-end gap-2 border-t border-line px-4 py-2">
          {onEdit && (
            <button
              type="button"
              onClick={onEdit}
              className={`inline-flex items-center rounded-token-sm bg-accent-fill px-4 py-2 text-sm font-semibold text-white hover:opacity-90 ${TOUCH}`}
            >
              Edit note
            </button>
          )}
          <button
            type="button"
            onClick={onClose}
            className={`inline-flex items-center rounded-token-sm border border-line bg-surface px-4 py-2 text-sm font-medium text-ink-2 hover:bg-surface-2 ${TOUCH}`}
          >
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
