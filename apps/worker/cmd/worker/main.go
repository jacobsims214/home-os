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

// billDueWindowDays and maintenanceDueWindowDays are how far ahead (in whole
// days, inclusive of today) a bill or maintenance task triggers a notification.
const (
	billDueWindowDays        = 3
	maintenanceDueWindowDays = 7
)

// billCandidatesSQL lists every bill with a usable schedule (a monthly bill
// with due_day 1-31, or a quarterly/semiannual/annual/once bill with a
// due_date, migration 044, #4468) and no bill_due notification in the last
// 24 hours, with what the due-date rule needs: the household's timezone (for
// "today"), frequency, due_day, due_date, paid_date and paid_for_due_date
// (migration 052, #4655). $1 is now's UTC date: a once bill due more than a
// day before it can never be in any household's window (every local date is
// within a day of UTC), so it is not fetched. Which of them
// are due within the window is decided in Go by nextUnpaidDue (billdue.go),
// the same rule as the MCP bill tools (apps/mcp/internal/bill.BillDue and
// coveredDueDate), so a bill marked paid with bill_pay is not notified for
// the due date it paid. Doing it in SQL would mean a second copy of
// paidForDueDate in another language, and AT TIME ZONE fails the whole
// query on a bad households.timezone where the MCP tools fall back to UTC.
const billCandidatesSQL = `
	SELECT b.id::text, h.timezone, b.frequency, b.due_day, b.due_date, b.paid_date, b.paid_for_due_date
	FROM bills b
	JOIN households h ON h.id = b.household_id
	WHERE ((b.frequency = 'monthly' AND b.due_day BETWEEN 1 AND 31)
	    OR (b.frequency <> 'monthly' AND b.due_date IS NOT NULL))
	AND NOT (b.frequency = 'once' AND b.due_date < $1::date - 1)
	AND NOT EXISTS (
		SELECT 1 FROM notifications n
		WHERE n.entity_id = b.id AND n.type = 'bill_due'
		AND n.created_at > NOW() - INTERVAL '24 hours'
	)
`

// billDueSQL inserts a bill_due notification for each ($1[i], $2[i]) pair of
// bill id and due date chosen by dueBills. notifications.entity_id is UUID,
// so bill ids are passed and compared as UUIDs (casting them to text fails
// with "operator does not exist: uuid = text"). The NOT EXISTS is repeated
// so two overlapping runs still keep one bill_due per bill per 24 hours.
const billDueSQL = `
	INSERT INTO notifications (household_id, type, title, body, entity_type, entity_id)
	SELECT b.household_id, 'bill_due', b.name || ' due soon',
	       CASE WHEN b.amount IS NULL THEN 'Payment due on ' ELSE 'Payment of $' || b.amount || ' due on ' END
	           || to_char(d.due_date, 'Mon FMDD'),
	       'bill', b.id
	FROM unnest($1::uuid[], $2::date[]) AS d(bill_id, due_date)
	JOIN bills b ON b.id = d.bill_id
	WHERE NOT EXISTS (
		SELECT 1 FROM notifications n
		WHERE n.entity_id = b.id AND n.type = 'bill_due'
		AND n.created_at > NOW() - INTERVAL '24 hours'
	)
	RETURNING id, household_id, type, title, body
`

// billCandidate is one row of billCandidatesSQL.
type billCandidate struct {
	ID        string
	Timezone  string
	Frequency string
	DueDay    *int
	DueDate   *time.Time
	PaidDate  *time.Time
	PaidFor   *time.Time
}

// dueBills returns the ids and next unpaid due dates of the candidates whose
// due date falls within [today, today + windowDays], where today is now's
// date in each bill's household timezone.
//
// A monthly bill's due_day is a day of the month (1-31), so the next due
// date can be in this month or the next one: on Nov 29 a bill due on the 1st
// is due Dec 1. The day is clamped to the month's length, so due_day 31 is
// due Feb 28 (or 29), and Apr 30. A quarterly, semiannual or annual bill is
// due every 3/6/12 months on its due_date's day; a once bill on due_date
// only. A payment that covers that due date moves the bill on to the
// following one, which is at least 28 days away and so never notifies (a
// paid once bill has none). An unpaid once bill whose date has passed is
// overdue, not due soon, so it is not notified again.
func dueBills(now time.Time, candidates []billCandidate, windowDays int) (ids []string, dueDates []time.Time) {
	for _, c := range candidates {
		today := householdToday(now, c.Timezone)
		s := schedule{frequency: c.Frequency, dueDay: c.DueDay, dueDate: c.DueDate}
		next, ok := nextUnpaidDue(today, s, c.PaidDate, c.PaidFor)
		if ok && !next.Before(today) && !next.After(today.AddDate(0, 0, windowDays)) {
			ids = append(ids, c.ID)
			dueDates = append(dueDates, next)
		}
	}
	return ids, dueDates
}

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
		WHERE n.entity_id = mt.id AND n.type = 'maintenance_due'
		AND n.created_at > NOW() - INTERVAL '24 hours'
	)
`

// maintenanceDueSQL inserts a maintenance_due notification for each task id
// in $1 chosen by dueTasks. The status and NOT EXISTS checks are repeated so
// a task completed meanwhile is skipped and two overlapping runs still keep
// one maintenance_due per task per 24 hours.
const maintenanceDueSQL = `
	INSERT INTO notifications (household_id, type, title, body, entity_type, entity_id)
	SELECT mt.household_id, 'maintenance_due', mt.name || ' due soon',
	       COALESCE(mt.description, ''),
	       'maintenance_task', mt.id
	FROM unnest($1::uuid[]) AS d(task_id)
	JOIN maintenance_tasks mt ON mt.id = d.task_id
	WHERE mt.status = 'pending'
	AND NOT EXISTS (
		SELECT 1 FROM notifications n
		WHERE n.entity_id = mt.id AND n.type = 'maintenance_due'
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

// insertDueNotifications inserts bill_due notifications (see dueBills) and
// maintenance_due notifications (see dueTasks), both with today taken from
// now in each household's timezone (households.timezone, UTC when unknown),
// and returns the inserted rows.
func insertDueNotifications(ctx context.Context, pool *pgxpool.Pool, now time.Time) ([]notif, error) {
	rows, err := pool.Query(ctx, billCandidatesSQL, now.UTC().Format(time.DateOnly))
	if err != nil {
		return nil, fmt.Errorf("bill notifications: %w", err)
	}
	candidates, err := pgx.CollectRows(rows, pgx.RowToStructByPos[billCandidate])
	if err != nil {
		return nil, fmt.Errorf("bill notifications: %w", err)
	}

	var allNotifs []notif
	if ids, dueDates := dueBills(now, candidates, billDueWindowDays); len(ids) > 0 {
		rows, err = pool.Query(ctx, billDueSQL, ids, dueDates)
		if err != nil {
			return nil, fmt.Errorf("bill notifications: %w", err)
		}
		allNotifs, err = pgx.CollectRows(rows, pgx.RowToStructByPos[notif])
		if err != nil {
			return nil, fmt.Errorf("bill notifications: %w", err)
		}
	}

	rows, err = pool.Query(ctx, maintenanceCandidatesSQL,
		now.UTC().Format(time.DateOnly), maintenanceDueWindowDays)
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
	rows, err = pool.Query(ctx, maintenanceDueSQL, ids)
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
