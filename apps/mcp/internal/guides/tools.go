// Package guides serves the family's how-tos and runbooks (migration 049;
// KB architecture/mcp-tool-catalog.md "guide: how-tos and runbooks" and
// architecture/guides-api.md): "replace the air filter", "reset the router",
// "pet care for the sitter".
//
// Tools: guide_find (R), guide_get (R), guide_create (C), guide_update (U)
// and guide_delete (X). They follow the API's contract
// (apps/api/internal/guides):
//
//   - Every save (create, or an update that changes something) writes a
//     guide_revisions row in the same transaction. An update first takes the
//     guide's row lock, and the revision is stamped clock_timestamp(), not
//     NOW(), so of two racing saves the one that commits last is the newest
//     revision and the newest revision always equals guides.body.
//   - Each save upserts household_search doc "guide-<uuid>" (title, summary +
//     body, tags, property_id), the same document the API writes, and a
//     delete removes it. Indexing is best effort: it runs after the commit,
//     detached from the call with a 5s bound, and a failure is only logged.
//   - guide_find with query searches household_search by meaning (Typesense
//     vector search with its local embedding model, best first), then adds
//     guides whose words match in SQL. Without Typesense, or when it fails, only the SQL
//     word match runs.
//   - A guide may be about one property, asset or vehicle (about_type +
//     about_id, a name or an id), or the family. A vehicle excludes the other
//     two (guides_subject_check); a guide about an asset is also about its
//     property (derive_subject_property), so about_type=property finds both.
//
// Roles follow the API's policy (apps/api/internal/auth/roles.go, ResGuides):
// owner, family_manager, family_member, viewer and housesitter read guides;
// only the family roles write; vendor and unknown roles get nothing. The role
// is re-read from memberships on every call.
package guides

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/search"
	"home-os/mcp/internal/server"
)

type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// Limits. The save limits are the API's (apps/api/internal/guides).
const (
	defaultLimit = 25
	maxLimit     = 100

	maxTitle      = 255
	maxSummary    = 2000
	maxBody       = 256 << 10 // 256 KiB of markdown
	maxTags       = 30
	maxTagLen     = 50
	maxChangeNote = 500
	maxQuery      = 500

	// duplicateWindow is the guide_create loop guard (catalog §1).
	duplicateWindow = "10 minutes"
	// recentRevisions is how many revisions guide_get lists.
	recentRevisions = 5
	// excerptLen is the length of the body excerpt guide_find shows for a
	// guide without a summary.
	excerptLen = 160
	// maxNames is how many candidates an error lists.
	maxNames = 30
)

// entityType is the guide's entity_type in household_search (and its
// entity_kind, migration 049).
const entityType = "guide"

// ---------------------------------------------------------------------------
// Search index (Typesense household_search)
// ---------------------------------------------------------------------------

// Typesense settings. They match apps/api/internal/search/typesense.go.
const (
	searchTimeout = 5 * time.Second
	// semanticHits is how many hits guide_find asks Typesense for; guides
	// are then filtered and paged in SQL/Go.
	semanticHits = 100
	// embeddingField is household_search's auto-embedding field.
	embeddingField = "embedding"
	// semanticDistanceThreshold drops vector neighbours whose cosine
	// distance is above it (all-MiniLM-L12-v2: related pairs land at
	// 0.50-0.69, unrelated ones at 0.88+). Without it every guide is some
	// distance away and would match.
	semanticDistanceThreshold = 0.75
	collectionName            = "household_search"
)

// searchIndex keeps household_search in step with guides and searches it.
// *typesense implements it; nil means Typesense is not configured.
type searchIndex interface {
	index(ctx context.Context, doc search.Document) error
	remove(ctx context.Context, docID string) error
	// search returns the household's guide ids matching query by meaning,
	// best first. propertyID, when set, keeps guides about that property
	// (and its assets).
	search(ctx context.Context, householdID uuid.UUID, query string, propertyID *uuid.UUID, limit int) ([]uuid.UUID, error)
}

// typesense talks to Typesense's REST API with plain HTTP, like
// search.Indexer and the API's search client.
type typesense struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// newTypesense returns nil when Typesense is not configured (no API key).
func newTypesense(host, port, apiKey string) *typesense {
	if apiKey == "" || host == "" {
		return nil
	}
	base := "http://" + host
	if port != "" {
		base += ":" + port
	}
	return &typesense{baseURL: base, apiKey: apiKey, client: &http.Client{Timeout: 10 * time.Second}}
}

// statusError is a non-2xx Typesense response.
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string { return fmt.Sprintf("typesense returned %d: %s", e.code, e.body) }

func (t *typesense) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-TYPESENSE-API-KEY", t.apiKey)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &statusError{code: resp.StatusCode, body: string(b)}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (t *typesense) index(ctx context.Context, doc search.Document) error {
	return t.do(ctx, http.MethodPost, "/collections/"+collectionName+"/documents?action=upsert", doc, nil)
}

func (t *typesense) remove(ctx context.Context, docID string) error {
	err := t.do(ctx, http.MethodDelete, "/collections/"+collectionName+"/documents/"+url.PathEscape(docID), nil, nil)
	var se *statusError
	if errors.As(err, &se) && se.code == http.StatusNotFound {
		return nil
	}
	return err
}

// searchParams is a semantic query: query_by lists only the embedding
// field, so Typesense embeds q with the same local model the documents were
// embedded with and ranks guides by vector distance alone. Keyword matching
// is left to SQL (findByWords): fused into Typesense's hybrid rank, words
// such as "the" or "filter" let an oil-change guide outrank the HVAC guide
// for "How do I replace the air filter?".
func searchParams(householdID uuid.UUID, query string, propertyID *uuid.UUID, limit int) url.Values {
	filter := fmt.Sprintf("household_id:=`%s` && entity_type:=`%s`", householdID, entityType)
	if propertyID != nil {
		filter += fmt.Sprintf(" && property_id:=`%s`", *propertyID)
	}
	v := url.Values{}
	v.Set("q", query)
	v.Set("query_by", embeddingField)
	v.Set("vector_query", fmt.Sprintf("%s:([], distance_threshold: %s)",
		embeddingField, strconv.FormatFloat(semanticDistanceThreshold, 'f', -1, 64)))
	v.Set("filter_by", filter)
	v.Set("include_fields", "entity_id")
	v.Set("exclude_fields", embeddingField)
	v.Set("per_page", strconv.Itoa(limit))
	return v
}

// search fails when the collection has no embedding field (the API's
// keyword-only fallback schema); guide_find then matches words in SQL.
func (t *typesense) search(ctx context.Context, householdID uuid.UUID, query string, propertyID *uuid.UUID, limit int) ([]uuid.UUID, error) {
	var resp struct {
		Hits []struct {
			Document struct {
				EntityID string `json:"entity_id"`
			} `json:"document"`
		} `json:"hits"`
	}
	path := "/collections/" + collectionName + "/documents/search?" + searchParams(householdID, query, propertyID, limit).Encode()
	if err := t.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(resp.Hits))
	for _, h := range resp.Hits {
		if id, err := uuid.Parse(h.Document.EntityID); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// docID is the guide's household_search document id (same as the API).
func docID(id uuid.UUID) string { return entityType + "-" + id.String() }

// reindex upserts guide id's household_search document, as the API's
// guides.Document builds it: the summary leads the body because the
// embedding model only reads about 256 tokens. Best effort: detached from
// the call, bounded, and a failure is only logged.
func reindex(ctx context.Context, pool *pgxpool.Pool, idx searchIndex, id uuid.UUID) {
	if idx == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), searchTimeout)
	defer cancel()
	var (
		doc        search.Document
		summary    *string
		body       string
		propertyID *uuid.UUID
		created    time.Time
		updated    time.Time
		household  uuid.UUID
	)
	err := pool.QueryRow(ctx, `SELECT household_id, title, summary, body, tags, property_id, created_at, updated_at
		FROM guides WHERE id = $1`, id).Scan(&household, &doc.Title, &summary, &body, &doc.Tags, &propertyID, &created, &updated)
	if err != nil {
		slog.Warn("guides: saved but not indexed for search", "guide_id", id, "error", err)
		return
	}
	doc.ID, doc.HouseholdID, doc.EntityType, doc.EntityID = docID(id), household.String(), entityType, id.String()
	doc.Body = body
	if summary != nil && *summary != "" {
		doc.Body = *summary + "\n\n" + body
	}
	if doc.Tags == nil {
		doc.Tags = []string{}
	}
	if propertyID != nil {
		s := propertyID.String()
		doc.PropertyID = &s
	}
	doc.CreatedAt, doc.UpdatedAt = created.Unix(), updated.Unix()
	if err := idx.index(ctx, doc); err != nil {
		slog.Warn("guides: saved but not indexed for search", "guide_id", id, "error", err)
	}
}

