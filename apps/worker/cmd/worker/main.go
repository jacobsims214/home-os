package main

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/worker/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("failed to create pgx pool: %v", err)
	}
	defer pool.Close()

	actorSys := actor.NewActorSystem()
	_ = actorSys // will be used when actors are registered in future tasks

	log.Println("worker started, polling outbox...")

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("worker shutting down")
			return
		case <-ticker.C:
			if err := generateNotifications(ctx, pool); err != nil {
				log.Printf("failed to generate notifications: %v", err)
			}
			log.Println("polling outbox...")
		}
	}
}

func generateNotifications(ctx context.Context, pool *pgxpool.Pool) error {
	// Bills due within 3 days
	_, err := pool.Exec(ctx, `
		INSERT INTO notifications (household_id, type, title, body, entity_type, entity_id)
		SELECT b.household_id, 'bill_due', b.name || ' due soon',
		       'Payment of $' || b.amount || ' due on day ' || b.due_day,
		       'bill', b.id::text
		FROM bills b
		WHERE NOT EXISTS (
			SELECT 1 FROM notifications n
			WHERE n.entity_id = b.id::text AND n.type = 'bill_due'
			AND n.created_at > NOW() - INTERVAL '24 hours'
		)
		AND b.due_day BETWEEN EXTRACT(DAY FROM NOW())::int AND EXTRACT(DAY FROM NOW() + INTERVAL '3 days')::int
	`)
	if err != nil {
		return fmt.Errorf("bill notifications: %w", err)
	}

	// Maintenance tasks due within 7 days
	_, err = pool.Exec(ctx, `
		INSERT INTO notifications (household_id, type, title, body, entity_type, entity_id)
		SELECT mt.household_id, 'maintenance_due', mt.name || ' due soon',
		       COALESCE(mt.description, ''),
		       'maintenance_task', mt.id::text
		FROM maintenance_tasks mt
		WHERE mt.status = 'pending'
		AND mt.due_date BETWEEN NOW() AND NOW() + INTERVAL '7 days'
		AND NOT EXISTS (
			SELECT 1 FROM notifications n
			WHERE n.entity_id = mt.id::text AND n.type = 'maintenance_due'
			AND n.created_at > NOW() - INTERVAL '24 hours'
		)
	`)
	if err != nil {
		return fmt.Errorf("maintenance notifications: %w", err)
	}

	return nil
}
