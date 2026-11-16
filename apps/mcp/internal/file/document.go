// Package file provides the MCP document tools of catalog v2 (KB
// architecture/mcp-tool-catalog.md, "document: the private document hub"):
// document_find, document_get, document_upload, document_update and
// document_delete (#4477 added the last two, hybrid semantic search and the
// role rules of access.go).
//
// A document is a row of the files table (migration 023) with its bytes in
// file_blobs, plus the hub metadata of migration 048 (title, category,
// document_date, expires_on, description, person_id). It belongs to the
// family (entity ('household', household_id)) unless it is about one row of
// the same family; the DB rejects references to other families (038/039).
// The worker extracts text into files.extracted_text and keeps retry state in
// an ocr_error prefix ("[retry k/n next=...]", #4419); these tools turn that
// into a clean text_status and never show the raw prefix. The worker indexes
// the extracted text into the files collection (#4444); this package keeps
// each file's metadata document in household_search up to date on upload,
// update and delete, exactly like the API (#4478), see typesense.go.
//
// Extra links (document_update add_links/remove_links) are document_links
// rows of link_type 'file' (link_id = the file id) owned by the linked row;
// the primary "about" stays files.entity_type/entity_id.
//
// Every result is built with internal/respond, every tool sets all four
// annotation hints and declares an outputSchema with string ids. Extracted
// text is never returned whole: document_get pages it 4000 characters at a
// time, and document_find returns a short snippet. All operations are scoped
// to the caller's household.
package file

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// toolHandler is the signature every document tool handler has.
type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// querier is a pool or a transaction.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const (
	defaultLimit = 25
	maxLimit     = 100
	// textPageSize is how many characters of extracted text document_get
	// returns per call (catalog: 4000 chars per page).
	textPageSize = 4000
	// snippetLen is the length of a document_find snippet.
	snippetLen = 200
	// maxNameRunes is files.name (VARCHAR(255)); also content_type.
	maxNameRunes = 255
	// maxUploadBytes matches the API's upload limit (25 MB).
	maxUploadBytes = 25 << 20
	// maxTags caps how many tags one document carries.
	maxTags = 20
)

// exampleDocumentID is the UUID used in error examples.
const exampleDocumentID = "7c1e2f3a-4b5d-4e6f-8a9b-0c1d2e3f4a5b"

// categories is the files.category set of migration 048. The catalog lists
// nine of them; vehicle and home are also accepted because the DB and the
// app use them.
var categories = []string{
	"receipt", "insurance", "tax", "medical", "manual", "warranty",
	"legal", "school", "vehicle", "home", "other",
}

// aboutTypes is the catalog's about_type enum, in catalog order.
var aboutTypes = []string{
	"family", "person", "property", "room", "asset", "vehicle", "pet", "contact",
	"bill", "loan", "maintenance", "event", "list", "recipe", "guide",
}

// aboutToDB maps the catalog names that differ from the DB entity_kind.
var aboutToDB = map[string]string{
	"family":      "household",
	"contact":     "vendor",
	"maintenance": "maintenance_task",
}

// aboutFinder names the registered tool that finds ids for each about_type.
var aboutFinder = map[string]string{
	"person": "person_find", "property": "property_find", "room": "property_find",
	"asset": "asset_find", "vehicle": "vehicle_find", "pet": "pet_find",
	"contact": "contact_find", "bill": "bill_find", "loan": "loan_find",
	"maintenance": "maintenance_find", "event": "event_find", "list": "list_find",
	"guide": "guide_find", "recipe": "recipe_find",
}

// findIDWith says how to get an about_type's id: its finder tool, or the
// user when no finder is registered yet.
func findIDWith(t string) string {
	if tool, ok := aboutFinder[t]; ok {
		return "find it with " + tool
	}
	return "ask the user for it; looking up a " + t + " by name is coming soon"
}

// aboutOpen names the registered tool that opens one row of an about_type by
// id (catalog "<area>_find id=..."). Kinds whose tool is not registered yet
// get no hint.
var aboutOpen = map[string]string{
	"person": "person_find", "property": "property_find", "asset": "asset_find",
	"vehicle": "vehicle_find", "bill": "bill_find", "loan": "loan_find",
	"maintenance": "maintenance_find",
}

