package note

// The near-duplicate title rule (#4564), the API half of #4945.
//
// The threshold table is copied from apps/mcp/internal/note/duplicates.go: the
// two implementations must score the same pair the same way, or a person and
// their agent are told about different twins.

import (
	"testing"
)

func TestTitleSimilarity(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		// The ticket's exact pair: the war game's contradictory twins.
		{"Ava's school — teacher & pickup rules", "Ava's school: teacher & pickup", 0.8},
		{"Ava's school pickup", "Ava's school — teacher & pickup rules", 0.6},
		{"Ava's school — teacher & pickup rules", "Ava's school — teacher & pickup rules", 1},
		{"Ava's school pickup", "Ava's school bus route", 0.4},
		{"School pickup rules", "School bus route", 0.2},
		{"Ava's school — teacher & pickup rules", "Ava's swimming lessons", 0.14},
		{"Wi-Fi", "Wi-Fi password", 0.5},
		{"Groceries", "Grocery list", 0},
		{"The", "The", 1},
	}
	for _, c := range cases {
		if got := TitleSimilarity(c.a, c.b); got != c.want {
			t.Errorf("TitleSimilarity(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
		if got := TitleSimilarity(c.b, c.a); got != c.want {
			t.Errorf("TitleSimilarity(%q, %q) = %v, want %v (not symmetric)", c.b, c.a, got, c.want)
		}
	}
}

func TestTitleSimilarityThresholdDecisions(t *testing.T) {
	// The positive the rule exists for is reported...
	if got := TitleSimilarity("Ava's school — teacher & pickup rules", "Ava's school: teacher & pickup"); got < duplicateThreshold {
		t.Errorf("the ticket's twin scored %v, below the %v threshold", got, duplicateThreshold)
	}
	// ...and the noise cases stay quiet.
	for _, pair := range [][2]string{
		{"Wi-Fi", "Wi-Fi password"},
		{"School pickup rules", "School bus route"},
		{"Ava's school pickup", "Ava's school bus route"},
	} {
		if got := TitleSimilarity(pair[0], pair[1]); got >= duplicateThreshold {
			t.Errorf("%q vs %q scored %v, at or above the threshold", pair[0], pair[1], got)
		}
	}
}

func TestTitleTokens(t *testing.T) {
	// Hyphens and underscores join, so "pick-up" is one word.
	if a, b := titleTokens("pick-up"), titleTokens("pickup"); len(a) != len(b) {
		t.Errorf("pick-up tokenised to %v, pickup to %v", a, b)
	}
	// A possessive reduces to its stem.
	if toks := titleTokens("Ava's school"); !toks["ava"] || !toks["school"] {
		t.Errorf("Ava's school tokenised to %v", toks)
	}
	// Stop words go; a title of only stop words falls back to its raw words.
	if toks := titleTokens("The and of"); !toks["the"] {
		t.Errorf("a stop-word-only title should fall back to its raw words, got %v", toks)
	}
	// An empty title resembles nothing.
	if toks := titleTokens("   "); len(toks) != 0 {
		t.Errorf("blank title tokenised to %v", toks)
	}
}

func TestExcerpt(t *testing.T) {
	long := ""
	for i := 0; i < 300; i++ {
		long += "x"
	}
	if got := excerpt(long); len([]rune(got)) != duplicateExcerptRunes {
		t.Errorf("excerpt length = %d, want %d", len([]rune(got)), duplicateExcerptRunes)
	}
	// Whitespace runs collapse, so a note written as short lines reads as one.
	if got := excerpt("line one\n\nline two\t\tend"); got != "line one line two end" {
		t.Errorf("excerpt = %q", got)
	}
}

func TestTitleSimilarityIsNotLevenshtein(t *testing.T) {
	// A typo'd single word still leaves the rest of the title scoring well:
	// set-based Jaccard is the deliberate choice (#4945).
	if got := TitleSimilarity("Ava's school — teacher & pickup rules", "Ava's school — teecher & pickup rules"); got < duplicateThreshold {
		t.Errorf("a one-word typo scored %v; the rest of the title should still match", got)
	}
}
