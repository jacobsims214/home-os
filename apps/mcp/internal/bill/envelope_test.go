package bill

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/mark3labs/mcp-go/mcp"
)

// toolDefs are the bill tools' definitions; they don't touch the pool.
func toolDefs() []mcp.Tool {
	defs := []func() (string, mcp.Tool, toolHandler){
		func() (string, mcp.Tool, toolHandler) { return NewBillFindTool(nil) },
		func() (string, mcp.Tool, toolHandler) { return NewBillCreateTool(nil) },
		func() (string, mcp.Tool, toolHandler) { return NewBillUpdateTool(nil) },
		func() (string, mcp.Tool, toolHandler) { return NewBillDeleteTool(nil) },
		func() (string, mcp.Tool, toolHandler) { return NewBillPayTool(nil) },
	}
	out := make([]mcp.Tool, 0, len(defs))
	for _, d := range defs {
		_, tool, _ := d()
		out = append(out, tool)
	}
	return out
}

var (
	schemasOnce sync.Once
	schemas     map[string]*jsonschema.Resolved
	schemasErr  error
)

// outputSchema resolves the outputSchema the named tool advertises.
func outputSchema(t *testing.T, name string) *jsonschema.Resolved {
	t.Helper()
	schemasOnce.Do(func() {
		schemas = map[string]*jsonschema.Resolved{}
		for _, tool := range toolDefs() {
			b, err := json.Marshal(tool)
			if err != nil {
				schemasErr = err
				return
			}
			var raw struct {
				OutputSchema json.RawMessage `json:"outputSchema"`
			}
			if err := json.Unmarshal(b, &raw); err != nil {
				schemasErr = err
				return
			}
			if len(raw.OutputSchema) == 0 {
				schemasErr = fmt.Errorf("tool %s declares no outputSchema", tool.Name)
				return
			}
			var s jsonschema.Schema
			if err := json.Unmarshal(raw.OutputSchema, &s); err != nil {
				schemasErr = err
				return
			}
			rs, err := s.Resolve(nil)
			if err != nil {
				schemasErr = fmt.Errorf("resolve %s outputSchema: %w", tool.Name, err)
				return
			}
			schemas[tool.Name] = rs
		}
	})
	if schemasErr != nil {
		t.Fatalf("output schemas: %v", schemasErr)
	}
	rs := schemas[name]
	if rs == nil {
		t.Fatalf("no bill tool named %q", name)
	}
	return rs
}

// checkShape asserts the respond invariants on a real result: one text
// block whose JSON equals structuredContent, valid against the tool's
// advertised outputSchema (errors included).
func checkShape(t *testing.T, name string, res *mcp.CallToolResult, text string) {
	t.Helper()
	sc, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("%s: marshal structuredContent: %v", name, err)
	}
	if string(sc) != text {
		t.Fatalf("%s: text block differs from structuredContent:\n text %s\n sc   %s", name, text, sc)
	}
	var instance map[string]any
	if err := json.Unmarshal(sc, &instance); err != nil {
		t.Fatalf("%s: structuredContent is not an object: %v", name, err)
	}
	if err := outputSchema(t, name).Validate(instance); err != nil {
		t.Fatalf("%s: result does not match its outputSchema: %v\n%s", name, err, text)
	}
	if ok, _ := instance["ok"].(bool); ok == res.IsError {
		t.Fatalf("%s: ok=%v but isError=%v", name, ok, res.IsError)
	}
}

// wireEnvelope is the JSON every bill tool returns.
type wireEnvelope struct {
	OK        bool            `json:"ok"`
	Summary   string          `json:"summary"`
	Data      json.RawMessage `json:"data"`
	Items     json.RawMessage `json:"items"`
	Total     *int            `json:"total"`
	Truncated bool            `json:"truncated"`
	Narrow    string          `json:"narrow"`
	Next      []string        `json:"next"`
	Error     *struct {
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Field   string         `json:"field"`
		Example map[string]any `json:"example"`
	} `json:"error"`
}

func decodeEnvelope(t *testing.T, text string) wireEnvelope {
	t.Helper()
	var env wireEnvelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", text, err)
	}
	return env
}

// unmarshalData decodes a result's payload into v: items for a list
// result, data for a single object.
func unmarshalData(text string, v any) error {
	var env wireEnvelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		return err
	}
	if !env.OK {
		return fmt.Errorf("result is an error: %s", text)
	}
	payload := env.Data
	if len(env.Items) > 0 {
		payload = env.Items
	}
	return json.Unmarshal(payload, v)
}

