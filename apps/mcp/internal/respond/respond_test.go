package respond_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"home-os/mcp/internal/respond"
)

type note struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Tags  []string `json:"tags,omitempty"`
}

// wire is a tool result as a client sees it after JSON decoding.
type wire struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent map[string]any `json:"structuredContent"`
	IsError           bool           `json:"isError"`
}

func toWire(t *testing.T, res *mcp.CallToolResult) wire {
	t.Helper()
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return w
}

// outputSchema returns the tool's declared outputSchema, resolved for validation,
// taken from the tool's wire form (exactly what tools/list advertises).
func outputSchema(t *testing.T, tool mcp.Tool) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal tool: %v", err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal tool: %v", err)
	}
	if len(raw.OutputSchema) == 0 {
		t.Fatalf("tool %s declares no outputSchema", tool.Name)
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

// checkShape asserts the invariants every respond result must keep: one text
// block whose JSON equals structuredContent, and structuredContent valid
// against the tool's outputSchema.
func checkShape(t *testing.T, rs *jsonschema.Resolved, w wire) {
	t.Helper()
	if len(w.Content) != 1 || w.Content[0].Type != "text" {
		t.Fatalf("want exactly one text block, got %+v", w.Content)
	}
	var fromText map[string]any
	if err := json.Unmarshal([]byte(w.Content[0].Text), &fromText); err != nil {
		t.Fatalf("text block is not JSON: %v (%q)", err, w.Content[0].Text)
	}
	a, _ := json.Marshal(fromText)
	b, _ := json.Marshal(w.StructuredContent)
	if string(a) != string(b) {
		t.Fatalf("text and structuredContent differ:\ntext: %s\nstructured: %s", a, b)
	}
	if err := rs.Validate(w.StructuredContent); err != nil {
		t.Fatalf("structuredContent does not match outputSchema: %v\n%s", err, b)
	}
}

func TestOK(t *testing.T) {
	tool := mcp.NewTool("notes_get", respond.Schema[note]())
	rs := outputSchema(t, tool)

	res := respond.OK(`Found note "Groceries".`, note{ID: "n1", Title: "Groceries"},
		"Use notes_update with note_id to edit it.")
	w := toWire(t, res)
	checkShape(t, rs, w)

	if w.IsError {
		t.Fatal("OK result has isError")
	}
	sc := w.StructuredContent
	if sc["ok"] != true || sc["summary"] != `Found note "Groceries".` {
		t.Fatalf("bad envelope: %v", sc)
	}
	if data, _ := sc["data"].(map[string]any); data["id"] != "n1" {
		t.Fatalf("bad data: %v", sc["data"])
	}
	if next, _ := sc["next"].([]any); len(next) != 1 {
		t.Fatalf("bad next: %v", sc["next"])
	}
	if _, ok := sc["error"]; ok {
		t.Fatal("OK result carries error")
	}
}

func TestOKWithoutNextOmitsIt(t *testing.T) {
	w := toWire(t, respond.OK("Deleted 1 note.", map[string]int{"deleted": 1}))
	if _, ok := w.StructuredContent["next"]; ok {
		t.Fatalf("next should be omitted: %v", w.StructuredContent)
	}
}

func TestList(t *testing.T) {
	tool := mcp.NewTool("notes_search", respond.ListSchema[note]())
	rs := outputSchema(t, tool)

	three := []note{{ID: "a", Title: "A"}, {ID: "b", Title: "B"}, {ID: "c", Title: "C"}}

	tests := []struct {
		name          string
		items         []note
		page          respond.Page
		wantReturned  float64
		wantTotal     float64
		wantTruncated bool
		wantNarrow    string
		wantCursor    string
	}{
		{name: "complete", items: three, page: respond.Page{Total: 3}, wantReturned: 3, wantTotal: 3},
		{name: "total unset means complete", items: three, wantReturned: 3, wantTotal: 3},
		{name: "empty", items: nil, wantReturned: 0, wantTotal: 0},
		{
			name: "truncated with hint", items: three,
			page:         respond.Page{Total: 143, Narrow: "Pass tag or query to narrow the results."},
			wantReturned: 3, wantTotal: 143, wantTruncated: true,
			wantNarrow: "Pass tag or query to narrow the results.",
		},
		{
			name: "next cursor", items: three,
			page:         respond.Page{Total: 9, NextCursor: respond.EncodeCursor(3), Narrow: "Pass next_cursor as cursor for the next page."},
			wantReturned: 3, wantTotal: 9, wantTruncated: true,
			wantNarrow: "Pass next_cursor as cursor for the next page.", wantCursor: respond.EncodeCursor(3),
		},
		{
			name: "next cursor alone marks truncated", items: three,
			page:         respond.Page{NextCursor: respond.EncodeCursor(3)},
			wantReturned: 3, wantTotal: 3, wantTruncated: true,
			wantNarrow: "More items matched than were returned. Add filters or a more specific query to narrow the results.",
			wantCursor: respond.EncodeCursor(3),
		},
		{
			name: "truncated default hint", items: three, page: respond.Page{Total: 10},
			wantReturned: 3, wantTotal: 10, wantTruncated: true,
			wantNarrow: "More items matched than were returned. Add filters or a more specific query to narrow the results.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := toWire(t, respond.List("Found notes.", tc.items, tc.page, "Use notes_get with an id for details."))
			checkShape(t, rs, w)
			sc := w.StructuredContent

			items, ok := sc["items"].([]any)
			if !ok {
				t.Fatalf("items must always be an array on success: %v", sc)
			}
			if float64(len(items)) != tc.wantReturned || sc["returned"] != tc.wantReturned {
				t.Fatalf("returned: items=%d returned=%v want %v", len(items), sc["returned"], tc.wantReturned)
			}
			if sc["total"] != tc.wantTotal {
				t.Fatalf("total = %v, want %v", sc["total"], tc.wantTotal)
			}
			gotTrunc, _ := sc["truncated"].(bool)
			if gotTrunc != tc.wantTruncated {
				t.Fatalf("truncated = %v, want %v", gotTrunc, tc.wantTruncated)
			}
			gotNarrow, _ := sc["narrow"].(string)
			if gotNarrow != tc.wantNarrow {
				t.Fatalf("narrow = %q, want %q", gotNarrow, tc.wantNarrow)
			}
			gotCursor, _ := sc["next_cursor"].(string)
			if _, present := sc["next_cursor"]; present != (tc.wantCursor != "") || gotCursor != tc.wantCursor {
				t.Fatalf("next_cursor = %v (present %v), want %q", sc["next_cursor"], present, tc.wantCursor)
			}
		})
	}
}

