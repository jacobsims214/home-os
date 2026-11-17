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
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/file"
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
	// Changes is activity_log.changes (migration 071, #4941): the before and
	// after VALUE of the columns the log records values for (money, dates and
	// a bill's source document), as {"amount": {"from": 5, "to": 35}}. Nil for
	// every other change.
	Changes   map[string]ValueChange
	CreatedAt time.Time

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

// ValueChange is one column's before and after value.
type ValueChange struct {
	From any `json:"from"`
	To   any `json:"to"`
}

// Filter narrows the feed. Zero values mean "no filter".
type Filter struct {
	// Before returns entries with id < Before (the next page, newest first).
	Before int64
	// After polls for new entries: it is the PollAfter of the previous
	// response (an entry id). Entries come in (xact_id, id) order after that
	// entry's position, and only from transactions every later snapshot sees
	// as finished, so a late commit is never skipped (see List).
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
	// HideSecretFiles hides entries about a file attached to or linked from
	// a credential (file.SecretFileSQL): the file's own entries, records in
	// it (a note on it) and links pointing at it. Set when the role may not
	// see secrets.
	HideSecretFiles bool
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

// Page is one response of the feed.
type Page struct {
	// Entries, newest first: id DESC, or for a poll (Filter.After) the poll
	// order (xact_id, id) DESC.
	Entries []Entry
	// More: another page exists. For a head or cursor page, page on with the
	// last entry's id as Filter.Before. For a poll, poll again at once.
	More bool
	// PollAfter is the entry id to pass as Filter.After on the next poll; 0
	// when the household has no settled entry yet. It is set for the head
	// of the feed and for polls, not for older pages (Filter.Before).
	PollAfter int64
}

// snapXmin is the reading snapshot's xmin: every transaction below it has
// finished, and every row not yet visible belongs to one at or above it.
const snapXmin = "pg_snapshot_xmin(pg_current_snapshot())"

// List returns householdID's entries matching f, newest first. It reads up
// to f.Limit+1 rows to tell whether another page exists.
//
// Polling (f.After) is safe against transactions that commit out of id
// order (#4741). activity_log.id is taken at INSERT, so "id > newest seen"
// misses a row whose transaction commits after a newer one was returned.
// Instead a poll walks the log in (xact_id, id) order, xact_id being the
// writing transaction (migration 062), and returns only rows with xact_id
// below the snapshot's xmin. Those rows form a prefix of that order that
// can no longer change: their transactions have all finished, and every row
// still to become visible has xact_id >= xmin, so it sorts after anything a
// poll returned. Comparing xids alone is not enough, since a transaction
// can take its xid before another but log after it.
//
// Everything runs in one REPEATABLE READ snapshot so the rows and PollAfter
// agree. A write transaction that stays open holds polls back (anywhere in
// the database: xmin is global) until it ends; the head of the feed and
// older pages are not held back.
func (r *Repo) List(ctx context.Context, householdID uuid.UUID, f Filter) (Page, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Page{}, fmt.Errorf("list activity: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	where := []string{"a.household_id = $1"}
	args := []any{householdID}
	add := func(cond string, v ...any) {
		for _, x := range v {
			args = append(args, x)
			cond = strings.Replace(cond, "?", fmt.Sprintf("$%d", len(args)), 1)
		}
		where = append(where, cond)
	}
	order := "a.id DESC"
	if f.Before > 0 {
		add("a.id < ?", f.Before)
	}
	if f.After > 0 {
		add("a.xact_id < " + snapXmin)
		// The poll position is the After entry's (xact_id, id). An id that
		// is not in this household (a forged or stale one) falls back to
		// plain id order.
		var xact *string
		err := tx.QueryRow(ctx,
			`SELECT xact_id::text FROM activity_log WHERE id = $1 AND household_id = $2`,
			f.After, householdID).Scan(&xact)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			add("a.id > ?", f.After)
		case err != nil:
			return Page{}, fmt.Errorf("list activity: poll position: %w", err)
		default:
			add("(a.xact_id, a.id) > (?::xid8, ?)", *xact, f.After)
		}
		// Oldest first, so a full page is continued by the next poll.
		order = "a.xact_id, a.id"
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
			*f.Member, *f.Member)
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
		// A record attached to a hidden one (a note on a secret) is hidden too.
		add("(a.parent_type IS NULL OR a.parent_type <> ALL(?))", f.ExcludeEntityTypes)
	}
	if f.HideSecretFiles {
		where = append(where, hiddenFileEntries)
	}
	args = append(args, f.Limit+1)

	q := `
SELECT a.id, a.household_id, a.action, a.entity_type, a.entity_id,
       a.parent_type, a.parent_id, a.changed_fields, a.summary, a.changes, a.created_at,
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
ORDER BY %s
LIMIT $%d`, order, len(args))

	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("list activity: %w", err)
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		var e Entry
		var icalData, icalRaw *string
		var changes []byte
		if err := rows.Scan(&e.ID, &e.HouseholdID, &e.Action, &e.EntityType, &e.EntityID,
			&e.ParentType, &e.ParentID, &e.ChangedFields, &e.Summary, &changes, &e.CreatedAt,
			&e.ActorUserID, &e.ActorPersonID, &e.ActorName,
			&e.AgentTokenID, &e.AgentName, &icalData, &icalRaw); err != nil {
			return Page{}, fmt.Errorf("scan activity: %w", err)
		}
		if icalData != nil {
			e.Summary = withLabel(e.Summary, "calendar event", eventName(*icalData, icalRaw))
		}
		if len(changes) > 0 {
			var decoded map[string]ValueChange
			if err := json.Unmarshal(changes, &decoded); err == nil {
				e.Changes = decoded
			}
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("list activity: %w", err)
	}
	rows.Close()

	p := Page{Entries: entries}
	if len(entries) > f.Limit {
		p.Entries, p.More = entries[:f.Limit], true
	}
	if f.After > 0 {
		// Present a poll newest first, like the rest of the feed.
		slices.Reverse(p.Entries)
	}
	switch {
	case f.Before > 0:
		// An older page: polling starts from the head, not from here.
	case f.After > 0 && p.More:
		// A cut poll continues right after its newest entry.
		p.PollAfter = p.Entries[0].ID
	default:
		// The settled end of the log: the last (xact_id, id) below xmin. A
		// head page may show newer rows from transactions at or above xmin;
		// the next poll returns them again (dedupe by id), never skips them.
		err := tx.QueryRow(ctx, `
SELECT id FROM activity_log
WHERE household_id = $1 AND xact_id < `+snapXmin+`
ORDER BY xact_id DESC, id DESC
LIMIT 1`, householdID).Scan(&p.PollAfter)
		if errors.Is(err, pgx.ErrNoRows) {
			p.PollAfter = f.After
		} else if err != nil {
			return Page{}, fmt.Errorf("list activity: poll position: %w", err)
		}
	}
	return p, nil
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

// hiddenFileEntries leaves out entries about files a role without secrets
// read must not see (file.SecretFileSQL, the same rule as the files API and
// the MCP): the file's own entries, entries whose parent is the file (a note
// on it) and document links pointing at it. An entry about a file deleted
// since is kept: the rule needs the file row.
var hiddenFileEntries = `NOT (a.entity_type = 'file' AND ` + file.SecretFileSQL("a.entity_id") + `)
	AND NOT (a.parent_type IS NOT DISTINCT FROM 'file' AND ` + file.SecretFileSQL("a.parent_id") + `)
	AND NOT (a.entity_type = 'document_link' AND EXISTS (
		SELECT 1 FROM document_links al
		 WHERE al.id = a.entity_id AND al.link_type = 'file'
		   AND ` + file.SecretFileSQL(`(CASE WHEN al.link_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
		                                   THEN al.link_id::uuid END)`) + `))`
