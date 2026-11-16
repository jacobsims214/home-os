package main

import (
	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/activity"
	"home-os/api/internal/agenttoken"
	"home-os/api/internal/asset"
	"home-os/api/internal/auth"
	"home-os/api/internal/bill"
	"home-os/api/internal/budget"
	"home-os/api/internal/calendar"
	"home-os/api/internal/config"
	"home-os/api/internal/file"
	"home-os/api/internal/guides"
	"home-os/api/internal/household"
	"home-os/api/internal/invite"
	"home-os/api/internal/link"
	"home-os/api/internal/lists"
	"home-os/api/internal/loan"
	"home-os/api/internal/maintenance"
	"home-os/api/internal/meals"
	"home-os/api/internal/note"
	"home-os/api/internal/notification"
	"home-os/api/internal/people"
	"home-os/api/internal/pet"
	"home-os/api/internal/property"
	"home-os/api/internal/search"
	"home-os/api/internal/secret"
	"home-os/api/internal/vehicle"
	"home-os/api/internal/vendors"
	"home-os/api/pkg/smtp"
)

// protectedDeps is what registerProtectedRoutes needs from main.
type protectedDeps struct {
	pool             *pgxpool.Pool
	cfg              *config.Config
	search           *search.Client
	smtp             *smtp.Client
	householdHandler *household.Handler
	inviteHandler    *invite.Handler
}

