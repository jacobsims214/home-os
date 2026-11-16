package file

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/testutil"
)

type handlerFunc = func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)

// wireDoc is a full document (documentData) as a client decodes it.
type wireDoc struct {
	ID              uuid.UUID  `json:"id"`
	Title           string     `json:"title"`
	FileName        string     `json:"file_name"`
	Category        string     `json:"category"`
	ContentType     *string    `json:"content_type"`
	SizeBytes       int64      `json:"size_bytes"`
	Description     *string    `json:"description"`
	DocumentDate    *string    `json:"document_date"`
	DocumentWeekday *string    `json:"document_weekday"`
	ExpiresOn       *string    `json:"expires_on"`
	PersonID        *uuid.UUID `json:"person_id"`
	PersonName      *string    `json:"person_name"`
	AboutType       string     `json:"about_type"`
	AboutID         *uuid.UUID `json:"about_id"`
	AboutName       *string    `json:"about_name"`
	Tags            []string   `json:"tags"`
	TextStatus      string     `json:"text_status"`
	TextNote        string     `json:"text_note"`
	TextError       *string    `json:"text_error"`
	RetryAttempt    *int       `json:"retry_attempt"`
	RetryMax        *int       `json:"retry_max"`
	NextRetryAt     *string    `json:"next_retry_at"`
	TextChars       int        `json:"text_chars"`
	Text            *struct {
		Content       string `json:"content"`
		Offset        int    `json:"offset"`
		ReturnedChars int    `json:"returned_chars"`
		TotalChars    int    `json:"total_chars"`
		HasMore       bool   `json:"has_more"`
		NextOffset    *int   `json:"next_offset"`
	} `json:"text"`
	Links []struct {
		AboutType string    `json:"about_type"`
		AboutID   uuid.UUID `json:"about_id"`
		AboutName *string   `json:"about_name"`
	} `json:"links"`
	CreatedLocalDate string `json:"created_local_date"`
	CreatedWeekday   string `json:"created_weekday"`
	Duplicate        bool   `json:"duplicate"`
}

// wireItem is one document_find item as a client decodes it.
type wireItem struct {
	ID         uuid.UUID  `json:"id"`
	Title      string     `json:"title"`
	Category   string     `json:"category"`
	PersonName *string    `json:"person_name"`
	AboutType  string     `json:"about_type"`
	AboutID    *uuid.UUID `json:"about_id"`
	AboutName  *string    `json:"about_name"`
	Tags       []string   `json:"tags"`
	TextStatus string     `json:"text_status"`
	TextChars  int        `json:"text_chars"`
	Snippet    *string    `json:"snippet"`
}

type envelope[T any] struct {
	OK         bool     `json:"ok"`
	Summary    string   `json:"summary"`
	Data       *T       `json:"data"`
	Items      []T      `json:"items"`
	Total      *int     `json:"total"`
	Truncated  bool     `json:"truncated"`
	Narrow     string   `json:"narrow"`
	NextCursor string   `json:"next_cursor"`
	Next       []string `json:"next"`
	Error      *struct {
		Message string `json:"message"`
		Fix     string `json:"fix"`
		Field   string `json:"field"`
	} `json:"error"`
}

func outputSchema(t *testing.T, tool mcp.Tool) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal tool: %v", err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || len(raw.OutputSchema) == 0 {
		t.Fatalf("tool %s declares no outputSchema (%v)", tool.Name, err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw.OutputSchema, &s); err != nil {
		t.Fatalf("unmarshal outputSchema: %v", err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve outputSchema: %v", err)
	}
	return rs
}

// caller runs one tool and checks every result like a strict client: a tool
// result with one text block equal to structuredContent, which validates
// against the outputSchema.
type caller[T any] struct {
	tool    mcp.Tool
	handler handlerFunc
	rs      *jsonschema.Resolved
}

func newCaller[T any](t *testing.T, name string, tool mcp.Tool, h handlerFunc) caller[T] {
	t.Helper()
	if name != tool.Name {
		t.Fatalf("returned name %q != tool name %q", name, tool.Name)
	}
	return caller[T]{tool: tool, handler: h, rs: outputSchema(t, tool)}
}