// unindex removes a deleted guide from household_search (best effort).
func unindex(ctx context.Context, idx searchIndex, id uuid.UUID) {
	if idx == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), searchTimeout)
	defer cancel()
	if err := idx.remove(ctx, docID(id)); err != nil {
		slog.Warn("guides: deleted but still in search; run reindex", "guide_id", id, "error", err)
	}
}

// ---------------------------------------------------------------------------
// Access
// ---------------------------------------------------------------------------

// querier is what both the pool and a transaction offer.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// caller is who is calling, with the role re-read from memberships.
type caller struct {
	householdID uuid.UUID
	// userID is nil for local-dev claims without a user; revisions then
	// record no editor.
	userID *uuid.UUID
	role   string
}

// callerOf resolves the caller's current role the way the people, pet and
// vehicle tools do: re-read from memberships for the claims' user and
// household, so a demoted member loses access at once and a removed one gets
// "not signed in". A non-nil result is the isError to return.
func callerOf(ctx context.Context, q querier) (caller, *mcp.CallToolResult) {
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
		var ok bool
		err = q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM households WHERE id = $1)`, hid).Scan(&ok)
		if err == nil && !ok {
			return caller{}, notSignedIn()
		}
	} else {
		out.userID = &uid
		err = q.QueryRow(ctx, `SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
			hid, uid).Scan(&out.role)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return caller{}, notSignedIn()
	}
	if err != nil {
		return caller{}, respond.Failed("check your access", err)
	}
	return out, nil
}

// canRead: API ResGuides read (rolesSitter). Unknown roles may not, so a new
// role fails closed. A vendor reads no guides.
func canRead(role string) bool {
	switch role {
	case "owner", "family_manager", "family_member", "viewer", "housesitter":
		return true
	}
	return false
}

// canWrite: API ResGuides write (rolesFamily).
func canWrite(role string) bool {
	return role == "owner" || role == "family_manager" || role == "family_member"
}

// canSeeVehicles: API ResVehicles read (rolesReaders). A housesitter may read
// a guide about a car, but not the car's name.
func canSeeVehicles(role string) bool {
	return canWrite(role) || role == "viewer"
}

func roleLabel(role string) string {
	if role == "" {
		return "current"
	}
	return role
}

func cannotRead(role string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't read guides.", roleLabel(role)),
		Fix:     "Tell the user to ask an owner or family manager for access; retrying will not help.",
	})
}

// cannotWrite is the refusal for a role that may not change guides. The
// server middleware refuses read-only roles first; this is the second check.
func cannotWrite(role, tool string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't change guides (tool %s): only an owner, family manager or family member can.", roleLabel(role), tool),
		Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
	})
}

func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}

// annotations sets all four hints (catalog §1). openWorldHint is always
// false.
func annotations(title string, readOnly, destructive, idempotent bool) []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithTitleAnnotation(title),
		mcp.WithReadOnlyHintAnnotation(readOnly),
		mcp.WithDestructiveHintAnnotation(destructive),
		mcp.WithIdempotentHintAnnotation(idempotent),
		mcp.WithOpenWorldHintAnnotation(false),
	}
}

// ---------------------------------------------------------------------------
// Arguments
// ---------------------------------------------------------------------------

// argError is a bad argument: which field, what is wrong, how to fix it.
type argError struct {
	field, message, fix string
}

func (e *argError) Error() string { return e.message }

func badArg(field, message, fix string) error {
	return &argError{field: field, message: message, fix: fix}
}

// invalid turns a parse error into an isError result with a fix and a
// valid example.
func invalid(err error, example map[string]any) *mcp.CallToolResult {
	var ae *argError
	if errors.As(err, &ae) {
		return respond.Invalid(ae.field, ae.message, ae.fix, example)
	}
	return respond.Invalid("", err.Error(), "Correct the arguments and call again.", example)
}

// given reports whether args has a non-null value for key.
func given(args map[string]any, key string) bool {
	v, present := args[key]
	return present && v != nil
}

// withAliases returns a copy of args with other parameter names moved to
// their catalog names (a catalog name that is present wins).
func withAliases(args map[string]any, aliases map[string]string) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	for old, name := range aliases {
		if v, ok := out[old]; ok {
			if !given(out, name) {
				out[name] = v
			}
			delete(out, old)
		}
	}
	return out
}

// Aliases small models use. A catalog name that is present wins.
var (
	idAliases    = map[string]string{"id": "guide_id", "guide": "guide_id"}
	writeAliases = map[string]string{
		"id": "guide_id", "guide": "guide_id",
		"name": "title", "steps": "body", "content": "body", "text": "body", "markdown": "body",
		"description": "summary", "tag": "tags",
		"note": "change_note", "reason": "change_note",
	}
	findAliases = map[string]string{"q": "query", "search": "query", "question": "query", "tags": "tag"}
)

// optionalString returns the trimmed string argument, or nil when it is
// absent, null or blank. A non-string value is an error.
func optionalString(args map[string]any, key string) (*string, error) {
	v, present := args[key]
	if !present || v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, badArg(key, key+" must be text.", fmt.Sprintf("Pass %s as text in quotes.", key))
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	return &s, nil
}

// textField is one optional text param. An absent param or "" is not given
// (small models send "" for unused params); JSON null asks to clear it.
type textField struct {
	set, clear bool
	val        string
}

func (f textField) given() bool { return f.set || f.clear }

// readText reads a text param, trimmed. trim=false keeps the text as sent
// (a markdown body), but blank still means not given.
func readText(args map[string]any, key string, maxRunes int, trim bool) (textField, error) {
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
	if strings.TrimSpace(s) == "" {
		return textField{}, nil
	}
	if trim {
		s = strings.TrimSpace(s)
	}
	if maxRunes > 0 && utf8.RuneCountInString(s) > maxRunes {
		return textField{}, badArg(key, fmt.Sprintf("%s must be at most %d characters.", key, maxRunes),
			fmt.Sprintf("Pass a shorter %s.", key))
	}
	return textField{set: true, val: s}, nil
}

// readBody reads the markdown body: kept as sent, at most 256 KiB.
func readBody(args map[string]any) (textField, error) {
	f, err := readText(args, "body", 0, false)
	if err != nil {
		return f, err
	}
	if f.set && len(f.val) > maxBody {
		return textField{}, badArg("body", "body must be at most 256 KiB.", "Shorten the steps, or split them into two guides.")
	}
	return f, nil
}

// tagsField is the tags param: absent or "" is not given, null or [] clears.
type tagsField struct {
	set  bool
	tags []string
}

// readTags reads tags as a list of strings, or as one comma-separated
// string ("hvac, filters"). Tags are trimmed and lower-cased; blanks and
// repeats are dropped (the API's CleanTags).
func readTags(args map[string]any) (tagsField, error) {
	v, present := args["tags"]
	if !present {
		return tagsField{}, nil
	}
	bad := badArg("tags", "tags must be a list of words.", `Pass tags as a list, e.g. ["hvac","filters"].`)
	var raw []string
	switch x := v.(type) {
	case nil:
		return tagsField{set: true, tags: []string{}}, nil
	case string:
		if strings.TrimSpace(x) == "" {
			return tagsField{}, nil
		}
		if strings.HasPrefix(strings.TrimSpace(x), "[") { // a JSON list sent as text
			if err := json.Unmarshal([]byte(x), &raw); err != nil {
				return tagsField{}, bad
			}
		} else {
			raw = strings.Split(x, ",")
		}
	case []any:
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return tagsField{}, bad
			}
			raw = append(raw, s)
		}
	case []string:
		raw = x
	default:
		return tagsField{}, bad
	}
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || slices.Contains(out, t) {
			continue
		}
		if utf8.RuneCountInString(t) > maxTagLen {
			return tagsField{}, badArg("tags", fmt.Sprintf("Each tag must be at most %d characters.", maxTagLen), "Use short tags, e.g. hvac.")
		}
		out = append(out, t)
	}
	if len(out) > maxTags {
		return tagsField{}, badArg("tags", fmt.Sprintf("At most %d tags.", maxTags), "Keep the few tags that matter most.")
	}
	return tagsField{set: true, tags: out}, nil
}

