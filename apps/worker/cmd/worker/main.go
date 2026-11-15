package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/pkg/smtp"
	"home-os/worker/internal/alarms"
	"home-os/worker/internal/config"
	"home-os/worker/internal/fileprocessor"
	"home-os/worker/internal/files"
	"home-os/worker/internal/search"
	"home-os/worker/internal/tika"
)

// shutdownTimeout bounds how long main waits for the background jobs to
// return after SIGINT/SIGTERM. All jobs watch ctx, and in-flight Tika and
// DB calls are bound to the same ctx, so they normally stop within
// milliseconds; the bound only matters if something ignores cancellation.
// It sits below Kubernetes' default 30s terminationGracePeriodSeconds so the
// pool is closed and the process exits before SIGKILL.
const shutdownTimeout = 20 * time.Second

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

	// File OCR processor: Tika extraction + Typesense `files` indexing.
	// InitCollection never fails hard (it logs and returns nil when
	// Typesense is unreachable), so a search outage never stops OCR.
	searchClient := search.NewClient(cfg.TypesenseHost, cfg.TypesensePort, cfg.TypesenseAPIKey)
	if err := searchClient.InitCollection(ctx); err != nil {
		slog.Warn("typesense files collection init failed; indexing may fail until it exists", "error", err)
	}
	processor := fileprocessor.New(
		files.NewRepo(pool),
		tika.NewClient(tika.WithBaseURL(cfg.TikaURL)),
		fileprocessor.WithInterval(cfg.FileProcessorPollInterval),
		fileprocessor.WithSearchClient(searchClient),
	)
	slog.Info("file processor configured", "tika_url", cfg.TikaURL, "interval", cfg.FileProcessorPollInterval)

	// Calendar alarms: VALARMs in calendar_objects.ical_raw become in-app
	// notifications, one per household member per firing (deduped in DB).
	alarmJob := alarms.New(pool)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		runNotificationLoop(ctx, pool, smtpClient, cfg.PollInterval)
	}()
	go func() {
		defer wg.Done()
		if err := processor.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("file processor exited", "error", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := alarmJob.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("alarm job exited", "error", err)
		}
	}()

	<-ctx.Done()
	slog.Info("worker shutting down")

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		slog.Info("worker stopped")
	case <-time.After(shutdownTimeout):
		slog.Warn("worker jobs did not stop in time; exiting anyway", "timeout", shutdownTimeout)
	}
}

