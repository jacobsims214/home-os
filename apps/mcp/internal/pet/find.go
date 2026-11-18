package pet

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// findAliases are old list_pets/get_pet parameter names, still accepted but
// not advertised.
var findAliases = map[string]string{"pet_id": "id", "name": "query", "search": "query"}

// findExample is a valid pet_find call for error examples.
var findExample = map[string]any{"query": "dog"}

// findInput is the validated pet_find arguments.
type findInput struct {
	id     *uuid.UUID
	query  string
	limit  int
	offset int
}

func (in findInput) filtered() bool { return in.id != nil || in.query != "" }

// petSelect loads one page of the household's pets with their vet, and the
// total before paging. The vet is the linked vendor (043 vet_vendor_id; the
// composite FK keeps it in the pet's family, and the join repeats the
// household check anyway). The old pets.vet_name / vet_phone columns are
// never read (#4698): a pet with no linked vendor has no vet, so clearing
// the vet never brings back a stale typed name. query matches the name,
// species, breed, notes, microchip id, or the vet's name. Age is whole years in the
// family's timezone.
const petSelect = `
	SELECT p.id, p.name, NULLIF(btrim(p.species), ''), NULLIF(btrim(p.breed), ''), p.date_of_birth,
	       CASE WHEN p.date_of_birth IS NULL THEN NULL
	            ELSE date_part('year', age((NOW() AT TIME ZONE h.timezone)::date, p.date_of_birth))::int END,
	       v.id, v.name, NULLIF(btrim(v.phone), ''), NULLIF(btrim(v.email), ''),
	       p.microchip_id, p.registration_id, p.insurance_provider, p.insurance_policy, p.notes,
	       p.created_at, p.updated_at,
	       count(*) OVER ()
	FROM pets p
	JOIN households h ON h.id = p.household_id
	LEFT JOIN vendors v ON v.id = p.vet_vendor_id AND v.household_id = p.household_id
	WHERE p.household_id = $1
	  AND ($2::uuid IS NULL OR p.id = $2)
	  AND ($3 = '' OR p.name ILIKE '%' || $3 || '%' OR p.species ILIKE '%' || $3 || '%'
	       OR p.breed ILIKE '%' || $3 || '%' OR p.notes ILIKE '%' || $3 || '%'
	       OR p.microchip_id ILIKE '%' || $3 || '%'
	       OR v.name ILIKE '%' || $3 || '%')
	ORDER BY lower(p.name), p.created_at, p.id
	LIMIT $4 OFFSET $5`

// NewPetFindTool creates the pet_find MCP tool (catalog v2, class R),
// replacing list_pets and get_pet. Every pet comes back in full (pets have
// few fields), with its vet. Pagination is limit + the opaque next_cursor
// (respond.EncodeCursor), passed back as cursor.
func NewPetFindTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("List or search pets with species, breed, birthday and vet. Pass id for full details."),
		mcp.WithString("id", mcp.Description("A pet_id (UUID) to get exactly that pet")),
		mcp.WithString("query", mcp.Description("Text in the name, species, breed, notes or vet name, e.g. beagle")),
		mcp.WithNumber("limit", mcp.Description("Max pets to return, 1-100 (default 25)"), mcp.Min(1), mcp.Max(maxLimit)),
		mcp.WithString("cursor", mcp.Description("next_cursor from the previous result, for the next page")),
		respond.ListSchema[petData](),
	}
	tool := mcp.NewTool("pet_find", append(opts, annotations("Find pets", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}
		in, err := parseFindInput(req.GetArguments())
		if err != nil {
			return invalid(err, findExample), nil
		}
		page, total, err := findPets(ctx, pool, claims.HouseholdID, in)
		if err != nil {
			return respond.Failed("find pets", err), nil
		}
		if in.id != nil && len(page) == 0 {
			return respond.NotFound("pet", in.id.String(),
				"Call pet_find without id to list the family's pets and their ids."), nil
		}
		if total == 0 && in.offset > 0 {
			// Past the end: count what there is so the summary is honest.
			if _, total, err = findPets(ctx, pool, claims.HouseholdID, findInput{id: in.id, query: in.query, limit: 1}); err != nil {
				return respond.Failed("find pets", err), nil
			}
			in.offset = total
		}
		end := in.offset + len(page)
		return respond.List(findSummary(in, page, total), page, respond.Page{
			Total:      max(total-in.offset, len(page)),
			NextCursor: respond.CursorAfter(end, total),
			Narrow:     "Pass next_cursor as cursor for the next page, or add query.",
		}, findNext(in, page)...), nil
	}
	return "pet_find", tool, handler
}

