// Package people serves the catalog v2 tools that read and change one person
// of the family (KB architecture/mcp-tool-catalog.md, "person"; ticket
// #4464): person_get, person_create, person_update and person_delete.
// person_find lives in internal/household with family_overview.
//
// People are the migration 041 tables: people (members who log in, and kids,
// grandparents and nannies who don't) and person_preferences. The catalog's
// preference kinds map onto them as follows:
//
//	allergy -> people.allergies (text[]; housesitters may see these; notes
//	           are kept in the entry: "peanuts (carries an EpiPen)")
//	diet    -> person_preferences kind 'dietary'
//	like    -> 'like'
//	dislike -> 'dislike'
//	note    -> 'other'
//
// so "Nora is allergic to peanuts" is one person_update call
// (add_preferences=[{kind:"allergy", item:"peanuts"}]) and reads back with
// one person_get call.
//
// Access follows the household role policy (KB
// decisions/2026-11-15-role-policy.md, #4689) and the API's people handler
// (KB api/people-preferences.md). The role is re-read from memberships on
// every call, as credential and search do:
//
//   - owner, family_manager: read everything; create, update and delete any
//     person.
//   - family_member: read everything; update only their own linked record.
//   - viewer: read everything, change nothing.
//   - housesitter: name, relationship and allergies only; change nothing.
//   - vendor (and any unknown role): nothing.
package people

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// handlerFunc is the handler signature server.RegisterTool takes.
type handlerFunc = func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)

// querier is what both a pool and a transaction offer.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Limits, matching the column sizes of migration 041 and the API.
const (
	maxName        = 255
	maxSchool      = 255
	maxGrade       = 50
	maxCategory    = 50
	maxItem        = 255
	maxPrefChanges = 20
)

// relationships is the catalog's relationship enum for person_create and
// person_update.
var relationships = []string{"parent", "child", "grandparent", "caregiver", "other"}

// relationshipAliases maps words small models use to the enum.
var relationshipAliases = map[string]string{
	"mom": "parent", "mother": "parent", "dad": "parent", "father": "parent",
	"son": "child", "daughter": "child", "kid": "child", "baby": "child",
	"grandma": "grandparent", "grandmother": "grandparent", "grandpa": "grandparent", "grandfather": "grandparent",
	"nanny": "caregiver", "babysitter": "caregiver", "sitter": "caregiver", "au pair": "caregiver",
}

// prefKinds is the catalog's preference kind enum.
var prefKinds = []string{"allergy", "diet", "dislike", "like", "note"}

// kindAliases maps other spellings to a catalog kind.
var kindAliases = map[string]string{
	"allergies": "allergy", "allergic": "allergy", "allergen": "allergy",
	"dietary": "diet", "diets": "diet",
	"likes": "like", "favorite": "like", "favourite": "like", "loves": "like",
	"dislikes": "dislike", "hates": "dislike",
	"notes": "note", "other": "note",
}

// dbKind maps a catalog kind (not allergy) to person_preference_kind.
func dbKind(kind string) string {
	switch kind {
	case "diet":
		return "dietary"
	case "note":
		return "other"
	}
	return kind
}

// catalogKind maps a person_preference_kind to the catalog kind.
func catalogKind(kind string) string {
	switch kind {
	case "dietary":
		return "diet"
	case "other":
		return "note"
	}
	return kind
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

// Preference is one stored like, dislike, diet or note.
type Preference struct {
	PreferenceID string  `json:"preference_id"`
	Kind         string  `json:"kind" jsonschema_description:"diet, dislike, like or note (allergies are in allergies)"`
	Item         string  `json:"item" jsonschema_description:"What it is, e.g. broccoli or vegetarian"`
	Category     string  `json:"category" jsonschema_description:"What it is about, e.g. food; general when not set"`
	Notes        *string `json:"notes,omitempty"`
}

// Birthday is a person's next birthday in the family timezone.
type Birthday struct {
	LocalDate string `json:"local_date" jsonschema_description:"YYYY-MM-DD"`
	Weekday   string `json:"weekday" jsonschema_description:"e.g. Thu"`
	Turns     int    `json:"turns" jsonschema_description:"Age they turn that day"`
}

// Linked counts the records about a person.
type Linked struct {
	Notes         int `json:"notes" jsonschema_description:"Notes about them (note_find about_type=person)"`
	Documents     int `json:"documents" jsonschema_description:"Documents for them (document_find person_id)"`
	OpenListItems int `json:"open_list_items" jsonschema_description:"Open list items assigned to them"`
}

// Person is one person in full, the data of person_get, person_create and
// person_update. A housesitter gets name, relationship and allergies only
// (limited:true).
type Person struct {
	PersonID     string       `json:"person_id" jsonschema_description:"person_id to pass to other tools"`
	Name         string       `json:"name"`
	Relationship *string      `json:"relationship" jsonschema_description:"e.g. parent or child; null if not set"`
	HasAccount   bool         `json:"has_account" jsonschema_description:"true when they log in to Home OS"`
	Allergies    []string     `json:"allergies" jsonschema_description:"Allergies, e.g. peanuts"`
	Age          *int         `json:"age,omitempty" jsonschema_description:"Age in years in the family timezone"`
	Birthdate    *string      `json:"birthdate,omitempty" jsonschema_description:"YYYY-MM-DD"`
	NextBirthday *Birthday    `json:"next_birthday,omitempty"`
	School       *string      `json:"school,omitempty"`
	Grade        *string      `json:"grade,omitempty"`
	Notes        *string      `json:"notes,omitempty"`
	Preferences  []Preference `json:"preferences,omitzero" jsonschema_description:"Likes, dislikes, diets and notes"`
	UserID       *string      `json:"user_id,omitempty" jsonschema_description:"Their login's user id"`
	Email        *string      `json:"email,omitempty"`
	Role         *string      `json:"role,omitempty" jsonschema_description:"Membership role, e.g. owner or family_member"`
	Linked       *Linked      `json:"linked,omitempty"`
	Limited      bool         `json:"limited,omitempty" jsonschema_description:"true when your role sees only name, relationship and allergies"`

	// Set by person_create.
	Duplicate bool `json:"duplicate,omitempty" jsonschema_description:"true when person_create returned the person added minutes ago instead of a copy"`
	// Set by person_update.
	Changed   []string `json:"changed,omitzero" jsonschema_description:"Fields person_update changed"`
	Added     []string `json:"added,omitzero" jsonschema_description:"Preferences added, e.g. allergy: peanuts"`
	Removed   []string `json:"removed,omitzero" jsonschema_description:"Preferences removed"`
	Unchanged []string `json:"unchanged,omitzero" jsonschema_description:"Preferences already there (add) or not there (remove)"`
}

// Removed counts what person_delete deleted with the person.
type Removed struct {
	Preferences int `json:"preferences"`
	Allergies   int `json:"allergies"`
	Notes       int `json:"notes" jsonschema_description:"Notes about them, deleted with them"`
}

// Unlinked counts records that stay but no longer point at the person.
type Unlinked struct {
	Documents   int `json:"documents"`
	ListItems   int `json:"list_items"`
	MealRatings int `json:"meal_ratings"`
	Events      int `json:"events" jsonschema_description:"Events that keep their name as text"`
}

// Deleted is the data of person_delete.
type Deleted struct {
	PersonID string   `json:"person_id"`
	Name     string   `json:"name"`
	Deleted  bool     `json:"deleted"`
	Removed  Removed  `json:"removed"`
	Unlinked Unlinked `json:"unlinked"`
}

// ---------------------------------------------------------------------------
// Access
// ---------------------------------------------------------------------------

// caller is who is calling, with the role re-read from memberships.
type caller struct {
	householdID uuid.UUID
	userID      string
	role        string
	tz          string
}

// callerOf resolves the caller's current role the way credential and search
// do: re-read from memberships for the claims' user and household, so a
// demoted member loses access at once and a removed one gets nothing. It
// also reads the family timezone. A non-nil result is the isError to return.
func callerOf(ctx context.Context, pool *pgxpool.Pool) (caller, *mcp.CallToolResult) {
	c := server.ClaimsFromContext(ctx)
	if c == nil {
		return caller{}, notSignedIn()
	}
	hid, err := uuid.Parse(c.HouseholdID)
	if err != nil {
		return caller{}, notSignedIn()
	}
	out := caller{householdID: hid, role: c.Role}
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.AgentTokenID != "" { // an agent token always resolves to a users.id
			return caller{}, notSignedIn()
		}
		// Local-dev claims without a user: keep the claims' role.
		if err := pool.QueryRow(ctx, `SELECT timezone FROM households WHERE id = $1`, hid).Scan(&out.tz); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return caller{}, notSignedIn()
			}
			return caller{}, respond.Failed("check your access", err)
		}
		return out, nil
	}
	out.userID = uid.String()
	err = pool.QueryRow(ctx,
		`SELECT m.role::text, h.timezone FROM memberships m JOIN households h ON h.id = m.household_id
		  WHERE m.household_id = $1 AND m.user_id = $2`, hid, uid).Scan(&out.role, &out.tz)
	if errors.Is(err, pgx.ErrNoRows) {
		return caller{}, notSignedIn()
	}
	if err != nil {
		return caller{}, respond.Failed("check your access", err)
	}
	return out, nil
}

