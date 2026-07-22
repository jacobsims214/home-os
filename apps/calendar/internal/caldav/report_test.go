package caldav

import "testing"

// TestFormatAndParseSyncToken verifies the round-trip between formatSyncToken
// and parseSyncTokenRevision for the boundary revisions sync-collection
// depends on (0 = no changes yet, and a typical mid-stream revision).
func TestFormatAndParseSyncToken(t *testing.T) {
	cases := []struct {
		name string
		rev  int64
	}{
		{"zero revision (initial state)", 0},
		{"one", 1},
		{"large revision", 9223372036854775807},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := formatSyncToken(tc.rev)
			got, ok := parseSyncTokenRevision(tok)
			if !ok {
				t.Fatalf("parseSyncTokenRevision(%q) = _, false, want true", tok)
			}
			if got != tc.rev {
				t.Errorf("parseSyncTokenRevision(%q) = %d, want %d", tok, got, tc.rev)
			}
		})
	}
}

// TestParseSyncTokenRevisionRejectsForeign confirms that tokens we did not
// emit (old CTag-based tokens, tokens from other servers, garbage) are
// rejected so the handler can fall back to a full initial sync.
func TestParseSyncTokenRevisionRejectsForeign(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"empty string", ""},
		{"old ctag token", "http://home-os.local/ns/sync/abc-uuid-ctag"},
		{"foreign server", "https://example.com/dav/sync/42"},
		{"bare integer", "42"},
		{"non-numeric revision", syncTokenPrefix + "not-a-number"},
		{"revision with trailing garbage", syncTokenPrefix + "42abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := parseSyncTokenRevision(tc.token); ok {
				t.Errorf("parseSyncTokenRevision(%q) = _, true, want false", tc.token)
			}
		})
	}
}

// TestExtractSyncToken covers the three request shapes Apple Calendar sends:
// no token (initial sync), an empty self-closing token (also initial sync),
// and a populated token (incremental sync). Namespace prefixes vary between
// clients so the matcher must rely on the local name only.
func TestExtractSyncToken(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "no sync-token element",
			body: `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:"><D:prop/></D:sync-collection>`,
			want: "",
		},
		{
			name: "empty self-closing token",
			body: `<?xml version="1.0"?><sync-collection xmlns="DAV:"><sync-token/><prop/></sync-collection>`,
			want: "",
		},
		{
			name: "empty paired token",
			body: `<?xml version="1.0"?><sync-collection><sync-token></sync-token></sync-collection>`,
			want: "",
		},
		{
			name: "populated token with D prefix",
			body: `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:"><D:sync-token>http://home-os.local/ns/sync/rev/42</D:sync-token></D:sync-collection>`,
			want: "http://home-os.local/ns/sync/rev/42",
		},
		{
			name: "populated token with whitespace",
			body: `<?xml version="1.0"?><sync-collection><sync-token>  http://home-os.local/ns/sync/rev/7  </sync-token></sync-collection>`,
			want: "http://home-os.local/ns/sync/rev/7",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractSyncToken(tc.body); got != tc.want {
				t.Errorf("extractSyncToken() = %q, want %q", got, tc.want)
			}
		})
	}
}
