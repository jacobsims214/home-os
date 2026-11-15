// Package activity serves the family change feed: the activity_log rows that
// migration 050's triggers write for every create, update and delete of a
// family-owned record.
//
// The log never holds record values. Its summary is built from label columns
// only (a title, a name, a credential's name), and changed_fields lists column
// names, never values, so secrets rows carry nothing but the credential's name.
// This package adds no other columns from the records themselves: actor names
// come from people/users and agent names from agent_tokens.name (never the
// token prefix or hash).
//
// Every query is scoped to the household the request resolved to.
package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Entry is one activity_log row with its actor and agent resolved to names.
type Entry struct {
	ID            int64
	HouseholdID   uuid.UUID
	Action        string
	EntityType    string
	EntityID      uuid.UUID
	ParentType    *string
	ParentID      *uuid.UUID
	ChangedFields []string
	Summary       string
	CreatedAt     time.Time

	// ActorUserID is NULL for writes by the system or an unknown caller, and
	// after the acting user was deleted.
	ActorUserID   *uuid.UUID
	ActorPersonID *uuid.UUID
	// ActorName: the actor's person display_name in this household, else
	// users.name. Nil when there is no actor.
	ActorName *string

	// AgentTokenID is set when an agent token acted on the user's behalf.
	AgentTokenID *uuid.UUID
	AgentName    *string
}

// Filter narrows the feed. Zero values mean "no filter".
type Filter struct {
	// Before returns entries with id < Before (the next page, newest first).
	Before int64
	// After returns entries with id > After (polling for new entries).
	After int64
	// Since returns entries created at or after Since.
	Since time.Time
	// Member is a user id or a person id; entries acted by that member.
	Member *uuid.UUID
	// EntityType, EntityID: one kind of record, or one record.
	EntityType string
	EntityID   *uuid.UUID
	// ParentType, ParentID: records inside one parent (items of one list).
	ParentType string
	ParentID   *uuid.UUID
	// ExcludeEntityTypes hides kinds the caller may not see (the read policy
	// hook for roles, #4689).
	ExcludeEntityTypes []string
	// Limit is the page size; the caller validates it.
	Limit int
}

