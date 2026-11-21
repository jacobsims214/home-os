// Package kb is the family's own knowledge base (migration 076, #5502):
// durable, household-scoped knowledge a person OR an agent writes once and
// anyone can find later.
//
// A doc is addressed by PATH, not by id: 'how-to/networking/router-reset',
// 'pets/vet-after-hours', 'sitters/what-the-babysitter-needs-to-know'. The
// path is the family's own namespace and UNIQUE (household_id, path), so a
// doc has exactly ONE home and a write REPLACES it instead of forking a
// second copy under a slightly different title.
//
// It is distinct from the two things it is often confused with:
//
//   - a NOTE is transient and attached to one record ("the boiler is noisy");
//   - a FILE is an uploaded document (a PDF of the warranty).
//
// A KB doc is the durable "how we do this here" that outlives both, and it is
// written to be FOUND: it is indexed into household_search (see
// internal/searchrecon, the one mapping from a Postgres row to its index
// document) so "what do we do if the dog is sick at night?" finds
// how-to/pets/vet-after-hours by meaning.
//
// Roles follow the one policy table (internal/auth/roles.go, ResKB): every
// household member reads the KB — a housesitter included, because the
// babysitter needs the router doc — and writes are the family roles.
//
// No secrets belong in a doc. A credential is stored in the secrets feature
// and REFERRED to here ("the wifi password is in the vault"), never copied.
package kb

import (
	"context"
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
)

// EntityType is a KB doc's entity_type in household_search. It is also its
// kind in the role policy's entity registry (auth.ResKB).
const EntityType = "kb_doc"

// Limits on what a caller may save. Body and title match guides so an agent
// that learned one set of limits has learned both.
const (
	MaxPath   = 512
	MaxTitle  = 255
	MaxBody   = 256 << 10 // 256 KiB of markdown
	MaxTags   = 30
	MaxTagLen = 50
	// MaxQuery bounds a search query.
	MaxQuery = 500
)

// ErrNotFound: no live doc at that path in this household.
var ErrNotFound = errors.New("not found")

// ValidationError is a message safe to show the caller (400).
type ValidationError string

func (e ValidationError) Error() string { return string(e) }

// Doc is a row of kb_docs with its authors' display names.
type Doc struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	Path        string
	Title       string
	Body        string
	Tags        []string
	CreatedBy   *uuid.UUID
	CreatedName *string
	UpdatedBy   *uuid.UUID
	UpdatedName *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Summary is one list/search row: everything a caller needs to decide whether
// to read the doc, without its body.
type Summary struct {
	Path        string
	Title       string
	Tags        []string
	UpdatedAt   time.Time
	UpdatedName *string
	// Excerpt is the opening of the body, or the matching region for a
	// search hit.
	Excerpt string
}

// Repo is the kb_docs data access layer. Every query is household-scoped, so
// a path from another family behaves exactly like a path that does not exist.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a KB repo.
func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

const docCols = `d.id, d.household_id, d.path, d.title, d.body, d.tags,
	d.created_by, cu.name, d.updated_by, uu.name, d.created_at, d.updated_at`

// docFrom is the joined FROM the author names come from.
const docFrom = `kb_docs d
	LEFT JOIN users cu ON cu.id = d.created_by
	LEFT JOIN users uu ON uu.id = d.updated_by`

func scanDoc(row pgx.Row) (*Doc, error) {
	var d Doc
	if err := row.Scan(&d.ID, &d.HouseholdID, &d.Path, &d.Title, &d.Body, &d.Tags,
		&d.CreatedBy, &d.CreatedName, &d.UpdatedBy, &d.UpdatedName, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	return &d, nil
}

// Get returns the live doc at path.
func (r *Repo) Get(ctx context.Context, householdID uuid.UUID, path string) (*Doc, error) {
	d, err := scanDoc(r.pool.QueryRow(ctx,
		`SELECT `+docCols+` FROM `+docFrom+`
		 WHERE d.household_id = $1 AND d.path = $2 AND d.deleted_at IS NULL`, householdID, path))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("kb: get %q: %w", path, err)
	}
	return d, nil
}

