package meals

// The week plan's grocery list (#4568).
//
// Two endpoints, one merge:
//
//	GET  /api/v1/meal-plans/grocery?from&to&slots&list_id   PREVIEW. Writes nothing.
//	POST /api/v1/meal-plans/grocery?from&to&slots&list_id   APPLY. Idempotent.
//
// Both return the same shape, so the page renders the preview with the very
// component it renders the result with, and the two cannot disagree.
//
// WHY A PREVIEW. The write goes into a SHARED list another family member may be
// working through in store mode at that moment (manager decision on #4519,
// comment #13046). So the page shows every merged item with Add / On the list /
// Check amount, and only then does anyone confirm.
//
// WHY NOT THE MCP TOOL. meal_build_grocery_list (#4500,
// apps/mcp/internal/meals/grocery.go) already merges a week into items, and the
// rules here are ported from it (apps/api/internal/grocery). But the UI cannot
// call the MCP (it refuses `home-os-ui` tokens by audience, #4692), and that
// tool computes and writes in one go with no preview. The merge is duplicated
// rather than shared for that reason; apps/api/internal/grocery's header names
// the follow-up that removes it.
//
// IDEMPOTENCE lives in lists.Repo.MergeItems, not here: an item whose words
// already match an open row is not added again, a checked match is reopened, and
// a match with a different amount is reported as check_amount and left alone.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"home-os/api/internal/grocery"
	"home-os/api/internal/lists"
	"home-os/api/pkg/apierr"
)

// maxGroceryDays bounds the range, like the meal plan's own limit.
const maxGroceryDays = 31

// --- rows ---

// loadGroceryMeals reads the meals planned in [from, to] with the recipe each
// uses. The LATERAL join is meal_build_grocery_list's query, so a free-text
// "Sheet-pan chicken fajitas" is that recipe in both.
func (r *Repo) loadGroceryMeals(ctx context.Context, hid uuid.UUID, from, to time.Time, slots []string) ([]grocery.Meal, error) {
	q := `
SELECT m.date::text, m.slot::text, m.title, r.id::text, r.title, r.servings, r.ingredients
  FROM meal_plans m
  LEFT JOIN LATERAL (
       SELECT r.id, r.title, r.servings, r.ingredients FROM recipes r
        WHERE ` + grocery.MealIsRecipe + `
        ORDER BY (r.id = m.recipe_id) IS TRUE DESC, r.id
        LIMIT 1) r ON true
 WHERE m.household_id = $1 AND m.date BETWEEN $2::date AND $3::date
   AND ($4::text[] IS NULL OR m.slot::text = ANY($4::text[]))
 ORDER BY m.date, array_position(ARRAY['breakfast','lunch','dinner','snack']::text[], m.slot::text), m.id`
	var slotFilter []string
	if len(slots) > 0 {
		slotFilter = slots
	}
	rows, err := r.pool.Query(ctx, q, hid, from.Format(dateLayout), to.Format(dateLayout), slotFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []grocery.Meal{}
	for rows.Next() {
		var (
			date, slot, title string
			recipeID          *string
			recipeTitle       *string
			servings          *int
			raw               []byte
		)
		if err := rows.Scan(&date, &slot, &title, &recipeID, &recipeTitle, &servings, &raw); err != nil {
			return nil, err
		}
		ings, err := decodeIngredients(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, grocery.Meal{
			Date: date, Slot: slot, Title: title,
			RecipeID: recipeID, RecipeTitle: recipeTitle, Servings: servings, Ingredients: ings,
		})
	}
	return out, rows.Err()
}

// decodeIngredients turns recipes.ingredients into the merge's ingredient
// rows. A recipe with no ingredients column value is empty, not an error.
func decodeIngredients(raw []byte) ([]grocery.Ingredient, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var parsed []Ingredient
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	out := make([]grocery.Ingredient, 0, len(parsed))
	for _, in := range parsed {
		out = append(out, grocery.Ingredient{Name: in.Name, Quantity: in.Quantity, Unit: in.Unit})
	}
	return out, nil
}

// --- the shared merge ---

// GroceryList is the target list as the page needs it.
type GroceryList struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
}

// GroceryItem is one merged ingredient with what the list already says.
type GroceryItem struct {
	Text     string   `json:"text"`
	Quantity *float64 `json:"quantity"`
	Unit     *string  `json:"unit"`
	Section  string   `json:"section"`
	Meals    []string `json:"meals"`
	Note     string   `json:"note,omitempty"`
	// Status: add | already_on_list | check_amount. (A re-opened checked item
	// reads as "add" — it is going back on the list — and the summary says so.)
	Status string `json:"status"`
	// Existing is what the list says, for a check_amount row.
	ExistingQuantity *float64 `json:"existing_quantity,omitempty"`
	ExistingUnit     *string  `json:"existing_unit,omitempty"`
}

// GroceryCounts is the summary strip above the confirm button.
type GroceryCounts struct {
	Add         int `json:"add"`
	AlreadyOn   int `json:"already_on"`
	CheckAmount int `json:"check_amount"`
	Reopened    int `json:"reopened"`
}

