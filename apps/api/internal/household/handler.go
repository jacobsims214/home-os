package household

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	_ "time/tzdata" // embed the IANA database so timezone validation never depends on the image
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

type Handler struct {
	repo     *Repo
	verifier *auth.Verifier
}

func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

// WithVerifier lets Create verify the caller's Dex token itself, so the route
// can be mounted outside RequireAuth (which answers 403 for non-members).
func (h *Handler) WithVerifier(v *auth.Verifier) *Handler {
	h.verifier = v
	return h
}

// Column limits from households (migration 001).
const (
	maxHouseholdName = 255
	maxTimezone      = 100
)

// Create POST /api/v1/households — onboarding: create a family.
//
// The caller is usually a brand-new Dex user with no Home OS membership (and
// often no users row), so this route is registered OUTSIDE the RequireAuth
// group, like /api/v1/invites/accept. The handler verifies the Dex bearer
// token itself (signature, issuer, audience, expiry; no membership needed).
// Allowed: callers with no membership, and instance admins
// (users.is_instance_admin), who may create any number of families.
//
// Body: {"name": "Sims Family", "timezone": "America/Chicago"}. timezone must
// be an IANA zone name.
//
// In one transaction it finds or creates the user, inserts the household and
// the owner membership, and creates the Family/Bills/Maintenance calendars.
//
// Responses: 201 with the household and role; 400 bad body, name or timezone;
// 401 missing or invalid token; 403 deactivated user; 409 caller already
// belongs to a household and is not an instance admin.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	ident, ok := h.creatorIdentity(w, r)
	if !ok {
		return
	}

	var req struct {
		Name     string `json:"name"`
		Timezone string `json:"timezone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}
	if utf8.RuneCountInString(name) > maxHouseholdName {
		apierr.BadRequest(w, "name must be at most 255 characters")
		return
	}
	tz, err := validateTimezone(req.Timezone)
	if err != nil {
		apierr.BadRequest(w, err.Error())
		return
	}

	res, err := h.repo.CreateFamily(r.Context(), ident, name, tz)
	switch {
	case err == nil:
	case errors.Is(err, ErrAlreadyMember):
		apierr.Conflict(w, "this account already belongs to a household")
		return
	case errors.Is(err, ErrUserInactive):
		apierr.Forbidden(w, "this account is deactivated")
		return
	case errors.Is(err, ErrNoIdentity):
		apierr.Unauthorized(w, "token carries no subject or email")
		return
	default:
		apierr.InternalError(w, err)
		return
	}

	hh := res.Household
	slog.Info("household: created", "household_id", hh.ID, "user_id", res.UserID,
		"user_created", res.UserCreated, "timezone", hh.Timezone)
	apierr.JSON(w, http.StatusCreated, map[string]any{"data": map[string]any{
		"id":         hh.ID,
		"name":       hh.Name,
		"timezone":   hh.Timezone,
		"created_at": hh.CreatedAt,
		"user_id":    res.UserID,
		"role":       res.Role,
	}})
}

// validateTimezone returns the trimmed IANA zone name, or an error suitable
// for a 400. "Local" and "" are rejected: time.LoadLocation accepts them but
// they are not zone names.
func validateTimezone(raw string) (string, error) {
	tz := strings.TrimSpace(raw)
	if tz == "" {
		return "", errors.New("timezone is required")
	}
	if len(tz) > maxTimezone || tz == "Local" {
		return "", errors.New("timezone must be an IANA time zone name, e.g. America/Chicago")
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", errors.New("timezone must be an IANA time zone name, e.g. America/Chicago")
	}
	return tz, nil
}

// creatorIdentity returns the verified Dex identity of the caller, writing a
// 401 and returning false when the token is missing or invalid.
func (h *Handler) creatorIdentity(w http.ResponseWriter, r *http.Request) (CreatorIdentity, bool) {
	if h.verifier == nil {
		// Mounted behind RequireAuth (no verifier wired): fall back to claims.
		if c := middleware.ClaimsFromContext(r.Context()); c != nil {
			return CreatorIdentity{Email: c.Email, Name: c.Name}, true
		}
		apierr.Unauthorized(w, "not authenticated")
		return CreatorIdentity{}, false
	}
	if !h.verifier.Enabled() {
		// Local dev mode: Verify returns the anonymous local-dev identity.
		id, _ := h.verifier.Verify(r.Context(), "")
		return CreatorIdentity{Subject: id.Subject, Email: id.Email, Name: id.Name}, true
	}

	parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		apierr.Unauthorized(w, "missing or invalid authorization header")
		return CreatorIdentity{}, false
	}
	id, err := h.verifier.Verify(r.Context(), strings.TrimSpace(parts[1]))
	if err != nil {
		slog.Warn("household create: token verification failed", "error", err)
		apierr.Unauthorized(w, "invalid or expired token")
		return CreatorIdentity{}, false
	}
	return CreatorIdentity{Subject: id.Subject, Email: id.Email, Name: id.Name}, true
}

// GetMe GET /api/v1/households/me — returns the current user's household.
func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	hh, err := h.repo.GetHousehold(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if hh == nil {
		apierr.NotFound(w, "household not found")
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": hh})
}

// ListMembers GET /api/v1/households/me/members
func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	members, err := h.repo.ListMembers(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": members})
}

// UpdateMemberRole PATCH /api/v1/households/me/members/{userId}
func (h *Handler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userId"))
	if err != nil {
		apierr.BadRequest(w, "invalid user id")
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid body")
		return
	}
	if req.Role == "" {
		apierr.BadRequest(w, "role is required")
		return
	}
	if err := h.repo.UpdateMemberRole(r.Context(), hid, userID, req.Role); err != nil {
		apierr.InternalError(w, err)
		return
	}
	members, err := h.repo.ListMembers(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": members})
}

// RemoveMember DELETE /api/v1/households/me/members/{userId}
func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userId"))
	if err != nil {
		apierr.BadRequest(w, "invalid user id")
		return
	}
	if err := h.repo.RemoveMember(r.Context(), hid, userID); err != nil {
		apierr.InternalError(w, err)
		return
	}
	members, err := h.repo.ListMembers(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": members})
}
