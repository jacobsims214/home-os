package meals

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// Policy decides what a member with a given household role may do with
// meals. Roles are re-read from memberships for the household the request
// resolved to (X-Household-ID aware). It is an interface so the household
// role policy (#4689) can replace DefaultPolicy without touching handlers.
type Policy interface {
	// CanRead: list/get recipes, the meal plan and feedback.
	CanRead(role string) bool
	// CanWrite: create/update/delete recipes, set/clear meal plan slots, and
	// rate meals for themselves.
	CanWrite(role string) bool
	// CanRateOthers: record or remove feedback for any person of the
	// household (kids have no login, so a parent rates for them).
	CanRateOthers(role string) bool
}

// DefaultPolicy is the meals policy until #4689 lands:
//   - owner, family_manager: everything, including rating for anyone;
//   - family_member: read, write, and rate for themselves only;
//   - viewer, housesitter: read only (a sitter needs to know the plan);
//   - vendor: nothing.
type DefaultPolicy struct{}

// CanRead implements Policy.
func (DefaultPolicy) CanRead(role string) bool {
	return auth.ValidRole(role) && role != auth.RoleVendor
}

// CanWrite implements Policy.
func (DefaultPolicy) CanWrite(role string) bool {
	return auth.CanManageMembers(role) || role == auth.RoleFamilyMember
}

// CanRateOthers implements Policy.
func (DefaultPolicy) CanRateOthers(role string) bool { return auth.CanManageMembers(role) }

// Handler serves the recipe, meal plan and meal feedback endpoints. Ids from
// another household answer 404. Plan dates are calendar dates in the
// household's timezone ("today" is computed there).
type Handler struct {
	repo   *Repo
	policy Policy
	now    func() time.Time
}

// NewHandler creates a meals handler with DefaultPolicy.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo, policy: DefaultPolicy{}, now: time.Now}
}

// WithPolicy replaces the access policy (for #4689's household role policy).
func (h *Handler) WithPolicy(p Policy) *Handler {
	h.policy = p
	return h
}

// RegisterRoutes mounts the meals routes. The router must already run
// middleware.RequireAuth.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/recipes", h.ListRecipes)
	r.Post("/api/v1/recipes", h.CreateRecipe)
	r.Get("/api/v1/recipes/{id}", h.GetRecipe)
	r.Put("/api/v1/recipes/{id}", h.UpdateRecipe)
	r.Delete("/api/v1/recipes/{id}", h.DeleteRecipe)

	r.Get("/api/v1/meal-plans", h.ListMeals)
	r.Put("/api/v1/meal-plans/{date}/{slot}", h.SetMeal)
	r.Delete("/api/v1/meal-plans/{date}/{slot}", h.ClearMeal)

	// "Put this week's dinners on the grocery list" (#4568): a preview and an
	// idempotent write. The routes live in grocery.go.
	h.registerGrocery(r)

	// One planned meal by id (and its feedback). A separate prefix, because
	// chi can't route /meal-plans/{id}/... next to /meal-plans/{date}/{slot}.
	r.Get("/api/v1/meals/{id}", h.GetMeal)
	r.Get("/api/v1/meals/{id}/feedback", h.ListFeedback)
	r.Put("/api/v1/meals/{id}/feedback/{personId}", h.SetFeedback)
	r.Delete("/api/v1/meals/{id}/feedback/{personId}", h.DeleteFeedback)
}

// --- request / response types ---

type recipeRequest struct {
	Title       string       `json:"title"`
	Description *string      `json:"description"`
	Servings    *int         `json:"servings"`
	PrepMinutes *int         `json:"prep_minutes"`
	CookMinutes *int         `json:"cook_minutes"`
	Ingredients []Ingredient `json:"ingredients"`
	Steps       *string      `json:"steps"`
	Tags        []string     `json:"tags"`
	SourceURL   *string      `json:"source_url"`
}

type mealRequest struct {
	RecipeID *string `json:"recipe_id"`
	Title    *string `json:"title"`
	Notes    *string `json:"notes"`
}

type feedbackRequest struct {
	Rating  *int    `json:"rating"`
	Comment *string `json:"comment"`
}

