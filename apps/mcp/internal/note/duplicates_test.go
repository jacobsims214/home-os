package note

// #4945: near-duplicate note titles are named at create and update, so two
// notes that disagree about the same thing cannot coexist silently. War game
// R1 (KB discoveries/war-game-r1-amber.md, G4): "Ava's school — teacher &
// pickup rules" (Mrs. Patel, car line 2:50, green tag) and "Ava's school:
// teacher & pickup" (Ms. Delgado, 3:15 side door, nut allergy/EpiPen) both
// existed, nothing said so, and the agent reading them stitched one procedure
// out of both.
//
// The rule under test: a title whose words overlap an existing note's title by
// duplicateThreshold (0.6) comes back in possible_duplicates with everything a
// caller needs to choose "update that note" or "separate note"; the write
// itself always happens.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"home-os/mcp/internal/testutil"
)

// The two titles from the ticket, and the procedure each one carries. These
// are the strings every test below is built on.
const (
	avaTitleOld = "Ava's school — teacher & pickup rules"
	avaBodyOld  = "Teacher: Mrs. Patel. Pickup: car line at 2:50, green tag on the mirror, " +
		"collect from the front office if you are late."
	avaTitleNew = "Ava's school: teacher & pickup"
	avaBodyNew  = "Teacher: Ms. Delgado. Pickup 3:15 at the side door. Nut allergy — EpiPen in the office."
)

// wireCandidate is one possible_duplicates item as a client decodes it.
type wireCandidate struct {
	ID               uuid.UUID `json:"id"`
	Title            string    `json:"title"`
	UpdatedAt        time.Time `json:"updated_at"`
	UpdatedLocalDate string    `json:"updated_local_date"`
	Excerpt          string    `json:"excerpt"`
	TitleSimilarity  float64   `json:"title_similarity"`
}

// wireWrite is note_create/note_update's result: the note, plus the notes its
// title resembles.
type wireWrite struct {
	wireNote
	PossibleDuplicates []wireCandidate `json:"possible_duplicates"`
}

// TestTitleSimilarity pins the scale duplicateThreshold is measured on, over
// the ticket's pair and the pairs that must stay quiet. It needs no database:
// a threshold that drifts here changes every prompt below it.
func TestTitleSimilarity(t *testing.T) {
	tests := []struct {
		name  string
		a, b  string
		want  float64
		match bool
	}{
		// The ticket's pair: the failure this ticket exists to stop.
		{"the ticket's two pickup notes", avaTitleOld, avaTitleNew, 0.80, true},
		// The same note twice, and a shape-only rewrite of it.
		{"identical titles", avaTitleOld, avaTitleOld, 1.00, true},
		{"punctuation and case only", "Ava's school — teacher & pickup rules", "ava's school teacher and pickup rules", 1.00, true},
		{"hyphen joins a word", "Wi-Fi password", "wifi password", 1.00, true},
		{"possessive is reduced", "Ava's school pickup", "Ava school pickup", 1.00, true},
		// A shortened twin: fewer words, same subject. 0.60 is the floor, and
		// this is the pair that sets it — at 0.7 it would be missed.
		{"shortened twin", "Ava's school pickup", avaTitleOld, 0.60, true},
		{"one word added to a two-word title", "Trash day", "Trash pickup day", 0.67, true},
		{"one word swapped in a long title", "Ava school teacher pickup", "Ava school teacher dropoff", 0.60, true},
		// Unrelated school notes: the negative the ticket asks for.
		{"two different school notes", "School pickup rules", "School bus route", 0.20, false},
		{"the same child, different subject", "Ava's school pickup", "Ava's school bus route", 0.40, false},
		{"the same child, no shared subject", avaTitleOld, "Ava's swimming lessons", 0.14, false},
		{"the ticket's note against a calendar note", avaTitleOld, "School holiday calendar 2026", 0.13, false},
		// Two-word titles sharing one word stay quiet: the noise that would
		// teach a caller to ignore the field.
		{"short titles sharing one word", "Wi-Fi", "Wi-Fi password", 0.50, false},
		{"different word forms do not stem", "Groceries", "Grocery list", 0.00, false},
		{"nothing in common", "Wi-Fi password", "Garage door code", 0.00, false},
		{"one-word titles", "Pediatrician", "Dentist", 0.00, false},
		// Degenerate input never matches by accident.
		{"empty against a real title", "", "anything", 0.00, false},
		{"punctuation only", "!!!", "???", 0.00, false},
		{"a title of stop words matches itself", "the", "the", 1.00, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := titleSimilarity(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("titleSimilarity(%q, %q) = %.2f, want %.2f", tc.a, tc.b, got, tc.want)
			}
			if matched := got >= duplicateThreshold; matched != tc.match {
				t.Errorf("%q vs %q: %.2f against threshold %.2f gives match=%v, want %v",
					tc.a, tc.b, got, duplicateThreshold, matched, tc.match)
			}
			// Similarity is symmetric: the order of two titles cannot matter.
			if back := titleSimilarity(tc.b, tc.a); back != got {
				t.Errorf("not symmetric: %.2f vs %.2f", got, back)
			}
		})
	}
}

