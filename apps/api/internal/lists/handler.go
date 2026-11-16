package lists

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// Limits on request sizes and column widths (migration 046).
const (
	maxBatch      = 500
	maxNameLen    = 255
	maxUnitLen    = 50
	maxSectionLen = 100
	maxTextLen    = 2000
	// feedOverlap is subtracted from as_of so that a write committed just
	// after a read (with an earlier updated_at) is still returned by the
	// next ?since= poll. Clients merge items by id, so repeats are harmless.
	feedOverlap = 5 * time.Second
)

// Action is what a request does to lists, for the role policy.
type Action string

const (
	// ActionRead: list lists and items.
	ActionRead Action = "read"
	// ActionManage: create, rename, re-subject, archive, delete lists.
	ActionManage Action = "manage"
	// ActionEditItems: edit, remove and reorder items.
	ActionEditItems Action = "edit_items"
	// ActionAddItems: add items (e.g. a housesitter adding to the shopping
	// list).
	ActionAddItems Action = "add_items"
	// ActionCheckItems: check and uncheck items (e.g. a housesitter working
	// through a chores list).
	ActionCheckItems Action = "check_items"
)

// Policy decides whether a member with role (their role in the request's
// X-Household-ID household) may perform action. Role restrictions (#4689)
// plug in here with Handler.WithPolicy.
type Policy func(role string, action Action) bool

// AllowAll lets every household member do everything. It is the default
// until role restrictions land.
func AllowAll(string, Action) bool { return true }

// Handler serves the lists endpoints.
type Handler struct {
	repo   *Repo
	policy Policy
	now    func() time.Time
}

// NewHandler creates a lists handler with the AllowAll policy.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo, policy: AllowAll, now: time.Now}
}

// WithPolicy replaces the role policy.
func (h *Handler) WithPolicy(p Policy) *Handler {
	h.policy = p
	return h
}

// RegisterRoutes mounts the lists routes. The router must already run
// middleware.RequireAuth.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/lists", h.List)
	r.Post("/api/v1/lists", h.Create)
	r.Get("/api/v1/lists/{id}", h.Get)
	r.Patch("/api/v1/lists/{id}", h.Update)
	r.Delete("/api/v1/lists/{id}", h.Delete)
	r.Post("/api/v1/lists/{id}/archive", h.Archive)
	r.Post("/api/v1/lists/{id}/unarchive", h.Unarchive)

	r.Get("/api/v1/lists/{id}/items", h.Items)
	r.Post("/api/v1/lists/{id}/items", h.AddItems)
	r.Post("/api/v1/lists/{id}/items/check", h.CheckItems)
	r.Post("/api/v1/lists/{id}/items/uncheck", h.UncheckItems)
	r.Post("/api/v1/lists/{id}/items/remove", h.RemoveItems)
	r.Patch("/api/v1/lists/{id}/items/{itemId}", h.UpdateItem)
	r.Delete("/api/v1/lists/{id}/items/{itemId}", h.DeleteItem)
	r.Post("/api/v1/lists/{id}/items/{itemId}/move", h.MoveItem)
}

// --- JSON types ---

// opt is a JSON field that records whether it was sent, so PATCH can tell
// "absent" (keep) from null (clear).
type opt[T any] struct {
	Set bool
	Val *T
}

