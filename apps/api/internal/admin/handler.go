// Package admin holds the instance-admin endpoints: listing families and
// users across the whole instance, creating users (with a Dex password) into
// an existing or new family, and deactivating/reactivating users.
//
// Only users with users.is_instance_admin = true and users.active = true get
// through RequireInstanceAdmin; the flag is read from the database on every
// request, never from the token.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // timezone validation must not depend on the image's zoneinfo
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/auth"
	"home-os/api/internal/db"
	"home-os/api/pkg/apierr"
)

// PasswordStore creates a password entry in Dex's local password database.
// *dex.Client implements it. The hash is bcrypt; Dex does not hash.
type PasswordStore interface {
	CreatePassword(ctx context.Context, email, hash, userID string) error
}

// Handler serves the /api/v1/admin endpoints.
type Handler struct {
	repo     *Repo
	verifier *auth.Verifier
	dex      PasswordStore
}

// NewHandler returns an admin Handler. verifier checks the caller's Dex token.
func NewHandler(repo *Repo, verifier *auth.Verifier) *Handler {
	return &Handler{repo: repo, verifier: verifier}
}

// WithDex wires Dex so CreateUser can register the new user's password.
// Without it CreateUser answers 503: the user could never sign in.
func (h *Handler) WithDex(d PasswordStore) *Handler {
	h.dex = d
	return h
}

// RegisterRoutes mounts the admin endpoints on r, behind RequireInstanceAdmin.
// Mount them OUTSIDE middleware.RequireAuth: an instance admin need not be a
// member of any household, and RequireAuth answers 403 for non-members.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.RequireInstanceAdmin)
		r.Get("/api/v1/admin/families", h.ListFamilies)
		r.Get("/api/v1/admin/users", h.ListUsers)
		r.Post("/api/v1/admin/users", h.CreateUser)
		r.Post("/api/v1/admin/users/{id}/deactivate", h.Deactivate)
		r.Post("/api/v1/admin/users/{id}/reactivate", h.Reactivate)
	})
}

type callerCtxKey struct{}

func callerFrom(ctx context.Context) *Caller {
	c, _ := ctx.Value(callerCtxKey{}).(*Caller)
	return c
}

// RequireInstanceAdmin verifies the Dex bearer token (signature, issuer,
// audience, expiry), finds the Home OS user by dex_sub then email, and lets
// the request through only when that user is active and an instance admin.
//
//   - missing or invalid token: 401
//   - unknown user, deactivated user, or not an instance admin: 403
//   - database failure: 500
//
// In auth-disabled mode the verifier yields the local-dev identity, which is
// an admin only if a matching user row has the flag.
func (h *Handler) RequireInstanceAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw string
		if h.verifier.Enabled() {
			tok, errMsg := auth.BearerToken(r)
			if errMsg != "" {
				auth.WriteUnauthorized(w, errMsg)
				return
			}
			raw = tok
		}
		ident, err := h.verifier.Verify(r.Context(), raw)
		if err != nil {
			slog.Warn("admin: token verification failed", "error", err)
			auth.WriteUnauthorized(w, "invalid or expired token")
			return
		}
		c, err := h.repo.LookupCaller(r.Context(), ident.Subject, ident.Email)
		if err != nil {
			apierr.InternalError(w, err)
			return
		}
		if c != nil && !c.Active {
			// Same 403 wording as auth.WriteVerifyError, so a deactivated
			// admin learns why, not just that admin access is missing.
			slog.Warn("admin: refused deactivated caller", "user_id", c.ID)
			apierr.Forbidden(w, "this account has been deactivated")
			return
		}
		if c == nil || !c.IsInstanceAdmin {
			slog.Warn("admin: refused non-admin caller", "email", ident.Email)
			apierr.Forbidden(w, "instance admin access required")
			return
		}
		// The admin is the database actor for what the route writes
		// (050 authorship). No household: admin routes act instance-wide.
		ctx := db.WithActor(context.WithValue(r.Context(), callerCtxKey{}, c), db.Actor{UserID: c.ID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ListFamilies GET /api/v1/admin/families: every household with its member
// count. 200 {"data": [{id, name, timezone, member_count, created_at}]}.
func (h *Handler) ListFamilies(w http.ResponseWriter, r *http.Request) {
	fams, err := h.repo.ListFamilies(r.Context())
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": fams})
}

// ListUsers GET /api/v1/admin/users: every user with active, is_instance_admin
// and memberships. Never includes password or CalDAV hashes.
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.repo.ListUsers(r.Context())
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": users})
}

// Validation limits.
const (
	maxName          = 100 // runes, as invite signup
	maxEmail         = 255 // users.email VARCHAR(255)
	minPassword      = 8   // bytes
	maxPassword      = 72  // bytes: bcrypt's limit; longer is refused, not truncated
	maxHouseholdName = 255 // runes, households.name
	maxTimezone      = 100 // households.timezone
)

var validRoles = []string{
	auth.RoleOwner, auth.RoleFamilyManager, auth.RoleFamilyMember,
	auth.RoleViewer, auth.RoleHousesitter, auth.RoleVendor,
}

type createUserRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"` // temporary password, registered with Dex
	Role     string `json:"role"`
	// Exactly one of household_id or household_name.
	HouseholdID   string `json:"household_id"`
	HouseholdName string `json:"household_name"`
	Timezone      string `json:"timezone"` // new family only; default UTC
}

