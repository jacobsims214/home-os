package metadata

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"home-os/mcp/internal/config"
)

func TestResourceMetadataURL(t *testing.T) {
	tests := []struct {
		cfg  config.Config
		want string
	}{
		{config.Config{ResourceURL: "https://home.example/mcp"}, "https://home.example/.well-known/oauth-protected-resource/mcp"},
		{config.Config{ResourceURL: "https://mcp.example"}, "https://mcp.example/.well-known/oauth-protected-resource"},
		{config.Config{PublicBaseURL: "http://localhost:8000"}, "http://localhost:8000/.well-known/oauth-protected-resource/mcp"},
	}
	for _, tt := range tests {
		if got := ResourceMetadataURL(&tt.cfg); got != tt.want {
			t.Errorf("ResourceMetadataURL(%+v) = %q, want %q", tt.cfg, got, tt.want)
		}
	}
}

// TestHandler_Metadata: the metadata server (Envoy's target for
// /.well-known/oauth-*) serves the protected resource document at both paths
// with the configured resource and issuer, and the authorization server
// document with that issuer at the bare and the issuer path-inserted URL.
func TestHandler_Metadata(t *testing.T) {
	cfg := &config.Config{
		PublicBaseURL: "https://home.example",
		ResourceURL:   "https://home.example/mcp",
		DexIssuer:     "https://home.example/dex",
	}
	ts := httptest.NewServer(NewHandler(cfg))
	t.Cleanup(ts.Close)

	get := func(t *testing.T, path string, out any) {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", path, resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}

	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		var doc struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
			ClientID             string   `json:"client_id"`
		}
		get(t, path, &doc)
		if doc.Resource != cfg.ResourceURL || len(doc.AuthorizationServers) != 1 ||
			doc.AuthorizationServers[0] != cfg.DexIssuer || doc.ClientID != PreRegisteredClientID {
			t.Errorf("%s = %+v", path, doc)
		}
	}

	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-authorization-server/dex"} {
		var doc struct {
			Issuer                string `json:"issuer"`
			AuthorizationEndpoint string `json:"authorization_endpoint"`
			RegistrationEndpoint  string `json:"registration_endpoint"`
		}
		get(t, path, &doc)
		if doc.Issuer != cfg.DexIssuer || doc.AuthorizationEndpoint != "https://home.example/authorize" ||
			doc.RegistrationEndpoint != "https://home.example/register" {
			t.Errorf("%s = %+v", path, doc)
		}
	}
}