// documentSelect loads documents. It is completed by a WHERE clause and is
// scanned by scanDocument (plus any extra columns the caller appends after
// the about name). Dates and weekdays are computed in the family timezone.
const documentColumns = `f.id, f.household_id, f.name, f.title, f.category, f.content_type, f.size,
	f.entity_type::text, f.entity_id, f.person_id, pe.display_name,
	COALESCE(f.tags, '{}'::text[]), f.description,
	to_char(f.document_date, 'YYYY-MM-DD'), to_char(f.document_date, 'Dy'),
	to_char(f.expires_on, 'YYYY-MM-DD'), to_char(f.expires_on, 'Dy'),
	f.ocr_status, f.ocr_error, char_length(COALESCE(f.extracted_text, '')),
	f.created_at, f.updated_at,
	to_char(f.created_at AT TIME ZONE h.timezone, 'YYYY-MM-DD'),
	to_char(f.created_at AT TIME ZONE h.timezone, 'Dy'),
	h.timezone,
	CASE f.entity_type::text
		WHEN 'property'         THEN (SELECT x.name FROM properties x WHERE x.id = f.entity_id)
		WHEN 'room'             THEN (SELECT x.name FROM rooms x WHERE x.id = f.entity_id)
		WHEN 'asset'            THEN (SELECT x.name FROM assets x WHERE x.id = f.entity_id)
		WHEN 'vehicle'          THEN (SELECT NULLIF(concat_ws(' ', x.year::text, x.make, x.model), '') FROM vehicles x WHERE x.id = f.entity_id)
		WHEN 'pet'              THEN (SELECT x.name FROM pets x WHERE x.id = f.entity_id)
		WHEN 'vendor'           THEN (SELECT x.name FROM vendors x WHERE x.id = f.entity_id)
		WHEN 'bill'             THEN (SELECT x.name FROM bills x WHERE x.id = f.entity_id)
		WHEN 'loan'             THEN (SELECT x.name FROM loans x WHERE x.id = f.entity_id)
		WHEN 'maintenance_task' THEN (SELECT x.name FROM maintenance_tasks x WHERE x.id = f.entity_id)
		WHEN 'person'           THEN (SELECT x.display_name FROM people x WHERE x.id = f.entity_id)
		WHEN 'list'             THEN (SELECT x.name FROM lists x WHERE x.id = f.entity_id)
		WHEN 'recipe'           THEN (SELECT x.title FROM recipes x WHERE x.id = f.entity_id)
		WHEN 'guide'            THEN (SELECT x.title FROM guides x WHERE x.id = f.entity_id)
		WHEN 'secret'           THEN (SELECT x.name FROM secrets x WHERE x.id = f.entity_id)
	END`

// documentFrom is the FROM clause documentColumns needs.
const documentFrom = ` FROM files f
	JOIN households h ON h.id = f.household_id
	LEFT JOIN people pe ON pe.id = f.person_id`

// docRow is one document as the tools load it.
type docRow struct {
	ID              uuid.UUID
	HouseholdID     uuid.UUID
	Name            string
	Title           *string
	Category        string
	ContentType     *string
	Size            int64
	EntityType      *string
	EntityID        *uuid.UUID
	PersonID        *uuid.UUID
	PersonName      *string
	Tags            []string
	Description     *string
	DocumentDate    *string
	DocumentWeekday *string
	ExpiresOn       *string
	ExpiresWeekday  *string
	OCRStatus       string
	OCRError        *string
	TextChars       int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CreatedLocal    string
	CreatedWeekday  string
	Timezone        string
	AboutName       *string
}

// scanDocument reads documentColumns from row, then any extra columns.
func scanDocument(row pgx.Row, extra ...any) (docRow, error) {
	var r docRow
	dest := append([]any{&r.ID, &r.HouseholdID, &r.Name, &r.Title, &r.Category, &r.ContentType, &r.Size,
		&r.EntityType, &r.EntityID, &r.PersonID, &r.PersonName,
		&r.Tags, &r.Description,
		&r.DocumentDate, &r.DocumentWeekday, &r.ExpiresOn, &r.ExpiresWeekday,
		&r.OCRStatus, &r.OCRError, &r.TextChars,
		&r.CreatedAt, &r.UpdatedAt, &r.CreatedLocal, &r.CreatedWeekday, &r.Timezone,
		&r.AboutName}, extra...)
	err := row.Scan(dest...)
	return r, err
}

