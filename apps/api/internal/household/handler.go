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
	// maxCreateBody caps the unauthenticated POST /api/v1/households body;
	// a real one ({"name","timezone"}) is a few hundred bytes.
	maxCreateBody = 16 << 10
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
// 413 body over 16 KiB;
// 401 missing or invalid token; 403 deactivated user; 409 caller already
// belongs to a household and is not an instance admin.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	// Cap the body first: this route is outside RequireAuth.
	r.Body = http.MaxBytesReader(w, r.Body, maxCreateBody)
	ident, ok := h.creatorIdentity(w, r)
	if !ok {
		return
	}

	var req struct {
		Name     string `json:"name"`
		Timezone string `json:"timezone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apierr.JSON(w, http.StatusRequestEntityTooLarge, apierr.ErrorResponse{Error: apierr.ErrorDetail{
				Code: "PAYLOAD_TOO_LARGE", Message: "request body too large",
			}})
			return
		}
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
		apierr.Forbidden(w, "this account has been deactivated")
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

// errBadTimezone is the one 400 message for a timezone that is not an IANA
// zone name. It names a real example so a client can correct itself.
var errBadTimezone = errors.New("timezone must be an IANA time zone name, e.g. America/Chicago")

// validateTimezone returns the trimmed IANA zone name, or an error suitable
// for a 400. It is used both when a family is created and when its timezone
// is changed (PATCH /api/v1/households/{id}).
//
// A wrong zone does not fail loudly downstream: every consumer that loads the
// stored name (the API, the MCP's date words in meal_set/agenda, the worker)
// falls back to UTC when LoadLocation fails, so a bad value silently shifts
// every date the family sees (#4513 war game P1 #6/#8). Hence all four checks:
//
//  1. not empty after trimming, and within households.timezone (VARCHAR(100));
//  2. LoadLocation accepts it, which rules out junk like "Mars/Olympus_Mons"
//     and path-like names ("../etc/passwd" is an invalid location name);
//  3. it is written the way the tz database writes it — every "/"-separated
//     segment starts with an uppercase ASCII letter or a digit, and segments
//     hold only [A-Za-z0-9_+-]. This rejects "america/chicago" and
//     "posixrules": Go's LoadLocation resolves those from the HOST zoneinfo
//     files (case-insensitive on macOS), so they pass on a developer machine
//     but fail on the case-sensitive filesystem of a Linux container, where
//     every consumer then silently uses UTC;
//  4. it is not one of the two names that load but are not family places:
//     "Local" (the SERVER's zone, which would follow the container) and
//     "Factory" (tzdata's fixed-UTC test placeholder).
func validateTimezone(raw string) (string, error) {
	tz := strings.TrimSpace(raw)
	if tz == "" {
		return "", errors.New("timezone is required")
	}
	if len(tz) > maxTimezone || tz == "Local" || tz == "Factory" {
		return "", errBadTimezone
	}
	if !canonicalZoneName(tz) {
		return "", errBadTimezone
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", errBadTimezone
	}
	return tz, nil
}

// canonicalZoneName reports whether name is written the way the IANA tz
// database writes a zone: "/"-separated segments that each start with an
// uppercase ASCII letter or a digit (America/Chicago, Etc/GMT+5, EST5EDT,
// US/Eastern) and contain only zone-name characters. It rejects the case
// variants a case-insensitive filesystem would otherwise resolve.
func canonicalZoneName(name string) bool {
	for _, seg := range strings.Split(name, "/") {
		if seg == "" {
			return false
		}
		if c := seg[0]; !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return false
		}
		for i := 0; i < len(seg); i++ {
			c := seg[i]
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			case c == '_' || c == '+' || c == '-':
			default:
				return false
			}
		}
	}
	return true
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

// ListMine GET /api/v1/me/households — the households the caller belongs to,
// with their role in each, oldest membership first. Registered inside
// RequireAuth. Each entry carries:
//   - is_default: the household used when a request has no X-Household-ID
//     (the oldest membership).
//   - current: the household this request resolved to (the X-Household-ID
//     header when sent, otherwise the default).
//
// A client switches family by sending X-Household-ID: <id> on later requests.
func (h *Handler) ListMine(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	uid, err := uuid.Parse(claims.UserID)
	if err != nil {
		// Local dev mode (no verifier) has no Home OS user behind it.
		apierr.JSON(w, http.StatusOK, map[string]any{"data": []any{}})
		return
	}
	list, err := h.repo.ListForUser(r.Context(), uid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	type entry struct {
		UserHousehold
		IsDefault bool `json:"is_default"`
		Current   bool `json:"current"`
	}
	data := make([]entry, 0, len(list))
	for i, hh := range list {
		data = append(data, entry{
			UserHousehold: hh,
			IsDefault:     i == 0,
			Current:       hh.ID == claims.HouseholdID,
		})
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": data})
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

// writeHouseholdError maps UpdateHousehold errors to responses.
func writeHouseholdError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotHouseholdManager):
		apierr.Forbidden(w, "only owners and family managers can change the household")
	case errors.Is(err, ErrHouseholdNotFound):
		apierr.NotFound(w, "household not found")
	default:
		apierr.InternalError(w, err)
	}
}

// Update PATCH /api/v1/households/{id} — rename a family and/or set its time
// zone. Body: {"name"?, "timezone"?}; at least one, and a field that is sent
// must be valid (a null or "" name/timezone is refused, not ignored, so a UI
// bug never silently drops the user's edit).
//
// Who may: owner and family_manager of that household only (the household
// role policy's household/write grant, decisions/2026-11-15-role-policy.md).
// family_member, viewer, housesitter and vendor answer 403; this is the
// conservative reading of the policy — renaming the family and, above all,
// changing its time zone changes what every date means for everybody, so it
// sits with the roles that manage members rather than with every writer.
// The role is read from memberships by the repo, inside the request, so a
// demotion applies at once and an instance admin gets no extra rights.
//
// {id} must be one of the caller's own households. Another family's id — or
// an unknown one — answers 404, the same as a household that does not exist,
// so ids cannot be probed. In practice a caller reaches this route with the
// id it got from GET /api/v1/me/households; the /me routes (households/me)
// remain the shorthand for "the household this request resolved to".
//
// timezone must be a real IANA zone name (see validateTimezone): a wrong zone
// silently shifts every date the family sees (#4513 war game P1 #6/#8), so
// junk is a 400 with nothing written.
//
// Responses: 200 {"data": {id, name, timezone, created_at, updated_at}};
// 400 bad body, {id} or timezone; 403 not a manager; 404 not one of the
// caller's households.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	// A malformed {id} can never name a household, so it is a 400 rather than
	// a 404: it leaks nothing, and it tells a client that guessed
	// "households/me" (a GET-only shorthand) what to send instead.
	householdID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "id must be a household UUID, from GET /api/v1/me/households")
		return
	}
	// A caller without a users.id (auth-disabled local dev) has no
	// membership, so it cannot be a manager of any household.
	actorID, err := uuid.Parse(claims.UserID)
	if err != nil {
		apierr.Forbidden(w, "only owners and family managers can change the household")
		return
	}

	// RawMessage keeps "field absent" (leave it unchanged) apart from "field
	// sent null" (refused): a null decodes into a *string as nil, which would
	// quietly drop the client's edit. Unknown keys are ignored, so a client
	// may PATCH back the whole household object it read; a body with only a
	// misspelt key still fails the "at least one field" check below.
	var req struct {
		Name     json.RawMessage `json:"name"`
		Timezone json.RawMessage `json:"timezone"`
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid body")
		return
	}
	if len(req.Name) == 0 && len(req.Timezone) == 0 {
		apierr.BadRequest(w, "name or timezone is required")
		return
	}
	upd := HouseholdUpdate{}
	if len(req.Name) > 0 {
		var raw string
		if string(req.Name) == "null" || json.Unmarshal(req.Name, &raw) != nil {
			apierr.BadRequest(w, "name must be a string")
			return
		}
		name := strings.TrimSpace(raw)
		if name == "" {
			apierr.BadRequest(w, "name is required")
			return
		}
		if utf8.RuneCountInString(name) > maxHouseholdName {
			apierr.BadRequest(w, "name must be at most 255 characters")
			return
		}
		upd.Name = &name
	}
	if len(req.Timezone) > 0 {
		var raw string
		if string(req.Timezone) == "null" || json.Unmarshal(req.Timezone, &raw) != nil {
			apierr.BadRequest(w, errBadTimezone.Error())
			return
		}
		tz, err := validateTimezone(raw)
		if err != nil {
			apierr.BadRequest(w, err.Error())
			return
		}
		upd.Timezone = &tz
	}

	hh, err := h.repo.UpdateHousehold(r.Context(), householdID, actorID, upd)
	if err != nil {
		writeHouseholdError(w, err)
		return
	}
	slog.Info("household: updated", "household_id", hh.ID, "user_id", actorID,
		"name_changed", upd.Name != nil, "timezone_changed", upd.Timezone != nil,
		"timezone", hh.Timezone)
	apierr.JSON(w, http.StatusOK, map[string]any{"data": householdResponse{
		ID:        hh.ID.String(),
		Name:      hh.Name,
		Timezone:  hh.Timezone,
		CreatedAt: hh.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: hh.UpdatedAt.UTC().Format(time.RFC3339),
	}})
}

// householdResponse is the body of PATCH /api/v1/households/{id}: lowercase
// keys like every other payload this API adds (UserHousehold, Member), so the
// Settings screen can read data.name and data.timezone directly. (GET
// /api/v1/households/me predates that convention and still serialises the
// Household struct's own field names.)
type householdResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Timezone  string `json:"timezone"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
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

// callerUserID parses the caller's users.id from the claims. A caller
// without one (auth-disabled local dev) cannot manage members: 403.
func callerUserID(w http.ResponseWriter, claims *auth.Claims) (uuid.UUID, bool) {
	id, err := uuid.Parse(claims.UserID)
	if err != nil {
		apierr.Forbidden(w, "only owners and family managers can manage members")
		return uuid.Nil, false
	}
	return id, true
}

// writeMemberError maps ChangeMemberRole / RemoveMember errors to responses.
func writeMemberError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotManager):
		apierr.Forbidden(w, "only owners and family managers can manage members, and only owners can manage owners")
	case errors.Is(err, ErrMemberNotFound):
		apierr.NotFound(w, "member not found")
	case errors.Is(err, ErrLastOwner):
		apierr.Conflict(w, "the household must keep at least one owner")
	case errors.Is(err, ErrInvalidRole):
		apierr.BadRequest(w, "invalid role")
	case errors.Is(err, ErrVendorLinkRole):
		apierr.BadRequest(w, "vendor_id is only allowed for the vendor role")
	case errors.Is(err, ErrVendorNotFound):
		apierr.NotFound(w, "vendor not found")
	default:
		apierr.InternalError(w, err)
	}
}

