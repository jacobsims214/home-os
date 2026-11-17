/**
 * Notes: the client half of #4564's notes section.
 *
 * Two things this module exists for, both of them data the API already sends:
 *
 * 1. **possible_duplicates** (#4945's rule, on the API since #4564). A note
 *    whose title is close to an existing note comes back with up to three
 *    candidates. The note is ALWAYS saved — the prompt is never a gate — so
 *    the UI must not treat the response as an error, must not silently drop
 *    the candidates, and must not quietly merge or delete anything. It asks
 *    "this looks like an existing note — update that or keep both?", and the
 *    person decides.
 *
 * 2. **authorship**. `author_name` is why the war game cared: two co-parents'
 *    notes about the same pickup are not interchangeable, so a note row always
 *    says who wrote it, and an old note by a member who has left says so
 *    instead of rendering blank.
 *
 * The response envelope is `{data: note, possible_duplicates: [...]}`. The
 * API keeps `data` as the note object itself, so a plain read of `data` is
 * unchanged; only the extra key is new.
 */

import { apiFetch } from "@/lib/api";
import {
  normalizeWriteResult,
  type Note,
  type NoteWriteResult,
} from "@/lib/note-write";

export { duplicateQuestion, normalizeWriteResult } from "@/lib/note-write";
export type { DuplicateCandidate, Note, NoteWriteResult } from "@/lib/note-write";

/** Notes on one entity, newest first. */
export async function listNotes(entityType: string, entityId: string): Promise<Note[]> {
  const res = await apiFetch<{ data: Note[] }>("/api/v1/notes", {
    params: { entity_type: entityType, entity_id: entityId },
  });
  return Array.isArray(res?.data) ? res.data : [];
}

/** Create a note. Always saves; the result may carry duplicate candidates. */
export async function createNote(input: {
  entityType: string;
  entityId: string;
  title?: string;
  body: string;
}): Promise<NoteWriteResult> {
  const res = await apiFetch<NoteWriteResult>("/api/v1/notes", {
    method: "POST",
    body: {
      entity_type: input.entityType,
      entity_id: input.entityId,
      title: input.title?.trim() || undefined,
      body: input.body,
    },
  });
  return normalizeWriteResult(res);
}

/** Edit a note's title and body. A supplied title is duplicate-checked. */
export async function updateNote(
  noteId: string,
  input: { title?: string | null; body?: string },
): Promise<NoteWriteResult> {
  const body: Record<string, unknown> = {};
  if (input.title !== undefined) body.title = input.title;
  if (input.body !== undefined) body.body = input.body;
  const res = await apiFetch<NoteWriteResult>(`/api/v1/notes/${noteId}`, {
    method: "PUT",
    body,
  });
  return normalizeWriteResult(res);
}

/** Delete a note. Used by "keep both" only when the person removes the copy. */
export async function deleteNote(noteId: string): Promise<void> {
  await apiFetch(`/api/v1/notes/${noteId}`, { method: "DELETE" });
}