// Repo reads activity_log.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates an activity repo.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// MemberRole returns userID's role in householdID, read from memberships, or
// "" if the user is not a member there.
func (r *Repo) MemberRole(ctx context.Context, householdID, userID uuid.UUID) (string, error) {
	var role string
	err := r.pool.QueryRow(ctx,
		`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
		householdID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("member role: %w", err)
	}
	return role, nil
}

// List returns householdID's entries matching f, newest first (id DESC).
// It reads up to f.Limit+1 rows; more reports whether another page exists.
func (r *Repo) List(ctx context.Context, householdID uuid.UUID, f Filter) (entries []Entry, more bool, err error) {
	where := []string{"a.household_id = $1"}
	args := []any{householdID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.Before > 0 {
		add("a.id < ?", f.Before)
	}
	if f.After > 0 {
		add("a.id > ?", f.After)
	}
	if !f.Since.IsZero() {
		add("a.created_at >= ?", f.Since)
	}
	if f.Member != nil {
		// A member is named by user id or by person id (kids have no login,
		// so only members with a user ever act).
		// Both placeholders bind the same argument.
		add(`a.actor_user_id = COALESCE(
			(SELECT mp.user_id FROM people mp WHERE mp.id = ? AND mp.household_id = $1), ?)`,
			*f.Member)
	}
	if f.EntityType != "" {
		add("a.entity_type = ?", f.EntityType)
	}
	if f.EntityID != nil {
		add("a.entity_id = ?", *f.EntityID)
	}
	if f.ParentType != "" {
		add("a.parent_type = ?", f.ParentType)
	}
	if f.ParentID != nil {
		add("a.parent_id = ?", *f.ParentID)
	}
	if len(f.ExcludeEntityTypes) > 0 {
		add("a.entity_type <> ALL(?)", f.ExcludeEntityTypes)
	}
	args = append(args, f.Limit+1)

	q := `
SELECT a.id, a.household_id, a.action, a.entity_type, a.entity_id,
       a.parent_type, a.parent_id, a.changed_fields, a.summary, a.created_at,
       a.actor_user_id, p.id, COALESCE(p.display_name, u.name),
       a.actor_agent_token_id, t.name,
       ev.ical_data, ev.ical_raw
FROM activity_log a
LEFT JOIN users u ON u.id = a.actor_user_id
LEFT JOIN people p ON p.household_id = a.household_id AND p.user_id = a.actor_user_id
LEFT JOIN agent_tokens t ON t.id = a.actor_agent_token_id
-- 050 names an event from an iCal SUMMARY line, but the apps store
-- ical_data as JSON, so most event rows carry no name. Read the event (only
-- for those rows, and only while it still exists) to name it; see eventName.
LEFT JOIN LATERAL (
    SELECT co.ical_data, co.ical_raw
    FROM calendar_objects co
    JOIN calendars c ON c.id = co.calendar_id AND c.household_id = a.household_id
    WHERE a.entity_type = 'calendar_event' AND a.summary NOT LIKE '%"%'
      AND co.id = a.entity_id
) ev ON true
WHERE ` + strings.Join(where, " AND ") + fmt.Sprintf(`
ORDER BY a.id DESC
LIMIT $%d`, len(args))

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list activity: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e Entry
		var icalData, icalRaw *string
		if err := rows.Scan(&e.ID, &e.HouseholdID, &e.Action, &e.EntityType, &e.EntityID,
			&e.ParentType, &e.ParentID, &e.ChangedFields, &e.Summary, &e.CreatedAt,
			&e.ActorUserID, &e.ActorPersonID, &e.ActorName,
			&e.AgentTokenID, &e.AgentName, &icalData, &icalRaw); err != nil {
			return nil, false, fmt.Errorf("scan activity: %w", err)
		}
		if icalData != nil {
			e.Summary = withLabel(e.Summary, "calendar event", eventName(*icalData, icalRaw))
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list activity: %w", err)
	}
	if len(entries) > f.Limit {
		return entries[:f.Limit], true, nil
	}
	return entries, false, nil
}

// summaryRe matches a VEVENT's SUMMARY line (with or without parameters).
var summaryRe = regexp.MustCompile(`(?m)^SUMMARY[^:\r\n]*:([^\r\n]*)`)

// eventName returns an event's title: the JSON "title" the api, mcp and
// calendar app write to ical_data, else the VEVENT's SUMMARY in ical_data or
// ical_raw. It is cleaned like 050's labels (trimmed, whitespace collapsed,
// at most 80 characters) and is "" when nothing can be read. Only the title
// is read: never the description, location or any other field.
func eventName(icalData string, icalRaw *string) string {
	var name string
	if strings.HasPrefix(strings.TrimSpace(icalData), "{") {
		var v struct {
			Title string `json:"title"`
		}
		if json.Unmarshal([]byte(icalData), &v) == nil {
			name = v.Title
		}
	}
	if name == "" {
		for _, src := range []*string{&icalData, icalRaw} {
			if src == nil {
				continue
			}
			ics := *src
			if i := strings.Index(ics, "BEGIN:VEVENT"); i >= 0 {
				ics = ics[i:]
			} else {
				continue
			}
			if m := summaryRe.FindStringSubmatch(ics); m != nil {
				name = m[1]
				break
			}
		}
	}
	name = strings.Join(strings.Fields(name), " ")
	if utf8.RuneCountInString(name) > 80 {
		name = string([]rune(name)[:80])
	}
	return name
}

// withLabel puts label into a 050 summary that has none:
// `updated calendar event (start)` -> `updated calendar event "Dinner" (start)`.
// A summary that already has a label, or doesn't name kind, is returned as is.
func withLabel(summary, kind, label string) string {
	if label == "" || strings.Contains(summary, `"`) {
		return summary
	}
	verb, rest, ok := strings.Cut(summary, " ")
	if !ok || !strings.HasPrefix(rest, kind) {
		return summary
	}
	label = strings.ReplaceAll(label, `"`, "'")
	return verb + " " + kind + ` "` + label + `"` + rest[len(kind):]
}