// registerProtectedRoutes mounts every route that runs behind
// middleware.RequireAuth and middleware.RequirePolicy. r must already run
// both. Each route must have a rule in middleware/roles.go (routes_test.go
// walks this router and fails on any route without one).
func registerProtectedRoutes(r chi.Router, d protectedDeps) {
	// Household (Create is registered above, outside this group).
	// /me/households lists the caller's families; X-Household-ID on any
	// request in this group picks one (see middleware.RequireAuth).
	r.Get("/api/v1/me/households", d.householdHandler.ListMine)
	r.Get("/api/v1/households/me", d.householdHandler.GetMe)
	r.Get("/api/v1/households/me/members", d.householdHandler.ListMembers)
	r.Patch("/api/v1/households/me/members/{userId}", d.householdHandler.UpdateMemberRole)
	r.Delete("/api/v1/households/me/members/{userId}", d.householdHandler.RemoveMember)

	// Asset CRUD
	assetRepo := asset.NewRepo(d.pool)
	assetHandler := asset.NewHandler(assetRepo, d.cfg).WithSearchClient(d.search)
	r.Get("/api/v1/assets", assetHandler.List)
	r.Post("/api/v1/assets", assetHandler.Create)
	r.Get("/api/v1/assets/{id}", assetHandler.Get)
	r.Put("/api/v1/assets/{id}", assetHandler.Update)
	r.Delete("/api/v1/assets/{id}", assetHandler.Delete)

	// Loan CRUD
	loanRepo := loan.NewRepo(d.pool)
	loanHandler := loan.NewHandler(loanRepo)
	r.Get("/api/v1/loans", loanHandler.List)
	r.Post("/api/v1/loans", loanHandler.Create)
	r.Get("/api/v1/loans/{id}", loanHandler.Get)
	r.Put("/api/v1/loans/{id}", loanHandler.Update)
	r.Delete("/api/v1/loans/{id}", loanHandler.Delete)

	// Vehicle CRUD
	vehicleRepo := vehicle.NewRepo(d.pool)
	vehicleHandler := vehicle.NewHandler(vehicleRepo).WithSearchClient(d.search)
	r.Get("/api/v1/vehicles", vehicleHandler.List)
	r.Post("/api/v1/vehicles", vehicleHandler.Create)
	r.Get("/api/v1/vehicles/{id}", vehicleHandler.Get)
	r.Put("/api/v1/vehicles/{id}", vehicleHandler.Update)
	r.Delete("/api/v1/vehicles/{id}", vehicleHandler.Delete)

	// Pet CRUD
	petRepo := pet.NewRepo(d.pool)
	petHandler := pet.NewHandler(petRepo).WithSearchClient(d.search)
	r.Get("/api/v1/pets", petHandler.List)
	r.Post("/api/v1/pets", petHandler.Create)
	r.Get("/api/v1/pets/{id}", petHandler.Get)
	r.Put("/api/v1/pets/{id}", petHandler.Update)
	r.Delete("/api/v1/pets/{id}", petHandler.Delete)

	// Vendor CRUD
	vendorRepo := vendors.NewRepo(d.pool)
	vendorHandler := vendors.NewHandler(vendorRepo).WithSearchClient(d.search)
	r.Get("/api/v1/vendors", vendorHandler.List)
	r.Post("/api/v1/vendors", vendorHandler.Create)
	r.Get("/api/v1/vendors/{id}", vendorHandler.Get)
	r.Put("/api/v1/vendors/{id}", vendorHandler.Update)
	r.Delete("/api/v1/vendors/{id}", vendorHandler.Delete)

	// Calendar CRUD (must be before maintenance — maintenance handler needs calendarRepo)
	calendarRepo := calendar.NewRepo(d.pool)
	calendarHandler := calendar.NewHandler(calendarRepo)
	r.Get("/api/v1/calendars", calendarHandler.ListCalendars)
	r.Post("/api/v1/calendars", calendarHandler.CreateCalendar)
	r.Get("/api/v1/calendars/events", calendarHandler.ListAllEvents)
	r.Get("/api/v1/calendars/{id}/events", calendarHandler.ListEvents)
	r.Post("/api/v1/calendars/{id}/events", calendarHandler.CreateEvent)
	r.Patch("/api/v1/calendars/{id}/events/{eventId}", calendarHandler.UpdateEvent)
	r.Delete("/api/v1/calendars/{id}/events/{eventId}", calendarHandler.DeleteEvent)

	// Maintenance CRUD (with bidirectional calendar sync)
	maintenanceRepo := maintenance.NewRepo(d.pool)
	maintenanceHandler := maintenance.NewHandler(maintenanceRepo).WithCalendarRepo(calendarRepo).WithSearchClient(d.search)
	r.Get("/api/v1/maintenance/tasks", maintenanceHandler.ListTasks)
	r.Post("/api/v1/maintenance/tasks", maintenanceHandler.CreateTask)
	r.Patch("/api/v1/maintenance/tasks/{id}", maintenanceHandler.UpdateTask)
	r.Delete("/api/v1/maintenance/tasks/{id}", maintenanceHandler.DeleteTask)
	r.Get("/api/v1/maintenance/schedules", maintenanceHandler.ListSchedules)
	r.Post("/api/v1/maintenance/schedules", maintenanceHandler.CreateSchedule)

	// Property and Room CRUD
	propertyRepo := property.NewRepo(d.pool)
	propertyHandler := property.NewHandler(propertyRepo).WithSearchClient(d.search)
	r.Get("/api/v1/properties", propertyHandler.ListProperties)
	r.Post("/api/v1/properties", propertyHandler.CreateProperty)
	r.Get("/api/v1/properties/{id}", propertyHandler.GetProperty)
	r.Put("/api/v1/properties/{id}", propertyHandler.UpdateProperty)
	r.Delete("/api/v1/properties/{id}", propertyHandler.DeleteProperty)
	r.Get("/api/v1/properties/{id}/rooms", propertyHandler.ListRooms)
	r.Post("/api/v1/properties/{id}/rooms", propertyHandler.CreateRoom)

	// Bill CRUD
	billRepo := bill.NewRepo(d.pool)
	billHandler := bill.NewHandler(billRepo).WithSearchClient(d.search)
	r.Get("/api/v1/bills", billHandler.List)
	r.Post("/api/v1/bills", billHandler.Create)
	r.Get("/api/v1/bills/{id}", billHandler.Get)
	r.Put("/api/v1/bills/{id}", billHandler.Update)
	r.Delete("/api/v1/bills/{id}", billHandler.Delete)

	// Search
	// File hits are re-checked against Postgres (deleted files, and files
	// attached to credentials for roles that cannot read credentials).
	searchHandler := search.NewHandler(d.search).WithFileChecker(file.NewRepo(d.pool))
	r.Get("/api/v1/search", searchHandler.Search)

	// Note CRUD (polymorphic)
	noteRepo := note.NewRepo(d.pool)
	noteHandler := note.NewHandler(noteRepo).WithSearchClient(d.search).WithFileChecker(file.NewRepo(d.pool))
	r.Get("/api/v1/notes", noteHandler.List)
	r.Post("/api/v1/notes", noteHandler.Create)
	r.Get("/api/v1/notes/{id}", noteHandler.Get)
	r.Put("/api/v1/notes/{id}", noteHandler.Update)
	r.Patch("/api/v1/notes/{id}", noteHandler.Update)
	r.Delete("/api/v1/notes/{id}", noteHandler.Delete)

	// Notification CRUD
	notificationRepo := notification.NewRepo(d.pool)
	notificationHandler := notification.NewHandler(notificationRepo, d.smtp)
	r.Get("/api/v1/notifications", notificationHandler.List)
	r.Post("/api/v1/notifications", notificationHandler.Create)
	r.Patch("/api/v1/notifications/{id}/read", notificationHandler.MarkRead)

	// File storage (polymorphic, bytea in Postgres)
	fileRepo := file.NewRepo(d.pool)
	fileHandler := file.NewHandler(fileRepo).WithSearch(d.search)
	r.Post("/api/v1/files/upload", fileHandler.UploadFile)
	r.Get("/api/v1/files", fileHandler.ListFiles)
	r.Get("/api/v1/files/{id}", fileHandler.GetFile)
	r.Get("/api/v1/files/{id}/content", fileHandler.GetFileContent)
	r.Patch("/api/v1/files/{id}", fileHandler.UpdateFile)
	r.Delete("/api/v1/files/{id}", fileHandler.DeleteFile)

	// Secret storage (zero-knowledge, encrypted client-side)
	secretRepo := secret.NewRepo(d.pool)
	secretHandler := secret.NewHandler(secretRepo).WithSearchClient(d.search)
	// "Shared with agents" (#4446): off unless AGENT_CREDENTIALS_KEY holds
	// a valid key. A bad key leaves sharing off (503) and is logged
	// without its value.
	if agentKey, err := secret.ParseAgentKey(d.cfg.AgentCredentialsKey); err != nil {
		slog.Error("secrets: agent credential sharing disabled", "error", err)
	} else if agentKey != nil {
		secretHandler.WithAgentKey(agentKey)
	}
	r.Post("/api/v1/secrets", secretHandler.CreateSecret)
	r.Get("/api/v1/secrets", secretHandler.ListSecrets)
	r.Get("/api/v1/secrets/{id}", secretHandler.GetSecret)
	r.Patch("/api/v1/secrets/{id}", secretHandler.UpdateSecret)
	r.Delete("/api/v1/secrets/{id}", secretHandler.DeleteSecret)
	r.Post("/api/v1/secrets/setup", secretHandler.SetupKey)
	r.Post("/api/v1/secrets/verify", secretHandler.VerifyKey)
	r.Get("/api/v1/secrets/key", secretHandler.GetKeyInfo)
	r.Put("/api/v1/secrets/{id}/agent-share", secretHandler.ShareWithAgents)
	r.Delete("/api/v1/secrets/{id}/agent-share", secretHandler.UnshareWithAgents)

	// Link CRUD (polymorphic)
	linkRepo := link.NewRepo(d.pool)
	linkHandler := link.NewHandler(linkRepo).WithFileChecker(file.NewRepo(d.pool))
	r.Post("/api/v1/links", linkHandler.Create)
	r.Get("/api/v1/links", linkHandler.List)
	r.Delete("/api/v1/links/{id}", linkHandler.Delete)

	// Invite routes (accept is registered above, outside this group).
	r.Post("/api/v1/invites", d.inviteHandler.CreateInvite)
	r.Get("/api/v1/invites", d.inviteHandler.ListInvites)
	r.Delete("/api/v1/invites/{id}", d.inviteHandler.RevokeInvite)

	// Agent tokens: each member creates, lists and revokes their own.
	agenttoken.NewHandler(agenttoken.NewRepo(d.pool)).RegisterRoutes(r)

	// Stage 2 family routes: people, budget, lists, meals, activity, guides.
	registerStage2Routes(r, d.pool, d.search)
}