type recipeResponse struct {
	ID          string       `json:"id"`
	HouseholdID string       `json:"household_id"`
	Title       string       `json:"title"`
	Description *string      `json:"description"`
	Servings    *int         `json:"servings"`
	PrepMinutes *int         `json:"prep_minutes"`
	CookMinutes *int         `json:"cook_minutes"`
	Ingredients []Ingredient `json:"ingredients"`
	Steps       *string      `json:"steps"`
	Tags        []string     `json:"tags"`
	SourceURL   *string      `json:"source_url"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
}

type mealResponse struct {
	ID          string             `json:"id"`
	HouseholdID string             `json:"household_id"`
	Date        string             `json:"date"`
	Slot        string             `json:"slot"`
	RecipeID    *string            `json:"recipe_id"`
	Title       string             `json:"title"`
	Notes       *string            `json:"notes"`
	Feedback    []feedbackResponse `json:"feedback"`
	CreatedAt   string             `json:"created_at"`
	UpdatedAt   string             `json:"updated_at"`
}

type feedbackResponse struct {
	ID         string  `json:"id"`
	MealPlanID string  `json:"meal_plan_id"`
	PersonID   *string `json:"person_id"`
	Rating     *int    `json:"rating"`
	Comment    *string `json:"comment"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func optUUID(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func toRecipeResponse(rc *Recipe) recipeResponse {
	return recipeResponse{
		ID: rc.ID.String(), HouseholdID: rc.HouseholdID.String(), Title: rc.Title,
		Description: rc.Description, Servings: rc.Servings, PrepMinutes: rc.PrepMinutes,
		CookMinutes: rc.CookMinutes, Ingredients: rc.Ingredients, Steps: rc.Steps, Tags: rc.Tags,
		SourceURL: rc.SourceURL, CreatedAt: ts(rc.CreatedAt), UpdatedAt: ts(rc.UpdatedAt),
	}
}

func toFeedbackResponse(f *Feedback) feedbackResponse {
	return feedbackResponse{
		ID: f.ID.String(), MealPlanID: f.MealPlanID.String(), PersonID: optUUID(f.PersonID),
		Rating: f.Rating, Comment: f.Comment, CreatedAt: ts(f.CreatedAt), UpdatedAt: ts(f.UpdatedAt),
	}
}

func toFeedbackResponses(fb []*Feedback) []feedbackResponse {
	out := make([]feedbackResponse, 0, len(fb))
	for _, f := range fb {
		out = append(out, toFeedbackResponse(f))
	}
	return out
}

func toMealResponse(m *Meal) mealResponse {
	return mealResponse{
		ID: m.ID.String(), HouseholdID: m.HouseholdID.String(), Date: m.Date, Slot: m.Slot,
		RecipeID: optUUID(m.RecipeID), Title: m.Title, Notes: m.Notes,
		Feedback: toFeedbackResponses(m.Feedback), CreatedAt: ts(m.CreatedAt), UpdatedAt: ts(m.UpdatedAt),
	}
}

// --- validation ---

const (
	dateLayout   = "2006-01-02"
	maxRangeDays = 366
	defaultDays  = 7
)

var validSlots = map[string]bool{"breakfast": true, "lunch": true, "dinner": true, "snack": true}

// optText trims s and turns blank into nil. It reports false if the trimmed
// value is longer than max runes (max 0 means unlimited).
func optText(s *string, max int) (*string, bool) {
	if s == nil {
		return nil, true
	}
	v := strings.TrimSpace(*s)
	if v == "" {
		return nil, true
	}
	if max > 0 && utf8.RuneCountInString(v) > max {
		return nil, false
	}
	return &v, true
}

func (req recipeRequest) input() (RecipeInput, string) {
	in := RecipeInput{
		Title: strings.TrimSpace(req.Title), Servings: req.Servings,
		PrepMinutes: req.PrepMinutes, CookMinutes: req.CookMinutes,
	}
	if in.Title == "" {
		return in, "title is required"
	}
	if utf8.RuneCountInString(in.Title) > 255 {
		return in, "title must be at most 255 characters"
	}
	if in.Servings != nil && *in.Servings <= 0 {
		return in, "servings must be positive"
	}
	if (in.PrepMinutes != nil && *in.PrepMinutes < 0) || (in.CookMinutes != nil && *in.CookMinutes < 0) {
		return in, "prep_minutes and cook_minutes can't be negative"
	}
	in.Description, _ = optText(req.Description, 0)
	in.Steps, _ = optText(req.Steps, 0)
	if u, _ := optText(req.SourceURL, 2048); u != nil {
		parsed, err := url.Parse(*u)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return in, "source_url must be an http(s) URL"
		}
		in.SourceURL = u
	} else if req.SourceURL != nil && utf8.RuneCountInString(strings.TrimSpace(*req.SourceURL)) > 2048 {
		return in, "source_url must be at most 2048 characters"
	}

	in.Ingredients = make([]Ingredient, 0, len(req.Ingredients))
	for _, ing := range req.Ingredients {
		ing.Name = strings.TrimSpace(ing.Name)
		if ing.Name == "" {
			return in, "each ingredient needs a name"
		}
		ing.Unit, _ = optText(ing.Unit, 0)
		ing.Section, _ = optText(ing.Section, 0)
		switch q := ing.Quantity.(type) {
		case nil:
		case float64:
			if math.IsNaN(q) || math.IsInf(q, 0) || q < 0 {
				return in, "ingredient quantity can't be negative"
			}
		case string:
			if s := strings.TrimSpace(q); s == "" {
				ing.Quantity = nil
			} else {
				ing.Quantity = s
			}
		default:
			return in, "ingredient quantity must be a number or a string"
		}
		in.Ingredients = append(in.Ingredients, ing)
	}

	// Lower-case, trim, drop blanks and duplicates, keeping order.
	in.Tags = []string{}
	seen := map[string]bool{}
	for _, t := range req.Tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		if utf8.RuneCountInString(t) > 50 {
			return in, "each tag must be at most 50 characters"
		}
		seen[t] = true
		in.Tags = append(in.Tags, t)
	}
	return in, ""
}

