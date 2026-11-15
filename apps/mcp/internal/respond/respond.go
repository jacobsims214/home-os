// Package respond builds the results every Home OS MCP tool returns.
//
// Small local models do best when every call answers three questions:
// what happened, what the data is, and what to do next. Every result built
// here therefore carries
//
//   - structuredContent: a JSON envelope with "ok", a one-line "summary",
//     the payload and optional "next" hints, and
//   - one text content block holding exactly the same JSON, for clients
//     that ignore structuredContent (MCP spec: tools SHOULD mirror it).
//
// Failures the model can correct (bad input, unknown id, business rules,
// upstream outages) are tool results with isError=true, never protocol
// errors: they say what went wrong, how to fix it, and give a valid example.
//
// Declare the matching outputSchema on the tool so clients can validate:
//
//	mcp.NewTool("notes_get", ..., respond.Schema[note.Note]())
//	return respond.OK("Found note \"Groceries\".", n, "Use notes_update with note_id to edit it."), nil
//
//	mcp.NewTool("notes_search", ..., respond.ListSchema[note.Note]())
//	return respond.List("Found 20 of 143 notes.", notes, respond.Page{Total: 143,
//	    Narrow: "Pass query or tag to narrow the results."}), nil
//
//	return respond.Invalid("due_date", "due_date \"next week\" is not a date.",
//	    "Pass due_date as YYYY-MM-DD.", map[string]any{"due_date": "2026-12-01"}), nil
//
// Error envelopes only set "ok", "summary" and "error", which every
// outputSchema built by Schema and ListSchema accepts, so an error result
// validates against whichever tool returned it.
package respond

