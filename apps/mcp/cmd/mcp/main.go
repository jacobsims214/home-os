// Package main is the entrypoint for the Home OS MCP server.
// It loads configuration, creates the database pool, sets up the MCP server
// with tool registration, and starts the HTTP server with SSE transport.
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
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/asset"
	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/bill"
	"home-os/mcp/internal/calendar"
	"home-os/mcp/internal/config"
	"home-os/mcp/internal/file"
	"home-os/mcp/internal/finance"
	"home-os/mcp/internal/household"
	"home-os/mcp/internal/loan"
	"home-os/mcp/internal/maintenance"
	"home-os/mcp/internal/metadata"
	"home-os/mcp/internal/note"
	"home-os/mcp/internal/notification"
	"home-os/mcp/internal/pet"
	"home-os/mcp/internal/prompts"
	"home-os/mcp/internal/property"
	"home-os/mcp/internal/resources"
	"home-os/mcp/internal/search"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/vehicle"
	"home-os/mcp/internal/vendor"
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

	// Register the "ping" test tool.
	pingTool := mcp.NewTool("ping",
		mcp.WithDescription("A simple test tool that returns pong to verify the server is running"),
	)
	srv.RegisterTool("ping", pingTool, handlePing)

	// Register file tools.
	fileListName, fileListTool, fileListHandler := file.NewListFilesTool(pool)
	srv.RegisterTool(fileListName, fileListTool, fileListHandler)
	fileUploadName, fileUploadTool, fileUploadHandler := file.NewUploadFileTool(pool)
	srv.RegisterTool(fileUploadName, fileUploadTool, fileUploadHandler)
	fileGetName, fileGetTool, fileGetHandler := file.NewGetFileTool(pool)
	srv.RegisterTool(fileGetName, fileGetTool, fileGetHandler)
	fileSearchName, fileSearchTool, fileSearchHandler := file.NewSearchFilesTool(pool)
	srv.RegisterTool(fileSearchName, fileSearchTool, fileSearchHandler)

	// Register note tools.
	noteListName, noteListTool, noteListHandler := note.NewListNotesTool(pool)
	srv.RegisterTool(noteListName, noteListTool, noteListHandler)
	noteCreateName, noteCreateTool, noteCreateHandler := note.NewCreateNoteTool(pool)
	srv.RegisterTool(noteCreateName, noteCreateTool, noteCreateHandler)

	// Register search tool.
	searchName, searchTool, searchHandler := search.NewSearchTool(pool)
	srv.RegisterTool(searchName, searchTool, searchHandler)

	// Register bill tools.
	billListName, billListTool, billListHandler := bill.NewListBillsTool(pool)
	srv.RegisterTool(billListName, billListTool, billListHandler)
	billUpcomingName, billUpcomingTool, billUpcomingHandler := bill.NewGetUpcomingBillsTool(pool)
	srv.RegisterTool(billUpcomingName, billUpcomingTool, billUpcomingHandler)
	billSummaryName, billSummaryTool, billSummaryHandler := bill.NewGetBillSummaryTool(pool)
	srv.RegisterTool(billSummaryName, billSummaryTool, billSummaryHandler)

	// Register maintenance tools.
	maintListName, maintListTool, maintListHandler := maintenance.NewListMaintenanceTasksTool(pool)
	srv.RegisterTool(maintListName, maintListTool, maintListHandler)
	maintUpcomingName, maintUpcomingTool, maintUpcomingHandler := maintenance.NewGetUpcomingMaintenanceTool(pool)
	srv.RegisterTool(maintUpcomingName, maintUpcomingTool, maintUpcomingHandler)
	maintCreateName, maintCreateTool, maintCreateHandler := maintenance.NewCreateMaintenanceTaskTool(pool)
	srv.RegisterTool(maintCreateName, maintCreateTool, maintCreateHandler)

	// Register asset tools.
	assetTools := asset.NewTools(pool, search.NewIndexer(cfg))
	srv.RegisterTool("list_assets", assetTools.ListAssetsTool(), assetTools.HandleListAssets)
	srv.RegisterTool("get_asset", assetTools.GetAssetTool(), assetTools.HandleGetAsset)
	srv.RegisterTool("create_asset", assetTools.CreateAssetTool(), assetTools.HandleCreateAsset)
	srv.RegisterTool("update_asset", assetTools.UpdateAssetTool(), assetTools.HandleUpdateAsset)
	srv.RegisterTool("delete_asset", assetTools.DeleteAssetTool(), assetTools.HandleDeleteAsset)

	// Register property tools.
	propertyTools := property.NewTools(pool)
	srv.RegisterTool("list_properties", propertyTools.ListPropertiesTool(), propertyTools.HandleListProperties)
	srv.RegisterTool("get_property", propertyTools.GetPropertyTool(), propertyTools.HandleGetProperty)

	// Register vehicle tools.
	srv.RegisterTool("list_vehicles", vehicle.NewListTool(), vehicle.HandleList(pool))
	srv.RegisterTool("get_vehicle", vehicle.NewGetTool(), vehicle.HandleGet(pool))

	// Register pet tools.
	srv.RegisterTool("list_pets", pet.NewListTool(), pet.HandleList(pool))
	srv.RegisterTool("get_pet", pet.NewGetTool(), pet.HandleGet(pool))

	// Register vendor tools.
	srv.RegisterTool("list_vendors", vendor.NewListTool(), vendor.HandleList(pool))
	srv.RegisterTool("get_vendor", vendor.NewGetTool(), vendor.HandleGet(pool))

	// Register calendar tools.
	listCalTool, listCalHandler := calendar.NewListCalendarsTool(pool)
	srv.RegisterTool("list_calendars", listCalTool, listCalHandler)

	listEvtTool, listEvtHandler := calendar.NewListEventsTool(pool)
	srv.RegisterTool("list_events", listEvtTool, listEvtHandler)

	createEvtTool, createEvtHandler := calendar.NewCreateEventTool(pool)
	srv.RegisterTool("create_event", createEvtTool, createEvtHandler)

	updateEvtTool, updateEvtHandler := calendar.NewUpdateEventTool(pool)
	srv.RegisterTool("update_event", updateEvtTool, updateEvtHandler)

	deleteEvtTool, deleteEvtHandler := calendar.NewDeleteEventTool(pool)
	srv.RegisterTool("delete_event", deleteEvtTool, deleteEvtHandler)

	slotsTool, slotsHandler := calendar.NewFindAvailableSlotsTool(pool)
	srv.RegisterTool("find_available_slots", slotsTool, slotsHandler)

	briefTool, briefHandler := calendar.NewGetDailyBriefingTool(pool)
	srv.RegisterTool("get_daily_briefing", briefTool, briefHandler)

	conflictTool, conflictHandler := calendar.NewCheckConflictsTool(pool)
	srv.RegisterTool("check_conflicts", conflictTool, conflictHandler)

	// Register notification tools.
	srv.RegisterTool("list_notifications", notification.NewListNotificationsTool(), notification.HandleListNotifications(pool))
	srv.RegisterTool("get_unread_count", notification.NewGetUnreadCountTool(), notification.HandleGetUnreadCount(pool))

	// Register household tools.
	srv.RegisterTool("get_household", household.NewGetHouseholdTool(), household.HandleGetHousehold(pool))
	srv.RegisterTool("list_members", household.NewListMembersTool(), household.HandleListMembers(pool))

	// Register loan tools.
	loanTools := loan.NewTools(pool)
	srv.RegisterTool("list_loans", loanTools.ListLoansTool(), loanTools.HandleListLoans)
	srv.RegisterTool("get_loan", loanTools.GetLoanTool(), loanTools.HandleGetLoan)
	srv.RegisterTool("create_loan", loanTools.CreateLoanTool(), loanTools.HandleCreateLoan)
	srv.RegisterTool("update_loan", loanTools.UpdateLoanTool(), loanTools.HandleUpdateLoan)
	srv.RegisterTool("delete_loan", loanTools.DeleteLoanTool(), loanTools.HandleDeleteLoan)

	// Register finance tools.
	srv.RegisterTool("get_financial_summary", finance.NewGetFinancialSummaryTool(), finance.HandleGetFinancialSummary(pool))

	// Register prompts.
	srv.RegisterPrompt("daily-briefing", prompts.NewDailyBriefingPrompt(), prompts.HandleDailyBriefing)
	srv.RegisterPrompt("schedule-event", prompts.NewScheduleEventPrompt(), prompts.HandleScheduleEvent)
	srv.RegisterPrompt("check-bills", prompts.NewCheckBillsPrompt(), prompts.HandleCheckBills)

	// Register resource templates.
	srv.RegisterResourceTemplate(resources.NewHouseholdResourceTemplate(), resources.HandleHouseholdResource(pool))
	srv.RegisterResourceTemplate(resources.NewCalendarEventsResourceTemplate(), resources.HandleCalendarEventsResource(pool))
	srv.RegisterResourceTemplate(resources.NewAssetResourceTemplate(), resources.HandleAssetResource(pool))

	// Start the metadata server on a separate port for health checks and
	// OAuth protected resource metadata. This runs on a separate port to
	// avoid route conflicts with the MCP SSE handler.
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

// handlePing is the handler for the "ping" tool. It returns a simple
// {"pong": true} response to verify the server is operational.
func handlePing(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText(`{"pong":true}`), nil
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
