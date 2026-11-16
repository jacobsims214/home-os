package people

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// Handler serves the people and preferences endpoints.
//
// Access (roles from apps/api/internal/auth/roles.go, re-read from
// memberships for the household the request resolved to, so X-Household-ID
// is honoured):
//   - any member of the household may read its people and preferences;
//   - owner and family_manager may create, update and delete any person and
//     anyone's preferences;
//   - any member may update their own person record (the one linked to their
//     login) and its preferences. People without a login (kids and others)
//     are therefore editable only by owner/family_manager;
//   - a person linked to a current member can't be deleted (409): remove the
//     member instead, which unlinks the person.
//
// Ids from another household answer 404. People records hold sensitive data
// about kids (birthdate, school, allergies): they are never indexed for
// search, every response is Cache-Control: no-store, and there is no public
// route.
type Handler struct {
	repo *Repo
}

// NewHandler creates a people handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes mounts the people routes. The router must already run
// middleware.RequireAuth.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/people", h.List)
	r.Post("/api/v1/people", h.Create)
	r.Get("/api/v1/people/{id}", h.Get)
	r.Put("/api/v1/people/{id}", h.Update)
	r.Delete("/api/v1/people/{id}", h.Delete)
	r.Get("/api/v1/people/{id}/preferences", h.ListPreferences)
	r.Post("/api/v1/people/{id}/preferences", h.CreatePreference)
	r.Put("/api/v1/people/{id}/preferences/{prefId}", h.UpdatePreference)
	r.Delete("/api/v1/people/{id}/preferences/{prefId}", h.DeletePreference)
}

// --- request / response types ---

type personRequest struct {
	DisplayName  string   `json:"display_name"`
	Relationship *string  `json:"relationship"`
	Birthdate    *string  `json:"birthdate"` // YYYY-MM-DD
	School       *string  `json:"school"`
	Grade        *string  `json:"grade"`
	Allergies    []string `json:"allergies"`
	Notes        *string  `json:"notes"`
}

type preferenceRequest struct {
	Kind     string  `json:"kind"`
	Category string  `json:"category"`
	Value    string  `json:"value"`
	Notes    *string `json:"notes"`
}

type personResponse struct {
	ID           string                `json:"id"`
	HouseholdID  string                `json:"household_id"`
	UserID       *string               `json:"user_id"`
	HasLogin     bool                  `json:"has_login"`
	DisplayName  string                `json:"display_name"`
	Relationship *string               `json:"relationship"`
	Birthdate    *string               `json:"birthdate"`
	School       *string               `json:"school"`
	Grade        *string               `json:"grade"`
	Allergies    []string              `json:"allergies"`
	Notes        *string               `json:"notes"`
	CreatedAt    string                `json:"created_at"`
	UpdatedAt    string                `json:"updated_at"`
	Preferences  *[]preferenceResponse `json:"preferences,omitempty"` // Get only
}

