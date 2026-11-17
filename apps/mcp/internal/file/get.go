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

// NewDocumentGetTool creates document_get (catalog v2, class R; replaces
// get_file). It returns one document's details, its clean text status, and
// one page of its extracted text: textPageSize characters from text_offset
// (default 0). The text is never returned all at once; the result's
// text.next_offset and next hint name the call for the following page. The
// file's bytes are never returned. A document of another family, or one
// attached to a credential when the caller may not see those (viewer), is
// reported as not found; housesitters and vendors get no documents.
//
// A document whose body is the caller's own pasted text (text_origin raw,
// #4941) also reports text_sha256 and text_verified: the stored text is
// re-hashed and compared with the fingerprint taken when it was filed, so a
// reader can tell "this is the text that was pasted" from "this is a copy
// someone re-typed". Anything a bill's amount is checked against should be a
// raw document with text_verified true.
func NewDocumentGetTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("document_get",
		mcp.WithDescription("Read a document's details and its extracted text one page (4000 characters) at a time, starting at text_offset; never all at once."),
		mcp.WithString("document_id",
			mcp.Description("The document's id (UUID), e.g. from document_find"),
			mcp.Required(),
		),
		mcp.WithNumber("text_offset",
			mcp.Description("Character offset to read text from (default 0; pages of 4000)"),
			mcp.Min(0),
		),
		mcp.WithTitleAnnotation("Get document"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.Schema[documentData](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := readerOf(ctx, pool, "document_get")
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
		offset, given, ok := intArg(args, "text_offset")
		if !ok || (given && offset < 0) {
			return respond.Invalid("text_offset", "text_offset must be a whole number, 0 or more.",
				"Pass text_offset as a number, e.g. 4000, or leave it out to start at the beginning.",
				map[string]any{"document_id": exampleDocumentID, "text_offset": textPageSize}), nil
		}

		var content string
		var hash []byte
		var storedHash string
		r, err := scanDocument(pool.QueryRow(ctx,
			`SELECT `+documentColumns+`, COALESCE(substr(f.extracted_text, $3 + 1, $4), ''),
			        f.text_sha256, encode(sha256(convert_to(COALESCE(f.extracted_text, ''), 'UTF8')), 'hex')`+documentFrom+`
			 WHERE f.id = $1 AND f.household_id = $2 AND `+secretFileWhere(5),
			id, c.householdID, offset, textPageSize, c.seesSecretFiles(),
		), &content, &hash, &storedHash)
		r.StoredHash = storedHash
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("document", raw, "Call document_find to find the document_id."), nil
		}
		if err != nil {
			return respond.Failed("get the document", err), nil
		}
		if offset > 0 && offset >= r.TextChars {
			return respond.Invalid("text_offset",
				fmt.Sprintf("text_offset %d is past the end of the text (%d characters).", offset, r.TextChars),
				"Pass a text_offset below text_chars, or 0 to start over.",
				map[string]any{"document_id": raw, "text_offset": 0}), nil
		}

		d := r.data()
		if d.Links, err = loadLinks(ctx, pool, c, r.ID); err != nil {
			return respond.Failed("get the document", err), nil
		}
		// Provenance (#4941): a raw-text body carries the fingerprint it was
		// filed with, and text_verified says whether the stored text still
		// matches it. Derived text has neither.
		if r.isRawText() && len(hash) > 0 {
			hex := fmt.Sprintf("%x", hash)
			verified := hex == r.StoredHash
			d.TextSHA256, d.TextVerified = &hex, &verified
		}
		returned := len([]rune(content))
		page := &textPage{Content: content, Offset: offset, ReturnedChars: returned, TotalChars: r.TextChars}
		if end := offset + returned; end < r.TextChars {
			page.HasMore = true
			page.NextOffset = &end
		}
		d.Text = page

		summary := "Found document " + r.describe() + "."
		next := []string{}
		switch {
		case d.TextStatus == "done" && r.TextChars > 0:
			summary += fmt.Sprintf(" Showing text characters %d–%d of %d.", offset+1, offset+returned, r.TextChars)
		case d.TextStatus == "done":
			summary += " It has no extracted text."
		default:
			summary += " " + d.TextNote
		}
		if page.HasMore {
			next = append(next, fmt.Sprintf("More text: document_get document_id=%s text_offset=%d.", d.ID, *page.NextOffset))
		}
		if d.TextStatus == "pending" || d.TextStatus == "retrying" {
			next = append(next, fmt.Sprintf("Check again later with document_get document_id=%s.", d.ID))
		}
		if d.TextVerified != nil && !*d.TextVerified {
			next = append(next, "The stored text no longer matches the fingerprint taken when it was filed, "+
				"so it has been changed since; don't treat it as the original.")
		}
		if h := r.aboutHint(); h != "" {
			next = append(next, h)
		}
		if len(next) > 3 {
			next = next[:3]
		}
		return respond.OK(summary, d, next...), nil
	}

	return "document_get", tool, handler
}