// TestToolDefinitions pins every bill tool to the catalog: name, required
// params, all four annotation hints (class R/C/U/X), description lengths,
// no undeclared old names, and an outputSchema with string ids.
func TestToolDefinitions(t *testing.T) {
	type hints struct{ readOnly, destructive, idempotent bool }
	var (
		classR = hints{true, false, true}
		classC = hints{false, false, false}
		classU = hints{false, false, true}
		classX = hints{false, true, true}
	)
	want := map[string]struct {
		title    string
		required []string
		params   []string
		class    hints
	}{
		"bill_find":   {"Find bills", nil, []string{"id", "query", "category", "due_within_days", "autopay", "limit", "cursor"}, classR},
		"bill_create": {"Create bill", []string{"name", "amount", "due_day"}, []string{"name", "amount", "due_day", "category", "autopay", "contact_id", "notes"}, classC},
		"bill_update": {"Update bill", []string{"bill_id"}, []string{"bill_id", "name", "amount", "due_day", "category", "autopay", "contact_id", "notes"}, classU},
		"bill_delete": {"Delete bill", []string{"bill_id"}, []string{"bill_id"}, classX},
		"bill_pay":    {"Pay bill", []string{"bill_id"}, []string{"bill_id", "paid_date", "amount"}, classU},
	}
	tools := toolDefs()
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(tools), len(want))
	}
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			w, ok := want[tool.Name]
			if !ok {
				t.Fatalf("unexpected tool %q", tool.Name)
			}
			if fmt.Sprint(tool.InputSchema.Required) != fmt.Sprint(w.required) && !(len(w.required) == 0 && len(tool.InputSchema.Required) == 0) {
				t.Errorf("required = %v, want %v", tool.InputSchema.Required, w.required)
			}
			if len(tool.InputSchema.Properties) != len(w.params) {
				t.Errorf("params = %v, want %v", keys(tool.InputSchema.Properties), w.params)
			}
			for _, p := range w.params {
				if _, ok := tool.InputSchema.Properties[p]; !ok {
					t.Errorf("missing param %q", p)
				}
			}
			for _, old := range []string{"is_autopay", "vendor_id", "days", "entity_type", "entity_id", "payment_url"} {
				if _, ok := tool.InputSchema.Properties[old]; ok {
					t.Errorf("advertises old/hidden param %q", old)
				}
			}
			if n := len(tool.Description); n == 0 || n > 200 {
				t.Errorf("description is %d chars, want 1-200", n)
			}
			for param, schema := range tool.InputSchema.Properties {
				d, _ := schema.(map[string]any)["description"].(string)
				if d == "" || len(d) > 80 {
					t.Errorf("%s description is %d chars, want 1-80", param, len(d))
				}
			}
			a := tool.Annotations
			if a.Title != w.title {
				t.Errorf("title = %q, want %q", a.Title, w.title)
			}
			if a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
				t.Fatalf("annotations not all explicit: %+v", a)
			}
			got := hints{*a.ReadOnlyHint, *a.DestructiveHint, *a.IdempotentHint}
			if got != w.class || *a.OpenWorldHint {
				t.Errorf("hints = %+v openWorld=%v, want %+v openWorld=false", got, *a.OpenWorldHint, w.class)
			}

			// The advertised schema types ids as strings, not byte arrays.
			b, _ := json.Marshal(tool)
			var raw struct {
				OutputSchema map[string]any `json:"outputSchema"`
			}
			if err := json.Unmarshal(b, &raw); err != nil || raw.OutputSchema == nil {
				t.Fatalf("no outputSchema: %v", err)
			}
			item := raw.OutputSchema["properties"].(map[string]any)
			var bill map[string]any
			if d, ok := item["data"]; ok {
				bill = d.(map[string]any)
			} else {
				bill = item["items"].(map[string]any)["items"].(map[string]any)
			}
			props := bill["properties"].(map[string]any)
			for _, id := range []string{"id", "contact_id", "entity_id"} {
				if s := fmt.Sprint(props[id].(map[string]any)["type"]); !strings.Contains(s, "string") {
					t.Errorf("%s schema type = %s, want string", id, s)
				}
			}
			outputSchema(t, tool.Name) // resolves
		})
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
