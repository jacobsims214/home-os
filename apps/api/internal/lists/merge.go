package lists

// Merging items into a list without duplicating them (#4568).
//
// WHY NOT AddItems. Repo.AddItems appends, and appends unconditionally: it is
// the primitive behind "type milk onto the list", where two rows saying milk is
// what the person asked for. The grocery flow is the opposite — a week of meals
// becomes a set of items, and pressing the button twice (an impatient second
// tap, a retry after a dropped response, a phone that slept mid-request) must
// not turn 12 items into 24 on a list another family member may be working
// through in store mode right now. That is the manager decision on #4519
// (comment #13046).
//
// The assistant already has this behaviour: apps/mcp/internal/lists/tools.go's
// addItems, with wordMatch on, treats an existing item whose WORDS match as the
// same item (any case, singular or plural, without a leading the/a/an), skips
// it when it is open, and reopens it when it was checked off. This is that
// behaviour, in the lists package where the write belongs, so the REST merge
// endpoint does not have to fork it into the meals package or reach into MCP.
//
// One deliberate difference from the MCP path: a match that is open but says a
// DIFFERENT amount is reported as CheckAmount instead of being silently skipped.
// The preview page shows it as "Check amount", because the list says 3 lb and
// the plan needs 6 lb and only a person knows whether to buy more. Nothing is
// written for those rows.

import (
	"context"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MergeItemStatus is what happened to one proposed item.
type MergeItemStatus string

const (
	// MergeAdded: a new row was inserted.
	MergeAdded MergeItemStatus = "added"
	// MergeReopened: a checked-off row with the same words was put back on the list.
	MergeReopened MergeItemStatus = "reopened"
	// MergeAlreadyOnList: an open row with the same words is already there.
	MergeAlreadyOnList MergeItemStatus = "already_on_list"
	// MergeCheckAmount: an open row with the same words says a different
	// amount. Nothing is written; a person decides.
	MergeCheckAmount MergeItemStatus = "check_amount"
)

// MergeItem is one item to merge in.
type MergeItem struct {
	Text     string
	Quantity *float64
	Unit     *string
	Section  *string
}

// MergedItem is one item's outcome.
type MergedItem struct {
	Text   string          `json:"text"`
	Status MergeItemStatus `json:"status"`
	// ItemID is the row this refers to: the new one, or the existing match.
	// Empty only if a row vanished between the read and the write.
	ItemID string `json:"item_id,omitempty"`
	// Existing is the amount already on the list, for a CheckAmount row.
	ExistingQuantity *float64 `json:"existing_quantity,omitempty"`
	ExistingUnit     *string  `json:"existing_unit,omitempty"`
	// Wanted is the amount the plan needs, echoed for a CheckAmount row.
	WantedQuantity *float64 `json:"wanted_quantity,omitempty"`
	WantedUnit     *string  `json:"wanted_unit,omitempty"`
}

// MergeResult is the outcome of one merge.
type MergeResult struct {
	ListID string       `json:"list_id"`
	Items  []MergedItem `json:"items"`
	// Repeats counts items that were already on the list this call found there
	// (already on it, reopened, or a different amount). It is the honest answer
	// to "did my second tap do anything": 0 means it wrote every row.
	Repeats int `json:"repeats"`
}

var wordSep = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// matchTokens is the word form of an item's text for matching: lower-case
// words, each made singular, without leading filler. Copied from
// apps/mcp/internal/lists/tools.go matchTokens so the assistant and the page
// agree about which rows are "the same item".
func matchTokens(s string) []string {
	var out []string
	for _, w := range wordSep.Split(strings.ToLower(s), -1) {
		if w == "" || (len(out) == 0 && mergeFiller[w]) {
			continue
		}
		out = append(out, mergeSingular(w))
	}
	return out
}

// mergeKey is matchTokens joined back up, the map key for matching.
func mergeKey(s string) string { return strings.Join(matchTokens(s), " ") }

var mergeFiller = map[string]bool{"the": true, "a": true, "an": true, "some": true, "my": true, "our": true}

// mergeSingular strips a plural ending, so "eggs" matches "egg". Matching
// only; the stored text is never changed.
func mergeSingular(w string) string {
	switch {
	case (strings.HasSuffix(w, "sses") || strings.HasSuffix(w, "xes") ||
		strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "shes")) && len(w) > 4:
		return strings.TrimSuffix(w, "es") // glasses, boxes, peaches, dishes
	case strings.HasSuffix(w, "oes") && len(w) > 4:
		return strings.TrimSuffix(w, "es")
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		return strings.TrimSuffix(w, "ies") + "y"
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && len(w) > 3:
		return strings.TrimSuffix(w, "s")
	}
	return w
}

