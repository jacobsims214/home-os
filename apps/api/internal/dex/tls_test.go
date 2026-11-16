package dex

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCertFiles writes a self-signed cert+key pair and its PEM CA bundle into
// dir, returning the three paths.
func writeCertFiles(t *testing.T, dir string) (certFile, keyFile, caFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	certFile = filepath.Join(dir, "tls.crt")
	keyFile = filepath.Join(dir, "tls.key")
	caFile = filepath.Join(dir, "ca.crt")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	require.NoError(t, os.WriteFile(certFile, certPEM, 0o600))
	require.NoError(t, os.WriteFile(caFile, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyFile,
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certFile, keyFile, caFile
}

func TestTransportCredentialsInsecure(t *testing.T) {
	creds, err := TLSConfig{Insecure: true}.TransportCredentials()
	require.NoError(t, err)
	assert.Equal(t, "insecure", creds.Info().SecurityProtocol)
}

// The clients must never quietly fall back to plaintext: a missing DEX_GRPC_TLS_*
// value is an error, not an insecure connection.
func TestTransportCredentialsRequiresMaterial(t *testing.T) {
	tests := []struct {
		name string
		cfg  TLSConfig
	}{
		{"no cert", TLSConfig{KeyFile: "k", CAFile: "c"}},
		{"no key", TLSConfig{CertFile: "c", CAFile: "c"}},
		{"no ca", TLSConfig{CertFile: "c", KeyFile: "k"}},
		{"all empty", TLSConfig{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.cfg.TransportCredentials()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "DEX_GRPC_TLS_CERT")
		})
	}
}

func TestTransportCredentialsReadsMaterial(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, caFile := writeCertFiles(t, dir)

	creds, err := TLSConfig{CertFile: certFile, KeyFile: keyFile, CAFile: caFile}.TransportCredentials()
	require.NoError(t, err)
	assert.Equal(t, "tls", creds.Info().SecurityProtocol)
}

func TestTransportCredentialsRejectsMissingFiles(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, caFile := writeCertFiles(t, dir)

	_, err := TLSConfig{
		CertFile: filepath.Join(dir, "nope.crt"), KeyFile: keyFile, CAFile: caFile,
	}.TransportCredentials()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stat client cert")

	_, err = TLSConfig{CertFile: certFile, KeyFile: keyFile, CAFile: filepath.Join(dir, "nope.crt")}.TransportCredentials()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read client CA")

	// A file with no certificate in it is not a usable CA pool.
	junk := filepath.Join(dir, "junk.crt")
	require.NoError(t, os.WriteFile(junk, []byte("not a cert"), 0o600))
	_, err = TLSConfig{CertFile: certFile, KeyFile: keyFile, CAFile: junk}.TransportCredentials()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no certificates")
}

// cert-manager renews the Secret in place; a long-running pod must serve the
// new certificate without a restart, or it keeps an expired one until it dies.
func TestReloadingCertPicksUpRenewal(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, _ := writeCertFiles(t, dir)

	rc, err := newReloadingCert(certFile, keyFile)
	require.NoError(t, err)
	first, err := rc.GetClientCertificate(nil)
	require.NoError(t, err)

	// Rewrite the pair with a newer mtime, as a renewal would.
	time.Sleep(10 * time.Millisecond)
	certFile2, keyFile2, _ := writeCertFiles(t, t.TempDir())
	newCert, err := os.ReadFile(certFile2)
	require.NoError(t, err)
	newKey, err := os.ReadFile(keyFile2)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(certFile, newCert, 0o600))
	require.NoError(t, os.WriteFile(keyFile, newKey, 0o600))
	future := time.Now().Add(time.Minute)
	require.NoError(t, os.Chtimes(certFile, future, future))

	second, err := rc.GetClientCertificate(nil)
	require.NoError(t, err)
	assert.NotEqual(t, first.Leaf, second.Leaf)
	assert.NotEqual(t, first.Certificate[0], second.Certificate[0],
		"renewed certificate should replace the cached one")
}

func TestTLSConfigFromEnvDefaults(t *testing.T) {
	t.Setenv("DEX_GRPC_TLS_CERT", "")
	t.Setenv("DEX_GRPC_TLS_KEY", "")
	t.Setenv("DEX_GRPC_TLS_CA", "")
	t.Setenv("DEX_GRPC_INSECURE", "")
	t.Setenv("DEX_GRPC_TLS_SERVER_NAME", "")

	cfg := TLSConfigFromEnv()
	assert.Equal(t, "/etc/dex-grpc-tls/tls.crt", cfg.CertFile)
	assert.Equal(t, "/etc/dex-grpc-tls/tls.key", cfg.KeyFile)
	assert.Equal(t, "/etc/dex-grpc-tls/ca.crt", cfg.CAFile)
	assert.False(t, cfg.Insecure, "Insecure must be opt-in, never a default")
	assert.Empty(t, cfg.ServerName)
}

func TestTLSConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("DEX_GRPC_TLS_CERT", "/c")
	t.Setenv("DEX_GRPC_TLS_KEY", "/k")
	t.Setenv("DEX_GRPC_TLS_CA", "/a")
	t.Setenv("DEX_GRPC_INSECURE", "true")
	t.Setenv("DEX_GRPC_TLS_SERVER_NAME", "home-os-dex.home-os.svc")

	cfg := TLSConfigFromEnv()
	assert.Equal(t, "/c", cfg.CertFile)
	assert.Equal(t, "/k", cfg.KeyFile)
	assert.Equal(t, "/a", cfg.CAFile)
	assert.True(t, cfg.Insecure)
	assert.Equal(t, "home-os-dex.home-os.svc", cfg.ServerName)
}