// CreateUser POST /api/v1/admin/users: creates a user with a temporary
// password into an existing family (household_id) or a new one
// (household_name, optional timezone). role defaults to family_member when
// joining an existing family; a new family's first member is always its owner.
//
// The password is bcrypt-hashed before the transaction; the user, membership
// (and new family with its default calendars) are written and the Dex
// password created in one transaction, so a Dex failure writes nothing.
//
// Responses: 201 {"data": {user_id, email, name, household_id,
// household_name, role, household_created}}; 400 validation; 404 unknown
// household_id; 409 email already registered; 502 Dex failed; 503 Dex not
// configured.
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	in, msg := validateCreate(req)
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	if h.dex == nil {
		writeError(w, http.StatusServiceUnavailable, "DEX_UNAVAILABLE",
			"password sign-in is not configured; the user could not sign in")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	in.PasswordHash = hash

	var dexErr error
	res, err := h.repo.CreateUser(r.Context(), in, func(ctx context.Context, email, userID string) error {
		dexErr = h.dex.CreatePassword(ctx, email, hash, userID)
		return dexErr
	})
	switch {
	case err == nil:
	case errors.Is(err, ErrFamilyNotFound):
		apierr.NotFound(w, "family not found")
		return
	case errors.Is(err, ErrEmailTaken):
		apierr.Conflict(w, "a user with this email already exists")
		return
	case dexErr != nil:
		slog.Error("admin: dex password creation failed; nothing written", "email", in.Email, "error", dexErr)
		writeError(w, http.StatusBadGateway, "DEX_ERROR", "could not register the password with the identity provider")
		return
	default:
		apierr.InternalError(w, err)
		return
	}

	slog.Info("admin: created user", "user_id", res.UserID, "household_id", res.HouseholdID,
		"role", res.Role, "household_created", res.HouseholdCreated, "by", callerID(r))
	apierr.JSON(w, http.StatusCreated, map[string]any{"data": map[string]any{
		"user_id":           res.UserID,
		"email":             res.Email,
		"name":              res.Name,
		"household_id":      res.HouseholdID,
		"household_name":    res.HouseholdName,
		"role":              res.Role,
		"household_created": res.HouseholdCreated,
	}})
}

