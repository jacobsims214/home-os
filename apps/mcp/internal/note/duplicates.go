package note

// Near-duplicate note titles (#4945).
//
// War game R1 (KB discoveries/war-game-r1-amber.md, G4): two notes titled
// "Ava's school — teacher & pickup rules" and "Ava's school: teacher &
// pickup" disagreed about who does pickup, at what time and at which door.
// Nothing at write time said the second note had a twin, so the collision was
// only visible to whoever compared titles — and the agent reading them
// stitched both into one procedure ("3:15 side door AND 2:50 car line AND
// green tag"), with a nut-allergy/EpiPen fact buried in one of the two.
//
// note_create and note_update therefore compare the title being written with
// the household's existing note titles and, when one is close enough, name the
// candidates in the response (possible_duplicates: id, title, updated_at, a
// short excerpt and the score) so the caller can decide "that's an update to
// note X" (note_update it) or "no, a separate note".
//
// It is a prompt, never a gate. The note is still created or updated, nothing
// is merged, renamed or deleted, and an unrelated title is untouched: a second
// note is sometimes right (two children at two schools, a new procedure that
// takes effect next term). What the write must never do is create the twin
// silently.
//
// The scan follows the role policy of #4881: a note the caller may not see is
// not a candidate, so the prompt cannot reveal the title or the body of a note
// its role is blind to (a housesitter adding a pet note is not told about a
// family note with the same title).
//
// On update only a supplied title is checked. A rename is the only change that
// can introduce a new collision; append_text and content cannot, and repeating
// the warning on every line appended to a note would be exactly the noise that
// teaches a caller to ignore the field.
//
// A supersedes / related_note_id column was considered and deliberately not
// built: it needs a migration, a same-family composite FK (058's rule), read
// semantics for a superseded note (does it disappear from note_find, and for
// which roles?) and delete semantics for the note that supersedes. It also only
// helps a caller that has already noticed the collision, which is the step this
// file fixes. See the ticket for the full reasoning.

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// duplicateThreshold is the Jaccard similarity (shared words over all
	// words, 0..1) at which two titles are called near-duplicates. 0.6 means
	// "at least three of every five words are shared", and it was chosen
	// against the real pair and its neighbours:
	//
	//	"Ava's school — teacher & pickup rules" vs
	//	"Ava's school: teacher & pickup"                     4/5 = 0.80  prompt
	//	"Ava's school pickup" vs the first                   3/5 = 0.60  prompt
	//	"Ava's school — teacher & pickup rules" vs itself    5/5 = 1.00  prompt
	//	"Ava's school pickup" vs "Ava's school bus route"    2/5 = 0.40  quiet
	//	"Ava's school — teacher & pickup rules" vs
	//	"Ava's swimming lessons"                             1/7 = 0.14  quiet
	//	"School pickup rules" vs "School bus route"          1/5 = 0.20  quiet
	//	"Ava's school — teacher & pickup rules" vs
	//	"School holiday calendar 2026"                       1/8 = 0.13  quiet
	//	"Wi-Fi" vs "Wi-Fi password"                          1/2 = 0.50  quiet
	//	"Groceries" vs "Grocery list"                        0/3 = 0.00  quiet
	//
	// Lower (0.5) and every short title sharing one word prompts — "Wi-Fi"
	// against "Wi-Fi password", "school pickup" against "school bus" — which
	// is the noise that makes a caller stop reading the field. Higher (0.7)
	// and the shortened but genuine twin above ("Ava's school pickup") is
	// missed, which is the failure this exists to stop.
	duplicateThreshold = 0.6

	// duplicateMaxCandidates caps how many matches one response names, best
	// first. Past three the caller is better served by note_find.
	duplicateMaxCandidates = 3

	// duplicateExcerptRunes is how much of a candidate's body the prompt
	// quotes. Long enough to carry the facts that disagree (a teacher, a time,
	// a door, an allergy), short enough that three of them do not swamp the
	// response.
	duplicateExcerptRunes = 140

	// duplicateExcerptRead is how much of the body is read to fill that
	// excerpt: whitespace runs are collapsed after the read, so a note written
	// as short lines still fills the quote.
	duplicateExcerptRead = duplicateExcerptRunes * 4

	// duplicateScanLimit bounds the titles one write compares against, newest
	// change first, so a household with thousands of notes cannot make a
	// create slow. Real households sit two orders of magnitude below it.
	duplicateScanLimit = 2000
)

