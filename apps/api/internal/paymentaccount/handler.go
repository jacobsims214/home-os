package paymentaccount

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// Handler holds dependencies for payment account HTTP handlers.
type Handler struct {
	repo *Repo
}

// NewHandler creates a new payment account handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

// --- request / response types ---

// createPaymentAccountRequest is the JSON body for POST /api/v1/payment-accounts.
//
// THERE IS NO FIELD FOR A FULL ACCOUNT NUMBER OR A ROUTING NUMBER, and there
// never will be: the table has no column for one. `last_four` is the only
// digit-bearing field, it is capped at four digits by the database and by
// ValidateLastFour, and a client that pastes a longer number is refused with a
// message that says so. A JSON body naming an unknown field (account_number,
// routing_number) is ignored by encoding/json rather than silently stored --
// there is nowhere for it to go.
type createPaymentAccountRequest struct {
	Name        string `json:"name"`
	Institution string `json:"institution"`
	AccountType string `json:"account_type"`
	LastFour    string `json:"last_four"`
	Notes       string `json:"notes"`
}

// updatePaymentAccountRequest is a full replace of the editable fields, with
// account_type optional so a client that omits it keeps the stored value
// rather than resetting a savings account to checking.
type updatePaymentAccountRequest struct {
	Name        string  `json:"name"`
	Institution string  `json:"institution"`
	AccountType *string `json:"account_type"`
	LastFour    string  `json:"last_four"`
	Notes       string  `json:"notes"`
}

// linkSecretRequest attaches (or detaches) the bank login. `secret_id` is the
// uuid returned by POST /api/v1/secrets; the ciphertext stays in `secrets`.
// Explicit JSON null detaches.
type linkSecretRequest struct {
	SecretID *string `json:"secret_id"`
}

