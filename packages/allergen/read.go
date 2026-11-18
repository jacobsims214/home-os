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
	//
	// A word here can still be part of a food's NAME in the vocabulary:
	// "food" is in the alias "sea food", "intolerance"/"intolerant" are in
	// "lactose intolerance"/"lactose intolerant". aliasPhrases protects those
	// names from this regex (#4883), so a filler word can no longer silently
	// make an alias unreachable — TestEveryAliasIsReachableFromStoredText is
	// what proves it, and a new alias containing a filler word needs no
	// special case.
	allergyFiller = regexp.MustCompile(`(?i)\b(severe(ly)?|mild|moderate|major|minor|serious|terrible|awful|horrible|horribly|life[- ]threatening|anaphylactic|anaphylaxis|food|foods|allergic|allergy|allergies|allergen|allergens|intolerant|intolerance|intolerances|sensitivity|sensitive|sensitised|sensitized|reaction|reactions|reacts|reacting|reacted|gets|gives|causes|cause|caused|to|of|for|a|an|the|has|have|is|no|avoid|avoids|carries|carry|wears|wear|epi-?pens?|medicalert|medic[- ]?alert|medic[- ]?id|medical([- ]id)?|medication|medicines?|meds?|prescribed|prescription|antihistamines?|epinephrine|adrenaline|inhaler|bracelet|alert|must|strictly|strict|very|really|extremely|highly|strongly|slightly|badly|possibly|all|deathly|deadly|super|totally|absolutely|incredibly|insanely|ridiculously)\b`)

	// aliasPhrases finds the alias names allergyFiller would otherwise take a
	// word out of ("sea food"), so the reading keeps them whole (#4883).
	// Built from Aliases, so an alias added later is covered without a code
	// change.
	aliasPhrases = buildAliasPhrases()

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
//
// Filler words are stripped, but never out of a multi-word name the
// vocabulary defines. Without that, "sea food" lost "food" (a filler word) and
// became the one-word phrase "sea", which keys to no alias: the
// "sea food" -> fish + shellfish alias was unreachable from any stored text,
// so a parent who wrote "sea food" got an allergy phrase that matched no
// ingredient (#4883). It is the fail-closed rule's blind spot — a one-word
// phrase is always "known" — so the guard has to be here, in the reading.
func AllergensIn(text string) []string {
	text = noteStart.ReplaceAllString(strings.ToLower(text), " ")
	text = strings.ReplaceAll(text, "coeliac", "celiac")
	text = freeSuffix.ReplaceAllString(text, " ")
	text = stripFiller(text, aliasPhrases.FindAllStringIndex(text, -1))
	var out []string
	for _, p := range ListSeparator.Split(text, -1) {
		p = strings.Join(strings.Fields(p), " ")
		if len(Words(p)) > 0 && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// buildAliasPhrases compiles the alias names that allergyFiller would
// otherwise destroy into one alternation (#4883). Two kinds need no
// protection, and both are left out so the list stays exactly as small as the
// problem:
//
//   - single-word keys: the filler matches whole words, so it cannot reach
//     inside "seafood";
//   - multi-word keys that survive stripping with their meaning intact
//     ("lactose intolerant" -> "lactose", which is itself the group key the
//     alias points at), so the reading is unchanged and so are the labels
//     warnings have always shown.
//
// "sea food" is the one that fails both: "food" is filler, and what is left
// ("sea") is a one-word phrase that keys to no group at all.
func buildAliasPhrases() *regexp.Regexp {
	var keys []string
	for key := range Aliases {
		if !strings.Contains(key, " ") {
			continue
		}
		if strippedAliasStillMeans(key) {
			continue
		}
		keys = append(keys, key)
	}
	// Longest first, so a whole-alias match wins over a shorter alias that
	// happens to be its prefix.
	slices.SortFunc(keys, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	parts := make([]string, len(keys))
	for i, key := range keys {
		ws := Words(key)
		for j, w := range ws {
			ws[j] = WordForm(w) // the plural of the name still names it
		}
		parts[i] = strings.Join(ws, `[^\w]+`)
	}
	// The body is PhrasePatterns' own, so "sea-food" and "sea  food" match as
	// "sea food" does. (?i) is belt and braces: AllergensIn lowercases first,
	// but the expression is exported behaviour through AllergensIn only, and
	// keeping it case-insensitive costs nothing.
	return regexp.MustCompile(`(?i)\b(?:` + strings.Join(parts, "|") + `)\b`)
}

// strippedAliasStillMeans reports whether the filler can take words out of an
// alias without losing what it means: the stripped phrase has to resolve to
// every group the alias points at. "lactose intolerant" does, because it
// strips to "lactose", the group key it maps to; "sea food" does not, because
// it strips to "sea".
func strippedAliasStillMeans(key string) bool {
	stripped := strings.Join(strings.Fields(allergyFiller.ReplaceAllString(key, " ")), " ")
	if stripped == key {
		return true // no filler word in it: nothing can be stripped
	}
	got := GroupKeys(stripped)
	for _, want := range Aliases[key] {
		if !slices.Contains(got, want) {
			return false
		}
	}
	return true
}

// stripFiller replaces every allergyFiller word in text with a space, except
// the ones that fall inside a keep span (a multi-word alias name), which are
// left exactly as written. Spans are byte offsets into the text passed in, and
// everything outside a match is copied through untouched, so the offsets stay
// valid as the builder advances.
func stripFiller(text string, keep [][]int) string {
	matches := allergyFiller.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for _, m := range matches {
		if insideSpan(m, keep) {
			continue
		}
		b.WriteString(text[last:m[0]])
		b.WriteByte(' ')
		last = m[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

// insideSpan reports whether the filler match m lies wholly inside one of the
// protected spans.
func insideSpan(m []int, spans [][]int) bool {
	for _, s := range spans {
		if m[0] >= s[0] && m[1] <= s[1] {
			return true
		}
	}
	return false
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