type preferenceResponse struct {
	ID        string  `json:"id"`
	PersonID  string  `json:"person_id"`
	Kind      string  `json:"kind"`
	Category  string  `json:"category"`
	Value     string  `json:"value"`
	Notes     *string `json:"notes"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

const dateLayout = "2006-01-02"

func toPersonResponse(p *Person) personResponse {
	out := personResponse{
		ID:           p.ID.String(),
		HouseholdID:  p.HouseholdID.String(),
		HasLogin:     p.UserID != nil,
		DisplayName:  p.DisplayName,
		Relationship: p.Relationship,
		School:       p.School,
		Grade:        p.Grade,
		Allergies:    p.Allergies,
		Notes:        p.Notes,
		CreatedAt:    p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:    p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if p.UserID != nil {
		s := p.UserID.String()
		out.UserID = &s
	}
	if p.Birthdate != nil {
		s := p.Birthdate.Format(dateLayout)
		out.Birthdate = &s
	}
	return out
}

func toPreferenceResponse(p *Preference) preferenceResponse {
	return preferenceResponse{
		ID:        p.ID.String(),
		PersonID:  p.PersonID.String(),
		Kind:      p.Kind,
		Category:  p.Category,
		Value:     p.Value,
		Notes:     p.Notes,
		CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: p.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func toPreferenceResponses(prefs []*Preference) []preferenceResponse {
	out := make([]preferenceResponse, 0, len(prefs))
	for _, p := range prefs {
		out = append(out, toPreferenceResponse(p))
	}
	return out
}

// --- validation ---

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

func (req personRequest) input() (PersonInput, string) {
	var in PersonInput
	in.DisplayName = strings.TrimSpace(req.DisplayName)
	if in.DisplayName == "" {
		return in, "display_name is required"
	}
	if utf8.RuneCountInString(in.DisplayName) > 255 {
		return in, "display_name must be at most 255 characters"
	}
	var ok bool
	if in.Relationship, ok = optText(req.Relationship, 50); !ok {
		return in, "relationship must be at most 50 characters"
	}
	if in.School, ok = optText(req.School, 255); !ok {
		return in, "school must be at most 255 characters"
	}
	if in.Grade, ok = optText(req.Grade, 50); !ok {
		return in, "grade must be at most 50 characters"
	}
	in.Notes, _ = optText(req.Notes, 0)
	if b, _ := optText(req.Birthdate, 0); b != nil {
		d, err := time.Parse(dateLayout, *b)
		if err != nil {
			return in, "birthdate must be a date (YYYY-MM-DD)"
		}
		if d.After(time.Now().UTC()) {
			return in, "birthdate can't be in the future"
		}
		in.Birthdate = &d
	}
	// Trim, drop blanks, and drop case-insensitive duplicates, keeping order.
	in.Allergies = []string{}
	seen := map[string]bool{}
	for _, a := range req.Allergies {
		a = strings.TrimSpace(a)
		if a == "" || seen[strings.ToLower(a)] {
			continue
		}
		if utf8.RuneCountInString(a) > 255 {
			return in, "each allergy must be at most 255 characters"
		}
		seen[strings.ToLower(a)] = true
		in.Allergies = append(in.Allergies, a)
	}
	return in, ""
}

var validKinds = map[string]bool{"like": true, "dislike": true, "dietary": true, "other": true}

func (req preferenceRequest) input() (PreferenceInput, string) {
	in := PreferenceInput{
		Kind:     strings.ToLower(strings.TrimSpace(req.Kind)),
		Category: strings.TrimSpace(req.Category),
		Value:    strings.TrimSpace(req.Value),
	}
	if !validKinds[in.Kind] {
		return in, "kind must be one of like, dislike, dietary, other"
	}
	if in.Category == "" {
		in.Category = "general"
	}
	if utf8.RuneCountInString(in.Category) > 50 {
		return in, "category must be at most 50 characters"
	}
	if in.Value == "" {
		return in, "value is required"
	}
	in.Notes, _ = optText(req.Notes, 0)
	return in, ""
}

// --- caller and access ---

type caller struct {
	householdID uuid.UUID
	userID      uuid.UUID
	role        string
}

func (c caller) isManager() bool { return auth.CanManageMembers(c.role) }

// canEdit: managers may edit anyone; other roles with people write (family
// members) only their own record. Viewers, housesitters and vendors edit
// nobody, not even themselves (household role policy, #4689).
func (c caller) canEdit(p *Person) bool {
	if !auth.Can(c.role, auth.ResPeople, auth.ActWrite) {
		return false
	}
	return c.isManager() || (p.UserID != nil && *p.UserID == c.userID)
}

// canReadFull: the role may see birthdates, schools, grades and notes. A
// housesitter sees names, relationship and allergies only (read_basic).
func (c caller) canReadFull() bool { return auth.Can(c.role, auth.ResPeople, auth.ActRead) }

// canReadPreferences: the role may see people's preferences.
func (c caller) canReadPreferences() bool {
	return auth.Can(c.role, auth.ResPreferences, auth.ActRead)
}

// view returns p as the caller may see it: everything for roles with people
// read, otherwise only the basic fields (id, name, relationship, whether
// they log in, allergies).
func (c caller) view(p *Person) personResponse {
	out := toPersonResponse(p)
	if !c.canReadFull() {
		out.UserID = nil
		out.Birthdate = nil
		out.School = nil
		out.Grade = nil
		out.Notes = nil
	}
	return out
}

// resolveCaller reads the household the request resolved to and the caller's
// role there, from memberships. It writes 403/500 and returns ok=false for
// anyone who is not a member of that household.
func (h *Handler) resolveCaller(w http.ResponseWriter, r *http.Request) (caller, bool) {
	w.Header().Set("Cache-Control", "no-store")
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
	// Household role policy (#4689): a vendor sees no people at all.
	if !auth.Can(role, auth.ResPeople, auth.ActReadBasic) {
		apierr.Forbidden(w, middleware.DenyMessage(role, auth.ResPeople, auth.ActRead))
		return caller{}, false
	}
	return caller{householdID: hid, userID: uid, role: role}, true
}

func pathID(w http.ResponseWriter, r *http.Request, name, what string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		apierr.BadRequest(w, "invalid "+what+" id")
		return uuid.Nil, false
	}
	return id, true
}

// loadPerson resolves the {id} person in the caller's household, writing
// 400/404/500 on failure.
func (h *Handler) loadPerson(w http.ResponseWriter, r *http.Request, c caller) (*Person, bool) {
	id, ok := pathID(w, r, "id", "person")
	if !ok {
		return nil, false
	}
	p, err := h.repo.Get(r.Context(), c.householdID, id)
	if errors.Is(err, ErrNotFound) {
		apierr.NotFound(w, "person not found")
		return nil, false
	}
	if err != nil {
		apierr.InternalError(w, err)
		return nil, false
	}
	return p, true
}

func forbidEdit(w http.ResponseWriter, c caller, p *Person, action string) {
	slog.Warn("people: refused "+action, "user_id", c.userID, "household_id", c.householdID,
		"role", c.role, "person_id", p.ID)
	if !auth.Can(c.role, auth.ResPeople, auth.ActWrite) {
		apierr.Forbidden(w, middleware.DenyMessage(c.role, auth.ResPeople, auth.ActWrite))
		return
	}
	apierr.Forbidden(w, "only owners and family managers can change other people's records")
}

// --- people ---

// List returns the household's people.
// GET /api/v1/people
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	c, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	people, err := h.repo.List(r.Context(), c.householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	out := make([]personResponse, 0, len(people))
	for _, p := range people {
		out = append(out, c.view(p))
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": out})
}

// Get returns one person with their preferences.
// GET /api/v1/people/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	c, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	p, ok := h.loadPerson(w, r, c)
	if !ok {
		return
	}
	out := c.view(p)
	if c.canReadPreferences() {
		prefs, err := h.repo.ListPreferences(r.Context(), c.householdID, p.ID)
		if err != nil {
			apierr.InternalError(w, err)
			return
		}
		list := toPreferenceResponses(prefs)
		out.Preferences = &list
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": out})
}

// Create adds a person without a login (members get theirs automatically).
// Owner/family_manager only.
// POST /api/v1/people
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	c, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	if !c.isManager() {
		apierr.Forbidden(w, "only owners and family managers can add people")
		return
	}
	var req personRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	in, msg := req.input()
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	p, err := h.repo.Create(r.Context(), c.householdID, in)
	if err != nil {
		apierr.DB(w, err)
		return
	}
	apierr.JSON(w, http.StatusCreated, map[string]any{"data": toPersonResponse(p)})
}

// Update replaces a person's editable fields (all of them: omitted optional
// fields are cleared). Managers may update anyone; a member only themselves.
// PUT /api/v1/people/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	c, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	p, ok := h.loadPerson(w, r, c)
	if !ok {
		return
	}
	if !c.canEdit(p) {
		forbidEdit(w, c, p, "person update")
		return
	}
	var req personRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	in, msg := req.input()
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	updated, err := h.repo.Update(r.Context(), c.householdID, p.ID, in)
	if errors.Is(err, ErrNotFound) {
		apierr.NotFound(w, "person not found")
		return
	}
	if err != nil {
		apierr.DB(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toPersonResponse(updated)})
}

// Delete removes a person and their preferences. Owner/family_manager only;
// 409 if the person is linked to a current member.
// DELETE /api/v1/people/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	c, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	p, ok := h.loadPerson(w, r, c)
	if !ok {
		return
	}
	if !c.isManager() {
		forbidEdit(w, c, p, "person delete")
		return
	}
	switch err := h.repo.Delete(r.Context(), c.householdID, p.ID); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrNotFound):
		apierr.NotFound(w, "person not found")
	case errors.Is(err, ErrLinkedMember):
		apierr.Conflict(w, "this person is a member of the household; remove the member instead")
	default:
		apierr.InternalError(w, err)
	}
}

// --- preferences ---

// ListPreferences returns a person's preferences.
// GET /api/v1/people/{id}/preferences
func (h *Handler) ListPreferences(w http.ResponseWriter, r *http.Request) {
	c, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	if !c.canReadPreferences() {
		apierr.Forbidden(w, middleware.DenyMessage(c.role, auth.ResPreferences, auth.ActRead))
		return
	}
	p, ok := h.loadPerson(w, r, c)
	if !ok {
		return
	}
	prefs, err := h.repo.ListPreferences(r.Context(), c.householdID, p.ID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toPreferenceResponses(prefs)})
}

// editablePerson resolves {id} and checks the caller may edit it.
func (h *Handler) editablePerson(w http.ResponseWriter, r *http.Request) (caller, *Person, bool) {
	c, ok := h.resolveCaller(w, r)
	if !ok {
		return c, nil, false
	}
	p, ok := h.loadPerson(w, r, c)
	if !ok {
		return c, nil, false
	}
	if !c.canEdit(p) {
		forbidEdit(w, c, p, "preference change")
		return c, nil, false
	}
	return c, p, true
}

func writePrefErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		apierr.NotFound(w, "preference not found")
	case errors.Is(err, ErrDuplicate):
		apierr.Conflict(w, "this person already has that preference")
	default:
		apierr.DB(w, err)
	}
}

// CreatePreference adds a preference to a person.
// POST /api/v1/people/{id}/preferences
func (h *Handler) CreatePreference(w http.ResponseWriter, r *http.Request) {
	c, p, ok := h.editablePerson(w, r)
	if !ok {
		return
	}
	var req preferenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	in, msg := req.input()
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	pref, err := h.repo.CreatePreference(r.Context(), c.householdID, p.ID, in)
	if err != nil {
		writePrefErr(w, err)
		return
	}
	apierr.JSON(w, http.StatusCreated, map[string]any{"data": toPreferenceResponse(pref)})
}

// UpdatePreference replaces a preference's fields.
// PUT /api/v1/people/{id}/preferences/{prefId}
func (h *Handler) UpdatePreference(w http.ResponseWriter, r *http.Request) {
	c, p, ok := h.editablePerson(w, r)
	if !ok {
		return
	}
	prefID, ok := pathID(w, r, "prefId", "preference")
	if !ok {
		return
	}
	var req preferenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	in, msg := req.input()
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	pref, err := h.repo.UpdatePreference(r.Context(), c.householdID, p.ID, prefID, in)
	if err != nil {
		writePrefErr(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toPreferenceResponse(pref)})
}

// DeletePreference removes a preference.
// DELETE /api/v1/people/{id}/preferences/{prefId}
func (h *Handler) DeletePreference(w http.ResponseWriter, r *http.Request) {
	c, p, ok := h.editablePerson(w, r)
	if !ok {
		return
	}
	prefID, ok := pathID(w, r, "prefId", "preference")
	if !ok {
		return
	}
	if err := h.repo.DeletePreference(r.Context(), c.householdID, p.ID, prefID); err != nil {
		writePrefErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