// canManage reports whether role may create, update and delete any person.
func canManage(role string) bool { return role == "owner" || role == "family_manager" }

// readsFull reports whether role may read people in full.
func readsFull(role string) bool {
	return canManage(role) || role == "family_member" || role == "viewer"
}

// readsBasic reports whether role may read names, relationships and
// allergies. Unknown roles may not, so a new role fails closed.
func readsBasic(role string) bool { return readsFull(role) || role == "housesitter" }

func roleLabel(role string) string {
	if role == "" {
		return "current"
	}
	return role
}

// noPeopleAccess is the refusal for roles that may not see people (vendor).
func noPeopleAccess(role string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("Members with the %s role can't see the family's people.", roleLabel(role)),
		Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
	})
}

// managersOnly is the refusal for a create or delete by a role that may not.
func managersOnly(role, tool, what string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't %s: only an owner or family manager can (tool %s).", roleLabel(role), what, tool),
		Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
	})
}

// canEdit reports, as an isError or nil, whether c may update person p.
func canEdit(c caller, p personRow) *mcp.CallToolResult {
	if canManage(c.role) {
		return nil
	}
	if c.role == "family_member" {
		if p.userID != nil && c.userID != "" && *p.userID == c.userID {
			return nil
		}
		return respond.Fail(respond.Error{
			Message: fmt.Sprintf("The family_member role can change only their own person record, not %s's.", p.name),
			Fix:     "Tell the user to ask an owner or family manager to change it; retrying will not help.",
		})
	}
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role is read-only: tool person_update can change data.", roleLabel(c.role)),
		Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
	})
}

func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}

// ---------------------------------------------------------------------------
// Lookup
// ---------------------------------------------------------------------------

// resolvePerson matches a person_id (people id, user id, exact name or first
// name, case-insensitive) to a person of household hid. Failures are
// corrective results that list the family's people.
func resolvePerson(ctx context.Context, q querier, hid uuid.UUID, ref string) (string, *mcp.CallToolResult) {
	ref = strings.TrimSpace(ref)
	example := map[string]any{"person_id": "Nora"}
	if ref == "" {
		return "", respond.Invalid("person_id", "person_id is required.",
			"Pass the person's name or id from person_find, e.g. person_id=\"Nora\".", example)
	}
	type candidate struct{ id, userID, name string }
	rows, err := q.Query(ctx,
		`SELECT id::text, COALESCE(user_id::text, ''), display_name FROM people WHERE household_id = $1 ORDER BY lower(display_name), id`, hid)
	if err != nil {
		return "", respond.Failed("look up people", err)
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (candidate, error) {
		var c candidate
		return c, r.Scan(&c.id, &c.userID, &c.name)
	})
	if err != nil {
		return "", respond.Failed("look up people", err)
	}
	var hits []candidate
	for _, c := range all {
		if strings.EqualFold(ref, c.id) || (c.userID != "" && strings.EqualFold(ref, c.userID)) {
			hits = []candidate{c}
			break
		}
	}
	if _, isID := uuid.Parse(ref); len(hits) == 0 && isID != nil {
		for _, c := range all {
			if strings.EqualFold(ref, strings.TrimSpace(c.name)) {
				hits = append(hits, c)
			}
		}
		if len(hits) == 0 { // first name only: "Nora" for "Nora Smith"
			for _, c := range all {
				if f := strings.Fields(c.name); len(f) > 1 && strings.EqualFold(ref, f[0]) {
					hits = append(hits, c)
				}
			}
		}
	}
	switch len(hits) {
	case 1:
		return hits[0].id, nil
	case 0:
		names := make([]string, 0, len(all))
		for i, c := range all {
			if i == 30 {
				names = append(names, "...")
				break
			}
			names = append(names, c.name)
		}
		list := strings.Join(names, ", ")
		if list == "" {
			list = "none yet"
		}
		return "", respond.Fail(respond.Error{
			Message: fmt.Sprintf("No person %q in the family (people: %s).", ref, list),
			Fix:     "Pass an exact name or a person id from person_find.",
			Field:   "person_id", Example: example,
		})
	default:
		c := make([]string, len(hits))
		for i, h := range hits {
			c[i] = fmt.Sprintf("%s (%s)", h.name, h.id)
		}
		return "", respond.Fail(respond.Error{
			Message: fmt.Sprintf("%q matches more than one person: %s.", ref, strings.Join(c, "; ")),
			Fix:     "Pass the person's id instead of the name.",
			Field:   "person_id", Example: map[string]any{"person_id": hits[0].id},
		})
	}
}

// personRow is a people row with its login, as stored.
type personRow struct {
	id, name     string
	relationship *string
	birthdate    *time.Time
	school       *string
	grade        *string
	notes        *string
	allergies    []string
	userID       *string // people.user_id
	email        *string // only while a current member
	role         *string // only while a current member
}

