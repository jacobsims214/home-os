// Package file provides MCP tools for managing file storage in Home OS.
// Files are stored natively in PostgreSQL with metadata in the files table
// and binary content in the file_blobs table. All operations are scoped to
// the authenticated household via JWT claims.
package file

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
)

// NewListFilesTool creates the list_files MCP tool.
// It returns file metadata (id, name, content_type, size, ocr_status, created_at)
// for the authenticated household, optionally filtered by entity_type and entity_id.
func NewListFilesTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("list_files",
		mcp.WithDescription("List files for the household, optionally filtered by entity type and entity ID"),
		mcp.WithString("entity_type",
			mcp.Description("Optional entity type filter (e.g. property, vehicle, bill)"),
		),
		mcp.WithString("entity_id",
			mcp.Description("Optional entity ID filter (UUID)"),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		args := req.GetArguments()
		entityType, _ := args["entity_type"].(string)
		entityIDStr, _ := args["entity_id"].(string)

		if (entityType == "") != (entityIDStr == "") {
			return mcp.NewToolResultText(`{"error":"entity_type and entity_id must be provided together"}`), nil
		}

		var rows pgx.Rows
		var err error

		if entityType != "" && entityIDStr != "" {
			entityID, parseErr := uuid.Parse(entityIDStr)
			if parseErr != nil {
				return mcp.NewToolResultText(fmt.Sprintf(`{"error":"invalid entity_id: %s"}`, parseErr.Error())), nil
			}
			rows, err = pool.Query(ctx,
				`SELECT id, name, content_type, size, ocr_status, created_at
				 FROM files
				 WHERE household_id = $1 AND entity_type = $2 AND entity_id = $3
				 ORDER BY created_at DESC`,
				claims.HouseholdID, entityType, entityID,
			)
		} else {
			rows, err = pool.Query(ctx,
				`SELECT id, name, content_type, size, ocr_status, created_at
				 FROM files
				 WHERE household_id = $1
				 ORDER BY created_at DESC`,
				claims.HouseholdID,
			)
		}
		if err != nil {
			return nil, fmt.Errorf("list files: %w", err)
		}
		defer rows.Close()

		type fileResult struct {
			ID          uuid.UUID `json:"id"`
			Name        string    `json:"name"`
			ContentType string    `json:"content_type"`
			Size        int64     `json:"size"`
			OCRStatus   string    `json:"ocr_status"`
			CreatedAt   time.Time `json:"created_at"`
		}

		files, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[fileResult])
		if err != nil {
			return nil, fmt.Errorf("collect files: %w", err)
		}

		if files == nil {
			files = []*fileResult{}
		}

		result, _ := json.Marshal(files)
		return mcp.NewToolResultText(string(result)), nil
	}

	return "list_files", tool, handler
}

// NewUploadFileTool creates the upload_file MCP tool.
// It accepts base64-encoded content, decodes it, and stores the file in the
// database with ocr_status set to 'pending' for background OCR processing.
func NewUploadFileTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("upload_file",
		mcp.WithDescription("Upload a file with base64-encoded content. The file will be queued for OCR processing."),
		mcp.WithString("name",
			mcp.Description("File name"),
			mcp.Required(),
		),
		mcp.WithString("content",
			mcp.Description("Base64-encoded file content"),
			mcp.Required(),
		),
		mcp.WithString("content_type",
			mcp.Description("MIME content type (e.g. application/pdf, image/jpeg)"),
			mcp.Required(),
		),
		mcp.WithString("entity_type",
			mcp.Description("Optional entity type to associate the file with"),
		),
		mcp.WithString("entity_id",
			mcp.Description("Optional entity ID (UUID) to associate the file with"),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		args := req.GetArguments()
		name, _ := args["name"].(string)
		contentB64, _ := args["content"].(string)
		contentType, _ := args["content_type"].(string)
		entityType, _ := args["entity_type"].(string)
		entityIDStr, _ := args["entity_id"].(string)

		if name == "" || contentB64 == "" || contentType == "" {
			return mcp.NewToolResultText(`{"error":"name, content, and content_type are required"}`), nil
		}

		// Decode base64 content.
		data, err := base64.StdEncoding.DecodeString(contentB64)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf(`{"error":"invalid base64 content: %s"}`, err.Error())), nil
		}

		// Parse optional entity_id.
		var entityID uuid.UUID
		if entityIDStr != "" {
			entityID, err = uuid.Parse(entityIDStr)
			if err != nil {
				return mcp.NewToolResultText(fmt.Sprintf(`{"error":"invalid entity_id: %s"}`, err.Error())), nil
			}
		}

		// Insert blob and metadata in a transaction.
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return nil, fmt.Errorf("upload file: begin tx: %w", err)
		}
		defer tx.Rollback(ctx)

		var blobID uuid.UUID
		err = tx.QueryRow(ctx,
			`INSERT INTO file_blobs (data, size) VALUES ($1, $2) RETURNING id`,
			data, len(data),
		).Scan(&blobID)
		if err != nil {
			return nil, fmt.Errorf("upload file: insert blob: %w", err)
		}

		var fileID uuid.UUID
		var createdAt time.Time
		err = tx.QueryRow(ctx,
			`INSERT INTO files (household_id, blob_id, name, content_type, size, entity_type, entity_id, ocr_status)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending')
			 RETURNING id, created_at`,
			claims.HouseholdID, blobID, name, contentType, len(data),
			nullIfEmpty(entityType), nullUUIDIfNil(entityID),
		).Scan(&fileID, &createdAt)
		if err != nil {
			return nil, fmt.Errorf("upload file: insert metadata: %w", err)
		}

		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("upload file: commit: %w", err)
		}

		result, _ := json.Marshal(map[string]interface{}{
			"id":           fileID.String(),
			"name":         name,
			"content_type": contentType,
			"size":         len(data),
			"ocr_status":   "pending",
			"created_at":   createdAt,
		})
		return mcp.NewToolResultText(string(result)), nil
	}

	return "upload_file", tool, handler
}