func (o *opt[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Val = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Val = &v
	return nil
}

type itemRequest struct {
	Text             string   `json:"text"`
	Quantity         *float64 `json:"quantity"`
	Unit             *string  `json:"unit"`
	Section          *string  `json:"section"`
	AssigneePersonID *string  `json:"assignee_person_id"`
	DueDate          *string  `json:"due_date"`
	Checked          bool     `json:"checked"`
}

type createRequest struct {
	Name       string        `json:"name"`
	Kind       string        `json:"kind"`
	EntityType *string       `json:"entity_type"`
	EntityID   *string       `json:"entity_id"`
	IsTemplate bool          `json:"is_template"`
	TemplateID *string       `json:"template_id"`
	Items      []itemRequest `json:"items"`
}

type updateRequest struct {
	Name       *string `json:"name"`
	Kind       *string `json:"kind"`
	EntityType *string `json:"entity_type"`
	EntityID   *string `json:"entity_id"`
	IsTemplate *bool   `json:"is_template"`
	Archived   *bool   `json:"archived"`
}

type itemPatchRequest struct {
	Text             *string      `json:"text"`
	Quantity         opt[float64] `json:"quantity"`
	Unit             opt[string]  `json:"unit"`
	Section          opt[string]  `json:"section"`
	AssigneePersonID opt[string]  `json:"assignee_person_id"`
	DueDate          opt[string]  `json:"due_date"`
	Checked          *bool        `json:"checked"`
}

type idsRequest struct {
	ItemIDs []string `json:"item_ids"`
}

type moveRequest struct {
	AfterID *string `json:"after_id"`
}

type listResponse struct {
	ID          string          `json:"id"`
	HouseholdID string          `json:"household_id"`
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	EntityType  string          `json:"entity_type"`
	EntityID    string          `json:"entity_id"`
	IsTemplate  bool            `json:"is_template"`
	Archived    bool            `json:"archived"`
	ItemCount   int             `json:"item_count"`
	OpenCount   int             `json:"open_count"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
	Items       *[]itemResponse `json:"items,omitempty"`
}

type itemResponse struct {
	ID               string   `json:"id"`
	ListID           string   `json:"list_id"`
	Text             string   `json:"text"`
	Quantity         *float64 `json:"quantity"`
	Unit             *string  `json:"unit"`
	Section          *string  `json:"section"`
	Checked          bool     `json:"checked"`
	CheckedAt        *string  `json:"checked_at"`
	CheckedBy        *string  `json:"checked_by"`
	CheckedByName    *string  `json:"checked_by_name"`
	AssigneePersonID *string  `json:"assignee_person_id"`
	DueDate          *string  `json:"due_date"`
	Position         float64  `json:"position"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
}

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func toListResponse(l *List, items []*Item) listResponse {
	resp := listResponse{
		ID: l.ID.String(), HouseholdID: l.HouseholdID.String(), Name: l.Name, Kind: l.Kind,
		EntityType: l.EntityType, EntityID: l.EntityID.String(),
		IsTemplate: l.IsTemplate, Archived: l.Archived,
		ItemCount: l.ItemCount, OpenCount: l.OpenCount,
		CreatedAt: fmtTime(l.CreatedAt), UpdatedAt: fmtTime(l.UpdatedAt),
	}
	if items != nil {
		ir := toItemResponses(items)
		resp.Items = &ir
	}
	return resp
}

func toItemResponse(it *Item) itemResponse {
	resp := itemResponse{
		ID: it.ID.String(), ListID: it.ListID.String(), Text: it.Text,
		Quantity: it.Quantity, Unit: it.Unit, Section: it.Section,
		Checked: it.Checked, DueDate: it.DueDate, Position: it.Position,
		CreatedAt: fmtTime(it.CreatedAt), UpdatedAt: fmtTime(it.UpdatedAt),
	}
	if it.CheckedAt != nil {
		s := fmtTime(*it.CheckedAt)
		resp.CheckedAt = &s
	}
	if it.CheckedBy != nil {
		s := it.CheckedBy.String()
		resp.CheckedBy = &s
		resp.CheckedByName = it.CheckedByName
	}
	if it.AssigneePersonID != nil {
		s := it.AssigneePersonID.String()
		resp.AssigneePersonID = &s
	}
	return resp
}

func toItemResponses(items []*Item) []itemResponse {
	out := make([]itemResponse, 0, len(items))
	for _, it := range items {
		out = append(out, toItemResponse(it))
	}
	return out
}

// --- helpers ---

// authorize returns the request's household after the role policy allows
// action. It writes the error response and returns false otherwise.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, action Action) (uuid.UUID, bool) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing household context")
		return uuid.Nil, false
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return uuid.Nil, false
	}
	if !h.policy(claims.Role, action) {
		apierr.Forbidden(w, "your role cannot do this")
		return uuid.Nil, false
	}
	return hid, true
}