// GetByID returns the live doc with this id. A search hit carries the doc's
// id, and this is how the handler turns it back into the row Postgres holds:
// a doc soft-deleted while the index was stale resolves to ErrNotFound rather
// than resurfacing from the index.
func (r *Repo) GetByID(ctx context.Context, householdID, id uuid.UUID) (*Doc, error) {
	d, err := scanDoc(r.pool.QueryRow(ctx,
		`SELECT `+docCols+` FROM `+docFrom+`
		 WHERE d.household_id = $1 AND d.id = $2 AND d.deleted_at IS NULL`, householdID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("kb: get %s: %w", id, err)
	}
	return d, nil
}

// SearchByWord is the no-index fallback for a search request: a
// case-insensitive match over the path and title. It is deliberately not the
// primary path — the whole point of the feature is finding a doc by meaning
// (see the handler's searchDocs) — but the endpoint must still work in a
// deployment with no Typesense.
func (r *Repo) SearchByWord(ctx context.Context, householdID uuid.UUID, q string) ([]Summary, error) {
	like := "%" + strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(q), `\`, `\\`), "%", `\%`) + "%"
	rows, err := r.pool.Query(ctx,
		`SELECT d.path, d.title, d.tags, d.updated_at, uu.name, left(d.body, 400)
		   FROM `+docFrom+`
		  WHERE d.household_id = $1 AND d.deleted_at IS NULL
		    AND (d.path ILIKE $2 OR d.title ILIKE $2)
		  ORDER BY d.path
		  LIMIT 20`, householdID, like)
	if err != nil {
		return nil, fmt.Errorf("kb: search by word: %w", err)
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var s Summary
		if err := rows.Scan(&s.Path, &s.Title, &s.Tags, &s.UpdatedAt, &s.UpdatedName, &s.Excerpt); err != nil {
			return nil, fmt.Errorf("kb: search by word scan: %w", err)
		}
		s.Excerpt = Excerpt(s.Excerpt)
		out = append(out, s)
	}
	return out, rows.Err()
}

// List returns the household's live docs' paths and titles, alphabetically by
// path. prefix, when set, keeps docs under that namespace
// ('how-to/networking' matches 'how-to/networking/router-reset' but not
// 'how-to/networking-old/x').
func (r *Repo) List(ctx context.Context, householdID uuid.UUID, prefix string) ([]Summary, error) {
	q := `SELECT d.path, d.title, d.tags, d.updated_at, uu.name, left(d.body, 400)
	      FROM ` + docFrom + `
	      WHERE d.household_id = $1 AND d.deleted_at IS NULL`
	args := []any{householdID}
	if prefix != "" {
		// A namespace prefix matches the path itself and anything below it,
		// but not a sibling that merely starts with the same letters.
		args = append(args, prefix, prefix+"/%")
		q += ` AND (d.path = $2 OR d.path LIKE $3)`
	}
	q += ` ORDER BY d.path`
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("kb: list: %w", err)
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var s Summary
		if err := rows.Scan(&s.Path, &s.Title, &s.Tags, &s.UpdatedAt, &s.UpdatedName, &s.Excerpt); err != nil {
			return nil, fmt.Errorf("kb: list scan: %w", err)
		}
		if s.Tags == nil {
			s.Tags = []string{}
		}
		s.Excerpt = Excerpt(s.Excerpt)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Upsert creates the doc at path, or REPLACES its title, body and tags when
// it already exists. created_by is kept on an update; updated_by always
// records the writer. A soft-deleted doc at that path is revived as a new
// doc: its body is replaced and deleted_at cleared, so a path always names
// one doc and history never forks.
//
// It returns the saved doc and whether it was created (true) or replaced
// (false), which is what the tool and the API report back.
//
// created is decided in the same statement: Postgres sets xmax = 0 on a row
// this statement INSERTED and non-zero on one it UPDATEd, so two racing
// writers cannot both believe they created it.
func (r *Repo) Upsert(ctx context.Context, householdID uuid.UUID, d *Doc, by *uuid.UUID) (*Doc, bool, error) {
	// The author names are not available in RETURNING, so the row is written
	// here and read back through Get, which joins them.
	var created bool
	err := r.pool.QueryRow(ctx,
		`INSERT INTO kb_docs (household_id, path, title, body, tags, created_by, updated_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $6)
		 ON CONFLICT (household_id, path) DO UPDATE
		   SET title = EXCLUDED.title,
		       body = EXCLUDED.body,
		       tags = EXCLUDED.tags,
		       updated_by = EXCLUDED.updated_by,
		       -- A doc written again after a soft delete comes back as live.
		       deleted_at = NULL
		 RETURNING (xmax = 0)`,
		householdID, d.Path, d.Title, d.Body, d.Tags, by).Scan(&created)
	if err != nil {
		return nil, false, fmt.Errorf("kb: upsert %q: %w", d.Path, err)
	}
	saved, err := r.Get(ctx, householdID, d.Path)
	if err != nil {
		return nil, false, err
	}
	return saved, created, nil
}

// Delete soft-deletes the live doc at path. It reports ErrNotFound when there
// is no live doc there.
func (r *Repo) Delete(ctx context.Context, householdID uuid.UUID, path string, by *uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE kb_docs SET deleted_at = NOW(), updated_by = $3
		 WHERE household_id = $1 AND path = $2 AND deleted_at IS NULL`, householdID, path, by)
	if err != nil {
		return fmt.Errorf("kb: delete %q: %w", path, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AllInHousehold calls fn for every live doc of one household, for
// searchrecon (#5298: the index is derived from Postgres, never from the
// write path).
func (r *Repo) AllInHousehold(ctx context.Context, householdID string, fn func(*Doc) error) error {
	rows, err := r.pool.Query(ctx,
		`SELECT `+docCols+` FROM `+docFrom+`
		 WHERE d.household_id = $1::uuid AND d.deleted_at IS NULL
		 ORDER BY d.path`, householdID)
	if err != nil {
		return fmt.Errorf("kb: all in household: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return fmt.Errorf("kb: all in household scan: %w", err)
		}
		if err := fn(d); err != nil {
			return err
		}
	}
	return rows.Err()
}

// --- validation -------------------------------------------------------------

// pathSegment matches one segment of a path: lower-case letters, digits, and
// '.', '_' or '-' inside a segment. It is the same rule migration 076's CHECK
// enforces, restated so a bad path is a clear 400 rather than a 500 from the
// database.
var pathSegment = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// CleanPath validates and normalizes a doc path. A path is a namespaced slug:
// lower-case, '/' between segments, no leading/trailing/doubled slash and no
// '..'. Upper case, spaces and '_' in the wrong place are corrected where the
// intent is unambiguous: 'How To/Router Reset' becomes
// 'how-to/router-reset'.
func CleanPath(in string) (string, error) {
	p := strings.TrimSpace(in)
	p = strings.Trim(p, "/")
	if p == "" {
		return "", ValidationError("path is required, e.g. how-to/networking/router-reset")
	}
	if utf8.RuneCountInString(p) > MaxPath {
		return "", ValidationError("path must be at most 512 characters")
	}
	segments := strings.Split(p, "/")
	out := make([]string, 0, len(segments))
	for _, s := range segments {
		s = slugSegment(s)
		if s == "" || s == "." || s == ".." {
			return "", ValidationError(fmt.Sprintf("path %q has an empty or invalid segment", in))
		}
		if !pathSegment.MatchString(s) {
			return "", ValidationError(fmt.Sprintf("path %q must be lower-case letters, digits, '-', '_' or '.', separated by '/'", in))
		}
		out = append(out, s)
	}
	joined := strings.Join(out, "/")
	if len(joined) > MaxPath {
		return "", ValidationError("path must be at most 512 characters")
	}
	return joined, nil
}

// slugSegment lower-cases a segment and turns runs of whitespace into '-'.
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

// CleanTitle trims and checks a title.
func CleanTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ValidationError("title is required")
	}
	if utf8.RuneCountInString(s) > MaxTitle {
		return "", ValidationError("title must be at most 255 characters")
	}
	return s, nil
}

// CheckBody enforces the body limit.
func CheckBody(s string) error {
	if len(s) > MaxBody {
		return ValidationError("body must be at most 256 KiB")
	}
	return nil
}

// CleanTags trims and lower-cases tags, drops blanks and duplicates (keeping
// the first occurrence's order) and enforces the limits.
func CleanTags(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		if utf8.RuneCountInString(t) > MaxTagLen {
			return nil, ValidationError("each tag must be at most 50 characters")
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > MaxTags {
		return nil, ValidationError("at most 30 tags")
	}
	return out, nil
}

// Excerpt shortens a body to its opening, on a word boundary, for a list or
// search row. Markdown headings are kept: "## After hours" is exactly the
// line that tells a reader the doc answers their question.
func Excerpt(body string) string {
	const max = 240
	s := strings.TrimSpace(body)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	cut := string(runes[:max])
	if i := strings.LastIndexFunc(cut, unicode.IsSpace); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…"
}