// registerStage2Routes mounts the Stage 2 family routes (#4489): people and
// preferences (#4463), budget categories and obligations (#4471), lists
// (#4472), recipes/meal plans/feedback (#4475), the activity feed (#4505) and
// guides (#4479).
//
// r must already run middleware.RequireAuth, which resolves the caller's
// household (X-Household-ID aware, #4429) and role, and
// middleware.RequirePolicy. Each handler's own role hook is plugged into the
// same household role policy (auth.Can, #4689), so route-level and
// record-level decisions come from one table. index may be nil (guides are
// then not indexed).
func registerStage2Routes(r chi.Router, pool *pgxpool.Pool, index guides.SearchIndex) {
	people.NewHandler(people.NewRepo(pool)).RegisterRoutes(r)
	budget.NewHandler(budget.NewRepo(pool)).WithPolicy(budgetPolicy{}).RegisterRoutes(r)
	lists.NewHandler(lists.NewRepo(pool)).WithPolicy(listsPolicy).RegisterRoutes(r)
	meals.NewHandler(meals.NewRepo(pool)).WithPolicy(mealsPolicy{}).RegisterRoutes(r)
	activity.NewHandler(activity.NewRepo(pool)).WithReadPolicy(activityPolicy).RegisterRoutes(r)
	guides.NewHandler(guides.NewRepo(pool)).WithPolicy(guidesPolicy).WithSearch(index).RegisterRoutes(r)
}