// loadDocument loads one document of the household, pgx.ErrNoRows when there
// is none (or it is another family's).
func loadDocument(ctx context.Context, q querier, id, householdID uuid.UUID) (docRow, error) {
	return scanDocument(q.QueryRow(ctx,
		`SELECT `+documentColumns+documentFrom+` WHERE f.id = $1 AND f.household_id = $2`,
		id, householdID))
}

// loadVisible loads one document the caller may see: pgx.ErrNoRows when it
// is missing, another family's, or attached to a credential and the caller
// may not see those. forUpdate locks the row (inside a transaction).
func loadVisible(ctx context.Context, q querier, c member, id uuid.UUID, forUpdate bool) (docRow, error) {
	sql := `SELECT ` + documentColumns + documentFrom + ` WHERE f.id = $1 AND f.household_id = $2 AND ` + secretFileWhere(3)
	if forUpdate {
		sql += ` FOR UPDATE OF f`
	}
	return scanDocument(q.QueryRow(ctx, sql, id, c.householdID, c.seesSecretFiles()))
}

// docLink is one extra link of a document (a document_links row).
type docLink struct {
	AboutType string  `json:"about_type" jsonschema_description:"What the link points at: vehicle, property, ..."`
	AboutID   string  `json:"about_id"`
	AboutName *string `json:"about_name"`
}

// linkNameSQL names the linked row of a document_links row aliased l.
const linkNameSQL = `CASE l.entity_type::text
		WHEN 'household'        THEN (SELECT x.name FROM households x WHERE x.id = l.entity_id)
		WHEN 'property'         THEN (SELECT x.name FROM properties x WHERE x.id = l.entity_id)
		WHEN 'room'             THEN (SELECT x.name FROM rooms x WHERE x.id = l.entity_id)
		WHEN 'asset'            THEN (SELECT x.name FROM assets x WHERE x.id = l.entity_id)
		WHEN 'vehicle'          THEN (SELECT NULLIF(concat_ws(' ', x.year::text, x.make, x.model), '') FROM vehicles x WHERE x.id = l.entity_id)
		WHEN 'pet'              THEN (SELECT x.name FROM pets x WHERE x.id = l.entity_id)
		WHEN 'vendor'           THEN (SELECT x.name FROM vendors x WHERE x.id = l.entity_id)
		WHEN 'bill'             THEN (SELECT x.name FROM bills x WHERE x.id = l.entity_id)
		WHEN 'loan'             THEN (SELECT x.name FROM loans x WHERE x.id = l.entity_id)
		WHEN 'maintenance_task' THEN (SELECT x.name FROM maintenance_tasks x WHERE x.id = l.entity_id)
		WHEN 'person'           THEN (SELECT x.display_name FROM people x WHERE x.id = l.entity_id)
		WHEN 'list'             THEN (SELECT x.name FROM lists x WHERE x.id = l.entity_id)
		WHEN 'recipe'           THEN (SELECT x.title FROM recipes x WHERE x.id = l.entity_id)
		WHEN 'guide'            THEN (SELECT x.title FROM guides x WHERE x.id = l.entity_id)
		WHEN 'secret'           THEN (SELECT x.name FROM secrets x WHERE x.id = l.entity_id)
	END`

