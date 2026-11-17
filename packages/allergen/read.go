package allergen

// Reading allergy text: the one normaliser, used for people.allergies entries
// and for preference text (#4802). Both are free text a person or a small model
// typed, so both get the same reading: "Shrimp allergy", "allergic to
// peanuts", "Peanuts - anaphylaxis" and "peanuts: EpiPen" all mean an
// allergen.

import (
	"regexp"
	"slices"
	"strings"
)

var (
	// Mention spots allergy talk in a preference.
	Mention = regexp.MustCompile(`(?i)\b(allerg\w*|anaphyla\w*|epi-?pens?|intoleran\w*|celiac|coeliac)\b`)

	// allergyFiller is what is left out of "severe peanut allergy" to get the
	// allergen. "celiac" and "lactose" stay: they are allergen group keys.
	//
	// The intensifiers, connectors and medical-ID words are #4874: without
	// them "Deathly allergic to peanuts" normalised to "deathly peanuts", a
	// phrase that matches no ingredient, so the peanut allergy silently did
	// nothing. Every word here has to be one that is never a food. "and" and
	// "or" are deliberately absent: ListSeparator splits on them after this runs, so
	// "peanuts and tree nuts" stays two allergens.
	allergyFiller = regexp.MustCompile(`(?i)\b(severe(ly)?|mild|moderate|major|minor|serious|terrible|awful|horrible|horribly|life[- ]threatening|anaphylactic|anaphylaxis|food|foods|allergic|allergy|allergies|allergen|allergens|intolerant|intolerance|intolerances|sensitivity|sensitive|sensitised|sensitized|reaction|reactions|reacts|reacting|reacted|gets|gives|causes|cause|caused|to|of|for|a|an|the|has|have|is|no|avoid|avoids|carries|carry|wears|wear|epi-?pens?|medicalert|medic[- ]?alert|medic[- ]?id|medical([- ]id)?|medication|medicines?|meds?|prescribed|prescription|antihistamines?|epinephrine|adrenaline|inhaler|bracelet|alert|must|strictly|strict|very|really|extremely|highly|strongly|slightly|badly|possibly|all|deathly|deadly|super|totally|absolutely|incredibly|insanely|ridiculously)\b`)

	ListSeparator = regexp.MustCompile(`(?i)\s*(?:,|;|/|&|\band\b|\bor\b|\+)\s*`)

	freeSuffix = regexp.MustCompile(`(?i)[- ]free\b`)

	// noteStart is where an allergy stops being the allergen and becomes a
	// note: parentheses ("peanuts (carries an EpiPen)", #4742), a spaced dash
	// ("peanuts - anaphylaxis", "peanuts – anaphylaxis") or a colon
	// ("peanuts: EpiPen").
	noteStart = regexp.MustCompile(`\([^)]*\)|\s[-–—]\s[^\n]*|:[^\n]*`)
)

// AllergensIn reads the allergens out of allergy text: "Severe peanut
// allergy" -> [peanut], "allergic to shrimp and crab" -> [shrimp, crab],
// "nut-free (allergy)" -> [nut]. Empty when nothing is left to match.
func AllergensIn(text string) []string {
	text = noteStart.ReplaceAllString(strings.ToLower(text), " ")
	text = strings.ReplaceAll(text, "coeliac", "celiac")
	text = freeSuffix.ReplaceAllString(text, " ")
	text = allergyFiller.ReplaceAllString(text, " ")
	var out []string
	for _, p := range ListSeparator.Split(text, -1) {
		p = strings.Join(strings.Fields(p), " ")
		if len(Words(p)) > 0 && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// CompilePhrases compiles each phrase's Go regex (PhrasePatterns): whole
// words only. The allergy's own word, the exceptions and the recipe search
// filters use this.
func CompilePhrases(phrases []string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, p := range phrases {
		if _, goRe, ok := PhrasePatterns(p); ok {
			out = append(out, regexp.MustCompile(goRe))
		}
	}
	return out
}

// CompileGroupPhrases compiles a group's words with the curated compound
// forms (GroupPatterns). Groups and the diet word lists go through
// here; the allergy's own word and the exceptions never do, so an egg allergy
// still ignores eggplant and a dairy one still ignores peanut butter (#4805).
func CompileGroupPhrases(phrases []string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, p := range phrases {
		if goRe, ok := GroupPatterns(p); ok {
			out = append(out, regexp.MustCompile(goRe))
		}
	}
	return out
}

// NewMatcher builds the regexes for one allergen, e.g. "tree nuts".
// allergen is already normalised (AllergensIn); it is both the phrase matched
// against ingredient names and the label shown in warnings.
//
// ok=false means the phrase has no words at all, or it is multi-word and the
// vocabulary does not know it (KnownPhrase). Before #4874 the only test
// was "has a word", so any junk phrase compiled, matched nothing, and was
// treated as a checked allergy. The matcher is still built, so a caller
// matching text that is not an allergy (a dislike) can use it as before; an
// allergy caller must report the phrase instead of trusting it.
func NewMatcher(personID, person, allergen string) (Matcher, bool) {
	m := Matcher{PersonID: personID, Person: person, Allergy: allergen}
	m.Own = CompilePhrases([]string{allergen})
	for _, key := range GroupKeys(allergen) {
		// Group words allow the curated compound forms (catfish, cornbread,
		// cheesecake); the allergy's own word and the exceptions do not (#4805).
		m.Group = append(m.Group, CompileGroupPhrases(Groups[key])...)
		m.Except = append(m.Except, CompilePhrases(Exceptions[key])...)
	}
	return m, len(m.Own) > 0 && KnownPhrase(allergen)
}

// possessive is the "'s" in "cow's milk": a group key is built from the words
// alone, so "cow's milk" and "cows milk" both key to "cow milk" (#4804).
var possessive = strings.NewReplacer("'s", "", "\u2019s", "", "'", "", "\u2019", "")

// AllergyKey is what the group and alias maps are looked up with: the
// allergen's words, singularised and with the possessive dropped, so "cow's
// milk" is one allergen and not "cow s milk" (#4804).
func AllergyKey(allergen string) string {
	ws := Words(possessive.Replace(strings.ToLower(allergen)))
	for i, w := range ws {
		ws[i] = Singular(w)
	}
	return strings.Join(ws, " ")
}