// MergeItems adds items to a list idempotently: an item whose words already
// match a row on the list is not added again. Returns one MergedItem per
// proposed item, in order, so a preview can be rendered from a dry run and the
// applied result matches it row for row.
//
// dryRun reports what WOULD happen without writing: the merge endpoint uses it
// for the preview page, and because it runs the same code the preview cannot
// disagree with the write.
func (r *Repo) MergeItems(ctx context.Context, hid, listID uuid.UUID, items []MergeItem, dryRun bool) (*MergeResult, error) {
	out := &MergeResult{ListID: listID.String(), Items: []MergedItem{}}
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		out.Items = out.Items[:0]
		out.Repeats = 0
		// The list row lock is what makes this race-free against a second
		// confirm tap (or the assistant adding milk at the same moment): no
		// other adder runs until this transaction ends.
		if dryRun {
			if err := checkWritable(ctx, tx, hid, listID); err != nil {
				return err
			}
		} else {
			if err := lockList(ctx, tx, hid, listID); err != nil {
				return err
			}
		}

		rows, err := tx.Query(ctx, `
			SELECT id, text, quantity::float8, unit, checked
			  FROM list_items WHERE list_id = $1 AND household_id = $2`, listID, hid)
		if err != nil {
			return err
		}
		type existing struct {
			id       uuid.UUID
			quantity *float64
			unit     *string
			checked  bool
		}
		// Several rows can share a key ("Chicken thighs" added by the merge and
		// "Chicken thigh" typed by hand). They are all kept, because more than
		// one is itself a reason to ask a person rather than guess which one
		// the plan means.
		open := map[string][]existing{}
		done := map[string][]existing{}
		for rows.Next() {
			var (
				id  uuid.UUID
				txt string
				q   *float64
				u   *string
				chk bool
			)
			if err := rows.Scan(&id, &txt, &q, &u, &chk); err != nil {
				rows.Close()
				return err
			}
			e := existing{id: id, quantity: q, unit: u, checked: chk}
			k := mergeKey(txt)
			if chk {
				done[k] = append(done[k], e)
			} else {
				open[k] = append(open[k], e)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		seen := map[string]bool{}
		for _, it := range items {
			k := mergeKey(it.Text)
			res := MergedItem{Text: it.Text}
			if k == "" {
				continue // "the" alone is not an item
			}
			if seen[k] {
				// The plan lists the same ingredient twice under two names
				// ("tomato" and "tomatoes"): the merge already handles it, so
				// report the first outcome rather than adding a second row.
				continue
			}
			seen[k] = true

			if rows, ok := open[k]; ok && len(rows) > 0 {
				// Already on the list. One row with the same amount: nothing to
				// do. A different amount — or several rows saying different
				// things — is a person's decision, not this endpoint's.
				e := rows[0]
				res.ItemID = e.id.String()
				if len(rows) > 1 || amountsDiffer(it.Quantity, it.Unit, e.quantity, e.unit) {
					res.Status = MergeCheckAmount
					res.ExistingQuantity, res.ExistingUnit = e.quantity, e.unit
					res.WantedQuantity, res.WantedUnit = it.Quantity, it.Unit
				} else {
					res.Status = MergeAlreadyOnList
				}
				out.Repeats++
				out.Items = append(out.Items, res)
				continue
			}
			if rows, ok := done[k]; ok && len(rows) > 0 {
				e := rows[0]
				// It was checked off. Back on the list: one row, unchecked.
				res.ItemID = e.id.String()
				res.Status = MergeReopened
				out.Repeats++
				if !dryRun {
					if _, err := tx.Exec(ctx, `
						UPDATE list_items
						   SET checked = false,
						       quantity = COALESCE($2::numeric, quantity),
						       unit = COALESCE($3, unit),
						       section = COALESCE($4, section)
						 WHERE id = $1`, e.id, it.Quantity, it.Unit, it.Section); err != nil {
						return err
					}
				}
				out.Items = append(out.Items, res)
				continue
			}

			res.Status = MergeAdded
			if dryRun {
				out.Items = append(out.Items, res)
				continue
			}
			var id uuid.UUID
			if err := tx.QueryRow(ctx, `
				INSERT INTO list_items (list_id, household_id, text, quantity, unit, section)
				VALUES ($1, $2, $3, $4::numeric, $5, $6) RETURNING id`,
				listID, hid, it.Text, it.Quantity, it.Unit, it.Section).Scan(&id); err != nil {
				return err
			}
			res.ItemID = id.String()
			// A row added earlier in this same call must be visible to the
			// rows after it, so "tomatoes" and "tomato" in one plan collapse
			// to one insert.
			open[k] = append(open[k], existing{id: id, quantity: it.Quantity, unit: it.Unit, checked: false})
			out.Items = append(out.Items, res)
		}
		if dryRun {
			// The preview must not leave a transaction's worth of work behind;
			// returning an error rolls it back with nothing written.
			return errDryRun
		}
		return nil
	})
	if err == errDryRun {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// errDryRun unwinds a dry-run transaction. It never escapes this file.
var errDryRun = dryRunRollback{}

type dryRunRollback struct{}

func (dryRunRollback) Error() string { return "dry run: rolling back" }

// amountsDiffer reports whether the plan's amount and the list's differ.
//
// An absent amount on either side is not a difference: a row saying "chicken
// thighs" with no quantity is not in conflict with a plan that needs 6 lb, it
// just does not say. When both sides do say, the quantities are compared, and
// so are the units — "2 cup" and "2 tbsp" are different amounts even though the
// numbers match.
func amountsDiffer(wantQ *float64, wantU *string, haveQ *float64, haveU *string) bool {
	if wantQ == nil || haveQ == nil {
		return false
	}
	if diff := *wantQ - *haveQ; diff >= 0.001 || diff <= -0.001 {
		return true
	}
	return !sameUnitText(wantU, haveU)
}

// sameUnitText compares two units the way a person reads them: absent and empty
// are the same, and case/whitespace do not matter.
func sameUnitText(a, b *string) bool {
	norm := func(s *string) string {
		if s == nil {
			return ""
		}
		return strings.ToLower(strings.TrimSpace(*s))
	}
	return norm(a) == norm(b)
}
