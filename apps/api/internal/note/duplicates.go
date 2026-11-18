package note

// Near-duplicate note titles on the HTTP API (#4564, following #4945).
//
// #4945 added the check to the MCP's note_create / note_update: a note whose
// title is close to an existing note comes back with `possible_duplicates`,
// so a caller can decide "that's an update to note X" or "no, a separate
// note". The war game's failure (two notes, "Ava's school — teacher & pickup
// rules" and "Ava's school: teacher & pickup", disagreeing about the teacher,
// the time and the door) is not an agent-only failure: a parent typing the
// second note on a phone makes the same collision.
//
// The UI cannot invent this prompt: it can only show what the API returns. So
// this is the same rule, in the same shape, for the web:
//
//   - note_create and note_update return `possible_duplicates` beside the note
//     they already return. The note is ALWAYS written; nothing is merged,
//     renamed or deleted. It is a prompt, never a gate.
//   - Similarity is the MCP's rule exactly (Jaccard over normalised title
//     token sets, threshold 0.6, at most three candidates, best first), so a
//     person and their agent are told about the same twins. The reasoning and
//     the threshold table live in apps/mcp/internal/note/duplicates.go and
//     decisions/2026-11-17-note-near-duplicate-titles.md; the two copies are
//     deliberately separate (the API does not import the MCP) and both are
//     pinned by tests.
//   - The scan follows the role policy (#4689): a note the caller's role may
//     not see is never a candidate, so the prompt cannot leak the title or the
//     body of a note its role is blind to (a housesitter adding a pet note is
//     not told about a family note with the same title).
//   - A failed scan never stops the write: the note is saved either way and
//     the failure is logged.

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"home-os/api/internal/auth"
)

const (
	// duplicateThreshold is the Jaccard similarity at which two titles are
	// called near-duplicates. 0.6 is "at least three of every five words are
	// shared"; see the MCP's table for why not 0.5 (short titles prompt on one
	// shared word) and not 0.7 (the shortened but genuine twin is missed).
	duplicateThreshold = 0.6

	// duplicateMaxCandidates caps how many matches one response names.
	duplicateMaxCandidates = 3

	// duplicateExcerptRunes is how much of a candidate's body the prompt
	// quotes: enough for the facts that disagree, not enough for three of them
	// to swamp the response.
	duplicateExcerptRunes = 140

	// duplicateExcerptRead is how much of the body is read to fill that
	// excerpt, since whitespace runs are collapsed afterwards.
	duplicateExcerptRead = duplicateExcerptRunes * 4

	// duplicateScanLimit bounds the titles one write compares against.
	duplicateScanLimit = 2000
)

// DuplicateCandidate is one existing note whose title resembles the one being
// written: enough to say "that's an update to note X", and nothing more.
type DuplicateCandidate struct {
	ID               string  `json:"id"`
	Title            string  `json:"title"`
	UpdatedAt        string  `json:"updated_at"`
	UpdatedLocalDate string  `json:"updated_local_date"`
	Excerpt          string  `json:"excerpt"`
	TitleSimilarity  float64 `json:"title_similarity"`
}

// stopWords carry no subject in a title, so they are not compared. "and" is
// here because the same conjunction is written as "&", which normalises away.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true,
	"of": true, "for": true, "to": true, "in": true, "on": true,
	"at": true, "with": true, "from": true, "our": true, "my": true,
}

