// Package main is the entrypoint for the Home OS core API.
// It loads configuration, creates the database pool, sets up the chi router
// with middleware, registers handlers, and starts the HTTP server.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/asset"
	"home-os/api/internal/auth"
	"home-os/api/internal/bill"
	"home-os/api/internal/calendar"
	"home-os/api/internal/config"
	"home-os/api/internal/dex"
	"home-os/api/internal/file"
	"home-os/api/internal/household"
	"home-os/api/internal/integration"
"home-os/api/internal/invite"
	"home-os/api/internal/link"
	"home-os/api/internal/loan"
	"home-os/api/internal/maintenance"
	"home-os/api/internal/middleware"
	"home-os/api/internal/note"
	"home-os/api/internal/notification"
	"home-os/api/internal/pet"
	"home-os/api/internal/property"
	"home-os/api/internal/search"
	"home-os/api/internal/secret"
	"home-os/api/internal/seed"
	"home-os/api/internal/vehicle"
	"home-os/api/internal/vendor"
	"home-os/api/pkg/apierr"
	"home-os/api/pkg/smtp"
)

func main() {
	// Load configuration from environment variables.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// Create the pgx connection pool.
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to create database pool", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Verify the database connection is alive.
	if err := pool.Ping(ctx); err != nil {
		slog.Error("failed to ping database", "error", err)
		os.Exit(1)
	}

	// Create Dex gRPC client for password management.
	// Must be created before SeedDemo and authHandler which may use it.
	var dexClient *dex.Client
	if cfg.DexGRPCAddr != "" {
		dexClient, err = dex.NewClient(cfg.DexGRPCAddr)
		if err != nil {
			slog.Warn("dex: failed to connect, password sync disabled", "addr", cfg.DexGRPCAddr, "error", err)
		} else {
			slog.Info("dex: connected", "addr", cfg.DexGRPCAddr)
			defer dexClient.Close()
		}
	}

	// Seed demo data when DEMO_MODE is enabled.
	if cfg.DemoMode {
		if err := seed.SeedDemo(ctx, pool, dexClient); err != nil {
			slog.Error("failed to seed demo data", "error", err)
			os.Exit(1)
		}
	}

	// Create OIDC token verifier using Dex JWKS endpoint.
	// This validates Dex-issued RS256-signed OIDC tokens on protected routes.
	// The JWKS URL is the internal K8s address for key fetching, while the
	// expected issuer matches the token's "iss" claim (the public URL).
	oidcVerifier, err := auth.NewVerifier(ctx, cfg.DexIssuer, cfg.DexJWKSURL)
	if err != nil {
		slog.Error("failed to create OIDC verifier", "error", err)
		os.Exit(1)
	}
	defer oidcVerifier.Close()

	// Initialize the search client and create the Typesense collection if needed.
	searchClient := search.NewClient(cfg)
	if err := searchClient.InitCollection(ctx); err != nil {
		slog.Warn("search: failed to initialize collection", "error", err)
	}

	// Create the chi router and apply middleware.
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.CORS(true, "")) // dev mode: allow all origins

	// Health endpoint — returns service status.
	r.Get("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		apierr.JSON(w, http.StatusOK, map[string]string{
			"status":  "ok",
			"service": "home-os-api",
		})
	})

	// Auth endpoints.
	authRepo := auth.NewRepo(pool)
	householdRepo := household.NewRepo(pool)

	// Create SMTP client if configured.
	var smtpClient *smtp.Client
	if cfg.SMTPHost != "" {
		smtpClient = smtp.New(smtp.Config{
			Host:        cfg.SMTPHost,
			Port:        cfg.SMTPPort,
			Username:    cfg.SMTPUsername,
			Password:    cfg.SMTPPassword,
			FromAddress: cfg.SMTPFrom,
		})
		slog.Info("SMTP client configured", "host", cfg.SMTPHost)
	} else {
		slog.Info("SMTP not configured; password reset emails will be skipped")
	}

	authHandler := auth.NewHandler(authRepo, &householdAdapter{repo: householdRepo}, cfg, smtpClient, dexClient, oidcVerifier)

	r.Get("/api/v1/auth/me", authHandler.Me)
	r.Post("/api/v1/auth/caldav-password", authHandler.GenerateCaldavPassword)
	r.Post("/api/v1/auth/forgot-password", authHandler.ForgotPassword)
	r.Post("/api/v1/auth/reset-password", authHandler.ResetPassword)

	// Protected endpoints (require valid JWT).
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(oidcVerifier))

		// Household
		householdHandler := household.NewHandler(householdRepo)
		r.Get("/api/v1/households/me", householdHandler.GetMe)
		r.Get("/api/v1/households/me/members", householdHandler.ListMembers)
		r.Patch("/api/v1/households/me/members/{userId}", householdHandler.UpdateMemberRole)
		r.Delete("/api/v1/households/me/members/{userId}", householdHandler.RemoveMember)

		// Asset CRUD
		assetRepo := asset.NewRepo(pool)
		assetHandler := asset.NewHandler(assetRepo, cfg).WithSearchClient(searchClient)
		r.Get("/api/v1/assets", assetHandler.List)
		r.Post("/api/v1/assets", assetHandler.Create)
		r.Get("/api/v1/assets/{id}", assetHandler.Get)
		r.Put("/api/v1/assets/{id}", assetHandler.Update)
		r.Delete("/api/v1/assets/{id}", assetHandler.Delete)

		// Loan CRUD
		loanRepo := loan.NewRepo(pool)
		loanHandler := loan.NewHandler(loanRepo)
		r.Get("/api/v1/loans", loanHandler.List)
		r.Post("/api/v1/loans", loanHandler.Create)
		r.Get("/api/v1/loans/{id}", loanHandler.Get)
		r.Put("/api/v1/loans/{id}", loanHandler.Update)
		r.Delete("/api/v1/loans/{id}", loanHandler.Delete)

		// Vehicle CRUD
		vehicleRepo := vehicle.NewRepo(pool)
		vehicleHandler := vehicle.NewHandler(vehicleRepo).WithSearchClient(searchClient)
		r.Get("/api/v1/vehicles", vehicleHandler.List)
		r.Post("/api/v1/vehicles", vehicleHandler.Create)
		r.Get("/api/v1/vehicles/{id}", vehicleHandler.Get)
		r.Put("/api/v1/vehicles/{id}", vehicleHandler.Update)
		r.Delete("/api/v1/vehicles/{id}", vehicleHandler.Delete)

		// Pet CRUD
		petRepo := pet.NewRepo(pool)
		petHandler := pet.NewHandler(petRepo).WithSearchClient(searchClient)
		r.Get("/api/v1/pets", petHandler.List)
		r.Post("/api/v1/pets", petHandler.Create)
		r.Get("/api/v1/pets/{id}", petHandler.Get)
		r.Put("/api/v1/pets/{id}", petHandler.Update)
		r.Delete("/api/v1/pets/{id}", petHandler.Delete)

		// Vendor CRUD
		vendorRepo := vendor.NewRepo(pool)
		vendorHandler := vendor.NewHandler(vendorRepo).WithSearchClient(searchClient)
		r.Get("/api/v1/vendors", vendorHandler.List)
		r.Post("/api/v1/vendors", vendorHandler.Create)
		r.Get("/api/v1/vendors/{id}", vendorHandler.Get)
		r.Put("/api/v1/vendors/{id}", vendorHandler.Update)
		r.Delete("/api/v1/vendors/{id}", vendorHandler.Delete)

		// Calendar CRUD (must be before maintenance — maintenance handler needs calendarRepo)
		calendarRepo := calendar.NewRepo(pool)
		calendarHandler := calendar.NewHandler(calendarRepo)
		r.Get("/api/v1/calendars", calendarHandler.ListCalendars)
		r.Post("/api/v1/calendars", calendarHandler.CreateCalendar)
		r.Get("/api/v1/calendars/events", calendarHandler.ListAllEvents)
		r.Get("/api/v1/calendars/{id}/events", calendarHandler.ListEvents)
		r.Post("/api/v1/calendars/{id}/events", calendarHandler.CreateEvent)
		r.Delete("/api/v1/calendars/{id}/events/{eventId}", calendarHandler.DeleteEvent)

		// Maintenance CRUD (with bidirectional calendar sync)
		maintenanceRepo := maintenance.NewRepo(pool)
		maintenanceHandler := maintenance.NewHandler(maintenanceRepo).WithCalendarRepo(calendarRepo).WithSearchClient(searchClient)
		r.Get("/api/v1/maintenance/tasks", maintenanceHandler.ListTasks)
		r.Post("/api/v1/maintenance/tasks", maintenanceHandler.CreateTask)
		r.Patch("/api/v1/maintenance/tasks/{id}", maintenanceHandler.UpdateTask)
		r.Delete("/api/v1/maintenance/tasks/{id}", maintenanceHandler.DeleteTask)
		r.Get("/api/v1/maintenance/schedules", maintenanceHandler.ListSchedules)
		r.Post("/api/v1/maintenance/schedules", maintenanceHandler.CreateSchedule)

		// Property and Room CRUD
		propertyRepo := property.NewRepo(pool)
		propertyHandler := property.NewHandler(propertyRepo).WithSearchClient(searchClient)
		r.Get("/api/v1/properties", propertyHandler.ListProperties)
		r.Post("/api/v1/properties", propertyHandler.CreateProperty)
		r.Get("/api/v1/properties/{id}", propertyHandler.GetProperty)
		r.Put("/api/v1/properties/{id}", propertyHandler.UpdateProperty)
		r.Delete("/api/v1/properties/{id}", propertyHandler.DeleteProperty)
		r.Get("/api/v1/properties/{id}/rooms", propertyHandler.ListRooms)
		r.Post("/api/v1/properties/{id}/rooms", propertyHandler.CreateRoom)

		// Bill CRUD
		billRepo := bill.NewRepo(pool)
		billHandler := bill.NewHandler(billRepo).WithSearchClient(searchClient)
		r.Get("/api/v1/bills", billHandler.List)
		r.Post("/api/v1/bills", billHandler.Create)
		r.Get("/api/v1/bills/{id}", billHandler.Get)
		r.Put("/api/v1/bills/{id}", billHandler.Update)
		r.Delete("/api/v1/bills/{id}", billHandler.Delete)

		// Search
		searchHandler := search.NewHandler(searchClient)
		r.Get("/api/v1/search", searchHandler.Search)

		// Note CRUD (polymorphic)
		noteRepo := note.NewRepo(pool)
		noteHandler := note.NewHandler(noteRepo).WithSearchClient(searchClient)
		r.Get("/api/v1/notes", noteHandler.List)
		r.Post("/api/v1/notes", noteHandler.Create)
		r.Delete("/api/v1/notes/{id}", noteHandler.Delete)

		// Notification CRUD
		notificationRepo := notification.NewRepo(pool)
		notificationHandler := notification.NewHandler(notificationRepo, smtpClient)
		r.Get("/api/v1/notifications", notificationHandler.List)
		r.Post("/api/v1/notifications", notificationHandler.Create)
		r.Patch("/api/v1/notifications/{id}/read", notificationHandler.MarkRead)

		// File storage (polymorphic, bytea in Postgres)
		fileRepo := file.NewRepo(pool)
		fileHandler := file.NewHandler(fileRepo)
		r.Post("/api/v1/files/upload", fileHandler.UploadFile)
		r.Get("/api/v1/files", fileHandler.ListFiles)
		r.Get("/api/v1/files/{id}", fileHandler.GetFile)
		r.Get("/api/v1/files/{id}/content", fileHandler.GetFileContent)
		r.Patch("/api/v1/files/{id}", fileHandler.UpdateFile)
		r.Delete("/api/v1/files/{id}", fileHandler.DeleteFile)

		// Secret storage (zero-knowledge, encrypted client-side)
		secretRepo := secret.NewRepo(pool)
		secretHandler := secret.NewHandler(secretRepo).WithSearchClient(searchClient)
		r.Post("/api/v1/secrets", secretHandler.CreateSecret)
		r.Get("/api/v1/secrets", secretHandler.ListSecrets)
		r.Get("/api/v1/secrets/{id}", secretHandler.GetSecret)
		r.Patch("/api/v1/secrets/{id}", secretHandler.UpdateSecret)
		r.Delete("/api/v1/secrets/{id}", secretHandler.DeleteSecret)
		r.Post("/api/v1/secrets/setup", secretHandler.SetupKey)
		r.Post("/api/v1/secrets/verify", secretHandler.VerifyKey)
		r.Get("/api/v1/secrets/key", secretHandler.GetKeyInfo)

		// Link CRUD (polymorphic)
		linkRepo := link.NewRepo(pool)
		linkHandler := link.NewHandler(linkRepo)
		r.Post("/api/v1/links", linkHandler.Create)
		r.Get("/api/v1/links", linkHandler.List)
		r.Delete("/api/v1/links/{id}", linkHandler.Delete)

		// Integration CRUD
		integrationRepo := integration.NewRepo(pool)
		integrationHandler := integration.NewHandler(integrationRepo, cfg)
		r.Get("/api/v1/integrations", integrationHandler.List)
		r.Post("/api/v1/integrations/{type}/connect", integrationHandler.Connect)
		r.Post("/api/v1/integrations/{type}/test", integrationHandler.Test)
		r.Delete("/api/v1/integrations/{type}", integrationHandler.Disconnect)

		// Invite routes
		inviteRepo := invite.NewRepo(pool)
		inviteHandler := invite.NewHandler(inviteRepo, cfg, smtpClient)
		r.Post("/api/v1/invites", inviteHandler.CreateInvite)
		r.Get("/api/v1/invites", inviteHandler.ListInvites)
		r.Post("/api/v1/invites/accept", inviteHandler.AcceptInvite)
	})

	// Start the HTTP server.
	addr := ":" + cfg.Port
	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("shutting down gracefully...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pool.Close()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutdown error", "error", err)
		}
	}()

	slog.Info("starting server", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

// householdAdapter wraps household.Repo to satisfy auth.HouseholdCreator
// without creating an import cycle (auth → household → auth).
type householdAdapter struct {
	repo *household.Repo
}

func (a *householdAdapter) CreateHousehold(ctx context.Context, name string) (string, error) {
	hh, err := a.repo.CreateHousehold(ctx, name)
	if err != nil {
		return "", err
	}
	return hh.ID.String(), nil
}

func (a *householdAdapter) CreateMembership(ctx context.Context, householdID, userID, role string) error {
	hhUUID, err := uuid.Parse(householdID)
	if err != nil {
		return fmt.Errorf("parse household id: %w", err)
	}
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return fmt.Errorf("parse user id: %w", err)
	}
	return a.repo.CreateMembership(ctx, hhUUID, userUUID, role)
}
