// Package tls generates self-signed TLS certificates for local development.
// Tests focus on the empty/nil hosts guard added to prevent a startup panic
// when CALDAV_TLS_HOSTS parses to zero non-empty entries.
package tls

import "testing"

// TestGenerateSelfSignedCert_EmptyHosts verifies that the generator returns a
// non-nil error (and does NOT panic) when called with nil or an empty slice.
// Regression guard for the index-out-of-range panic that hosts[0] caused
// before the len(hosts) == 0 guard was added.
func TestGenerateSelfSignedCert_EmptyHosts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		hosts []string
	}{
		{"nil slice", nil},
		{"empty slice", []string{}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			certPEM, keyPEM, err := GenerateSelfSignedCert(tc.hosts)

			if err == nil {
				t.Fatalf("GenerateSelfSignedCert(%v) returned nil error; expected a non-nil error for empty hosts", tc.hosts)
			}
			if certPEM != nil {
				t.Errorf("GenerateSelfSignedCert(%v) returned non-nil certPEM; expected nil on error", tc.hosts)
			}
			if keyPEM != nil {
				t.Errorf("GenerateSelfSignedCert(%v) returned non-nil keyPEM; expected nil on error", tc.hosts)
			}
		})
	}
}

// TestGenerateSelfSignedCert_ValidHosts is a smoke test that the happy path
// still works after the guard was added — the generator must return non-nil
// PEM bytes and a nil error when given at least one host.
func TestGenerateSelfSignedCert_ValidHosts(t *testing.T) {
	t.Parallel()

	certPEM, keyPEM, err := GenerateSelfSignedCert([]string{"localhost", "127.0.0.1"})
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert returned unexpected error: %v", err)
	}
	if len(certPEM) == 0 {
		t.Error("GenerateSelfSignedCert returned empty certPEM for valid hosts")
	}
	if len(keyPEM) == 0 {
		t.Error("GenerateSelfSignedCert returned empty keyPEM for valid hosts")
	}
}
