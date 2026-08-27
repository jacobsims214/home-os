package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/pkg/smtp"
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

	// SMTP client is optional — if SMTP_HOST is empty, no email is sent
	var smtpClient *smtp.Client
	if cfg.SMTPHost != "" {
		smtpClient = smtp.New(smtp.Config{
			Host:        cfg.SMTPHost,
			Port:        cfg.SMTPPort,
			Username:    cfg.SMTPUsername,
			Password:    cfg.SMTPPassword,
			FromAddress: cfg.SMTPFrom,
		})
		slog.Info("smtp client configured", "host", cfg.SMTPHost)
	} else {
		slog.Info("SMTP_HOST not set; notification emails disabled")
	}

	log.Println("worker started, polling outbox...")

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("worker shutting down")
			return
		case <-ticker.C:
			if err := generateNotifications(ctx, pool, smtpClient); err != nil {
				log.Printf("failed to generate notifications: %v", err)
			}
			log.Println("polling outbox...")
		}
	}
}

// notif holds a single row returned by the INSERT … RETURNING clause.
type notif struct {
	ID          string
	HouseholdID string
	Type        string
	Title       string
	Body        string
}

func generateNotifications(ctx context.Context, pool *pgxpool.Pool, smtpClient *smtp.Client) error {
	var allNotifs []notif

	// Bills due within 3 days
	rows, err := pool.Query(ctx, `
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
		RETURNING id, household_id, type, title, body
	`)
	if err != nil {
		return fmt.Errorf("bill notifications: %w", err)
	}
	billNotifs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[notif])
	rows.Close()
	if err != nil {
		return fmt.Errorf("bill notifications collect: %w", err)
	}
	allNotifs = append(allNotifs, billNotifs...)

	// Maintenance tasks due within 7 days
	rows, err = pool.Query(ctx, `
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
		RETURNING id, household_id, type, title, body
	`)
	if err != nil {
		return fmt.Errorf("maintenance notifications: %w", err)
	}
	maintNotifs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[notif])
	rows.Close()
	if err != nil {
		return fmt.Errorf("maintenance notifications collect: %w", err)
	}
	allNotifs = append(allNotifs, maintNotifs...)

	if len(allNotifs) == 0 {
		return nil
	}

	// Best-effort email delivery — never block on send failures.
	sendNotificationEmails(ctx, pool, smtpClient, allNotifs)

	return nil
}

// sendNotificationEmails sends HTML email notifications to all household members.
// It is best-effort: failures are logged but never returned.
func sendNotificationEmails(ctx context.Context, pool *pgxpool.Pool, smtpClient *smtp.Client, notifs []notif) {
	if smtpClient == nil {
		slog.Debug("notification: smtp not configured, skipping email")
		return
	}

	// Collect unique household IDs to avoid querying the same household twice.
	seen := make(map[string]bool)
	var hhIDs []string
	for _, n := range notifs {
		if !seen[n.HouseholdID] {
			seen[n.HouseholdID] = true
			hhIDs = append(hhIDs, n.HouseholdID)
		}
	}

	// Pre-load member emails for all affected households.
	type member struct {
		Email string
		Name  string
	}
	hhMembers := make(map[string][]member, len(hhIDs))
	for _, hhID := range hhIDs {
		mRows, err := pool.Query(ctx, `
			SELECT u.email, u.name
			FROM memberships m
			JOIN users u ON u.id = m.user_id
			WHERE m.household_id = $1
		`, hhID)
		if err != nil {
			slog.Error("notification: failed to query household members",
				"household_id", hhID, "error", err)
			continue
		}
		members, err := pgx.CollectRows(mRows, pgx.RowToStructByPos[member])
		mRows.Close()
		if err != nil {
			slog.Error("notification: failed to collect household members",
				"household_id", hhID, "error", err)
			continue
		}
		hhMembers[hhID] = members
	}

	// Send email for each notification to each household member.
	for _, n := range notifs {
		members, ok := hhMembers[n.HouseholdID]
		if !ok || len(members) == 0 {
			continue
		}
		subject := fmt.Sprintf("Home OS — %s", n.Title)
		htmlBody := buildNotificationHTML(n.Type, n.Title, n.Body)
		for _, m := range members {
			if err := smtpClient.SendHTMLEmail(m.Email, subject, htmlBody); err != nil {
				slog.Error("notification: failed to send email",
					"type", n.Type,
					"to", m.Email,
					"error", err,
				)
				continue
			}
			slog.Info("notification: email sent",
				"type", n.Type,
				"to", m.Email,
				"title", n.Title,
			)
		}
	}
}

// buildNotificationHTML creates a simple HTML email body for a notification.
func buildNotificationHTML(notifType, title, body string) string {
	icon := "&#128276;" // bell
	switch notifType {
	case "bill_due":
		icon = "&#128176;" // money bag
	case "maintenance_due":
		icon = "&#128295;" // wrench
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; margin: 0; padding: 0; background-color: #f4f4f5; }
    .container { max-width: 560px; margin: 24px auto; background: #ffffff; border-radius: 12px; overflow: hidden; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
    .header { background: #18181b; color: #ffffff; padding: 24px; text-align: center; }
    .header h1 { margin: 0; font-size: 20px; font-weight: 600; }
    .body { padding: 24px; }
    .icon { font-size: 48px; text-align: center; margin-bottom: 16px; }
    .title { font-size: 18px; font-weight: 600; color: #18181b; margin-bottom: 8px; }
    .message { font-size: 14px; color: #52525b; line-height: 1.6; }
    .footer { padding: 16px 24px; background: #f4f4f5; font-size: 12px; color: #a1a1aa; text-align: center; }
  </style>
</head>
<body>
  <div class="container">
    <div class="header">
      <h1>Home OS Notification</h1>
    </div>
    <div class="body">
      <div class="icon">%s</div>
      <div class="title">%s</div>
      <div class="message">%s</div>
    </div>
    <div class="footer">
      This is an automated message from your Home OS system.
    </div>
  </div>
</body>
</html>`, icon, title, body)
}