// loadPerson reads person id of household hid; lock takes FOR UPDATE on the
// people row. pgx.ErrNoRows means it is gone (or another family's).
func loadPerson(ctx context.Context, q querier, hid uuid.UUID, id string, lock bool) (personRow, error) {
	sql := `
SELECT p.id::text, p.display_name, p.relationship, p.birthdate, p.school, p.grade, p.notes, p.allergies,
       p.user_id::text, u.email, m.role::text
  FROM people p
  LEFT JOIN memberships m ON m.household_id = p.household_id AND m.user_id = p.user_id
  LEFT JOIN users u ON u.id = m.user_id
 WHERE p.household_id = $1 AND p.id = $2`
	if lock {
		sql += ` FOR UPDATE OF p`
	}
	var p personRow
	err := q.QueryRow(ctx, sql, hid, id).Scan(&p.id, &p.name, &p.relationship, &p.birthdate, &p.school, &p.grade,
		&p.notes, &p.allergies, &p.userID, &p.email, &p.role)
	if p.allergies == nil {
		p.allergies = []string{}
	}
	return p, err
}

// familyToday is today's date at midnight UTC as seen in timezone tz (UTC
// when tz does not load).
func familyToday(now time.Time, tz string) time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ageOn returns the age on today of someone born on b, and their next
// birthday (today counts). A 29 February birthday falls on 28 February in
// other years.
func ageOn(b, today time.Time) (int, Birthday) {
	age := today.Year() - b.Year()
	if today.Month() < b.Month() || (today.Month() == b.Month() && today.Day() < b.Day()) {
		age--
	}
	on := func(year int) time.Time {
		if b.Month() == time.February && b.Day() == 29 && !isLeap(year) {
			return time.Date(year, time.February, 28, 0, 0, 0, 0, time.UTC)
		}
		return time.Date(year, b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	}
	next := on(today.Year())
	if next.Before(today) {
		next = on(today.Year() + 1)
	}
	return age, Birthday{LocalDate: next.Format(time.DateOnly), Weekday: next.Format("Mon"), Turns: next.Year() - b.Year()}
}

func isLeap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

// present builds the Person a caller with role sees. Full views load the
// preferences and linked counts; a housesitter gets the limited view.
func present(ctx context.Context, q querier, hid uuid.UUID, p personRow, role, tz string) (Person, error) {
	out := Person{
		PersonID:     p.id,
		Name:         p.name,
		Relationship: p.relationship,
		HasAccount:   p.role != nil,
		Allergies:    slices.Clone(p.allergies),
	}
	if out.Allergies == nil {
		out.Allergies = []string{}
	}
	if !readsFull(role) {
		out.Limited = true
		return out, nil
	}
	if p.birthdate != nil {
		age, next := ageOn(*p.birthdate, familyToday(time.Now(), tz))
		b := p.birthdate.Format(time.DateOnly)
		out.Age, out.Birthdate, out.NextBirthday = &age, &b, &next
	}
	out.School, out.Grade, out.Notes = p.school, p.grade, p.notes
	if p.role != nil {
		out.UserID, out.Email, out.Role = p.userID, p.email, p.role
	}

	rows, err := q.Query(ctx, `
SELECT id::text, kind::text, value, category, notes
  FROM person_preferences
 WHERE household_id = $1 AND person_id = $2
 ORDER BY kind::text, lower(category), lower(value), id`, hid, p.id)
	if err != nil {
		return out, err
	}
	out.Preferences, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Preference, error) {
		var pr Preference
		err := r.Scan(&pr.PreferenceID, &pr.Kind, &pr.Item, &pr.Category, &pr.Notes)
		pr.Kind = catalogKind(pr.Kind)
		return pr, err
	})
	if err != nil {
		return out, err
	}
	if out.Preferences == nil {
		out.Preferences = []Preference{}
	}

	var l Linked
	if err := q.QueryRow(ctx, `
SELECT (SELECT count(*) FROM notes WHERE household_id = $1 AND entity_type = 'person' AND entity_id = $2),
       (SELECT count(*) FROM files WHERE household_id = $1 AND (person_id = $2 OR (entity_type = 'person' AND entity_id = $2))),
       (SELECT count(*) FROM list_items WHERE household_id = $1 AND assignee_person_id = $2 AND NOT checked)`,
		hid, p.id).Scan(&l.Notes, &l.Documents, &l.OpenListItems); err != nil {
		return out, err
	}
	out.Linked = &l
	return out, nil
}

// describe is a short "child, age 10" for summaries.
func describe(p Person) string {
	var parts []string
	if p.Relationship != nil && *p.Relationship != "" {
		parts = append(parts, *p.Relationship)
	}
	if p.Age != nil {
		parts = append(parts, fmt.Sprintf("age %d", *p.Age))
	}
	if len(parts) == 0 {
		return p.Name
	}
	return fmt.Sprintf("%s (%s)", p.Name, strings.Join(parts, ", "))
}

// ---------------------------------------------------------------------------
// Arguments
// ---------------------------------------------------------------------------

// textArg is an optional text argument. An absent key or "" means "not
// given" (small models send "" for unused params); JSON null means "clear".
type textArg struct {
	set, clear bool
	val        string
}

// readText reads args[key]; numbers are accepted as text (grade 5). ok is
// false for any other type.
func readText(args map[string]any, key string) (textArg, bool) {
	v, present := args[key]
	if !present {
		return textArg{}, true
	}
	switch x := v.(type) {
	case nil:
		return textArg{set: true, clear: true}, true
	case string:
		x = strings.TrimSpace(x)
		if x == "" {
			return textArg{}, true
		}
		return textArg{set: true, val: x}, true
	case float64:
		return textArg{set: true, val: strconv.FormatFloat(x, 'f', -1, 64)}, true
	case int:
		return textArg{set: true, val: strconv.Itoa(x)}, true
	default:
		return textArg{}, false
	}
}

func boolArg(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(v))
		return b
	}
	return false
}

// normRelationship maps v to the relationship enum; ok is false when it is
// not one.
func normRelationship(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if slices.Contains(relationships, v) {
		return v, true
	}
	r, ok := relationshipAliases[v]
	return r, ok
}

// fields are the editable columns given in a create or update call.
type fields struct {
	name, relationship, birthdate, school, grade, notes textArg
}

