package pet

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// duplicateWindow is the pet_create loop guard (catalog §1 "Loop guard").
const duplicateWindow = "10 minutes"

// Column sizes (006_pets, 004 vendors).
const (
	maxNameLen    = 255
	maxSpeciesLen = 100
	maxBreedLen   = 100
	maxNotesLen   = 10000
	maxVetNameLen = 255
	maxPhoneLen   = 50
)

// writeAliases are other names small models use for the write params. A
// catalog name that is present wins.
var writeAliases = map[string]string{
	"id": "pet_id", "pet": "pet_id",
	"pet_name": "name",
	"type":     "species", "animal": "species", "kind": "species",
	"dob": "date_of_birth", "birthdate": "date_of_birth", "birthday": "date_of_birth", "born": "date_of_birth",
	"vet_vendor_id": "vet_contact_id", "vet_id": "vet_contact_id", "vet_contact": "vet_contact_id",
	"vet": "vet_name", "vet_phone_number": "vet_phone",
	"note": "notes",
}

// Examples for error results.
var (
	createExample = map[string]any{"name": "Biscuit", "species": "dog", "breed": "Beagle",
		"date_of_birth": "2021-06-01", "vet_name": "Dr. Paws Clinic", "vet_phone": "555-0100"}
	updateExample = map[string]any{"pet_id": "Biscuit", "notes": "Takes thyroid pills with breakfast"}
	deleteExample = map[string]any{"pet_id": "Biscuit"}
)

// ---------------------------------------------------------------------------
// Access
// ---------------------------------------------------------------------------

// caller is who is calling, with the role re-read from memberships.
type caller struct {
	householdID uuid.UUID
	role        string
	tz          string
}

// callerOf resolves the caller's current role the way the person tools do:
// re-read from memberships for the claims' user and household, so a demoted
// member loses write access at once and a removed one gets nothing. It also
// reads the family timezone. A non-nil result is the isError to return.
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
		err = pool.QueryRow(ctx, `SELECT timezone FROM households WHERE id = $1`, hid).Scan(&out.tz)
	} else {
		err = pool.QueryRow(ctx,
			`SELECT m.role::text, h.timezone FROM memberships m JOIN households h ON h.id = m.household_id
			  WHERE m.household_id = $1 AND m.user_id = $2`, hid, uid).Scan(&out.role, &out.tz)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return caller{}, notSignedIn()
	}
	if err != nil {
		return caller{}, respond.Failed("check your access", err)
	}
	return out, nil
}

// canWrite reports whether role may create, change and delete pets (API
// ResPets write: the family roles). Unknown roles may not, so a new role
// fails closed.
func canWrite(role string) bool {
	return role == "owner" || role == "family_manager" || role == "family_member"
}

// cannotWrite is the refusal for a role that may not change pets. The
// server middleware refuses these roles first; this is the second check.
func cannotWrite(role, tool string) *mcp.CallToolResult {
	label := role
	if label == "" {
		label = "current"
	}
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't change pets (tool %s): only an owner, family manager or family member can.", label, tool),
		Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
	})
}

// familyToday is today's date in the family timezone (UTC midnight).
func familyToday(tz string) time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	y, m, d := time.Now().In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ---------------------------------------------------------------------------
// Arguments
// ---------------------------------------------------------------------------

// textField is one optional text param. An absent param or "" is not given
// (small models send "" for unused params); JSON null asks to clear it.
type textField struct {
	set   bool // given with a value
	clear bool // JSON null
	val   string
}

func (f textField) given() bool { return f.set || f.clear }

// ptr is the value to store: nil when not set.
func (f textField) ptr() *string {
	if !f.set {
		return nil
	}
	v := f.val
	return &v
}

func readText(args map[string]any, key string, maxLen int) (textField, error) {
	v, present := args[key]
	if !present {
		return textField{}, nil
	}
	if v == nil {
		return textField{clear: true}, nil
	}
	s, ok := v.(string)
	if !ok {
		return textField{}, badArg(key, key+" must be text.", fmt.Sprintf("Pass %s as text in quotes.", key))
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return textField{}, nil
	}
	if utf8.RuneCountInString(s) > maxLen {
		return textField{}, badArg(key, fmt.Sprintf("%s must be at most %d characters.", key, maxLen),
			fmt.Sprintf("Pass a shorter %s.", key))
	}
	return textField{set: true, val: s}, nil
}