// GroceryResult is the preview and the applied result.
type GroceryResult struct {
	From        string            `json:"from"`
	To          string            `json:"to"`
	Slots       []string          `json:"slots"`
	List        GroceryList       `json:"list"`
	Items       []GroceryItem     `json:"items"`
	Sections    []string          `json:"sections"`
	Skipped     []grocery.Skipped `json:"skipped"`
	Planned     int               `json:"planned"`
	RecipeMeals int               `json:"recipe_meals"`
	Notes       []string          `json:"notes"`
	Counts      GroceryCounts     `json:"counts"`
	// Applied is false for a preview and true once the items were written.
	Applied bool `json:"applied"`
}

// mergeGrocery is the one implementation behind both endpoints. dryRun is the
// only difference between the preview and the write.
func (h *Handler) mergeGrocery(ctx context.Context, hid uuid.UUID, from, to time.Time, slots []string, listRef string, dryRun bool) (*GroceryResult, error) {
	meals, err := h.repo.loadGroceryMeals(ctx, hid, from, to, slots)
	if err != nil {
		return nil, err
	}
	merged := grocery.Merge(meals, from.Format(dateLayout), to.Format(dateLayout))

	out := &GroceryResult{
		From: merged.From, To: merged.To, Slots: slots,
		Items: []GroceryItem{}, Sections: merged.Sections, Skipped: merged.Skipped,
		Planned: merged.Planned, RecipeMeals: merged.RecipeMeals, Notes: merged.Notes,
		Applied: !dryRun,
	}
	if out.Sections == nil {
		out.Sections = []string{}
	}
	if out.Notes == nil {
		out.Notes = []string{}
	}
	if out.Slots == nil {
		out.Slots = []string{}
	}

	listRepo := lists.NewRepo(h.repo.Pool())
	target, err := resolveGroceryList(ctx, h.repo.Pool(), hid, listRepo, listRef)
	if err != nil {
		return nil, err
	}

	if len(merged.Items) == 0 {
		out.List = target
		return out, nil
	}

	proposed := make([]lists.MergeItem, 0, len(merged.Items))
	for _, it := range merged.Items {
		section := it.Section
		proposed = append(proposed, lists.MergeItem{
			Text: it.Text, Quantity: it.Quantity, Unit: it.Unit, Section: &section,
		})
	}

	// No grocery list yet: the apply makes one. The preview says so and marks
	// every row as an add, because that is what the confirm will do.
	outcomes := make([]lists.MergedItem, len(proposed))
	if target.Exists {
		listID, parseErr := uuid.Parse(target.ID)
		if parseErr != nil {
			return nil, parseErr
		}
		res, mergeErr := listRepo.MergeItems(ctx, hid, listID, proposed, dryRun)
		if mergeErr != nil {
			return nil, mergeErr
		}
		outcomes = res.Items
	} else {
		for i, p := range proposed {
			outcomes[i] = lists.MergedItem{Text: p.Text, Status: lists.MergeAdded}
		}
		if !dryRun {
			created, createdErr := listRepo.Create(ctx, hid, lists.NewList{Name: "Groceries", Kind: "grocery"}, nil)
			if createdErr != nil {
				return nil, createdErr
			}
			target = GroceryList{ID: created.ID.String(), Name: created.Name, Exists: true}
			listID := created.ID
			// The new list is empty, so this merge inserts every row.
			res, mergeErr := listRepo.MergeItems(ctx, hid, listID, proposed, false)
			if mergeErr != nil {
				return nil, mergeErr
			}
			outcomes = res.Items
		}
	}
	out.List = target

	for i, it := range merged.Items {
		item := GroceryItem{
			Text: it.Text, Quantity: it.Quantity, Unit: it.Unit, Section: it.Section,
			Meals: it.Meals, Note: it.Note, Status: "add",
		}
		if i < len(outcomes) {
			switch outcomes[i].Status {
			case lists.MergeAlreadyOnList:
				item.Status = "already_on_list"
				out.Counts.AlreadyOn++
			case lists.MergeCheckAmount:
				item.Status = "check_amount"
				item.ExistingQuantity, item.ExistingUnit = outcomes[i].ExistingQuantity, outcomes[i].ExistingUnit
				out.Counts.CheckAmount++
			case lists.MergeReopened:
				// Back on the list: a real change, so it counts as an add, and
				// the count of re-opened rows is reported separately.
				out.Counts.Add++
				out.Counts.Reopened++
			default:
				out.Counts.Add++
			}
		} else {
			out.Counts.Add++
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// resolveGroceryList finds the target list without creating one, so a preview
// can say "no grocery list yet; the confirm will start one".
func resolveGroceryList(ctx context.Context, pool pgxQuerier, hid uuid.UUID, listRepo *lists.Repo, listRef string) (GroceryList, error) {
	ref := strings.TrimSpace(listRef)
	if ref != "" {
		var (
			id   uuid.UUID
			name string
		)
		// An id, then a kind word, then a name — the same order the lists
		// package resolves a list_id in, so "grocery" and "costco" both work.
		err := pool.QueryRow(ctx, `
			SELECT id, name FROM lists
			 WHERE household_id = $1 AND NOT archived AND NOT is_template
			   AND (kind = 'grocery' OR id::text = $2 OR lower(name) = lower($2)
			        OR lower(name) LIKE '%' || lower($2) || '%')
			 ORDER BY (id::text = $2) DESC, (lower(name) = lower($2)) DESC,
			          (kind = 'grocery') DESC, updated_at DESC, id
			 LIMIT 1`, hid, ref).Scan(&id, &name)
		if errors.Is(err, pgx.ErrNoRows) {
			return GroceryList{}, apierr.ErrNotFound
		}
		if err != nil {
			return GroceryList{}, err
		}
		return GroceryList{ID: id.String(), Name: name, Exists: true}, nil
	}
	var (
		id   uuid.UUID
		name string
	)
	err := pool.QueryRow(ctx, `
		SELECT id, name FROM lists
		 WHERE household_id = $1 AND kind = 'grocery' AND NOT archived AND NOT is_template
		 ORDER BY updated_at DESC, id LIMIT 1`, hid).Scan(&id, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return GroceryList{Name: "Groceries", Exists: false}, nil
	}
	if err != nil {
		return GroceryList{}, err
	}
	return GroceryList{ID: id.String(), Name: name, Exists: true}, nil
}

// pgxQuerier is the slice of pgx the list lookup needs.
type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// --- HTTP ---

// groceryRange reads and validates from/to/slots/list_id.
func groceryRange(w http.ResponseWriter, r *http.Request) (from, to time.Time, slots []string, listRef string, ok bool) {
	q := r.URL.Query()
	fromS, toS := strings.TrimSpace(q.Get("from")), strings.TrimSpace(q.Get("to"))
	if fromS == "" || toS == "" {
		apierr.BadRequest(w, "from and to are required, as YYYY-MM-DD")
		return
	}
	var err error
	if from, err = time.Parse(dateLayout, fromS); err != nil {
		apierr.BadRequest(w, "from must be YYYY-MM-DD")
		return
	}
	if to, err = time.Parse(dateLayout, toS); err != nil {
		apierr.BadRequest(w, "to must be YYYY-MM-DD")
		return
	}
	if to.Before(from) {
		apierr.BadRequest(w, "to must be on or after from")
		return
	}
	if to.Sub(from) > time.Duration(maxGroceryDays)*24*time.Hour {
		apierr.BadRequest(w, "the range can be at most 31 days")
		return
	}
	for _, raw := range q["slots"] {
		for _, s := range strings.Split(raw, ",") {
			v := strings.ToLower(strings.TrimSpace(s))
			if v == "" {
				continue
			}
			if !validSlots[v] {
				apierr.BadRequest(w, "slots must be breakfast, lunch, dinner or snack")
				return
			}
			slots = append(slots, v)
		}
	}
	return from, to, slots, strings.TrimSpace(q.Get("list_id")), true
}

// GetGrocery previews the merge. Writes nothing.
// GET /api/v1/meal-plans/grocery?from=&to=&slots=dinner&list_id=
func (h *Handler) GetGrocery(w http.ResponseWriter, r *http.Request) {
	c, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	from, to, slots, listRef, ok := groceryRange(w, r)
	if !ok {
		return
	}
	res, err := h.mergeGrocery(r.Context(), c.householdID, from, to, slots, listRef, true)
	if err != nil {
		h.groceryErr(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": res})
}

// PostGrocery writes the merge into the grocery list, idempotently.
// POST /api/v1/meal-plans/grocery?from=&to=&slots=dinner&list_id=
func (h *Handler) PostGrocery(w http.ResponseWriter, r *http.Request) {
	c, ok := h.writer(w, r, "add the week's dinners to the grocery list")
	if !ok {
		return
	}
	from, to, slots, listRef, ok := groceryRange(w, r)
	if !ok {
		return
	}
	res, err := h.mergeGrocery(r.Context(), c.householdID, from, to, slots, listRef, false)
	if err != nil {
		h.groceryErr(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": res})
}

// groceryErr maps the merge's failures. A named list that is gone, or archived,
// is a 404/409 rather than a 500, because the page offers to pick another one.
func (h *Handler) groceryErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apierr.ErrNotFound):
		apierr.NotFound(w, "that list is not here")
	case errors.Is(err, lists.ErrArchived):
		apierr.Conflict(w, "that list is archived; unarchive it to add to it")
	default:
		apierr.DB(w, err)
	}
}

// registerGrocery mounts the two routes next to the rest of the meals API.
func (h *Handler) registerGrocery(r chi.Router) {
	r.Get("/api/v1/meal-plans/grocery", h.GetGrocery)
	r.Post("/api/v1/meal-plans/grocery", h.PostGrocery)
}
