package file

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// maxLinks caps how many links one document_update call adds or removes.
const maxLinks = 20

// optField is one optional, clearable param. An absent param or "" is not
// given (small models send "" for unused params); JSON null clears it.
type optField struct {
	set   bool
	clear bool
	val   string
}

func (f optField) given() bool { return f.set || f.clear }

// readOpt reads the first of keys (a param and its hidden aliases) that is
// present. ok is false when the value is not a string or null.
func readOpt(args map[string]any, keys ...string) (optField, string, bool) {
	for _, k := range keys {
		v, present := args[k]
		if !present {
			continue
		}
		if v == nil {
			return optField{clear: true}, k, true
		}
		s, ok := v.(string)
		if !ok {
			return optField{}, k, false
		}
		if s = strings.TrimSpace(s); s != "" {
			return optField{set: true, val: s}, k, true
		}
	}
	return optField{}, keys[0], true
}

// linkRef is one parsed add_links/remove_links entry.
type linkRef struct {
	Type string // catalog name
	Kind string // DB entity_kind
	ID   uuid.UUID
}

// parseLinks reads add_links or remove_links: a list of {about_type,
// about_id} objects (one object alone is accepted too). Repeats are dropped.
func parseLinks(args map[string]any, key string) ([]linkRef, *mcp.CallToolResult) {
	example := map[string]any{"document_id": exampleDocumentID,
		key: []any{map[string]any{"about_type": "vehicle", "about_id": "9a1c2b3d-4e5f-4a6b-8c7d-0e1f2a3b4c5d"}}}
	var raw []any
	switch v := args[key].(type) {
	case nil:
		return nil, nil
	case []any:
		raw = v
	case map[string]any:
		raw = []any{v}
	default:
		return nil, respond.Invalid(key, key+" must be a list of {about_type, about_id} objects.",
			"Pass e.g. [{\"about_type\":\"vehicle\",\"about_id\":\"<uuid>\"}].", example)
	}
	if len(raw) > maxLinks {
		return nil, respond.Invalid(key, fmt.Sprintf("%d links were given; the limit is %d per call.", len(raw), maxLinks),
			"Split the change over several document_update calls.", example)
	}
	out := []linkRef{}
	seen := map[string]bool{}
	for i, x := range raw {
		m, ok := x.(map[string]any)
		if !ok {
			return nil, respond.Invalid(key, fmt.Sprintf("%s[%d] is not an object.", key, i),
				"Each link is an object with about_type and about_id.", example)
		}
		t := strings.ToLower(firstStr(m, "about_type", "entity_type"))
		switch t {
		case "vendor":
			t = "contact"
		case "maintenance_task":
			t = "maintenance"
		}
		valid := false
		for _, a := range aboutTypes {
			valid = valid || (a == t && a != "family")
		}
		if !valid {
			return nil, respond.Invalid(key, fmt.Sprintf("%s[%d].about_type %q is not a kind a document can be linked to.", key, i, t),
				"Use one of: "+strings.Join(aboutTypes[1:], ", ")+". A family-wide document needs no link.", example)
		}
		rawID := firstStr(m, "about_id", "entity_id")
		id, err := uuid.Parse(rawID)
		if err != nil {
			return nil, respond.Invalid(key, fmt.Sprintf("%s[%d].about_id %q is not a UUID.", key, i, rawID),
				fmt.Sprintf("Pass the %s's id (a UUID), not its name (%s).", t, findIDWith(t)), example)
		}
		l := linkRef{Type: t, Kind: t, ID: id}
		if k, ok := aboutToDB[t]; ok {
			l.Kind = k
		}
		if k := l.Kind + ":" + id.String(); !seen[k] {
			seen[k] = true
			out = append(out, l)
		}
	}
	return out, nil
}

