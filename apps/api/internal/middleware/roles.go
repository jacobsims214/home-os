package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"home-os/api/internal/auth"
	"home-os/api/pkg/apierr"
)

// RouteRule is the resource and action one route needs from the household
// role policy (auth.Can).
//
// A route whose handler makes a finer, record-level decision names the
// narrowest action that can succeed on it, and the handler checks the rest
// against the same policy: PATCH /lists/{id}/items/{itemId} needs
// check_items (checking an item off), and the handler asks for edit_items
// when the body changes anything else; GET /people needs read_basic and the
// handler hides birthdates, schools and notes from roles without people
// read; POST /notes needs annotate and the handler checks the noted record;
// PATCH /maintenance/tasks/{id} needs update_status and the handler refuses
// other fields to roles without maintenance write.
type RouteRule struct {
	Resource auth.Resource
	Action   auth.Action
}

func rr(res auth.Resource, act auth.Action) RouteRule { return RouteRule{Resource: res, Action: act} }

// routeRules maps "METHOD pattern" (chi route patterns, exactly as the routes
// are registered in cmd/api) to the rule that route needs. Every route behind
// RequirePolicy must be here: an unmapped route answers 403 for everyone, and
// cmd/api's route test fails, so a new route cannot ship without a policy.
var routeRules = map[string]RouteRule{
	// Household and members.
	"GET /api/v1/me/households":                     rr(auth.ResHousehold, auth.ActRead),
	"GET /api/v1/households/me":                     rr(auth.ResHousehold, auth.ActRead),
	"PATCH /api/v1/households/{id}":                 rr(auth.ResHousehold, auth.ActWrite),
	"GET /api/v1/households/me/members":             rr(auth.ResMembers, auth.ActRead),
	"PATCH /api/v1/households/me/members/{userId}":  rr(auth.ResMembers, auth.ActWrite),
	"DELETE /api/v1/households/me/members/{userId}": rr(auth.ResMembers, auth.ActWrite),

	// Invitations (accept is outside RequireAuth).
	"POST /api/v1/invites":        rr(auth.ResInvites, auth.ActWrite),
	"GET /api/v1/invites":         rr(auth.ResInvites, auth.ActRead),
	"DELETE /api/v1/invites/{id}": rr(auth.ResInvites, auth.ActWrite),

	// Agent tokens: the handler refuses the write scope without ActWriteScope.
	"POST /api/v1/agent-tokens":        rr(auth.ResAgentTokens, auth.ActCreate),
	"GET /api/v1/agent-tokens":         rr(auth.ResAgentTokens, auth.ActRead),
	"DELETE /api/v1/agent-tokens/{id}": rr(auth.ResAgentTokens, auth.ActRevoke),

	// Assets.
	"GET /api/v1/assets":         rr(auth.ResAssets, auth.ActRead),
	"POST /api/v1/assets":        rr(auth.ResAssets, auth.ActWrite),
	"GET /api/v1/assets/{id}":    rr(auth.ResAssets, auth.ActRead),
	"PUT /api/v1/assets/{id}":    rr(auth.ResAssets, auth.ActWrite),
	"DELETE /api/v1/assets/{id}": rr(auth.ResAssets, auth.ActWrite),

	// Loans.
	"GET /api/v1/loans":         rr(auth.ResLoans, auth.ActRead),
	"POST /api/v1/loans":        rr(auth.ResLoans, auth.ActWrite),
	"GET /api/v1/loans/{id}":    rr(auth.ResLoans, auth.ActRead),
	"PUT /api/v1/loans/{id}":    rr(auth.ResLoans, auth.ActWrite),
	"DELETE /api/v1/loans/{id}": rr(auth.ResLoans, auth.ActWrite),

	// Vehicles.
	"GET /api/v1/vehicles":         rr(auth.ResVehicles, auth.ActRead),
	"POST /api/v1/vehicles":        rr(auth.ResVehicles, auth.ActWrite),
	"GET /api/v1/vehicles/{id}":    rr(auth.ResVehicles, auth.ActRead),
	"PUT /api/v1/vehicles/{id}":    rr(auth.ResVehicles, auth.ActWrite),
	"DELETE /api/v1/vehicles/{id}": rr(auth.ResVehicles, auth.ActWrite),

	// Pets.
	"GET /api/v1/pets":         rr(auth.ResPets, auth.ActRead),
	"POST /api/v1/pets":        rr(auth.ResPets, auth.ActWrite),
	"GET /api/v1/pets/{id}":    rr(auth.ResPets, auth.ActRead),
	"PUT /api/v1/pets/{id}":    rr(auth.ResPets, auth.ActWrite),
	"DELETE /api/v1/pets/{id}": rr(auth.ResPets, auth.ActWrite),

	// Vendors (contacts).
	"GET /api/v1/vendors":         rr(auth.ResVendors, auth.ActRead),
	"POST /api/v1/vendors":        rr(auth.ResVendors, auth.ActWrite),
	"GET /api/v1/vendors/{id}":    rr(auth.ResVendors, auth.ActRead),
	"PUT /api/v1/vendors/{id}":    rr(auth.ResVendors, auth.ActWrite),
	"DELETE /api/v1/vendors/{id}": rr(auth.ResVendors, auth.ActWrite),

	// Calendar.
	"GET /api/v1/calendars":                          rr(auth.ResCalendar, auth.ActRead),
	"POST /api/v1/calendars":                         rr(auth.ResCalendar, auth.ActWrite),
	"GET /api/v1/calendars/events":                   rr(auth.ResCalendar, auth.ActRead),
	"GET /api/v1/calendars/{id}/events":              rr(auth.ResCalendar, auth.ActRead),
	"POST /api/v1/calendars/{id}/events":             rr(auth.ResCalendar, auth.ActWrite),
	"PATCH /api/v1/calendars/{id}/events/{eventId}":  rr(auth.ResCalendar, auth.ActWrite),
	"DELETE /api/v1/calendars/{id}/events/{eventId}": rr(auth.ResCalendar, auth.ActWrite),

	// Maintenance: the handler limits update_status to status/notes.
	"GET /api/v1/maintenance/tasks":         rr(auth.ResMaintenance, auth.ActRead),
	"POST /api/v1/maintenance/tasks":        rr(auth.ResMaintenance, auth.ActWrite),
	"PATCH /api/v1/maintenance/tasks/{id}":  rr(auth.ResMaintenance, auth.ActUpdateStatus),
	"DELETE /api/v1/maintenance/tasks/{id}": rr(auth.ResMaintenance, auth.ActWrite),
	"GET /api/v1/maintenance/schedules":     rr(auth.ResMaintenance, auth.ActRead),
	"POST /api/v1/maintenance/schedules":    rr(auth.ResMaintenance, auth.ActWrite),

	// Properties and rooms.
	"GET /api/v1/properties":             rr(auth.ResProperties, auth.ActRead),
	"POST /api/v1/properties":            rr(auth.ResProperties, auth.ActWrite),
	"GET /api/v1/properties/{id}":        rr(auth.ResProperties, auth.ActRead),
	"PUT /api/v1/properties/{id}":        rr(auth.ResProperties, auth.ActWrite),
	"DELETE /api/v1/properties/{id}":     rr(auth.ResProperties, auth.ActWrite),
	"GET /api/v1/properties/{id}/rooms":  rr(auth.ResProperties, auth.ActRead),
	"POST /api/v1/properties/{id}/rooms": rr(auth.ResProperties, auth.ActWrite),

	// Bills.
	"GET /api/v1/bills":         rr(auth.ResBills, auth.ActRead),
	"POST /api/v1/bills":        rr(auth.ResBills, auth.ActWrite),
	"GET /api/v1/bills/{id}":    rr(auth.ResBills, auth.ActRead),
	"PUT /api/v1/bills/{id}":    rr(auth.ResBills, auth.ActWrite),
	"DELETE /api/v1/bills/{id}": rr(auth.ResBills, auth.ActWrite),

	// Search: the handler drops results the role may not see.
	"GET /api/v1/search": rr(auth.ResSearch, auth.ActRead),

	// Notes: reads and annotate are record-checked in the handler.
	"GET /api/v1/notes":         rr(auth.ResNotes, auth.ActRead),
	"POST /api/v1/notes":        rr(auth.ResNotes, auth.ActAnnotate),
	"GET /api/v1/notes/{id}":    rr(auth.ResNotes, auth.ActRead),
	"PUT /api/v1/notes/{id}":    rr(auth.ResNotes, auth.ActWrite),
	"PATCH /api/v1/notes/{id}":  rr(auth.ResNotes, auth.ActWrite),
	"DELETE /api/v1/notes/{id}": rr(auth.ResNotes, auth.ActWrite),

	// Notifications.
	"GET /api/v1/notifications":             rr(auth.ResNotifications, auth.ActRead),
	"POST /api/v1/notifications":            rr(auth.ResNotifications, auth.ActWrite),
	"PATCH /api/v1/notifications/{id}/read": rr(auth.ResNotifications, auth.ActMarkRead),

	// Files.
	"POST /api/v1/files/upload":      rr(auth.ResFiles, auth.ActWrite),
	"GET /api/v1/files":              rr(auth.ResFiles, auth.ActRead),
	"GET /api/v1/files/{id}":         rr(auth.ResFiles, auth.ActRead),
	"GET /api/v1/files/{id}/content": rr(auth.ResFiles, auth.ActRead),
	"PATCH /api/v1/files/{id}":       rr(auth.ResFiles, auth.ActWrite),
	"DELETE /api/v1/files/{id}":      rr(auth.ResFiles, auth.ActWrite),

	// Secrets: credentials are hidden entirely from viewer, housesitter and
	// vendor (the secrets handler applies its own per-secret rules on top).
	"POST /api/v1/secrets":                    rr(auth.ResSecrets, auth.ActWrite),
	"GET /api/v1/secrets":                     rr(auth.ResSecrets, auth.ActRead),
	"GET /api/v1/secrets/{id}":                rr(auth.ResSecrets, auth.ActRead),
	"PATCH /api/v1/secrets/{id}":              rr(auth.ResSecrets, auth.ActWrite),
	"DELETE /api/v1/secrets/{id}":             rr(auth.ResSecrets, auth.ActWrite),
	"POST /api/v1/secrets/setup":              rr(auth.ResSecrets, auth.ActWrite),
	"POST /api/v1/secrets/verify":             rr(auth.ResSecrets, auth.ActRead),
	"GET /api/v1/secrets/key":                 rr(auth.ResSecrets, auth.ActRead),
	"PUT /api/v1/secrets/{id}/agent-share":    rr(auth.ResSecrets, auth.ActWrite),
	"DELETE /api/v1/secrets/{id}/agent-share": rr(auth.ResSecrets, auth.ActWrite),

	// Links: the handler drops secret links for roles without secret read.
	"POST /api/v1/links":        rr(auth.ResLinks, auth.ActWrite),
	"GET /api/v1/links":         rr(auth.ResLinks, auth.ActRead),
	"DELETE /api/v1/links/{id}": rr(auth.ResLinks, auth.ActWrite),

	// People: the handler hides all but names/allergies without people read.
	// PATCH names the colour action (#4821), the narrowest one that can
	// succeed there; the handler then narrows it to the person themselves or
	// a manager from memberships.
	"GET /api/v1/people":                              rr(auth.ResPeople, auth.ActReadBasic),
	"POST /api/v1/people":                             rr(auth.ResPeople, auth.ActWrite),
	"GET /api/v1/people/{id}":                         rr(auth.ResPeople, auth.ActReadBasic),
	"PUT /api/v1/people/{id}":                         rr(auth.ResPeople, auth.ActWrite),
	"PATCH /api/v1/people/{id}":                       rr(auth.ResPeople, auth.ActSetColor),
	"DELETE /api/v1/people/{id}":                      rr(auth.ResPeople, auth.ActWrite),
	"GET /api/v1/people/{id}/preferences":             rr(auth.ResPreferences, auth.ActRead),
	"POST /api/v1/people/{id}/preferences":            rr(auth.ResPreferences, auth.ActWrite),
	"PUT /api/v1/people/{id}/preferences/{prefId}":    rr(auth.ResPreferences, auth.ActWrite),
	"DELETE /api/v1/people/{id}/preferences/{prefId}": rr(auth.ResPreferences, auth.ActWrite),

	// Budget.
	"GET /api/v1/budget/categories":         rr(auth.ResBudget, auth.ActRead),
	"POST /api/v1/budget/categories":        rr(auth.ResBudget, auth.ActWrite),
	"GET /api/v1/budget/categories/{id}":    rr(auth.ResBudget, auth.ActRead),
	"PUT /api/v1/budget/categories/{id}":    rr(auth.ResBudget, auth.ActWrite),
	"DELETE /api/v1/budget/categories/{id}": rr(auth.ResBudget, auth.ActWrite),
	"GET /api/v1/budget/obligations":        rr(auth.ResBudget, auth.ActRead),
	"GET /api/v1/budget/due-soon":           rr(auth.ResBudget, auth.ActRead),

	// Lists: PATCH of an item is check_items here; the handler asks for
	// edit_items when the body changes more than "checked".
	"GET /api/v1/lists":                           rr(auth.ResLists, auth.ActRead),
	"POST /api/v1/lists":                          rr(auth.ResLists, auth.ActManage),
	"GET /api/v1/lists/{id}":                      rr(auth.ResLists, auth.ActRead),
	"PATCH /api/v1/lists/{id}":                    rr(auth.ResLists, auth.ActManage),
	"DELETE /api/v1/lists/{id}":                   rr(auth.ResLists, auth.ActManage),
	"POST /api/v1/lists/{id}/archive":             rr(auth.ResLists, auth.ActManage),
	"POST /api/v1/lists/{id}/unarchive":           rr(auth.ResLists, auth.ActManage),
	"GET /api/v1/lists/{id}/items":                rr(auth.ResLists, auth.ActRead),
	"POST /api/v1/lists/{id}/items":               rr(auth.ResLists, auth.ActAddItems),
	"POST /api/v1/lists/{id}/items/merge":          rr(auth.ResLists, auth.ActAddItems),
	"POST /api/v1/lists/{id}/items/check":         rr(auth.ResLists, auth.ActCheckItems),
	"POST /api/v1/lists/{id}/items/uncheck":       rr(auth.ResLists, auth.ActCheckItems),
	"POST /api/v1/lists/{id}/items/remove":        rr(auth.ResLists, auth.ActEditItems),
	"PATCH /api/v1/lists/{id}/items/{itemId}":     rr(auth.ResLists, auth.ActCheckItems),
	"DELETE /api/v1/lists/{id}/items/{itemId}":    rr(auth.ResLists, auth.ActEditItems),
	"POST /api/v1/lists/{id}/items/{itemId}/move": rr(auth.ResLists, auth.ActEditItems),

	// Meals: rating for others is checked in the handler (rate_others).
	"GET /api/v1/recipes":                           rr(auth.ResMeals, auth.ActRead),
	"POST /api/v1/recipes":                          rr(auth.ResMeals, auth.ActWrite),
	"GET /api/v1/recipes/{id}":                      rr(auth.ResMeals, auth.ActRead),
	"PUT /api/v1/recipes/{id}":                      rr(auth.ResMeals, auth.ActWrite),
	"DELETE /api/v1/recipes/{id}":                   rr(auth.ResMeals, auth.ActWrite),
	"GET /api/v1/meal-plans":                        rr(auth.ResMeals, auth.ActRead),
	"GET /api/v1/meal-plans/grocery":                rr(auth.ResMeals, auth.ActRead),
	"POST /api/v1/meal-plans/grocery":               rr(auth.ResMeals, auth.ActWrite),
	"PUT /api/v1/meal-plans/{date}/{slot}":          rr(auth.ResMeals, auth.ActWrite),
	"DELETE /api/v1/meal-plans/{date}/{slot}":       rr(auth.ResMeals, auth.ActWrite),
	"GET /api/v1/meals/{id}":                        rr(auth.ResMeals, auth.ActRead),
	"GET /api/v1/meals/{id}/feedback":               rr(auth.ResMeals, auth.ActRead),
	"PUT /api/v1/meals/{id}/feedback/{personId}":    rr(auth.ResMeals, auth.ActWrite),
	"DELETE /api/v1/meals/{id}/feedback/{personId}": rr(auth.ResMeals, auth.ActWrite),

	// Activity feed: the handler hides kinds the role may not see.
	"GET /api/v1/activity": rr(auth.ResActivity, auth.ActRead),

	// The caller's own sign-in password (#4822). Self-scoped: the handler reads
	// the account from the verified token, so this grants no access to anyone
	// else's password.
	"POST /api/v1/auth/change-password": rr(auth.ResPassword, auth.ActWrite),

	// Guides.
	"GET /api/v1/guides":                                      rr(auth.ResGuides, auth.ActRead),
	"POST /api/v1/guides":                                     rr(auth.ResGuides, auth.ActWrite),
	"GET /api/v1/guides/{id}":                                 rr(auth.ResGuides, auth.ActRead),
	"PUT /api/v1/guides/{id}":                                 rr(auth.ResGuides, auth.ActWrite),
	"DELETE /api/v1/guides/{id}":                              rr(auth.ResGuides, auth.ActWrite),
	"GET /api/v1/guides/{id}/revisions":                       rr(auth.ResGuides, auth.ActRead),
	"GET /api/v1/guides/{id}/revisions/{revisionId}":          rr(auth.ResGuides, auth.ActRead),
	"POST /api/v1/guides/{id}/revisions/{revisionId}/restore": rr(auth.ResGuides, auth.ActWrite),

	// The family knowledge base (#5502). Every role reads it — a housesitter
	// included, because the babysitter needs the router doc — and the family
	// roles write. The {path...} wildcard carries the doc's namespaced slug.
	"GET /api/v1/kb":        rr(auth.ResKB, auth.ActRead),
	"GET /api/v1/kb/*":      rr(auth.ResKB, auth.ActRead),
	"PUT /api/v1/kb/*":      rr(auth.ResKB, auth.ActWrite),
	"DELETE /api/v1/kb/*":   rr(auth.ResKB, auth.ActWrite),
}