// pathID parses a UUID path parameter. A malformed id is a 404: it can't
// name a row of this family.
func pathID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		apierr.NotFound(w, "list not found")
		return uuid.Nil, false
	}
	return id, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return false
	}
	return true
}

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apierr.ErrNotFound):
		apierr.NotFound(w, "list or item not found")
	case errors.Is(err, ErrArchived):
		apierr.Conflict(w, "list is archived; unarchive it to change its items")
	case errors.Is(err, apierr.ErrBadRequest):
		apierr.BadRequest(w, "invalid request")
	default:
		apierr.DB(w, err)
	}
}

func parseOptUUID(s *string) (*uuid.UUID, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func checkLen(s *string, max int) bool {
	return s == nil || utf8.RuneCountInString(*s) <= max
}

func validDate(s *string) bool {
	if s == nil {
		return true
	}
	_, err := time.Parse("2006-01-02", *s)
	return err == nil
}

// toNewItem validates one item of a request. It returns a message for a 400.
func toNewItem(in itemRequest) (NewItem, string) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return NewItem{}, "item text is required"
	}
	if utf8.RuneCountInString(text) > maxTextLen {
		return NewItem{}, "item text is too long"
	}
	if in.Quantity != nil && *in.Quantity <= 0 {
		return NewItem{}, "quantity must be positive"
	}
	if !checkLen(in.Unit, maxUnitLen) || !checkLen(in.Section, maxSectionLen) {
		return NewItem{}, "unit or section is too long"
	}
	if !validDate(in.DueDate) {
		return NewItem{}, "due_date must be YYYY-MM-DD"
	}
	assignee, err := parseOptUUID(in.AssigneePersonID)
	if err != nil {
		return NewItem{}, "assignee_person_id must be a UUID"
	}
	return NewItem{
		Text: text, Quantity: in.Quantity, Unit: in.Unit, Section: in.Section,
		AssigneePersonID: assignee, DueDate: in.DueDate, Checked: in.Checked,
	}, ""
}

func toNewItems(w http.ResponseWriter, in []itemRequest) ([]NewItem, bool) {
	if len(in) > maxBatch {
		apierr.BadRequest(w, "too many items in one request")
		return nil, false
	}
	out := make([]NewItem, 0, len(in))
	for _, it := range in {
		ni, msg := toNewItem(it)
		if msg != "" {
			apierr.BadRequest(w, msg)
			return nil, false
		}
		out = append(out, ni)
	}
	return out, true
}