// NewDocumentUpdateTool creates document_update (catalog v2, class U). Only
// the given fields change; JSON null clears title, description, doc_date,
// expires_on, person_id and tags, while "" counts as not given. category
// can't be cleared ("other" is the default). The rules match the API's PATCH
// /files/{id} (#4478): known category, YYYY-MM-DD dates, expires_on not
// before doc_date (checked against the stored value too), a person of this
// family. about_type/about_id replace what the document is about;
// add_links/remove_links add or remove extra links (document_links rows).
// A repeat call changes nothing more. The metadata document in
// household_search is re-indexed after the commit.
//
// issuer and amount (catalog) have no DB columns yet and are not params.
func NewDocumentUpdateTool(pool *pgxpool.Pool, ts *Typesense) (string, mcp.Tool, toolHandler) {
	linkItem := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"about_type": map[string]any{"type": "string", "enum": aboutTypes[1:]},
			"about_id":   map[string]any{"type": "string", "description": "The row's id (UUID)"},
		},
		"required": []string{"about_type", "about_id"},
	}
	tool := mcp.NewTool("document_update",
		mcp.WithDescription("Change a document's title, category, tags, dates, person or links. Only the fields you pass change; null clears a field."),
		mcp.WithString("document_id",
			mcp.Description("The document's id (UUID), e.g. from document_find"),
			mcp.Required(),
		),
		mcp.WithString("title",
			mcp.Description("New title, e.g. 2026 car insurance policy; null clears it"),
		),
		mcp.WithString("category",
			mcp.Description("New kind of document, e.g. insurance"),
			mcp.Enum(categories...),
		),
		mcp.WithArray("tags",
			mcp.Description("Replaces all tags, e.g. [\"taxes-2026\"]; null or [] clears"),
			mcp.WithStringItems(),
		),
		mcp.WithString("person_id",
			mcp.Description("Family member it is for: id or exact name, e.g. Nora; null clears"),
		),
		mcp.WithString("doc_date",
			mcp.Description("Date on the document, YYYY-MM-DD, e.g. 2026-03-01; null clears"),
		),
		mcp.WithString("expires_on",
			mcp.Description("When it lapses, YYYY-MM-DD, e.g. 2027-03-01; null clears"),
		),
		mcp.WithString("description",
			mcp.Description("Free-text notes about the document; null clears"),
		),
		mcp.WithString("about_type",
			mcp.Description("Move it to be about this kind (family = family-wide)"),
			mcp.Enum(aboutTypes...),
		),
		mcp.WithString("about_id",
			mcp.Description("Id (UUID) of what it's about; needed unless about_type is family"),
		),
		mcp.WithArray("add_links",
			mcp.Description("Also link it to these rows: [{about_type, about_id}]"),
			mcp.Items(linkItem),
		),
		mcp.WithArray("remove_links",
			mcp.Description("Remove these links: [{about_type, about_id}]"),
			mcp.Items(linkItem),
		),
		mcp.WithTitleAnnotation("Update document"),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.Schema[documentData](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := writerOf(ctx, pool, "document_update")
		if bad != nil {
			return bad, nil
		}
		args := req.GetArguments()
		example := map[string]any{"document_id": exampleDocumentID, "category": "insurance", "title": "2026 car insurance"}

		raw := firstStr(args, "document_id", "id", "file_id")
		if raw == "" {
			return respond.Invalid("document_id", "document_id is required.",
				"Pass the document's id from document_find.", example), nil
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			return respond.Invalid("document_id", fmt.Sprintf("document_id %q is not a UUID.", raw),
				"Pass the document's id (a UUID) from document_find, not its title.", example), nil
		}

		// --- read and validate every param before touching the DB ---------
		fields := map[string]optField{}
		for _, p := range []struct {
			name    string
			keys    []string
			maxLen  int
			isDate  bool
			noClear bool
		}{
			{name: "title", keys: []string{"title"}},
			{name: "description", keys: []string{"description"}},
			{name: "category", keys: []string{"category"}, noClear: true},
			{name: "person_id", keys: []string{"person_id"}},
			{name: "doc_date", keys: []string{"doc_date", "document_date"}, isDate: true},
			{name: "expires_on", keys: []string{"expires_on"}, isDate: true},
		} {
			f, _, ok := readOpt(args, p.keys...)
			if !ok {
				return respond.Invalid(p.name, p.name+" must be text or null.",
					fmt.Sprintf("Pass %s as text in quotes, or null to clear it.", p.name), example), nil
			}
			switch {
			case p.noClear && f.clear:
				return respond.Invalid(p.name, "category can't be cleared.",
					"Pass category \"other\" instead.", map[string]any{"document_id": raw, "category": "other"}), nil
			case f.set && p.maxLen > 0 && utf8.RuneCountInString(f.val) > p.maxLen:
				return respond.Invalid(p.name, fmt.Sprintf("%s must be at most %d characters.", p.name, p.maxLen),
					fmt.Sprintf("Pass a shorter %s.", p.name), example), nil
			case f.set && p.isDate:
				if _, err := time.Parse(time.DateOnly, f.val); err != nil {
					return badDate(p.name, f.val, map[string]any{"document_id": raw, p.name: "2026-03-01"}), nil
				}
			}
			fields[p.name] = f
		}
		if f := fields["category"]; f.set {
			cat, ok := parseCategory(f.val)
			if !ok {
				return badCategory(f.val, example), nil
			}
			f.val = cat
			fields["category"] = f
		}

		var tags []string
		tagsGiven := false
		if v, present := args["tags"]; present {
			tagsGiven = true
			if v != nil {
				var ok bool
				if tags, ok = stringList(args, "tags"); !ok {
					return respond.Invalid("tags", "tags must be a list of strings.",
						"Pass tags as a list, e.g. [\"taxes-2026\", \"car\"], or null to clear them.",
						map[string]any{"document_id": raw, "tags": []string{"taxes-2026"}}), nil
				}
				if len(tags) > maxTags {
					return respond.Invalid("tags", fmt.Sprintf("%d tags were given; the limit is %d.", len(tags), maxTags),
						"Keep the few labels that matter.", map[string]any{"document_id": raw, "tags": []string{"taxes-2026"}}), nil
				}
			}
			if tags == nil {
				tags = []string{}
			}
		}

		aboutExample := map[string]any{"document_id": raw, "about_type": "vehicle", "about_id": "9a1c2b3d-4e5f-4a6b-8c7d-0e1f2a3b4c5d"}
		a, bad := parseAbout(args, true, aboutExample)
		if bad != nil {
			return bad, nil
		}
		if a.Type == "family" {
			a.ID = c.householdID
		}
		adds, bad := parseLinks(args, "add_links")
		if bad != nil {
			return bad, nil
		}
		removes, bad := parseLinks(args, "remove_links")
		if bad != nil {
			return bad, nil
		}

		changed := []string{}
		for _, name := range []string{"title", "category", "tags", "person_id", "doc_date", "expires_on", "description"} {
			if name == "tags" {
				if tagsGiven {
					changed = append(changed, name)
				}
				continue
			}
			if fields[name].given() {
				changed = append(changed, name)
			}
		}
		if a.Type != "" {
			changed = append(changed, "about")
		}
		if len(changed) == 0 && len(adds) == 0 && len(removes) == 0 {
			return respond.Invalid("document_id", "Nothing to change was given.",
				"Pass at least one field to change, e.g. category or title, or add_links/remove_links.", example), nil
		}

		// --- write ---------------------------------------------------------
		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("update the document", err), nil
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		cur, err := loadVisible(ctx, tx, c, id, true)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("document", raw, "Call document_find to find the document_id."), nil
		}
		if err != nil {
			return respond.Failed("update the document", err), nil
		}

		// expires_on must not be before doc_date, counting the stored value
		// of whichever one is not being changed.
		docDate, expires := cur.DocumentDate, cur.ExpiresOn
		if f := fields["doc_date"]; f.given() {
			docDate = nil
			if f.set {
				docDate = &f.val
			}
		}
		if f := fields["expires_on"]; f.given() {
			expires = nil
			if f.set {
				expires = &f.val
			}
		}
		if docDate != nil && expires != nil && *expires < *docDate {
			return respond.Invalid("expires_on", fmt.Sprintf("expires_on %s would be before doc_date %s.", *expires, *docDate),
				"Pass an expiry on or after the document date, or change doc_date too.",
				map[string]any{"document_id": raw, "doc_date": *docDate, "expires_on": *docDate}), nil
		}

		set := []string{"updated_at = NOW()"}
		vals := []any{id, c.householdID}
		add := func(col, cast string, v any) {
			vals = append(vals, v)
			set = append(set, fmt.Sprintf("%s = $%d%s", col, len(vals), cast))
		}
		for _, p := range []struct{ name, col, cast string }{
			{"title", "title", ""}, {"description", "description", ""}, {"category", "category", ""},
			{"doc_date", "document_date", "::date"}, {"expires_on", "expires_on", "::date"},
		} {
			f := fields[p.name]
			switch {
			case f.set:
				add(p.col, p.cast, f.val)
			case f.clear:
				add(p.col, p.cast, nil)
			}
		}
		if tagsGiven {
			add("tags", "", tags)
		}
		if f := fields["person_id"]; f.given() {
			var pid *uuid.UUID
			if f.set {
				got, _, bad, err := resolvePerson(ctx, tx, c.householdID, f.val, map[string]any{"document_id": raw, "person_id": "Nora"})
				if err != nil {
					return respond.Failed("update the document", err), nil
				}
				if bad != nil {
					return bad, nil
				}
				pid = &got
			}
			add("person_id", "::uuid", pid)
		}
		if a.Type != "" {
			add("entity_type", "", a.Kind)
			add("entity_id", "::uuid", a.ID)
		}

		_, err = tx.Exec(ctx,
			`UPDATE files AS f SET `+strings.Join(set, ", ")+` WHERE f.id = $1 AND f.household_id = $2`, vals...)
		if res := mapWriteError(err, a.Type, a.ID, aboutExample); res != nil {
			return res, nil
		}
		if err != nil {
			return respond.Failed("update the document", err), nil
		}

		title := cur.displayTitle()
		if f := fields["title"]; f.set {
			title = f.val
		} else if f.clear {
			title = cur.Name
		}
		linked, unlinked := 0, 0
		for _, l := range adds {
			tag, err := tx.Exec(ctx,
				`INSERT INTO document_links (household_id, entity_type, entity_id, link_type, link_id, title)
				 SELECT $1::uuid, $2::text, $3::uuid, 'file', $4::text, $5::text
				  WHERE NOT EXISTS (SELECT 1 FROM document_links
				                     WHERE household_id = $1 AND entity_type::text = $2::text AND entity_id = $3::uuid
				                       AND link_type = 'file' AND link_id = $4::text)`,
				c.householdID, l.Kind, l.ID, id.String(), title)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				return respond.Invalid("add_links", fmt.Sprintf("No %s with id %s was found in this family.", l.Type, l.ID),
					fmt.Sprintf("Check the %s's id (%s).", l.Type, findIDWith(l.Type)), aboutExample), nil
			}
			if errors.As(err, &pgErr) && pgErr.Code == "23514" {
				return respond.Invalid("add_links", fmt.Sprintf("Documents can't be linked to a %s yet.", l.Type),
					"Name it in the title or description instead.", example), nil
			}
			if err != nil {
				return respond.Failed("link the document", err), nil
			}
			linked += int(tag.RowsAffected())
		}
		for _, l := range removes {
			tag, err := tx.Exec(ctx,
				`DELETE FROM document_links
				  WHERE household_id = $1 AND entity_type::text = $2 AND entity_id = $3
				    AND link_type = 'file' AND link_id = $4`,
				c.householdID, l.Kind, l.ID, id.String())
			if err != nil {
				return respond.Failed("unlink the document", err), nil
			}
			unlinked += int(tag.RowsAffected())
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("update the document", err), nil
		}

		r, err := loadDocument(ctx, pool, id, c.householdID)
		if err != nil {
			return respond.Failed("read back the document", err), nil
		}
		ts.indexDocument(ctx, r)
		d := r.data()
		if d.Links, err = loadLinks(ctx, pool, c, id); err != nil {
			return respond.Failed("read back the document", err), nil
		}

		summary := "Updated " + r.describe() + "."
		if len(changed) > 0 {
			summary += " Changed " + strings.Join(changed, ", ") + "."
		}
		if len(adds) > 0 {
			summary += fmt.Sprintf(" Added %d link(s)", linked)
			if linked < len(adds) {
				summary += fmt.Sprintf(" (%d already linked)", len(adds)-linked)
			}
			summary += "."
		}
		if len(removes) > 0 {
			summary += fmt.Sprintf(" Removed %d link(s)", unlinked)
			if unlinked < len(removes) {
				summary += fmt.Sprintf(" (%d were not linked)", len(removes)-unlinked)
			}
			summary += "."
		}
		next := []string{fmt.Sprintf("Read it with document_get document_id=%s.", d.ID)}
		if h := r.aboutHint(); h != "" {
			next = append(next, h)
		}
		return respond.OK(summary, d, next...), nil
	}

	return "document_update", tool, handler
}

