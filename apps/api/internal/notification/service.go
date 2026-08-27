package notification

import (
	"fmt"
	"html"
	"log/slog"

	"home-os/api/pkg/smtp"
)

// SendNotificationEmail sends an HTML email for a notification if SMTP is configured.
// It is best-effort — failures are logged but not returned.
// Only bill_due and maintenance_due notification types trigger an email.
func SendNotificationEmail(smtpClient *smtp.Client, userEmail, notifType, title, body string) {
	if smtpClient == nil {
		slog.Debug("notification: smtp not configured, skipping email", "type", notifType)
		return
	}

	if notifType != "bill_due" && notifType != "maintenance_due" {
		return
	}

	subject := fmt.Sprintf("Home OS — %s", title)
	htmlBody := buildNotificationHTML(notifType, title, body)

	if err := smtpClient.SendHTMLEmail(userEmail, subject, htmlBody); err != nil {
		slog.Error("notification: failed to send email",
			"type", notifType,
			"to", userEmail,
			"error", err,
		)
		return
	}

	slog.Info("notification: email sent",
		"type", notifType,
		"to", userEmail,
		"title", title,
	)
}

// buildNotificationHTML creates a simple HTML email body for a notification.
func buildNotificationHTML(notifType, title, body string) string {
	icon := "🔔"
	switch notifType {
	case "bill_due":
		icon = "💰"
	case "maintenance_due":
		icon = "🔧"
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
</html>`, icon, html.EscapeString(title), html.EscapeString(body))
}