// RuleFor returns the rule for a route (HEAD is looked up as GET).
func RuleFor(method, pattern string) (RouteRule, bool) {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	rule, ok := routeRules[method+" "+pattern]
	return rule, ok
}

// RequirePolicy applies the household role policy to every route of the
// router it is mounted on. It must run after RequireAuth (it reads the
// caller's role, which RequireAuth re-reads from memberships for the
// request's household on every request) and inside a chi Group or With, so
// that chi has matched the route before it runs.
//
// The caller's role must allow the route's RouteRule, else 403 with a
// message naming the role, action and resource. OPTIONS passes (CORS
// preflight). An unmapped route is a programming error: 403 for everyone,
// logged at error level.
func RequirePolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		claims := ClaimsFromContext(r.Context())
		if claims == nil {
			apierr.Forbidden(w, "missing household context")
			return
		}
		pattern := ""
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			pattern = rctx.RoutePattern()
		}
		rule, ok := RuleFor(r.Method, pattern)
		if !ok {
			slog.Error("roles: route has no access policy", "method", r.Method, "pattern", pattern)
			apierr.Forbidden(w, "this route has no access policy")
			return
		}
		if !auth.Can(claims.Role, rule.Resource, rule.Action) {
			slog.Info("roles: refused", "user_id", claims.UserID, "household_id", claims.HouseholdID,
				"role", claims.Role, "method", r.Method, "pattern", pattern,
				"resource", rule.Resource, "action", rule.Action)
			apierr.Forbidden(w, DenyMessage(claims.Role, rule.Resource, rule.Action))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// DenyMessage is the 403 message for a refused action, e.g. "the vendor role
// cannot write bills".
func DenyMessage(role string, res auth.Resource, act auth.Action) string {
	if role == "" {
		role = "unknown"
	}
	return fmt.Sprintf("the %s role cannot %s %s", role, strings.ReplaceAll(string(act), "_", " "), strings.ReplaceAll(string(res), "_", " "))
}

// Can reports whether the request's caller may perform act on res under the
// household role policy. Handlers use it for record-level decisions.
func Can(r *http.Request, res auth.Resource, act auth.Action) bool {
	claims := ClaimsFromContext(r.Context())
	return claims != nil && auth.Can(claims.Role, res, act)
}

// Role returns the request caller's household role ("" without claims).
func Role(r *http.Request) string {
	if claims := ClaimsFromContext(r.Context()); claims != nil {
		return claims.Role
	}
	return ""
}

// RulePatterns returns every "METHOD pattern" that has a rule (for tests that
// check the table against the real router).
func RulePatterns() []string {
	out := make([]string, 0, len(routeRules))
	for k := range routeRules {
		out = append(out, k)
	}
	return out
}