// runNotificationLoop polls for due bills / maintenance tasks every interval
// and inserts notifications (plus best-effort emails) until ctx is cancelled.
func runNotificationLoop(ctx context.Context, pool *pgxpool.Pool, smtpClient *smtp.Client, interval time.Duration) {
	log.Println("worker started, polling outbox...")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("notification loop stopped")
			return
		case <-ticker.C:
			if err := generateNotifications(ctx, pool, smtpClient, time.Now()); err != nil {
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

// billDueWindowDays and maintenanceDueWindowDays are how far ahead (in whole
// days, inclusive of today) a bill or maintenance task triggers a notification.
const (
	billDueWindowDays        = 3
	maintenanceDueWindowDays = 7
)

// billDueSQL inserts a bill_due notification for every bill whose next due
// date falls within [today, today + $2 days].
//
// bills.due_day is a day of the month (1-31), so the next due date can be in
// this month or the next one: on Nov 29 a bill due on the 1st is due Dec 1.
// For both months the day is clamped to the month's length, so due_day 31 is
// due Feb 28 (or 29), and Apr 30. Comparing day numbers directly breaks every
// time the window wraps a month end.
//
// notifications.entity_id is UUID, so bill ids are inserted and compared as
// UUIDs (casting them to text fails with "operator does not exist: uuid = text").
// The NOT EXISTS keeps one bill_due per bill per 24 hours.
const billDueSQL = `
	INSERT INTO notifications (household_id, type, title, body, entity_type, entity_id)
	SELECT b.household_id, 'bill_due', b.name || ' due soon',
	       CASE WHEN b.amount IS NULL THEN 'Payment due on ' ELSE 'Payment of $' || b.amount || ' due on ' END
	           || to_char(nd.due_date, 'Mon FMDD'),
	       'bill', b.id
	FROM bills b
	CROSS JOIN LATERAL (
		SELECT MIN(o.due_date) AS due_date
		FROM (
			SELECT (ms.month_start
			        + (LEAST(b.due_day, EXTRACT(DAY FROM ms.month_start + INTERVAL '1 month' - INTERVAL '1 day')::int) - 1)
			          * INTERVAL '1 day')::date AS due_date
			FROM (
				SELECT date_trunc('month', $1::date) + m * INTERVAL '1 month' AS month_start
				FROM generate_series(0, 1) AS m
			) ms
		) o
		WHERE o.due_date BETWEEN $1::date AND $1::date + $2::int
	) nd
	WHERE b.due_day BETWEEN 1 AND 31
	AND nd.due_date IS NOT NULL
	AND NOT EXISTS (
		SELECT 1 FROM notifications n
		WHERE n.entity_id = b.id AND n.type = 'bill_due'
		AND n.created_at > NOW() - INTERVAL '24 hours'
	)
	RETURNING id, household_id, type, title, body
`

// maintenanceDueSQL inserts a maintenance_due notification for every pending
// task whose due_date falls within [today, today + $2 days]. due_date is a
// DATE, so the window is in dates: comparing it with NOW() would skip tasks
// due today (midnight is earlier than NOW()).
const maintenanceDueSQL = `
	INSERT INTO notifications (household_id, type, title, body, entity_type, entity_id)
	SELECT mt.household_id, 'maintenance_due', mt.name || ' due soon',
	       COALESCE(mt.description, ''),
	       'maintenance_task', mt.id
	FROM maintenance_tasks mt
	WHERE mt.status = 'pending'
	AND mt.due_date BETWEEN $1::date AND $1::date + $2::int
	AND NOT EXISTS (
		SELECT 1 FROM notifications n
		WHERE n.entity_id = mt.id AND n.type = 'maintenance_due'
		AND n.created_at > NOW() - INTERVAL '24 hours'
	)
	RETURNING id, household_id, type, title, body
`

// generateNotifications inserts due-soon notifications for bills and
// maintenance tasks as of the calendar day of now, then emails them.
func generateNotifications(ctx context.Context, pool *pgxpool.Pool, smtpClient *smtp.Client, now time.Time) error {
	notifs, err := insertDueNotifications(ctx, pool, now)
	if err != nil {
		return err
	}
	if len(notifs) == 0 {
		return nil
	}

	// Best-effort email delivery — never block on send failures.
	sendNotificationEmails(ctx, pool, smtpClient, notifs)
	return nil
}

// insertDueNotifications runs the bill and maintenance queries for the
// calendar day of now (in now's location) and returns the inserted rows.
func insertDueNotifications(ctx context.Context, pool *pgxpool.Pool, now time.Time) ([]notif, error) {
	today := now.Format(time.DateOnly)

	rows, err := pool.Query(ctx, billDueSQL, today, billDueWindowDays)
	if err != nil {
		return nil, fmt.Errorf("bill notifications: %w", err)
	}
	allNotifs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[notif])
	if err != nil {
		return nil, fmt.Errorf("bill notifications: %w", err)
	}

	rows, err = pool.Query(ctx, maintenanceDueSQL, today, maintenanceDueWindowDays)
	if err != nil {
		return nil, fmt.Errorf("maintenance notifications: %w", err)
	}
	maintNotifs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[notif])
	if err != nil {
		return nil, fmt.Errorf("maintenance notifications: %w", err)
	}
	return append(allNotifs, maintNotifs...), nil
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