func (req mealRequest) input() (MealInput, string) {
	var in MealInput
	var ok bool
	if in.Title, ok = optText(req.Title, 255); !ok {
		return in, "title must be at most 255 characters"
	}
	in.Notes, _ = optText(req.Notes, 0)
	if rid, _ := optText(req.RecipeID, 0); rid != nil {
		id, err := uuid.Parse(*rid)
		if err != nil {
			return in, "invalid recipe_id"
		}
		in.RecipeID = &id
	}
	if in.RecipeID == nil && in.Title == nil {
		return in, "title is required when there is no recipe_id"
	}
	return in, ""
}

func (req feedbackRequest) input() (FeedbackInput, string) {
	in := FeedbackInput{Rating: req.Rating}
	if in.Rating != nil && (*in.Rating < 1 || *in.Rating > 5) {
		return in, "rating must be between 1 and 5"
	}
	in.Comment, _ = optText(req.Comment, 0)
	if in.Rating == nil && in.Comment == nil {
		return in, "a rating or a comment is required"
	}
	return in, ""
}

// --- caller and access ---

type caller struct {
	householdID uuid.UUID
	userID      uuid.UUID
	role        string
}

// resolveCaller reads the household the request resolved to and the caller's
// role there, from memberships, and checks the policy lets them read. It
// writes 403/500 and returns ok=false otherwise.
func (h *Handler) resolveCaller(w http.ResponseWriter, r *http.Request) (caller, bool) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing household context")
		return caller{}, false
	}
	hid, err1 := uuid.Parse(claims.HouseholdID)
	uid, err2 := uuid.Parse(claims.UserID)
	if err1 != nil || err2 != nil {
		apierr.Forbidden(w, "missing household context")
		return caller{}, false
	}
	role, err := h.repo.MemberRole(r.Context(), hid, uid)
	if err != nil {
		apierr.InternalError(w, err)
		return caller{}, false
	}
	if role == "" {
		apierr.Forbidden(w, "not a member of this household")
		return caller{}, false
	}
	if !h.policy.CanRead(role) {
		apierr.Forbidden(w, "your role can't see meals")
		return caller{}, false
	}
	return caller{householdID: hid, userID: uid, role: role}, true
}