// readFields reads and validates the person fields. today is the family's
// date, for "birthdate not in the future". requireCore makes display_name
// and relationship required (person_create).
func readFields(args map[string]any, today time.Time, requireCore bool, example map[string]any) (fields, *mcp.CallToolResult) {
	var f fields
	for _, it := range []struct {
		key string
		dst *textArg
	}{{"display_name", &f.name}, {"relationship", &f.relationship}, {"birthdate", &f.birthdate},
		{"school", &f.school}, {"grade", &f.grade}, {"notes", &f.notes}} {
		a, ok := readText(args, it.key)
		if !ok {
			return f, respond.Invalid(it.key, it.key+" must be text.", "Pass "+it.key+" as a string, or leave it out.", example)
		}
		*it.dst = a
	}
	if requireCore && (!f.name.set || f.name.clear) {
		return f, respond.Invalid("display_name", "display_name is required.",
			"Pass the person's name, e.g. display_name=\"Nora\".", example)
	}
	if f.name.clear {
		return f, respond.Invalid("display_name", "display_name can't be cleared.",
			"Pass a new name, or leave display_name out to keep it.", example)
	}
	if f.name.set && len([]rune(f.name.val)) > maxName {
		return f, respond.Invalid("display_name", fmt.Sprintf("display_name is longer than %d characters.", maxName),
			"Pass a shorter name.", example)
	}
	if requireCore && (!f.relationship.set || f.relationship.clear) {
		return f, respond.Invalid("relationship", "relationship is required.",
			"Pass one of "+strings.Join(relationships, ", ")+".", example)
	}
	if f.relationship.set && !f.relationship.clear {
		r, ok := normRelationship(f.relationship.val)
		if !ok {
			return f, respond.Invalid("relationship", fmt.Sprintf("relationship %q is not one of %s.", f.relationship.val, strings.Join(relationships, ", ")),
				"Pass one of "+strings.Join(relationships, ", ")+"; use other and notes for anything else.", example)
		}
		f.relationship.val = r
	}
	if f.birthdate.set && !f.birthdate.clear {
		d, err := time.Parse(time.DateOnly, f.birthdate.val)
		switch {
		case err != nil:
			return f, respond.Invalid("birthdate", fmt.Sprintf("birthdate %q is not a date.", f.birthdate.val),
				"Pass birthdate as YYYY-MM-DD, e.g. 2015-04-02.", map[string]any{"birthdate": "2015-04-02"})
		case d.After(today):
			return f, respond.Invalid("birthdate", fmt.Sprintf("birthdate %s is in the future.", f.birthdate.val),
				"Pass the real birthdate as YYYY-MM-DD.", map[string]any{"birthdate": "2015-04-02"})
		case d.Year() < 1900:
			return f, respond.Invalid("birthdate", fmt.Sprintf("birthdate %s is before 1900.", f.birthdate.val),
				"Pass the real birthdate as YYYY-MM-DD.", map[string]any{"birthdate": "1950-06-01"})
		}
	}
	if f.school.set && len([]rune(f.school.val)) > maxSchool {
		return f, respond.Invalid("school", fmt.Sprintf("school is longer than %d characters.", maxSchool), "Pass a shorter school name.", example)
	}
	if f.grade.set && len([]rune(f.grade.val)) > maxGrade {
		return f, respond.Invalid("grade", fmt.Sprintf("grade is longer than %d characters.", maxGrade), "Pass the grade, e.g. 5 or K.", example)
	}
	return f, nil
}

// given reports whether any field was passed.
func (f fields) given() bool {
	return f.name.set || f.relationship.set || f.birthdate.set || f.school.set || f.grade.set || f.notes.set
}

// prefChange is one add_preferences / remove_preferences entry.
type prefChange struct {
	kind     string // catalog kind
	item     string
	category string // "" when not given
	notes    *string
}

func (c prefChange) label() string { return c.kind + ": " + c.value() }

// value is what is stored. An allergy keeps its notes in people.allergies as
// "peanuts (carries an EpiPen)", because that array is the only allergy
// storage and housesitters see it (they do not see notes or preferences).
// Other kinds store notes in person_preferences.notes.
func (c prefChange) value() string {
	if c.kind == "allergy" && c.notes != nil {
		return c.item + " (" + *c.notes + ")"
	}
	return c.item
}

// allergen is the bare allergen of a stored or given allergy: the part before
// " (", so "peanuts (carries an EpiPen)" is "peanuts".
func allergen(s string) string {
	base, _, _ := strings.Cut(strings.TrimSpace(s), " (")
	return strings.TrimSpace(base)
}

// allergyIndex returns the index of the first stored allergy whose value or
// bare allergen equals the given allergen (ignoring case), or -1.
func allergyIndex(list []string, item string) int {
	for i, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), item) || strings.EqualFold(allergen(v), allergen(item)) {
			return i
		}
	}
	return -1
}

// prefSchema is the JSON schema of one preference entry.
var prefSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"kind":     map[string]any{"type": "string", "enum": prefKinds, "description": "allergy, diet, dislike, like or note"},
		"item":     map[string]any{"type": "string", "description": "What it is, e.g. peanuts"},
		"category": map[string]any{"type": "string", "description": "Optional topic, e.g. food (ignored for allergy)"},
		"notes":    map[string]any{"type": "string", "description": "Optional detail, e.g. carries an EpiPen. For an allergy it is kept with it as peanuts (carries an EpiPen); adding the allergy again with a new note replaces the note"},
	},
	"required": []string{"kind", "item"},
}

// readPrefs reads add_preferences or remove_preferences: an array of
// {kind, item} objects. A single object, and the same encoded as a JSON
// string, are accepted too. Duplicates within the call are dropped.
func readPrefs(args map[string]any, key string) ([]prefChange, *mcp.CallToolResult) {
	list, bad := prefList(args, key)
	if bad != nil {
		return nil, bad
	}
	out, bad := parsePrefs(list, key)
	if bad != nil {
		return nil, bad
	}
	return dedupePrefs(out), nil
}

// readAddPrefs reads what to add: add_preferences, plus the allergies alias
// that person_create takes (["peanuts"], "peanuts", or "peanuts, milk"),
// each meaning {kind:"allergy", item:x}. Both lists are validated and
// deduplicated together, as one add_preferences list would be.
func readAddPrefs(args map[string]any) ([]prefChange, *mcp.CallToolResult) {
	list, bad := prefList(args, "add_preferences")
	if bad != nil {
		return nil, bad
	}
	out, bad := parsePrefs(list, "add_preferences")
	if bad != nil {
		return nil, bad
	}
	alias, bad := allergyList(args, "allergies")
	if bad != nil {
		return nil, bad
	}
	more, bad := parsePrefs(alias, "allergies")
	if bad != nil {
		return nil, bad
	}
	out = dedupePrefs(append(out, more...))
	if len(out) > maxPrefChanges {
		return nil, respond.Invalid("add_preferences", fmt.Sprintf("%d preferences and allergies to add; at most %d per call.", len(out), maxPrefChanges),
			"Add the rest with person_update.", map[string]any{"allergies": []any{"peanuts"}})
	}
	return out, nil
}

// prefList is args[key] as a list of entries: an array, a single object, or
// either encoded as a JSON string.
func prefList(args map[string]any, key string) ([]any, *mcp.CallToolResult) {
	raw := args[key]
	if s, ok := raw.(string); ok {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, nil
		}
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			return nil, prefsInvalid(key, key+" is text, not a list of preferences.")
		}
	}
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case []any:
		return v, nil
	case map[string]any:
		return []any{v}, nil
	default:
		return nil, prefsInvalid(key, key+" must be a list of preferences.")
	}
}

// allergyList is the allergies alias as preference entries. Strings are
// allergens; a plain string may list several, separated by commas or
// semicolons outside parentheses. Objects are entries whose kind defaults to
// allergy.
func allergyList(args map[string]any, key string) ([]any, *mcp.CallToolResult) {
	example := map[string]any{"display_name": "Leo", "relationship": "child", key: []any{"peanuts"}}
	bad := func(msg string) ([]any, *mcp.CallToolResult) {
		return nil, respond.Invalid(key, msg, `Pass `+key+` as a list of allergens, e.g. ["peanuts"].`, example)
	}
	raw := args[key]
	if s, ok := raw.(string); ok {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, nil
		}
		if strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{") {
			if err := json.Unmarshal([]byte(s), &raw); err != nil {
				return bad(key + " is not a list of allergens.")
			}
		} else {
			raw = splitAllergens(s)
		}
	}
	var list []any
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case []any:
		list = v
	case []string:
		for _, x := range v {
			list = append(list, x)
		}
	case map[string]any:
		list = []any{v}
	default:
		return bad(key + " must be a list of allergens.")
	}
	if len(list) > maxPrefChanges {
		return bad(fmt.Sprintf("%s has %d entries; at most %d per call.", key, len(list), maxPrefChanges))
	}
	out := make([]any, 0, len(list))
	for i, e := range list {
		switch x := e.(type) {
		case string:
			if strings.TrimSpace(x) == "" {
				continue
			}
			out = append(out, map[string]any{"kind": "allergy", "item": x})
		case map[string]any:
			m := map[string]any{"kind": "allergy"}
			for k, v := range x {
				if k == "kind" && strings.TrimSpace(fmt.Sprint(valueOr(v, ""))) == "" {
					continue
				}
				m[k] = v
			}
			out = append(out, m)
		default:
			return bad(fmt.Sprintf("%s[%d] is not an allergen.", key, i))
		}
	}
	return out, nil
}

