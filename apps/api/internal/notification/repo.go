package notification

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/pkg/apierr"
)

// Repo provides read/write access to the notifications table using a pgx connection pool.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new notification repository backed by the given connection pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// ListUnread returns the unread notifications the caller may see, newest
// first: family-wide ones and their own personal ones, limited to what their
// current role may see (visibility.go). userID is nil for a caller without a
// user UUID (family-wide rows only). body may be NULL (calendar alarms),
// read as "".
func (r *Repo) ListUnread(ctx context.Context, householdID string, userID *uuid.UUID, role string) ([]*Notification, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT n.id, n.household_id, n.type, n.title, COALESCE(n.body, ''), n.entity_type, n.entity_id, n.read_at, n.created_at
		 FROM notifications n
		 WHERE `+visibleSQL+` AND n.read_at IS NULL
		 ORDER BY n.created_at DESC`,
		householdID, userID, role,
	)
	if err != nil {
		return nil, fmt.Errorf("list unread notifications: %w", err)
	}
	defer rows.Close()

	notifications, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Notification])
	if err != nil {
		return nil, fmt.Errorf("collect notifications: %w", err)
	}
	return notifications, nil
}

// Create inserts a new notification and returns the created record.
func (r *Repo) Create(ctx context.Context, householdID, notifType, title, body string, entityType, entityID *string) (*Notification, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO notifications (household_id, type, title, body, entity_type, entity_id)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, household_id, type, title, body, entity_type, entity_id, read_at, created_at`,
		householdID, notifType, title, body, entityType, entityID,
	)
	if err != nil {
		return nil, fmt.Errorf("create notification: %w", err)
	}
	defer rows.Close()

	n, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Notification])
	if err != nil {
		return nil, fmt.Errorf("collect created notification: %w", err)
	}
	return n, nil
}

// MarkRead sets read_at on a notification the caller may see (the same rule
// as ListUnread). It returns apierr.ErrNotFound when there is none: another
// family's, another member's personal one, or one their current role can't
// see all look the same.
func (r *Repo) MarkRead(ctx context.Context, id, householdID string, userID *uuid.UUID, role string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE notifications n SET read_at = NOW() WHERE `+visibleSQL+` AND n.id = $4`,
		householdID, userID, role, id,
	)
	if err != nil {
		return fmt.Errorf("mark notification as read: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return apierr.ErrNotFound
	}
	return nil
}

// callerUser returns the caller's user id as a UUID, or nil when the claims
// carry none (local dev mode uses "local-dev").
func callerUser(userID string) *uuid.UUID {
	id, err := uuid.Parse(userID)
	if err != nil {
		return nil
	}
	return &id
}