// writer resolves the caller and checks they may change meals.
func (h *Handler) writer(w http.ResponseWriter, r *http.Request, action string) (caller, bool) {
	c, ok := h.resolveCaller(w, r)
	if !ok {
		return c, false
	}
	if !h.policy.CanWrite(c.role) {
		slog.Warn("meals: refused "+action, "user_id", c.userID, "household_id", c.householdID, "role", c.role)
		apierr.Forbidden(w, "your role can only view meals")
		return c, false
	}
	return c, true
}

func pathID(w http.ResponseWriter, r *http.Request, name, what string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		apierr.BadRequest(w, "invalid "+what+" id")
		return uuid.Nil, false
	}
	return id, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return false
	}
	return true
}

// today returns the current date in the household's timezone.
func (h *Handler) today(r *http.Request, c caller) (time.Time, error) {
	name, err := h.repo.HouseholdTimezone(r.Context(), c.householdID)
	if err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		slog.Warn("meals: bad household timezone, using UTC", "household_id", c.householdID, "timezone", name)
		loc = time.UTC
	}
	y, m, d := h.now().In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC), nil
}

// slotPath parses {date}/{slot}, writing 400 on failure.
func slotPath(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	date := chi.URLParam(r, "date")
	if _, err := time.Parse(dateLayout, date); err != nil {
		apierr.BadRequest(w, "date must be YYYY-MM-DD")
		return "", "", false
	}
	slot := strings.ToLower(chi.URLParam(r, "slot"))
	if !validSlots[slot] {
		apierr.BadRequest(w, "slot must be one of breakfast, lunch, dinner, snack")
		return "", "", false
	}
	return date, slot, true
}

// --- recipes ---

// ListRecipes returns the household's recipes. ?q= matches the title
// (case-insensitive substring), ?tag= filters by tag.
// GET /api/v1/recipes
func (h *Handler) ListRecipes(w http.ResponseWriter, r *http.Request) {
	c, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	f := RecipeFilter{
		Query: strings.TrimSpace(r.URL.Query().Get("q")),
		Tag:   strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tag"))),
	}
	list, err := h.repo.ListRecipes(r.Context(), c.householdID, f)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	out := make([]recipeResponse, 0, len(list))
	for _, rc := range list {
		out = append(out, toRecipeResponse(rc))
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": out})
}

// GetRecipe returns one recipe.
// GET /api/v1/recipes/{id}
func (h *Handler) GetRecipe(w http.ResponseWriter, r *http.Request) {
	c, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id", "recipe")
	if !ok {
		return
	}
	rc, err := h.repo.GetRecipe(r.Context(), c.householdID, id)
	if errors.Is(err, ErrNotFound) {
		apierr.NotFound(w, "recipe not found")
		return
	}
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toRecipeResponse(rc)})
}

// CreateRecipe adds a recipe.
// POST /api/v1/recipes
func (h *Handler) CreateRecipe(w http.ResponseWriter, r *http.Request) {
	c, ok := h.writer(w, r, "recipe create")
	if !ok {
		return
	}
	var req recipeRequest
	if !decodeBody(w, r, &req) {
		return
	}
	in, msg := req.input()
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	rc, err := h.repo.CreateRecipe(r.Context(), c.householdID, in)
	if err != nil {
		apierr.DB(w, err)
		return
	}
	apierr.JSON(w, http.StatusCreated, map[string]any{"data": toRecipeResponse(rc)})
}

// UpdateRecipe replaces a recipe's fields (omitted optional fields are
// cleared).
// PUT /api/v1/recipes/{id}
func (h *Handler) UpdateRecipe(w http.ResponseWriter, r *http.Request) {
	c, ok := h.writer(w, r, "recipe update")
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id", "recipe")
	if !ok {
		return
	}
	var req recipeRequest
	if !decodeBody(w, r, &req) {
		return
	}
	in, msg := req.input()
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	rc, err := h.repo.UpdateRecipe(r.Context(), c.householdID, id, in)
	if errors.Is(err, ErrNotFound) {
		apierr.NotFound(w, "recipe not found")
		return
	}
	if err != nil {
		apierr.DB(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toRecipeResponse(rc)})
}

