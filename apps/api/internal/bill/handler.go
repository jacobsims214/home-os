package bill

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
	"home-os/api/pkg/apierr"
)

type searchIndexer interface {
	IndexDocument(ctx context.Context, doc search.SearchDocument) error
	DeleteDocument(ctx context.Context, id string) error
}

// Handler holds the dependencies needed by bill HTTP handlers.
type Handler struct {
	repo   *Repo
	search searchIndexer
}

func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

func (h *Handler) WithSearchClient(sc *search.Client) *Handler {
	h.search = sc
	return h
}

func (h *Handler) indexBill(ctx context.Context, b *Bill) {
	if h.search == nil { return }
	defer func() { if r := recover(); r != nil { slog.Warn("bill: panic during search indexing", "panic", r) } }()
	body := ""
	if b.Category != nil { body = *b.Category }
	doc := search.SearchDocument{
		ID: "bill-" + b.ID.String(), HouseholdID: b.HouseholdID.String(),
		EntityType: "bill", EntityID: b.ID.String(),
		Title: b.Name, Body: body,
		CreatedAt: b.CreatedAt.Unix(), UpdatedAt: b.UpdatedAt.Unix(),
	}
	if b.PropertyID != nil { pid := b.PropertyID.String(); doc.PropertyID = &pid }
	if err := h.search.IndexDocument(context.Background(), doc); err != nil {
		slog.Warn("bill: failed to index", "id", b.ID, "error", err)
	}
}

func (h *Handler) deleteBillIndex(ctx context.Context, id string) {
	if h.search == nil { return }
	defer func() { if r := recover(); r != nil { slog.Warn("bill: panic during search deletion", "panic", r) } }()
	if err := h.search.DeleteDocument(context.Background(), "bill-"+id); err != nil {
		slog.Warn("bill: failed to delete from search", "id", id, "error", err)
	}
}

// --- request / response types ---

type createBillRequest struct {
	PropertyID    *string `json:"property_id,omitempty"`
	Name          string  `json:"name"`
	Amount        *string `json:"amount,omitempty"`
	DueDay        *int    `json:"due_day,omitempty"`
	Category      *string `json:"category,omitempty"`
	VendorID      *string `json:"vendor_id,omitempty"`
	Rrule         *string `json:"rrule,omitempty"`
	Notes         *string `json:"notes,omitempty"`
	EntityType    *string `json:"entity_type,omitempty"`
	EntityID      *string `json:"entity_id,omitempty"`
	IsAutopay     *bool   `json:"is_autopay,omitempty"`
	AccountNumber *string `json:"account_number,omitempty"`
	PaymentURL    *string `json:"payment_url,omitempty"`
}

type updateBillRequest struct {
	PropertyID    *string `json:"property_id,omitempty"`
	Name          *string `json:"name,omitempty"`
	Amount        *string `json:"amount,omitempty"`
	DueDay        *int    `json:"due_day,omitempty"`
	Category      *string `json:"category,omitempty"`
	VendorID      *string `json:"vendor_id,omitempty"`
	Rrule         *string `json:"rrule,omitempty"`
	Notes         *string `json:"notes,omitempty"`
	EntityType    *string `json:"entity_type,omitempty"`
	EntityID      *string `json:"entity_id,omitempty"`
	IsAutopay     *bool   `json:"is_autopay,omitempty"`
	AccountNumber *string `json:"account_number,omitempty"`
	PaymentURL    *string `json:"payment_url,omitempty"`
}