// UpdateMemberRole PATCH /api/v1/households/me/members/{userId}
//
// Only an owner or family_manager of the request's household (X-Household-ID
// aware) may change roles, judged from memberships inside the transaction. A
// family_manager may not grant owner or change an owner. The last owner may
// not be demoted. 403 not allowed; 404 not a member; 409 last owner; 400 bad
// role.
//
// Body {"role"?, "vendor_id"?} (#4799): at least one. vendor_id links a
// vendor member to the family's vendors row (only their own maintenance
// jobs are then visible); null clears it. 400 when the resulting role is not
// vendor, 404 for another family's vendor. Changing the role away from
// vendor clears the link.
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
		// VendorID: absent keeps the link, null or "" clears it, a vendors
		// id sets it (vendor members only).
		VendorID json.RawMessage `json:"vendor_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid body")
		return
	}
	upd := MemberUpdate{Role: req.Role}
	if len(req.VendorID) > 0 {
		upd.SetVendor = true
		var raw *string
		if err := json.Unmarshal(req.VendorID, &raw); err != nil {
			apierr.BadRequest(w, "vendor_id must be a string or null")
			return
		}
		if raw != nil && strings.TrimSpace(*raw) != "" {
			id, err := uuid.Parse(strings.TrimSpace(*raw))
			if err != nil {
				apierr.BadRequest(w, "invalid vendor_id")
				return
			}
			upd.VendorID = &id
		}
	}
	if req.Role == "" && !upd.SetVendor {
		apierr.BadRequest(w, "role or vendor_id is required")
		return
	}
	actorID, ok := callerUserID(w, claims)
	if !ok {
		return
	}
	if err := h.repo.UpdateMember(r.Context(), hid, actorID, userID, upd); err != nil {
		writeMemberError(w, err)
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
//
// The same rules as UpdateMemberRole; the last owner may not be removed.
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
	actorID, ok := callerUserID(w, claims)
	if !ok {
		return
	}
	if err := h.repo.RemoveMember(r.Context(), hid, actorID, userID); err != nil {
		writeMemberError(w, err)
		return
	}
	members, err := h.repo.ListMembers(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": members})
}