// DeleteRecipe removes a recipe. Planned meals that used it keep their title.
// DELETE /api/v1/recipes/{id}
func (h *Handler) DeleteRecipe(w http.ResponseWriter, r *http.Request) {
	c, ok := h.writer(w, r, "recipe delete")
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id", "recipe")
	if !ok {
		return
	}
	switch err := h.repo.DeleteRecipe(r.Context(), c.householdID, id); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrNotFound):
		apierr.NotFound(w, "recipe not found")
	default:
		apierr.InternalError(w, err)
	}
}

// --- meal plans ---

// ListMeals returns planned meals with their feedback for ?from=..?to=
// (inclusive, YYYY-MM-DD). Without from, the range starts today in the
// household's timezone; without to, it runs 7 days. Past dates are the meal
// history. ?recipe_id= narrows to one recipe ("when did we last have
// tacos"). At most 366 days per request.
// GET /api/v1/meal-plans
func (h *Handler) ListMeals(w http.ResponseWriter, r *http.Request) {
	c, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var from, to time.Time
	var err error
	if s := strings.TrimSpace(q.Get("from")); s != "" {
		if from, err = time.Parse(dateLayout, s); err != nil {
			apierr.BadRequest(w, "from must be YYYY-MM-DD")
			return
		}
	} else if from, err = h.today(r, c); err != nil {
		apierr.InternalError(w, err)
		return
	}
	if s := strings.TrimSpace(q.Get("to")); s != "" {
		if to, err = time.Parse(dateLayout, s); err != nil {
			apierr.BadRequest(w, "to must be YYYY-MM-DD")
			return
		}
	} else {
		to = from.AddDate(0, 0, defaultDays-1)
	}
	if to.Before(from) {
		apierr.BadRequest(w, "to must not be before from")
		return
	}
	if to.Sub(from) >= maxRangeDays*24*time.Hour {
		apierr.BadRequest(w, "the range can be at most 366 days")
		return
	}
	f := MealFilter{From: from.Format(dateLayout), To: to.Format(dateLayout)}
	if s := strings.TrimSpace(q.Get("recipe_id")); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			apierr.BadRequest(w, "invalid recipe_id")
			return
		}
		f.RecipeID = &id
	}
	meals, err := h.repo.ListMeals(r.Context(), c.householdID, f)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	out := make([]mealResponse, 0, len(meals))
	for _, m := range meals {
		out = append(out, toMealResponse(m))
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": out, "from": f.From, "to": f.To})
}

// loadMeal resolves {id} to a planned meal of the caller's household.
func (h *Handler) loadMeal(w http.ResponseWriter, r *http.Request, c caller) (*Meal, bool) {
	id, ok := pathID(w, r, "id", "meal plan")
	if !ok {
		return nil, false
	}
	m, err := h.repo.GetMeal(r.Context(), c.householdID, id)
	if errors.Is(err, ErrNotFound) {
		apierr.NotFound(w, "meal plan not found")
		return nil, false
	}
	if err != nil {
		apierr.InternalError(w, err)
		return nil, false
	}
	return m, true
}

// GetMeal returns one planned meal with its feedback.
// GET /api/v1/meals/{id}
func (h *Handler) GetMeal(w http.ResponseWriter, r *http.Request) {
	c, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	m, ok := h.loadMeal(w, r, c)
	if !ok {
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toMealResponse(m)})
}

// SetMeal plans (or replaces) the meal in a date's slot. Body: recipe_id
// and/or title (title defaults to the recipe's), notes. Feedback already
// given on the slot is kept.
// PUT /api/v1/meal-plans/{date}/{slot}
func (h *Handler) SetMeal(w http.ResponseWriter, r *http.Request) {
	c, ok := h.writer(w, r, "meal plan set")
	if !ok {
		return
	}
	date, slot, ok := slotPath(w, r)
	if !ok {
		return
	}
	var req mealRequest
	if !decodeBody(w, r, &req) {
		return
	}
	in, msg := req.input()
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	m, err := h.repo.SetMeal(r.Context(), c.householdID, date, slot, in)
	if errors.Is(err, ErrNotFound) {
		apierr.NotFound(w, "recipe not found")
		return
	}
	if err != nil {
		apierr.DB(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toMealResponse(m)})
}

