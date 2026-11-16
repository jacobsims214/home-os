package activity

import (
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

// kindRe matches activity_log entity_type / parent_type values ('bill',
// 'list_item', 'calendar_event', ...).
var kindRe = regexp.MustCompile(`^[a-z][a-z_]{0,49}$`)

// ReadPolicy decides, from the caller's role in the household, whether they
// may read the feed at all and which entity types are hidden from them. The
// default lets every member read everything. The roles policy (#4689) plugs
// in here, e.g. to keep a vendor role away from secrets or the whole feed.
type ReadPolicy func(role string) (allowed bool, hiddenEntityTypes []string)

// AllMembers is the default ReadPolicy: any member reads the whole feed.
func AllMembers(string) (bool, []string) { return true, nil }

// Handler serves GET /api/v1/activity, the family change feed.
//
// Access: any member of the household the request resolved to (so
// X-Household-ID is honoured), subject to the ReadPolicy. Responses are
// Cache-Control: no-store.
type Handler struct {
	repo   *Repo
	policy ReadPolicy
}

// NewHandler creates an activity handler with the AllMembers policy.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo, policy: AllMembers}
}

// WithReadPolicy replaces the read policy.
func (h *Handler) WithReadPolicy(p ReadPolicy) *Handler {
	if p != nil {
		h.policy = p
	}
	return h
}

// RegisterRoutes mounts the activity routes. The router must already run
// middleware.RequireAuth.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/activity", h.List)
}

type actorResponse struct {
	UserID   string  `json:"user_id"`
	PersonID *string `json:"person_id"`
	Name     *string `json:"name"`
}

type agentResponse struct {
	TokenID string  `json:"token_id"`
	Name    *string `json:"name"`
}

type entryResponse struct {
	ID            int64          `json:"id"`
	Action        string         `json:"action"`
	EntityType    string         `json:"entity_type"`
	EntityID      string         `json:"entity_id"`
	ParentType    *string        `json:"parent_type"`
	ParentID      *string        `json:"parent_id"`
	ChangedFields []string       `json:"changed_fields"`
	Summary       string         `json:"summary"`
	CreatedAt     time.Time      `json:"created_at"`
	Actor         *actorResponse `json:"actor"`
	ViaAgent      bool           `json:"via_agent"`
	Agent         *agentResponse `json:"agent"`
	// Description is a ready-to-show line: `Dana updated bill "Netflix"
	// (amount) via agent "Claude"`.
	Description string `json:"description"`
}

func uuidPtr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func toResponse(e Entry) entryResponse {
	out := entryResponse{
		ID:            e.ID,
		Action:        e.Action,
		EntityType:    e.EntityType,
		EntityID:      e.EntityID.String(),
		ParentType:    e.ParentType,
		ParentID:      uuidPtr(e.ParentID),
		ChangedFields: e.ChangedFields,
		Summary:       e.Summary,
		CreatedAt:     e.CreatedAt,
		ViaAgent:      e.AgentTokenID != nil,
	}
	if out.ChangedFields == nil {
		out.ChangedFields = []string{}
	}
	who := "Someone"
	if e.ActorUserID != nil {
		out.Actor = &actorResponse{UserID: e.ActorUserID.String(), PersonID: uuidPtr(e.ActorPersonID), Name: e.ActorName}
		if e.ActorName != nil && *e.ActorName != "" {
			who = *e.ActorName
		}
	} else if e.AgentTokenID == nil {
		who = "System"
	}
	out.Description = who + " " + e.Summary
	if e.AgentTokenID != nil {
		out.Agent = &agentResponse{TokenID: e.AgentTokenID.String(), Name: e.AgentName}
		if e.AgentName != nil && *e.AgentName != "" {
			out.Description += ` via agent "` + *e.AgentName + `"`
		} else {
			out.Description += " via agent"
		}
	}
	return out
}

// List handles GET /api/v1/activity.
//
// Entries come newest first (id DESC; a poll in its own order, see
// Repo.List). Query parameters, all optional:
//   - limit: page size, 1..200 (default 50);
//   - cursor: the next_cursor of the previous page (entries older than it);
//   - after: the poll_after of the previous response; only entries that
//     became visible since. A poll that returns limit entries is cut: poll
//     again at once. Dedupe by id: an entry may come twice, never not at all;
//   - since: RFC 3339 time; only entries created at or after it;
//   - member: a user id or person id; only that member's changes;
//   - entity_type, entity_id: one kind of record, or one record;
//   - parent_type, parent_id: records inside one parent (a list's items).
//
// Response: {"data": [...], "next_cursor": "<id>" | null,
// "poll_after": "<id>" | null}. next_cursor is null for a poll; poll_after
// is null for an older page (cursor) and while the household has no entry.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing household context")
		return
	}
	hid, err1 := uuid.Parse(claims.HouseholdID)
	uid, err2 := uuid.Parse(claims.UserID)
	if err1 != nil || err2 != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}
	role, err := h.repo.MemberRole(r.Context(), hid, uid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if role == "" {
		apierr.Forbidden(w, "not a member of this household")
		return
	}
	allowed, hidden := h.policy(role)
	if !allowed {
		apierr.Forbidden(w, "your role can't read the activity feed")
		return
	}

	f, msg := parseFilter(r)
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	f.ExcludeEntityTypes = hidden

	pg, err := h.repo.List(r.Context(), hid, f)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	data := make([]entryResponse, 0, len(pg.Entries))
	for _, e := range pg.Entries {
		data = append(data, toResponse(e))
	}
	var next, pollAfter *string
	if pg.More && len(pg.Entries) > 0 && f.After == 0 {
		s := strconv.FormatInt(pg.Entries[len(pg.Entries)-1].ID, 10)
		next = &s
	}
	if pg.PollAfter > 0 {
		s := strconv.FormatInt(pg.PollAfter, 10)
		pollAfter = &s
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": data, "next_cursor": next, "poll_after": pollAfter})
}

// parseFilter reads the query parameters; msg is non-empty for a bad one.
func parseFilter(r *http.Request) (f Filter, msg string) {
	q := r.URL.Query()
	f.Limit = defaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLimit {
			return f, "limit must be between 1 and 200"
		}
		f.Limit = n
	}
	posID := func(name string) (int64, bool) {
		v := q.Get(name)
		if v == "" {
			return 0, true
		}
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil && n > 0
	}
	var ok bool
	if f.Before, ok = posID("cursor"); !ok {
		return f, "invalid cursor"
	}
	if f.After, ok = posID("after"); !ok {
		return f, "after must be a positive entry id"
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, "since must be an RFC 3339 time"
		}
		f.Since = t
	}
	optUUID := func(name string) (*uuid.UUID, bool) {
		v := q.Get(name)
		if v == "" {
			return nil, true
		}
		id, err := uuid.Parse(v)
		if err != nil {
			return nil, false
		}
		return &id, true
	}
	if f.Member, ok = optUUID("member"); !ok {
		return f, "member must be a user or person id"
	}
	if f.EntityID, ok = optUUID("entity_id"); !ok {
		return f, "invalid entity_id"
	}
	if f.ParentID, ok = optUUID("parent_id"); !ok {
		return f, "invalid parent_id"
	}
	f.EntityType = q.Get("entity_type")
	if f.EntityType != "" && !kindRe.MatchString(f.EntityType) {
		return f, "invalid entity_type"
	}
	f.ParentType = q.Get("parent_type")
	if f.ParentType != "" && !kindRe.MatchString(f.ParentType) {
		return f, "invalid parent_type"
	}
	return f, ""
}
