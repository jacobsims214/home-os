package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/config"
	"home-os/mcp/internal/server"
)

// toolClass is an annotation class from the tool catalog
// (architecture/mcp-tool-catalog.md). Every tool is exactly one of them.
type toolClass string

const (
	classR toolClass = "R" // read: find/get/summary/overview
	classC toolClass = "C" // create: a repeat call makes another row
	classU toolClass = "U" // update/state change: same args, same result
	classX toolClass = "X" // delete
)

// hints is one tool's four annotation hints as served on tools/list. A nil
// field means the hint is missing from the wire.
type hints struct {
	Title           string `json:"title"`
	ReadOnlyHint    *bool  `json:"readOnlyHint"`
	DestructiveHint *bool  `json:"destructiveHint"`
	IdempotentHint  *bool  `json:"idempotentHint"`
	OpenWorldHint   *bool  `json:"openWorldHint"`
}

// classHints is each class's (readOnly, destructive, idempotent, openWorld).
// No Home OS tool reaches outside the household's own data, so openWorld is
// always false.
var classHints = map[toolClass][4]bool{
	classR: {true, false, true, false},
	classC: {false, false, false, false},
	classU: {false, false, true, false},
	classX: {false, true, true, false},
}

// wantToolClass is the catalog class of every registered tool. A new tool
// must be added here (and to wantTools in protocol_test.go) in the ticket
// that registers it, with the class the catalog gives it.
var wantToolClass = map[string]toolClass{
	"activity_find":           classR,
	"agenda_get":              classR,
	"asset_create":            classC,
	"asset_delete":            classX,
	"asset_find":              classR,
	"asset_update":            classU,
	"bill_create":             classC,
	"bill_delete":             classX,
	"bill_find":               classR,
	"bill_pay":                classU,
	"bill_update":             classU,
	"budget_summary":          classR,
	"calendar_create":         classC,
	"calendar_find":           classR,
	"calendar_find_free_time": classR,
	"contact_create":          classC,
	"contact_delete":          classX,
	"contact_find":            classR,
	"contact_update":          classU,
	"credential_find":         classR,
	"credential_reveal":       classR,
	"document_find":           classR,
	"document_get":            classR,
	"document_upload":         classC,
	"event_create":            classC,
	"event_delete":            classX,
	"event_find":              classR,
	"event_update":            classU,
	"family_overview":         classR,
	"family_update":           classU,
	"guide_create":            classC,
	"guide_delete":            classX,
	"guide_find":              classR,
	"guide_get":               classR,
	"guide_update":            classU,
	"list_add_items":          classU,
	"list_check_items":        classU,
	"list_create":             classC,
	"list_find":               classR,
	"list_get":                classR,
	"list_remove_items":       classX,
	"list_update":             classU,
	"list_update_item":        classU,
	"loan_create":             classC,
	"loan_delete":             classX,
	"loan_find":               classR,
	"loan_update":             classU,
	"maintenance_complete":    classU,
	"maintenance_create":      classC,
	"maintenance_delete":      classX,
	"maintenance_find":        classR,
	"maintenance_update":      classU,
	"note_create":             classC,
	"note_delete":             classX,
	"note_find":               classR,
	"note_get":                classR,
	"note_update":             classU,
	"notification_find":       classR,
	"notification_mark_read":  classU,
	"person_create":           classC,
	"person_delete":           classX,
	"person_find":             classR,
	"person_get":              classR,
	"person_update":           classU,
	"pet_create":              classC,
	"pet_delete":              classX,
	"pet_find":                classR,
	"pet_update":              classU,
	"property_create":         classC,
	"property_delete":         classX,
	"property_find":           classR,
	"property_update":         classU,
	"room_delete":             classX,
	"search_all":              classR,
	"vehicle_create":          classC,
	"vehicle_delete":          classX,
	"vehicle_find":            classR,
	"vehicle_update":          classU,
}

// verbClass is the class a tool name's verb implies, for the verbs whose
// class the catalog fixes. Other verbs (pay, complete, mark_read, upload,
// all) are covered by wantToolClass alone.
func verbClass(name string) (toolClass, bool) {
	switch {
	case strings.HasSuffix(name, "_create"):
		return classC, true
	case strings.HasSuffix(name, "_delete"):
		return classX, true
	case strings.HasSuffix(name, "_update"):
		return classU, true
	case strings.Contains(name, "_find"), strings.HasSuffix(name, "_get"),
		strings.HasSuffix(name, "_summary"), strings.HasSuffix(name, "_overview"):
		return classR, true
	}
	return "", false
}