// duplicateCandidate is one existing note whose title resembles the one being
// written: everything a caller needs to say "that's an update to note X", and
// nothing it needs to read the whole note for.
type duplicateCandidate struct {
	ID               string    `json:"id" jsonschema_description:"The existing note's id; pass it as note_id."`
	Title            string    `json:"title" jsonschema_description:"The existing note's title, as stored."`
	UpdatedAt        time.Time `json:"updated_at" jsonschema_description:"When that note last changed; the newer one is usually the one that is current."`
	UpdatedLocalDate string    `json:"updated_local_date" jsonschema_description:"updated_at as YYYY-MM-DD in the family timezone."`
	Excerpt          string    `json:"excerpt" jsonschema_description:"The start of its body, whitespace collapsed."`
	TitleSimilarity  float64   `json:"title_similarity" jsonschema_description:"How close the two titles are, 0..1; 0.6 or more is reported."`
}

// noteWriteResult is what note_create and note_update return: the note, plus
// the existing notes its title may be a rewrite of.
type noteWriteResult struct {
	noteResult
	PossibleDuplicates []duplicateCandidate `json:"possible_duplicates,omitzero" jsonschema_description:"Existing notes with a similar title. Empty when the title is distinct. Nothing was merged: decide whether this is an update to one of them or a separate note."`
}

// stopWords carry no subject in a title, so they are not compared. "and" is
// here because the ticket's two titles write the same conjunction as "&",
// which normalises away: without it the pair would score lower for no reason.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true,
	"of": true, "for": true, "to": true, "in": true, "on": true,
	"at": true, "with": true, "from": true, "our": true, "my": true,
}

// titleTokens normalises a title into the set of words two titles are compared
// on: lowercased, with every punctuation rune a separator except the hyphens
// and underscores that join a word ("pick-up" is "pickup", "Wi-Fi" is "wifi"),
// with possessives reduced ("Ava's" is "ava": the apostrophe separates and the
// one-letter leftover is dropped), stop words removed and repeats counted once.
// A title that is nothing but punctuation, stop words or single letters falls
// back to its raw words, so "The" still matches "The"; an empty title yields
// no tokens and therefore resembles nothing.
func titleTokens(title string) map[string]bool {
	joined := strings.Map(func(r rune) rune {
		switch r {
		case '-', '_', '\u2010', '\u2011': // word joiners: "pick-up" is "pickup"
			return -1
		}
		return r
	}, strings.ToLower(title))
	fields := strings.FieldsFunc(joined, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	all := make(map[string]bool, len(fields))
	tokens := make(map[string]bool, len(fields))
	for _, f := range fields {
		if f == "" {
			continue
		}
		all[f] = true
		if stopWords[f] || isSingleLetter(f) {
			continue
		}
		tokens[f] = true
	}
	if len(tokens) == 0 {
		return all
	}
	return tokens
}

// isSingleLetter reports whether s is one letter: the "s" left over from a
// possessive, or a stray initial. A single digit ("Route 5") is kept.
func isSingleLetter(s string) bool {
	r, size := utf8.DecodeRuneInString(s)
	return size == len(s) && unicode.IsLetter(r)
}

// jaccard is the similarity of two token sets: the share of words they have in
// common out of every word either of them has. 1 is the same words, 0 is
// nothing in common, and an empty set resembles nothing.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	shared := 0
	for t := range a {
		if b[t] {
			shared++
		}
	}
	union := len(a) + len(b) - shared
	if union == 0 {
		return 0
	}
	return float64(shared) / float64(union)
}

// titleSimilarity scores two raw titles on the scale duplicateThreshold is
// measured in, rounded to two decimals so a response reads 0.8 rather than
// 0.8000000000000001.
func titleSimilarity(a, b string) float64 {
	return math.Round(jaccard(titleTokens(a), titleTokens(b))*100) / 100
}

// nearDuplicates is similarNotes for a write path, where a failed check must
// never stop the write: the prompt is a courtesy, and a caller that cannot be
// told about a twin is still better served by its note being saved. The failure
// is logged so it is visible in the server's log, not swallowed silently.
func nearDuplicates(ctx context.Context, q querier, r reader, title string, exclude uuid.UUID) []duplicateCandidate {
	candidates, err := similarNotes(ctx, q, r, title, exclude)
	if err != nil {
		slog.Warn("note: near-duplicate title check failed; the write went ahead without it",
			"household_id", r.householdID, "error", err)
		return nil
	}
	return candidates
}

