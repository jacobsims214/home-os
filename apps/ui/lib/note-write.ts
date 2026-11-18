/**
 * The shape of a note write's response, and the prompt it may carry (#4564).
 *
 * Kept apart from lib/notes-api.ts (which imports the fetch client) so the
 * wording and the normalisation are testable with `npm test` and nothing else.
 *
 * #4945's rule, on the API since #4564: a note whose title is close to an
 * existing note comes back with up to three `possible_duplicates`. The note is
 * ALWAYS saved — the prompt is never a gate — so the UI must not treat the
 * response as an error, must not drop the candidates, and must not quietly
 * merge or delete anything. It asks "update that one, or keep both?" and the
 * person decides.
 */

export interface Note {
  id: string;
  household_id?: string;
  entity_type: string;
  entity_id: string;
  title: string | null;
  body: string;
  author_id?: string | null;
  author_name?: string | null;
  created_at: string;
  updated_at?: string;
}

/** One existing note whose title resembles the one being written (#4945). */
export interface DuplicateCandidate {
  id: string;
  title: string;
  updated_at: string;
  updated_local_date: string;
  excerpt: string;
  title_similarity: number;
}

/** What a note write returns: the saved note plus any near-duplicate prompt. */
export interface NoteWriteResult {
  data: Note;
  possible_duplicates?: DuplicateCandidate[];
}

/**
 * A write result from an API that predates the prompt (or one whose scan found
 * nothing) has no `possible_duplicates` key. Normalise it to an array so
 * callers never have to test for both undefined and empty.
 */
export function normalizeWriteResult(res: NoteWriteResult): NoteWriteResult {
  return {
    data: res.data,
    possible_duplicates: Array.isArray(res.possible_duplicates) ? res.possible_duplicates : [],
  };
}

/** The prompt's question, in one line, naming the candidate it is about. */
export function duplicateQuestion(candidate: DuplicateCandidate): string {
  return `This looks like an existing note — "${candidate.title}" (updated ${candidate.updated_local_date}). Update that one, or keep both?`;
}
