package notification

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the notifications table using a pgx connection pool.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new notification repository backed by the given connection pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// ListUnread returns all unread notifications for a household, ordered by created_at descending.
func (r *Repo) ListUnread(ctx context.Context, householdID string) ([]*Notification, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, type, title, body, entity_type, entity_id, read_at, created_at
		 FROM notifications
		 WHERE household_id = $1 AND read_at IS NULL
		 ORDER BY created_at DESC`,
		householdID,
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

// MarkRead marks a notification as read by setting read_at to the current timestamp.
func (r *Repo) MarkRead(ctx context.Context, id, householdID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE notifications SET read_at = NOW() WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return fmt.Errorf("mark notification as read: %w", err)
	}
	return nil
}
