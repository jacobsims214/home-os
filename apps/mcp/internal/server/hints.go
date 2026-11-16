package server

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// toolNameRE matches snake_case words in a hint; the registered ones are
// tool names.
var toolNameRE = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)

// namesWriteTool reports whether hint names a registered tool that may
// change data (not readOnlyHint, or destructive).
func (s *Server) namesWriteTool(hint string) bool {
	for _, w := range toolNameRE.FindAllString(hint, -1) {
		if s.mcpServer.GetTool(w) != nil && !s.toolIsReadOnly(w) {
			return true
		}
	}
	return false
}

// dropWriteHints removes, for a caller who can't write (a viewer,
// housesitter or vendor, or a read-only agent token), every next-step hint
// that names a write tool (#4771). Small models follow next hints
// literally; a hint the caller can't act on is a call requireWriteScope
// always refuses. Tools may still gate their own hints by role; this is the
// backstop that holds for every tool.
func (s *Server) dropWriteHints(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := next(ctx, req)
		if c := ClaimsFromContext(ctx); err == nil && c != nil && !c.CanWrite() {
			s.withoutWriteHints(res)
		}
		return res, err
	}
}

// withoutWriteHints filters the Next []string field of res's structured
// content (respond.Envelope / respond.ListEnvelope) and re-renders its
// text content from it, as respond.build does. A result without a Next
// field, or with nothing to drop, is left as it is.
func (s *Server) withoutWriteHints(res *mcp.CallToolResult) {
	if res == nil || res.StructuredContent == nil {
		return
	}
	v := reflect.ValueOf(res.StructuredContent)
	if v.Kind() != reflect.Struct {
		return
	}
	f := v.FieldByName("Next")
	if !f.IsValid() || f.Type() != reflect.TypeFor[[]string]() || f.Len() == 0 {
		return
	}
	hints := f.Interface().([]string)
	kept := make([]string, 0, len(hints))
	for _, h := range hints {
		if !s.namesWriteTool(h) {
			kept = append(kept, h)
		}
	}
	if len(kept) == len(hints) {
		return
	}
	if len(kept) == 0 {
		kept = nil // omitempty: no "next" at all
	}
	cp := reflect.New(v.Type()).Elem()
	cp.Set(v)
	cp.FieldByName("Next").Set(reflect.ValueOf(kept))
	b, err := json.Marshal(cp.Interface())
	if err != nil {
		return // keep the original result rather than break it
	}
	res.StructuredContent = cp.Interface()
	if len(res.Content) == 1 {
		if _, ok := res.Content[0].(mcp.TextContent); ok {
			res.Content[0] = mcp.NewTextContent(string(b))
		}
	}
}
