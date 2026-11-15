package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/asset"
	"home-os/mcp/internal/bill"
	"home-os/mcp/internal/calendar"
	"home-os/mcp/internal/config"
	"home-os/mcp/internal/file"
	"home-os/mcp/internal/finance"
	"home-os/mcp/internal/household"
	"home-os/mcp/internal/loan"
	"home-os/mcp/internal/maintenance"
	"home-os/mcp/internal/note"
	"home-os/mcp/internal/notification"
	"home-os/mcp/internal/pet"
	"home-os/mcp/internal/prompts"
	"home-os/mcp/internal/property"
	"home-os/mcp/internal/resources"
	"home-os/mcp/internal/search"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/vehicle"
	"home-os/mcp/internal/vendors"
)

// registerAll registers every tool, prompt and resource template on srv.
// main and the protocol-compatibility test share it, so the test sees exactly
// the tool set production serves.
func registerAll(srv *server.Server, cfg *config.Config, pool *pgxpool.Pool) {
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
	srv.RegisterTool("list_vendors", vendors.NewListTool(), vendors.HandleList(pool))
	srv.RegisterTool("get_vendor", vendors.NewGetTool(), vendors.HandleGet(pool))

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
}

// handlePing is the handler for the "ping" tool. It returns a simple
// {"pong": true} response to verify the server is operational.
func handlePing(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText(`{"pong":true}`), nil
}