// splitAllergens splits "peanuts, milk (hives only)" at commas and
// semicolons outside parentheses.
func splitAllergens(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',', ';':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

func prefsInvalid(key, msg string) *mcp.CallToolResult {
	example := map[string]any{"person_id": "Nora", key: []any{map[string]any{"kind": "allergy", "item": "peanuts"}}}
	return respond.Invalid(key, msg,
		`Pass `+key+` as a list of {"kind","item"} objects; kind is one of `+strings.Join(prefKinds, ", ")+".", example)
}

// parsePrefs validates preference entries; key names the argument in errors.
// An allergy item written with its note, "peanuts (carries an EpiPen)", is
// split into item and notes so it matches and replaces like one sent with
// notes.
func parsePrefs(list []any, key string) ([]prefChange, *mcp.CallToolResult) {
	if len(list) > maxPrefChanges {
		return nil, prefsInvalid(key, fmt.Sprintf("%s has %d entries; at most %d per call.", key, len(list), maxPrefChanges))
	}
	var out []prefChange
	for i, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, prefsInvalid(key, fmt.Sprintf("%s[%d] is not an object with kind and item.", key, i))
		}
		kind := strings.ToLower(strings.TrimSpace(fmt.Sprint(valueOr(m["kind"], ""))))
		if k, ok := kindAliases[kind]; ok {
			kind = k
		}
		if !slices.Contains(prefKinds, kind) {
			return nil, prefsInvalid(key, fmt.Sprintf("%s[%d].kind %q is not one of %s.", key, i, kind, strings.Join(prefKinds, ", ")))
		}
		item, _ := m["item"].(string)
		if strings.TrimSpace(item) == "" { // "value" is the API's name for it
			item, _ = m["value"].(string)
		}
		item = strings.Join(strings.Fields(item), " ")
		if item == "" {
			return nil, prefsInvalid(key, fmt.Sprintf("%s[%d].item is empty.", key, i))
		}
		if len([]rune(item)) > maxItem {
			return nil, prefsInvalid(key, fmt.Sprintf("%s[%d].item is longer than %d characters.", key, i, maxItem))
		}
		c := prefChange{kind: kind, item: item}
		if kind != "allergy" { // category is ignored for allergies
			if cat, _ := m["category"].(string); strings.TrimSpace(cat) != "" {
				c.category = strings.ToLower(strings.TrimSpace(cat))
				if len([]rune(c.category)) > maxCategory {
					return nil, prefsInvalid(key, fmt.Sprintf("%s[%d].category is longer than %d characters.", key, i, maxCategory))
				}
			}
		}
		if n, _ := m["notes"].(string); strings.TrimSpace(n) != "" {
			n = strings.Join(strings.Fields(n), " ")
			c.notes = &n
		} else if kind == "allergy" {
			if base, rest, ok := strings.Cut(item, " ("); ok && strings.HasSuffix(rest, ")") {
				if n := strings.TrimSpace(strings.TrimSuffix(rest, ")")); n != "" && strings.TrimSpace(base) != "" {
					c.item, c.notes = strings.TrimSpace(base), &n
				}
			}
		}
		if kind == "allergy" && len([]rune(c.value())) > maxItem {
			return nil, prefsInvalid(key, fmt.Sprintf("%s[%d] allergy with its notes is longer than %d characters.", key, i, maxItem))
		}
		out = append(out, c)
	}
	return out, nil
}

// dedupePrefs drops repeated entries, keeping the first; for one allergen
// (bare, ignoring case) the entry that carries notes wins.
func dedupePrefs(in []prefChange) []prefChange {
	var out []prefChange
	seen := map[string]int{} // key -> index in out
	for _, c := range in {
		k := c.kind + "\x00" + strings.ToLower(c.item) + "\x00" + c.category
		if c.kind == "allergy" {
			k = c.kind + "\x00" + strings.ToLower(allergen(c.item))
		}
		if j, ok := seen[k]; !ok {
			seen[k] = len(out)
			out = append(out, c)
		} else if out[j].notes == nil && c.notes != nil {
			out[j] = c // keep the entry that carries detail
		}
	}
	return out
}

// mergeAllergies applies the allergy entries of adds to the stored list
// (#4742 rules): a new allergen is appended; the same allergen with the same
// or no note is unchanged (a bare re-add never drops a stored note); the
// same allergen with a new note replaces the entry in place.
func mergeAllergies(stored []string, adds []prefChange) (out, added, unchanged []string, changed bool) {
	out = slices.Clone(stored)
	for _, a := range adds {
		if a.kind != "allergy" {
			continue
		}
		i := allergyIndex(out, a.item)
		switch {
		case i < 0:
			out = append(out, a.value())
			added = append(added, a.label())
		case strings.EqualFold(strings.TrimSpace(out[i]), a.value()):
			unchanged = append(unchanged, a.label()+" (already listed)")
			continue
		case a.notes == nil:
			unchanged = append(unchanged, "allergy: "+out[i]+" (already listed)")
			continue
		default: // same allergen, new note: replace, don't duplicate
			added = append(added, a.label()+", replacing "+out[i])
			out[i] = a.value()
		}
		changed = true
	}
	return out, added, unchanged, changed
}

// insertPreferences stores the non-allergy entries of adds for person id;
// ones already there (ignoring case) are reported in unchanged.
func insertPreferences(ctx context.Context, q querier, hid uuid.UUID, id string, adds []prefChange) (added, unchanged []string, err error) {
	for _, a := range adds {
		if a.kind == "allergy" {
			continue
		}
		cat := a.category
		if cat == "" {
			cat = "general"
		}
		var prefID string
		err := q.QueryRow(ctx, `
INSERT INTO person_preferences (household_id, person_id, kind, category, value, notes)
VALUES ($1, $2, $3::person_preference_kind, $4, $5, $6)
ON CONFLICT (person_id, kind, lower(category), lower(value)) DO NOTHING
RETURNING id::text`, hid, id, dbKind(a.kind), cat, a.item, a.notes).Scan(&prefID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			unchanged = append(unchanged, a.label()+" (already listed)")
		case err != nil:
			return added, unchanged, err
		default:
			added = append(added, a.label())
		}
	}
	return added, unchanged, nil
}

func valueOr(v, def any) any {
	if v == nil {
		return def
	}
	return v
}

// textOrNil turns a textArg into the value to store: nil clears.
func textOrNil(a textArg) *string {
	if a.clear {
		return nil
	}
	v := a.val
	return &v
}

