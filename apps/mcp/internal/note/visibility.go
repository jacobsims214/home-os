package note

// Note visibility by role (#4881).
//
// A note is derived data: it quotes the record it is attached to ("passport
// number 123" on a credential, "the renewal is late" on a bill). The role
// policy (KB decisions/2026-11-15-role-policy.md) therefore says a note is
// visible exactly when its PARENT is: a note the role may not see is treated
// as absent — omitted from note_find and "not found" on note_get — never
// returned with fields stripped.
//
// The API closed this for /api/v1/notes in #4775/#4798
// (apps/api/internal/search/notes.go VisibleNoteIDs) and search_all's note
// area applies the same rule (apps/mcp/internal/search/tools.go). This file
// is the third caller of that rule, not a restatement of it:
//
//   - role_can_see_entity($role, 'note') AND role_can_see_entity($role,
//     n.entity_type::text) — migration 061, the SQL copy of
//     auth.CanSeeEntity. A vendor gets no note at all (notes read is
//     rolesSitter); a housesitter none on a bill, a family note or a
//     credential; a viewer none on a credential.
//   - for roles without secrets read, the documents' credential rule: no
//     note on a secret, and none on a file attached to a credential or
//     linked from one (file.SecretFileSQL, shared with search_all so the two
//     cannot drift; #4478/#4775).
//
// It lives in one helper, visibleNotesSQL, which both read tools call, so
// the two cannot disagree and a later tool cannot quietly skip the rule. The
// caller's role is re-read from memberships on every call (readerOf, the
// shape apps/mcp/internal/file/access.go callerOf uses), so a demotion takes
// effect at once.

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/file"
	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// querier is what the pool and a transaction offer; the read tools take the
// pool, and tests may pass either.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// reader is the note tools' caller: their household and the role re-read
// from memberships.
type reader struct {
	householdID uuid.UUID
	role        string
}

// seesSecrets reports whether the role may read credentials, which decides
// whether the credential-file rule applies at all (a family role sees every
// note). Ask the policy table, never a list of role names.
func (r reader) seesSecrets() bool {
	return auth.Can(r.role, auth.ResSecrets, auth.ActRead)
}

// visibleNotesSQL is the condition, for a note aliased n, that the reader
// may see it. roleArg is the SQL expression for the reader's current role
// (e.g. "$7::text") and secretsArg the boolean expression that is true when
// the role may read credentials (e.g. "$8::boolean"); both are bound
// parameters, so the role never becomes SQL text.
func visibleNotesSQL(roleArg, secretsArg string) string {
	return `role_can_see_entity(` + roleArg + `, 'note')
		   AND role_can_see_entity(` + roleArg + `, n.entity_type::text)
		   AND (` + secretsArg + `
		        OR (n.entity_type::text <> 'secret'
		            AND NOT (n.entity_type::text = 'file' AND ` + file.SecretFileSQL("n.entity_id") + `)))`
}

// readerOf resolves the caller's current role the way the file, vehicle and
// loan tools do: from memberships, for the claims' user and household, on
// every call. A non-nil result is the isError to return.
func readerOf(ctx context.Context, q querier) (reader, *mcp.CallToolResult) {
	c := server.ClaimsFromContext(ctx)
	if c == nil {
		return reader{}, notSignedIn()
	}
	hid, err := uuid.Parse(c.HouseholdID)
	if err != nil {
		return reader{}, notSignedIn()
	}
	out := reader{householdID: hid, role: c.Role}
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.AgentTokenID != "" { // an agent token always resolves to a users.id
			return reader{}, notSignedIn()
		}
		// Local-dev claims without a user: keep the claims' role, as long
		// as the household exists.
		var ok bool
		err = q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM households WHERE id = $1)`, hid).Scan(&ok)
		if err == nil && !ok {
			return reader{}, notSignedIn()
		}
	} else {
		err = q.QueryRow(ctx, `SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
			hid, uid).Scan(&out.role)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return reader{}, notSignedIn()
	}
	if err != nil {
		return reader{}, respond.Failed("check your access", err)
	}
	return out, nil
}
