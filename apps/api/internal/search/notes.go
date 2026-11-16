package search

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/auth"
)

// noteEntityType is the entity_type of note documents ("note-<uuid>") in
// household_search.
const noteEntityType = "note"

// NoteChecker answers which of a household's note ids the role may see.
// The index stores a note's text but not what it is attached to, so a note
// on a credential, or on a file linked from one, would otherwise be found
// by its text (#4798). The check runs against Postgres, the source of
// truth.
type NoteChecker interface {
	VisibleNoteIDs(ctx context.Context, householdID, role string, ids []string) (map[string]bool, error)
}

// NoteRepo is the Postgres NoteChecker. It reuses the existing rules rather
// than restating them:
//   - role_can_see_entity() (migration 061, the SQL copy of
//     auth.CanSeeEntity) for notes themselves and for the parent's kind, so
//     a viewer, housesitter or vendor gets no note on a secret, and a
//     housesitter none on a bill;
//   - the credential-file rule (file.Repo.VisibleFileIDs, secretFileWhere)
//     for notes on files, for roles without secrets read: a viewer gets no
//     note on a file attached to or linked from a credential.
//
// A note deleted while Typesense was down is not in the result either.
type NoteRepo struct {
	pool  *pgxpool.Pool
	files FileChecker // nil: notes on files are hidden from roles without secrets read
}

// NewNoteRepo returns a NoteChecker on pool. files (normally *file.Repo)
// answers the credential-file rule; nil hides every note on a file from
// roles without secrets read (fail closed).
func NewNoteRepo(pool *pgxpool.Pool, files FileChecker) *NoteRepo {
	return &NoteRepo{pool: pool, files: files}
}

// VisibleNoteIDs returns which of ids are notes of the household that role
// may see, by their parent. Ids that are not UUIDs are ignored.
func (r *NoteRepo) VisibleNoteIDs(ctx context.Context, householdID, role string, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	hid, err := uuid.Parse(householdID)
	if err != nil {
		return out, fmt.Errorf("visible notes: household id: %w", err)
	}
	uids := make([]uuid.UUID, 0, len(ids))
	for _, s := range ids {
		if u, err := uuid.Parse(s); err == nil {
			uids = append(uids, u)
		}
	}
	if len(uids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT n.id::text, n.entity_type::text, n.entity_id::text FROM notes n
		 WHERE n.household_id = $1 AND n.id = ANY($2)
		   AND role_can_see_entity($3, 'note')
		   AND role_can_see_entity($3, n.entity_type::text)`,
		hid, uids, role)
	if err != nil {
		return out, fmt.Errorf("visible notes: %w", err)
	}
	type noteParent struct{ id, kind, parent string }
	var notes []noteParent
	for rows.Next() {
		var n noteParent
		if err := rows.Scan(&n.id, &n.kind, &n.parent); err != nil {
			rows.Close()
			return out, fmt.Errorf("visible notes: scan: %w", err)
		}
		notes = append(notes, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("visible notes: %w", err)
	}

	// Notes on files: the credential-file rule, for roles without secrets
	// read. role_can_see_entity already let 'file' through for them.
	fileOK := map[string]bool{}
	checkFiles := !auth.Can(role, auth.ResSecrets, auth.ActRead)
	if checkFiles && r.files != nil {
		var fileIDs []string
		for _, n := range notes {
			if n.kind == "file" {
				fileIDs = append(fileIDs, n.parent)
			}
		}
		if len(fileIDs) > 0 {
			if fileOK, err = r.files.VisibleFileIDs(ctx, householdID, fileIDs, false); err != nil {
				return map[string]bool{}, fmt.Errorf("visible notes: files: %w", err)
			}
		}
	}
	for _, n := range notes {
		if checkFiles && n.kind == "file" && !fileOK[n.parent] {
			continue
		}
		out[n.id] = true
	}
	return out, nil
}

// checkNotes drops note hits whose note is gone or whose parent the role
// may not see. Without a NoteChecker, or when the check fails, every note
// hit is dropped (fail closed) and the rest are returned.
func (h *Handler) checkNotes(ctx context.Context, householdID, role string, results []SearchResult) []SearchResult {
	var ids []string
	for _, res := range results {
		if res.EntityType == noteEntityType {
			ids = append(ids, res.EntityID)
		}
	}
	if len(ids) == 0 {
		return results
	}
	ok := map[string]bool{}
	if h.notes != nil {
		var err error
		if ok, err = h.notes.VisibleNoteIDs(ctx, householdID, role, ids); err != nil {
			slog.Warn("search: note visibility check failed, omitting note hits", "error", err)
			ok = map[string]bool{}
		}
	}
	out := results[:0]
	for _, res := range results {
		if res.EntityType != noteEntityType || ok[res.EntityID] {
			out = append(out, res)
		}
	}
	return out
}