// findPets loads one page of pets and the total number of matches. q is the
// pool, or the write tools' transaction.
func findPets(ctx context.Context, q querier, householdID string, in findInput) ([]petData, int, error) {
	rows, err := q.Query(ctx, petSelect, householdID, in.id, escapeLike(in.query), in.limit, in.offset)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	page, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (petData, error) {
		var (
			d                    petData
			id                   uuid.UUID
			born                 *time.Time
			vendorID             *uuid.UUID
			vendorName           *string
			vendorPhone, vendorE *string
		)
		err := row.Scan(&id, &d.Name, &d.Species, &d.Breed, &born, &d.Age,
			&vendorID, &vendorName, &vendorPhone, &vendorE,
			&d.MicrochipID, &d.RegistrationID, &d.InsuranceProvider, &d.InsurancePolicy, &d.Notes,
			&d.CreatedAt, &d.UpdatedAt, &total)
		d.ID = id.String()
		if born != nil {
			s := born.Format(time.DateOnly)
			d.DateOfBirth = &s
		}
		d.Vet = vet(vendorID, vendorName, vendorPhone, vendorE)
		return d, err
	})
	return page, total, err
}

// vet is the pet's linked vet contact, or nil when vet_vendor_id is unset
// (or points outside the family, which the join already filters out).
func vet(vendorID *uuid.UUID, name, phone, email *string) *vetData {
	if vendorID == nil {
		return nil
	}
	id := vendorID.String()
	v := &vetData{ContactID: &id, Phone: phone, Email: email, Source: vetContact}
	if name != nil {
		v.Name = *name
	}
	return v
}

// findSummary is pet_find's one-line summary.
func findSummary(in findInput, page []petData, total int) string {
	switch {
	case total == 0 && !in.filtered():
		return "No pets yet."
	case total == 0:
		return "No pets match."
	case len(page) == 1 && total == 1:
		return fmt.Sprintf("Found %s: %s.", page[0].Name, page[0].describe())
	case len(page) == 0:
		return fmt.Sprintf("Found %s; this page is past the end.", plural(total, "pet"))
	case len(page) < total-in.offset || in.offset > 0:
		return fmt.Sprintf("Found %d pets; showing %d-%d.", total, in.offset+1, in.offset+len(page))
	default:
		names := make([]string, 0, len(page))
		for _, p := range page {
			if p.Species != nil {
				names = append(names, fmt.Sprintf("%s (%s)", p.Name, *p.Species))
			} else {
				names = append(names, p.Name)
			}
		}
		return fmt.Sprintf("Found %d pets: %s.", total, strings.Join(names, ", "))
	}
}

// findNext is pet_find's next-step hints.
func findNext(in findInput, page []petData) []string {
	if len(page) == 0 {
		if !in.filtered() {
			return []string{"The family has not added a pet yet; add one with pet_create name=… species=…."}
		}
		return []string{"Call pet_find without query to list every pet."}
	}
	p := page[0]
	var next []string
	switch {
	case p.Vet != nil && p.Vet.ContactID != nil:
		next = append(next, fmt.Sprintf("See %s's vet with contact_find id=%s.", p.Name, *p.Vet.ContactID))
	case p.Vet == nil:
		next = append(next, fmt.Sprintf("%s has no vet saved; look for one with contact_find trade=vet.", p.Name))
	}
	next = append(next,
		fmt.Sprintf("Book a vet visit for %s with event_create (calendar_id \"Pets\" if the family has one).", p.Name),
		fmt.Sprintf("Read notes about %s with note_find about_type=pet about_id=%s.", p.Name, p.ID))
	return next
}

// parseFindInput validates pet_find arguments.
func parseFindInput(raw map[string]any) (findInput, error) {
	in := findInput{limit: defaultLimit}
	args := withAliases(raw, findAliases)
	if s, err := optionalString(args, "id"); err != nil {
		return in, err
	} else if s != nil {
		id, perr := uuid.Parse(*s)
		if perr != nil {
			return in, badArg("id", fmt.Sprintf("id must be a UUID; got %q.", *s),
				"Pass a pet's id (a UUID) from pet_find, or use query to search by name.")
		}
		in.id = &id
	}
	if q, err := optionalString(args, "query"); err != nil {
		return in, err
	} else if q != nil {
		in.query = *q
	}
	if n, ok, err := wholeNumber(args, "limit", 1, maxLimit); err != nil {
		return in, err
	} else if ok {
		in.limit = n
	}
	if c, err := optionalString(args, "cursor"); err != nil {
		return in, err
	} else if c != nil {
		off, ok := respond.DecodeCursor(*c)
		if !ok {
			return in, badArg("cursor", fmt.Sprintf("cursor %q is not a cursor from pet_find.", *c),
				"Pass next_cursor from the previous result as cursor, or leave cursor out for the first page.")
		}
		in.offset = off
	}
	return in, nil
}
