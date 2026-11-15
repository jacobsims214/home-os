package main

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"home-os/mcp/internal/config"
	"home-os/mcp/internal/server"
)

type mcpServerTool = mcpserver.ServerTool

// snakeToken matches lower snake_case words such as bill_find or due_within_days.
var snakeToken = regexp.MustCompile(`\b[a-z]+(?:_[a-z]+)+\b`)

// toolLikePrefixes are first words that make a snake_case token a tool name:
// every catalog v2 area (registered yet or not) and the v1 verb-first names
// (list_events, get_upcoming_bills, mark_bill_paid). A token like local_date
// or unread_only starts with neither, so it is left alone.
var toolLikePrefixes = map[string]bool{
	// catalog v2 areas
	"family": true, "agenda": true, "person": true, "calendar": true, "event": true,
	"bill": true, "budget": true, "loan": true, "maintenance": true, "property": true,
	"room": true, "asset": true, "vehicle": true, "pet": true, "contact": true,
	"note": true, "list": true, "recipe": true, "meal": true, "document": true,
	"guide": true, "credential": true, "activity": true, "notification": true, "search": true,
	// v1 verb-first names
	"get": true, "create": true, "update": true, "delete": true, "mark": true,
	"check": true, "add": true, "remove": true, "set": true,
}

// removedSingleWordTools are tool names without an underscore that no longer
// exist; the regexp above can't see them.
var removedSingleWordTools = regexp.MustCompile(`\bping\b`)

// promptText renders every prompt with each argument filled in, and returns
// prompt name → description plus message text.
func promptText(t *testing.T, srv *server.Server) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, sp := range srv.MCPServer().ListPrompts() {
		args := map[string]string{}
		for _, a := range sp.Prompt.Arguments {
			args[a.Name] = "2026-11-18"
		}
		var req mcp.GetPromptRequest
		req.Params.Name = name
		req.Params.Arguments = args
		res, err := sp.Handler(context.Background(), req)
		if err != nil {
			t.Fatalf("prompt %s: %v", name, err)
		}
		var b strings.Builder
		b.WriteString(sp.Prompt.Description + "\n")
		for _, a := range sp.Prompt.Arguments {
			b.WriteString(a.Description + "\n")
		}
		b.WriteString(res.Description + "\n")
		for _, m := range res.Messages {
			if tc, ok := m.Content.(mcp.TextContent); ok {
				b.WriteString(tc.Text + "\n")
			}
		}
		out[name] = b.String()
	}
	return out
}

// paramNames returns every input property name of every registered tool, so
// calendar_id or due_within_days in a prompt isn't mistaken for a tool.
func paramNames(t *testing.T, srv *server.Server) map[string]bool {
	t.Helper()
	params := map[string]bool{}
	for _, st := range srv.MCPServer().ListTools() {
		raw := st.Tool.RawInputSchema
		if raw == nil {
			var err error
			if raw, err = json.Marshal(st.Tool.InputSchema); err != nil {
				t.Fatalf("marshal %s schema: %v", st.Tool.Name, err)
			}
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("unmarshal %s schema: %v", st.Tool.Name, err)
		}
		for p := range schema.Properties {
			params[p] = true
		}
	}
	return params
}

// resourceTemplate is the part of a resources/templates/list entry the test reads.
type resourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// resourceTemplates lists the registered resource templates through the
// server's own resources/templates/list handler.
func resourceTemplates(t *testing.T, srv *server.Server) []resourceTemplate {
	t.Helper()
	ctx := context.Background()
	srv.MCPServer().HandleMessage(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"prompts-test","version":"0"}}}`))
	resp := srv.MCPServer().HandleMessage(ctx, []byte(`{"jsonrpc":"2.0","id":2,"method":"resources/templates/list","params":{}}`))
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var out struct {
		Result struct {
			ResourceTemplates []resourceTemplate `json:"resourceTemplates"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if out.Error != nil {
		t.Fatalf("resources/templates/list: %s", out.Error)
	}
	return out.Result.ResourceTemplates
}