// mapWriteError turns the DB's refusals of a files write into isError
// results: a person or subject from another family (23503), a subject kind
// the DB does not support (23514 entity_kind_check), and expires_on before
// document_date (23514, 048's CHECK). nil means err is none of those.
func mapWriteError(err error, aboutType string, aboutID uuid.UUID, example map[string]any) *mcp.CallToolResult {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch {
	case pgErr.Code == "23503" && pgErr.ConstraintName == "files_person_fkey":
		return respond.Invalid("person_id", "That person is not in this family.",
			"Find people with person_find.", map[string]any{"person_id": "Nora"})
	case pgErr.Code == "23503":
		return respond.Invalid("about_id",
			fmt.Sprintf("No %s with id %s was found in this family.", aboutType, aboutID),
			fmt.Sprintf("Check the %s's id (%s), or pass about_type family to make it family-wide.", aboutType, findIDWith(aboutType)),
			example)
	case pgErr.Code == "23514" && pgErr.ConstraintName == "entity_kind_check":
		return respond.Invalid("about_type",
			fmt.Sprintf("Documents about a %s can't be filed yet.", aboutType),
			fmt.Sprintf("Use about_type family, and name the %s in the title.", aboutType), example)
	case pgErr.Code == "23514" && pgErr.ConstraintName == "files_expires_after_document_date":
		return respond.Invalid("expires_on", "expires_on can't be before the document date.",
			"Pass an expiry on or after doc_date.", map[string]any{"expires_on": "2027-03-01"})
	}
	return nil
}