type billResponse struct {
	ID            string  `json:"id"`
	HouseholdID   string  `json:"household_id"`
	PropertyID    *string `json:"property_id,omitempty"`
	Name          string  `json:"name"`
	Amount        *string `json:"amount,omitempty"`
	DueDay        *int    `json:"due_day,omitempty"`
	Category      *string `json:"category,omitempty"`
	VendorID      *string `json:"vendor_id,omitempty"`
	Rrule         *string `json:"rrule,omitempty"`
	Notes         *string `json:"notes,omitempty"`
	EntityType    *string `json:"entity_type,omitempty"`
	EntityID      *string `json:"entity_id,omitempty"`
	IsAutopay     *bool   `json:"is_autopay,omitempty"`
	AccountNumber *string `json:"account_number,omitempty"`
	PaymentURL    *string `json:"payment_url,omitempty"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

// toResponse converts a Bill domain model to a JSON-safe response.
func toResponse(b *Bill) billResponse {
	resp := billResponse{
		ID:            b.ID.String(),
		HouseholdID:   b.HouseholdID.String(),
		Name:          b.Name,
		DueDay:        b.DueDay,
		Category:      b.Category,
		Rrule:         b.Rrule,
		Notes:         b.Notes,
		EntityType:    b.EntityType,
		IsAutopay:     b.IsAutopay,
		AccountNumber: b.AccountNumber,
		PaymentURL:    b.PaymentURL,
		CreatedAt:     b.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:     b.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}

	if b.PropertyID != nil {
		s := b.PropertyID.String()
		resp.PropertyID = &s
	}
	if b.VendorID != nil {
		s := b.VendorID.String()
		resp.VendorID = &s
	}
	if b.EntityID != nil {
		s := b.EntityID.String()
		resp.EntityID = &s
	}
	if b.Amount.Valid {
		s := b.Amount.Int.String()
		resp.Amount = &s
	}
	return resp
}

// extractHouseholdID extracts the household UUID from the JWT claims in the
// request context. Returns uuid.Nil if no claims are present.
func extractHouseholdID(r *http.Request) uuid.UUID {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		return uuid.Nil
	}
	id, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		return uuid.Nil
	}
	return id
}

// List returns all bills for the authenticated user's household.
// GET /api/v1/bills
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	householdID := extractHouseholdID(r)
	if householdID == uuid.Nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	bills, err := h.repo.List(r.Context(), householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	var resp []billResponse
	for _, b := range bills {
		resp = append(resp, toResponse(b))
	}
	if resp == nil {
		resp = []billResponse{}
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": resp})
}

// Get returns a single bill by ID, scoped to the authenticated household.
// GET /api/v1/bills/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid bill id")
		return
	}

	householdID := extractHouseholdID(r)
	if householdID == uuid.Nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	bill, err := h.repo.Get(r.Context(), id, householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if bill == nil {
		apierr.NotFound(w, "bill not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{"data": toResponse(bill)})
}

// Create creates a new bill for the authenticated household.
// POST /api/v1/bills
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	householdID := extractHouseholdID(r)
	if householdID == uuid.Nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	var req createBillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}

	b := &Bill{
		HouseholdID:   householdID,
		Name:          req.Name,
		DueDay:        req.DueDay,
		Category:      req.Category,
		Rrule:         req.Rrule,
		Notes:         req.Notes,
		EntityType:    req.EntityType,
		IsAutopay:     req.IsAutopay,
		AccountNumber: req.AccountNumber,
		PaymentURL:    req.PaymentURL,
	}

	if req.PropertyID != nil {
		pid, err := uuid.Parse(*req.PropertyID)
		if err != nil {
			apierr.BadRequest(w, "invalid property_id")
			return
		}
		b.PropertyID = &pid
	}
	if req.VendorID != nil {
		vid, err := uuid.Parse(*req.VendorID)
		if err != nil {
			apierr.BadRequest(w, "invalid vendor_id")
			return
		}
		b.VendorID = &vid
	}
	if req.Amount != nil {
		if err := b.Amount.Scan(*req.Amount); err != nil {
			apierr.BadRequest(w, "invalid amount")
			return
		}
	}
	if req.EntityID != nil {
		eid, err := uuid.Parse(*req.EntityID)
		if err != nil {
			apierr.BadRequest(w, "invalid entity_id")
			return
		}
		b.EntityID = &eid
	}

	created, err := h.repo.Create(r.Context(), b)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	h.indexBill(r.Context(), created)

	apierr.JSON(w, http.StatusCreated, map[string]any{"data": toResponse(created)})
}

// Update updates an existing bill, scoped to the authenticated household.
// PUT /api/v1/bills/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid bill id")
		return
	}

	householdID := extractHouseholdID(r)
	if householdID == uuid.Nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	var req updateBillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	existing, err := h.repo.Get(r.Context(), id, householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if existing == nil {
		apierr.NotFound(w, "bill not found")
		return
	}

	// Merge: only overwrite fields that were provided in the request.
	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.DueDay != nil {
		existing.DueDay = req.DueDay
	}
	if req.Category != nil {
		existing.Category = req.Category
	}
	if req.Rrule != nil {
		existing.Rrule = req.Rrule
	}
	if req.Notes != nil {
		existing.Notes = req.Notes
	}
	if req.PropertyID != nil {
		if *req.PropertyID == "" {
			existing.PropertyID = nil
		} else {
			pid, err := uuid.Parse(*req.PropertyID)
			if err != nil {
				apierr.BadRequest(w, "invalid property_id")
				return
			}
			existing.PropertyID = &pid
		}
	}
	if req.VendorID != nil {
		if *req.VendorID == "" {
			existing.VendorID = nil
		} else {
			vid, err := uuid.Parse(*req.VendorID)
			if err != nil {
				apierr.BadRequest(w, "invalid vendor_id")
				return
			}
			existing.VendorID = &vid
		}
	}
	if req.Amount != nil {
		if *req.Amount == "" {
			existing.Amount.Valid = false
		} else {
			if err := existing.Amount.Scan(*req.Amount); err != nil {
				apierr.BadRequest(w, "invalid amount")
				return
			}
		}
	}
	if req.EntityType != nil {
		existing.EntityType = req.EntityType
	}
	if req.EntityID != nil {
		if *req.EntityID == "" {
			existing.EntityID = nil
		} else {
			eid, err := uuid.Parse(*req.EntityID)
			if err != nil {
				apierr.BadRequest(w, "invalid entity_id")
				return
			}
			existing.EntityID = &eid
		}
	}
	if req.IsAutopay != nil {
		existing.IsAutopay = req.IsAutopay
	}
	if req.AccountNumber != nil {
		existing.AccountNumber = req.AccountNumber
	}
	if req.PaymentURL != nil {
		existing.PaymentURL = req.PaymentURL
	}

	updated, err := h.repo.Update(r.Context(), existing)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if updated == nil {
		apierr.NotFound(w, "bill not found")
		return
	}

	h.indexBill(r.Context(), updated)

	apierr.JSON(w, http.StatusOK, map[string]any{"data": toResponse(updated)})
}

// Delete removes a bill, scoped to the authenticated household.
// DELETE /api/v1/bills/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid bill id")
		return
	}

	householdID := extractHouseholdID(r)
	if householdID == uuid.Nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	if err := h.repo.Delete(r.Context(), id, householdID); err != nil {
		apierr.InternalError(w, err)
		return
	}

	h.deleteBillIndex(r.Context(), id.String())

	w.WriteHeader(http.StatusNoContent)
}