// TestTitleTokens pins the normaliser the scores above depend on.
func TestTitleTokens(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  []string
	}{
		{"lowercase, punctuation as separators", "Ava's school: teacher & pickup", []string{"ava", "school", "teacher", "pickup"}},
		{"em dash and ampersand", "Ava's school — teacher & pickup rules", []string{"ava", "school", "teacher", "pickup", "rules"}},
		{"hyphen joins", "Wi-Fi password", []string{"wifi", "password"}},
		{"underscore joins", "pick_up rules", []string{"pickup", "rules"}},
		{"stop words dropped", "The rules for the school", []string{"rules", "school"}},
		{"repeats counted once", "school school school", []string{"school"}},
		{"digits kept", "Route 5 pickup", []string{"route", "5", "pickup"}},
		{"stop words only fall back to the raw words", "the and a", []string{"the", "and", "a"}},
		{"punctuation only is empty", "— !!! &", nil},
		{"empty is empty", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := titleTokens(tc.title)
			if len(got) != len(tc.want) {
				t.Fatalf("titleTokens(%q) = %v, want %v", tc.title, sortedKeys(got), tc.want)
			}
			for _, w := range tc.want {
				if !got[w] {
					t.Errorf("titleTokens(%q) = %v, missing %q", tc.title, sortedKeys(got), w)
				}
			}
		})
	}
}

// sortedKeys lists a token set for a readable failure message.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSameTitleWords covers the update path's "nothing new to collide with"
// rule: a rename that only changes punctuation or case is not a rename.
func TestSameTitleWords(t *testing.T) {
	str := func(s string) *string { return &s }
	tests := []struct {
		name   string
		stored *string
		next   string
		want   bool
	}{
		{"case and punctuation", str("Groceries"), "groceries!", true},
		{"possessive and dash", str("Ava's school — teacher & pickup rules"), "ava's school teacher and pickup rules", true},
		{"a real rename", str("Groceries"), "Weekly groceries", false},
		{"a shortened title is a rename", str(avaTitleOld), "Ava's school pickup", false},
		{"an untitled note is never the same", nil, "Anything", false},
		{"an empty stored title is never the same", str(""), "Anything", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameTitleWords(tc.stored, tc.next); got != tc.want {
				t.Errorf("sameTitleWords(%v, %q) = %v, want %v", tc.stored, tc.next, got, tc.want)
			}
		})
	}
}