// loadLinks returns a document's extra links, oldest first. Links from a
// credential are only listed for callers who may see credentials (by name,
// never the value).
func loadLinks(ctx context.Context, q querier, c member, fileID uuid.UUID) ([]docLink, error) {
	rows, err := q.Query(ctx,
		`SELECT l.entity_type::text, l.entity_id, `+linkNameSQL+`
		   FROM document_links l
		  WHERE l.household_id = $1 AND l.link_type = 'file' AND l.link_id = $2::text
		    AND ($3::boolean OR l.entity_type::text <> 'secret')
		  ORDER BY l.created_at, l.id`, c.householdID, fileID.String(), c.seesSecretFiles())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []docLink{}
	for rows.Next() {
		var kind string
		var id uuid.UUID
		var name *string
		if err := rows.Scan(&kind, &id, &name); err != nil {
			return nil, err
		}
		out = append(out, docLink{AboutType: catalogKind(kind), AboutID: id.String(), AboutName: name})
	}
	return out, rows.Err()
}

// catalogKind maps a DB entity_kind to its catalog about_type name. A
// credential (secret) is reported as "credential"; kinds outside the catalog
// come back raw.
func catalogKind(kind string) string {
	if kind == "secret" {
		return "credential"
	}
	for about, k := range aboutToDB {
		if k == kind {
			return about
		}
	}
	return kind
}

// displayTitle is the title people see: the title, or the file name.
func (r docRow) displayTitle() string {
	if r.Title != nil && strings.TrimSpace(*r.Title) != "" {
		return *r.Title
	}
	return r.Name
}

// about returns the catalog about_type and about_id. Family documents (and
// rows with no entity, allowed until 056) have no about_id.
func (r docRow) about() (string, *string) {
	if r.EntityType == nil || r.EntityID == nil {
		return "family", nil
	}
	t := catalogKind(*r.EntityType)
	if t == "family" {
		return t, nil
	}
	s := r.EntityID.String()
	return t, &s
}

// textState is the clean extraction status shown instead of ocr_status and
// the raw ocr_error.
type textState struct {
	Status       string // pending, retrying, done, failed, skipped
	Note         string // one plain sentence
	Error        *string
	RetryAttempt *int
	RetryMax     *int
	NextRetryAt  *string
}

// retryMarker matches the worker's ocr_error prefix (#4419):
// "[retry <k>/<n> next=<RFC3339 UTC>] <error>".
var retryMarker = regexp.MustCompile(`^\[retry (\d+)/(\d+) next=([^\]]+)\]\s*`)

// text maps ocr_status and ocr_error to a textState.
func (r docRow) text() textState {
	errText := ""
	if r.OCRError != nil {
		errText = strings.TrimSpace(*r.OCRError)
	}
	switch r.OCRStatus {
	case "done":
		if r.TextChars == 0 {
			return textState{Status: "done", Note: "Extraction finished but found no text."}
		}
		return textState{Status: "done", Note: fmt.Sprintf("Text extracted (%d characters).", r.TextChars)}
	case "skipped":
		return textState{Status: "skipped", Note: "No text is extracted from this kind of file."}
	case "failed":
		if m := retryMarker.FindStringSubmatch(errText); m != nil {
			k, _ := strconv.Atoi(m[1])
			n, _ := strconv.Atoi(m[2])
			s := textState{Status: "retrying", RetryAttempt: &k, RetryMax: &n}
			msg := strings.TrimSpace(errText[len(m[0]):])
			if msg != "" {
				s.Error = &msg
			}
			s.Note = fmt.Sprintf("Text extraction failed (attempt %d of %d) and will be retried.", k, n+1)
			if t, err := time.Parse(time.RFC3339, m[3]); err == nil {
				if loc, lerr := time.LoadLocation(r.Timezone); lerr == nil {
					t = t.In(loc)
				}
				at := t.Format(time.RFC3339)
				s.NextRetryAt = &at
				s.Note = fmt.Sprintf("Text extraction failed (attempt %d of %d); the next try is after %s.", k, n+1, t.Format("Mon Jan 2 15:04"))
			}
			return s
		}
		s := textState{Status: "failed", Note: "Text extraction failed for good; the file itself is stored."}
		if errText != "" {
			s.Error = &errText
			s.Note = "Text extraction failed for good: " + errText
		}
		return s
	default: // pending, processing
		return textState{Status: "pending", Note: "Text extraction is queued; it usually finishes within a few minutes."}
	}
}