// validateCreate trims and checks req. On failure it returns a 400 message.
func validateCreate(req createUserRequest) (CreateUserInput, string) {
	in := CreateUserInput{
		Name:  strings.TrimSpace(req.Name),
		Email: strings.TrimSpace(req.Email),
		Role:  strings.TrimSpace(req.Role),
	}
	switch {
	case in.Name == "":
		return in, "name is required"
	case utf8.RuneCountInString(in.Name) > maxName:
		return in, "name must be at most 100 characters"
	case in.Email == "":
		return in, "email is required"
	case len(in.Email) > maxEmail || strings.Count(in.Email, "@") != 1 ||
		strings.HasPrefix(in.Email, "@") || strings.HasSuffix(in.Email, "@") ||
		strings.ContainsAny(in.Email, " \t\r\n"):
		return in, "email must be a valid email address"
	case len(req.Password) < minPassword:
		return in, "password must be at least 8 characters"
	case len(req.Password) > maxPassword:
		return in, "password must be at most 72 bytes"
	}

	hhID := strings.TrimSpace(req.HouseholdID)
	hhName := strings.TrimSpace(req.HouseholdName)
	switch {
	case hhID != "" && hhName != "":
		return in, "give household_id (existing family) or household_name (new family), not both"
	case hhID != "":
		id, err := uuid.Parse(hhID)
		if err != nil {
			return in, "household_id must be a UUID"
		}
		in.HouseholdID = &id
		if in.Role == "" {
			in.Role = auth.RoleFamilyMember
		}
	case hhName != "":
		if utf8.RuneCountInString(hhName) > maxHouseholdName {
			return in, "household_name must be at most 255 characters"
		}
		if in.Role == "" {
			in.Role = auth.RoleOwner
		}
		if in.Role != auth.RoleOwner {
			return in, "the first member of a new family must be its owner"
		}
		tz, msg := validateTimezone(req.Timezone)
		if msg != "" {
			return in, msg
		}
		in.NewHouseholdName = hhName
		in.NewTimezone = tz
	default:
		return in, "household_id (existing family) or household_name (new family) is required"
	}
	if !slices.Contains(validRoles, in.Role) {
		return in, "role must be one of owner, family_manager, family_member, viewer, housesitter, vendor"
	}
	return in, ""
}

// validateTimezone defaults an empty zone to UTC and otherwise requires an
// IANA zone name ("Local" is refused even though LoadLocation accepts it).
func validateTimezone(raw string) (string, string) {
	tz := strings.TrimSpace(raw)
	if tz == "" {
		return "UTC", ""
	}
	if len(tz) > maxTimezone || tz == "Local" {
		return "", "timezone must be an IANA time zone name, e.g. America/Chicago"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", "timezone must be an IANA time zone name, e.g. America/Chicago"
	}
	return tz, ""
}

// Deactivate POST /api/v1/admin/users/{id}/deactivate: sets users.active =
// false. Effective immediately for the API, CalDAV and the admin endpoints,
// which all read the flag per request. 200 {"data": user}; 400 bad id or
// self-deactivation; 404 unknown user; 409 last active instance admin.
func (h *Handler) Deactivate(w http.ResponseWriter, r *http.Request) {
	h.setActive(w, r, false)
}

// Reactivate POST /api/v1/admin/users/{id}/reactivate: sets users.active =
// true. 200 {"data": user}; 400 bad id; 404 unknown user.
func (h *Handler) Reactivate(w http.ResponseWriter, r *http.Request) {
	h.setActive(w, r, true)
}

func (h *Handler) setActive(w http.ResponseWriter, r *http.Request, active bool) {
	targetID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "user id must be a UUID")
		return
	}
	caller := callerFrom(r.Context())
	if caller == nil { // unreachable behind RequireInstanceAdmin
		apierr.Forbidden(w, "instance admin access required")
		return
	}

	u, err := h.repo.SetActive(r.Context(), caller.ID, targetID, active)
	switch {
	case err == nil:
	case errors.Is(err, ErrSelfDeactivation):
		apierr.BadRequest(w, "you cannot deactivate your own account")
		return
	case errors.Is(err, ErrLastAdmin):
		apierr.Conflict(w, "cannot deactivate the last active instance admin")
		return
	case errors.Is(err, ErrUserNotFound):
		apierr.NotFound(w, "user not found")
		return
	case errors.Is(err, ErrCallerNotAdmin):
		apierr.Forbidden(w, "instance admin access required")
		return
	default:
		apierr.InternalError(w, err)
		return
	}
	slog.Info("admin: set user active", "user_id", targetID, "active", active, "by", caller.ID)
	apierr.JSON(w, http.StatusOK, map[string]any{"data": u})
}

func callerID(r *http.Request) string {
	if c := callerFrom(r.Context()); c != nil {
		return c.ID.String()
	}
	return ""
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	apierr.JSON(w, status, apierr.ErrorResponse{Error: apierr.ErrorDetail{Code: code, Message: msg}})
}
