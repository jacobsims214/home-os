package asset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the assets table using a pgx connection pool.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new asset repository backed by the given connection pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// List returns all assets for a household, optionally filtered by property_id.
func (r *Repo) List(ctx context.Context, householdID uuid.UUID, propertyID *uuid.UUID) ([]*Asset, error) {
	var rows pgx.Rows
	var err error

	if propertyID != nil {
		rows, err = r.pool.Query(ctx,
			`SELECT id, household_id, property_id, room_id,
			        name, category, manufacturer, model, serial_number,
			        purchase_date, purchase_price, warranty_expiry, notes,
			        created_at, updated_at
			 FROM assets
			 WHERE household_id = $1 AND property_id = $2
			 ORDER BY created_at DESC`,
			householdID, propertyID,
		)
	} else {
		rows, err = r.pool.Query(ctx,
			`SELECT id, household_id, property_id, room_id,
			        name, category, manufacturer, model, serial_number,
			        purchase_date, purchase_price, warranty_expiry, notes,
			        created_at, updated_at
			 FROM assets
			 WHERE household_id = $1
			 ORDER BY created_at DESC`,
			householdID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list assets: %w", err)
	}
	defer rows.Close()

	assets, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Asset])
	if err != nil {
		return nil, fmt.Errorf("collect assets: %w", err)
	}
	return assets, nil
}

// Get returns a single asset by ID, scoped to the given household.
// Returns nil if the asset does not exist or belongs to a different household.
func (r *Repo) Get(ctx context.Context, assetID, householdID uuid.UUID) (*Asset, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, property_id, room_id,
		        name, category, manufacturer, model, serial_number,
		        purchase_date, purchase_price, warranty_expiry, notes,
		        created_at, updated_at
		 FROM assets
		 WHERE id = $1 AND household_id = $2`,
		assetID, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("get asset: %w", err)
	}
	defer rows.Close()

	asset, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Asset])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect asset: %w", err)
	}
	return asset, nil
}

// Create inserts a new asset and writes an outbox event in a single transaction.
func (r *Repo) Create(ctx context.Context, asset *Asset) (*Asset, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("create asset: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) // no-op after commit

	rows, err := tx.Query(ctx,
		`INSERT INTO assets (household_id, property_id, room_id,
		                     name, category, manufacturer, model, serial_number,
		                     purchase_date, purchase_price, warranty_expiry, notes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 RETURNING id, household_id, property_id, room_id,
		           name, category, manufacturer, model, serial_number,
		           purchase_date, purchase_price, warranty_expiry, notes,
		           created_at, updated_at`,
		asset.HouseholdID,
		asset.PropertyID,
		asset.RoomID,
		asset.Name,
		asset.Category,
		asset.Manufacturer,
		asset.Model,
		asset.SerialNumber,
		asset.PurchaseDate,
		asset.PurchasePrice,
		asset.WarrantyExpiry,
		asset.Notes,
	)
	if err != nil {
		return nil, fmt.Errorf("create asset: insert: %w", err)
	}
	defer rows.Close()

	created, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Asset])
	if err != nil {
		return nil, fmt.Errorf("create asset: collect: %w", err)
	}

	// Build outbox payload.
	payload, err := json.Marshal(map[string]any{
		"asset_id":      created.ID.String(),
		"name":          created.Name,
		"category":      created.Category,
		"household_id":  created.HouseholdID.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("create asset: marshal outbox payload: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO outbox_events (household_id, type, payload)
		 VALUES ($1, $2, $3)`,
		created.HouseholdID, "asset.created", payload,
	)
	if err != nil {
		return nil, fmt.Errorf("create asset: outbox: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("create asset: commit: %w", err)
	}

	return created, nil
}

// Update modifies an existing asset and writes an outbox event in a single transaction.
// Only non-nil fields in the input asset are applied (partial update).
func (r *Repo) Update(ctx context.Context, assetID, householdID uuid.UUID, updates *Asset) (*Asset, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("update asset: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Fetch existing asset to verify ownership and get current values.
	existing, err := r.getInTx(ctx, tx, assetID, householdID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("update asset: %w", pgx.ErrNoRows)
	}

	// Apply non-nil updates.
	if updates.PropertyID != nil {
		existing.PropertyID = updates.PropertyID
	}
	if updates.RoomID != nil {
		existing.RoomID = updates.RoomID
	}
	if updates.Name != "" {
		existing.Name = updates.Name
	}
	if updates.Category != nil {
		existing.Category = updates.Category
	}
	if updates.Manufacturer != nil {
		existing.Manufacturer = updates.Manufacturer
	}
	if updates.Model != nil {
		existing.Model = updates.Model
	}
	if updates.SerialNumber != nil {
		existing.SerialNumber = updates.SerialNumber
	}
	if updates.PurchaseDate != nil {
		existing.PurchaseDate = updates.PurchaseDate
	}
	if updates.PurchasePrice != nil {
		existing.PurchasePrice = updates.PurchasePrice
	}
	if updates.WarrantyExpiry != nil {
		existing.WarrantyExpiry = updates.WarrantyExpiry
	}
	if updates.Notes != nil {
		existing.Notes = updates.Notes
	}

	_, err = tx.Exec(ctx,
		`UPDATE assets SET
		    property_id = $3, room_id = $4,
		    name = $5, category = $6, manufacturer = $7, model = $8,
		    serial_number = $9, purchase_date = $10, purchase_price = $11,
		    warranty_expiry = $12, notes = $13,
		    updated_at = NOW()
		 WHERE id = $1 AND household_id = $2`,
		assetID, householdID,
		existing.PropertyID, existing.RoomID,
		existing.Name, existing.Category, existing.Manufacturer, existing.Model,
		existing.SerialNumber, existing.PurchaseDate, existing.PurchasePrice,
		existing.WarrantyExpiry, existing.Notes,
	)
	if err != nil {
		return nil, fmt.Errorf("update asset: exec: %w", err)
	}

	// Build outbox payload.
	payload, err := json.Marshal(map[string]any{
		"asset_id":      assetID.String(),
		"name":          existing.Name,
		"category":      existing.Category,
		"household_id":  householdID.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("update asset: marshal outbox payload: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO outbox_events (household_id, type, payload)
		 VALUES ($1, $2, $3)`,
		householdID, "asset.updated", payload,
	)
	if err != nil {
		return nil, fmt.Errorf("update asset: outbox: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("update asset: commit: %w", err)
	}

	return existing, nil
}

// Delete removes an asset by ID, scoped to the given household.
// Returns an error if the asset does not exist or belongs to a different household.
func (r *Repo) Delete(ctx context.Context, assetID, householdID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM assets WHERE id = $1 AND household_id = $2`,
		assetID, householdID,
	)
	if err != nil {
		return fmt.Errorf("delete asset: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete asset: %w", pgx.ErrNoRows)
	}
	return nil
}

// getInTx returns an asset within the given transaction, or nil if not found.
func (r *Repo) getInTx(ctx context.Context, tx pgx.Tx, assetID, householdID uuid.UUID) (*Asset, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, household_id, property_id, room_id,
		        name, category, manufacturer, model, serial_number,
		        purchase_date, purchase_price, warranty_expiry, notes,
		        created_at, updated_at
		 FROM assets
		 WHERE id = $1 AND household_id = $2`,
		assetID, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("get asset in tx: %w", err)
	}
	defer rows.Close()

	asset, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Asset])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect asset in tx: %w", err)
	}
	return asset, nil
}