// documentItem is one document_find result: metadata and a snippet, never
// the whole text. IDs are strings: jsonschema-go would advertise uuid.UUID
// as a byte array.
type documentItem struct {
	ID               string    `json:"id" jsonschema_description:"The document_id to pass to document_get."`
	Title            string    `json:"title" jsonschema_description:"The document's title, or its file name when it has none."`
	FileName         string    `json:"file_name"`
	Category         string    `json:"category"`
	ContentType      *string   `json:"content_type"`
	SizeBytes        int64     `json:"size_bytes"`
	DocumentDate     *string   `json:"document_date" jsonschema_description:"The date on the document, YYYY-MM-DD."`
	DocumentWeekday  *string   `json:"document_weekday" jsonschema_description:"Weekday of document_date, e.g. Thu."`
	ExpiresOn        *string   `json:"expires_on" jsonschema_description:"When it lapses, YYYY-MM-DD."`
	ExpiresWeekday   *string   `json:"expires_weekday" jsonschema_description:"Weekday of expires_on, e.g. Thu."`
	PersonID         *string   `json:"person_id" jsonschema_description:"The family member it is for."`
	PersonName       *string   `json:"person_name"`
	AboutType        string    `json:"about_type" jsonschema_description:"What it is about: family, vehicle, property, ..."`
	AboutID          *string   `json:"about_id" jsonschema_description:"Id of the row it is about; null for family documents."`
	AboutName        *string   `json:"about_name"`
	Tags             []string  `json:"tags"`
	TextStatus       string    `json:"text_status" jsonschema_description:"Text extraction: pending, retrying, done, failed or skipped."`
	TextChars        int       `json:"text_chars" jsonschema_description:"Length of the extracted text; read it with document_get."`
	Snippet          *string   `json:"snippet" jsonschema_description:"Up to 200 characters around the match (or the start of the text)."`
	CreatedAt        time.Time `json:"created_at" jsonschema_description:"When it was uploaded."`
	CreatedLocalDate string    `json:"created_local_date" jsonschema_description:"created_at as YYYY-MM-DD in the family timezone."`
	CreatedWeekday   string    `json:"created_weekday" jsonschema_description:"Weekday of created_local_date, e.g. Thu."`
}

// textPage is one page of a document's extracted text.
type textPage struct {
	Content       string `json:"content" jsonschema_description:"This page of the extracted text."`
	Offset        int    `json:"offset" jsonschema_description:"Character offset of this page in the whole text."`
	ReturnedChars int    `json:"returned_chars"`
	TotalChars    int    `json:"total_chars"`
	HasMore       bool   `json:"has_more" jsonschema_description:"true when more text follows; call again with next_offset."`
	NextOffset    *int   `json:"next_offset" jsonschema_description:"text_offset for the next page; null on the last page."`
}