// parseIDs validates a batch of item ids, dropping duplicates.
func parseIDs(w http.ResponseWriter, in []string) ([]uuid.UUID, bool) {
	if len(in) == 0 {
		apierr.BadRequest(w, "item_ids is required")
		return nil, false
	}
	if len(in) > maxBatch {
		apierr.BadRequest(w, "too many items in one request")
		return nil, false
	}
	seen := make(map[uuid.UUID]bool, len(in))
	out := make([]uuid.UUID, 0, len(in))
	for _, s := range in {
		id, err := uuid.Parse(s)
		if err != nil {
			// A malformed id can't be an item of this list.
			apierr.NotFound(w, "list or item not found")
			return nil, false
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, true
}

// --- list handlers ---

// List returns the household's lists.
// GET /api/v1/lists?kind=&archived=&is_template=&entity_type=&entity_id=
// archived defaults to false; archived=all returns both.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	hid, ok := h.authorize(w, r, ActionRead)
	if !ok {
		return
	}
	q := r.URL.Query()
	var f ListFilter
	if k := q.Get("kind"); k != "" {
		if !ValidKind(k) {
			apierr.BadRequest(w, "invalid kind")
			return
		}
		f.Kind = k
	}
	switch q.Get("archived") {
	case "", "false":
		no := false
		f.Archived = &no
	case "true":
		yes := true
		f.Archived = &yes
	case "all":
	default:
		apierr.BadRequest(w, "archived must be true, false or all")
		return
	}
	switch q.Get("is_template") {
	case "":
	case "true", "false":
		v := q.Get("is_template") == "true"
		f.IsTemplate = &v
	default:
		apierr.BadRequest(w, "is_template must be true or false")
		return
	}
	f.EntityType = q.Get("entity_type")
	if s := q.Get("entity_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			apierr.BadRequest(w, "entity_id must be a UUID")
			return
		}
		f.EntityID = &id
	}
	lists, err := h.repo.List(r.Context(), hid, f)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	out := make([]listResponse, 0, len(lists))
	for _, l := range lists {
		out = append(out, toListResponse(l, nil))
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": out})
}

// Create creates a list, optionally with items and/or copied from another
// list of the family (template_id: its items are copied unchecked after any
// given items; empty name/kind default to the source's).
// POST /api/v1/lists
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	hid, ok := h.authorize(w, r, ActionManage)
	if !ok {
		return
	}
	var req createRequest
	if !decodeBody(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	tmpl, err := parseOptUUID(req.TemplateID)
	if err != nil {
		apierr.NotFound(w, "template not found")
		return
	}
	if req.Name == "" && tmpl == nil {
		apierr.BadRequest(w, "name is required")
		return
	}
	if utf8.RuneCountInString(req.Name) > maxNameLen {
		apierr.BadRequest(w, "name is too long")
		return
	}
	if req.Kind != "" && !ValidKind(req.Kind) {
		apierr.BadRequest(w, "invalid kind")
		return
	}
	entityID, err := parseOptUUID(req.EntityID)
	if err != nil {
		apierr.BadRequest(w, "entity_id must be a UUID")
		return
	}
	in := NewList{Name: req.Name, Kind: req.Kind, IsTemplate: req.IsTemplate}
	switch {
	case req.EntityType == nil && entityID == nil:
		// The family itself (046 lists_default_entity).
	case req.EntityType != nil && *req.EntityType == "household" && entityID == nil:
		in.EntityType, in.EntityID = req.EntityType, &hid
	case req.EntityType != nil && entityID != nil:
		in.EntityType, in.EntityID = req.EntityType, entityID
	default:
		apierr.BadRequest(w, "entity_type and entity_id go together")
		return
	}
	items, ok := toNewItems(w, req.Items)
	if !ok {
		return
	}
	in.Items = items

	l, err := h.repo.Create(r.Context(), hid, in, tmpl)
	if err != nil {
		writeErr(w, err)
		return
	}
	its, err := h.repo.Items(r.Context(), hid, l.ID, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	apierr.JSON(w, http.StatusCreated, map[string]any{"data": toListResponse(l, its)})
}

// Get returns a list with all its items in order.
// GET /api/v1/lists/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	hid, ok := h.authorize(w, r, ActionRead)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	l, err := h.repo.Get(r.Context(), hid, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	its, err := h.repo.Items(r.Context(), hid, id, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toListResponse(l, its)})
}

// Update changes a list's name, kind, subject, template flag or archived flag.
// PATCH /api/v1/lists/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	hid, ok := h.authorize(w, r, ActionManage)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req updateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	var p ListPatch
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || utf8.RuneCountInString(n) > maxNameLen {
			apierr.BadRequest(w, "name must be 1-255 characters")
			return
		}
		p.Name = &n
	}
	if req.Kind != nil {
		if !ValidKind(*req.Kind) {
			apierr.BadRequest(w, "invalid kind")
			return
		}
		p.Kind = req.Kind
	}
	entityID, err := parseOptUUID(req.EntityID)
	if err != nil {
		apierr.BadRequest(w, "entity_id must be a UUID")
		return
	}
	if req.EntityType != nil && *req.EntityType != "household" && entityID == nil {
		apierr.BadRequest(w, "entity_type and entity_id go together")
		return
	}
	p.EntityType, p.EntityID = req.EntityType, entityID
	p.IsTemplate, p.Archived = req.IsTemplate, req.Archived
	h.update(w, r, hid, id, p)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request, hid, id uuid.UUID, p ListPatch) {
	l, err := h.repo.Update(r.Context(), hid, id, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toListResponse(l, nil)})
}

