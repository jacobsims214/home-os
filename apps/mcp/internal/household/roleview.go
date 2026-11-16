package household

import (
	"context"
	"errors"
	"reflect"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/respond"
)

// Role view of family_overview (#4717).
//
// family_overview is the entry point for every household role, so it cannot
// be refused to a role the way bill_find is: it has to answer with the part
// of the family the caller may see. overviewView is that decision, computed
// once per call from the same policy table every other tool asks
// (internal/auth), and applied in one place (apply) so a section added to the
// overview cannot be shown to a role by default: it has to be named here.
//
// The trims, and the policy rule each mirrors:
//
//   - people: a vendor sees no people at all (ResPeople read_basic is
//     sitter-only); a housesitter sees names, relationships and allergies but
//     not ages (birthdates), accounts, emails or roles (ActReadBasic).
//   - calendars and next_days.events: the calendar read grant (ResCalendar
//     read), so a vendor gets neither.
//   - next_days.bills_due: money (auth.CanSeeMoney), so a housesitter and a
//     vendor get no bills.
//   - next_days.maintenance_due: the maintenance read grant, and a vendor
//     linked to a vendors row (memberships.vendor_id, #4799) sees only their
//     own jobs.
//   - counts: each count is shown only to a role that may see that kind
//     (countKinds maps each field to its entity kind), so a housesitter's
//     overview does not say how many bills, loans, vehicles or documents the
//     family has.
//   - recent_changes: the activity feed is readers-only (ResActivity read).
//   - unread_notifications: always the caller's own, so never trimmed.
type overviewView struct {
	// people is false when the role sees no people at all (vendor).
	people bool
	// peopleBasic hides age, account, email and role (housesitter).
	peopleBasic bool
	// calendars is false when the role may not read the calendar (vendor).
	calendars bool
	// billsDue is false when the role may not see money.
	billsDue bool
	// maintenance is false when the role may not read maintenance (a
	// housesitter).
	maintenance bool
	// ownJobs, when set, limits maintenance to one vendor's own jobs (#4799).
	ownJobs *uuid.UUID
	// recentChanges is false when the role may not read the activity feed.
	recentChanges bool
	// role is the caller's role, for count visibility.
	role string
}

// countKinds maps each areaCounts field to the entity kind that guards it, so
// a count is shown only to a role that may see that kind. A field added to
// areaCounts without an entry here fails TestCountKindsComplete, and
// trimCounts zeroes every field whose kind the role may not see, so a new
// count cannot leak by default.
var countKinds = map[string]string{
	"Bills":     "bill",
	"Loans":     "loan",
	"Assets":    "asset",
	"Vehicles":  "vehicle",
	"Pets":      "pet",
	"Contacts":  "vendor",
	"Notes":     "note",
	"Lists":     "list",
	"Recipes":   "recipe",
	"Documents": "file",
	"Guides":    "guide",
}

// newOverviewView decides what role may see of the overview. vendorID is the
// vendors row the caller's vendor membership is linked to, or nil (#4799).
func newOverviewView(role string, vendorID *uuid.UUID) overviewView {
	return overviewView{
		// people: read_basic, so a housesitter sees names and relationships
		// and a vendor sees nobody. peopleBasic then hides the fields beyond
		// read_basic for a role without full people read.
		people:        auth.Can(role, auth.ResPeople, auth.ActReadBasic),
		peopleBasic:   !auth.Can(role, auth.ResPeople, auth.ActRead),
		calendars:     auth.CanSeeEntity(role, "calendar_event"),
		billsDue:      auth.CanSeeMoney(role),
		maintenance:   auth.CanSeeEntity(role, "maintenance_task"),
		ownJobs:       vendorID,
		recentChanges: auth.Can(role, auth.ResActivity, auth.ActRead),
		role:          role,
	}
}

// apply trims ov in place to what the view allows.
func (v overviewView) apply(ov *overview) {
	if !v.people {
		ov.People = []personRow{}
	} else if v.peopleBasic {
		for i := range ov.People {
			ov.People[i].Age = nil
			ov.People[i].UserID = nil
			ov.People[i].Email = nil
			ov.People[i].Role = nil
			ov.People[i].HasAccount = false
		}
	}
	if !v.calendars {
		ov.Calendars = []calendarRow{}
		ov.NextDays.Events = eventsSection{Items: []eventBrief{}}
	}
	if !v.billsDue {
		ov.NextDays.BillsDue = billsSection{Items: []billBrief{}}
	}
	if !v.maintenance {
		ov.NextDays.MaintenanceDue = maintenanceSection{Items: []taskBrief{}}
	}
	if !v.recentChanges {
		ov.RecentChanges = []recentChange{}
	}
	v.trimCounts(&ov.Counts)
}

// trimCounts zeroes the counts of kinds the role may not see.
func (v overviewView) trimCounts(c *areaCounts) {
	val := reflect.ValueOf(c).Elem()
	for field, kind := range countKinds {
		f := val.FieldByName(field)
		if !f.IsValid() || f.Kind() != reflect.Int {
			continue // TestCountKindsComplete catches a bad entry
		}
		if !auth.CanSeeEntity(v.role, kind) {
			f.SetInt(0)
		}
	}
}

// visible reports whether the view shows a section, for the summary line.
func (v overviewView) visible(section string) bool {
	switch section {
	case "people":
		return v.people
	case "calendars":
		return v.calendars
	case "bills_due":
		return v.billsDue
	case "maintenance_due":
		return v.maintenance
	case "recent_changes":
		return v.recentChanges
	}
	return true
}

// overviewViewFor resolves the caller's current role and vendor link the way
// maintenance_find, search_all and the API do: re-read from memberships on
// every call, so a demotion or a new vendor link applies at once. A non-nil
// result is the isError to return.
//
// A caller with no users.id (auth-disabled local development, or an
// unenriched claim) keeps the role on its claims; an agent token always
// resolves to a users.id, so one that cannot be read is refused.
func overviewViewFor(ctx context.Context, pool *pgxpool.Pool, c *auth.Claims) (overviewView, *mcp.CallToolResult) {
	hid, err := uuid.Parse(c.HouseholdID)
	if err != nil {
		return overviewView{}, notSignedIn()
	}
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.AgentTokenID != "" {
			return overviewView{}, notSignedIn()
		}
		var exists bool
		if qerr := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM households WHERE id = $1)`, hid).Scan(&exists); qerr == nil && !exists {
			return overviewView{}, notSignedIn()
		}
		return newOverviewView(c.Role, nil), nil
	}
	var (
		role     string
		vendorID *uuid.UUID
	)
	err = pool.QueryRow(ctx,
		`SELECT role::text, CASE WHEN role = 'vendor' THEN vendor_id END
		 FROM memberships WHERE household_id = $1 AND user_id = $2`, hid, uid).Scan(&role, &vendorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return overviewView{}, notSignedIn()
	}
	if err != nil {
		return overviewView{}, respond.Failed("check your access", err)
	}
	return newOverviewView(role, vendorID), nil
}