// boolArg reads a boolean param; "true"/"false" text is accepted.
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

// wholeNumber reads an integer argument in [lo, hi]; numeric strings are
// accepted.
func wholeNumber(args map[string]any, key string, lo, hi int) (int, bool, error) {
	v, present := args[key]
	if !present || v == nil {
		return 0, false, nil
	}
	bad := badArg(key, fmt.Sprintf("%s must be a whole number from %d to %d.", key, lo, hi),
		fmt.Sprintf("Pass %s as a whole number between %d and %d.", key, lo, hi))
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case int:
		n = float64(x)
	case int64:
		n = float64(x)
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, false, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false, bad
		}
		n = f
	default:
		return 0, false, bad
	}
	if math.IsNaN(n) || n != math.Trunc(n) || n < float64(lo) || n > float64(hi) {
		return 0, false, bad
	}
	return int(n), true, nil
}

// ---------------------------------------------------------------------------
// about_type / about_id
// ---------------------------------------------------------------------------

// aboutTypes is the about_type enum for guides: the catalog's about link,
// narrowed to what guides can be about (049's typed subject columns).
var aboutTypes = []string{"family", "property", "asset", "vehicle"}

// aboutTypeAliases are other words small models use.
var aboutTypeAliases = map[string]string{
	"household": "family", "none": "family",
	"house": "property", "home": "property", "properties": "property",
	"thing": "asset", "appliance": "asset", "item": "asset", "equipment": "asset", "assets": "asset",
	"car": "vehicle", "truck": "vehicle", "van": "vehicle", "vehicles": "vehicle",
}

// otherAboutTypes are catalog about types a guide cannot be about.
var otherAboutTypes = map[string]bool{
	"person": true, "room": true, "pet": true, "contact": true, "bill": true, "loan": true,
	"maintenance": true, "event": true, "list": true, "recipe": true, "guide": true,
}

// subject is a property, asset or vehicle of the family.
type subject struct {
	kind string // property | asset | vehicle
	id   uuid.UUID
	name string
}

// aboutArg is the parsed about_type + about_id.
type aboutArg struct {
	given  bool     // the call asked for a subject (or for none)
	family bool     // about the family: no subject
	subj   *subject // set when about a property, asset or vehicle
}

// readAboutType normalizes about_type: "" when not given.
func readAboutType(args map[string]any) (string, error) {
	s, err := optionalString(args, "about_type")
	if err != nil || s == nil {
		return "", err
	}
	t := strings.ToLower(*s)
	if a, ok := aboutTypeAliases[t]; ok {
		t = a
	}
	if slices.Contains(aboutTypes, t) {
		return t, nil
	}
	if otherAboutTypes[t] {
		fix := "Pass about_type property, asset or vehicle with its about_id, or leave both out for a family-wide guide."
		if t == "room" {
			fix = "Pass the room's property (about_type=property), or the asset in the room (about_type=asset)."
		}
		return "", badArg("about_type", fmt.Sprintf("A guide can't be about a %s: only a property, asset or vehicle, or the family.", t), fix)
	}
	return "", badArg("about_type", fmt.Sprintf("about_type %q is not one of %s.", *s, strings.Join(aboutTypes, ", ")),
		"Pass about_type property, asset or vehicle, or family.")
}

// subjectCandidate is one property, asset or vehicle with the names the
// family calls it by.
type subjectCandidate struct {
	subject
	names []string
}

// loadSubjects lists the family's properties, assets and vehicles of kind
// (all three when kind is "").
func loadSubjects(ctx context.Context, q querier, hid uuid.UUID, kind string) ([]subjectCandidate, error) {
	rows, err := q.Query(ctx, `
SELECT * FROM (
  SELECT 'property' AS kind, id, name AS label, ARRAY[name] AS names, 1 AS ord FROM properties WHERE household_id = $1
  UNION ALL
  SELECT 'asset', id, name, ARRAY[name], 2 FROM assets WHERE household_id = $1
  UNION ALL
  SELECT 'vehicle', id, concat_ws(' ', year::text, make, model),
         ARRAY[concat_ws(' ', year::text, make, model), concat_ws(' ', make, model), COALESCE(model, ''), COALESCE(vin, '')], 3
    FROM vehicles WHERE household_id = $1
) s WHERE $2 = '' OR s.kind = $2
ORDER BY s.ord, lower(s.label), s.id`, hid, kind)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (subjectCandidate, error) {
		var c subjectCandidate
		var ord int
		err := r.Scan(&c.kind, &c.id, &c.name, &c.names, &ord)
		return c, err
	})
}

// resolveAbout reads about_type and about_id. about_id is the subject's id
// or its name ("Furnace", "Lake House", "Honda Odyssey"), any case; without
// about_type all three kinds are searched. about_type=family, or about_id
// null, means no subject. Several matches, or none, is an isError that lists
// the candidates with their ids.
func resolveAbout(ctx context.Context, q querier, hid uuid.UUID, args map[string]any, example map[string]any) (aboutArg, *mcp.CallToolResult, error) {
	t, err := readAboutType(args)
	if err != nil {
		return aboutArg{}, invalid(err, example), nil
	}
	rawID, idPresent := args["about_id"]
	ref, err := optionalString(args, "about_id")
	if err != nil {
		return aboutArg{}, invalid(err, example), nil
	}
	switch {
	case t == "family":
		return aboutArg{given: true, family: true}, nil, nil
	case ref == nil && idPresent && rawID == nil && t == "":
		return aboutArg{given: true, family: true}, nil, nil // about_id: null clears
	case ref == nil && t == "":
		return aboutArg{}, nil, nil
	case ref == nil:
		return aboutArg{}, respond.Invalid("about_id", fmt.Sprintf("about_id is required with about_type=%s.", t),
			fmt.Sprintf("Pass the %s's name or id as about_id, e.g. %s.", t, aboutExample(t)), example), nil
	}

	all, err := loadSubjects(ctx, q, hid, t)
	if err != nil {
		return aboutArg{}, nil, err
	}
	refID, idErr := uuid.Parse(*ref)
	var hits []subjectCandidate
	for _, c := range all {
		if idErr == nil {
			if c.id == refID {
				hits = append(hits, c)
			}
			continue
		}
		for _, n := range c.names {
			if n != "" && strings.EqualFold(n, *ref) {
				hits = append(hits, c)
				break
			}
		}
	}
	what := t
	if what == "" {
		what = "property, asset or vehicle"
	}
	switch {
	case len(hits) == 1:
		s := hits[0].subject
		return aboutArg{given: true, subj: &s}, nil, nil
	case len(hits) > 1:
		labels := make([]string, len(hits))
		for i, c := range hits {
			labels[i] = fmt.Sprintf("%s %s (%s)", c.kind, c.name, c.id)
		}
		return aboutArg{}, respond.Fail(respond.Error{
			Message: fmt.Sprintf("%d records match %s: %s.", len(hits), *ref, strings.Join(labels, ", ")),
			Fix:     "Pass about_type and the about_id of the one you mean.",
			Field:   "about_id",
			Example: example,
		}), nil
	}
	msg := fmt.Sprintf("No %s %q in this family.", what, *ref)
	if len(all) == 0 {
		msg += fmt.Sprintf(" The family has no %s records yet.", what)
	} else {
		names := make([]string, 0, min(len(all), maxNames))
		for _, c := range all[:min(len(all), maxNames)] {
			names = append(names, c.kind+" "+c.name)
		}
		msg += " Choose from: " + strings.Join(names, ", ") + "."
	}
	return aboutArg{}, respond.Fail(respond.Error{
		Message: msg,
		Fix:     "Pass about_id as one of these names or its id (property_find, asset_find or vehicle_find list ids).",
		Field:   "about_id",
		Example: example,
	}), nil
}

func aboutExample(t string) string {
	switch t {
	case "property":
		return "Lake House"
	case "vehicle":
		return "Honda Odyssey"
	}
	return "Furnace"
}

// ---------------------------------------------------------------------------
// guide_id
// ---------------------------------------------------------------------------