// Adapters from the Stage 2 handlers' policy hooks to auth.Can.

type budgetPolicy struct{}

func (budgetPolicy) CanRead(role string) bool  { return auth.Can(role, auth.ResBudget, auth.ActRead) }
func (budgetPolicy) CanWrite(role string) bool { return auth.Can(role, auth.ResBudget, auth.ActWrite) }

type mealsPolicy struct{}

func (mealsPolicy) CanRead(role string) bool  { return auth.Can(role, auth.ResMeals, auth.ActRead) }
func (mealsPolicy) CanWrite(role string) bool { return auth.Can(role, auth.ResMeals, auth.ActWrite) }
func (mealsPolicy) CanRateOthers(role string) bool {
	return auth.Can(role, auth.ResMeals, auth.ActRateOthers)
}

// listsActions maps the lists handler's actions to policy actions.
var listsActions = map[lists.Action]auth.Action{
	lists.ActionRead:       auth.ActRead,
	lists.ActionManage:     auth.ActManage,
	lists.ActionEditItems:  auth.ActEditItems,
	lists.ActionAddItems:   auth.ActAddItems,
	lists.ActionCheckItems: auth.ActCheckItems,
}

func listsPolicy(role string, a lists.Action) bool {
	act, ok := listsActions[a]
	return ok && auth.Can(role, auth.ResLists, act)
}

func guidesPolicy(role string, a guides.Action) bool {
	switch a {
	case guides.ActionRead:
		return auth.Can(role, auth.ResGuides, auth.ActRead)
	case guides.ActionWrite:
		return auth.Can(role, auth.ResGuides, auth.ActWrite)
	}
	return false
}

// activityPolicy: the feed needs activity read, and hides every entity kind
// the role may not see (a viewer never sees secret lines, #4448).
func activityPolicy(role string) (bool, []string) {
	if !auth.Can(role, auth.ResActivity, auth.ActRead) {
		return false, nil
	}
	return true, auth.HiddenEntityKinds(role)
}