import (
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// Envelope is the structuredContent of a single-object result (OK) and of
// every error result.
type Envelope[T any] struct {
	OK      bool     `json:"ok" jsonschema_description:"true when the call succeeded. When false read error.fix and retry."`
	Summary string   `json:"summary" jsonschema_description:"One-line plain-language description of what was found or changed."`
	Data    *T       `json:"data,omitempty" jsonschema_description:"The result object. Absent when ok is false."`
	Next    []string `json:"next,omitempty" jsonschema_description:"Suggested next steps."`
	Error   *Error   `json:"error,omitempty" jsonschema_description:"What went wrong and how to fix it. Present only when ok is false."`
}

// ListEnvelope is the structuredContent of a list result (List).
type ListEnvelope[T any] struct {
	OK        bool     `json:"ok" jsonschema_description:"true when the call succeeded. When false read error.fix and retry."`
	Summary   string   `json:"summary" jsonschema_description:"One-line plain-language description of what was found."`
	Items     []T      `json:"items,omitzero" jsonschema_description:"The returned items. Always present (possibly empty) when ok is true."`
	Returned  *int     `json:"returned,omitempty" jsonschema_description:"Number of items returned in this response."`
	Total     *int     `json:"total,omitempty" jsonschema_description:"Number of items that matched in total."`
	Truncated bool     `json:"truncated,omitempty" jsonschema_description:"true when more items matched than were returned."`
	Narrow    string   `json:"narrow,omitempty" jsonschema_description:"How to narrow or page the query when truncated."`
	Next      []string `json:"next,omitempty" jsonschema_description:"Suggested next steps."`
	Error     *Error   `json:"error,omitempty" jsonschema_description:"What went wrong and how to fix it. Present only when ok is false."`
}

// Error is the corrective payload of a failed call.
type Error struct {
	Message string         `json:"message" jsonschema_description:"What went wrong."`
	Fix     string         `json:"fix" jsonschema_description:"How to fix the call."`
	Field   string         `json:"field,omitempty" jsonschema_description:"The argument that caused the error."`
	Example map[string]any `json:"example,omitempty" jsonschema_description:"Example of valid arguments."`
}

// Page describes how a list relates to everything that matched.
type Page struct {
	// Total is the number of items that matched. Zero or less than
	// len(items) means "exactly the items returned".
	Total int
	// Narrow tells the model how to narrow or page the query. It is only
	// emitted when the list is truncated; a generic hint is used if empty.
	Narrow string
}

// defaultNarrow is used when a truncated list gives no Narrow hint.
const defaultNarrow = "More items matched than were returned. Add filters or a more specific query to narrow the results."

// Schema returns the outputSchema tool option matching OK (and every error
// result) for payload type T.
func Schema[T any]() mcp.ToolOption { return mcp.WithOutputSchema[Envelope[T]]() }

// ListSchema returns the outputSchema tool option matching List (and every
// error result) for item type T.
func ListSchema[T any]() mcp.ToolOption { return mcp.WithOutputSchema[ListEnvelope[T]]() }

// OK is a successful single-object result: summary says in one line what was
// found or changed, data is the object, next are optional next-step hints.
func OK[T any](summary string, data T, next ...string) *mcp.CallToolResult {
	return build(Envelope[T]{OK: true, Summary: summary, Data: &data, Next: next}, false)
}

// List is a successful list result. Items beyond what is returned are
// reported through page: when page.Total exceeds len(items) the result is
// marked truncated and carries page.Narrow (or a generic hint).
func List[T any](summary string, items []T, page Page, next ...string) *mcp.CallToolResult {
	if items == nil {
		items = []T{}
	}
	returned, total := len(items), page.Total
	if total < returned {
		total = returned
	}
	env := ListEnvelope[T]{
		OK: true, Summary: summary, Items: items,
		Returned: &returned, Total: &total, Next: next,
	}
	if total > returned {
		env.Truncated = true
		env.Narrow = page.Narrow
		if env.Narrow == "" {
			env.Narrow = defaultNarrow
		}
	}
	return build(env, false)
}

// Fail is an isError result built from e. Its summary is e.Message.
func Fail(e Error) *mcp.CallToolResult {
	return build(Envelope[struct{}]{Summary: e.Message, Error: &e}, true)
}

// Invalid reports a bad argument: which field, what is wrong with it, how
// to fix it, and an example of valid arguments (may be nil).
func Invalid(field, message, fix string, example map[string]any) *mcp.CallToolResult {
	return Fail(Error{Message: message, Fix: fix, Field: field, Example: example})
}

// NotFound reports that the thing the model referred to does not exist (or
// is not visible to the caller). fix should name the tool that finds valid
// ids, e.g. "Call notes_search to find the note_id.".
func NotFound(what, id, fix string) *mcp.CallToolResult {
	return Fail(Error{Message: fmt.Sprintf("No %s with id %q was found.", what, id), Fix: fix})
}

// Failed reports a server-side or upstream failure while doing action
// (e.g. "list notes"). The model is told to retry once and then tell the
// user, rather than to change its arguments.
func Failed(action string, err error) *mcp.CallToolResult {
	msg := "Could not " + action + "."
	if err != nil {
		msg = fmt.Sprintf("Could not %s: %v", action, err)
	}
	return Fail(Error{Message: msg, Fix: "Retry the same call once. If it fails again, tell the user it is unavailable right now."})
}

// build marshals env once for the text block and keeps the typed value as
// structuredContent; json.Marshal is deterministic, so both serialize to the
// same bytes on the wire.
func build(env any, isError bool) *mcp.CallToolResult {
	b, err := json.Marshal(env)
	if err != nil {
		e := Error{Message: "Could not encode the result: " + err.Error(), Fix: "Retry the same call once. If it fails again, tell the user it is unavailable right now."}
		fallback := Envelope[struct{}]{Summary: e.Message, Error: &e}
		b, _ = json.Marshal(fallback) // cannot fail: only strings
		env, isError = fallback, true
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{mcp.NewTextContent(string(b))},
		StructuredContent: env,
		IsError:           isError,
	}
}