// resolveGuide reads guide_id: the guide's id, or its exact title (any
// case). A UUID is returned as is; the caller's read reports a missing
// guide. Several title matches, or none, is an isError.
func resolveGuide(ctx context.Context, q querier, hid uuid.UUID, args map[string]any, example map[string]any) (uuid.UUID, *mcp.CallToolResult, error) {
	ref, err := optionalString(args, "guide_id")
	if err != nil {
		return uuid.Nil, invalid(err, example), nil
	}
	if ref == nil {
		return uuid.Nil, respond.Invalid("guide_id", "guide_id is required.",
			"Pass the guide's id from guide_find, or its exact title.", example), nil
	}
	if id, perr := uuid.Parse(*ref); perr == nil {
		return id, nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id, title FROM guides WHERE household_id = $1 AND lower(btrim(title)) = lower($2)
		ORDER BY created_at, id`, hid, *ref)
	if err != nil {
		return uuid.Nil, nil, err
	}
	type hit struct {
		id    uuid.UUID
		title string
	}
	hits, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (hit, error) {
		var h hit
		return h, r.Scan(&h.id, &h.title)
	})
	if err != nil {
		return uuid.Nil, nil, err
	}
	switch {
	case len(hits) == 1:
		return hits[0].id, nil, nil
	case len(hits) > 1:
		labels := make([]string, len(hits))
		for i, h := range hits {
			labels[i] = fmt.Sprintf("%s (%s)", h.title, h.id)
		}
		return uuid.Nil, respond.Fail(respond.Error{
			Message: fmt.Sprintf("%d guides are titled %s: %s.", len(hits), *ref, strings.Join(labels, ", ")),
			Fix:     "Pass the guide_id of the one you mean.",
			Field:   "guide_id",
		}), nil
	}
	return uuid.Nil, respond.Fail(respond.Error{
		Message: fmt.Sprintf("No guide titled %q in this family.", *ref),
		Fix:     fmt.Sprintf("Search by meaning with guide_find query=%q, then pass the guide_id it returns.", *ref),
		Field:   "guide_id",
	}), nil
}

// ---------------------------------------------------------------------------
// Rows and output
// ---------------------------------------------------------------------------

// guideRow is a guide with its subject's name and local dates.
type guideRow struct {
	id                                   uuid.UUID
	title                                string
	summary                              *string
	body                                 string
	tags                                 []string
	propertyID, assetID, vehicleID       *uuid.UUID
	propertyName, assetName, vehicleName *string
	createdDate, createdDay              string
	updatedDate, updatedDay              string
}

// guideSelect reads guides as g with their subject's name and dates in the
// family timezone. bodyExpr is the body column (find reads only a prefix).
func guideSelect(bodyExpr string) string {
	return `SELECT g.id, g.title, g.summary, ` + bodyExpr + `, g.tags, g.property_id, g.asset_id, g.vehicle_id,
	p.name, a.name, NULLIF(concat_ws(' ', v.year::text, v.make, v.model), ''),
	to_char(g.created_at AT TIME ZONE h.timezone, 'YYYY-MM-DD'), to_char(g.created_at AT TIME ZONE h.timezone, 'Dy'),
	to_char(g.updated_at AT TIME ZONE h.timezone, 'YYYY-MM-DD'), to_char(g.updated_at AT TIME ZONE h.timezone, 'Dy')
FROM guides g
JOIN households h ON h.id = g.household_id
LEFT JOIN properties p ON p.id = g.property_id
LEFT JOIN assets a ON a.id = g.asset_id
LEFT JOIN vehicles v ON v.id = g.vehicle_id`
}

func scanGuide(row pgx.Row) (guideRow, error) {
	var r guideRow
	err := row.Scan(&r.id, &r.title, &r.summary, &r.body, &r.tags, &r.propertyID, &r.assetID, &r.vehicleID,
		&r.propertyName, &r.assetName, &r.vehicleName, &r.createdDate, &r.createdDay, &r.updatedDate, &r.updatedDay)
	if r.tags == nil {
		r.tags = []string{}
	}
	return r, err
}

// loadGuide reads one guide of the family with its full body.
func loadGuide(ctx context.Context, q querier, hid, id uuid.UUID) (guideRow, error) {
	return scanGuide(q.QueryRow(ctx, guideSelect("g.body")+` WHERE g.id = $1 AND g.household_id = $2`, id, hid))
}

// aboutInfo is what a guide is about.
type aboutInfo struct {
	Type     string  `json:"type" jsonschema_description:"family, property, asset or vehicle."`
	ID       *string `json:"id,omitempty" jsonschema_description:"The property, asset or vehicle id."`
	Name     string  `json:"name,omitempty"`
	Property string  `json:"property,omitempty" jsonschema_description:"Assets only: the property the asset is at."`
}

// label is "the Furnace (Home)", "Lake House", "the 2019 Honda Odyssey" or
// "the family", for summaries.
func (a aboutInfo) label() string {
	switch {
	case a.Type == "family":
		return "the family"
	case a.Name == "":
		return "a " + a.Type
	case a.Type == "asset" && a.Property != "":
		return fmt.Sprintf("%s (%s)", a.Name, a.Property)
	}
	return a.Name
}

func idString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// about is the guide's subject as the caller's role may see it: a
// housesitter may read a guide about a car, not the car's name.
func (r guideRow) about(role string) aboutInfo {
	switch {
	case r.assetID != nil:
		return aboutInfo{Type: "asset", ID: idString(r.assetID), Name: deref(r.assetName), Property: deref(r.propertyName)}
	case r.propertyID != nil:
		return aboutInfo{Type: "property", ID: idString(r.propertyID), Name: deref(r.propertyName)}
	case r.vehicleID != nil:
		a := aboutInfo{Type: "vehicle", ID: idString(r.vehicleID)}
		if canSeeVehicles(role) {
			a.Name = deref(r.vehicleName)
		}
		return a
	}
	return aboutInfo{Type: "family"}
}

// subjectParam is the maintenance_create param naming the guide's subject,
// e.g. "asset_id=…", or "" for a family guide.
func (a aboutInfo) subjectParam() string {
	if a.ID == nil {
		return ""
	}
	return fmt.Sprintf("%s_id=%s", a.Type, *a.ID)
}

// guideItem is one guide in guide_find: no body, a summary (or the start of
// the body when the guide has no summary).
type guideItem struct {
	ID               string    `json:"id"`
	Title            string    `json:"title"`
	Summary          string    `json:"summary,omitempty" jsonschema_description:"The guide's summary, or the start of its steps (… marks cut text)."`
	Tags             []string  `json:"tags"`
	About            aboutInfo `json:"about"`
	UpdatedLocalDate string    `json:"updated_local_date" jsonschema_description:"Last change, YYYY-MM-DD in the family timezone."`
	UpdatedWeekday   string    `json:"updated_weekday"`
}

// excerpt is the first excerptLen characters of the body on one line.
func excerpt(body string) string {
	s := strings.Join(strings.FieldsFunc(body, func(r rune) bool {
		return unicode.IsSpace(r) || r == '#' || r == '*'
	}), " ")
	if utf8.RuneCountInString(s) <= excerptLen {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:excerptLen])) + "…"
}

func (r guideRow) item(role string) guideItem {
	sum := deref(r.summary)
	if sum == "" {
		sum = excerpt(r.body)
	}
	return guideItem{
		ID: r.id.String(), Title: r.title, Summary: sum, Tags: r.tags, About: r.about(role),
		UpdatedLocalDate: r.updatedDate, UpdatedWeekday: r.updatedDay,
	}
}

// revisionInfo is one saved version of a guide (no body).
type revisionInfo struct {
	ID         string `json:"id"`
	LocalDate  string `json:"local_date" jsonschema_description:"When it was saved, YYYY-MM-DD in the family timezone."`
	Weekday    string `json:"weekday"`
	LocalTime  string `json:"local_time" jsonschema_description:"HH:MM in the family timezone."`
	EditedBy   string `json:"edited_by,omitempty" jsonschema_description:"Who saved it."`
	BodyLength int    `json:"body_length" jsonschema_description:"Characters in the steps as saved."`
}

// guideData is one guide in full (guide_get, create, update).
type guideData struct {
	ID               string         `json:"id"`
	Title            string         `json:"title"`
	Summary          *string        `json:"summary"`
	Body             string         `json:"body" jsonschema_description:"The steps, markdown."`
	Tags             []string       `json:"tags"`
	About            aboutInfo      `json:"about"`
	CreatedLocalDate string         `json:"created_local_date"`
	UpdatedLocalDate string         `json:"updated_local_date"`
	UpdatedWeekday   string         `json:"updated_weekday"`
	RevisionCount    int            `json:"revision_count" jsonschema_description:"Saved versions; every save keeps one."`
	RecentRevisions  []revisionInfo `json:"recent_revisions,omitempty" jsonschema_description:"The newest saved versions, newest first."`
	Duplicate        bool           `json:"duplicate,omitempty" jsonschema_description:"true when guide_create returned a guide added minutes ago instead of a copy."`
	Changed          []string       `json:"changed,omitempty" jsonschema_description:"guide_update: the fields that changed."`
}

// loadData reads a guide in full, with its revision count and newest
// revisions.
func loadData(ctx context.Context, q querier, hid, id uuid.UUID, role string) (guideData, error) {
	r, err := loadGuide(ctx, q, hid, id)
	if err != nil {
		return guideData{}, err
	}
	d := guideData{
		ID: r.id.String(), Title: r.title, Summary: r.summary, Body: r.body, Tags: r.tags, About: r.about(role),
		CreatedLocalDate: r.createdDate, UpdatedLocalDate: r.updatedDate, UpdatedWeekday: r.updatedDay,
	}
	rows, err := q.Query(ctx, `
SELECT r.id::text, to_char(r.created_at AT TIME ZONE h.timezone, 'YYYY-MM-DD'), to_char(r.created_at AT TIME ZONE h.timezone, 'Dy'),
       to_char(r.created_at AT TIME ZONE h.timezone, 'HH24:MI'), COALESCE(u.name, ''), char_length(r.body), count(*) OVER ()
  FROM guide_revisions r
  JOIN households h ON h.id = r.household_id
  LEFT JOIN users u ON u.id = r.edited_by
 WHERE r.guide_id = $1 AND r.household_id = $2
 ORDER BY r.created_at DESC, r.id DESC
 LIMIT $3`, id, hid, recentRevisions)
	if err != nil {
		return guideData{}, err
	}
	d.RecentRevisions, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (revisionInfo, error) {
		var v revisionInfo
		return v, row.Scan(&v.ID, &v.LocalDate, &v.Weekday, &v.LocalTime, &v.EditedBy, &v.BodyLength, &d.RevisionCount)
	})
	return d, err
}

// insertRevision writes the body as saved, in the caller's transaction,
// exactly as the API does: an editor with no users row is stored as NULL,
// and created_at is clock_timestamp() (taken after the guide's row lock), not
// NOW(), so revisions sort by commit.
func insertRevision(ctx context.Context, tx pgx.Tx, guideID uuid.UUID, body string, editedBy *uuid.UUID) error {
	_, err := tx.Exec(ctx, `INSERT INTO guide_revisions (guide_id, body, edited_by, created_at)
		VALUES ($1, $2, (SELECT id FROM users WHERE id = $3), clock_timestamp())`, guideID, body, editedBy)
	return err
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// joinNames lists names for a summary: "title, body and tags".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// ---------------------------------------------------------------------------
// guide_find
// ---------------------------------------------------------------------------

var findExample = map[string]any{"query": "replace the air filter"}

// stopWords are dropped from the SQL word match ("how do I reset the
// router" matches on reset and router).
var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "can": true, "do": true, "does": true, "for": true,
	"from": true, "get": true, "guide": true, "how": true, "i": true, "in": true, "is": true, "it": true,
	"me": true, "my": true, "of": true, "on": true, "or": true, "our": true, "should": true, "the": true,
	"to": true, "we": true, "what": true, "when": true, "where": true, "which": true, "who": true,
	"why": true, "with": true, "you": true, "your": true, "this": true, "that": true, "there": true,
	"steps": true, "instructions": true, "please": true,
}

// queryWords are the words of query the SQL match uses: letters and digits
// only (so they are safe inside a regex), stop words dropped, a plural "s"
// dropped so "filters" matches "filter", at most 8.
func queryWords(query string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if stopWords[w] || utf8.RuneCountInString(w) < 2 {
			continue
		}
		if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
			w = strings.TrimSuffix(w, "s")
		}
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
		if len(out) == 8 {
			break
		}
	}
	return out
}

// findFilter is guide_find's SQL filter.
type findFilter struct {
	about aboutArg
	tag   string
}

// where returns the WHERE clause (household first, as $1) and its args.
func (f findFilter) where(hid uuid.UUID) (string, []any) {
	conds := []string{"g.household_id = $1"}
	args := []any{hid}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	switch {
	case f.about.family:
		conds = append(conds, "g.property_id IS NULL AND g.asset_id IS NULL AND g.vehicle_id IS NULL")
	case f.about.subj != nil:
		add("g."+f.about.subj.kind+"_id = $%d", f.about.subj.id)
	}
	if f.tag != "" {
		add("g.tags @> ARRAY[$%d]::text[]", f.tag)
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

const findBody = "left(g.body, 600)"

// findByIDs loads the guides among ids that pass f, in ids' order.
func findByIDs(ctx context.Context, q querier, hid uuid.UUID, f findFilter, ids []uuid.UUID) ([]guideRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	where, args := f.where(hid)
	args = append(args, ids)
	rows, err := q.Query(ctx, guideSelect(findBody)+where+fmt.Sprintf(" AND g.id = ANY($%d)", len(args)), args...)
	if err != nil {
		return nil, err
	}
	found, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (guideRow, error) { return scanGuide(r) })
	if err != nil {
		return nil, err
	}
	rank := make(map[uuid.UUID]int, len(ids))
	for i, id := range ids {
		if _, seen := rank[id]; !seen {
			rank[id] = i
		}
	}
	slices.SortFunc(found, func(a, b guideRow) int { return rank[a.id] - rank[b.id] })
	return found, nil
}

// findByWords matches query's words (word starts, any case) in the title,
// tags, summary and body, best first: a title match counts 3, a tag 2, the
// text 1, summed over the words. A query of only stop words matches the
// whole query as text.
func findByWords(ctx context.Context, q querier, hid uuid.UUID, f findFilter, query string) ([]guideRow, error) {
	where, args := f.where(hid)
	words := queryWords(query)
	var score string
	if len(words) == 0 {
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)+"%")
		score = fmt.Sprintf(`(CASE WHEN concat_ws(' ', g.title, array_to_string(g.tags, ' '), g.summary, g.body) ILIKE $%d THEN 1 ELSE 0 END)`, len(args))
	} else {
		parts := make([]string, 0, len(words))
		for _, w := range words {
			args = append(args, `\m`+w)
			n := len(args)
			parts = append(parts, fmt.Sprintf(`(CASE WHEN g.title ~* $%[1]d THEN 3 ELSE 0 END
				+ CASE WHEN array_to_string(g.tags, ' ') ~* $%[1]d THEN 2 ELSE 0 END
				+ CASE WHEN concat_ws(' ', g.summary, g.body) ~* $%[1]d THEN 1 ELSE 0 END)`, n))
		}
		score = "(" + strings.Join(parts, " + ") + ")"
	}
	rows, err := q.Query(ctx, guideSelect(findBody)+where+" AND "+score+" > 0 ORDER BY "+score+" DESC, lower(g.title), g.id", args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (guideRow, error) { return scanGuide(r) })
}

// findAll lists the guides passing f alphabetically.
func findAll(ctx context.Context, q querier, hid uuid.UUID, f findFilter) ([]guideRow, error) {
	where, args := f.where(hid)
	rows, err := q.Query(ctx, guideSelect(findBody)+where+" ORDER BY lower(g.title), g.id", args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (guideRow, error) { return scanGuide(r) })
}

// NewGuideFindTool creates guide_find (catalog v2, class R). With query it
// searches by meaning ("How do I replace the air filter?" finds "HVAC filter
// replacement") through Typesense, then adds guides whose words match;
// without Typesense only the word match runs. about_type/about_id and tag
// narrow the results; without query the guides are listed alphabetically.
func NewGuideFindTool(pool *pgxpool.Pool, idx searchIndex) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription(`Find how-to guides by meaning ("replace the air filter") or by what they're about. ` +
			"Returns titles and summaries."),
		mcp.WithString("query", mcp.Description("The question or task in plain words, e.g. how do I reset the router")),
		mcp.WithString("about_type", mcp.Description("Only guides about this kind of thing (family = family-wide only)"),
			mcp.Enum(aboutTypes...)),
		mcp.WithString("about_id", mcp.Description("The property, asset or vehicle: its id or name, e.g. Furnace")),
		mcp.WithString("tag", mcp.Description("Only guides with this tag, e.g. hvac")),
		mcp.WithNumber("limit", mcp.Description("Max guides to return, 1-100 (default 25)"), mcp.Min(1), mcp.Max(maxLimit)),
		mcp.WithString("cursor", mcp.Description("next_cursor from the previous result, for the next page")),
		respond.ListSchema[guideItem](),
	}
	tool := mcp.NewTool("guide_find", append(opts, annotations("Find guides", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canRead(c.role) {
			return cannotRead(c.role), nil
		}
		args := withAliases(req.GetArguments(), findAliases)
		query, err := optionalString(args, "query")
		if err != nil {
			return invalid(err, findExample), nil
		}
		if query != nil && utf8.RuneCountInString(*query) > maxQuery {
			return respond.Invalid("query", fmt.Sprintf("query must be at most %d characters.", maxQuery), "Ask in a few words.", findExample), nil
		}
		var f findFilter
		if t, err := optionalString(args, "tag"); err != nil {
			return invalid(err, findExample), nil
		} else if t != nil {
			f.tag = strings.ToLower(*t)
		}
		limit, ok, err := wholeNumber(args, "limit", 1, maxLimit)
		if err != nil {
			return invalid(err, findExample), nil
		}
		if !ok {
			limit = defaultLimit
		}
		offset := 0
		if cur, err := optionalString(args, "cursor"); err != nil {
			return invalid(err, findExample), nil
		} else if cur != nil {
			if offset, ok = respond.DecodeCursor(*cur); !ok {
				return respond.Invalid("cursor", "cursor is not a next_cursor from guide_find.",
					"Pass next_cursor exactly as returned, or leave cursor out for the first page.", findExample), nil
			}
		}
		f.about, bad, err = resolveAbout(ctx, pool, c.householdID, args, findExample)
		if err != nil {
			return respond.Failed("find guides", err), nil
		} else if bad != nil {
			return bad, nil
		}

		var found []guideRow
		byMeaning := false
		if query == nil {
			if found, err = findAll(ctx, pool, c.householdID, f); err != nil {
				return respond.Failed("find guides", err), nil
			}
		} else {
			if idx != nil {
				var propertyID *uuid.UUID
				if f.about.subj != nil && f.about.subj.kind == "property" {
					propertyID = &f.about.subj.id
				}
				sctx, cancel := context.WithTimeout(ctx, searchTimeout)
				ids, serr := idx.search(sctx, c.householdID, *query, propertyID, semanticHits)
				cancel()
				if serr != nil {
					slog.Warn("guides: semantic search failed; matching words instead", "error", serr)
				} else if found, err = findByIDs(ctx, pool, c.householdID, f, ids); err != nil {
					return respond.Failed("find guides", err), nil
				}
				byMeaning = len(found) > 0
			}
			words, err := findByWords(ctx, pool, c.householdID, f, *query)
			if err != nil {
				return respond.Failed("find guides", err), nil
			}
			for _, w := range words {
				if !slices.ContainsFunc(found, func(r guideRow) bool { return r.id == w.id }) {
					found = append(found, w)
				}
			}
		}

		total := len(found)
		offset = min(offset, total)
		end := min(offset+limit, total)
		page := make([]guideItem, 0, end-offset)
		for _, r := range found[offset:end] {
			page = append(page, r.item(c.role))
		}
		return respond.List(findSummary(query, f, byMeaning, page, total, offset), page, respond.Page{
			Total:      total - offset,
			NextCursor: respond.CursorAfter(end, total),
			Narrow:     "Pass next_cursor as cursor for the next page, or add query, about_id or tag.",
		}, findNext(query, f, page)...), nil
	}
	return "guide_find", tool, handler
}

func findSummary(query *string, f findFilter, byMeaning bool, page []guideItem, total, offset int) string {
	filtered := query != nil || f.about.given || f.tag != ""
	var how string
	switch {
	case query != nil && byMeaning:
		how = fmt.Sprintf(" for %q (by meaning)", *query)
	case query != nil:
		how = fmt.Sprintf(" for %q", *query)
	}
	switch {
	case total == 0 && !filtered:
		return "No guides yet."
	case total == 0:
		return "No guides match" + how + "."
	case total == 1:
		g := page[0]
		s := fmt.Sprintf("Found guide %s", g.Title)
		if g.About.Type != "family" {
			s += ", about " + g.About.label()
		}
		return s + how + "."
	case len(page) < total-offset || offset > 0:
		return fmt.Sprintf("Found %d guides%s; showing %d-%d, best first.", total, how, offset+1, offset+len(page))
	case query != nil:
		return fmt.Sprintf("Found %d guides%s, best first.", total, how)
	}
	return fmt.Sprintf("Found %d guides.", total)
}

func findNext(query *string, f findFilter, page []guideItem) []string {
	if len(page) == 0 {
		next := []string{}
		if query != nil || f.about.given || f.tag != "" {
			next = append(next, "Try other words, or call guide_find with no filters to list every guide.")
		}
		return append(next, "Write one with guide_create title=… body=… (markdown steps).")
	}
	g := page[0]
	next := []string{fmt.Sprintf("Read the steps with guide_get guide_id=%s.", g.ID)}
	if p := g.About.subjectParam(); p != "" {
		next = append(next, fmt.Sprintf("Schedule this job with maintenance_create name=… due_date=… %s.", p))
	}
	return next
}

// ---------------------------------------------------------------------------
// guide_get
// ---------------------------------------------------------------------------

var getExample = map[string]any{"guide_id": "HVAC filter replacement"}

// NewGuideGetTool creates guide_get (catalog v2, class R): one guide's steps
// (markdown), what it is about, and its newest saved versions.
func NewGuideGetTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Read one guide's steps, with links to the asset, car or house it's for."),
		mcp.WithString("guide_id", mcp.Description("The guide's id from guide_find, or its exact title"), mcp.Required()),
		respond.Schema[guideData](),
	}
	tool := mcp.NewTool("guide_get", append(opts, annotations("Read guide", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canRead(c.role) {
			return cannotRead(c.role), nil
		}
		args := withAliases(req.GetArguments(), idAliases)
		id, bad, err := resolveGuide(ctx, pool, c.householdID, args, getExample)
		if err != nil {
			return respond.Failed("read the guide", err), nil
		} else if bad != nil {
			return bad, nil
		}
		d, err := loadData(ctx, pool, c.householdID, id, c.role)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("guide", id.String(), "Call guide_find to search the family's guides and get their ids."), nil
		}
		if err != nil {
			return respond.Failed("read the guide", err), nil
		}
		s := "Guide " + d.Title
		if d.About.Type != "family" {
			s += ", about " + d.About.label()
		}
		s += fmt.Sprintf(" (%s, last changed %s %s).", plural(d.RevisionCount, "saved version"), d.UpdatedWeekday, d.UpdatedLocalDate)
		var next []string
		if p := d.About.subjectParam(); p != "" {
			next = append(next, fmt.Sprintf("Schedule this job with maintenance_create name=%q due_date=… %s.", d.Title, p))
		} else {
			next = append(next, fmt.Sprintf("Schedule this job with maintenance_create name=%q due_date=….", d.Title))
		}
		if canWrite(c.role) {
			next = append(next, fmt.Sprintf("Change the steps with guide_update guide_id=%s body=…; the old version is kept.", d.ID))
		}
		return respond.OK(s, d, next...), nil
	}
	return "guide_get", tool, handler
}