// annotationProblems returns everything wrong with one tool's annotations:
// a missing hint or title, a combination that is no catalog class, or a
// class that disagrees with wantToolClass or with the tool name's verb.
func annotationProblems(name string, h hints) []string {
	var probs []string
	if strings.TrimSpace(h.Title) == "" {
		probs = append(probs, "title is empty")
	}
	ptrs := []struct {
		key string
		v   *bool
	}{
		{"readOnlyHint", h.ReadOnlyHint},
		{"destructiveHint", h.DestructiveHint},
		{"idempotentHint", h.IdempotentHint},
		{"openWorldHint", h.OpenWorldHint},
	}
	var got [4]bool
	missing := false
	for i, p := range ptrs {
		if p.v == nil {
			probs = append(probs, p.key+" is missing")
			missing = true
			continue
		}
		got[i] = *p.v
	}
	if missing {
		return probs // a missing hint makes the class meaningless
	}

	var class toolClass
	for c, want := range classHints {
		if got == want {
			class = c
		}
	}
	if class == "" {
		return append(probs, fmt.Sprintf(
			"hints (readOnly=%v, destructive=%v, idempotent=%v, openWorld=%v) match no catalog class R/C/U/X",
			got[0], got[1], got[2], got[3]))
	}
	want, ok := wantToolClass[name]
	if !ok {
		probs = append(probs, fmt.Sprintf("not in wantToolClass: add it with its catalog class (hints say %s)", class))
	} else if class != want {
		probs = append(probs, fmt.Sprintf("hints say class %s, catalog says %s", class, want))
	}
	if vc, ok := verbClass(name); ok && class != vc {
		probs = append(probs, fmt.Sprintf("hints say class %s, but the name's verb means %s", class, vc))
	}
	return probs
}

// newRegisteredServer builds the production tool set without a database:
// registration never touches the pool.
func newRegisteredServer(t *testing.T) *server.Server {
	t.Helper()
	srv := server.NewServer(&config.Config{}, nil, nil)
	registerAll(srv, &config.Config{}, nil)
	return srv
}