func (c caller[T]) call(t *testing.T, ctx context.Context, args map[string]any) (bool, envelope[T], string) {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = c.tool.Name
	req.Params.Arguments = args
	res, err := c.handler(ctx, req)
	if err != nil {
		t.Fatalf("%s returned a protocol error: %v", c.tool.Name, err)
	}
	b, _ := json.Marshal(res)
	var w struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent map[string]any `json:"structuredContent"`
		IsError           bool           `json:"isError"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(w.Content) != 1 || w.Content[0].Type != "text" {
		t.Fatalf("want exactly one text block, got %+v", w.Content)
	}
	var fromText map[string]any
	if err := json.Unmarshal([]byte(w.Content[0].Text), &fromText); err != nil {
		t.Fatalf("text block is not JSON: %v", err)
	}
	x, _ := json.Marshal(fromText)
	y, _ := json.Marshal(w.StructuredContent)
	if string(x) != string(y) {
		t.Fatalf("text and structuredContent differ:\n%s\n%s", x, y)
	}
	if err := c.rs.Validate(w.StructuredContent); err != nil {
		t.Fatalf("structuredContent does not match outputSchema: %v\n%s", err, y)
	}
	var env envelope[T]
	if err := json.Unmarshal([]byte(w.Content[0].Text), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if w.IsError == env.OK {
		t.Fatalf("isError=%v but ok=%v", w.IsError, env.OK)
	}
	return w.IsError, env, w.Content[0].Text
}

func (c caller[T]) mustOK(t *testing.T, ctx context.Context, args map[string]any) envelope[T] {
	t.Helper()
	isErr, env, _ := c.call(t, ctx, args)
	if isErr || !env.OK {
		t.Fatalf("%s %v: want success, got %+v (error %+v)", c.tool.Name, args, env, env.Error)
	}
	return env
}

type errCase struct {
	name      string
	ctx       context.Context
	args      map[string]any
	wantField string
	wantMsg   string // substring
}

func runErrCases[T any](t *testing.T, c caller[T], cases []errCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isErr, env, _ := c.call(t, tc.ctx, tc.args)
			if !isErr || env.OK || env.Error == nil {
				t.Fatalf("want isError result, got %+v", env)
			}
			if env.Data != nil || env.Items != nil {
				t.Errorf("error result leaked data")
			}
			if !strings.Contains(env.Error.Message, tc.wantMsg) {
				t.Errorf("message: want %q in %q", tc.wantMsg, env.Error.Message)
			}
			if env.Error.Field != tc.wantField {
				t.Errorf("field: want %q, got %q", tc.wantField, env.Error.Field)
			}
			if env.Error.Fix == "" {
				t.Error("fix hint is empty")
			}
		})
	}
}

func TestToolDefinitions(t *testing.T) {
	type class struct{ readOnly, destructive, idempotent bool }
	classR, classC := class{true, false, true}, class{false, false, false}
	classU, classX := class{false, false, true}, class{false, true, true}
	tests := []struct {
		build    func() (string, mcp.Tool, handlerFunc)
		name     string
		title    string
		required []string
		optional []string
		class    class
	}{
		{func() (string, mcp.Tool, handlerFunc) { return NewDocumentFindTool(nil, nil) }, "document_find", "Find documents",
			nil, []string{"query", "category", "person_id", "about_type", "about_id", "from", "to", "tag", "limit", "cursor"}, classR},
		{func() (string, mcp.Tool, handlerFunc) { return NewDocumentGetTool(nil) }, "document_get", "Get document",
			[]string{"document_id"}, []string{"text_offset"}, classR},
		{func() (string, mcp.Tool, handlerFunc) { return NewDocumentUploadTool(nil, nil) }, "document_upload", "Upload document",
			[]string{"name"}, []string{"content_base64", "content_type", "text", "title", "category", "tags", "person_id",
				"about_type", "about_id", "doc_date", "expires_on", "description"}, classC},
		{func() (string, mcp.Tool, handlerFunc) { return NewDocumentUpdateTool(nil, nil) }, "document_update", "Update document",
			[]string{"document_id"}, []string{"title", "category", "tags", "person_id", "doc_date", "expires_on", "description",
				"about_type", "about_id", "add_links", "remove_links"}, classU},
		{func() (string, mcp.Tool, handlerFunc) { return NewDocumentDeleteTool(nil, nil) }, "document_delete", "Delete document",
			[]string{"document_id"}, nil, classX},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, tool, _ := tc.build()
			if name != tc.name || tool.Name != tc.name {
				t.Fatalf("name: got %q / %q", name, tool.Name)
			}
			if got := tool.InputSchema.Required; strings.Join(got, ",") != strings.Join(tc.required, ",") {
				t.Errorf("required: want %v, got %v", tc.required, got)
			}
			want := map[string]bool{}
			for _, p := range append(append([]string{}, tc.required...), tc.optional...) {
				want[p] = true
			}
			if len(tool.InputSchema.Properties) != len(want) {
				t.Errorf("params: want %d, got %d", len(want), len(tool.InputSchema.Properties))
			}
			for p, raw := range tool.InputSchema.Properties {
				if !want[p] {
					t.Errorf("unexpected param %q", p)
				}
				desc, _ := raw.(map[string]any)["description"].(string)
				if desc == "" || len([]rune(desc)) > 80 {
					t.Errorf("param %q description must be 1-80 chars, got %d: %q", p, len([]rune(desc)), desc)
				}
			}
			if n := len([]rune(tool.Description)); n == 0 || n > 200 {
				t.Errorf("description must be 1-200 chars, got %d", n)
			}
			a := tool.Annotations
			if a.Title != tc.title || a.ReadOnlyHint == nil || a.DestructiveHint == nil ||
				a.IdempotentHint == nil || a.OpenWorldHint == nil {
				t.Fatalf("annotations: want title %q and all four hints, got %+v", tc.title, a)
			}
			if got := (class{*a.ReadOnlyHint, *a.DestructiveHint, *a.IdempotentHint}); got != tc.class || *a.OpenWorldHint {
				t.Errorf("hints: want %+v openWorld=false, got %+v openWorld=%v", tc.class, got, *a.OpenWorldHint)
			}
			outputSchema(t, tool)
			if p, ok := tool.InputSchema.Properties["about_type"]; ok {
				enum, _ := p.(map[string]any)["enum"].([]string)
				if strings.Join(enum, ",") != "family,person,property,room,asset,vehicle,pet,contact,bill,loan,maintenance,event,list,recipe,guide" {
					t.Errorf("about_type enum: %v", enum)
				}
			}
		})
	}
}

// TestTextState covers the clean OCR status: the worker's retry prefix is
// parsed, never shown raw.
func TestTextState(t *testing.T) {
	s := func(v string) *string { return &v }
	tests := []struct {
		name       string
		status     string
		ocrErr     *string
		chars      int
		want       string
		wantErr    string
		wantRetry  int
		wantMax    int
		wantNextAt bool
	}{
		{name: "pending", status: "pending", want: "pending"},
		{name: "processing reads as pending", status: "processing", want: "pending"},
		{name: "done", status: "done", chars: 10, want: "done"},
		{name: "done empty", status: "done", want: "done"},
		{name: "skipped", status: "skipped", want: "skipped"},
		{name: "retrying", status: "failed", ocrErr: s("[retry 2/5 next=2026-11-15T21:00:00Z] tika: connection refused"),
			want: "retrying", wantErr: "tika: connection refused", wantRetry: 2, wantMax: 5, wantNextAt: true},
		{name: "retrying with bad time", status: "failed", ocrErr: s("[retry 1/3 next=soon] boom"),
			want: "retrying", wantErr: "boom", wantRetry: 1, wantMax: 3},
		{name: "failed for good", status: "failed", ocrErr: s("failed after 6 attempts: tika down"),
			want: "failed", wantErr: "failed after 6 attempts: tika down"},
		{name: "failed no message", status: "failed", want: "failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := docRow{OCRStatus: tc.status, OCRError: tc.ocrErr, TextChars: tc.chars, Timezone: "America/Chicago"}
			ts := r.text()
			if ts.Status != tc.want || ts.Note == "" {
				t.Fatalf("status: want %q, got %+v", tc.want, ts)
			}
			if strings.Contains(ts.Note, "[retry") || (ts.Error != nil && strings.Contains(*ts.Error, "[retry")) {
				t.Errorf("raw retry prefix leaked: %+v", ts)
			}
			if (ts.Error == nil) != (tc.wantErr == "") || (ts.Error != nil && *ts.Error != tc.wantErr) {
				t.Errorf("error: want %q, got %v", tc.wantErr, ts.Error)
			}
			if tc.wantRetry > 0 {
				if ts.RetryAttempt == nil || *ts.RetryAttempt != tc.wantRetry || *ts.RetryMax != tc.wantMax {
					t.Errorf("retry: want %d/%d, got %v/%v", tc.wantRetry, tc.wantMax, ts.RetryAttempt, ts.RetryMax)
				}
			} else if ts.RetryAttempt != nil {
				t.Errorf("unexpected retry attempt %v", *ts.RetryAttempt)
			}
			if (ts.NextRetryAt != nil) != tc.wantNextAt {
				t.Errorf("next_retry_at: %v", ts.NextRetryAt)
			}
			if tc.wantNextAt && *ts.NextRetryAt != "2026-11-15T15:00:00-06:00" {
				t.Errorf("next_retry_at in family time: got %s", *ts.NextRetryAt)
			}
		})
	}
}

func seed(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("seed %q: %v", sql, err)
	}
	return id
}

func TestDocumentUpload(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, other := testutil.SeedHousehold(t, pool)

	name, tool, h := NewDocumentUploadTool(pool, nil)
	up := newCaller[wireDoc](t, name, tool, h)

	vehicleID := seed(t, pool, `INSERT INTO vehicles (household_id, year, make, model) VALUES ($1, 2022, 'Honda', 'Odyssey') RETURNING id`, hh.HouseholdID)
	theirVehicle := seed(t, pool, `INSERT INTO vehicles (household_id, make) VALUES ($1, 'Kia') RETURNING id`, other.HouseholdID)
	noraID := seed(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Nora') RETURNING id`, hh.HouseholdID)
	seed(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Sam') RETURNING id`, hh.HouseholdID)
	seed(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'sam') RETURNING id`, hh.HouseholdID)

	t.Run("text upload is searchable at once", func(t *testing.T) {
		env := up.mustOK(t, ctx, map[string]any{
			"name": "costco.txt", "text": "Costco 2026-03-01\nTotal $142.50", "category": "Receipts",
			"tags": []any{"groceries", " Groceries ", "march"}, "doc_date": "2026-03-01",
		})
		d := env.Data
		if d.Category != "receipt" || d.TextStatus != "done" || d.TextChars != 31 || d.Title != "costco.txt" {
			t.Errorf("got %+v", d)
		}
		if strings.Join(d.Tags, ",") != "groceries,march" || d.AboutType != "family" || d.AboutID != nil {
			t.Errorf("tags/about: %v %s %v", d.Tags, d.AboutType, d.AboutID)
		}
		if d.DocumentDate == nil || *d.DocumentDate != "2026-03-01" || *d.DocumentWeekday != "Sun" {
			t.Errorf("document date: %v %v", d.DocumentDate, d.DocumentWeekday)
		}
		if d.Text != nil {
			t.Error("upload must not echo the text")
		}
		if !strings.Contains(env.Summary, "searchable now") || !strings.Contains(env.Next[0], "document_get document_id="+d.ID.String()) {
			t.Errorf("summary/next: %q %v", env.Summary, env.Next)
		}
		var entityType string
		var entityID uuid.UUID
		if err := pool.QueryRow(context.Background(), `SELECT entity_type, entity_id FROM files WHERE id = $1`, d.ID).Scan(&entityType, &entityID); err != nil {
			t.Fatal(err)
		}
		if entityType != "household" || entityID != hh.HouseholdID {
			t.Errorf("stored entity: %s %s", entityType, entityID)
		}
	})

	t.Run("bytes about a vehicle for a person", func(t *testing.T) {
		pdf := []byte("%PDF-1.4 fake policy")
		env := up.mustOK(t, ctx, map[string]any{
			"name": "policy.pdf", "content_base64": base64.StdEncoding.EncodeToString(pdf),
			"title": "Car insurance 2026", "category": "insurance", "about_type": "vehicle", "about_id": vehicleID.String(),
			"person_id": "nora", "doc_date": "2026-01-01", "expires_on": "2027-01-01", "description": "Full coverage",
		})
		d := env.Data
		if d.ContentType == nil || *d.ContentType != "application/pdf" || d.SizeBytes != int64(len(pdf)) || d.TextStatus != "pending" {
			t.Errorf("got %+v", d)
		}
		if d.AboutType != "vehicle" || d.AboutID == nil || *d.AboutID != vehicleID || d.AboutName == nil || *d.AboutName != "2022 Honda Odyssey" {
			t.Errorf("about: %+v", d)
		}
		if d.PersonID == nil || *d.PersonID != noraID || *d.PersonName != "Nora" {
			t.Errorf("person: %v %v", d.PersonID, d.PersonName)
		}
		if !strings.Contains(env.Summary, `"Car insurance 2026" (insurance) about vehicle 2022 Honda Odyssey, for Nora`) {
			t.Errorf("summary: %q", env.Summary)
		}
		var stored []byte
		if err := pool.QueryRow(context.Background(), `SELECT b.data FROM files f JOIN file_blobs b ON b.id = f.blob_id WHERE f.id = $1`, d.ID).Scan(&stored); err != nil || string(stored) != string(pdf) {
			t.Errorf("stored bytes: %q %v", stored, err)
		}
	})

	t.Run("data URL, legacy content alias and loop guard", func(t *testing.T) {
		args := map[string]any{"name": "scan.png", "content": "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("pngbytes"))}
		first := up.mustOK(t, ctx, args)
		if first.Data.ContentType == nil || *first.Data.ContentType != "image/png" || first.Data.Duplicate {
			t.Errorf("first: %+v", first.Data)
		}
		again := up.mustOK(t, ctx, args)
		if !again.Data.Duplicate || again.Data.ID != first.Data.ID {
			t.Errorf("want duplicate of %s, got %+v", first.Data.ID, again.Data)
		}
		var n int
		pool.QueryRow(context.Background(), `SELECT count(*) FROM files WHERE household_id = $1 AND name = 'scan.png'`, hh.HouseholdID).Scan(&n)
		if n != 1 {
			t.Errorf("rows: %d", n)
		}
	})

	t.Run("bill hint", func(t *testing.T) {
		env := up.mustOK(t, ctx, map[string]any{"name": "electric.txt", "text": "City Power invoice. Amount due $88.10"})
		if !strings.Contains(strings.Join(env.Next, " "), "bill_create") {
			t.Errorf("next: %v", env.Next)
		}
	})

	runErrCases(t, up, []errCase{
		{"not signed in", context.Background(), map[string]any{"name": "a.txt", "text": "x"}, "", "not signed in"},
		{"no name", ctx, map[string]any{"text": "x"}, "name", "name is required"},
		{"no content", ctx, map[string]any{"name": "a.txt"}, "content_base64", "no content"},
		{"both contents", ctx, map[string]any{"name": "a.txt", "text": "x", "content_base64": "eA=="}, "text", "both given"},
		{"bad base64", ctx, map[string]any{"name": "a.bin", "content_base64": "!!!not base64"}, "content_base64", "not valid base64"},
		{"bad category", ctx, map[string]any{"name": "a.txt", "text": "x", "category": "recipes"}, "category", "not one of"},
		{"bad date", ctx, map[string]any{"name": "a.txt", "text": "x", "doc_date": "March 1"}, "doc_date", "not a date"},
		{"expiry before date", ctx, map[string]any{"name": "a.txt", "text": "x", "doc_date": "2026-03-01", "expires_on": "2026-01-01"}, "expires_on", "before doc_date"},
		{"about_id without type", ctx, map[string]any{"name": "a.txt", "text": "x", "about_id": vehicleID.String()}, "about_type", "without about_type"},
		{"about type without id", ctx, map[string]any{"name": "a.txt", "text": "x", "about_type": "vehicle"}, "about_id", "needs about_id"},
		{"other family's vehicle", ctx, map[string]any{"name": "a.txt", "text": "x", "about_type": "vehicle", "about_id": theirVehicle.String()}, "about_id", "was found in this family"},
		{"unknown person", ctx, map[string]any{"name": "a.txt", "text": "x", "person_id": "Zed"}, "person_id", `No person named "Zed"`},
		{"ambiguous person", ctx, map[string]any{"name": "a.txt", "text": "x", "person_id": "SAM"}, "person_id", "2 people are named"},
		{"bad tags", ctx, map[string]any{"name": "a.txt", "text": "x", "tags": []any{1, 2}}, "tags", "list of strings"},
	})
}

func TestDocumentFindAndGet(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)

	upName, upTool, upH := NewDocumentUploadTool(pool, nil)
	up := newCaller[wireDoc](t, upName, upTool, upH)
	findName, findTool, findH := NewDocumentFindTool(pool, nil)
	find := newCaller[wireItem](t, findName, findTool, findH)
	getName, getTool, getH := NewDocumentGetTool(pool)
	get := newCaller[wireDoc](t, getName, getTool, getH)

	vehicleID := seed(t, pool, `INSERT INTO vehicles (household_id, make, model) VALUES ($1, 'Honda', 'Fit') RETURNING id`, hh.HouseholdID)
	seed(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Nora') RETURNING id`, hh.HouseholdID)

	long := strings.Repeat("abcdefghij", 950) // 9500 chars
	policy := up.mustOK(t, ctx, map[string]any{"name": "policy.txt", "title": "Car insurance policy", "category": "insurance",
		"text": "Policy number 42. " + long, "about_type": "vehicle", "about_id": vehicleID.String(), "doc_date": "2026-01-15", "tags": []any{"car"}}).Data
	receipt := up.mustOK(t, ctx, map[string]any{"name": "receipt.txt", "category": "receipt",
		"text": "Hardware store. Bought a 50% off drill for the garage.", "doc_date": "2026-03-02", "person_id": "Nora"}).Data
	report := up.mustOK(t, ctx, map[string]any{"name": "report-card.pdf", "category": "school",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("%PDF report")), "doc_date": "2025-06-01"}).Data
	up.mustOK(t, otherCtx, map[string]any{"name": "theirs.txt", "text": "insurance drill policy"})

	ids := func(items []wireItem) string {
		var s []string
		for _, it := range items {
			switch it.ID {
			case policy.ID:
				s = append(s, "policy")
			case receipt.ID:
				s = append(s, "receipt")
			case report.ID:
				s = append(s, "report")
			default:
				s = append(s, "?"+it.Title)
			}
		}
		return strings.Join(s, ",")
	}

	findTests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"all, newest document date first", map[string]any{}, "receipt,policy,report"},
		{"query in text", map[string]any{"query": "DRILL"}, "receipt"},
		{"query in title ranks first", map[string]any{"query": "policy"}, "policy"},
		{"wildcards are literal", map[string]any{"query": "%"}, "receipt"},
		{"underscore is literal", map[string]any{"query": "_"}, ""},
		{"category", map[string]any{"category": "insurance"}, "policy"},
		{"person by name", map[string]any{"person_id": "nora"}, "receipt"},
		{"about vehicle", map[string]any{"about_type": "vehicle", "about_id": vehicleID.String()}, "policy"},
		{"about any vehicle", map[string]any{"about_type": "vehicle"}, "policy"},
		{"family-level", map[string]any{"about_type": "family"}, "receipt,report"},
		{"date range inclusive", map[string]any{"from": "2026-01-15", "to": "2026-03-01"}, "policy"},
		{"tag any case", map[string]any{"tag": "CAR"}, "policy"},
		{"no match", map[string]any{"query": "zzz"}, ""},
	}
	for _, tc := range findTests {
		t.Run("find/"+tc.name, func(t *testing.T) {
			env := find.mustOK(t, ctx, tc.args)
			if got := ids(env.Items); got != tc.want {
				t.Errorf("want %q, got %q (%s)", tc.want, got, env.Summary)
			}
		})
	}

	t.Run("find/snippet and status", func(t *testing.T) {
		env := find.mustOK(t, ctx, map[string]any{"query": "drill"})
		it := env.Items[0]
		if it.Snippet == nil || !strings.Contains(*it.Snippet, "drill") || it.TextStatus != "done" || it.PersonName == nil {
			t.Errorf("item: %+v", it)
		}
		env = find.mustOK(t, ctx, map[string]any{"query": "Policy number"})
		s := *env.Items[0].Snippet
		if !strings.HasSuffix(s, "…") || len([]rune(s)) > snippetLen+2 {
			t.Errorf("snippet should be cut at %d chars: %d %q", snippetLen, len([]rune(s)), s)
		}
		if env.Items[0].AboutName == nil || *env.Items[0].AboutName != "Honda Fit" {
			t.Errorf("about_name: %v", env.Items[0].AboutName)
		}
	})

	t.Run("find/paging", func(t *testing.T) {
		first := find.mustOK(t, ctx, map[string]any{"limit": 2})
		if ids(first.Items) != "receipt,policy" || !first.Truncated || *first.Total != 3 {
			t.Fatalf("first page: %s %+v", ids(first.Items), first)
		}
		if first.NextCursor != respond.EncodeCursor(2) {
			t.Fatalf("first page next_cursor = %q", first.NextCursor)
		}
		second := find.mustOK(t, ctx, map[string]any{"limit": "2", "cursor": first.NextCursor})
		if ids(second.Items) != "report" || second.Truncated || second.NextCursor != "" {
			t.Errorf("second page: %s %+v", ids(second.Items), second)
		}
	})

	runErrCases(t, find, []errCase{
		{"not signed in", context.Background(), map[string]any{}, "", "not signed in"},
		{"bad category", ctx, map[string]any{"category": "junk"}, "category", "not one of"},
		{"bad from", ctx, map[string]any{"from": "yesterday"}, "from", "not a date"},
		{"to before from", ctx, map[string]any{"from": "2026-02-01", "to": "2026-01-01"}, "to", "before from"},
		{"bad limit", ctx, map[string]any{"limit": 500}, "limit", "limit must be"},
		{"bad cursor", ctx, map[string]any{"cursor": "nope"}, "cursor", "cursor is not"},
		{"bad about type", ctx, map[string]any{"about_type": "spaceship"}, "about_type", "not one of"},
		{"unknown person", ctx, map[string]any{"person_id": uuid.NewString()}, "person_id", "No person with id"},
	})

	t.Run("get/pages the text", func(t *testing.T) {
		total := len("Policy number 42. ") + len(long)
		offset := 0
		var got strings.Builder
		for page := 0; ; page++ {
			env := get.mustOK(t, ctx, map[string]any{"document_id": policy.ID.String(), "text_offset": offset})
			p := env.Data.Text
			if p == nil || p.Offset != offset || p.TotalChars != total || p.ReturnedChars > textPageSize {
				t.Fatalf("page %d: %+v", page, p)
			}
			got.WriteString(p.Content)
			if !p.HasMore {
				if p.NextOffset != nil {
					t.Errorf("last page has next_offset")
				}
				break
			}
			if !strings.Contains(env.Next[0], "text_offset=") {
				t.Errorf("next: %v", env.Next)
			}
			offset = *p.NextOffset
			if page > 5 {
				t.Fatal("too many pages")
			}
		}
		if got.String() != "Policy number 42. "+long {
			t.Errorf("reassembled text differs (%d chars)", got.Len())
		}
	})

	t.Run("get/retrying status is clean", func(t *testing.T) {
		if _, err := pool.Exec(context.Background(),
			`UPDATE files SET ocr_status = 'failed', ocr_error = '[retry 1/5 next=2030-01-01T00:00:00Z] tika timeout' WHERE id = $1`, report.ID); err != nil {
			t.Fatal(err)
		}
		isErr, env, raw := get.call(t, ctx, map[string]any{"id": report.ID.String()})
		if isErr || env.Data.TextStatus != "retrying" || *env.Data.TextError != "tika timeout" || *env.Data.RetryAttempt != 1 || env.Data.NextRetryAt == nil {
			t.Fatalf("got %+v", env.Data)
		}
		if strings.Contains(raw, "[retry") {
			t.Errorf("raw prefix leaked: %s", raw)
		}
		if !strings.Contains(strings.Join(env.Next, " "), "Check again later") {
			t.Errorf("next: %v", env.Next)
		}
		isErr, fenv, raw := find.call(t, ctx, map[string]any{"category": "school"})
		if isErr || fenv.Items[0].TextStatus != "retrying" || strings.Contains(raw, "[retry") {
			t.Errorf("find: %+v", fenv.Items)
		}
	})

	runErrCases(t, get, []errCase{
		{"not signed in", context.Background(), map[string]any{"document_id": policy.ID.String()}, "", "not signed in"},
		{"missing id", ctx, map[string]any{}, "document_id", "required"},
		{"bad id", ctx, map[string]any{"document_id": "car insurance"}, "document_id", "not a UUID"},
		{"other family", otherCtx, map[string]any{"document_id": policy.ID.String()}, "", "No document with id"},
		{"negative offset", ctx, map[string]any{"document_id": policy.ID.String(), "text_offset": -1}, "text_offset", "0 or more"},
		{"offset past end", ctx, map[string]any{"document_id": receipt.ID.String(), "text_offset": 4000}, "text_offset", "past the end"},
	})

}
