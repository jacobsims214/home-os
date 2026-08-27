// Package asset provides MCP tools for managing household assets.
// Assets represent physical items (HVAC, appliances, vehicles) owned by a
// household, optionally linked to a property and room. Every operation is
// scoped to the household_id extracted from the JWT claims.
package asset

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/search"
	"home-os/mcp/internal/server"
)

// searchIndexer is the minimal interface for indexing documents into Typesense.
// The search.Indexer implements this; a nil indexer means indexing is disabled.
type searchIndexer interface {
	IndexDocument(ctx context.Context, doc search.Document) error
}

// Tools holds the database pool and optional search indexer, providing asset
// MCP tool handlers. Each exported method corresponds to one MCP tool.
type Tools struct {
	pool    *pgxpool.Pool
	indexer searchIndexer
}

// NewTools creates a new asset Tools instance. The indexer may be nil to
// disable search indexing (e.g. when Typesense is not configured).
func NewTools(pool *pgxpool.Pool, indexer searchIndexer) *Tools {
	return &Tools{pool: pool, indexer: indexer}
}

// Asset represents a single asset for JSON serialization in MCP responses.
type Asset struct {
	ID             string   `json:"id"`
	HouseholdID    string   `json:"household_id"`
	PropertyID     *string  `json:"property_id,omitempty"`
	RoomID         *string  `json:"room_id,omitempty"`
	Name           string   `json:"name"`
	Category       *string  `json:"category,omitempty"`
	Manufacturer   *string  `json:"manufacturer,omitempty"`
	Model          *string  `json:"model,omitempty"`
	SerialNumber   *string  `json:"serial_number,omitempty"`
	PurchaseDate   *string  `json:"purchase_date,omitempty"`
	PurchasePrice  *float64 `json:"purchase_price,omitempty"`
	CurrentValue   *float64 `json:"current_value,omitempty"`
	WarrantyExpiry *string  `json:"warranty_expiry,omitempty"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

// --- Tool definitions ---

// ListAssetsTool returns the tool definition for listing/searching assets.
func (t *Tools) ListAssetsTool() mcp.Tool {
	return mcp.NewTool("list_assets",
		mcp.WithDescription("List or search assets belonging to the household. Optionally filter by property_id, category, or a text query that searches name, manufacturer, and model."),
		mcp.WithString("property_id",
			mcp.Description("Optional. Filter by property ID"),
		),
		mcp.WithString("category",
			mcp.Description("Optional. Filter by category"),
		),
		mcp.WithString("query",
			mcp.Description("Optional. Search text matching name, manufacturer, or model"),
		),
	)
}

// GetAssetTool returns the tool definition for getting a single asset.
func (t *Tools) GetAssetTool() mcp.Tool {
	return mcp.NewTool("get_asset",
		mcp.WithDescription("Get details of a single asset by its ID."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The asset ID"),
		),
	)
}

// CreateAssetTool returns the tool definition for creating a new asset.
func (t *Tools) CreateAssetTool() mcp.Tool {
	return mcp.NewTool("create_asset",
		mcp.WithDescription("Create a new asset. The property_id must belong to the household."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("Asset name (required)"),
		),
		mcp.WithString("category",
			mcp.Required(),
			mcp.Description("Asset category (required)"),
		),
		mcp.WithString("manufacturer",
			mcp.Description("Optional manufacturer"),
		),
		mcp.WithString("model",
			mcp.Description("Optional model"),
		),
		mcp.WithString("serial_number",
			mcp.Description("Optional serial number"),
		),
		mcp.WithString("property_id",
			mcp.Required(),
			mcp.Description("Required. ID of the property this asset belongs to"),
		),
		mcp.WithString("room_id",
			mcp.Description("Optional. ID of the room this asset is located in"),
		),
		mcp.WithString("purchase_date",
			mcp.Description("Optional purchase date (YYYY-MM-DD)"),
		),
		mcp.WithNumber("purchase_price",
			mcp.Description("Optional purchase price"),
		),
		mcp.WithNumber("current_value",
			mcp.Description("Optional current estimated value"),
		),
		mcp.WithString("warranty_expiry",
			mcp.Description("Optional warranty expiry date (YYYY-MM-DD)"),
		),
	)
}

// UpdateAssetTool returns the tool definition for updating an asset.
func (t *Tools) UpdateAssetTool() mcp.Tool {
	return mcp.NewTool("update_asset",
		mcp.WithDescription("Update an existing asset. Only the provided fields are changed."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The asset ID to update"),
		),
		mcp.WithString("name",
			mcp.Description("New asset name"),
		),
		mcp.WithString("category",
			mcp.Description("New category"),
		),
		mcp.WithString("manufacturer",
			mcp.Description("New manufacturer"),
		),
		mcp.WithString("model",
			mcp.Description("New model"),
		),
		mcp.WithString("serial_number",
			mcp.Description("New serial number"),
		),
		mcp.WithString("property_id",
			mcp.Description("New property ID"),
		),
		mcp.WithString("room_id",
			mcp.Description("New room ID"),
		),
		mcp.WithString("purchase_date",
			mcp.Description("New purchase date (YYYY-MM-DD)"),
		),
		mcp.WithNumber("purchase_price",
			mcp.Description("New purchase price"),
		),
		mcp.WithNumber("current_value",
			mcp.Description("New current estimated value"),
		),
		mcp.WithString("warranty_expiry",
			mcp.Description("New warranty expiry date (YYYY-MM-DD)"),
		),
	)
}

// DeleteAssetTool returns the tool definition for deleting an asset.
func (t *Tools) DeleteAssetTool() mcp.Tool {
	return mcp.NewTool("delete_asset",
		mcp.WithDescription("Delete an asset by its ID. Scoped to household."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The asset ID to delete"),
		),
	)
}

// --- Handler implementations ---

// HandleListAssets handles the list_assets tool call.
func (t *Tools) HandleListAssets(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	claims := server.ClaimsFromContext(ctx)
	if claims == nil {
		return mcp.NewToolResultError("unauthorized: no claims in context"), nil
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid household_id in claims: %v", err)), nil
	}

	propertyID := req.GetString("property_id", "")
	category := req.GetString("category", "")
	query := req.GetString("query", "")

	// Build dynamic query with optional filters.
	args := []any{householdID}
	where := []string{"household_id = $1"}
	paramIdx := 2

	if propertyID != "" {
		pid, err := uuid.Parse(propertyID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid property_id: %v", err)), nil
		}
		where = append(where, fmt.Sprintf("property_id = $%d", paramIdx))
		args = append(args, pid)
		paramIdx++
	}

	if category != "" {
		where = append(where, fmt.Sprintf("category = $%d", paramIdx))
		args = append(args, category)
		paramIdx++
	}

	if query != "" {
		like := "%" + strings.ToLower(query) + "%"
		where = append(where, fmt.Sprintf(
			"(LOWER(name) LIKE $%d OR LOWER(COALESCE(manufacturer, '')) LIKE $%d OR LOWER(COALESCE(model, '')) LIKE $%d)",
			paramIdx, paramIdx+1, paramIdx+2,
		))
		args = append(args, like, like, like)
		paramIdx += 3
	}

	sql := fmt.Sprintf(`
		SELECT id, household_id, property_id, room_id,
		       name, category, manufacturer, model, serial_number,
		       purchase_date, purchase_price, current_value, warranty_expiry,
		       created_at, updated_at
		FROM assets
		WHERE %s
		ORDER BY created_at DESC`, strings.Join(where, " AND "))

	rows, err := t.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list assets: %w", err)
	}
	defer rows.Close()

	assets, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[assetRow])
	if err != nil {
		return nil, fmt.Errorf("collect assets: %w", err)
	}

	result := make([]Asset, 0, len(assets))
	for _, a := range assets {
		result = append(result, assetRowToResponse(a))
	}

	payload, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal assets: %w", err)
	}

	return mcp.NewToolResultText(string(payload)), nil
}

// HandleGetAsset handles the get_asset tool call.
func (t *Tools) HandleGetAsset(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	claims := server.ClaimsFromContext(ctx)
	if claims == nil {
		return mcp.NewToolResultError("unauthorized: no claims in context"), nil
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid household_id in claims: %v", err)), nil
	}

	assetID, err := req.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("id is required: %v", err)), nil
	}

	id, err := uuid.Parse(assetID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid asset id: %v", err)), nil
	}

	rows, err := t.pool.Query(ctx, `
		SELECT id, household_id, property_id, room_id,
		       name, category, manufacturer, model, serial_number,
		       purchase_date, purchase_price, current_value, warranty_expiry,
		       created_at, updated_at
		FROM assets
		WHERE id = $1 AND household_id = $2`, id, householdID)
	if err != nil {
		return nil, fmt.Errorf("get asset: %w", err)
	}
	defer rows.Close()

	a, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[assetRow])
	if err != nil {
		if err == pgx.ErrNoRows {
			return mcp.NewToolResultText(`null`), nil
		}
		return nil, fmt.Errorf("collect asset: %w", err)
	}

	payload, err := json.Marshal(assetRowToResponse(a))
	if err != nil {
		return nil, fmt.Errorf("marshal asset: %w", err)
	}

	return mcp.NewToolResultText(string(payload)), nil
}

// HandleCreateAsset handles the create_asset tool call.
func (t *Tools) HandleCreateAsset(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

	category, err := req.RequireString("category")
	if err != nil {
		return mcp.NewToolResultError("category is required"), nil
	}

	propertyIDStr, err := req.RequireString("property_id")
	if err != nil {
		return mcp.NewToolResultError("property_id is required"), nil
	}
	propertyID, err := uuid.Parse(propertyIDStr)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid property_id: %v", err)), nil
	}

	// Validate property_id belongs to this household.
	var propHouseholdID uuid.UUID
	err = t.pool.QueryRow(ctx,
		`SELECT household_id FROM properties WHERE id = $1`, propertyID).Scan(&propHouseholdID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return mcp.NewToolResultError("property_id not found"), nil
		}
		return nil, fmt.Errorf("validate property: %w", err)
	}
	if propHouseholdID != householdID {
		return mcp.NewToolResultError("property_id does not belong to this household"), nil
	}

	// Parse optional fields.
	var roomID *uuid.UUID
	if ridStr := req.GetString("room_id", ""); ridStr != "" {
		rid, err := uuid.Parse(ridStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid room_id: %v", err)), nil
		}
		roomID = &rid
	}

	// Validate room_id — must exist, belong to the same household, and belong to this property.
	if roomID != nil {
		if result, err := t.validateRoomID(ctx, *roomID, propertyID, householdID); err != nil {
			return nil, fmt.Errorf("validate room_id: %w", err)
		} else if result != nil {
			return result, nil
		}
	}

	var purchaseDate *time.Time
	if pdStr := req.GetString("purchase_date", ""); pdStr != "" {
		pd, err := time.Parse("2006-01-02", pdStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid purchase_date (expected YYYY-MM-DD): %v", err)), nil
		}
		purchaseDate = &pd
	}

	var purchasePrice *float64
	if req.GetArguments()["purchase_price"] != nil {
		pp := req.GetFloat("purchase_price", 0)
		purchasePrice = &pp
	}

	var currentValue *float64
	if req.GetArguments()["current_value"] != nil {
		cv := req.GetFloat("current_value", 0)
		currentValue = &cv
	}

	var warrantyExpiry *time.Time
	if weStr := req.GetString("warranty_expiry", ""); weStr != "" {
		we, err := time.Parse("2006-01-02", weStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid warranty_expiry (expected YYYY-MM-DD): %v", err)), nil
		}
		warrantyExpiry = &we
	}

	manufacturer := req.GetString("manufacturer", "")
	model := req.GetString("model", "")
	serialNumber := req.GetString("serial_number", "")

	var manufacturerPtr *string
	if manufacturer != "" {
		manufacturerPtr = &manufacturer
	}
	var modelPtr *string
	if model != "" {
		modelPtr = &model
	}
	var serialNumberPtr *string
	if serialNumber != "" {
		serialNumberPtr = &serialNumber
	}

	rows, err := t.pool.Query(ctx, `
		INSERT INTO assets (household_id, property_id, room_id,
		                    name, category, manufacturer, model, serial_number,
		                    purchase_date, purchase_price, current_value, warranty_expiry)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id, household_id, property_id, room_id,
		          name, category, manufacturer, model, serial_number,
		          purchase_date, purchase_price, current_value, warranty_expiry,
		          created_at, updated_at`,
		householdID, propertyID, roomID,
		name, category, manufacturerPtr, modelPtr, serialNumberPtr,
		purchaseDate, purchasePrice, currentValue, warrantyExpiry,
	)
	if err != nil {
		return nil, fmt.Errorf("create asset: %w", err)
	}
	defer rows.Close()

	a, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[assetRow])
	if err != nil {
		return nil, fmt.Errorf("collect created asset: %w", err)
	}

	payload, err := json.Marshal(assetRowToResponse(a))
	if err != nil {
		return nil, fmt.Errorf("marshal asset: %w", err)
	}

	t.indexAsset(a)

	return mcp.NewToolResultText(string(payload)), nil
}

// HandleUpdateAsset handles the update_asset tool call.
func (t *Tools) HandleUpdateAsset(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	claims := server.ClaimsFromContext(ctx)
	if claims == nil {
		return mcp.NewToolResultError("unauthorized: no claims in context"), nil
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid household_id in claims: %v", err)), nil
	}

	assetIDStr, err := req.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError("id is required"), nil
	}
	assetID, err := uuid.Parse(assetIDStr)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid asset id: %v", err)), nil
	}

	// Fetch existing to verify ownership and merge updates.
	existing, err := t.getByID(ctx, assetID, householdID)
	if err != nil {
		return nil, fmt.Errorf("get existing asset: %w", err)
	}
	if existing == nil {
		return mcp.NewToolResultError("asset not found"), nil
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
	if v, ok := args["category"]; ok {
		if cat, ok := v.(string); ok {
			existing.Category = &cat
		}
	}
	if v, ok := args["manufacturer"]; ok {
		if m, ok := v.(string); ok {
			existing.Manufacturer = &m
		}
	}
	if v, ok := args["model"]; ok {
		if m, ok := v.(string); ok {
			existing.Model = &m
		}
	}
	if v, ok := args["serial_number"]; ok {
		if sn, ok := v.(string); ok {
			existing.SerialNumber = &sn
		}
	}
	if v, ok := args["property_id"]; ok {
		if pidStr, ok := v.(string); ok {
			pid, err := uuid.Parse(pidStr)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid property_id: %v", err)), nil
			}
			// Validate property_id belongs to this household.
			var propHouseholdID uuid.UUID
			err = t.pool.QueryRow(ctx,
				`SELECT household_id FROM properties WHERE id = $1`, pid).Scan(&propHouseholdID)
			if err != nil {
				if err == pgx.ErrNoRows {
					return mcp.NewToolResultError("property_id not found"), nil
				}
				return nil, fmt.Errorf("validate property: %w", err)
			}
			if propHouseholdID != householdID {
				return mcp.NewToolResultError("property_id does not belong to this household"), nil
			}
			existing.PropertyID = &pid
		}
	}
	if v, ok := args["room_id"]; ok {
		if ridStr, ok := v.(string); ok && ridStr != "" {
			rid, err := uuid.Parse(ridStr)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid room_id: %v", err)), nil
			}
			if existing.PropertyID == nil {
				return mcp.NewToolResultError("asset has no property — cannot assign a room"), nil
			}
			if result, err := t.validateRoomID(ctx, rid, *existing.PropertyID, householdID); err != nil {
				return nil, fmt.Errorf("validate room_id: %w", err)
			} else if result != nil {
				return result, nil
			}
			existing.RoomID = &rid
		} else {
			existing.RoomID = nil
		}
	}
	if v, ok := args["purchase_date"]; ok {
		if pdStr, ok := v.(string); ok && pdStr != "" {
			pd, err := time.Parse("2006-01-02", pdStr)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid purchase_date (expected YYYY-MM-DD): %v", err)), nil
			}
			existing.PurchaseDate = &pd
		} else {
			existing.PurchaseDate = nil
		}
	}
	if _, ok := args["purchase_price"]; ok {
		pp := req.GetFloat("purchase_price", 0)
		existing.PurchasePrice = &pp
	}
	if _, ok := args["current_value"]; ok {
		cv := req.GetFloat("current_value", 0)
		existing.CurrentValue = &cv
	}
	if v, ok := args["warranty_expiry"]; ok {
		if weStr, ok := v.(string); ok && weStr != "" {
			we, err := time.Parse("2006-01-02", weStr)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid warranty_expiry (expected YYYY-MM-DD): %v", err)), nil
			}
			existing.WarrantyExpiry = &we
		} else {
			existing.WarrantyExpiry = nil
		}
	}

	rows, err := t.pool.Query(ctx, `
		UPDATE assets SET
		    property_id = $3, room_id = $4,
		    name = $5, category = $6, manufacturer = $7, model = $8,
		    serial_number = $9, purchase_date = $10, purchase_price = $11,
		    current_value = $12, warranty_expiry = $13,
		    updated_at = NOW()
		WHERE id = $1 AND household_id = $2
		RETURNING id, household_id, property_id, room_id,
		          name, category, manufacturer, model, serial_number,
		          purchase_date, purchase_price, current_value, warranty_expiry,
		          created_at, updated_at`,
		assetID, householdID,
		existing.PropertyID, existing.RoomID,
		existing.Name, existing.Category, existing.Manufacturer, existing.Model,
		existing.SerialNumber, existing.PurchaseDate, existing.PurchasePrice,
		existing.CurrentValue, existing.WarrantyExpiry,
	)
	if err != nil {
		return nil, fmt.Errorf("update asset: %w", err)
	}
	defer rows.Close()

	a, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[assetRow])
	if err != nil {
		return nil, fmt.Errorf("collect updated asset: %w", err)
	}

	payload, err := json.Marshal(assetRowToResponse(a))
	if err != nil {
		return nil, fmt.Errorf("marshal asset: %w", err)
	}

	t.indexAsset(a)

	return mcp.NewToolResultText(string(payload)), nil
}

// HandleDeleteAsset handles the delete_asset tool call.
func (t *Tools) HandleDeleteAsset(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	claims := server.ClaimsFromContext(ctx)
	if claims == nil {
		return mcp.NewToolResultError("unauthorized: missing claims"), nil
	}

	assetIDStr, ok := req.GetArguments()["id"].(string)
	if !ok || assetIDStr == "" {
		return mcp.NewToolResultError("id is required"), nil
	}

	aid, err := uuid.Parse(assetIDStr)
	if err != nil {
		return mcp.NewToolResultError("invalid id: must be a valid UUID"), nil
	}

	tag, err := t.pool.Exec(ctx,
		`DELETE FROM assets WHERE id = $1 AND household_id = $2`,
		aid, claims.HouseholdID,
	)
	if err != nil {
		return nil, fmt.Errorf("delete asset: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return mcp.NewToolResultError("asset not found"), nil
	}

	return mcp.NewToolResultText(`{"deleted":true}`), nil
}

// --- Internal types and helpers ---

// assetRow is the database row scan target for assets.
// It mirrors the SQL column order for pgx.RowToAddrOfStructByPos.
type assetRow struct {
	ID             uuid.UUID
	HouseholdID    uuid.UUID
	PropertyID     *uuid.UUID
	RoomID         *uuid.UUID
	Name           string
	Category       *string
	Manufacturer   *string
	Model          *string
	SerialNumber   *string
	PurchaseDate   *time.Time
	PurchasePrice  *float64
	CurrentValue   *float64
	WarrantyExpiry *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// getByID fetches a single asset by ID within a household, or returns nil.
func (t *Tools) getByID(ctx context.Context, assetID, householdID uuid.UUID) (*assetRow, error) {
	rows, err := t.pool.Query(ctx, `
		SELECT id, household_id, property_id, room_id,
		       name, category, manufacturer, model, serial_number,
		       purchase_date, purchase_price, current_value, warranty_expiry,
		       created_at, updated_at
		FROM assets
		WHERE id = $1 AND household_id = $2`, assetID, householdID)
	if err != nil {
		return nil, fmt.Errorf("get asset by id: %w", err)
	}
	defer rows.Close()

	a, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[assetRow])
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("collect asset: %w", err)
	}
	return a, nil
}

// validateRoomID checks that the room exists, belongs to the specified property,
// and that property belongs to the specified household. Returns a clean tool
// error if validation fails, or (nil, nil) on success.
func (t *Tools) validateRoomID(ctx context.Context, roomID, propertyID, householdID uuid.UUID) (*mcp.CallToolResult, error) {
	var roomHouseholdID uuid.UUID
	var roomPropertyID uuid.UUID
	err := t.pool.QueryRow(ctx, `
		SELECT p.household_id, r.property_id
		FROM rooms r
		JOIN properties p ON p.id = r.property_id
		WHERE r.id = $1`, roomID).Scan(&roomHouseholdID, &roomPropertyID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return mcp.NewToolResultError("room_id not found"), nil
		}
		return nil, fmt.Errorf("validate room_id: %w", err)
	}
	if roomHouseholdID != householdID {
		return mcp.NewToolResultError("room_id does not belong to this household"), nil
	}
	if roomPropertyID != propertyID {
		return mcp.NewToolResultError("room_id does not belong to the specified property"), nil
	}
	return nil, nil
}

// indexAsset upserts the asset into the Typesense household_search collection
// on a background context. Failures are logged but never returned — the tool
// call always succeeds regardless of indexing outcome. If the indexer is nil
// (Typesense not configured), indexing is silently skipped.
func (t *Tools) indexAsset(a *assetRow) {
	if t.indexer == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("asset: panic during search indexing", "panic", r)
		}
	}()

	body := ""
	if a.Category != nil {
		body = *a.Category
	}
	if a.Manufacturer != nil {
		body += " " + *a.Manufacturer
	}
	if a.Model != nil {
		body += " " + *a.Model
	}
	if a.SerialNumber != nil {
		body += " " + *a.SerialNumber
	}

	doc := search.Document{
		ID:          "asset-" + a.ID.String(),
		HouseholdID: a.HouseholdID.String(),
		EntityType:  "asset",
		EntityID:    a.ID.String(),
		Title:       a.Name,
		Body:        body,
		CreatedAt:   a.CreatedAt.Unix(),
		UpdatedAt:   a.UpdatedAt.Unix(),
	}
	if a.PropertyID != nil {
		pid := a.PropertyID.String()
		doc.PropertyID = &pid
	}

	if err := t.indexer.IndexDocument(context.Background(), doc); err != nil {
		slog.Warn("asset: failed to index into search", "id", a.ID, "error", err)
	}
}

// assetRowToResponse converts a database row to the JSON-friendly Asset response.
func assetRowToResponse(a *assetRow) Asset {
	r := Asset{
		ID:          a.ID.String(),
		HouseholdID: a.HouseholdID.String(),
		Name:        a.Name,
		CreatedAt:   a.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   a.UpdatedAt.Format(time.RFC3339),
	}
	if a.PropertyID != nil {
		s := a.PropertyID.String()
		r.PropertyID = &s
	}
	if a.RoomID != nil {
		s := a.RoomID.String()
		r.RoomID = &s
	}
	if a.Category != nil {
		r.Category = a.Category
	}
	if a.Manufacturer != nil {
		r.Manufacturer = a.Manufacturer
	}
	if a.Model != nil {
		r.Model = a.Model
	}
	if a.SerialNumber != nil {
		r.SerialNumber = a.SerialNumber
	}
	if a.PurchaseDate != nil {
		s := a.PurchaseDate.Format("2006-01-02")
		r.PurchaseDate = &s
	}
	if a.PurchasePrice != nil {
		r.PurchasePrice = a.PurchasePrice
	}
	if a.CurrentValue != nil {
		r.CurrentValue = a.CurrentValue
	}
	if a.WarrantyExpiry != nil {
		s := a.WarrantyExpiry.Format("2006-01-02")
		r.WarrantyExpiry = &s
	}
	return r
}