// rpc sends one JSON-RPC request through the mcp-go server (and so through
// every tool handler middleware) and returns the raw response.
func rpc(t *testing.T, ctx context.Context, srv *server.Server, method string, params any) []byte {
	t.Helper()
	msg, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(srv.MCPServer().HandleMessage(ctx, msg))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// listedHints returns every tool's annotations exactly as tools/list serves
// them to clients.
func listedHints(t *testing.T, srv *server.Server) map[string]hints {
	t.Helper()
	var resp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Annotations hints  `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rpc(t, context.Background(), srv, "tools/list", map[string]any{}), &resp); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	if resp.Result.NextCursor != "" {
		t.Fatal("tools/list is paginated; follow nextCursor here")
	}
	out := map[string]hints{}
	for _, tl := range resp.Result.Tools {
		out[tl.Name] = tl.Annotations
	}
	return out
}

// TestEveryToolAnnotated is the registry-wide guard for #4405: a read-only
// agent token may call a tool only when it says readOnlyHint=true, so every
// tool must carry a title and all four hints, and its class must be the one
// the catalog gives it.
func TestEveryToolAnnotated(t *testing.T) {
	srv := newRegisteredServer(t)
	listed := listedHints(t, srv)
	if len(listed) == 0 {
		t.Fatal("tools/list returned no tools")
	}
	for name, h := range listed {
		for _, p := range annotationProblems(name, h) {
			t.Errorf("%s: %s", name, p)
		}
	}
	for name := range wantToolClass {
		if _, ok := listed[name]; !ok {
			t.Errorf("wantToolClass has %s, but it is not registered", name)
		}
	}
	if len(wantToolClass) != len(wantTools) {
		t.Errorf("wantToolClass has %d tools, wantTools has %d: keep them in step", len(wantToolClass), len(wantTools))
	}
}

// TestAnnotationProblemsCatchesMistakes proves the guard above fails on the
// mistakes it exists for, so it can't pass vacuously.
func TestAnnotationProblemsCatchesMistakes(t *testing.T) {
	b := mcp.ToBoolPtr
	r := hints{Title: "Find notes", ReadOnlyHint: b(true), DestructiveHint: b(false), IdempotentHint: b(true), OpenWorldHint: b(false)}
	without := func(f func(*hints)) hints { h := r; f(&h); return h }

	// An unannotated mcp.NewTool: mcp-go's defaults, no title.
	bare := mcp.NewTool("note_find").Annotations
	bareHints := hints{Title: bare.Title, ReadOnlyHint: bare.ReadOnlyHint, DestructiveHint: bare.DestructiveHint, IdempotentHint: bare.IdempotentHint, OpenWorldHint: bare.OpenWorldHint}

	tests := []struct {
		name, tool string
		h          hints
		want       string
	}{
		{"correct R", "note_find", r, ""},
		{"unannotated NewTool", "note_find", bareHints, "title is empty"},
		{"unannotated NewTool class", "note_find", bareHints, "match no catalog class"},
		{"no annotations at all", "note_find", hints{}, "readOnlyHint is missing"},
		{"empty title", "note_find", without(func(h *hints) { h.Title = " " }), "title is empty"},
		{"readOnlyHint missing", "note_find", without(func(h *hints) { h.ReadOnlyHint = nil }), "readOnlyHint is missing"},
		{"destructiveHint missing", "note_find", without(func(h *hints) { h.DestructiveHint = nil }), "destructiveHint is missing"},
		{"idempotentHint missing", "note_find", without(func(h *hints) { h.IdempotentHint = nil }), "idempotentHint is missing"},
		{"openWorldHint missing", "note_find", without(func(h *hints) { h.OpenWorldHint = nil }), "openWorldHint is missing"},
		{"openWorld true", "note_find", without(func(h *hints) { h.OpenWorldHint = b(true) }), "match no catalog class"},
		{"find marked U", "note_find", without(func(h *hints) { h.ReadOnlyHint = b(false) }), "catalog says R"},
		{"create marked idempotent", "note_create", without(func(h *hints) { h.ReadOnlyHint = b(false) }), "catalog says C"},
		{"delete not destructive", "note_delete", without(func(h *hints) { h.ReadOnlyHint = b(false) }), "catalog says X"},
		{"update marked R", "note_update", r, "catalog says U"},
		{"unlisted tool", "recipe_find", r, "not in wantToolClass"},
		{"unlisted create marked R", "recipe_create", r, "verb means C"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probs := strings.Join(annotationProblems(tt.tool, tt.h), "; ")
			if tt.want == "" {
				if probs != "" {
					t.Fatalf("unexpected problems: %s", probs)
				}
				return
			}
			if !strings.Contains(probs, tt.want) {
				t.Fatalf("problems %q do not mention %q", probs, tt.want)
			}
		})
	}
}

// TestReadOnlyTokenScopeEveryTool drives the #4405 scope check
// (requireWriteScope, through server.ContextWithClaims) over the whole
// registry: a read-only agent token reaches the handler of every
// readOnlyHint tool and is refused on every other tool. Handlers are swapped
// for stubs (re-adding the same mcp.Tool keeps its annotations), so no
// database is needed.
func TestReadOnlyTokenScopeEveryTool(t *testing.T) {
	srv := newRegisteredServer(t)
	ran := map[string]bool{}
	for name, st := range srv.MCPServer().ListTools() {
		srv.MCPServer().AddTool(st.Tool, func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			ran[req.Params.Name] = true
			return mcp.NewToolResultText("stub " + name), nil
		})
	}

	readOnly := server.ContextWithClaims(context.Background(), &auth.Claims{
		UserID: "u", HouseholdID: "h", AgentTokenID: "t", Scopes: []string{auth.ScopeRead},
	})
	readWrite := server.ContextWithClaims(context.Background(), &auth.Claims{
		UserID: "u", HouseholdID: "h", AgentTokenID: "t", Scopes: []string{auth.ScopeRead, auth.ScopeWrite},
	})

	listed := listedHints(t, srv)
	names := make([]string, 0, len(listed))
	for name := range listed {
		names = append(names, name)
	}
	slices.Sort(names)

	var sawRead, sawWrite bool
	for _, name := range names {
		h := listed[name]
		readOnlyTool := h.ReadOnlyHint != nil && *h.ReadOnlyHint && (h.DestructiveHint == nil || !*h.DestructiveHint)
		params := map[string]any{"name": name, "arguments": map[string]any{}}

		clear(ran)
		out := string(rpc(t, readOnly, srv, string(mcp.MethodToolsCall), params))
		refused := strings.Contains(out, "this agent token is read-only")
		switch {
		case readOnlyTool && (refused || !ran[name]):
			t.Errorf("%s is readOnlyHint, but a read-only token was refused: %s", name, out)
		case !readOnlyTool && (!refused || ran[name]):
			t.Errorf("%s can change data, but a read-only token reached its handler: %s", name, out)
		}
		sawRead = sawRead || readOnlyTool
		sawWrite = sawWrite || !readOnlyTool

		// A write-scoped token reaches every handler.
		clear(ran)
		out = string(rpc(t, readWrite, srv, string(mcp.MethodToolsCall), params))
		if !ran[name] {
			t.Errorf("%s: a write-scoped token did not reach the handler: %s", name, out)
		}
	}
	if !sawRead || !sawWrite {
		t.Fatalf("registry needs both read and write tools to test the scope check (read=%v write=%v)", sawRead, sawWrite)
	}
}
