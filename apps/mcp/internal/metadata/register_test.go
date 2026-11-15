package metadata

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"home-os/mcp/internal/config"
)

func TestValidateRedirectURIs(t *testing.T) {
	tooMany := make([]string, MaxRedirectURIs+1)
	for i := range tooMany {
		tooMany[i] = "http://127.0.0.1/cb"
	}
	maxOK := tooMany[:MaxRedirectURIs]

	tests := []struct {
		name    string
		uris    []string
		wantErr bool
	}{
		// Accepted: loopback HTTP, any port and path.
		{"ipv4 loopback", []string{"http://127.0.0.1/callback"}, false},
		{"ipv4 loopback with port", []string{"http://127.0.0.1:54321/oauth/callback"}, false},
		{"ipv6 loopback", []string{"http://[::1]/cb"}, false},
		{"ipv6 loopback with port", []string{"http://[::1]:8080/cb"}, false},
		{"localhost", []string{"http://localhost/cb"}, false},
		{"localhost with port", []string{"http://localhost:19876/mcp/oauth/callback"}, false},
		{"localhost upper case", []string{"HTTP://LOCALHOST:3000/cb"}, false},
		{"localhost with query", []string{"http://localhost:3000/cb?x=1"}, false},
		// Accepted: reverse-DNS private-use schemes (RFC 8252 7.1).
		{"reverse-dns scheme", []string{"com.example.app:/oauth2redirect"}, false},
		{"reverse-dns scheme with authority", []string{"io.simstech.homeos://callback"}, false},
		{"mixed valid list", []string{"http://127.0.0.1:1234/cb", "com.example.app:/cb"}, false},
		{"max uris", maxOK, false},

		// Rejected: list size.
		{"nil list", nil, true},
		{"empty list", []string{}, true},
		{"too many uris", tooMany, true},
		{"empty string", []string{""}, true},

		// Rejected: anything that can deliver the code to a web server.
		{"https", []string{"https://evil.example/cb"}, true},
		{"https localhost", []string{"https://localhost/cb"}, true},
		{"non-loopback http", []string{"http://evil.example/cb"}, true},
		{"http private ip", []string{"http://192.168.1.10/cb"}, true},
		{"http 127.0.0.2", []string{"http://127.0.0.2/cb"}, true},
		{"look-alike localhost", []string{"http://localhost.evil.com/cb"}, true},
		{"look-alike 127.0.0.1", []string{"http://127.0.0.1.evil.com/cb"}, true},
		{"look-alike ipv6", []string{"http://[::1].evil.com/cb"}, true},
		{"one bad among good", []string{"http://localhost/cb", "https://evil.example/cb"}, true},

		// Rejected: userinfo and fragments.
		{"userinfo localhost", []string{"http://user@localhost/cb"}, true},
		{"userinfo hiding evil host", []string{"http://localhost@evil.example/cb"}, true},
		{"userinfo custom scheme", []string{"com.example.app://user:pw@host/cb"}, true},
		{"fragment", []string{"http://localhost/cb#frag"}, true},
		{"empty fragment", []string{"http://localhost/cb#"}, true},

		// Rejected: dangerous or non-reverse-DNS schemes, relative and opaque.
		{"javascript", []string{"javascript:alert(1)"}, true},
		{"javascript upper case", []string{"JavaScript:alert(1)"}, true},
		{"data", []string{"data:text/html,<script>alert(1)</script>"}, true},
		{"file", []string{"file:///etc/passwd"}, true},
		{"vbscript", []string{"vbscript:msgbox(1)"}, true},
		{"custom scheme without dot", []string{"myapp://callback"}, true},
		{"ftp", []string{"ftp://evil.example/cb"}, true},
		{"relative", []string{"/callback"}, true},
		{"scheme-relative", []string{"//evil.example/cb"}, true},
		{"opaque http", []string{"http:localhost/cb"}, true},
		{"invalid port", []string{"http://localhost:abc/cb"}, true},
		{"control character", []string{"http://localhost/\ncb"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRedirectURIs(tt.uris)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRedirectURIs(%q) error = %v, wantErr %v", tt.uris, err, tt.wantErr)
			}
		})
	}
}

// TestHandler_RegisterRejectsBadRedirectURIs: /register answers a bad
// redirect URI list with 400 invalid_redirect_uri before it contacts Dex,
// and lets a valid list through to the Dex call (unreachable here, so 500).
func TestHandler_RegisterRejectsBadRedirectURIs(t *testing.T) {
	cfg := &config.Config{
		PublicBaseURL: "https://home.example",
		DexGRPCAddr:   "127.0.0.1:1", // nothing listens: a Dex call fails fast
	}
	ts := httptest.NewServer(NewHandler(cfg))
	t.Cleanup(ts.Close)
	client := &http.Client{Timeout: 10 * time.Second}

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantError  string
	}{
		{"https", `{"redirect_uris":["https://attacker.example/cb"]}`, http.StatusBadRequest, "invalid_redirect_uri"},
		{"look-alike", `{"redirect_uris":["http://localhost.evil.com/cb"]}`, http.StatusBadRequest, "invalid_redirect_uri"},
		{"userinfo", `{"redirect_uris":["http://user@localhost/cb"]}`, http.StatusBadRequest, "invalid_redirect_uri"},
		{"javascript", `{"redirect_uris":["javascript:alert(1)"]}`, http.StatusBadRequest, "invalid_redirect_uri"},
		{"empty list", `{"redirect_uris":[]}`, http.StatusBadRequest, "invalid_redirect_uri"},
		{"missing list", `{"client_name":"x"}`, http.StatusBadRequest, "invalid_redirect_uri"},
		{"valid loopback reaches dex", `{"redirect_uris":["http://127.0.0.1:5555/cb"]}`, http.StatusInternalServerError, "server_error"},
		{"valid custom scheme reaches dex", `{"redirect_uris":["com.example.app:/cb"]}`, http.StatusInternalServerError, "server_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := client.Post(ts.URL+"/register", "application/json", strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Error != tt.wantError {
				t.Errorf("error = %q, want %q", body.Error, tt.wantError)
			}
		})
	}
}
