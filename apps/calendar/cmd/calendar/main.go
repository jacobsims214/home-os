// Command calendar is the CalDAV calendar service for Home OS.
// It serves a CalDAV-compatible HTTP API on the configured port.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/calendar/internal/auth"
	"home-os/calendar/internal/caldav"
	"home-os/calendar/internal/config"
	"home-os/calendar/internal/db"
	"home-os/calendar/internal/logging"
	"home-os/calendar/internal/middleware"
	"home-os/calendar/internal/metrics"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "calendar: %v\n", err)
		os.Exit(1)
	}

	// Configure structured logging
	logging.Init("info")

	// Create database connection pool
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logging.Logger.Error("calendar: failed to create database pool", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Verify database connection
	if err := pool.Ping(ctx); err != nil {
		logging.Logger.Error("calendar: failed to ping database", "error", err)
		os.Exit(1)
	}

	// Create repository and CalDAV handler
	repo, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logging.Logger.Error("calendar: failed to create db repo", "error", err)
		os.Exit(1)
	}
	defer repo.Close()
	caldavHandler := caldav.NewHandler(repo)

	// Create the router
	mux := http.NewServeMux()

	// Health endpoint (no auth required)
	mux.HandleFunc("GET /health", healthHandler)

	// Well-known CalDAV redirect
	mux.HandleFunc("GET /.well-known/caldav", caldav.RedirectToWellKnown)

	// CalDAV endpoints (with auth middleware)
	caldavRouter := auth.AuthMiddleware(repo)(caldavHandler)
	mux.Handle("/dav/", caldavRouter)

	// Apply request body size limits
	wrappedMux := middleware.BodyLimitMiddleware(1024 * 1024)(mux)

	// Apply metrics middleware
	wrappedMux = metrics.Middleware(wrappedMux)

	addr := fmt.Sprintf(":%d", cfg.Port)
	logging.Logger.Info("calendar: starting CalDAV server", "addr", addr)

	server := &http.Server{
		Addr:              addr,
		Handler:           wrappedMux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		logging.Logger.Error("calendar: server error", "error", err)
		os.Exit(1)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"service": "home-os-calendar",
	})
}