// petFields is the validated create/update fields.
type petFields struct {
	name, species, breed, notes textField
	dob                         textField  // val is YYYY-MM-DD
	vetRef                      textField  // vet_contact_id as given
	vetID                       *uuid.UUID // vet_contact_id when it is a UUID
	vetName, vetPhone           textField
}

// vetGiven reports whether the call names or clears a vet.
func (f petFields) vetGiven() bool {
	return f.vetRef.given() || f.vetName.set || f.vetPhone.set
}

func (f petFields) given() bool {
	return f.name.given() || f.species.given() || f.breed.given() || f.notes.given() || f.dob.given() || f.vetGiven()
}

// parseFields validates every create/update field present in args. today is
// the family's date, for the birthday check.
func parseFields(args map[string]any, today time.Time) (petFields, error) {
	var f petFields
	var err error
	for _, t := range []struct {
		key string
		max int
		dst *textField
	}{
		{"name", maxNameLen, &f.name}, {"species", maxSpeciesLen, &f.species}, {"breed", maxBreedLen, &f.breed},
		{"notes", maxNotesLen, &f.notes}, {"date_of_birth", 10, &f.dob}, {"vet_contact_id", 255, &f.vetRef},
		{"vet_name", maxVetNameLen, &f.vetName}, {"vet_phone", maxPhoneLen, &f.vetPhone},
	} {
		if *t.dst, err = readText(args, t.key, t.max); err != nil {
			if t.key == "date_of_birth" {
				return f, badDate(fmt.Sprint(args[t.key]))
			}
			return f, err
		}
	}
	if f.dob.set {
		d, perr := time.Parse(time.DateOnly, f.dob.val)
		switch {
		case perr != nil:
			return f, badDate(f.dob.val)
		case d.After(today):
			return f, badArg("date_of_birth", fmt.Sprintf("date_of_birth %s is in the future (today is %s).", f.dob.val, today.Format(time.DateOnly)),
				"Pass the pet's birthday as YYYY-MM-DD, today or earlier; leave it out if not known.")
		case d.Year() < 1900:
			return f, badArg("date_of_birth", fmt.Sprintf("date_of_birth %s is before 1900.", f.dob.val),
				"Pass the pet's birthday as YYYY-MM-DD; leave it out if not known.")
		}
	}
	if f.vetRef.set {
		if id, perr := uuid.Parse(f.vetRef.val); perr == nil {
			f.vetID = &id
		} else if looksLikeID(f.vetRef.val) {
			return f, badArg("vet_contact_id", fmt.Sprintf("vet_contact_id %q is not a valid id.", f.vetRef.val),
				"Pass the vet's contact_id (a UUID) from contact_find trade=vet, or the vet's name as vet_name.")
		}
	}
	return f, nil
}

func badDate(v string) error {
	return badArg("date_of_birth", fmt.Sprintf("date_of_birth %q is not a date.", v),
		"Pass date_of_birth as YYYY-MM-DD, e.g. 2021-06-01; leave it out if not known.")
}

// idLike matches text made only of hex digits and dashes: a mistyped id,
// never a vet's name.
var idLike = regexp.MustCompile(`^[0-9a-fA-F-]{8,}$`)

func looksLikeID(s string) bool { return idLike.MatchString(s) && strings.Contains(s, "-") }

// ---------------------------------------------------------------------------
// Lookups
// ---------------------------------------------------------------------------