// ClearMeal empties a date's slot (its feedback goes with it). Clearing an
// empty slot is not an error.
// DELETE /api/v1/meal-plans/{date}/{slot}
func (h *Handler) ClearMeal(w http.ResponseWriter, r *http.Request) {
	c, ok := h.writer(w, r, "meal plan clear")
	if !ok {
		return
	}
	date, slot, ok := slotPath(w, r)
	if !ok {
		return
	}
	if _, err := h.repo.ClearMeal(r.Context(), c.householdID, date, slot); err != nil {
		apierr.InternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- feedback ---

// ListFeedback returns everyone's feedback on a planned meal.
// GET /api/v1/meals/{id}/feedback
func (h *Handler) ListFeedback(w http.ResponseWriter, r *http.Request) {
	c, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	m, ok := h.loadMeal(w, r, c)
	if !ok {
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toFeedbackResponses(m.Feedback)})
}

// feedbackTarget resolves {id} and {personId} and checks the caller may
// record feedback for that person: anyone with CanRateOthers, otherwise a
// writer only for the person linked to their own login.
func (h *Handler) feedbackTarget(w http.ResponseWriter, r *http.Request, action string) (caller, *Meal, uuid.UUID, bool) {
	c, ok := h.writer(w, r, action)
	if !ok {
		return c, nil, uuid.Nil, false
	}
	m, ok := h.loadMeal(w, r, c)
	if !ok {
		return c, nil, uuid.Nil, false
	}
	pid, ok := pathID(w, r, "personId", "person")
	if !ok {
		return c, nil, uuid.Nil, false
	}
	uid, err := h.repo.PersonUserID(r.Context(), c.householdID, pid)
	if errors.Is(err, ErrNotFound) {
		apierr.NotFound(w, "person not found")
		return c, nil, uuid.Nil, false
	}
	if err != nil {
		apierr.InternalError(w, err)
		return c, nil, uuid.Nil, false
	}
	if !h.policy.CanRateOthers(c.role) && (uid == nil || *uid != c.userID) {
		slog.Warn("meals: refused "+action, "user_id", c.userID, "household_id", c.householdID,
			"role", c.role, "person_id", pid)
		apierr.Forbidden(w, "you can only rate meals for yourself")
		return c, nil, uuid.Nil, false
	}
	return c, m, pid, true
}

// SetFeedback records (or replaces) a person's rating (1-5) and/or comment
// on a planned meal.
// PUT /api/v1/meals/{id}/feedback/{personId}
func (h *Handler) SetFeedback(w http.ResponseWriter, r *http.Request) {
	c, m, pid, ok := h.feedbackTarget(w, r, "feedback set")
	if !ok {
		return
	}
	var req feedbackRequest
	if !decodeBody(w, r, &req) {
		return
	}
	in, msg := req.input()
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	f, err := h.repo.SetFeedback(r.Context(), c.householdID, m.ID, pid, in)
	if errors.Is(err, ErrNotFound) {
		apierr.NotFound(w, "meal plan or person not found")
		return
	}
	if err != nil {
		apierr.DB(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toFeedbackResponse(f)})
}

// DeleteFeedback removes a person's feedback on a planned meal.
// DELETE /api/v1/meals/{id}/feedback/{personId}
func (h *Handler) DeleteFeedback(w http.ResponseWriter, r *http.Request) {
	c, m, pid, ok := h.feedbackTarget(w, r, "feedback delete")
	if !ok {
		return
	}
	switch err := h.repo.DeleteFeedback(r.Context(), c.householdID, m.ID, pid); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrNotFound):
		apierr.NotFound(w, "feedback not found")
	default:
		apierr.InternalError(w, err)
	}
}
