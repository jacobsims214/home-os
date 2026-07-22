package caldav

import "testing"

// TestNormalizeETag covers the quote/weak-prefix stripping used to compare
// client-supplied If-Match values against the bare hex ETags the calendar
// service stores in the database.
func TestNormalizeETag(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare hex", "1718467200000000000", "1718467200000000000"},
		{"quoted strong", `"1718467200000000000"`, "1718467200000000000"},
		{"quoted weak", `W/"1718467200000000000"`, "1718467200000000000"},
		{"surrounding whitespace", `  "1718467200000000000"  `, "1718467200000000000"},
		{"weak with whitespace", `  W/"1718467200000000000"  `, "1718467200000000000"},
		{"empty string", "", ""},
		{"only quotes", `""`, ""},
		{"interior quotes preserved", `"ab"cd"`, `ab"cd`},
		{"single quote not stripped", `"abc`, `"abc`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeETag(tc.in); got != tc.want {
				t.Errorf("normalizeETag(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCheckPUTPreconditions covers the If-Match / If-None-Match decision
// matrix. The function is pure (no DB) so we can exercise every branch.
func TestCheckPUTPreconditions(t *testing.T) {
	const currentETag = "1718467200000000000"

	cases := []struct {
		name         string
		ifMatch      string
		ifNoneMatch  string
		currentETag  string
		exists       bool
		want         putPreconditionResult
	}{
		// No preconditions: always pass (the "no regression" criterion).
		{
			name:        "no headers, missing resource",
			ifMatch:     "",
			ifNoneMatch: "",
			currentETag: "",
			exists:      false,
			want:        preconditionPass,
		},
		{
			name:        "no headers, existing resource",
			ifMatch:     "",
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},

		// If-None-Match: * — create-only guard.
		{
			name:        "If-None-Match:* on missing resource passes (create allowed)",
			ifMatch:     "",
			ifNoneMatch: "*",
			currentETag: "",
			exists:      false,
			want:        preconditionPass,
		},
		{
			name:        "If-None-Match:* on existing resource fails (exists)",
			ifMatch:     "",
			ifNoneMatch: "*",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionFailExists,
		},

		// If-Match: * — matches any existing resource.
		{
			name:        "If-Match:* on existing resource passes",
			ifMatch:     "*",
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},
		{
			name:        "If-Match:* on missing resource fails (missing)",
			ifMatch:     "*",
			ifNoneMatch: "",
			currentETag: "",
			exists:      false,
			want:        preconditionFailMissing,
		},

		// If-Match with a specific ETag — strong comparison.
		{
			name:        "If-Match quoted ETag matches current",
			ifMatch:     `"1718467200000000000"`,
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},
		{
			name:        "If-Match bare ETag matches current",
			ifMatch:     "1718467200000000000",
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},
		{
			name:        "If-Match weak ETag matches current (normalized)",
			ifMatch:     `W/"1718467200000000000"`,
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},
		{
			name:        "If-Match ETag mismatch fails",
			ifMatch:     `"deadbeef"`,
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionFailMismatch,
		},
		{
			name:        "If-Match on missing resource fails (missing)",
			ifMatch:     `"1718467200000000000"`,
			ifNoneMatch: "",
			currentETag: "",
			exists:      false,
			want:        preconditionFailMissing,
		},

		// Comma-separated If-Match list (RFC 7232 allows multiple ETags).
		{
			name:        "If-Match list with one matching ETag passes",
			ifMatch:     `"deadbeef", "1718467200000000000"`,
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},
		{
			name:        "If-Match list with no matching ETag fails",
			ifMatch:     `"deadbeef", "cafef00d"`,
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionFailMismatch,
		},

		// If-None-Match:* is evaluated before If-Match. If the resource
		// exists, If-None-Match:* short-circuits to preconditionFailExists
		// regardless of what If-Match says.
		{
			name:        "If-None-Match:* takes precedence over If-Match when resource exists",
			ifMatch:     `"1718467200000000000"`,
			ifNoneMatch: "*",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionFailExists,
		},

		// Non-wildcard If-None-Match on a PUT is not meaningful for CalDAV
		// optimistic concurrency and is ignored — the PUT proceeds.
		{
			name:        "non-wildcard If-None-Match ignored",
			ifMatch:     "",
			ifNoneMatch: `"some-other-etag"`,
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},

		// Whitespace-only headers are treated as absent.
		{
			name:        "whitespace-only If-Match treated as absent",
			ifMatch:     "   ",
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkPUTPreconditions(tc.ifMatch, tc.ifNoneMatch, tc.currentETag, tc.exists)
			if got != tc.want {
				t.Errorf("checkPUTPreconditions(ifMatch=%q, ifNoneMatch=%q, currentETag=%q, exists=%v) = %d, want %d",
					tc.ifMatch, tc.ifNoneMatch, tc.currentETag, tc.exists, got, tc.want)
			}
		})
	}
}