func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func classR(title string) []mcp.ToolOption {
	return []mcp.ToolOption{mcp.WithTitleAnnotation(title), mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false), mcp.WithIdempotentHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false)}
}

func classC(title string) []mcp.ToolOption {
	return []mcp.ToolOption{mcp.WithTitleAnnotation(title), mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false), mcp.WithIdempotentHintAnnotation(false), mcp.WithOpenWorldHintAnnotation(false)}
}

func classU(title string) []mcp.ToolOption {
	return []mcp.ToolOption{mcp.WithTitleAnnotation(title), mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false), mcp.WithIdempotentHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false)}
}

func classX(title string) []mcp.ToolOption {
	return []mcp.ToolOption{mcp.WithTitleAnnotation(title), mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true), mcp.WithIdempotentHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false)}
}

func personIDParam(desc string) mcp.ToolOption {
	return mcp.WithString("person_id", mcp.Description(desc), mcp.Required())
}

// fieldParams are the optional person fields of create and update.
func fieldParams() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString("birthdate", mcp.Description("YYYY-MM-DD, e.g. 2015-04-02")),
		mcp.WithString("school", mcp.Description("School name, e.g. Maple Elementary")),
		mcp.WithString("grade", mcp.Description("School grade, e.g. 5 or K")),
		mcp.WithString("notes", mcp.Description("Anything else to remember about them")),
	}
}

// ---------------------------------------------------------------------------
// person_get
// ---------------------------------------------------------------------------

// NewPersonGetTool creates person_get (class R): one person in full, with
// allergies, preferences, age and next birthday, and counts of the records
// about them.
func NewPersonGetTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("person_get", append([]mcp.ToolOption{
		mcp.WithDescription("Get one person in full: birthdate, school/grade, preferences and allergies, and how many notes, documents and list items are about them."),
		personIDParam("Person name or id, e.g. Nora"),
		respond.Schema[Person](),
	}, classR("Get person")...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !readsBasic(c.role) {
			return noPeopleAccess(c.role), nil
		}
		ref, _ := req.GetArguments()["person_id"].(string)
		id, bad := resolvePerson(ctx, pool, c.householdID, ref)
		if bad != nil {
			return bad, nil
		}
		row, err := loadPerson(ctx, pool, c.householdID, id, false)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("person", id, "Call person_find to see everyone's person_id."), nil
		}
		if err != nil {
			return respond.Failed("read the person", err), nil
		}
		p, err := present(ctx, pool, c.householdID, row, c.role, c.tz)
		if err != nil {
			return respond.Failed("read the person", err), nil
		}

		summary := describe(p) + "."
		if len(p.Allergies) > 0 {
			summary += " Allergies: " + strings.Join(p.Allergies, ", ") + "."
		} else if !p.Limited {
			summary += " No allergies recorded."
		}
		if p.Limited {
			return respond.OK(summary+" Your role sees only name, relationship and allergies.", p), nil
		}
		if n := len(p.Preferences); n > 0 {
			summary += fmt.Sprintf(" %d %s.", n, plural(n, "preference", "preferences"))
		}
		next := []string{fmt.Sprintf(`Add or remove allergies and likes with person_update person_id=%s add_preferences=[{"kind":"allergy","item":"…"}].`, p.PersonID)}
		if p.Linked != nil && p.Linked.Notes > 0 {
			next = append(next, fmt.Sprintf("Read notes about them with note_find about_type=person about_id=%s.", p.PersonID))
		}
		return respond.OK(summary, p, next...), nil
	}
	return tool.Name, tool, handler
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ---------------------------------------------------------------------------
// person_create
// ---------------------------------------------------------------------------

// NewPersonCreateTool creates person_create (class C): a family member
// without a login, with their allergies and preferences in the same call
// (add_preferences, or the allergies alias). The catalog loop guard returns
// the same person (duplicate:true) when the same name and relationship were
// added in the last 10 minutes; the given allergies and preferences are then
// merged into that person instead of dropped (#4744).
func NewPersonCreateTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("person_create", append(append([]mcp.ToolOption{
		mcp.WithDescription("Add a family member who doesn't log in (a kid, grandparent, nanny), with allergies and likes in the same call. Login members are added by invite in the app."),
		mcp.WithString("display_name", mcp.Description("Their name, e.g. Nora"), mcp.Required()),
		mcp.WithString("relationship", mcp.Description("How they belong to the family"), mcp.Required(), mcp.Enum(relationships...)),
	}, fieldParams()...), append(classC("Add person"),
		mcp.WithArray("add_preferences", mcp.Description(`e.g. [{"kind":"allergy","item":"peanuts"}]`), mcp.Items(prefSchema)),
		mcp.WithArray("allergies", mcp.Description(`Shortcut for allergy entries, e.g. ["peanuts"]`), mcp.WithStringItems()),
		respond.Schema[Person](),
	)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canManage(c.role) {
			return managersOnly(c.role, "person_create", "add people"), nil
		}
		args := req.GetArguments()
		example := map[string]any{"display_name": "Nora", "relationship": "child", "birthdate": "2015-04-02"}
		f, bad := readFields(args, familyToday(time.Now(), c.tz), true, example)
		if bad != nil {
			return bad, nil
		}
		adds, bad := readAddPrefs(args)
		if bad != nil {
			return bad, nil
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("add the person", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		// Loop guard. The advisory lock makes two identical creates in
		// parallel return one person.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('person_create:' || $1::text || ':' || lower($2), 0))`,
			c.householdID, f.name.val); err != nil {
			return respond.Failed("add the person", err), nil
		}
		var id string
		duplicate := false
		err = tx.QueryRow(ctx, `
SELECT id::text FROM people
 WHERE household_id = $1 AND lower(display_name) = lower($2) AND lower(COALESCE(relationship, '')) = $3
   AND created_at > NOW() - interval '10 minutes'
 ORDER BY created_at DESC LIMIT 1`, c.householdID, f.name.val, f.relationship.val).Scan(&id)
		switch {
		case err == nil:
			duplicate = true
		case errors.Is(err, pgx.ErrNoRows):
			var birthdate *string
			if f.birthdate.set && !f.birthdate.clear {
				birthdate = &f.birthdate.val
			}
			opt := func(a textArg) *string {
				if !a.set || a.clear {
					return nil
				}
				return &a.val
			}
			if err := tx.QueryRow(ctx, `
INSERT INTO people (household_id, display_name, relationship, birthdate, school, grade, notes)
VALUES ($1, $2, $3, $4::date, $5, $6, $7) RETURNING id::text`,
				c.householdID, f.name.val, f.relationship.val, birthdate, opt(f.school), opt(f.grade), opt(f.notes)).Scan(&id); err != nil {
				return respond.Failed("add the person", err), nil
			}
		default:
			return respond.Failed("add the person", err), nil
		}
		var sameName []string
		rows, err := tx.Query(ctx, `SELECT id::text FROM people WHERE household_id = $1 AND lower(display_name) = lower($2) AND id <> $3 ORDER BY created_at`,
			c.householdID, f.name.val, id)
		if err == nil {
			sameName, err = pgx.CollectRows(rows, pgx.RowTo[string])
		}
		if err != nil {
			return respond.Failed("add the person", err), nil
		}
		row, err := loadPerson(ctx, tx, c.householdID, id, len(adds) > 0)
		if err != nil {
			return respond.Failed("add the person", err), nil
		}
		// Allergies and preferences, with person_update's rules, for a new
		// person and for the loop guard's existing one alike: on a duplicate
		// they are merged in, and ones already there go to unchanged.
		allergies, added, unchanged, allergiesChanged := mergeAllergies(row.allergies, adds)
		if allergiesChanged {
			if _, err := tx.Exec(ctx, `UPDATE people SET allergies = $3, updated_at = NOW() WHERE household_id = $1 AND id = $2`,
				c.householdID, id, allergies); err != nil {
				return respond.Failed("add the allergies", err), nil
			}
			row.allergies = allergies
		}
		prefAdded, prefUnchanged, err := insertPreferences(ctx, tx, c.householdID, id, adds)
		if err != nil {
			return respond.Failed("add the preference", err), nil
		}
		added, unchanged = append(added, prefAdded...), append(unchanged, prefUnchanged...)
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("add the person", err), nil
		}
		p, err := present(ctx, pool, c.householdID, row, c.role, c.tz)
		if err != nil {
			return respond.Failed("read the new person", err), nil
		}
		p.Duplicate = duplicate
		p.Added, p.Unchanged = added, unchanged
		if duplicate && allergiesChanged {
			p.Changed = []string{"allergies"}
		}
		var extra string
		if len(added) > 0 {
			extra += " Added " + strings.Join(added, ", ")
			if duplicate {
				extra += " to them"
			}
			extra += "."
		}
		if len(unchanged) > 0 {
			extra += " Unchanged: " + strings.Join(unchanged, ", ") + "."
		}

		if duplicate {
			return respond.OK(fmt.Sprintf("%s was already added a few minutes ago; returned that person instead of a copy.%s", describe(p), extra), p,
				fmt.Sprintf("Change details with person_update person_id=%s.", p.PersonID)), nil
		}
		next := []string{fmt.Sprintf(`Put them on events with event_create person_ids=["%s"].`, p.PersonID)}
		if len(adds) == 0 {
			next = append([]string{fmt.Sprintf(`Add allergies or likes with person_update person_id=%s add_preferences=[{"kind":"allergy","item":"…"}].`, p.PersonID)}, next...)
		}
		if len(sameName) > 0 {
			next = append(next, fmt.Sprintf("Another person is also named %s (%s); pass person ids, not the name, from now on.", p.Name, strings.Join(sameName, ", ")))
		}
		return respond.OK(fmt.Sprintf("Added %s.%s", describe(p), extra), p, next...), nil
	}
	return tool.Name, tool, handler
}

