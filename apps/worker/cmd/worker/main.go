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
	"home-os/notify"
	"home-os/worker/internal/alarms"
	"home-os/worker/internal/billmirror"
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

	// Bills calendar backfill (#4716): bills with no mirror event on the
	// Bills calendar get one, at startup and then hourly. Idempotent.
	billMirrorJob := billmirror.New(pool, billmirror.DefaultInterval)

	var wg sync.WaitGroup
	wg.Add(4)
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
	go func() {
		defer wg.Done()
		if err := billMirrorJob.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("bill calendar backfill exited", "error", err)
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

// billLeadDays and maintenanceDueWindowDays are how far ahead (in whole days,
// inclusive of today) a bill or maintenance task triggers a notification.
//
// billLeadDays is only the lead time for an ORDINARY manual bill (due_soon).
// Which bills earn a reminder at all is decided by billdue.AdviceFor, keyed on
// the shared attention rule (#5498/#5500, see billreminders.go), and
// needs_setup bills are reminded earlier than this on purpose: the family has to
// set something up before the money can move.
const (
	billLeadDays             = 3
	maintenanceDueWindowDays = 7
)

// maintenanceCandidatesSQL lists pending maintenance tasks with no
// maintenance_due notification in the last 24 hours, with their household's
// timezone. Which of them are due within the window is decided in Go by
// dueTasks, with today from households.timezone and the same fallback to UTC
// as bill_due (AT TIME ZONE in SQL would fail the whole query on a bad
// timezone). $1 is now's UTC date and $2 the window: every timezone's local
// date is within a day of the UTC date, so due dates outside
// [$1 - 1, $1 + $2 + 1] can never be in any household's window and are not
// fetched.
const maintenanceCandidatesSQL = `
	SELECT mt.id::text, h.timezone, mt.due_date
	FROM maintenance_tasks mt
	JOIN households h ON h.id = mt.household_id
	WHERE mt.status = 'pending'
	AND mt.due_date BETWEEN $1::date - 1 AND $1::date + $2::int + 1
	AND NOT EXISTS (
		SELECT 1 FROM notifications n
		WHERE n.entity_id = mt.id AND n.type = $3::notification_type
		AND n.created_at > NOW() - INTERVAL '24 hours'
	)
`

// maintenanceDueSQL inserts a maintenance_due notification for each task id
// in $1 chosen by dueTasks. The status and NOT EXISTS checks are repeated so
// a task completed meanwhile is skipped and two overlapping runs still keep
// one maintenance_due per task per 24 hours.
const maintenanceDueSQL = `
	INSERT INTO notifications (household_id, type, title, body, entity_type, entity_id)
	SELECT mt.household_id, $2::notification_type, mt.name || ' due soon',
	       COALESCE(mt.description, ''),
	       'maintenance_task', mt.id
	FROM unnest($1::uuid[]) AS d(task_id)
	JOIN maintenance_tasks mt ON mt.id = d.task_id
	WHERE mt.status = 'pending'
	AND NOT EXISTS (
		SELECT 1 FROM notifications n
		WHERE n.entity_id = mt.id AND n.type = $2::notification_type
		AND n.created_at > NOW() - INTERVAL '24 hours'
	)
	RETURNING id, household_id, type, title, body
`

// maintenanceCandidate is one row of maintenanceCandidatesSQL.
type maintenanceCandidate struct {
	ID       string
	Timezone string
	DueDate  time.Time
}

// dueTasks returns the ids of the candidates whose due_date falls within
// [today, today + windowDays], where today is now's date in each task's
// household timezone (householdToday, UTC for an unknown zone). due_date is a
// DATE, so the window is in whole dates and a task due today is included.
func dueTasks(now time.Time, candidates []maintenanceCandidate, windowDays int) []string {
	var ids []string
	for _, c := range candidates {
		today := householdToday(now, c.Timezone)
		y, m, d := c.DueDate.Date()
		due := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		if !due.Before(today) && !due.After(today.AddDate(0, 0, windowDays)) {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// generateNotifications inserts due-soon notifications for bills and
// maintenance tasks as of now (see insertDueNotifications), then emails them.
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

// insertDueNotifications inserts bill reminders (see billreminders.go: the
// policy keyed on billdue.AttentionFor, with its digest cap and dedupe key) and
// maintenance_due notifications (see dueTasks), both with today taken from now
// in each household's timezone (households.timezone, UTC when unknown), and
// returns the inserted rows.
func insertDueNotifications(ctx context.Context, pool *pgxpool.Pool, now time.Time) ([]notif, error) {
	allNotifs, err := insertBillReminders(ctx, pool, now)
	if err != nil {
		return nil, err
	}

	rows, err := pool.Query(ctx, maintenanceCandidatesSQL,
		now.UTC().Format(time.DateOnly), maintenanceDueWindowDays, notify.TypeMaintenanceDue)
	if err != nil {
		return nil, fmt.Errorf("maintenance notifications: %w", err)
	}
	tasks, err := pgx.CollectRows(rows, pgx.RowToStructByPos[maintenanceCandidate])
	if err != nil {
		return nil, fmt.Errorf("maintenance notifications: %w", err)
	}
	ids := dueTasks(now, tasks, maintenanceDueWindowDays)
	if len(ids) == 0 {
		return allNotifs, nil
	}
	rows, err = pool.Query(ctx, maintenanceDueSQL, ids, notify.TypeMaintenanceDue)
	if err != nil {
		return nil, fmt.Errorf("maintenance notifications: %w", err)
	}
	maintNotifs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[notif])
	if err != nil {
		return nil, fmt.Errorf("maintenance notifications: %w", err)
	}
	return append(allNotifs, maintNotifs...), nil
}

// recipient is one household member a notification email can go to.
type recipient struct {
	Email string
	Name  string
}

// householdMembers returns the active members of a household: the people who
// can still sign in and act on a notification. A deactivated account
// (users.active = false, the flag auth and CalDAV already reject, migration
// 037) keeps its membership row, so it has to be filtered here rather than by
// the membership alone; the in-app notification is suppressed for them on the
// same grounds (alarms.insertSQL).
func householdMembers(ctx context.Context, pool *pgxpool.Pool, householdID string) ([]recipient, error) {
	rows, err := pool.Query(ctx, `
		SELECT u.email, u.name
		FROM memberships m
		JOIN users u ON u.id = m.user_id
		WHERE m.household_id = $1
		  AND u.active
	`, householdID)
	if err != nil {
		return nil, err
	}
	members, err := pgx.CollectRows(rows, pgx.RowToStructByPos[recipient])
	rows.Close()
	if err != nil {
		return nil, err
	}
	return members, nil
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
	hhMembers := make(map[string][]recipient, len(hhIDs))
	for _, hhID := range hhIDs {
		members, err := householdMembers(ctx, pool, hhID)
		if err != nil {
			slog.Error("notification: failed to query household members",
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
