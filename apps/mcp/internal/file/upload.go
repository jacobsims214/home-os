package file

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// uploadDedupeWindow is how far back document_upload looks for the same file
// (catalog v2 loop guard).
const uploadDedupeWindow = "10 minutes"

// dataURL matches a "data:<type>;base64," prefix some clients send.
var dataURL = regexp.MustCompile(`^data:([^;,]*)(;[^,]*)?;base64,`)

// billWords suggest a document is a bill to pay.
var billWords = []string{"invoice", "bill", "statement", "amount due", "balance due", "payment due"}

// NewDocumentUploadTool creates document_upload (catalog v2, class C;
// replaces upload_file). It files a document from base64 bytes
// (content_base64, hidden alias content) or from plain text (text), with
// optional title, category, tags, person, about link, document date, expiry
// and description. Text uploads are searchable at once (extracted_text is
// the text itself); byte uploads are queued for the worker's text
// extraction. The result echoes the metadata, never the bytes or the text.
//
// Loop guard: the same bytes under the same name about the same thing, filed
// in the last 10 minutes, come back with duplicate:true instead of a second
// row. Files have no author column, so the guard is per household; an
// advisory lock on (household, name) makes parallel repeats safe.
//
// issuer and amount (catalog) have no DB columns yet and are not params.
func NewDocumentUploadTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("document_upload",
		mcp.WithDescription("File a document from base64 bytes or plain text (e.g. a pasted receipt), with title, category and what it's about. Text is extracted for search."),
		mcp.WithString("name",
			mcp.Description("File name with extension, e.g. home-policy-2026.pdf"),
			mcp.Required(),
		),
		mcp.WithString("content_base64",
			mcp.Description("The file's bytes, base64-encoded (25 MB max); or use text"),
		),
		mcp.WithString("content_type",
			mcp.Description("MIME type of content_base64, e.g. application/pdf; guessed if left out"),
		),
		mcp.WithString("text",
			mcp.Description("Plain text to file instead of bytes, e.g. a pasted receipt"),
		),
		mcp.WithString("title",
			mcp.Description("Human title, e.g. 2026 homeowners policy (default: the file name)"),
		),
		mcp.WithString("category",
			mcp.Description("Kind of document (default other), e.g. insurance"),
			mcp.Enum(categories...),
		),
		mcp.WithArray("tags",
			mcp.Description("Short labels to find it by, e.g. [\"taxes-2026\"]"),
			mcp.WithStringItems(),
		),
		mcp.WithString("person_id",
			mcp.Description("Family member it is for: id or exact name, e.g. Nora"),
		),
		mcp.WithString("about_type",
			mcp.Description("What it's about (default family); others need about_id"),
			mcp.Enum(aboutTypes...),
		),
		mcp.WithString("about_id",
			mcp.Description("Id (UUID) of the vehicle, property, asset, ... it's about"),
		),
		mcp.WithString("doc_date",
			mcp.Description("Date on the document, YYYY-MM-DD, e.g. 2026-03-01"),
		),
		mcp.WithString("expires_on",
			mcp.Description("When it lapses, YYYY-MM-DD, e.g. 2027-03-01"),
		),
		mcp.WithString("description",
			mcp.Description("Free-text notes about the document"),
		),
		mcp.WithTitleAnnotation("Upload document"),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.Schema[documentData](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		householdID, ok := householdOf(ctx)
		if !ok {
			return notSignedIn(), nil
		}
		args := req.GetArguments()
		example := map[string]any{"name": "costco-receipt.txt", "text": "Costco 2026-03-01 Total $142.50", "category": "receipt"}

		// --- name and content ---------------------------------------------
		name := str(args, "name")
		if name == "" {
			return respond.Invalid("name", "name is required.",
				"Pass a file name with an extension, e.g. receipt.pdf.", example), nil
		}
		if n := utf8.RuneCountInString(name); n > maxNameRunes {
			return respond.Invalid("name", fmt.Sprintf("name is %d characters; the limit is %d.", n, maxNameRunes),
				"Use a shorter file name and put the rest in title.", example), nil
		}
		b64, _ := args["content_base64"].(string)
		if strings.TrimSpace(b64) == "" {
			b64, _ = args["content"].(string) // hidden alias (upload_file)
		}
		b64 = strings.TrimSpace(b64)
		text, _ := args["text"].(string)
		hasText := strings.TrimSpace(text) != ""
		switch {
		case b64 != "" && hasText:
			return respond.Invalid("text", "content_base64 and text were both given.",
				"Pass either content_base64 (a file's bytes) or text (plain text), not both.", example), nil
		case b64 == "" && !hasText:
			return respond.Invalid("content_base64", "The document has no content.",
				"Pass the file's bytes as content_base64, or plain text as text.", example), nil
		}

		contentType := str(args, "content_type")
		var data []byte
		var extracted *string
		ocrStatus := "pending"
		if hasText {
			text = strings.ReplaceAll(text, "\x00", "")
			data = []byte(text)
			extracted = &text
			ocrStatus = "done"
			if contentType == "" {
				contentType = "text/plain; charset=utf-8"
			}
		} else {
			if m := dataURL.FindStringSubmatch(b64); m != nil {
				if contentType == "" {
					contentType = m[1]
				}
				b64 = b64[len(m[0]):]
			}
			var ok bool
			if data, ok = decodeBase64(b64); !ok {
				return respond.Invalid("content_base64", "content_base64 is not valid base64.",
					"Send the file's bytes base64-encoded (standard alphabet), or send plain text as text instead.",
					map[string]any{"name": "note.txt", "content_base64": "SGVsbG8=", "content_type": "text/plain"}), nil
			}
			if len(data) == 0 {
				return respond.Invalid("content_base64", "content_base64 decodes to an empty file.",
					"Send the file's bytes base64-encoded.", example), nil
			}
			if contentType == "" {
				contentType = mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
			}
			if contentType == "" {
				contentType = http.DetectContentType(data)
			}
		}
		if len(data) > maxUploadBytes {
			return respond.Invalid("content_base64",
				fmt.Sprintf("The file is %s; the limit is 25 MB.", humanSize(int64(len(data)))),
				"Upload a smaller file, or split it into parts.", example), nil
		}
		if utf8.RuneCountInString(contentType) > maxNameRunes {
			return respond.Invalid("content_type", "content_type is too long.",
				"Pass a MIME type such as application/pdf, or leave it out.", example), nil
		}

		// --- metadata -----------------------------------------------------
		var title *string
		if t := str(args, "title"); t != "" {
			title = &t
		}
		category := "other"
		if raw := str(args, "category"); raw != "" {
			c, ok := parseCategory(raw)
			if !ok {
				return badCategory(raw, example), nil
			}
			category = c
		}
		tags, ok := stringList(args, "tags")
		if !ok {
			return respond.Invalid("tags", "tags must be a list of strings.",
				"Pass tags as a list, e.g. [\"taxes-2026\", \"car\"].", map[string]any{"tags": []string{"taxes-2026"}}), nil
		}
		if len(tags) > maxTags {
			return respond.Invalid("tags", fmt.Sprintf("%d tags were given; the limit is %d.", len(tags), maxTags),
				"Keep the few labels that matter.", map[string]any{"tags": []string{"taxes-2026"}}), nil
		}
		docDate, rawDoc, ok := parseDate(args, "doc_date", "document_date")
		if !ok {
			return badDate("doc_date", rawDoc, example), nil
		}
		expires, rawExp, ok := parseDate(args, "expires_on")
		if !ok {
			return badDate("expires_on", rawExp, example), nil
		}
		if docDate != nil && expires != nil && expires.Before(*docDate) {
			return respond.Invalid("expires_on", fmt.Sprintf("expires_on %s is before doc_date %s.", rawExp, rawDoc),
				"Pass an expiry on or after the document date.", map[string]any{"doc_date": rawDoc, "expires_on": rawDoc}), nil
		}
		var description *string
		if d := str(args, "description"); d != "" {
			description = &d
		}
		aboutExample := map[string]any{"name": "policy.pdf", "text": "…", "about_type": "vehicle",
			"about_id": "9a1c2b3d-4e5f-4a6b-8c7d-0e1f2a3b4c5d"}
		a, bad := parseAbout(args, true, aboutExample)
		if bad != nil {
			return bad, nil
		}
		if a.Type == "" || a.Type == "family" {
			a = about{Type: "family", Kind: "household", ID: householdID}
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("file the document", err), nil
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

		var personID *uuid.UUID
		if ref := str(args, "person_id"); ref != "" {
			id, _, bad, err := resolvePerson(ctx, tx, householdID, ref, map[string]any{"name": "report.pdf", "text": "…", "person_id": "Nora"})
			if err != nil {
				return respond.Failed("file the document", err), nil
			}
			if bad != nil {
				return bad, nil
			}
			personID = &id
		}

		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			"document_upload:"+householdID.String()+":"+strings.ToLower(name)); err != nil {
			return respond.Failed("file the document", err), nil
		}

		// Loop guard: the same bytes, name and subject filed minutes ago.
		var dupID uuid.UUID
		err = tx.QueryRow(ctx,
			`SELECT f.id FROM files f JOIN file_blobs b ON b.id = f.blob_id
			 WHERE f.household_id = $1 AND lower(f.name) = lower($2) AND f.size = $3
			   AND f.entity_type::text = $4 AND f.entity_id = $5
			   AND f.created_at > NOW() - $6::interval AND b.data = $7
			 ORDER BY f.created_at DESC LIMIT 1`,
			householdID, name, len(data), a.Kind, a.ID, uploadDedupeWindow, data,
		).Scan(&dupID)
		switch {
		case err == nil:
			if err := tx.Commit(ctx); err != nil {
				return respond.Failed("file the document", err), nil
			}
			r, err := loadDocument(ctx, pool, dupID, householdID)
			if err != nil {
				return respond.Failed("file the document", err), nil
			}
			d := r.data()
			d.Duplicate = true
			return respond.OK(fmt.Sprintf("You already filed %s in the last 10 minutes; returned it instead of filing a copy.", r.describe()), d,
				fmt.Sprintf("Read it with document_get document_id=%s.", d.ID)), nil
		case !errors.Is(err, pgx.ErrNoRows):
			return respond.Failed("file the document", err), nil
		}

		var blobID uuid.UUID
		if err := tx.QueryRow(ctx,
			`INSERT INTO file_blobs (data, size) VALUES ($1, $2) RETURNING id`, data, len(data),
		).Scan(&blobID); err != nil {
			return respond.Failed("file the document", err), nil
		}
		var fileID uuid.UUID
		err = tx.QueryRow(ctx,
			`INSERT INTO files (household_id, blob_id, name, content_type, size, entity_type, entity_id,
			                    extracted_text, ocr_status, tags, title, category, document_date, expires_on,
			                    description, person_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
			 RETURNING id`,
			householdID, blobID, name, contentType, len(data), a.Kind, a.ID,
			extracted, ocrStatus, tags, title, category, docDate, expires, description, personID,
		).Scan(&fileID)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch {
			case pgErr.Code == "23503" && pgErr.ConstraintName == "files_person_fkey":
				return respond.Invalid("person_id", "That person is not in this family.",
					"Find people with person_find.", map[string]any{"person_id": "Nora"}), nil
			case pgErr.Code == "23503": // enforce_entity_owner: missing or another family's row
				return respond.Invalid("about_id",
					fmt.Sprintf("No %s with id %s was found in this family.", a.Type, a.ID),
					fmt.Sprintf("Check the %s's id (%s), or leave about_type and about_id out to file it for the family.", a.Type, findIDWith(a.Type)),
					aboutExample), nil
			case pgErr.Code == "23514" && pgErr.ConstraintName == "entity_kind_check":
				return respond.Invalid("about_type",
					fmt.Sprintf("Documents about a %s can't be filed yet.", a.Type),
					fmt.Sprintf("Leave about_type and about_id out to file it for the family, and name the %s in the title.", a.Type),
					example), nil
			}
		}
		if err != nil {
			return respond.Failed("file the document", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("file the document", err), nil
		}

		r, err := loadDocument(ctx, pool, fileID, householdID)
		if err != nil {
			return respond.Failed("read back the document", err), nil
		}
		summary := fmt.Sprintf("Filed %s, %s.", r.describe(), humanSize(r.Size))
		if hasText {
			summary += " Its text is searchable now."
		} else {
			summary += " Text extraction runs in the background; document_find matches its text once done."
		}
		next := []string{fmt.Sprintf("Read it back with document_get document_id=%s.", r.ID)}
		if looksLikeBill(name, r.Title, text, category) {
			next = append(next, "If this is a bill to pay, add it with bill_create.")
		}
		if h := r.aboutHint(); h != "" {
			next = append(next, h)
		}
		return respond.OK(summary, r.data(), next...), nil
	}

	return "document_upload", tool, handler
}

// decodeBase64 decodes standard or URL-safe base64, padded or not, ignoring
// whitespace and line breaks.
func decodeBase64(s string) ([]byte, bool) {
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, true
		}
	}
	return nil, false
}

// looksLikeBill is true when the name, title or text reads like a bill.
func looksLikeBill(name string, title *string, text, category string) bool {
	if category != "other" && category != "home" && category != "vehicle" {
		return false
	}
	hay := strings.ToLower(name + " " + text)
	if title != nil {
		hay += " " + strings.ToLower(*title)
	}
	for _, w := range billWords {
		if strings.Contains(hay, w) {
			return true
		}
	}
	return false
}
