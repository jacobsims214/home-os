package allergen

// Loading the family's allergies. An allergy can be saved in two places —
// people.allergies and any person_preferences row whose value or notes mention
// one — and both count (#4803): before that, only meal_suggest read the second,
// so a recipe the suggester refused could be planned with no warning at all.

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the database handle the loader needs: a *pgxpool.Pool, a pgx.Tx or
// anything else that can run these two queries. It is an interface so the MCP
// can pass a pool and a transaction alike.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Sources is the one loader for the family's allergies (#4803). An
// allergy can be saved in two places, and both count:
//
//  1. people.allergies, the field person_update writes.
//  2. any person_preferences row whose value or notes mention an allergy,
//     whatever kind it was filed under: a dietary "Severe shellfish allergy",
//     an other "tree nuts" noted "allergic", a dislike "sesame" noted
//     "allergy".
//
// Before #4803 only meal_suggest read the second source, so a recipe the
// suggester refused could be planned with no warning at all.
//
// opts.PersonIDs limits it to those people (meal_suggest's eaters); nil means
// everyone in the family, an empty slice nobody. checks holds one line per
// allergy that could not be read as an allergen, so the caller can show it
// rather than let the warning silently vanish.
//
// opts.QuotePreferenceText decides whether a preference's own value and notes
// may appear in those lines. The MCP and the API's family roles pass true. A
// caller who may see allergies but NOT preferences (a housesitter,
// decisions/2026-11-15-role-policy.md) passes false: the allergy is still read
// and still applied, and the check still fires, but it says only that an
// allergy could not be read — never what the preference said.
func Load(ctx context.Context, q Querier, hid uuid.UUID, opts Options) (matchers []Matcher, checks []string, err error) {
	rows, err := q.Query(ctx, `SELECT id::text, display_name, allergies FROM people
	                            WHERE household_id = $1 AND ($2::uuid[] IS NULL OR id = ANY($2::uuid[]))
	                            ORDER BY lower(display_name), id`, hid, opts.PersonIDs)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	names := map[string]string{}
	for rows.Next() {
		var id, name string
		var allergies []string
		if err := rows.Scan(&id, &name, &allergies); err != nil {
			return nil, nil, err
		}
		names[id] = name
		for _, a := range allergies {
			ms, unchecked := Matchers(id, name, a)
			matchers = append(matchers, ms...)
			if len(ms) == 0 && len(unchecked) == 0 {
				// Nothing in the entry was readable at all ("allergy",
				// "anaphylactic"): report it as it was saved (#4802).
				unchecked = []string{a}
			}
			// One line per fragment the matcher would not vouch for, so the
			// allergy is visible in checks instead of silently matching
			// nothing (#4874).
			for _, u := range unchecked {
				checks = append(checks, UncheckedAllergyLine(name, u))
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	prefs, err := q.Query(ctx, `
SELECT person_id::text, kind::text, value, coalesce(notes, '')
  FROM person_preferences
 WHERE household_id = $1 AND ($2::uuid[] IS NULL OR person_id = ANY($2::uuid[]))
 ORDER BY person_id, kind, lower(value)`, hid, opts.PersonIDs)
	if err != nil {
		return nil, nil, err
	}
	defer prefs.Close()
	for prefs.Next() {
		var personID, kind, value, notes string
		if err := prefs.Scan(&personID, &kind, &value, &notes); err != nil {
			return nil, nil, err
		}
		// Allergy talk anywhere is an allergy, whatever kind it was filed as.
		if !Mention.MatchString(value) && !Mention.MatchString(notes) {
			continue
		}
		name := names[personID]
		// "peanut allergy" -> peanut; a dislike "shrimp" noted "allergic" -> shrimp.
		found := AllergensIn(value)
		if len(found) == 0 {
			found = AllergensIn(notes)
		}
		if kind == "like" && !Mention.MatchString(value) && opts.QuotePreferenceText {
			// A like noted "allergic" is contradictory: flag it, and treat it as
			// an allergy. The note is preference text, so a caller that may not
			// read preferences gets no line: the allergy is applied either way,
			// and a warning fires if it matches.
			checks = append(checks, fmt.Sprintf("%s has %q saved as a like with the note %q; treated as an allergy.", name, value, notes))
		}
		added := 0
		for _, a := range found {
			// found is already normalised by AllergensIn, but it is still the
			// PREFERENCE's text ("purple elephants"), so the fail-closed line
			// must not quote it to a caller who may not read preferences.
			if m, ok := NewMatcher(personID, name, a); ok {
				matchers = append(matchers, m)
				added++
			} else if opts.QuotePreferenceText {
				checks = append(checks, UncheckedAllergyLine(name, a))
			} else {
				checks = append(checks, UnreadableAllergyLine(name))
			}
		}
		if added == 0 && len(found) == 0 {
			if opts.QuotePreferenceText {
				checks = append(checks, fmt.Sprintf("%s has an allergy note %q that could not be read as an allergen; confirm the meal is safe.", name, strings.TrimSpace(value+" "+notes)))
			} else {
				checks = append(checks, UnreadableAllergyLine(name))
			}
		}
	}
	return matchers, checks, prefs.Err()
}

// Options selects what Load reads and how much of a preference it may quote.
type Options struct {
	// PersonIDs limits the load to those people; nil means the whole family,
	// an empty slice nobody.
	PersonIDs []string
	// QuotePreferenceText lets a preference's value and notes appear in the
	// checks lines. False for a caller that may see allergies but not
	// preferences: the allergy is still read and applied, the wording just
	// stops short of the preference text.
	QuotePreferenceText bool
}

// Sources loads the family's allergies for the given people, quoting the
// stored allergy text as saved: the historical signature, kept so every
// existing MCP caller and the engine's own tests read exactly as before.
func Sources(ctx context.Context, q Querier, hid uuid.UUID, personIDs []string) (matchers []Matcher, checks []string, err error) {
	return Load(ctx, q, hid, Options{PersonIDs: personIDs, QuotePreferenceText: true})
}

// FamilyAllergies loads every person's allergies as matchers, from both places
// an allergy can be saved (Sources). checks holds one line per allergy
// that could not be read as an allergen: it matches no ingredient, so the
// caller must show it rather than let the warning silently vanish.
func FamilyAllergies(ctx context.Context, q Querier, hid uuid.UUID) (matchers []Matcher, checks []string, err error) {
	return Load(ctx, q, hid, Options{QuotePreferenceText: true})
}