// ---------------------------------------------------------------------------
// guide_create
// ---------------------------------------------------------------------------

var createExample = map[string]any{
	"title": "HVAC filter replacement", "body": "1. Turn the furnace off.\n2. Slide out the old filter...",
	"summary": "Swap the 16x25x1 furnace filter every 90 days", "tags": []string{"hvac"},
	"about_type": "asset", "about_id": "Furnace",
}

// saveFields is the validated title/summary/body/tags of a create or update.
type saveFields struct {
	title, summary, body, changeNote textField
	tags                             tagsField
}

func parseSaveFields(args map[string]any) (saveFields, error) {
	var f saveFields
	var err error
	if f.title, err = readText(args, "title", maxTitle, true); err != nil {
		return f, err
	}
	if f.summary, err = readText(args, "summary", maxSummary, true); err != nil {
		return f, err
	}
	if f.body, err = readBody(args); err != nil {
		return f, err
	}
	if f.tags, err = readTags(args); err != nil {
		return f, err
	}
	if f.changeNote, err = readText(args, "change_note", maxChangeNote, true); err != nil {
		return f, err
	}
	return f, nil
}

func sharedSaveParams() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString("summary", mcp.Description("One line on what it's for, e.g. Swap the furnace filter every 90 days")),
		mcp.WithArray("tags", mcp.Description(`Short words to group guides, e.g. ["hvac","filters"]`), mcp.WithStringItems()),
		mcp.WithString("about_type", mcp.Description("What it's about: property, asset or vehicle (family = none)"),
			mcp.Enum(aboutTypes...)),
		mcp.WithString("about_id", mcp.Description("The property, asset or vehicle: its id or name, e.g. Furnace")),
	}
}