// TestExcerpt pins what a prompt quotes of the candidate's body.
func TestExcerpt(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"one line", avaBodyNew, avaBodyNew},
		{"whitespace collapsed", "Teacher: Ms. Patel.\n\nPickup:\t3:15  side door.", "Teacher: Ms. Patel. Pickup: 3:15 side door."},
		{"cut at 140 runes", strings.Repeat("x", 200), strings.Repeat("x", 140) + "..."},
		{"cut counts runes, not bytes", strings.Repeat("é", 200), strings.Repeat("é", 140) + "..."},
		{"empty body", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := excerpt(tc.body); got != tc.want {
				t.Errorf("excerpt(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// TestNoteCreateNearDuplicate is the ticket's case end to end, on a real fully
// migrated Postgres: the second of the two contradictory notes is created, the
// response names the first one, and both rows exist afterwards (a prompt, not a
// gate). Unrelated school notes stay quiet.
func TestNoteCreateNearDuplicate(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)

	name, tool, h := NewNoteCreateTool(pool)
	create := newCaller[wireWrite](t, name, tool, h)

	// The first note, as it was written before the ticket.
	first := create.mustOK(t, ctx, map[string]any{"title": avaTitleOld, "content": avaBodyOld}).Data
	if len(first.PossibleDuplicates) != 0 {
		t.Fatalf("the first note of its title should prompt nothing, got %+v", first.PossibleDuplicates)
	}
	// Backdate it so "the newer one" is unambiguous in the excerpt/updated_at.
	if _, err := pool.Exec(context.Background(),
		`UPDATE notes SET updated_at = NOW() - interval '2 days' WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}

	t.Run("the ticket's second note names the first", func(t *testing.T) {
		env := create.mustOK(t, ctx, map[string]any{"title": avaTitleNew, "content": avaBodyNew})
		second := env.Data
		if second.ID == first.ID {
			t.Fatal("the second note was not created; the prompt must not be a gate")
		}

		// It names the existing note with everything needed to decide.
		if len(second.PossibleDuplicates) != 1 {
			t.Fatalf("want 1 candidate, got %+v", second.PossibleDuplicates)
		}
		c := second.PossibleDuplicates[0]
		if c.ID != first.ID {
			t.Errorf("candidate id = %s, want the existing note %s", c.ID, first.ID)
		}
		if c.Title != avaTitleOld {
			t.Errorf("candidate title = %q, want %q", c.Title, avaTitleOld)
		}
		if c.UpdatedAt.IsZero() || !localDateRe.MatchString(c.UpdatedLocalDate) {
			t.Errorf("candidate updated_at = %v / %q", c.UpdatedAt, c.UpdatedLocalDate)
		}
		if !strings.HasPrefix(c.Excerpt, "Teacher: Mrs. Patel.") {
			t.Errorf("candidate excerpt = %q, want the start of its body", c.Excerpt)
		}
		if c.TitleSimilarity != 0.8 {
			t.Errorf("candidate similarity = %v, want 0.8", c.TitleSimilarity)
		}

		// The summary and next steps say what to do about it, without doing it.
		if !strings.Contains(env.Summary, avaTitleOld) || !strings.Contains(env.Summary, "may say something different") {
			t.Errorf("summary does not name the twin: %q", env.Summary)
		}
		if len(env.Next) < 3 {
			t.Fatalf("want the append and the note_delete hints, got %v", env.Next)
		}
		joined := strings.Join(env.Next, "\n")
		for _, want := range []string{"note_update note_id=" + first.ID.String(), "note_delete", "ask which one is right"} {
			if !strings.Contains(joined, want) {
				t.Errorf("next steps are missing %q: %v", want, env.Next)
			}
		}

		// Both notes exist: nothing was merged, renamed or deleted.
		var rows int
		if err := pool.QueryRow(context.Background(),
			`SELECT count(*) FROM notes WHERE household_id = $1 AND title IN ($2, $3)`,
			hh.HouseholdID, avaTitleOld, avaTitleNew).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 2 {
			t.Errorf("want both notes stored, got %d", rows)
		}
		var storedBody string
		if err := pool.QueryRow(context.Background(),
			`SELECT body FROM notes WHERE id = $1`, first.ID).Scan(&storedBody); err != nil {
			t.Fatal(err)
		}
		if storedBody != avaBodyOld {
			t.Errorf("the existing note's body changed: %q", storedBody)
		}
	})

	t.Run("an unrelated school note stays quiet", func(t *testing.T) {
		for _, title := range []string{"School bus route", "Ava's swimming lessons", "School holiday calendar 2026"} {
			env := create.mustOK(t, ctx, map[string]any{"title": title, "content": "unrelated"})
			if len(env.Data.PossibleDuplicates) != 0 {
				t.Errorf("%q prompted about %+v", title, env.Data.PossibleDuplicates)
			}
			if strings.Contains(env.Summary, "Heads-up") {
				t.Errorf("%q: summary carries a heads-up: %q", title, env.Summary)
			}
			for _, n := range env.Next {
				if strings.Contains(n, "note_delete") {
					t.Errorf("%q: an unrelated note got the duplicate hints: %v", title, env.Next)
				}
			}
		}
	})

	t.Run("candidates are best first and capped at three", func(t *testing.T) {
		for _, title := range []string{"Ava's school pickup", "Ava's school teacher pickup", "Ava's school pickup rules"} {
			create.mustOK(t, ctx, map[string]any{"title": title, "content": "variant"})
		}
		env := create.mustOK(t, ctx, map[string]any{"title": "Ava's school teacher pickup rules", "content": "another variant"})
		got := env.Data.PossibleDuplicates
		if len(got) != duplicateMaxCandidates {
			t.Fatalf("want %d candidates, got %d: %+v", duplicateMaxCandidates, len(got), got)
		}
		for i := 1; i < len(got); i++ {
			if got[i-1].TitleSimilarity < got[i].TitleSimilarity {
				t.Errorf("candidates are not best first: %+v", got)
			}
		}
		if got[0].Title != avaTitleOld {
			t.Errorf("best match = %q, want the ticket's note %q", got[0].Title, avaTitleOld)
		}
	})

	t.Run("another household's identical title is not a candidate", func(t *testing.T) {
		env := create.mustOK(t, otherCtx, map[string]any{"title": avaTitleOld, "content": "their own copy"})
		if len(env.Data.PossibleDuplicates) != 0 {
			t.Errorf("another household's notes leaked: %+v", env.Data.PossibleDuplicates)
		}
	})

	t.Run("a repeated create reports the twin, not itself", func(t *testing.T) {
		args := map[string]any{"title": "Ava's school pickup rules", "content": "car line"}
		create.mustOK(t, ctx, args) // the first save
		again := create.mustOK(t, ctx, args)
		if !again.Data.Duplicate {
			t.Fatalf("want the loop guard's duplicate:true, got %+v", again.Data)
		}
		for _, c := range again.Data.PossibleDuplicates {
			if c.ID == again.Data.ID {
				t.Errorf("the returned note is its own candidate: %+v", c)
			}
		}
		if len(again.Data.PossibleDuplicates) == 0 {
			t.Errorf("the repeated create lost the twin it was first told about")
		}
	})
}

// TestNoteUpdateNearDuplicate covers the rename path: a rename into a collision
// is reported, an append or a body replacement is not checked at all, and the
// update always happens.
func TestNoteUpdateNearDuplicate(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)

	cn, ct, ch := NewNoteCreateTool(pool)
	create := newCaller[wireWrite](t, cn, ct, ch)
	un, ut, uh := NewNoteUpdateTool(pool)
	update := newCaller[wireWrite](t, un, ut, uh)

	existing := create.mustOK(t, ctx, map[string]any{"title": avaTitleOld, "content": avaBodyOld}).Data
	if _, err := pool.Exec(context.Background(),
		`UPDATE notes SET updated_at = NOW() - interval '2 days' WHERE id = $1`, existing.ID); err != nil {
		t.Fatal(err)
	}
	other := create.mustOK(t, ctx, map[string]any{"title": "Ava's piano lesson", "content": "Mondays 4:15"}).Data

	t.Run("renaming into a collision reports it and still renames", func(t *testing.T) {
		env := update.mustOK(t, ctx, map[string]any{"note_id": other.ID.String(), "title": avaTitleNew})
		if env.Data.Title == nil || *env.Data.Title != avaTitleNew {
			t.Fatalf("the rename did not happen: %+v", env.Data.Title)
		}
		if len(env.Data.PossibleDuplicates) != 1 || env.Data.PossibleDuplicates[0].ID != existing.ID {
			t.Fatalf("want the ticket's note as the candidate, got %+v", env.Data.PossibleDuplicates)
		}
		if !strings.Contains(env.Summary, "renamed it") || !strings.Contains(env.Summary, avaTitleOld) {
			t.Errorf("summary: %q", env.Summary)
		}
		var stored *string
		if err := pool.QueryRow(context.Background(), `SELECT title FROM notes WHERE id = $1`, other.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored == nil || *stored != avaTitleNew {
			t.Errorf("stored title = %v, want the rename", stored)
		}
	})

	t.Run("an append is not checked", func(t *testing.T) {
		env := update.mustOK(t, ctx, map[string]any{"note_id": other.ID.String(), "append_text": "- bring the green tag"})
		if len(env.Data.PossibleDuplicates) != 0 {
			t.Errorf("an append prompted about a title it did not change: %+v", env.Data.PossibleDuplicates)
		}
		if !strings.HasSuffix(env.Data.Body, "- bring the green tag") {
			t.Errorf("the append did not happen: %q", env.Data.Body)
		}
	})

	t.Run("a same-words rewrite is not a new title", func(t *testing.T) {
		id := create.mustOK(t, ctx, map[string]any{"title": "Groceries", "content": "eggs"}).Data.ID
		env := update.mustOK(t, ctx, map[string]any{"note_id": id.String(), "title": "groceries!"})
		if len(env.Data.PossibleDuplicates) != 0 {
			t.Errorf("a punctuation-only rename prompted: %+v", env.Data.PossibleDuplicates)
		}
	})

	t.Run("renaming to an unrelated title stays quiet", func(t *testing.T) {
		id := create.mustOK(t, ctx, map[string]any{"title": "Spare key", "content": "under the mat"}).Data.ID
		env := update.mustOK(t, ctx, map[string]any{"note_id": id.String(), "title": "Garage door code"})
		if len(env.Data.PossibleDuplicates) != 0 {
			t.Errorf("an unrelated rename prompted: %+v", env.Data.PossibleDuplicates)
		}
	})

	t.Run("a note is never its own candidate", func(t *testing.T) {
		env := update.mustOK(t, ctx, map[string]any{"note_id": existing.ID.String(), "content": "rewritten"})
		if len(env.Data.PossibleDuplicates) != 0 {
			t.Errorf("a body replacement prompted: %+v", env.Data.PossibleDuplicates)
		}
	})
}

// TestNearDuplicateRespectsRoles proves the prompt cannot become a leak: a note
// the caller's role may not see is not a candidate, so its title and body are
// never returned to that role.
func TestNearDuplicateRespectsRoles(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	petID := seedRow(t, pool, "pets", hh.HouseholdID)

	name, tool, h := NewNoteCreateTool(pool)
	create := newCaller[wireWrite](t, name, tool, h)

	// A family note the housesitter may not see (the #4881 matrix).
	family := create.mustOK(t, ctx, map[string]any{"title": "Feeding schedule", "content": "Bella eats at 7am"}).Data

	// The housesitter adds the pet note the role policy allows, with the same
	// title. It must not be told about the family note.
	env := create.mustOK(t, sitterCtx(t, pool, hh), map[string]any{
		"title": "Feeding schedule", "content": "Bella eats at 7am and 6pm",
		"about_type": "pet", "about_id": petID.String(),
	})
	if len(env.Data.PossibleDuplicates) != 0 {
		t.Fatalf("a note the role cannot see was named: %+v", env.Data.PossibleDuplicates)
	}
	// The body of the hidden note is the leak that would matter: its facts are
	// the ones the ticket is about. The new note's own title is the caller's
	// input and is expected in the summary.
	leak := strings.Join([]string{env.Summary, strings.Join(env.Next, " ")}, " ")
	if strings.Contains(leak, "Bella eats at 7am") {
		t.Errorf("the hidden note's body leaked: %q", leak)
	}

	// The owner sees both, so the rule is the caller's role and not the note:
	// the family note is a candidate, and so is the pet note the sitter just
	// added (the owner may see it).
	again := create.mustOK(t, ctx, map[string]any{"title": "Feeding schedule", "content": "owner copy"})
	if len(again.Data.PossibleDuplicates) != 2 {
		t.Fatalf("the owner should be told about both notes, got %+v", again.Data.PossibleDuplicates)
	}
	var sawFamily, sawSitter bool
	for _, c := range again.Data.PossibleDuplicates {
		switch c.ID {
		case family.ID:
			sawFamily = true
		case env.Data.ID:
			sawSitter = true
		}
	}
	if !sawFamily || !sawSitter {
		t.Errorf("the owner should see both notes: family=%v sitter=%v (%+v)",
			sawFamily, sawSitter, again.Data.PossibleDuplicates)
	}
}