// NewGetFileTool creates the get_file MCP tool.
// It returns file metadata (without binary content) for a given file ID,
// scoped to the authenticated household.
func NewGetFileTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("get_file",
		mcp.WithDescription("Get file metadata by ID (without binary content)"),
		mcp.WithString("id",
			mcp.Description("File ID (UUID)"),
			mcp.Required(),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		args := req.GetArguments()
		idStr, _ := args["id"].(string)
		id, err := uuid.Parse(idStr)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf(`{"error":"invalid id: %s"}`, err.Error())), nil
		}

		type fileMeta struct {
			ID            uuid.UUID  `json:"id"`
			Name          string     `json:"name"`
			ContentType   string     `json:"content_type"`
			Size          int64      `json:"size"`
			EntityType    *string    `json:"entity_type"`
			EntityID      *uuid.UUID `json:"entity_id"`
			ExtractedText *string    `json:"extracted_text,omitempty"`
			OCRStatus     string     `json:"ocr_status"`
			OCRError      *string    `json:"ocr_error,omitempty"`
			CreatedAt     time.Time  `json:"created_at"`
			UpdatedAt     time.Time  `json:"updated_at"`
		}

		var f fileMeta
		err = pool.QueryRow(ctx,
			`SELECT id, name, content_type, size, entity_type, entity_id,
			        extracted_text, ocr_status, ocr_error, created_at, updated_at
			 FROM files
			 WHERE id = $1 AND household_id = $2`,
			id, claims.HouseholdID,
		).Scan(&f.ID, &f.Name, &f.ContentType, &f.Size, &f.EntityType, &f.EntityID,
			&f.ExtractedText, &f.OCRStatus, &f.OCRError, &f.CreatedAt, &f.UpdatedAt)
		if err != nil {
			if err == pgx.ErrNoRows {
				return mcp.NewToolResultText(`{"error":"file not found"}`), nil
			}
			return nil, fmt.Errorf("get file: %w", err)
		}

		result, _ := json.Marshal(f)
		return mcp.NewToolResultText(string(result)), nil
	}

	return "get_file", tool, handler
}

// NewSearchFilesTool creates the search_files MCP tool.
// It performs a LIKE search on file name and extracted_text columns,
// returning matching files with relevance snippets.
func NewSearchFilesTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("search_files",
		mcp.WithDescription("Search files by name or extracted text content"),
		mcp.WithString("query",
			mcp.Description("Search query string"),
			mcp.Required(),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		args := req.GetArguments()
		query, _ := args["query"].(string)
		if query == "" {
			return mcp.NewToolResultText(`{"error":"query is required"}`), nil
		}

		searchPattern := "%" + query + "%"

		rows, err := pool.Query(ctx,
			`SELECT id, name, content_type, size, ocr_status, extracted_text, created_at
			 FROM files
			 WHERE household_id = $1 AND (name ILIKE $2 OR extracted_text ILIKE $2)
			 ORDER BY
			   CASE
			     WHEN name ILIKE $2 AND extracted_text ILIKE $2 THEN 0
			     WHEN name ILIKE $2 THEN 1
			     ELSE 2
			   END,
			   created_at DESC`,
			claims.HouseholdID, searchPattern,
		)
		if err != nil {
			return nil, fmt.Errorf("search files: %w", err)
		}
		defer rows.Close()

		type fileSearchResult struct {
			ID            uuid.UUID `json:"id"`
			Name          string    `json:"name"`
			ContentType   string    `json:"content_type"`
			Size          int64     `json:"size"`
			OCRStatus     string    `json:"ocr_status"`
			ExtractedText *string   `json:"extracted_text,omitempty"`
			CreatedAt     time.Time `json:"created_at"`
			Relevance     string    `json:"relevance"`
		}

		var results []fileSearchResult
		for rows.Next() {
			var r fileSearchResult
			if err := rows.Scan(&r.ID, &r.Name, &r.ContentType, &r.Size, &r.OCRStatus, &r.ExtractedText, &r.CreatedAt); err != nil {
				return nil, fmt.Errorf("scan search result: %w", err)
			}

			// Determine relevance label.
			if contains(r.Name, query) && r.ExtractedText != nil && contains(*r.ExtractedText, query) {
				r.Relevance = "high"
			} else if contains(r.Name, query) {
				r.Relevance = "high"
			} else {
				r.Relevance = "medium"
			}
			results = append(results, r)
		}

		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate search results: %w", err)
		}

		if results == nil {
			results = []fileSearchResult{}
		}

		result, _ := json.Marshal(results)
		return mcp.NewToolResultText(string(result)), nil
	}

	return "search_files", tool, handler
}

// nullIfEmpty returns nil if s is empty, otherwise returns a pointer to s.
// Used for nullable text columns in INSERT statements.
func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// nullUUIDIfNil returns nil if id is the zero UUID, otherwise returns the UUID.
// Used for nullable UUID columns in INSERT statements.
func nullUUIDIfNil(id uuid.UUID) interface{} {
	if id == uuid.Nil {
		return nil
	}
	return id
}

// contains reports whether substr is in s, case-insensitively.
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			sc := s[i+j]
			tc := substr[j]
			if sc >= 'A' && sc <= 'Z' {
				sc += 32
			}
			if tc >= 'A' && tc <= 'Z' {
				tc += 32
			}
			if sc != tc {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}