// documentData is one document in full, as document_get and document_upload
// return it. Text is only included one page at a time (document_get).
type documentData struct {
	ID               string    `json:"id" jsonschema_description:"The document_id to pass to document_get."`
	Title            string    `json:"title" jsonschema_description:"The document's title, or its file name when it has none."`
	FileName         string    `json:"file_name"`
	Category         string    `json:"category"`
	ContentType      *string   `json:"content_type"`
	SizeBytes        int64     `json:"size_bytes"`
	Description      *string   `json:"description"`
	DocumentDate     *string   `json:"document_date" jsonschema_description:"The date on the document, YYYY-MM-DD."`
	DocumentWeekday  *string   `json:"document_weekday" jsonschema_description:"Weekday of document_date, e.g. Thu."`
	ExpiresOn        *string   `json:"expires_on" jsonschema_description:"When it lapses, YYYY-MM-DD."`
	ExpiresWeekday   *string   `json:"expires_weekday" jsonschema_description:"Weekday of expires_on, e.g. Thu."`
	PersonID         *string   `json:"person_id" jsonschema_description:"The family member it is for."`
	PersonName       *string   `json:"person_name"`
	AboutType        string    `json:"about_type" jsonschema_description:"What it is about: family, vehicle, property, ..."`
	AboutID          *string   `json:"about_id" jsonschema_description:"Id of the row it is about; null for family documents."`
	AboutName        *string   `json:"about_name"`
	Tags             []string  `json:"tags"`
	TextStatus       string    `json:"text_status" jsonschema_description:"Text extraction: pending, retrying, done, failed or skipped."`
	TextNote         string    `json:"text_note" jsonschema_description:"The extraction status in one plain sentence."`
	TextError        *string   `json:"text_error" jsonschema_description:"Why extraction failed (failed or retrying only)."`
	RetryAttempt     *int      `json:"retry_attempt" jsonschema_description:"Failed attempts so far (retrying only)."`
	RetryMax         *int      `json:"retry_max" jsonschema_description:"Retries allowed in total (retrying only)."`
	NextRetryAt      *string   `json:"next_retry_at" jsonschema_description:"Earliest next attempt, family time (retrying only)."`
	TextChars        int       `json:"text_chars" jsonschema_description:"Length of the whole extracted text in characters."`
	Text             *textPage `json:"text,omitempty" jsonschema_description:"document_get only: one page of the extracted text."`
	Links            []docLink `json:"links" jsonschema_description:"Other things this document is also linked to (document_update add_links)."`
	CreatedAt        time.Time `json:"created_at" jsonschema_description:"When it was uploaded."`
	CreatedLocalDate string    `json:"created_local_date" jsonschema_description:"created_at as YYYY-MM-DD in the family timezone."`
	CreatedWeekday   string    `json:"created_weekday" jsonschema_description:"Weekday of created_local_date, e.g. Thu."`
	UpdatedAt        time.Time `json:"updated_at"`
	Duplicate        bool      `json:"duplicate,omitempty" jsonschema_description:"document_upload only: true when the same file was filed minutes ago and was returned instead."`
}

func (r docRow) item(snippet *string) documentItem {
	aboutType, aboutID := r.about()
	d := documentItem{
		ID: r.ID.String(), Title: r.displayTitle(), FileName: r.Name, Category: r.Category,
		ContentType: r.ContentType, SizeBytes: r.Size,
		DocumentDate: r.DocumentDate, DocumentWeekday: r.DocumentWeekday,
		ExpiresOn: r.ExpiresOn, ExpiresWeekday: r.ExpiresWeekday,
		PersonID: uuidString(r.PersonID), PersonName: r.PersonName,
		AboutType: aboutType, AboutID: aboutID, AboutName: r.AboutName,
		Tags: nonNil(r.Tags), TextStatus: r.text().Status, TextChars: r.TextChars, Snippet: snippet,
		CreatedAt: r.CreatedAt, CreatedLocalDate: r.CreatedLocal, CreatedWeekday: r.CreatedWeekday,
	}
	if aboutID == nil {
		d.AboutName = nil
	}
	return d
}

func (r docRow) data() documentData {
	aboutType, aboutID := r.about()
	ts := r.text()
	d := documentData{
		ID: r.ID.String(), Title: r.displayTitle(), FileName: r.Name, Category: r.Category,
		ContentType: r.ContentType, SizeBytes: r.Size, Description: r.Description,
		DocumentDate: r.DocumentDate, DocumentWeekday: r.DocumentWeekday,
		ExpiresOn: r.ExpiresOn, ExpiresWeekday: r.ExpiresWeekday,
		PersonID: uuidString(r.PersonID), PersonName: r.PersonName,
		AboutType: aboutType, AboutID: aboutID, AboutName: r.AboutName,
		Tags:       nonNil(r.Tags),
		TextStatus: ts.Status, TextNote: ts.Note, TextError: ts.Error,
		RetryAttempt: ts.RetryAttempt, RetryMax: ts.RetryMax, NextRetryAt: ts.NextRetryAt,
		TextChars: r.TextChars, Links: []docLink{},
		CreatedAt: r.CreatedAt, CreatedLocalDate: r.CreatedLocal, CreatedWeekday: r.CreatedWeekday,
		UpdatedAt: r.UpdatedAt,
	}
	if aboutID == nil {
		d.AboutName = nil
	}
	return d
}

