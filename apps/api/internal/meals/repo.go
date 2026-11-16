// Package meals serves the family's recipe box, its meal plan and what
// everyone thought of each meal (migration 047).
//
// A planned meal whose date has passed is the meal history: there is no
// separate history table, so "what did we eat last month" and "when did we
// last have tacos" are range queries on meal_plans.
//
// Every query is scoped to the household the request resolved to, so an id
// from another family behaves exactly like an id that does not exist.
package meals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrNotFound: no such recipe/meal/feedback in this household, or a
	// referenced recipe/person belongs to another household.
	ErrNotFound = errors.New("not found")
)

// Ingredient is one element of recipes.ingredients. Quantity is a number, a
// string ("1/2", "a pinch") or nil.
type Ingredient struct {
	Name     string  `json:"name"`
	Quantity any     `json:"quantity,omitempty"`
	Unit     *string `json:"unit,omitempty"`
	Section  *string `json:"section,omitempty"`
}

// Recipe is a row of recipes.
type Recipe struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	Title       string
	Description *string
	Servings    *int
	PrepMinutes *int
	CookMinutes *int
	Ingredients []Ingredient
	Steps       *string
	Tags        []string
	SourceURL   *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// RecipeInput holds the fields a caller may set on a recipe.
type RecipeInput struct {
	Title       string
	Description *string
	Servings    *int
	PrepMinutes *int
	CookMinutes *int
	Ingredients []Ingredient
	Steps       *string
	Tags        []string
	SourceURL   *string
}

// RecipeFilter narrows List. Empty fields are ignored.
type RecipeFilter struct {
	Query string // case-insensitive substring of the title
	Tag   string // exact (lower-cased) tag
}

// Meal is a row of meal_plans. Date is YYYY-MM-DD (a calendar date in the
// household's timezone, stored without one).
type Meal struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	Date        string
	Slot        string
	RecipeID    *uuid.UUID
	Title       string
	Notes       *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Feedback    []*Feedback
}

// MealInput sets a slot. Title may be nil when RecipeID is set: the database
// copies the recipe's title so the history keeps naming the meal.
type MealInput struct {
	RecipeID *uuid.UUID
	Title    *string
	Notes    *string
}

// MealFilter selects meals for ListMeals: From..To inclusive (YYYY-MM-DD),
// optionally only one recipe.
type MealFilter struct {
	From, To string
	RecipeID *uuid.UUID
}

