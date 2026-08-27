package invite

import (
	"context"
	"encoding/json"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"home-os/api/internal/config"
	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
	"home-os/api/pkg/smtp"
)

type Handler struct {
	repo *Repo
	cfg  *config.Config
	smtp *smtp.Client
}

func NewHandler(repo *Repo, cfg *config.Config, smtpClient *smtp.Client) *Handler {
	return &Handler{repo: repo, cfg: cfg, smtp: smtpClient}
}

func (h *Handler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Unauthorized(w, "not authenticated")
		return
	}

	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	if req.Email == "" {
		apierr.BadRequest(w, "email is required")
		return
	}
	if req.Role == "" {
		req.Role = "family_member"
	}

	token := uuid.New().String()
	expiresAt := time.Now().Add(7 * 24 * time.Hour)

	userID, err := uuid.Parse(claims.UserID)
	if err != nil {
		apierr.BadRequest(w, "invalid user ID")
		return
	}
	inv, err := h.repo.Create(r.Context(), claims.HouseholdID, req.Email, token, req.Role, userID, expiresAt)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Send invitation email best-effort, off the request path.
	go h.sendInviteEmail(context.WithoutCancel(r.Context()), inv, req.Email)

	apierr.JSON(w, http.StatusCreated, map[string]any{"data": inv})
}

// sendInviteEmail sends an HTML invitation email if SMTP is configured.
// This is best-effort: failures are logged but never returned to the caller.
func (h *Handler) sendInviteEmail(ctx context.Context, inv *Invitation, toEmail string) {
	if h.smtp == nil {
		slog.Info("invite: SMTP not configured, skipping email", "email", toEmail)
		return
	}

	// Bound the goroutine lifetime so it never leaks.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	householdName, err := h.repo.GetHouseholdName(ctx, inv.HouseholdID)
	if err != nil {
		slog.Warn("invite: failed to get household name for email", "error", err, "household_id", inv.HouseholdID)
		householdName = "your household"
	}

	// Prevent SMTP header injection in the subject line.
	subjectName := strings.NewReplacer("\r", " ", "\n", " ").Replace(householdName)

	// Prevent HTML injection in the body.
	escapedName := html.EscapeString(householdName)

	acceptLink := h.cfg.PublicURL + "/invites/accept?token=" + inv.Token

	subject := "You're invited to " + subjectName
	htmlBody := `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"></head>
<body style="font-family: sans-serif; padding: 20px;">
	<h2>You're invited to ` + escapedName + `</h2>
	<p>You have been invited to join <strong>` + escapedName + `</strong> on Home OS.</p>
	<p>
		<a href="` + acceptLink + `"
		   style="display: inline-block; padding: 12px 24px; background-color: #4F46E5; color: #fff; text-decoration: none; border-radius: 6px;">
			Accept Invitation
		</a>
	</p>
	<p>Or copy this link into your browser:</p>
	<p><a href="` + acceptLink + `">` + acceptLink + `</a></p>
	<p>This invitation expires in 7 days.</p>
</body>
</html>`

	if err := h.smtp.SendHTMLEmail(toEmail, subject, htmlBody); err != nil {
		slog.Warn("invite: failed to send email", "error", err, "email", toEmail)
		return
	}
	slog.Info("invite: invitation email sent", "email", toEmail)
}

func (h *Handler) ListInvites(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Unauthorized(w, "not authenticated")
		return
	}

	invs, err := h.repo.ListByHousehold(r.Context(), claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if invs == nil {
		invs = []Invitation{}
	}

	apierr.JSON(w, http.StatusOK, map[string]any{"data": invs})
}

func (h *Handler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	inv, err := h.repo.GetByToken(r.Context(), req.Token)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if inv == nil {
		apierr.NotFound(w, "invalid or expired invitation")
		return
	}

	if inv.AcceptedAt != nil {
		apierr.Conflict(w, "invitation already accepted")
		return
	}

	if time.Now().After(inv.ExpiresAt) {
		apierr.BadRequest(w, "invitation has expired")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{"data": inv})
}
