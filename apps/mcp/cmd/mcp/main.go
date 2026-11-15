// Package main is the entrypoint for the Home OS MCP server.
// It loads configuration, creates the database pool, sets up the MCP server
// with tool registration, and starts the HTTP server with the stateless Streamable HTTP transport.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/config"
	"home-os/mcp/internal/metadata"
	"home-os/mcp/internal/server"
)

func main() {
	// Load configuration from environment variables.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// Set up structured logging.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.LogLevel),
	})))

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

	// Create the OIDC token verifier using Dex's JWKS endpoint.
	// The JWKS URL is the internal K8s address for key fetching, while the
	// expected issuer matches the token's "iss" claim (the public URL).
	verifier, err := auth.NewVerifier(ctx, cfg.DexIssuer, cfg.DexJWKSURL, pool)
	if err != nil {
		slog.Error("failed to create OIDC verifier", "error", err)
		os.Exit(1)
	}
	defer verifier.Close()

	// Create the MCP server.
	srv := server.NewServer(cfg, pool, verifier)

	registerAll(srv, cfg, pool)

	// Start the metadata server on a separate port for health checks and
	// OAuth protected resource metadata. This runs on a separate port to
	// avoid route conflicts with the MCP handler.
	metadataAddr := fmt.Sprintf(":%s", cfg.MetadataPort)
	metadataSrv := metadata.Start(metadataAddr, cfg)
	slog.Info("metadata server started", "addr", metadataAddr)
	defer func() {
		if err := metadataSrv.Shutdown(context.Background()); err != nil {
			slog.Error("metadata server shutdown error", "error", err)
		}
	}()

	// Start the MCP server with graceful shutdown.
	slog.Info("MCP server initialized", "port", cfg.Port)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

// parseLogLevel converts a log level string to a slog.Level.
func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