// resolvePet matches a pet_id (a UUID, or the pet's exact name, any case) in
// the caller's family. A UUID is returned as is; the caller's locked read
// reports a missing pet. Several pets with the name, or none, is an isError
// listing the candidates or the family's pets.
func resolvePet(ctx context.Context, q querier, hid uuid.UUID, args map[string]any, example map[string]any) (uuid.UUID, *mcp.CallToolResult, error) {
	ref, err := optionalString(args, "pet_id")
	if err != nil {
		return uuid.Nil, invalid(err, example), nil
	}
	if ref == nil {
		return uuid.Nil, respond.Invalid("pet_id", "pet_id is required.",
			"Pass the pet's id from pet_find, or its name, e.g. Biscuit.", example), nil
	}
	if id, perr := uuid.Parse(*ref); perr == nil {
		return id, nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id, name FROM pets WHERE household_id = $1 ORDER BY lower(name), created_at, id`, hid)
	if err != nil {
		return uuid.Nil, nil, err
	}
	type cand struct {
		id   uuid.UUID
		name string
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cand, error) {
		var c cand
		return c, r.Scan(&c.id, &c.name)
	})
	if err != nil {
		return uuid.Nil, nil, err
	}
	var hits []cand
	for _, c := range all {
		if strings.EqualFold(strings.TrimSpace(c.name), *ref) {
			hits = append(hits, c)
		}
	}
	switch {
	case len(hits) == 1:
		return hits[0].id, nil, nil
	case len(hits) > 1:
		labels := make([]string, len(hits))
		for i, c := range hits {
			labels[i] = fmt.Sprintf("%s (%s)", c.name, c.id)
		}
		return uuid.Nil, respond.Fail(respond.Error{
			Message: fmt.Sprintf("%d pets are named %s: %s.", len(hits), *ref, strings.Join(labels, ", ")),
			Fix:     "Pass the pet_id of the one you mean.",
			Field:   "pet_id",
		}), nil
	}
	msg := fmt.Sprintf("No pet named %q in this family.", *ref)
	if len(all) == 0 {
		msg += " The family has no pets yet."
	} else {
		names := make([]string, 0, min(len(all), 30))
		for _, c := range all[:min(len(all), 30)] {
			names = append(names, c.name)
		}
		msg += " The family's pets: " + strings.Join(names, ", ") + "."
	}
	return uuid.Nil, respond.Fail(respond.Error{
		Message: msg,
		Fix:     "Pass one of the family's pet names, or the pet_id from pet_find.",
		Field:   "pet_id",
	}), nil
}

// petRow is a pet's stored fields, for update and delete.
type petRow struct {
	name                  string
	species, breed, notes *string
	dob                   *time.Time
	vetID                 *uuid.UUID
}

// lockPet reads and row-locks one pet of the family.
func lockPet(ctx context.Context, tx pgx.Tx, hid, id uuid.UUID) (petRow, error) {
	var r petRow
	err := tx.QueryRow(ctx,
		`SELECT name, species, breed, notes, date_of_birth, vet_vendor_id FROM pets
		  WHERE id = $1 AND household_id = $2 FOR UPDATE`, id, hid).
		Scan(&r.name, &r.species, &r.breed, &r.notes, &r.dob, &r.vetID)
	return r, err
}

// loadPet reads one pet as pet_find returns it.
func loadPet(ctx context.Context, q querier, hid, id uuid.UUID) (petData, error) {
	page, _, err := findPets(ctx, q, hid.String(), findInput{id: &id, limit: 1})
	if err != nil {
		return petData{}, err
	}
	if len(page) == 0 {
		return petData{}, pgx.ErrNoRows
	}
	return page[0], nil
}

// ---------------------------------------------------------------------------
// Vet
// ---------------------------------------------------------------------------

// vetChoice is the vet a write call asked for.
type vetChoice struct {
	given   bool       // the call names or clears a vet
	id      *uuid.UUID // nil: no vet
	created bool       // the vet was saved as a new contact
}

// chooseVet turns vet_contact_id / vet_name / vet_phone into a family
// contact. vet_contact_id wins: a UUID must be a contact of this family; a
// name in it is read as vet_name. vet_name / vet_phone follow the API's
// rules (#4700, migration 043): with a name, the family contact with the
// same name whose phone does not contradict; with only a phone, the contact
// with the same digits; ties go to contacts with trade vet, then the
// oldest. No match saves a new contact {name or "Vet", specialty
// Veterinarian, phone, trades [vet]}. JSON null vet_contact_id clears the
// vet. A non-nil result is the isError to return.
func chooseVet(ctx context.Context, tx pgx.Tx, hid uuid.UUID, f petFields, example map[string]any) (vetChoice, *mcp.CallToolResult, error) {
	if f.vetID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vendors WHERE id = $1 AND household_id = $2)`,
			*f.vetID, hid).Scan(&ok); err != nil {
			return vetChoice{}, nil, err
		}
		if !ok {
			return vetChoice{}, respond.Invalid("vet_contact_id", fmt.Sprintf("No contact with id %q in this family.", f.vetID.String()),
				"Call contact_find trade=vet for the vet's contact_id, or pass vet_name to save a new vet.", example), nil
		}
		return vetChoice{given: true, id: f.vetID}, nil, nil
	}
	name, phone := f.vetName, f.vetPhone
	if f.vetRef.set { // a name passed as vet_contact_id
		name = f.vetRef
	}
	if !name.set && !phone.set {
		if f.vetRef.clear {
			return vetChoice{given: true}, nil, nil
		}
		return vetChoice{}, nil, nil
	}

	// One vet lookup-or-create at a time per family, so two parallel calls
	// naming the same new vet save one contact.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('pet_vet:' || $1::text, 0))`, hid); err != nil {
		return vetChoice{}, nil, err
	}
	digits := nonDigits.ReplaceAllString(phone.val, "")
	var id uuid.UUID
	var err error
	if name.set {
		err = tx.QueryRow(ctx, `
SELECT id FROM vendors
 WHERE household_id = $1
   AND lower(btrim(name)) = lower($2)
   AND ($3 = '' OR phone IS NULL OR btrim(phone) = '' OR regexp_replace(phone, '\D', '', 'g') = $3)
 ORDER BY ('vet' = ANY (trades)) DESC, created_at, id
 LIMIT 1`, hid, name.val, digits).Scan(&id)
	} else {
		err = tx.QueryRow(ctx, `
SELECT id FROM vendors
 WHERE household_id = $1
   AND $2 <> ''
   AND regexp_replace(COALESCE(phone, ''), '\D', '', 'g') = $2
 ORDER BY ('vet' = ANY (trades)) DESC, created_at, id
 LIMIT 1`, hid, digits).Scan(&id)
	}
	if err == nil {
		return vetChoice{given: true, id: &id}, nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return vetChoice{}, nil, err
	}
	vetName := "Vet"
	if name.set {
		vetName = name.val
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO vendors (household_id, name, specialty, phone, trades)
		 VALUES ($1, $2, 'Veterinarian', $3, ARRAY['vet']) RETURNING id`,
		hid, vetName, phone.ptr()).Scan(&id); err != nil {
		return vetChoice{}, nil, err
	}
	return vetChoice{given: true, id: &id, created: true}, nil, nil
}

var nonDigits = regexp.MustCompile(`\D`)

// vetFKError maps the composite vet FK (a contact deleted meanwhile) to an
// isError result; nil for any other error.
func vetFKError(err error, example map[string]any) *mcp.CallToolResult {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return respond.Invalid("vet_contact_id", "That vet contact does not exist in this family.",
			"Call contact_find trade=vet for the vet's contact_id, or pass vet_name to save a new vet.", example)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Shared tool params
// ---------------------------------------------------------------------------

// fieldParams are the optional params of pet_create (and pet_update).
func fieldParams() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString("breed", mcp.Description("Breed, e.g. Beagle")),
		mcp.WithString("date_of_birth", mcp.Description("Birthday, YYYY-MM-DD, e.g. 2021-06-01")),
		mcp.WithString("vet_contact_id", mcp.Description("The vet's contact_id from contact_find trade=vet")),
		mcp.WithString("vet_name", mcp.Description("Or the vet's name, e.g. Dr. Paws Clinic; saved as a contact if new")),
		mcp.WithString("vet_phone", mcp.Description("The vet's phone, e.g. 555-0100; with vet_name, or alone")),
		mcp.WithString("notes", mcp.Description("Medical notes, food, meds, e.g. thyroid pill with breakfast")),
	}
}

// vetSentence is the summary addition for a vet saved by this call.
func vetSentence(d petData) string {
	if d.VetCreated && d.Vet != nil {
		return fmt.Sprintf(" Saved %s as a new vet contact.", d.Vet.Name)
	}
	return ""
}

// ---------------------------------------------------------------------------
// pet_create
// ---------------------------------------------------------------------------

// NewPetCreateTool creates the pet_create MCP tool (catalog v2, class C).
// "Add our dog Biscuit" is one call: name and species are required, and the
// vet can be linked in the same call by vet_contact_id or by vet_name /
// vet_phone. The loop guard returns a pet with the same name and species
// added to the family in the last 10 minutes (duplicate:true) instead of a
// second row, under an advisory lock so parallel calls make one row.
func NewPetCreateTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := append([]mcp.ToolOption{
		mcp.WithDescription("Add a pet. Link the vet with vet_contact_id (a contact with trade vet)."),
		mcp.WithString("name", mcp.Description("The pet's name, e.g. Biscuit"), mcp.Required()),
		mcp.WithString("species", mcp.Description("Kind of animal, e.g. dog, cat, fish"), mcp.Required()),
	}, fieldParams()...)
	opts = append(opts, respond.Schema[petData]())
	tool := mcp.NewTool("pet_create", append(opts, annotations("Add pet", false, false, false)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "pet_create"), nil
		}
		f, err := parseFields(withAliases(req.GetArguments(), writeAliases), familyToday(c.tz))
		if err != nil {
			return invalid(err, createExample), nil
		}
		switch {
		case !f.name.set:
			return respond.Invalid("name", "name is required.", "Pass the pet's name, e.g. Biscuit.", createExample), nil
		case !f.species.set:
			return respond.Invalid("species", "species is required.", "Pass the kind of animal, e.g. dog or cat.", createExample), nil
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("add the pet", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		// Loop guard.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('pet_create:' || $1::text || ':' || lower($2), 0))`,
			c.householdID, f.name.val); err != nil {
			return respond.Failed("add the pet", err), nil
		}
		var dupID uuid.UUID
		err = tx.QueryRow(ctx, `
SELECT id FROM pets
 WHERE household_id = $1 AND lower(name) = lower($2) AND lower(COALESCE(species, '')) = lower($3)
   AND created_at > NOW() - $4::interval
 ORDER BY created_at DESC LIMIT 1`, c.householdID, f.name.val, f.species.val, duplicateWindow).Scan(&dupID)
		switch {
		case err == nil:
			d, err := loadPet(ctx, tx, c.householdID, dupID)
			if err != nil {
				return respond.Failed("add the pet", err), nil
			}
			d.Duplicate = true
			return respond.OK(fmt.Sprintf("%s was already added a few minutes ago; returned that pet instead of a copy.", d.Name), d,
				fmt.Sprintf("Change details with pet_update pet_id=%s instead of adding it again.", d.ID)), nil
		case !errors.Is(err, pgx.ErrNoRows):
			return respond.Failed("add the pet", err), nil
		}

		vet, bad, err := chooseVet(ctx, tx, c.householdID, f, createExample)
		if err != nil {
			return respond.Failed("add the pet", err), nil
		} else if bad != nil {
			return bad, nil
		}

		var id uuid.UUID
		if err := tx.QueryRow(ctx, `
INSERT INTO pets (household_id, name, species, breed, date_of_birth, vet_vendor_id, notes)
VALUES ($1, $2, $3, $4, $5::date, $6, $7) RETURNING id`,
			c.householdID, f.name.val, f.species.val, f.breed.ptr(), f.dob.ptr(), vet.id, f.notes.ptr()).Scan(&id); err != nil {
			if res := vetFKError(err, createExample); res != nil {
				return res, nil
			}
			return respond.Failed("add the pet", err), nil
		}
		var sameName int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pets WHERE household_id = $1 AND lower(name) = lower($2) AND id <> $3`,
			c.householdID, f.name.val, id).Scan(&sameName); err != nil {
			return respond.Failed("add the pet", err), nil
		}
		d, err := loadPet(ctx, tx, c.householdID, id)
		if err != nil {
			return respond.Failed("add the pet", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("add the pet", err), nil
		}
		d.VetCreated = vet.created

		next := []string{
			`Book a vet visit with event_create title=… start=… calendar_id="Pets" (if the family has that calendar).`,
			fmt.Sprintf("Save a vaccine record with document_upload about_type=pet about_id=%s.", d.ID),
		}
		switch {
		case sameName > 0:
			next = append(next, fmt.Sprintf("Another pet is also named %s; pass pet ids, not the name, from now on.", d.Name))
		case d.Vet == nil:
			next = append(next, fmt.Sprintf("Link a vet with pet_update pet_id=%s vet_contact_id=… (contact_find trade=vet) or vet_name=….", d.ID))
		}
		return respond.OK(fmt.Sprintf("Added %s: %s.%s", d.Name, d.describe(), vetSentence(d)), d, next...), nil
	}
	return "pet_create", tool, handler
}

// ---------------------------------------------------------------------------
// pet_update
// ---------------------------------------------------------------------------

// NewPetUpdateTool creates the pet_update MCP tool (catalog v2, class U).
// Only the given fields change: an absent field or "" is left alone, and
// JSON null clears breed, date_of_birth, notes or the vet (vet_contact_id
// null). name and species cannot be cleared. A call that changes nothing
// writes nothing, so a repeat is safe. pet_id is the pet's id or its exact
// name.
func NewPetUpdateTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := append([]mcp.ToolOption{
		mcp.WithDescription("Change a pet; only the given fields change."),
		mcp.WithString("pet_id", mcp.Description("The pet's id from pet_find, or its name, e.g. Biscuit"), mcp.Required()),
		mcp.WithString("name", mcp.Description("New name")),
		mcp.WithString("species", mcp.Description("Kind of animal, e.g. dog")),
	}, fieldParams()...)
	opts = append(opts, respond.Schema[petData]())
	tool := mcp.NewTool("pet_update", append(opts, annotations("Update pet", false, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "pet_update"), nil
		}
		args := withAliases(req.GetArguments(), writeAliases)
		f, err := parseFields(args, familyToday(c.tz))
		if err != nil {
			return invalid(err, updateExample), nil
		}
		for _, p := range []struct {
			key string
			f   textField
		}{{"name", f.name}, {"species", f.species}} {
			if p.f.clear {
				return respond.Invalid(p.key, p.key+" cannot be cleared.", "Pass a new value, or leave "+p.key+" out to keep it.", updateExample), nil
			}
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("update the pet", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		id, bad, err := resolvePet(ctx, tx, c.householdID, args, updateExample)
		if err != nil {
			return respond.Failed("update the pet", err), nil
		} else if bad != nil {
			return bad, nil
		}
		if !f.given() {
			return respond.Invalid("", "No fields to update.",
				"Pass pet_id plus at least one field, e.g. notes, date_of_birth or vet_name.", updateExample), nil
		}
		// The pet is checked before the vet, so a missing pet never saves a
		// new vet contact.
		cur, err := lockPet(ctx, tx, c.householdID, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("pet", id.String(), "Call pet_find to list the family's pets and their ids."), nil
		}
		if err != nil {
			return respond.Failed("update the pet", err), nil
		}
		vet, bad, err := chooseVet(ctx, tx, c.householdID, f, updateExample)
		if err != nil {
			return respond.Failed("update the pet", err), nil
		} else if bad != nil {
			return bad, nil
		}

		// Columns come only from this list, never from the caller.
		vals := []any{id, c.householdID}
		var sets, changed []string
		set := func(param, col, cast string, v any) {
			vals = append(vals, v)
			sets = append(sets, fmt.Sprintf("%s = $%d%s", col, len(vals), cast))
			changed = append(changed, param)
		}
		text := func(param, col string, tf textField, now *string) {
			if !tf.given() {
				return
			}
			if want := tf.ptr(); !samePtr(want, now) {
				set(param, col, "", want)
			}
		}
		curName := cur.name
		text("name", "name", f.name, &curName)
		text("species", "species", f.species, cur.species)
		text("breed", "breed", f.breed, cur.breed)
		var curDOB *string
		if cur.dob != nil {
			s := cur.dob.Format(time.DateOnly)
			curDOB = &s
		}
		if f.dob.given() && !samePtr(f.dob.ptr(), curDOB) {
			set("date_of_birth", "date_of_birth", "::date", f.dob.ptr())
		}
		if vet.given && !sameID(vet.id, cur.vetID) {
			set("vet", "vet_vendor_id", "", vet.id)
		}
		text("notes", "notes", f.notes, cur.notes)

		if len(sets) > 0 {
			sets = append(sets, "updated_at = NOW()")
			if _, err := tx.Exec(ctx, `UPDATE pets SET `+strings.Join(sets, ", ")+` WHERE id = $1 AND household_id = $2`, vals...); err != nil {
				if res := vetFKError(err, updateExample); res != nil {
					return res, nil
				}
				return respond.Failed("update the pet", err), nil
			}
		}
		d, err := loadPet(ctx, tx, c.householdID, id)
		if err != nil {
			return respond.Failed("update the pet", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("update the pet", err), nil
		}
		d.VetCreated = vet.created
		d.Changed = changed

		next := []string{fmt.Sprintf("See %s with pet_find id=%s.", d.Name, d.ID)}
		if d.Vet != nil && d.Vet.ContactID != nil {
			next = append(next, fmt.Sprintf("See the vet's details with contact_find id=%s.", *d.Vet.ContactID))
		}
		if len(changed) == 0 {
			return respond.OK(fmt.Sprintf("Nothing changed: %s already has those details (%s).", d.Name, d.describe()), d, next...), nil
		}
		return respond.OK(fmt.Sprintf("Updated %s (%s): %s.%s", d.Name, strings.Join(changed, ", "), d.describe(), vetSentence(d)), d, next...), nil
	}
	return "pet_update", tool, handler
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ---------------------------------------------------------------------------
// pet_delete
// ---------------------------------------------------------------------------

// NewPetDeleteTool creates the pet_delete MCP tool (catalog v2, class X). It
// deletes one pet of the family and returns it as it was, with what went
// with it: documents about it move to the family and notes about it are
// deleted (rehome_entity_refs, 038). Its vet contact stays. A missing or
// already deleted pet is reported as not found, so a repeat call is safe.
func NewPetDeleteTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Delete a pet. Its documents move to the family."),
		mcp.WithString("pet_id", mcp.Description("The pet's id from pet_find, or its name, e.g. Biscuit"), mcp.Required()),
		respond.Schema[petData](),
	}
	tool := mcp.NewTool("pet_delete", append(opts, annotations("Delete pet", false, true, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "pet_delete"), nil
		}
		args := withAliases(req.GetArguments(), writeAliases)

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("delete the pet", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		id, bad, err := resolvePet(ctx, tx, c.householdID, args, deleteExample)
		if err != nil {
			return respond.Failed("delete the pet", err), nil
		} else if bad != nil {
			return bad, nil
		}
		if _, err := lockPet(ctx, tx, c.householdID, id); errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("pet", id.String(), "Call pet_find to list the family's pets and their ids."), nil
		} else if err != nil {
			return respond.Failed("delete the pet", err), nil
		}
		d, err := loadPet(ctx, tx, c.householdID, id)
		if err != nil {
			return respond.Failed("delete the pet", err), nil
		}
		var docs, notes int
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM files WHERE household_id = $2 AND entity_type = 'pet' AND entity_id = $1),
			(SELECT count(*) FROM notes WHERE household_id = $2 AND entity_type = 'pet' AND entity_id = $1)`,
			id, c.householdID).Scan(&docs, &notes); err != nil {
			return respond.Failed("delete the pet", err), nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM pets WHERE id = $1 AND household_id = $2`, id, c.householdID); err != nil {
			return respond.Failed("delete the pet", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("delete the pet", err), nil
		}

		d.DocumentsMoved, d.NotesDeleted = &docs, &notes
		summary := fmt.Sprintf("Deleted %s.", d.Name)
		var extra []string
		if docs > 0 {
			extra = append(extra, plural(docs, "document")+" moved to the family")
		}
		if notes > 0 {
			extra = append(extra, plural(notes, "note")+" deleted")
		}
		if len(extra) > 0 {
			summary += " " + strings.Join(extra, "; ") + "."
		}
		return respond.OK(summary, d, "List the pets that are left with pet_find."), nil
	}
	return "pet_delete", tool, handler
}