// describe is a short phrase for summaries, e.g. `"Policy 2026" (insurance)
// about vehicle 2022 Honda Odyssey, for Nora`.
func (r docRow) describe() string {
	s := fmt.Sprintf("%q (%s)", r.displayTitle(), r.Category)
	if t, id := r.about(); id != nil {
		name := *id
		if r.AboutName != nil && *r.AboutName != "" {
			name = *r.AboutName
		}
		s += fmt.Sprintf(" about %s %s", t, name)
	}
	if r.PersonName != nil {
		s += ", for " + *r.PersonName
	}
	return s
}

// aboutHint is a next hint that opens what the document is about, or "".
func (r docRow) aboutHint() string {
	t, id := r.about()
	if id == nil {
		return ""
	}
	if tool, ok := aboutOpen[t]; ok {
		return fmt.Sprintf("See the %s it's about with %s id=%s.", t, tool, *id)
	}
	return ""
}

// ---------------------------------------------------------------------------
// Argument helpers
// ---------------------------------------------------------------------------

// str returns args[key] as a trimmed string ("" when missing or not a string).
func str(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

// firstStr returns the first non-empty string among keys (a param and its
// hidden aliases).
func firstStr(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(args, k); s != "" {
			return s
		}
	}
	return ""
}

// intArg reads a whole number sent as a JSON number or a numeric string:
// (value, given, ok).
func intArg(args map[string]any, key string) (int, bool, bool) {
	switch v := args[key].(type) {
	case nil:
		return 0, false, true
	case float64:
		return int(v), true, v == float64(int(v))
	case int:
		return v, true, true
	case string:
		if strings.TrimSpace(v) == "" {
			return 0, false, true
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, true, err == nil
	}
	return 0, true, false
}

// stringList reads a string array, also accepting a comma-separated string.
// Items are trimmed; empty items and case-insensitive repeats are dropped.
func stringList(args map[string]any, key string) ([]string, bool) {
	var raw []string
	switch v := args[key].(type) {
	case nil:
		return nil, true
	case string:
		raw = strings.Split(v, ",")
	case []any:
		for _, x := range v {
			s, ok := x.(string)
			if !ok {
				return nil, false
			}
			raw = append(raw, s)
		}
	case []string:
		raw = v
	default:
		return nil, false
	}
	out := []string{}
	seen := map[string]bool{}
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	return out, true
}

// parseCategory resolves a category name case-insensitively (a plural such
// as "receipts" also works). It returns "" and false for an unknown name.
func parseCategory(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, c := range categories {
		if s == c || s == c+"s" || (c == "warranty" && s == "warranties") || (c == "tax" && s == "taxes") {
			return c, true
		}
	}
	return "", false
}

// badCategory is the isError result for an unknown category.
func badCategory(raw string, example map[string]any) *mcp.CallToolResult {
	return respond.Invalid("category", fmt.Sprintf("category %q is not one of the document categories.", raw),
		"Use one of: "+strings.Join(categories, ", ")+".", example)
}

// parseDate reads a YYYY-MM-DD param. ok is false for a malformed value.
func parseDate(args map[string]any, keys ...string) (*time.Time, string, bool) {
	raw := firstStr(args, keys...)
	if raw == "" {
		return nil, "", true
	}
	t, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		return nil, raw, false
	}
	return &t, raw, true
}

// badDate is the isError result for a malformed date param.
func badDate(field, raw string, example map[string]any) *mcp.CallToolResult {
	return respond.Invalid(field, fmt.Sprintf("%s %q is not a date.", field, raw),
		fmt.Sprintf("Pass %s as YYYY-MM-DD, e.g. 2026-03-01.", field), example)
}

// about is a parsed about_type/about_id pair.
type about struct {
	Type string    // catalog name, e.g. "contact"
	Kind string    // DB entity_kind, e.g. "vendor"
	ID   uuid.UUID // the row; uuid.Nil when only the type was given (find)
}

