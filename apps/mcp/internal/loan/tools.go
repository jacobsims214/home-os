// Package loan provides MCP tools for managing household loans.
// Loans represent financial debts (mortgages, vehicle loans, personal loans)
// belonging to a household. Every operation is scoped to the household_id
// extracted from the JWT claims.
package loan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
)

// Tools holds the database pool and provides loan MCP tool handlers.
type Tools struct {
	pool *pgxpool.Pool
}

// NewTools creates a new loan Tools instance.
func NewTools(pool *pgxpool.Pool) *Tools {
	return &Tools{pool: pool}
}

// Loan represents a single loan for JSON serialization in MCP responses.
type Loan struct {
	ID               string   `json:"id"`
	HouseholdID      string   `json:"household_id"`
	Name             string   `json:"name"`
	EntityType       *string  `json:"entity_type,omitempty"`
	EntityID         *string  `json:"entity_id,omitempty"`
	Lender           *string  `json:"lender,omitempty"`
	OriginalAmount   float64  `json:"original_amount"`
	InterestRate     *float64 `json:"interest_rate,omitempty"`
	TermMonths       *int     `json:"term_months,omitempty"`
	MonthlyPayment   *float64 `json:"monthly_payment,omitempty"`
	RemainingBalance float64  `json:"remaining_balance"`
	StartDate        *string  `json:"start_date,omitempty"`
	Notes            *string  `json:"notes,omitempty"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
}

// --- Tool definitions ---

// ListLoansTool returns the tool definition for listing loans.
func (t *Tools) ListLoansTool() mcp.Tool {
	return mcp.NewTool("list_loans",
		mcp.WithDescription("List all loans for the household. Optionally filter by entity_type (e.g. 'property', 'vehicle')."),
		mcp.WithString("entity_type",
			mcp.Description("Optional. Filter by entity type (e.g. 'property', 'vehicle')"),
		),
	)
}

// GetLoanTool returns the tool definition for getting a single loan.
func (t *Tools) GetLoanTool() mcp.Tool {
	return mcp.NewTool("get_loan",
		mcp.WithDescription("Get details of a single loan by its ID."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The loan ID"),
		),
	)
}

// CreateLoanTool returns the tool definition for creating a new loan.
func (t *Tools) CreateLoanTool() mcp.Tool {
	return mcp.NewTool("create_loan",
		mcp.WithDescription("Create a new loan record."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("Loan name (required)"),
		),
		mcp.WithString("entity_type",
			mcp.Description("Optional. Entity type this loan is associated with (e.g. 'property', 'vehicle')"),
		),
		mcp.WithString("entity_id",
			mcp.Description("Optional. Entity ID this loan is associated with"),
		),
		mcp.WithString("lender",
			mcp.Description("Optional. Lender name"),
		),
		mcp.WithNumber("original_amount",
			mcp.Required(),
			mcp.Description("Original loan amount (required)"),
		),
		mcp.WithNumber("interest_rate",
			mcp.Description("Optional. Annual interest rate as a decimal (e.g. 6.5 for 6.5%)"),
		),
		mcp.WithNumber("term_months",
			mcp.Description("Optional. Loan term in months"),
		),
		mcp.WithNumber("monthly_payment",
			mcp.Description("Optional. Monthly payment amount"),
		),
		mcp.WithNumber("remaining_balance",
			mcp.Required(),
			mcp.Description("Current remaining balance (required)"),
		),
		mcp.WithString("start_date",
			mcp.Description("Optional. Loan start date (YYYY-MM-DD)"),
		),
		mcp.WithString("notes",
			mcp.Description("Optional. Notes about the loan"),
		),
	)
}

// UpdateLoanTool returns the tool definition for updating a loan.
func (t *Tools) UpdateLoanTool() mcp.Tool {
	return mcp.NewTool("update_loan",
		mcp.WithDescription("Update an existing loan. Only the provided fields are changed."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The loan ID to update"),
		),
		mcp.WithString("name",
			mcp.Description("New loan name"),
		),
		mcp.WithString("entity_type",
			mcp.Description("New entity type"),
		),
		mcp.WithString("entity_id",
			mcp.Description("New entity ID"),
		),
		mcp.WithString("lender",
			mcp.Description("New lender name"),
		),
		mcp.WithNumber("original_amount",
			mcp.Description("New original loan amount"),
		),
		mcp.WithNumber("interest_rate",
			mcp.Description("New annual interest rate"),
		),
		mcp.WithNumber("term_months",
			mcp.Description("New loan term in months"),
		),
		mcp.WithNumber("monthly_payment",
			mcp.Description("New monthly payment amount"),
		),
		mcp.WithNumber("remaining_balance",
			mcp.Description("New remaining balance"),
		),
		mcp.WithString("start_date",
			mcp.Description("New loan start date (YYYY-MM-DD)"),
		),
		mcp.WithString("notes",
			mcp.Description("New notes"),
		),
	)
}

// DeleteLoanTool returns the tool definition for deleting a loan.
func (t *Tools) DeleteLoanTool() mcp.Tool {
	return mcp.NewTool("delete_loan",
		mcp.WithDescription("Delete a loan by its ID. Scoped to household."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The loan ID to delete"),
		),
	)
}

// --- Handler implementations ---

// HandleListLoans handles the list_loans tool call.
func (t *Tools) HandleListLoans(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	claims := server.ClaimsFromContext(ctx)
	if claims == nil {
		return mcp.NewToolResultError("unauthorized: no claims in context"), nil
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid household_id in claims: %v", err)), nil
	}

	entityType := req.GetString("entity_type", "")

	// Build dynamic query with optional entity_type filter.
	args := []any{householdID}
	where := []string{"household_id = $1"}
	paramIdx := 2

	if entityType != "" {
		where = append(where, fmt.Sprintf("entity_type = $%d", paramIdx))
		args = append(args, entityType)
		paramIdx++
	}

	sql := fmt.Sprintf(`
		SELECT id, household_id, name, entity_type, entity_id,
		       lender, original_amount, interest_rate, term_months,
		       monthly_payment, remaining_balance, start_date, notes,
		       created_at, updated_at
		FROM loans
		WHERE %s
		ORDER BY created_at DESC`, strings.Join(where, " AND "))

	rows, err := t.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list loans: %w", err)
	}
	defer rows.Close()

	loans, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[loanRow])
	if err != nil {
		return nil, fmt.Errorf("collect loans: %w", err)
	}

	result := make([]Loan, 0, len(loans))
	for _, l := range loans {
		result = append(result, loanRowToResponse(l))
	}

	payload, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal loans: %w", err)
	}

	return mcp.NewToolResultText(string(payload)), nil
}

// HandleGetLoan handles the get_loan tool call.
func (t *Tools) HandleGetLoan(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	claims := server.ClaimsFromContext(ctx)
	if claims == nil {
		return mcp.NewToolResultError("unauthorized: no claims in context"), nil
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid household_id in claims: %v", err)), nil
	}

	loanID, err := req.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("id is required: %v", err)), nil
	}

	id, err := uuid.Parse(loanID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid loan id: %v", err)), nil
	}

	rows, err := t.pool.Query(ctx, `
		SELECT id, household_id, name, entity_type, entity_id,
		       lender, original_amount, interest_rate, term_months,
		       monthly_payment, remaining_balance, start_date, notes,
		       created_at, updated_at
		FROM loans
		WHERE id = $1 AND household_id = $2`, id, householdID)
	if err != nil {
		return nil, fmt.Errorf("get loan: %w", err)
	}
	defer rows.Close()

	l, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[loanRow])
	if err != nil {
		if err == pgx.ErrNoRows {
			return mcp.NewToolResultText(`null`), nil
		}
		return nil, fmt.Errorf("collect loan: %w", err)
	}

	payload, err := json.Marshal(loanRowToResponse(l))
	if err != nil {
		return nil, fmt.Errorf("marshal loan: %w", err)
	}

	return mcp.NewToolResultText(string(payload)), nil
}

// HandleCreateLoan handles the create_loan tool call.
func (t *Tools) HandleCreateLoan(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	claims := server.ClaimsFromContext(ctx)
	if claims == nil {
		return mcp.NewToolResultError("unauthorized: no claims in context"), nil
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid household_id in claims: %v", err)), nil
	}

	name, err := req.RequireString("name")
	if err != nil {
		return mcp.NewToolResultError("name is required"), nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return mcp.NewToolResultError("name cannot be empty"), nil
	}

	args := req.GetArguments()

	// Parse optional entity_type and entity_id.
	var entityType, entityID *string
	if v, ok := args["entity_type"]; ok {
		if s, ok := v.(string); ok && s != "" {
			entityType = &s
		}
	}
	if v, ok := args["entity_id"]; ok {
		if s, ok := v.(string); ok && s != "" {
			entityID = &s
		}
	}

	// If entity_type is set but entity_id is not, flag it.
	if entityType != nil && entityID == nil {
		return mcp.NewToolResultError("entity_id is required when entity_type is provided"), nil
	}

	var entityIDUUID *uuid.UUID
	if entityID != nil {
		parsed, err := uuid.Parse(*entityID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid entity_id: %v", err)), nil
		}
		entityIDUUID = &parsed
	}

	// Parse lender.
	var lender *string
	if v, ok := args["lender"]; ok {
		if s, ok := v.(string); ok && s != "" {
			lender = &s
		}
	}

	// Parse original_amount.
	if _, ok := args["original_amount"]; !ok {
		return mcp.NewToolResultError("original_amount is required"), nil
	}
	originalAmount := req.GetFloat("original_amount", 0)

	// Parse remaining_balance.
	if _, ok := args["remaining_balance"]; !ok {
		return mcp.NewToolResultError("remaining_balance is required"), nil
	}
	remainingBalance := req.GetFloat("remaining_balance", 0)

	// Parse optional numeric fields.
	var interestRate *float64
	if _, ok := args["interest_rate"]; ok {
		v := req.GetFloat("interest_rate", 0)
		interestRate = &v
	}

	var termMonths *int
	if v, ok := args["term_months"]; ok {
		if f, ok := v.(float64); ok {
			i := int(f)
			termMonths = &i
		}
	}

	var monthlyPayment *float64
	if _, ok := args["monthly_payment"]; ok {
		v := req.GetFloat("monthly_payment", 0)
		monthlyPayment = &v
	}

	// Parse optional start_date.
	var startDate *time.Time
	if sdStr := req.GetString("start_date", ""); sdStr != "" {
		sd, err := time.Parse("2006-01-02", sdStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid start_date (expected YYYY-MM-DD): %v", err)), nil
		}
		startDate = &sd
	}

	// Parse optional notes.
	var notes *string
	if v, ok := args["notes"]; ok {
		if s, ok := v.(string); ok && s != "" {
			notes = &s
		}
	}

	rows, err := t.pool.Query(ctx, `
		INSERT INTO loans (household_id, name, entity_type, entity_id,
		                   lender, original_amount, interest_rate, term_months,
		                   monthly_payment, remaining_balance, start_date, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id, household_id, name, entity_type, entity_id,
		          lender, original_amount, interest_rate, term_months,
		          monthly_payment, remaining_balance, start_date, notes,
		          created_at, updated_at`,
		householdID, name, entityType, entityIDUUID,
		lender, originalAmount, interestRate, termMonths,
		monthlyPayment, remainingBalance, startDate, notes,
	)
	if err != nil {
		return nil, fmt.Errorf("create loan: %w", err)
	}
	defer rows.Close()

	l, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[loanRow])
	if err != nil {
		return nil, fmt.Errorf("collect created loan: %w", err)
	}

	payload, err := json.Marshal(loanRowToResponse(l))
	if err != nil {
		return nil, fmt.Errorf("marshal loan: %w", err)
	}

	return mcp.NewToolResultText(string(payload)), nil
}

// HandleUpdateLoan handles the update_loan tool call.
func (t *Tools) HandleUpdateLoan(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	claims := server.ClaimsFromContext(ctx)
	if claims == nil {
		return mcp.NewToolResultError("unauthorized: no claims in context"), nil
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid household_id in claims: %v", err)), nil
	}

	loanIDStr, err := req.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError("id is required"), nil
	}
	loanID, err := uuid.Parse(loanIDStr)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid loan id: %v", err)), nil
	}

	// Fetch existing to verify ownership and merge updates.
	existing, err := t.getByID(ctx, loanID, householdID)
	if err != nil {
		return nil, fmt.Errorf("get existing loan: %w", err)
	}
	if existing == nil {
		return mcp.NewToolResultError("loan not found"), nil
	}

	// Apply updates for fields that were provided in the request.
	args := req.GetArguments()

	if v, ok := args["name"]; ok {
		if name, ok := v.(string); ok {
			name = strings.TrimSpace(name)
			if name == "" {
				return mcp.NewToolResultError("name cannot be empty"), nil
			}
			existing.Name = name
		}
	}
	if v, ok := args["entity_type"]; ok {
		if s, ok := v.(string); ok && s != "" {
			existing.EntityType = &s
		} else {
			existing.EntityType = nil
		}
	}
	if v, ok := args["entity_id"]; ok {
		if s, ok := v.(string); ok && s != "" {
			parsed, err := uuid.Parse(s)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid entity_id: %v", err)), nil
			}
			existing.EntityID = &parsed
		} else {
			existing.EntityID = nil
		}
	}
	if v, ok := args["lender"]; ok {
		if s, ok := v.(string); ok && s != "" {
			existing.Lender = &s
		} else {
			existing.Lender = nil
		}
	}
	if _, ok := args["original_amount"]; ok {
		existing.OriginalAmount = req.GetFloat("original_amount", 0)
	}
	if _, ok := args["interest_rate"]; ok {
		v := req.GetFloat("interest_rate", 0)
		existing.InterestRate = &v
	}
	if v, ok := args["term_months"]; ok {
		if f, ok := v.(float64); ok {
			i := int(f)
			existing.TermMonths = &i
		}
	}
	if _, ok := args["monthly_payment"]; ok {
		v := req.GetFloat("monthly_payment", 0)
		existing.MonthlyPayment = &v
	}
	if _, ok := args["remaining_balance"]; ok {
		existing.RemainingBalance = req.GetFloat("remaining_balance", 0)
	}
	if v, ok := args["start_date"]; ok {
		if sdStr, ok := v.(string); ok && sdStr != "" {
			sd, err := time.Parse("2006-01-02", sdStr)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid start_date (expected YYYY-MM-DD): %v", err)), nil
			}
			existing.StartDate = &sd
		} else {
			existing.StartDate = nil
		}
	}
	if v, ok := args["notes"]; ok {
		if s, ok := v.(string); ok && s != "" {
			existing.Notes = &s
		} else {
			existing.Notes = nil
		}
	}

	rows, err := t.pool.Query(ctx, `
		UPDATE loans SET
		    name = $3, entity_type = $4, entity_id = $5,
		    lender = $6, original_amount = $7, interest_rate = $8,
		    term_months = $9, monthly_payment = $10, remaining_balance = $11,
		    start_date = $12, notes = $13,
		    updated_at = NOW()
		WHERE id = $1 AND household_id = $2
		RETURNING id, household_id, name, entity_type, entity_id,
		          lender, original_amount, interest_rate, term_months,
		          monthly_payment, remaining_balance, start_date, notes,
		          created_at, updated_at`,
		loanID, householdID,
		existing.Name, existing.EntityType, existing.EntityID,
		existing.Lender, existing.OriginalAmount, existing.InterestRate,
		existing.TermMonths, existing.MonthlyPayment, existing.RemainingBalance,
		existing.StartDate, existing.Notes,
	)
	if err != nil {
		return nil, fmt.Errorf("update loan: %w", err)
	}
	defer rows.Close()

	l, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[loanRow])
	if err != nil {
		return nil, fmt.Errorf("collect updated loan: %w", err)
	}

	payload, err := json.Marshal(loanRowToResponse(l))
	if err != nil {
		return nil, fmt.Errorf("marshal loan: %w", err)
	}

	return mcp.NewToolResultText(string(payload)), nil
}

// HandleDeleteLoan handles the delete_loan tool call.
func (t *Tools) HandleDeleteLoan(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	claims := server.ClaimsFromContext(ctx)
	if claims == nil {
		return mcp.NewToolResultError("unauthorized: missing claims"), nil
	}

	loanIDStr, ok := req.GetArguments()["id"].(string)
	if !ok || loanIDStr == "" {
		return mcp.NewToolResultError("id is required"), nil
	}

	lid, err := uuid.Parse(loanIDStr)
	if err != nil {
		return mcp.NewToolResultError("invalid id: must be a valid UUID"), nil
	}

	tag, err := t.pool.Exec(ctx,
		`DELETE FROM loans WHERE id = $1 AND household_id = $2`,
		lid, claims.HouseholdID,
	)
	if err != nil {
		return nil, fmt.Errorf("delete loan: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return mcp.NewToolResultError("loan not found"), nil
	}

	return mcp.NewToolResultText(`{"deleted":true}`), nil
}

// --- Internal types and helpers ---

// loanRow is the database row scan target for loans.
// It mirrors the SQL column order for pgx.RowToAddrOfStructByPos.
type loanRow struct {
	ID               uuid.UUID
	HouseholdID      uuid.UUID
	Name             string
	EntityType       *string
	EntityID         *uuid.UUID
	Lender           *string
	OriginalAmount   float64
	InterestRate     *float64
	TermMonths       *int
	MonthlyPayment   *float64
	RemainingBalance float64
	StartDate        *time.Time
	Notes            *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// getByID fetches a single loan by ID within a household, or returns nil.
func (t *Tools) getByID(ctx context.Context, loanID, householdID uuid.UUID) (*loanRow, error) {
	rows, err := t.pool.Query(ctx, `
		SELECT id, household_id, name, entity_type, entity_id,
		       lender, original_amount, interest_rate, term_months,
		       monthly_payment, remaining_balance, start_date, notes,
		       created_at, updated_at
		FROM loans
		WHERE id = $1 AND household_id = $2`, loanID, householdID)
	if err != nil {
		return nil, fmt.Errorf("get loan by id: %w", err)
	}
	defer rows.Close()

	l, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[loanRow])
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("collect loan: %w", err)
	}
	return l, nil
}

// loanRowToResponse converts a database row to the JSON-friendly Loan response.
func loanRowToResponse(l *loanRow) Loan {
	r := Loan{
		ID:               l.ID.String(),
		HouseholdID:      l.HouseholdID.String(),
		Name:             l.Name,
		OriginalAmount:   l.OriginalAmount,
		RemainingBalance: l.RemainingBalance,
		CreatedAt:        l.CreatedAt.Format(time.RFC3339),
		UpdatedAt:        l.UpdatedAt.Format(time.RFC3339),
	}
	if l.EntityType != nil {
		r.EntityType = l.EntityType
	}
	if l.EntityID != nil {
		s := l.EntityID.String()
		r.EntityID = &s
	}
	if l.Lender != nil {
		r.Lender = l.Lender
	}
	if l.InterestRate != nil {
		r.InterestRate = l.InterestRate
	}
	if l.TermMonths != nil {
		r.TermMonths = l.TermMonths
	}
	if l.MonthlyPayment != nil {
		r.MonthlyPayment = l.MonthlyPayment
	}
	if l.StartDate != nil {
		s := l.StartDate.Format("2006-01-02")
		r.StartDate = &s
	}
	if l.Notes != nil {
		r.Notes = l.Notes
	}
	return r
}