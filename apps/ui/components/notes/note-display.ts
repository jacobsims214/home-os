/**
 * Display rules for a note row (#4564). Plain functions, no JSX, so
 * `npm test` (node --test) covers them.
 *
 * Authorship is the reason this file exists. The war game (R1, G4) had two
 * notes about the same school pickup that disagreed about the teacher, the
 * time and the door; a reader could not tell whose word was newer or whose it
 * was. A note row therefore always names an author — "Former member" when the
 * account is gone, never a blank — and always shows a date.
 */

import type { Note } from "@/lib/notes-api";

/** Rows shown before "Show all". A page with 40 notes must stay navigable. */
export const COLLAPSED_ROWS = 8;

/** "Jun 5, 2026". Falls back to the raw value when it is not a date. */
export function formatNoteDate(iso: string | null | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  // UTC: a note's date is a civil date, so UTC cannot move it (#5316).
  return d.toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
    timeZone: "UTC",
  });
}

/**
 * Who wrote it: the name, or "Former member" when the account is gone. Never
 * blank: a note without a visible author is exactly what the war game punished.
 */
export function noteAuthor(note: Pick<Note, "author_name">): string {
  const name = note.author_name?.trim();
  return name || "Former member";
}

/** The initial for the avatar: "Amber Sims" → "A", "?" when unknown. */
export function authorInitial(note: Pick<Note, "author_name">): string {
  const name = note.author_name?.trim();
  return name ? name.charAt(0).toUpperCase() : "?";
}

/** Notes shown for a list: the newest COLLAPSED_ROWS unless showAll. */
export function visibleNotes(notes: Note[], showAll: boolean): Note[] {
  return showAll ? notes : notes.slice(0, COLLAPSED_ROWS);
}

/** True when the list needs a "Show all" control. */
export function isCollapsible(notes: Note[]): boolean {
  return notes.length > COLLAPSED_ROWS;
}

/**
 * What the add form says about the note it is about to write. It is worth a
 * line: a title is what lets the app warn about a near-duplicate (#4945), and
 * a person will not know that from an empty optional field.
 */
export function titleHint(): string {
  return "A title lets the app warn you when a similar note already exists.";
}
