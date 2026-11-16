package file

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// documentDeleted is what document_delete returns: the deleted document's
// metadata (never its text or bytes).
type documentDeleted struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	FileName     string  `json:"file_name"`
	Category     string  `json:"category"`
	AboutType    string  `json:"about_type"`
	AboutID      *string `json:"about_id"`
	SizeBytes    int64   `json:"size_bytes"`
	LinksRemoved int     `json:"links_removed" jsonschema_description:"Extra links (document_links) removed with it."`
}

// NewDocumentDeleteTool creates document_delete (catalog v2, class X). It
// deletes the files row in one transaction with the document_links rows that
// point at it; 038's trigger deletes the blob when no other file shares it,
// and 053's trigger queues the file for removal from the worker's files
// collection. After the commit the metadata documents and text chunks are
// also deleted from Typesense directly. A missing, other-family or
// already-deleted id is not found and deletes nothing, so a repeat is safe.
func NewDocumentDeleteTool(pool *pgxpool.Pool, ts *Typesense) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("document_delete",
		mcp.WithDescription("Delete a document and its stored file. This can't be undone."),
		mcp.WithString("document_id",
			mcp.Description("The document's id (UUID), e.g. from document_find"),
			mcp.Required(),
		),
		mcp.WithTitleAnnotation("Delete document"),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.Schema[documentDeleted](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := writerOf(ctx, pool, "document_delete")
		if bad != nil {
			return bad, nil
		}
		args := req.GetArguments()
		example := map[string]any{"document_id": exampleDocumentID}
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

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("delete the document", err), nil
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		r, err := loadVisible(ctx, tx, c, id, true)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("document", raw, "Call document_find to find the document_id; it may already be deleted."), nil
		}
		if err != nil {
			return respond.Failed("delete the document", err), nil
		}
		links, err := tx.Exec(ctx,
			`DELETE FROM document_links WHERE household_id = $1 AND link_type = 'file' AND link_id = $2`,
			c.householdID, id.String())
		if err != nil {
			return respond.Failed("delete the document", err), nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM files WHERE id = $1 AND household_id = $2`, id, c.householdID); err != nil {
			return respond.Failed("delete the document", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("delete the document", err), nil
		}
		ts.removeDocument(ctx, id.String())

		aboutType, aboutID := r.about()
		d := documentDeleted{
			ID: r.ID.String(), Title: r.displayTitle(), FileName: r.Name, Category: r.Category,
			AboutType: aboutType, AboutID: aboutID, SizeBytes: r.Size, LinksRemoved: int(links.RowsAffected()),
		}
		return respond.OK(fmt.Sprintf("Deleted document %s.", r.describe()), d,
			"File a new one with document_upload."), nil
	}

	return "document_delete", tool, handler
}