func TestCursor(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for _, off := range []int{0, 1, 25, 1000} {
		if got, ok := respond.DecodeCursor(respond.EncodeCursor(off)); !ok || got != off {
			t.Fatalf("round trip %d: got %d, %v", off, got, ok)
		}
	}
	tests := []struct {
		name   string
		cursor string
		want   int
		ok     bool
	}{
		{"current form", respond.EncodeCursor(40), 40, true},
		{"padded base64", base64.URLEncoding.EncodeToString([]byte("o:7")), 7, true},
		{"surrounding space", " " + respond.EncodeCursor(5) + " ", 5, true},
		{"legacy plain offset", "25", 25, true},
		{"legacy event_find", b64("evt:50"), 50, true},
		{"legacy maintenance_find", b64("maintenance:2"), 2, true},
		{"empty", "", 0, false},
		{"negative offset", "-3", 0, false},
		{"negative encoded", b64("o:-3"), 0, false},
		{"made up", "page2", 0, false},
		{"not base64 of a cursor", "abc", 0, false},
		{"unknown prefix", b64("x:4"), 0, false},
		{"prefix without number", b64("o:"), 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := respond.DecodeCursor(tc.cursor)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Fatalf("DecodeCursor(%q) = %d, %v; want %d, %v", tc.cursor, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	// Error results must validate against every respond schema, so a tool can
	// return one regardless of its declared output type.
	schemas := map[string]*jsonschema.Resolved{
		"single": outputSchema(t, mcp.NewTool("notes_get", respond.Schema[note]())),
		"list":   outputSchema(t, mcp.NewTool("notes_search", respond.ListSchema[note]())),
	}

	tests := []struct {
		name        string
		res         *mcp.CallToolResult
		wantMessage string
		wantFix     string
		wantField   string
		wantExample bool
	}{
		{
			name: "invalid with example",
			res: respond.Invalid("due_date", `due_date "next week" is not a date.`, "Pass due_date as YYYY-MM-DD.",
				map[string]any{"due_date": "2026-12-01"}),
			wantMessage: `due_date "next week" is not a date.`, wantFix: "Pass due_date as YYYY-MM-DD.",
			wantField: "due_date", wantExample: true,
		},
		{
			name:        "not found",
			res:         respond.NotFound("note", "n9", "Call notes_search to find the note_id."),
			wantMessage: `No note with id "n9" was found.`, wantFix: "Call notes_search to find the note_id.",
		},
		{
			name:        "failed",
			res:         respond.Failed("list notes", errors.New("connection refused")),
			wantMessage: "Could not list notes: connection refused",
			wantFix:     "Retry the same call once. If it fails again, tell the user it is unavailable right now.",
		},
		{
			name:        "failed nil err",
			res:         respond.Failed("list notes", nil),
			wantMessage: "Could not list notes.",
			wantFix:     "Retry the same call once. If it fails again, tell the user it is unavailable right now.",
		},
		{
			name:        "fail generic",
			res:         respond.Fail(respond.Error{Message: "That bill is already paid.", Fix: "Call bills_search with status=unpaid."}),
			wantMessage: "That bill is already paid.", wantFix: "Call bills_search with status=unpaid.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := toWire(t, tc.res)
			for kind, rs := range schemas {
				t.Run(kind, func(t *testing.T) { checkShape(t, rs, w) })
			}
			if !w.IsError {
				t.Fatal("error result must set isError")
			}
			sc := w.StructuredContent
			if sc["ok"] != false || sc["summary"] != tc.wantMessage {
				t.Fatalf("bad envelope: %v", sc)
			}
			e, _ := sc["error"].(map[string]any)
			if e["message"] != tc.wantMessage || e["fix"] != tc.wantFix {
				t.Fatalf("bad error: %v", e)
			}
			if f, _ := e["field"].(string); f != tc.wantField {
				t.Fatalf("field = %q, want %q", f, tc.wantField)
			}
			if _, has := e["example"]; has != tc.wantExample {
				t.Fatalf("example present = %v, want %v", has, tc.wantExample)
			}
			for _, k := range []string{"data", "items", "total"} {
				if _, has := sc[k]; has {
					t.Fatalf("error result must not carry %q: %v", k, sc)
				}
			}
		})
	}
}

func TestUnencodableDataBecomesError(t *testing.T) {
	w := toWire(t, respond.OK("bad", map[string]any{"ch": make(chan int)}))
	if !w.IsError || w.StructuredContent["ok"] != false {
		t.Fatalf("want error result, got %+v", w)
	}
	if !strings.Contains(w.Content[0].Text, "Could not encode the result") {
		t.Fatalf("unexpected text: %s", w.Content[0].Text)
	}
}

// TestThroughServer checks the advertised schema and the returned result on
// the real JSON-RPC path, including that an isError result is a tool result
// rather than a protocol error.
func TestThroughServer(t *testing.T) {
	srv := server.NewMCPServer("test", "0.0.0")
	srv.AddTool(mcp.NewTool("notes_search",
		mcp.WithString("tag"),
		respond.ListSchema[note](),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tag := req.GetString("tag", "")
		if tag == "" {
			return respond.Invalid("tag", "tag is required.", "Pass a tag name.", map[string]any{"tag": "kitchen"}), nil
		}
		return respond.List("Found 1 of 5 notes tagged "+tag+".", []note{{ID: "a", Title: "A", Tags: []string{tag}}},
			respond.Page{Total: 5, Narrow: "Pass query to narrow."}), nil
	})

	call := func(method string, params any) json.RawMessage {
		t.Helper()
		msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		out, _ := json.Marshal(srv.HandleMessage(context.Background(), msg))
		var resp struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("decode %s: %v", method, err)
		}
		if resp.Error != nil {
			t.Fatalf("%s returned protocol error: %s", method, resp.Error)
		}
		return resp.Result
	}

	call("initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "t", "version": "0"},
	})

	var list struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(call("tools/list", map[string]any{}), &list); err != nil || len(list.Tools) != 1 {
		t.Fatalf("tools/list: %v %d", err, len(list.Tools))
	}
	var advertised struct {
		OutputSchema jsonschema.Schema `json:"outputSchema"`
	}
	if err := json.Unmarshal(list.Tools[0], &advertised); err != nil {
		t.Fatalf("decode tool: %v", err)
	}
	rs, err := advertised.OutputSchema.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	for _, args := range []map[string]any{{"tag": "kitchen"}, {}} {
		var w wire
		if err := json.Unmarshal(call("tools/call", map[string]any{"name": "notes_search", "arguments": args}), &w); err != nil {
			t.Fatalf("decode call: %v", err)
		}
		checkShape(t, rs, w)
		if wantErr := len(args) == 0; w.IsError != wantErr {
			t.Fatalf("args %v: isError = %v", args, w.IsError)
		}
	}
}

// TestSchemaRejectsBadOutput guards against a vacuous validator: output that
// breaks the envelope contract must fail against the declared schema.
func TestSchemaRejectsBadOutput(t *testing.T) {
	single := outputSchema(t, mcp.NewTool("notes_get", respond.Schema[note]()))
	list := outputSchema(t, mcp.NewTool("notes_search", respond.ListSchema[note]()))

	bad := []struct {
		name string
		rs   *jsonschema.Resolved
		sc   map[string]any
	}{
		{"missing summary", single, map[string]any{"ok": true}},
		{"unknown key", single, map[string]any{"ok": true, "summary": "x", "extra": 1}},
		{"data wrong shape", single, map[string]any{"ok": true, "summary": "x", "data": map[string]any{"id": 1, "title": "t"}}},
		{"items not array", list, map[string]any{"ok": true, "summary": "x", "items": "nope"}},
		{"error missing fix", list, map[string]any{"ok": false, "summary": "x", "error": map[string]any{"message": "x"}}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.rs.Validate(tc.sc); err == nil {
				t.Fatalf("schema accepted invalid output %v", tc.sc)
			}
		})
	}
}
