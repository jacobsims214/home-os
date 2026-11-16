package server_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// TestDropWriteHints: a caller who can't write gets no next hint naming a
// write tool, from any tool (#4771); a caller who can write gets them all.
func TestDropWriteHints(t *testing.T) {
	readOnly := func(name string) mcp.Tool {
		return mcp.NewTool(name, mcp.WithDescription("r"), mcp.WithReadOnlyHintAnnotation(true), mcp.WithDestructiveHintAnnotation(false))
	}
	all := []string{
		"Read it with thing_get thing_id=1.",
		"Schedule it with thing_create name=… due_date=….",
		"Remove it with thing_delete thing_id=1.",
		"Pass next_cursor as cursor.",
	}
	readHints := []string{all[0], all[3]}

	tests := []struct {
		name   string
		claims *auth.Claims
		want   []string
	}{
		{"owner", &auth.Claims{UserID: "u", HouseholdID: "h", Role: "owner"}, all},
		{"family_member", &auth.Claims{UserID: "u", HouseholdID: "h", Role: "family_member"}, all},
		{"viewer", &auth.Claims{UserID: "u", HouseholdID: "h", Role: "viewer"}, readHints},
		{"housesitter", &auth.Claims{UserID: "u", HouseholdID: "h", Role: "housesitter"}, readHints},
		{"vendor", &auth.Claims{UserID: "u", HouseholdID: "h", Role: "vendor"}, readHints},
		{"read token", &auth.Claims{UserID: "u", HouseholdID: "h", Role: "owner", AgentTokenID: "t", Scopes: []string{auth.ScopeRead}}, readHints},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := server.NewServer(testConfig(), nil, testutil.NewTokenIssuer(t).Verifier(nil))
			s.RegisterTool("thing_get", readOnly("thing_get"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return respond.OK("Thing 1.", map[string]any{"id": "1"}, all...), nil
			})
			s.RegisterTool("thing_find", readOnly("thing_find"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return respond.List("Found 1 thing.", []string{"1"}, respond.Page{Total: 1}, all...), nil
			})
			s.RegisterTool("thing_create", mcp.NewTool("thing_create", mcp.WithDescription("w")), nil)
			s.RegisterTool("thing_delete", mcp.NewTool("thing_delete", mcp.WithDescription("d"),
				mcp.WithReadOnlyHintAnnotation(false), mcp.WithDestructiveHintAnnotation(true)), nil)

			ctx := server.ContextWithClaims(context.Background(), tc.claims)
			for _, tool := range []string{"thing_get", "thing_find"} {
				msg, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0", "id": 1, "method": mcp.MethodToolsCall,
					"params": map[string]any{"name": tool, "arguments": map[string]any{}},
				})
				out, _ := json.Marshal(s.MCPServer().HandleMessage(ctx, msg))
				var rpc struct {
					Result struct {
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
						Structured struct {
							OK   bool     `json:"ok"`
							Next []string `json:"next"`
						} `json:"structuredContent"`
					} `json:"result"`
				}
				if err := json.Unmarshal(out, &rpc); err != nil || len(rpc.Result.Content) != 1 {
					t.Fatalf("%s: decode %s: %v", tool, out, err)
				}
				var text struct {
					OK   bool     `json:"ok"`
					Next []string `json:"next"`
				}
				if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &text); err != nil {
					t.Fatalf("%s: text content is not the envelope: %v", tool, err)
				}
				if !text.OK || !slices.Equal(text.Next, tc.want) {
					t.Errorf("%s text next = %q, want %q", tool, text.Next, tc.want)
				}
				if !rpc.Result.Structured.OK || !slices.Equal(rpc.Result.Structured.Next, tc.want) {
					t.Errorf("%s structuredContent next = %q, want %q", tool, rpc.Result.Structured.Next, tc.want)
				}
			}
		})
	}
}
