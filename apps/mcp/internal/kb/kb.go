// Package kb is the family knowledge base over MCP (#5502): the family's
// durable, household-scoped knowledge — "how to reset the router", "our
// pediatrician's after-hours number and when to use it", "what the babysitter
// needs to know".
//
// WHY THIS EXISTS. The family's agents need somewhere to put knowledge for
// EACH OTHER. A note is transient and attached to a record; a file is an
// uploaded document; a guide is a how-to about a property, asset or car. A KB
// doc is the durable "how we do this here" that is written once by one agent
// or person and found later by a different one, by meaning rather than by
// exact address. So the tool descriptions say, in the first sentence, that the
// doc may have been written by someone else FOR the caller.
//
// Tools:
//
//	kb_search  read  — find docs by meaning and keyword; returns path, title,
//	                   excerpt, updated_at and author
//	kb_get     read  — one full doc by path
//	kb_write   write — create or REPLACE the doc at a path (upsert)
//	kb_list    read  — paths and titles, for discovery
//
// THE INDEX IS DERIVED FROM POSTGRES (#5298). kb_write does NOT index: the
// API's reconciler (apps/api/internal/searchrecon, run by cmd/reindex and the
// periodic sweep) builds every doc's household_search document from the
// kb_docs row. That is deliberate. The lesson of #5298 was that indexing on
// the write path means every writer must remember to index, and the next
// writer forgets — which is exactly how the index was empty for a family that
// wrote everything through agents. An agent's kb_write therefore lands in
// Postgres and becomes searchable on the next reconcile pass, so a doc is
// never silently invisible.
//
// Roles follow the one policy table (apps/api/internal/auth/roles.go, ResKB,
// mirrored in internal/auth): EVERY role reads the KB, a housesitter and a
// vendor included, and the family roles write.
//
// Secrets do not belong in a doc. A credential lives in the secrets feature
// (credential_find / credential_reveal) and a doc REFERS to it.
package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// entityType is a KB doc's entity_type in household_search and its kind in
// the role policy's entity registry.
const entityType = "kb_doc"

// Limits, the same as the API's (apps/api/internal/kb).
const (
	MaxPath   = 512
	MaxTitle  = 255
	MaxBody   = 256 << 10
	MaxTags   = 30
	MaxTagLen = 50
	MaxQuery  = 500

	defaultLimit = 20
	maxLimit     = 50
	excerptLen   = 400
)

// Typesense settings, as the API indexes and queries household_search.
const (
	collectionName = "household_search"
	// embeddingField is household_search's auto-embedding field, computed by
	// Typesense from title + body with its local model.
	embeddingField = "embedding"
	// semanticDistanceThreshold drops vector neighbours whose cosine distance
	// is above it (all-MiniLM-L12-v2: related pairs land at 0.50-0.69,
	// unrelated 0.88+).
	semanticDistanceThreshold = 0.75
)

// toolHandler is the shape server.RegisterTool takes.
type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// --- data -------------------------------------------------------------------

// Doc is a KB doc plus its authors' display names.
type Doc struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	Path        string
	Title       string
	Body        string
	Tags        []string
	CreatedName *string
	UpdatedName *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Summary is one search or list row: no body, just what a caller needs to
// decide whether to read the doc.
type Summary struct {
	Path      string
	Title     string
	Tags      []string
	Excerpt   string
	UpdatedAt time.Time
	UpdatedBy *string
	Score     float64
}

// --- access -----------------------------------------------------------------

// caller is who is calling, with the role re-read from memberships so a
// demoted member loses access at once.
type caller struct {
	householdID uuid.UUID
	userID      *uuid.UUID
	role        string
}

// callerOf resolves the caller the way the other tools do.
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
		if c.AgentTokenID != "" {
			// An agent token always resolves to a users.id.
			return caller{}, notSignedIn()
		}
		// Local-dev claims without a user: keep the claims' role.
		var ok bool
		err = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM households WHERE id = $1)`, hid).Scan(&ok)
		if err == nil && !ok {
			return caller{}, notSignedIn()
		}
	} else {
		out.userID = &uid
		err = pool.QueryRow(ctx, `SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
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

func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}

// canRead is API auth.ResKB read: EVERY role, including a housesitter and a
// vendor. The KB exists so the babysitter can find the router doc; an unknown
// or empty role still fails closed, as everywhere.
func canRead(role string) bool {
	return auth.Can(role, auth.ResKB, auth.ActRead)
}

// canWrite is API auth.ResKB write: the family roles.
func canWrite(role string) bool {
	return auth.Can(role, auth.ResKB, auth.ActWrite)
}

func roleLabel(role string) string {
	if role == "" {
		return "current"
	}
	return role
}

func cannotRead(role string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't read the family knowledge base.", roleLabel(role)),
		Fix:     "Tell the user to ask an owner or family manager for access; retrying will not help.",
	})
}

func cannotWrite(role, tool string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't write to the family knowledge base (tool %s): only an owner, family manager or family member can.", roleLabel(role), tool),
		Fix:     "Tell the user this doc is read-only for them; retrying will not help.",
	})
}

// --- validation -------------------------------------------------------------