// NewGuideCreateTool creates guide_create (catalog v2, class C). The guide
// and its first revision are written in one transaction, then indexed for
// search. The loop guard returns a guide with the same title added to the
// family in the last 10 minutes (duplicate:true) instead of a copy, under an
// advisory lock so parallel calls make one row.
func NewGuideCreateTool(pool *pgxpool.Pool, idx searchIndex) (string, mcp.Tool, toolHandler) {
	opts := append([]mcp.ToolOption{
		mcp.WithDescription("Write a how-to as markdown steps, optionally about an asset, vehicle or property."),
		mcp.WithString("title", mcp.Description("What it's for, e.g. HVAC filter replacement"), mcp.Required()),
		mcp.WithString("body", mcp.Description("The steps as markdown, e.g. 1. Turn the furnace off"), mcp.Required()),
	}, sharedSaveParams()...)
	opts = append(opts, respond.Schema[guideData]())
	tool := mcp.NewTool("guide_create", append(opts, annotations("Write guide", false, false, false)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "guide_create"), nil
		}
		args := withAliases(req.GetArguments(), writeAliases)
		f, err := parseSaveFields(args)
		if err != nil {
			return invalid(err, createExample), nil
		}
		switch {
		case !f.title.set:
			return respond.Invalid("title", "title is required.", "Pass what the guide is for, e.g. HVAC filter replacement, and the steps as body.", createExample), nil
		case !f.body.set:
			return respond.Invalid("body", "body is required.", "Pass the steps as markdown in body, e.g. 1. Turn the furnace off.", createExample), nil
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("save the guide", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		about, bad, err := resolveAbout(ctx, tx, c.householdID, args, createExample)
		if err != nil {
			return respond.Failed("save the guide", err), nil
		} else if bad != nil {
			return bad, nil
		}

		// Loop guard.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('guide_create:' || $1::text || ':' || lower($2), 0))`,
			c.householdID, f.title.val); err != nil {
			return respond.Failed("save the guide", err), nil
		}
		var dupID uuid.UUID
		err = tx.QueryRow(ctx, `SELECT id FROM guides WHERE household_id = $1 AND lower(btrim(title)) = lower($2)
			AND created_at > NOW() - $3::interval ORDER BY created_at DESC LIMIT 1`,
			c.householdID, f.title.val, duplicateWindow).Scan(&dupID)
		switch {
		case err == nil:
			d, err := loadData(ctx, tx, c.householdID, dupID, c.role)
			if err != nil {
				return respond.Failed("save the guide", err), nil
			}
			d.Duplicate = true
			return respond.OK(fmt.Sprintf("Guide %s was already written a few minutes ago; returned it instead of a copy.", d.Title), d,
				fmt.Sprintf("Change its steps with guide_update guide_id=%s body=… instead of creating it again.", d.ID)), nil
		case !errors.Is(err, pgx.ErrNoRows):
			return respond.Failed("save the guide", err), nil
		}

		var propertyID, assetID, vehicleID *uuid.UUID
		if about.subj != nil {
			id := about.subj.id
			switch about.subj.kind {
			case "property":
				propertyID = &id
			case "asset":
				assetID = &id
			case "vehicle":
				vehicleID = &id
			}
		}
		tags := f.tags.tags
		if tags == nil {
			tags = []string{}
		}
		var summary *string
		if f.summary.set {
			summary = &f.summary.val
		}
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO guides (household_id, title, summary, body, tags, property_id, asset_id, vehicle_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
			c.householdID, f.title.val, summary, f.body.val, tags, propertyID, assetID, vehicleID).Scan(&id); err != nil {
			return respond.Failed("save the guide", err), nil
		}
		if err := insertRevision(ctx, tx, id, f.body.val, c.userID); err != nil {
			return respond.Failed("save the guide", err), nil
		}
		var sameTitle int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM guides WHERE household_id = $1 AND id <> $2 AND lower(btrim(title)) = lower($3)`,
			c.householdID, id, f.title.val).Scan(&sameTitle); err != nil {
			return respond.Failed("save the guide", err), nil
		}
		d, err := loadData(ctx, tx, c.householdID, id, c.role)
		if err != nil {
			return respond.Failed("save the guide", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("save the guide", err), nil
		}
		reindex(ctx, pool, idx, id)

		s := "Saved guide " + d.Title
		if d.About.Type != "family" {
			s += ", about " + d.About.label()
		}
		next := []string{fmt.Sprintf("Read it back with guide_get guide_id=%s.", d.ID)}
		if p := d.About.subjectParam(); p != "" {
			next = append(next, fmt.Sprintf("Schedule this job with maintenance_create name=%q due_date=… %s.", d.Title, p))
		}
		if sameTitle > 0 {
			next = append(next, fmt.Sprintf("The family has another guide titled %s; pass guide ids, not the title, from now on.", d.Title))
		}
		return respond.OK(s+".", d, next...), nil
	}
	return "guide_create", tool, handler
}

// ---------------------------------------------------------------------------
// guide_update
// ---------------------------------------------------------------------------

var updateExample = map[string]any{"guide_id": "HVAC filter replacement", "body": "1. Turn the furnace off.\n2. ...", "change_note": "new filter size"}

// NewGuideUpdateTool creates guide_update (catalog v2, class U). Only the
// given fields change: an absent field or "" is left alone; null clears the
// summary or tags; about_type=family (or about_id null) makes it a
// family-wide guide. The guide is row-locked and every save writes a
// revision in the same transaction, so the previous steps stay in history.
// A call that changes nothing writes nothing.
//
// change_note is accepted and echoed in the summary, but not stored:
// guide_revisions (049) has no column for it.
func NewGuideUpdateTool(pool *pgxpool.Pool, idx searchIndex) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Change a guide. Every update keeps the previous version as a revision. Only the given fields change."),
		mcp.WithString("guide_id", mcp.Description("The guide's id from guide_find, or its exact title"), mcp.Required()),
		mcp.WithString("title", mcp.Description("New title, e.g. HVAC filter replacement")),
		mcp.WithString("body", mcp.Description("The full new steps as markdown (replaces the old steps)")),
	}
	opts = append(opts, sharedSaveParams()...)
	opts = append(opts,
		mcp.WithString("change_note", mcp.Description("Why it changed, e.g. new filter size")),
		respond.Schema[guideData]())
	tool := mcp.NewTool("guide_update", append(opts, annotations("Update guide", false, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "guide_update"), nil
		}
		args := withAliases(req.GetArguments(), writeAliases)
		f, err := parseSaveFields(args)
		if err != nil {
			return invalid(err, updateExample), nil
		}
		for _, p := range []struct {
			key string
			f   textField
		}{{"title", f.title}, {"body", f.body}} {
			if p.f.clear {
				return respond.Invalid(p.key, p.key+" cannot be cleared.", "Pass new text, or leave "+p.key+" out to keep it.", updateExample), nil
			}
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("update the guide", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		id, bad, err := resolveGuide(ctx, tx, c.householdID, args, updateExample)
		if err != nil {
			return respond.Failed("update the guide", err), nil
		} else if bad != nil {
			return bad, nil
		}
		about, bad, err := resolveAbout(ctx, tx, c.householdID, args, updateExample)
		if err != nil {
			return respond.Failed("update the guide", err), nil
		} else if bad != nil {
			return bad, nil
		}
		if !f.title.given() && !f.summary.given() && !f.body.given() && !f.tags.set && !about.given {
			return respond.Invalid("", "No fields to update.",
				"Pass guide_id plus at least one of title, body, summary, tags or about_id.", updateExample), nil
		}

		// Row lock first: the revision's clock_timestamp() is taken after it.
		var (
			title, body                    string
			summary                        *string
			tags                           []string
			propertyID, assetID, vehicleID *uuid.UUID
		)
		err = tx.QueryRow(ctx, `SELECT title, summary, body, tags, property_id, asset_id, vehicle_id
			FROM guides WHERE id = $1 AND household_id = $2 FOR UPDATE`, id, c.householdID).
			Scan(&title, &summary, &body, &tags, &propertyID, &assetID, &vehicleID)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("guide", id.String(), "Call guide_find to search the family's guides and get their ids."), nil
		}
		if err != nil {
			return respond.Failed("update the guide", err), nil
		}

		var changed []string
		if f.title.set && f.title.val != title {
			title = f.title.val
			changed = append(changed, "title")
		}
		if f.summary.given() {
			var v *string
			if f.summary.set {
				v = &f.summary.val
			}
			if deref(v) != deref(summary) {
				summary = v
				changed = append(changed, "summary")
			}
		}
		if f.body.set && f.body.val != body {
			body = f.body.val
			changed = append(changed, "body")
		}
		if f.tags.set && !slices.Equal(f.tags.tags, tags) {
			tags = f.tags.tags
			changed = append(changed, "tags")
		}
		if about.given {
			var np, na, nv *uuid.UUID
			if about.subj != nil {
				sid := about.subj.id
				switch about.subj.kind {
				case "property":
					np = &sid
				case "asset":
					na = &sid
				case "vehicle":
					nv = &sid
				}
			}
			// An asset's property is derived (derive_subject_property), so
			// compare what was asked for: the asset, or the property alone.
			same := func(a, b *uuid.UUID) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
			var cur *uuid.UUID
			if assetID == nil {
				cur = propertyID
			}
			if !same(na, assetID) || !same(nv, vehicleID) || (na == nil && !same(np, cur)) {
				propertyID, assetID, vehicleID = np, na, nv
				changed = append(changed, "about")
			}
		}

		if len(changed) == 0 {
			d, err := loadData(ctx, tx, c.householdID, id, c.role)
			if err != nil {
				return respond.Failed("update the guide", err), nil
			}
			return respond.OK(fmt.Sprintf("Nothing changed: guide %s already has those values.", d.Title), d,
				fmt.Sprintf("Read it with guide_get guide_id=%s.", d.ID)), nil
		}
		if tags == nil {
			tags = []string{}
		}
		if _, err := tx.Exec(ctx, `UPDATE guides SET title = $3, summary = $4, body = $5, tags = $6,
				property_id = $7, asset_id = $8, vehicle_id = $9
			WHERE id = $1 AND household_id = $2`,
			id, c.householdID, title, summary, body, tags, propertyID, assetID, vehicleID); err != nil {
			return respond.Failed("update the guide", err), nil
		}
		if err := insertRevision(ctx, tx, id, body, c.userID); err != nil {
			return respond.Failed("update the guide", err), nil
		}
		d, err := loadData(ctx, tx, c.householdID, id, c.role)
		if err != nil {
			return respond.Failed("update the guide", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("update the guide", err), nil
		}
		reindex(ctx, pool, idx, id)
		d.Changed = changed

		s := fmt.Sprintf("Updated guide %s: changed %s", d.Title, joinNames(changed))
		if f.changeNote.set {
			s += fmt.Sprintf(" (%s)", f.changeNote.val)
		}
		s += fmt.Sprintf(". The previous version is kept; %s in all.", plural(d.RevisionCount, "saved version"))
		next := []string{fmt.Sprintf("Read it back with guide_get guide_id=%s.", d.ID)}
		if p := d.About.subjectParam(); p != "" {
			next = append(next, fmt.Sprintf("Schedule this job with maintenance_create name=%q due_date=… %s.", d.Title, p))
		}
		return respond.OK(s, d, next...), nil
	}
	return "guide_update", tool, handler
}

// ---------------------------------------------------------------------------
// guide_delete
// ---------------------------------------------------------------------------

var deleteExample = map[string]any{"guide_id": "HVAC filter replacement"}

// deleteData is guide_delete's result.
type deleteData struct {
	GuideID          string `json:"guide_id"`
	Title            string `json:"title"`
	Deleted          bool   `json:"deleted"`
	RevisionsDeleted int    `json:"revisions_deleted"`
	NotesDeleted     int    `json:"notes_deleted" jsonschema_description:"Notes about the guide, deleted with it."`
	DocumentsMoved   int    `json:"documents_moved" jsonschema_description:"Documents about the guide, now about the family."`
	ListsMoved       int    `json:"lists_moved" jsonschema_description:"Lists about the guide, now about the family."`
}

// NewGuideDeleteTool creates guide_delete (catalog v2, class X). Its
// revisions go with it; 049's rehome trigger deletes notes about the guide
// and moves documents, credentials and lists about it to the family. Notes
// would be lost, so a guide with notes needs confirm:true.
func NewGuideDeleteTool(pool *pgxpool.Pool, idx searchIndex) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Delete a guide and its revisions."),
		mcp.WithString("guide_id", mcp.Description("The guide's id from guide_find, or its exact title"), mcp.Required()),
		mcp.WithBoolean("confirm", mcp.Description("true to also delete notes about the guide")),
		respond.Schema[deleteData](),
	}
	tool := mcp.NewTool("guide_delete", append(opts, annotations("Delete guide", false, true, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "guide_delete"), nil
		}
		args := withAliases(req.GetArguments(), idAliases)

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("delete the guide", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		id, bad, err := resolveGuide(ctx, tx, c.householdID, args, deleteExample)
		if err != nil {
			return respond.Failed("delete the guide", err), nil
		} else if bad != nil {
			return bad, nil
		}
		d := deleteData{GuideID: id.String()}
		err = tx.QueryRow(ctx, `SELECT g.title,
			(SELECT count(*) FROM guide_revisions r WHERE r.guide_id = g.id),
			(SELECT count(*) FROM notes n WHERE n.household_id = g.household_id AND n.entity_type = 'guide' AND n.entity_id = g.id),
			(SELECT count(*) FROM files f WHERE f.household_id = g.household_id AND f.entity_type = 'guide' AND f.entity_id = g.id),
			(SELECT count(*) FROM lists l WHERE l.household_id = g.household_id AND l.entity_type = 'guide' AND l.entity_id = g.id)
			FROM guides g WHERE g.id = $1 AND g.household_id = $2 FOR UPDATE OF g`, id, c.householdID).
			Scan(&d.Title, &d.RevisionsDeleted, &d.NotesDeleted, &d.DocumentsMoved, &d.ListsMoved)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("guide", id.String(), "Call guide_find to search the family's guides and get their ids."), nil
		}
		if err != nil {
			return respond.Failed("delete the guide", err), nil
		}
		if d.NotesDeleted > 0 && !boolArg(args, "confirm") {
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("Guide %s has %s about it, which would be deleted too.", d.Title, plural(d.NotesDeleted, "note")),
				Fix:     "Ask the user, then call again with confirm:true to delete the guide and its notes.",
				Field:   "confirm",
				Example: map[string]any{"guide_id": id.String(), "confirm": true},
			}), nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM guides WHERE id = $1 AND household_id = $2`, id, c.householdID); err != nil {
			return respond.Failed("delete the guide", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("delete the guide", err), nil
		}
		unindex(ctx, idx, id)
		d.Deleted = true

		s := fmt.Sprintf("Deleted guide %s and its %s", d.Title, plural(d.RevisionsDeleted, "saved version"))
		if d.NotesDeleted > 0 {
			s += fmt.Sprintf(", with %s", plural(d.NotesDeleted, "note"))
		}
		s += "."
		if moved := d.DocumentsMoved + d.ListsMoved; moved > 0 {
			s += fmt.Sprintf(" %s about it now belong to the family.", plural(moved, "linked record"))
		}
		return respond.OK(s, d, "List the remaining guides with guide_find."), nil
	}
	return "guide_delete", tool, handler
}
