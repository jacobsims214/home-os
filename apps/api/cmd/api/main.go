// Package main is the entrypoint for the Home OS core API.
// It loads configuration, creates the database pool, sets up the chi router
// with middleware, registers handlers, and starts the HTTP server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"home-os/api/internal/admin"
	"home-os/api/internal/auth"
	"home-os/api/internal/config"
	"home-os/api/internal/db"
	"home-os/api/internal/dex"
	"home-os/api/internal/household"
	"home-os/api/internal/invite"
	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
	"home-os/api/internal/searchsweep"
	"home-os/api/internal/seed"
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
		dexClient, err = dex.NewClient(cfg.DexGRPCAddr, dex.TLSConfigFromEnv())
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
	var bootstrapDex dex.PasswordSetter
	if dexClient != nil { // a nil *dex.Client must not become a non-nil interface
		bootstrapDex = dexClient
	}
	if err := seed.BootstrapAdmin(ctx, pool, cfg, bootstrapDex); err != nil {
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

	// Reconcile the index with Postgres on a timer (#5298). Records reach the
	// database through four writers — these REST handlers, the MCP, CalDAV and
	// the worker — and only the REST handlers index on write, so the index
	// drifts by design. The sweep re-derives it from the source of truth, which
	// is also what backfills a deployment that predates this change. It runs in
	// the background and never fails startup: the index keeps its previous
	// contents and the next tick retries.
	sweepCtx, stopSweep := context.WithCancel(ctx)
	defer stopSweep()
	go func() {
		job := searchsweep.New(pool, searchClient, time.Duration(cfg.SearchReconcileSeconds)*time.Second)
		if err := job.Run(sweepCtx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("search sweep exited", "error", err)
		}
	}()

	// Create the chi router and apply middleware.
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	// CORS: only the public origin (PUBLIC_URL). The UI is served from that
	// same origin through Envoy, so browsers on any other site get no CORS
	// headers. localhost origins are added only with LOCAL_DEV=true.
	r.Use(middleware.CORSFor(middleware.OriginOf(cfg.PublicURL), cfg.LocalDev))

	// Per-client-IP rate limit for the unauthenticated routes below (#4685).
	// In memory per pod (see middleware/ratelimit.go): with 2 replicas a
	// client can get up to twice the budget, which is acceptable here.
	trustedProxies, err := middleware.ParseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		slog.Error("invalid API_TRUSTED_PROXIES", "error", err)
		os.Exit(1)
	}
	publicLimiter := middleware.NewRateLimiter(cfg.PublicRatePerMinute, cfg.PublicRateBurst, trustedProxies)
	limiterCtx, stopLimiter := context.WithCancel(ctx)
	defer stopLimiter()
	go publicLimiter.Run(limiterCtx)
	limited := func(h http.HandlerFunc) http.Handler { return publicLimiter.Middleware(h) }

	// Change-password gets its own limiter instance rather than sharing the
	// public one: it is authenticated, so keying it per client IP alongside
	// signup and forgot-password would let one password change spend the
	// household's signup budget. The handler adds a per-account budget from the
	// same instance, so a stolen session cannot outrun the IP budget by moving
	// between addresses (#4822).
	changePasswordLimiter := middleware.NewRateLimiter(
		cfg.ChangePasswordRatePerMinute, cfg.ChangePasswordRateBurst, trustedProxies)
	go changePasswordLimiter.Run(limiterCtx)

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

	authHandler := auth.NewHandler(authRepo, cfg, smtpClient, dexClient, oidcVerifier).
		WithChangePasswordLimiter(changePasswordLimiter)

	r.Get("/api/v1/auth/me", authHandler.Me)
	r.Post("/api/v1/auth/caldav-password", authHandler.GenerateCaldavPassword)
	r.Method(http.MethodPost, "/api/v1/auth/forgot-password", limited(authHandler.ForgotPassword))
	r.Method(http.MethodPost, "/api/v1/auth/reset-password", limited(authHandler.ResetPassword))

	// Invites. Accept is outside RequireAuth: the invitee is usually not a
	// household member yet (RequireAuth answers 403 for that), so the handler
	// verifies the Dex token itself.
	inviteHandler := invite.NewHandler(invite.NewRepo(pool), cfg, smtpClient).WithVerifier(oidcVerifier)
	if dexClient != nil { // a nil *dex.Client must not become a non-nil interface
		inviteHandler.WithDex(dexClient)
	}
	r.Method(http.MethodPost, "/api/v1/invites/accept", limited(inviteHandler.AcceptInvite))

	// Household handler. Create (onboarding) is outside RequireAuth for the
	// same reason as invite accept: a new Dex user has no membership yet, so
	// the handler verifies the token itself. The /me routes stay protected.
	householdHandler := household.NewHandler(householdRepo).WithVerifier(oidcVerifier)
	r.Method(http.MethodPost, "/api/v1/households", limited(householdHandler.Create))

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

		// Household role policy (#4689): every route below names the
		// resource and action it needs (middleware/roles.go), checked against
		// the one policy table in auth/roles.go. Unmapped routes answer 403.
		r.Use(middleware.RequirePolicy)
		registerProtectedRoutes(r, protectedDeps{
			pool:                  pool,
			cfg:                   cfg,
			search:                searchClient,
			smtp:                  smtpClient,
			householdHandler:      householdHandler,
			inviteHandler:         inviteHandler,
			authHandler:           authHandler,
			changePasswordLimiter: changePasswordLimiter,
		})
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
		// Stop the search sweep before the pool closes, so an in-flight
		// reconcile cannot use a closed connection.
		stopSweep()
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