// pathSegment is migration 076's CHECK, restated so a bad path is a clear
// argument error instead of a database error.
var pathSegment = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// cleanPath validates and normalizes a doc path. A path is a namespaced slug.
// Upper case and spaces are corrected where the intent is unambiguous
// ('How To/Router Reset' -> 'how-to/router-reset'), because a small model will
// otherwise write a second doc at a near-identical path.
func cleanPath(in string) (string, error) {
	p := strings.Trim(strings.TrimSpace(in), "/")
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	segs := strings.Split(p, "/")
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		s = slugSegment(s)
		if s == "" || s == "." || s == ".." || !pathSegment.MatchString(s) {
			return "", fmt.Errorf("path %q must be lower-case letters, digits, '-', '_' or '.', in segments separated by '/'", in)
		}
		out = append(out, s)
	}
	joined := strings.Join(out, "/")
	if utf8.RuneCountInString(joined) > MaxPath {
		return "", fmt.Errorf("path must be at most 512 characters")
	}
	return joined, nil
}

// slugSegment lower-cases a segment and turns whitespace runs into '-'.
func slugSegment(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsSpace(r) {
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
			continue
		}
		lastDash = false
		b.WriteRune(r)
	}
	return b.String()
}

// cleanTags trims and lower-cases tags, drops blanks and duplicates.
func cleanTags(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		if utf8.RuneCountInString(t) > MaxTagLen {
			return nil, fmt.Errorf("each tag must be at most 50 characters")
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > MaxTags {
		return nil, fmt.Errorf("at most 30 tags")
	}
	return out, nil
}

// excerpt shortens a body for a list or search row.
func excerpt(body string) string {
	s := strings.TrimSpace(body)
	if utf8.RuneCountInString(s) <= excerptLen/2 {
		return s
	}
	runes := []rune(s)
	cut := string(runes[:excerptLen/2])
	if i := strings.LastIndexFunc(cut, unicode.IsSpace); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…"
}

// --- storage ----------------------------------------------------------------

const docSelect = `SELECT d.id, d.household_id, d.path, d.title, d.body, d.tags,
       cu.name, uu.name, d.created_at, d.updated_at
  FROM kb_docs d
  LEFT JOIN users cu ON cu.id = d.created_by
  LEFT JOIN users uu ON uu.id = d.updated_by`

func scanDoc(row pgx.Row) (*Doc, error) {
	var d Doc
	if err := row.Scan(&d.ID, &d.HouseholdID, &d.Path, &d.Title, &d.Body, &d.Tags,
		&d.CreatedName, &d.UpdatedName, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	return &d, nil
}

// getDoc returns the live doc at path, or pgx.ErrNoRows.
func getDoc(ctx context.Context, q querier, householdID uuid.UUID, path string) (*Doc, error) {
	return scanDoc(q.QueryRow(ctx,
		docSelect+` WHERE d.household_id = $1 AND d.path = $2 AND d.deleted_at IS NULL`, householdID, path))
}

// getDocByID returns the live doc with this id, or pgx.ErrNoRows.
func getDocByID(ctx context.Context, q querier, householdID, id uuid.UUID) (*Doc, error) {
	return scanDoc(q.QueryRow(ctx,
		docSelect+` WHERE d.household_id = $1 AND d.id = $2 AND d.deleted_at IS NULL`, householdID, id))
}

// querier is what the pool offers.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// upsert writes the doc at path, replacing an existing one, and reports
// whether it was created (xmax = 0: a row this statement inserted).
func upsert(ctx context.Context, pool *pgxpool.Pool, householdID uuid.UUID, path, title, body string, tags []string, by *uuid.UUID) (*Doc, bool, error) {
	var created bool
	err := pool.QueryRow(ctx,
		`INSERT INTO kb_docs (household_id, path, title, body, tags, created_by, updated_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $6)
		 ON CONFLICT (household_id, path) DO UPDATE
		   SET title = EXCLUDED.title,
		       body = EXCLUDED.body,
		       tags = EXCLUDED.tags,
		       updated_by = EXCLUDED.updated_by,
		       deleted_at = NULL
		 RETURNING (xmax = 0)`,
		householdID, path, title, body, tags, by).Scan(&created)
	if err != nil {
		return nil, false, err
	}
	saved, err := getDoc(ctx, pool, householdID, path)
	if err != nil {
		return nil, false, err
	}
	return saved, created, nil
}

// --- arguments --------------------------------------------------------------

// argString reads a string argument, accepting the aliases small models use.
func argString(req mcp.CallToolRequest, names ...string) string {
	for _, n := range names {
		if v, ok := req.GetArguments()[n]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

// argStringList reads a string array argument.
func argStringList(req mcp.CallToolRequest, names ...string) []string {
	for _, n := range names {
		v, ok := req.GetArguments()[n]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case []string:
			return t
		case []any:
			out := make([]string, 0, len(t))
			for _, e := range t {
				if s, ok := e.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return nil
}

// argLimit reads a limit argument, clamped to [1, maxLimit].
func argLimit(req mcp.CallToolRequest) int {
	if v, ok := req.GetArguments()["limit"]; ok {
		switch t := v.(type) {
		case float64:
			if int(t) > 0 {
				return min(int(t), maxLimit)
			}
		case int:
			if t > 0 {
				return min(t, maxLimit)
			}
		case json.Number:
			if n, err := t.Int64(); err == nil && n > 0 {
				return min(int(n), maxLimit)
			}
		}
	}
	return defaultLimit
}

// annotations builds the four catalog hints every tool carries.
func annotations(title string, readOnly, destructive, idempotent bool) []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithTitleAnnotation(title),
		mcp.WithReadOnlyHintAnnotation(readOnly),
		mcp.WithDestructiveHintAnnotation(destructive),
		mcp.WithIdempotentHintAnnotation(idempotent),
		mcp.WithOpenWorldHintAnnotation(false),
	}
}
