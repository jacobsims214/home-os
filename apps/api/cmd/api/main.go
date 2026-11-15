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

	"github.com/go-chi/chi/v5"

	"home-os/api/internal/activity"
	"home-os/api/internal/admin"
	"home-os/api/internal/agenttoken"
	"home-os/api/internal/asset"
	"home-os/api/internal/auth"
	"home-os/api/internal/bill"
	"home-os/api/internal/calendar"
	"home-os/api/internal/config"
	"home-os/api/internal/db"
	"home-os/api/internal/dex"
	"home-os/api/internal/file"
	"home-os/api/internal/guides"
	"home-os/api/internal/household"
	"home-os/api/internal/invite"
	"home-os/api/internal/link"
	"home-os/api/internal/lists"
	"home-os/api/internal/loan"
	"home-os/api/internal/maintenance"
	"home-os/api/internal/meals"
	"home-os/api/internal/middleware"
	"home-os/api/internal/note"
	"home-os/api/internal/notification"
	"home-os/api/internal/people"
	"home-os/api/internal/pet"
	"home-os/api/internal/property"
	"home-os/api/internal/search"
	"home-os/api/internal/secret"
	"home-os/api/internal/seed"
	"home-os/api/internal/vehicle"
	"home-os/api/internal/vendors"
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

	// Create the pgx connection pool. db.NewPool sets app.user_id /
	// app.household_id on each connection from the acquiring context, so
	// writes made for an authenticated request (middleware.RequireAuth puts
	// the member on the context) are attributed by the 050 authorship
	// triggers; startup seeds and other context-less writes stay anonymous.
	ctx := context.Background()
	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
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

	// Bootstrap admin user if this is a fresh installation with no users.
	if err := seed.BootstrapAdmin(ctx, pool, cfg, dexClient); err != nil {
		slog.Error("failed to bootstrap admin", "error", err)
		os.Exit(1)
	}

	// Give every existing family the starter list templates it lacks
	// (idempotent; a no-op once all are seeded).
	// A failure only means templates are missing, so the api still starts.
	if err := seed.BackfillStarterLists(ctx, pool); err != nil {
		slog.Warn("failed to backfill starter lists", "error", err)
	}

	// Create OIDC token verifier using Dex JWKS endpoint.
	// This validates Dex-issued RS256-signed OIDC tokens on protected routes.
	// The JWKS URL is the internal K8s address for key fetching, while the
	// expected issuer matches the token's "iss" claim (the public URL).
	// Tokens must have an "aud" claim matching one of the expected audiences.
	oidcVerifier, err := auth.NewVerifier(ctx, cfg.DexIssuer, cfg.DexJWKSURL, []string{"home-os-ui", "home-os-mcp"})
	if err != nil {
		slog.Error("failed to create OIDC verifier", "error", err)
		os.Exit(1)
	}
	defer oidcVerifier.Close()
	// Resolve Dex identities to Home OS users/households: users.dex_sub first,
	// then email (backfilling dex_sub). Multi-household users pick one per
	// request with X-Household-ID (must be a membership, else 403); without it
	// the oldest membership wins.
	oidcVerifier.WithResolver(auth.NewMemberResolver(pool))

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

	authHandler := auth.NewHandler(authRepo, cfg, smtpClient, dexClient, oidcVerifier)

	r.Get("/api/v1/auth/me", authHandler.Me)
	r.Post("/api/v1/auth/caldav-password", authHandler.GenerateCaldavPassword)
	r.Post("/api/v1/auth/forgot-password", authHandler.ForgotPassword)
	r.Post("/api/v1/auth/reset-password", authHandler.ResetPassword)

	// Invites. Accept is outside RequireAuth: the invitee is usually not a
	// household member yet (RequireAuth answers 403 for that), so the handler
	// verifies the Dex token itself.
	inviteHandler := invite.NewHandler(invite.NewRepo(pool), cfg, smtpClient).WithVerifier(oidcVerifier)
	if dexClient != nil { // a nil *dex.Client must not become a non-nil interface
		inviteHandler.WithDex(dexClient)
	}
	r.Post("/api/v1/invites/accept", inviteHandler.AcceptInvite)

	// Household handler. Create (onboarding) is outside RequireAuth for the
	// same reason as invite accept: a new Dex user has no membership yet, so
	// the handler verifies the token itself. The /me routes stay protected.
	householdHandler := household.NewHandler(householdRepo).WithVerifier(oidcVerifier)
	r.Post("/api/v1/households", householdHandler.Create)

	// Instance admin endpoints (families, users, deactivation). Outside
	// RequireAuth: an instance admin need not be a household member. The
	// handler's RequireInstanceAdmin verifies the Dex token and reads
	// users.is_instance_admin and users.active from the DB on every request.
	adminHandler := admin.NewHandler(admin.NewRepo(pool), oidcVerifier)
	if dexClient != nil { // a nil *dex.Client must not become a non-nil interface
		adminHandler.WithDex(dexClient)
	}
	adminHandler.RegisterRoutes(r)

	// Protected endpoints (require valid JWT).
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(oidcVerifier))

		// Household (Create is registered above, outside this group).
		// /me/households lists the caller's families; X-Household-ID on any
		// request in this group picks one (see middleware.RequireAuth).
		r.Get("/api/v1/me/households", householdHandler.ListMine)
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
		vendorRepo := vendors.NewRepo(pool)
		vendorHandler := vendors.NewHandler(vendorRepo).WithSearchClient(searchClient)
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
		r.Patch("/api/v1/calendars/{id}/events/{eventId}", calendarHandler.UpdateEvent)
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
		r.Get("/api/v1/notes/{id}", noteHandler.Get)
		r.Put("/api/v1/notes/{id}", noteHandler.Update)
		r.Patch("/api/v1/notes/{id}", noteHandler.Update)
		r.Delete("/api/v1/notes/{id}", noteHandler.Delete)

		// Notification CRUD
		notificationRepo := notification.NewRepo(pool)
		notificationHandler := notification.NewHandler(notificationRepo, smtpClient)
		r.Get("/api/v1/notifications", notificationHandler.List)
		r.Post("/api/v1/notifications", notificationHandler.Create)
		r.Patch("/api/v1/notifications/{id}/read", notificationHandler.MarkRead)

		// File storage (polymorphic, bytea in Postgres)
		fileRepo := file.NewRepo(pool)
		fileHandler := file.NewHandler(fileRepo).WithSearch(searchClient)
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

		// Invite routes (accept is registered above, outside this group).
		r.Post("/api/v1/invites", inviteHandler.CreateInvite)
		r.Get("/api/v1/invites", inviteHandler.ListInvites)

		// Agent tokens: each member creates, lists and revokes their own.
		agenttoken.NewHandler(agenttoken.NewRepo(pool)).RegisterRoutes(r)

		// People and preferences (Stage 2, #4463). #4489 may consolidate the
		// Stage 2 route wiring.
		people.NewHandler(people.NewRepo(pool)).RegisterRoutes(r)

		// Shared lists (grocery, packing, chores, ...) and their items.
		lists.NewHandler(lists.NewRepo(pool)).RegisterRoutes(r)

		// Recipes, meal plans and meal feedback (Stage 2, #4475).
		meals.NewHandler(meals.NewRepo(pool)).RegisterRoutes(r)

		// Family change feed (activity_log, migration 050). Any member reads;
		// the roles policy (#4689) plugs in via WithReadPolicy.
		activity.NewHandler(activity.NewRepo(pool)).RegisterRoutes(r)

		// Guides and runbooks with revisions, indexed for hybrid search (#4479).
		guides.NewHandler(guides.NewRepo(pool)).WithSearch(searchClient).RegisterRoutes(r)
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