// parseAbout reads about_type and about_id (hidden aliases: the legacy
// entity_type/entity_id, with DB kind names). requireID is true for upload,
// where any about_type but family needs an about_id. Neither given returns a
// zero about.
func parseAbout(args map[string]any, requireID bool, example map[string]any) (about, *mcp.CallToolResult) {
	t := strings.ToLower(firstStr(args, "about_type", "entity_type"))
	rawID := firstStr(args, "about_id", "entity_id")
	switch t {
	case "household":
		t = "family"
	case "vendor":
		t = "contact"
	case "maintenance_task":
		t = "maintenance"
	}
	if t == "" && rawID == "" {
		return about{}, nil
	}
	if t == "" {
		return about{}, respond.Invalid("about_type", "about_id was given without about_type.",
			"Pass about_type too (e.g. vehicle), or leave both out for a family document.", example)
	}
	valid := false
	for _, a := range aboutTypes {
		valid = valid || a == t
	}
	if !valid {
		return about{}, respond.Invalid("about_type", fmt.Sprintf("about_type %q is not one of the known kinds.", t),
			"Use one of: "+strings.Join(aboutTypes, ", ")+".", example)
	}
	a := about{Type: t, Kind: t}
	if k, ok := aboutToDB[t]; ok {
		a.Kind = k
	}
	if t == "family" {
		return a, nil // the family's id comes from the caller's household
	}
	if rawID == "" {
		if !requireID {
			return a, nil
		}
		return about{}, respond.Invalid("about_id", fmt.Sprintf("about_type %s needs about_id.", t),
			fmt.Sprintf("Pass the %s's id as about_id (%s), or leave about_type out for a family document.", t, findIDWith(t)),
			example)
	}
	id, err := uuid.Parse(rawID)
	if err != nil {
		return about{}, respond.Invalid("about_id", fmt.Sprintf("about_id %q is not a UUID.", rawID),
			fmt.Sprintf("Pass the %s's id (a UUID), not its name (%s).", t, findIDWith(t)), example)
	}
	a.ID = id
	return a, nil
}

// resolvePerson turns person_id (a people.id, the person's user id, or an
// exact display name, any case) into a person of the household.
func resolvePerson(ctx context.Context, q querier, householdID uuid.UUID, ref string, example map[string]any) (uuid.UUID, string, *mcp.CallToolResult, error) {
	fix := "Find people with person_find, or pass the person's exact name, e.g. Nora."
	if id, err := uuid.Parse(ref); err == nil {
		var pid uuid.UUID
		var name string
		err := q.QueryRow(ctx,
			`SELECT id, display_name FROM people
			 WHERE household_id = $1 AND (id = $2 OR user_id = $2)
			 ORDER BY (id = $2) DESC LIMIT 1`, householdID, id).Scan(&pid, &name)
		if err == pgx.ErrNoRows {
			return uuid.Nil, "", respond.Invalid("person_id", fmt.Sprintf("No person with id %s was found in this family.", ref), fix, example), nil
		}
		return pid, name, nil, err
	}
	rows, err := q.Query(ctx,
		`SELECT id, display_name FROM people
		 WHERE household_id = $1 AND lower(btrim(display_name)) = lower($2)
		 ORDER BY display_name, id`, householdID, ref)
	if err != nil {
		return uuid.Nil, "", nil, err
	}
	defer rows.Close()
	type cand struct {
		id   uuid.UUID
		name string
	}
	var found []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.name); err != nil {
			return uuid.Nil, "", nil, err
		}
		found = append(found, c)
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, "", nil, err
	}
	switch len(found) {
	case 0:
		return uuid.Nil, "", respond.Invalid("person_id", fmt.Sprintf("No person named %q was found in this family.", ref), fix, example), nil
	case 1:
		return found[0].id, found[0].name, nil, nil
	}
	parts := make([]string, len(found))
	for i, c := range found {
		parts[i] = fmt.Sprintf("%s (%s)", c.name, c.id)
	}
	return uuid.Nil, "", respond.Invalid("person_id",
		fmt.Sprintf("%d people are named %q: %s.", len(found), ref, strings.Join(parts, ", ")),
		"Pass the person_id of the one you mean.", example), nil
}

// notSignedIn is the result for calls without household claims.
func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}

// likePattern escapes LIKE wildcards in q and wraps it in %...% (ESCAPE '\').
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// collapseSpace joins runs of whitespace into single spaces.
func collapseSpace(s string) string {
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

// humanSize renders a byte count, e.g. "34.2 KB".
func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func uuidString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