// unknownTools returns the tool-like names in text that aren't registered.
func unknownTools(text string, tools map[string]*mcpServerTool, params map[string]bool) []string {
	var bad []string
	for _, tok := range snakeToken.FindAllString(text, -1) {
		if tools[tok] != nil || params[tok] {
			continue
		}
		if toolLikePrefixes[strings.SplitN(tok, "_", 2)[0]] && !slices.Contains(bad, tok) {
			bad = append(bad, tok)
		}
	}
	bad = append(bad, removedSingleWordTools.FindAllString(text, -1)...)
	return bad
}

// TestPromptsNameOnlyRegisteredTools fails when a prompt or resource template
// names a tool that tools/list doesn't serve (a v1 name, a removed tool, or a
// catalog tool that isn't built yet), and checks the catalog §3 mapping.
func TestPromptsNameOnlyRegisteredTools(t *testing.T) {
	t.Parallel()
	srv := server.NewServer(&config.Config{}, nil, nil)
	registerAll(srv, &config.Config{}, nil)
	tools := srv.MCPServer().ListTools()
	params := paramNames(t, srv)

	texts := promptText(t, srv)
	wantPrompts := []string{"check-bills", "daily-briefing", "find-document", "home-upkeep", "plan-week", "schedule-event", "trip-packing"}
	var got []string
	for name := range texts {
		got = append(got, name)
	}
	sort.Strings(got)
	if !slices.Equal(got, wantPrompts) {
		t.Errorf("prompts = %v, want %v", got, wantPrompts)
	}

	// Catalog §3: the tools each prompt must use.
	mustUse := map[string][]string{
		"daily-briefing": {"agenda_get"},
		"check-bills":    {"budget_summary", "bill_find"},
		"schedule-event": {"calendar_find_free_time", "event_create"},
		"plan-week":      {"agenda_get", "calendar_find_free_time", "event_create"},
		"home-upkeep":    {"maintenance_find", "maintenance_complete"},
		"find-document":  {"document_find", "document_get"},
		"trip-packing":   {"list_find", "list_create", "list_add_items", "list_check_items"},
	}

	for name, text := range texts {
		t.Run(name, func(t *testing.T) {
			if bad := unknownTools(text, tools, params); len(bad) > 0 {
				t.Errorf("prompt %s names unregistered tools %v", name, bad)
			}
			named := 0
			for _, tok := range snakeToken.FindAllString(text, -1) {
				if tools[tok] != nil {
					named++
				}
			}
			if named == 0 {
				t.Errorf("prompt %s names no tool", name)
			}
			for _, tool := range mustUse[name] {
				if !regexp.MustCompile(`\b` + tool + `\b`).MatchString(text) {
					t.Errorf("prompt %s must use %s", name, tool)
				}
			}
		})
	}

	templates := resourceTemplates(t, srv)
	if len(templates) == 0 {
		t.Error("resources/templates/list returned no templates")
	}
	for _, rt := range templates {
		text := rt.Name + "\n" + rt.Description
		if bad := unknownTools(text, tools, params); len(bad) > 0 {
			t.Errorf("resource template %s names unregistered tools %v", rt.URITemplate, bad)
		}
	}
}

// TestUnknownToolsDetects proves the checker flags v1 names and ping while
// leaving params, output fields and registered tools alone.
func TestUnknownToolsDetects(t *testing.T) {
	t.Parallel()
	tools := map[string]*mcpServerTool{"agenda_get": {}, "bill_find": {}}
	params := map[string]bool{"due_within_days": true, "calendar_id": true}
	tests := []struct {
		text string
		want []string
	}{
		{"Call agenda_get then bill_find with due_within_days=7.", nil},
		{"Use the local_date and unread_only fields; calendar_id accepts a name.", nil},
		{"Call list_calendars and list_events.", []string{"list_calendars", "list_events"}},
		{"Call get_upcoming_bills, then mark_bill_paid.", []string{"get_upcoming_bills", "mark_bill_paid"}},
		{"Call meal_suggest for dinner.", []string{"meal_suggest"}},
		{"Call ping first.", []string{"ping"}},
	}
	for _, tt := range tests {
		if got := unknownTools(tt.text, tools, params); !slices.Equal(got, tt.want) {
			t.Errorf("unknownTools(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}