func (h *Handler) setArchived(w http.ResponseWriter, r *http.Request, archived bool) {
	hid, ok := h.authorize(w, r, ActionManage)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	h.update(w, r, hid, id, ListPatch{Archived: &archived})
}

// Archive hides a list from the default listing and freezes its items.
// POST /api/v1/lists/{id}/archive
func (h *Handler) Archive(w http.ResponseWriter, r *http.Request) { h.setArchived(w, r, true) }

// Unarchive restores an archived list.
// POST /api/v1/lists/{id}/unarchive
func (h *Handler) Unarchive(w http.ResponseWriter, r *http.Request) { h.setArchived(w, r, false) }

// Delete removes a list and its items.
// DELETE /api/v1/lists/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	hid, ok := h.authorize(w, r, ActionManage)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.repo.Delete(r.Context(), hid, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- item handlers ---

// Items returns a list's items. With ?since=<RFC3339>, only items changed
// after that time. The response's as_of is the value to pass as since next
// time (it overlaps a little, so merge by id). Deleted items do not appear
// in a since feed; refetch without since to drop them.
// GET /api/v1/lists/{id}/items
func (h *Handler) Items(w http.ResponseWriter, r *http.Request) {
	hid, ok := h.authorize(w, r, ActionRead)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var since *time.Time
	if s := r.URL.Query().Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			apierr.BadRequest(w, "since must be an RFC 3339 time")
			return
		}
		since = &t
	}
	asOf := h.now().Add(-feedOverlap)
	its, err := h.repo.Items(r.Context(), hid, id, since)
	if err != nil {
		writeErr(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{
		"data":  toItemResponses(its),
		"as_of": fmtTime(asOf),
	})
}