// similarNotes returns up to duplicateMaxCandidates of the household's notes
// whose titles are near-duplicates of title, best match first. exclude is the
// note being updated (uuid.Nil when a note is being created): a note never
// matches itself. Only notes the caller's role may see are candidates
// (visibleNotesSQL), and a query error is returned, not swallowed: both
// callers run this before their write, so a failure is reported without
// leaving a note behind.
func similarNotes(ctx context.Context, q querier, r reader, title string, exclude uuid.UUID) ([]duplicateCandidate, error) {
	want := titleTokens(title)
	if len(want) == 0 {
		return nil, nil
	}
	var excludeArg *uuid.UUID
	if exclude != uuid.Nil {
		excludeArg = &exclude
	}
	rows, err := q.Query(ctx,
		`SELECT n.id, n.title, n.updated_at,
		        to_char(n.updated_at AT TIME ZONE h.timezone, 'YYYY-MM-DD'),
		        left(n.body, $5::int)
		 FROM notes n JOIN households h ON h.id = n.household_id
		 WHERE n.household_id = $1
		   AND `+visibleNotesSQL("$2::text", "$3::boolean")+`
		   AND n.title IS NOT NULL AND btrim(n.title) <> ''
		   AND ($4::uuid IS NULL OR n.id <> $4::uuid)
		 ORDER BY n.updated_at DESC, n.id
		 LIMIT $6::int`,
		r.householdID, r.role, r.seesSecrets(), excludeArg, duplicateExcerptRead, duplicateScanLimit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []duplicateCandidate
	for rows.Next() {
		var (
			c    duplicateCandidate
			id   uuid.UUID
			body string
		)
		if err := rows.Scan(&id, &c.Title, &c.UpdatedAt, &c.UpdatedLocalDate, &body); err != nil {
			return nil, err
		}
		score := titleSimilarity(title, c.Title)
		if score < duplicateThreshold {
			continue
		}
		c.ID = id.String()
		c.Excerpt = excerpt(body)
		c.TitleSimilarity = score
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Best match first; among equals the most recently changed note, then the
	// id, so the same household always gets the same order.
	sort.Slice(out, func(i, j int) bool {
		if out[i].TitleSimilarity != out[j].TitleSimilarity {
			return out[i].TitleSimilarity > out[j].TitleSimilarity
		}
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > duplicateMaxCandidates {
		out = out[:duplicateMaxCandidates]
	}
	return out, nil
}

// currentTitle reads a note's stored title, so note_update can tell a real
// rename from a rewrite of the same words. pgx.ErrNoRows means the note is not
// in this household (or does not exist), which the update itself reports.
func currentTitle(ctx context.Context, q querier, householdID, noteID uuid.UUID) (*string, error) {
	var title *string
	err := q.QueryRow(ctx,
		`SELECT title FROM notes WHERE id = $1 AND household_id = $2`, noteID, householdID,
	).Scan(&title)
	if err != nil {
		return nil, err
	}
	return title, nil
}

// sameTitleWords reports whether two titles normalise to the same words, so a
// rewrite that changes nothing a reader compares ("groceries!" for "Groceries")
// is not treated as a new title. A NULL stored title is never the same.
func sameTitleWords(stored *string, next string) bool {
	if stored == nil || *stored == "" {
		return false
	}
	a, b := titleTokens(*stored), titleTokens(next)
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	for t := range a {
		if !b[t] {
			return false
		}
	}
	return len(a) == len(b)
}

// excerpt is the part of a body a duplicate prompt quotes: at most
// duplicateExcerptRunes characters with whitespace runs collapsed to single
// spaces, so a note written as short lines still reads as one line and the
// quote reaches the facts rather than the blank space.
func excerpt(body string) string {
	return truncate(strings.Join(strings.Fields(body), " "), duplicateExcerptRunes)
}

// duplicateHeadsUp is the sentence appended to a write's summary when the title
// resembles existing notes. It names them (small models repeat the summary, and
// the summary is what the user reads) and says what may be true, without
// deciding for the caller.
func duplicateHeadsUp(candidates []duplicateCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	named := make([]string, len(candidates))
	for i, c := range candidates {
		named[i] = "\"" + c.Title + "\" (updated " + c.UpdatedLocalDate + ")"
	}
	if len(named) == 1 {
		return "Heads-up: its title is close to note " + named[0] + ", which may say something different."
	}
	return "Heads-up: its title is close to notes " + joinAnd(named) + ", which may say something different."
}

// duplicateNext are the next-step hints a write with candidates returns: how to
// make it an update of the existing note, and what to do when both notes are
// real. Text is a … placeholder, so neither hint is a ready-to-run call that
// invents a value.
func duplicateNext(candidates []duplicateCandidate) []string {
	best := candidates[0].ID
	return []string{
		fmt.Sprintf("If this replaces note %s, put its text there with note_update note_id=%s append_text=… and remove the copy with note_delete.", best, best),
		"If both are current, show the user both and ask which one is right before anyone relies on either.",
	}
}
