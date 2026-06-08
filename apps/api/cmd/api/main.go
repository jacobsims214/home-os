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
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/asset"
	"home-os/api/internal/auth"
	"home-os/api/internal/bill"
	"home-os/api/internal/config"
	"home-os/api/internal/household"
	"home-os/api/internal/maintenance"
	"home-os/api/internal/middleware"
	"home-os/api/internal/pet"
	"home-os/api/internal/property"
	"home-os/api/internal/search"
	"home-os/api/internal/seed"
	"home-os/api/internal/vehicle"
	"home-os/api/internal/vendor"
	"home-os/api/pkg/apierr"
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

	// Seed demo data when DEMO_MODE is enabled.
	if cfg.DemoMode {
		if err := seed.SeedDemo(ctx, pool); err != nil {
			slog.Error("failed to seed demo data", "error", err)
			os.Exit(1)
		}
	}

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
	authHandler := auth.NewHandler(authRepo, householdRepo, cfg)

	r.Post("/api/v1/auth/register", authHandler.Register)
	r.Post("/api/v1/auth/login", authHandler.Login)
	r.Get("/api/v1/auth/me", authHandler.Me)

	// Protected endpoints (require valid JWT).
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(cfg))

		// Asset CRUD
		assetRepo := asset.NewRepo(pool)
		assetHandler := asset.NewHandler(assetRepo, cfg)
		r.Get("/api/v1/assets", assetHandler.List)
		r.Post("/api/v1/assets", assetHandler.Create)
		r.Get("/api/v1/assets/{id}", assetHandler.Get)
		r.Put("/api/v1/assets/{id}", assetHandler.Update)
		r.Delete("/api/v1/assets/{id}", assetHandler.Delete)

		// Vehicle CRUD
		vehicleRepo := vehicle.NewRepo(pool)
		vehicleHandler := vehicle.NewHandler(vehicleRepo)
		r.Get("/api/v1/vehicles", vehicleHandler.List)
		r.Post("/api/v1/vehicles", vehicleHandler.Create)
		r.Get("/api/v1/vehicles/{id}", vehicleHandler.Get)
		r.Put("/api/v1/vehicles/{id}", vehicleHandler.Update)
		r.Delete("/api/v1/vehicles/{id}", vehicleHandler.Delete)

		// Pet CRUD
		petRepo := pet.NewRepo(pool)
		petHandler := pet.NewHandler(petRepo)
		r.Get("/api/v1/pets", petHandler.List)
		r.Post("/api/v1/pets", petHandler.Create)
		r.Get("/api/v1/pets/{id}", petHandler.Get)
		r.Put("/api/v1/pets/{id}", petHandler.Update)
		r.Delete("/api/v1/pets/{id}", petHandler.Delete)

		// Vendor CRUD
		vendorRepo := vendor.NewRepo(pool)
		vendorHandler := vendor.NewHandler(vendorRepo)
		r.Get("/api/v1/vendors", vendorHandler.List)
		r.Post("/api/v1/vendors", vendorHandler.Create)
		r.Get("/api/v1/vendors/{id}", vendorHandler.Get)
		r.Put("/api/v1/vendors/{id}", vendorHandler.Update)
		r.Delete("/api/v1/vendors/{id}", vendorHandler.Delete)

		// Maintenance
		maintenanceRepo := maintenance.NewRepo(pool)
		maintenanceHandler := maintenance.NewHandler(maintenanceRepo)
		r.Get("/api/v1/maintenance/tasks", maintenanceHandler.ListTasks)
		r.Post("/api/v1/maintenance/tasks", maintenanceHandler.CreateTask)
		r.Patch("/api/v1/maintenance/tasks/{id}", maintenanceHandler.UpdateTask)
		r.Get("/api/v1/maintenance/schedules", maintenanceHandler.ListSchedules)
		r.Post("/api/v1/maintenance/schedules", maintenanceHandler.CreateSchedule)

		// Property and Room CRUD
		propertyRepo := property.NewRepo(pool)
		propertyHandler := property.NewHandler(propertyRepo)
		r.Get("/api/v1/properties", propertyHandler.ListProperties)
		r.Post("/api/v1/properties", propertyHandler.CreateProperty)
		r.Get("/api/v1/properties/{id}", propertyHandler.GetProperty)
		r.Put("/api/v1/properties/{id}", propertyHandler.UpdateProperty)
		r.Delete("/api/v1/properties/{id}", propertyHandler.DeleteProperty)
		r.Get("/api/v1/properties/{id}/rooms", propertyHandler.ListRooms)
		r.Post("/api/v1/properties/{id}/rooms", propertyHandler.CreateRoom)

		// Bill CRUD
		billRepo := bill.NewRepo(pool)
		billHandler := bill.NewHandler(billRepo)
		r.Get("/api/v1/bills", billHandler.List)
		r.Post("/api/v1/bills", billHandler.Create)
		r.Get("/api/v1/bills/{id}", billHandler.Get)
		r.Put("/api/v1/bills/{id}", billHandler.Update)
		r.Delete("/api/v1/bills/{id}", billHandler.Delete)

		// Search
		searchHandler := search.NewHandler(searchClient)
		r.Get("/api/v1/search", searchHandler.Search)
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