// paymentAccountResponse is a payment account as the API serializes it.
//
// LASTFOUR is four characters and the table cannot hold more. LOGINSTORED is
// the credential fact -- it says a login exists, never how to open it -- and
// it is ABSENT (not false) for a role the credential policy excludes, so the
// response cannot be used to tell "no login" from "you may not know".
type paymentAccountResponse struct {
	ID          string  `json:"id"`
	HouseholdID string  `json:"household_id"`
	Name        string  `json:"name"`
	Institution *string `json:"institution,omitempty"`
	AccountType string  `json:"account_type"`
	// LastFour is exactly four digits, or null when the family has not looked
	// them up. Never a full number (075: there is no column for one).
	LastFour    *string `json:"last_four"`
	Notes       *string `json:"notes,omitempty"`
	LoginStored *bool   `json:"login_stored,omitempty"`
	// LoginSecretID is the LINKED CREDENTIAL'S ID, so a caller that may see
	// credentials can open it in the vault (GET /api/v1/secrets/{id} returns the
	// ciphertext). It is the HANDLE, never the credential: the blob is AES-GCM
	// ciphertext the server cannot read, and the uuid alone decrypts nothing.
	// Gated on the same credential policy as LoginStored, so it is absent for a
	// viewer exactly when LoginStored is.
	LoginSecretID *string `json:"login_secret_id,omitempty"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

// canSeeCredentialExistence reports whether a member with role may be told that
// a credential EXISTS.
//
// It asks the EXISTING credential policy -- ResSecrets read, which is rolesFamily
// -- and not auth.CanSeeMoney. That is a deliberate, load-bearing distinction:
// a viewer may read bills (and so money), but a viewer does not get secrets at
// all (`ResSecrets: {ActRead: rolesFamily}`). The ticket's rule is that a viewer
// does not see even the NAME of a stored credential, so "a login is stored" must
// key on the secrets resource, not on money. Asking `auth.Can` keeps this a use
// of the one policy rather than a second copy of it.
func canSeeCredentialExistence(role string) bool {
	return auth.Can(role, auth.ResSecrets, auth.ActRead)
}

// toResponse converts a domain model to a response for a caller with role
// ("" when unknown, which fails closed).
func toResponse(a *PaymentAccount, role string) paymentAccountResponse {
	resp := paymentAccountResponse{
		ID:          a.ID.String(),
		HouseholdID: a.HouseholdID.String(),
		Name:        a.Name,
		Institution: a.Institution,
		AccountType: a.AccountType,
		LastFour:    a.LastFour,
		Notes:       a.Notes,
		CreatedAt:   a.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   a.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if canSeeCredentialExistence(role) {
		stored := a.SecretID != nil
		resp.LoginStored = &stored
		if a.SecretID != nil {
			id := a.SecretID.String()
			resp.LoginSecretID = &id
		}
	}
	return resp
}

// householdIDFrom extracts the household uuid from the request's claims.
func householdIDFrom(r *http.Request) (uuid.UUID, bool) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// List returns the household's payment accounts.
// GET /api/v1/payment-accounts
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	hid, ok := householdIDFrom(r)
	if !ok {
		apierr.Forbidden(w, "missing household context")
		return
	}
	accounts, err := h.repo.List(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	role := middleware.Role(r)
	out := make([]paymentAccountResponse, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, toResponse(a, role))
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": out})
}

// Get returns one payment account.
// GET /api/v1/payment-accounts/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	hid, ok := householdIDFrom(r)
	if !ok {
		apierr.Forbidden(w, "missing household context")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid id")
		return
	}
	a, err := h.repo.Get(r.Context(), hid, id)
	if errors.Is(err, ErrNotFound) {
		apierr.NotFound(w, "payment account not found")
		return
	}
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, toResponse(a, middleware.Role(r)))
}

// Create adds a payment account.
// POST /api/v1/payment-accounts
//
// This is where a pasted full account number is refused, with a message that
// explains the rule instead of relaying a CHECK constraint name.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	hid, ok := householdIDFrom(r)
	if !ok {
		apierr.Forbidden(w, "missing household context")
		return
	}
	var req createPaymentAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	a, msg := buildAccount(req.Name, req.Institution, req.Notes, req.AccountType, req.LastFour, "")
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	a.HouseholdID = hid
	created, err := h.repo.Create(r.Context(), a)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusCreated, toResponse(created, middleware.Role(r)))
}

// Update replaces a payment account's editable fields.
// PUT /api/v1/payment-accounts/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	hid, ok := householdIDFrom(r)
	if !ok {
		apierr.Forbidden(w, "missing household context")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid id")
		return
	}
	var req updatePaymentAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	a, msg := buildAccount(req.Name, req.Institution, req.Notes, deref(req.AccountType), req.LastFour,
		valueOr(req.AccountType, ""))
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	updated, err := h.repo.Update(r.Context(), hid, id, a)
	if errors.Is(err, ErrNotFound) {
		apierr.NotFound(w, "payment account not found")
		return
	}
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, toResponse(updated, middleware.Role(r)))
}

// Delete removes a payment account. A bill that named it keeps existing and
// simply stops naming an account (ON DELETE SET NULL, 075).
// DELETE /api/v1/payment-accounts/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	hid, ok := householdIDFrom(r)
	if !ok {
		apierr.Forbidden(w, "missing household context")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid id")
		return
	}
	if err := h.repo.Delete(r.Context(), hid, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			apierr.NotFound(w, "payment account not found")
			return
		}
		apierr.InternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// LinkSecret attaches or detaches the bank login for an account.
// PUT /api/v1/payment-accounts/{id}/secret
//
// The body carries a SECRET UUID AND NOTHING ELSE. The credential itself was
// encrypted in the browser (POST /api/v1/secrets) and this route never sees a
// plaintext, a ciphertext or a password -- which is what lets the UI say "login
// stored" without the server knowing the login.
func (h *Handler) LinkSecret(w http.ResponseWriter, r *http.Request) {
	hid, ok := householdIDFrom(r)
	if !ok {
		apierr.Forbidden(w, "missing household context")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid id")
		return
	}
	var req linkSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	var secretID *uuid.UUID
	if req.SecretID != nil && *req.SecretID != "" {
		parsed, err := uuid.Parse(*req.SecretID)
		if err != nil {
			apierr.BadRequest(w, "invalid secret_id")
			return
		}
		secretID = &parsed
	}
	updated, err := h.repo.SetSecret(r.Context(), hid, id, secretID)
	if errors.Is(err, ErrNotFound) {
		apierr.NotFound(w, "payment account not found")
		return
	}
	if errors.Is(err, ErrSecretNotFound) {
		apierr.BadRequest(w, "that credential is not one of this family's — save it in the vault first")
		return
	}
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, toResponse(updated, middleware.Role(r)))
}

// buildAccount validates the editable fields and returns the row to write, or
// the message to refuse with. It calls ValidateLastFour, the one rule, so the
// 400 a client gets and the CHECK the database enforces cannot drift apart.
func buildAccount(name, institution, notes, accountType, lastFour, currentType string) (*PaymentAccount, string) {
	n, inst, nt := NormalizeInput(name, institution, notes)
	if n == "" {
		return nil, "name is required"
	}
	if accountType == "" {
		// Omitting the type keeps the stored value on an update, and defaults
		// to checking on a create -- which is also the column default.
		if currentType != "" {
			accountType = currentType
		} else {
			accountType = TypeChecking
		}
	}
	if !ValidType(accountType) {
		return nil, "account_type must be one of checking, savings, credit_card, other"
	}
	lf, empty, msg := ValidateLastFour(lastFour)
	if msg != "" {
		return nil, msg
	}
	a := &PaymentAccount{Name: n, Institution: inst, AccountType: accountType, Notes: nt}
	if !empty {
		a.LastFour = &lf
	}
	return a, ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func valueOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}