// ---------------------------------------------------------------------------
// person_update
// ---------------------------------------------------------------------------

// NewPersonUpdateTool creates person_update (class U): change details and
// add or remove preferences and allergies in one call. Only the given fields
// change; JSON null clears birthdate, school, grade, notes or relationship.
// Adding a preference that is already there, or removing one that is not,
// changes nothing and is reported in unchanged.
func NewPersonUpdateTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("person_update", append(append([]mcp.ToolOption{
		mcp.WithDescription("Change a person's details, or add/remove preferences and allergies in the same call (e.g. add allergy peanuts for Nora)."),
		personIDParam("Person name or id, e.g. Nora"),
		mcp.WithString("display_name", mcp.Description("New name")),
		mcp.WithString("relationship", mcp.Description("How they belong to the family"), mcp.Enum(relationships...)),
	}, fieldParams()...), append(classU("Update person"),
		mcp.WithArray("add_preferences", mcp.Description(`e.g. [{"kind":"allergy","item":"peanuts"}]`), mcp.Items(prefSchema)),
		mcp.WithArray("remove_preferences", mcp.Description(`Same shape, e.g. [{"kind":"like","item":"broccoli"}]`), mcp.Items(prefSchema)),
		respond.Schema[Person](),
	)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canManage(c.role) && c.role != "family_member" {
			if !readsBasic(c.role) {
				return noPeopleAccess(c.role), nil
			}
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("The %s role is read-only: tool person_update can change data.", roleLabel(c.role)),
				Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
			}), nil
		}
		args := req.GetArguments()
		example := map[string]any{"person_id": "Nora", "add_preferences": []any{map[string]any{"kind": "allergy", "item": "peanuts"}}}
		f, bad := readFields(args, familyToday(time.Now(), c.tz), false, example)
		if bad != nil {
			return bad, nil
		}
		adds, bad := readPrefs(args, "add_preferences")
		if bad != nil {
			return bad, nil
		}
		removes, bad := readPrefs(args, "remove_preferences")
		if bad != nil {
			return bad, nil
		}
		if !f.given() && len(adds) == 0 && len(removes) == 0 {
			return respond.Invalid("person_id", "Nothing to change: pass a field or add_preferences/remove_preferences.",
				"Pass at least one of display_name, relationship, birthdate, school, grade, notes, add_preferences, remove_preferences.", example), nil
		}
		ref, _ := args["person_id"].(string)
		id, bad := resolvePerson(ctx, pool, c.householdID, ref)
		if bad != nil {
			return bad, nil
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("update the person", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()
		row, err := loadPerson(ctx, tx, c.householdID, id, true)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("person", id, "Call person_find to see everyone's person_id."), nil
		}
		if err != nil {
			return respond.Failed("update the person", err), nil
		}
		if bad := canEdit(c, row); bad != nil {
			return bad, nil
		}

		var changed, removed []string
		next := row
		setText := func(name string, a textArg, cur **string) {
			if !a.set {
				return
			}
			if v := textOrNil(a); !strPtrEqual(v, *cur) {
				*cur = v
				changed = append(changed, name)
			}
		}
		if f.name.set && f.name.val != row.name {
			next.name = f.name.val
			changed = append(changed, "display_name")
		}
		setText("relationship", f.relationship, &next.relationship)
		setText("school", f.school, &next.school)
		setText("grade", f.grade, &next.grade)
		setText("notes", f.notes, &next.notes)
		if f.birthdate.set {
			var nb *time.Time
			if !f.birthdate.clear {
				d, _ := time.Parse(time.DateOnly, f.birthdate.val) // validated
				nb = &d
			}
			if (nb == nil) != (row.birthdate == nil) || (nb != nil && !nb.Equal(*row.birthdate)) {
				next.birthdate = nb
				changed = append(changed, "birthdate")
			}
		}
		allergies, added, unchanged, allergiesChanged := mergeAllergies(row.allergies, adds)
		for _, r := range removes {
			if r.kind != "allergy" {
				continue
			}
			// Match by the bare allergen, so "peanuts" removes
			// "peanuts (carries an EpiPen)"; any duplicates go too.
			if allergyIndex(allergies, r.item) < 0 {
				unchanged = append(unchanged, r.kind+": "+r.item+" (was not listed)")
				continue
			}
			allergies = slices.DeleteFunc(allergies, func(v string) bool {
				if allergyIndex([]string{v}, r.item) < 0 {
					return false
				}
				removed = append(removed, r.kind+": "+v)
				return true
			})
			allergiesChanged = true
		}
		if allergiesChanged {
			changed = append(changed, "allergies")
		}

		if len(changed) > 0 {
			var birthdate *string
			if next.birthdate != nil {
				b := next.birthdate.Format(time.DateOnly)
				birthdate = &b
			}
			if _, err := tx.Exec(ctx, `
UPDATE people SET display_name = $3, relationship = $4, birthdate = $5::date, school = $6, grade = $7, notes = $8,
       allergies = $9, updated_at = NOW()
 WHERE household_id = $1 AND id = $2`,
				c.householdID, id, next.name, next.relationship, birthdate, next.school, next.grade, next.notes, allergies); err != nil {
				return respond.Failed("update the person", err), nil
			}
		}
		prefAdded, prefUnchanged, err := insertPreferences(ctx, tx, c.householdID, id, adds)
		if err != nil {
			return respond.Failed("add the preference", err), nil
		}
		added, unchanged = append(added, prefAdded...), append(unchanged, prefUnchanged...)
		for _, r := range removes {
			if r.kind == "allergy" {
				continue
			}
			rows, err := tx.Query(ctx, `
DELETE FROM person_preferences
 WHERE household_id = $1 AND person_id = $2 AND kind = $3::person_preference_kind AND lower(value) = lower($4)
   AND ($5 = '' OR lower(category) = $5)
RETURNING value`, c.householdID, id, dbKind(r.kind), r.item, r.category)
			var gone []string
			if err == nil {
				gone, err = pgx.CollectRows(rows, pgx.RowTo[string])
			}
			if err != nil {
				return respond.Failed("remove the preference", err), nil
			}
			if len(gone) == 0 {
				unchanged = append(unchanged, r.label()+" (was not listed)")
				continue
			}
			for _, g := range gone {
				removed = append(removed, r.kind+": "+g)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("update the person", err), nil
		}

		row, err = loadPerson(ctx, pool, c.householdID, id, false)
		if err != nil {
			return respond.Failed("read the updated person", err), nil
		}
		p, err := present(ctx, pool, c.householdID, row, c.role, c.tz)
		if err != nil {
			return respond.Failed("read the updated person", err), nil
		}
		p.Changed, p.Added, p.Removed, p.Unchanged = changed, added, removed, unchanged

		var parts []string
		if fs := slices.DeleteFunc(slices.Clone(changed), func(s string) bool { return s == "allergies" }); len(fs) > 0 {
			parts = append(parts, "changed "+strings.Join(fs, ", "))
		}
		if len(added) > 0 {
			parts = append(parts, "added "+strings.Join(added, ", "))
		}
		if len(removed) > 0 {
			parts = append(parts, "removed "+strings.Join(removed, ", "))
		}
		var summary string
		if len(parts) == 0 {
			summary = fmt.Sprintf("Nothing changed for %s", p.Name)
		} else {
			summary = fmt.Sprintf("Updated %s: %s", p.Name, strings.Join(parts, "; "))
		}
		if len(unchanged) > 0 {
			summary += "; unchanged: " + strings.Join(unchanged, ", ")
		}
		summary += "."
		return respond.OK(summary, p, fmt.Sprintf("Read it back with person_get person_id=%s.", p.PersonID)), nil
	}
	return tool.Name, tool, handler
}