// Feedback is a row of meal_feedback. PersonID is nil once the person was
// deleted (the feedback is kept).
type Feedback struct {
	ID         uuid.UUID
	MealPlanID uuid.UUID
	PersonID   *uuid.UUID
	Rating     *int
	Comment    *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// FeedbackInput holds a person's rating and/or comment.
type FeedbackInput struct {
	Rating  *int
	Comment *string
}

// Repo reads and writes recipes, meal_plans and meal_feedback.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a meals repository.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// Pool exposes the connection pool. The grocery merge (#4568) needs it to call
// lists.Repo.MergeItems: that write belongs to the lists package, not this one,
// so the merge does not fork the idempotent-add rule.
func (r *Repo) Pool() *pgxpool.Pool { return r.pool }

const recipeCols = `id, household_id, title, description, servings, prep_minutes, cook_minutes,
	ingredients, steps, tags, source_url, created_at, updated_at`

const mealCols = `id, household_id, date::text, slot, recipe_id, title, notes, created_at, updated_at`

const feedbackCols = `id, meal_plan_id, person_id, rating, comment, created_at, updated_at`

func scanRecipe(row pgx.Row) (*Recipe, error) {
	var (
		rc  Recipe
		raw []byte
	)
	err := row.Scan(&rc.ID, &rc.HouseholdID, &rc.Title, &rc.Description, &rc.Servings, &rc.PrepMinutes,
		&rc.CookMinutes, &raw, &rc.Steps, &rc.Tags, &rc.SourceURL, &rc.CreatedAt, &rc.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rc.Ingredients = []Ingredient{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rc.Ingredients); err != nil {
			return nil, fmt.Errorf("decode ingredients: %w", err)
		}
	}
	if rc.Tags == nil {
		rc.Tags = []string{}
	}
	return &rc, nil
}

func scanMeal(row pgx.Row) (*Meal, error) {
	var m Meal
	err := row.Scan(&m.ID, &m.HouseholdID, &m.Date, &m.Slot, &m.RecipeID, &m.Title, &m.Notes,
		&m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Feedback = []*Feedback{}
	return &m, nil
}

func scanFeedback(row pgx.Row) (*Feedback, error) {
	var (
		f      Feedback
		rating *int16
	)
	err := row.Scan(&f.ID, &f.MealPlanID, &f.PersonID, &rating, &f.Comment, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if rating != nil {
		v := int(*rating)
		f.Rating = &v
	}
	return &f, nil
}

// writeErr maps a write's error: a composite foreign key failure (recipe or
// person missing or in another household) is ErrNotFound; other errors are
// wrapped so the handler can still map check violations to 400.
func writeErr(op string, err error) error {
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}

func ingredientsJSON(in []Ingredient) ([]byte, error) {
	if in == nil {
		in = []Ingredient{}
	}
	return json.Marshal(in)
}

func tags(t []string) []string {
	if t == nil {
		return []string{}
	}
	return t
}

// MemberRole returns userID's role in householdID, read from memberships, or
// "" if the user is not a member there.
func (r *Repo) MemberRole(ctx context.Context, householdID, userID uuid.UUID) (string, error) {
	var role string
	err := r.pool.QueryRow(ctx,
		`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
		householdID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("member role: %w", err)
	}
	return role, nil
}

// HouseholdTimezone returns the household's IANA timezone name ("UTC" if the
// household has none).
func (r *Repo) HouseholdTimezone(ctx context.Context, householdID uuid.UUID) (string, error) {
	var tz string
	err := r.pool.QueryRow(ctx, `SELECT timezone FROM households WHERE id = $1`, householdID).Scan(&tz)
	if errors.Is(err, pgx.ErrNoRows) {
		return "UTC", nil
	}
	if err != nil {
		return "", fmt.Errorf("household timezone: %w", err)
	}
	return tz, nil
}

// PersonUserID returns the user linked to a person of the household (nil for
// people without a login), or ErrNotFound.
func (r *Repo) PersonUserID(ctx context.Context, householdID, personID uuid.UUID) (*uuid.UUID, error) {
	var uid *uuid.UUID
	err := r.pool.QueryRow(ctx,
		`SELECT user_id FROM people WHERE id = $1 AND household_id = $2`, personID, householdID).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("person user: %w", err)
	}
	return uid, nil
}

// --- recipes ---

// ListRecipes returns the household's recipes, ordered by title.
func (r *Repo) ListRecipes(ctx context.Context, householdID uuid.UUID, f RecipeFilter) ([]*Recipe, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+recipeCols+` FROM recipes
		  WHERE household_id = $1
		    AND ($2 = '' OR title ILIKE '%' || replace(replace(replace($2, '\', '\\'), '%', '\%'), '_', '\_') || '%')
		    AND ($3 = '' OR tags @> ARRAY[$3]::text[])
		  ORDER BY lower(title), created_at, id`, householdID, f.Query, f.Tag)
	if err != nil {
		return nil, fmt.Errorf("list recipes: %w", err)
	}
	defer rows.Close()
	out := []*Recipe{}
	for rows.Next() {
		rc, err := scanRecipe(rows)
		if err != nil {
			return nil, fmt.Errorf("scan recipe: %w", err)
		}
		out = append(out, rc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list recipes: %w", err)
	}
	return out, nil
}

// GetRecipe returns one recipe of the household, or ErrNotFound.
func (r *Repo) GetRecipe(ctx context.Context, householdID, id uuid.UUID) (*Recipe, error) {
	rc, err := scanRecipe(r.pool.QueryRow(ctx,
		`SELECT `+recipeCols+` FROM recipes WHERE id = $1 AND household_id = $2`, id, householdID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("get recipe: %w", err)
	}
	return rc, err
}

// CreateRecipe adds a recipe to the household.
func (r *Repo) CreateRecipe(ctx context.Context, householdID uuid.UUID, in RecipeInput) (*Recipe, error) {
	ing, err := ingredientsJSON(in.Ingredients)
	if err != nil {
		return nil, fmt.Errorf("encode ingredients: %w", err)
	}
	rc, err := scanRecipe(r.pool.QueryRow(ctx,
		`INSERT INTO recipes (household_id, title, description, servings, prep_minutes, cook_minutes,
		                      ingredients, steps, tags, source_url)
		 VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10)
		 RETURNING `+recipeCols,
		householdID, in.Title, in.Description, in.Servings, in.PrepMinutes, in.CookMinutes,
		string(ing), in.Steps, tags(in.Tags), in.SourceURL))
	if err != nil {
		return nil, writeErr("create recipe", err)
	}
	return rc, nil
}

// UpdateRecipe replaces a recipe's fields, or returns ErrNotFound.
func (r *Repo) UpdateRecipe(ctx context.Context, householdID, id uuid.UUID, in RecipeInput) (*Recipe, error) {
	ing, err := ingredientsJSON(in.Ingredients)
	if err != nil {
		return nil, fmt.Errorf("encode ingredients: %w", err)
	}
	rc, err := scanRecipe(r.pool.QueryRow(ctx,
		`UPDATE recipes SET title = $3, description = $4, servings = $5, prep_minutes = $6,
		        cook_minutes = $7, ingredients = $8::jsonb, steps = $9, tags = $10, source_url = $11
		  WHERE id = $1 AND household_id = $2
		 RETURNING `+recipeCols,
		id, householdID, in.Title, in.Description, in.Servings, in.PrepMinutes, in.CookMinutes,
		string(ing), in.Steps, tags(in.Tags), in.SourceURL))
	if err != nil {
		return nil, writeErr("update recipe", err)
	}
	return rc, nil
}

// DeleteRecipe removes a recipe, or returns ErrNotFound. Planned meals that
// used it keep their title (recipe_id is set to NULL).
func (r *Repo) DeleteRecipe(ctx context.Context, householdID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM recipes WHERE id = $1 AND household_id = $2`, id, householdID)
	if err != nil {
		return fmt.Errorf("delete recipe: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- meal plans ---

// ListMeals returns the household's planned meals in the filter's date
// range, ordered by date then slot (breakfast, lunch, dinner, snack), each
// with its feedback.
func (r *Repo) ListMeals(ctx context.Context, householdID uuid.UUID, f MealFilter) ([]*Meal, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+mealCols+` FROM meal_plans
		  WHERE household_id = $1 AND date BETWEEN $2::date AND $3::date
		    AND ($4::uuid IS NULL OR recipe_id = $4)
		  ORDER BY date, array_position(ARRAY['breakfast','lunch','dinner','snack']::varchar[], slot), id`,
		householdID, f.From, f.To, f.RecipeID)
	if err != nil {
		return nil, fmt.Errorf("list meals: %w", err)
	}
	defer rows.Close()
	out := []*Meal{}
	byID := map[uuid.UUID]*Meal{}
	ids := []uuid.UUID{}
	for rows.Next() {
		m, err := scanMeal(rows)
		if err != nil {
			return nil, fmt.Errorf("scan meal: %w", err)
		}
		out = append(out, m)
		byID[m.ID] = m
		ids = append(ids, m.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list meals: %w", err)
	}
	if len(ids) == 0 {
		return out, nil
	}
	fb, err := r.listFeedback(ctx, householdID, ids)
	if err != nil {
		return nil, err
	}
	for _, f := range fb {
		if m := byID[f.MealPlanID]; m != nil {
			m.Feedback = append(m.Feedback, f)
		}
	}
	return out, nil
}

// GetMeal returns one planned meal of the household with its feedback, or
// ErrNotFound.
func (r *Repo) GetMeal(ctx context.Context, householdID, id uuid.UUID) (*Meal, error) {
	m, err := scanMeal(r.pool.QueryRow(ctx,
		`SELECT `+mealCols+` FROM meal_plans WHERE id = $1 AND household_id = $2`, id, householdID))
	if errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("get meal: %w", err)
	}
	if m.Feedback, err = r.listFeedback(ctx, householdID, []uuid.UUID{m.ID}); err != nil {
		return nil, err
	}
	return m, nil
}

// SetMeal plans (or replaces) the meal in a date's slot. A recipe from
// another household is ErrNotFound. Existing feedback on the slot is kept.
func (r *Repo) SetMeal(ctx context.Context, householdID uuid.UUID, date, slot string, in MealInput) (*Meal, error) {
	// Check the recipe is this family's first: with no title the insert
	// would otherwise fail NOT NULL (the trigger finds no title to copy)
	// instead of the composite FK. The FK still catches a concurrent delete.
	if in.RecipeID != nil {
		if _, err := r.GetRecipe(ctx, householdID, *in.RecipeID); err != nil {
			return nil, err
		}
	}
	m, err := scanMeal(r.pool.QueryRow(ctx,
		`INSERT INTO meal_plans (household_id, date, slot, recipe_id, title, notes)
		 VALUES ($1, $2::date, $3, $4, $5, $6)
		 ON CONFLICT (household_id, date, slot) DO UPDATE
		    SET recipe_id = EXCLUDED.recipe_id, title = EXCLUDED.title, notes = EXCLUDED.notes
		 RETURNING `+mealCols,
		householdID, date, slot, in.RecipeID, in.Title, in.Notes))
	if err != nil {
		return nil, writeErr("set meal", err)
	}
	if m.Feedback, err = r.listFeedback(ctx, householdID, []uuid.UUID{m.ID}); err != nil {
		return nil, err
	}
	return m, nil
}

// ClearMeal removes the meal in a date's slot (and its feedback). It reports
// whether there was one.
func (r *Repo) ClearMeal(ctx context.Context, householdID uuid.UUID, date, slot string) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM meal_plans WHERE household_id = $1 AND date = $2::date AND slot = $3`,
		householdID, date, slot)
	if err != nil {
		return false, fmt.Errorf("clear meal: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// --- feedback ---

func (r *Repo) listFeedback(ctx context.Context, householdID uuid.UUID, mealIDs []uuid.UUID) ([]*Feedback, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+feedbackCols+` FROM meal_feedback
		  WHERE household_id = $1 AND meal_plan_id = ANY($2)
		  ORDER BY meal_plan_id, created_at, id`, householdID, mealIDs)
	if err != nil {
		return nil, fmt.Errorf("list feedback: %w", err)
	}
	defer rows.Close()
	out := []*Feedback{}
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, fmt.Errorf("scan feedback: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list feedback: %w", err)
	}
	return out, nil
}

// SetFeedback records (or replaces) a person's rating and/or comment on a
// planned meal of the household. A person from another household is
// ErrNotFound; the caller checks the meal belongs to the household first.
func (r *Repo) SetFeedback(ctx context.Context, householdID, mealID, personID uuid.UUID, in FeedbackInput) (*Feedback, error) {
	f, err := scanFeedback(r.pool.QueryRow(ctx,
		`INSERT INTO meal_feedback (meal_plan_id, household_id, person_id, rating, comment)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (meal_plan_id, person_id) DO UPDATE
		    SET rating = EXCLUDED.rating, comment = EXCLUDED.comment
		 RETURNING `+feedbackCols,
		mealID, householdID, personID, in.Rating, in.Comment))
	if err != nil {
		return nil, writeErr("set feedback", err)
	}
	return f, nil
}

// DeleteFeedback removes a person's feedback on a meal, or returns
// ErrNotFound.
func (r *Repo) DeleteFeedback(ctx context.Context, householdID, mealID, personID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM meal_feedback WHERE household_id = $1 AND meal_plan_id = $2 AND person_id = $3`,
		householdID, mealID, personID)
	if err != nil {
		return fmt.Errorf("delete feedback: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