// titleTokens normalises a title into the set of words two titles are compared
// on: lowercased, every punctuation rune a separator except the hyphens and
// underscores that join a word ("pick-up" is "pickup", "Wi-Fi" is "wifi"),
// possessives reduced ("Ava's" is "ava"), stop words removed and repeats
// counted once. A title of nothing but stop words falls back to its raw words;
// an empty title yields no tokens and so resembles nothing.
func titleTokens(title string) map[string]bool {
	joined := strings.Map(func(r rune) rune {
		switch r {
		case '-', '_', '\u2010', '\u2011': // word joiners
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

// jaccard is the share of words two token sets have in common out of every
// word either of them has. An empty set resembles nothing.
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

// TitleSimilarity scores two raw titles on the scale duplicateThreshold is
// measured in, rounded to two decimals so a response reads 0.8 rather than
// 0.8000000000000001.
func TitleSimilarity(a, b string) float64 {
	return math.Round(jaccard(titleTokens(a), titleTokens(b))*100) / 100
}

// excerpt is the part of a body a prompt quotes: at most duplicateExcerptRunes
// characters with whitespace runs collapsed to single spaces.
func excerpt(body string) string {
	flat := strings.Join(strings.Fields(body), " ")
	if utf8.RuneCountInString(flat) <= duplicateExcerptRunes {
		return flat
	}
	return string([]rune(flat)[:duplicateExcerptRunes])
}

// SimilarNotes returns up to duplicateMaxCandidates of the household's notes
// whose titles are near-duplicates of title, best match first. exclude is the
// note being updated (uuid.Nil on create): a note never matches itself. Only
// notes the caller's role may see are candidates.
func (r *Repo) SimilarNotes(ctx context.Context, householdID uuid.UUID, role, title string, exclude uuid.UUID) ([]DuplicateCandidate, error) {
	if len(titleTokens(title)) == 0 {
		return nil, nil
	}
	var excludeArg *uuid.UUID
	if exclude != uuid.Nil {
		excludeArg = &exclude
	}
	rows, err := r.pool.Query(ctx,
		`SELECT n.id, n.entity_type, n.title, n.updated_at,
		        to_char(n.updated_at AT TIME ZONE h.timezone, 'YYYY-MM-DD'),
		        left(n.body, $4::int)
		 FROM notes n JOIN households h ON h.id = n.household_id
		 WHERE n.household_id = $1
		   AND n.title IS NOT NULL AND btrim(n.title) <> ''
		   AND ($2::uuid IS NULL OR n.id <> $2::uuid)
		 ORDER BY n.updated_at DESC, n.id
		 LIMIT $3::int`,
		householdID, excludeArg, duplicateScanLimit, duplicateExcerptRead,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DuplicateCandidate
	for rows.Next() {
		var (
			c          DuplicateCandidate
			id         uuid.UUID
			entityType string
			updatedAt  time.Time
			body       string
		)
		if err := rows.Scan(&id, &entityType, &c.Title, &updatedAt, &c.UpdatedLocalDate, &body); err != nil {
			return nil, err
		}
		// A note the role may not see is not a candidate: the prompt must not
		// leak its title or its body (#4689's rule, as the MCP applies it).
		if !auth.CanSeeEntity(role, entityType) {
			continue
		}
		score := TitleSimilarity(title, c.Title)
		if score < duplicateThreshold {
			continue
		}
		c.ID = id.String()
		c.UpdatedAt = updatedAt.Format(time.RFC3339)
		c.Excerpt = excerpt(body)
		c.TitleSimilarity = score
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Best match first; among equals the most recently changed, then the id,
	// so the same household always gets the same order.
	sort.Slice(out, func(i, j int) bool {
		if out[i].TitleSimilarity != out[j].TitleSimilarity {
			return out[i].TitleSimilarity > out[j].TitleSimilarity
		}
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > duplicateMaxCandidates {
		out = out[:duplicateMaxCandidates]
	}
	return out, nil
}

// NearDuplicates is SimilarNotes for a write path, where a failed check must
// never stop the write. The failure is logged, not swallowed silently.
func (r *Repo) NearDuplicates(ctx context.Context, householdID uuid.UUID, role, title string, exclude uuid.UUID) []DuplicateCandidate {
	candidates, err := r.SimilarNotes(ctx, householdID, role, title, exclude)
	if err != nil {
		slog.Warn("note: near-duplicate title check failed; the write went ahead without it",
			"household_id", householdID, "error", err)
		return nil
	}
	return candidates
}