// ---------------------------------------------------------------------------
// person_delete
// ---------------------------------------------------------------------------

// NewPersonDeleteTool creates person_delete (class X): remove a person
// without a login. A person linked to a current member is refused (remove
// the member in the app instead). When notes, documents, list items, meal
// ratings or events point at the person, confirm:true is required.
func NewPersonDeleteTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("person_delete", append([]mcp.ToolOption{
		mcp.WithDescription("Remove a person without a login. Events and lists keep their text. Needs confirm:true if things are assigned to them."),
		personIDParam("Person name or id, e.g. Nora"),
		mcp.WithBoolean("confirm", mcp.Description("true to delete even though records point at them")),
		respond.Schema[Deleted](),
	}, classX("Delete person")...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canManage(c.role) {
			return managersOnly(c.role, "person_delete", "delete people"), nil
		}
		args := req.GetArguments()
		ref, _ := args["person_id"].(string)
		id, bad := resolvePerson(ctx, pool, c.householdID, ref)
		if bad != nil {
			return bad, nil
		}
		confirm := boolArg(args, "confirm")

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("delete the person", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()
		row, err := loadPerson(ctx, tx, c.householdID, id, true)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("person", id, "Call person_find to see who is in the family."), nil
		}
		if err != nil {
			return respond.Failed("delete the person", err), nil
		}
		if row.role != nil {
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("%s logs in to Home OS as a member (%s), so their person record can't be deleted here.", row.name, *row.role),
				Fix:     "Tell the user to remove the member in the app. Their person record is kept and can be deleted afterwards.",
				Field:   "person_id",
			}), nil
		}

		out := Deleted{PersonID: row.id, Name: row.name, Removed: Removed{Allergies: len(row.allergies)}}
		// person_reference_counts (migration 063) is shared with the REST API's
		// DELETE /api/v1/people/{id}, so both ask for the same confirmation.
		if err := tx.QueryRow(ctx, `
SELECT (SELECT count(*) FROM person_preferences WHERE household_id = $1 AND person_id = $2),
       r.notes, r.documents, r.list_items, r.meal_ratings, r.events
  FROM person_reference_counts($1, $2) r`,
			c.householdID, id).Scan(
			&out.Removed.Preferences, &out.Removed.Notes, &out.Unlinked.Documents, &out.Unlinked.ListItems,
			&out.Unlinked.MealRatings, &out.Unlinked.Events); err != nil {
			return respond.Failed("delete the person", err), nil
		}

		var assigned []string
		for _, it := range []struct {
			n         int
			one, many string
		}{
			{out.Removed.Notes, "note about them (deleted with them)", "notes about them (deleted with them)"},
			{out.Unlinked.Documents, "document", "documents"},
			{out.Unlinked.ListItems, "list item", "list items"},
			{out.Unlinked.MealRatings, "meal rating", "meal ratings"},
			{out.Unlinked.Events, "event", "events"},
		} {
			if it.n > 0 {
				assigned = append(assigned, fmt.Sprintf("%d %s", it.n, plural(it.n, it.one, it.many)))
			}
		}
		if len(assigned) > 0 && !confirm {
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("%s has %s; pass confirm:true to delete them.", row.name, strings.Join(assigned, ", ")),
				Fix:     "Ask the user to confirm, then call person_delete again with confirm:true. Documents, list items and events stay, without the person.",
				Field:   "confirm",
				Example: map[string]any{"person_id": row.id, "confirm": true},
			}), nil
		}

		tag, err := tx.Exec(ctx, `
DELETE FROM people p
 WHERE p.household_id = $1 AND p.id = $2
   AND NOT EXISTS (SELECT 1 FROM memberships m WHERE m.household_id = p.household_id AND m.user_id = p.user_id)`,
			c.householdID, id)
		if err != nil {
			return respond.Failed("delete the person", err), nil
		}
		if tag.RowsAffected() == 0 {
			return respond.NotFound("person", id, "Call person_find to see who is in the family."), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("delete the person", err), nil
		}
		out.Deleted = true

		summary := fmt.Sprintf("Deleted %s.", row.name)
		if len(assigned) > 0 {
			summary = fmt.Sprintf("Deleted %s (had %s).", row.name, strings.Join(assigned, ", "))
		}
		return respond.OK(summary, out, "Call person_find to see who is left."), nil
	}
	return tool.Name, tool, handler
}