// AddItems appends items to a list, all or none.
// POST /api/v1/lists/{id}/items  {"items": [{"text": "milk", ...}]}
func (h *Handler) AddItems(w http.ResponseWriter, r *http.Request) {
	hid, ok := h.authorize(w, r, ActionAddItems)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		Items []itemRequest `json:"items"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Items) == 0 {
		apierr.BadRequest(w, "items is required")
		return
	}
	items, ok := toNewItems(w, req.Items)
	if !ok {
		return
	}
	its, err := h.repo.AddItems(r.Context(), hid, id, items)
	if err != nil {
		writeErr(w, err)
		return
	}
	apierr.JSON(w, http.StatusCreated, map[string]any{"data": toItemResponses(its)})
}

func (h *Handler) setChecked(w http.ResponseWriter, r *http.Request, checked bool) {
	hid, ok := h.authorize(w, r, ActionCheckItems)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req idsRequest
	if !decodeBody(w, r, &req) {
		return
	}
	ids, ok := parseIDs(w, req.ItemIDs)
	if !ok {
		return
	}
	its, err := h.repo.SetChecked(r.Context(), hid, id, ids, checked)
	if err != nil {
		writeErr(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toItemResponses(its)})
}

// CheckItems checks off items, all or none.
// POST /api/v1/lists/{id}/items/check  {"item_ids": [...]}
func (h *Handler) CheckItems(w http.ResponseWriter, r *http.Request) { h.setChecked(w, r, true) }

// UncheckItems unchecks items, all or none.
// POST /api/v1/lists/{id}/items/uncheck  {"item_ids": [...]}
func (h *Handler) UncheckItems(w http.ResponseWriter, r *http.Request) { h.setChecked(w, r, false) }

// RemoveItems deletes items, all or none.
// POST /api/v1/lists/{id}/items/remove  {"item_ids": [...]}
func (h *Handler) RemoveItems(w http.ResponseWriter, r *http.Request) {
	hid, ok := h.authorize(w, r, ActionEditItems)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req idsRequest
	if !decodeBody(w, r, &req) {
		return
	}
	ids, ok := parseIDs(w, req.ItemIDs)
	if !ok {
		return
	}
	if err := h.repo.RemoveItems(r.Context(), hid, id, ids); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteItem deletes one item.
// DELETE /api/v1/lists/{id}/items/{itemId}
func (h *Handler) DeleteItem(w http.ResponseWriter, r *http.Request) {
	hid, ok := h.authorize(w, r, ActionEditItems)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	itemID, ok := pathID(w, r, "itemId")
	if !ok {
		return
	}
	if err := h.repo.RemoveItems(r.Context(), hid, id, []uuid.UUID{itemID}); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpdateItem edits one item. A body with only "checked" needs the
// check_items permission; anything else needs edit_items. Nullable fields
// sent as null are cleared.
// PATCH /api/v1/lists/{id}/items/{itemId}
func (h *Handler) UpdateItem(w http.ResponseWriter, r *http.Request) {
	var req itemPatchRequest
	if !decodeBody(w, r, &req) {
		return
	}
	action := ActionCheckItems
	if req.Checked == nil && req.Text == nil && !req.Quantity.Set && !req.Unit.Set &&
		!req.Section.Set && !req.AssigneePersonID.Set && !req.DueDate.Set {
		apierr.BadRequest(w, "nothing to update")
		return
	}
	if req.Text != nil || req.Quantity.Set || req.Unit.Set || req.Section.Set ||
		req.AssigneePersonID.Set || req.DueDate.Set {
		action = ActionEditItems
	}
	hid, ok := h.authorize(w, r, action)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	itemID, ok := pathID(w, r, "itemId")
	if !ok {
		return
	}
	p := ItemPatch{
		Quantity: req.Quantity.Val, QuantitySet: req.Quantity.Set,
		Unit: req.Unit.Val, UnitSet: req.Unit.Set,
		Section: req.Section.Val, SectionSet: req.Section.Set,
		DueDate: req.DueDate.Val, DueDateSet: req.DueDate.Set,
		AssigneePersonIDSet: req.AssigneePersonID.Set,
		Checked:             req.Checked,
	}
	if req.Text != nil {
		t := strings.TrimSpace(*req.Text)
		if t == "" || utf8.RuneCountInString(t) > maxTextLen {
			apierr.BadRequest(w, "item text must be 1-2000 characters")
			return
		}
		p.Text = &t
	}
	if p.Quantity != nil && *p.Quantity <= 0 {
		apierr.BadRequest(w, "quantity must be positive")
		return
	}
	if !checkLen(p.Unit, maxUnitLen) || !checkLen(p.Section, maxSectionLen) {
		apierr.BadRequest(w, "unit or section is too long")
		return
	}
	if !validDate(p.DueDate) {
		apierr.BadRequest(w, "due_date must be YYYY-MM-DD")
		return
	}
	assignee, err := parseOptUUID(req.AssigneePersonID.Val)
	if err != nil {
		apierr.BadRequest(w, "assignee_person_id must be a UUID")
		return
	}
	p.AssigneePersonID = assignee
	it, err := h.repo.UpdateItem(r.Context(), hid, id, itemID, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toItemResponse(it)})
}

// MoveItem moves an item to just after after_id, or to the top when
// after_id is absent or null. Only the moved row is rewritten.
// POST /api/v1/lists/{id}/items/{itemId}/move  {"after_id": "..."}
func (h *Handler) MoveItem(w http.ResponseWriter, r *http.Request) {
	hid, ok := h.authorize(w, r, ActionEditItems)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	itemID, ok := pathID(w, r, "itemId")
	if !ok {
		return
	}
	var req moveRequest
	if !decodeBody(w, r, &req) {
		return
	}
	afterID, err := parseOptUUID(req.AfterID)
	if err != nil {
		apierr.NotFound(w, "list or item not found")
		return
	}
	if afterID != nil && *afterID == itemID {
		apierr.BadRequest(w, "an item cannot move after itself")
		return
	}
	it, err := h.repo.MoveItem(r.Context(), hid, id, itemID, afterID)
	if err != nil {
		writeErr(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toItemResponse(it)})
}
